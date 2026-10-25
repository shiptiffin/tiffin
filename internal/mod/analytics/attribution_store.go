package analytics

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// attrStore keeps what marketing attribution links to people: each new
// account's source, the visits that came back from a checkout, first visits
// remembered by the cookie, and payments. It is its own SQLite file
// (attribution.db, next to analytics.db) so the anonymous statistics and
// what is about customers never mix, and turning attribution off empties it
// for the project.
type attrStore struct {
	db *sql.DB
	mu sync.Mutex // one writer at a time
}

// touchCols are a Touch's columns, in Touch field order after Session and At.
const touchCols = `source TEXT NOT NULL DEFAULT '', ref_host TEXT NOT NULL DEFAULT '', utm_source TEXT NOT NULL DEFAULT '',
	utm_medium TEXT NOT NULL DEFAULT '', utm_campaign TEXT NOT NULL DEFAULT '', landing TEXT NOT NULL DEFAULT '', country TEXT NOT NULL DEFAULT ''`

var attrSchema = []string{
	// One row per account: where its sign-up visit came from (source...) and,
	// with the first-visit cookie, where its first visit came from (first_*).
	// matched is visit (the sign-up joined a visit going on), or none.
	`CREATE TABLE IF NOT EXISTS signups (
		project TEXT NOT NULL, user_id TEXT NOT NULL, at INTEGER NOT NULL, app TEXT NOT NULL DEFAULT '',
		session INTEGER NOT NULL DEFAULT 0, matched TEXT NOT NULL, ` + touchCols + `,
		first_at INTEGER NOT NULL DEFAULT 0, first_source TEXT NOT NULL DEFAULT '', first_ref_host TEXT NOT NULL DEFAULT '',
		first_utm_source TEXT NOT NULL DEFAULT '', first_utm_medium TEXT NOT NULL DEFAULT '', first_utm_campaign TEXT NOT NULL DEFAULT '',
		first_landing TEXT NOT NULL DEFAULT '', first_country TEXT NOT NULL DEFAULT '',
		PRIMARY KEY(project, user_id)) WITHOUT ROWID`,
	`CREATE INDEX IF NOT EXISTS signups_at ON signups(project, at)`,
	// A buyer's visit, noted when they come back from Stripe Checkout to a
	// page carrying ?session_id=cs_...; payments join it by that id. Kept 30 days.
	`CREATE TABLE IF NOT EXISTS checkouts (
		project TEXT NOT NULL, checkout_id TEXT NOT NULL, at INTEGER NOT NULL, session INTEGER NOT NULL DEFAULT 0, visit_at INTEGER NOT NULL DEFAULT 0,
		` + touchCols + `, PRIMARY KEY(project, checkout_id)) WITHOUT ROWID`,
	// First visits remembered by the first-visit cookie (its random id), 90 days.
	`CREATE TABLE IF NOT EXISTS first_visits (
		project TEXT NOT NULL, id TEXT NOT NULL, at INTEGER NOT NULL, ` + touchCols + `,
		PRIMARY KEY(project, id)) WITHOUT ROWID`,
	`CREATE INDEX IF NOT EXISTS first_visits_at ON first_visits(at)`,
}

// attrTables are the tables a project's attribution data lives in; other
// files (revenue) add theirs with registerAttrTable.
var attrTables = []string{"signups", "checkouts", "first_visits"}

var attrSchemaMu sync.Mutex

// registerAttrSchema adds statements to attribution.db's schema and tables
// to what deleting a project's attribution data empties. Call from init.
func registerAttrSchema(tables []string, stmts ...string) {
	attrSchemaMu.Lock()
	defer attrSchemaMu.Unlock()
	attrSchema = append(attrSchema, stmts...)
	attrTables = append(attrTables, tables...)
}

func openAttrStore(path string) (*attrStore, error) {
	dsn := "file::memory:"
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
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
	attrSchemaMu.Lock()
	stmts := append([]string(nil), attrSchema...)
	attrSchemaMu.Unlock()
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &attrStore{db: db}, nil
}

// Signup is one account's attribution.
type Signup struct {
	UserID  string    `json:"userId"`
	At      time.Time `json:"at"`
	App     string    `json:"app,omitempty"`
	Matched string    `json:"matched" enum:"visit,none" doc:"visit: the sign-up joined a visit going on, whose source this is; none: no visit was found (a sign-up link opened on another device, say)"`
	Visit   Touch     `json:"visit" doc:"Where the sign-up visit came from"`
	First   *Touch    `json:"first,omitempty" doc:"Where the person's first visit came from, remembered by the first-visit cookie"`
}

