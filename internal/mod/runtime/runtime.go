// Package runtime is the Tiffin app runtime: builds (Railpack + BuildKit),
// containers (containerd via nerdctl), zero-downtime deploys, rollbacks,
// previews with scale-to-zero, logs and a git push endpoint.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/platform"
)

func init() { platform.Register(&Module{}) }

// Module implements the runtime module. See internal/platform for the optional interfaces.
type Module struct {
	mu sync.Mutex
	r  *rt // set by Start; nil until the box serves
}

func (*Module) Name() string { return "runtime" }
func (*Module) Order() int   { return 40 }

// Options tune the runtime (tests shorten the timings).
type Options struct {
	DataDir       string        // sources, build logs, static files, git repos
	LogDir        string        // app container logs
	HealthTimeout time.Duration // how long new instances get to pass health checks
	Drain         time.Duration // longest old instances may take to finish in-flight requests after a switch
	StopGrace     time.Duration // SIGTERM → SIGKILL
	PreviewIdle   time.Duration // previews sleep after this long without requests
	KeepImages    int           // rollback targets kept per app environment
	Engine        Engine
	Builder       Builder
}

func defaultOptions() Options {
	idle := 15 * time.Minute
	if v, err := time.ParseDuration(os.Getenv("TIFFIN_PREVIEW_IDLE")); err == nil && v > 0 {
		idle = v
	}
	return Options{DataDir: DataDir, LogDir: LogDir, HealthTimeout: 120 * time.Second, Drain: 30 * time.Second,
		StopGrace: 10 * time.Second, PreviewIdle: idle, KeepImages: 5}
}

// rt is the running runtime.
type rt struct {
	p     *platform.Platform
	opt   Options
	st    store
	eng   Engine
	bld   Builder
	ctx   context.Context // box lifetime
	build chan struct{}   // one build at a time
	warm  warmSlot        // the build warm-up's claim on the slot (deploys preempt it)

	mu       sync.Mutex
	locks    map[string]*sync.Mutex // per app environment
	ports    map[int]string         // allocated port → container
	lastSeen map[string]time.Time   // preview env key → last request
	orphanAt map[string]time.Time   // leftover container → when status first saw it
	actAddr  string                 // switchboard listener (app traffic, previews, git hooks)
	dispatch map[string][]dispatchEntry
	// loadedRoutes is the hash of the routes the edge last loaded from us.
	loadedRoutes string
	hooks        *hookTokens
	// gitResolve resolves hosts of git URLs to deploy from (nil: DNS).
	gitResolve resolver
	// gh is the GitHub connection and its deploy queue.
	gh ghState
}

// Start wires the runtime to the platform: it fails deploys a restart
// interrupted, restores port bookkeeping and starts the preview activator.
func (m *Module) Start(ctx context.Context, p *platform.Platform) error {
	opt := defaultOptions()
	opt.Engine = newNerdctl()
	// A box keeps its data under /var/lib/tiffin; a laptop running
	// `tiffin serve --box` (the dashboard e2e) keeps it under its home.
	onBox := true
	if p != nil && p.DataRoot != "" && filepath.Clean(p.DataRoot) != filepath.Dir(DataDir) {
		opt.DataDir = filepath.Join(p.DataRoot, "runtime")
		opt.LogDir = filepath.Join(p.DataRoot, "logs", "apps")
		onBox = false
	}
	if err := m.start(ctx, p, opt); err != nil {
		return err
	}
	if _, err := os.Stat("/usr/local/bin/buildctl"); err == nil && onBox {
		if r, err := m.rt(); err == nil {
			go r.warmUp(ctx)
		}
	}
	return nil
}

