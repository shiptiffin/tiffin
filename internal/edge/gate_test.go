package edge

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func newGate(t *testing.T, now *time.Time) (*GateHandler, []byte) {
	t.Helper()
	key := []byte(strings.Repeat("k", 32))
	g := &GateHandler{Secret: hex.EncodeToString(key), SignIn: "https://dashboard.example.com/gate"}
	if err := g.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	g.now = func() time.Time { return *now }
	return g, key
}

// gateDo sends one request through the gate to an app that echoes the
// cookies it got.
func gateDo(g *GateHandler, method, host, target string, hdr map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.Host = host
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	_ = g.ServeHTTP(w, req, caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		fmt.Fprintf(w, "app cookies=%q", r.Header.Get("Cookie"))
		return nil
	}))
	return w
}

// The gate: no cookie → sign in on the dashboard and come back; a hand-off
// token works once, for its host, within its minute; the cookie it sets
// opens only that host, for an hour, and never reaches the app.
func TestGate(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	g, key := newGate(t, &now)
	host := "d-1a2b3c4d--shop.example.app"

	// Browsers go to sign in, with where they were going.
	w := gateDo(g, "GET", host, "/pricing?plan=pro", nil)
	loc, _ := url.Parse(w.Header().Get("Location"))
	if w.Code != http.StatusFound || loc.Host != "dashboard.example.com" || loc.Path != "/gate" ||
		loc.Query().Get("host") != host || loc.Query().Get("next") != "/pricing?plan=pro" {
		t.Fatalf("no cookie: %d %s", w.Code, w.Header().Get("Location"))
	}
	// API clients and writes get a 401 that says how.
	for _, c := range []struct{ method, accept string }{{"GET", "application/json"}, {"POST", "text/html"}} {
		w := gateDo(g, c.method, host, "/api/x", map[string]string{"Accept": c.accept})
		var body map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != http.StatusUnauthorized || body["code"] != "sign_in_required" || !strings.HasPrefix(body["signIn"], "https://dashboard.example.com/gate?") {
			t.Fatalf("%s %s: %d %s", c.method, c.accept, w.Code, w.Body)
		}
	}

	// A hand-off for another host, an expired one, a forged one: refused.
	for name, tok := range map[string]string{
		"other host": HandoffToken(key, "d-99999999--shop.example.app", now),
		"expired":    HandoffToken(key, host, now.Add(-2*time.Minute)),
		"forged":     HandoffToken([]byte(strings.Repeat("x", 32)), host, now),
		"garbage":    "1.2.3.4",
	} {
		if w := gateDo(g, "GET", host, GatePath+"?t="+url.QueryEscape(tok)+"&next=/", nil); w.Code != http.StatusForbidden || len(w.Result().Cookies()) != 0 {
			t.Fatalf("%s token: %d", name, w.Code)
		}
	}

	// A good one sets the cookie and goes on, on this host only.
	tok := HandoffToken(key, host, now)
	w = gateDo(g, "GET", host, GatePath+"?t="+url.QueryEscape(tok)+"&next="+url.QueryEscape("//evil.example/x"), nil)
	cs := w.Result().Cookies()
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" || len(cs) != 1 || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("hand-off: %d %v", w.Code, w.Header())
	}
	c := cs[0]
	if c.Name != "__Host-tiffin-deploy" || !c.Secure || !c.HttpOnly || c.Path != "/" || c.Domain != "" || c.SameSite != http.SameSiteLaxMode || c.MaxAge != 3600 {
		t.Fatalf("cookie %+v", c)
	}
	if w := gateDo(g, "GET", host, GatePath+"?t="+url.QueryEscape(tok), nil); w.Code != http.StatusForbidden {
		t.Fatalf("a token worked twice: %d", w.Code)
	}

	// The cookie lets requests through, without itself reaching the app.
	other := &http.Cookie{Name: "app_session", Value: "abc"}
	w = gateDo(g, "POST", host+":8443", "/api/x", nil, c, other)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "app_session=abc") || strings.Contains(w.Body.String(), "tiffin-deploy") {
		t.Fatalf("with the cookie: %d %s", w.Code, w.Body)
	}
	// Not on another deploy's host, and not after its hour.
	if w := gateDo(g, "GET", "d-99999999--shop.example.app", "/", nil, c); w.Code != http.StatusFound {
		t.Fatalf("cookie on another host: %d", w.Code)
	}
	now = now.Add(time.Hour + time.Second)
	if w := gateDo(g, "GET", host, "/", nil, c); w.Code != http.StatusFound {
		t.Fatalf("expired cookie: %d", w.Code)
	}
}

// Gated routes run the gate (and say noindex); without the gate's secret
// they answer 503 rather than open.
func TestGateConfig(t *testing.T) {
	cfg := Config{Domain: "example.com", Apps: "example.app", Upstream: "127.0.0.1:7070", DataDir: "/tmp/caddy", Internal: true,
		Routes: []Route{
			{Host: "d-1a2b3c4d--shop.example.app", Upstream: "127.0.0.1:9001", Gate: true, NoIndex: true},
			{Host: "shop.example.app", Upstream: "127.0.0.1:9001"},
		}}
	without, err := ConfigJSON(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(without), `"status_code": 503`) || strings.Contains(string(without), "tiffin_gate") {
		t.Fatal("a gated route without the gate's secret must refuse, not open")
	}
	cfg.Gate = &Gate{Secret: []byte(strings.Repeat("s", 32))}
	with, err := ConfigJSON(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := string(with)
	if strings.Count(s, `"handler": "tiffin_gate"`) != 1 || !strings.Contains(s, `"sign_in": "https://dashboard.example.com/gate"`) || !strings.Contains(s, `"X-Robots-Tag"`) {
		t.Fatalf("gated config:\n%s", s)
	}
	cfg.Gate = &Gate{Secret: []byte("short")}
	if _, err := ConfigJSON(cfg); err == nil {
		t.Fatal("a short gate secret was accepted")
	}
}
