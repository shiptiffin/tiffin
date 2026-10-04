package edge

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func freePorts(t *testing.T, n int) []int {
	t.Helper()
	var ls []net.Listener
	var ports []int
	for range n {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		ls = append(ls, l)
		ports = append(ports, l.Addr().(*net.TCPAddr).Port)
	}
	for _, l := range ls {
		l.Close()
	}
	return ports
}

func portFree(port int) bool {
	l, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

// isolate keeps Caddy's per-user files (instance id, autosave) out of $HOME.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
}

func testConfig(t *testing.T, upstream string) Config {
	t.Helper()
	ports := freePorts(t, 2)
	return Config{
		Domain:    "tiffin.localhost",
		Upstream:  upstream,
		DataDir:   filepath.Join(t.TempDir(), "caddy"),
		HTTPPort:  ports[0],
		HTTPSPort: ports[1],
		Internal:  true,
	}
}

func upstreamServer(t *testing.T, name string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", name)
		w.Header().Set("X-Seen-Proto", r.Header.Get("X-Forwarded-Proto"))
		w.Header().Set("X-Seen-Host", r.Host)
		w.Header().Set("X-Seen-Fwd-Host", r.Header.Get("X-Forwarded-Host"))
		w.Header().Set("X-Seen-Fwd-For", r.Header.Get("X-Forwarded-For"))
		if r.URL.Path == "/own-headers" {
			w.Header().Set("Content-Security-Policy", "default-src 'self'")
			w.Header().Set("Strict-Transport-Security", "max-age=1")
		}
		io.WriteString(w, "hello from "+name+" "+r.URL.Path)
	}))
	t.Cleanup(s.Close)
	return s
}

func addr(s *httptest.Server) string { return strings.TrimPrefix(s.URL, "http://") }

