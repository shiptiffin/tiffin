package edge

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// The deploy-address gate. Every production deploy has an address of its
// own (d-<id>--<app>.<apps domain>, see the runtime); by default only people
// signed in to the box's dashboard may open one.
//
// The dashboard's session cookie (__Host-tiffin_session) belongs to the
// dashboard's host alone, and the apps domain is often another domain
// altogether (dashboard.example.com beside *.example.app), so the gate
// cannot read it. Instead:
//
//  1. A request without a valid gate cookie is sent (302, GET and HEAD
//     only; other methods get a 401) to the dashboard's /gate page with
//     the host and path it asked for.
//  2. The dashboard signs the person in if needed, checks they may read
//     the project, and asks the API for a hand-off link
//     (POST /v1/deploy-link): https://<host>/.tiffin/gate?t=<token>&next=<path>.
//     The token is HMAC-signed with a secret only the control plane and
//     the edge hold, bound to that one host, valid for a minute and good
//     for one use.
//  3. Here, at GatePath, the edge checks the token and sets a host-only
//     cookie (__Host-: Secure, Path=/, no Domain, HttpOnly, SameSite=Lax),
//     signed the same way and bound to the host, for an hour, then
//     redirects to the path. The cookie never reaches the app.
//
// No dashboard credential ever travels to an app host: an old version's
// code (which runs on that host) can at most see its own host's gate
// cookie, which opens nothing else. A link that leaks is dead after a
// minute or its first use. Signing out of the dashboard does not end a
// gate cookie early: it lasts its hour.

// GatePath is where a hand-off link lands on a deploy address. It never
// reaches the app.
const GatePath = "/.tiffin/gate"

const (
	gateCookie    = "__Host-tiffin-deploy"
	gateCookieTTL = time.Hour
	// HandoffTTL is how long a hand-off link works.
	HandoffTTL = time.Minute
)

// Gate holds what the gate signs with. The runtime registers its source
// (SetGateSource); a Config may carry its own.
type Gate struct {
	// Secret signs hand-off tokens and gate cookies (at least 32 bytes).
	Secret []byte
}

var (
	gateMu     sync.Mutex
	gateSource func() *Gate
)

// SetGateSource registers the function the edge asks for the gate's
// secret on every Start and Reload of a Config without one. nil
// unregisters it.
func SetGateSource(fn func() *Gate) {
	gateMu.Lock()
	defer gateMu.Unlock()
	gateSource = fn
}

func currentGate() *Gate {
	gateMu.Lock()
	src := gateSource
	gateMu.Unlock()
	if src == nil {
		return nil
	}
	return src()
}

func gateSign(key []byte, kind, host, payload string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("tiffin-gate\n" + kind + "\n" + host + "\n" + payload))
	return hex.EncodeToString(m.Sum(nil)[:16])
}

// HandoffToken returns a one-use token for host, valid for HandoffTTL:
// "1.<expiry>.<nonce>.<signature>".
func HandoffToken(secret []byte, host string, now time.Time) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	payload := fmt.Sprintf("1.%d.%s", now.Add(HandoffTTL).Unix(), hex.EncodeToString(b[:]))
	return payload + "." + gateSign(secret, "handoff", strings.ToLower(host), payload)
}

// checkHandoff returns a valid token's nonce and expiry.
func checkHandoff(secret []byte, tok, host string, now time.Time) (string, int64, error) {
	if len(tok) > 128 {
		return "", 0, errors.New("malformed token")
	}
	i := strings.LastIndexByte(tok, '.')
	if i < 0 {
		return "", 0, errors.New("malformed token")
	}
	payload, sig := tok[:i], tok[i+1:]
	if !hmac.Equal([]byte(sig), []byte(gateSign(secret, "handoff", host, payload))) {
		return "", 0, errors.New("bad signature")
	}
	f := strings.Split(payload, ".")
	if len(f) != 3 || f[0] != "1" {
		return "", 0, errors.New("malformed token")
	}
	exp, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil || now.Unix() >= exp || exp > now.Add(HandoffTTL+time.Minute).Unix() {
		return "", 0, errors.New("expired token")
	}
	return f[2], exp, nil
}

// usedNonces keeps hand-off tokens one-use. It lives in the process, so a
// config reload keeps it; an edge restart within a token's minute forgets
// it (the token still only opens its own host).
var usedNonces = struct {
	sync.Mutex
	m map[string]int64
}{m: map[string]int64{}}

// useNonce reports whether nonce was unused, and marks it used until exp.
func useNonce(nonce string, exp int64, now time.Time) bool {
	usedNonces.Lock()
	defer usedNonces.Unlock()
	for n, e := range usedNonces.m {
		if e < now.Unix() {
			delete(usedNonces.m, n)
		}
	}
	if _, used := usedNonces.m[nonce]; used {
		return false
	}
	usedNonces.m[nonce] = exp
	return true
}

func init() { caddy.RegisterModule(GateHandler{}) }

// GateHandler is the Caddy module http.handlers.tiffin_gate: it lets
// through requests with a valid gate cookie for their host and sends the
// rest to the dashboard to sign in.
type GateHandler struct {
	Secret string `json:"secret"`  // hex, at least 32 bytes
	SignIn string `json:"sign_in"` // the dashboard's hand-off page (https://dashboard.<domain>/gate)

	key []byte
	now func() time.Time
}

