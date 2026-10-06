package edge

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Config describes the platform edge: one domain, one upstream and any extra
// host routes.
type Config struct {
	Domain    string // e.g. "tiffin.localhost"; dashboard lives at Dashboard+"."+Domain
	Dashboard string // the dashboard's first-level name; default "dashboard"
	// Apps is the domain of the one-label app hosts ("shop.<Apps>") when it
	// is not Domain (e.g. example.app beside example.com). Empty: Domain.
	Apps string
	// DashboardURL is the dashboard as people reach it (the box's public
	// URL, e.g. https://dashboard.tiffin.localhost:8475 on a VM whose 8443
	// is forwarded to 8475), linked from the "Nothing here" page. Default:
	// https://<dashboard host>[:HTTPSPort].
	DashboardURL string
	Upstream     string  // tiffin API/dashboard http address, e.g. "127.0.0.1:7070"
	DataDir      string  // Caddy storage (certs, CA), e.g. /var/lib/tiffin/platform/caddy
	HTTPPort     int     // default 80
	HTTPSPort    int     // default 443
	Internal     bool    // true: Caddy internal CA (local/dev); false: public ACME (see ACME)
	Routes       []Route // extra host -> upstream routes
	AccessLog    string  // file for JSON access logs (rolled); empty disables them
	// ACME configures public certificates; required when Internal is false.
	ACME *ACME
	// Aliases are earlier box and apps domains still served while a domain
	// switch completes: the dashboard and every one-label host under the
	// apps domain also answer under each alias ("shop.<alias>"). Nil: the ones the registered
	// cert source gives (see SetCertSource).
	Aliases []string
	// HSTS is the Strict-Transport-Security max-age. 0 means the default:
	// 5 minutes with the internal CA, 30 days with a public CA, off with
	// any other ACME CA (a test CA's certificates are not trusted, so
	// pinning HTTPS would only lock browsers out). Negative turns it off.
	HSTS time.Duration
	// Protect is the protection layer (rate limits, challenge, CrowdSec,
	// WAF). Nil: the one registered with SetProtectionSource, if any.
	Protect *Protection

	paths []Route // routes without a host (see Route.Host), split out by normalized
}

// Route sends requests for Host (and optionally a path prefix) somewhere:
// one upstream, several (load-balanced, least connections) or a directory
// of static files.
//
// Text responses are compressed (zstd or gzip) unless NoCompress is set.
// Static files are revalidated on every use (Cache-Control: no-cache),
// except fingerprinted build assets, which are cached for a year (see
// hashedAsset).
type Route struct {
	// Host is the route's host. Empty: a box path, served on every host
	// but the dashboard's ahead of its own routes (PathPrefix and an
	// upstream required), such as /_tiffin/vitals.
	Host       string
	PathPrefix string   // e.g. "/api"; empty matches every path. Longer prefixes win.
	Upstream   string   // host:port
	Upstreams  []string // several host:ports; overrides Upstream
	FileRoot   string   // serve static files from this directory instead of proxying
	SPA        bool     // with FileRoot: unknown paths serve index.html
	// NoCompress passes responses through as the upstream sent them: for
	// the S3 gateway, whose clients check lengths and ETags.
	NoCompress bool
	// NextCache marks a Next.js app. Next.js sends prerendered and ISR
	// pages with "s-maxage=N" for Vercel's CDN, which a deploy purges; no
	// CDN here does, and a proxy in front (Cloudflare) would keep the page
	// past the next deploy. Those exact values become what Vercel sends
	// browsers: "public, max-age=0, must-revalidate". A Cache-Control the
	// app wrote itself is left alone.
	NextCache bool
	// RedirectTo permanently redirects (308, path and query kept) to this
	// host over HTTPS instead of serving: "www.example.com" → "example.com".
	RedirectTo string
	// Rules are the site's own headers, redirects and rewrites (rewrites,
	// cleanUrls and trailingSlash apply to file roots only).
	Rules *Rules `json:",omitempty"`
}

