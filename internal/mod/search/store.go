package search

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// The search history: Google's searches day by day (totals, queries and
// pages) and the crawler log's daily counts, kept for 16 months in one
// SQLite file beside analytics. The KV records hold the latest snapshot; the
// Search tab reads any range from here.

// histKeep is how far back Google's Search Console goes, and so the store.
const histKeep = 486 * 24 * time.Hour // 16 months

type histStore struct {
	db *sql.DB
	mu sync.Mutex // one writer at a time
}

var histSchema = []string{
	// pos is position × impressions, so a range's average position is
	// sum(pos) / sum(impressions).
	`CREATE TABLE IF NOT EXISTS gsc_day (host TEXT NOT NULL, day TEXT NOT NULL,
		clicks INTEGER NOT NULL, impressions INTEGER NOT NULL, pos REAL NOT NULL, PRIMARY KEY(host, day)) WITHOUT ROWID`,
	`CREATE TABLE IF NOT EXISTS gsc_query (host TEXT NOT NULL, day TEXT NOT NULL, query TEXT NOT NULL,
		clicks INTEGER NOT NULL, impressions INTEGER NOT NULL, pos REAL NOT NULL, PRIMARY KEY(host, day, query)) WITHOUT ROWID`,
	`CREATE TABLE IF NOT EXISTS gsc_page (host TEXT NOT NULL, day TEXT NOT NULL, page TEXT NOT NULL,
		clicks INTEGER NOT NULL, impressions INTEGER NOT NULL, pos REAL NOT NULL, PRIMARY KEY(host, day, page)) WITHOUT ROWID`,
	// bot is the crawler's name; fakeBot counts claims from outside its list.
	`CREATE TABLE IF NOT EXISTS crawl_day (host TEXT NOT NULL, day TEXT NOT NULL, bot TEXT NOT NULL,
		hits INTEGER NOT NULL, errors INTEGER NOT NULL, PRIMARY KEY(host, day, bot)) WITHOUT ROWID`,
}

const fakeBot = "~fake"

// openHist opens (creating) the store; ":memory:" for tests.
func openHist(path string) (*histStore, error) {
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
	for _, q := range histSchema {
		if _, err := db.Exec(q); err != nil {
			db.Close()
			return nil, fmt.Errorf("search db: %w", err)
		}
	}
	if path != ":memory:" {
		_ = os.Chmod(path, 0o600)
	}
	return &histStore{db: db}, nil
}

// gscRow is one row of a day's searches: the day's totals (key ""), a
// query or a page (a full URL).
type gscRow struct {
	Day, Key           string
	Clicks, Impression int
	Pos                float64 // position × impressions
}

// putGSC replaces a host's searches from..to (inclusive) with rows: Google
// revises its last few days, and a query that drops out goes too.
func (s *histStore) putGSC(ctx context.Context, host, from, to string, days, queries, pages []gscRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range []struct {
		table, col string
		rows       []gscRow
	}{{"gsc_day", "", days}, {"gsc_query", "query", queries}, {"gsc_page", "page", pages}} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+t.table+` WHERE host = ? AND day BETWEEN ? AND ?`, host, from, to); err != nil {
			return err
		}
		cols, marks := "host, day, clicks, impressions, pos", "?,?,?,?,?"
		if t.col != "" {
			cols, marks = "host, day, "+t.col+", clicks, impressions, pos", "?,?,?,?,?,?"
		}
		st, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO `+t.table+` (`+cols+`) VALUES (`+marks+`)`)
		if err != nil {
			return err
		}
		for _, r := range t.rows {
			if r.Day < from || r.Day > to {
				continue
			}
			args := []any{host, r.Day, r.Clicks, r.Impression, r.Pos}
			if t.col != "" {
				args = []any{host, r.Day, r.Key, r.Clicks, r.Impression, r.Pos}
			}
			if _, err := st.ExecContext(ctx, args...); err != nil {
				st.Close()
				return err
			}
		}
		st.Close()
	}
	return tx.Commit()
}

// putCrawl replaces a host's crawler counts for the days given.
func (s *histStore) putCrawl(ctx context.Context, host string, rec *crawlRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO crawl_day (host, day, bot, hits, errors) VALUES (?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer st.Close()
	for name, b := range rec.Bots {
		for day, n := range b.Days {
			if _, err := st.ExecContext(ctx, host, day, name, n.Hits, n.Errors); err != nil {
				return err
			}
		}
	}
	for day, n := range rec.Fakes {
		if _, err := st.ExecContext(ctx, host, day, fakeBot, n, 0); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// forget deletes a host's searches (every host's with host ""), and its
// crawler counts too with crawl.
func (s *histStore) forget(ctx context.Context, host string, crawl bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tables := []string{"gsc_day", "gsc_query", "gsc_page"}
	if crawl {
		tables = append(tables, "crawl_day")
	}
	for _, t := range tables {
		q, args := `DELETE FROM `+t, []any{}
		if host != "" {
			q, args = q+` WHERE host = ?`, []any{host}
		}
		_, _ = s.db.ExecContext(ctx, q, args...)
	}
}

// prune drops what is older than Google keeps.
func (s *histStore) prune(ctx context.Context, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	oldest := now.Add(-histKeep).UTC().Format(time.DateOnly)
	for _, t := range []string{"gsc_day", "gsc_query", "gsc_page", "crawl_day"} {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM `+t+` WHERE day < ?`, oldest)
	}
}

