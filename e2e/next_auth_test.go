//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestNextAuth deploys e2e/nextauth, a Next.js 16 app using
// tiffin-sdk/next/auth with plain (no JavaScript) forms, on two hosts:
//
//	proxy.ts redirects a signed-out visitor to /sign-in?next= → sign up
//	through a Server Action (its cookies reach the browser) → the dashboard
//	(verifySession) → requireRole: 200 in your org, 403 elsewhere, 403 for an
//	API key capped below the role → sign out clears the cookies → a stale
//	cookie gets past proxy.ts but not verifySession → sign in through a
//	Server Action → each host gets its own passkey rpID → Server Actions on
//	/sign-in don't count as sign-in attempts at the edge → the app's CSP
//	frame-ancestors replaces X-Frame-Options → getSession latency per path →
//	a preview: the production account signs in there (host-only cookies,
//	verifySession), a preview sign-up signs in on production, and the
//	preview's host is its passkey rpID.
func TestNextAuth(t *testing.T) {
	start := time.Now()
	phase := phaseLogger(t)
	b := newCLIBox(t, "nextauth", "nextauth")
	phase("up", start)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("app log:\n%s", b.inBox(`sudo sh -c 'tail -n 80 $(ls -t /var/lib/tiffin/logs/apps/nextauth/web/prod/*.log | head -1)' || true`))
		}
	})

	app := filepath.Join(b.dir, "nextauth")
	if out, err := exec.Command("rsync", "-a", filepath.Join(RepoRoot(), "e2e", "nextauth")+"/", app+"/").CombinedOutput(); err != nil {
		t.Fatalf("copy app: %v %s", err, out)
	}
	b.ok("sdk", "add", app)
	plan := b.ok("plan", app)
	hash, _ := plan["hash"].(string)
	b.ok("apply", app, "--confirm", hash, "-m", "e2e: next auth")
	b.waitReady("app/web", "service/postgres", "service/auth")
	p := time.Now()
	d := deployArgs(t, b, app)
	phase("deploy", p)
	t.Logf("deploy: build %.1fs, total %.1fs", d.BuildSecs, d.TotalSecs)

	site, site2 := b.url("nextauth"), b.url("nextauth-two")
	br := newBrowser(t, b, site)

	// ---- signed out: proxy.ts redirects, with ?next= ----
	res := br.do("GET", "/dashboard", nil, nil)
	if res.StatusCode != 307 || res.Header.Get("Location") != "/sign-in?next=%2Fdashboard" {
		t.Fatalf("signed-out /dashboard: %d %q", res.StatusCode, res.Header.Get("Location"))
	}

	// ---- sign up through a Server Action ----
	signInPage := br.body(br.do("GET", "/sign-in?next=%2Fdashboard", nil, nil))
	res = br.submit("/sign-in?next=%2Fdashboard", formByID(t, signInPage, "sign-up"), map[string]string{
		"email": "ana@example.com", "password": "correct horse battery", "captcha": br.captcha(),
	})
	if res.StatusCode != 303 || res.Header.Get("Location") != "/dashboard" || br.cookie("__Secure-tiffin.session_token") == "" || br.cookie("__Secure-tiffin.session_data") == "" {
		t.Fatalf("sign-up action: %d %q, cookies %v", res.StatusCode, res.Header.Get("Location"), br.jar)
	}
	dash := br.body(br.do("GET", "/dashboard", nil, nil))
	if !strings.Contains(dash, `id="who">ana@example.com<`) || !strings.Contains(dash, "Personal<!-- -->:<!-- -->owner") {
		t.Fatalf("dashboard:\n%s", head(dash))
	}
	var me struct {
		User         struct{ Email string }
		Organization struct{ ID, Name, Role string }
	}
	if err := json.Unmarshal([]byte(br.body(br.do("GET", "/api/me", nil, nil))), &me); err != nil || me.Organization.Role != "owner" {
		t.Fatalf("/api/me: %+v %v", me, err)
	}

	// ---- requireRole ----
	if res := br.do("GET", "/team/"+me.Organization.ID, nil, nil); res.StatusCode != 200 {
		t.Fatalf("own org page: %d", res.StatusCode)
	}
	if res := br.do("GET", "/team/someone-elses", nil, nil); res.StatusCode != 403 {
		t.Fatalf("another org's page: %d, want 403", res.StatusCode)
	}
	key := br.engineJSON("POST", "/api/auth/api-key/create", `{"name":"reporter","metadata":{"maxRole":"viewer"}}`)
	keyHdr := map[string]string{"X-Api-Key": fmt.Sprint(key["key"])}
	agent := newBrowser(t, b, site)
	if res := agent.do("GET", "/api/team?org="+me.Organization.ID, nil, keyHdr); res.StatusCode != 403 {
		t.Fatalf("viewer API key on a members-only route: %d, want 403", res.StatusCode)
	}
	if body := br.body(br.do("GET", "/api/team?org="+me.Organization.ID, nil, nil)); !strings.Contains(body, `"role":"owner"`) {
		t.Fatalf("owner on the members-only route: %s", body)
	}

	// ---- getSession latency inside the app ----
	res = br.do("GET", "/api/bench", nil, nil)
	bench := br.body(res)
	if res.StatusCode != 200 || !strings.Contains(bench, "signedCookieMs") {
		t.Fatalf("/api/bench: %d %s", res.StatusCode, bench)
	}
	t.Logf("getSession in the app: %s", bench)

	// ---- sign out ----
	stale := br.cookie("__Secure-tiffin.session_token")
	res = br.submit("/dashboard", formByID(t, dash, "sign-out"), nil)
	if res.StatusCode != 303 || br.cookie("__Secure-tiffin.session_token") != "" || br.cookie("__Secure-tiffin.session_data") != "" {
		t.Fatalf("sign-out: %d, cookies left %v", res.StatusCode, br.jar)
	}
	if res := br.do("GET", "/dashboard", nil, nil); res.StatusCode != 307 {
		t.Fatalf("/dashboard after signing out: %d", res.StatusCode)
	}
	// The old token alone gets past proxy.ts (it only looks), not verifySession.
	br.jar["__Secure-tiffin.session_token"] = stale
	res = br.do("GET", "/dashboard?tab=1", nil, nil)
	if res.StatusCode != 307 || res.Header.Get("Location") != "/sign-in?next=%2Fdashboard%3Ftab%3D1" {
		t.Fatalf("revoked session on /dashboard: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	delete(br.jar, "__Secure-tiffin.session_token")

	// ---- sign in through a Server Action ----
	res = br.submit("/sign-in", formByID(t, signInPage, "sign-in"), map[string]string{
		"email": "ana@example.com", "password": "wrong password", "captcha": br.captcha(),
	})
	if res.StatusCode != 303 || res.Header.Get("Location") != "/sign-in?error=INVALID_EMAIL_OR_PASSWORD" {
		t.Fatalf("wrong password: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	res = br.submit("/sign-in", formByID(t, signInPage, "sign-in"), map[string]string{
		"email": "ana@example.com", "password": "correct horse battery", "captcha": br.captcha(),
	})
	if res.StatusCode != 303 || res.Header.Get("Location") != "/dashboard" || br.cookie("__Secure-tiffin.session_data") == "" {
		t.Fatalf("sign-in action: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	if home := br.body(br.do("GET", "/", nil, nil)); !strings.Contains(home, "Hello Ana") {
		t.Fatalf("home after signing in: %s", head(home))
	}

	// ---- passkeys: each host is its own rpID ----
	for _, s := range []string{site, site2} {
		var o struct{ RpID string }
		_, _, raw := b.get(b.https(), "GET", s+"/api/auth/passkey/generate-authenticate-options", nil)
		u, _ := url.Parse(s)
		if json.Unmarshal([]byte(raw), &o); o.RpID != u.Hostname() {
			t.Fatalf("passkey rpID on %s: %q (%s)", s, o.RpID, raw)
		}
	}

	// ---- edge: Server Actions on /sign-in are not sign-in attempts ----
	for i := range 15 {
		if res := agent.do("POST", "/sign-in", strings.NewReader("[]"), map[string]string{"Next-Action": "0000", "Content-Type": "text/plain;charset=UTF-8"}); res.StatusCode == 429 {
			t.Fatalf("server action %d on /sign-in was rate limited as a sign-in", i+1)
		}
	}

	// ---- edge: the app's frame-ancestors wins over X-Frame-Options ----
	if res := br.do("GET", "/embed", nil, nil); res.Header.Get("X-Frame-Options") != "" || !strings.Contains(res.Header.Get("Content-Security-Policy"), "frame-ancestors https://partner.example") {
		t.Fatalf("/embed: X-Frame-Options %q, CSP %q", res.Header.Get("X-Frame-Options"), res.Header.Get("Content-Security-Policy"))
	}
	if res := br.do("GET", "/", nil, nil); res.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("/: X-Frame-Options %q, want DENY", res.Header.Get("X-Frame-Options"))
	}
	phase("checks", p)

	// ---- a preview: auth on its own host, on the project's users ----
	p = time.Now()
	pv := deployArgs(t, b, app, "--preview", "pr-1")
	pu, _ := url.Parse(pv.URL)
	if !strings.HasPrefix(pu.Hostname(), "pr-1--nextauth.") {
		t.Fatalf("preview URL %s", pv.URL)
	}
	waitBody(t, b, pv.URL+"/api/auth/tiffin/config", `"methods"`, 30*time.Second)
	pb := newBrowser(t, b, pv.URL)
	if res := pb.do("GET", "/dashboard", nil, nil); res.StatusCode != 307 {
		t.Fatalf("signed-out /dashboard on the preview: %d", res.StatusCode)
	}
	pvSignIn := pb.body(pb.do("GET", "/sign-in", nil, nil))
	res = pb.submit("/sign-in", formByID(t, pvSignIn, "sign-in"), map[string]string{
		"email": "ana@example.com", "password": "correct horse battery", "captcha": pb.captcha(),
	})
	if res.StatusCode != 303 || res.Header.Get("Location") != "/dashboard" || pb.cookie("__Secure-tiffin.session_data") == "" {
		t.Fatalf("production account signs in on the preview: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	for _, c := range res.Header.Values("Set-Cookie") {
		if strings.Contains(strings.ToLower(c), "domain=") {
			t.Fatalf("preview cookies must be host-only: %s", c)
		}
	}
	if dash := pb.body(pb.do("GET", "/dashboard", nil, nil)); !strings.Contains(dash, `id="who">ana@example.com<`) {
		t.Fatalf("verifySession on the preview:\n%s", head(dash))
	}
	// A tester signing up on the preview is a user of the project.
	tb := newBrowser(t, b, pv.URL)
	res = tb.submit("/sign-in", formByID(t, pvSignIn, "sign-up"), map[string]string{
		"email": "tess@example.com", "password": "correct horse battery", "captcha": tb.captcha(),
	})
	if res.StatusCode != 303 || tb.cookie("__Secure-tiffin.session_token") == "" {
		t.Fatalf("sign-up on the preview: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	prodTess := newBrowser(t, b, site)
	res = prodTess.submit("/sign-in", formByID(t, signInPage, "sign-in"), map[string]string{
		"email": "tess@example.com", "password": "correct horse battery", "captcha": prodTess.captcha(),
	})
	if res.StatusCode != 303 || res.Header.Get("Location") != "/dashboard" {
		t.Fatalf("preview sign-up signs in on production: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	var o struct{ RpID string }
	_, _, raw := b.get(b.https(), "GET", pv.URL+"/api/auth/passkey/generate-authenticate-options", nil)
	if json.Unmarshal([]byte(raw), &o); o.RpID != pu.Hostname() {
		t.Fatalf("passkey rpID on the preview: %q (%s)", o.RpID, raw)
	}
	phase("preview", p)
	t.Logf("TOTAL %s", time.Since(start).Round(time.Second))
}

// browser is a cookie jar over the box's edge that doesn't follow redirects.
type browser struct {
	t    *testing.T
	b    *cliBox
	c    *http.Client
	site string
	jar  map[string]string
}

func newBrowser(t *testing.T, b *cliBox, site string) *browser {
	c := b.https()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &browser{t: t, b: b, c: c, site: site, jar: map[string]string{}}
}

func (br *browser) cookie(name string) string { return br.jar[name] }

func (br *browser) do(method, path string, body io.Reader, hdr map[string]string) *http.Response {
	br.t.Helper()
	req, err := http.NewRequest(method, br.site+path, body)
	if err != nil {
		br.t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	var cs []string
	for k, v := range br.jar {
		cs = append(cs, k+"="+v)
	}
	if len(cs) > 0 {
		req.Header.Set("Cookie", strings.Join(cs, "; "))
	}
	res, err := br.c.Do(req)
	if err != nil {
		br.t.Fatalf("%s %s: %v", method, path, err)
	}
	for _, c := range res.Cookies() {
		if c.MaxAge < 0 || c.Value == "" {
			delete(br.jar, c.Name)
		} else {
			br.jar[c.Name] = c.Value
		}
	}
	return res
}

func (br *browser) body(res *http.Response) string {
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return string(raw)
}

// submit posts a page's form as a browser without JavaScript does (a
// multipart form with the Server Action's hidden fields), with some fields set.
func (br *browser) submit(path string, fields [][2]string, set map[string]string) *http.Response {
	br.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, f := range fields {
		v := f[1]
		if s, ok := set[f[0]]; ok {
			v = s
		}
		_ = mw.WriteField(f[0], v)
	}
	mw.Close()
	res := br.do("POST", path, &buf, map[string]string{"Content-Type": mw.FormDataContentType(), "Origin": br.site})
	res.Body.Close()
	return res
}

// captcha solves the engine's proof of work, as the sign-in components do.
func (br *browser) captcha() string {
	br.t.Helper()
	sol, err := solveAltcha([]byte(br.body(br.do("GET", "/api/auth/altcha/challenge", nil, nil))))
	if err != nil {
		br.t.Fatal(err)
	}
	return sol
}

func (br *browser) engineJSON(method, path, body string) map[string]any {
	br.t.Helper()
	res := br.do(method, path, strings.NewReader(body), map[string]string{"Content-Type": "application/json", "Origin": br.site})
	raw := br.body(res)
	var m map[string]any
	if res.StatusCode != 200 || json.Unmarshal([]byte(raw), &m) != nil {
		br.t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, raw)
	}
	return m
}

// formByID returns the inputs (name, value) of the form with that id.
func formByID(t *testing.T, page, id string) [][2]string {
	t.Helper()
	i := strings.Index(page, `id="`+id+`"`)
	if i < 0 {
		t.Fatalf("no form %q in the page:\n%s", id, head(page))
	}
	return formFields(t, page[strings.LastIndex(page[:i], "<form"):])
}
