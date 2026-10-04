package manifest

import (
	"slices"
	"strings"
)

// Defaults applied by Normalize.
const (
	DefaultAppPath   = "."
	DefaultFramework = FrameworkBun
	DefaultRole      = RoleWeb
	DefaultInstances = 1
	// DefaultMemoryMB is an app's per-copy memory cap when it sets none: 0,
	// no per-copy cap (its copies share the project's memory).
	DefaultMemoryMB    = 0
	DefaultHealthcheck = "/"
	DefaultGitBranch   = "main"
	DefaultValkeyMemMB = 64

	DefaultAnalyticsRetentionDays = 365
	DefaultCronPathPrefix         = "/cron/"

	DefaultQueuePathPrefix = "/queues/"
	DefaultRatePeriodSecs  = 60
	DefaultMaxAttempts     = 8
	DefaultLeaseSeconds    = 60
)

// DefaultAuthMethods is the Auth.Methods default.
var DefaultAuthMethods = []string{AuthEmail, AuthMagicLink}

// Normalize fills in defaults and canonicalizes values in place, returning m
// for chaining. It is idempotent. It does not validate: call Validate after.
//
//   - version 1; app path ".", framework "bun", role "web", instances 1,
//     memoryMB unset (no per-copy cap)
//   - an empty resources object is dropped (automatic)
//   - web apps get routes [appName] when none are given; workers get none
//   - app git: branch "main", previews "same-repo", path without slashes at the ends
//   - web, non-static apps get healthcheck "/"
//   - valkey maxMemoryMB 64
//   - postgres extensions are sorted and de-duplicated
//   - auth methods default to ["email", "magic-link"], sorted and de-duplicated
//     (auth organizations default to true when decoded from JSON)
//   - analytics retentionDays 365
//   - email from is left empty: the box resolves "<project>@<box domain>"
//   - cron path "/cron/<name>"
//   - queue path "/queues/<name>", ratePeriodSeconds 60 when rateLimit is
//     set, maxAttempts 8, leaseSeconds 60
//   - topic subscribers are sorted and de-duplicated
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
		if g := app.Git; g != nil {
			gc := *g
			if gc.Branch == "" {
				gc.Branch = DefaultGitBranch
			}
			if gc.Previews == "" {
				gc.Previews = PreviewsSameRepo
			}
			gc.Path = strings.Trim(gc.Path, "/")
			if gc.Path == "." {
				gc.Path = ""
			}
			app.Git = &gc
		}
		m.Apps[name] = app
	}
	if r := m.Resources; r != nil && *r == (Resources{}) {
		m.Resources = nil
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
	if a := m.Services.Auth; a != nil {
		methods := slices.Clone(a.Methods)
		slices.Sort(methods)
		methods = slices.Compact(methods)
		if len(methods) == 0 {
			methods = slices.Clone(DefaultAuthMethods)
		}
		a.Methods = methods
	}
	if a := m.Services.Analytics; a != nil && a.RetentionDays == 0 {
		a.RetentionDays = DefaultAnalyticsRetentionDays
	}
	for name, c := range m.Crons {
		if c.Path == "" {
			c.Path = DefaultCronPathPrefix + name
		}
		m.Crons[name] = c
	}
	for name, q := range m.Queues {
		if q.Path == "" {
			q.Path = DefaultQueuePathPrefix + name
		}
		if q.RateLimit > 0 && q.RatePeriodSeconds == 0 {
			q.RatePeriodSeconds = DefaultRatePeriodSecs
		}
		if q.MaxAttempts == 0 {
			q.MaxAttempts = DefaultMaxAttempts
		}
		if q.LeaseSeconds == 0 {
			q.LeaseSeconds = DefaultLeaseSeconds
		}
		m.Queues[name] = q
	}
	for name, t := range m.Topics {
		subs := slices.Clone(t.Subscribers)
		slices.Sort(subs)
		subs = slices.Compact(subs)
		if len(subs) == 0 {
			subs = nil
		}
		t.Subscribers = subs
		m.Topics[name] = t
	}
	if len(m.Domains) > 0 {
		ds := make(map[string]Domain, len(m.Domains))
		for name, d := range m.Domains {
			ds[strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")] = d
		}
		m.Domains = ds
	} else {
		m.Domains = nil
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
