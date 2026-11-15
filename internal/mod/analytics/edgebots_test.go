package analytics

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/mod/observe/edgelog"
)

// Request headers as browsers send them for a page (trimmed to what the
// checks read).
var (
	chromeDoc = map[string]string{"Sec-Fetch-Dest": "document", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Site": "none",
		"Accept-Language": "en-US,en;q=0.9", "Accept-Encoding": "gzip, deflate, br, zstd"}
	safariDoc = map[string]string{"Sec-Fetch-Dest": "document", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Site": "none",
		"Accept-Language": "en-GB,en;q=0.9", "Accept-Encoding": "gzip, deflate, br"}
	firefoxDoc = map[string]string{"Sec-Fetch-Dest": "document", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Site": "none",
		"Accept-Language": "de,en-US;q=0.7,en;q=0.3", "Accept-Encoding": "gzip, deflate, br, zstd", "Te": "trailers"}
)

const (
	safariUA  = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_7 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.7 Mobile/15E148 Safari/604.1"
	firefoxUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0"
)

// edgeLine is an access log line at a given time, from ip, over proto
// (HTTPS unless plain).
func edgeLine(at time.Time, ip, host, uri, ua, proto string, plain bool, hdr map[string]string) []byte {
	h := map[string][]string{"User-Agent": {ua}}
	for k, v := range hdr {
		h[k] = []string{v}
	}
	req := map[string]any{"client_ip": ip, "remote_ip": ip, "proto": proto, "method": "GET", "host": host, "uri": uri, "headers": h}
	if !plain {
		req["tls"] = map[string]any{"version": 772, "proto": "h2", "server_name": host}
	}
	b, _ := json.Marshal(map[string]any{"level": "info", "ts": float64(at.UnixNano()) / 1e9, "logger": "http.log.access.access",
		"request": req, "duration": 0.01, "size": 100, "status": 200})
	return b
}

// Browsers speak HTTP/2 or 3 over HTTPS and write Accept-Encoding with a
// space after each comma; HTTP libraries dressed as browsers often do not.
func TestEdgeNotBrowser(t *testing.T) {
	at := time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)
	for name, c := range map[string]struct {
		ua, proto string
		plain     bool
		hdr       map[string]string
		bot       bool
	}{
		"Chrome over HTTP/2":                   {human(1), "HTTP/2.0", false, chromeDoc, false},
		"Chrome over HTTP/3":                   {human(1), "HTTP/3.0", false, chromeDoc, false},
		"Safari over HTTP/2":                   {safariUA, "HTTP/2.0", false, safariDoc, false},
		"Firefox over HTTP/3":                  {firefoxUA, "HTTP/3.0", false, firefoxDoc, false},
		"no Accept-Encoding":                   {human(1), "HTTP/2.0", false, doc, false},
		"one encoding":                         {human(1), "HTTP/2.0", false, with(chromeDoc, "Accept-Encoding", "gzip"), false},
		"plain HTTP (a box without HTTPS)":     {human(1), "HTTP/1.1", true, chromeDoc, false},
		"HTTP/1.1 behind a CDN":                {human(1), "HTTP/1.1", false, with(chromeDoc, "Cf-Ray", "8f1e2d3c4b5a6978-AMS"), false},
		"HTTP/1.1 through a proxy":             {human(1), "HTTP/1.1", false, with(chromeDoc, "Via", "1.1 proxy.example"), false},
		"HTTP/1.1 over HTTPS":                  {human(1), "HTTP/1.1", false, chromeDoc, true},
		"HTTP/1.0 over HTTPS":                  {safariUA, "HTTP/1.0", false, safariDoc, true},
		"Accept-Encoding without spaces":       {human(1), "HTTP/2.0", false, with(chromeDoc, "Accept-Encoding", "zstd,gzip,deflate,br"), true},
		"Accept-Encoding of an older library":  {firefoxUA, "HTTP/2.0", false, with(firefoxDoc, "Accept-Encoding", "gzip,deflate"), true},
		"Accept-Encoding with a space missing": {human(1), "HTTP/2.0", false, with(chromeDoc, "Accept-Encoding", "gzip, deflate,br"), true},
	} {
		e, ok := edgelog.Parse(edgeLine(at, "203.0.113.7", "shop.box.test", "/", c.ua, c.proto, c.plain, c.hdr))
		if !ok || !EdgePageview(&e) {
			t.Fatalf("%s: not a page view", name)
		}
		if why := edgeNotBrowser(&e); (why != "") != c.bot {
			t.Errorf("%s: %q, want bot %v", name, why, c.bot)
		}
	}
}

