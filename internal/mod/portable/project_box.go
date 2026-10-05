package portable

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/mod/postgres"
	"github.com/btahir/tiffin/internal/mod/runtime"
	"github.com/btahir/tiffin/internal/mod/storage"
	"github.com/btahir/tiffin/internal/mod/valkey"
	"github.com/btahir/tiffin/internal/platform"
)

// boxBackend is the backend of a real box.
type boxBackend struct{ p *platform.Platform }

func runtimeModule() (*runtime.Module, error) {
	for _, m := range platform.Modules() {
		if rm, ok := m.(*runtime.Module); ok {
			return rm, nil
		}
	}
	return nil, errors.New("the app runtime is not running")
}

func (b boxBackend) databaseInfo(ctx context.Context, project string) (*PostgresInfo, error) {
	db := postgres.Database(project)
	c, err := postgres.Admin(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", db, err)
	}
	defer c.Close(ctx)
	info := &PostgresInfo{Database: db, Role: postgres.Role(project), Extensions: []string{}}
	rows, err := c.Query(ctx, `SELECT extname FROM pg_extension WHERE extname <> 'plpgsql' ORDER BY extname`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		info.Extensions = append(info.Extensions, n)
	}
	rows.Close()
	if err := c.QueryRow(ctx, `SELECT pg_database_size(current_database())`).Scan(&info.SizeBytes); err != nil {
		return nil, err
	}
	admin, err := postgres.Admin(ctx, "postgres")
	if err != nil {
		return nil, err
	}
	defer admin.Close(ctx)
	var hasCron bool
	_ = admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_cron')`).Scan(&hasCron)
	if hasCron {
		rows, err := admin.Query(ctx, `SELECT schedule, command, nodename, nodeport, database, username, active, coalesce(jobname, '')
			FROM cron.job WHERE username = $1 AND database = $2 ORDER BY jobid`, info.Role, db)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var j cronJob
			if err := rows.Scan(&j.Schedule, &j.Command, &j.Nodename, &j.Nodeport, &j.Database, &j.Username, &j.Active, &j.Jobname); err != nil {
				rows.Close()
				return nil, err
			}
			info.Cron = append(info.Cron, j)
		}
		rows.Close()
	}
	return info, nil
}

// dumpDatabase is a plain pg_dump without owners, grants, extensions or
// Tiffin's helper schema: SQL any Postgres (and any role) can load, after
// database-setup.sql.
func (b boxBackend) dumpDatabase(ctx context.Context, project string, w io.Writer) error {
	cmd := pgTool(ctx, "pg_dump", "--format=plain", "--no-sync", "--no-owner", "--no-privileges", "--quote-all-identifiers",
		"--exclude-schema=tiffin", "--exclude-extension=*", "-d", postgres.Database(project))
	var errb tailBuffer
	cmd.Stdout, cmd.Stderr = w, &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_dump: %w: %s", err, errb.String())
	}
	return nil
}

// prepareDatabase makes the role and database (Prepare, as the reconcile
// would), then the extensions the source database had beyond its config's,
// as the box's superuser: the same any config may ask for.
func (b boxBackend) prepareDatabase(ctx context.Context, project string, manifestExt, extra []string) error {
	if err := postgres.Prepare(ctx, b.p, project, manifestExt); err != nil {
		return err
	}
	c, err := postgres.Admin(ctx, postgres.Database(project))
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	for _, e := range extra {
		var available bool
		if err := c.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = $1)`, e).Scan(&available); err != nil {
			return err
		}
		if !available {
			return fmt.Errorf("the database uses extension %s, which this box does not have", e)
		}
		if _, err := c.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS `+qi(e)+` CASCADE`); err != nil {
			return fmt.Errorf("extension %s: %w", e, err)
		}
	}
	return nil
}

// restoreDatabase loads database.sql as the project's own role, never as
// the superuser: an archive can do nothing in Postgres its project could
// not do itself.
func (b boxBackend) restoreDatabase(ctx context.Context, project string, r io.Reader) error {
	env, err := postgres.ConnEnv(ctx, b.p, project, "", true)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, postgres.BinDir+"/psql", "-X", "-q", "-v", "ON_ERROR_STOP=1", "-f", "-")
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "PGAPPNAME=tiffin-import", "LC_ALL=C.UTF-8"}
	for _, k := range []string{"PGHOST", "PGPORT", "PGUSER", "PGPASSWORD", "PGDATABASE"} {
		cmd.Env = append(cmd.Env, k+"="+env[k])
	}
	cmd.WaitDelay = 10 * time.Second
	var errb tailBuffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = r, io.Discard, &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("psql: %w: %s", err, errb.String())
	}
	return nil
}

