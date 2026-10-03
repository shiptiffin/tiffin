package analytics

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// Store is where events and rollups live. The SQLite implementation below
// is what boxes use today; the interface is small so a Postgres
// implementation (partitioned events, COPY ingest) can replace it.
type Store interface {
	Insert(ctx context.Context, evs []Event) error
	Rollup(ctx context.Context, project, app, day string) error
	Daily(ctx context.Context, q Query) ([]DayRow, error)
	Hourly(ctx context.Context, q Query) ([]Point, error)
	Top(ctx context.Context, q Query, dim string, limit int) ([]Count, error)
	Sessions(ctx context.Context, q Query) (SessionStats, error)
	Counts(ctx context.Context, q Query) (visitors, pageviews, events int64, err error)
	CustomEvents(ctx context.Context, q Query, limit int) ([]EventSummary, error)
	Recent(ctx context.Context, since time.Time) ([]Event, error)
	Apps(ctx context.Context, project string) ([]string, error)
	Purge(ctx context.Context, project string, before time.Time) (int64, error)
	// Footprint counts a project's stored events older than before (zero
	// time: all of them) and their approximate stored size in bytes.
	Footprint(ctx context.Context, project string, before time.Time) (events, bytes int64, err error)
	DeleteProject(ctx context.Context, project string) error
	Salt(ctx context.Context, day string) ([]byte, error)
	Key(ctx context.Context, project, app string) (string, error)
	LookupKey(ctx context.Context, key string) (project, app string, ok bool)
	Close() error
}

// Event is one stored hit: a pageview or a custom event.
type Event struct {
	TS        time.Time
	Project   string
	App       string
	Kind      string // pageview | event
	Name      string // "pageview" or the custom event name
	Host      string
	Path      string
	RefSource string
	RefHost   string
	UTMSource string
	UTMMedium string
	UTMCamp   string
	Country   string
	Browser   string
	OS        string
	Device    string
	Visitor   int64
	Session   int64
	Props     string // JSON object, custom events only
	Src       string // edge | script | server
}

// Query selects events: one project, optionally one app, a time range.
type Query struct {
	Project string
	App     string // "" = every app
	From    time.Time
	To      time.Time
}

// DayRow is one rollup row.
type DayRow struct {
	Day        string `json:"day"`
	Visitors   int64  `json:"visitors"`
	Pageviews  int64  `json:"pageviews"`
	Sessions   int64  `json:"sessions"`
	Bounces    int64  `json:"bounces"`
	DurationMS int64  `json:"-"`
	Events     int64  `json:"events"`
}

// Point is a timeseries point.
type Point struct {
	T         time.Time `json:"t"`
	Visitors  int64     `json:"visitors"`
	Pageviews int64     `json:"pageviews"`
}

// Count is one breakdown row.
type Count struct {
	Value     string `json:"value"`
	Visitors  int64  `json:"visitors"`
	Pageviews int64  `json:"pageviews"`
}

// SessionStats summarises sessions in a range.
type SessionStats struct {
	Sessions   int64
	Bounces    int64
	DurationMS int64
}

// EventSummary is one custom event name with its props.
type EventSummary struct {
	Name     string                 `json:"name"`
	Count    int64                  `json:"count"`
	Visitors int64                  `json:"visitors"`
	Props    map[string][]PropCount `json:"props" doc:"Top values per property"`
}

// PropCount is one property value's count.
type PropCount struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

// sqliteStore implements Store on one SQLite file.
type sqliteStore struct {
	db *sql.DB
	mu sync.Mutex
}

var sqliteSchema = []string{
	`CREATE TABLE IF NOT EXISTS events (
		id INTEGER PRIMARY KEY,
		ts INTEGER NOT NULL, day TEXT NOT NULL,
		project TEXT NOT NULL, app TEXT NOT NULL, kind TEXT NOT NULL, name TEXT NOT NULL,
		host TEXT NOT NULL, path TEXT NOT NULL, ref_source TEXT NOT NULL, ref_host TEXT NOT NULL,
		utm_source TEXT NOT NULL, utm_medium TEXT NOT NULL, utm_campaign TEXT NOT NULL,
		country TEXT NOT NULL, browser TEXT NOT NULL, os TEXT NOT NULL, device TEXT NOT NULL,
		visitor INTEGER NOT NULL, session INTEGER NOT NULL, props TEXT NOT NULL, src TEXT NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS events_pa_ts ON events(project, app, ts)`,
	`CREATE INDEX IF NOT EXISTS events_ts ON events(ts)`,
	`CREATE TABLE IF NOT EXISTS daily (
		project TEXT NOT NULL, app TEXT NOT NULL, day TEXT NOT NULL,
		visitors INTEGER NOT NULL, pageviews INTEGER NOT NULL, sessions INTEGER NOT NULL,
		bounces INTEGER NOT NULL, duration_ms INTEGER NOT NULL, events INTEGER NOT NULL,
		updated_at INTEGER NOT NULL, PRIMARY KEY(project, app, day))`,
	`CREATE TABLE IF NOT EXISTS salts (day TEXT PRIMARY KEY, salt BLOB NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS keys (project TEXT NOT NULL, app TEXT NOT NULL, key TEXT NOT NULL UNIQUE, PRIMARY KEY(project, app))`,
}