// A click on a link of the site (Referer: a page of the project) from an
// address and browser that loaded nothing from the project is a crawler
// hopping between IP addresses. People who loaded a page (or only its
// files), on any app of the project, people arriving from elsewhere and
// people with cached files still count; for its first half hour the check
// is off (the history is too short to tell).
func TestEdgeLinkFromPageNeverLoaded(t *testing.T) {
	ctx := context.Background()
	m := accuracyFixture(t)
	t0 := time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)
	ref := func(u string) map[string]string {
		return with(chromeDoc, "Referer", u, "Sec-Fetch-Site", "same-origin")
	}
	type step struct {
		name      string
		at        time.Duration
		ip, ua    string
		host, uri string
		hdr       map[string]string
		counted   bool
	}
	ip := func(n int) string { return "203.0.113." + string(rune('0'+n)) }
	steps := []step{
		// The box just started: a crawler's click counts, for now.
		{"click while warming up", 0, ip(1), human(1), "shop.box.test", "/pricing", ref("https://shop.box.test/"), true},
		// Person 2 lands from a search, then clicks through 3 hours later.
		{"search visit", time.Minute, ip(2), human(2), "shop.box.test", "/", with(chromeDoc, "Referer", "https://www.google.com/", "Sec-Fetch-Site", "cross-site"), true},
		// Person 3's browser loads the page's files, its page came from the cache.
		{"files only", 2 * time.Minute, ip(3), human(3), "shop.box.test", "/app.css", map[string]string{"Sec-Fetch-Dest": "style"}, false},
		// Person 5 reads the docs app of the same project first.
		{"docs app", 3 * time.Minute, ip(5), safariUA, "docs-shop.box.test", "/guide", safariDoc, true},
		{"crawler click", 40 * time.Minute, ip(4), human(4), "shop.box.test", "/pricing", ref("https://shop.box.test/"), false},
		{"crawler click, another page", 41 * time.Minute, ip(6), human(6), "shop.box.test", "/blog", ref("https://shop.box.test/pricing"), false},
		{"crawler click, other app's page", 42 * time.Minute, ip(7), human(7), "shop.box.test", "/blog", ref("https://docs-shop.box.test/guide"), false},
		{"person clicks hours later", 3 * time.Hour, ip(2), human(2), "shop.box.test", "/pricing", ref("https://shop.box.test/"), true},
		{"person with a cached page clicks", 3 * time.Hour, ip(3), human(3), "shop.box.test", "/pricing", ref("https://shop.box.test/"), true},
		{"from the docs app to the shop", 3 * time.Hour, ip(5), safariUA, "shop.box.test", "/", with(safariDoc, "Referer", "https://docs-shop.box.test/"), true},
		{"returning person, typed address, cached files", 4 * time.Hour, ip(8), firefoxUA, "shop.box.test", "/", firefoxDoc, true},
		{"returning person from a search", 4 * time.Hour, ip(9), safariUA, "shop.box.test", "/pricing", with(safariDoc, "Referer", "https://www.google.com/"), true},
		{"link on another site", 4 * time.Hour, ip(1), human(9), "shop.box.test", "/", with(chromeDoc, "Referer", "https://news.example/item"), true},
		{"other project's page", 4 * time.Hour, ip(1), human(10), "shop.box.test", "/", with(chromeDoc, "Referer", "https://blog.box.test/"), true},
	}
	for _, s := range steps {
		before := m.pipe.Stats()
		m.handleEdge(ctx, edgeLine(t0.Add(s.at), s.ip, s.host, s.uri, s.ua, "HTTP/2.0", false, s.hdr))
		after := m.pipe.Stats()
		counted, dropped := after.Accepted-before.Accepted, after.Bots-before.Bots
		if s.hdr["Sec-Fetch-Dest"] != "document" {
			if counted+dropped != 0 {
				t.Errorf("%s: a file counted as a page view", s.name)
			}
			continue
		}
		if (counted == 1) != s.counted || counted+dropped != 1 {
			t.Errorf("%s: counted %d, dropped as a bot %d; want counted %v", s.name, counted, dropped, s.counted)
		}
	}
}

// The history remembers between half a window and a window, and turns
// early when full; then it is not sure for a while (the check is off).
func TestRecentWindow(t *testing.T) {
	var r recent
	t0 := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	if before, sure := r.seen("p", "203.0.113.1", "ua", t0); before || sure {
		t.Fatalf("first request: before %v sure %v", before, sure)
	}
	if before, sure := r.seen("p", "203.0.113.1", "ua", t0.Add(recentWarmup-time.Second)); !before && sure {
		t.Fatal("sure during warmup")
	}
	if before, sure := r.seen("p", "203.0.113.2", "ua", t0.Add(time.Hour)); before || !sure {
		t.Fatalf("new visitor after warmup: before %v sure %v", before, sure)
	}
	if before, _ := r.seen("q", "203.0.113.2", "ua", t0.Add(time.Hour)); before {
		t.Fatal("another project shares the history")
	}
	if before, sure := r.seen("p", "203.0.113.2", "ua", t0.Add(recentWindow-time.Minute)); !before || !sure {
		t.Fatalf("a visitor from %v ago forgotten", recentWindow-time.Hour)
	}
	// 203.0.113.1 last came at t0+29m; two turns later it is forgotten.
	r.seen("p", "203.0.113.3", "ua", t0.Add(recentWindow+time.Hour))
	if before, _ := r.seen("p", "203.0.113.1", "ua", t0.Add(2*recentWindow)); before {
		t.Fatal("remembered past two windows")
	}
	// A flood fills a generation: it turns early and the check is off
	// until the history covers the warmup again.
	at := t0.Add(3 * recentWindow)
	r.seen("p", "203.0.113.4", "ua", at)
	for i := 0; i < recentMax; i++ {
		r.seen("p", "flood", strconv.Itoa(i), at)
	}
	r.seen("p", "flood", "last", at)
	if _, sure := r.seen("p", "203.0.113.5", "ua", at.Add(time.Minute)); sure {
		t.Fatal("sure right after a flood turned the history early")
	}
}
