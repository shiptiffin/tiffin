package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
	"golang.org/x/oauth2"
)

// Signing in to the dashboard with Google or GitHub, for the people already
// on the box (owner, admins, members). It never makes anyone: the provider
// proves an email address, and the box signs in the active person with that
// address, or nobody.
//
//	GET  /v1/session/oauth             -> which providers the login page offers
//	POST /v1/session/oauth/{provider}  -> {url}; sets the signed state cookie
//	GET  /api/auth/callback/{provider} -> session cookie, then the dashboard
//
// The callback is the one the box-wide sign-in keys already register for
// apps (auth.DashboardSignIn hands it here first). The box tells its own
// sign-ins apart by the state's prefix; every other callback goes on to the
// auth engine as before.
//
// The flow is the authorization code flow with PKCE (S256), a random state
// and, for Google, an OpenID Connect nonce. State, verifier, nonce, provider
// and where to go next ride in a short-lived cookie, HMAC-signed with a key
// the API process makes at start (a sign-in in flight when the box restarts
// has to start again). Each state works once. The provider's token is used
// once, to read the address, and never kept.

// OAuthClient is a provider's box-wide OAuth app, for dashboard sign-in.
type OAuthClient struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string // https://<dashboard host>/api/auth/callback/<provider>
}

// DashboardOAuth gives dashboard sign-in the box-wide provider keys. The
// auth module implements it (SetDashboardOAuth).
type DashboardOAuth interface {
	// Configured lists the providers (of google, github) with keys set.
	Configured(ctx context.Context) []string
	// Client returns a provider's keys, or ok=false when they aren't set.
	Client(ctx context.Context, provider string) (c OAuthClient, ok bool, err error)
}

var (
	dashOAuthMu sync.RWMutex
	dashOAuth   DashboardOAuth
)

// SetDashboardOAuth installs the provider keys (nil turns the buttons off).
func SetDashboardOAuth(d DashboardOAuth) {
	dashOAuthMu.Lock()
	dashOAuth = d
	dashOAuthMu.Unlock()
}

func dashboardOAuth() DashboardOAuth {
	dashOAuthMu.RLock()
	defer dashOAuthMu.RUnlock()
	return dashOAuth
}

// oauthEndpoints are the providers' URLs (tests point them at fakes).
type oauthEndpoints struct {
	GoogleAuth, GoogleToken               string
	GitHubAuth, GitHubToken, GitHubEmails string
	HTTP                                  *http.Client
}

var oauthURLs = oauthEndpoints{
	GoogleAuth:   "https://accounts.google.com/o/oauth2/v2/auth",
	GoogleToken:  "https://oauth2.googleapis.com/token",
	GitHubAuth:   "https://github.com/login/oauth/authorize",
	GitHubToken:  "https://github.com/login/oauth/access_token",
	GitHubEmails: "https://api.github.com/user/emails",
	HTTP:         &http.Client{Timeout: 15 * time.Second},
}

// OAuthSignInProviders are the providers people can sign in to the dashboard with.
var OAuthSignInProviders = []struct{ ID, Name string }{{"google", "Google"}, {"github", "GitHub"}}

func oauthProviderName(id string) string {
	for _, p := range OAuthSignInProviders {
		if p.ID == id {
			return p.Name
		}
	}
	return ""
}

const (
	// OAuthStateCookie holds the signed state. __Host- keeps the box's apps
	// (same site, other subdomains) from planting one.
	OAuthStateCookie = "__Host-tiffin_oauth"
	// OAuthStatePrefix marks the dashboard's own states at the shared callback.
	OAuthStatePrefix = "tdash."
	// OAuthStateTTL is how long a sign-in may take at the provider.
	OAuthStateTTL = 10 * time.Minute
	// OAuthSignInRate is how many starts, and callbacks, per minute one
	// client address may make.
	OAuthSignInRate = 10
)

// OAuthSignInStatus says which providers the login page offers.
type OAuthSignInStatus struct {
	Providers []OAuthSignInProvider `json:"providers" doc:"Providers with box-wide keys set, in the order to show them"`
}

// OAuthSignInProvider is one sign-in button.
type OAuthSignInProvider struct {
	ID   string `json:"id" enum:"google,github"`
	Name string `json:"name"`
}