// OpenSQLite opens (creating) the SQLite store; ":memory:" for tests.
func OpenSQLite(path string) (Store, error) {
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
	for _, s := range sqliteSchema {
		if _, err := db.Exec(s); err != nil {
			db.Close()
			return nil, fmt.Errorf("analytics db: %w", err)
		}
	}
	if path != ":memory:" {
		_ = os.Chmod(path, 0o600)
	}
	return &sqliteStore{db: db}, nil
}

func (s *sqliteStore) Close() error { return s.db.Close() }

func dayOf(t time.Time) string { return t.UTC().Format("2006-01-02") }

func (s *sqliteStore) Insert(ctx context.Context, evs []Event) error {
	if len(evs) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st, err := tx.PrepareContext(ctx, `INSERT INTO events(ts, day, project, app, kind, name, host, path, ref_source, ref_host,
		utm_source, utm_medium, utm_campaign, country, browser, os, device, visitor, session, props, src)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, e := range evs {
		if _, err := st.ExecContext(ctx, e.TS.UnixMilli(), dayOf(e.TS), e.Project, e.App, e.Kind, e.Name, e.Host, e.Path, e.RefSource, e.RefHost,
			e.UTMSource, e.UTMMedium, e.UTMCamp, e.Country, e.Browser, e.OS, e.Device, e.Visitor, e.Session, e.Props, e.Src); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Rollup recomputes one day of one app from raw events.
func (s *sqliteStore) Rollup(ctx context.Context, project, app, day string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO daily(project, app, day, visitors, pageviews, sessions, bounces, duration_ms, events, updated_at)
		SELECT ?, ?, ?,
			(SELECT COUNT(DISTINCT visitor) FROM events WHERE project = ?1 AND app = ?2 AND day = ?3 AND kind = 'pageview'),
			(SELECT COUNT(*) FROM events WHERE project = ?1 AND app = ?2 AND day = ?3 AND kind = 'pageview'),
			COALESCE(SUM(1), 0), COALESCE(SUM(pv = 1), 0), COALESCE(SUM(dur), 0),
			(SELECT COUNT(*) FROM events WHERE project = ?1 AND app = ?2 AND day = ?3 AND kind = 'event'),
			?4
		FROM (SELECT session, COUNT(*) AS pv, MAX(ts) - MIN(ts) AS dur FROM events
			WHERE project = ?1 AND app = ?2 AND day = ?3 AND kind = 'pageview' GROUP BY session) WHERE true
		ON CONFLICT(project, app, day) DO UPDATE SET visitors = excluded.visitors, pageviews = excluded.pageviews,
			sessions = excluded.sessions, bounces = excluded.bounces, duration_ms = excluded.duration_ms,
			events = excluded.events, updated_at = excluded.updated_at`,
		project, app, day, time.Now().UnixMilli())
	return err
}

func appFilter(q Query) (string, []any) {
	if q.App == "" {
		return `project = ?`, []any{q.Project}
	}
	return `project = ? AND app = ?`, []any{q.Project, q.App}
}