func (s *attrStore) putSignup(ctx context.Context, project string, su Signup) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := su.Visit
	f := Touch{}
	if su.First != nil {
		f = *su.First
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO signups (project, user_id, at, app, session, matched, source, ref_host, utm_source, utm_medium, utm_campaign, landing, country,
		first_at, first_source, first_ref_host, first_utm_source, first_utm_medium, first_utm_campaign, first_landing, first_country)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(project, user_id) DO NOTHING`,
		project, su.UserID, su.At.UnixMilli(), su.App, v.Session, su.Matched, v.Source, v.RefHost, v.UTMSource, v.UTMMedium, v.UTMCampaign, v.Landing, v.Country,
		ms(f.At), f.Source, f.RefHost, f.UTMSource, f.UTMMedium, f.UTMCampaign, f.Landing, f.Country)
	return err
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}

// signup returns one account's attribution.
func (s *attrStore) signup(ctx context.Context, project, userID string) (*Signup, error) {
	row := s.db.QueryRowContext(ctx, `SELECT user_id, at, app, session, matched, source, ref_host, utm_source, utm_medium, utm_campaign, landing, country,
		first_at, first_source, first_ref_host, first_utm_source, first_utm_medium, first_utm_campaign, first_landing, first_country
		FROM signups WHERE project = ? AND user_id = ?`, project, userID)
	var su Signup
	var at, fat int64
	var f Touch
	err := row.Scan(&su.UserID, &at, &su.App, &su.Visit.Session, &su.Matched, &su.Visit.Source, &su.Visit.RefHost, &su.Visit.UTMSource, &su.Visit.UTMMedium,
		&su.Visit.UTMCampaign, &su.Visit.Landing, &su.Visit.Country, &fat, &f.Source, &f.RefHost, &f.UTMSource, &f.UTMMedium, &f.UTMCampaign, &f.Landing, &f.Country)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	su.At = fromMS(at)
	if fat != 0 {
		f.At = fromMS(fat)
		su.First = &f
	}
	return &su, nil
}

func (s *attrStore) deleteSignup(ctx context.Context, project, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM signups WHERE project = ? AND user_id = ?`, project, userID)
	return err
}

// putCheckout notes a buyer's visit coming back from Checkout; the first
// return of a checkout session wins.
func (s *attrStore) putCheckout(ctx context.Context, project, id string, t Touch, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO checkouts (project, checkout_id, at, session, visit_at, source, ref_host, utm_source, utm_medium, utm_campaign, landing, country)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(project, checkout_id) DO NOTHING`,
		project, id, at.UnixMilli(), t.Session, ms(t.At), t.Source, t.RefHost, t.UTMSource, t.UTMMedium, t.UTMCampaign, t.Landing, t.Country)
	return err
}

// checkout returns the visit a buyer came back to from checkout session id.
func (s *attrStore) checkout(ctx context.Context, project, id string) (Touch, bool, error) {
	var t Touch
	var vat int64
	err := s.db.QueryRowContext(ctx, `SELECT session, visit_at, source, ref_host, utm_source, utm_medium, utm_campaign, landing, country FROM checkouts
		WHERE project = ? AND checkout_id = ?`, project, id).Scan(&t.Session, &vat, &t.Source, &t.RefHost, &t.UTMSource, &t.UTMMedium, &t.UTMCampaign, &t.Landing, &t.Country)
	if errors.Is(err, sql.ErrNoRows) {
		return t, false, nil
	}
	t.At = fromMS(vat)
	return t, err == nil, err
}

// putFirstVisit remembers a first visit under the cookie's id; a later visit
// with the same id keeps the first.
func (s *attrStore) putFirstVisit(ctx context.Context, project, id string, t Touch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO first_visits (project, id, at, source, ref_host, utm_source, utm_medium, utm_campaign, landing, country)
		VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT(project, id) DO NOTHING`,
		project, id, t.At.UnixMilli(), t.Source, t.RefHost, t.UTMSource, t.UTMMedium, t.UTMCampaign, t.Landing, t.Country)
	return err
}

func (s *attrStore) firstVisit(ctx context.Context, project, id string) (*Touch, error) {
	var t Touch
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT at, source, ref_host, utm_source, utm_medium, utm_campaign, landing, country FROM first_visits WHERE project = ? AND id = ?`,
		project, id).Scan(&at, &t.Source, &t.RefHost, &t.UTMSource, &t.UTMMedium, &t.UTMCampaign, &t.Landing, &t.Country)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.At = fromMS(at)
	return &t, nil
}

// Retention of the short-lived tables: a checkout return only matters until
// its payment arrives, a first visit for the cookie's 90 days.
const (
	checkoutKeep   = 30 * 24 * time.Hour
	firstVisitKeep = 90 * 24 * time.Hour
)

// purge deletes checkout returns and first visits past their time.
func (s *attrStore) purge(ctx context.Context, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM checkouts WHERE at < ?`, now.Add(-checkoutKeep).UnixMilli()); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM first_visits WHERE at < ?`, now.Add(-firstVisitKeep).UnixMilli())
	return err
}

// deleteProject deletes all of a project's attribution data.
func (s *attrStore) deleteProject(ctx context.Context, project string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	attrSchemaMu.Lock()
	tables := append([]string(nil), attrTables...)
	attrSchemaMu.Unlock()
	for _, t := range tables {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM `+t+` WHERE project = ?`, project); err != nil {
			return err
		}
	}
	return nil
}

// projectRows counts a project's attribution rows (plans say what turning
// attribution off deletes).
func (s *attrStore) projectRows(ctx context.Context, project string) (map[string]int64, error) {
	attrSchemaMu.Lock()
	tables := append([]string(nil), attrTables...)
	attrSchemaMu.Unlock()
	out := map[string]int64{}
	for _, t := range tables {
		var n int64
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM `+t+` WHERE project = ?`, project).Scan(&n); err != nil {
			return nil, err
		}
		out[t] = n
	}
	return out, nil
}

func (s *attrStore) close() error { return s.db.Close() }
