package analytics

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// boxFixture is filterFixture (shop: 3 visitors and 6 page views on 10
// March, 1 and 1 the week before) plus blog (5 visitors, one page each, on
// 9 March, two of them from Bing) and quiet, a project nobody visited.
func boxFixture(t *testing.T) (*Module, string, time.Time) {
	t.Helper()
	ctx := context.Background()
	m, p, owner, now := filterFixture(t)
	applyManifest(t, p.DB, `{"project":"blog","apps":{"web":{}}}`)
	applyManifest(t, p.DB, `{"project":"quiet","apps":{"web":{}}}`)
	at := time.Date(2026, 3, 9, 14, 0, 0, 0, time.UTC)
	var evs []Event
	for i := int64(1); i <= 5; i++ {
		src := ""
		if i <= 2 {
			src = "Bing"
		}
		evs = append(evs, Event{TS: at.Add(time.Duration(i) * time.Minute), Project: "blog", App: "web", Kind: "pageview", Name: "pageview",
			Host: "blog.box.test", Path: "/", RefSource: src, Country: "DE", Browser: "Chrome", OS: "Mac OS X", Device: "desktop", Visitor: 900 + i, Session: 900 + i, Src: "edge"})
	}
	if err := m.store.Insert(ctx, evs); err != nil {
		t.Fatal(err)
	}
	if err := m.store.Rollup(ctx, "blog", "web", "2026-03-09"); err != nil {
		t.Fatal(err)
	}
	return m, owner, now
}

func TestBoxOverview(t *testing.T) {
	ctx := context.Background()
	m, _, now := boxFixture(t)
	o, err := m.BoxOverview(ctx, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if o.Period != "7d" || o.Timeseries.Granularity != "day" || len(o.Timeseries.Points) != 7 {
		t.Fatalf("default period: %s, %s, %d points", o.Period, o.Timeseries.Granularity, len(o.Timeseries.Points))
	}
	if len(o.Projects) != 3 || o.Projects[0].Project != "blog" || o.Projects[1].Project != "shop" || o.Projects[2].Project != "quiet" {
		t.Fatalf("most visitors first, the unvisited project last: %+v", o.Projects)
	}
	if o.Totals != (BoxTotals{8, 11}) || o.Previous != (BoxTotals{1, 1}) {
		t.Fatalf("totals %+v, before %+v", o.Totals, o.Previous)
	}
	blog, shop, quiet := o.Projects[0], o.Projects[1], o.Projects[2]
	if blog.Visitors != 5 || blog.Pageviews != 5 || blog.Previous != (BoxTotals{}) || blog.TopSource != "Bing" {
		t.Fatalf("blog %+v", blog)
	}
	if shop.Visitors != 3 || shop.Pageviews != 6 || shop.Previous != (BoxTotals{1, 1}) || shop.TopSource != "Google" {
		t.Fatalf("shop %+v", shop)
	}
	if quiet.Visitors != 0 || quiet.Pageviews != 0 || quiet.TopSource != "" || len(quiet.Series) != 7 {
		t.Fatalf("a project without visits is listed at zero: %+v", quiet)
	}
	// 9 March is the sixth day, 10 March the seventh.
	if blog.Series[5] != 5 || shop.Series[6] != 3 || o.Timeseries.Points[5].Visitors != 5 || o.Timeseries.Points[6].Visitors != 3 || o.Timeseries.Points[6].Pageviews != 6 {
		t.Fatalf("series: blog %v shop %v box %+v", blog.Series, shop.Series, o.Timeseries.Points)
	}

	// Every period gives each project the numbers of its own overview.
	for _, period := range []string{"24h", "7d", "30d"} {
		o, err := m.BoxOverview(ctx, period, now)
		if err != nil {
			t.Fatal(err)
		}
		var sum, before BoxTotals
		for _, row := range o.Projects {
			own, err := m.Overview(ctx, rangeQuery{Project: row.Project, Period: period}, now)
			if err != nil {
				t.Fatal(err)
			}
			if row.Visitors != own.Totals.Visitors || row.Pageviews != own.Totals.Pageviews ||
				row.Previous != (BoxTotals{own.Previous.Visitors, own.Previous.Pageviews}) || len(row.Series) != len(o.Timeseries.Points) {
				t.Fatalf("%s %s: box %+v, own overview %+v before %+v", period, row.Project, row, own.Totals, own.Previous)
			}
			top := ""
			if len(own.Sources) > 0 {
				top = own.Sources[0].Value
			}
			if row.TopSource != top {
				t.Fatalf("%s %s: top source %q, own overview %q", period, row.Project, row.TopSource, top)
			}
			sum.Visitors += row.Visitors
			sum.Pageviews += row.Pageviews
			before.Visitors += row.Previous.Visitors
			before.Pageviews += row.Previous.Pageviews
		}
		if o.Totals != sum || o.Previous != before {
			t.Fatalf("%s: totals %+v and %+v, rows add up to %+v and %+v", period, o.Totals, o.Previous, sum, before)
		}
	}
	day, err := m.BoxOverview(ctx, "24h", now)
	if err != nil {
		t.Fatal(err)
	}
	if day.Timeseries.Granularity != "hour" || day.Totals != (BoxTotals{8, 11}) {
		t.Fatalf("24h: %s %+v", day.Timeseries.Granularity, day.Totals)
	}
}

func TestBoxOverviewOverHTTP(t *testing.T) {
	ctx := context.Background()
	m, owner, _ := boxFixture(t)
	p := m.p
	srv := httptest.NewServer(api.New(api.Deps{DB: p.DB, Engine: p.Engine, Tokens: p.Tokens, Platform: p}).Handler())
	defer srv.Close()
	get := func(path, key string) (int, BoxOverview) {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+key)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		var o BoxOverview
		_ = json.Unmarshal(b, &o)
		return res.StatusCode, o
	}
	// The fixture's visits are in March 2026, long before today: every project is listed, at zero.
	code, o := get("/v1/analytics/box", owner)
	if code != 200 || o.Period != "7d" || len(o.Projects) != 3 || o.Projects[0].Project != "blog" || o.Projects[0].Series == nil {
		t.Fatalf("%d %+v", code, o)
	}
	if code, _ := get("/v1/analytics/box?period=90d", owner); code != 422 {
		t.Fatalf("a period the box overview does not have: %d", code)
	}
	// A key for some projects cannot read a report that mixes them all.
	ownerP, _ := p.Tokens.Authenticate(ctx, owner)
	limited, _, err := p.Tokens.Create(ctx, ownerP, tokens.CreateRequest{Name: "shop only", Scopes: []tokens.Scope{tokens.ScopeRead}, Projects: []string{"shop"}})
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := get("/v1/analytics/box", limited); code != 403 {
		t.Fatalf("a key limited to one project: %d", code)
	}
}
