package state

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/change/changetest"
	"github.com/btahir/tiffin/internal/manifest"
)

func openTemp(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestConformance(t *testing.T) {
	changetest.Run(t, func(t *testing.T) change.Store { return openTemp(t) })
}

func TestConformanceInMemory(t *testing.T) {
	changetest.Run(t, func(t *testing.T) change.Store {
		db, err := Open(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	})
}

func TestPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c := changetest.Converge(t, change.NewEngine(db), changetest.M("shop", nil))
	db.Close()

	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ver, cur, err := db.Load(context.Background(), "shop")
	if err != nil || ver != 1 || len(cur) != 4 {
		t.Fatalf("after reopen: ver=%d n=%d err=%v", ver, len(cur), err)
	}
	got, err := db.GetChange(context.Background(), c.ID)
	if err != nil || got.Plan.Hash != c.Plan.Hash {
		t.Fatalf("change after reopen: %v", err)
	}
}

// Concurrent appliers racing on the same base version: exactly one wins,
// the rest get ErrConflict, and the version advances by exactly one.
func TestConcurrentApplyOneWinner(t *testing.T) {
	db := openTemp(t)
	e := change.NewEngine(db)
	changetest.Converge(t, e, changetest.M("shop", nil))
	ctx := context.Background()
	const n = 8
	plans := make([]*change.Plan, n)
	for i := range n {
		m := changetest.M("shop", func(m *manifest.Manifest) { m.Env["WORKER"] = string(rune('a' + i)) })
		r, _ := change.Resources(m)
		p, err := e.Plan(ctx, "shop", r)
		if err != nil {
			t.Fatal(err)
		}
		plans[i] = p
	}
	var wg sync.WaitGroup
	results := make(chan error, n)
	for _, p := range plans {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.Apply(ctx, change.ApplyRequest{Plan: p, Confirm: p.Hash, Actor: change.Actor{Kind: "agent", ID: "t"}})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		switch err {
		case nil:
			wins++
		case change.ErrConflict:
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	ver, _, _ := db.Load(ctx, "shop")
	if wins != 1 || ver != 2 {
		t.Fatalf("wins=%d version=%d, want 1 and 2", wins, ver)
	}
}

func TestAudit(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	for _, a := range []string{"token.create", "token.revoke"} {
		if err := db.Audit(ctx, "tok_owner", a, "tok_x", map[string]string{"name": "ci"}); err != nil {
			t.Fatal(err)
		}
	}
	log, err := db.AuditLog(ctx, 10)
	if err != nil || len(log) != 2 || log[0].Action != "token.revoke" || log[0].At.IsZero() {
		t.Fatalf("audit log: %v %+v", err, log)
	}
}