// client dials 127.0.0.1 for any host and trusts only caPEM.
func client(t *testing.T, caPEM []byte, port int) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("bad root CA PEM")
	}
	tr := &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: pool},
		ForceAttemptHTTP2: true,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, "127.0.0.1:"+strconv.Itoa(port))
		},
	}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{
		Transport: tr,
		Timeout:   5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func get(t *testing.T, c *http.Client, url string) (*http.Response, string) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestConfigJSONGolden(t *testing.T) {
	got, err := ConfigJSON(Config{
		Domain:    "tiffin.localhost",
		Upstream:  "127.0.0.1:7070",
		DataDir:   "/var/lib/tiffin/platform/caddy",
		Internal:  true,
		HTTPPort:  8080,
		HTTPSPort: 8443,
		Routes: []Route{
			{Host: "shop.tiffin.localhost", Upstream: "127.0.0.1:9001"},
			{Host: "Custom.Example.test", Upstream: "127.0.0.1:9002"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "config.golden.json")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, append(got, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(want)) != strings.TrimSpace(string(got)) {
		t.Fatalf("config JSON differs from %s (run UPDATE_GOLDEN=1 go test)\n%s", golden, got)
	}
}

func TestConfigValidation(t *testing.T) {
	ok := Config{Domain: "tiffin.localhost", Upstream: "127.0.0.1:7070", DataDir: "/x", Internal: true}
	for name, mut := range map[string]func(*Config){
		"no domain":     func(c *Config) { c.Domain = "" },
		"bad upstream":  func(c *Config) { c.Upstream = "nope" },
		"no datadir":    func(c *Config) { c.DataDir = "" },
		"same ports":    func(c *Config) { c.HTTPPort, c.HTTPSPort = 9000, 9000 },
		"dup route":     func(c *Config) { c.Routes = []Route{{Host: "a.x", Upstream: "h:1"}, {Host: "A.x", Upstream: "h:2"}} },
		"dashboard dup": func(c *Config) { c.Routes = []Route{{Host: "dashboard.tiffin.localhost", Upstream: "h:1"}} },
		"wildcard host": func(c *Config) { c.Routes = []Route{{Host: "*.x", Upstream: "h:1"}} },
	} {
		c := ok
		mut(&c)
		if _, err := ConfigJSON(c); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	c := ok
	c.Internal = false
	if _, err := ConfigJSON(c); err != ErrACMERequired {
		t.Errorf("Internal=false: got %v", err)
	}
	if _, err := ConfigJSON(ok); err != nil {
		t.Errorf("valid config: %v", err)
	}
}

func TestEdgeEndToEnd(t *testing.T) {
	isolate(t)
	up := upstreamServer(t, "platform")
	app := upstreamServer(t, "shop")
	cfg := testConfig(t, addr(up))

	e, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			e.Stop()
		}
	})

	if _, err := Start(context.Background(), cfg); err == nil {
		t.Fatal("second Start in one process must fail")
	}

	ca, err := e.RootCAPEM()
	if err != nil {
		t.Fatal(err)
	}
	c := client(t, ca, cfg.HTTPSPort)
	base := "https://dashboard.tiffin.localhost:" + strconv.Itoa(cfg.HTTPSPort)

	// Proxied dashboard request over HTTP/2 with forwarded headers.
	resp, body := get(t, c, base+"/api/ping")
	if resp.StatusCode != 200 || body != "hello from platform /api/ping" {
		t.Fatalf("proxy: %d %q", resp.StatusCode, body)
	}
	if resp.ProtoMajor != 2 {
		t.Errorf("want HTTP/2, got %s", resp.Proto)
	}
	if got := resp.Header.Get("X-Seen-Proto"); got != "https" {
		t.Errorf("X-Forwarded-Proto = %q", got)
	}
	if got := resp.Header.Get("X-Seen-Fwd-Host"); got != "dashboard.tiffin.localhost:"+strconv.Itoa(cfg.HTTPSPort) {
		t.Errorf("X-Forwarded-Host = %q", got)
	}
	if got := resp.Header.Get("X-Seen-Host"); !strings.HasPrefix(got, "dashboard.tiffin.localhost") {
		t.Errorf("upstream Host = %q (must be preserved)", got)
	}
	if resp.Header.Get("X-Seen-Fwd-For") == "" {
		t.Error("X-Forwarded-For missing")
	}
	for k, v := range map[string]string{
		"Strict-Transport-Security": "max-age=300",
		"X-Content-Type-Options":    "nosniff",
		"Referrer-Policy":           "strict-origin-when-cross-origin",
		"Content-Security-Policy":   "frame-ancestors 'none'",
		"X-Frame-Options":           "DENY",
	} {
		if got := resp.Header.Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}

	// Upstream headers: its own CSP is kept, HSTS is replaced not duplicated.
	resp, _ = get(t, c, base+"/own-headers")
	if got := resp.Header.Values("Content-Security-Policy"); len(got) != 1 || got[0] != "default-src 'self'" {
		t.Errorf("upstream CSP = %q", got)
	}
	if got := resp.Header.Values("Strict-Transport-Security"); len(got) != 1 || got[0] != "max-age=300" {
		t.Errorf("HSTS = %q", got)
	}

	// Unknown host: friendly 404 with security headers, not Caddy's default.
	resp, body = get(t, c, "https://nope.tiffin.localhost:"+strconv.Itoa(cfg.HTTPSPort)+"/")
	if resp.StatusCode != 404 || !strings.Contains(body, "Nothing here") {
		t.Fatalf("unknown host: %d %q", resp.StatusCode, body)
	}
	if resp.Header.Get("Strict-Transport-Security") == "" || resp.Header.Get("Content-Security-Policy") != "frame-ancestors 'none'" {
		t.Error("404 lacks security headers")
	}
	if got := resp.Header.Get("Server"); got != "" {
		t.Errorf("Server header leaked: %q", got)
	}
	// Only our CA may exist: Caddy's default "local" CA tries to install itself
	// into the OS trust store.
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "pki", "authorities", "local")); err == nil {
		t.Error("default local CA was created")
	}

	// HTTP redirects to HTTPS.
	hc := &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{DialContext: func(ctx context.Context, n, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, n, "127.0.0.1:"+strconv.Itoa(cfg.HTTPPort))
		}},
	}
	rr, err := hc.Get("http://dashboard.tiffin.localhost:" + strconv.Itoa(cfg.HTTPPort) + "/a/b?x=1")
	if err != nil {
		t.Fatal(err)
	}
	rr.Body.Close()
	if rr.StatusCode != 308 || rr.Header.Get("Location") != base+"/a/b?x=1" {
		t.Fatalf("redirect: %d %q", rr.StatusCode, rr.Header.Get("Location"))
	}

	// Reload with extra routes, one inside the wildcard and one outside.
	cfg.Routes = []Route{
		{Host: "shop.tiffin.localhost", Upstream: addr(app)},
		{Host: "shop.example.test", Upstream: addr(app)},
	}
	if err := e.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"shop.tiffin.localhost", "shop.example.test"} {
		resp, body = get(t, c, "https://"+h+":"+strconv.Itoa(cfg.HTTPSPort)+"/x")
		if resp.StatusCode != 200 || body != "hello from shop /x" {
			t.Fatalf("route %s: %d %q", h, resp.StatusCode, body)
		}
	}
	if _, body = get(t, c, base+"/y"); body != "hello from platform /y" {
		t.Fatalf("dashboard after reload: %q", body)
	}
	// The same CA still signs after reload.
	if ca2, _ := e.RootCAPEM(); string(ca2) != string(ca) {
		t.Error("root CA changed on reload")
	}

	// Stop frees both ports; a second Stop is harmless.
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	stopped = true
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []int{cfg.HTTPPort, cfg.HTTPSPort} {
		if !portFree(p) {
			t.Errorf("port %d still bound after Stop", p)
		}
	}
	if err := e.Reload(cfg); err == nil {
		t.Error("Reload after Stop must fail")
	}
}

