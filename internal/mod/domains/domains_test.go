package domains

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/dnskit"
	"github.com/shiptiffin/tiffin/internal/dnskit/dnstest"
	"github.com/shiptiffin/tiffin/internal/edge"
	"github.com/shiptiffin/tiffin/internal/edge/switchboard"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/state"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

const boxIP = "198.51.100.7"

type fakeEdge struct {
	mu      sync.Mutex
	reloads int
	routes  []edge.Route
}

func (f *fakeEdge) SetRoutes(rs []edge.Route) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reloads++
	f.routes = rs
	return nil
}

func (f *fakeEdge) Switchboard() string                     { return "127.0.0.1:1" }
func (f *fakeEdge) TableSource(func() switchboard.Table)    {}
func (f *fakeEdge) SyncTable() error                        { return nil }
func (f *fakeEdge) Busy([]string) (int64, error)            { return 0, nil }
func (f *fakeEdge) Activity() (map[string]time.Time, error) { return nil, nil }

type harness struct {
	t        *testing.T
	m        *Module
	p        *platform.Platform
	dns      *dnstest.Server
	srv      *httptest.Server
	owner    string
	edge     *fakeEdge
	clock    time.Time
	restarts []string
}

var fakeProvider *dnstest.Provider

func init() {
	dnskit.Register(dnskit.Kind{Name: "fake", Label: "Fake DNS", Help: "a test provider",
		Fields: []dnskit.Field{{Name: "token", Label: "token", Secret: true}},
		New: func(c map[string]string) (dnskit.Provider, error) {
			if c["token"] == "bad" {
				return &dnstest.Provider{S: fakeProvider.S, Fail: errors.New("401 invalid token"), Calls: map[string]int{}}, nil
			}
			return fakeProvider, nil
		}})
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	db, err := state.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tm := tokens.NewManager(db)
	owner, _, err := tm.Bootstrap(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sec, err := platform.OpenSecrets(db, dir)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, dns: dnstest.Start(t), owner: owner, edge: &fakeEdge{}, clock: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}
	for _, z := range []string{"example.test", "shop.test", "box.test", "auto.test"} {
		h.dns.AddZone(z)
	}
	fakeProvider = dnstest.NewProvider(h.dns)
	h.p = &platform.Platform{DB: db, Engine: change.NewEngine(db), Tokens: tm, Secrets: sec, Domain: "box.test",
		PublicURL: "https://dashboard.box.test", Edge: h.edge,
		Reach: platform.Reach{ACME: true, PublicIPs: []netip.Addr{netip.MustParseAddr(boxIP)}, Resolvers: []string{h.dns.Addr()},
			DomainSource: "flag", DefaultDomain: "box.test"}}
	h.p.Restart = func(reason string) { h.restarts = append(h.restarts, reason) }
	for _, m := range platform.Modules() {
		if dm, ok := m.(*Module); ok {
			h.m = dm
		}
	}
	*h.m = *New()
	h.m.now = func() time.Time { return h.clock }
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := h.m.attach(ctx, h.p); err != nil {
		t.Fatal(err)
	}
	h.srv = httptest.NewServer(api.New(api.Deps{DB: db, Engine: h.p.Engine, Tokens: tm, Platform: h.p, Version: "test"}).Handler())
	t.Cleanup(h.srv.Close)
	return h
}

func (h *harness) call(method, path string, body any) (int, map[string]any) {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.srv.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+h.owner)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var out map[string]any
	if raw[0] == '[' {
		var list []any
		_ = json.Unmarshal(raw, &list)
		out = map[string]any{"list": list}
	} else {
		_ = json.Unmarshal(raw, &out)
	}
	return res.StatusCode, out
}

// confirmed calls a plan-driven operation twice: first for the plan, then
// with its hash.
func (h *harness) confirmed(method, path string, body map[string]any) map[string]any {
	h.t.Helper()
	code, out := h.call(method, path, body)
	if code != 428 {
		h.t.Fatalf("%s %s without confirm: %d %v", method, path, code, out)
	}
	body["confirm"] = out["plan"].(map[string]any)["hash"]
	code, out = h.call(method, path, body)
	if code != 200 {
		h.t.Fatalf("%s %s: %d %v", method, path, code, out)
	}
	return out
}

