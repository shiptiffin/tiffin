package search

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/edge"
	"github.com/shiptiffin/tiffin/internal/mod/runtime"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/state"
)

// site is a fake box edge: each host's files, and IndexNow at /indexnow.
type site struct {
	mu     sync.Mutex
	files  map[string]string // host+path → body
	pings  []map[string]any
	code   int          // IndexNow's answer
	google http.Handler // google.test, when set
}

func (s *site) set(host, path, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if body == "" {
		delete(s.files, host+path)
		return
	}
	s.files[host+path] = body
}

func (s *site) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.Host, "google.test") && s.google != nil {
		s.google.ServeHTTP(w, r)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	host, _, _ := strings.Cut(r.Host, ":")
	if host == "indexnow.test" && r.Method == http.MethodPost {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.pings = append(s.pings, body)
		w.WriteHeader(s.code)
		return
	}
	body, ok := s.files[host+r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, _ = io.WriteString(w, body)
}

func (s *site) sent() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out [][]string
	for _, p := range s.pings {
		var urls []string
		for _, u := range p["urlList"].([]any) {
			urls = append(urls, u.(string))
		}
		out = append(out, urls)
	}
	return out
}

func sitemap(pages map[string]string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
	keys := make([]string, 0, len(pages))
	for k := range pages {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "<url><loc>%s</loc><lastmod>%s</lastmod></url>", k, pages[k])
	}
	b.WriteString("</urlset>")
	return b.String()
}

type harness struct {
	m    *Module
	s    *site
	now  time.Time
	live []runtime.LiveHost
	rts  []edge.Route
	ctx  context.Context
	t    *testing.T
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := &site{files: map[string]string{}, code: 200}
	srv := httptest.NewTLSServer(s)
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	h := &harness{s: s, now: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC), ctx: context.Background(), t: t}
	m := New()
	m.now = func() time.Time { return h.now }
	m.client = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		DialContext: func(ctx context.Context, n, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, n, addr)
		},
	}}
	m.endpoint = "https://indexnow.test/indexnow"
	m.liveHosts = func(context.Context) ([]runtime.LiveHost, error) { return h.live, nil }
	m.routes = func() []edge.Route { return h.rts }
	m.domainLive = func(host string) bool { return host != "pending.example" }
	m.acme = func() bool { return true }
	m.lookupTXT = func(_ context.Context, name string) ([]string, error) {
		if name == "example" {
			return nil, fmt.Errorf("no such host")
		}
		if name == "shop.example" {
			return []string{"v=spf1 -all", "google-site-verification=abc"}, nil
		}
		return nil, nil
	}
	p := &platform.Platform{DB: db, Domain: "box.example.net"}
	if err := m.init(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	h.m = m
	return h
}

// serve makes host a production host of project shop, deployed as dep.
func (h *harness) serve(host, dep string) {
	for i, l := range h.live {
		if l.Host == host {
			h.live[i].Deploys = dep
			return
		}
	}
	h.live = append(h.live, runtime.LiveHost{Host: host, Project: "shop", App: "web", Deploys: dep})
	h.rts = append(h.rts, edge.Route{Host: host, Upstream: "127.0.0.1:1", NoIndex: platform.HiddenFromSearch(host)})
}

func (h *harness) pass() {
	h.t.Helper()
	h.m.pass(h.ctx)
}

func (h *harness) status() Status {
	h.t.Helper()
	s, err := h.m.status(h.ctx, "shop")
	if err != nil {
		h.t.Fatal(err)
	}
	return s
}

func (h *harness) host(name string) HostStatus {
	h.t.Helper()
	for _, x := range h.status().Hosts {
		if x.Host == name {
			return x
		}
	}
	h.t.Fatalf("no host %s", name)
	return HostStatus{}
}

