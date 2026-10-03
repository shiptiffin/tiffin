package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/btahir/tiffin/internal/platform"
)

// TableColumn is one column of a table.
type PGTableColumn struct {
	Name     string  `json:"name"`
	Type     string  `json:"type" doc:"SQL type, e.g. \"text\", \"vector(1536)\", \"timestamp with time zone\""`
	Nullable bool    `json:"nullable"`
	Default  *string `json:"default,omitempty"`
	Primary  bool    `json:"primary,omitempty" doc:"Part of the primary key"`
}

// Table is one table, view or materialized view.
type PGTable struct {
	Schema      string          `json:"schema"`
	Name        string          `json:"name"`
	Kind        string          `json:"kind" enum:"table,partitioned,view,materialized-view,foreign" doc:"What it is"`
	RowEstimate *int64          `json:"rowEstimate" doc:"Planner estimate (exact for small tables never analyzed); null when unknown"`
	SizeBytes   int64           `json:"sizeBytes" doc:"Table plus indexes and TOAST"`
	RLS         bool            `json:"rls" doc:"Row-level security is enabled"`
	Columns     []PGTableColumn `json:"columns"`
}

// Tables lists the user tables of a project's database (or a branch) with
// columns and size estimates, as the project's role sees them.
func Tables(ctx context.Context, p *platform.Platform, project, branch string) ([]PGTable, error) {
	db, err := targetDatabase(ctx, p, project, branch)
	if err != nil {
		return nil, err
	}
	conn, err := roleConn(ctx, p, project, db, 15*time.Second, true)
	if err != nil {
		return nil, err
	}
	defer conn.Close(context.Background())
	rows, err := conn.Query(ctx, `
SELECT c.oid, n.nspname, c.relname, c.relkind::text, c.reltuples::bigint, pg_total_relation_size(c.oid), c.relrowsecurity
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('r','p','v','m','f')
  AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'tiffin', 'cron')
  AND n.nspname NOT LIKE 'pg\_%'
  AND NOT c.relispartition
  AND has_schema_privilege(n.oid, 'USAGE')
ORDER BY n.nspname = 'public' DESC, n.nspname, c.relname
LIMIT 1000`)
	if err != nil {
		return nil, sqlError(err)
	}
	type key = uint32
	byOID := map[key]*PGTable{}
	var order []key
	for rows.Next() {
		var oid key
		var t PGTable
		var kind string
		var tuples int64
		if err := rows.Scan(&oid, &t.Schema, &t.Name, &kind, &tuples, &t.SizeBytes, &t.RLS); err != nil {
			rows.Close()
			return nil, err
		}
		t.Kind = map[string]string{"r": "table", "p": "partitioned", "v": "view", "m": "materialized-view", "f": "foreign"}[kind]
		if tuples >= 0 {
			t.RowEstimate = &tuples
		}
		t.Columns = []PGTableColumn{}
		byOID[oid] = &t
		order = append(order, oid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	cols, err := conn.Query(ctx, `
SELECT a.attrelid, a.attname, format_type(a.atttypid, a.atttypmod), NOT a.attnotnull, pg_get_expr(d.adbin, d.adrelid),
       EXISTS (SELECT 1 FROM pg_index i WHERE i.indrelid = a.attrelid AND i.indisprimary AND a.attnum = ANY(i.indkey))
FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE a.attrelid = ANY($1) AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attrelid, a.attnum`, order)
	if err != nil {
		return nil, sqlError(err)
	}
	for cols.Next() {
		var oid key
		var c PGTableColumn
		if err := cols.Scan(&oid, &c.Name, &c.Type, &c.Nullable, &c.Default, &c.Primary); err != nil {
			cols.Close()
			return nil, err
		}
		if t := byOID[oid]; t != nil {
			t.Columns = append(t.Columns, c)
		}
	}
	cols.Close()
	if err := cols.Err(); err != nil {
		return nil, err
	}
	out := make([]PGTable, 0, len(order))
	for _, oid := range order {
		t := byOID[oid]
		// Never analyzed but small: count exactly so the browser isn't blank.
		if t.RowEstimate == nil && (t.Kind == "table" || t.Kind == "partitioned") && t.SizeBytes < 8<<20 {
			var n int64
			if conn.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s.%s`, quoteIdent(t.Schema), quoteIdent(t.Name))).Scan(&n) == nil {
				t.RowEstimate = &n
			}
		}
		out = append(out, *t)
	}
	return out, nil
}

// Info is a project's database at a glance.
type PGInfo struct {
	Database    string   `json:"database"`
	Role        string   `json:"role"`
	Version     string   `json:"version" doc:"Postgres server version"`
	SizeBytes   int64    `json:"sizeBytes"`
	Connections int      `json:"connections" doc:"Open connections to the main database"`
	Extensions  []string `json:"extensions" doc:"Extensions installed in the database (name@version)"`
	Branches    int      `json:"branches"`
	Snapshots   int      `json:"snapshots"`
	Host        string   `json:"host" doc:"TCP address apps use (inside the box)"`
	SocketDir   string   `json:"socketDir" doc:"Unix socket directory (bind-mountable into containers)"`
}

// GetInfo returns database size, connections, extensions and branch counts.
func GetInfo(ctx context.Context, p *platform.Platform, project string) (*PGInfo, error) {
	db, err := targetDatabase(ctx, p, project, "")
	if err != nil {
		return nil, err
	}
	admin, err := Admin(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	defer admin.Close(ctx)
	in := &PGInfo{Database: db, Role: Role(project), Host: fmt.Sprintf("127.0.0.1:%d", Port), SocketDir: SocketDir, Extensions: []string{}}
	if err := admin.QueryRow(ctx, `SELECT current_setting('server_version'), pg_database_size(current_database()),
		(SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND pid <> pg_backend_pid())`).Scan(&in.Version, &in.SizeBytes, &in.Connections); err != nil {
		return nil, err
	}
	rows, err := admin.Query(ctx, `SELECT extname || '@' || extversion FROM pg_extension WHERE extname <> 'plpgsql' ORDER BY extname`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			rows.Close()
			return nil, err
		}
		in.Extensions = append(in.Extensions, e)
	}
	rows.Close()
	for _, e := range trackedExtensions(ctx, p, project) {
		if e == "pg_cron" {
			in.Extensions = append(in.Extensions, "pg_cron (box-wide, schedule with cron.schedule_in_database)")
		}
	}
	if b, err := ListBranches(ctx, p, project); err == nil {
		in.Branches = len(b)
	}
	if s, err := ListSnapshots(project); err == nil {
		in.Snapshots = len(s)
	}
	return in, nil
}
