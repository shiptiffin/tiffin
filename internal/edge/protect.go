package edge

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	// Protection modules compiled into the edge.
	_ "github.com/corazawaf/coraza-caddy/v2"
	_ "github.com/hslatman/caddy-crowdsec-bouncer/crowdsec"
	_ "github.com/hslatman/caddy-crowdsec-bouncer/http"
	_ "github.com/mholt/caddy-ratelimit"
)

// Protection is the edge's protection layer: per-IP rate limits, the
// proof-of-work challenge, the CrowdSec bouncer and the opt-in WAF. The
// protect module builds it; see SetProtectionSource.
//
// Handlers run in this order on every HTTPS request, before routing:
// CrowdSec (banned IPs get 403), rate limits (429 with Retry-After), the
// challenge (on the hosts it covers), then the WAF.
type Protection struct {
	// App limits each client IP on every host except the dashboard.
	App Limit
	// Auth limits each client IP's POST/PUT/PATCH requests to AuthPaths on
	// those hosts. It applies on top of App.
	Auth Limit
	// Dashboard limits each client IP on the dashboard host. Keep it
	// generous: the owner must never be locked out of their own box.
	Dashboard Limit
	// AuthPaths are Caddy path patterns ("/login", "/api/auth/*"). Empty
	// means DefaultAuthPaths.
	AuthPaths []string
	// Challenge puts the proof-of-work challenge in front of some hosts. Nil: off.
	Challenge *Challenge
	// CrowdSec enforces the local CrowdSec engine's decisions. Nil: off.
	CrowdSec *CrowdSec
	// WAF turns on the Coraza web application firewall with the OWASP core
	// rule set on every host except the dashboard.
	WAF bool
}

// Limit is a sliding-window rate limit per client IP (IPv6 per /64).
// Events 0 turns the limit off.
type Limit struct {
	Events int
	Window time.Duration
}

// Challenge configures the proof-of-work challenge.
type Challenge struct {
	// Hosts that get the challenge. "*" means every host except the dashboard.
	Hosts []string
	// Secret signs challenges and clearance cookies (at least 32 bytes).
	Secret []byte
	// Difficulty is the number of leading zero bits the SHA-256 proof needs
	// (8–24; 16 takes a phone well under a second).
	Difficulty int
	// TTL is how long a clearance cookie lasts. Default 24h.
	TTL time.Duration
}

// CrowdSec points the bouncer at the local CrowdSec API.
type CrowdSec struct {
	APIURL string // e.g. "http://127.0.0.1:7422/"
	APIKey string // a bouncer key (cscli bouncers add)
	// Every is how often new decisions are pulled. Default 5s.
	Every time.Duration
}

// DefaultAuthPaths are the sign-in, sign-up and password-reset endpoints
// common web stacks use (Better Auth, NextAuth, Rails, Django, Laravel...).
var DefaultAuthPaths = []string{
	// Better Auth (Tiffin's engine): only the credential endpoints, never
	// session or organization calls, which normal app use makes often.
	"/api/auth/sign-in/*", "/api/auth/sign-up/*", "/api/auth/request-password-reset",
	"/api/auth/reset-password", "/api/auth/email-otp/*", "/api/auth/magic-link/*",
	"/api/auth/two-factor/verify-*", "/api/auth/forget-password",
	"/login", "/login/*", "/signin", "/signin/*", "/sign-in", "/sign-in/*",
	"/signup", "/sign-up", "/register",
	"/password/*", "/forgot-password", "/reset-password", "/password-reset", "/password-reset/*",
	"/users/sign_in", "/users/password", "/accounts/login/", "/wp-login.php", "/xmlrpc.php",
}

// ChallengePath is where the challenge page posts its proof. It is served
// on every challenged host and never reaches the app.
const ChallengePath = "/.tiffin/challenge"

const (
	zoneApp       = "tiffin_app"
	zoneAuth      = "tiffin_auth"
	zoneDashboard = "tiffin_dashboard"
)

var (
	protectMu     sync.Mutex
	protectSource func() *Protection
	protectErr    error
	protectActive *Protection
)

// SetProtectionSource registers the function the edge asks for the current
// Protection on every Start and Reload of a Config without one. The protect
// module registers it and reloads the edge (Platform.RefreshRoutes) when the
// settings change. nil unregisters it.
func SetProtectionSource(fn func() *Protection) {
	protectMu.Lock()
	defer protectMu.Unlock()
	protectSource = fn
}

