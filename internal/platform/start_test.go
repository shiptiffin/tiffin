package platform

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/state"
)

// spy is a service module that records what it reconciles.
type spy struct {
	name  string
	order int
	log   *spyLog
}

type spyLog struct {
	mu    sync.Mutex
	calls []string
}

func (s *spy) Name() string    { return "spy-" + s.name }
func (s *spy) Order() int      { return s.order }
func (s *spy) Kinds() []string { return []string{change.KindService + "/" + s.name} }
func (s *spy) Reconcile(_ context.Context, _ *Platform, project, address string, spec json.RawMessage) error {
	s.log.mu.Lock()
	defer s.log.mu.Unlock()
	verb := "ensure"
	if spec == nil {
		verb = "delete"
	}
	s.log.calls = append(s.log.calls, verb+" "+project+" "+address)
	return nil
}

func (l *spyLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

var spies = &spyLog{}

func init() {
	// The real modules' order: postgres 10, auth 30.
	Register(&spy{name: "postgres", order: 10, log: spies})
	Register(&spy{name: "auth", order: 30, log: spies})
}

// A project deleted just before a restart still has live resources; the
// next start finishes the deletion, Postgres first (its snapshot must still
// hold auth's data), and health reports ready only after Start.
func TestStartFinishesAnInterruptedDeletion(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := &Platform{DB: db, Engine: change.NewEngine(db), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	create := &change.Change{ID: "chg_1", Project: "shop", Version: 1, At: time.Now(), Plan: change.Plan{Project: "shop", Ops: []change.Op{
		{Action: change.Create, Address: "service/postgres", After: json.RawMessage(`{}`)},
		{Action: change.Create, Address: "service/auth", After: json.RawMessage(`{}`)}}}}
	if err := db.Commit(ctx, create); err != nil {
		t.Fatal(err)
	}
	newReconciler(p).converge(ctx, "shop")
	del := &change.Change{ID: "chg_2", Project: "shop", Version: 2, At: time.Now(), Plan: change.Plan{Project: "shop", BaseVersion: 1, Ops: []change.Op{
		{Action: change.Delete, Address: "service/postgres", Before: json.RawMessage(`{}`)},
		{Action: change.Delete, Address: "service/auth", Before: json.RawMessage(`{}`)}}}}
	if err := db.Commit(ctx, del); err != nil {
		t.Fatal(err)
	}
	// ...and the box restarts before the reconciler runs.
	spies.mu.Lock()
	spies.calls = nil
	spies.mu.Unlock()
	if p.Started() {
		t.Fatal("started before Start")
	}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := p.Start(sctx); err != nil {
		t.Fatal(err)
	}
	if !p.Started() {
		t.Fatal("not started after Start")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, _ := db.ResourceStatuses(ctx, "shop")
		if len(st) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("deletion not finished: %v, calls %v", st, spies.snapshot())
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := spies.snapshot()
	if len(got) != 2 || got[0] != "delete shop service/postgres" || got[1] != "delete shop service/auth" {
		t.Fatalf("calls %v", got)
	}
}
