// Package box reports what the machine has and what each part of Tiffin
// uses: CPU, memory, disks and uptime, every systemd service the box runs
// and every app container. It reads /proc, statfs, cgroup v2 files and
// systemd directly (no agent), and caches a sample for a few seconds so the
// dashboard can poll it cheaply.
package box

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
)

func init() { platform.Register(&Module{}) }

// Module is the box-resources module.
type Module struct {
	mu sync.Mutex
	s  *sampler // set by Start: only a box measures itself
}

func (*Module) Name() string { return "box" }
func (*Module) Order() int   { return 5 }

// DataMount is the data disk.
const DataMount = "/var/lib/tiffin"

// Start enables sampling (the box serves).
func (m *Module) Start(ctx context.Context, p *platform.Platform) error {
	data := DataMount
	if p != nil && p.DataRoot != "" {
		data = p.DataRoot
	}
	s := newSampler("", data)
	s.track = newTracker("", p)
	go s.track.loop(ctx)
	m.mu.Lock()
	m.s = s
	m.mu.Unlock()
	return nil
}

func (m *Module) sampler() *sampler {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.s
}

// RegisterAPI adds GET /v1/box/resources.
func (m *Module) RegisterAPI(a huma.API, _ *platform.Platform) {
	op := api.Op("box-resources", http.MethodGet, "/v1/box/resources", "box resources", api.RiskRead, "Show the box's resources",
		"What the machine has and what uses it, sampled now (cached for 3 seconds): CPU count, load and use; memory total, used "+
			"and available; data disk and system disk size and use; uptime; and for every service Tiffin runs (postgres, valkey, "+
			"storage, auth, the tiffin service itself, victoria-metrics, victoria-logs, containerd, buildkit, crowdsec...) and every "+
			"app container: state, memory, CPU seconds and CPU percent (100 = one core) over the last window; and every project's totals "+
			"(all its app copies' memory and CPU, its data on disk, its limits and memory pressure). Box only: a laptop "+
			"running tiffin serve without --box answers 503.", "system")
	op.Errors = append(op.Errors, 503)
	huma.Register(a, op, api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body *Resources }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, ""); err != nil {
			return nil, err
		}
		s := m.sampler()
		if s == nil {
			p := api.NewProblem(503, "internal", "box resources are measured on a box; this server runs without --box")
			p.Hint = "Point the CLI or dashboard at a box (tiffin up), where tiffin serve runs with --box."
			return nil, p
		}
		return &struct{ Body *Resources }{s.get(ctx)}, nil
	}))

	uo := api.Op("project-usage", http.MethodGet, "/v1/projects/{project}/usage", "projects usage", api.RiskRead,
		"Show what a project uses of the box",
		"One project's share of the box, live (cached for 2 seconds): its budget from tiffin.config.ts (auto: true when it sets none); "+
			"memory used by all its app copies together (production and previews) against its limit, the part the kernel protects for it, "+
			"its headroom (how much more it could take right now) and pressure (\"oom\" when an app copy was killed for memory in the last "+
			"hour: the project is using all the memory it was given); CPU use against its cap; data on disk (database, files, KV; measured "+
			"in the background every 30 seconds); each app's copies, memory and CPU; and service numbers (Postgres connections, KV keys, "+
			"bucket objects). limitSource says where the limits come from: \"project\" (its resources), \"box default\" (the box-wide "+
			"default share, see box settings) or \"automatic\" (elastic: it grows into whatever the box has free). To change the "+
			"limits, set resources in tiffin.config.ts and plan/apply.", "system")
	uo.Errors = append(uo.Errors, 404, 503)
	huma.Register(a, uo, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
	}) (*struct{ Body *Usage }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
			return nil, err
		}
		s := m.sampler()
		if s == nil {
			p := api.NewProblem(503, "internal", "project usage is measured on a box; this server runs without --box")
			p.Hint = "Point the CLI or dashboard at a box (tiffin up), where tiffin serve runs with --box."
			return nil, p
		}
		apps, ok, err := appNames(ctx, s.track.p, in.Project)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, api.NewProblem(404, "not_found", "project "+in.Project+" does not exist")
		}
		return &struct{ Body *Usage }{s.usage(ctx, s.track, in.Project, apps, memAvailable(s.root))}, nil
	}))
}

// cacheFor is how long a sample is served before a new one is taken.
const cacheFor = 3 * time.Second