// ProtectionStatus reports the protection the edge is enforcing now (nil:
// none) and, when Caddy refused the protected config and the edge fell
// back to serving without it, why.
func ProtectionStatus() (*Protection, error) {
	protectMu.Lock()
	defer protectMu.Unlock()
	return protectActive, protectErr
}

// loadProtected loads c, with the registered protection layer when c has
// none of its own. If Caddy refuses the protected config, it loads c
// unprotected so the box stays reachable, and records the error for
// ProtectionStatus.
func loadProtected(c Config) error {
	protectMu.Lock()
	src := protectSource
	protectMu.Unlock()
	if c.Protect == nil && src != nil {
		if p := src(); p != nil {
			pc := c
			pc.Protect = p
			err := pc.Protect.validate()
			if err == nil {
				if err = safeLoad(pc); err == nil {
					setProtectState(p, nil)
					return nil
				}
			}
			setProtectState(nil, fmt.Errorf("edge: protection not applied: %w", err))
			return load(c)
		}
	}
	err := load(c)
	if err == nil {
		setProtectState(c.Protect, nil)
	}
	return err
}

// safeLoad is load that turns a panic in a third-party Caddy module's
// setup into an error instead of taking the box down.
func safeLoad(c Config) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("edge: caddy module panicked while loading: %v", r)
		}
	}()
	return load(c)
}

func setProtectState(p *Protection, err error) {
	protectMu.Lock()
	defer protectMu.Unlock()
	protectActive, protectErr = p, err
}