func (r Route) upstreams() []string {
	if len(r.Upstreams) > 0 {
		return r.Upstreams
	}
	if r.Upstream != "" {
		return []string{r.Upstream}
	}
	return nil
}

// ErrACMERequired is returned when Internal is false and no ACME settings
// say where public certificates come from.
var ErrACMERequired = errors.New("edge: Internal is false but ACME is not set; public certificates need ACME settings")

const (
	caID     = "tiffin"
	caName   = "Tiffin Local CA"
	cspValue = "frame-ancestors 'none'"
	// hstsLocal is the max-age with the internal CA: short, so a browser
	// that saw a dev box does not insist on HTTPS for long.
	hstsLocal = 5 * time.Minute
	// hstsPublic is the max-age once certificates come from a public CA:
	// long enough to matter, short enough that a domain moved to a host
	// without HTTPS recovers within a month. No includeSubDomains: other
	// names under a custom domain may live elsewhere.
	hstsPublic = 30 * 24 * time.Hour
	notFoundHT = `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Not found</title>` +
		`<meta name="viewport" content="width=device-width,initial-scale=1"></head>` +
		`<body style="font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem">` +
		`<h1>Nothing here</h1><p>This address is not served by this Tiffin box.</p>` +
		`<p>Open the <a href="%s/">dashboard</a> to see what is running.</p></body></html>`
)

// DashboardHost is the host the dashboard is served on.
func (c Config) DashboardHost() string { return c.dashboardName() + "." + c.Domain }

func (c Config) dashboardName() string {
	if c.Dashboard == "" {
		return "dashboard"
	}
	return c.Dashboard
}

// appsDomain is the domain of the one-label app hosts.
func (c Config) appsDomain() string {
	if c.Apps != "" {
		return c.Apps
	}
	return c.Domain
}

// dashboardHosts are the dashboard's host and its names under each alias.
func (c Config) dashboardHosts() []string {
	out := []string{c.DashboardHost()}
	for _, a := range c.Aliases {
		for _, h := range []string{c.dashboardName() + "." + a, "dashboard." + a} {
			if !slices.Contains(out, h) {
				out = append(out, h)
			}
		}
	}
	return out
}

// BoxLabel returns the first-level name when host is exactly one label
// under domain ("shop.example.com" in "example.com" → "shop"), else "".
func BoxLabel(host, domain string) string {
	label, rest, ok := strings.Cut(host, ".")
	if !ok || label == "" || rest != domain {
		return ""
	}
	return label
}

// hostsFor is a route host plus its names under each alias, when it is a
// one-label host under the apps domain.
func (c Config) hostsFor(host string) []string {
	out := []string{host}
	if label := BoxLabel(host, c.appsDomain()); label != "" {
		for _, a := range c.Aliases {
			out = append(out, label+"."+a)
		}
	}
	return out
}

// hstsMaxAge is the effective Strict-Transport-Security max-age (0: off).
func (c Config) hstsMaxAge() time.Duration {
	switch {
	case c.HSTS < 0:
		return 0
	case c.HSTS > 0:
		return c.HSTS
	case c.Internal:
		return hstsLocal
	case c.ACME != nil && c.ACME.publicCA():
		return hstsPublic
	}
	return 0
}