func (h *harness) project(name string) {
	h.t.Helper()
	man := map[string]any{"project": name, "apps": map[string]any{"web": map[string]any{}, "api": map[string]any{}, "jobs": map[string]any{"role": "worker"}}}
	h.confirmed("POST", "/v1/apply", map[string]any{"manifest": man})
}

func (h *harness) domain(project, host string) map[string]any {
	h.t.Helper()
	_, out := h.call("GET", "/v1/projects/"+project+"/domains", nil)
	for _, d := range out["list"].([]any) {
		if dm := d.(map[string]any); dm["domain"] == host {
			return dm
		}
	}
	return nil
}

func TestCustomDomainLifecycle(t *testing.T) {
	h := newHarness(t)
	h.project("shop")

	// Adding a domain is a planned change to the manifest.
	out := h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "Example.test", "app": "web", "www": true})
	d := out["domain"].(map[string]any)
	if d["state"] != StateWaiting || !strings.Contains(d["reason"].(string), "no A or AAAA record") {
		t.Fatalf("new domain: %v", d)
	}
	recs := d["records"].([]any)
	if len(recs) != 1 || recs[0].(map[string]any)["value"] != boxIP || recs[0].(map[string]any)["host"] != "@" {
		t.Errorf("records: %v", recs)
	}
	if d["alternative"] != nil {
		t.Errorf("an apex gets no CNAME alternative: %v", d["alternative"])
	}
	// tiffin pull sees it: the manifest has the route and the www option.
	_, man := h.call("GET", "/v1/projects/shop/manifest", nil)
	cfg := man["config"].(string)
	if !strings.Contains(cfg, `"example.test"`) || !strings.Contains(cfg, `www: "redirect"`) {
		t.Errorf("manifest config:\n%s", cfg)
	}
	// The API path on another app.
	h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "example.test", "app": "api", "path": "/api"})
	if d := h.domain("shop", "example.test"); len(d["routes"].([]any)) != 2 || d["wwwRedirect"] != true {
		t.Errorf("routes: %v", d)
	}
	if w := h.domain("shop", "www.example.test"); w == nil || w["redirectTo"] != "example.test" {
		t.Errorf("www: %v", w)
	}
	// The www redirect is an edge route.
	if !slices.ContainsFunc(h.edge.routes, func(r edge.Route) bool { return r.Host == "www.example.test" && r.RedirectTo == "example.test" }) {
		t.Errorf("edge routes: %+v", h.edge.routes)
	}
	if st := h.m.certState(); len(st.Ready) != 0 {
		t.Errorf("nothing is ready before DNS points here: %v", st.Ready)
	}

	// DNS points elsewhere.
	h.dns.Set("example.test", "A", "5.6.7.8")
	code, d := h.call("POST", "/v1/projects/shop/domains/example.test/check", nil)
	if code != 200 || d["state"] != StateWaiting || !strings.Contains(d["reason"].(string), "points to 5.6.7.8, not this box ("+boxIP+")") {
		t.Fatalf("pointing elsewhere: %d %v", code, d)
	}
	if d["found"].([]any)[0] != "5.6.7.8" {
		t.Errorf("found: %v", d["found"])
	}
	// Backoff: checks space out.
	r := h.m.recs["example.test"]
	first := r.NextCheckAt.Sub(h.clock)
	h.clock = r.NextCheckAt
	h.m.tick(t.Context())
	if second := h.m.recs["example.test"].NextCheckAt.Sub(h.clock); first != 15*time.Second || second != 30*time.Second {
		t.Errorf("backoff %s then %s", first, second)
	}

	// Fixed: the domain becomes ready and the edge reloads.
	h.dns.Set("example.test", "A", boxIP)
	h.dns.Set("www.example.test", "CNAME", "example.test.")
	before := h.edge.reloads
	h.call("POST", "/v1/projects/shop/domains/example.test/check", nil)
	h.call("POST", "/v1/projects/shop/domains/www.example.test/check", nil)
	if d := h.domain("shop", "example.test"); d["state"] != StateIssuing || d["certificate"] == nil {
		t.Fatalf("after DNS: %v", d)
	}
	if st := h.m.certState(); !slices.Equal(st.Ready, []string{"example.test", "www.example.test"}) {
		t.Errorf("ready: %v", st.Ready)
	}
	if h.edge.reloads == before {
		t.Error("the edge was not reloaded")
	}
	// A subdomain gets the CNAME alternative; a CAA record blocks it.
	h.dns.Set("shop.test", "CAA", `0 issue "digicert.com"`)
	h.dns.Set("app.shop.test", "A", boxIP)
	h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "app.shop.test", "app": "api"})
	d = h.domain("shop", "app.shop.test")
	if d["state"] != StateError || !strings.Contains(d["reason"].(string), `CAA 0 issue "letsencrypt.org"`) {
		t.Errorf("CAA: %v", d)
	}
	if alt := d["alternative"].([]any); len(alt) != 1 || alt[0].(map[string]any)["type"] != "CNAME" || alt[0].(map[string]any)["value"] != "box.test" {
		t.Errorf("alternative: %v", d["alternative"])
	}
	if slices.Contains(h.m.certState().Ready, "app.shop.test") {
		t.Error("a CAA-blocked domain must not reach the edge")
	}

	// Refusals.
	for _, tc := range []struct {
		body map[string]any
		code int
	}{
		{map[string]any{"domain": "web.box.test", "app": "web"}, 422},  // under the box domain
		{map[string]any{"domain": "example.test", "app": "nope"}, 404}, // no such app
		{map[string]any{"domain": "x.example.test", "app": "jobs"}, 422},
		{map[string]any{"domain": "localhost", "app": "web"}, 422},
	} {
		if code, out := h.call("POST", "/v1/projects/shop/domains", tc.body); code != tc.code {
			t.Errorf("%v: %d %v", tc.body, code, out)
		}
	}

	// Removing it is a planned change too.
	h.confirmed("POST", "/v1/projects/shop/domains/example.test/remove", map[string]any{})
	h.m.tick(t.Context())
	if d := h.domain("shop", "example.test"); d != nil {
		t.Errorf("still listed: %v", d)
	}
	if st := h.m.certState(); slices.Contains(st.Ready, "example.test") {
		t.Errorf("still ready: %v", st.Ready)
	}
	if code, _ := h.call("POST", "/v1/projects/shop/domains/example.test/remove", map[string]any{}); code != 404 {
		t.Errorf("remove twice: %d", code)
	}
}

