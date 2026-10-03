package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/btahir/tiffin/internal/mod/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// clusterTable is one user table and its row count.
type clusterTable struct {
	Name     string `json:"name"` // schema.table
	Rows     int64  `json:"rows"` // -1: unknown
	Exact    bool   `json:"exact,omitempty"`
	Unlogged bool   `json:"unlogged,omitempty"`
	Err      string `json:"error,omitempty"`
}

// clusterDB is one database's user tables.
type clusterDB struct {
	Name   string         `json:"name"`
	Tables []clusterTable `json:"tables"`
	Err    string         `json:"error,omitempty"`
}

// clusterCatalog is what a cluster holds. One is saved with every backup
// set (catalog.json, names and estimates only) so a drill knows what the
// restored copy must contain.
type clusterCatalog struct {
	TakenAt   time.Time   `json:"takenAt"`
	Databases []clusterDB `json:"databases"`
}

func (c *clusterCatalog) db(name string) *clusterDB {
	if c == nil {
		return nil
	}
	for i := range c.Databases {
		if c.Databases[i].Name == name {
			return &c.Databases[i]
		}
	}
	return nil
}

// How scanCluster counts rows.
const (
	countNone  = iota // planner estimates only (backup time: cheap)
	countSmall        // exact for small tables, estimates for the rest (live cluster)
	countAll          // exact for every table within a time budget (restored copy)
)

type connectFunc func(ctx context.Context, db string) (*pgx.Conn, error)

// scanCluster lists every connectable, non-template database and its user
// tables (ordinary tables and partitions; partitioned parents would count
// rows twice).
func scanCluster(ctx context.Context, connect connectFunc, mode int) (*clusterCatalog, error) {
	c, err := connect(ctx, "postgres")
	if err != nil {
		return nil, err
	}
	rows, err := c.Query(ctx, `SELECT datname FROM pg_database WHERE datallowconn AND NOT datistemplate ORDER BY datname`)
	var names []string
	if err == nil {
		names, err = pgx.CollectRows(rows, pgx.RowTo[string])
	}
	c.Close(ctx)
	if err != nil {
		return nil, err
	}
	out := &clusterCatalog{TakenAt: time.Now().UTC(), Databases: []clusterDB{}}
	deadline := time.Now().Add(verifyBudget)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out.Databases = append(out.Databases, scanDB(ctx, connect, name, mode, deadline))
	}
	return out, nil
}

func scanDB(ctx context.Context, connect connectFunc, name string, mode int, deadline time.Time) clusterDB {
	db := clusterDB{Name: name, Tables: []clusterTable{}}
	c, err := connect(ctx, name)
	if err != nil {
		db.Err = "cannot connect: " + err.Error()
		return db
	}
	defer c.Close(context.WithoutCancel(ctx))
	type rel struct {
		schema, name string
		est, size    int64
		unlogged     bool
	}
	rows, err := c.Query(ctx, `SELECT n.nspname, c.relname, c.reltuples::bigint, pg_relation_size(c.oid), c.relpersistence = 'u'
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind = 'r' AND n.nspname NOT IN ('pg_catalog', 'information_schema')
		  AND n.nspname NOT LIKE 'pg\_toast%' AND n.nspname NOT LIKE 'pg\_temp\_%'
		ORDER BY 1, 2`)
	var rels []rel
	if err == nil {
		for rows.Next() {
			var r rel
			if err = rows.Scan(&r.schema, &r.name, &r.est, &r.size, &r.unlogged); err != nil {
				break
			}
			rels = append(rels, r)
		}
		rows.Close()
		if err == nil {
			err = rows.Err()
		}
	}
	if err != nil {
		db.Err = "listing tables: " + err.Error()
		return db
	}
	for _, r := range rels {
		t := clusterTable{Name: r.schema + "." + r.name, Rows: r.est, Unlogged: r.unlogged}
		if t.Rows < 0 {
			t.Rows = -1 // never analysed
		}
		exact := false
		timeout := countTimeout
		switch mode {
		case countAll:
			exact = time.Now().Before(deadline)
		case countSmall:
			// Small tables, and tables never analysed (no estimate), are
			// counted; a count slower than the timeout keeps the estimate.
			exact, timeout = r.size < liveExactBelow || r.est < 0, liveCountTimeout
		}
		if exact {
			n, err := countRows(ctx, c, r.schema, r.name, timeout)
			switch {
			case err == nil:
				t.Rows, t.Exact = n, true
			case isTimeout(err) || (mode == countSmall && ctx.Err() == nil):
				// Too slow (or, on the live cluster, not readable now): keep the estimate.
			case ctx.Err() != nil:
				t.Err = ctx.Err().Error()
			default:
				t.Err = err.Error()
			}
		}
		db.Tables = append(db.Tables, t)
	}
	return db
}

func countRows(ctx context.Context, c *pgx.Conn, schema, name string, timeout time.Duration) (int64, error) {
	tx, err := c.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", timeout.Milliseconds())); err != nil {
		return 0, err
	}
	var n int64
	err = tx.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{schema, name}.Sanitize()).Scan(&n)
	return n, err
}

