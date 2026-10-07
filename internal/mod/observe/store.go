package observe

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/ids"
	"github.com/btahir/tiffin/internal/mod/observe/logtail"
	"github.com/btahir/tiffin/internal/mod/observe/sentry"
	"github.com/btahir/tiffin/internal/tokens"
	_ "github.com/ncruces/go-sqlite3/driver"
)

// Store is observe's own SQLite database (issues, alerts, ingest keys,
// tenants, tail positions). It lives beside the stores it describes, at
// /var/lib/tiffin/observe/observe.db, not in the platform state DB, so busy
// error ingest never contends with changes.
type Store struct {
	db *sql.DB
	mu sync.Mutex // serialises writers
}

var schema = []string{
	`CREATE TABLE IF NOT EXISTS ingest_keys (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		project TEXT NOT NULL, app TEXT NOT NULL, key TEXT NOT NULL UNIQUE,
		created_at TEXT NOT NULL, UNIQUE(project, app))`,
	`CREATE TABLE IF NOT EXISTS tenants (
		id INTEGER PRIMARY KEY AUTOINCREMENT, project TEXT NOT NULL UNIQUE)`,
	`CREATE TABLE IF NOT EXISTS issues (
		id TEXT PRIMARY KEY, project TEXT NOT NULL, app TEXT NOT NULL, fingerprint TEXT NOT NULL,
		title TEXT NOT NULL, culprit TEXT NOT NULL, level TEXT NOT NULL, platform TEXT NOT NULL,
		status TEXT NOT NULL, count INTEGER NOT NULL, first_seen TEXT NOT NULL, last_seen TEXT NOT NULL,
		resolved_at TEXT, last_release TEXT NOT NULL DEFAULT '',
		UNIQUE(project, app, fingerprint))`,
	`CREATE INDEX IF NOT EXISTS issues_project_seen ON issues(project, last_seen DESC)`,
	`CREATE TABLE IF NOT EXISTS issue_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT, issue_id TEXT NOT NULL, event_id TEXT NOT NULL,
		at TEXT NOT NULL, body TEXT NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS issue_events_issue ON issue_events(issue_id, id DESC)`,
	`CREATE TABLE IF NOT EXISTS error_counts (
		project TEXT NOT NULL, minute INTEGER NOT NULL, n INTEGER NOT NULL, PRIMARY KEY(project, minute))`,
	`CREATE TABLE IF NOT EXISTS alert_rules (name TEXT PRIMARY KEY, body TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS alert_state (
		rule TEXT NOT NULL, subject TEXT NOT NULL, firing INTEGER NOT NULL, value REAL NOT NULL,
		since TEXT NOT NULL, summary TEXT NOT NULL, PRIMARY KEY(rule, subject))`,
	`CREATE TABLE IF NOT EXISTS alert_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT, at TEXT NOT NULL, rule TEXT NOT NULL, subject TEXT NOT NULL,
		state TEXT NOT NULL, value REAL NOT NULL, summary TEXT NOT NULL, delivery TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS settings (k TEXT PRIMARY KEY, v TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS tail_positions (path TEXT PRIMARY KEY, inode INTEGER NOT NULL, off INTEGER NOT NULL)`,
}

