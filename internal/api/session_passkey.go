package api

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/passkeys"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
)

// Passkey sign-in: two unauthenticated, dashboard-only operations.
//
//	POST /v1/session/passkey/options  -> WebAuthn assertion options (discoverable, UV required)
//	POST /v1/session/passkey          -> {credential} -> session cookie + {person, name, role, expiresAt}
//
// Both are rate-limited per client IP. Two more confirm, inside a dashboard
// session, that the person is still at the keyboard ("sudo mode": creating a
// long-lived or full-access API key, or adding a passkey, needs a strong
// sign-in in the last 10 minutes):
//
//	POST /v1/session/confirm/options  -> WebAuthn assertion options
//	POST /v1/session/confirm          -> {credential} -> {confirmedUntil}

// PasskeySignInRate is how many calls per minute one client IP may make to
// each passkey sign-in operation.
const PasskeySignInRate = 10

// PasskeySignIn is who a passkey signed in.
type PasskeySignIn struct {
	Person    string    `json:"person" doc:"The person's ID (usr_...)"`
	Name      string    `json:"name" doc:"The person's name; the session is named after them"`
	Role      string    `json:"role" enum:"owner,admin,member,viewer" doc:"Their role; the session has exactly its power"`
	ExpiresAt time.Time `json:"expiresAt" doc:"When the session ends (12 hours, like a login link's)"`
}

type clientIPKey struct{}

func clientIPFrom(ctx context.Context) string { s, _ := ctx.Value(clientIPKey{}).(string); return s }

// clientIP is the caller's address. The API listens on loopback behind the
// edge, which replaces any client-sent X-Forwarded-For with the real address
// (Caddy trusts no proxies by default), so the last entry is used only when
// the request comes from loopback. IPv6 addresses count per /64.
func clientIP(remoteAddr, xff string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() && xff != "" {
		parts := strings.Split(xff, ",")
		if last := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(last) != nil {
			host = last
		}
	}
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
	}
	return host
}

// ipLimiter is a small in-memory token bucket per key.
type ipLimiter struct {
	mu       sync.Mutex
	perMin   float64
	capacity float64 // the burst; 0 means perMin
	buckets  map[string]*bucket
	now      func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newIPLimiter(perMin int) *ipLimiter {
	return &ipLimiter{perMin: float64(perMin), buckets: map[string]*bucket{}, now: time.Now}
}

// allow spends one token for key, or says how long until one is free.
func (l *ipLimiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	perSec := l.perMin / 60
	capacity := l.perMin
	if l.capacity > 0 {
		capacity = l.capacity
	}
	if len(l.buckets) > 10000 {
		for k, b := range l.buckets { // forget buckets that have refilled
			if b.tokens+now.Sub(b.last).Seconds()*perSec >= capacity {
				delete(l.buckets, k)
			}
		}
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: capacity, last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(capacity, b.tokens+now.Sub(b.last).Seconds()*perSec)
	b.last = now
	if b.tokens < 1 {
		return false, time.Duration((1 - b.tokens) / perSec * float64(time.Second))
	}
	b.tokens--
	return true, 0
}

// limitPerIP rejects callers over the limit with a rate_limited problem and
// puts their IP in the context for the audit log.
func (a *API) limitPerIP(l *ipLimiter, op string) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		ip := clientIP(ctx.RemoteAddr(), ctx.Header("X-Forwarded-For"))
		if ok, wait := l.allow(op + " " + ip); !ok {
			secs := int(math.Ceil(wait.Seconds()))
			p := problem(http.StatusTooManyRequests, "rate_limited", "too many passkey sign-in attempts from your address")
			p.Hint = "wait " + strconv.Itoa(secs) + "s and try again, or sign in with a link from `tiffin login`"
			ctx.SetHeader("Retry-After", strconv.Itoa(secs))
			ctx.SetHeader("Content-Type", "application/problem+json")
			ctx.SetStatus(http.StatusTooManyRequests)
			_ = json.NewEncoder(ctx.BodyWriter()).Encode(p)
			return
		}
		next(huma.WithValue(ctx, clientIPKey{}, ip))
	}
}

