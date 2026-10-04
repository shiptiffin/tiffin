package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/edge"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/platform"
)

// Routes sends each live web app's hosts to its instances (or its files, for
// static sites) and every preview host to the activator, which wakes
// sleeping previews on their first request.
func (m *Module) Routes(ctx context.Context, p *platform.Platform) ([]edge.Route, error) {
	r, err := m.rt()
	if err != nil {
		return nil, nil
	}
	routes, _ := r.routes(ctx)
	r.mu.Lock()
	r.loadedRoutes = routesHash(routes)
	r.mu.Unlock()
	return routes, nil
}

// routeConflict is a route two apps claim; the first (by project, app) wins.
type routeConflict struct{ Key, Winner, Loser string }

func (r *rt) routes(ctx context.Context) ([]edge.Route, []routeConflict) {
	states, err := r.st.allStates(ctx)
	if err != nil {
		r.p.Log.Error("runtime routes", "err", err)
		return nil, nil
	}
	specs := map[string]map[string]*manifest.App{} // project → app → spec
	specOf := func(project, app string) *manifest.App {
		if _, ok := specs[project]; !ok {
			specs[project] = map[string]*manifest.App{}
			if _, res, err := r.p.DB.Load(ctx, project); err == nil {
				for addr, rs := range res {
					if change.Kind(addr) == change.KindApp {
						var a manifest.App
						if json.Unmarshal(rs.Spec, &a) == nil {
							specs[project][change.Name(addr)] = &a
						}
					}
				}
			}
		}
		return specs[project][app]
	}
	var out []edge.Route
	var conflicts []routeConflict
	owner := map[string]string{}
	table := map[string][]dispatchEntry{}
	add := func(rt edge.Route, who, env string) {
		key := rt.Host + rt.PathPrefix
		if w, taken := owner[key]; taken {
			conflicts = append(conflicts, routeConflict{Key: key, Winner: w, Loser: who})
			return
		}
		owner[key] = who
		out = append(out, rt)
		if rt.FileRoot == "" {
			table[rt.Host] = append(table[rt.Host], dispatchEntry{prefix: rt.PathPrefix, env: env})
		}
	}
	for _, st := range states {
		if st.Live == "" || st.Stopped {
			continue
		}
		spec := specOf(st.Project, st.App)
		if spec == nil || spec.Role == manifest.RoleWorker {
			continue
		}
		d, err := r.st.getDeploy(ctx, st.Project, st.App, st.Live)
		if err != nil {
			continue
		}
		who := st.Project + "/" + st.App
		env := envKey(st.Project, st.App, st.Preview)
		static := d.StaticRoot != ""
		spa := strings.HasSuffix(d.Framework, "+spa")
		if st.Preview != "" {
			who += "@" + st.Preview
			rt := edge.Route{Host: previewHost(st.Preview, st.App, r.p.Domain)}
			if static {
				rt.FileRoot, rt.SPA = r.staticLink(st.Project, st.App, st.Preview), spa
			} else {
				rt.Upstream = r.actAddr // the switchboard wakes the preview if it sleeps
			}
			add(rt, who, env)
			continue
		}
		routes := spec.Routes
		if len(routes) == 0 {
			routes = []string{st.App}
		}
		for _, rs := range routes {
			host, prefix := r.splitRoute(rs)
			rt := edge.Route{Host: host, PathPrefix: prefix}
			if static {
				rt.FileRoot, rt.SPA = r.staticLink(st.Project, st.App, ""), spa
			} else {
				rt.Upstream = r.actAddr // instances are switched behind the switchboard
			}
			add(rt, who, env)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Host+out[i].PathPrefix < out[j].Host+out[j].PathPrefix })
	r.setDispatch(table)
	for _, c := range conflicts {
		r.p.Log.Warn("route conflict", "route", c.Key, "served_by", c.Winner, "ignored", c.Loser)
	}
	return out, conflicts
}

