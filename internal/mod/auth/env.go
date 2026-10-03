package auth

import (
	"context"
	"os"
	"strconv"
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
		"TIFFIN_AUTH_INTERNAL_URL": "http://" + EngineAddr + PathPrefix,
		"TIFFIN_AUTH_HOST":         host,
		"TIFFIN_AUTH_JWKS_URL":     base + "/jwks",
	}, nil
}

// Routes sends /api/auth on every web app host of auth-enabled projects to
// the engine. Longer path prefixes win at the edge, so the app keeps the rest.
func (*Module) Routes(ctx context.Context, p *platform.Platform) ([]edge.Route, error) {
	projects, err := p.DB.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	var out []edge.Route
	for _, project := range projects {
		_, res, err := p.DB.Load(ctx, project)
		if err != nil {
			return nil, err
		}
		if !hasAuth(res) {
			continue
		}
		out = append(out, routesFor(p, res)...)
	}
	return out, nil
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