func (m *Module) start(ctx context.Context, p *platform.Platform, opt Options) error {
	if opt.Builder == nil {
		opt.Builder = &boxBuilder{eng: opt.Engine, staticDir: filepath.Join(opt.DataDir, "static"), memoryMB: buildMemoryMB(memTotalMB())}
	}
	r := &rt{p: p, opt: opt, st: store{db: p.DB, cache: newStateCache()}, eng: opt.Engine, bld: opt.Builder, ctx: ctx,
		build: make(chan struct{}, 1), locks: map[string]*sync.Mutex{}, ports: map[int]string{},
		lastSeen: map[string]time.Time{}, hooks: newHookTokens(), warm: warmSlot{poll: 10 * time.Second, quiet: time.Minute}}
	for _, d := range []string{opt.DataDir, opt.LogDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	if err := r.recover(ctx); err != nil {
		return err
	}
	// App containers use host networking, so box services listening on
	// 127.0.0.1 are reachable from apps. Published for modules that bind
	// per-app listeners (storage, email).
	if err := p.DB.KVPut(ctx, "runtime", "host-ip", []byte("127.0.0.1")); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	r.actAddr = ln.Addr().String()
	go r.serveInternal(ctx, ln)
	go r.loop(ctx)
	go r.resumeReports(ctx)
	m.mu.Lock()
	m.r = r
	m.mu.Unlock()
	return nil
}

func (m *Module) rt() (*rt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.r == nil {
		return nil, errNotReady
	}
	return m.r, nil
}

var errNotReady = errors.New("the app runtime is not running on this box yet")

// recover marks deploys a restart interrupted as failed, rebuilds the
// port table from what is running and removes app containers no state owns.
func (r *rt) recover(ctx context.Context) error {
	states, err := r.st.allStates(ctx)
	if err != nil {
		return err
	}
	for _, s := range states {
		r.st.cache.put(s)
		for _, in := range s.Instances {
			r.ports[in.Port] = in.Name
		}
		for _, ds := range s.Draining {
			for _, in := range ds.Instances {
				r.ports[in.Port] = in.Name
			}
		}
	}
	r.removeOrphans(ctx)
	projects, err := r.p.DB.ListProjects(ctx)
	if err != nil {
		return err
	}
	for _, pr := range projects {
		_, res, err := r.p.DB.Load(ctx, pr)
		if err != nil {
			return err
		}
		for addr := range res {
			if change.Kind(addr) != change.KindApp {
				continue
			}
			ds, err := r.st.listDeploys(ctx, pr, change.Name(addr), "*")
			if err != nil {
				return err
			}
			for _, d := range ds {
				if !d.Terminal() {
					d.Status, d.Error = StatusFailed, "interrupted: the box restarted during the deploy"
					d.Hint = "Deploy again."
					now := time.Now().UTC()
					d.FinishedAt = &now
					_ = r.st.putDeploy(ctx, d)
				}
			}
		}
	}
	return nil
}

// removeOrphans removes the app containers that are neither live, draining
// nor starting: started by a deploy a restart interrupted before it was
// recorded, a crashed start or a stop or drain whose removal failed. It runs
// on start, before any deploy can, and every few minutes after.
func (r *rt) removeOrphans(ctx context.Context) {
	r.sweep(ctx, func(_ Container, owned bool) bool { return !owned })
}

// sweep removes the app containers remove picks. owned: an app environment
// runs or drains it, or a start is under way (its name is in the port
// table from before it ran, so it is never mistaken for an orphan).
func (r *rt) sweep(ctx context.Context, remove func(c Container, owned bool) bool) {
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cs, err := r.eng.List(lctx)
	if err != nil {
		r.p.Log.Warn("runtime: list containers", "err", err)
		return
	}
	owned := r.ownedNames() // after List: anything it saw starting is in the table
	for _, c := range cs {
		if !remove(c, owned[c.Name] != 0) {
			continue
		}
		if err := r.eng.Remove(ctx, c.Name, r.opt.StopGrace); err != nil {
			r.p.Log.Error("runtime: remove container", "container", c.Name, "err", err)
			continue
		}
		if p := owned[c.Name]; p != 0 {
			r.freePort(p)
		}
		r.p.Log.Info("runtime: removed a container", "container", c.Name, "deploy", c.Labels["tiffin.deploy"], "owned", owned[c.Name] != 0)
	}
}

// ownedNames maps the containers in the port table to their ports.
func (r *rt) ownedNames() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]int, len(r.ports))
	for p, name := range r.ports {
		out[name] = p
	}
	return out
}