// routeKeys returns host+path for every route a project's web apps claim,
// mapped to the app that claims it.
func routeKeys(p *platform.Platform, res map[string]change.Resource) map[string]string {
	out := map[string]string{}
	for addr, rs := range res {
		if change.Kind(addr) != change.KindApp {
			continue
		}
		var a manifest.App
		if json.Unmarshal(rs.Spec, &a) != nil || a.Role == manifest.RoleWorker {
			continue
		}
		routes := a.Routes
		if len(routes) == 0 {
			routes = []string{change.Name(addr)}
		}
		for _, route := range routes {
			host, rest, hasPath := strings.Cut(route, "/")
			if !strings.Contains(host, ".") {
				host = p.Host(host)
			}
			key := strings.ToLower(host)
			if hasPath && strings.Trim(rest, "/") != "" {
				key += "/" + strings.Trim(rest, "/")
			}
			out[key] = change.Name(addr)
		}
	}
	return out
}

// CheckPlan refuses a route another project already serves: the edge would
// keep sending it to the other project, so this app would deploy "live" at an
// address that never reaches it. Routes the project already has are not
// re-checked, so an old clash does not block unrelated changes.
func (m *Module) CheckPlan(ctx context.Context, p *platform.Platform, project string, desired map[string]change.Resource) error {
	want := routeKeys(p, desired)
	if len(want) == 0 {
		return nil
	}
	_, cur, err := p.DB.Load(ctx, project)
	if err != nil {
		return err
	}
	have := routeKeys(p, cur)
	names, err := p.DB.ListProjects(ctx)
	if err != nil {
		return err
	}
	taken := map[string]string{}
	for _, n := range names {
		if n == project {
			continue
		}
		_, res, err := p.DB.Load(ctx, n)
		if err != nil {
			return err
		}
		for k, app := range routeKeys(p, res) {
			taken[k] = n + "/" + app
		}
	}
	var prob *api.Problem
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		owner, clash := taken[k]
		if _, had := have[k]; !clash || had {
			continue
		}
		app := want[k]
		msg := fmt.Sprintf("%s is already served by %s", k, owner)
		if prob == nil {
			suggest := project
			if _, used := taken[p.Host(suggest)]; used {
				suggest = app + "-" + project
			}
			prob = api.NewProblem(422, "validation", "app "+app+" would share an address with another project: "+msg)
			prob.Hint = fmt.Sprintf("An app is served at <app name>.%s unless it sets routes. Give it its own: routes: [%q] (served at %s), or rename the app.",
				p.Domain, suggest, p.Host(suggest))
		}
		prob.Errors = append(prob.Errors, api.FieldError{Path: "/apps/" + app + "/routes", Message: msg})
	}
	if prob == nil {
		return nil
	}
	return prob
}

// serveInternal is the runtime's own localhost listener: the preview
// activator (edge → here → preview instance) and git push hooks.
func (r *rt) serveInternal(ctx context.Context, ln net.Listener) {
	mux := http.NewServeMux()
	mux.HandleFunc("/_tiffin/git-hook", r.handleGitHook)
	mux.HandleFunc("/", r.activate)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	_ = srv.Serve(ln)
}

