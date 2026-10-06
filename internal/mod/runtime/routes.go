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
			rt := edge.Route{Host: previewHost(st.Preview, st.Project, st.App, spec, r.p.AppsDomain())}
			if static {
				rt.FileRoot, rt.SPA = r.staticLink(st.Project, st.App, st.Preview), spa
			} else {
				rt.Upstream = r.actAddr // the switchboard wakes the preview if it sleeps
			}
			add(rt, who, env)
			continue
		}
		for _, rs := range appRoutes(st.Project, st.App, spec) {
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
func routeKeys(p *platform.Platform, project string, res map[string]change.Resource) map[string]string {
	out := map[string]string{}
	for addr, rs := range res {
		if change.Kind(addr) != change.KindApp {
			continue
		}
		var a manifest.App
		if json.Unmarshal(rs.Spec, &a) != nil || a.Role == manifest.RoleWorker {
			continue
		}
		for _, route := range appRoutes(project, change.Name(addr), &a) {
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
	want := routeKeys(p, project, desired)
	if len(want) == 0 {
		return nil
	}
	_, cur, err := p.DB.Load(ctx, project)
	if err != nil {
		return err
	}
	have := routeKeys(p, project, cur)
	names, err := p.DB.ListProjects(ctx)
	if err != nil {
		return err
	}
	// The box's own names are never an app's.
	taken := map[string]string{p.DashboardHost(): "the dashboard"}
	// "dashboard" stays the box's on the apps domain too: an app there would pass for it.
	for _, n := range []string{"dashboard", "s3", "files", "t", "otel", "errors"} {
		taken[p.Host(n)] = "the box"
	}
	for _, n := range names {
		if n == project {
			continue
		}
		_, res, err := p.DB.Load(ctx, n)
		if err != nil {
			return err
		}
		for k, app := range routeKeys(p, n, res) {
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
			suggest := project + "-" + app
			if _, used := taken[p.Host(suggest)]; used || k == p.Host(suggest) {
				suggest += "-2"
			}
			prob = api.NewProblem(422, "validation", "app "+app+" would share an address with another project: "+msg)
			prob.Hint = fmt.Sprintf("An app that sets no routes is served at <project>.%s (the main app) or <project>-<app>.%s. Give it a free one: routes: [%q] (served at %s), or rename the project.",
				p.AppsDomain(), p.AppsDomain(), suggest, p.Host(suggest))
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
	key, prefix, ok := r.lookup(host, req.URL.Path)
	if !ok {
		r.routes(req.Context()) // rebuild the table (first request after a start)
		if key, prefix, ok = r.lookup(host, req.URL.Path); !ok {
			http.Error(w, "no app is served here", http.StatusNotFound)
			return
		}
	}
	st := r.st.cache.get(key)
	if st != nil && st.Preview != "" {
		r.mu.Lock()
		r.lastSeen[key] = time.Now()
		r.mu.Unlock()
		defer func() {
			r.mu.Lock()
			r.lastSeen[key] = time.Now()
			r.mu.Unlock()
		}()
	}
	// Client assets come from the box's copy of them; a sleeping preview
	// need not wake for them.
	if st != nil && !st.Stopped && r.serveAsset(w, req, st, prefix) {
		return
	}
	if st != nil && st.Preview != "" {
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
// stuckAfter is how long a deploy may stay queued, building or starting
// before status calls it stuck: past the build timeout and a long queue.
const stuckAfter = time.Hour

// stuckDeploys lists deploys (project/app/id: status) that have not
// finished within stuckAfter.
func (r *rt) stuckDeploys(ctx context.Context) []string {
	projects, err := r.p.DB.ListProjects(ctx)
	if err != nil {
		return nil
	}
	var out []string
	for _, pr := range projects {
		_, res, err := r.p.DB.Load(ctx, pr)
		if err != nil {
			continue
		}
		for addr := range res {
			if change.Kind(addr) != change.KindApp {
				continue
			}
			ds, _ := r.st.listDeploys(ctx, pr, change.Name(addr), "*")
			for _, d := range ds {
				if !d.Terminal() && time.Since(d.CreatedAt) > stuckAfter {
					out = append(out, fmt.Sprintf("%s/%s/%s (%s)", d.Project, d.App, d.ID, d.Status))
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

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
	if names := r.orphans(cs); len(names) > 0 {
		// A failed start's container is usually gone within a sweep; only one
		// that outlives two sweeps is a problem.
		old := r.lingering(names, 10*time.Minute)
		c := platform.Check{Name: "containers", OK: len(old) == 0, Detail: fmt.Sprintf("removing %d leftover container(s): %s", len(names), strings.Join(names, ", "))}
		if len(old) > 0 {
			c.Detail = fmt.Sprintf("%d container(s) no app runs, for over 10 minutes: %s. journalctl -u tiffin says why the runtime cannot remove them.",
				len(old), strings.Join(old, ", "))
		}
		out = append(out, c)
	}
	if stuck := r.stuckDeploys(ctx); len(stuck) > 0 {
		out = append(out, platform.Check{Name: "deploys", OK: false, Detail: "stuck for over " + stuckAfter.String() + ": " + strings.Join(stuck, ", ") +
			". Restarting the service fails them and frees the builder: sudo systemctl restart tiffin."})
	}
	if _, conflicts := r.routes(ctx); len(conflicts) > 0 {
		var parts []string
		for _, cf := range conflicts {
			parts = append(parts, cf.Key+" (served by "+cf.Winner+", not "+cf.Loser+")")
		}
		out = append(out, platform.Check{Name: "routes", OK: false, Detail: "routes claimed twice: " + strings.Join(parts, "; ")})
	}
	return out
}

// lingering remembers when each leftover container was first seen and
// returns those seen longer ago than d.
func (r *rt) lingering(names []string, d time.Duration) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	seen := map[string]time.Time{}
	var old []string
	for _, n := range names {
		at, ok := r.orphanAt[n]
		if !ok {
			at = now
		}
		seen[n] = at
		if now.Sub(at) > d {
			old = append(old, n)
		}
	}
	r.orphanAt = seen
	return old
}
