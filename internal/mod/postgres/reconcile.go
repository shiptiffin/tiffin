package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/mod/budget"
	"github.com/btahir/tiffin/internal/mod/datakit"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/jackc/pgx/v5"
)

// Kinds implements platform.Reconciler.
func (*Module) Kinds() []string { return []string{"service/postgres"} }

// Reconcile makes the cluster match a project's postgres service.
func (*Module) Reconcile(ctx context.Context, p *platform.Platform, project, address string, spec json.RawMessage) error {
	mu.Lock()
	defer mu.Unlock()
	if spec == nil {
		return remove(ctx, p, project)
	}
	var s manifest.Postgres
	if err := json.Unmarshal(spec, &s); err != nil {
		return fmt.Errorf("postgres spec: %w", err)
	}
	return ensure(ctx, p, project, s)
}

// extAliases maps friendly names to Postgres extension names.
var extAliases = map[string]string{"pgvector": "vector", "cron": "pg_cron", "trgm": "pg_trgm"}

func extName(e string) string {
	if a, ok := extAliases[e]; ok {
		return a
	}
	return strings.ReplaceAll(e, "-", "_")
}

// deletedRecord remembers the snapshot taken when a project's postgres
// service was deleted, so re-adding it (an undo) brings the data back.
type deletedRecord struct {
	Snapshot string    `json:"snapshot"`
	At       time.Time `json:"at"`
	// Failed: restoring it failed; deleting the service again drops the
	// record, so adding it back starts with an empty database.
	Failed bool `json:"failed,omitempty"`
}

