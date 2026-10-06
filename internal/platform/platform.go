// Package platform is the box's runtime spine. Everything the box does
// beyond recording changes — Postgres, Valkey, app containers, storage,
// email, auth, queues, observability — is a Module that plugs in here.
//
// A module registers itself in init() and implements any of the optional
// interfaces below:
//
//	Provisioner    installs system packages and units (root, idempotent) — `tiffin provision`
//	APIRegistrar   adds API operations; they become CLI commands and MCP tools for free
//	Reconciler     makes the machine match a resource after a Change is applied
//	EnvProvider    contributes env vars to a project's apps (DATABASE_URL, REDIS_URL, ...)
//	RouteProvider  contributes edge routes (app hosts, s3, files, ...)
//	Starter        runs background loops while the box serves
//	Checker        contributes health checks to /v1/status
//	LossEstimator  says what an irreversible op would destroy (rows, files, events)
//	ProjectStopper holds a stopped project's background work (jobs, crons)
//
// Modules never edit each other's files; they meet here.
package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/edge"
	"github.com/btahir/tiffin/internal/edge/switchboard"
	"github.com/btahir/tiffin/internal/state"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
)

// Check is one health check.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Module is a named plug-in. See the package doc for the optional interfaces.
type Module interface{ Name() string }

// Provisioner installs what a module needs on the machine. It runs as root
// from `tiffin provision`, before the service starts, and must be idempotent
// and fast when nothing changed. Pin versions and verify checksums.
type Provisioner interface {
	Provision(ctx context.Context, s *System) error
}

// APIRegistrar adds operations to the API. p is nil when only the OpenAPI
// document is being built (CLI and MCP generation): register operations
// without touching p, and use it only inside handlers.
type APIRegistrar interface {
	RegisterAPI(api huma.API, p *Platform)
}

// Reconciler converges the machine to a resource's desired spec. Kinds are
// resource kinds ("app", "bucket") or exact addresses ("service/postgres").
// spec is nil when the resource was deleted. Reconcile must be idempotent:
// it runs after every apply and again for everything when the box starts.
type Reconciler interface {
	Kinds() []string
	Reconcile(ctx context.Context, p *Platform, project, address string, spec json.RawMessage) error
}

// EnvProvider contributes environment variables to a project's app.
type EnvProvider interface {
	Env(ctx context.Context, p *Platform, project, app string) (map[string]string, error)
}

// RouteProvider contributes edge routes.
type RouteProvider interface {
	Routes(ctx context.Context, p *Platform) ([]edge.Route, error)
}

// Starter starts background work. Start must return promptly; stop when ctx ends.
type Starter interface {
	Start(ctx context.Context, p *Platform) error
}

// ProjectCleaner forgets data it keeps outside resources (logs, error
// issues, analytics) once a project has been destroyed.
type ProjectCleaner interface {
	ProjectDeleted(ctx context.Context, p *Platform, project string) error
}

// Notifier tells the box's owner, once, about something that needs a look
// (a failed Postgres update): the observe module sends it through the
// alert webhook and email and lists it in the alert history.
type Notifier interface {
	Notify(ctx context.Context, subject, summary string)
}

// Notify sends a one-off notice through every Notifier.
func (p *Platform) Notify(ctx context.Context, subject, summary string) {
	for _, m := range Modules() {
		if n, ok := m.(Notifier); ok {
			n.Notify(ctx, subject, summary)
		}
	}
}

// ProjectStopper holds a project's background work (queued jobs, crons)
// while the project is stopped (change.KindStopped) and lets it go when the
// project starts. It must be idempotent: the runtime calls it on every
// reconcile of the stop.
type ProjectStopper interface {
	ProjectStopped(ctx context.Context, p *Platform, project string, stopped bool) error
}

// ProjectStopped tells every ProjectStopper that project stopped or started.
func (p *Platform) ProjectStopped(ctx context.Context, project string, stopped bool) error {
	var errs []error
	for _, m := range Modules() {
		if s, ok := m.(ProjectStopper); ok {
			if err := s.ProjectStopped(ctx, p, project, stopped); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", m.Name(), err))
			}
		}
	}
	return errors.Join(errs...)
}

