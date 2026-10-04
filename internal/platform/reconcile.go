package platform

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/change"
)

// Resource states shown to people and agents.
const (
	StatePending = "pending" // applied, not yet converged
	StateReady   = "ready"
	StateFailed  = "failed"
)

// ResourceStatus is the live state of one resource on the machine.
type ResourceStatus struct {
	Address   string    `json:"address"`
	State     string    `json:"state" enum:"pending,ready,failed"`
	Message   string    `json:"message,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

const postgresService = change.KindService + "/postgres"

type reconciler struct {
	p     *Platform
	mu    sync.Mutex
	queue map[string]bool // projects waiting
	wake  chan struct{}
}

func newReconciler(p *Platform) *reconciler {
	return &reconciler{p: p, queue: map[string]bool{}, wake: make(chan struct{}, 1)}
}

// ReconcileProject schedules a full converge of a project's resources,
// including deleted ones the machine still has.
func (p *Platform) ReconcileProject(project string) {
	if p.rec == nil {
		return
	}
	p.rec.mu.Lock()
	p.rec.queue[project] = true
	p.rec.mu.Unlock()
	select {
	case p.rec.wake <- struct{}{}:
	default:
	}
}

// AfterApply is called by the API after a change commits.
func (p *Platform) AfterApply(c *change.Change) {
	if c != nil {
		p.ReconcileProject(c.Project)
	}
}

func (r *reconciler) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		}
		for {
			r.mu.Lock()
			var project string
			for pr := range r.queue {
				project = pr
				break
			}
			if project != "" {
				delete(r.queue, project)
			}
			r.mu.Unlock()
			if project == "" {
				break
			}
			r.converge(ctx, project)
		}
	}
}

// converge reconciles every resource of project in dependency order, then
// handles resources the machine still has that the project no longer wants.
func (r *reconciler) converge(ctx context.Context, project string) {
	p := r.p
	_, res, err := p.DB.Load(ctx, project)
	if err != nil {
		p.Log.Error("reconcile load", "project", project, "err", err)
		return
	}
	known, _ := p.DB.ResourceStatuses(ctx, project)
	// Desired resources, services first, apps last (same order as plans).
	var ops []change.Op
	for addr, rs := range res {
		ops = append(ops, change.Op{Action: change.Create, Address: addr, After: rs.Spec})
	}
	for addr := range known {
		if _, ok := res[addr]; !ok {
			ops = append(ops, change.Op{Action: change.Delete, Address: addr})
		}
	}
	change.SortOps(ops)
	// Within a kind, follow module order: postgres (10) before auth (30),
	// so a service is converged after the services it needs; deletes reverse,
	// except Postgres's, which goes first: its snapshot (what undoes the
	// delete) must still hold what other services keep in the project's
	// database, such as auth's users, and dropping the database drops those.
	sort.SliceStable(ops, func(i, j int) bool {
		a, b := ops[i], ops[j]
		if a.Action != b.Action || change.Kind(a.Address) != change.Kind(b.Address) {
			return false
		}
		oa, ob := moduleOrder(a.Address), moduleOrder(b.Address)
		if a.Action == change.Delete {
			if pa, pb := a.Address == postgresService, b.Address == postgresService; pa != pb {
				return pa
			}
			return oa > ob
		}
		return oa < ob
	})
	for _, o := range ops {
		if change.Unmanaged(o.Address) {
			// Secrets have nothing to converge (apps read them when they
			// (re)start), so they have no live status. Drop any row one has
			// (an import marks every resource pending): a pending row that
			// nothing ever settles would keep the project from converging.
			if _, ok := known[o.Address]; ok {
				_ = p.DB.DeleteResourceStatus(ctx, project, o.Address)
			}
			continue
		}
		rc := reconcilerFor(o.Address)
		if rc == nil {
			if o.Action == change.Delete {
				_ = p.DB.DeleteResourceStatus(ctx, project, o.Address)
			} else {
				_ = p.DB.SetResourceStatus(ctx, project, o.Address, StateReady, "")
			}
			continue
		}
		var spec json.RawMessage
		if o.Action != change.Delete {
			spec = o.After
			_ = p.DB.SetResourceStatus(ctx, project, o.Address, StatePending, "")
		}
		err := safeReconcile(ctx, rc, p, project, o.Address, spec)
		switch {
		case err != nil:
			p.Log.Error("reconcile", "project", project, "address", o.Address, "err", err)
			_ = p.DB.SetResourceStatus(ctx, project, o.Address, StateFailed, err.Error())
		case o.Action == change.Delete:
			_ = p.DB.DeleteResourceStatus(ctx, project, o.Address)
		default:
			_ = p.DB.SetResourceStatus(ctx, project, o.Address, StateReady, "")
		}
	}
	if len(res) == 0 {
		// The project is gone: let modules drop what they keep about it.
		for _, m := range Modules() {
			if c, ok := m.(ProjectCleaner); ok {
				if err := c.ProjectDeleted(ctx, p, project); err != nil {
					p.Log.Error("project cleanup", "module", m.Name(), "project", project, "err", err)
				}
			}
		}
	}
	if err := p.RefreshRoutes(ctx); err != nil {
		p.Log.Error("refresh routes", "err", err)
	}
}

func safeReconcile(ctx context.Context, rc Reconciler, p *Platform, project, addr string, spec json.RawMessage) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &panicError{r}
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	return rc.Reconcile(ctx, p, project, addr, spec)
}

type panicError struct{ v any }

func (e *panicError) Error() string { return "reconciler panicked: " + strings.TrimSpace(fmtAny(e.v)) }

func fmtAny(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if e, ok := v.(error); ok {
		return e.Error()
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// reconcilerFor finds the module handling an address: an exact address
// match wins over a kind match.
func reconcilerFor(address string) Reconciler {
	var byKind Reconciler
	for _, m := range Modules() {
		rc, ok := m.(Reconciler)
		if !ok {
			continue
		}
		for _, k := range rc.Kinds() {
			if k == address {
				return rc
			}
			if !strings.Contains(k, "/") && k == change.Kind(address) && byKind == nil {
				byKind = rc
			}
		}
	}
	return byKind
}

func moduleOrder(address string) int {
	if rc := reconcilerFor(address); rc != nil {
		if m, ok := rc.(Module); ok {
			return orderOf(m)
		}
	}
	return 50
}