// activate is the switchboard's front door: it finds the app environment a
// request belongs to (by host and path, as the edge routed it), wakes a
// sleeping preview, and proxies to the least busy instance.
func (r *rt) activate(w http.ResponseWriter, req *http.Request) {
	host := strings.ToLower(req.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	key, ok := r.lookup(host, req.URL.Path)
	if !ok {
		r.routes(req.Context()) // rebuild the table (first request after a start)
		if key, ok = r.lookup(host, req.URL.Path); !ok {
			http.Error(w, "no app is served here", http.StatusNotFound)
			return
		}
	}
	if st := r.st.cache.get(key); st != nil && st.Preview != "" {
		r.mu.Lock()
		r.lastSeen[key] = time.Now()
		r.mu.Unlock()
		defer func() {
			r.mu.Lock()
			r.lastSeen[key] = time.Now()
			r.mu.Unlock()
		}()
		if st.Sleeping || len(st.Instances) == 0 {
			if _, err := r.wake(req.Context(), st.Project, st.App, st.Preview); err != nil {
				w.Header().Set("Retry-After", "5")
				http.Error(w, "the preview could not start: "+err.Error(), http.StatusServiceUnavailable)
				return
			}
		}
	}
	r.serveApp(w, req, key)
}

// wake returns a running instance's port for a preview, starting it first if
// it sleeps.
func (r *rt) wake(ctx context.Context, project, app, preview string) (int, error) {
	unlock := r.lock(envKey(project, app, preview))
	defer unlock()
	st, err := r.st.getState(ctx, project, app, preview)
	if err != nil {
		return 0, err
	}
	if len(st.Instances) > 0 && !st.Sleeping {
		return st.Instances[0].Port, nil
	}
	d, err := r.st.getDeploy(ctx, project, app, st.Live)
	if err != nil {
		return 0, err
	}
	spec, err := r.appSpec(ctx, project, app)
	if err != nil {
		return 0, err
	}
	began := time.Now()
	// Detach from the request: a client giving up must not abort the start.
	if err := r.promoteLocked(r.ctx, d, spec, modeWake, io.Discard); err != nil {
		return 0, err
	}
	st, _ = r.st.getState(ctx, project, app, preview)
	r.p.Log.Info("preview woke", "project", project, "app", app, "preview", preview, "seconds", round1(time.Since(began).Seconds()))
	if len(st.Instances) == 0 {
		return 0, fmt.Errorf("no instance")
	}
	return st.Instances[0].Port, nil
}

// sleepIdlePreviews stops previews nobody requested for PreviewIdle.
func (r *rt) sleepIdlePreviews(ctx context.Context) {
	states, err := r.st.allStates(ctx)
	if err != nil {
		return
	}
	for _, s := range states {
		if s.Preview == "" || s.Sleeping || len(s.Instances) == 0 {
			continue
		}
		key := envKey(s.Project, s.App, s.Preview)
		r.mu.Lock()
		seen, ok := r.lastSeen[key]
		if !ok {
			// Unknown since the box started: count from its last change.
			seen = s.UpdatedAt
			r.lastSeen[key] = seen
		}
		r.mu.Unlock()
		if time.Since(seen) < r.opt.PreviewIdle {
			continue
		}
		r.sleep(ctx, s.Project, s.App, s.Preview)
	}
}

func (r *rt) sleep(ctx context.Context, project, app, preview string) {
	unlock := r.lock(envKey(project, app, preview))
	defer unlock()
	st, err := r.st.getState(ctx, project, app, preview)
	if err != nil || st.Sleeping || len(st.Instances) == 0 {
		return
	}
	ins := st.Instances
	st.Instances, st.Sleeping = nil, true
	if r.st.putState(ctx, st) != nil {
		return
	}
	r.removeInstances(ctx, ins)
	r.p.Log.Info("preview asleep", "project", project, "app", app, "preview", preview)
}

// checks reports the container runtime and each app environment.
func (r *rt) checks(ctx context.Context) []platform.Check {
	var out []platform.Check
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cs, err := r.eng.List(ctx)
	if err != nil {
		return append(out, platform.Check{Name: "runtime", OK: false, Detail: "containerd is not answering: " + err.Error()})
	}
	running := map[string]bool{}
	for _, c := range cs {
		running[c.Name] = c.Running
	}
	states, _ := r.st.allStates(ctx)
	var bad []string
	apps := 0
	for _, s := range states {
		if s.Live == "" || s.Stopped || s.Sleeping {
			continue
		}
		apps++
		for _, in := range s.Instances {
			if !running[in.Name] {
				bad = append(bad, in.Name)
			}
		}
	}
	c := platform.Check{Name: "runtime", OK: len(bad) == 0, Detail: fmt.Sprintf("%d app environment(s) live, %d container(s)", apps, len(cs))}
	if len(bad) > 0 {
		c.Detail = "not running: " + strings.Join(bad, ", ")
	}
	out = append(out, c)
	if _, conflicts := r.routes(ctx); len(conflicts) > 0 {
		var parts []string
		for _, cf := range conflicts {
			parts = append(parts, cf.Key+" (served by "+cf.Winner+", not "+cf.Loser+")")
		}
		out = append(out, platform.Check{Name: "routes", OK: false, Detail: "routes claimed twice: " + strings.Join(parts, "; ")})
	}
	return out
}