func TestBoxDomainSwitch(t *testing.T) {
	h := newHarness(t)
	code, chk := h.call("GET", "/v1/domain/check?domain=example.test", nil)
	if code != 200 || chk["ok"] != false || len(chk["records"].([]any)) != 2 {
		t.Fatalf("check: %d %v", code, chk)
	}
	// Not pointing here: nothing changes, and the answer lists the records.
	code, out := h.call("POST", "/v1/domain", map[string]any{"domain": "example.test"})
	if code != 412 || len(out["errors"].([]any)) != 2 || !strings.Contains(out["detail"].(string), "example.test A "+boxIP) {
		t.Fatalf("set without DNS: %d %v", code, out)
	}
	if len(h.restarts) != 0 {
		t.Fatal("restarted without a switch")
	}
	h.dns.Set("example.test", "A", boxIP)
	h.dns.Set("*.example.test", "A", boxIP)
	code, out = h.call("POST", "/v1/domain", map[string]any{"domain": "example.test", "email": "ops@example.test"})
	if code != 200 || out["restarting"] != true || out["dashboard"] != "dashboard.example.test" {
		t.Fatalf("set: %d %v", code, out)
	}
	if len(h.restarts) != 1 {
		t.Fatalf("restarts: %v", h.restarts)
	}
	saved, _ := platform.LoadBoxDomain(t.Context(), h.p.DB)
	if saved.Domain != "example.test" || saved.Previous != "box.test" || saved.Email != "ops@example.test" {
		t.Fatalf("saved: %+v", saved)
	}
	// After the restart the box serves the new domain, and the old one as
	// an alias until the new certificates are live, then for an hour.
	d, src, _ := platform.ChooseDomain("box.test", saved, h.p.Reach.PublicIPs)
	if d != "example.test" || src != "set" {
		t.Fatalf("ChooseDomain = %s %s", d, src)
	}
	h.p.Domain, h.p.Reach.DomainSource = d, src
	if a := h.m.certState().Aliases; !slices.Equal(a, []string{"box.test"}) {
		t.Fatalf("aliases: %v", a)
	}
	h.m.tick(t.Context()) // the dashboard certificate is not live (no edge here): keep the alias
	if h.m.box.PreviousUntil != (time.Time{}) {
		t.Fatal("grace started before the new certificate")
	}
	h.p.Reach.ACME = false // stand-in for "the new certificates are live"
	h.m.tick(t.Context())
	if want := h.clock.Add(AliasGrace); !h.m.box.PreviousUntil.Equal(want) {
		t.Fatalf("grace until %s, want %s", h.m.box.PreviousUntil, want)
	}
	_, st := h.call("GET", "/v1/domain", nil)
	if prev := st["previous"].(map[string]any); prev["domain"] != "box.test" || prev["until"] == nil {
		t.Errorf("status previous: %v", st["previous"])
	}
	h.clock = h.clock.Add(AliasGrace + time.Second)
	reloads := h.edge.reloads
	h.m.tick(t.Context())
	if a := h.m.certState().Aliases; len(a) != 0 || h.edge.reloads == reloads {
		t.Errorf("alias after the grace: %v (reloads %d → %d)", a, reloads, h.edge.reloads)
	}
	// Back to the automatic domain.
	h.p.Reach.ACME = true
	code, out = h.call("DELETE", "/v1/domain", nil)
	if code != 200 || len(h.restarts) != 2 {
		t.Fatalf("unset: %d %v", code, out)
	}
	saved, _ = platform.LoadBoxDomain(t.Context(), h.p.DB)
	if saved.Domain != "" || saved.Previous != "example.test" {
		t.Errorf("after unset: %+v", saved)
	}
	// A local box cannot take a domain.
	h.p.Reach = platform.Reach{}
	if code, out := h.call("POST", "/v1/domain", map[string]any{"domain": "example.test"}); code != 412 || !strings.Contains(out["detail"].(string), "no public IP") {
		t.Errorf("local box: %d %v", code, out)
	}
}

