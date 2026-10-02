// Package changetest is a conformance suite every change.Store must pass.
package changetest

import (
	"context"
	"errors"
	"testing"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
)

// Run runs the suite. newStore must return a fresh, empty store.
func Run(t *testing.T, newStore func(t *testing.T) change.Store) {
	t.Run("PlanApplyIdempotent", func(t *testing.T) { testPlanApply(t, newStore(t)) })
	t.Run("ConfirmRequired", func(t *testing.T) { testConfirm(t, newStore(t)) })
	t.Run("Conflict", func(t *testing.T) { testConflict(t, newStore(t)) })
	t.Run("Undo", func(t *testing.T) { testUndo(t, newStore(t)) })
	t.Run("UndoAfterDrift", func(t *testing.T) { testUndoDrift(t, newStore(t)) })
	t.Run("RiskAndPolicy", func(t *testing.T) { testRisk(t, newStore(t)) })
	t.Run("ListAndProjects", func(t *testing.T) { testList(t, newStore(t)) })
}

var human = change.Actor{Kind: "human", ID: "tok_owner", Name: "owner"}

// M builds a normalized-looking manifest for tests.
func M(project string, edit func(m *manifest.Manifest)) *manifest.Manifest {
	m := &manifest.Manifest{
		Version: 1,
		Project: project,
		Apps: map[string]manifest.App{
			"web": {Path: ".", Framework: manifest.FrameworkNext, Role: manifest.RoleWeb, Routes: []string{"web"}, Instances: 1, MemoryMB: 512, Healthcheck: "/"},
		},
		Services: manifest.Services{Postgres: &manifest.Postgres{Extensions: []string{"vector"}}},
		Env:      map[string]string{"LOG_LEVEL": "info"},
	}
	if edit != nil {
		edit(m)
	}
	return m
}