// OpenStore opens (creating) the database at path; ":memory:" for tests.
func OpenStore(path string) (*Store, error) {
	dsn := "file::memory:"
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		dsn = "file:" + (&url.URL{Path: path}).EscapedPath()
	}
	dsn += "?_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=synchronous(normal)&_pragma=journal_mode(wal)"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	if path == ":memory:" {
		db.SetMaxOpenConns(1)
	}
	for _, s := range schema {
		if _, err := db.Exec(s); err != nil {
			db.Close()
			return nil, fmt.Errorf("observe db: %w", err)
		}
	}
	if path != ":memory:" {
		_ = os.Chmod(path, 0o600)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func parseT(s string) time.Time { t, _ := time.Parse(time.RFC3339Nano, s); return t }

// ---- ingest keys and tenants ----

// IngestKey identifies one app when it sends errors, metrics or logs.
type IngestKey struct {
	ID      int64  `json:"id"`
	Project string `json:"project"`
	App     string `json:"app"`
	Key     string `json:"-"`
}

// KeyFor returns (creating) the ingest key of project/app.
func (s *Store) KeyFor(ctx context.Context, project, app string) (IngestKey, error) {
	k := IngestKey{Project: project, App: app}
	err := s.db.QueryRowContext(ctx, `SELECT id, key FROM ingest_keys WHERE project = ? AND app = ?`, project, app).Scan(&k.ID, &k.Key)
	if err == nil {
		return k, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return k, err
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.ExecContext(ctx, `INSERT INTO ingest_keys(project, app, key, created_at) VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		project, app, hex.EncodeToString(b[:]), now())
	if err != nil {
		return k, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT id, key FROM ingest_keys WHERE project = ? AND app = ?`, project, app).Scan(&k.ID, &k.Key)
	return k, err
}

// LookupKey resolves an ingest key.
func (s *Store) LookupKey(ctx context.Context, key string) (IngestKey, bool) {
	k := IngestKey{Key: key}
	err := s.db.QueryRowContext(ctx, `SELECT id, project, app FROM ingest_keys WHERE key = ?`, key).Scan(&k.ID, &k.Project, &k.App)
	return k, err == nil
}

// DeleteKeys removes a project's ingest keys (when the project goes away).
func (s *Store) DeleteKeys(ctx context.Context, project string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM ingest_keys WHERE project = ?`, project)
	return err
}

// TenantFor returns (creating) the VictoriaLogs tenant of a project. "" is
// the box itself (tenant 0).
func (s *Store) TenantFor(ctx context.Context, project string) (Tenant, error) {
	if project == "" {
		return 0, nil
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM tenants WHERE project = ?`, project).Scan(&id)
	if err == nil {
		return Tenant(id), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO tenants(project) VALUES (?) ON CONFLICT DO NOTHING`, project); err != nil {
		return 0, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT id FROM tenants WHERE project = ?`, project).Scan(&id)
	return Tenant(id), err
}

// ExistingTenant returns a project's tenant without creating one.
func (s *Store) ExistingTenant(ctx context.Context, project string) (Tenant, bool) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM tenants WHERE project = ?`, project).Scan(&id)
	return Tenant(id), err == nil
}

// ---- tail positions ----

func (s *Store) loadPos(path string) (logtail.Position, bool) {
	var p logtail.Position
	err := s.db.QueryRow(`SELECT inode, off FROM tail_positions WHERE path = ?`, path).Scan(&p.Inode, &p.Offset)
	return p, err == nil
}

func (s *Store) savePos(path string, p logtail.Position) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.db.Exec(`INSERT INTO tail_positions(path, inode, off) VALUES (?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET inode = excluded.inode, off = excluded.off`, path, p.Inode, p.Offset)
}

// deletePos forgets a tailed file's position (the file is gone).
func (s *Store) deletePos(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.db.Exec(`DELETE FROM tail_positions WHERE path = ?`, path)
}

// ---- settings ----

// Setting reads a setting.
func (s *Store) Setting(ctx context.Context, k string) string {
	var v string
	_ = s.db.QueryRowContext(ctx, `SELECT v FROM settings WHERE k = ?`, k).Scan(&v)
	return v
}

// SetSetting writes a setting ("" deletes it).
func (s *Store) SetSetting(ctx context.Context, k, v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE k = ?`, k)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings(k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, k, v)
	return err
}

// ---- issues ----

// Issue is a group of error events with the same fingerprint.
type Issue struct {
	ID          string     `json:"id"`
	Project     string     `json:"project"`
	App         string     `json:"app"`
	Title       string     `json:"title"`
	Culprit     string     `json:"culprit,omitempty" doc:"Where it happened: the innermost in-app frame"`
	Level       string     `json:"level"`
	Platform    string     `json:"platform,omitempty"`
	Status      string     `json:"status" enum:"unresolved,resolved,ignored"`
	Count       int64      `json:"count" doc:"Events in this issue since it was first seen"`
	FirstSeen   time.Time  `json:"firstSeen"`
	LastSeen    time.Time  `json:"lastSeen"`
	ResolvedAt  *time.Time `json:"resolvedAt,omitempty"`
	LastRelease string     `json:"lastRelease,omitempty"`
	Fingerprint string     `json:"fingerprint"`
}