// TestBareBoxDomain: the box domain itself redirects to the dashboard until
// a project claims it (tiffin domains add), which the API allows.
func TestBareBoxDomain(t *testing.T) {
	h := newHarness(t)
	_, st := h.call("GET", "/v1/domain", nil)
	if b, _ := json.Marshal(st["bare"]); string(b) != `[{"host":"box.test","redirectsTo":"https://dashboard.box.test/"}]` {
		t.Errorf("bare: %s", b)
	}
	if !strings.Contains(st["summary"].(string), "box.test itself sends visitors to the dashboard until an app uses it.") {
		t.Errorf("summary: %s", st["summary"])
	}
	h.project("shop")
	out := h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "box.test", "app": "web"})
	if d := out["domain"].(map[string]any); d["domain"] != "box.test" || d["alternative"] != nil {
		t.Errorf("added: %v", d)
	}
	_, st = h.call("GET", "/v1/domain", nil)
	if b, _ := json.Marshal(st["bare"]); string(b) != `[{"host":"box.test","project":"shop"}]` {
		t.Errorf("bare once claimed: %s", b)
	}
	if !strings.Contains(st["summary"].(string), "box.test itself is served by project shop.") {
		t.Errorf("summary: %s", st["summary"])
	}
	// A managed name (not an apex) gets no CNAME to itself.
	h.p.Domain = "acme.box.test"
	h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "acme.box.test", "app": "api"})
	if d := h.domain("shop", "acme.box.test"); d == nil || d["alternative"] != nil {
		t.Errorf("managed name: %v", d)
	}
	// Removing the claim brings the redirect back.
	h.p.Domain = "box.test"
	h.confirmed("POST", "/v1/projects/shop/domains/box.test/remove", map[string]any{})
	_, st = h.call("GET", "/v1/domain", nil)
	if b, _ := json.Marshal(st["bare"]); string(b) != `[{"host":"box.test","redirectsTo":"https://dashboard.box.test/"}]` {
		t.Errorf("bare after remove: %s", b)
	}
}