// normalized returns a validated copy with defaults applied and routes sorted.
func (c Config) normalized() (Config, error) {
	c.Domain = strings.ToLower(strings.Trim(strings.TrimSpace(c.Domain), "."))
	if err := validHost(c.Domain); err != nil || strings.Contains(c.Domain, "*") {
		return c, fmt.Errorf("edge: invalid Domain %q", c.Domain)
	}
	if c.Apps = strings.ToLower(strings.Trim(strings.TrimSpace(c.Apps), ".")); c.Apps == c.Domain {
		c.Apps = ""
	} else if c.Apps != "" && (validHost(c.Apps) != nil || strings.Contains(c.Apps, "*")) {
		return c, fmt.Errorf("edge: invalid apps domain %q", c.Apps)
	}
	if _, _, err := net.SplitHostPort(c.Upstream); err != nil {
		return c, fmt.Errorf("edge: invalid Upstream %q: want host:port", c.Upstream)
	}
	if strings.TrimSpace(c.DataDir) == "" {
		return c, errors.New("edge: DataDir is required")
	}
	c.Dashboard = strings.ToLower(strings.TrimSpace(c.Dashboard))
	if c.Dashboard != "" && (validHost(c.Dashboard) != nil || strings.ContainsAny(c.Dashboard, ".*")) {
		return c, fmt.Errorf("edge: invalid dashboard name %q: one label, like \"dashboard\"", c.Dashboard)
	}
	if c.HTTPPort == 0 {
		c.HTTPPort = 80
	}
	if c.HTTPSPort == 0 {
		c.HTTPSPort = 443
	}
	for _, p := range []int{c.HTTPPort, c.HTTPSPort} {
		if p < 1 || p > 65535 {
			return c, fmt.Errorf("edge: invalid port %d", p)
		}
	}
	if c.HTTPPort == c.HTTPSPort {
		return c, errors.New("edge: HTTPPort and HTTPSPort must differ")
	}
	if c.Internal {
		c.ACME = nil
	} else {
		if c.ACME == nil {
			return c, ErrACMERequired
		}
		a, err := c.ACME.normalized()
		if err != nil {
			return c, err
		}
		c.ACME = a
	}
	aliases := make([]string, 0, len(c.Aliases))
	for _, a := range c.Aliases {
		a = strings.ToLower(strings.Trim(strings.TrimSpace(a), "."))
		if a == "" || a == c.appsDomain() || slices.Contains(aliases, a) {
			continue
		}
		if err := validHost(a); err != nil || strings.Contains(a, "*") {
			return c, fmt.Errorf("edge: invalid alias domain %q", a)
		}
		aliases = append(aliases, a)
	}
	c.Aliases = aliases
	if c.Protect != nil {
		if err := c.Protect.validate(); err != nil {
			return c, err
		}
	}
	seen := map[string]bool{}
	for _, d := range c.dashboardHosts() {
		seen[d] = true
	}
	routes := make([]Route, 0, len(c.Routes))
	c.paths = append([]Route(nil), c.paths...) // normalized twice keeps them
	for _, r := range c.Routes {
		r.Host = strings.ToLower(strings.TrimSpace(r.Host))
		if r.Host == "" {
			// A box path: the same path on every app host, ahead of the app.
			if !strings.HasPrefix(r.PathPrefix, "/") || strings.HasSuffix(r.PathPrefix, "/") || len(r.upstreams()) == 0 || r.FileRoot != "" || r.RedirectTo != "" {
				return c, fmt.Errorf("edge: a route without a host needs a path prefix and an upstream (got %q)", r.PathPrefix)
			}
			if seen[r.PathPrefix] {
				return c, fmt.Errorf("edge: duplicate route %q", r.PathPrefix)
			}
			seen[r.PathPrefix] = true
			c.paths = append(c.paths, r)
			continue
		}
		if err := validHost(r.Host); err != nil {
			return c, fmt.Errorf("edge: route host %q: %w", r.Host, err)
		}
		if strings.Contains(r.Host, "*") {
			return c, fmt.Errorf("edge: route host %q: wildcards are not allowed", r.Host)
		}
		if r.PathPrefix != "" && (!strings.HasPrefix(r.PathPrefix, "/") || strings.HasSuffix(r.PathPrefix, "/")) {
			return c, fmt.Errorf("edge: route %q: path prefix %q must start with / and not end with /", r.Host, r.PathPrefix)
		}
		switch {
		case r.RedirectTo != "":
			r.RedirectTo = strings.ToLower(strings.TrimSpace(r.RedirectTo))
			if err := validHost(r.RedirectTo); err != nil || strings.Contains(r.RedirectTo, "*") || r.RedirectTo == r.Host {
				return c, fmt.Errorf("edge: route %q: invalid redirect target %q", r.Host, r.RedirectTo)
			}
		case r.FileRoot == "":
			ups := r.upstreams()
			if len(ups) == 0 {
				return c, fmt.Errorf("edge: route %q: needs an upstream or a file root", r.Host)
			}
			for _, u := range ups {
				if _, _, err := net.SplitHostPort(u); err != nil {
					return c, fmt.Errorf("edge: route %q: invalid upstream %q: want host:port", r.Host, u)
				}
			}
		}
		if err := r.Rules.Validate(); err != nil {
			return c, fmt.Errorf("edge: route %q: %w", r.Host+r.PathPrefix, err)
		}
		key := r.Host + r.PathPrefix
		if seen[key] {
			return c, fmt.Errorf("edge: duplicate route %q", key)
		}
		seen[key] = true
		routes = append(routes, r)
	}
	// Same host: longer path prefixes first, so /api wins over the catch-all.
	sort.SliceStable(routes, func(i, j int) bool {
		if routes[i].Host != routes[j].Host {
			return routes[i].Host < routes[j].Host
		}
		return len(routes[i].PathPrefix) > len(routes[j].PathPrefix)
	})
	c.Routes = routes
	return c, nil
}