func TestDomainLiveThenDeploys(t *testing.T) {
	h := newHarness(t)
	key := h.m.key
	for _, host := range []string{"shop.example", "shop.shiptiffin.app"} {
		h.s.set(host, "/"+key+".txt", key)
		h.s.set(host, "/", "<html><title>Shop</title></html>")
	}
	pages := map[string]string{
		"https://shop.example/":        "2026-10-01",
		"https://shop.example/pricing": "2026-10-02",
		"https://shop.example/docs":    "2026-10-03",
		"https://elsewhere.example/x":  "2026-10-03",
	}
	h.s.set("shop.example", "/sitemap.xml", sitemap(pages))
	h.serve("shop.example", "d1")
	h.serve("shop.shiptiffin.app", "d1")

	// A new findable host: every page of the sitemap, on that host only.
	h.pass()
	got := h.s.sent()
	if len(got) != 1 || strings.Join(got[0], " ") != "https://shop.example/ https://shop.example/docs https://shop.example/pricing" {
		t.Fatalf("first ping %v", got)
	}
	hs := h.host("shop.example")
	if !hs.Checks.Google.OK {
		t.Fatalf("google %+v", hs.Checks.Google)
	}
	if !hs.Findable || hs.Last == nil || hs.Last.Reason != ReasonLive || hs.Last.Pages != 3 || !hs.Checks.Sitemap.OK || !hs.Checks.Key.OK || !hs.Checks.Robots.OK || !hs.Checks.Home.OK {
		t.Fatalf("status %+v checks %+v", hs, hs.Checks)
	}
	if hid := h.host("shop.shiptiffin.app"); hid.Findable || !strings.Contains(hid.Why, "shiptiffin.app") || hid.Fix == "" {
		t.Fatalf("hidden host %+v", hid)
	}

	// Nothing changed: nothing sent.
	h.now = h.now.Add(time.Minute)
	h.pass()
	if len(h.s.sent()) != 1 {
		t.Fatal("a pass with nothing new pinged")
	}

	// A deploy adds a page, dates another and removes one: sent after the
	// settle time, as one ping.
	delete(pages, "https://shop.example/docs")
	pages["https://shop.example/blog"] = "2026-10-24"
	pages["https://shop.example/pricing"] = "2026-10-24"
	h.s.set("shop.example", "/sitemap.xml", sitemap(pages))
	h.serve("shop.example", "d2")
	h.pass()
	if len(h.s.sent()) != 1 || h.host("shop.example").Pending == nil {
		t.Fatal("pinged before the deploy settled")
	}
	h.now = h.now.Add(time.Minute)
	h.serve("shop.example", "d3") // another deploy pushes the wait out
	h.pass()
	h.now = h.now.Add(Settle)
	h.pass()
	got = h.s.sent()
	if len(got) != 2 || strings.Join(got[1], " ") != "https://shop.example/blog https://shop.example/docs https://shop.example/pricing" {
		t.Fatalf("deploy ping %v", got)
	}
	if hs := h.host("shop.example"); hs.Last.Reason != ReasonDeploy || hs.Pending != nil || len(hs.History) != 1 {
		t.Fatalf("after deploy %+v", hs)
	}

	// The regular check finds nothing new; then one more page appears
	// without a deploy (a database page).
	h.now = h.now.Add(CheckEvery)
	h.pass()
	if len(h.s.sent()) != 2 {
		t.Fatal("the regular check resent unchanged pages")
	}
	pages["https://shop.example/s/123"] = "2026-10-24"
	h.s.set("shop.example", "/sitemap.xml", sitemap(pages))
	h.now = h.now.Add(CheckEvery)
	h.pass()
	if got := h.s.sent(); len(got) != 3 || strings.Join(got[2], " ") != "https://shop.example/s/123" {
		t.Fatalf("check ping %v", got)
	}
}

func TestPingRefusedKeepsSnapshot(t *testing.T) {
	h := newHarness(t)
	h.s.set("shop.example", "/sitemap.xml", sitemap(map[string]string{"https://shop.example/": "2026-10-01"}))
	h.s.code = 429
	h.serve("shop.example", "d1")
	h.pass()
	hs := h.host("shop.example")
	if hs.Last == nil || hs.Last.OK || hs.Last.Status != 429 || !strings.Contains(hs.Last.Detail, "spam") {
		t.Fatalf("refused ping %+v", hs.Last)
	}
	// The next check sends the same pages again: they were never taken.
	h.s.code = 202
	h.now = h.now.Add(CheckEvery)
	h.pass()
	if got := h.s.sent(); len(got) != 2 || len(got[1]) != 1 {
		t.Fatalf("retry %v", got)
	}
	if hs := h.host("shop.example"); !hs.Last.OK || !strings.Contains(hs.Last.Detail, "key file") {
		t.Fatalf("accepted %+v", hs.Last)
	}
}

func TestNotFindable(t *testing.T) {
	h := newHarness(t)
	h.serve("pending.example", "d1")
	h.serve("old.example", "d1")
	h.rts = append(h.rts, edge.Route{Host: "old.example", RedirectTo: "shop.example"})
	h.serve("shop.box.example.net", "d1") // the box's own (set) domain
	h.s.set("shop.box.example.net", "/robots.txt", "User-agent: *\nDisallow: /\n")
	h.pass()
	if p := h.host("pending.example"); p.Findable || !strings.Contains(p.Why, "not live") {
		t.Fatalf("pending %+v", p)
	}
	if o := h.host("old.example"); o.Findable || !strings.Contains(o.Why, "redirects") {
		t.Fatalf("redirect %+v", o)
	}
	b := h.host("shop.box.example.net")
	if !b.Findable || b.Checks.Robots.OK || !strings.Contains(b.Checks.Robots.Detail, "Google, Bing") || b.Checks.Sitemap.OK || b.Checks.Google.OK {
		t.Fatalf("box host %+v %+v", b, b.Checks)
	}
	// No sitemap: the home page, once.
	if got := h.s.sent(); len(got) != 1 || got[0][0] != "https://shop.box.example.net/" {
		t.Fatalf("no-sitemap ping %v", got)
	}
}