func recordNamesOf(v any) []string {
	var out []string
	for _, r := range v.([]any) {
		out = append(out, r.(map[string]any)["name"].(string))
	}
	return out
}

// TestAppsDomain: the dashboard on the box domain, apps on a domain of
// their own (the vercel.com / vercel.app split).
func TestAppsDomain(t *testing.T) {
	h := newHarness(t)
	h.dns.AddZone("apps.test")
	code, chk := h.call("GET", "/v1/domain/check?domain=example.test&appsDomain=apps.test", nil)
	if code != 200 || chk["ok"] != false || chk["appsDomain"] != "apps.test" ||
		!slices.Equal(recordNamesOf(chk["records"]), []string{"dashboard.example.test", "*.apps.test"}) {
		t.Fatalf("check: %d %v", code, chk)
	}
	code, out := h.call("POST", "/v1/domain", map[string]any{"domain": "example.test", "appsDomain": "apps.test"})
	if detail, _ := out["detail"].(string); code != 412 || len(out["errors"].([]any)) != 2 ||
		!strings.Contains(detail, "dashboard.example.test A "+boxIP) || !strings.Contains(detail, "*.apps.test A "+boxIP) {
		t.Fatalf("set without DNS: %d %v", code, out)
	}
	if code, _ := h.call("POST", "/v1/domain", map[string]any{"domain": "example.test", "appsDomain": "1-2-3-4.sslip.io"}); code != 422 {
		t.Errorf("sslip apps domain: %d", code)
	}
	// The box domain's apex is not needed when apps live elsewhere.
	h.dns.Set("dashboard.example.test", "A", boxIP)
	h.dns.Set("*.apps.test", "A", boxIP)
	_, chk = h.call("GET", "/v1/domain/check?domain=example.test&appsDomain=apps.test", nil)
	if chk["ok"] != true || !strings.Contains(chk["summary"].(string), "tiffin domain set example.test --apps-domain apps.test") {
		t.Fatalf("check with DNS: %v", chk)
	}
	code, out = h.call("POST", "/v1/domain", map[string]any{"domain": "example.test", "appsDomain": "apps.test"})
	if code != 200 || out["appsDomain"] != "apps.test" || out["dashboard"] != "dashboard.example.test" || len(h.restarts) != 1 {
		t.Fatalf("set: %d %v", code, out)
	}
	saved, _ := platform.LoadBoxDomain(t.Context(), h.p.DB)
	if saved.Domain != "example.test" || saved.Apps != "apps.test" || saved.Previous != "box.test" || saved.PreviousApps != "" {
		t.Fatalf("saved: %+v", saved)
	}
	// After the restart: apps under apps.test, the dashboard under example.test.
	d, src, _ := platform.ChooseDomain("box.test", saved, h.p.Reach.PublicIPs)
	h.p.Domain, h.p.Reach.DomainSource, h.p.Reach.AppsDomain = d, src, saved.Apps
	h.p.PublicURL = "https://dashboard.example.test"
	if h.p.Host("web") != "web.apps.test" || h.p.DashboardHost() != "dashboard.example.test" {
		t.Fatalf("hosts: %s %s", h.p.Host("web"), h.p.DashboardHost())
	}
	if a := h.m.certState().Aliases; !slices.Equal(a, []string{"box.test"}) {
		t.Fatalf("aliases: %v", a)
	}
	_, st := h.call("GET", "/v1/domain", nil)
	if st["domain"] != "example.test" || st["appsDomain"] != "apps.test" || !slices.Equal(recordNamesOf(st["records"]), []string{"dashboard.example.test", "*.apps.test"}) ||
		!strings.Contains(st["summary"].(string), "<project>.apps.test") {
		t.Errorf("status: %v", st)
	}
	if code, _ := h.call("POST", "/v1/domain", map[string]any{"domain": "example.test", "appsDomain": "apps.test"}); code != 409 {
		t.Errorf("same again: %d", code)
	}
	// Project domains: the box's own names are refused, and a subdomain's
	// CNAME alternative targets the dashboard's host (the apex need not exist).
	h.project("shop")
	for _, own := range []string{"web.apps.test", "dashboard.example.test"} {
		if code, _ := h.call("POST", "/v1/projects/shop/domains", map[string]any{"domain": own, "app": "web"}); code != 422 {
			t.Errorf("project domain %s: %d", own, code)
		}
	}
	// The bare domains redirect to the dashboard; once the website claims
	// example.test, apps.test goes there instead.
	_, st = h.call("GET", "/v1/domain", nil)
	if b, _ := json.Marshal(st["bare"]); string(b) != `[{"host":"example.test","redirectsTo":"https://dashboard.example.test/"},{"host":"apps.test","redirectsTo":"https://dashboard.example.test/"}]` {
		t.Errorf("bare: %s", b)
	}
	h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "example.test", "app": "web"})
	_, st = h.call("GET", "/v1/domain", nil)
	if b, _ := json.Marshal(st["bare"]); string(b) != `[{"host":"example.test","project":"shop"},{"host":"apps.test","redirectsTo":"https://example.test/"}]` {
		t.Errorf("bare with example.test served: %s", b)
	}
	if !strings.Contains(st["summary"].(string), "apps.test itself sends visitors to https://example.test/ until an app uses it.") {
		t.Errorf("summary: %s", st["summary"])
	}
	out = h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "www.shop.test", "app": "web"})
	if alt, _ := json.Marshal(out["domain"].(map[string]any)["alternative"]); !strings.Contains(string(alt), "dashboard.example.test") {
		t.Errorf("CNAME alternative: %s", alt)
	}
	// Back to one domain: the apps domain's names keep working for a while,
	// and so does box.test, whose grace period has not run out yet.
	h.dns.Set("example.test", "A", boxIP)
	h.dns.Set("*.example.test", "A", boxIP)
	if code, out := h.call("POST", "/v1/domain", map[string]any{"domain": "example.test"}); code != 200 || out["appsDomain"] != "example.test" {
		t.Fatalf("back to one domain: %d %v", code, out)
	}
	saved, _ = platform.LoadBoxDomain(t.Context(), h.p.DB)
	if saved.Apps != "" || saved.Previous != "box.test" || saved.PreviousApps != "apps.test" {
		t.Fatalf("saved: %+v", saved)
	}
	h.p.Reach.AppsDomain = ""
	if a := h.m.certState().Aliases; !slices.Equal(a, []string{"box.test", "apps.test"}) {
		t.Fatalf("aliases: %v", a)
	}
	if _, st := h.call("GET", "/v1/domain", nil); st["previous"].(map[string]any)["domain"] != "box.test" || st["previous"].(map[string]any)["appsDomain"] != "apps.test" {
		t.Errorf("previous: %v", st["previous"])
	}
}

