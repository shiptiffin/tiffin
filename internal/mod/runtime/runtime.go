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
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/dashboard"
	"github.com/shiptiffin/tiffin/internal/edge/switchboard"
	"github.com/shiptiffin/tiffin/internal/manifest"
	"github.com/shiptiffin/tiffin/internal/mod/budget"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/projicon"
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
	RetireGrace   time.Duration // the same for a replaced release once drained: work after its responses (Next.js after())
	PreviewIdle   time.Duration // previews sleep after this long without requests
	PreviewExpire time.Duration // previews are deleted after this long without requests or deploys
	// SleepAfter replaces the sleepAfter of every project that sets one
	// (0: each project's own). TIFFIN_SLEEP_AFTER sets it, for tests.
	SleepAfter time.Duration
	KeepImages int // rollback targets kept per production environment (previews keep none); each has its own address
	// VersionIdle: an old version woken at its own address sleeps after
	// this long without requests.
	VersionIdle time.Duration
	// DiskUsedPercent reads how full the data disk is (nil: statfs of
	// DataDir). Past the disk guard's warning level gc keeps fewer
	// rollback targets (pressureKeep).
	DiskUsedPercent func() float64
	// ReleaseTimeout is how long an app's release command may run.
	ReleaseTimeout time.Duration
	Engine         Engine
	Builder        Builder
	// Quota is the data disk's project quotas (nil: found on the disk).
	Quota quotaFS
	// Branches makes and drops previews' database branches (nil: the
	// postgres module's).
	Branches BranchStore
	// ReadAccess gives builds read-only database and Valkey users (nil:
	// the postgres and valkey modules').
	ReadAccess ReadAccess
	// PeerCgroup is the cgroup directory whose processes may answer a
	// project's app ports: every connection the box opens to an app is
	// checked against it (internal/peer). Nil checks nothing (tests,
	// whose fake engines serve in process).
	PeerCgroup func(project string) string
	// pipelineDone (tests) is called as a deploy's background pipeline
	// returns, after its last write: the record, the log, the cleanup.
	pipelineDone func(id string)
}

func defaultOptions() Options {
	idle, expire := 15*time.Minute, 7*24*time.Hour
	if v, err := time.ParseDuration(os.Getenv("TIFFIN_PREVIEW_IDLE")); err == nil && v > 0 {
		idle = v
	}
	if v, err := time.ParseDuration(os.Getenv("TIFFIN_PREVIEW_EXPIRE")); err == nil && v > 0 {
		expire = v
	}
	sleepAfter, _ := time.ParseDuration(os.Getenv("TIFFIN_SLEEP_AFTER"))
	// Drain lets a request that was under way when a new version took over
	// finish, however long it takes: the switchboard ends every request at
	// its app's time limit (timeoutSeconds, at most 24 hours). Old instances
	// with no request in flight stop at once.
	return Options{DataDir: DataDir, LogDir: LogDir, HealthTimeout: 120 * time.Second, Drain: 24*time.Hour + time.Minute,
		StopGrace: 10 * time.Second, RetireGrace: 30 * time.Second, PreviewIdle: idle, PreviewExpire: expire, SleepAfter: max(0, sleepAfter),
		KeepImages: keepVersions, VersionIdle: versionIdle, ReleaseTimeout: 10 * time.Minute}
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

	mu    sync.Mutex
	locks map[string]*sync.Mutex // per app environment
	ports map[int]string         // allocated port → container
	// leftover: containers whose removal failed. Their ports stay
	// reserved (they may still listen) until a sweep removes them.
	leftover map[string]bool
	lastSeen map[string]time.Time // env key → last request or delivery
	// seenDirty: lastSeen entries not saved yet.
	seenDirty map[string]bool
	// wakeTried: when sleepIdle last started an app that may no longer sleep.
	wakeTried map[string]time.Time
	orphanAt  map[string]time.Time           // leftover container → when status first saw it
	actAddr   string                         // the runtime's own listener (git hooks)
	dispatch  map[string][]switchboard.Route // the switchboard's routes: host → environments
	// previewFiles: the hosts of static previews (the edge serves their
	// files, so the switchboard never sees their requests) → environment.
	previewFiles map[string]string
	// addrs: what routes found at deploy addresses (see deployaddr.go).
	addrs deployAddrs
	// gateKey signs the deploy-address gate's hand-off links and cookies;
	// the edge holds it too (edge.SetGateSource).
	gateKey []byte
	// loadedRoutes is the hash of the routes the edge last loaded from us;
	// givenRoutes, of those Routes last gave (loaded once RoutesLoaded).
	loadedRoutes, givenRoutes string
	routesMu                  sync.Mutex // one routes() at a time
	hooks                     *hookTokens
	// gitResolve resolves hosts of git URLs to deploy from (nil: DNS).
	gitResolve resolver
	// gh is the GitHub connection and its deploy queue.
	gh    ghState
	keyMu sync.Mutex // creating Next.js Server Actions keys
	// timeouts caches each app's request time limit ("project/app").
	timeouts map[string]time.Duration
	// quotas holds disk folders to their sizes.
	quotas *quotas
	// drains: replaced instances draining before they are removed.
	drains sync.WaitGroup
}

