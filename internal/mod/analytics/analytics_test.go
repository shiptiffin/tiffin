package analytics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/mod/analytics/enrich"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
	"github.com/btahir/tiffin/internal/tokens"
)

func applyManifest(t *testing.T, db *state.DB, raw string) {
	t.Helper()
	ctx := context.Background()
	m, err := manifest.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	res, err := change.Resources(m)
	if err != nil {
		t.Fatal(err)
	}
	eng := change.NewEngine(db)
	plan, err := eng.Plan(ctx, m.Project, res)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Apply(ctx, change.ApplyRequest{Plan: plan, Confirm: plan.Hash, Actor: change.Actor{Kind: "system", ID: "test"},
		Authorize: func(*change.Plan) error { return nil }}); err != nil {
		t.Fatal(err)
	}
}

func access(host, uri, ua string, status int, hdr map[string]string) []byte {
	h := map[string][]string{"User-Agent": {ua}}
	for k, v := range hdr {
		h[k] = []string{v}
	}
	method := "GET"
	if hdr["X-Method"] != "" {
		method = hdr["X-Method"]
	}
	b, _ := json.Marshal(map[string]any{"level": "info", "ts": float64(time.Now().UnixNano()) / 1e9, "logger": "http.log.access.access",
		"request":  map[string]any{"client_ip": "8.8.8.8", "method": method, "host": host + ":8443", "uri": uri, "headers": h},
		"duration": 0.01, "size": 100, "status": status})
	return b
}

var doc = map[string]string{"Sec-Fetch-Dest": "document", "Sec-Fetch-Mode": "navigate"}