// TestAppsDomainCreateRecords: createRecords sets the records in zones the
// connected provider holds and names the rest to add by hand.
func TestAppsDomainCreateRecords(t *testing.T) {
	h := newHarness(t)
	if code, out := h.call("PUT", "/v1/dns/providers/fake", map[string]any{"token": "secret-token-123"}); code != 200 {
		t.Fatalf("connect: %d %v", code, out)
	}
	h.dns.AddZone("apps.test") // after connecting: the provider does not hold it
	_, chk := h.call("GET", "/v1/domain/check?domain=example.test&appsDomain=apps.test", nil)
	recs := chk["records"].([]any)
	if recs[0].(map[string]any)["managedBy"] != "fake" || recs[1].(map[string]any)["managedBy"] != nil || chk["managedBy"] != nil ||
		!strings.Contains(chk["summary"].(string), "add these by hand: *.apps.test A "+boxIP) {
		t.Fatalf("check: %v", chk)
	}
	code, out := h.call("POST", "/v1/domain", map[string]any{"domain": "example.test", "appsDomain": "apps.test", "createRecords": true})
	if hint, _ := out["hint"].(string); code != 412 || len(out["errors"].([]any)) != 1 || !strings.Contains(hint, "add them by hand: *.apps.test A "+boxIP) {
		t.Fatalf("partly held: %d %v", code, out)
	}
	if got := h.dns.Get("dashboard.example.test", "A"); !slices.Equal(got, []string{boxIP}) {
		t.Errorf("dashboard record: %v", got)
	}
	h.dns.Set("*.apps.test", "A", boxIP)
	if code, out := h.call("POST", "/v1/domain", map[string]any{"domain": "example.test", "appsDomain": "apps.test", "createRecords": true}); code != 200 {
		t.Fatalf("after adding by hand: %d %v", code, out)
	}
	// Both zones held: the box creates everything, and no apex record.
	if code, out := h.call("POST", "/v1/domain", map[string]any{"domain": "auto.test", "appsDomain": "shop.test", "createRecords": true}); code != 200 {
		t.Fatalf("both held: %d %v", code, out)
	}
	if !slices.Equal(h.dns.Get("dashboard.auto.test", "A"), []string{boxIP}) || !slices.Equal(h.dns.Get("*.shop.test", "A"), []string{boxIP}) || len(h.dns.Get("auto.test", "A")) != 0 {
		t.Errorf("records: %v %v %v", h.dns.Get("dashboard.auto.test", "A"), h.dns.Get("*.shop.test", "A"), h.dns.Get("auto.test", "A"))
	}
	// The wildcard certificate follows the apps domain's zone.
	h.p.Domain, h.p.Reach.AppsDomain = "auto.test", "shop.test"
	if w := h.m.certState().Wildcard; w == nil || w.Name != "fake" {
		t.Errorf("wildcard for *.shop.test: %+v", w)
	}
	h.p.Reach.AppsDomain = "apps.test"
	if w := h.m.certState().Wildcard; w != nil {
		t.Errorf("wildcard for a zone the provider does not hold: %+v", w)
	}
}