func (b boxBackend) dropDatabase(ctx context.Context, project string) error {
	return postgres.Unprepare(ctx, b.p, project)
}

// restoreCron schedules the archive's pg_cron jobs for the new project's
// own role and database.
func (b boxBackend) restoreCron(ctx context.Context, project string, jobs []cronJob) error {
	c, err := postgres.Admin(ctx, "postgres")
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	for i := range jobs {
		jobs[i].Username, jobs[i].Database = postgres.Role(project), postgres.Database(project)
	}
	return restoreCron(ctx, c, jobs, false)
}

// dumpKeys reads every key under the project's prefix (SCAN, then PTTL and
// DUMP each).
func (b boxBackend) dumpKeys(ctx context.Context, project string, each func(cacheEntry) error) error {
	c, err := valkey.Admin(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	prefix := valkey.Prefix(project)
	cursor := "0"
	for {
		v, err := c.Do(ctx, "SCAN", cursor, "MATCH", prefix+"*", "COUNT", "1000")
		if err != nil {
			return err
		}
		arr, _ := v.([]any)
		if len(arr) != 2 {
			return errors.New("valkey: unexpected SCAN reply")
		}
		cursor, _ = arr[0].(string)
		keys, _ := arr[1].([]any)
		for _, k := range keys {
			key, _ := k.(string)
			ttl, err := c.Int(ctx, "PTTL", key)
			if err != nil || ttl == -2 {
				continue // gone meanwhile
			}
			dump, err := c.String(ctx, "DUMP", key)
			if errors.Is(err, valkey.ErrNil) {
				continue
			} else if err != nil {
				return err
			}
			if err := each(cacheEntry{Key: strings.TrimPrefix(key, prefix), TTLMs: max(ttl, 0), Dump: []byte(dump)}); err != nil {
				return err
			}
		}
		if cursor == "0" {
			return nil
		}
	}
}

// restoreKeys RESTOREs keys under the project's prefix, over one connection.
func (b boxBackend) restoreKeys(ctx context.Context, project string, keys func(put func(cacheEntry) error) error) error {
	c, err := valkey.Admin(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	prefix := valkey.Prefix(project)
	return keys(func(e cacheEntry) error {
		_, err := c.Do(ctx, "RESTORE", prefix+e.Key, strconv.FormatInt(e.TTLMs, 10), string(e.Dump), "REPLACE")
		return err
	})
}

func (b boxBackend) liveRelease(ctx context.Context, project, app string) (*runtime.Deploy, error) {
	rm, err := runtimeModule()
	if err != nil {
		return nil, err
	}
	return rm.LiveRelease(ctx, project, app)
}

func (b boxBackend) saveImage(ctx context.Context, ref string, w io.Writer) error {
	return saveImages(ctx, []string{ref}, func(r io.Reader) error { _, err := io.Copy(w, r); return err })
}

func (b boxBackend) release(ctx context.Context, project, app string, src runtime.ReleaseSource, by string) (*runtime.Deploy, error) {
	rm, err := runtimeModule()
	if err != nil {
		return nil, err
	}
	return rm.Release(ctx, project, app, src, by)
}

func (b boxBackend) gitDir(project string) string {
	rm, err := runtimeModule()
	if err != nil {
		return ""
	}
	d, _ := rm.GitDir(project)
	return d
}

func (b boxBackend) bucketDir(project, bucket string) string {
	return storage.BucketDir(b.p, project, bucket)
}

func (b boxBackend) recentlyDeleted(ctx context.Context, project string) bool {
	return postgres.RecentlyDeleted(ctx, b.p, project) || storage.InTrash(ctx, b.p, project)
}

// converged waits until every resource of project has converged (ready or
// failed), and says which failed.
func (b boxBackend) converged(ctx context.Context, project string) error {
	deadline := time.Now().Add(10 * time.Minute)
	for {
		_, res, err := b.p.DB.Load(ctx, project)
		if err != nil {
			return err
		}
		st, err := b.p.DB.ResourceStatuses(ctx, project)
		if err != nil {
			return err
		}
		pending := 0
		var failed []string
		for addr := range res {
			s, ok := st[addr]
			switch {
			case change.Unmanaged(addr) && addr != change.KindStopped:
			case !ok || s.State == platform.StatePending:
				pending++
			case s.State == platform.StateFailed:
				msg, _, _ := strings.Cut(s.Message, "\n")
				failed = append(failed, addr+": "+msg)
			}
		}
		if pending == 0 {
			if len(failed) > 0 {
				return fmt.Errorf("the new project's %s did not converge", strings.Join(failed, "; "))
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%d resource(s) of %s still converging after 10 minutes", pending, project)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
