// Package state is the platform's own record: project resources, the change
// log, tokens and the audit trail, in one SQLite database (ncruces, pure Go).
// It deliberately does not depend on Postgres, so the platform can report and
// repair itself when Postgres is down.
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
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/ncruces/go-sqlite3"
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
	dsn += sep(dsn) + "_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(normal)"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	if path == ":memory:" {
		db.SetMaxOpenConns(1) // one connection keeps the in-memory db alive and shared
	} else if err := enableWAL(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("state db: enable WAL: %w", err)
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

// enableWAL switches the database to WAL mode (persistent in the file).
// Switching needs an exclusive lock and SQLite does not run the busy handler
// for it, so several processes opening a fresh box at once would fail with
// "database is locked"; retry for up to the busy timeout instead.
func enableWAL(db *sql.DB) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		var mode string
		err := db.QueryRow(`PRAGMA journal_mode=wal`).Scan(&mode)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) || !errors.Is(err, sqlite3.BUSY) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
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
	`CREATE TABLE login_links (
		hash       BLOB PRIMARY KEY,
		created_by TEXT NOT NULL,
		created_at TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		used_at    TEXT
	) STRICT, WITHOUT ROWID`,
	`CREATE TABLE resource_status (
		project    TEXT NOT NULL,
		address    TEXT NOT NULL,
		state      TEXT NOT NULL,
		message    TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (project, address)
	) STRICT, WITHOUT ROWID`,
	`CREATE TABLE secrets (
		project    TEXT NOT NULL,
		name       TEXT NOT NULL,
		ciphertext BLOB NOT NULL,
		updated_at TEXT NOT NULL,
		updated_by TEXT NOT NULL,
		PRIMARY KEY (project, name)
	) STRICT, WITHOUT ROWID`,
	`CREATE TABLE kv (
		ns    TEXT NOT NULL,
		key   TEXT NOT NULL,
		value BLOB NOT NULL,
		PRIMARY KEY (ns, key)
	) STRICT, WITHOUT ROWID`,
	`CREATE TABLE passkeys (
		id         BLOB PRIMARY KEY,
		name       TEXT NOT NULL,
		credential TEXT NOT NULL,
		created_at TEXT NOT NULL,
		last_used  TEXT
	) STRICT, WITHOUT ROWID`,
	`CREATE TABLE approvals (
		id           TEXT PRIMARY KEY,
		project      TEXT NOT NULL,
		plan_hash    TEXT NOT NULL,
		plan         TEXT NOT NULL,
		intent       TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		requester    TEXT NOT NULL,
		status       TEXT NOT NULL,
		created_at   TEXT NOT NULL,
		expires_at   TEXT NOT NULL,
		decided_at   TEXT,
		decided_by   TEXT,
		reason       TEXT,
		challenge    TEXT,
		used_by      TEXT
	) STRICT`,
	`CREATE INDEX approvals_status ON approvals(status, created_at)`,
	`CREATE TABLE people (
		id          TEXT PRIMARY KEY,
		name        TEXT NOT NULL,
		email       TEXT NOT NULL,
		role        TEXT NOT NULL,
		created_at  TEXT NOT NULL,
		created_by  TEXT NOT NULL,
		disabled_at TEXT
	) STRICT`,
	`ALTER TABLE tokens ADD COLUMN person TEXT`,
	`ALTER TABLE login_links ADD COLUMN person TEXT`,
	`ALTER TABLE passkeys ADD COLUMN person TEXT`,
	// API keys: a JSON list of {projects, level} grants (see internal/tokens/keys.go).
	`ALTER TABLE tokens ADD COLUMN grants TEXT`,
	// Project secrets become resources ("secret/NAME", the value still sealed
	// to the box key), so setting one is a change with an undo. Projects that
	// only had secrets (never applied) get their project row first. Leftovers
	// of destroyed projects (a row, no resources) stay behind in the table,
	// where nothing reads them; module secrets ("_email"...) stay too.
	`INSERT INTO projects(name, version) SELECT DISTINCT project, 1 FROM secrets
		WHERE project GLOB '[a-z]*' AND project NOT IN (SELECT name FROM projects)`,
	`INSERT OR IGNORE INTO resources(project, address, spec)
		SELECT project, 'secret/' || name, json_object('sealed', lower(hex(ciphertext)), 'updatedAt', updated_at, 'updatedBy', updated_by)
		FROM secrets WHERE project GLOB '[a-z]*'
		AND (project IN (SELECT project FROM resources) OR project IN (SELECT name FROM projects WHERE version = 1))`,
	`DELETE FROM secrets WHERE project GLOB '[a-z]*' AND EXISTS
		(SELECT 1 FROM resources r WHERE r.project = secrets.project AND r.address = 'secret/' || secrets.name)`,
	// Dashboard sessions: how and where they signed in, as JSON (see internal/tokens/sessions.go).
	`ALTER TABLE tokens ADD COLUMN client TEXT`,
	// Answers kept for Idempotency-Keys (internal/api/idempotency.go), with
	// their time indexed so expired ones are deleted without reading them.
	`CREATE TABLE idempotency (
		id         TEXT PRIMARY KEY,
		created_at TEXT NOT NULL,
		record     BLOB NOT NULL
	) STRICT, WITHOUT ROWID`,
	`CREATE INDEX idempotency_created ON idempotency(created_at)`,
}

