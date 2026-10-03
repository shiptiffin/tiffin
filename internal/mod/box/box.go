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
	m.mu.Lock()
	m.s = newSampler("", data)
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
			"app container: state, memory, CPU seconds and CPU percent (100 = one core) over the last window. Box only: a laptop "+
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
}

// cacheFor is how long a sample is served before a new one is taken.
const cacheFor = 3 * time.Second
