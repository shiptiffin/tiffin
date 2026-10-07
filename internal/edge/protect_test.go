package edge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
)

var testSecret = bytes.Repeat([]byte{7}, 32)

func TestProtectConfigGolden(t *testing.T) {
	got, err := ConfigJSON(Config{
		Domain:    "tiffin.localhost",
		Upstream:  "127.0.0.1:7070",
		DataDir:   "/var/lib/tiffin/platform/caddy",
		Internal:  true,
		HTTPPort:  8080,
		HTTPSPort: 8443,
		Routes:    []Route{{Host: "shop.tiffin.localhost", Upstream: "127.0.0.1:9001"}},
		Protect: &Protection{
			App:       Limit{Events: 300, Window: 10 * time.Second},
			Auth:      Limit{Events: 10, Window: time.Minute},
			Dashboard: Limit{Events: 1000, Window: 10 * time.Second},
			Challenge: &Challenge{Hosts: []string{"*"}, Secret: testSecret, Difficulty: 16},
			CrowdSec:  &CrowdSec{APIURL: "http://127.0.0.1:7422/", APIKey: "k"},
			WAF:       true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "protect.golden.json")
	if os.Getenv("UPDATE_GOLDEN") != "" {
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

func TestProtectValidation(t *testing.T) {
	base := Config{Domain: "tiffin.localhost", Upstream: "127.0.0.1:7070", DataDir: "/x", Internal: true}
	for name, p := range map[string]*Protection{
		"negative events": {App: Limit{Events: -1, Window: time.Second}},
		"tiny window":     {App: Limit{Events: 5, Window: time.Millisecond}},
		"short secret":    {Challenge: &Challenge{Hosts: []string{"*"}, Secret: []byte("x"), Difficulty: 16}},
		"easy":            {Challenge: &Challenge{Hosts: []string{"*"}, Secret: testSecret, Difficulty: 2}},
		"no hosts":        {Challenge: &Challenge{Secret: testSecret, Difficulty: 16}},
		"bad host":        {Challenge: &Challenge{Hosts: []string{"a b"}, Secret: testSecret, Difficulty: 16}},
		"no key":          {CrowdSec: &CrowdSec{APIURL: "http://x"}},
		"bad lapi url":    {CrowdSec: &CrowdSec{APIURL: "::not a url", APIKey: "k"}},
		"lapi no scheme":  {CrowdSec: &CrowdSec{APIURL: "127.0.0.1:7422", APIKey: "k"}},
		"bad path":        {AuthPaths: []string{"login"}},
	} {
		c := base
		c.Protect = p
		if _, err := ConfigJSON(c); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestChallengeTokens(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	h := &ChallengeHandler{Secret: strings.Repeat("ab", 32), Difficulty: 10}
	h.now = func() time.Time { return now }
	if err := h.Provision(contextNone()); err != nil {
		t.Fatal(err)
	}
	tok := h.newToken("shop.x", "bind1")
	if d, err := h.checkToken(tok, "shop.x", "bind1"); err != nil || d != 10 {
		t.Fatalf("fresh token: %d %v", d, err)
	}
	if _, err := h.checkToken(tok, "blog.x", "bind1"); err == nil {
		t.Error("token accepted on another host")
	}
	if _, err := h.checkToken(tok, "shop.x", "bind2"); err == nil {
		t.Error("token accepted for another client")
	}
	if _, err := h.checkToken(strings.Replace(tok, ".10.", ".8.", 1), "shop.x", "bind1"); err == nil {
		t.Error("tampered difficulty accepted")
	}
	now = now.Add(11 * time.Minute)
	if _, err := h.checkToken(tok, "shop.x", "bind1"); err == nil {
		t.Error("expired token accepted")
	}
	nonce := SolveChallenge(tok, 10)
	if LeadingZeroBits(tok, nonce) < 10 {
		t.Fatal("solver returned a bad nonce")
	}
	for in, want := range map[string]string{
		"": "/", "/a?b=1": "/a?b=1", "//evil.com": "/", "/\\evil.com": "/", "https://evil.com": "/",
		"/x\r\nSet-Cookie: a": "/", ChallengePath: "/",
	} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestChallengeScriptMatchesGo runs the page's JavaScript solver (node) and
// checks its nonce with the Go verifier.
func TestChallengeScriptMatchesGo(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := challengeHTML[strings.Index(challengeHTML, `"use strict";`):strings.LastIndex(challengeHTML, "})();")]
	for _, tc := range []struct {
		token string
		bits  int
	}{
		{"1.0123456789abcdef01234567.1800000000.16.0011223344556677.00112233445566778899aabbccddeeff", 14},
		{"short", 12},
		{strings.Repeat("a", 64), 10},
		{strings.Repeat("b", 119), 12},
	} {
		js := `var out;var el={style:{},textContent:""};var f={getAttribute:function(){return "` + fmt.Sprint(tc.bits) + `"},
elements:{token:{value:"` + tc.token + `"},nonce:{value:""}},submit:function(){out=f.elements.nonce.value;}};
var document={getElementById:function(id){return id==="f"?f:el},body:{}};
var setTimeout=function(fn){fn()};
(function(){` + script + `})();
console.log(out);`
		cmd := exec.Command(node, "-e", js)
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("node: %v\n%s", err, b)
		}
		nonce := strings.TrimSpace(string(b))
		if got := LeadingZeroBits(tc.token, nonce); got < tc.bits {
			t.Errorf("token %q: JS nonce %q has %d zero bits, want %d", tc.token, nonce, got, tc.bits)
		}
		if want := SolveChallenge(tc.token, tc.bits); want != nonce {
			t.Errorf("token %q: JS found %s, Go found %s (both search from 0)", tc.token, nonce, want)
		}
	}
}

// fakeLAPI is a minimal CrowdSec local API for the streaming bouncer.
type fakeLAPI struct {
	mu      sync.Mutex
	banned  map[string]bool
	sent    map[string]bool
	streams int
}

func (f *fakeLAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("X-Api-Key") != "bouncer-key" {
		w.WriteHeader(403)
		return
	}
	if r.URL.Path != "/v1/decisions/stream" {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, "{}")
		return
	}
	f.streams++
	if r.URL.Query().Get("startup") == "true" {
		f.sent = map[string]bool{}
	}
	type dec struct {
		Duration string `json:"duration"`
		ID       int    `json:"id"`
		Origin   string `json:"origin"`
		Scenario string `json:"scenario"`
		Scope    string `json:"scope"`
		Type     string `json:"type"`
		Value    string `json:"value"`
	}
	var add, del []dec
	for ip, on := range f.banned {
		d := dec{Duration: "1h", ID: 1, Origin: "cscli", Scenario: "test", Scope: "Ip", Type: "ban", Value: ip}
		if on && !f.sent[ip] {
			add = append(add, d)
			f.sent[ip] = true
		}
		if !on && f.sent[ip] {
			del = append(del, d)
			delete(f.sent, ip)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"new": add, "deleted": del})
}

func (f *fakeLAPI) set(ip string, on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.banned[ip] = on
}

func do(t *testing.T, c *http.Client, method, u string, hdr map[string]string, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, u, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, u, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

var tokenRE = regexp.MustCompile(`name="token" value="([0-9a-f.]+)"`)

func TestProtectEndToEnd(t *testing.T) {
	isolate(t)
	up := upstreamServer(t, "platform")
	shop := upstreamServer(t, "shop")
	blog := upstreamServer(t, "blog")
	lapi := &fakeLAPI{banned: map[string]bool{}}
	lapiSrv := httptest.NewServer(lapi)
	t.Cleanup(lapiSrv.Close)

	cfg := testConfig(t, addr(up))
	cfg.Routes = []Route{{Host: "shop.tiffin.localhost", Upstream: addr(shop)}, {Host: "blog.tiffin.localhost", Upstream: addr(blog)}}
	var mu sync.Mutex
	prot := &Protection{App: Limit{Events: 5, Window: 2 * time.Second}, Dashboard: Limit{Events: 1000, Window: 10 * time.Second}}
	SetProtectionSource(func() *Protection { mu.Lock(); defer mu.Unlock(); return prot })
	t.Cleanup(func() { SetProtectionSource(nil) })
	setProt := func(p *Protection) {
		t.Helper()
		mu.Lock()
		prot = p
		mu.Unlock()
		if err := edgeReload(t); err != nil {
			t.Fatal(err)
		}
		if _, err := ProtectionStatus(); err != nil {
			t.Fatalf("protection not applied: %v", err)
		}
	}

	e, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Stop() })
	testEdge = e
	testEdgeCfg = cfg
	pem, err := e.RootCAPEM()
	if err != nil {
		t.Fatal(err)
	}
	c := client(t, pem, cfg.HTTPSPort)
	base := func(host string) string { return fmt.Sprintf("https://%s:%d", host, cfg.HTTPSPort) }
	shopURL, dashURL := base("shop.tiffin.localhost"), base("dashboard.tiffin.localhost")

	t.Run("flood gets 429 then recovers", func(t *testing.T) {
		for i := range 5 {
			if r, _ := get(t, c, shopURL+"/"); r.StatusCode != 200 {
				t.Fatalf("request %d: %d", i+1, r.StatusCode)
			}
		}
		r, body := get(t, c, shopURL+"/")
		if r.StatusCode != 429 {
			t.Fatalf("6th request: %d, want 429", r.StatusCode)
		}
		if r.Header.Get("Retry-After") == "" {
			t.Error("429 without Retry-After")
		}
		if !strings.Contains(body, "Easy there") || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/html") {
			t.Errorf("429 page: %s %q", r.Header.Get("Content-Type"), body)
		}
		// API calls get JSON: asking for it (and not HTML), or sending it.
		for _, h := range []map[string]string{{"Accept": "application/json"}, {"Content-Type": "application/json", "Accept": "*/*"}} {
			r, body := do(t, c, "POST", shopURL+"/api/x", h, "{}")
			if r.StatusCode != 429 || r.Header.Get("Content-Type") != "application/json" || !strings.Contains(body, `"RATE_LIMITED"`) || r.Header.Get("Retry-After") == "" {
				t.Errorf("429 for %v: %d %s %q", h, r.StatusCode, r.Header.Get("Content-Type"), body)
			}
		}
		if r, body := do(t, c, "GET", shopURL+"/", map[string]string{"Accept": "text/html,application/json"}, ""); r.StatusCode != 429 || !strings.Contains(body, "Easy there") {
			t.Errorf("a browser gets the page: %d %q", r.StatusCode, body)
		}
		// The dashboard has its own, generous zone.
		if r, _ := get(t, c, dashURL+"/"); r.StatusCode != 200 {
			t.Errorf("dashboard during app flood: %d", r.StatusCode)
		}
		// So do fingerprinted build files: 10× the app limit.
		for i := range 5 * assetsFactor {
			if r, _ := get(t, c, shopURL+"/_next/static/chunks/main.js"); r.StatusCode != 200 {
				t.Fatalf("asset %d during app flood: %d", i+1, r.StatusCode)
			}
		}
		if r, _ := get(t, c, shopURL+"/_next/static/chunks/main.js"); r.StatusCode != 429 {
			t.Errorf("asset past its own limit: %d, want 429", r.StatusCode)
		}
		time.Sleep(2100 * time.Millisecond)
		if r, _ := get(t, c, shopURL+"/"); r.StatusCode != 200 {
			t.Errorf("after the window: %d, want 200", r.StatusCode)
		}
	})

	t.Run("auth endpoints are stricter", func(t *testing.T) {
		// (A zone of its own for this first part: a 2-minute window.)
		setProt(&Protection{App: Limit{Events: 1000, Window: time.Second}, Auth: Limit{Events: 3, Window: 2 * time.Minute}})
		// A Next-Action header lifts nothing: any app can be sent one, and
		// only Next.js reads it. Sign-in pages and the engine's endpoints
		// count every write.
		action := map[string]string{"Next-Action": "7f3a"}
		for i := range 3 {
			if r, _ := do(t, c, "POST", shopURL+"/sign-in", action, "[]"); r.StatusCode != 200 {
				t.Fatalf("POST %d to /sign-in: %d", i+1, r.StatusCode)
			}
		}
		if r, _ := do(t, c, "POST", shopURL+"/sign-in", action, "[]"); r.StatusCode != 429 {
			t.Fatalf("a Next-Action header lifted the sign-in limit: %d, want 429", r.StatusCode)
		}
		setProt(&Protection{App: Limit{Events: 1000, Window: time.Second}, Auth: Limit{Events: 3, Window: time.Minute}})
		for i := range 3 {
			if r, _ := do(t, c, "POST", shopURL+"/api/auth/sign-in/email", nil, "{}"); r.StatusCode != 200 {
				t.Fatalf("sign-in %d: %d", i+1, r.StatusCode)
			}
		}
		if r, _ := do(t, c, "POST", shopURL+"/api/auth/sign-in/email", nil, "{}"); r.StatusCode != 429 {
			t.Fatalf("4th sign-in: %d, want 429", r.StatusCode)
		}
		if r, _ := do(t, c, "POST", shopURL+"/api/auth/sign-in/email", action, "{}"); r.StatusCode != 429 {
			t.Errorf("a Next-Action header must not lift the engine's limit: %d", r.StatusCode)
		}
		if r, _ := do(t, c, "POST", shopURL+"/sign-in", nil, "{}"); r.StatusCode != 429 {
			t.Errorf("a plain POST to /sign-in still counts: %d", r.StatusCode)
		}
		if r, _ := get(t, c, shopURL+"/api/auth/get-session"); r.StatusCode != 200 {
			t.Errorf("GET get-session must not count as a sign-in: %d", r.StatusCode)
		}
		if r, _ := do(t, c, "POST", shopURL+"/api/orders", nil, "{}"); r.StatusCode != 200 {
			t.Errorf("POST outside auth paths: %d", r.StatusCode)
		}
		if r, _ := do(t, c, "POST", dashURL+"/api/auth/sign-in", nil, "{}"); r.StatusCode != 200 {
			t.Errorf("the dashboard is not an app: %d", r.StatusCode)
		}
	})

	t.Run("challenge", func(t *testing.T) {
		setProt(&Protection{Challenge: &Challenge{Hosts: []string{"shop.tiffin.localhost"}, Secret: testSecret, Difficulty: 12, Exempt: []string{"/api/v1/"}}})
		ua := map[string]string{"User-Agent": "test-browser/1.0", "Accept": "text/html"}
		r, body := do(t, c, "GET", shopURL+"/cart?x=1", ua, "")
		if r.StatusCode != 403 || r.Header.Get("X-Tiffin-Challenge") != "required" {
			t.Fatalf("no challenge: %d %v", r.StatusCode, r.Header)
		}
		csp := r.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "script-src 'nonce-") || strings.Contains(body, "http://") || strings.Contains(body, "https://") {
			t.Errorf("challenge page must be self-contained: csp=%q", csp)
		}
		if !strings.Contains(body, `role="status"`) || !strings.Contains(body, "prefers-reduced-motion") || !strings.Contains(body, `value="/cart?x=1"`) {
			t.Error("challenge page lacks status text, reduced motion or the return path")
		}
		m := tokenRE.FindStringSubmatch(body)
		if m == nil {
			t.Fatal("no token in the page")
		}
		tok := m[1]

		// Wrong proof: challenged again.
		form := func(nonce string) string {
			return url.Values{"token": {tok}, "nonce": {nonce}, "next": {"/cart?x=1"}}.Encode()
		}
		fh := map[string]string{"User-Agent": "test-browser/1.0", "Content-Type": "application/x-www-form-urlencoded"}
		bad := "0"
		for LeadingZeroBits(tok, bad) >= 12 {
			bad += "0"
		}
		if r, _ := do(t, c, "POST", shopURL+ChallengePath, fh, form(bad)); r.StatusCode != 403 {
			t.Errorf("wrong nonce: %d", r.StatusCode)
		}
		// Another client cannot use this token.
		other := map[string]string{"User-Agent": "other/1.0", "Content-Type": "application/x-www-form-urlencoded"}
		nonce := SolveChallenge(tok, 12)
		if r, _ := do(t, c, "POST", shopURL+ChallengePath, other, form(nonce)); r.StatusCode != 403 {
			t.Errorf("token from another UA: %d", r.StatusCode)
		}
		r, _ = do(t, c, "POST", shopURL+ChallengePath, fh, form(nonce))
		if r.StatusCode != 303 || r.Header.Get("Location") != "/cart?x=1" {
			t.Fatalf("solved: %d %q", r.StatusCode, r.Header.Get("Location"))
		}
		var cookie *http.Cookie
		for _, ck := range r.Cookies() {
			if ck.Name == clearanceName {
				cookie = ck
			}
		}
		if cookie == nil || !cookie.HttpOnly || !cookie.Secure || cookie.Path != "/" {
			t.Fatalf("clearance cookie: %+v", cookie)
		}
		withCookie := map[string]string{"User-Agent": "test-browser/1.0", "Cookie": clearanceName + "=" + cookie.Value}
		if r, body := do(t, c, "GET", shopURL+"/cart", withCookie, ""); r.StatusCode != 200 || !strings.Contains(body, "hello from shop") {
			t.Errorf("cleared request: %d %q", r.StatusCode, body)
		}
		// The cookie is bound to the UA and the host.
		if r, _ := do(t, c, "GET", shopURL+"/", map[string]string{"User-Agent": "else", "Cookie": withCookie["Cookie"]}, ""); r.StatusCode != 403 {
			t.Errorf("cookie with another UA: %d", r.StatusCode)
		}
		// No header exempts a request: a made-up bearer token is no API client.
		if r, _ := do(t, c, "GET", shopURL+"/cart", map[string]string{"Authorization": "Bearer made-up"}, ""); r.StatusCode != 403 {
			t.Errorf("a bearer header passed the challenge: %d", r.StatusCode)
		}
		// Exempt: the owner's API paths, /.well-known, robots.txt, preflights.
		for _, tc := range []struct {
			method, path string
			hdr          map[string]string
		}{
			{"GET", "/api/v1/data", nil},
			{"GET", "/.well-known/security.txt", nil},
			{"GET", "/robots.txt", nil},
			{"OPTIONS", "/api/data", nil},
		} {
			if r, _ := do(t, c, tc.method, shopURL+tc.path, tc.hdr, ""); r.StatusCode != 200 {
				t.Errorf("%s %s should pass: %d", tc.method, tc.path, r.StatusCode)
			}
		}
		// JSON clients get JSON.
		r, body = do(t, c, "GET", shopURL+"/api/x", map[string]string{"Accept": "application/json"}, "")
		if r.StatusCode != 403 || !strings.Contains(body, `"challenge_required"`) {
			t.Errorf("json client: %d %q", r.StatusCode, body)
		}
		// Hosts outside the list and the dashboard are untouched.
		if r, _ := get(t, c, base("blog.tiffin.localhost")+"/"); r.StatusCode != 200 {
			t.Errorf("blog: %d", r.StatusCode)
		}
		// Every app host, never the dashboard.
		setProt(&Protection{Challenge: &Challenge{Hosts: []string{"*"}, Secret: testSecret, Difficulty: 12}})
		if r, _ := get(t, c, base("blog.tiffin.localhost")+"/"); r.StatusCode != 403 {
			t.Errorf("blog under *: %d", r.StatusCode)
		}
		if r, _ := get(t, c, base("nothing.tiffin.localhost")+"/"); r.StatusCode != 403 {
			t.Errorf("unknown host under *: %d", r.StatusCode)
		}
		if r, _ := get(t, c, dashURL+"/"); r.StatusCode != 200 {
			t.Errorf("dashboard under *: %d", r.StatusCode)
		}
	})

	t.Run("waf", func(t *testing.T) {
		start := time.Now()
		setProt(&Protection{WAF: true})
		t.Logf("WAF load took %s", time.Since(start).Round(time.Millisecond))
		if r, _ := get(t, c, shopURL+"/search?q=shoes"); r.StatusCode != 200 {
			t.Errorf("ordinary request: %d", r.StatusCode)
		}
		long := "/" + strings.Repeat("a", 2000)
		if r, body := fetch(t, c, shopURL+long, "gzip"); r.StatusCode != 200 || r.Header.Get("Content-Encoding") != "gzip" || body != "hello from shop "+long {
			t.Errorf("compressed behind the WAF: %d %q, body of %d bytes", r.StatusCode, r.Header.Get("Content-Encoding"), len(body))
		}
		r, body := get(t, c, shopURL+"/search?q="+url.QueryEscape("<script>alert(1)</script>"))
		if r.StatusCode != 403 || !strings.Contains(body, "firewall") {
			t.Errorf("XSS probe: %d %q", r.StatusCode, body)
		}
		r, _ = get(t, c, shopURL+"/?id="+url.QueryEscape("1' OR '1'='1"))
		if r.StatusCode != 403 {
			t.Errorf("SQLi probe: %d", r.StatusCode)
		}
		if r, _ := get(t, c, dashURL+"/?q="+url.QueryEscape("<script>alert(1)</script>")); r.StatusCode != 200 {
			t.Errorf("WAF must not touch the dashboard: %d", r.StatusCode)
		}
	})

	t.Run("crowdsec decisions are enforced", func(t *testing.T) {
		setProt(&Protection{CrowdSec: &CrowdSec{APIURL: lapiSrv.URL + "/", APIKey: "bouncer-key", Every: time.Second}})
		if r, _ := get(t, c, shopURL+"/"); r.StatusCode != 200 {
			t.Fatalf("before ban: %d", r.StatusCode)
		}
		lapi.set("127.0.0.1", true)
		waitStatus(t, c, shopURL+"/", 403, 5*time.Second)
		r, body := get(t, c, dashURL+"/")
		if r.StatusCode != 403 || !strings.Contains(body, "blocked for now") {
			t.Errorf("banned IP on the dashboard: %d %q", r.StatusCode, body)
		}
		lapi.set("127.0.0.1", false)
		waitStatus(t, c, shopURL+"/", 200, 5*time.Second)
	})

	t.Run("a refused protection layer falls back to serving", func(t *testing.T) {
		mu.Lock()
		prot = &Protection{Challenge: &Challenge{Hosts: []string{"*"}, Secret: testSecret, Difficulty: 99}}
		mu.Unlock()
		if err := edgeReload(t); err != nil {
			t.Fatalf("reload must not fail: %v", err)
		}
		if p, err := ProtectionStatus(); err == nil || p != nil {
			t.Errorf("status: %v %v", p, err)
		}
		if r, _ := get(t, c, shopURL+"/"); r.StatusCode != 200 {
			t.Errorf("unprotected fallback serves: %d", r.StatusCode)
		}
	})
}

var (
	testEdge    *Edge
	testEdgeCfg Config
)

func edgeReload(t *testing.T) error {
	t.Helper()
	return testEdge.Reload(testEdgeCfg)
}

func waitStatus(t *testing.T, c *http.Client, u string, want int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	var got int
	for time.Now().Before(deadline) {
		r, _ := get(t, c, u)
		if got = r.StatusCode; got == want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s: status %d, want %d", u, got, want)
}

func contextNone() caddy.Context { return caddy.Context{} }
