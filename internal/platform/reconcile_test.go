package platform

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/state"
)

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