// SchemaVersion is the state schema this build writes (box exports record
// it so an older box can refuse a newer box's state).
func SchemaVersion() int { return len(migrations) }

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

// ChangeSeq returns a change's sequence number, for paging the change log.
func (s *DB) ChangeSeq(ctx context.Context, id string) (int64, error) {
	var seq int64
	err := s.sql.QueryRowContext(ctx, `SELECT seq FROM changes WHERE id = ?`, id).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, change.ErrNotFound
	}
	return seq, err
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
	// One query per shape, so a project's page seeks its (project, seq)
	// index instead of scanning the whole box's history.
	var rows *sql.Rows
	var err error
	switch {
	case f.Project != "":
		rows, err = s.sql.QueryContext(ctx, `SELECT body, undone_by FROM changes
			WHERE project = ? AND seq < ? ORDER BY seq DESC LIMIT ?`, f.Project, before, limit)
	case len(f.Projects) > 0:
		args := make([]any, 0, len(f.Projects)+2)
		for _, p := range f.Projects {
			args = append(args, p)
		}
		args = append(args, before, limit)
		rows, err = s.sql.QueryContext(ctx, `SELECT body, undone_by FROM changes
			WHERE project IN (?`+strings.Repeat(`, ?`, len(f.Projects)-1)+`) AND seq < ? ORDER BY seq DESC LIMIT ?`, args...)
	default:
		rows, err = s.sql.QueryContext(ctx, `SELECT body, undone_by FROM changes
			WHERE seq < ? ORDER BY seq DESC LIMIT ?`, before, limit)
	}
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
	return s.projects(ctx, `SELECT DISTINCT project FROM resources ORDER BY project`)
}

// ListConvergingProjects lists ListProjects plus the projects that have no
// resources left but still have live statuses: a deletion the machine has
// not finished (the box restarted, or a delete failed).
func (s *DB) ListConvergingProjects(ctx context.Context) ([]string, error) {
	return s.projects(ctx, `SELECT project FROM resources UNION SELECT project FROM resource_status ORDER BY 1`)
}