func (p *Protection) validate() error {
	for name, l := range map[string]Limit{"app": p.App, "auth": p.Auth, "dashboard": p.Dashboard} {
		if l.Events < 0 {
			return fmt.Errorf("edge: %s limit: events must not be negative", name)
		}
		if l.Events > 0 && l.Window < time.Second {
			return fmt.Errorf("edge: %s limit: window must be at least 1s", name)
		}
	}
	for _, path := range p.AuthPaths {
		if !strings.HasPrefix(path, "/") {
			return fmt.Errorf("edge: auth path %q must start with /", path)
		}
	}
	if ch := p.Challenge; ch != nil {
		if len(ch.Secret) < 32 {
			return errors.New("edge: challenge secret must be at least 32 bytes")
		}
		if ch.Difficulty < minDifficulty || ch.Difficulty > maxDifficulty {
			return fmt.Errorf("edge: challenge difficulty %d: want %d–%d", ch.Difficulty, minDifficulty, maxDifficulty)
		}
		if len(ch.Hosts) == 0 {
			return errors.New("edge: challenge needs at least one host (or \"*\")")
		}
		for _, h := range ch.Hosts {
			if h == "*" {
				continue
			}
			if err := validHost(strings.ToLower(h)); err != nil || strings.Contains(h, "*") {
				return fmt.Errorf("edge: challenge host %q is not a host name", h)
			}
		}
	}
	if cs := p.CrowdSec; cs != nil {
		// Checked here because the bouncer panics when its own setup fails.
		u, err := url.Parse(cs.APIURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || cs.APIKey == "" {
			return errors.New("edge: crowdsec needs an http(s) API URL and a bouncer key")
		}
	}
	return nil
}

func (p *Protection) authPaths() []string {
	if len(p.AuthPaths) > 0 {
		return p.AuthPaths
	}
	return DefaultAuthPaths
}

// clientKey is the rate-limit key: the client IP Caddy resolved.
const clientKey = "{http.vars.client_ip}"

// zoneName includes the limit: caddy-ratelimit keeps a zone's counters
// across reloads by name, and resizing a live zone upward keeps blocking
// until the old window passes. A new limit gets fresh counters instead.
func zoneName(base string, l Limit) string {
	return fmt.Sprintf("%s_%d_%s", base, l.Events, l.Window)
}

func zone(match []obj, l Limit) obj {
	return obj{
		"match":       match,
		"key":         clientKey,
		"window":      l.Window.String(),
		"max_events":  l.Events,
		"ipv6_prefix": 64,
	}
}

// protectRoutes are the non-terminal routes that run before host routing.
func (p *Protection) protectRoutes(c Config) []obj {
	dash := c.DashboardHost()
	notDash := obj{"not": []obj{{"host": []string{dash}}}}
	var routes []obj
	if p.CrowdSec != nil {
		routes = append(routes, obj{"handle": []obj{{"handler": "crowdsec"}}})
	}
	zones := obj{}
	if p.Dashboard.Events > 0 {
		zones[zoneName(zoneDashboard, p.Dashboard)] = zone([]obj{{"host": []string{dash}}}, p.Dashboard)
	}
	if p.App.Events > 0 {
		zones[zoneName(zoneApp, p.App)] = zone([]obj{notDash}, p.App)
	}
	if p.Auth.Events > 0 {
		m := obj{"method": []string{"POST", "PUT", "PATCH"}, "path": p.authPaths(), "not": notDash["not"]}
		zones[zoneName(zoneAuth, p.Auth)] = zone([]obj{m}, p.Auth)
	}
	if len(zones) > 0 {
		routes = append(routes, obj{"handle": []obj{{"handler": "rate_limit", "rate_limits": zones}}})
	}
	if ch := p.Challenge; ch != nil {
		var match []obj
		if hasStar(ch.Hosts) {
			match = []obj{notDash}
		} else {
			hosts := make([]string, 0, len(ch.Hosts))
			for _, h := range ch.Hosts {
				if h = strings.ToLower(h); h != dash {
					hosts = append(hosts, h)
				}
			}
			sort.Strings(hosts)
			match = []obj{{"host": hosts}}
		}
		if len(match) > 0 && (hasStar(ch.Hosts) || len(match[0]["host"].([]string)) > 0) {
			ttl := ch.TTL
			if ttl <= 0 {
				ttl = 24 * time.Hour
			}
			routes = append(routes, obj{"match": match, "handle": []obj{{
				"handler":    "tiffin_challenge",
				"secret":     hex.EncodeToString(ch.Secret),
				"difficulty": ch.Difficulty,
				"ttl":        ttl.String(),
			}}})
		}
	}
	if p.WAF {
		routes = append(routes, obj{"match": []obj{notDash}, "handle": []obj{wafHandler()}})
	}
	return routes
}

func hasStar(hosts []string) bool {
	for _, h := range hosts {
		if h == "*" {
			return true
		}
	}
	return false
}

// errorRoutes render the protection layer's refusals as small pages.
func (p *Protection) errorRoutes() []obj {
	page := func(status int, body string) obj {
		return obj{
			"handler":     "static_response",
			"status_code": status,
			"headers": obj{
				"Content-Type":            []string{"text/html; charset=utf-8"},
				"Cache-Control":           []string{"no-store"},
				"Content-Security-Policy": []string{pageCSP("")},
				"X-Content-Type-Options":  []string{"nosniff"},
			},
			"body": body,
		}
	}
	return []obj{
		{
			"match":    []obj{{"expression": "{http.error.status_code} == 429"}},
			"handle":   []obj{page(429, tooManyPage)},
			"terminal": true,
		},
		{
			"match":    []obj{{"expression": "{http.error.status_code} == 403 && {http.error.message} == 'banned by crowdsec'"}},
			"handle":   []obj{page(403, blockedPage)},
			"terminal": true,
		},
		{
			"match":    []obj{{"expression": "{http.error.status_code} == 403 && {http.error.message} == 'interruption triggered'"}},
			"handle":   []obj{page(403, wafPage)},
			"terminal": true,
		},
	}
}

func (p *Protection) apps() obj {
	if p.CrowdSec == nil {
		return nil
	}
	every := p.CrowdSec.Every
	if every <= 0 {
		every = 5 * time.Second
	}
	api := p.CrowdSec.APIURL
	if !strings.HasSuffix(api, "/") {
		api += "/"
	}
	return obj{"crowdsec": obj{
		"api_url":            api,
		"api_key":            p.CrowdSec.APIKey,
		"ticker_interval":    every.String(),
		"enable_streaming":   true,
		"enable_hard_fails":  false, // CrowdSec down must never take the edge down
		"enable_caddy_error": true,  // refusals go through errorRoutes
	}}
}

// wafDirectives run Coraza with the OWASP core rule set (embedded in the
// binary) at paranoia level 1, blocking. Response bodies are not inspected.
const wafDirectives = `Include @coraza.conf-recommended
Include @crs-setup.conf.example
Include @owasp_crs/*.conf
SecRuleEngine On
SecResponseBodyAccess Off
SecRequestBodyLimit 13107200
SecRequestBodyNoFilesLimit 1048576`

func wafHandler() obj {
	return obj{"handler": "waf", "load_owasp_crs": true, "directives": wafDirectives}
}