// IssueDetail is an issue with its most recent events (newest first).
type IssueDetail struct {
	Issue
	Events []StoredEvent `json:"events"`
}

// StoredEvent is one kept event of an issue.
type StoredEvent struct {
	EventID string        `json:"eventId"`
	At      time.Time     `json:"at"`
	Event   *sentry.Event `json:"event"`
}

// keptEvents is how many events each issue keeps (the count keeps going).
const keptEvents = 20

// RecordEvent groups an event into its issue. A resolved issue that sees a
// new event is unresolved again (a regression). It returns the issue and
// whether it is new.
func (s *Store) RecordEvent(ctx context.Context, project, app string, e *sentry.Event) (*Issue, bool, error) {
	fp := sentry.Fingerprint(e)
	at := e.Timestamp.UTC()
	if at.IsZero() || at.After(time.Now().Add(5*time.Minute)) {
		at = time.Now().UTC()
	}
	body, _ := json.Marshal(e)
	if red := tokens.RedactBytes(body); !bytes.Equal(red, body) {
		// Mask credentials everywhere in the event, title and culprit too.
		body, *e = red, sentry.Event{}
		if err := json.Unmarshal(body, e); err != nil {
			return nil, false, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	var id string
	created := false
	err = tx.QueryRowContext(ctx, `SELECT id FROM issues WHERE project = ? AND app = ? AND fingerprint = ?`, project, app, fp).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		id, created = ids.New("iss"), true
		_, err = tx.ExecContext(ctx, `INSERT INTO issues(id, project, app, fingerprint, title, culprit, level, platform, status, count, first_seen, last_seen, last_release)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'unresolved', 1, ?, ?, ?)`,
			id, project, app, fp, sentry.Title(e), sentry.Culprit(e), e.Level, e.Platform, at.Format(time.RFC3339Nano), at.Format(time.RFC3339Nano), e.Release)
	case err == nil:
		_, err = tx.ExecContext(ctx, `UPDATE issues SET count = count + 1, title = ?, culprit = ?, level = ?,
			last_seen = MAX(last_seen, ?), last_release = CASE WHEN ? != '' THEN ? ELSE last_release END,
			status = CASE WHEN status = 'resolved' THEN 'unresolved' ELSE status END,
			resolved_at = CASE WHEN status = 'resolved' THEN NULL ELSE resolved_at END
			WHERE id = ?`, sentry.Title(e), sentry.Culprit(e), e.Level, at.Format(time.RFC3339Nano), e.Release, e.Release, id)
	}
	if err != nil {
		return nil, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO issue_events(issue_id, event_id, at, body) VALUES (?, ?, ?, ?)`, id, e.EventID, at.Format(time.RFC3339Nano), string(body)); err != nil {
		return nil, false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM issue_events WHERE issue_id = ? AND id NOT IN
		(SELECT id FROM issue_events WHERE issue_id = ? ORDER BY id DESC LIMIT ?)`, id, id, keptEvents); err != nil {
		return nil, false, err
	}
	minute := time.Now().Unix() / 60
	if _, err := tx.ExecContext(ctx, `INSERT INTO error_counts(project, minute, n) VALUES (?, ?, 1)
		ON CONFLICT(project, minute) DO UPDATE SET n = n + 1`, project, minute); err != nil {
		return nil, false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM error_counts WHERE minute < ?`, minute-24*60); err != nil {
		return nil, false, err
	}
	// The summary only: loading the issue's kept events here would decode
	// up to 20 bodies on every ingest, under the writer lock.
	is, err := scanIssue(tx.QueryRowContext(ctx, `SELECT `+issueCols+` FROM issues WHERE id = ?`, id))
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return &is, created, nil
}

// ErrorCount returns error events per project over the last window.
func (s *Store) ErrorCount(ctx context.Context, window time.Duration) (map[string]int64, error) {
	since := time.Now().Add(-window).Unix() / 60
	rows, err := s.db.QueryContext(ctx, `SELECT project, SUM(n) FROM error_counts WHERE minute >= ? GROUP BY project`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var p string
		var n int64
		if err := rows.Scan(&p, &n); err != nil {
			return nil, err
		}
		out[p] = n
	}
	return out, rows.Err()
}

const issueCols = `id, project, app, title, culprit, level, platform, status, count, first_seen, last_seen, resolved_at, last_release, fingerprint`

func scanIssue(sc interface{ Scan(...any) error }) (Issue, error) {
	var i Issue
	var fs, ls string
	var ra sql.NullString
	err := sc.Scan(&i.ID, &i.Project, &i.App, &i.Title, &i.Culprit, &i.Level, &i.Platform, &i.Status, &i.Count, &fs, &ls, &ra, &i.LastRelease, &i.Fingerprint)
	i.FirstSeen, i.LastSeen = parseT(fs), parseT(ls)
	if ra.Valid {
		t := parseT(ra.String)
		i.ResolvedAt = &t
	}
	return i, err
}

// IssueFilter narrows ListIssues.
type IssueFilter struct {
	Projects []string // nil = all
	App      string
	Status   string
	Limit    int
}

// ListIssues lists issues, most recently seen first.
func (s *Store) ListIssues(ctx context.Context, f IssueFilter) ([]Issue, error) {
	q := `SELECT ` + issueCols + ` FROM issues WHERE 1 = 1`
	var args []any
	if f.Projects != nil {
		if len(f.Projects) == 0 {
			return []Issue{}, nil
		}
		q += ` AND project IN (?` + strings.Repeat(",?", len(f.Projects)-1) + `)`
		for _, p := range f.Projects {
			args = append(args, p)
		}
	}
	if f.App != "" {
		q += ` AND app = ?`
		args = append(args, f.App)
	}
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, f.Status)
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	q += ` ORDER BY last_seen DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Issue{}
	for rows.Next() {
		i, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// ErrIssueNotFound is returned for unknown issue IDs.
var ErrIssueNotFound = errors.New("issue not found")

// GetIssue returns an issue with its recent events.
func (s *Store) GetIssue(ctx context.Context, id string) (*IssueDetail, error) {
	i, err := scanIssue(s.db.QueryRowContext(ctx, `SELECT `+issueCols+` FROM issues WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrIssueNotFound
	}
	if err != nil {
		return nil, err
	}
	d := &IssueDetail{Issue: i, Events: []StoredEvent{}}
	rows, err := s.db.QueryContext(ctx, `SELECT event_id, at, body FROM issue_events WHERE issue_id = ? ORDER BY id DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ev StoredEvent
		var at, body string
		if err := rows.Scan(&ev.EventID, &at, &body); err != nil {
			return nil, err
		}
		ev.At = parseT(at)
		ev.Event = &sentry.Event{}
		_ = json.Unmarshal([]byte(body), ev.Event)
		d.Events = append(d.Events, ev)
	}
	return d, rows.Err()
}

// SetIssueStatus resolves, ignores or reopens an issue.
func (s *Store) SetIssueStatus(ctx context.Context, id, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ra any
	if status == "resolved" {
		ra = now()
	}
	res, err := s.db.ExecContext(ctx, `UPDATE issues SET status = ?, resolved_at = ? WHERE id = ?`, status, ra, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrIssueNotFound
	}
	return nil
}

// DeleteProject removes a project's issues (when the project is deleted).
func (s *Store) DeleteProject(ctx context.Context, project string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, q := range []string{
		`DELETE FROM issue_events WHERE issue_id IN (SELECT id FROM issues WHERE project = ?)`,
		`DELETE FROM issues WHERE project = ?`,
		`DELETE FROM error_counts WHERE project = ?`,
		`DELETE FROM ingest_keys WHERE project = ?`,
	} {
		if _, err := s.db.ExecContext(ctx, q, project); err != nil {
			return err
		}
	}
	return nil
}