// LossEstimator measures what an irreversible op would destroy, so a plan
// can say "18,204 rows · 41 MB" rather than "all its data". It runs on
// every plan with such an op, so it must be fast (the whole plan waits at
// most change.LossBudget) and read-only. Return nil, nil for ops that aren't
// yours or that you can't measure; errors just leave the estimate out.
type LossEstimator interface {
	EstimateLoss(ctx context.Context, p *Platform, project string, op change.Op) (*change.Loss, error)
}

// EstimateLoss asks every LossEstimator about op; the first answer wins.
// It has the shape of change.EstimateFunc, for Engine.Estimate.
func (p *Platform) EstimateLoss(ctx context.Context, project string, op change.Op) (*change.Loss, error) {
	for _, m := range Modules() {
		if le, ok := m.(LossEstimator); ok {
			l, err := le.EstimateLoss(ctx, p, project, op)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", m.Name(), err)
			}
			if l != nil {
				return l, nil
			}
		}
	}
	return nil, nil
}

// StatusReporter adds to a project's resource statuses what only a module
// knows at read time (whether an app has a live release, say). It runs on
// every read of a project's status, so it must be fast and read-only; it
// changes the statuses read, never the stored ones.
type StatusReporter interface {
	ReportStatus(ctx context.Context, p *Platform, project string, st map[string]state.ResourceStatus)
}

// ResourceStatuses is each of a project's resources' live state, as the
// project's status API reports it: the stored state, with what every
// StatusReporter adds.
func (p *Platform) ResourceStatuses(ctx context.Context, project string) (map[string]state.ResourceStatus, error) {
	st, err := p.DB.ResourceStatuses(ctx, project)
	if err != nil {
		return nil, err
	}
	for _, m := range Modules() {
		if r, ok := m.(StatusReporter); ok {
			r.ReportStatus(ctx, p, project, st)
		}
	}
	return st, nil
}

// PlanChecker can refuse a manifest before it is planned: the desired state
// cannot work on this box (a budget bigger than the machine, say). It runs
// on every plan and apply of a manifest, so it must be fast and read-only.
// Return nil to allow, or an error that says why (an *api.Problem with
// status 422 and a hint reads best).
type PlanChecker interface {
	CheckPlan(ctx context.Context, p *Platform, project string, desired map[string]change.Resource) error
}

// CheckPlan asks every PlanChecker about a desired state; the first refusal wins.
func (p *Platform) CheckPlan(ctx context.Context, project string, desired map[string]change.Resource) error {
	for _, m := range Modules() {
		if pc, ok := m.(PlanChecker); ok {
			if err := pc.CheckPlan(ctx, p, project, desired); err != nil {
				return err
			}
		}
	}
	return nil
}

// PlanWarner adds to a plan's warnings what only a module can tell from the
// box (apps that would rebuild, a connection budget the apps would
// overrun). It runs on every plan, so it must be fast and read-only.
type PlanWarner interface {
	PlanWarnings(ctx context.Context, p *Platform, project string, plan *change.Plan, desired map[string]change.Resource) []string
}

// PlanWarnings asks every PlanWarner about a plan.
func (p *Platform) PlanWarnings(ctx context.Context, plan *change.Plan, desired map[string]change.Resource) []string {
	var out []string
	for _, m := range Modules() {
		if pw, ok := m.(PlanWarner); ok {
			out = append(out, pw.PlanWarnings(ctx, p, plan.Project, plan, desired)...)
		}
	}
	return out
}

// ServiceUsage is what one project holds in one service.
type ServiceUsage struct {
	Service string // "postgres", "valkey", "storage"
	// Disk says which disk total Bytes counts toward: "database", "kv" or "files".
	Disk  string
	Bytes int64
	// Counts the service reports, by name (e.g. "connections", "keys",
	// "objects", "buckets").
	Counts map[string]int64
}

// UsageReporter measures what a project holds in a module's service. It
// runs in the background for the usage API (results are cached), so it may
// take a second, but it must be read-only. Return nil, nil when the project
// does not use the service.
type UsageReporter interface {
	ProjectUsage(ctx context.Context, p *Platform, project string) (*ServiceUsage, error)
}

// Checker contributes health checks.
type Checker interface {
	Checks(ctx context.Context, p *Platform) []Check
}

var (
	regMu   sync.Mutex
	modules []Module
)