func TestRootCAPersistsAcrossRestarts(t *testing.T) {
	isolate(t)
	up := upstreamServer(t, "platform")
	cfg := testConfig(t, addr(up))

	e, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	ca1, err := e.RootCAPEM()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	// Readable from disk while stopped, for a `tiffin trust` style command.
	if disk, err := RootCAPEM(cfg.DataDir); err != nil || string(disk) != string(ca1) {
		t.Fatalf("RootCAPEM(dir): %v", err)
	}

	// Fresh ports, same DataDir; cancelling the context stops the edge.
	ports := freePorts(t, 2)
	cfg.HTTPPort, cfg.HTTPSPort = ports[0], ports[1]
	ctx, cancel := context.WithCancel(context.Background())
	e, err = Start(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ca2, err := e.RootCAPEM()
	if err != nil {
		t.Fatal(err)
	}
	if string(ca1) != string(ca2) {
		t.Fatal("root CA changed across restarts")
	}
	// A client that trusts the first run's root accepts the second run's cert.
	resp, body := get(t, client(t, ca1, cfg.HTTPSPort), "https://dashboard.tiffin.localhost:"+strconv.Itoa(cfg.HTTPSPort)+"/")
	if resp.StatusCode != 200 || !strings.HasPrefix(body, "hello from platform") {
		t.Fatalf("after restart: %d %q", resp.StatusCode, body)
	}

	cancel()
	deadline := time.Now().Add(3 * time.Second)
	for !portFree(cfg.HTTPSPort) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !portFree(cfg.HTTPSPort) {
		t.Error("context cancel did not stop the edge")
	}
}

func TestStartRejectsPublicACME(t *testing.T) {
	cfg := Config{Domain: "example.com", Upstream: "127.0.0.1:1", DataDir: t.TempDir()}
	if _, err := Start(context.Background(), cfg); err != ErrACMERequired {
		t.Fatalf("got %v", err)
	}
}