// OAuthSignInStart is where to send the browser.
type OAuthSignInStart struct {
	URL string `json:"url" doc:"The provider's sign-in page; open it in this tab"`
}

// oauthState is what the state cookie carries.
type oauthState struct {
	Provider string `json:"p"`
	State    string `json:"s"`
	Verifier string `json:"v"`
	Nonce    string `json:"n,omitempty"`
	Next     string `json:"x"`
	Exp      int64  `json:"e"`
}

// oauthFlow is one API's signing key, spent states and limits.
type oauthFlow struct {
	key        [32]byte
	mu         sync.Mutex
	spent      map[string]time.Time // state -> when it stops mattering
	startLimit *ipLimiter
	backLimit  *ipLimiter
	now        func() time.Time
}

func newOAuthFlow() *oauthFlow {
	f := &oauthFlow{spent: map[string]time.Time{}, startLimit: newIPLimiter(OAuthSignInRate),
		backLimit: newIPLimiter(OAuthSignInRate), now: time.Now}
	_, _ = rand.Read(f.key[:])
	return f
}

func randToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (f *oauthFlow) sign(payload []byte) string {
	m := hmac.New(sha256.New, f.key[:])
	m.Write([]byte("tiffin-oauth-state\x00"))
	m.Write(payload)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (f *oauthFlow) seal(s oauthState) string {
	raw, _ := json.Marshal(s)
	p := base64.RawURLEncoding.EncodeToString(raw)
	return p + "." + f.sign([]byte(p))
}

var errOAuthState = errors.New("the sign-in state is missing, expired or not this browser's")

// open checks the cookie's signature and age and that it is for this state
// and provider. It does not spend it.
func (f *oauthFlow) open(cookie, state, provider string) (*oauthState, error) {
	p, sig, ok := strings.Cut(cookie, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(f.sign([]byte(p)))) {
		return nil, errOAuthState
	}
	raw, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		return nil, errOAuthState
	}
	var s oauthState
	if json.Unmarshal(raw, &s) != nil {
		return nil, errOAuthState
	}
	if f.now().Unix() > s.Exp || s.Provider != provider ||
		subtle.ConstantTimeCompare([]byte(s.State), []byte(state)) != 1 {
		return nil, errOAuthState
	}
	return &s, nil
}

// spend marks a state used; false if it already was.
func (f *oauthFlow) spend(s *oauthState) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	if len(f.spent) > 1000 {
		for k, until := range f.spent {
			if now.After(until) {
				delete(f.spent, k)
			}
		}
	}
	if _, used := f.spent[s.State]; used {
		return false
	}
	f.spent[s.State] = time.Unix(s.Exp, 0).Add(time.Minute)
	return true
}

