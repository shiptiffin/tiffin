package box

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/mod/budget"
	"github.com/btahir/tiffin/internal/mod/storage"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
)

func TestDecide(t *testing.T) {
	set := budget.DefaultSettings // warn 85, stop 95, resume 90
	sizes := map[string]int64{"big": 900, "fast": 300, "calm": 100}
	growth := map[string]int64{"big": 0, "fast": 200, "calm": 5}
	in := func(used float64, held map[string]string) guardIn {
		return guardIn{used: used, set: set, sizes: sizes, growth: growth, recent: map[string]int64{}, limit: map[string]int64{}, held: held, waived: map[string]string{}}
	}
	eq := func(name string, got, want map[string]string) {
		t.Helper()
		if !maps.Equal(got, want) {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
	none := map[string]string{}

	w, _ := decide(in(90, none))
	eq("below stop", w, none)
	w, _ = decide(in(95, none))
	eq("at stop the fastest grower is held, not the biggest", w, map[string]string{"fast": "disk"})
	w, _ = decide(in(97, map[string]string{"fast": "disk"}))
	eq("one hold a round: nobody else is writing", w, map[string]string{"fast": "disk"})
	i := in(97, map[string]string{"fast": "disk"})
	i.recent = map[string]int64{"calm": 10}
	w, _ = decide(i)
	eq("still full and calm is writing: calm too", w, map[string]string{"fast": "disk", "calm": "disk"})
	w, _ = decide(in(91, map[string]string{"fast": "disk"}))
	eq("hysteresis: held until below resume", w, map[string]string{"fast": "disk"})
	w, _ = decide(in(89, map[string]string{"fast": "disk"}))
	eq("below resume: lifted", w, none)

	g0 := map[string]int64{}
	i = in(96, none)
	i.growth = g0
	w, _ = decide(i)
	eq("nobody grew: the biggest", w, map[string]string{"big": "disk"})

	i = in(99, none)
	i.set.DiskStopPercent = 100
	w, _ = decide(i)
	eq("stop at 100 only warns", w, none)

	// A storage limit holds that project alone, whatever the disk.
	i = in(10, none)
	i.limit = map[string]int64{"calm": 100, "big": 1000}
	w, _ = decide(i)
	eq("at its limit", w, map[string]string{"calm": "limit"})
	i.limit["calm"] = 200
	w, _ = decide(i)
	eq("limit raised", w, none)

	// The owner lifted fast's hold while the disk was still full: it stays
	// lifted, and no bystander is stopped instead, until the disk recovers.
	i = in(96, none)
	i.waived = map[string]string{"fast": "disk"}
	w, wv := decide(i)
	eq("waived", w, none)
	eq("waiver kept", wv, map[string]string{"fast": "disk"})
	i.recent = map[string]int64{"calm": 1}
	w, _ = decide(i)
	eq("waived, and calm still writing", w, map[string]string{"calm": "disk"})
	i = in(80, none)
	i.waived = map[string]string{"fast": "disk"}
	_, wv = decide(i)
	eq("waiver ends below resume", wv, none)
}

// A round measures, holds the top grower as a change by the system, lifts
// it below the resume level, and respects an owner who lifted it by hand.
func TestGuardRound(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := &platform.Platform{DB: db, Engine: change.NewEngine(db), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, pr := range []string{"shop", "blog"} {
		plan, _ := p.Engine.Plan(ctx, pr, map[string]change.Resource{change.KindProject: {Address: change.KindProject, Spec: json.RawMessage(`{}`)}})
		if _, err := p.Engine.Apply(ctx, change.ApplyRequest{Plan: plan, Confirm: plan.Hash, Actor: change.Actor{Kind: "human", ID: "t"}}); err != nil {
			t.Fatal(err)
		}
	}
	used := 50.0
	sizes := map[string]int64{"shop": 1 << 30, "blog": 5 << 30}
	g := newGuard(p, "/data")
	g.disk = func(string) Disk { return Disk{TotalBytes: 100 << 30, UsedPercent: used} }
	g.measure = func(context.Context, []string) (map[string]int64, map[string]int64) {
		return map[string]int64{"shop": sizes["shop"]}, map[string]int64{"blog": sizes["blog"]}
	}
	g.limit = func(context.Context, string) int64 { return 0 }
	clock := time.Now()
	g.now = func() time.Time { return clock }
	round := func() {
		t.Helper()
		clock = clock.Add(guardEvery)
		if err := g.round(ctx); err != nil {
			t.Fatal(err)
		}
	}
	hold := func(pr string) *holdSpec {
		t.Helper()
		_, res, _ := db.Load(ctx, pr)
		r, ok := res[change.KindReadOnly]
		if !ok {
			return nil
		}
		var h holdSpec
		_ = json.Unmarshal(r.Spec, &h)
		return &h
	}

	round()
	if v := g.state(); v.Level != "ok" || v.Top.Project != "blog" || hold("shop") != nil || hold("blog") != nil {
		t.Fatalf("calm box: %+v", v)
	}
	used, sizes["shop"] = 87, 3<<30 // shop grew 2 GiB
	round()
	if v := g.state(); v.Level != "warn" || v.Top.Project != "shop" || !strings.Contains(v.Message, "shop is growing fastest") || hold("shop") != nil {
		t.Fatalf("warning: %+v", v)
	}
	if storage.ReadOnly("shop") != "" {
		t.Fatal("a warning holds nothing")
	}
	used = 96
	round()
	h := hold("shop")
	if h == nil || h.Reason != "disk" || !strings.Contains(h.Message, "96% full") || !strings.Contains(h.Message, "below 90%") || hold("blog") != nil {
		t.Fatalf("stop: shop %+v, blog %+v", h, hold("blog"))
	}
	cs, _ := db.ListChanges(ctx, change.ListFilter{Project: "shop", Limit: 1})
	if c := cs[0]; c.Actor.Kind != "system" || c.Actor.Name != "disk guard" || !strings.Contains(c.Intent, "read-only") || c.Plan.Ops[0].Risk != change.TierReversible {
		t.Fatalf("history: %+v", c)
	}
	// The reconciler applies it: uploads refused with the same words.
	raw, _ := json.Marshal(h)
	if err := g.hold(ctx, "shop", raw); err != nil || storage.ReadOnly("shop") != h.Message || len(g.state().ReadOnly) != 1 {
		t.Fatalf("hold: %v %q", err, storage.ReadOnly("shop"))
	}

	// The owner undoes it while the disk is still full: the guard leaves
	// shop alone (and stops nobody else) until the disk recovers.
	plan, _ := p.Engine.PlanUndo(ctx, cs[0].ID)
	if _, err := p.Engine.Apply(ctx, change.ApplyRequest{Plan: plan, Confirm: plan.Hash, Actor: change.Actor{Kind: "human", ID: "owner"}}); err != nil {
		t.Fatal(err)
	}
	_ = g.hold(ctx, "shop", nil)
	round()
	if hold("shop") != nil || hold("blog") != nil || storage.ReadOnly("shop") != "" {
		t.Fatal("an owner's lift stands while the disk is still full")
	}
	used = 80
	round()
	used = 96
	round()
	if hold("shop") == nil {
		t.Fatal("once the disk recovered, the waiver ends")
	}
	used = 89
	round()
	cs, _ = db.ListChanges(ctx, change.ListFilter{Project: "shop", Limit: 1})
	if hold("shop") != nil || !strings.Contains(cs[0].Intent, "can write again") {
		t.Fatalf("below resume: %+v %+v", hold("shop"), cs[0])
	}

	// A storage limit holds the project over it, and raising it lifts.
	limit := int64(2 << 30)
	g.limit = func(_ context.Context, pr string) int64 {
		if pr == "shop" {
			return limit
		}
		return 0
	}
	round()
	if h := hold("shop"); h == nil || h.Reason != "limit" || !strings.Contains(h.Message, "storage quota set shop") ||
		!strings.Contains(h.Message, "Delete data from its database (3.0 GiB) or files (0 B)") {
		t.Fatalf("limit: %+v", h)
	}
	cs, _ = db.ListChanges(ctx, change.ListFilter{Project: "shop", Limit: 1})
	if cs[0].Actor.Name != "storage limit" {
		t.Fatalf("a limit's hold is the storage limit's doing: %+v", cs[0].Actor)
	}
	limit = 4 << 30
	round()
	cs, _ = db.ListChanges(ctx, change.ListFilter{Project: "shop", Limit: 1})
	if hold("shop") != nil || cs[0].Actor.Name != "storage limit" || cs[0].Intent != "shop is under its 4.0 GiB storage limit: it can write again" {
		t.Fatalf("raising the limit lifts the hold: %+v", cs[0])
	}
	limit = 2 << 30
	round()
	limit = 0
	round()
	if cs, _ = db.ListChanges(ctx, change.ListFilter{Project: "shop", Limit: 1}); hold("shop") != nil || cs[0].Intent != "shop has no storage limit now: it can write again" {
		t.Fatalf("clearing the limit lifts the hold: %+v", cs[0])
	}
}