// Start wires the runtime to the platform: it fails deploys a restart
// interrupted, restores port bookkeeping and starts the preview activator.
func (m *Module) Start(ctx context.Context, p *platform.Platform) error {
	opt := defaultOptions()
	opt.Engine = newNerdctl()
	opt.PeerCgroup = func(project string) string { return budget.SliceDir("", project) }
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
		opt.Builder = &boxBuilder{eng: opt.Engine, staticDir: filepath.Join(opt.DataDir, "static"), cacheDir: filepath.Join(opt.DataDir, buildCacheDir), memoryMB: buildMemoryMB(memTotalMB())}
	}
	if opt.Branches == nil {
		opt.Branches = pgBranches{}
	}
	if opt.ReadAccess == nil {
		opt.ReadAccess = boxReadAccess{}
	}
	if opt.ReleaseTimeout <= 0 {
		opt.ReleaseTimeout = 10 * time.Minute
	}
	if opt.VersionIdle <= 0 {
		opt.VersionIdle = versionIdle
	}
	r := &rt{p: p, opt: opt, st: store{db: p.DB, cache: newStateCache()}, eng: opt.Engine, bld: opt.Builder, ctx: ctx,
		build: make(chan struct{}, 1), locks: map[string]*sync.Mutex{}, ports: map[int]string{}, leftover: map[string]bool{},
		lastSeen: map[string]time.Time{}, seenDirty: map[string]bool{}, wakeTried: map[string]time.Time{}, hooks: newHookTokens(), timeouts: map[string]time.Duration{}, warm: warmSlot{poll: 10 * time.Second, quiet: time.Minute}}
	for _, d := range []string{opt.DataDir, opt.LogDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	disks := filepath.Join(opt.DataDir, "disks")
	setDiskRoot(disks)
	if err := os.MkdirAll(disks, 0o755); err != nil {
		return err
	}
	qfs, why := opt.Quota, ""
	if qfs == nil {
		qfs, why = detectQuota(disks)
	}
	r.quotas = newQuotas(qfs, why, p.DB, disks)
	setActiveQuotas(r.quotas)
	if err := r.recover(ctx); err != nil {
		return err
	}
	if err := r.loadActivity(ctx); err != nil {
		return err
	}
	if err := r.loadGateKey(ctx); err != nil {
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
	// The switchboard's table, with its routes, before anything sends it:
	// a table without routes would send the edge's hosts nowhere.
	if _, _, err := r.routes(ctx); err != nil {
		r.p.Log.Error("runtime: the switchboard's first table", "err", err)
	}
	if p.Edge != nil {
		p.Edge.TableSource(r.table)
	}
	r.st.changed = func() {
		if err := r.syncEdge(); err != nil {
			r.p.Log.Warn("runtime: send the switchboard table to the edge", "err", err)
		}
	}
	r.removeOrphans(ctx)
	if h, ok := r.eng.(interface{ RemoveHelpers(context.Context) error }); ok {
		// Before any build: helpers a restart interrupted (static builds,
		// copies out of images) would run on, unowned.
		if err := h.RemoveHelpers(ctx); err != nil {
			r.p.Log.Warn("runtime: remove leftover helper containers", "err", err)
		}
	}
	go r.loop(ctx)
	go r.resumeReports(ctx, true)
	// Connect GitHub posts its form to the GitHub this box uses.
	formOrigins := func() []string {
		if u, err := url.Parse(r.ghEndpoints().Web); err == nil && u.Scheme == "https" && u.Host != "" {
			return []string{"https://" + u.Host}
		}
		return nil
	}
	dashboard.FormOrigins.Store(&formOrigins)
	go r.syncQuotas(ctx)
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
		for _, in := range slices.Concat(s.Instances, s.Parked) {
			r.ports[in.Port] = in.Name
		}
		for _, ds := range s.Draining {
			for _, in := range ds.Instances {
				r.ports[in.Port] = in.Name
			}
		}
	}
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
// on start, before any deploy can, and every few minutes after. Only once
// the edge serves the current table, and an orphan the edge still has
// requests in flight on (old instances a restart interrupted the drain of)
// is left for a later sweep.
func (r *rt) removeOrphans(ctx context.Context) {
	if r.syncEdge() != nil {
		return
	}
	r.sweep(ctx, func(c Container, owned bool) bool { return !owned && r.busy([]Instance{{Name: c.Name}}) == 0 })
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
		r.freeName(c.Name)
		r.p.Log.Info("runtime: removed a container", "container", c.Name, "deploy", c.Labels["tiffin.deploy"], "owned", owned[c.Name] != 0)
	}
}

