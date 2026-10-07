package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/edge"
	"github.com/btahir/tiffin/internal/edge/switchboard"
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
	routes, _, err := r.routes(ctx)
	if err != nil {
		return nil, err // the edge keeps the routes it has
	}
	r.mu.Lock()
	r.loadedRoutes = routesHash(routes)
	r.mu.Unlock()
	return routes, nil
}

// PreviewHosts maps the hosts of a project's deployed web app previews to
// their app. The auth module serves /api/auth on them; a preview's first
// deploy and its deletion change the runtime's routes, and the route refresh
// that follows is how it learns.
func (m *Module) PreviewHosts(ctx context.Context, p *platform.Platform, project string) (map[string]string, error) {
	r, err := m.rt()
	if err != nil {
		return nil, nil
	}
	states, err := r.st.allStates(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, st := range states {
		if st.Project != project || st.Preview == "" || st.Live == "" || st.Stopped {
			continue
		}
		spec, err := r.appSpec(ctx, project, st.App)
		if errors.Is(err, errNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if spec.Role == manifest.RoleWorker {
			continue
		}
		out[previewHost(st.Preview, project, st.App, spec, p.AppsDomain())] = st.App
	}
	return out, nil
}

// routeConflict is a route two apps claim; the first (by project, app) wins.
type routeConflict struct{ Key, Winner, Loser string }

// routes returns every route, or an error when the state could not be read
// in full: a partial list would take the missing apps off the edge.
func (r *rt) routes(ctx context.Context) ([]edge.Route, []routeConflict, error) {
	states, err := r.st.allStates(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("runtime routes: %w", err)
	}
	specs := map[string]map[string]*manifest.App{} // project → app → spec
	var loadErr error
	specOf := func(project, app string) *manifest.App {
		if _, ok := specs[project]; !ok {
			specs[project] = map[string]*manifest.App{}
			if _, res, err := r.p.DB.Load(ctx, project); err != nil {
				loadErr = err
			} else {
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
	sb := r.switchboardAddr()
	owner := map[string]string{}
	table := map[string][]switchboard.Route{}
	files := map[string]string{} // static previews' hosts → environment
	add := func(rt edge.Route, who, env string) {
		key := rt.Host + rt.PathPrefix
		if w, taken := owner[key]; taken {
			conflicts = append(conflicts, routeConflict{Key: key, Winner: w, Loser: who})
			return
		}
		owner[key] = who
		out = append(out, rt)
		if rt.FileRoot == "" {
			table[rt.Host] = append(table[rt.Host], switchboard.Route{Prefix: rt.PathPrefix, Env: env})
		}
	}
	for _, st := range states {
		if st.Live == "" || st.Stopped {
			continue
		}
		spec := specOf(st.Project, st.App)
		if loadErr != nil {
			return nil, nil, fmt.Errorf("runtime routes: %w", loadErr)
		}
		if spec == nil || spec.Role == manifest.RoleWorker {
			continue
		}
		d, err := r.st.getDeploy(ctx, st.Project, st.App, st.Live)
		if errors.Is(err, errNotFound) {
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("runtime routes: %w", err)
		}
		who := st.Project + "/" + st.App
		env := envKey(st.Project, st.App, st.Preview)
		static := d.StaticRoot != ""
		spa := strings.HasSuffix(d.Framework, "+spa")
		next := !static && spec.Framework == manifest.FrameworkNext
		var rules *edge.Rules
		if d.Vercel != nil {
			rules = d.Vercel.Rules
		}
		if st.Preview != "" {
			who += "@" + st.Preview
			rt := edge.Route{Host: previewHost(st.Preview, st.Project, st.App, spec, r.p.AppsDomain()), Rules: rules, NextCache: next}
			if static {
				files[rt.Host] = env
				rt.FileRoot, rt.SPA, rt.SPAPage = r.staticLink(st.Project, st.App, st.Preview), spa, d.SPAPage
			} else {
				rt.Upstream = sb // the switchboard wakes the preview if it sleeps
			}
			add(rt, who, env)
			continue
		}
		for _, rs := range appRoutes(st.Project, st.App, spec) {
			host, prefix := r.splitRoute(rs)
			rt := edge.Route{Host: host, PathPrefix: prefix, Rules: rules, NextCache: next}
			if static {
				rt.FileRoot, rt.SPA, rt.SPAPage = r.staticLink(st.Project, st.App, ""), spa, d.SPAPage
			} else {
				rt.Upstream = sb // instances are switched behind the switchboard
			}
			add(rt, who, env)
		}
	}
	out = append(out, liveRoutes(out, owner, sb)...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Host+out[i].PathPrefix < out[j].Host+out[j].PathPrefix })
	r.setDispatch(table)
	r.mu.Lock()
	r.previewFiles = files
	r.mu.Unlock()
	for _, c := range conflicts {
		r.p.Log.Warn("route conflict", "route", c.Key, "served_by", c.Winner, "ignored", c.Loser)
	}
	return out, conflicts, nil
}

// livePrefix is where browsers watch jobs and workflow runs on every app host.
const livePrefix = switchboard.LivePrefix

// liveRoutes sends livePrefix to the switchboard on hosts whose root it does
// not already get: static sites, and hosts where apps only serve paths.
func liveRoutes(routes []edge.Route, owner map[string]string, act string) []edge.Route {
	proxied := map[string]bool{}
	for _, rt := range routes {
		if rt.PathPrefix == "" && rt.FileRoot == "" && rt.RedirectTo == "" {
			proxied[rt.Host] = true
		}
	}
	var out []edge.Route
	seen := map[string]bool{}
	for _, rt := range routes {
		if proxied[rt.Host] || seen[rt.Host] || rt.RedirectTo != "" || owner[rt.Host+livePrefix] != "" {
			continue
		}
		seen[rt.Host] = true
		out = append(out, edge.Route{Host: rt.Host, PathPrefix: livePrefix, Upstream: act})
	}
	return out
}

// liveServer is the queue module's side of live streams (internal/mod/queue/contract.go).
type liveServer interface {
	ServeLive(w http.ResponseWriter, r *http.Request)
}

// serveLive hands a browser's subscription to the queue module; it never
// reaches the app.
func serveLive(w http.ResponseWriter, req *http.Request) {
	for _, mod := range platform.Modules() {
		if ls, ok := mod.(liveServer); ok {
			ls.ServeLive(w, req)
			return
		}
	}
	http.Error(w, "live progress needs the queue module", http.StatusNotFound)
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
// re-checked, so an old clash does not block unrelated changes. It also
// refuses disk folder sizes that cannot work (see checkDisks).
func (m *Module) CheckPlan(ctx context.Context, p *platform.Platform, project string, desired map[string]change.Resource) error {
	if err := checkDisks(ctx, p, project, desired); err != nil {
		return err
	}
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

// serveInternal is the runtime's own localhost listener, for git push hooks.
func (r *rt) serveInternal(ctx context.Context, ln net.Listener) {
	mux := http.NewServeMux()
	mux.HandleFunc("/_tiffin/git-hook", r.handleGitHook)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	_ = srv.Serve(ln)
}

// expirePreviews deletes the previews nobody requested or deployed to for
// PreviewExpire, as closing their pull request would. Previews with
// instances qualify once asleep: their state is last written when they
// fall asleep, after their last request, so the clock survives a restart.
// Static previews never sleep (the edge serves their files): their
// requests come from the edge's access log (NoteHostActivity), saved with
// the rest of the activity every minute.
func (r *rt) expirePreviews(ctx context.Context) {
	r.pullActivity()
	states, err := r.st.allStates(ctx)
	if err != nil {
		return
	}
	for _, s := range states {
		if s.Preview == "" || s.Stopped || s.Live == "" || time.Since(s.UpdatedAt) < r.opt.PreviewExpire {
			continue
		}
		if !s.Sleeping {
			if d, err := r.st.getDeploy(ctx, s.Project, s.App, s.Live); err != nil || d.StaticRoot == "" {
				continue
			}
		}
		r.mu.Lock()
		seen := r.lastSeen[envKey(s.Project, s.App, s.Preview)]
		r.mu.Unlock()
		ds, err := r.st.listDeploys(ctx, s.Project, s.App, s.Preview)
		if err != nil || time.Since(seen) < r.opt.PreviewExpire || (len(ds) > 0 && time.Since(ds[0].CreatedAt) < r.opt.PreviewExpire) {
			continue
		}
		if err := r.deletePreview(ctx, s.Project, s.App, s.Preview); err != nil {
			r.p.Log.Warn("runtime: delete an unused preview", "project", s.Project, "app", s.App, "preview", s.Preview, "err", err)
			continue
		}
		r.p.Log.Info("preview deleted: unused", "project", s.Project, "app", s.App, "preview", s.Preview, "after", r.opt.PreviewExpire)
		if d, err := r.st.getDeploy(ctx, s.Project, s.App, s.Live); err == nil && d.PullRequest > 0 {
			r.expireReport(ctx, s.Project, s.App, d.PullRequest)
		}
	}
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
	if c := r.diskCheck(ctx); c != nil {
		out = append(out, *c)
	}
	if _, conflicts, _ := r.routes(ctx); len(conflicts) > 0 {
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