func (s *sqliteStore) Daily(ctx context.Context, q Query) ([]DayRow, error) {
	where, args := appFilter(q)
	args = append(args, dayOf(q.From), dayOf(q.To.Add(-time.Millisecond)))
	rows, err := s.db.QueryContext(ctx, `SELECT day, SUM(visitors), SUM(pageviews), SUM(sessions), SUM(bounces), SUM(duration_ms), SUM(events)
		FROM daily WHERE `+where+` AND day >= ? AND day <= ? GROUP BY day ORDER BY day`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DayRow
	for rows.Next() {
		var d DayRow
		if err := rows.Scan(&d.Day, &d.Visitors, &d.Pageviews, &d.Sessions, &d.Bounces, &d.DurationMS, &d.Events); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *sqliteStore) Hourly(ctx context.Context, q Query) ([]Point, error) {
	where, args := appFilter(q)
	args = append(args, q.From.UnixMilli(), q.To.UnixMilli())
	// Visitors per hour are distinct within the hour (and day: hashes rotate daily).
	rows, err := s.db.QueryContext(ctx, `SELECT ts / 3600000 AS h, COUNT(DISTINCT visitor), COUNT(*) FROM events
		WHERE `+where+` AND kind = 'pageview' AND ts >= ? AND ts < ? GROUP BY h ORDER BY h`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Point
	for rows.Next() {
		var h int64
		var p Point
		if err := rows.Scan(&h, &p.Visitors, &p.Pageviews); err != nil {
			return nil, err
		}
		p.T = time.UnixMilli(h * 3600000).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

// Dimensions available for breakdowns.
var dims = map[string]string{
	"page":         "path",
	"entry":        "path", // handled specially
	"referrer":     "ref_source",
	"country":      "country",
	"browser":      "browser",
	"os":           "os",
	"device":       "device",
	"utm_source":   "utm_source",
	"utm_medium":   "utm_medium",
	"utm_campaign": "utm_campaign",
	"host":         "host",
}

func (s *sqliteStore) Top(ctx context.Context, q Query, dim string, limit int) ([]Count, error) {
	col, ok := dims[dim]
	if !ok {
		return nil, fmt.Errorf("unknown dimension %q", dim)
	}
	where, args := appFilter(q)
	args = append(args, q.From.UnixMilli(), q.To.UnixMilli())
	var query string
	if dim == "entry" {
		query = `SELECT path, COUNT(DISTINCT visitor), COUNT(*) FROM (
			SELECT path, visitor, ROW_NUMBER() OVER (PARTITION BY session ORDER BY ts) AS rn FROM events
			WHERE ` + where + ` AND kind = 'pageview' AND ts >= ? AND ts < ?) WHERE rn = 1
			GROUP BY path ORDER BY 3 DESC, 1 LIMIT ?`
	} else {
		query = `SELECT ` + col + `, COUNT(DISTINCT day || ':' || visitor), COUNT(*) FROM events
			WHERE ` + where + ` AND kind = 'pageview' AND ts >= ? AND ts < ? AND ` + col + ` != ''
			GROUP BY 1 ORDER BY 2 DESC, 3 DESC, 1 LIMIT ?`
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Count{}
	for rows.Next() {
		var c Count
		if err := rows.Scan(&c.Value, &c.Visitors, &c.Pageviews); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *sqliteStore) Sessions(ctx context.Context, q Query) (SessionStats, error) {
	where, args := appFilter(q)
	args = append(args, q.From.UnixMilli(), q.To.UnixMilli())
	var st SessionStats
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(pv = 1), 0), COALESCE(SUM(dur), 0) FROM
		(SELECT session, COUNT(*) AS pv, MAX(ts) - MIN(ts) AS dur FROM events
		 WHERE `+where+` AND kind = 'pageview' AND ts >= ? AND ts < ? GROUP BY session)`, args...).Scan(&st.Sessions, &st.Bounces, &st.DurationMS)
	return st, err
}

func (s *sqliteStore) Counts(ctx context.Context, q Query) (visitors, pageviews, events int64, err error) {
	where, args := appFilter(q)
	args = append(args, q.From.UnixMilli(), q.To.UnixMilli())
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT CASE WHEN kind = 'pageview' THEN day || ':' || visitor END),
		COALESCE(SUM(kind = 'pageview'), 0), COALESCE(SUM(kind = 'event'), 0)
		FROM events WHERE `+where+` AND ts >= ? AND ts < ?`, args...).Scan(&visitors, &pageviews, &events)
	return
}

func (s *sqliteStore) CustomEvents(ctx context.Context, q Query, limit int) ([]EventSummary, error) {
	where, args := appFilter(q)
	args = append(args, q.From.UnixMilli(), q.To.UnixMilli(), limit)
	rows, err := s.db.QueryContext(ctx, `SELECT name, COUNT(*), COUNT(DISTINCT day || ':' || visitor) FROM events
		WHERE `+where+` AND kind = 'event' AND ts >= ? AND ts < ? GROUP BY name ORDER BY 2 DESC, 1 LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	out := []EventSummary{}
	for rows.Next() {
		e := EventSummary{Props: map[string][]PropCount{}}
		if err := rows.Scan(&e.Name, &e.Count, &e.Visitors); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, e)
	}
	rows.Close()
	for i := range out {
		where, args := appFilter(q)
		args = append(args, out[i].Name, q.From.UnixMilli(), q.To.UnixMilli())
		pr, err := s.db.QueryContext(ctx, `SELECT j.key, CAST(j.value AS TEXT), COUNT(*) FROM events, json_each(events.props) AS j
			WHERE `+where+` AND kind = 'event' AND name = ? AND ts >= ? AND ts < ? AND props != ''
			GROUP BY 1, 2 ORDER BY 1, 3 DESC`, args...)
		if err != nil {
			return nil, err
		}
		for pr.Next() {
			var k, v string
			var n int64
			if err := pr.Scan(&k, &v, &n); err != nil {
				pr.Close()
				return nil, err
			}
			if len(out[i].Props[k]) < 10 {
				out[i].Props[k] = append(out[i].Props[k], PropCount{v, n})
			}
		}
		pr.Close()
	}
	return out, nil
}

func (s *sqliteStore) Recent(ctx context.Context, since time.Time) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ts, project, app, kind, name, path, ref_source, country, visitor, session
		FROM events WHERE ts >= ? ORDER BY ts`, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var ts int64
		if err := rows.Scan(&ts, &e.Project, &e.App, &e.Kind, &e.Name, &e.Path, &e.RefSource, &e.Country, &e.Visitor, &e.Session); err != nil {
			return nil, err
		}
		e.TS = time.UnixMilli(ts).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *sqliteStore) Apps(ctx context.Context, project string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT app FROM daily WHERE project = ? UNION SELECT DISTINCT app FROM events WHERE project = ? ORDER BY 1`, project, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *sqliteStore) Purge(ctx context.Context, project string, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE project = ? AND ts < ?`, project, before.UnixMilli())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	_, err = s.db.ExecContext(ctx, `DELETE FROM daily WHERE project = ? AND day < ?`, project, dayOf(before))
	return n, err
}

// rowOverhead approximates an event row's fixed cost in SQLite: integer
// columns, the record header and its share of the two indexes.
const rowOverhead = 72

func (s *sqliteStore) Footprint(ctx context.Context, project string, before time.Time) (int64, int64, error) {
	q := `SELECT count(*), coalesce(sum(length(app) + length(kind) + length(name) + length(host) + length(path) + length(ref_source) +
		length(ref_host) + length(utm_source) + length(utm_medium) + length(utm_campaign) + length(country) + length(browser) +
		length(os) + length(device) + length(props) + length(src) + length(day) + length(project) + ?), 0)
		FROM events WHERE project = ?`
	args := []any{rowOverhead, project}
	if !before.IsZero() {
		q += ` AND ts < ?`
		args = append(args, before.UnixMilli())
	}
	var n, b int64
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&n, &b)
	return n, b, err
}

func (s *sqliteStore) DeleteProject(ctx context.Context, project string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, q := range []string{`DELETE FROM events WHERE project = ?`, `DELETE FROM daily WHERE project = ?`, `DELETE FROM keys WHERE project = ?`} {
		if _, err := s.db.ExecContext(ctx, q, project); err != nil {
			return err
		}
	}
	return nil
}

// Salt returns the day's visitor salt, creating it; salts older than two
// days are deleted, so yesterday's visitors cannot be linked to today's.
func (s *sqliteStore) Salt(ctx context.Context, day string) ([]byte, error) {
	var salt []byte
	err := s.db.QueryRowContext(ctx, `SELECT salt FROM salts WHERE day = ?`, day).Scan(&salt)
	if err == nil {
		return salt, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	salt = make([]byte, 32)
	_, _ = rand.Read(salt)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO salts(day, salt) VALUES (?, ?) ON CONFLICT DO NOTHING`, day, salt); err != nil {
		return nil, err
	}
	t, _ := time.Parse("2006-01-02", day)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM salts WHERE day < ?`, dayOf(t.Add(-48*time.Hour)))
	err = s.db.QueryRowContext(ctx, `SELECT salt FROM salts WHERE day = ?`, day).Scan(&salt)
	return salt, err
}

func (s *sqliteStore) Key(ctx context.Context, project, app string) (string, error) {
	var k string
	err := s.db.QueryRowContext(ctx, `SELECT key FROM keys WHERE project = ? AND app = ?`, project, app).Scan(&k)
	if err == nil {
		return k, nil
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO keys(project, app, key) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`, project, app, "tak_"+hex.EncodeToString(b[:])); err != nil {
		return "", err
	}
	err = s.db.QueryRowContext(ctx, `SELECT key FROM keys WHERE project = ? AND app = ?`, project, app).Scan(&k)
	return k, err
}

func (s *sqliteStore) LookupKey(ctx context.Context, key string) (string, string, bool) {
	if !strings.HasPrefix(key, "tak_") {
		return "", "", false
	}
	var p, a string
	err := s.db.QueryRowContext(ctx, `SELECT project, app FROM keys WHERE key = ?`, key).Scan(&p, &a)
	return p, a, err == nil
}
