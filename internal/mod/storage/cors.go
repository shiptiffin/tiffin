package storage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/manifest"
	"github.com/shiptiffin/tiffin/internal/platform"
)

// Browsers upload to s3.<domain> with presigned URLs from app pages on
// other hosts, so the S3 API needs CORS. The front answers preflights and
// adds the headers itself rather than storing a CORS document in the
// gateway: the default (the project's own app hosts) follows the project's
// routes, previews and custom domains as they change, with nothing to keep
// in sync.

// corsExpose are the response headers browser code may read: ETag is how a
// multipart upload learns each part's tag.
const corsExpose = "ETag, Content-Length, Content-Range, Content-Type, Last-Modified, x-amz-request-id, x-amz-version-id"

// appHosts are the hosts a project's apps answer on, for the default CORS
// rule: exact names, and preview name suffixes ("--shop.<apps domain>").
type appHosts struct {
	exact    map[string]bool
	previews []string
	at       time.Time
}

type hostCache struct {
	mu sync.Mutex
	m  map[string]appHosts
}

// hostsOf returns a project's app hosts, cached for 15 seconds.
func (c *hostCache) hostsOf(ctx context.Context, p *platform.Platform, project string) appHosts {
	c.mu.Lock()
	h, ok := c.m[project]
	c.mu.Unlock()
	if ok && time.Since(h.at) < 15*time.Second {
		return h
	}
	h = appHosts{exact: map[string]bool{}, at: time.Now()}
	if _, res, err := p.DB.Load(ctx, project); err == nil {
		apps := map[string]manifest.App{}
		for addr, r := range res {
			var a manifest.App
			if change.Kind(addr) == change.KindApp && json.Unmarshal(r.Spec, &a) == nil && a.Role != manifest.RoleWorker {
				apps[change.Name(addr)] = a
			}
		}
		h = projectHosts(p, project, apps)
	}
	c.mu.Lock()
	if c.m == nil {
		c.m = map[string]appHosts{}
	}
	c.m[project] = h
	c.mu.Unlock()
	return h
}

// projectHosts lists the hosts of a project's web apps the way the runtime
// routes them: "shop" is shop.<apps domain>, "example.com/api" is
// example.com, and previews are <preview>--<name>.<apps domain>.
func projectHosts(p *platform.Platform, project string, apps map[string]manifest.App) appHosts {
	h := appHosts{exact: map[string]bool{}, at: time.Now()}
	for name, a := range apps {
		routes := a.Routes
		if len(routes) == 0 {
			routes = []string{manifest.DefaultName(project, name, "")}
		}
		preview := project + "-" + name
		for _, r := range routes {
			if !strings.ContainsAny(r, "./") {
				preview = r
				break
			}
		}
		for _, r := range routes {
			host, _, _ := strings.Cut(r, "/")
			if !strings.Contains(host, ".") {
				host = p.Host(host)
			}
			h.exact[strings.ToLower(host)] = true
		}
		h.previews = append(h.previews, "--"+preview+"."+p.AppsDomain())
	}
	return h
}

// originAllowed reports whether a browser origin may call bucket b's S3 API.
func (f *frontServer) originAllowed(ctx context.Context, b *bucketMeta, origin string) bool {
	if len(b.CORS) > 0 {
		return matchOrigin(b.CORS, origin)
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	h := f.hosts.hostsOf(ctx, f.p, b.Project)
	if h.exact[host] {
		return true
	}
	for _, s := range h.previews {
		if strings.HasSuffix(host, s) {
			return true
		}
	}
	return false
}

// matchOrigin checks an origin against a bucket's cors list: "*", an exact
// origin, or a wildcard such as "https://*.example.com".
func matchOrigin(rules []string, origin string) bool {
	origin = strings.ToLower(strings.TrimRight(origin, "/"))
	for _, r := range rules {
		r = strings.ToLower(strings.TrimRight(r, "/"))
		if r == "*" || r == origin {
			return true
		}
		if scheme, rest, ok := strings.Cut(r, "://*."); ok {
			if o, ok := strings.CutPrefix(origin, scheme+"://"); ok && strings.HasSuffix(o, "."+rest) {
				return true
			}
		}
	}
	return false
}

// cors adds CORS headers for an allowed origin and answers preflights. It
// reports whether the request was a preflight it answered.
func (f *frontServer) cors(w http.ResponseWriter, r *http.Request, b *bucketMeta) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	preflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
	if b == nil || !f.originAllowed(r.Context(), b, origin) {
		if preflight {
			plain(w, http.StatusForbidden, "origin "+origin+" may not use this bucket: add it to the bucket's cors list in tiffin.config.ts")
			return true
		}
		return false
	}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", origin)
	h.Add("Vary", "Origin")
	if !preflight {
		h.Set("Access-Control-Expose-Headers", corsExpose)
		return false
	}
	h.Set("Access-Control-Allow-Methods", "GET, HEAD, PUT, POST, DELETE")
	if req := r.Header.Get("Access-Control-Request-Headers"); req != "" {
		h.Set("Access-Control-Allow-Headers", req)
	}
	h.Set("Access-Control-Max-Age", "3600")
	w.WriteHeader(http.StatusNoContent)
	return true
}
