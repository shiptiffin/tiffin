package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/platform"
)

// exactRowsUnder is the row estimate below which tables are counted exactly
// (count(*) on a few thousand rows takes a millisecond); above it the plan
// says "about" and uses Postgres's live-row estimate.
const exactRowsUnder = 250_000

// EstimateLoss says what deleting a project's Postgres (or all its data)
// destroys: its tables and rows, and the size of its databases (branches
// included).
func (*Module) EstimateLoss(ctx context.Context, p *platform.Platform, project string, op change.Op) (*change.Loss, error) {
	// Deleting the service, or Delete all data (and its restore, which
	// replaces what is there then): what the project's databases hold now.
	if (op.Address != change.KindService+"/postgres" || op.Action != change.Delete) && op.Address != change.EmptyAddress("postgres") {
		return nil, nil
	}
	admin, err := Admin(ctx, "postgres")
	if err != nil {
		return nil, err
	}
	defer admin.Close(ctx)
	dbs, err := projectDatabases(ctx, admin, project)
	if err != nil || len(dbs) == 0 {
		return nil, err
	}
	names := make([]string, len(dbs))
	for i, d := range dbs {
		names[i] = d.Name
	}
	loss := &change.Loss{}
	if err := admin.QueryRow(ctx, `SELECT coalesce(sum(pg_database_size(datname)), 0)::bigint FROM pg_database WHERE datname = ANY($1)`, names).Scan(&loss.Bytes); err != nil {
		return nil, err
	}
	main, err := Admin(ctx, dbs[0].Name)
	if err != nil {
		return nil, err
	}
	defer main.Close(ctx)
	tables, rows, approx, err := measureTables(ctx, main)
	if err != nil {
		return nil, err
	}
	loss.Counts = []change.LossCount{{N: rows, Unit: "row", Approx: approx}, {N: tables, Unit: "table"}}
	if len(dbs) > 1 {
		loss.Counts = append(loss.Counts, change.LossCount{N: int64(len(dbs)), Unit: "database"})
	}
	return loss, nil
}

// measureTables counts the user tables of the connected database and their
// rows: exactly when they are small, from the statistics otherwise.
func measureTables(ctx context.Context, c *pgx.Conn) (tables, rows int64, approx bool, err error) {
	q, err := c.Query(ctx, `SELECT schemaname, relname, greatest(n_live_tup, 0) FROM pg_stat_user_tables
		WHERE schemaname NOT IN ('pg_catalog', 'information_schema', 'cron') ORDER BY 1, 2`)
	if err != nil {
		return 0, 0, false, err
	}
	var idents []string
	for q.Next() {
		var s, t string
		var n int64
		if err := q.Scan(&s, &t, &n); err != nil {
			q.Close()
			return 0, 0, false, err
		}
		idents = append(idents, pgx.Identifier{s, t}.Sanitize())
		rows += n
	}
	q.Close()
	if err := q.Err(); err != nil {
		return 0, 0, false, err
	}
	tables = int64(len(idents))
	if tables == 0 || rows >= exactRowsUnder || tables > 200 {
		return tables, rows, rows > 0, nil
	}
	// Small enough to count for real, under a short timeout; the estimate stands if it runs out.
	parts := make([]string, len(idents))
	for i, id := range idents {
		parts[i] = fmt.Sprintf("(SELECT count(*) FROM %s)", id)
	}
	tx, err := c.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return tables, rows, rows > 0, nil
	}
	defer tx.Rollback(ctx)
	var exact int64
	if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = 120`); err == nil {
		if err := tx.QueryRow(ctx, "SELECT ("+strings.Join(parts, " + ")+")::bigint").Scan(&exact); err == nil {
			return tables, exact, false, nil
		}
	}
	return tables, rows, rows > 0, nil
}