// CaddyModule returns the Caddy module information.
func (GateHandler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.tiffin_gate", New: func() caddy.Module { return new(GateHandler) }}
}

// Provision decodes the secret.
func (h *GateHandler) Provision(caddy.Context) error {
	key, err := hex.DecodeString(h.Secret)
	if err != nil || len(key) < 32 {
		return errors.New("tiffin_gate: secret must be at least 32 bytes of hex")
	}
	if u, err := url.Parse(h.SignIn); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("tiffin_gate: sign_in %q is not an http(s) URL", h.SignIn)
	}
	h.key = key
	if h.now == nil {
		h.now = time.Now
	}
	return nil
}

func requestHost(r *http.Request) string {
	host := strings.ToLower(r.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return host
}

// ServeHTTP lets signed-in visitors through, completes hand-offs at
// GatePath and sends everyone else to sign in.
func (h *GateHandler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	host := requestHost(r)
	hdr := w.Header()
	if r.URL.Path == GatePath {
		hdr.Set("Cache-Control", "no-store")
		hdr.Set("Referrer-Policy", "no-referrer") // the token is in this URL
		q := r.URL.Query()
		to := gateNext(q.Get("next"))
		nonce, exp, err := checkHandoff(h.key, q.Get("t"), host, h.now())
		if err == nil && !useNonce(nonce, exp, h.now()) {
			err = errors.New("used token")
		}
		if err != nil {
			h.page(w, r, http.StatusForbidden, "That sign-in link has expired or was used already.", h.signInURL(host, to))
			return nil
		}
		exp = h.now().Add(gateCookieTTL).Unix()
		payload := "1." + strconv.FormatInt(exp, 10)
		http.SetCookie(w, &http.Cookie{
			Name:     gateCookie,
			Value:    base64.RawURLEncoding.EncodeToString([]byte(payload + "." + gateSign(h.key, "cookie", host, payload))),
			Path:     "/",
			MaxAge:   int(gateCookieTTL.Seconds()),
			Secure:   true,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
		http.Redirect(w, r, to, http.StatusSeeOther)
		return nil
	}
	if c, err := r.Cookie(gateCookie); err == nil && h.valid(c.Value, host) {
		stripCookie(r, gateCookie)
		return next.ServeHTTP(w, r)
	}
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("X-Robots-Tag", "noindex")
	to := h.signInURL(host, gateNext(r.URL.RequestURI()))
	accept := r.Header.Get("Accept")
	if (r.Method == http.MethodGet || r.Method == http.MethodHead) && !(strings.Contains(accept, "application/json") && !strings.Contains(accept, "text/html")) {
		http.Redirect(w, r, to, http.StatusFound)
		return nil
	}
	hdr.Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	b := fmt.Sprintf(`{"code":"sign_in_required","detail":"This address shows an earlier version of the app to people signed in to the box's dashboard. Open it in a browser to sign in, or get a link with tiffin deploys link.","signIn":%q}`+"\n", to)
	_, _ = w.Write([]byte(b))
	return nil
}

func (h *GateHandler) signInURL(host, next string) string {
	return strings.TrimRight(h.SignIn, "/") + "?host=" + url.QueryEscape(host) + "&next=" + url.QueryEscape(next)
}

// valid checks a gate cookie for host.
func (h *GateHandler) valid(value, host string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return false
	}
	s := string(raw)
	i := strings.LastIndexByte(s, '.')
	if i < 0 {
		return false
	}
	payload, sig := s[:i], s[i+1:]
	if !hmac.Equal([]byte(sig), []byte(gateSign(h.key, "cookie", host, payload))) {
		return false
	}
	ver, expS, ok := strings.Cut(payload, ".")
	exp, err := strconv.ParseInt(expS, 10, 64)
	return ok && ver == "1" && err == nil && h.now().Unix() < exp
}

// gateNext keeps the redirect on this host, away from GatePath.
func gateNext(next string) string {
	next = safeNext(next)
	if next == GatePath || strings.HasPrefix(next, GatePath+"?") || strings.HasPrefix(next, GatePath+"/") {
		return "/"
	}
	return next
}

// stripCookie removes one cookie from a request, so the app never sees it.
func stripCookie(r *http.Request, name string) {
	cs := r.Cookies()
	r.Header.Del("Cookie")
	for _, c := range cs {
		if c.Name != name {
			r.AddCookie(c)
		}
	}
}

func (h *GateHandler) page(w http.ResponseWriter, r *http.Request, status int, msg, signIn string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", pageCSP(""))
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Sign in again</title>`+
		`<meta name="viewport" content="width=device-width,initial-scale=1"><meta name="robots" content="noindex"></head>`+
		`<body style="font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem">`+
		`<h1>Sign in again</h1><p>%s</p><p><a href="%s">Sign in to open this version</a></p></body></html>`,
		html.EscapeString(msg), html.EscapeString(signIn))
}

var (
	_ caddy.Provisioner           = (*GateHandler)(nil)
	_ caddyhttp.MiddlewareHandler = (*GateHandler)(nil)
)