func TestDNSProvider(t *testing.T) {
	h := newHarness(t)
	if code, out := h.call("PUT", "/v1/dns/providers/fake", map[string]any{"token": "bad"}); code != 422 || !strings.Contains(out["detail"].(string), "invalid token") {
		t.Fatalf("bad token: %d %v", code, out)
	}
	if code, _ := h.call("PUT", "/v1/dns/providers/nope", map[string]any{"token": "x"}); code != 404 {
		t.Errorf("unknown provider: %d", code)
	}
	code, out := h.call("PUT", "/v1/dns/providers/fake", map[string]any{"token": "secret-token-123"})
	if code != 200 {
		t.Fatalf("connect: %d %v", code, out)
	}
	c := out["connected"].([]any)[0].(map[string]any)
	if c["boxDomain"] != true || len(c["zones"].([]any)) != 4 {
		t.Errorf("connected: %v", c)
	}
	// Credentials are sealed with the box key.
	raw, _, _ := h.p.DB.KVGet(t.Context(), ns, "provider/fake")
	if bytes.Contains(raw, []byte("secret-token-123")) {
		t.Fatal("the token is stored in the clear")
	}
	// It holds the box domain's zone: wildcard certificates by DNS-01.
	if w := h.m.certState().Wildcard; w == nil || w.Name != "fake" {
		t.Fatalf("wildcard: %+v", w)
	}
	// The box domain with records created for it.
	code, out = h.call("POST", "/v1/domain", map[string]any{"domain": "auto.test", "createRecords": true})
	if code != 200 {
		t.Fatalf("set with createRecords: %d %v", code, out)
	}
	if got := h.dns.Get("*.auto.test", "A"); !slices.Equal(got, []string{boxIP}) {
		t.Errorf("wildcard record: %v", got)
	}
	// A project domain with records created for it.
	h.project("shop")
	out = h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "shop.test", "app": "web", "www": true, "createRecords": true})
	if len(out["created"].([]any)) != 2 || out["domain"].(map[string]any)["state"] != StateIssuing {
		t.Errorf("add with createRecords: %v", out)
	}
	// Records for email, through the same provider (and Platform.DNS).
	code, out = h.call("PUT", "/v1/dns/records", map[string]any{"records": []map[string]any{{"type": "TXT", "name": "_dmarc.shop.test", "value": "v=DMARC1; p=none"}}})
	if code != 200 || !slices.Equal(h.dns.Get("_dmarc.shop.test", "TXT"), []string{"v=DMARC1; p=none"}) {
		t.Errorf("records set: %d %v", code, out)
	}
	if !h.p.DNS.Manages(t.Context(), "mail.shop.test") || h.p.DNS.Manages(t.Context(), "example.com") {
		t.Error("Manages")
	}
	if code, _ := h.call("PUT", "/v1/dns/records", map[string]any{"records": []map[string]any{{"type": "TXT", "name": "x.example.com", "value": "v"}}}); code != 412 {
		t.Errorf("a zone no provider holds: %d", code)
	}
	// After a restart the provider is still there (credentials unsealed).
	m2 := New()
	m2.p = h.p
	if err := m2.load(t.Context()); err != nil || m2.provs["fake"] == nil || m2.provs["fake"].prov == nil {
		t.Fatalf("reload: %v %+v", err, m2.provs)
	}
	if code, out := h.call("DELETE", "/v1/dns/providers/fake", nil); code != 200 || len(out["connected"].([]any)) != 0 {
		t.Errorf("disconnect: %d %v", code, out)
	}
	if h.m.certState().Wildcard != nil {
		t.Error("wildcard after disconnect")
	}
}