// orphans lists the app containers nothing owns.
func (r *rt) orphans(cs []Container) []string {
	owned := r.ownedNames()
	var out []string
	for _, c := range cs {
		if owned[c.Name] == 0 {
			out = append(out, c.Name)
		}
	}
	sort.Strings(out)
	return out
}

// ProjectDeleted stops a destroyed project's app environments and removes
// every container labelled for it, live or not.
func (m *Module) ProjectDeleted(ctx context.Context, p *platform.Platform, project string) error {
	r, err := m.rt()
	if err != nil {
		return nil
	}
	states, err := r.st.allStates(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, s := range states {
		if s.Project == project {
			errs = append(errs, r.stopEnv(ctx, s))
		}
	}
	r.sweep(ctx, func(c Container, _ bool) bool { return c.Labels["tiffin.project"] == project })
	return errors.Join(errs...)
}

// loop runs housekeeping: sleeping idle previews, stopping drained releases
// and, every 5 minutes, removing orphaned containers.
func (r *rt) loop(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for tick := 1; ; tick++ {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.sleepIdlePreviews(ctx)
			r.reapDrained(ctx)
			if tick%20 == 0 {
				r.removeOrphans(ctx)
			}
		}
	}
}

func (r *rt) lock(key string) func() {
	r.mu.Lock()
	l, ok := r.locks[key]
	if !ok {
		l = &sync.Mutex{}
		r.locks[key] = l
	}
	r.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// allocPort reserves a free localhost port for a container.
func (r *rt) allocPort(name string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for p := PortMin; p <= PortMax; p++ {
		if _, used := r.ports[p]; used {
			continue
		}
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			continue // something else has it
		}
		ln.Close()
		r.ports[p] = name
		return p, nil
	}
	return 0, errors.New("no free app ports left")
}

func (r *rt) freePort(p int) {
	r.mu.Lock()
	delete(r.ports, p)
	r.mu.Unlock()
}

// appSpec loads an app's spec from the project's applied resources.
func (r *rt) appSpec(ctx context.Context, project, app string) (*manifest.App, error) {
	_, res, err := r.p.DB.Load(ctx, project)
	if err != nil {
		return nil, err
	}
	rs, ok := res[change.KindApp+"/"+app]
	if !ok {
		return nil, errNotFound
	}
	var a manifest.App
	if err := json.Unmarshal(rs.Spec, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// Kinds: the runtime converges app resources, and a project's stop.
func (*Module) Kinds() []string { return []string{change.KindApp, change.KindStopped} }

// Reconcile makes running instances match the app spec and the project's
// env and secrets: a changed env hash, instance count or memory cap
// restarts the app with zero downtime; a deleted app is stopped (its images
// and deploys are kept so an undo brings it back).
func (m *Module) Reconcile(ctx context.Context, p *platform.Platform, project, address string, spec json.RawMessage) error {
	r, err := m.rt()
	if err != nil {
		return nil // not serving (spec build, CLI): nothing to converge
	}
	if address == change.KindStopped {
		return r.reconcileStop(ctx, project, spec != nil)
	}
	app := change.Name(address)
	states, err := r.st.statesOf(ctx, project, app)
	if err != nil {
		return err
	}
	if spec == nil || r.stopped(ctx, project) {
		for _, s := range states {
			if err := r.stopEnv(ctx, s); err != nil {
				return err
			}
		}
		return nil
	}
	var a manifest.App
	if err := json.Unmarshal(spec, &a); err != nil {
		return err
	}
	var errs []error
	for _, s := range states {
		if err := r.converge(ctx, s.Project, s.App, s.Preview, &a); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Checks reports the container runtime and app health.
func (m *Module) Checks(ctx context.Context, p *platform.Platform) []platform.Check {
	r, err := m.rt()
	if err != nil {
		return nil
	}
	return r.checks(ctx)
}
