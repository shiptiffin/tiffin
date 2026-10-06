package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/jackc/pgx/v5"
)

// Branch is a preview copy of a project's database.
type PGBranch struct {
	Name      string    `json:"name" doc:"Branch name"`
	Database  string    `json:"database" doc:"Postgres database name"`
	From      string    `json:"from" doc:"What it was cloned from: \"main\" or another branch"`
	CreatedAt time.Time `json:"createdAt"`
	SizeBytes int64     `json:"sizeBytes" doc:"Logical size. Clones share unchanged blocks with their source on disk (reflinks), so this overstates real disk use."`
	Preview   string    `json:"preview,omitempty" doc:"The app preview this branch was made for: it is deleted with the preview"`
}

// CreatedBranch is a new branch and how long the clone took.
type PGBranchCreated struct {
	PGBranch
	CloneMs   int64 `json:"cloneMs" doc:"Time spent in CREATE DATABASE ... STRATEGY FILE_COPY (the reflink clone)"`
	BlockedMs int64 `json:"blockedMs" doc:"How long new connections to the source database were refused"`
	TotalMs   int64 `json:"totalMs"`
}

// CreateBranch clones a project's database (or another branch, with from)
// into a new branch database with PG18's reflink clone. The source must have
// no open connections while the files are cloned, so connections to it are
// refused and existing ones are closed for that moment (usually well under a
// second); apps reconnect through their pool.
func CreateBranch(ctx context.Context, p *platform.Platform, project, name, from string) (*PGBranchCreated, error) {
	return createBranch(ctx, p, project, name, from, "")
}

// CreatePreviewBranch clones a project's main database for an app preview
// (named preview): the runtime gives the preview's apps its DATABASE_URL
// and deletes it with the preview.
func CreatePreviewBranch(ctx context.Context, p *platform.Platform, project, name, preview string) (*PGBranchCreated, error) {
	return createBranch(ctx, p, project, name, "main", preview)
}

