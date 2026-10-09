package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/state"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// fakeKeys is the box-wide Google and GitHub keys.
type fakeKeys struct{ set map[string]bool }

func (k fakeKeys) Configured(context.Context) []string {
	var out []string
	for _, id := range []string{"github", "google"} {
		if k.set[id] {
			out = append(out, id)
		}
	}
	return out
}

func (k fakeKeys) Client(_ context.Context, provider string) (api.OAuthClient, bool, error) {
	if !k.set[provider] {
		return api.OAuthClient{}, false, nil
	}
	return api.OAuthClient{ClientID: provider + "-client", ClientSecret: provider + "-secret",
		RedirectURL: pkOrigin + "/api/auth/callback/" + provider}, true, nil
}

// fakeIdP plays Google and GitHub: authorize hands out codes for an account
// chosen by the test; the token endpoint checks the client and the PKCE verifier.
type fakeIdP struct {
	t     *testing.T
	mu    sync.Mutex
	codes map[string]grant // code -> grant
	toks  map[string]grant // GitHub access token -> grant
	n     int
	// what the next Google ID token says (tests change it)
	badNonce bool
	badAud   bool
	// account is the provider account the person approves with; empty: one
	// per address (the first, or GitHub's primary).
	account string
	ids     map[string]int // GitHub: account -> numeric user id
}

type grant struct {
	provider, challenge, nonce, account string
	emails                              []ghEmail
}

type ghEmail struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