func TestSitemapIndexAndSameDates(t *testing.T) {
	h := newHarness(t)
	h.s.set("shop.example", "/robots.txt", "User-agent: *\nAllow: /\nSitemap: https://shop.example/sitemap-index.xml\n")
	h.s.set("shop.example", "/sitemap-index.xml", `<sitemapindex><sitemap><loc>https://shop.example/a.xml</loc></sitemap><sitemap><loc>https://evil.example/b.xml</loc></sitemap></sitemapindex>`)
	built := "2026-10-24T12:00:00Z"
	pages := map[string]string{"https://shop.example/": built, "https://shop.example/1": built, "https://shop.example/2": built}
	h.s.set("shop.example", "/a.xml", sitemap(pages))
	h.serve("shop.example", "d1")
	h.pass()
	hs := h.host("shop.example")
	if hs.Checks.Sitemap.Pages != 3 || !hs.Checks.Sitemap.SameDates || hs.Checks.Sitemap.URL != "https://shop.example/sitemap-index.xml" {
		t.Fatalf("sitemap %+v", hs.Checks.Sitemap)
	}
	// Every date moves with the build: only the new page is sent.
	built = "2026-10-25T12:00:00Z"
	pages = map[string]string{"https://shop.example/": built, "https://shop.example/1": built, "https://shop.example/2": built, "https://shop.example/3": built}
	h.s.set("shop.example", "/a.xml", sitemap(pages))
	h.serve("shop.example", "d2")
	h.pass()
	h.now = h.now.Add(Settle)
	h.pass()
	if got := h.s.sent(); len(got) != 2 || strings.Join(got[1], " ") != "https://shop.example/3" {
		t.Fatalf("same-dates ping %v", got)
	}
}

func TestSettingsAndManual(t *testing.T) {
	h := newHarness(t)
	h.s.set("shop.example", "/sitemap.xml", sitemap(map[string]string{"https://shop.example/": "2026-10-01"}))
	h.serve("shop.example", "d1")
	h.serve("staging.example", "d1")
	// The whole project hidden: no address findable, nothing sent, and the
	// edge is told to say noindex on them.
	if err := h.m.saveSettings(h.ctx, "shop", Settings{Hidden: true}); err != nil {
		t.Fatal(err)
	}
	h.pass()
	if hs := h.host("shop.example"); hs.Findable || !hs.Hidden || len(h.s.sent()) != 0 {
		t.Fatalf("hidden project %+v sent %v", hs, h.s.sent())
	}
	if hh := h.m.HiddenHosts(h.ctx); !hh["shop.example"] || !hh["staging.example"] {
		t.Fatalf("hidden hosts %v", hh)
	}
	// One address hidden, the other findable.
	_ = h.m.saveSettings(h.ctx, "shop", Settings{HiddenHosts: []string{"staging.example"}})
	h.pass()
	if st := h.host("staging.example"); st.Findable || !st.Hidden || !strings.Contains(st.Why, "kept it out") {
		t.Fatalf("hidden host %+v", st)
	}
	if hh := h.m.HiddenHosts(h.ctx); hh["shop.example"] || !hh["staging.example"] {
		t.Fatalf("hidden hosts %v", hh)
	}
	if got := h.s.sent(); len(got) != 1 || got[0][0] != "https://shop.example/" {
		t.Fatalf("shown again: %v", got)
	}
	if _, err := h.m.check(h.ctx, "shop.example", pingOpts{reason: ReasonManual, urls: []string{"https://shop.example/x"}}); err != nil {
		t.Fatal(err)
	}
	if got := h.s.sent(); len(got) != 2 || got[1][0] != "https://shop.example/x" {
		t.Fatalf("manual %v", got)
	}
	if hs := h.host("shop.example"); hs.Last.Reason != ReasonManual {
		t.Fatalf("manual reason %+v", hs.Last)
	}
	// A restart: the edge's routes are not loaded yet. Nothing is forgotten.
	sent := len(h.s.sent())
	h.rts = nil
	h.pass()
	if r, _ := h.m.record(h.ctx, "shop.example"); r == nil || !r.Gone.IsZero() {
		t.Fatalf("a pass before routes loaded touched the record: %+v", r)
	}
	// Briefly not findable (the app stopped): kept, and no full resend when it is back.
	live := h.live
	h.live, h.rts = nil, []edge.Route{}
	h.pass()
	if r, _ := h.m.record(h.ctx, "shop.example"); r == nil || r.Gone.IsZero() {
		t.Fatal("record dropped at once")
	}
	h.live = live
	h.rts = []edge.Route{{Host: "shop.example", Upstream: "127.0.0.1:1"}}
	h.now = h.now.Add(time.Hour)
	h.pass()
	if len(h.s.sent()) != sent {
		t.Fatalf("pages resent after a short absence: %v", h.s.sent()[sent:])
	}
	// Gone for a day: forgotten.
	h.live, h.rts = nil, []edge.Route{}
	h.pass()
	h.now = h.now.Add(ForgetAfter)
	h.pass()
	if r, _ := h.m.record(h.ctx, "shop.example"); r != nil {
		t.Fatal("record of a host gone for a day")
	}
}

