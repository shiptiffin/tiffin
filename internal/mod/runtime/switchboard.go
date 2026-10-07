package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/edge"
	"github.com/btahir/tiffin/internal/edge/switchboard"
	"github.com/btahir/tiffin/internal/manifest"
)

// The switchboard (internal/edge/switchboard) runs in the edge process,
// between Caddy and app instances. The runtime is its control plane: it
// sends the switchboard a new table (routes to environments, each
// environment's instances) on every change and waits for the edge to serve
// it, so deploys, rollbacks and restarts switch instances without reloading
// Caddy. The switchboard calls back for what needs the runtime: waking a
// sleeping app, live progress and bucket images (Module.Wake, Forward).

// stateCache mirrors app states for the switchboard table (the database
// stays the source of truth).
type stateCache struct {
	mu sync.RWMutex
	m  map[string]*AppState
}

func newStateCache() *stateCache { return &stateCache{m: map[string]*AppState{}} }

func (c *stateCache) put(st *AppState) {
	cp := *st
	cp.Instances = append([]Instance(nil), st.Instances...)
	cp.Retired = append([]Retired(nil), st.Retired...)
	c.mu.Lock()
	c.m[envKey(st.Project, st.App, st.Preview)] = &cp
	c.mu.Unlock()
}

func (c *stateCache) del(key string) {
	c.mu.Lock()
	delete(c.m, key)
	c.mu.Unlock()
}

func (c *stateCache) get(key string) *AppState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.m[key]
}

// table is the switchboard's table as the runtime sees it now.
func (r *rt) table() switchboard.Table {
	r.mu.Lock()
	hosts := make(map[string][]switchboard.Route, len(r.dispatch))
	for h, rs := range r.dispatch {
		hosts[h] = append([]switchboard.Route(nil), rs...)
	}
	r.mu.Unlock()
	r.st.cache.mu.RLock()
	states := make([]*AppState, 0, len(r.st.cache.m))
	for _, st := range r.st.cache.m {
		states = append(states, st)
	}
	r.st.cache.mu.RUnlock()
	t := switchboard.Table{Hosts: hosts, Envs: make(map[string]*switchboard.Env, len(states)), Files: r.p.URL(r.p.Host("files"))}
	for _, st := range states {
		e := &switchboard.Env{Project: st.Project, App: st.App, Preview: st.Preview, Live: st.Live, Stopped: st.Stopped, Sleeping: st.Sleeping,
			Assets: r.assetsDir(st.Project, st.App, st.Preview), Timeout: r.requestTimeout(r.ctx, st.Project, st.App), Cgroup: r.peerDir(st.Project)}
		for _, in := range st.Instances {
			e.Instances = append(e.Instances, switchboard.Instance{Name: in.Name, Port: in.Port})
		}
		for _, rt := range st.Retired {
			e.Retired = append(e.Retired, switchboard.Retired{Deploy: rt.Deploy, At: rt.At})
		}
		t.Envs[envKey(st.Project, st.App, st.Preview)] = e
	}
	return t
}

// syncEdge sends the switchboard table to the edge and returns once the
// edge serves it. Without an edge (off-box) there is nothing to do.
func (r *rt) syncEdge() error {
	if r.p.Edge == nil {
		return nil
	}
	return r.p.Edge.SyncTable()
}

// switchboardAddr is where routes to app instances point.
func (r *rt) switchboardAddr() string {
	if r.p.Edge != nil {
		return r.p.Edge.Switchboard()
	}
	return r.actAddr // off-box nothing routes to it
}

// busy counts the requests in flight on instances. When the edge cannot
// say, they count as busy.
func (r *rt) busy(ins []Instance) int64 {
	if r.p.Edge == nil || len(ins) == 0 {
		return 0
	}
	names := make([]string, len(ins))
	for i, in := range ins {
		names[i] = in.Name
	}
	n, err := r.p.Edge.Busy(names)
	if err != nil {
		return 1
	}
	return n
}

// pullActivity merges in the requests the switchboard saw. The edge keeps
// them while the control plane is down, so a restart loses none.
func (r *rt) pullActivity() {
	if r.p.Edge == nil {
		return
	}
	seen, err := r.p.Edge.Activity()
	if err != nil {
		return
	}
	for k, t := range seen {
		r.touchAt(k, t)
	}
}

