package postgres

import (
	"context"
	"fmt"
	"sync"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/jackc/pgx/v5"
)

// Read-only holds. When the box's disk guard or a project's storage limit
// stops a project's writes, every database of the project (branches too)
// defaults to read-only transactions, and its sessions are closed so apps
// reconnect into that. Lifting the hold reverses both.

var holds = struct {
	sync.Mutex
	why map[string]string
}{why: map[string]string{}}

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
	if why == "" {
		delete(holds.why, project)
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
	return setReadOnly(ctx, admin, project, why != "")
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
	mains := map[string]string{}
	for _, pr := range projects {
		mains[Database(pr)] = pr
	}
	rows, err := admin.Query(ctx, `SELECT datname, shobj_description(oid, 'pg_database'), pg_database_size(oid) FROM pg_database WHERE NOT datistemplate`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var name string
		var comment *string
		var size int64
		if err := rows.Scan(&name, &comment, &size); err != nil {
			return nil, err
		}
		m, ok := parseMeta(comment)
		if pr, main := mains[name]; main && (!ok || m.Project == pr) {
			out[pr] += size
		} else if ok && m.Tiffin == "branch" {
			if _, known := mains[Database(m.Project)]; known {
				out[m.Project] += size
			}
		}
	}
	return out, rows.Err()
}
