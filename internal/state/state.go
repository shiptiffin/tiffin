// Package state is the platform's own record: project resources, the change
// log, tokens and the audit trail, in one SQLite database (ncruces, pure Go).
// It deliberately does not depend on Postgres, so the platform can report and
// repair itself when Postgres is down. Litestream replication arrives in M4.
package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/btahir/tiffin/internal/change"
	_ "github.com/ncruces/go-sqlite3/driver"
)

// DB is the platform state database. It implements change.Store.
type DB struct {
	sql *sql.DB
}

var _ change.Store = (*DB)(nil)

// Open opens (creating if needed) the state database at path and migrates it.
// Use ":memory:" for an ephemeral database.
func Open(path string) (*DB, error) {
	dsn := "file::memory:"
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		dsn = "file:" + (&url.URL{Path: path}).EscapedPath()
	}
	dsn += sep(dsn) + "_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(wal)&_pragma=synchronous(normal)"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	if path == ":memory:" {
		db.SetMaxOpenConns(1) // one connection keeps the in-memory db alive and shared
	}
	s := &DB{sql: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate state db: %w", err)
	}
	if path != ":memory:" {
		_ = os.Chmod(path, 0o600)
	}
	return s, nil
}

func sep(dsn string) string {
	for _, c := range dsn {
		if c == '?' {
			return "&"
		}
	}
	return "?"
}

// Close closes the database.
func (s *DB) Close() error { return s.sql.Close() }

// SQL exposes the handle for packages that own their own tables (tokens).
func (s *DB) SQL() *sql.DB { return s.sql }

var migrations = []string{
	`CREATE TABLE projects (
		name    TEXT PRIMARY KEY,
		version INTEGER NOT NULL
	) STRICT`,
	`CREATE TABLE resources (
		project TEXT NOT NULL REFERENCES projects(name),
		address TEXT NOT NULL,
		spec    TEXT NOT NULL,
		PRIMARY KEY (project, address)
	) STRICT, WITHOUT ROWID`,
	`CREATE TABLE changes (
		seq       INTEGER PRIMARY KEY AUTOINCREMENT,
		id        TEXT NOT NULL UNIQUE,
		project   TEXT NOT NULL,
		version   INTEGER NOT NULL,
		at        TEXT NOT NULL,
		actor     TEXT NOT NULL,
		intent    TEXT NOT NULL,
		risk      TEXT NOT NULL,
		hash      TEXT NOT NULL,
		undo_of   TEXT,
		undone_by TEXT,
		body      TEXT NOT NULL
	) STRICT`,
	`CREATE INDEX changes_project ON changes(project, seq)`,
	`CREATE TABLE tokens (
		id           TEXT PRIMARY KEY,
		name         TEXT NOT NULL,
		kind         TEXT NOT NULL,
		hash         BLOB NOT NULL UNIQUE,
		scopes       TEXT NOT NULL,
		projects     TEXT NOT NULL,
		sponsor      TEXT,
		created_at   TEXT NOT NULL,
		expires_at   TEXT,
		revoked_at   TEXT,
		last_used_at TEXT
	) STRICT`,
	`CREATE TABLE audit (
		seq    INTEGER PRIMARY KEY AUTOINCREMENT,
		at     TEXT NOT NULL,
		actor  TEXT NOT NULL,
		action TEXT NOT NULL,
		target TEXT NOT NULL,
		detail TEXT NOT NULL
	) STRICT`,
}

func (s *DB) migrate(ctx context.Context) error {
	tx, err := s.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var v int
	if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v > len(migrations) {
		return fmt.Errorf("state db is from a newer tiffin (schema %d > %d)", v, len(migrations))
	}
	for i := v; i < len(migrations); i++ {
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, len(migrations))); err != nil {
		return err
	}
	return tx.Commit()
}

// Load implements change.Store.
func (s *DB) Load(ctx context.Context, project string) (int64, map[string]change.Resource, error) {
	tx, err := s.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()
	return load(ctx, tx, project)
}

type querier interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

