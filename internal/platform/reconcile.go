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
	queue map[string]bool  // projects waiting
	retry map[string]retry // projects a pass left with failed resources
	wake  chan struct{}
	now   func() time.Time
}

// retry is when to converge a project again after a pass left resources
// failed.
type retry struct {
	failures int
	at       time.Time
}

// A pass that leaves resources failed converges the project again after
// retryBase, doubling up to retryMax while it keeps failing: what failed for
// a moment (a service still starting, a read-only hold being lifted)
// recovers without anyone applying a change.
const (
	retryBase  = 30 * time.Second
	retryMax   = 30 * time.Minute
	retryCheck = 10 * time.Second
)

func newReconciler(p *Platform) *reconciler {
	return &reconciler{p: p, queue: map[string]bool{}, retry: map[string]retry{}, wake: make(chan struct{}, 1), now: time.Now}
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

// Failed returns a project's resources whose last reconcile failed, as
// "address: first line of the reason", sorted.
func (p *Platform) Failed(ctx context.Context, project string) []string {
	st, err := p.DB.ResourceStatuses(ctx, project)
	if err != nil {
		return nil
	}
	var out []string
	for addr, rs := range st {
		if rs.State == StateFailed {
			msg, _, _ := strings.Cut(rs.Message, "\n")
			out = append(out, addr+": "+msg)
		}
	}
	sort.Strings(out)
	return out
}

// AfterApply is called by the API after a change commits.
func (p *Platform) AfterApply(c *change.Change) {
	if c != nil {
		p.ReconcileProject(c.Project)
	}
}

func (r *reconciler) run(ctx context.Context) {
	t := time.NewTicker(retryCheck)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		case <-t.C:
			r.queueDue()
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

// queueDue queues the projects whose retry is due.
func (r *reconciler) queueDue() {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	for pr, rt := range r.retry {
		if !now.Before(rt.at) {
			r.queue[pr] = true
		}
	}
}

// settled records how a pass over project ended: with failed resources it
// schedules the next pass (backing off), without them it forgets the project.
func (r *reconciler) settled(project string, failed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !failed {
		delete(r.retry, project)
		return
	}
	rt := r.retry[project]
	wait := retryMax
	if rt.failures < 16 {
		wait = min(retryBase<<rt.failures, retryMax)
	}
	r.retry[project] = retry{failures: rt.failures + 1, at: r.now().Add(wait)}
}

// converge reconciles every resource of project in dependency order, then
// handles resources the machine still has that the project no longer wants.
func (r *reconciler) converge(ctx context.Context, project string) {
	p := r.p
	_, res, err := p.DB.Load(ctx, project)
	if err != nil {
		p.Log.Error("reconcile load", "project", project, "err", err)
		r.settled(project, true)
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
	// Lifting a read-only hold comes first: while it holds, the project's
	// database refuses the writes other services make as they converge
	// (Postgres's own setup, auth's migrations).
	sort.SliceStable(ops, func(i, j int) bool { return liftsHold(ops[i]) && !liftsHold(ops[j]) })
	failed := false
	for _, o := range ops {
		rc := reconcilerFor(o.Address)
		if rc == nil && change.Unmanaged(o.Address) {
			// Secrets have nothing to converge (apps read them when they
			// (re)start), so they have no live status. Drop any row one has
			// (an import marks every resource pending): a pending row that
			// nothing ever settles would keep the project from converging.
			if _, ok := known[o.Address]; ok {
				_ = p.DB.DeleteResourceStatus(ctx, project, o.Address)
			}
			continue
		}
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
			failed = true
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
	r.settled(project, failed)
}

func liftsHold(o change.Op) bool {
	return o.Action == change.Delete && o.Address == change.KindReadOnly
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
