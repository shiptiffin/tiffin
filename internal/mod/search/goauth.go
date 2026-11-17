package search

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/shiptiffin/tiffin/internal/api"
)

// Connect with Google. A box admin presses the button in Settings › Search
// engines; Google asks them to let the box's OAuth app (gsignin: its own
// Google sign-in keys, or ShipTiffin's app on a managed box) see and manage
// their Search Console and verify their sites; the browser comes back to
// the box's one callback URL, the same one sign-in uses (on a managed box
// by way of the control plane), and the box keeps the refresh token, sealed.
//
//	POST /v1/search/google/connect      -> {url}; sets the state cookie
//	GET  /api/auth/callback/google      -> stores the connection, then Settings
//
// It is the authorization code flow with PKCE (S256). The callback arrives
// from another site, so the dashboard's session cookie isn't with it: the
// state, made for a signed-in admin and kept here until it is used once,
// says who connects, and a cookie holding the same state says it is the
// browser that started.

const (
	// GoogleStatePrefix marks this flow's states at the shared callback.
	GoogleStatePrefix = "tgsc."
	// __Host- keeps the box's apps (same site, other subdomains) from
	// planting one.
	googleStateCookie = "__Host-tiffin_gsc"
	googleStateTTL    = 10 * time.Minute
	// googleSettings is where the callback sends the browser.
	googleSettings = "/settings/search"
)

// gpending is a connection someone started.
type gpending struct {
	verifier string
	by       string // the person's name
	tokenID  string
	session  string
	exp      time.Time
}

func randString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// startGoogle returns Google's consent page for the box's app and the state
// to put in the browser's cookie.
func (m *Module) startGoogle(ctx context.Context, by, tokenID, session string) (authURL, state string, err error) {
	app, err := m.signin(ctx)
	if errors.Is(err, errNoGoogleKeys) {
		pb := api.NewProblem(409, "precondition", "this box has no Google sign-in keys yet, and Connect with Google uses them")
		pb.Hint = "add them once in Box settings › Sign-in providers (tiffin auth providers set google), then connect"
		return "", "", pb
	} else if err != nil {
		return "", "", err
	}
	state = GoogleStatePrefix + randString(24)
	verifier := randString(48)
	sum := sha256.Sum256([]byte(verifier))
	now := m.now()
	m.pendMu.Lock()
	if m.pending == nil {
		m.pending = map[string]gpending{}
	}
	for k, p := range m.pending {
		if now.After(p.exp) || len(m.pending) > 50 {
			delete(m.pending, k)
		}
	}
	m.pending[state] = gpending{verifier: verifier, by: by, tokenID: tokenID, session: session, exp: now.Add(googleStateTTL)}
	m.pendMu.Unlock()
	authURL, err = app.consent(ctx, state, base64.RawURLEncoding.EncodeToString(sum[:]))
	if err != nil {
		m.pendMu.Lock()
		delete(m.pending, state)
		m.pendMu.Unlock()
		var ge *GoogleError
		if errors.As(err, &ge) {
			return "", "", api.NewProblem(409, "precondition", strings.TrimPrefix(ge.Message, "sign-in refused: "))
		}
		return "", "", api.NewProblem(502, "upstream", err.Error())
	}
	return authURL, state, nil
}

// Why a connection did not happen; Settings › Search engines reads them
// from ?google=<code> (and the last one's words from the connection).
const (
	gcConnected = "connected"
	gcDenied    = "denied"  // the person said no at Google
	gcScopes    = "scopes"  // they left a permission unticked
	gcExpired   = "expired" // state missing, used, too old or another browser's
	gcFailed    = "failed"  // Google's answer was wrong, or an API is off
)

// googleCallback finishes a connection: it gets the callbacks at the box's
// Google callback URL whose state is this flow's.
func (m *Module) googleCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer") // the URL carries the code
	h.Set("X-Content-Type-Options", "nosniff")
	http.SetCookie(w, &http.Cookie{Name: googleStateCookie, Value: "", Path: "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteLaxMode, MaxAge: -1})
	// A page, not a redirect: the next request starts on the dashboard
	// itself, so the browser sends its SameSite=Strict session with it.
	done := func(code, why string) {
		if code != gcConnected {
			m.noteTry(ctx, why)
		}
		next := googleSettings + "?google=" + code
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="color-scheme" content="light dark">`+
			`<meta http-equiv="refresh" content="0;url=%s"><title>Google · Tiffin</title></head>`+
			`<body><p><a href="%s">Back to Search engines</a>.</p></body></html>`, next, next)
	}
	q := r.URL.Query()
	state := q.Get("state")
	ck, err := r.Cookie(googleStateCookie)
	if r.Method != http.MethodGet || err != nil || subtle.ConstantTimeCompare([]byte(ck.Value), []byte(state)) != 1 {
		done(gcExpired, "")
		return
	}
	m.pendMu.Lock()
	p, ok := m.pending[state]
	delete(m.pending, state) // each state works once
	m.pendMu.Unlock()
	if !ok || m.now().After(p.exp) {
		done(gcExpired, "")
		return
	}
	if e := q.Get("error"); e != "" {
		if e == "access_denied" {
			done(gcDenied, "")
			return
		}
		done(gcFailed, "Google answered: "+trim(e, 80))
		return
	}
	code := q.Get("code")
	if code == "" || len(code) > 4096 {
		done(gcFailed, "Google sent no code.")
		return
	}
	app, err := m.signin(ctx)
	if err != nil {
		done(gcFailed, err.Error())
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	tok, err := app.exchange(cctx, code, p.verifier)
	if err != nil {
		done(gcFailed, err.Error())
		return
	}
	// Google lets people untick single permissions; the box needs both. A
	// grant the box doesn't keep is dropped, never revoked: revoking one
	// token ends everything the person granted the app, a working
	// connection on this or another box and their sign-ins included.
	granted := " " + tok.Scope + " "
	if !strings.Contains(granted, " "+gScopeSearch+" ") || !strings.Contains(granted, " "+gScopeVerify+" ") {
		done(gcScopes, "")
		return
	}
	if tok.RefreshToken == "" {
		done(gcFailed, "Google gave no lasting access. Try again.")
		return
	}
	email := tok.Email
	if email == "" {
		email = idTokenEmail(tok.IDToken)
	}
	c, err := m.connectGoogle(cctx, tok.RefreshToken, email, p.by)
	if err != nil {
		done(gcFailed, err.Error())
		return
	}
	_ = m.p.DB.Audit(ctx, p.tokenID, "search.google.connect", c.Email, map[string]any{"session": p.session,
		"summary": p.by + " connected Google Search Console as " + c.Email})
	done(gcConnected, "")
}

// noteTry keeps why the last try to connect failed, for Settings to show
// ("" forgets it).
func (m *Module) noteTry(ctx context.Context, why string) {
	if why == "" {
		_ = m.p.DB.KVDelete(ctx, kvNS, "google-try")
		return
	}
	_ = m.p.DB.KVPut(ctx, kvNS, "google-try", []byte(trim(why, 600)))
}

// idTokenEmail reads the account's address from Google's ID token. It came
// straight from Google's token endpoint over TLS, and the address is only
// shown ("connected as"), so the signature is not checked.
func idTokenEmail(idt string) string {
	parts := strings.Split(idt, ".")
	if len(parts) != 3 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var cl struct {
		Email string `json:"email"`
	}
	_ = json.Unmarshal(raw, &cl)
	return cl.Email
}

func trim(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
