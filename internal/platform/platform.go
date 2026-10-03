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
//
// Modules never edit each other's files; they meet here.
package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/edge"
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

// EdgeController updates the box's edge routes.
type EdgeController interface {
	SetRoutes(routes []edge.Route) error
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

	rec *reconciler
}

// Host returns the public hostname for a first-level name: "shop" → "shop.tiffin.localhost".
func (p *Platform) Host(name string) string { return name + "." + p.Domain }

// URL returns the public https URL for a host, with the public port when not 443.
func (p *Platform) URL(host string) string {
	port := ""
	if i := strings.LastIndex(p.PublicURL, ":"); i > len("https:") {
		port = p.PublicURL[i:]
	}
	return "https://" + host + port
}

// RefreshRoutes collects every RouteProvider's routes and loads them into the edge.
func (p *Platform) RefreshRoutes(ctx context.Context) error {
	if p.Edge == nil {
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

// Checks runs every Checker.
func (p *Platform) Checks(ctx context.Context) []Check {
	var out []Check
	for _, m := range Modules() {
		if c, ok := m.(Checker); ok {
			out = append(out, c.Checks(ctx, p)...)
		}
	}
	return out
}

// Start runs every Starter and the reconcile worker, then reconciles every
// resource once so a rebooted or reinstalled box converges.
func (p *Platform) Start(ctx context.Context) error {
	if p.Log == nil {
		p.Log = slog.Default()
	}
	p.rec = newReconciler(p)
	go p.rec.run(ctx)
	for _, m := range Modules() {
		if s, ok := m.(Starter); ok {
			if err := s.Start(ctx, p); err != nil {
				return fmt.Errorf("start %s: %w", m.Name(), err)
			}
		}
	}
	projects, err := p.DB.ListProjects(ctx)
	if err != nil {
		return err
	}
	for _, pr := range projects {
		p.ReconcileProject(pr)
	}
	return nil
}

// ProjectEnv assembles the environment for one app of a project: module
// env (DATABASE_URL, ...), then project env, then app env, then secrets.
// Later sources win.
func (p *Platform) ProjectEnv(ctx context.Context, project, app string) (map[string]string, error) {
	env := map[string]string{"TIFFIN_PROJECT": project, "TIFFIN_APP": app, "TIFFIN_DOMAIN": p.Domain}
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
