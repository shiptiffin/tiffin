package auth

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/platform"
)

// ParseRoute splits a manifest app route into its host and path prefix:
// "shop" → ("shop.<box domain>", ""), "example.com/api" → ("example.com", "/api").
// A first label without a dot is a box subdomain.
func ParseRoute(p *platform.Platform, route string) (host, prefix string) {
	route = strings.TrimSpace(strings.ToLower(route))
	host = route
	if i := strings.IndexByte(route, '/'); i >= 0 {
		host, prefix = route[:i], strings.TrimRight(route[i:], "/")
	}
	if !strings.Contains(host, ".") {
		host = p.Host(host)
	}
	return host, prefix
}

// AppHost is one web app host of a project.
type AppHost struct {
	App  string
	Host string
}

// WebHosts lists every host the project's web apps serve, in app then route
// order (the first is the project's primary host). Workers have no hosts.
func WebHosts(ctx context.Context, p *platform.Platform, project string) ([]AppHost, error) {
	_, res, err := p.DB.Load(ctx, project)
	if err != nil {
		return nil, err
	}
	return webHosts(p, res), nil
}

func primaryRank(app string) int {
	if app == "web" {
		return 0
	}
	return 1
}

func webHosts(p *platform.Platform, res map[string]change.Resource) []AppHost {
	var names []string
	for addr := range res {
		if change.Kind(addr) == change.KindApp {
			names = append(names, change.Name(addr))
		}
	}
	// The primary host comes from the app named "web", else the first app by name.
	sort.Slice(names, func(i, j int) bool {
		ri, rj := primaryRank(names[i]), primaryRank(names[j])
		if ri != rj {
			return ri < rj
		}
		return names[i] < names[j]
	})
	var out []AppHost
	seen := map[string]bool{}
	for _, name := range names {
		var app manifest.App
		if json.Unmarshal(res[change.KindApp+"/"+name].Spec, &app) != nil || app.Role == manifest.RoleWorker {
			continue
		}
		routes := app.Routes
		if len(routes) == 0 {
			routes = []string{name}
		}
		for _, r := range routes {
			h, _ := ParseRoute(p, r)
			if h == "" || seen[h] {
				continue
			}
			seen[h] = true
			out = append(out, AppHost{App: name, Host: h})
		}
	}
	return out
}
