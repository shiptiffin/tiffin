package platform

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/state"
)

// A failed resource makes the box status not ok, with what to run.
func TestResourceCheckReportsFailures(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := &Platform{DB: db}
	c := &change.Change{ID: "chg_1", Project: "shop", Version: 1, At: time.Now(),
		Plan: change.Plan{Project: "shop", Ops: []change.Op{{Action: change.Create, Address: "env/shared", After: json.RawMessage(`{"vars":{"A":"1"}}`)}}}}
	if err := db.Commit(ctx, c); err != nil {
		t.Fatal(err)
	}
	_ = db.SetResourceStatus(ctx, "shop", "env/shared", StateReady, "")
	if c := p.resourceCheck(ctx); !c.OK {
		t.Fatalf("ready: %+v", c)
	}
	_ = db.SetResourceStatus(ctx, "shop", "app/web", StateFailed, "instance tf.shop.web.prod.3 exited with code 1\nlast lines")
	got := p.resourceCheck(ctx)
	if got.OK || !strings.Contains(got.Detail, "shop app/web (instance tf.shop.web.prod.3 exited with code 1)") || !strings.Contains(got.Detail, "tiffin projects get shop") {
		t.Fatalf("failed: %+v", got)
	}
}

// A destroyed project whose deletion failed has no resources left, only
// the failed status: the check still reports it.
func TestResourceCheckReportsFailedDeletions(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := &Platform{DB: db}
	_ = db.SetResourceStatus(ctx, "gone", "service/postgres", StateFailed, "snapshot the database: disk full")
	got := p.resourceCheck(ctx)
	if got.OK || !strings.Contains(got.Detail, "gone service/postgres (snapshot the database: disk full)") {
		t.Fatalf("failed deletion not reported: %+v", got)
	}
}

