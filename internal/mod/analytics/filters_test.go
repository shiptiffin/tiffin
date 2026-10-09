package analytics

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/state"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// filterFixture stores three visits on 10 March 2026 and one a week before:
//
//	visit 1 (DE, desktop): Google → / → /pricing → /signup, and a Signup event
//	visit 2 (US, mobile):  direct → /pricing (a bounce)
//	visit 3 (DE, desktop): newsletter → /blog/a → /
//	visit 4 (DE, desktop): direct → / on 1 March (the period before)
func filterFixture(t *testing.T) (*Module, *platform.Platform, string, time.Time) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	db, err := state.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tm := tokens.NewManager(db)
	owner, _, _ := tm.Bootstrap(ctx)
	p := &platform.Platform{DB: db, Tokens: tm, Engine: change.NewEngine(db), Home: root, DataRoot: root, Domain: "box.test",
		PublicURL: "https://dashboard.box.test:8443", Log: slog.Default()}
	applyManifest(t, db, `{"project":"shop","apps":{"web":{"routes":["shop"]}},"services":{"analytics":{}}}`)
	st, err := OpenSQLite(filepath.Join(root, "analytics.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var m *Module
	for _, x := range platform.Modules() {
		if am, ok := x.(*Module); ok {
			m = am
		}
	}
	m.setup(p, st, nil)

	day := time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)
	ev := func(at time.Time, visit int64, kind, name, path, src, utm, country, device, browser string) Event {
		return Event{TS: at, Project: "shop", App: "web", Kind: kind, Name: name, Host: "shop.box.test", Path: path, RefSource: src,
			UTMSource: utm, Country: country, Browser: browser, OS: "Mac OS X", Device: device, Visitor: visit * 100, Session: visit, Src: "edge"}
	}
	min := func(n int) time.Duration { return time.Duration(n) * time.Minute }
	evs := []Event{
		ev(day, 1, "pageview", "pageview", "/", "Google", "", "DE", "desktop", "Chrome"),
		ev(day.Add(min(1)), 1, "pageview", "pageview", "/pricing", "", "", "DE", "desktop", "Chrome"),
		ev(day.Add(min(3)), 1, "pageview", "pageview", "/signup", "", "", "DE", "desktop", "Chrome"),
		ev(day.Add(min(4)), 1, "event", "Signup", "/signup", "", "", "DE", "desktop", "Chrome"),
		ev(day.Add(min(70)), 2, "pageview", "pageview", "/pricing", "", "", "US", "mobile", "Mobile Safari"),
		ev(day.Add(min(130)), 3, "pageview", "pageview", "/blog/a", "newsletter", "newsletter", "DE", "desktop", "Firefox"),
		ev(day.Add(min(132)), 3, "pageview", "pageview", "/", "", "", "DE", "desktop", "Firefox"),
		ev(day.AddDate(0, 0, -9), 4, "pageview", "pageview", "/", "", "", "DE", "desktop", "Chrome"),
	}
	if err := st.Insert(ctx, evs); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"2026-03-10", "2026-03-01"} {
		if err := st.Rollup(ctx, "shop", "web", d); err != nil {
			t.Fatal(err)
		}
	}
	return m, p, owner, day.Add(3 * time.Hour)
}