// Register adds a module. Call it from init().
func Register(m Module) {
	regMu.Lock()
	defer regMu.Unlock()
	for _, x := range modules {
		if x.Name() == m.Name() {
			panic("platform: duplicate module " + m.Name())
		}
	}
	modules = append(modules, m)
}

// Orderer sets a module's place in provisioning, startup and reconcile
// order (lower first; default 50). Data services come before apps.
type Orderer interface{ Order() int }

func orderOf(m Module) int {
	if o, ok := m.(Orderer); ok {
		return o.Order()
	}
	return 50
}

// Modules returns registered modules by Order, then name.
func Modules() []Module {
	regMu.Lock()
	defer regMu.Unlock()
	out := append([]Module(nil), modules...)
	sort.SliceStable(out, func(i, j int) bool {
		if oi, oj := orderOf(out[i]), orderOf(out[j]); oi != oj {
			return oi < oj
		}
		return out[i].Name() < out[j].Name()
	})
	return out
}

// EdgeController drives the box's edge: Caddy and the switchboard, which
// run in their own process (tiffin edge) so that restarting this one never
// interrupts the apps it serves. Every call returns once the edge serves
// the change, or fails when the edge cannot be reached.
type EdgeController interface {
	// SetRoutes loads new routes (with the switchboard table as it is now).
	SetRoutes(routes []edge.Route) error
	// Switchboard is the address routes to app instances point at.
	Switchboard() string
	// TableSource registers where the switchboard table comes from (the
	// runtime); SyncTable sends it as it is now.
	TableSource(func() switchboard.Table)
	SyncTable() error
	// Busy counts the requests in flight on the named app instances.
	Busy(names []string) (int64, error)
	// Activity is when each app environment last had a request.
	Activity() (map[string]time.Time, error)
}

// Platform is what modules get to work with on the box.
type Platform struct {
	DB        *state.DB
	Engine    *change.Engine
	Tokens    *tokens.Manager
	Secrets   *Secrets
	Home      string // platform data dir, e.g. /var/lib/tiffin/platform
	DataRoot  string // the data disk, e.g. /var/lib/tiffin
	Domain    string // e.g. "tiffin.localhost"
	PublicURL string // e.g. "https://dashboard.tiffin.localhost:8443"
	Version   string
	Edge      EdgeController // nil off-box
	Log       *slog.Logger
	// Reach is how the world reaches the box (public IPs, ACME, DNS).
	Reach Reach
	// DNS manages records through a connected DNS provider; nil when the
	// box has none (set by the domains module when it starts).
	DNS DNSManager
	// Restart asks the service to restart (systemd starts it again within
	// seconds), for settings read only at start such as the box domain.
	// Nil off-box.
	Restart func(reason string)

	rec      *reconciler
	started  atomic.Bool
	starting atomic.Bool
}

// Started reports whether Start finished: every module started. Health
// answers "starting" until then, so an update is not judged on a build
// whose modules may still fail to start.
func (p *Platform) Started() bool { return p.started.Load() }

// Host returns the public hostname for a first-level name under the apps
// domain: "shop" → "shop.tiffin.localhost". The dashboard is DashboardHost.
func (p *Platform) Host(name string) string { return name + "." + p.AppsDomain() }

// URL returns the public https URL for a host, with the public port when not 443.
func (p *Platform) URL(host string) string {
	port := ""
	if i := strings.LastIndex(p.PublicURL, ":"); i > len("https:") {
		port = p.PublicURL[i:]
	}
	return "https://" + host + port
}

// RefreshRoutes collects every RouteProvider's routes and loads them into
// the edge. While Start runs it does nothing: modules not started yet would
// give no routes, and the edge keeps serving the ones it has (a restart of
// this process changes nothing it serves). Start's caller refreshes once
// every module started.
func (p *Platform) RefreshRoutes(ctx context.Context) error {
	if p.Edge == nil || p.starting.Load() {
		return nil
	}
	var all []edge.Route
	for _, m := range Modules() {
		if rp, ok := m.(RouteProvider); ok {
			rs, err := rp.Routes(ctx, p)
			if err != nil {
				return fmt.Errorf("%s routes: %w", m.Name(), err)
			}
			all = append(all, rs...)
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Host < all[j].Host })
	return p.Edge.SetRoutes(all)
}