func TestBlocksAll(t *testing.T) {
	for txt, want := range map[string]string{
		"User-agent: *\nDisallow: /":                                    "Google,Bing",
		"User-agent: *\nDisallow: /api/":                                "",
		"User-agent: *\nDisallow: /\n\nUser-agent: Googlebot\nAllow: /": "Bing",
		"User-agent: GPTBot\nDisallow: /":                               "",
		"User-agent: Bingbot\nUser-agent: Googlebot\nDisallow: /":       "Google,Bing",
		"User-agent: *\nDisallow: /\nAllow: /$":                         "",
	} {
		var sm []string
		if got := strings.Join(blocksAll(txt, &sm), ","); got != want {
			t.Errorf("%q: %q, want %q", txt, got, want)
		}
	}
}

func accessLine(at time.Time, ip, host, uri, ua string, status int) []byte {
	b, _ := json.Marshal(map[string]any{
		"ts":      float64(at.Unix()),
		"status":  status,
		"request": map[string]any{"client_ip": ip, "method": "GET", "host": host, "uri": uri, "headers": map[string][]string{"User-Agent": {ua}}},
	})
	return b
}

func TestCrawlerLog(t *testing.T) {
	h := newHarness(t)
	h.m.crawl.lists = parseLists(map[string][]string{listGoogle: {"66.249.64.0/19"}, listChatGPT: {"23.98.142.176/28"}})
	h.s.set("shop.example", "/sitemap.xml", sitemap(map[string]string{"https://shop.example/": "2026-10-01"}))
	h.serve("shop.example", "d1")
	h.serve("shop.shiptiffin.app", "d1")
	h.pass()
	gb := "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"
	at := h.now.Add(-time.Hour)
	for _, l := range [][]byte{
		accessLine(at, "66.249.66.1", "shop.example", "/", gb, 200),
		accessLine(at.Add(time.Minute), "66.249.66.1", "shop.example", "/pricing?x=1", gb, 200),
		accessLine(at.Add(2*time.Minute), "66.249.66.1", "shop.example", "/old", gb, 404),
		accessLine(at, "203.0.113.9", "shop.example", "/", gb, 200),                                                   // a fake Googlebot
		accessLine(at, "23.98.142.180", "shop.example", "/pricing", "Mozilla/5.0; compatible; ChatGPT-User/1.0", 200), // a chat fetched it
		accessLine(at, "66.249.66.1", "shop.shiptiffin.app", "/", gb, 200),                                            // not findable: not tracked
		accessLine(at, "198.51.100.1", "shop.example", "/", "Mozilla/5.0 Safari", 200),                                // a person
	} {
		h.m.crawlLine(h.ctx, l)
	}
	h.pass() // saves
	// A fresh module reads what was saved.
	h.m.crawl = &crawlLog{tracked: map[string]bool{}, recs: map[string]*crawlRecord{}, dirty: map[string]bool{}}
	hs := h.host("shop.example")
	if len(hs.Crawled) != 2 || hs.Fakes7d != 1 {
		t.Fatalf("crawled %+v fakes %d", hs.Crawled, hs.Fakes7d)
	}
	g := hs.Crawled[0] // the most recent first: Googlebot's 404 came last

	if g.Bot != "Googlebot" || g.Purpose != "search" || g.Hits7d != 3 || g.Pages7d != 3 || g.Errors7d != 1 || g.LastPath != "/old" || g.LastStatus != 404 {
		t.Fatalf("googlebot %+v", g)
	}
	if hid := h.host("shop.shiptiffin.app"); len(hid.Crawled) != 0 {
		t.Fatalf("hidden host crawled %+v", hid.Crawled)
	}
}
