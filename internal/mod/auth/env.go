package auth

import (
	"context"
	"net"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/edge"
	"github.com/btahir/tiffin/internal/platform"
)

func hasAuth(res map[string]change.Resource) bool {
	_, ok := res[change.KindService+"/auth"]
	return ok
}

// Env gives every app of an auth-enabled project the endpoint:
//
//	TIFFIN_AUTH_URL           public base URL on the app's own host (browsers, links)
//	TIFFIN_AUTH_INTERNAL_URL  the engine on the box (server-side calls skip the edge)
//	TIFFIN_AUTH_HOST          the host to name in x-tiffin-host on internal calls
//	TIFFIN_AUTH_JWKS_URL      keys for verifying the engine's JWTs
func (*Module) Env(ctx context.Context, p *platform.Platform, project, app string) (map[string]string, error) {
	_, res, err := p.DB.Load(ctx, project)
	if err != nil || !hasAuth(res) {
		return nil, err
	}
	hosts := webHosts(p, res)
	host := p.Host(project)
	if len(hosts) > 0 {
		host = hosts[0].Host
	}
	for _, h := range hosts {
		if h.App == app {
			host = h.Host
			break
		}
	}
	base := p.URL(host) + PathPrefix
	return map[string]string{
		"TIFFIN_AUTH_URL":          base,
		"TIFFIN_AUTH_INTERNAL_URL": "http://" + net.JoinHostPort(hostIP(ctx, p), enginePort) + PathPrefix,
		"TIFFIN_AUTH_HOST":         host,
		"TIFFIN_AUTH_JWKS_URL":     base + "/jwks",
	}, nil
}

// PreviewEnv points a preview's auth env at the preview's own host (Env only
// knows production hosts; the runtime calls this for previews). Server-side
// calls then act for the preview, and its links lead back to it.
func PreviewEnv(env map[string]string, previewURL string) {
	u, err := url.Parse(previewURL)
	if env["TIFFIN_AUTH_URL"] == "" || err != nil || u.Hostname() == "" {
		return
	}
	base := strings.TrimRight(previewURL, "/") + PathPrefix
	env["TIFFIN_AUTH_URL"] = base
	env["TIFFIN_AUTH_HOST"] = u.Hostname()
	env["TIFFIN_AUTH_JWKS_URL"] = base + "/jwks"
}

// lastPreviews is the preview host set the engine config last got (Routes).
var lastPreviews struct {
	sync.Mutex
	key    string
	synced bool
}

// Routes sends /api/auth on every web app host of auth-enabled projects to
// the engine, previews included. Longer path prefixes win at the edge, so
// the app keeps the rest. Previews come and go between applies: when their
// hosts changed, the engine config is rewritten first, so the engine knows a
// host before the edge sends it there.
func (*Module) Routes(ctx context.Context, p *platform.Platform) ([]edge.Route, error) {
	projects, err := p.DB.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	sort.Strings(projects)
	var out []edge.Route
	var previews []string
	seen := map[string]string{} // host → project
	for _, project := range projects {
		_, res, err := p.DB.Load(ctx, project)
		if err != nil {
			return nil, err
		}
		if !hasAuth(res) {
			continue
		}
		rs := routesFor(p, res)
		previewed, err := previewHosts(ctx, p, project)
		if err != nil {
			return nil, err
		}
		for _, h := range previewed {
			rs = append(rs, edge.Route{Host: h.Host, PathPrefix: PathPrefix, Upstream: EngineAddr})
			previews = append(previews, project+" "+h.Host)
		}
		for _, r := range rs {
			// Two projects claiming one host would make the edge reject every
			// route; the first project (by name) keeps it.
			if other, dup := seen[r.Host]; dup {
				if p.Log != nil {
					p.Log.Warn("auth: host served by two projects; skipping", "host", r.Host, "kept", other, "skipped", project)
				}
				continue
			}
			seen[r.Host] = project
			out = append(out, r)
		}
	}
	syncPreviews(ctx, p, strings.Join(previews, ","))
	return out, nil
}

// syncPreviews rewrites the engine config when the preview hosts changed.
func syncPreviews(ctx context.Context, p *platform.Platform, key string) {
	lastPreviews.Lock()
	defer lastPreviews.Unlock()
	if lastPreviews.synced && key == lastPreviews.key {
		return
	}
	reconcileMu.Lock()
	_, err := syncConfig(ctx, p, defaultEngine)
	reconcileMu.Unlock()
	lastPreviews.key, lastPreviews.synced = key, err == nil
	if err != nil && p.Log != nil {
		p.Log.Warn("auth: engine config for previews", "err", err)
	}
}

func routesFor(p *platform.Platform, res map[string]change.Resource) []edge.Route {
	var out []edge.Route
	for _, h := range webHosts(p, res) {
		out = append(out, edge.Route{Host: h.Host, PathPrefix: PathPrefix, Upstream: EngineAddr})
	}
	return out
}

// Checks reports the engine's health once auth is in use on the box.
func (*Module) Checks(ctx context.Context, p *platform.Platform) []platform.Check {
	if _, err := os.Stat(ConfigPath(p)); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var h struct {
		Projects []string `json:"projects"`
	}
	if err := defaultEngine.do(ctx, "GET", "/health", nil, nil, &h); err != nil {
		return []platform.Check{{Name: "auth", OK: false, Detail: err.Error()}}
	}
	detail := "serving no projects"
	switch n := len(h.Projects); n {
	case 0:
	case 1:
		detail = "serving 1 project"
	default:
		detail = "serving " + strconv.Itoa(n) + " projects"
	}
	return []platform.Check{{Name: "auth", OK: true, Detail: detail}}
}