// Checks runs every Checker, then checks no project resource failed.
func (p *Platform) Checks(ctx context.Context) []Check {
	out := provisionChecks(ctx)
	for _, m := range Modules() {
		if c, ok := m.(Checker); ok {
			out = append(out, c.Checks(ctx, p)...)
		}
	}
	if p.DB != nil {
		out = append(out, p.resourceCheck(ctx))
	}
	return out
}

// resourceCheck reports the project resources whose last reconcile failed.
func (p *Platform) resourceCheck(ctx context.Context) Check {
	projects, err := p.DB.ListProjects(ctx)
	if err != nil {
		return Check{Name: "resources", OK: false, Detail: err.Error()}
	}
	var failed []string
	first := ""
	for _, pr := range projects {
		st, err := p.DB.ResourceStatuses(ctx, pr)
		if err != nil {
			return Check{Name: "resources", OK: false, Detail: err.Error()}
		}
		addrs := make([]string, 0, len(st))
		for a := range st {
			addrs = append(addrs, a)
		}
		sort.Strings(addrs)
		for _, a := range addrs {
			if st[a].State != StateFailed {
				continue
			}
			msg, _, _ := strings.Cut(st[a].Message, "\n")
			if len(msg) > 120 {
				msg = msg[:120] + "…"
			}
			failed = append(failed, pr+" "+a+" ("+msg+")")
			if first == "" {
				first = pr
			}
		}
	}
	if len(failed) == 0 {
		return Check{Name: "resources", OK: true, Detail: fmt.Sprintf("no failed resources in %d project(s)", len(projects))}
	}
	return Check{Name: "resources", OK: false, Detail: fmt.Sprintf("%d failed: %s. tiffin projects get %s says why; fix it and apply again.",
		len(failed), strings.Join(failed, "; "), first)}
}

// Start runs every Starter and the reconcile worker, then reconciles every
// resource once so a rebooted or reinstalled box converges.
func (p *Platform) Start(ctx context.Context) error {
	if p.Log == nil {
		p.Log = slog.Default()
	}
	p.starting.Store(true)
	defer p.starting.Store(false)
	p.rec = newReconciler(p)
	go p.rec.run(ctx)
	for _, m := range Modules() {
		if s, ok := m.(Starter); ok {
			if err := s.Start(ctx, p); err != nil {
				return fmt.Errorf("start %s: %w", m.Name(), err)
			}
		}
	}
	// Every project, and every deleted one whose cleanup a restart (or a
	// failure) left unfinished: its resources still have statuses.
	projects, err := p.DB.ListConvergingProjects(ctx)
	if err != nil {
		return err
	}
	for _, pr := range projects {
		p.ReconcileProject(pr)
	}
	p.started.Store(true)
	return nil
}

// ProjectEnv assembles the environment for one app of a project: module
// env (DATABASE_URL, ...), then project env, then app env, then secrets.
// Later sources win.
func (p *Platform) ProjectEnv(ctx context.Context, project, app string) (map[string]string, error) {
	env := map[string]string{"TIFFIN_PROJECT": project, "TIFFIN_APP": app, "TIFFIN_DOMAIN": p.AppsDomain()}
	for _, m := range Modules() {
		if ep, ok := m.(EnvProvider); ok {
			kv, err := ep.Env(ctx, p, project, app)
			if err != nil {
				return nil, fmt.Errorf("%s env: %w", m.Name(), err)
			}
			for k, v := range kv {
				env[k] = v
			}
		}
	}
	_, res, err := p.DB.Load(ctx, project)
	if err != nil {
		return nil, err
	}
	for addr, r := range res {
		if change.Kind(addr) == change.KindEnv {
			var v string
			if json.Unmarshal(r.Spec, &v) == nil {
				env[change.Name(addr)] = v
			}
		}
	}
	if r, ok := res[change.KindApp+"/"+app]; ok {
		var spec struct {
			Env map[string]string `json:"env"`
		}
		_ = json.Unmarshal(r.Spec, &spec)
		for k, v := range spec.Env {
			env[k] = v
		}
	}
	if p.Secrets != nil {
		sec, err := p.Secrets.All(ctx, project)
		if err != nil {
			return nil, err
		}
		for k, v := range sec {
			env[k] = v
		}
	}
	return env, nil
}