func with(m map[string]string, kv ...string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

func human(i int) string {
	return fmt.Sprintf("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.%d.0 Safari/537.36", i)
}

func TestFixtureTrafficExactCounts(t *testing.T) {
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
	geo, _ := enrich.OpenGeo(os.Getenv("TIFFIN_TEST_MMDB"))
	m.setup(p, st, geo)
	coll := httptest.NewServer(m.collectorHandler())
	defer coll.Close()
	apiSrv := httptest.NewServer(api.New(api.Deps{DB: db, Engine: p.Engine, Tokens: tm, Platform: p}).Handler())
	defer apiSrv.Close()

	// 5 visitors through the edge: v1 bounces, v2..v5 view two pages.
	for i := 1; i <= 5; i++ {
		m.handleEdge(ctx, access("shop.box.test", "/?utm_source=launch&token=secret", human(i), 200, with(doc, "Referer", "https://www.google.com/")))
		if i > 1 {
			m.handleEdge(ctx, access("shop.box.test", "/pricing", human(i), 200, with(doc, "Referer", "https://shop.box.test/")))
		}
	}
	// Not pageviews.
	for _, ua := range []string{"Googlebot/2.1 (+http://www.google.com/bot.html)", "curl/8.5.0",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/129.0.0.0 Safari/537.36"} {
		m.handleEdge(ctx, access("shop.box.test", "/", ua, 200, doc))
	}
	m.handleEdge(ctx, access("shop.box.test", "/app.js", human(1), 200, map[string]string{"Sec-Fetch-Dest": "script"}))
	m.handleEdge(ctx, access("shop.box.test", "/logo.png", human(1), 200, nil))
	m.handleEdge(ctx, access("shop.box.test", "/next", human(1), 200, with(doc, "Sec-Purpose", "prefetch")))
	m.handleEdge(ctx, access("shop.box.test", "/missing", human(1), 404, doc))
	m.handleEdge(ctx, access("shop.box.test", "/form", human(1), 200, with(doc, "X-Method", "POST")))
	m.handleEdge(ctx, access("site.plain.box.test", "/", human(1), 200, doc))
	m.handleEdge(ctx, access("site.box.test", "/", human(1), 200, doc)) // no project

	beacon := func(ua string, body string) int {
		req, _ := http.NewRequest("POST", coll.URL+"/e", strings.NewReader(body))
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
		req.Header.Set("X-Forwarded-For", "8.8.8.8")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	// v2 navigates client-side: one more pageview, same visitor and session.
	if c := beacon(human(2), `{"n":"pageview","u":"https://shop.box.test:8443/docs","r":null}`); c != 202 {
		t.Fatalf("spa pageview: %d", c)
	}
	if c := beacon(human(3), `{"n":"Signup","u":"https://shop.box.test:8443/pricing","p":{"plan":"pro"}}`); c != 202 {
		t.Fatalf("event: %d", c)
	}
	if c := beacon("python-requests/2.31", `{"n":"pageview","u":"https://shop.box.test/x"}`); c != 202 {
		t.Fatalf("bot beacon: %d", c)
	}
	if c := beacon(human(1), `{"n":"pageview","u":"https://unknown.example/x"}`); c != 404 {
		t.Fatalf("unknown host: %d", c)
	}
	// Server-side track() with the app's key.
	env, err := m.Env(ctx, p, "shop", "web")
	if err != nil || env["TIFFIN_ANALYTICS_KEY"] == "" || env["TIFFIN_ANALYTICS_SCRIPT"] != "https://t.box.test:8443/script.js" {
		t.Fatalf("env %v %v", env, err)
	}
	if e2, _ := m.Env(ctx, p, "nowhere", "site"); e2 != nil {
		t.Fatalf("no analytics env for a project the box doesn't have: %v", e2)
	}
	req, _ := http.NewRequest("POST", coll.URL+"/track", strings.NewReader(`{"name":"Purchase","props":{"amount":49,"currency":"usd"},"ip":"8.8.8.8","ua":"`+human(4)+`"}`))
	req.Header.Set("Authorization", "Bearer "+env["TIFFIN_ANALYTICS_KEY"])
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 202 {
		t.Fatalf("track: %v %v", res, err)
	}
	req, _ = http.NewRequest("POST", coll.URL+"/track", strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Authorization", "Bearer tak_wrong")
	if res, _ := http.DefaultClient.Do(req); res.StatusCode != 401 {
		t.Fatalf("bad key: %d", res.StatusCode)
	}

	get := func(path string, into any) int {
		req, _ := http.NewRequest("GET", apiSrv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+owner)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if err := json.Unmarshal(b, into); err != nil {
			t.Fatalf("%s: %s", path, b)
		}
		return res.StatusCode
	}
	var o Overview
	if c := get("/v1/analytics/overview?project=shop&period=today", &o); c != 200 {
		t.Fatalf("overview %d %+v", c, o)
	}
	want := Totals{Visitors: 5, Pageviews: 10, Sessions: 5, BounceRate: 0.2, ViewsPerVisit: 2, Events: 2}
	got := o.Totals
	got.AvgSessionSeconds = 0
	if got != want {
		t.Fatalf("totals\n got %+v\nwant %+v", o.Totals, want)
	}
	if o.Source != "rollups" || len(o.Pages) != 3 || o.Pages[0].Value != "/" || o.Pages[0].Pageviews != 5 {
		t.Fatalf("pages %+v", o.Pages)
	}
	if o.Sources[0].Value != "launch" || o.UTMSources[0].Value != "launch" || o.Devices[0].Value != "desktop" || o.Browsers[0].Value != "Chrome" {
		t.Fatalf("sources %+v utm %+v devices %+v browsers %+v", o.Sources, o.UTMSources, o.Devices, o.Browsers)
	}
	if geo.Loaded() && (len(o.Countries) == 0 || o.Countries[0].Value != "US") {
		t.Fatalf("countries %+v", o.Countries)
	}
	var o24 Overview
	get("/v1/analytics/overview?project=shop&period=24h", &o24)
	got = o24.Totals
	got.AvgSessionSeconds = 0
	if o24.Source != "events" || got != want {
		t.Fatalf("24h totals from raw events %+v", o24.Totals)
	}
	var rt RealtimeView
	get("/v1/analytics/realtime?project=shop", &rt)
	if rt.Pageviews30m != o.Totals.Pageviews || rt.VisitorsNow != 5 || rt.Events30m != 2 {
		t.Fatalf("realtime disagrees with rollups: %+v", rt)
	}
	var ev EventsView
	get("/v1/analytics/events?project=shop&period=today", &ev)
	if len(ev.Events) != 2 || ev.Events[0].Props == nil {
		t.Fatalf("events %+v", ev)
	}
	found := false
	for _, e := range ev.Events {
		if e.Name == "Purchase" && e.Props["currency"][0].Value == "usd" && e.Props["amount"][0].Value == "49" {
			found = true
		}
	}
	if !found {
		t.Fatalf("purchase props %+v", ev.Events)
	}
	// Privacy: no query strings (except utm) and no IPs or user agents stored.
	raw, _ := os.ReadFile(filepath.Join(root, "analytics.db"))
	wal, _ := os.ReadFile(filepath.Join(root, "analytics.db-wal"))
	for _, secret := range []string{"token=secret", "8.8.8.8", "Chrome/129.0.3.0"} {
		if bytes.Contains(raw, []byte(secret)) || bytes.Contains(wal, []byte(secret)) {
			t.Fatalf("%q stored in the analytics database", secret)
		}
	}
	// A restart rebuilds realtime from the store.
	m2 := &Module{}
	m2.setup(p, st, geo)
	if err := m2.pipe.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	if v := m2.rt.View("shop", "", time.Now()); v.Pageviews30m != 10 || v.VisitorsNow != 5 {
		t.Fatalf("restored realtime %+v", v)
	}
	// Removing the service deletes the data.
	if err := m.Reconcile(ctx, p, "shop", "service/analytics", nil); err != nil {
		t.Fatal(err)
	}
	if v, pv, e, _ := st.Counts(ctx, Query{Project: "shop", From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour)}); v+pv+e != 0 {
		t.Fatal("data left after the service was removed")
	}
}

func TestTrackerScript(t *testing.T) {
	if len(trackerJS) > 1600 {
		t.Fatalf("tracker is %d bytes; keep it under 1.6 KB", len(trackerJS))
	}
	for _, s := range []string{"sendBeacon", "pushState", "replaceState", "popstate", "data-initial", "Outbound Link: Click", "File Download"} {
		if !bytes.Contains(trackerJS, []byte(s)) {
			t.Fatalf("tracker lacks %q", s)
		}
	}
	for _, s := range []string{"cookie", "localStorage", "sessionStorage"} {
		if bytes.Contains(trackerJS, []byte(s)) {
			t.Fatalf("tracker must not use %s", s)
		}
	}
}