func signInProblem(err error) error {
	out := problem(401, "unauthenticated", err.Error())
	switch {
	case errors.Is(err, passkeys.ErrLoginBusy):
		out = problem(429, "rate_limited", err.Error())
	case errors.Is(err, passkeys.ErrLoginExpired), errors.Is(err, passkeys.ErrLoginFailed):
		out.Hint = "start again: POST /v1/session/passkey/options, then sign with the passkey within 2 minutes"
	case errors.Is(err, passkeys.ErrUnknownPasskey), errors.Is(err, passkeys.ErrCloned):
		out.Hint = "sign in with a one-time link (`tiffin login`, or ask an admin for one) and manage passkeys in Settings › Passkeys"
	case errors.Is(err, tokens.ErrPersonNotFound):
		out.Detail = "this person no longer has access to the box"
		out.Hint = "ask an owner or admin to invite you again"
	default:
		return toProblem(err)
	}
	return out
}

func (a *API) registerPasskeySignIn() {
	api := a.api
	optLimit, finLimit := newIPLimiter(PasskeySignInRate), newIPLimiter(PasskeySignInRate)

	o := op("session-passkey-options", http.MethodPost, "/v1/session/passkey/options", "-", RiskWrite, "Start signing in with a passkey",
		"Returns WebAuthn assertion options for a discoverable passkey (no username; user verification required) for "+
			"navigator.credentials.get(). The challenge is single use and expires after 2 minutes. Used by the dashboard's login page.", "system")
	o.Security = nil
	o.Errors = append(o.Errors, 429, 501)
	o.Middlewares = huma.Middlewares{a.limitPerIP(optLimit, "options")}
	huma.Register(api, o, wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body any }, error) {
		m, err := a.passkeysMgr()
		if err != nil {
			return nil, err
		}
		opts, err := m.BeginLogin(ctx)
		if err != nil {
			return nil, signInProblem(err)
		}
		return &struct{ Body any }{opts}, nil
	}))

	f := op("session-passkey", http.MethodPost, "/v1/session/passkey", "-", RiskWrite, "Sign in with a passkey",
		"Verifies the passkey assertion from navigator.credentials.get() against the passkeys people registered, then starts "+
			"the same dashboard session a login link gives (that person's role, 12 hours) and sets the session cookie. "+
			"Removed people cannot sign in. Used by the dashboard's login page.", "system")
	f.Security = nil
	f.Errors = append(f.Errors, 429, 501)
	f.Middlewares = huma.Middlewares{a.sameOriginOnly, a.limitPerIP(finLimit, "finish")}
	huma.Register(api, f, wrap(func(ctx context.Context, in *struct {
		Device string `cookie:"tiffin_device"`
		UA     string `header:"User-Agent"`
		Body   struct {
			Credential json.RawMessage `json:"credential" doc:"The PublicKeyCredential from navigator.credentials.get(), with byte fields base64url-encoded"`
		}
	}) (*struct {
		SetCookie []http.Cookie `header:"Set-Cookie"`
		Body      PasskeySignIn
	}, error) {
		m, err := a.passkeysMgr()
		if err != nil {
			return nil, err
		}
		if len(in.Body.Credential) == 0 || string(in.Body.Credential) == "null" {
			return nil, problem(422, "validation", "credential is required")
		}
		ip := clientIPFrom(ctx)
		who, err := m.FinishLogin(ctx, in.Body.Credential)
		if err != nil {
			return nil, signInProblem(err)
		}
		secret, t, person, err := a.deps.Tokens.SessionFor(ctx, who.Person, "passkey")
		if err != nil {
			if errors.Is(err, tokens.ErrPersonNotFound) {
				_ = a.deps.DB.Audit(ctx, who.Person, "session.passkey_refused", who.Person,
					map[string]any{"summary": "a removed person tried to sign in with a passkey", "passkey": who.PasskeyName, "passkeyId": who.PasskeyID, "ip": ip})
			}
			return nil, signInProblem(err)
		}
		_ = a.deps.DB.Audit(ctx, t.ID, "session.passkey", person.ID,
			map[string]any{"summary": person.Name + " signed in with a passkey", "passkey": who.PasskeyName, "passkeyId": who.PasskeyID, "ip": ip})
		out := &struct {
			SetCookie []http.Cookie `header:"Set-Cookie"`
			Body      PasskeySignIn
		}{Body: PasskeySignIn{Person: person.ID, Name: person.Name, Role: person.Role, ExpiresAt: *t.ExpiresAt}}
		out.SetCookie = []http.Cookie{{Name: SessionCookie, Value: secret, Path: "/", HttpOnly: true, Secure: true,
			SameSite: http.SameSiteStrictMode, Expires: *t.ExpiresAt}}
		if dc := a.signedIn(ctx, t.ID, person.ID, in.Device, in.UA, ip, tokens.MethodPasskey, "Passkey"); dc != nil {
			out.SetCookie = append(out.SetCookie, *dc)
		}
		return out, nil
	}))
}