func validHost(h string) error {
	if h == "" {
		return errors.New("empty host")
	}
	for _, r := range h {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.', r == '*':
		default:
			return fmt.Errorf("invalid character %q", r)
		}
	}
	return nil
}

// managedHosts are the TLS subjects with the internal CA: the one-level
// wildcard, the dashboard and every route host (and their alias names).
// Caddy matches policy subjects to route hosts by exact string; a host
// missing here would get an implicit policy on Caddy's default "local" CA,
// which would also try to install itself into the OS trust store. (Do not
// use tls.certificates.automate: it also creates that default CA.)
func (c Config) managedHosts() []string {
	subjects := []string{"*." + c.appsDomain(), c.DashboardHost()}
	for _, a := range c.Aliases {
		subjects = append(subjects, "*."+a)
	}
	subjects = append(subjects, c.dashboardHosts()[1:]...)
	have := map[string]bool{}
	for _, x := range subjects {
		have[x] = true
	}
	for _, r := range c.Routes {
		for _, h := range c.hostsFor(r.Host) {
			if !have[h] {
				have[h] = true
				subjects = append(subjects, h)
			}
		}
	}
	return subjects
}

type obj = map[string]any

// ConfigJSON returns the Caddy JSON config for cfg. It is pure: no I/O.
func ConfigJSON(cfg Config) ([]byte, error) {
	c, err := cfg.normalized()
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(buildConfig(c), "", "  ")
}

func proxy(upstream string) obj { return proxyMany([]string{upstream}) }

func proxyMany(upstreams []string) obj {
	ups := make([]obj, len(upstreams))
	for i, u := range upstreams {
		ups[i] = obj{"dial": u}
	}
	h := obj{
		"handler":        "reverse_proxy",
		"upstreams":      ups,
		"flush_interval": -1, // stream SSE and chunked responses straight through
		"headers": obj{"request": obj{"set": obj{
			// The access log's request_id: apps can log it, and the runtime
			// makes it the trace ID of the request's OpenTelemetry spans.
			"X-Request-Id":      []string{"{http.request.uuid}"},
			"X-Forwarded-Proto": []string{"{http.request.scheme}"},
			"X-Forwarded-Host":  []string{"{http.request.hostport}"},
			"X-Forwarded-Port":  []string{"{http.request.port}"},
		}}},
	}
	if len(upstreams) > 1 {
		h["load_balancing"] = obj{"selection_policy": obj{"policy": "least_conn"}, "retries": 2}
		h["health_checks"] = obj{"passive": obj{"fail_duration": "10s", "max_fails": 2}}
	}
	return h
}