// drainThenRemove stops old instances once the edge has switched away from
// them and they have no requests in flight (at most Drain later), giving
// them RetireGrace to finish work they do after responding. While the edge
// is out of reach they keep running: it may still send them requests.
func (r *rt) drainThenRemove(old []Instance) {
	deadline := time.Now().Add(r.opt.Drain)
	for r.syncEdge() != nil && time.Now().Before(deadline) {
		time.Sleep(time.Second)
	}
	time.Sleep(50 * time.Millisecond) // let responses being written finish flushing
	// Polled every 250 ms: each check asks the edge, and a release that
	// drains is stopped with RetireGrace to spare anyway.
	for r.busy(old) > 0 && time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
	}
	r.removeInstancesGrace(r.ctx, old, max(r.opt.RetireGrace, r.opt.StopGrace))
}

func (r *rt) setDispatch(t map[string][]switchboard.Route) {
	r.mu.Lock()
	r.dispatch = t
	r.mu.Unlock()
}

// routesChanged reports whether the edge needs new routes (hosts, paths,
// file roots), as opposed to only instances changing behind the switchboard.
func (r *rt) routesChanged(ctx context.Context) bool {
	routes, _, err := r.routes(ctx)
	if err != nil {
		return true // RefreshRoutes reports it, and the edge keeps what it has
	}
	h := routesHash(routes)
	r.mu.Lock()
	defer r.mu.Unlock()
	return h != r.loadedRoutes
}

func routesHash(rs []edge.Route) string {
	b, _ := json.Marshal(rs)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// refreshIfNeeded reloads the edge's routes only when the runtime's routes
// changed; otherwise it sends the switchboard table alone.
func (r *rt) refreshIfNeeded(ctx context.Context) error {
	if !r.routesChanged(ctx) {
		return r.syncEdge()
	}
	return r.p.RefreshRoutes(ctx)
}

func (r *rt) setTimeout(project, app string, d time.Duration) {
	r.mu.Lock()
	old, had := r.timeouts[project+"/"+app]
	r.timeouts[project+"/"+app] = d
	r.mu.Unlock()
	if had && old != d {
		_ = r.syncEdge() // the next request gets the new limit
	}
}

// requestTimeout is how long one request to the app may take: its
// timeoutSeconds, read once and then kept current by Reconcile.
func (r *rt) requestTimeout(ctx context.Context, project, app string) time.Duration {
	r.mu.Lock()
	d, ok := r.timeouts[project+"/"+app]
	r.mu.Unlock()
	if ok {
		return d
	}
	spec, err := r.appSpec(ctx, project, app)
	if err != nil {
		return manifest.DefaultTimeout
	}
	r.mu.Lock()
	r.timeouts[project+"/"+app] = spec.Timeout()
	r.mu.Unlock()
	return spec.Timeout()
}

// Wake starts a sleeping app environment for the switchboard ("project/app"
// or "project/app@preview"). It returns once the edge has its instances.
func (m *Module) Wake(ctx context.Context, env string) (bool, error) {
	r, err := m.rt()
	if err != nil {
		return false, err
	}
	project, app, preview := splitEnvKey(env)
	_, woke, err := r.wake(ctx, project, app, preview, wakeRequest)
	return woke, err
}

// WokeFirstByte records how long a request that woke its app waited for
// its first byte.
func (m *Module) WokeFirstByte(env string, secs float64) {
	if r, err := m.rt(); err == nil {
		project, app, preview := splitEnvKey(env)
		r.wokeFirstByte(project, app, preview, secs)
	}
}

// Forward answers what the switchboard hands over: live progress and
// images in the project's own buckets.
func (m *Module) Forward(w http.ResponseWriter, req *http.Request, env, prefix string) {
	if strings.HasPrefix(req.URL.Path, switchboard.LivePrefix+"/") {
		serveLive(w, req)
		return
	}
	if r, err := m.rt(); err == nil {
		if st := r.st.cache.get(env); st != nil && r.serveBucketImage(w, req, st, prefix) {
			return
		}
	}
	http.NotFound(w, req)
}

// splitEnvKey is envKey's inverse.
func splitEnvKey(k string) (project, app, preview string) {
	project, rest, _ := strings.Cut(k, "/")
	app, preview, _ = strings.Cut(rest, "@")
	return project, app, preview
}

// staticLink is the stable path the edge serves a static environment from;
// promote points it at the live deploy's files with an atomic rename.
func (r *rt) staticLink(project, app, preview string) string {
	env := "prod"
	if preview != "" {
		env = "pr-" + preview
	}
	return filepath.Join(r.opt.DataDir, "static", project, app, "live-"+env)
}

func swapSymlink(link, target string) error {
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	tmp := link + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, link)
}
