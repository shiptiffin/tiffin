package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
	"github.com/btahir/tiffin/internal/tokens"
)

func TestVitalsBeaconsToP75(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := state.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tm := tokens.NewManager(db)
	owner, _, _ := tm.Bootstrap(ctx)
	p := &platform.Platform{DB: db, Tokens: tm, Engine: change.NewEngine(db), Home: root, DataRoot: root, Domain: "box.test",
		PublicURL: "https://dashboard.box.test:8443", Log: slog.Default()}
	applyManifest(t, db, `{"project":"shop","apps":{"web":{"routes":["shop"]}},"services":{"analytics":{"retentionDays":30}}}`)
	applyManifest(t, db, `{"project":"plain","apps":{"site":{}}}`)
	var m *Module
	for _, x := range platform.Modules() {
		if am, ok := x.(*Module); ok {
			m = am
		}
	}
	st, err := OpenSQLite(filepath.Join(root, "analytics.db"))
	if err != nil {
		t.Fatal(err)
	}
	m.setup(p, st, nil)
	coll := httptest.NewServer(m.collectorHandler())
	defer coll.Close()
	apiSrv := httptest.NewServer(api.New(api.Deps{DB: db, Engine: p.Engine, Tokens: tm, Platform: p}).Handler())
	defer apiSrv.Close()

	var gpc bool
	send := func(host, ip, ua, body string) int {
		t.Helper()
		req, _ := http.NewRequest("POST", coll.URL+VitalsPath, strings.NewReader(body))
		if gpc {
			req.Header.Set("Sec-GPC", "1")
		}
		req.Host = host + ":8443" // the edge keeps the app's host
		req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
		req.Header.Set("User-Agent", ua)
		req.Header.Set("X-Forwarded-For", ip)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	// 100 page loads of /products/<id>: LCP 1010..2000 ms, CLS 0.001..0.1,
	// and 40 of /: LCP 5000 (poor), INP 120.
	for i := 1; i <= 100; i++ {
		body := fmt.Sprintf(`{"path":"/products/%d?ref=x","metrics":{"LCP":%d,"CLS":%g,"TTFB":120,"bogus":1}}`, 1000+i, 1000+i*10, float64(i)/1000)
		if c := send("shop.box.test", fmt.Sprintf("10.0.%d.1", i), human(i), body); c != 204 {
			t.Fatalf("beacon %d: %d", i, c)
		}
	}
	for i := 0; i < 40; i++ {
		if c := send("shop.box.test", "10.1.0.1", human(1), `{"path":"/","metrics":{"lcp":5000,"INP":120}}`); c != 204 {
			t.Fatalf("home beacon: %d", c)
		}
	}
	// Refused or ignored.
	for name, c := range map[string]int{
		"no analytics":    send("site.plain.box.test", "10.2.0.1", human(1), `{"path":"/","metrics":{"LCP":100}}`),
		"unknown host":    send("nope.box.test", "10.2.0.1", human(1), `{"path":"/","metrics":{"LCP":100}}`),
		"not json":        send("shop.box.test", "10.2.0.1", human(1), `LCP=100`),
		"nothing usable":  send("shop.box.test", "10.2.0.1", human(1), `{"path":"/","metrics":{"LCP":-5,"FID":10}}`),
		"bot (dropped)":   send("shop.box.test", "10.2.0.1", "Googlebot/2.1 (+http://www.google.com/bot.html)", `{"path":"/","metrics":{"LCP":9}}`),
		"out of range ok": send("shop.box.test", "10.2.0.1", human(1), `{"path":"/","metrics":{"LCP":999999,"FCP":900}}`),
	} {
		want := map[string]int{"no analytics": 404, "unknown host": 404, "not json": 400, "nothing usable": 400, "bot (dropped)": 204, "out of range ok": 204}[name]
		if c != want {
			t.Errorf("%s: %d, want %d", name, c, want)
		}
	}
	// Global Privacy Control: not counted.
	gpc = true
	if c := send("shop.box.test", "10.4.0.1", human(1), `{"path":"/users/ada@example.com","metrics":{"LCP":100}}`); c != 204 {
		t.Fatalf("gpc beacon: %d", c)
	}
	gpc = false
	// An email in the path is not stored.
	if c := send("shop.box.test", "10.4.0.2", human(1), `{"path":"/users/ada@example.com/settings","metrics":{"TTFB":100}}`); c != 204 {
		t.Fatalf("email path beacon: %d", c)
	}
	// One address may send 60 a minute.
	if time.Now().Second() >= 58 { // not across a minute
		time.Sleep(3 * time.Second)
	}
	limited := 0
	for i := 0; i < 70; i++ {
		if send("shop.box.test", "10.3.0.1", human(1), `{"path":"/x","metrics":{"FCP":100}}`) == 429 {
			limited++
		}
	}
	if limited != 10 {
		t.Fatalf("rate limit: %d refused, want 10", limited)
	}

	get := func(q string) map[string]any {
		t.Helper()
		req, _ := http.NewRequest("GET", apiSrv.URL+"/v1/analytics/vitals?"+q, nil)
		req.Header.Set("Authorization", "Bearer "+owner)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 {
			t.Fatalf("vitals %s: %d %s", q, res.StatusCode, raw)
		}
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return out
	}
	v := get("project=shop&period=today")
	metrics := map[string]map[string]any{}
	for _, x := range v["metrics"].([]any) {
		mm := x.(map[string]any)
		metrics[mm["name"].(string)] = mm
	}
	near := func(got, want float64) bool { return math.Abs(got-want)/want < 0.03 }
	// 140 LCP samples, the 100 product pages' first: p75 (rank 105) is a home
	// page's 5000 ms, and 100 of 140 are good.
	if lcp := metrics["LCP"]; lcp["samples"].(float64) != 140 || !near(lcp["p75"].(float64), 5000) || lcp["rating"] != "poor" || lcp["good"].(float64) != 0.714 {
		t.Fatalf("LCP: %v", lcp)
	}
	if cls := metrics["CLS"]; cls["samples"].(float64) != 100 || !near(cls["p75"].(float64), 0.075) || cls["rating"] != "good" || cls["unit"] != "" {
		t.Fatalf("CLS: %v", cls)
	}
	if inp := metrics["INP"]; inp["samples"].(float64) != 40 || !near(inp["p75"].(float64), 120) || inp["rating"] != "good" {
		t.Fatalf("INP: %v", inp)
	}
	if fcp := metrics["FCP"]; fcp["samples"].(float64) != 61 { // 60 allowed beacons + the one in range
		t.Fatalf("FCP: %v", fcp)
	}
	pages := v["pages"].([]any)
	first := pages[0].(map[string]any)
	if first["path"] != "/products/[id]" || first["samples"].(float64) != 100 || !near(first["p75"].(map[string]any)["LCP"].(float64), 1750) {
		t.Fatalf("pages: %v", pages)
	}
	for _, x := range pages {
		if p := x.(map[string]any)["path"].(string); strings.Contains(p, "ada") || (p != "/users/[email]/settings" && strings.HasPrefix(p, "/users")) {
			t.Fatalf("page %q stored (gpc or email)", p)
		}
	}
	if pv := get("project=shop&period=today&page=%2Fproducts%2F%5Bid%5D"); len(pv["metrics"].([]any)) == 0 {
		t.Fatalf("page filter found nothing: %v", pv)
	}
	if days := v["days"].([]any); len(days) != 1 || days[0].(map[string]any)["day"] != time.Now().UTC().Format("2006-01-02") {
		t.Fatalf("days: %v", days)
	}
	if v := get("project=shop&app=web&period=yesterday"); len(v["metrics"].([]any)) != 0 {
		t.Fatalf("yesterday: %v", v)
	}

	// Retention: vitals of days past it go with the events.
	if _, err := st.Purge(ctx, "shop", time.Now().Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if v := get("project=shop&period=today"); len(v["metrics"].([]any)) != 0 {
		t.Fatalf("after purge: %v", v)
	}
}

func TestVitalPathsAndPageCap(t *testing.T) {
	for in, want := range map[string]string{
		"/products/[id]":                            "/products/[id]",
		"/products/123?x=1#top":                     "/products/[id]",
		"/u/0b7c4e2a-9f1d-4c3b-8a6e-2d5f7e9a1b3c/a": "/u/[id]/a",
		"/blog/how-to-make-a-great-sandwich-2024":   "/blog/how-to-make-a-great-sandwich-2024",
		"/t/V1StGXR8Z5jdHi6BmyTaa9":                 "/t/[id]",
		"pricing":                                   "/pricing",
		"//a//<b>":                                  "/a/b",
	} {
		if got := vitalPath(in); got != want {
			t.Errorf("vitalPath(%q) = %q, want %q", in, got, want)
		}
	}
	st, _ := OpenSQLite(":memory:")
	ctx := context.Background()
	var rows []VitalCount
	for i := 0; i < maxVitalPaths+5; i++ {
		rows = append(rows, VitalCount{"shop", "web", "2026-10-05", fmt.Sprintf("/p%d", i), "LCP", 10, 1})
	}
	if err := st.AddVitals(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if err := st.AddVitals(ctx, []VitalCount{{"shop", "web", "2026-10-05", "/p1", "LCP", 10, 1}, {"shop", "web", "2026-10-05", "/new", "LCP", 10, 1}}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Vitals(ctx, Query{Project: "shop", From: time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)})
	paths := map[string]int64{}
	for _, r := range got {
		paths[r.Path] += r.N
	}
	if len(paths) != maxVitalPaths+1 || paths["(other)"] != 6 || paths["/p1"] != 2 {
		t.Fatalf("page cap: %d pages, other %d, /p1 %d", len(paths), paths["(other)"], paths["/p1"])
	}
}