func createBranch(ctx context.Context, p *platform.Platform, project, name, from, preview string) (*PGBranchCreated, error) {
	start := time.Now()
	if !BranchPattern.MatchString(name) || name == "main" {
		return nil, api.NewProblem(422, "validation", "branch names are 1-19 characters: lowercase letters, digits and hyphens, starting with a letter (not \"main\")")
	}
	if err := heldProblem(project); err != nil {
		return nil, err // a branch is a full copy, and it would not be read-only
	}
	if from == "" {
		from = "main"
	}
	src := Database(project)
	if from != "main" {
		if !BranchPattern.MatchString(from) {
			return nil, api.NewProblem(422, "validation", "from must be \"main\" or an existing branch name")
		}
		src = BranchDatabase(project, from)
	}
	dst := BranchDatabase(project, name)
	mu.Lock()
	defer mu.Unlock()
	admin, err := Admin(ctx, "postgres")
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	defer admin.Close(ctx)
	dbs, err := projectDatabases(ctx, admin, project)
	if err != nil {
		return nil, err
	}
	if len(dbs) == 0 {
		return nil, errNoService(project)
	}
	if !hasDB(dbs, src) {
		return nil, api.NewProblem(404, "not_found", fmt.Sprintf("branch %q does not exist in project %s", from, project))
	}
	var taken bool
	if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, dst).Scan(&taken); err != nil {
		return nil, err
	}
	if taken {
		p := api.NewProblem(409, "conflict", fmt.Sprintf("branch %q already exists", name))
		p.Hint = "pick another name, or delete the branch first"
		return nil, p
	}
	// Flush dirty buffers first, so the checkpoint inside CREATE DATABASE
	// (while the source is blocked) has little left to write.
	if _, err := admin.Exec(ctx, `CHECKPOINT`); err != nil {
		return nil, err
	}
	blocked := time.Now()
	if _, err := admin.Exec(ctx, fmt.Sprintf(`ALTER DATABASE %s WITH ALLOW_CONNECTIONS false`, quoteIdent(src))); err != nil {
		return nil, err
	}
	defer func() {
		// Always reopen the source, even if the clone failed or ctx ended,
		// through a fresh connection if this one broke. After a crash here,
		// the box's next start reopens it (reopenBlocked).
		c2, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		reopen := fmt.Sprintf(`ALTER DATABASE %s WITH ALLOW_CONNECTIONS true`, quoteIdent(src))
		if _, err := admin.Exec(c2, reopen); err != nil {
			if c, err := Admin(c2, "postgres"); err == nil {
				_, _ = c.Exec(c2, reopen)
				c.Close(c2)
			}
		}
	}()
	if err := drainSessions(ctx, admin, src, branchDrain); err != nil {
		return nil, err
	}
	clone := time.Now()
	if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s TEMPLATE %s STRATEGY FILE_COPY OWNER %s`,
		quoteIdent(dst), quoteIdent(src), quoteIdent(Role(project)))); err != nil {
		return nil, fmt.Errorf("clone %s: %w", src, err)
	}
	cloneMs := time.Since(clone).Milliseconds()
	if _, err := admin.Exec(ctx, fmt.Sprintf(`ALTER DATABASE %s WITH ALLOW_CONNECTIONS true`, quoteIdent(src))); err != nil {
		return nil, err
	}
	blockedMs := time.Since(blocked).Milliseconds()
	meta := dbMeta{Tiffin: "branch", Project: project, Branch: name, From: from, Preview: preview, CreatedAt: time.Now().UTC()}
	if err := setMeta(ctx, admin, dst, meta); err != nil {
		return nil, err
	}
	if _, err := admin.Exec(ctx, fmt.Sprintf(`REVOKE ALL ON DATABASE %[1]s FROM PUBLIC; GRANT ALL ON DATABASE %[1]s TO %[2]s`, quoteIdent(dst), quoteIdent(Role(project)))); err != nil {
		return nil, err
	}
	var size int64
	_ = admin.QueryRow(ctx, `SELECT pg_database_size($1)`, dst).Scan(&size)
	return &PGBranchCreated{
		PGBranch: PGBranch{Name: name, Database: dst, From: from, CreatedAt: meta.CreatedAt, SizeBytes: size, Preview: preview},
		CloneMs:  cloneMs, BlockedMs: blockedMs, TotalMs: time.Since(start).Milliseconds(),
	}, nil
}

// reopenBlocked lets connections back into every project database that
// refuses them. Only a branch clone blocks one, and only while it runs (it
// holds mu), so any found here was left by a crash or a broken connection.
func reopenBlocked(ctx context.Context, admin *pgx.Conn, log *slog.Logger) error {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	mu.Lock()
	defer mu.Unlock()
	rows, err := admin.Query(cctx, `SELECT datname FROM pg_database WHERE NOT datallowconn AND NOT datistemplate AND datname LIKE 'p\_%'`)
	if err != nil {
		return err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, db := range names {
		if _, err := admin.Exec(cctx, fmt.Sprintf(`ALTER DATABASE %s WITH ALLOW_CONNECTIONS true`, quoteIdent(db))); err != nil {
			return fmt.Errorf("reopen %s: %w", db, err)
		}
		log.Warn("postgres: reopened a database an interrupted branch clone left blocked", "database", db)
	}
	return nil
}

// DeleteBranch drops a branch database (closing its connections).
func DeleteBranch(ctx context.Context, p *platform.Platform, project, name string) error {
	if !BranchPattern.MatchString(name) {
		return api.NewProblem(422, "validation", "invalid branch name")
	}
	mu.Lock()
	defer mu.Unlock()
	admin, err := Admin(ctx, "postgres")
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer admin.Close(ctx)
	dbs, err := projectDatabases(ctx, admin, project)
	if err != nil {
		return err
	}
	dst := BranchDatabase(project, name)
	if !hasDB(dbs, dst) {
		return api.NewProblem(404, "not_found", fmt.Sprintf("branch %q does not exist in project %s", name, project))
	}
	_, err = admin.Exec(ctx, fmt.Sprintf(`DROP DATABASE %s WITH (FORCE)`, quoteIdent(dst)))
	return err
}

// ListBranches returns a project's branches, oldest first.
func ListBranches(ctx context.Context, p *platform.Platform, project string) ([]PGBranch, error) {
	admin, err := Admin(ctx, "postgres")
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	defer admin.Close(ctx)
	main := Database(project)
	rows, err := admin.Query(ctx, `SELECT datname, shobj_description(oid, 'pg_database'), pg_database_size(oid) FROM pg_database
		WHERE left(datname, $1) = $2 ORDER BY oid`, len(main)+2, main+"__")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PGBranch{}
	for rows.Next() {
		var b PGBranch
		var comment *string
		if err := rows.Scan(&b.Database, &comment, &b.SizeBytes); err != nil {
			return nil, err
		}
		m, ok := parseMeta(comment)
		if !ok || m.Tiffin != "branch" || m.Project != project {
			continue
		}
		b.Name, b.From, b.CreatedAt, b.Preview = m.Branch, m.From, m.CreatedAt, m.Preview
		out = append(out, b)
	}
	return out, rows.Err()
}

// targetDatabase resolves "" (main) or a branch name to a database of the
// project, checking it exists.
func targetDatabase(ctx context.Context, p *platform.Platform, project, branch string) (string, error) {
	ok, err := HasService(ctx, p, project)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errNoService(project)
	}
	if branch == "" || branch == "main" {
		return Database(project), nil
	}
	if !BranchPattern.MatchString(branch) {
		return "", api.NewProblem(422, "validation", "invalid branch name")
	}
	db := BranchDatabase(project, branch)
	admin, err := Admin(ctx, "postgres")
	if err != nil {
		return "", err
	}
	defer admin.Close(ctx)
	var comment *string
	err = admin.QueryRow(ctx, `SELECT shobj_description(oid, 'pg_database') FROM pg_database WHERE datname = $1`, db).Scan(&comment)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", api.NewProblem(404, "not_found", fmt.Sprintf("branch %q does not exist in project %s", branch, project))
	}
	if err != nil {
		return "", err
	}
	if m, ok := parseMeta(comment); !ok || m.Project != project {
		return "", api.NewProblem(404, "not_found", fmt.Sprintf("branch %q does not exist in project %s", branch, project))
	}
	return db, nil
}

func errNoService(project string) error {
	p := api.NewProblem(409, "precondition", "project "+project+" has no postgres service (or it is still being set up)")
	p.Hint = "add services.postgres to tiffin.config.ts and apply, then check `tiffin projects get " + project + "` until service/postgres is ready"
	return p
}

// branchDrain is how long a branch waits for queries running on its source
// to finish before ending them.
var branchDrain = 5 * time.Second

// drainSessions empties db (already closed to new connections) for CREATE
// DATABASE ... TEMPLATE: idle sessions end at once, so pools just reconnect;
// running queries get up to wait to finish before they are ended too.
func drainSessions(ctx context.Context, admin *pgx.Conn, db string, wait time.Duration) error {
	const others = `FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`
	deadline := time.Now().Add(wait)
	for {
		cond := ` AND state = 'idle'`
		if !time.Now().Before(deadline) {
			cond = ""
		}
		if _, err := admin.Exec(ctx, `SELECT pg_terminate_backend(pid) `+others+cond, db); err != nil {
			return err
		}
		var left int
		if err := admin.QueryRow(ctx, `SELECT count(*) `+others, db).Scan(&left); err != nil {
			return err
		}
		if left == 0 || cond == "" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