func TestScopes(t *testing.T) {
	h := newHarness(t)
	h.project("shop")
	_, k := h.call("POST", "/v1/tokens", map[string]any{"name": "shop-agent", "projects": []string{"shop"}, "access": "full"})
	key := k["secret"].(string)
	do := func(method, path string, body any) int {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, h.srv.URL+path, rd)
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if c := do("POST", "/v1/domain", map[string]any{"domain": "example.test"}); c != 403 {
		t.Errorf("a project key set the box domain: %d", c)
	}
	if c := do("PUT", "/v1/dns/providers/fake", map[string]any{"token": "x"}); c != 403 {
		t.Errorf("a project key connected DNS: %d", c)
	}
	if c := do("GET", "/v1/projects/shop/domains", nil); c != 200 {
		t.Errorf("own project domains: %d", c)
	}
	if c := do("POST", "/v1/projects/shop/domains", map[string]any{"domain": "example.test", "app": "web"}); c != 428 {
		t.Errorf("own project add: %d", c)
	}
	// createRecords writes through the box's DNS provider, box-wide: a
	// project key may not, not even for a domain already attached (an
	// unchanged manifest is an empty plan, which needs no confirm).
	if code, out := h.call("PUT", "/v1/dns/providers/fake", map[string]any{"token": "secret-token-123"}); code != 200 {
		t.Fatalf("connect: %d %v", code, out)
	}
	h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "shop.test", "app": "web"})
	for _, d := range []string{"shop.test", "other.test"} {
		if c := do("POST", "/v1/projects/shop/domains", map[string]any{"domain": d, "app": "web", "createRecords": true}); c != 403 {
			t.Errorf("a project key created records for %s: %d", d, c)
		}
		if got := h.dns.Get(d, "A"); len(got) != 0 {
			t.Errorf("records written for %s: %v", d, got)
		}
	}
}

func TestExplain(t *testing.T) {
	for in, want := range map[string]string{
		"urn:ietf:params:acme:error:rateLimited: too many certificates": "rate limit",
		"CAA record for example.com prevents issuance":                  "CAA",
		"Timeout during connect (likely firewall problem)":              "ports 80 and 443",
		"DNS problem: NXDOMAIN looking up A for example.com":            "DNS check failed",
	} {
		if got := explain(in); !strings.Contains(got, want) {
			t.Errorf("explain(%q) = %q", in, got)
		}
	}
}
