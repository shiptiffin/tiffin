package edge

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
)

// Config describes the platform edge: one domain, one upstream and any extra
// host routes.
type Config struct {
	Domain    string  // e.g. "tiffin.localhost"; dashboard lives at "dashboard."+Domain
	Upstream  string  // tiffin API/dashboard http address, e.g. "127.0.0.1:7070"
	DataDir   string  // Caddy storage (certs, CA), e.g. /var/lib/tiffin/platform/caddy
	HTTPPort  int     // default 80
	HTTPSPort int     // default 443
	Internal  bool    // true: Caddy internal CA (local/dev); false: public ACME (not implemented yet)
	Routes    []Route // extra host -> upstream routes
	AccessLog string  // file for JSON access logs (rolled); empty disables them
	// Protect is the protection layer (rate limits, challenge, CrowdSec,
	// WAF). Nil: the one registered with SetProtectionSource, if any.
	Protect *Protection
}

// Route sends requests for Host (and optionally a path prefix) somewhere:
// one upstream, several (load-balanced, least connections) or a directory
// of static files.
type Route struct {
	Host       string
	PathPrefix string   // e.g. "/api"; empty matches every path. Longer prefixes win.
	Upstream   string   // host:port
	Upstreams  []string // several host:ports; overrides Upstream
	FileRoot   string   // serve static files from this directory instead of proxying
	SPA        bool     // with FileRoot: unknown paths serve index.html
	Immutable  bool     // with FileRoot: long-lived cache headers
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

// ErrPublicACMEUnsupported is returned when Internal is false: public ACME
// arrives in Phase 2.
var ErrPublicACMEUnsupported = errors.New("edge: public ACME is not implemented yet; set Internal=true")

const (
	caID       = "tiffin"
	caName     = "Tiffin Local CA"
	hstsValue  = "max-age=300"
	cspValue   = "frame-ancestors 'none'"
	notFoundHT = `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Not found</title>` +
		`<meta name="viewport" content="width=device-width,initial-scale=1"></head>` +
		`<body style="font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem">` +
		`<h1>Nothing here</h1><p>This address is not served by this Tiffin box.</p>` +
		`<p>Open the <a href="https://dashboard.%s%s/">dashboard</a> to see what is running.</p></body></html>`
)

// DashboardHost is the host the dashboard is served on.
func (c Config) DashboardHost() string { return "dashboard." + c.Domain }

// normalized returns a validated copy with defaults applied and routes sorted.
func (c Config) normalized() (Config, error) {
	c.Domain = strings.ToLower(strings.Trim(strings.TrimSpace(c.Domain), "."))
	if err := validHost(c.Domain); err != nil || strings.Contains(c.Domain, "*") {
		return c, fmt.Errorf("edge: invalid Domain %q", c.Domain)
	}
	if _, _, err := net.SplitHostPort(c.Upstream); err != nil {
		return c, fmt.Errorf("edge: invalid Upstream %q: want host:port", c.Upstream)
	}
	if strings.TrimSpace(c.DataDir) == "" {
		return c, errors.New("edge: DataDir is required")
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
	if !c.Internal {
		return c, ErrPublicACMEUnsupported
	}
	if c.Protect != nil {
		if err := c.Protect.validate(); err != nil {
			return c, err
		}
	}
	seen := map[string]bool{c.DashboardHost(): true}
	routes := make([]Route, 0, len(c.Routes))
	for _, r := range c.Routes {
		r.Host = strings.ToLower(strings.TrimSpace(r.Host))
		if err := validHost(r.Host); err != nil {
			return c, fmt.Errorf("edge: route host %q: %w", r.Host, err)
		}
		if strings.Contains(r.Host, "*") {
			return c, fmt.Errorf("edge: route host %q: wildcards are not allowed", r.Host)
		}
		if r.PathPrefix != "" && (!strings.HasPrefix(r.PathPrefix, "/") || strings.HasSuffix(r.PathPrefix, "/")) {
			return c, fmt.Errorf("edge: route %q: path prefix %q must start with / and not end with /", r.Host, r.PathPrefix)
		}
		if r.FileRoot == "" {
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

// managedHosts are the TLS subjects: the one-level wildcard, the dashboard and
// every route host. Caddy matches policy subjects to route hosts by exact
// string; a host missing here would get an implicit policy on Caddy's default
// "local" CA, which would also try to install itself into the OS trust store.
// (Do not use tls.certificates.automate: it also creates that default CA.)
func (c Config) managedHosts() []string {
	subjects := []string{"*." + c.Domain, c.DashboardHost()}
	have := map[string]bool{}
	for _, x := range subjects {
		have[x] = true
	}
	for _, r := range c.Routes {
		if !have[r.Host] {
			have[r.Host] = true
			subjects = append(subjects, r.Host)
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

func routeFor(r Route) obj {
	m := obj{"host": []string{r.Host}}
	if r.PathPrefix != "" {
		m["path"] = []string{r.PathPrefix, r.PathPrefix + "/*"}
	}
	var handle []obj
	switch {
	case r.FileRoot != "":
		if r.Immutable {
			handle = append(handle, obj{"handler": "headers", "response": obj{"set": obj{"Cache-Control": []string{"public, max-age=31536000, immutable"}}}})
		}
		if r.SPA {
			handle = append(handle, obj{"handler": "rewrite", "uri": "{http.matchers.file.relative}"})
			m["file"] = obj{"root": r.FileRoot, "try_files": []string{"{http.request.uri.path}", "{http.request.uri.path}/index.html", "/index.html"}}
		}
		handle = append(handle, obj{"handler": "file_server", "root": r.FileRoot})
	default:
		handle = []obj{proxyMany(r.upstreams())}
	}
	return obj{"match": []obj{m}, "handle": handle, "terminal": true}
}

func notFound(domain, portSuffix string) obj {
	return obj{
		"handler":     "static_response",
		"status_code": 404,
		"headers":     obj{"Content-Type": []string{"text/html; charset=utf-8"}},
		"body":        fmt.Sprintf(notFoundHT, domain, portSuffix),
	}
}

func hostRoute(host, upstream string) obj {
	return obj{
		"match":    []obj{{"host": []string{host}}},
		"handle":   []obj{proxy(upstream)},
		"terminal": true,
	}
}

func buildConfig(c Config) obj {
	httpsPort := strconv.Itoa(c.HTTPSPort)
	portSuffix := ""
	if c.HTTPSPort != 443 {
		portSuffix = ":" + httpsPort
	}

	routes := []obj{
		// Non-terminal: security headers on every response, including 404s.
		{"handle": []obj{
			{
				"handler": "headers",
				"response": obj{
					"deferred": true, // after the upstream, so ours win rather than duplicate
					"set": obj{
						"Strict-Transport-Security": []string{hstsValue},
						"X-Content-Type-Options":    []string{"nosniff"},
						"X-Frame-Options":           []string{"DENY"},
						"Referrer-Policy":           []string{"strict-origin-when-cross-origin"},
					},
					"delete": []string{"Server"},
				},
			},
			{
				// Only when the upstream sent no CSP of its own.
				"handler": "headers",
				"response": obj{
					"set":     obj{"Content-Security-Policy": []string{cspValue}},
					"require": obj{"headers": obj{"Content-Security-Policy": nil}},
				},
			},
		}},
	}
	if c.Protect != nil {
		routes = append(routes, c.Protect.protectRoutes(c)...)
	}
	routes = append(routes, hostRoute(c.DashboardHost(), c.Upstream))
	for _, r := range c.Routes {
		routes = append(routes, routeFor(r))
	}
	// The wildcard route is what makes Caddy manage the wildcard certificate.
	routes = append(routes, obj{
		"match":    []obj{{"host": []string{"*." + c.Domain}}},
		"handle":   []obj{notFound(c.Domain, portSuffix)},
		"terminal": true,
	}, obj{"handle": []obj{notFound(c.Domain, portSuffix)}})

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
	out := obj{
		"admin":   obj{"disabled": true, "config": obj{"persist": false}},
		"logging": obj{"logs": logs},
		"storage": obj{"module": "file_system", "root": c.DataDir},
		"apps": obj{
			"http": obj{
				"http_port":  c.HTTPPort,
				"https_port": c.HTTPSPort,
				"servers":    obj{"https": httpsServer, "http": httpRedirect},
			},
			"pki": obj{"certificate_authorities": obj{caID: obj{
				"name":                     caName,
				"root_common_name":         caName,
				"intermediate_common_name": caName + " Intermediate",
				"install_trust":            false, // never touch the OS trust store
			}}},
			"tls": obj{
				"automation": obj{"policies": []obj{{
					"subjects": c.managedHosts(),
					"issuers":  []obj{{"module": "internal", "ca": caID}},
				}}},
			},
		},
	}
	if c.Protect != nil {
		for k, v := range c.Protect.apps() {
			out["apps"].(obj)[k] = v
		}
	}
	return out
}