func ensure(ctx context.Context, p *platform.Platform, project string, s manifest.Postgres) error {
	pw, err := datakit.EnsureSecret(ctx, p, nsPassword, project)
	if err != nil {
		return err
	}
	role, db := Role(project), Database(project)
	admin, err := Admin(ctx, "postgres")
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer admin.Close(ctx)

	var exists bool
	if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).Scan(&exists); err != nil {
		return err
	}
	verb := "ALTER"
	if !exists {
		verb = "CREATE"
	}
	limits := roleLimits(budget.SharedLimit(project).Percent, s.StatementTimeoutSeconds, dataDiskBytes())
	if _, err := admin.Exec(ctx, fmt.Sprintf(`%s ROLE %s WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS CONNECTION LIMIT %d PASSWORD %s`,
		verb, quoteIdent(role), limits.Connections, quoteLiteral(pw))); err != nil {
		return fmt.Errorf("%s role: %w", strings.ToLower(verb), err)
	}
	if err := applyRoleLimits(ctx, admin, role, limits); err != nil {
		return err
	}
	noteApplied(project, s.StatementTimeoutSeconds, limits)

	var comment *string
	err = admin.QueryRow(ctx, `SELECT shobj_description(oid, 'pg_database') FROM pg_database WHERE datname = $1`, db).Scan(&comment)
	exists = err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if m, ok := parseMeta(comment); exists && ok && m.Project != project {
		return fmt.Errorf("database %s already belongs to project %q (branch %q); rename one of the projects", db, m.Project, m.Branch)
	}
	if !exists {
		if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s OWNER %s`, quoteIdent(db), quoteIdent(role))); err != nil {
			return fmt.Errorf("create database: %w", err)
		}
		if err := setMeta(ctx, admin, db, dbMeta{Tiffin: "main", Project: project, CreatedAt: time.Now().UTC()}); err != nil {
			return err
		}
		if err := restoreAfterUndo(ctx, p, project); err != nil {
			// Never ready with an empty database instead of the data: drop
			// it, so the next reconcile creates it and restores again.
			if _, derr := admin.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, quoteIdent(db))); derr != nil {
				p.Log.Error("postgres: drop the database a failed restore left empty", "project", project, "err", derr)
			}
			return fmt.Errorf("bring back the data from when Postgres was deleted: %w (to start with an empty database instead, "+
				"remove postgres from the project, apply, then add it back)", err)
		}
	}
	if _, err := admin.Exec(ctx, fmt.Sprintf(`REVOKE ALL ON DATABASE %[1]s FROM PUBLIC; GRANT ALL ON DATABASE %[1]s TO %[2]s`, quoteIdent(db), quoteIdent(role))); err != nil {
		return err
	}
	if err := setupDatabase(ctx, db, role); err != nil {
		return err
	}
	return reconcileExtensions(ctx, p, project, admin, s.Extensions)
}

// setupDatabase adds the tiffin helper schema: RLS helpers reading the
// per-request settings an app sets with SET LOCAL app.org_id = '...'.
func setupDatabase(ctx context.Context, db, role string) error {
	c, err := Admin(ctx, db)
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	_, err = c.Exec(ctx, fmt.Sprintf(`
CREATE SCHEMA IF NOT EXISTS tiffin;
COMMENT ON SCHEMA tiffin IS 'Tiffin helpers (managed by the box)';
GRANT USAGE ON SCHEMA tiffin TO %[1]s;
CREATE OR REPLACE FUNCTION tiffin.org_id() RETURNS text LANGUAGE sql STABLE PARALLEL SAFE
  AS $$ SELECT nullif(current_setting('app.org_id', true), '') $$;
COMMENT ON FUNCTION tiffin.org_id() IS 'The current organization for row-level security: SET LOCAL app.org_id = ''org_123''. NULL when unset.';
CREATE OR REPLACE FUNCTION tiffin.user_id() RETURNS text LANGUAGE sql STABLE PARALLEL SAFE
  AS $$ SELECT nullif(current_setting('app.user_id', true), '') $$;
COMMENT ON FUNCTION tiffin.user_id() IS 'The current user for row-level security: SET LOCAL app.user_id = ''usr_123''. NULL when unset.';
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA tiffin TO %[1]s;
`, quoteIdent(role)))
	return err
}

func reconcileExtensions(ctx context.Context, p *platform.Platform, project string, admin *pgx.Conn, wanted []string) error {
	role, db := Role(project), Database(project)
	want := make([]string, 0, len(wanted))
	for _, e := range wanted {
		want = append(want, extName(e))
	}
	sort.Strings(want)
	want = slices.Compact(want)

	var available []string
	rows, err := admin.Query(ctx, `SELECT name FROM pg_available_extensions ORDER BY name`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return err
		}
		available = append(available, n)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var unknown []string
	for _, e := range want {
		if !slices.Contains(available, e) {
			unknown = append(unknown, e)
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("unknown Postgres extension(s) %s; available: %s", strings.Join(unknown, ", "), strings.Join(available, ", "))
	}

	c, err := Admin(ctx, db)
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	for _, e := range want {
		if e == "pg_cron" {
			// pg_cron runs in the "postgres" database; projects schedule jobs
			// into their own database with cron.schedule_in_database and only
			// see their own jobs (cron.job has row-level security).
			if _, err := admin.Exec(ctx, fmt.Sprintf(`GRANT CONNECT ON DATABASE postgres TO %[1]s; GRANT USAGE ON SCHEMA cron TO %[1]s;`+cronFuncs("GRANT", "TO"), quoteIdent(role))); err != nil {
				return fmt.Errorf("enable pg_cron: %w", err)
			}
			continue
		}
		if _, err := c.Exec(ctx, fmt.Sprintf(`CREATE EXTENSION IF NOT EXISTS %s CASCADE`, quoteIdent(e))); err != nil {
			return fmt.Errorf("enable extension %s: %w", e, err)
		}
	}
	// Drop only what Tiffin enabled and the spec no longer lists (the plan
	// marked that irreversible). Extensions created by hand are left alone.
	var drop []string
	for _, e := range trackedExtensions(ctx, p, project) {
		if !slices.Contains(want, e) {
			drop = append(drop, e)
		}
	}
	if len(drop) > 0 {
		if _, err := takeSnapshot(ctx, project, db, "", "before dropping extension(s) "+strings.Join(drop, ", ")); err != nil {
			return fmt.Errorf("snapshot before dropping extensions: %w", err)
		}
		for _, e := range drop {
			if e == "pg_cron" {
				if _, err := admin.Exec(ctx, fmt.Sprintf(`DELETE FROM cron.job WHERE username = %[2]s; REVOKE USAGE ON SCHEMA cron FROM %[1]s; REVOKE CONNECT ON DATABASE postgres FROM %[1]s;`+cronFuncs("REVOKE", "FROM"),
					quoteIdent(role), quoteLiteral(role))); err != nil {
					return fmt.Errorf("disable pg_cron: %w", err)
				}
				continue
			}
			if _, err := c.Exec(ctx, fmt.Sprintf(`DROP EXTENSION IF EXISTS %s CASCADE`, quoteIdent(e))); err != nil {
				return fmt.Errorf("drop extension %s: %w", e, err)
			}
		}
	}
	raw, _ := json.Marshal(want)
	return p.DB.KVPut(ctx, nsExtensions, project, raw)
}

// remove deletes a project's databases and role, after snapshotting every
// database so the delete can be undone for SnapshotKeep.
func remove(ctx context.Context, p *platform.Platform, project string) error {
	role := Role(project)
	admin, err := Admin(ctx, "postgres")
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer admin.Close(ctx)
	dbs, err := projectDatabases(ctx, admin, project)
	if err != nil {
		return err
	}
	if len(dbs) == 0 {
		// No database because restoring the delete snapshot failed: deleting
		// the service now is the owner choosing to start empty next time.
		if raw, ok, _ := p.DB.KVGet(ctx, nsDeleted, project); ok {
			var rec deletedRecord
			if json.Unmarshal(raw, &rec) != nil || rec.Failed {
				_ = p.DB.KVDelete(ctx, nsDeleted, project)
			}
		}
	}
	for _, d := range dbs {
		snap, err := takeSnapshot(ctx, project, d.Name, d.Branch, "service deleted")
		if err != nil {
			return fmt.Errorf("snapshot %s before deleting it (nothing was deleted): %w", d.Name, err)
		}
		if d.Branch == "" {
			raw, _ := json.Marshal(deletedRecord{Snapshot: snap.ID, At: snap.At})
			if err := p.DB.KVPut(ctx, nsDeleted, project, raw); err != nil {
				return err
			}
		}
	}
	for _, d := range dbs {
		if _, err := admin.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, quoteIdent(d.Name))); err != nil {
			return fmt.Errorf("drop %s: %w", d.Name, err)
		}
	}
	var exists bool
	if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).Scan(&exists); err != nil {
		return err
	}
	if exists {
		if _, err := admin.Exec(ctx, `DELETE FROM cron.job WHERE username = $1`, role); err != nil && !isUndefinedTable(err) {
			return err
		}
		if _, err := admin.Exec(ctx, fmt.Sprintf(`DROP OWNED BY %[1]s; DROP ROLE %[1]s`, quoteIdent(role))); err != nil {
			return fmt.Errorf("drop role: %w", err)
		}
	}
	forgetApplied(project)
	_ = p.DB.KVDelete(ctx, nsExtensions, project)
	return p.DB.KVDelete(ctx, nsPassword, project)
}

// restoreAfterUndo restores the snapshot taken when the service was deleted,
// if that happened within SnapshotKeep: re-adding postgres (an undo) brings
// the data back. The record is kept until the restore succeeds; a missing
// snapshot or a failed restore is an error (and marks the record Failed).
func restoreAfterUndo(ctx context.Context, p *platform.Platform, project string) error {
	raw, ok, err := p.DB.KVGet(ctx, nsDeleted, project)
	if err != nil || !ok {
		return err
	}
	var rec deletedRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return fmt.Errorf("unreadable record of the snapshot taken at the delete: %w", err)
	}
	if time.Since(rec.At) > SnapshotKeep {
		// Past the undo window, its snapshot is pruned: re-adding starts empty.
		return p.DB.KVDelete(ctx, nsDeleted, project)
	}
	snap, err := getSnapshot(project, rec.Snapshot)
	if errors.Is(err, os.ErrNotExist) {
		err = fmt.Errorf("snapshot %s is missing", rec.Snapshot)
	}
	if err == nil {
		p.Log.Info("postgres: restoring the data from when the service was deleted", "project", project, "snapshot", snap.ID)
		err = pgRestore(ctx, snap.Path, Database(project))
	}
	if err != nil {
		rec.Failed = true
		raw, _ := json.Marshal(rec)
		_ = p.DB.KVPut(ctx, nsDeleted, project, raw)
		return err
	}
	return p.DB.KVDelete(ctx, nsDeleted, project)
}

// dbMeta is stored as the database's COMMENT, so it travels with the
// cluster through backups and restores.
type dbMeta struct {
	Tiffin    string    `json:"tiffin"` // "main" or "branch"
	Project   string    `json:"project"`
	Branch    string    `json:"branch,omitempty"`
	From      string    `json:"from,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

func parseMeta(comment *string) (dbMeta, bool) {
	var m dbMeta
	if comment == nil || json.Unmarshal([]byte(*comment), &m) != nil || m.Tiffin == "" {
		return m, false
	}
	return m, true
}

func setMeta(ctx context.Context, admin *pgx.Conn, db string, m dbMeta) error {
	raw, _ := json.Marshal(m)
	_, err := admin.Exec(ctx, fmt.Sprintf(`COMMENT ON DATABASE %s IS %s`, quoteIdent(db), quoteLiteral(string(raw))))
	return err
}

// projectDB is one database of a project: the main one (Branch "") or a branch.
type projectDB struct {
	Name   string
	Branch string
}

// projectDatabases lists the main database and branch databases of a
// project, main first. Branch databases are recognised by their comment,
// so a project slug that happens to look like "<other>__<branch>" never
// claims another project's branches.
func projectDatabases(ctx context.Context, c *pgx.Conn, project string) ([]projectDB, error) {
	main := Database(project)
	rows, err := c.Query(ctx, `SELECT datname, shobj_description(oid, 'pg_database') FROM pg_database
		WHERE datname = $1 OR left(datname, $2) = $3 ORDER BY datname = $1 DESC, datname`, main, len(main)+2, main+"__")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []projectDB
	for rows.Next() {
		var name string
		var comment *string
		if err := rows.Scan(&name, &comment); err != nil {
			return nil, err
		}
		m, ok := parseMeta(comment)
		switch {
		case name == main && (!ok || m.Project == project):
			out = append(out, projectDB{Name: name})
		case ok && m.Tiffin == "branch" && m.Project == project:
			out = append(out, projectDB{Name: name, Branch: m.Branch})
		}
	}
	return out, rows.Err()
}

func hasDB(list []projectDB, name string) bool {
	for _, d := range list {
		if d.Name == name {
			return true
		}
	}
	return false
}

// cronFuncs grants (or revokes) the pg_cron functions a project role needs
// beyond schedule/unschedule: scheduling into its own database and altering
// its own jobs. pg_cron itself refuses databases the role cannot CONNECT
// to and changing a job's user, so a project cannot reach another's data.
// The role is %[1]s in the surrounding Sprintf.
func cronFuncs(verb, prep string) string {
	return `DO $cron$ DECLARE f regprocedure; BEGIN
  FOR f IN SELECT p.oid::regprocedure FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
           WHERE n.nspname = 'cron' AND p.proname IN ('schedule_in_database', 'alter_job') LOOP
    EXECUTE format('` + verb + ` EXECUTE ON FUNCTION %%s ` + prep + ` %[1]s', f);
  END LOOP; END $cron$;`
}

func isUndefinedTable(err error) bool { return err != nil && strings.Contains(err.Error(), "42P01") }