func load(ctx context.Context, q querier, project string) (int64, map[string]change.Resource, error) {
	var ver int64
	err := q.QueryRowContext(ctx, `SELECT version FROM projects WHERE name = ?`, project).Scan(&ver)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT address, spec FROM resources WHERE project = ?`, project)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	cur := map[string]change.Resource{}
	for rows.Next() {
		var addr, spec string
		if err := rows.Scan(&addr, &spec); err != nil {
			return 0, nil, err
		}
		cur[addr] = change.Resource{Address: addr, Spec: json.RawMessage(spec)}
	}
	return ver, cur, rows.Err()
}

// Commit implements change.Store.
func (s *DB) Commit(ctx context.Context, c *change.Change) error {
	tx, err := s.sql.BeginTx(ctx, nil) // _txlock=immediate: writers serialize here
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ver, cur, err := load(ctx, tx, c.Project)
	if err != nil {
		return err
	}
	if ver != c.Plan.BaseVersion {
		return change.ErrConflict
	}
	if err := change.CheckPreconditions(cur, c.Plan.Ops); err != nil {
		return err
	}
	if c.UndoOf != "" {
		var undoneBy sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT undone_by FROM changes WHERE id = ?`, c.UndoOf).Scan(&undoneBy)
		if errors.Is(err, sql.ErrNoRows) {
			return change.ErrNotFound
		} else if err != nil {
			return err
		}
		if undoneBy.Valid {
			return &change.PreconditionError{Address: c.UndoOf, Detail: "already undone by " + undoneBy.String}
		}
	}
	for _, o := range c.Plan.Ops {
		switch o.Action {
		case change.Delete:
			_, err = tx.ExecContext(ctx, `DELETE FROM resources WHERE project = ? AND address = ?`, c.Project, o.Address)
		default:
			if ver == 0 {
				// The project row must exist before its first resource.
				if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO projects(name, version) VALUES (?, 0)`, c.Project); err != nil {
					return err
				}
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO resources(project, address, spec) VALUES (?, ?, ?)
				ON CONFLICT(project, address) DO UPDATE SET spec = excluded.spec`, c.Project, o.Address, string(o.After))
		}
		if err != nil {
			return err
		}
	}
	c.Version = ver + 1
	if _, err := tx.ExecContext(ctx, `INSERT INTO projects(name, version) VALUES (?, ?)
		ON CONFLICT(name) DO UPDATE SET version = excluded.version`, c.Project, c.Version); err != nil {
		return err
	}
	body, err := json.Marshal(c)
	if err != nil {
		return err
	}
	actor, _ := json.Marshal(c.Actor)
	if _, err := tx.ExecContext(ctx, `INSERT INTO changes(id, project, version, at, actor, intent, risk, hash, undo_of, body)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Project, c.Version, c.At.UTC().Format(time.RFC3339Nano), string(actor), c.Intent,
		string(c.Plan.Risk), c.Plan.Hash, nullable(c.UndoOf), string(body)); err != nil {
		return err
	}
	if c.UndoOf != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE changes SET undone_by = ? WHERE id = ?`, c.ID, c.UndoOf); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// GetChange implements change.Store.
func (s *DB) GetChange(ctx context.Context, id string) (*change.Change, error) {
	var body string
	var undoneBy sql.NullString
	err := s.sql.QueryRowContext(ctx, `SELECT body, undone_by FROM changes WHERE id = ?`, id).Scan(&body, &undoneBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, change.ErrNotFound
	} else if err != nil {
		return nil, err
	}
	return decodeChange(body, undoneBy)
}

func decodeChange(body string, undoneBy sql.NullString) (*change.Change, error) {
	var c change.Change
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		return nil, err
	}
	c.UndoneBy = undoneBy.String
	return &c, nil
}

// ListChanges implements change.Store. Before is a change sequence number.
func (s *DB) ListChanges(ctx context.Context, f change.ListFilter) ([]*change.Change, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	before := f.Before
	if before <= 0 {
		before = 1<<62 - 1
	}
	rows, err := s.sql.QueryContext(ctx, `SELECT body, undone_by FROM changes
		WHERE (? = '' OR project = ?) AND seq < ? ORDER BY seq DESC LIMIT ?`,
		f.Project, f.Project, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*change.Change
	for rows.Next() {
		var body string
		var undoneBy sql.NullString
		if err := rows.Scan(&body, &undoneBy); err != nil {
			return nil, err
		}
		c, err := decodeChange(body, undoneBy)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListProjects implements change.Store.
func (s *DB) ListProjects(ctx context.Context) ([]string, error) {
	rows, err := s.sql.QueryContext(ctx, `SELECT DISTINCT project FROM resources ORDER BY project`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AuditEvent is a security-relevant event that is not a Change: tokens
// minted or revoked, requests denied.
type AuditEvent struct {
	Seq    int64           `json:"seq"`
	At     time.Time       `json:"at"`
	Actor  string          `json:"actor"`
	Action string          `json:"action"`
	Target string          `json:"target"`
	Detail json.RawMessage `json:"detail,omitempty"`
}

// Audit appends an event. Detail is marshalled to JSON.
func (s *DB) Audit(ctx context.Context, actor, action, target string, detail any) error {
	d, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = s.sql.ExecContext(ctx, `INSERT INTO audit(at, actor, action, target, detail) VALUES (?, ?, ?, ?, ?)`,
		time.Now().UTC().Format(time.RFC3339Nano), actor, action, target, string(d))
	return err
}

// AuditLog returns recent events, newest first.
func (s *DB) AuditLog(ctx context.Context, limit int) ([]AuditEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.sql.QueryContext(ctx, `SELECT seq, at, actor, action, target, detail FROM audit ORDER BY seq DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		var e AuditEvent
		var at, detail string
		if err := rows.Scan(&e.Seq, &at, &e.Actor, &e.Action, &e.Target, &detail); err != nil {
			return nil, err
		}
		e.At, _ = time.Parse(time.RFC3339Nano, at)
		e.Detail = json.RawMessage(detail)
		out = append(out, e)
	}
	return out, rows.Err()
}