func desired(t *testing.T, m *manifest.Manifest) map[string]change.Resource {
	t.Helper()
	r, err := change.Resources(m)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Converge plans and applies m with confirmation, failing the test on error.
func Converge(t *testing.T, e *change.Engine, m *manifest.Manifest) *change.Change {
	t.Helper()
	ctx := context.Background()
	p, err := e.Plan(ctx, m.Project, desired(t, m))
	if err != nil {
		t.Fatal(err)
	}
	c, err := e.Apply(ctx, change.ApplyRequest{Plan: p, Confirm: p.Hash, Actor: human, Intent: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func testPlanApply(t *testing.T, s change.Store) {
	ctx := context.Background()
	e := change.NewEngine(s)
	m := M("shop", nil)
	p, err := e.Plan(ctx, "shop", desired(t, m))
	if err != nil {
		t.Fatal(err)
	}
	if p.BaseVersion != 0 || len(p.Ops) != 4 || p.Risk != change.TierReversible {
		t.Fatalf("first plan: base=%d ops=%d risk=%s", p.BaseVersion, len(p.Ops), p.Risk)
	}
	if p.Ops[0].Address != "project" || p.Ops[1].Address != "service/postgres" || p.Ops[3].Address != "app/web" {
		t.Fatalf("create order wrong: %v", addrs(p.Ops))
	}
	c := Converge(t, e, m)
	if c.Version != 1 || len(c.Inverse) != 4 || c.Inverse[0].Action != change.Delete || c.Inverse[0].Address != "app/web" {
		t.Fatalf("change: version=%d inverse=%v", c.Version, addrs(c.Inverse))
	}
	ver, cur, err := s.Load(ctx, "shop")
	if err != nil || ver != 1 || len(cur) != 4 {
		t.Fatalf("load: ver=%d n=%d err=%v", ver, len(cur), err)
	}
	p2, _ := e.Plan(ctx, "shop", desired(t, m))
	if !p2.Empty() || p2.Summary != "no changes" {
		t.Fatalf("replan not empty: %s", p2.Summary)
	}
	if c, err := e.Apply(ctx, change.ApplyRequest{Plan: p2, Actor: human}); c != nil || err != nil {
		t.Fatalf("empty apply: %v %v", c, err)
	}
	// Update one field.
	m2 := M("shop", func(m *manifest.Manifest) {
		a := m.Apps["web"]
		a.Instances = 3
		m.Apps["web"] = a
	})
	p3, _ := e.Plan(ctx, "shop", desired(t, m2))
	if len(p3.Ops) != 1 || p3.Ops[0].Action != change.Update || len(p3.Ops[0].Fields) != 1 || p3.Ops[0].Fields[0] != "instances" {
		t.Fatalf("update plan: %+v", p3.Ops)
	}
}

func testConfirm(t *testing.T, s change.Store) {
	ctx := context.Background()
	e := change.NewEngine(s)
	p, _ := e.Plan(ctx, "shop", desired(t, M("shop", nil)))
	var cr *change.ConfirmRequiredError
	if _, err := e.Apply(ctx, change.ApplyRequest{Plan: p, Actor: human}); !errors.As(err, &cr) || cr.Mismatch || cr.Plan.Hash != p.Hash {
		t.Fatalf("no confirm: %v", err)
	}
	if _, err := e.Apply(ctx, change.ApplyRequest{Plan: p, Confirm: "deadbeefdead", Actor: human}); !errors.As(err, &cr) || !cr.Mismatch {
		t.Fatalf("wrong confirm: %v", err)
	}
	if _, err := e.Apply(ctx, change.ApplyRequest{Plan: p, Confirm: p.Hash[:7], Actor: human}); !errors.As(err, &cr) {
		t.Fatalf("7-char prefix must not be accepted: %v", err)
	}
	if ver, _, _ := s.Load(ctx, "shop"); ver != 0 {
		t.Fatalf("nothing should be written, version=%d", ver)
	}
	c, err := e.Apply(ctx, change.ApplyRequest{Plan: p, Confirm: p.Hash[:12], Actor: human, Intent: "launch shop"})
	if err != nil || c.Intent != "launch shop" || c.Actor != human {
		t.Fatalf("prefix confirm: %v %+v", err, c)
	}
	// Same hash for the same inputs, different once the base moves.
	p2, _ := e.Plan(ctx, "other", desired(t, M("other", nil)))
	p3, _ := e.Plan(ctx, "other", desired(t, M("other", nil)))
	if p2.Hash != p3.Hash || p2.Hash == p.Hash {
		t.Fatalf("hash not deterministic or not project-bound")
	}
}

func testConflict(t *testing.T, s change.Store) {
	ctx := context.Background()
	e := change.NewEngine(s)
	Converge(t, e, M("shop", nil))
	stale, _ := e.Plan(ctx, "shop", desired(t, M("shop", func(m *manifest.Manifest) { m.Env["A"] = "1" })))
	Converge(t, e, M("shop", func(m *manifest.Manifest) { m.Env["B"] = "2" }))
	if _, err := e.Apply(ctx, change.ApplyRequest{Plan: stale, Confirm: stale.Hash, Actor: human}); !errors.Is(err, change.ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	_, cur, _ := s.Load(ctx, "shop")
	if _, ok := cur["env/A"]; ok {
		t.Fatal("stale plan was applied")
	}
}

func testUndo(t *testing.T, s change.Store) {
	ctx := context.Background()
	e := change.NewEngine(s)
	c1 := Converge(t, e, M("shop", nil))
	c2 := Converge(t, e, M("shop", func(m *manifest.Manifest) { m.Env["LOG_LEVEL"] = "debug" }))
	p, err := e.PlanUndo(ctx, c2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.UndoOf != c2.ID || len(p.Ops) != 1 || string(p.Ops[0].After) != `"info"` {
		t.Fatalf("undo plan: %+v", p)
	}
	u, err := e.Apply(ctx, change.ApplyRequest{Plan: p, Confirm: p.Hash, Actor: human})
	if err != nil || u.UndoOf != c2.ID {
		t.Fatalf("apply undo: %v", err)
	}
	got, _ := s.GetChange(ctx, c2.ID)
	if got.UndoneBy != u.ID {
		t.Fatalf("UndoneBy=%q want %q", got.UndoneBy, u.ID)
	}
	if _, err := e.PlanUndo(ctx, c2.ID); err == nil {
		t.Fatal("double undo must fail")
	}
	// Undo the very first change: everything goes, including postgres (irreversible).
	p1, err := e.PlanUndo(ctx, c1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p1.Risk != change.TierIrreversible {
		t.Fatalf("undoing a postgres create must be irreversible, got %s", p1.Risk)
	}
	if _, err := e.Apply(ctx, change.ApplyRequest{Plan: p1, Confirm: p1.Hash, Actor: human}); err != nil {
		t.Fatal(err)
	}
	if _, cur, _ := s.Load(ctx, "shop"); len(cur) != 0 {
		t.Fatalf("state after full undo: %v", cur)
	}
	if _, err := s.GetChange(ctx, "chg_nope"); !errors.Is(err, change.ErrNotFound) {
		t.Fatalf("missing change: %v", err)
	}
}

func testUndoDrift(t *testing.T, s change.Store) {
	ctx := context.Background()
	e := change.NewEngine(s)
	c1 := Converge(t, e, M("shop", nil))
	Converge(t, e, M("shop", func(m *manifest.Manifest) { m.Env["LOG_LEVEL"] = "debug" }))
	var pe *change.PreconditionError
	if _, err := e.PlanUndo(ctx, c1.ID); !errors.As(err, &pe) || pe.Address != "env/LOG_LEVEL" {
		t.Fatalf("want precondition on env/LOG_LEVEL, got %v", err)
	}
}

func testRisk(t *testing.T, s change.Store) {
	ctx := context.Background()
	e := change.NewEngine(s)
	Converge(t, e, M("shop", func(m *manifest.Manifest) {
		m.Services.Storage = &manifest.Storage{Buckets: map[string]manifest.Bucket{"media": {}}}
	}))
	cases := []struct {
		name string
		edit func(m *manifest.Manifest)
		want change.Tier
	}{
		{"add env", func(m *manifest.Manifest) { m.Env["X"] = "1" }, change.TierReversible},
		{"bucket public", func(m *manifest.Manifest) {
			m.Services.Storage.Buckets["media"] = manifest.Bucket{Public: true}
		}, change.TierOutbound},
		{"drop extension", func(m *manifest.Manifest) { m.Services.Postgres.Extensions = nil }, change.TierIrreversible},
		{"drop postgres", func(m *manifest.Manifest) { m.Services.Postgres = nil }, change.TierIrreversible},
		{"drop bucket", func(m *manifest.Manifest) { m.Services.Storage.Buckets = nil }, change.TierIrreversible},
		{"drop app", func(m *manifest.Manifest) { m.Apps = nil }, change.TierReversible},
	}
	for _, tc := range cases {
		m := M("shop", func(m *manifest.Manifest) {
			m.Services.Storage = &manifest.Storage{Buckets: map[string]manifest.Bucket{"media": {}}}
		})
		tc.edit(m)
		p, err := e.Plan(ctx, "shop", desired(t, m))
		if err != nil {
			t.Fatal(err)
		}
		if p.Risk != tc.want {
			t.Errorf("%s: risk %s, want %s (%v)", tc.name, p.Risk, tc.want, p.Ops)
		}
		for _, o := range p.Ops {
			if o.Reason == "" {
				t.Errorf("%s: op %s has no reason", tc.name, o.Address)
			}
		}
	}
	// Policy denial leaves state untouched.
	p, _ := e.Plan(ctx, "shop", desired(t, M("shop", func(m *manifest.Manifest) { m.Services.Postgres = nil })))
	deny := func(p *change.Plan) error {
		if p.Risk.Rank() > change.TierReversible.Rank() {
			return errors.New("token lacks apply:irreversible")
		}
		return nil
	}
	var de *change.DeniedError
	if _, err := e.Apply(ctx, change.ApplyRequest{Plan: p, Confirm: p.Hash, Actor: human, Authorize: deny}); !errors.As(err, &de) {
		t.Fatalf("want DeniedError, got %v", err)
	}
	if ver, _, _ := s.Load(ctx, "shop"); ver != 1 {
		t.Fatalf("denied apply wrote state: version %d", ver)
	}
}

func testList(t *testing.T, s change.Store) {
	ctx := context.Background()
	e := change.NewEngine(s)
	a1 := Converge(t, e, M("alpha", nil))
	b1 := Converge(t, e, M("beta", nil))
	a2 := Converge(t, e, M("alpha", func(m *manifest.Manifest) { m.Env["X"] = "1" }))
	if a2.Version != 2 || b1.Version != 1 {
		t.Fatalf("versions are per project: a2=%d b1=%d", a2.Version, b1.Version)
	}
	all, err := s.ListChanges(ctx, change.ListFilter{})
	if err != nil || len(all) != 3 || all[0].ID != a2.ID || all[2].ID != a1.ID {
		t.Fatalf("list all: %v %v", err, ids(all))
	}
	alpha, _ := s.ListChanges(ctx, change.ListFilter{Project: "alpha", Limit: 1})
	if len(alpha) != 1 || alpha[0].ID != a2.ID {
		t.Fatalf("list alpha: %v", ids(alpha))
	}
	full, _ := s.GetChange(ctx, a2.ID)
	if len(full.Plan.Ops) != 1 || full.Plan.Ops[0].Reason == "" || !full.At.Equal(a2.At) {
		t.Fatalf("round-trip lost data: %+v", full)
	}
	ps, _ := s.ListProjects(ctx)
	if len(ps) != 2 || ps[0] != "alpha" || ps[1] != "beta" {
		t.Fatalf("projects: %v", ps)
	}
}

func addrs(ops []change.Op) []string {
	var out []string
	for _, o := range ops {
		out = append(out, string(o.Action)+" "+o.Address)
	}
	return out
}

func ids(cs []*change.Change) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}