func routeFor(c Config, r Route, portSuffix string) obj {
	m := obj{}
	if r.Host != "" {
		m["host"] = c.hostsFor(r.Host)
	}
	if r.PathPrefix != "" {
		m["path"] = []string{r.PathPrefix, r.PathPrefix + "/*"}
	}
	var handle []obj
	switch {
	case r.RedirectTo != "":
		handle = []obj{{
			"handler":     "static_response",
			"status_code": 308,
			"headers":     obj{"Location": []string{"https://" + r.RedirectTo + portSuffix + "{http.request.uri}"}},
		}}
	case r.FileRoot != "":
		handle = append(handle, r.Rules.headerHandlers()...)
		handle = append(handle, r.Rules.redirectHandlers(true)...)
		handle = append(handle, fileHandlers(r)...)
		// After the rewrites, so a missing asset answered with index.html
		// is never cached as the asset. The site's own headers (above)
		// still override these.
		handle = append(handle, staticCache()...)
		handle = append(handle, obj{
			"handler":             "file_server",
			"root":                r.FileRoot,
			"precompressed":       obj{"br": obj{}, "zstd": obj{}, "gzip": obj{}},
			"precompressed_order": []string{"br", "zstd", "gzip"},
		})
	default:
		if r.NextCache {
			handle = append(handle, nextCacheControl())
		}
		handle = append(handle, r.Rules.headerHandlers()...)
		handle = append(handle, r.Rules.redirectHandlers(false)...)
		handle = append(handle, proxyMany(r.upstreams()))
	}
	if r.RedirectTo == "" && !r.NoCompress {
		handle = append([]obj{compress()}, handle...)
	}
	return obj{"match": []obj{m}, "handle": handle, "terminal": true}
}

// nextCacheControl rewrites the Cache-Control Next.js writes for a CDN
// (see Route.NextCache).
func nextCacheControl() obj {
	return obj{"handler": "headers", "response": obj{"deferred": true, "replace": obj{"Cache-Control": []obj{{
		"search_regexp": `^s-maxage=[0-9]+(, ?stale-while-revalidate(=[0-9]+)?)?$`,
		"replace":       "public, max-age=0, must-revalidate",
	}}}}}
}

// compress encodes text responses with zstd or gzip, as the client
// prefers (zstd on a tie). Caddy leaves alone responses that already have
// a Content-Encoding, partial ones (206), Cache-Control: no-transform and
// types outside its list of text formats (images, video, archives and
// woff2 are compressed already). Streams keep flowing: a response whose
// first write is short is passed through, and every flush from the
// upstream flushes the compressor too. Headers wait for the first body
// byte (the encoding is chosen then), except text/event-stream's, which go
// out at once.
func compress() obj {
	return obj{
		"handler":        "encode",
		"encodings":      obj{"zstd": obj{}, "gzip": obj{}},
		"prefer":         []string{"zstd", "gzip"},
		"minimum_length": 1024, // smaller fits in a packet or two anyway
	}
}

// staticCache sets Cache-Control on static files: fingerprinted assets
// for a year (successful responses only, so a 404 during a deploy is not
// kept), everything else revalidated with the ETag on every use.
func staticCache() []obj {
	return []obj{
		{"handler": "headers", "response": obj{"set": obj{"Cache-Control": []string{"no-cache"}}}},
		{"handler": "subroute", "routes": []obj{{
			"match": []obj{{"tiffin_hashed_asset": obj{}}},
			"handle": []obj{{"handler": "headers", "response": obj{
				"set":     obj{"Cache-Control": []string{"public, max-age=31536000, immutable"}},
				"require": obj{"status_code": []int{2, 304}},
			}}},
		}}},
	}
}

func notFound(dashboardURL string) obj {
	return obj{
		"handler":     "static_response",
		"status_code": 404,
		"headers":     obj{"Content-Type": []string{"text/html; charset=utf-8"}},
		"body":        fmt.Sprintf(notFoundHT, html.EscapeString(dashboardURL)),
	}
}