func isTimeout(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "57014"
}

// ---- the catalog saved with a backup set ----

const catalogFile = "catalog.json"

// recordCatalog saves the live cluster's table list with a backup set,
// right after pgBackRest finished (the restore ends at that point).
// It is best effort: a backup never fails because of it.
func recordCatalog(ctx context.Context, b *Backup) {
	cat, err := scanCluster(ctx, postgres.Admin, countNone)
	if err != nil {
		return
	}
	raw, _ := json.Marshal(cat)
	_ = os.WriteFile(filepath.Join(b.dir(), catalogFile), raw, 0o600)
}

func loadCatalog(b *Backup) *clusterCatalog {
	raw, err := os.ReadFile(filepath.Join(b.dir(), catalogFile))
	if err != nil {
		return nil
	}
	var c clusterCatalog
	if json.Unmarshal(raw, &c) != nil || len(c.Databases) == 0 {
		return nil
	}
	return &c
}

// verify counts the restored copy and compares it with what the backup
// should hold. It returns the per-database results and what they were
// compared with ("backup" or "live").
func verify(ctx context.Context, b *Backup, s *scratchServer) ([]BackupDrillDatabase, string, error) {
	restored, err := scanCluster(ctx, s.connect, countAll)
	if err != nil {
		return []BackupDrillDatabase{}, "", fmt.Errorf("reading the restored copy: %w", err)
	}
	live, lerr := scanCluster(ctx, postgres.Admin, countSmall)
	if lerr != nil {
		live = nil // the live cluster may be stopped by a restore; the drill does not depend on it
	}
	expected, from := loadCatalog(b), "backup"
	if expected == nil {
		expected, from = live, "live"
	}
	return compare(expected, from == "backup", restored, live), from, nil
}

// compare checks the restored catalog against the expected one. strict:
// expected is the list recorded with the backup, so anything it has that
// the restored copy lacks is missing. Otherwise expected is the live
// cluster now, which may have tables and databases created after the
// backup; they are listed but do not fail the drill.
func compare(expected *clusterCatalog, strict bool, restored, live *clusterCatalog) []BackupDrillDatabase {
	names := []string{}
	for _, c := range []*clusterCatalog{expected, restored} {
		if c == nil {
			continue
		}
		for _, d := range c.Databases {
			if !slices.Contains(names, d.Name) {
				names = append(names, d.Name)
			}
		}
	}
	sort.Strings(names)
	out := make([]BackupDrillDatabase, 0, len(names))
	for _, name := range names {
		exp, got, lv := expected.db(name), restored.db(name), live.db(name)
		r := BackupDrillDatabase{Name: name, Missing: []string{}, LiveRows: 0}
		if lv != nil {
			r.LiveTables = len(lv.Tables)
			for _, t := range lv.Tables {
				if t.Rows > 0 {
					r.LiveRows += t.Rows
				}
			}
		}
		if got == nil {
			if strict {
				r.Problems = append(r.Problems, "the database is not in the restored copy")
			} else {
				r.OK = true
				r.Problems = append(r.Problems, "not in the backup (probably created after it; this backup has no table list to tell)")
			}
			out = append(out, r)
			continue
		}
		if got.Err != "" {
			r.Problems = append(r.Problems, got.Err)
		}
		have := map[string]bool{}
		for _, t := range got.Tables {
			have[t.Name] = true
			r.Tables++
			if t.Rows > 0 {
				r.Rows += t.Rows
			}
			if t.Err != "" {
				r.Problems = append(r.Problems, t.Name+" cannot be read: "+t.Err)
			}
			c := BackupDrillTable{Table: t.Name, Rows: t.Rows, Exact: t.Exact, LiveRows: -1, Error: t.Err}
			if lv != nil {
				for _, l := range lv.Tables {
					if l.Name == t.Name {
						c.LiveRows = l.Rows
					}
				}
			}
			r.Counts = append(r.Counts, c)
		}
		var notIn []string
		if exp != nil {
			for _, t := range exp.Tables {
				if !have[t.Name] {
					notIn = append(notIn, t.Name)
				}
			}
		}
		if strict {
			r.Missing = append(r.Missing, notIn...)
		} else if len(notIn) > 0 {
			r.Problems = append(r.Problems, fmt.Sprintf("%d live tables are not in the backup (probably created after it): %v", len(notIn), clip(notIn, 5)))
		}
		r.OK = len(r.Missing) == 0 && got.Err == ""
		for _, t := range got.Tables {
			if t.Err != "" {
				r.OK = false
			}
		}
		sort.SliceStable(r.Counts, func(i, j int) bool { return r.Counts[i].Rows > r.Counts[j].Rows })
		if len(r.Counts) > maxTableDetail {
			r.Counts = r.Counts[:maxTableDetail]
		}
		out = append(out, r)
	}
	return out
}