// SafeNext keeps a post-sign-in destination on the dashboard: a path, never
// another site ("//evil.example", "/\evil.example", "https://..."), and
// never the login page again. Anything else is "/".
func SafeNext(next string) string {
	if next == "" || len(next) > 1024 || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") ||
		strings.ContainsAny(next, "\\\r\n\t\x00") {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || !strings.HasPrefix(u.Path, "/") {
		return "/"
	}
	if u.Path == "/login" || strings.HasPrefix(u.Path, "/api/") || strings.HasPrefix(u.Path, "/v1/") {
		return "/"
	}
	return next
}

func (o OAuthClient) config(provider string) *oauth2.Config {
	c := &oauth2.Config{ClientID: o.ClientID, ClientSecret: o.ClientSecret, RedirectURL: o.RedirectURL}
	switch provider {
	case "google":
		c.Endpoint = oauth2.Endpoint{AuthURL: oauthURLs.GoogleAuth, TokenURL: oauthURLs.GoogleToken, AuthStyle: oauth2.AuthStyleInParams}
		c.Scopes = []string{"openid", "email"}
	case "github":
		c.Endpoint = oauth2.Endpoint{AuthURL: oauthURLs.GitHubAuth, TokenURL: oauthURLs.GitHubToken, AuthStyle: oauth2.AuthStyleInParams}
		c.Scopes = []string{"user:email"}
	}
	return c
}

func (a *API) registerOAuthSignIn() {
	api := a.api
	a.oauth = newOAuthFlow()

	st := op("session-oauth-status", http.MethodGet, "/v1/session/oauth", "-", RiskRead, "Which sign-in providers can open the dashboard?",
		"The providers the login page offers (\"Sign in with Google\", \"Sign in with GitHub\"): those with box-wide keys set.", "system")
	st.Security = nil
	huma.Register(api, st, wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body OAuthSignInStatus }, error) {
		out := OAuthSignInStatus{Providers: []OAuthSignInProvider{}}
		if d := dashboardOAuth(); d != nil {
			set := map[string]bool{}
			for _, id := range d.Configured(ctx) {
				set[id] = true
			}
			for _, p := range OAuthSignInProviders {
				if set[p.ID] {
					out.Providers = append(out.Providers, OAuthSignInProvider{p.ID, p.Name})
				}
			}
		}
		return &struct{ Body OAuthSignInStatus }{out}, nil
	}))

	o := op("session-oauth-start", http.MethodPost, "/v1/session/oauth/{provider}", "-", RiskWrite, "Start signing in with Google or GitHub",
		"Returns the provider's sign-in URL and sets a short-lived, signed state cookie (state, PKCE verifier, nonce). The provider "+
			"sends the browser back to /api/auth/callback/{provider}, where the box signs in the active person with the account's "+
			"verified email, or nobody: it never makes an account. Limited per client address. Used by the dashboard's login page.", "system")
	o.Security = nil
	o.Errors = append(o.Errors, 404, 429)
	o.Middlewares = huma.Middlewares{func(ctx huma.Context, next func(huma.Context)) {
		ip := clientIP(ctx.RemoteAddr(), ctx.Header("X-Forwarded-For"))
		if !sameOrigin(ctx) {
			_ = huma.WriteErr(a.api, ctx, http.StatusForbidden, "This request came from another site.")
			return
		}
		if ok, wait := a.oauth.startLimit.allow(ip); !ok {
			writeRateLimited(ctx, wait, "too many sign-in attempts from your address")
			return
		}
		next(huma.WithValue(ctx, clientIPKey{}, ip))
	}}
	huma.Register(api, o, wrap(func(ctx context.Context, in *struct {
		Provider string `path:"provider" enum:"google,github" doc:"google or github"`
		Next     string `query:"next" maxLength:"1024" doc:"A dashboard path to open after signing in; anything else opens the home page"`
	}) (*struct {
		SetCookie http.Cookie `header:"Set-Cookie"`
		Body      OAuthSignInStart
	}, error) {
		d := dashboardOAuth()
		name := oauthProviderName(in.Provider)
		var c OAuthClient
		ok := false
		if d != nil {
			var err error
			if c, ok, err = d.Client(ctx, in.Provider); err != nil {
				return nil, err
			}
		}
		if !ok {
			p := problem(404, "not_found", "signing in with "+name+" isn't set up on this box")
			p.Hint = "an owner adds the box-wide " + name + " keys in Box settings › Sign-in providers"
			return nil, p
		}
		s := oauthState{Provider: in.Provider, State: OAuthStatePrefix + randToken(24), Verifier: oauth2.GenerateVerifier(),
			Next: SafeNext(in.Next), Exp: a.oauth.now().Add(OAuthStateTTL).Unix()}
		opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(s.Verifier)}
		switch in.Provider {
		case "google":
			s.Nonce = randToken(18)
			opts = append(opts, oauth2.SetAuthURLParam("nonce", s.Nonce), oauth2.SetAuthURLParam("prompt", "select_account"))
		case "github":
			opts = append(opts, oauth2.SetAuthURLParam("allow_signup", "false"))
		}
		return &struct {
			SetCookie http.Cookie `header:"Set-Cookie"`
			Body      OAuthSignInStart
		}{
			SetCookie: http.Cookie{Name: OAuthStateCookie, Value: a.oauth.seal(s), Path: "/", HttpOnly: true, Secure: true,
				SameSite: http.SameSiteLaxMode, MaxAge: int(OAuthStateTTL / time.Second)},
			Body: OAuthSignInStart{URL: c.config(in.Provider).AuthCodeURL(s.State, opts...)},
		}, nil
	}))
}

// Why a dashboard sign-in with a provider ended on the login page. The
// login page reads them from ?reason=<provider>:<code>.
const (
	oauthUnknown    = "unknown"    // no active person has that address
	oauthUnverified = "unverified" // the provider hasn't verified it
	oauthExpired    = "expired"    // state missing, expired, replayed or another browser's
	oauthDenied     = "denied"     // the person said no at the provider
	oauthFailed     = "failed"     // the provider's answer was wrong or didn't come
	oauthBusy       = "busy"       // rate limited
	oauthOff        = "off"        // the box's keys for it were removed
)