func hostsIn(hosts []string) (string, []any) {
	args := make([]any, len(hosts))
	for i, h := range hosts {
		args[i] = h
	}
	return "host IN (" + strings.TrimSuffix(strings.Repeat("?,", len(hosts)), ",") + ")", args
}

// gscDays are the hosts' daily totals from..to, summed across hosts.
func (s *histStore) gscDays(ctx context.Context, hosts []string, from, to string) ([]SearchDay, error) {
	if len(hosts) == 0 {
		return nil, nil
	}
	in, args := hostsIn(hosts)
	rows, err := s.db.QueryContext(ctx, `SELECT day, SUM(clicks), SUM(impressions), SUM(pos) FROM gsc_day WHERE `+in+` AND day BETWEEN ? AND ? GROUP BY day ORDER BY day`,
		append(args, from, to)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SearchDay
	for rows.Next() {
		var d SearchDay
		var pos float64
		if err := rows.Scan(&d.Date, &d.Clicks, &d.Impressions, &pos); err != nil {
			return nil, err
		}
		if d.Impressions > 0 {
			d.Position = round1(pos / float64(d.Impressions))
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// gscTop are the queries or pages with the most clicks (then impressions) from..to.
func (s *histStore) gscTop(ctx context.Context, hosts []string, table, col, from, to string, limit int) ([]GTop, error) {
	if len(hosts) == 0 {
		return nil, nil
	}
	in, args := hostsIn(hosts)
	rows, err := s.db.QueryContext(ctx, `SELECT `+col+`, SUM(clicks) c, SUM(impressions) i, SUM(pos) FROM `+table+` WHERE `+in+` AND day BETWEEN ? AND ?
		GROUP BY `+col+` ORDER BY c DESC, i DESC, `+col+` LIMIT ?`, append(args, from, to, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GTop
	for rows.Next() {
		var t GTop
		var pos float64
		if err := rows.Scan(&t.Key, &t.Clicks, &t.Impressions, &pos); err != nil {
			return nil, err
		}
		if t.Impressions > 0 {
			t.Position = round1(pos / float64(t.Impressions))
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// gscSpan is the first and last day the hosts have searches for.
func (s *histStore) gscSpan(ctx context.Context, hosts []string) (first, last string) {
	if len(hosts) == 0 {
		return "", ""
	}
	in, args := hostsIn(hosts)
	var a, b sql.NullString
	_ = s.db.QueryRowContext(ctx, `SELECT MIN(day), MAX(day) FROM gsc_day WHERE `+in, args...).Scan(&a, &b)
	return a.String, b.String
}

// crawlTotals are each bot's hits and errors from..to across the hosts, and
// the fake claims.
func (s *histStore) crawlTotals(ctx context.Context, hosts []string, from, to string) (map[string][2]int, int, error) {
	out := map[string][2]int{}
	if len(hosts) == 0 {
		return out, 0, nil
	}
	in, args := hostsIn(hosts)
	rows, err := s.db.QueryContext(ctx, `SELECT bot, SUM(hits), SUM(errors) FROM crawl_day WHERE `+in+` AND day BETWEEN ? AND ? GROUP BY bot`, append(args, from, to)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	fakes := 0
	for rows.Next() {
		var bot string
		var h, e int
		if err := rows.Scan(&bot, &h, &e); err != nil {
			return nil, 0, err
		}
		if bot == fakeBot {
			fakes = h
			continue
		}
		out[bot] = [2]int{h, e}
	}
	return out, fakes, rows.Err()
}

// crawlDays are the hits per day (all bots but fakes) from..to: search
// engines and AI apart.
func (s *histStore) crawlDays(ctx context.Context, hosts []string, from, to string) ([]CrawlDay, error) {
	if len(hosts) == 0 {
		return nil, nil
	}
	in, args := hostsIn(hosts)
	rows, err := s.db.QueryContext(ctx, `SELECT day, bot, SUM(hits) FROM crawl_day WHERE `+in+` AND day BETWEEN ? AND ? AND bot != ? GROUP BY day, bot ORDER BY day`,
		append(args, from, to, fakeBot)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CrawlDay
	for rows.Next() {
		var day, bot string
		var h int
		if err := rows.Scan(&day, &bot, &h); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].Date != day {
			out = append(out, CrawlDay{Date: day})
		}
		d := &out[len(out)-1]
		if aiBot(bot) {
			d.AI += h
		} else {
			d.Search += h
		}
	}
	return out, rows.Err()
}

// aiBot reports whether a crawler is an AI company's (not a search engine's).
func aiBot(name string) bool {
	for _, b := range bots {
		if b.Name == name {
			return b.Operator == "OpenAI" || b.Operator == "Anthropic" || b.Operator == "Perplexity"
		}
	}
	return false
}
