package change

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func res(addr, spec string) Resource { return Resource{Address: addr, Spec: json.RawMessage(spec)} }

func set(rs ...Resource) map[string]Resource {
	m := map[string]Resource{}
	for _, r := range rs {
		m[r.Address] = r
	}
	return m
}

// applyVia plans desired and applies it as actor through via.
func applyVia(t *testing.T, e *Engine, desired map[string]Resource, via, who string) *Change {
	t.Helper()
	ctx := WithVia(context.Background(), via)
	p, err := e.Plan(ctx, "reelbird", desired)
	if err != nil {
		t.Fatal(err)
	}
	c, err := e.Apply(ctx, ApplyRequest{Plan: p, Confirm: p.Hash, Actor: Actor{Kind: "agent", ID: "tok_1", Name: who}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// configPlan plans desired as a config file and marks its drift.
func configPlan(t *testing.T, e *Engine, desired map[string]Resource) *Plan {
	t.Helper()
	p, err := e.Plan(context.Background(), "reelbird", desired)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.MarkDrift(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	return p
}

func opAt(p *Plan, addr string) *Op {
	for i := range p.Ops {
		if p.Ops[i].Address == addr {
			return &p.Ops[i]
		}
	}
	return nil
}

func TestDriftFromDomainsAdd(t *testing.T) {
	e := NewEngine(NewMemStore())
	config := set(res("project", `{}`), res("app/site", `{"framework":"static"}`), res("env/A", `"1"`))
	if c := applyVia(t, e, config, ViaConfig, "bilal"); c.Via != ViaConfig {
		t.Fatalf("via %q", c.Via)
	}
	// An agent adds a domain: a new resource and the app's routes.
	_, cur, _ := e.Store.Load(context.Background(), "reelbird")
	cur["domain/reelbird.app"] = res("domain/reelbird.app", `{"www":"redirect"}`)
	cur["app/site"] = res("app/site", `{"framework":"static","routes":["reelbird.app"]}`)
	added := applyVia(t, e, cur, "tiffin domains add", "claude-code")

	// The old config would remove both: each op is marked, with a warning.
	p := configPlan(t, e, config)
	plain, _ := e.Plan(context.Background(), "reelbird", config)
	if p.Hash != plain.Hash {
		t.Error("marking drift must not change the hash")
	}
	del, upd := opAt(p, "domain/reelbird.app"), opAt(p, "app/site")
	if del == nil || del.Action != Delete || del.Drift == nil || del.Drift.Change != added.ID || del.Drift.By != "claude-code" || del.Drift.Via != "tiffin domains add" {
		t.Fatalf("delete: %+v", del)
	}
	if upd == nil || upd.Drift == nil || strings.Join(upd.Drift.Fields, ",") != "routes" {
		t.Fatalf("update: %+v", upd)
	}
	if len(p.Warnings) == 0 || !strings.HasPrefix(p.Warnings[0], "Your tiffin.config.ts doesn't have changes made outside it: ") ||
		!strings.Contains(p.Warnings[0], "domain/reelbird.app (added "+added.At.Format("2006-01-02")+" by claude-code with `tiffin domains add`)") ||
		!strings.Contains(p.Warnings[0], "app/site routes (changed ") || !strings.Contains(p.Warnings[0], "tiffin pull") {
		t.Fatalf("warnings: %q", p.Warnings)
	}

	// Confirming the hash alone is not enough.
	var de *DriftError
	if _, err := e.Apply(context.Background(), ApplyRequest{Plan: p, Confirm: p.Hash, Actor: Actor{Kind: "human", ID: "u"}}); !errors.As(err, &de) {
		t.Fatalf("apply without discard: %v", err)
	}
	// After tiffin pull, the config has them: nothing to change, no drift.
	_, cur, _ = e.Store.Load(context.Background(), "reelbird")
	if p := configPlan(t, e, cur); !p.Empty() || p.HasDrift() || len(p.Warnings) > 0 {
		t.Fatalf("after pull: %+v", p)
	}
	// Pulled, then a deliberate edit of the routes: not a revert.
	edited := set(cur["project"], cur["env/A"], cur["domain/reelbird.app"], res("app/site", `{"framework":"static","routes":["reelbird.app","www2.reelbird.app"]}`))
	if p := configPlan(t, e, edited); len(p.Ops) != 1 || p.HasDrift() {
		t.Fatalf("deliberate edit: %+v", p)
	}
	// Discarding applies it, recorded as a config apply.
	c, err := e.Apply(context.Background(), ApplyRequest{Plan: p, Confirm: p.Hash, DiscardDrift: true, Actor: Actor{Kind: "human", ID: "u"}, Via: ViaConfig})
	if err != nil || c == nil || c.Via != ViaConfig {
		t.Fatalf("discard: %v %+v", err, c)
	}
	// Now the config made the current state: plans from it are plain.
	if p := configPlan(t, e, set(config["project"], config["app/site"], res("env/A", `"2"`))); p.HasDrift() {
		t.Fatalf("after discard: %+v", p)
	}
}

func TestDriftScalarAndSystem(t *testing.T) {
	e := NewEngine(NewMemStore())
	config := set(res("project", `{}`), res("env/A", `"1"`), res("env/B", `"x"`))
	applyVia(t, e, config, ViaConfig, "bilal")
	// The dashboard changes A; the box (no via on the context) changes B.
	applyVia(t, e, set(config["project"], res("env/A", `"2"`), config["env/B"]), ViaDashboard, "Bilal")
	p, _ := e.Plan(context.Background(), "reelbird", set(config["project"], res("env/A", `"2"`), res("env/B", `"y"`)))
	c, err := e.Apply(context.Background(), ApplyRequest{Plan: p, Confirm: p.Hash, Actor: Actor{Kind: "system", ID: "system", Name: "Email"}})
	if err != nil || c.Via != ViaSystem {
		t.Fatalf("system: %v %+v", err, c)
	}
	p = configPlan(t, e, config)
	a, b := opAt(p, "env/A"), opAt(p, "env/B")
	if a.Drift == nil || a.Drift.Via != ViaDashboard || len(a.Drift.Fields) != 1 || a.Drift.Fields[0] != "" {
		t.Errorf("A: %+v", a.Drift)
	}
	if b.Drift == nil || b.Drift.By != "Email" || b.Drift.Via != ViaSystem {
		t.Errorf("B: %+v", b.Drift)
	}
	if !strings.Contains(p.Warnings[0], "env/A (changed ") || !strings.Contains(p.Warnings[0], " in the dashboard)") {
		t.Errorf("warning: %q", p.Warnings[0])
	}
	// A config apply's own values are never drift.
	if p := configPlan(t, e, set(config["project"], res("env/A", `"3"`), res("env/B", `"y"`))); p.HasDrift() {
		t.Errorf("a new value is not a revert: %+v", p)
	}
}

func TestDriftOnlyOutsideConfig(t *testing.T) {
	e := NewEngine(NewMemStore())
	v1 := set(res("project", `{}`), res("app/web", `{}`), res("bucket/media", `{}`))
	applyVia(t, e, v1, ViaConfig, "bilal")
	applyVia(t, e, set(v1["project"], v1["app/web"], v1["bucket/media"], res("app/docs", `{}`)), ViaConfig, "bilal")
	// Removing what config files added, or editing them, is a normal plan.
	p := configPlan(t, e, set(v1["project"], res("app/web", `{"memoryMB":512}`)))
	if len(p.Ops) != 3 || p.HasDrift() || len(p.Warnings) > 0 {
		t.Fatalf("plain plan: %+v", p)
	}
	if _, err := e.Apply(context.Background(), ApplyRequest{Plan: p, Confirm: p.Hash, Actor: Actor{Kind: "human", ID: "u"}}); err != nil {
		t.Fatal(err)
	}
}

// A copied project (import, duplicate, move) says nothing about where its
// resources came from: drift looks past it, into the history it carried.
func TestDriftLooksPastCopies(t *testing.T) {
	mk := func(id, via string, ops ...Op) *Change {
		return &Change{ID: id, Via: via, Actor: Actor{Name: id}, Plan: Plan{Ops: ops}}
	}
	create := func(addr string) Op { return Op{Action: Create, Address: addr, After: json.RawMessage(`{}`)} }
	del := Op{Action: Delete, Address: "domain/reelbird.app", Before: json.RawMessage(`{}`)}
	moved := []*Change{ // newest first, as a move carries history over
		mk("copy", ViaCopy, create("app/web"), create("domain/reelbird.app")),
		mk("added", "tiffin domains add", create("domain/reelbird.app")),
		mk("config", ViaConfig, create("app/web")),
	}
	if d := driftOf(del, moved); d == nil || d.Change != "added" {
		t.Errorf("the domain's origin is before the copy: %+v", d)
	}
	// A copy alone marks nothing.
	if d := driftOf(del, moved[:1]); d != nil {
		t.Errorf("copy only: %+v", d)
	}
}
