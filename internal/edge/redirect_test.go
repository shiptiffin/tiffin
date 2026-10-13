package edge

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/dnskit/dnstest"
)

// A primary domain's old addresses: the whole-host redirect takes the host
// (the app's own routes there are dropped), chains end in one hop, and
// other hosts (previews, deploy addresses) are left alone.
func TestHostRedirectConfig(t *testing.T) {
	c, err := Config{Domain: "box.test", Upstream: "127.0.0.1:7070", DataDir: "/x", Internal: true, HTTPSPort: 8443, Routes: []Route{
		{Host: "shop.box.test", Upstream: "127.0.0.1:1"},
		{Host: "shop.box.test", PathPrefix: "/api/auth", Upstream: "127.0.0.1:2"},
		{Host: "Shop.box.test", RedirectTo: "example.test"},
		{Host: "pr-1--shop.box.test", Upstream: "127.0.0.1:1"},
		{Host: "d-abc123--shop.box.test", Upstream: "127.0.0.1:1", Gate: true},
		{Host: "example.test", Upstream: "127.0.0.1:1"},
		{Host: "www.example.test", RedirectTo: "example.test"},
		{Host: "old.test", Upstream: "127.0.0.1:1"},
		{Host: "old.test", RedirectTo: "example.test"},
		{Host: "www.old.test", RedirectTo: "old.test"},
		{Host: "loop-a.test", RedirectTo: "loop-b.test"},
		{Host: "loop-b.test", RedirectTo: "loop-a.test"},
	}}.normalized()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, r := range c.Routes {
		v := r.Upstream
		if r.RedirectTo != "" {
			v = "→" + r.RedirectTo
		}
		got[r.Host] = append(got[r.Host], r.PathPrefix+v)
	}
	want := map[string]string{
		"shop.box.test":           "→example.test",
		"pr-1--shop.box.test":     "127.0.0.1:1",
		"d-abc123--shop.box.test": "127.0.0.1:1",
		"example.test":            "127.0.0.1:1",
		"www.example.test":        "→example.test",
		"old.test":                "→example.test",
		"www.old.test":            "→example.test", // one hop, not via old.test
		"loop-a.test":             "→loop-b.test",
		"loop-b.test":             "→loop-a.test",
	}
	for h, w := range want {
		if g := strings.Join(got[h], ","); g != w {
			t.Errorf("%s: %s, want %s", h, g, w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("hosts: %v", got)
	}

	// Plain HTTP: redirected hosts go straight to the target over HTTPS;
	// everything else to its own HTTPS address, as before.
	raw, err := ConfigJSON(Config{Domain: "box.test", Upstream: "127.0.0.1:7070", DataDir: "/x", Internal: true, HTTPSPort: 8443, Routes: []Route{
		{Host: "shop.box.test", RedirectTo: "example.test"},
		{Host: "example.test", Upstream: "127.0.0.1:1"},
		{Host: "www.example.test", RedirectTo: "example.test"},
		{Host: "docs.test", PathPrefix: "/old", RedirectTo: "example.test"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Apps struct {
			HTTP struct {
				Servers map[string]json.RawMessage `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	var httpServer struct {
		Routes []struct {
			Match  []map[string][]string `json:"match"`
			Handle []struct {
				Headers map[string][]string `json:"headers"`
			} `json:"handle"`
		} `json:"routes"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(doc.Apps.HTTP.Servers["http"], &httpServer); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, r := range httpServer.Routes {
		host := "*"
		if len(r.Match) > 0 {
			host = strings.Join(r.Match[0]["host"], ",")
		}
		lines = append(lines, host+" "+r.Handle[0].Headers["Location"][0])
	}
	wantHTTP := []string{
		"shop.box.test https://example.test:8443{http.request.uri}",
		"www.example.test https://example.test:8443{http.request.uri}",
		"* https://{http.request.host}:8443{http.request.uri}",
	}
	if strings.Join(lines, "\n") != strings.Join(wantHTTP, "\n") {
		t.Errorf("http server routes:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(wantHTTP, "\n"))
	}
}

// httpClient sends every request to the edge's plain-HTTP port and never
// follows redirects.
func httpClient(t *testing.T, port int) *http.Client {
	t.Helper()
	tr := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, "127.0.0.1:"+strconv.Itoa(port))
	}}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// The redirects as a running edge serves them, over HTTPS and plain HTTP.
func TestHostRedirectServed(t *testing.T) {
	isolate(t)
	up := upstreamServer(t, "platform")
	app := upstreamServer(t, "shop")
	cfg := testConfig(t, addr(up))
	cfg.Routes = []Route{
		{Host: "shop.tiffin.localhost", Upstream: addr(app)},
		{Host: "shop.tiffin.localhost", RedirectTo: "new.tiffin.localhost"},
		{Host: "new.tiffin.localhost", Upstream: addr(app)},
		{Host: "pr-1--shop.tiffin.localhost", Upstream: addr(app)},
		{Host: "www.new.tiffin.localhost", RedirectTo: "new.tiffin.localhost"},
	}
	e, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Stop()
	pemRoot, err := e.RootCAPEM()
	if err != nil {
		t.Fatal(err)
	}
	c := client(t, pemRoot, cfg.HTTPSPort)
	hc := httpClient(t, cfg.HTTPPort)
	port := ":" + strconv.Itoa(cfg.HTTPSPort)
	for _, tc := range []struct {
		c        *http.Client
		url      string
		code     int
		location string
		body     string
	}{
		{c, "https://shop.tiffin.localhost" + port + "/a/b?c=1&d=2", 308, "https://new.tiffin.localhost" + port + "/a/b?c=1&d=2", ""},
		{c, "https://new.tiffin.localhost" + port + "/x", 200, "", "hello from shop /x"},
		{c, "https://pr-1--shop.tiffin.localhost" + port + "/x", 200, "", "hello from shop /x"},
		{c, "https://dashboard.tiffin.localhost" + port + "/", 200, "", "hello from platform /"},
		// Plain HTTP: one hop to the target, path and query kept.
		{hc, "http://shop.tiffin.localhost/p?q=1", 308, "https://new.tiffin.localhost" + port + "/p?q=1", ""},
		{hc, "http://www.new.tiffin.localhost/p?q=1", 308, "https://new.tiffin.localhost" + port + "/p?q=1", ""},
		{hc, "http://pr-1--shop.tiffin.localhost/p", 308, "https://pr-1--shop.tiffin.localhost" + port + "/p", ""},
	} {
		res, body := get(t, tc.c, tc.url)
		if res.StatusCode != tc.code || res.Header.Get("Location") != tc.location || (tc.body != "" && body != tc.body) {
			t.Errorf("%s: %d %q %q, want %d %q %q", tc.url, res.StatusCode, res.Header.Get("Location"), body, tc.code, tc.location, tc.body)
		}
	}
}

// TestACMEHTTP01OnRedirectedHosts: hosts that only redirect still get
// their certificates through HTTP-01. The CA cannot reach TLS-ALPN here
// (its port goes nowhere), so every certificate below comes through plain
// HTTP, where these hosts answer every other request with a redirect.
func TestACMEHTTP01OnRedirectedHosts(t *testing.T) {
	isolate(t)
	dns := dnstest.Start(t)
	dns.AddZone("box.test")
	dns.AddZone("new.test")
	dns.AddZone("old.test")
	for _, h := range []string{"box.test", "*.box.test", "new.test", "www.new.test", "old.test", "www.old.test"} {
		dns.Set(h, "A", "127.0.0.1")
	}
	ports := freePorts(t, 3) // http, https, and a TLS-ALPN port nothing listens on
	pb := startPebble(t, dns.Addr(), ports[0], ports[2], 0)
	platform := upstreamServer(t, "platform")
	app := upstreamServer(t, "app")
	ready := []string{"new.test", "www.new.test", "old.test", "www.old.test"}
	cfg := Config{
		Domain: "box.test", Upstream: addr(platform), DataDir: filepath.Join(t.TempDir(), "caddy"),
		HTTPPort: ports[0], HTTPSPort: ports[1],
		ACME: &ACME{CA: pb.dir, TrustedRoots: pb.roots, Ready: ready},
		Routes: []Route{
			{Host: "new.test", Upstream: addr(app)},
			{Host: "www.new.test", RedirectTo: "new.test"},
			{Host: "old.test", Upstream: addr(app)},
			{Host: "old.test", RedirectTo: "new.test"},
			{Host: "www.old.test", RedirectTo: "old.test"},
			{Host: "shop.box.test", Upstream: addr(app)},
			{Host: "shop.box.test", RedirectTo: "new.test"},
		},
	}
	e, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Stop()
	for _, h := range ready {
		waitCert(t, h, "live", 60*time.Second)
	}
	c := acmeClient(t, pb.pool, ports[1], ports[0])
	httpsPort := ":" + strconv.Itoa(ports[1])
	// On demand, for a box name that only redirects: its first visit.
	res, _ := get(t, c, "https://shop.box.test/a?b=1")
	if want := "https://new.test" + httpsPort + "/a?b=1"; res.StatusCode != 308 || res.Header.Get("Location") != want {
		t.Errorf("shop.box.test: %d %q, want 308 %q", res.StatusCode, res.Header.Get("Location"), want)
	}
	waitCert(t, "shop.box.test", "live", 5*time.Second)
	for _, h := range []string{"old.test", "www.old.test", "shop.box.test"} {
		res, _ := get(t, c, "http://"+h+":"+strconv.Itoa(ports[0])+"/p?q=1")
		if want := "https://new.test" + httpsPort + "/p?q=1"; res.StatusCode != 308 || res.Header.Get("Location") != want {
			t.Errorf("http://%s: %d %q, want 308 %q", h, res.StatusCode, res.Header.Get("Location"), want)
		}
	}
	res, body := get(t, c, "https://new.test/")
	if res.StatusCode != 200 || !strings.Contains(body, "hello from app") {
		t.Errorf("new.test: %d %s", res.StatusCode, body)
	}
}