func (s *DB) projects(ctx context.Context, query string) ([]string, error) {
	rows, err := s.sql.QueryContext(ctx, query)
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

// ResourceStatus is the live state of one resource.
type ResourceStatus struct {
	Address string `json:"address"`
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
	// Release is set on reads for apps (it is not stored): see the doc tag.
	Release   string    `json:"release,omitempty" enum:"live,none,failed" doc:"Apps: whether production has a release. live: it has one; none: not deployed yet; failed: none, because its last deploy failed (state is then failed too). A ready state alone only means the app's config is applied."`
	UpdatedAt time.Time `json:"updatedAt"`
}

// SetResourceStatus records a resource's live state.
func (s *DB) SetResourceStatus(ctx context.Context, project, address, st, msg string) error {
	_, err := s.sql.ExecContext(ctx, `INSERT INTO resource_status(project, address, state, message, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(project, address) DO UPDATE SET state = excluded.state, message = excluded.message, updated_at = excluded.updated_at`,
		project, address, st, msg, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// DeleteResourceStatus forgets a resource the machine no longer has.
func (s *DB) DeleteResourceStatus(ctx context.Context, project, address string) error {
	_, err := s.sql.ExecContext(ctx, `DELETE FROM resource_status WHERE project = ? AND address = ?`, project, address)
	return err
}

// ResourceStatuses returns a project's live resource states by address.
func (s *DB) ResourceStatuses(ctx context.Context, project string) (map[string]ResourceStatus, error) {
	rows, err := s.sql.QueryContext(ctx, `SELECT address, state, message, updated_at FROM resource_status WHERE project = ?`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ResourceStatus{}
	for rows.Next() {
		var r ResourceStatus
		var at string
		if err := rows.Scan(&r.Address, &r.State, &r.Message, &at); err != nil {
			return nil, err
		}
		r.UpdatedAt, _ = time.Parse(time.RFC3339Nano, at)
		out[r.Address] = r
	}
	return out, rows.Err()
}

// KVGet reads a small platform value (module bookkeeping: ports, ids).
func (s *DB) KVGet(ctx context.Context, ns, key string) ([]byte, bool, error) {
	var v []byte
	err := s.sql.QueryRowContext(ctx, `SELECT value FROM kv WHERE ns = ? AND key = ?`, ns, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return v, err == nil, err
}

// KVPut writes a small platform value.
func (s *DB) KVPut(ctx context.Context, ns, key string, value []byte) error {
	_, err := s.sql.ExecContext(ctx, `INSERT INTO kv(ns, key, value) VALUES (?, ?, ?)
		ON CONFLICT(ns, key) DO UPDATE SET value = excluded.value`, ns, key, value)
	return err
}

// KVDelete removes a platform value.
func (s *DB) KVDelete(ctx context.Context, ns, key string) error {
	_, err := s.sql.ExecContext(ctx, `DELETE FROM kv WHERE ns = ? AND key = ?`, ns, key)
	return err
}

// KVList returns every key/value in a namespace.
func (s *DB) KVList(ctx context.Context, ns string) (map[string][]byte, error) {
	rows, err := s.sql.QueryContext(ctx, `SELECT key, value FROM kv WHERE ns = ? ORDER BY key`, ns)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]byte{}
	for rows.Next() {
		var k string
		var v []byte
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// idemTime has a fixed width, so created_at sorts as time does.
const idemTime = "2006-01-02T15:04:05.000000000Z"

// IdemGet returns the answer kept for an Idempotency-Key id, if any.
func (s *DB) IdemGet(ctx context.Context, id string) ([]byte, time.Time, bool, error) {
	var rec []byte
	var at string
	err := s.sql.QueryRowContext(ctx, `SELECT record, created_at FROM idempotency WHERE id = ?`, id).Scan(&rec, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, false, nil
	} else if err != nil {
		return nil, time.Time{}, false, err
	}
	t, _ := time.Parse(idemTime, at)
	return rec, t, true, nil
}

// IdemPut keeps the answer for an Idempotency-Key id.
func (s *DB) IdemPut(ctx context.Context, id string, at time.Time, record []byte) error {
	_, err := s.sql.ExecContext(ctx, `INSERT INTO idempotency(id, created_at, record) VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET created_at = excluded.created_at, record = excluded.record`,
		id, at.UTC().Format(idemTime), record)
	return err
}

// IdemPurge deletes the answers kept since before t.
func (s *DB) IdemPurge(ctx context.Context, t time.Time) error {
	_, err := s.sql.ExecContext(ctx, `DELETE FROM idempotency WHERE created_at < ?`, t.UTC().Format(idemTime))
	return err
}