func (f *fakeIdP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/google/token", "/github/token":
		provider := strings.Split(r.URL.Path, "/")[1]
		_ = r.ParseForm()
		g, ok := f.codes[r.Form.Get("code")]
		delete(f.codes, r.Form.Get("code"))
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		switch {
		case !ok || g.provider != provider:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		case base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"code_verifier"}`)
			return
		case r.Form.Get("client_id") != provider+"-client" || r.Form.Get("client_secret") != provider+"-secret" ||
			r.Form.Get("redirect_uri") != pkOrigin+"/api/auth/callback/"+provider:
			w.WriteHeader(401)
			return
		}
		f.n++
		at := fmt.Sprintf("at-%d", f.n)
		out := map[string]any{"access_token": at, "token_type": "bearer"}
		if provider == "github" {
			f.toks[at] = g
			out["expires_in"], out["refresh_token"] = 28800, "rt"
		} else {
			e := g.emails[0]
			aud, nonce := "google-client", g.nonce
			if f.badAud {
				aud = "someone-else"
			}
			if f.badNonce {
				nonce = "other"
			}
			c := map[string]any{"iss": "https://accounts.google.com", "aud": aud, "sub": g.account,
				"exp": time.Now().Add(time.Hour).Unix(), "nonce": nonce, "email": e.Email, "email_verified": e.Verified}
			if strings.HasSuffix(strings.ToLower(e.Email), "@example.com") {
				c["hd"] = "example.com" // example.com is a Google Workspace domain here
			}
			claims, _ := json.Marshal(c)
			enc := base64.RawURLEncoding
			out["id_token"] = enc.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." + enc.EncodeToString(claims) + ".sig"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	case "/github/emails", "/github/user":
		g, ok := f.toks[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if !ok || r.Header.Get("X-GitHub-Api-Version") == "" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/github/user" {
			if f.ids[g.account] == 0 {
				f.ids[g.account] = len(f.ids) + 1001
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": f.ids[g.account], "login": g.account})
			return
		}
		if r.URL.Query().Get("per_page") != "100" {
			w.WriteHeader(400)
			return
		}
		_ = json.NewEncoder(w).Encode(g.emails)
	default:
		w.WriteHeader(404)
	}
}

// authorize is the person approving at the provider: it returns the
// callback URL the provider sends the browser to.
func (f *fakeIdP) authorize(authURL string, emails ...ghEmail) string {
	f.t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		f.t.Fatal(err)
	}
	q := u.Query()
	provider := strings.Split(u.Path, "/")[1]
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" {
		f.t.Fatalf("authorize URL without PKCE or state: %s", authURL)
	}
	if provider == "google" && (q.Get("nonce") == "" || !strings.Contains(q.Get("scope"), "openid") || !strings.Contains(q.Get("scope"), "email")) {
		f.t.Fatalf("google authorize URL: %s", authURL)
	}
	if provider == "github" && (q.Get("scope") != "user:email" || q.Get("allow_signup") != "false") {
		f.t.Fatalf("github authorize URL: %s", authURL)
	}
	f.mu.Lock()
	f.n++
	code := fmt.Sprintf("code-%d", f.n)
	account := f.account
	if account == "" && len(emails) > 0 {
		account = provider + "-" + strings.ToLower(emails[0].Email)
		for _, e := range emails {
			if e.Primary {
				account = provider + "-" + strings.ToLower(e.Email)
			}
		}
	}
	f.codes[code] = grant{provider: provider, challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), account: account, emails: emails}
	f.mu.Unlock()
	return q.Get("redirect_uri") + "?" + url.Values{"code": {code}, "state": {q.Get("state")}}.Encode()
}

type oauthEnv struct {
	t     *testing.T
	srv   *httptest.Server
	idp   *fakeIdP
	tm    *tokens.Manager
	db    *state.DB
	owner *tokens.Principal
	mail  *fakeMailer
	a     *api.API
}

func newOAuthEnv(t *testing.T, providers ...string) *oauthEnv {
	t.Helper()
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tm := tokens.NewManager(db)
	ownerKey, _, _ := tm.Bootstrap(t.Context())
	owner, _ := tm.Authenticate(t.Context(), ownerKey)
	set := map[string]bool{}
	for _, p := range providers {
		set[p] = true
	}
	api.SetDashboardOAuth(fakeKeys{set})
	t.Cleanup(func() { api.SetDashboardOAuth(nil) })
	mail := &fakeMailer{}
	api.SetBoxMailer(mail)
	t.Cleanup(func() { api.SetBoxMailer(nil) })
	idp := &fakeIdP{t: t, codes: map[string]grant{}, toks: map[string]grant{}, ids: map[string]int{}}
	idpSrv := httptest.NewServer(idp)
	t.Cleanup(idpSrv.Close)
	t.Cleanup(api.UseFakeOAuthProviders(idpSrv.URL, idpSrv.Client()))

	a := api.New(api.Deps{DB: db, Engine: change.NewEngine(db), Tokens: tm, PublicURL: pkOrigin})
	mux := http.NewServeMux()
	mux.Handle("/v1/", a.Handler())
	mux.HandleFunc("/api/auth/", func(w http.ResponseWriter, r *http.Request) {
		if a.DashboardOAuthCallback(w, r) {
			return
		}
		w.WriteHeader(299) // "forwarded to the auth engine"
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &oauthEnv{t: t, srv: srv, idp: idp, tm: tm, db: db, owner: owner, mail: mail, a: a}
}

var noRedirects = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// start asks the box for the provider's URL from the login page.
func (e *oauthEnv) start(provider, next, ip string) (*http.Response, string, *http.Cookie) {
	e.t.Helper()
	req, _ := http.NewRequest("POST", e.srv.URL+"/v1/session/oauth/"+provider+"?next="+url.QueryEscape(next), nil)
	req.Header.Set("X-Forwarded-For", ip)
	req.Header.Set(api.EdgeKeyHeader, testEdgeKey)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	res, err := noRedirects.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	var out api.OAuthSignInStart
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res, out.URL, cookieNamed(res, api.OAuthStateCookie)
}

// back is the browser returning from the provider to the box.
func (e *oauthEnv) back(callback, ip string, cookies ...*http.Cookie) (*http.Response, string) {
	e.t.Helper()
	u, _ := url.Parse(callback) // on the dashboard host: send it to the test server
	req, _ := http.NewRequest("GET", e.srv.URL+u.RequestURI(), nil)
	req.Header.Set("X-Forwarded-For", ip)
	req.Header.Set(api.EdgeKeyHeader, testEdgeKey)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Chrome/140.0 Safari/537.36")
	for _, c := range cookies {
		if c != nil {
			req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
		}
	}
	res, err := noRedirects.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func (e *oauthEnv) person(name, email, role string) *tokens.Person {
	e.t.Helper()
	p, err := e.tm.AddPerson(e.t.Context(), e.owner, name, email, role)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

// refused checks the browser was sent back to the login page with reason.
func refused(t *testing.T, res *http.Response, reason string) {
	t.Helper()
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/login?reason="+url.QueryEscape(reason) {
		t.Fatalf("want a redirect to the login page for %s, got %d %q", reason, res.StatusCode, res.Header.Get("Location"))
	}
	if cookieNamed(res, api.SessionCookie) != nil {
		t.Fatalf("%s: a session cookie was set", reason)
	}
}

// signedInAs checks the callback set a working session for person.
func (e *oauthEnv) signedInAs(res *http.Response, body string, person *tokens.Person, next string) {
	e.t.Helper()
	if res.StatusCode != 200 || !strings.Contains(body, `content="0;url=`+next+`"`) {
		e.t.Fatalf("callback: %d %s", res.StatusCode, body)
	}
	c := cookieNamed(res, api.SessionCookie)
	if c == nil || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode {
		e.t.Fatalf("session cookie: %+v", c)
	}
	p, err := e.tm.Authenticate(e.t.Context(), c.Value)
	if err != nil || p.Person != person.ID || p.Kind != tokens.KindHuman {
		e.t.Fatalf("session: %+v %v", p, err)
	}
	if st := cookieNamed(res, api.OAuthStateCookie); st == nil || st.MaxAge >= 0 {
		e.t.Fatalf("state cookie not cleared: %+v", st)
	}
}

func TestOAuthSignInStatusAndStart(t *testing.T) {
	e := newOAuthEnv(t, "google")
	res, err := http.Get(e.srv.URL + "/v1/session/oauth")
	if err != nil {
		t.Fatal(err)
	}
	var st api.OAuthSignInStatus
	_ = json.NewDecoder(res.Body).Decode(&st)
	res.Body.Close()
	if len(st.Providers) != 1 || st.Providers[0].ID != "google" || st.Providers[0].Name != "Google" {
		t.Fatalf("status: %+v", st)
	}

	// GitHub has no keys: no button, and starting is refused.
	if res, _, _ := e.start("github", "/", "203.0.113.1"); res.StatusCode != 404 {
		t.Fatalf("github without keys: %d", res.StatusCode)
	}

	res, authURL, c := e.start("google", "/", "203.0.113.1")
	if res.StatusCode != 200 || !strings.Contains(authURL, "/google/auth?") {
		t.Fatalf("start: %d %s", res.StatusCode, authURL)
	}
	if c == nil || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.MaxAge <= 0 || c.MaxAge > 600 {
		t.Fatalf("state cookie: %+v", c)
	}
	u, _ := url.Parse(authURL)
	if q := u.Query(); !strings.HasPrefix(q.Get("state"), api.OAuthStatePrefix) || q.Get("client_id") != "google-client" ||
		q.Get("redirect_uri") != pkOrigin+"/api/auth/callback/google" || q.Get("response_type") != "code" {
		t.Fatalf("authorize URL: %s", authURL)
	}
	if strings.Contains(c.Value, "google-secret") {
		t.Fatal("the client secret is in the cookie")
	}

	// From another site: refused.
	req, _ := http.NewRequest("POST", e.srv.URL+"/v1/session/oauth/google", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if res, _ := http.DefaultClient.Do(req); res.StatusCode != 403 {
		t.Fatalf("cross-site start: %d", res.StatusCode)
	}

	// Rate limited per client address.
	limited := false
	for range api.OAuthSignInRate + 1 {
		if res, _, _ := e.start("google", "/", "203.0.113.9"); res.StatusCode == 429 {
			limited = true
		}
	}
	if !limited {
		t.Fatal("starts are not rate limited")
	}
}

func TestOAuthSignInGoogle(t *testing.T) {
	e := newOAuthEnv(t, "google", "github")
	maya := e.person("Maya Okafor", "maya@example.com", "member")
	ip := "203.0.113.5"

	// Match: a verified address, compared without case. Next is kept.
	_, authURL, c := e.start("google", "/projects/shop?tab=logs", ip)
	res, body := e.back(e.idp.authorize(authURL, ghEmail{Email: "Maya@Example.com", Verified: true}), ip, c)
	e.signedInAs(res, body, maya, "/projects/shop?tab=logs")
	if res.Header.Get("Referrer-Policy") != "no-referrer" || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("callback headers: %v", res.Header)
	}
	device := cookieNamed(res, api.DeviceCookie)
	if device == nil {
		t.Fatal("no device cookie")
	}
	ok := false
	for _, ev := range auditEvents(t, e.db, "session.oauth") {
		ok = ok || strings.Contains(ev, "Maya Okafor signed in with Google")
	}
	if !ok {
		t.Fatal("the sign-in is not in the audit log")
	}

	// A second sign-in from a browser the box hasn't seen her use: a notice.
	_, authURL, c = e.start("google", "/", ip)
	res, body = e.back(e.idp.authorize(authURL, ghEmail{Email: "maya@example.com", Verified: true}), ip, c)
	e.signedInAs(res, body, maya, "/")
	e.a.WaitBackground()
	sent := e.mail.all()
	if len(sent) != 1 || sent[0].Kind != api.BoxMailNewDevice || sent[0].Via != "Google" || sent[0].To != "maya@example.com" {
		t.Fatalf("new sign-in notice: %+v", sent)
	}

	// No match: said plainly, nothing more.
	_, authURL, c = e.start("google", "/", ip)
	res, _ = e.back(e.idp.authorize(authURL, ghEmail{Email: "stranger@example.com", Verified: true}), ip, c)
	refused(t, res, "google:unknown")

	// Another Google account with Maya's address, unverified: refused.
	e.idp.account = "google-someone-else"
	_, authURL, c = e.start("google", "/", ip)
	res, _ = e.back(e.idp.authorize(authURL, ghEmail{Email: "maya@example.com", Verified: false}), ip, c)
	refused(t, res, "google:unverified")
	e.idp.account = ""

	// An ID token for another app, or with another nonce: refused.
	e.idp.badAud = true
	_, authURL, c = e.start("google", "/", ip)
	res, _ = e.back(e.idp.authorize(authURL, ghEmail{Email: "maya@example.com", Verified: true}), ip, c)
	refused(t, res, "google:failed")
	e.idp.badAud, e.idp.badNonce = false, true
	_, authURL, c = e.start("google", "/", ip)
	res, _ = e.back(e.idp.authorize(authURL, ghEmail{Email: "maya@example.com", Verified: true}), ip, c)
	refused(t, res, "google:failed")
	e.idp.badNonce = false

	// The person said no at Google.
	_, authURL, c = e.start("google", "/", ip)
	u, _ := url.Parse(authURL)
	res, _ = e.back(pkOrigin+"/api/auth/callback/google?error=access_denied&state="+url.QueryEscape(u.Query().Get("state")), ip, c)
	refused(t, res, "google:denied")

	// A removed person can't sign in.
	if err := e.tm.RemovePerson(t.Context(), e.owner, maya.ID); err != nil {
		t.Fatal(err)
	}
	_, authURL, c = e.start("google", "/", ip)
	res, _ = e.back(e.idp.authorize(authURL, ghEmail{Email: "maya@example.com", Verified: true}), ip, c)
	refused(t, res, "google:unknown")
}

func TestOAuthSignInGitHub(t *testing.T) {
	e := newOAuthEnv(t, "github")
	sam := e.person("Sam Lee", "sam@example.com", "admin")
	ip := "203.0.113.6"

	// The primary, verified address counts.
	_, authURL, c := e.start("github", "/", ip)
	res, body := e.back(e.idp.authorize(authURL,
		ghEmail{Email: "sam@users.noreply.example", Verified: true},
		ghEmail{Email: "sam@example.com", Primary: true, Verified: true}), ip, c)
	e.signedInAs(res, body, sam, "/")

	// Kim's verified address on GitHub that isn't the primary one still counts.
	kim := e.person("Kim Ito", "kim@example.com", "member")
	_, authURL, c = e.start("github", "/", ip)
	res, body = e.back(e.idp.authorize(authURL,
		ghEmail{Email: "other@example.org", Primary: true, Verified: true},
		ghEmail{Email: "kim@example.com", Verified: true}), ip, c)
	e.signedInAs(res, body, kim, "/")

	// Kim's address but unverified, next to a verified one nobody has: no match.
	e.person("Lou Park", "lou@example.com", "member")
	_, authURL, c = e.start("github", "/", ip)
	res, _ = e.back(e.idp.authorize(authURL,
		ghEmail{Email: "nobody@example.org", Primary: true, Verified: true},
		ghEmail{Email: "lou@example.com"}), ip, c)
	refused(t, res, "github:unknown")

	// No verified address at all.
	_, authURL, c = e.start("github", "/", ip)
	res, _ = e.back(e.idp.authorize(authURL, ghEmail{Email: "lou@example.com", Primary: true}), ip, c)
	refused(t, res, "github:unverified")
}

// The account someone first signs in with is theirs: it keeps signing them
// in, and another account of that provider showing their address doesn't.
// A provider's "verified" can outlive someone's hold on an address.
func TestOAuthSignInLinkedAccount(t *testing.T) {
	e := newOAuthEnv(t, "google", "github")
	ip := "203.0.113.9"
	ada := e.person("Ada Byron", "ada@example.com", "member")
	gina := e.person("Gina Gray", "gina@gmail.com", "member")
	ext := e.person("Ezra Ext", "ezra@outside.test", "member")

	signIn := func(provider, account string, emails ...ghEmail) (*http.Response, string) {
		e.idp.account = account
		_, authURL, c := e.start(provider, "/", ip)
		return e.back(e.idp.authorize(authURL, emails...), ip, c)
	}
	res, body := signIn("google", "g-ada", ghEmail{Email: "ada@example.com", Verified: true})
	e.signedInAs(res, body, ada, "/")
	// Another Google account with Ada's address: refused.
	res, _ = signIn("google", "g-intruder", ghEmail{Email: "ada@example.com", Verified: true})
	refused(t, res, "google:linked")
	// Ada's own account, whatever address it shows now, is still Ada.
	res, body = signIn("google", "g-ada", ghEmail{Email: "ada.byron@example.com", Verified: true})
	e.signedInAs(res, body, ada, "/")

	// Gmail: Google owns the address. Any other address with no Workspace
	// (hd): Google doesn't vouch for who holds it now.
	res, body = signIn("google", "g-gina", ghEmail{Email: "gina@gmail.com", Verified: true})
	e.signedInAs(res, body, gina, "/")
	res, _ = signIn("google", "g-ezra", ghEmail{Email: "ezra@outside.test", Verified: true})
	refused(t, res, "google:unverified")

	// GitHub: the same, by the account's numeric id.
	res, body = signIn("github", "ezra-gh", ghEmail{Email: "ezra@outside.test", Primary: true, Verified: true})
	e.signedInAs(res, body, ext, "/")
	res, _ = signIn("github", "old-owner-gh", ghEmail{Email: "ezra@outside.test", Primary: true, Verified: true})
	refused(t, res, "github:linked")

	// Removed and invited again: a new person, linked afresh.
	if err := e.tm.RemovePerson(t.Context(), e.owner, ada.ID); err != nil {
		t.Fatal(err)
	}
	res, _ = signIn("google", "g-ada", ghEmail{Email: "ada@example.com", Verified: true})
	refused(t, res, "google:unknown")
	ada2 := e.person("Ada Byron", "ada@example.com", "member")
	res, body = signIn("google", "g-intruder", ghEmail{Email: "ada@example.com", Verified: true})
	e.signedInAs(res, body, ada2, "/")
}

func TestOAuthSignInState(t *testing.T) {
	e := newOAuthEnv(t, "google", "github")
	maya := e.person("Maya Okafor", "maya@example.com", "admin")
	ip := "203.0.113.7"
	acct := ghEmail{Email: "maya@example.com", Verified: true}

	// Not the dashboard's state: it goes on to the auth engine (an app's sign-in).
	res, _ := e.back(pkOrigin+"/api/auth/callback/google?code=x&state=abcDEF123", ip)
	if res.StatusCode != 299 {
		t.Fatalf("an app's callback was not passed on: %d", res.StatusCode)
	}

	// No state cookie (another browser, or it expired).
	_, authURL, _ := e.start("google", "/", ip)
	res, _ = e.back(e.idp.authorize(authURL, acct), ip)
	refused(t, res, "google:expired")

	// A state that isn't the cookie's.
	_, authURL, c := e.start("google", "/", ip)
	cb := e.idp.authorize(authURL, acct)
	u, _ := url.Parse(cb)
	q := u.Query()
	q.Set("state", api.OAuthStatePrefix+"forged")
	u.RawQuery = q.Encode()
	res, _ = e.back(u.String(), ip, c)
	refused(t, res, "google:expired")

	// Another browser's cookie, for another sign-in.
	_, authURL, _ = e.start("google", "/", ip)
	_, _, other := e.start("google", "/", ip)
	res, _ = e.back(e.idp.authorize(authURL, acct), ip, other)
	refused(t, res, "google:expired")

	// A tampered cookie.
	_, authURL, c = e.start("google", "/", ip)
	payload, sig, _ := strings.Cut(c.Value, ".")
	raw, _ := base64.RawURLEncoding.DecodeString(payload)
	raw = []byte(strings.Replace(string(raw), `"x":"/"`, `"x":"/box"`, 1))
	c.Value = base64.RawURLEncoding.EncodeToString(raw) + "." + sig
	res, _ = e.back(e.idp.authorize(authURL, acct), ip, c)
	refused(t, res, "google:expired")

	// A Google state at the GitHub callback.
	_, authURL, c = e.start("google", "/", ip)
	cb = strings.Replace(e.idp.authorize(authURL, acct), "/callback/google", "/callback/github", 1)
	res, _ = e.back(cb, ip, c)
	refused(t, res, "github:expired")

	// Replayed: the same callback and cookie work once.
	_, authURL, c = e.start("google", "/", ip)
	cb = e.idp.authorize(authURL, acct)
	res, body := e.back(cb, ip, c)
	e.signedInAs(res, body, maya, "/")
	res, _ = e.back(cb, ip, c)
	refused(t, res, "google:expired")

	// Refusals are audited.
	if n := len(auditEvents(t, e.db, "session.oauth_refused")); n < 6 {
		t.Fatalf("refusals audited: %d", n)
	}
}

func TestOAuthSignInOpenRedirect(t *testing.T) {
	e := newOAuthEnv(t, "google")
	maya := e.person("Maya Okafor", "maya@example.com", "member")
	ip := "203.0.113.8"
	for _, next := range []string{"https://evil.example/", "//evil.example/x", "/\\evil.example", "javascript:alert(1)",
		"/login?next=//evil.example", "evil.example"} {
		_, authURL, c := e.start("google", next, ip)
		res, body := e.back(e.idp.authorize(authURL, ghEmail{Email: "maya@example.com", Verified: true}), ip, c)
		e.signedInAs(res, body, maya, "/")
		if strings.Contains(body, "evil") {
			t.Fatalf("next %q leaked into the page: %s", next, body)
		}
	}
	cases := map[string]string{
		"/":                    "/",
		"/projects/shop":       "/projects/shop",
		"/box?tab=people#x":    "/box?tab=people#x",
		"":                     "/",
		"//evil.example":       "/",
		"/\\evil.example":      "/",
		"https://evil.example": "/",
		"/v1/whoami":           "/",
		"/api/auth/x":          "/",
		"/login":               "/",
		"/\tevil":              "/",
	}
	for in, want := range cases {
		if got := api.SafeNext(in); got != want {
			t.Errorf("SafeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOAuthCallbackRateLimited(t *testing.T) {
	e := newOAuthEnv(t, "google")
	limited := false
	for range api.OAuthSignInRate + 1 {
		res, _ := e.back(pkOrigin+"/api/auth/callback/google?code=x&state="+api.OAuthStatePrefix+"x", "203.0.113.10")
		if res.Header.Get("Location") == "/login?reason="+url.QueryEscape("google:busy") {
			limited = true
		}
	}
	if !limited {
		t.Fatal("callbacks are not rate limited")
	}
}

// auditEvents returns the details of audit entries with action.
func auditEvents(t *testing.T, db *state.DB, action string) []string {
	t.Helper()
	rows, err := db.SQL().QueryContext(t.Context(), `SELECT COALESCE(detail, '') FROM audit WHERE action = ?`, action)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return out
}
