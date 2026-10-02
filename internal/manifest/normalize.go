package manifest

import (
	"slices"
	"strings"
)

// Defaults applied by Normalize.
const (
	DefaultAppPath     = "."
	DefaultFramework   = FrameworkBun
	DefaultRole        = RoleWeb
	DefaultInstances   = 1
	DefaultMemoryMB    = 512
	DefaultHealthcheck = "/"
	DefaultValkeyMemMB = 64
)

// Normalize fills in defaults and canonicalizes values in place, returning m
// for chaining. It is idempotent. It does not validate: call Validate after.
//
//   - version 1; app path ".", framework "bun", role "web", instances 1,
//     memoryMB 512
//   - web apps get routes [appName] when none are given; workers get none
//   - web, non-static apps get healthcheck "/"
//   - valkey maxMemoryMB 64
//   - postgres extensions are sorted and de-duplicated
//   - route hostnames are lowercased and path prefixes lose trailing slashes
func Normalize(m *Manifest) *Manifest {
	if m.Version == 0 {
		m.Version = Version
	}
	for name, app := range m.Apps {
		if app.Path == "" {
			app.Path = DefaultAppPath
		}
		if app.Framework == "" {
			app.Framework = DefaultFramework
		}
		if app.Role == "" {
			app.Role = DefaultRole
		}
		if app.Instances == 0 {
			app.Instances = DefaultInstances
		}
		if app.MemoryMB == 0 {
			app.MemoryMB = DefaultMemoryMB
		}
		if app.Role == RoleWeb {
			if len(app.Routes) == 0 {
				app.Routes = []string{name}
			}
			if app.Framework != FrameworkStatic && app.Healthcheck == "" {
				app.Healthcheck = DefaultHealthcheck
			}
		}
		if len(app.Routes) > 0 {
			routes := make([]string, len(app.Routes))
			for i, r := range app.Routes {
				routes[i] = normalizeRoute(r)
			}
			app.Routes = routes
		} else {
			app.Routes = nil
		}
		m.Apps[name] = app
	}
	if v := m.Services.Valkey; v != nil && v.MaxMemoryMB == 0 {
		v.MaxMemoryMB = DefaultValkeyMemMB
	}
	if pg := m.Services.Postgres; pg != nil {
		exts := slices.Clone(pg.Extensions)
		slices.Sort(exts)
		exts = slices.Compact(exts)
		if len(exts) == 0 {
			exts = nil
		}
		pg.Extensions = exts
	}
	return m
}

// normalizeRoute lowercases the host part and strips trailing slashes from
// the path prefix: "Example.com/API/" -> "example.com/API".
func normalizeRoute(r string) string {
	host, path, hasPath := strings.Cut(r, "/")
	host = strings.ToLower(host)
	if !hasPath {
		return host
	}
	path = strings.TrimRight(path, "/")
	if path == "" {
		return host
	}
	return host + "/" + path
}
