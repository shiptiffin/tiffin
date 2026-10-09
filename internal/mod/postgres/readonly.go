package postgres

import (
	"context"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/platform"
)

// Read-only holds. When the box's disk guard or a project's storage limit
// stops a project's writes, every database of the project (branches too)
// defaults to read-only transactions, and its sessions are closed so apps
// reconnect into that. Lifting the hold reverses both.
//
// That default is the app's to override (SET default_transaction_read_only
// = off; it owns the databases, so no privilege can stop it writing), so it
// only holds apps that play along. The guard measures the databases every
// round (EnforceHold): one that grew past holdSlack since the hold began
// is being written anyway, and its role is locked out (NOLOGIN, sessions
// closed) until the hold lifts: reads stop too, but the disk is safe.

var holds = struct {
	sync.Mutex
	why    map[string]string
	base   map[string]int64 // database bytes when the hold began
	locked map[string]bool  // the role may not log in
}{why: map[string]string{}, base: map[string]int64{}, locked: map[string]bool{}}

// holdSlack is how much a held project's databases may grow before its role
// is locked out: read-only sessions still write temporary tables and
// statistics.
const holdSlack = 64 << 20

// EnforceHold is called by the disk guard each round with a held project's
// database bytes (branches included). The first call records them; a later
// one past holdSlack locks the project's role out.
func EnforceHold(ctx context.Context, project string, dbBytes int64) error {
	holds.Lock()
	if holds.why[project] == "" || holds.locked[project] {
		holds.Unlock()
		return nil
	}
	base, ok := holds.base[project]
	if !ok || dbBytes < base {
		holds.base[project] = dbBytes
	}
	if !ok || dbBytes <= base+holdSlack {
		holds.Unlock()
		return nil
	}
	holds.locked[project] = true
	holds.Unlock()
	mu.Lock() // a reconcile running now must not log it back in
	defer mu.Unlock()
	admin, err := dbAdmin(ctx, "postgres")
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer admin.Close(ctx)
	return setLogin(ctx, admin, project, false)
}

// lockedOut reports whether a project's role is locked out by a hold.
func lockedOut(project string) bool {
	holds.Lock()
	defer holds.Unlock()
	return holds.locked[project]
}

// setLogin lets a project's role log in again, or locks it out and closes
// its sessions (the pooler's too: it can no longer log them back in).
func setLogin(ctx context.Context, admin *pgx.Conn, project string, on bool) error {
	role := Role(project)
	var exists bool
	if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).Scan(&exists); err != nil || !exists {
		return err
	}
	if on {
		_, err := admin.Exec(ctx, fmt.Sprintf(`ALTER ROLE %s LOGIN`, quoteIdent(role)))
		return err
	}
	if _, err := admin.Exec(ctx, fmt.Sprintf(`ALTER ROLE %s NOLOGIN`, quoteIdent(role))); err != nil {
		return err
	}
	_, err := admin.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = $1 AND pid <> pg_backend_pid()`, role)
	return err
}

// ReadOnly returns why a project's database writes are held, or "".
func ReadOnly(project string) string {
	holds.Lock()
	defer holds.Unlock()
	return holds.why[project]
}

// SetReadOnly holds a project's database writes (why says which limit and
// how to fix it) or, with why "", lifts the hold.
func SetReadOnly(ctx context.Context, p *platform.Platform, project, why string) error {
	holds.Lock()
	locked := holds.locked[project]
	if why == "" {
		delete(holds.why, project)
		delete(holds.base, project)
		delete(holds.locked, project)
	} else {
		holds.why[project] = why
	}
	holds.Unlock()
	if ok, err := HasService(ctx, p, project); err != nil || !ok {
		return err
	}
	admin, err := Admin(ctx, "postgres")
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer admin.Close(ctx)
	if err := setReadOnly(ctx, admin, project, why != ""); err != nil {
		return err
	}
	if why == "" && locked {
		mu.Lock()
		defer mu.Unlock()
		return setLogin(ctx, admin, project, true)
	}
	return nil
}

func setReadOnly(ctx context.Context, admin *pgx.Conn, project string, on bool) error {
	dbs, err := projectDatabases(ctx, admin, project)
	if err != nil {
		return err
	}
	for _, d := range dbs {
		var cur bool
		if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_db_role_setting s JOIN pg_database d ON d.oid = s.setdatabase
			WHERE d.datname = $1 AND s.setrole = 0 AND 'default_transaction_read_only=on' = ANY(s.setconfig))`, d.Name).Scan(&cur); err != nil {
			return err
		}
		if cur == on {
			continue
		}
		stmt := `ALTER DATABASE %s RESET default_transaction_read_only`
		if on {
			stmt = `ALTER DATABASE %s SET default_transaction_read_only = on`
		}
		if _, err := admin.Exec(ctx, fmt.Sprintf(stmt, quoteIdent(d.Name))); err != nil {
			return err
		}
		// Sessions keep the default they started with: close them so the
		// apps' pools reconnect into the new one.
		if _, err := admin.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, d.Name); err != nil {
			return err
		}
	}
	return nil
}

// heldProblem is the 409 for a write while the project is read-only.
func heldProblem(project string) error {
	why := ReadOnly(project)
	if why == "" {
		return nil
	}
	return api.NewProblem(409, "precondition", why)
}

// DatabaseSizes returns the size on disk of each project's databases
// (branches included), in one query.
func DatabaseSizes(ctx context.Context, projects []string) (map[string]int64, error) {
	admin, err := Admin(ctx, "postgres")
	if err != nil {
		return nil, err
	}
	defer admin.Close(ctx)
	return databaseSizes(ctx, admin, projects)
}

func databaseSizes(ctx context.Context, admin *pgx.Conn, projects []string) (map[string]int64, error) {
	known := map[string]bool{}
	for _, pr := range projects {
		known[pr] = true
	}
	dbs, err := listDatabases(ctx, admin, "", true)
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, d := range dbs {
		if known[d.Project] {
			out[d.Project] += d.Size
		}
	}
	return out, nil
}
