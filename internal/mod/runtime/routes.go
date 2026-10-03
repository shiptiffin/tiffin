package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"time"

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
	add := func(rt edge.Route, who string) {
		key := rt.Host + rt.PathPrefix
		if w, taken := owner[key]; taken {
			conflicts = append(conflicts, routeConflict{Key: key, Winner: w, Loser: who})
			return
		}
		owner[key] = who
		out = append(out, rt)
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
		if st.Preview != "" {
			who += "@" + st.Preview
			rt := edge.Route{Host: previewHost(st.Preview, st.App, r.p.Domain)}
			if d.StaticRoot != "" {
				rt.FileRoot, rt.SPA = d.StaticRoot, strings.HasSuffix(d.Framework, "+spa")
			} else {
				rt.Upstream = r.actAddr // wakes the preview if it sleeps
			}
			add(rt, who)
			continue
		}
		var ups []string
		for _, in := range st.Instances {
			ups = append(ups, fmt.Sprintf("127.0.0.1:%d", in.Port))
		}
		if d.StaticRoot == "" && len(ups) == 0 {
			continue
		}
		routes := spec.Routes
		if len(routes) == 0 {
			routes = []string{st.App}
		}
		for _, rs := range routes {
			host, prefix := r.splitRoute(rs)
			rt := edge.Route{Host: host, PathPrefix: prefix}
			if d.StaticRoot != "" {
				rt.FileRoot, rt.SPA = d.StaticRoot, strings.HasSuffix(d.Framework, "+spa")
			} else {
				rt.Upstreams = ups
			}
			add(rt, who)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Host+out[i].PathPrefix < out[j].Host+out[j].PathPrefix })
	for _, c := range conflicts {
		r.p.Log.Warn("route conflict", "route", c.Key, "served_by", c.Winner, "ignored", c.Loser)
	}
	return out, conflicts
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

// activate proxies a preview request, starting the preview first if it is
// asleep. The edge keeps the original Host header.
func (r *rt) activate(w http.ResponseWriter, req *http.Request) {
	host := strings.ToLower(req.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	label := strings.TrimSuffix(host, "."+r.p.Domain)
	preview, app, ok := strings.Cut(label, "--")
	if !ok || label == host {
		http.Error(w, "not a preview host", http.StatusNotFound)
		return
	}
	ctx := req.Context()
	st := r.findPreview(ctx, app, preview)
	if st == nil {
		http.Error(w, "no such preview", http.StatusNotFound)
		return
	}
	key := envKey(st.Project, st.App, st.Preview)
	r.mu.Lock()
	r.lastSeen[key] = time.Now()
	r.mu.Unlock()
	port, err := r.wake(ctx, st.Project, st.App, st.Preview)
	if err != nil {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "the preview could not start: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = req.Host
			pr.SetXForwarded()
			if v := req.Header.Get("X-Forwarded-Proto"); v != "" {
				pr.Out.Header.Set("X-Forwarded-Proto", v)
			}
			if v := req.Header.Get("X-Forwarded-Host"); v != "" {
				pr.Out.Header.Set("X-Forwarded-Host", v)
			}
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, "preview unavailable: "+err.Error(), http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(w, req)
	r.mu.Lock()
	r.lastSeen[key] = time.Now()
	r.mu.Unlock()
}

func (r *rt) findPreview(ctx context.Context, app, preview string) *AppState {
	states, _ := r.st.allStates(ctx)
	for _, s := range states {
		if s.App == app && s.Preview == preview && s.Live != "" && !s.Stopped {
			return s
		}
	}
	return nil
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
