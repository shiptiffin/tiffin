// Package edgelog reads the box edge's JSON access log (Caddy format) and
// maps request hosts to the project and app that serve them. Observe uses it
// for request metrics and log shipping; analytics uses it for no-JS pageviews.
package edgelog

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/state"
)

// Path is where the box edge writes access logs.
const Path = "/var/lib/tiffin/logs/access.log"

// Entry is one parsed access log line.
type Entry struct {
	Time     time.Time
	ClientIP string
	Method   string
	Host     string // lowercased, no port
	URI      string // path and query as requested
	Proto    string
	Status   int
	Duration float64 // seconds
	Size     int64
	Headers  map[string][]string // request headers (canonical names)
	// RequestID is the edge's ID for the request (a UUID), also sent to
	// the app as X-Request-Id; without dashes it is the request's trace ID.
	RequestID string
}

// Header returns the first value of a request header.
func (e *Entry) Header(name string) string {
	if v := e.Headers[name]; len(v) > 0 {
		return v[0]
	}
	return ""
}

type rawEntry struct {
	Logger  string  `json:"logger"`
	TS      float64 `json:"ts"`
	Request struct {
		RemoteIP string              `json:"remote_ip"`
		ClientIP string              `json:"client_ip"`
		Proto    string              `json:"proto"`
		Method   string              `json:"method"`
		Host     string              `json:"host"`
		URI      string              `json:"uri"`
		Headers  map[string][]string `json:"headers"`
	} `json:"request"`
	Duration  float64 `json:"duration"`
	Size      int64   `json:"size"`
	Status    int     `json:"status"`
	RequestID string  `json:"request_id"`
}

// Parse decodes one Caddy JSON access log line. ok is false for lines that
// are not access entries.
func Parse(line []byte) (Entry, bool) {
	var r rawEntry
	if err := json.Unmarshal(line, &r); err != nil || r.Request.Method == "" || r.Status == 0 {
		return Entry{}, false
	}
	sec, frac := math.Modf(r.TS)
	e := Entry{
		Time:      time.Unix(int64(sec), int64(frac*1e9)).UTC(),
		ClientIP:  r.Request.ClientIP,
		Method:    r.Request.Method,
		Host:      stripPort(strings.ToLower(r.Request.Host)),
		URI:       r.Request.URI,
		Proto:     r.Request.Proto,
		Status:    r.Status,
		Duration:  r.Duration,
		Size:      r.Size,
		Headers:   r.Request.Headers,
		RequestID: r.RequestID,
	}
	if e.ClientIP == "" {
		e.ClientIP = r.Request.RemoteIP
	}
	return e, true
}

func stripPort(h string) string {
	if i := strings.LastIndexByte(h, ':'); i > 0 && !strings.Contains(h[i:], "]") {
		return h[:i]
	}
	return h
}

// Site is the project and app serving a host (and path prefix).
type Site struct {
	Project   string
	App       string
	Prefix    string // "" or "/api"
	Analytics bool   // the project has service/analytics
}

// Sites resolves hosts to apps from the projects' current resources. It
// refreshes itself at most every few seconds.
type Sites struct {
	DB     *state.DB
	Domain string
	TTL    time.Duration

	mu        sync.Mutex
	loaded    time.Time
	byHost    map[string][]Site
	analytics map[string]bool
}

// Lookup returns the site serving host+path, if any.
func (s *Sites) Lookup(ctx context.Context, host, path string) (Site, bool) {
	s.refresh(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	best, found := Site{}, false
	for _, st := range s.byHost[stripPort(strings.ToLower(host))] {
		if st.Prefix != "" && path != st.Prefix && !strings.HasPrefix(path, st.Prefix+"/") {
			continue
		}
		if !found || len(st.Prefix) > len(best.Prefix) {
			best, found = st, true
		}
	}
	return best, found
}

// AnalyticsEnabled reports whether a project has service/analytics.
func (s *Sites) AnalyticsEnabled(ctx context.Context, project string) bool {
	s.refresh(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.analytics[project]
}

// Invalidate forces the next lookup to reload.
func (s *Sites) Invalidate() {
	s.mu.Lock()
	s.loaded = time.Time{}
	s.mu.Unlock()
}

// Hosts returns every app host of a project (for documentation and APIs).
func (s *Sites) Hosts(ctx context.Context, project, app string) []string {
	s.refresh(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for h, sts := range s.byHost {
		for _, st := range sts {
			if st.Project == project && (app == "" || st.App == app) {
				out = append(out, h)
				break
			}
		}
	}
	return out
}

func (s *Sites) refresh(ctx context.Context) {
	ttl := s.TTL
	if ttl == 0 {
		ttl = 5 * time.Second
	}
	s.mu.Lock()
	fresh := time.Since(s.loaded) < ttl
	s.mu.Unlock()
	if fresh || s.DB == nil {
		return
	}
	byHost := map[string][]Site{}
	analytics := map[string]bool{}
	projects, err := s.DB.ListProjects(ctx)
	if err != nil {
		return
	}
	for _, pr := range projects {
		_, res, err := s.DB.Load(ctx, pr)
		if err != nil {
			continue
		}
		_, an := res[change.KindService+"/analytics"]
		analytics[pr] = an
		for addr, r := range res {
			if change.Kind(addr) != change.KindApp {
				continue
			}
			var spec struct {
				Role   string   `json:"role"`
				Routes []string `json:"routes"`
			}
			_ = json.Unmarshal(r.Spec, &spec)
			for _, rt := range spec.Routes {
				host, prefix := RouteHost(rt, s.Domain)
				byHost[host] = append(byHost[host], Site{Project: pr, App: change.Name(addr), Prefix: prefix, Analytics: an})
			}
		}
	}
	s.mu.Lock()
	s.byHost, s.analytics, s.loaded = byHost, analytics, time.Now()
	s.mu.Unlock()
}

// RouteHost expands a manifest route ("shop", "example.com/api") into a
// host and path prefix. A route without a dot is a name under the box domain.
func RouteHost(route, domain string) (host, prefix string) {
	route = strings.ToLower(strings.TrimSpace(route))
	host, prefix = route, ""
	if i := strings.IndexByte(route, '/'); i >= 0 {
		host, prefix = route[:i], strings.TrimRight(route[i:], "/")
	}
	if !strings.Contains(host, ".") {
		host = host + "." + domain
	}
	return host, prefix
}