// A secret has nothing to converge, so it must not keep a status: an import
// marks every resource pending, and a pending secret nothing ever settled
// kept the import waiting 15 minutes for a project that was long converged.
func TestConvergeSettlesSecretStatuses(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := state.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sec, err := OpenSecrets(db, dir)
	if err != nil {
		t.Fatal(err)
	}
	p := &Platform{DB: db, Engine: change.NewEngine(db), Secrets: sec, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	c := &change.Change{ID: "chg_1", Project: "shop", Version: 1, At: time.Now(),
		Plan: change.Plan{Project: "shop", Ops: []change.Op{{Action: change.Create, Address: "env/shared", After: json.RawMessage(`{"vars":{"A":"1"}}`)}}}}
	if err := db.Commit(ctx, c); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"one", "two"} {
		if _, err := p.SetSecrets(ctx, "shop", map[string]string{"GREETING": v, "OTHER": "x"}, "test"); err != nil {
			t.Fatal(err)
		}
	}
	// What an import leaves: every resource pending, plus a stale row.
	for _, addr := range []string{"env/shared", "secret/GREETING", "secret/OTHER", "secret/GONE"} {
		_ = db.SetResourceStatus(ctx, "shop", addr, StatePending, "imported; converging")
	}
	newReconciler(p).converge(ctx, "shop")
	st, _ := db.ResourceStatuses(ctx, "shop")
	if len(st) != 1 || st["env/shared"].State != StateReady {
		t.Fatalf("after converge: %+v", st)
	}

	// Deleting a secret that still had a row drops the row too.
	plan, err := p.PlanSecrets(ctx, "shop", nil, []string{"OTHER"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Engine.Apply(ctx, change.ApplyRequest{Plan: plan, Confirm: plan.Hash, Actor: change.Actor{Kind: "system", ID: "system"}}); err != nil {
		t.Fatal(err)
	}
	_ = db.SetResourceStatus(ctx, "shop", "secret/OTHER", StatePending, "")
	newReconciler(p).converge(ctx, "shop")
	if st, _ = db.ResourceStatuses(ctx, "shop"); len(st) != 1 {
		t.Fatalf("after deleting a secret: %+v", st)
	}
}

// holdSpy applies "readonly" holds the way the box does: while one holds,
// the project's database refuses writes.
type holdSpy struct{}

var held = struct {
	sync.Mutex
	m map[string]bool
}{m: map[string]bool{}}

func (holdSpy) Name() string    { return "spy-readonly" }
func (holdSpy) Order() int      { return 5 }
func (holdSpy) Kinds() []string { return []string{change.KindReadOnly} }
func (holdSpy) Reconcile(_ context.Context, _ *Platform, project, _ string, spec json.RawMessage) error {
	held.Lock()
	defer held.Unlock()
	held.m[project] = spec != nil
	return nil
}

func init() { Register(holdSpy{}) }

// Lifting a read-only hold must come before the services that write to the
// database converge, and a resource that failed is tried again on its own
// (backing off) until it converges.
func TestConvergeLiftsHoldFirstAndRetriesFailures(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := &Platform{DB: db, Engine: change.NewEngine(db), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	spies.mu.Lock()
	spies.calls = nil
	spies.fail = func(project, address string) error {
		held.Lock()
		defer held.Unlock()
		if held.m[project] {
			return errors.New("cannot execute CREATE SCHEMA in a read-only transaction")
		}
		return nil
	}
	spies.mu.Unlock()
	defer func() { spies.mu.Lock(); spies.fail = nil; spies.mu.Unlock() }()

	apply := func(ops ...change.Op) {
		t.Helper()
		plan, err := p.Engine.PlanEdit(ctx, "held", func(cur map[string]change.Resource) (map[string]change.Resource, error) {
			return change.ApplyOps(cur, ops), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Engine.Apply(ctx, change.ApplyRequest{Plan: plan, Confirm: plan.Hash, Actor: change.Actor{Kind: "system", ID: "system"}}); err != nil {
			t.Fatal(err)
		}
	}
	r := newReconciler(p)
	now := time.Now()
	r.now = func() time.Time { return now }
	apply(change.Op{Action: change.Create, Address: "service/postgres", After: json.RawMessage(`{}`)},
		change.Op{Action: change.Create, Address: "service/auth", After: json.RawMessage(`{}`)},
		change.Op{Action: change.Create, Address: change.KindReadOnly, After: json.RawMessage(`{"reason":"limit"}`)})
	r.converge(ctx, "held")
	if st, _ := db.ResourceStatuses(ctx, "held"); st["service/postgres"].State != StateReady || st[change.KindReadOnly].State != StateReady {
		t.Fatalf("holding: %+v", st)
	}
	// Converging while held (another change, a restart) fails the services...
	r.converge(ctx, "held")
	if st, _ := db.ResourceStatuses(ctx, "held"); st["service/postgres"].State != StateFailed || st["service/auth"].State != StateFailed {
		t.Fatalf("converged while held: %+v", st)
	}
	if len(p.Failed(ctx, "held")) != 2 {
		t.Fatalf("Failed = %v", p.Failed(ctx, "held"))
	}
	// ...and is tried again after a backoff, not before.
	r.queueDue()
	if r.queue["held"] {
		t.Fatal("retried at once")
	}
	now = now.Add(retryBase)
	r.queueDue()
	if !r.queue["held"] {
		t.Fatal("not retried after the backoff")
	}
	delete(r.queue, "held")
	r.converge(ctx, "held") // still held: fails again, waits twice as long
	now = now.Add(retryBase)
	if r.queueDue(); r.queue["held"] {
		t.Fatal("backoff did not grow")
	}

	// Lifting the hold converges the services in the same pass.
	apply(change.Op{Action: change.Delete, Address: change.KindReadOnly})
	r.converge(ctx, "held")
	st, _ := db.ResourceStatuses(ctx, "held")
	if st["service/postgres"].State != StateReady || st["service/auth"].State != StateReady {
		t.Fatalf("after lifting: %+v", st)
	}
	if _, ok := st[change.KindReadOnly]; ok {
		t.Fatalf("hold status left: %+v", st)
	}
	if _, ok := r.retry["held"]; ok {
		t.Fatal("a converged project keeps retrying")
	}
}