// DashboardOAuthCallback handles /api/auth/callback/{provider} when the
// state is one of the dashboard's own, and reports whether it was. Install
// it as auth.DashboardSignIn.
func (a *API) DashboardOAuthCallback(w http.ResponseWriter, r *http.Request) bool {
	provider := strings.TrimPrefix(r.URL.Path, "/api/auth/callback/")
	name := oauthProviderName(provider)
	q := r.URL.Query()
	state := q.Get("state")
	if name == "" || a.oauth == nil || !strings.HasPrefix(state, OAuthStatePrefix) {
		return false
	}
	ctx := r.Context()
	ip := clientIP(r.RemoteAddr, r.Header.Get("X-Forwarded-For"))
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer") // the URL carries the code
	h.Set("X-Content-Type-Options", "nosniff")
	http.SetCookie(w, &http.Cookie{Name: OAuthStateCookie, Value: "", Path: "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteLaxMode, MaxAge: -1})
	fail := func(code string, detail map[string]any) bool {
		if detail == nil {
			detail = map[string]any{}
		}
		detail["provider"], detail["reason"], detail["ip"] = provider, code, ip
		detail["summary"] = "a sign-in with " + name + " was refused (" + code + ")"
		if a.deps.DB != nil {
			_ = a.deps.DB.Audit(ctx, "via:"+provider, "session.oauth_refused", "", detail)
		}
		http.Redirect(w, r, "/login?reason="+url.QueryEscape(provider+":"+code), http.StatusSeeOther)
		return true
	}
	if r.Method != http.MethodGet {
		return fail(oauthFailed, nil)
	}
	if ok, _ := a.oauth.backLimit.allow(ip); !ok {
		return fail(oauthBusy, nil)
	}
	ck, err := r.Cookie(OAuthStateCookie)
	if err != nil {
		return fail(oauthExpired, nil)
	}
	s, err := a.oauth.open(ck.Value, state, provider)
	if err != nil || !a.oauth.spend(s) {
		return fail(oauthExpired, nil)
	}
	if e := q.Get("error"); e != "" {
		if e == "access_denied" {
			return fail(oauthDenied, nil)
		}
		return fail(oauthFailed, map[string]any{"error": trimTo(e, 80)})
	}
	code := q.Get("code")
	if code == "" || len(code) > 2048 {
		return fail(oauthFailed, nil)
	}
	d := dashboardOAuth()
	if d == nil {
		return fail(oauthOff, nil)
	}
	c, ok, err := d.Client(ctx, provider)
	if err != nil {
		return fail(oauthFailed, map[string]any{"error": trimTo(err.Error(), 200)})
	}
	if !ok {
		return fail(oauthOff, nil)
	}
	emails, err := oauthEmails(ctx, provider, c, code, s)
	if err != nil {
		return fail(oauthFailed, map[string]any{"error": trimTo(err.Error(), 200)})
	}
	if len(emails) == 0 {
		return fail(oauthUnverified, nil)
	}
	// The first verified address that belongs to someone on the box wins
	// (GitHub's primary address is tried first).
	var person *tokens.Person
	for _, email := range emails {
		if p, err := a.deps.Tokens.PersonByEmail(ctx, email); err == nil {
			person = p
			break
		}
	}
	if person == nil {
		return fail(oauthUnknown, nil)
	}
	secret, t, person, err := a.deps.Tokens.SessionFor(ctx, person.ID, provider)
	if err != nil {
		if errors.Is(err, tokens.ErrPersonNotFound) {
			return fail(oauthUnknown, nil)
		}
		return fail(oauthFailed, map[string]any{"error": trimTo(err.Error(), 200)})
	}
	if a.deps.DB != nil {
		_ = a.deps.DB.Audit(ctx, t.ID, "session.oauth", person.ID,
			map[string]any{"summary": person.Name + " signed in with " + name, "provider": provider, "ip": ip})
	}
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: secret, Path: "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteStrictMode, Expires: *t.ExpiresAt})
	device := ""
	if dc, err := r.Cookie(DeviceCookie); err == nil {
		device = dc.Value
	}
	if dc := a.signedIn(ctx, t.ID, person.ID, device, r.UserAgent(), ip, provider, name); dc != nil {
		http.SetCookie(w, dc)
	}
	// A page, not a redirect: the next request starts on the dashboard
	// itself, so the browser sends the SameSite=Strict session with it.
	next := html.EscapeString(SafeNext(s.Next))
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="color-scheme" content="light dark">`+
		`<meta http-equiv="refresh" content="0;url=%s"><title>Signing in · Tiffin</title></head>`+
		`<body><p>Signed in. <a href="%s">Open the dashboard</a>.</p></body></html>`, next, next)
	return true
}

func trimTo(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// oauthEmail exchanges the code (with the PKCE verifier) and returns the
// account's email and whether the provider verified it.
func oauthEmails(ctx context.Context, provider string, c OAuthClient, code string, s *oauthState) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.WithValue(ctx, oauth2.HTTPClient, oauthURLs.HTTP), 20*time.Second)
	defer cancel()
	tok, err := c.config(provider).Exchange(ctx, code, oauth2.VerifierOption(s.Verifier))
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	switch provider {
	case "google":
		email, verified, err := googleEmail(tok, c.ClientID, s.Nonce, time.Now())
		if err != nil || !verified {
			return nil, err
		}
		return []string{email}, nil
	case "github":
		return githubEmails(ctx, tok.AccessToken)
	}
	return nil, errors.New("unknown provider")
}

// googleEmail reads the ID token from Google's token endpoint. It came
// straight from Google over TLS for our client secret, so, as Google's
// OpenID Connect guide allows, the signature is not checked; issuer,
// audience, expiry and nonce are.
func googleEmail(tok *oauth2.Token, clientID, nonce string, now time.Time) (string, bool, error) {
	idt, _ := tok.Extra("id_token").(string)
	parts := strings.Split(idt, ".")
	if len(parts) != 3 {
		return "", false, errors.New("google sent no ID token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false, errors.New("google's ID token is not readable")
	}
	var cl struct {
		Iss           string          `json:"iss"`
		Aud           json.RawMessage `json:"aud"`
		Exp           int64           `json:"exp"`
		Nonce         string          `json:"nonce"`
		Email         string          `json:"email"`
		EmailVerified any             `json:"email_verified"`
	}
	if err := json.Unmarshal(raw, &cl); err != nil {
		return "", false, errors.New("google's ID token is not readable")
	}
	if cl.Iss != "https://accounts.google.com" && cl.Iss != "accounts.google.com" {
		return "", false, fmt.Errorf("ID token issuer %q is not Google", trimTo(cl.Iss, 60))
	}
	var aud []string
	if json.Unmarshal(cl.Aud, &aud) != nil {
		var one string
		_ = json.Unmarshal(cl.Aud, &one)
		aud = []string{one}
	}
	if len(aud) != 1 || aud[0] != clientID {
		return "", false, errors.New("the ID token is for another app")
	}
	if now.Unix() > cl.Exp {
		return "", false, errors.New("the ID token has expired")
	}
	if nonce == "" || subtle.ConstantTimeCompare([]byte(cl.Nonce), []byte(nonce)) != 1 {
		return "", false, errors.New("the ID token's nonce does not match")
	}
	verified := cl.EmailVerified == true || cl.EmailVerified == "true"
	return tokens.NormEmail(cl.Email), verified && cl.Email != "", nil
}

// githubEmails reads the account's verified addresses from /user/emails,
// the primary one first. GitHub's user tokens (8 hours when the app
// has expiring tokens on) are used for this one call and dropped.
func githubEmails(ctx context.Context, accessToken string) ([]string, error) {
	if accessToken == "" {
		return nil, errors.New("github sent no access token")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, oauthURLs.GitHubEmails, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	req.Header.Set("User-Agent", "tiffin")
	res, err := oauthURLs.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github emails: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github emails: %s", res.Status)
	}
	var list []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 256<<10)).Decode(&list); err != nil {
		return nil, fmt.Errorf("github emails: %w", err)
	}
	var out []string
	for _, e := range list {
		if !e.Verified || e.Email == "" {
			continue
		}
		if e.Primary {
			out = append([]string{tokens.NormEmail(e.Email)}, out...)
		} else {
			out = append(out, tokens.NormEmail(e.Email))
		}
	}
	return out, nil
}