// ownedNames maps the containers in the port table to their ports, but
// for leftovers (their removal failed: the sweep retries it).
func (r *rt) ownedNames() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]int, len(r.ports))
	for p, name := range r.ports {
		if !r.leftover[name] {
			out[name] = p
		}
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

// ProjectDeleted stops a destroyed project's app environments, removes
// every container labelled for it, live or not, then everything else the
// runtime kept for it (forget): images, deploy records, app states, work
// folders and build logs, static files, logs, client assets and image
// caches. Disk folders go to the trash for diskTrashKeep.
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
	errs = append(errs, r.forget(ctx, project, ""))
	errs = append(errs, r.forgetNextKeys(ctx, project))
	errs = append(errs, projicon.Delete(ctx, p.DB, project))
	return errors.Join(errs...)
}

// loop runs housekeeping: sleeping idle apps, deleting long-unused
// previews, stopping drained releases, saving activity every minute (and
// on the way out), every 5 minutes removing orphaned containers and
// database branches of previews that are gone, and hourly the image sweep
// (sweepImages), first 5 minutes after start.
func (r *rt) loop(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for tick := 1; ; tick++ {
		select {
		case <-ctx.Done():
			sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			r.saveActivity(sctx)
			cancel()
			return
		case <-t.C:
			if r.p.RoutesStale() {
				if err := r.p.RefreshRoutes(ctx); err != nil {
					r.p.Log.Warn("runtime: retry the edge's routes", "err", err)
				}
			}
			r.sleepIdle(ctx)
			r.expirePreviews(ctx)
			r.reapDrained(ctx)
			if tick%4 == 0 {
				r.syncQuotas(ctx)
				r.saveActivity(ctx)
				if r.gh.reporting.CompareAndSwap(false, true) { // GitHub may be slow: not in the loop's way
					go func() {
						defer r.gh.reporting.Store(false)
						r.resumeReports(ctx, false)
					}()
				}
			}
			if tick%20 == 0 {
				r.removeOrphans(ctx)
				r.pruneAssets(ctx)
				r.emptyDiskTrash()
				r.sweepPreviewBranches(ctx)
			}
			// Hourly, and once 5 minutes after start: a box updated more
			// often than hourly would otherwise never sweep.
			if tick%sweepEveryTicks == 0 || tick == sweepFirstTick {
				if _, err := r.sweepImages(ctx, false); err != nil {
					r.p.Log.Warn("runtime: image sweep", "err", err)
				}
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

// portHolder is an Engine that binds the ports it is given itself, from
// allocation until they are freed: the tests' fake, whose containers serve
// in-process. allocPort asks it instead of probing, so no other process
// (another test binary using the same range) can take a port between the
// check and the container's start.
type portHolder interface {
	HoldPort(port int) bool
	ReleasePort(port int)
}

// allocPort reserves a free localhost port for a container.
func (r *rt) allocPort(name string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	holder, _ := r.eng.(portHolder)
	for p := PortMin; p <= PortMax; p++ {
		if _, used := r.ports[p]; used {
			continue
		}
		if holder != nil {
			if !holder.HoldPort(p) {
				continue
			}
		} else {
			ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
			if err != nil {
				continue // something else has it
			}
			ln.Close()
		}
		r.ports[p] = name
		return p, nil
	}
	return 0, errors.New("no free app ports left")
}

// freePort releases port p if container name still holds it: a delayed
// cleanup of a container that is long gone must not free a port another
// container has since been given.
func (r *rt) freePort(p int, name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ports[p] != name {
		return
	}
	delete(r.ports, p)
	if holder, ok := r.eng.(portHolder); ok {
		holder.ReleasePort(p)
	}
}

// freeName releases every port a removed container held.
func (r *rt) freeName(name string) {
	r.mu.Lock()
	var ps []int
	for p, n := range r.ports {
		if n == name {
			ps = append(ps, p)
		}
	}
	delete(r.leftover, name)
	r.mu.Unlock()
	for _, p := range ps {
		r.freePort(p, name)
	}
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
// restarts the app with zero downtime; a deleted app is stopped and keeps
// only each environment's live build, so an undo brings it back, for
// deletedAppKeep (the hourly sweep then forgets the app).
func (m *Module) Reconcile(ctx context.Context, p *platform.Platform, project, address string, spec json.RawMessage) error {
	r, err := m.rt()
	if err != nil {
		return nil // not serving (spec build, CLI): nothing to converge
	}
	if address == change.KindStopped {
		// Jobs are held before the apps go down and let go after they are
		// back, so no delivery meets a stopped app.
		if spec != nil {
			return errors.Join(p.ProjectStopped(ctx, project, true), r.reconcileStop(ctx, project, true))
		}
		return errors.Join(r.reconcileStop(ctx, project, false), p.ProjectStopped(ctx, project, false))
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
		if spec == nil {
			// Caches only: an undo copies the assets out of the image again.
			r.forgetFiles(project, app, "", true)
			r.trashDisks(project, app)
			// Rollback targets and failed builds go now; the live build stays for an undo.
			for _, s := range states {
				r.gcKeeping(ctx, s.Project, s.App, s.Preview, 0)
			}
		}
		// The app's vercel.json crons go with it. A queue that is not
		// running yet converges every project once it is, which comes back here.
		_ = r.syncCrons(ctx, project, app, nil)
		return nil
	}
	var a manifest.App
	if err := json.Unmarshal(spec, &a); err != nil {
		return err
	}
	r.setTimeout(project, app, a.Timeout())
	var errs []error
	for _, s := range states {
		if err := r.converge(ctx, s.Project, s.App, s.Preview, &a); err != nil {
			errs = append(errs, err)
		}
	}
	// A new size applies to running instances' folders too (Committed put
	// it in force already, as the change committed).
	if err := r.applySizes(ctx, project, app); err != nil {
		errs = append(errs, err)
	}
	_ = r.syncCrons(ctx, project, app, nil)
	return errors.Join(errs...)
}

// Committed puts an app's new disk folder sizes in force as its change
// commits: the reconciler's pass comes after apply returns, and a folder an
// apply grows must hold more by then.
func (m *Module) Committed(ctx context.Context, p *platform.Platform, c *change.Change) {
	r, err := m.rt()
	if err != nil {
		return
	}
	for _, op := range c.Plan.Ops {
		if change.Kind(op.Address) != change.KindApp || op.Action == change.Delete {
			continue
		}
		if err := r.applySizes(ctx, c.Project, change.Name(op.Address)); err != nil {
			p.Log.Error("runtime: disk folder sizes", "project", c.Project, "app", change.Name(op.Address), "err", err)
		}
	}
}

// applySizes holds an app's folders in every environment to the sizes of
// its committed spec, read now: a reconcile pass that started before a
// change must not put the older size back.
func (r *rt) applySizes(ctx context.Context, project, app string) error {
	a, err := r.appSpec(ctx, project, app)
	if errors.Is(err, errNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	states, err := r.st.statesOf(ctx, project, app)
	if err != nil {
		return err
	}
	var errs []error
	for _, s := range states {
		w := logWriter{log: r.p.Log, args: []any{"project", s.Project, "app", s.App, "preview", s.Preview}}
		if err := r.quotas.apply(ctx, s.Project, s.App, s.Preview, a.Disk, nil, w); err != nil {
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