// SessionConfirmed says until when the session counts as freshly signed in.
type SessionConfirmed struct {
	ConfirmedUntil time.Time `json:"confirmedUntil" doc:"Until then (10 minutes), this session may create long-lived and full-access API keys and add passkeys"`
}

func (a *API) registerSessionConfirm() {
	api := a.api
	optLimit, finLimit := newIPLimiter(PasskeySignInRate), newIPLimiter(PasskeySignInRate)
	onlySessions := func(ctx context.Context) error {
		if !PrincipalFrom(ctx).IsSession() {
			return problem(403, "forbidden", "only a dashboard session confirms it's you; API keys never need to")
		}
		return nil
	}

	o := op("session-confirm-options", http.MethodPost, "/v1/session/confirm/options", "-", RiskWrite, "Start confirming it's you",
		"Returns WebAuthn assertion options, like passkey sign-in, for confirming the person behind this dashboard session. "+
			"Dashboard sessions only. Used by the dashboard before creating a long-lived or full-access API key, or adding a passkey.", "system")
	o.Errors = append(o.Errors, 429, 501)
	o.Middlewares = huma.Middlewares{a.limitPerIP(optLimit, "confirm-options")}
	huma.Register(api, o, wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body any }, error) {
		if err := onlySessions(ctx); err != nil {
			return nil, err
		}
		m, err := a.passkeysMgr()
		if err != nil {
			return nil, err
		}
		opts, err := m.BeginLogin(ctx)
		if err != nil {
			return nil, signInProblem(err)
		}
		return &struct{ Body any }{opts}, nil
	}))

	f := op("session-confirm", http.MethodPost, "/v1/session/confirm", "-", RiskWrite, "Confirm it's you with a passkey",
		"Verifies a passkey of the person signed in to this dashboard session. For the next 10 minutes the session may create "+
			"API keys that last longer than a day or have full access, and add passkeys. Dashboard sessions only.", "system")
	f.Errors = append(f.Errors, 429, 501)
	f.Middlewares = huma.Middlewares{a.limitPerIP(finLimit, "confirm")}
	huma.Register(api, f, wrap(func(ctx context.Context, in *struct {
		Body struct {
			Credential json.RawMessage `json:"credential" doc:"The PublicKeyCredential from navigator.credentials.get(), with byte fields base64url-encoded"`
		}
	}) (*struct{ Body SessionConfirmed }, error) {
		if err := onlySessions(ctx); err != nil {
			return nil, err
		}
		m, err := a.passkeysMgr()
		if err != nil {
			return nil, err
		}
		if len(in.Body.Credential) == 0 || string(in.Body.Credential) == "null" {
			return nil, problem(422, "validation", "credential is required")
		}
		p := PrincipalFrom(ctx)
		who, err := m.FinishLogin(ctx, in.Body.Credential)
		if err != nil {
			return nil, signInProblem(err)
		}
		until, err := a.deps.Tokens.ConfirmSession(ctx, p, who.Person)
		if err != nil {
			if errors.Is(err, tokens.ErrForbidden) {
				out := problem(403, "forbidden", "that passkey belongs to someone else on this box")
				out.Hint = "use one of your own passkeys, or sign in again"
				return nil, out
			}
			return nil, err
		}
		_ = a.deps.DB.Audit(ctx, p.TokenID, "session.confirm", p.Person,
			map[string]any{"summary": orDefault(p.PersonName, p.Name) + " confirmed it was them with a passkey", "passkey": who.PasskeyName, "passkeyId": who.PasskeyID, "ip": clientIPFrom(ctx)})
		return &struct{ Body SessionConfirmed }{SessionConfirmed{ConfirmedUntil: until}}, nil
	}))
}