// dashboardURL is where the "Nothing here" page sends people.
func (c Config) dashboardURL(portSuffix string) string {
	if u := strings.TrimRight(c.DashboardURL, "/"); strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
		return u
	}
	return "https://" + c.DashboardHost() + portSuffix
}

func hostRoute(hosts []string, upstream string) obj {
	return obj{
		"match":    []obj{{"host": hosts}},
		"handle":   []obj{compress(), proxy(upstream)},
		"terminal": true,
	}
}

func buildConfig(c Config) obj {
	httpsPort := strconv.Itoa(c.HTTPSPort)
	portSuffix := ""
	if c.HTTPSPort != 443 {
		portSuffix = ":" + httpsPort
	}

	security := obj{}
	if age := c.hstsMaxAge(); age > 0 {
		security["Strict-Transport-Security"] = []string{"max-age=" + strconv.Itoa(int(age/time.Second))}
	}
	// Non-terminal: security headers on every response, including 404s.
	// HSTS is the box's (it owns TLS); the others are defaults an app or a
	// site's own headers replace.
	secure := []obj{stallHandler(), {
		"handler": "headers",
		"response": obj{
			"deferred": true, // after the upstream, so ours win rather than duplicate
			"set":      security,
			"delete":   []string{"Server"},
		},
	}}
	for _, h := range [][2]string{
		{"X-Content-Type-Options", "nosniff"},
		{"Referrer-Policy", "strict-origin-when-cross-origin"},
	} {
		secure = append(secure, obj{
			"handler": "headers",
			"response": obj{
				"set":     obj{h[0]: []string{h[1]}},
				"require": obj{"headers": obj{h[0]: nil}}, // only when the response has none
			},
		})
	}
	secure = append(secure, []obj{
		// No framing unless the app decides, with its own
		// X-Frame-Options or a CSP with frame-ancestors (which browsers
		// prefer). Inner handlers see the response first, so these run
		// bottom to top: DENY when the app sent no X-Frame-Options;
		// none when its CSP has frame-ancestors; then, when the app sent
		// neither a CSP nor an X-Frame-Options of its own, our CSP.
		{
			"handler": "headers",
			"response": obj{
				"set":     obj{"Content-Security-Policy": []string{cspValue}},
				"require": obj{"headers": obj{"Content-Security-Policy": nil, "X-Frame-Options": []string{"DENY"}}},
			},
		},
		{
			"handler": "headers",
			"response": obj{
				"delete":  []string{"X-Frame-Options"},
				"require": obj{"headers": obj{"Content-Security-Policy": []string{"*frame-ancestors*"}, "X-Frame-Options": []string{"DENY"}}},
			},
		},
		{
			"handler": "headers",
			"response": obj{
				"set":     obj{"X-Frame-Options": []string{"DENY"}},
				"require": obj{"headers": obj{"X-Frame-Options": nil}},
			},
		},
	}...)
	// Every access log line carries the request's ID (also sent upstream
	// as X-Request-Id, see proxyMany).
	secure = append(secure, obj{"handler": "log_append", "key": "request_id", "value": "{http.request.uuid}"})
	routes := []obj{{"handle": secure}}
	if c.Protect != nil {
		routes = append(routes, c.Protect.protectRoutes(c)...)
	}
	routes = append(routes, hostRoute(c.dashboardHosts(), c.Upstream))
	for _, r := range c.paths {
		routes = append(routes, routeFor(c, r, portSuffix))
	}
	for _, r := range c.Routes {
		routes = append(routes, routeFor(c, r, portSuffix))
	}
	wild := []string{"*." + c.appsDomain()}
	for _, a := range c.Aliases {
		wild = append(wild, "*."+a)
	}
	// With the internal CA, the wildcard route is what makes Caddy manage
	// the wildcard certificate. With ACME it only answers unknown names.
	routes = append(routes, obj{
		"match":    []obj{{"host": wild}},
		"handle":   []obj{notFound(c.dashboardURL(portSuffix))},
		"terminal": true,
	}, obj{"handle": []obj{notFound(c.dashboardURL(portSuffix))}})

	httpRedirect := obj{
		"listen": []string{":" + strconv.Itoa(c.HTTPPort)},
		"routes": []obj{{"handle": []obj{{
			"handler":     "static_response",
			"status_code": 308,
			"headers": obj{
				"Location":   []string{"https://{http.request.host}" + portSuffix + "{http.request.uri}"},
				"Connection": []string{"close"},
			},
			"close": true,
		}}}},
		"automatic_https": obj{"disable": true},
	}
	httpsServer := obj{
		"listen":    []string{":" + httpsPort},
		"protocols": []string{"h1", "h2"},
		"routes":    routes,
		"automatic_https": obj{
			"disable_redirects": true, // the http server above owns redirects
		},
		"tls_connection_policies": []obj{{}},
		// Requests may take as long as the app's time limit, which the
		// runtime's switchboard enforces (no read or write deadline here).
		// Caddy's stall timeouts are off (negative): as of 2.11 they cut a
		// request a minute after its body was read, a response stream with
		// a pause of a minute over HTTP/2, and every request longer than a
		// minute behind the WAF (TestLongRequests). The stall guard
		// (stall.go) does their job without that.
		"read_idle_timeout":  -1,
		"write_idle_timeout": -1,
	}
	if c.ACME != nil {
		// The automation policies below decide which names get certificates
		// (and when); Caddy must not manage every route host on its own.
		httpsServer["automatic_https"] = obj{"disable_redirects": true, "disable_certificates": true}
		// HTTP/3 (QUIC: UDP on the HTTPS port) on a box with public
		// certificates only; a local box's port is forwarded over TCP
		// alone. Browsers learn of it from Alt-Svc and stay on TCP where
		// UDP is blocked.
		httpsServer["protocols"] = []string{"h1", "h2", "h3"}
	}
	if c.Protect != nil {
		httpsServer["errors"] = obj{"routes": c.Protect.errorRoutes()}
	}

	logs := obj{"default": obj{
		"level":   "ERROR",
		"writer":  obj{"output": "stderr"},
		"encoder": obj{"format": "json"},
		"exclude": []string{"http.log.access"},
	}}
	if c.AccessLog != "" {
		httpsServer["logs"] = obj{"default_logger_name": "access"}
		logs["access"] = obj{
			"level":   "INFO",
			"writer":  obj{"output": "file", "filename": c.AccessLog, "roll_size_mb": 50, "roll_keep": 5},
			"encoder": obj{"format": "json"},
			"include": []string{"http.log.access.access"},
		}
	}
	cas := obj{caID: obj{
		"name":                     caName,
		"root_common_name":         caName,
		"intermediate_common_name": caName + " Intermediate",
		"install_trust":            false, // never touch the OS trust store
	}}
	tlsApp := obj{
		"automation": obj{"policies": []obj{{
			"subjects": c.managedHosts(),
			"issuers":  []obj{{"module": "internal", "ca": caID}},
		}}},
	}
	apps := obj{
		"http": obj{
			"http_port":  c.HTTPPort,
			"https_port": c.HTTPSPort,
			"servers":    obj{"https": httpsServer, "http": httpRedirect},
		},
		"pki": obj{"certificate_authorities": cas},
		"tls": tlsApp,
	}
	if c.ACME != nil {
		// Caddy keeps an internal policy for names that cannot get public
		// certificates (IPs, localhost) on its default "local" CA: define it
		// so it never installs itself into the OS trust store either.
		cas["local"] = obj{"install_trust": false}
		apps["tls"] = c.acmeTLS()
		apps["events"] = certEvents()
	}
	out := obj{
		"admin":   obj{"disabled": true, "config": obj{"persist": false}},
		"logging": obj{"logs": logs},
		"storage": obj{"module": "file_system", "root": c.DataDir},
		"apps":    apps,
	}
	if c.Protect != nil {
		for k, v := range c.Protect.apps() {
			out["apps"].(obj)[k] = v
		}
	}
	return out
}