func TestFiltersSelectWholeVisits(t *testing.T) {
	ctx := context.Background()
	m, _, _, now := filterFixture(t)
	view := func(f FilterQuery) *Overview {
		t.Helper()
		o, err := m.Overview(ctx, rangeQuery{Project: "shop", Period: "7d", FilterQuery: f}, now)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	all := view(FilterQuery{})
	if all.Source != "rollups" || all.Totals.Visitors != 3 || all.Totals.Pageviews != 6 || all.Totals.Sessions != 3 || all.Totals.BounceRate != 0.333 {
		t.Fatalf("unfiltered %+v from %s", all.Totals, all.Source)
	}
	if all.Previous.Visitors != 1 || all.Previous.Pageviews != 1 {
		t.Fatalf("previous %+v", all.Previous)
	}
	if len(all.ExitPages) != 3 || len(all.EntryPages) != 3 {
		t.Fatalf("entry %+v exit %+v", all.EntryPages, all.ExitPages)
	}

	cases := []struct {
		name   string
		f      FilterQuery
		visits int64
		views  int64
		bounce float64
		pages  []string
		events int64
	}{
		{"source", FilterQuery{Source: "Google"}, 1, 3, 0, []string{"/", "/pricing", "/signup"}, 1},
		{"utm", FilterQuery{UTMSource: "newsletter"}, 1, 2, 0, []string{"/", "/blog/a"}, 0},
		// A page filter keeps whole visits for bounce rate, but counts only the page.
		{"page", FilterQuery{Page: "/pricing"}, 2, 2, 0.5, []string{"/pricing"}, 0},
		{"entry", FilterQuery{Entry: "/blog/a"}, 1, 2, 0, []string{"/", "/blog/a"}, 0},
		{"exit", FilterQuery{Exit: "/pricing"}, 1, 1, 1, []string{"/pricing"}, 0},
		{"country", FilterQuery{Country: "DE"}, 2, 5, 0, []string{"/", "/blog/a", "/pricing", "/signup"}, 1},
		{"device", FilterQuery{Device: "mobile"}, 1, 1, 1, []string{"/pricing"}, 0},
		{"combined", FilterQuery{Country: "DE", Page: "/"}, 2, 2, 0, []string{"/"}, 0},
		{"none match", FilterQuery{Country: "FR"}, 0, 0, 0, nil, 0},
	}
	for _, c := range cases {
		o := view(c.f)
		if o.Source != "events" || o.Totals.Visitors != c.visits || o.Totals.Pageviews != c.views || o.Totals.BounceRate != c.bounce || o.Totals.Events != c.events {
			t.Errorf("%s: %+v from %s", c.name, o.Totals, o.Source)
		}
		var pages []string
		for _, p := range o.Pages {
			pages = append(pages, p.Value)
		}
		if len(pages) != len(c.pages) {
			t.Errorf("%s: pages %v, want %v", c.name, pages, c.pages)
			continue
		}
		have := map[string]bool{}
		for _, p := range pages {
			have[p] = true
		}
		for _, p := range c.pages {
			if !have[p] {
				t.Errorf("%s: pages %v, want %v", c.name, pages, c.pages)
			}
		}
		// The series adds up to the totals.
		var pv, ss int64
		for _, p := range o.Timeseries.Points {
			pv += p.Pageviews
			ss += p.Sessions
		}
		if pv != c.views || ss != o.Totals.Sessions {
			t.Errorf("%s: series sums %d views, %d visits; totals %+v", c.name, pv, ss, o.Totals)
		}
	}

	// Filtered custom events follow the visits.
	ev, err := m.store.CustomEvents(ctx, Query{Project: "shop", From: now.Add(-24 * time.Hour), To: now, Filters: Filters{Country: "US"}}, 10)
	if err != nil || len(ev) != 0 {
		t.Fatalf("US events %+v %v", ev, err)
	}
	ev, _ = m.store.CustomEvents(ctx, Query{Project: "shop", From: now.Add(-24 * time.Hour), To: now, Filters: Filters{Entry: "/"}}, 10)
	if len(ev) != 1 || ev[0].Name != "Signup" {
		t.Fatalf("events of visits entering on / %+v", ev)
	}
}

func TestSeriesStepsAndPreviousPeriod(t *testing.T) {
	ctx := context.Background()
	m, _, _, now := filterFixture(t)
	day, err := m.Overview(ctx, rangeQuery{Project: "shop", Period: "7d"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if day.Timeseries.Granularity != "day" || len(day.Timeseries.Points) != 7 || len(day.PreviousTimeseries.Points) != 7 {
		t.Fatalf("daily: %d points, %d before", len(day.Timeseries.Points), len(day.PreviousTimeseries.Points))
	}
	last := day.Timeseries.Points[6]
	if last.Pageviews != 6 || last.Sessions != 3 || last.Bounces != 1 || last.DurationMS != 5*60_000 {
		t.Fatalf("10 March %+v", last)
	}
	if p := day.PreviousTimeseries.Points[4]; p.T.Format("2006-01-02") != "2026-03-01" || p.Pageviews != 1 {
		t.Fatalf("1 March, before: %+v", p)
	}
	hours, err := m.Overview(ctx, rangeQuery{Project: "shop", Period: "7d", Interval: "hour"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if hours.Timeseries.Granularity != "hour" || len(hours.Timeseries.Points) != 7*24 {
		t.Fatalf("hourly: %s, %d points", hours.Timeseries.Granularity, len(hours.Timeseries.Points))
	}
	var nine Point
	for _, p := range hours.Timeseries.Points {
		if p.T.Equal(time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)) {
			nine = p
		}
	}
	if nine.Pageviews != 3 || nine.Visitors != 1 || nine.Sessions != 1 {
		t.Fatalf("09:00 %+v", nine)
	}
	if _, err := m.Overview(ctx, rangeQuery{Project: "shop", From: "2025-01-01", To: "2026-03-10", Interval: "hour"}, now); err == nil {
		t.Fatal("hourly points over a year should be refused")
	}
}

func TestFiltersOverHTTP(t *testing.T) {
	m, p, owner, _ := filterFixture(t)
	srv := httptest.NewServer(api.New(api.Deps{DB: p.DB, Engine: p.Engine, Tokens: p.Tokens, Platform: p}).Handler())
	defer srv.Close()
	get := func(path string) (int, Overview) {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+owner)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		var o Overview
		_ = json.Unmarshal(b, &o)
		return res.StatusCode, o
	}
	code, o := get("/v1/analytics/overview?project=shop&from=2026-03-04&to=2026-03-10&country=DE&page=%2F")
	if code != 200 || o.Filters != (Filters{Country: "DE", Page: "/"}) || o.Totals.Pageviews != 2 {
		t.Fatalf("%d %+v %+v", code, o.Filters, o.Totals)
	}
	if code, _ := get("/v1/analytics/overview?project=shop&country=germany"); code != 422 {
		t.Fatalf("bad country code: %d", code)
	}
	_ = m
}

func TestPrivacyGuards(t *testing.T) {
	ctx := context.Background()
	m, _, _, _ := filterFixture(t)
	ua := human(9)
	if ok, why := m.pipe.Add(ctx, Hit{Project: "shop", App: "web", Kind: "pageview", URL: "https://shop.box.test/", IP: "8.8.4.4", UA: ua, Src: "edge", GPC: true}); ok || why != "global privacy control" {
		t.Fatalf("GPC hit counted: %v %s", ok, why)
	}
	if m.pipe.Stats().OptedOut != 1 {
		t.Fatalf("stats %+v", m.pipe.Stats())
	}
	m.pipe.Add(ctx, Hit{Project: "shop", App: "web", Kind: "event", Name: "Outbound Link: Click", URL: "https://shop.box.test/a", IP: "8.8.4.4", UA: ua, Src: "script", Lang: true,
		Props: map[string]any{"url": "https://partner.example/welcome?invite=s3cret#token"}})
	m.pipe.Add(ctx, Hit{Project: "shop", App: "web", Kind: "pageview", URL: "https://shop.box.test/confirm/ada%40example.com/done", IP: "8.8.4.4", UA: ua, Src: "edge", Lang: true})
	if err := m.pipe.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	q := Query{Project: "shop", From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour)}
	ev, _ := m.store.CustomEvents(ctx, q, 10)
	if len(ev) != 1 || ev[0].Props["url"][0].Value != "https://partner.example/welcome" {
		t.Fatalf("outbound url %+v", ev)
	}
	pages, _ := m.store.Top(ctx, q, "page", 10)
	if len(pages) != 1 || pages[0].Value != "/confirm/[email]/done" {
		t.Fatalf("pages %+v", pages)
	}
	// Salts of days without traffic go too.
	if _, err := m.store.Salt(ctx, "2026-01-01"); err != nil {
		t.Fatal(err)
	}
	if err := m.store.ForgetSalts(ctx, time.Now().Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = m.store.(*sqliteStore).db.QueryRow(`SELECT COUNT(*) FROM salts WHERE day = '2026-01-01'`).Scan(&n)
	if n != 0 {
		t.Fatal("an old salt survived")
	}
}
