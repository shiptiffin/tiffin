package change

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/manifest"
)

// A read-only hold is the box's, not the manifest's: plans keep it, and
// setting or lifting it is reversible.
func TestReadOnlyHoldIsUnmanaged(t *testing.T) {
	ctx := context.Background()
	e := NewEngine(NewMemStore())
	proj := Resource{Address: KindProject, Spec: json.RawMessage(`{}`)}
	hold := Resource{Address: KindReadOnly, Spec: json.RawMessage(`{"reason":"disk"}`)}
	p, _ := e.Plan(ctx, "p", map[string]Resource{KindProject: proj, KindReadOnly: hold})
	if len(p.Ops) != 1 || p.Ops[0].Address != KindProject {
		t.Fatalf("a manifest cannot set a hold: %+v", p.Ops)
	}
	if _, err := e.Apply(ctx, ApplyRequest{Plan: p, Confirm: p.Hash}); err != nil {
		t.Fatal(err)
	}
	p, _ = e.PlanEdit(ctx, "p", func(cur map[string]Resource) (map[string]Resource, error) { cur[KindReadOnly] = hold; return cur, nil })
	if p.Risk != TierReversible || !strings.Contains(p.Ops[0].Reason, "database writes") {
		t.Fatalf("hold: %+v", p.Ops)
	}
	if _, err := e.Apply(ctx, ApplyRequest{Plan: p, Confirm: p.Hash}); err != nil {
		t.Fatal(err)
	}
	if p, _ = e.Plan(ctx, "p", map[string]Resource{KindProject: proj}); !p.Empty() {
		t.Fatalf("a manifest plan keeps the hold: %+v", p.Ops)
	}
	if _, err := ManifestFromResources("p", map[string]Resource{KindProject: proj, KindReadOnly: hold}); err != nil {
		t.Fatalf("the manifest leaves the hold out: %v", err)
	}
}

func platformManifest() *manifest.Manifest {
	return &manifest.Manifest{
		Version: 1,
		Project: "p",
		Apps:    map[string]manifest.App{"jobs": {Path: ".", Framework: manifest.FrameworkBun, Role: manifest.RoleWorker, Instances: 1, MemoryMB: 512}},
		Services: manifest.Services{
			Auth:      &manifest.Auth{Methods: []string{"email"}, Organizations: true},
			Email:     &manifest.Email{From: "hi@example.com"},
			Analytics: &manifest.Analytics{RetentionDays: 365},
		},
		Crons:  map[string]manifest.Cron{"tick": {Schedule: "@hourly", App: "jobs", Path: "/cron/tick"}},
		Queues: map[string]manifest.Queue{"emails": {App: "jobs", Path: "/queues/emails", MaxAttempts: 8, LeaseSeconds: 60}},
		Topics: map[string]manifest.Topic{"order.created": {Subscribers: []string{"emails"}}, "empty": {}},
	}
}

func TestResourcesPlatform(t *testing.T) {
	res, err := Resources(platformManifest())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"service/auth":        `{"methods":["email"],"organizations":true}`,
		"service/email":       `{"from":"hi@example.com"}`,
		"service/analytics":   `{"retentionDays":365}`,
		"cron/tick":           `{"schedule":"@hourly","app":"jobs","path":"/cron/tick"}`,
		"queue/emails":        `{"app":"jobs","path":"/queues/emails","concurrency":0,"keyConcurrency":0,"rateLimit":0,"ratePeriodSeconds":0,"maxAttempts":8,"leaseSeconds":60}`,
		"topic/order.created": `{"subscribers":["emails"]}`,
		"topic/empty":         `{}`,
	}
	for addr, spec := range want {
		if got := string(res[addr].Spec); got != spec {
			t.Errorf("%s spec = %s, want %s", addr, got, spec)
		}
	}
}

func TestCronsOrderedAfterApps(t *testing.T) {
	res, _ := Resources(platformManifest())
	ops := Diff(nil, res)
	idx := map[string]int{}
	for i, o := range ops {
		idx[o.Address] = i
	}
	if idx["cron/tick"] < idx["app/jobs"] {
		t.Errorf("create: cron before app: %v", idx)
	}
	del := Diff(res, nil)
	idx = map[string]int{}
	for i, o := range del {
		idx[o.Address] = i
	}
	if idx["cron/tick"] > idx["app/jobs"] {
		t.Errorf("delete: app before cron: %v", idx)
	}
}

func TestQueuesTopicsOrdering(t *testing.T) {
	res, _ := Resources(platformManifest())
	order := func(ops []Op) map[string]int {
		idx := map[string]int{}
		for i, o := range ops {
			idx[o.Address] = i
		}
		return idx
	}
	idx := order(Diff(nil, res))
	if !(idx["app/jobs"] < idx["queue/emails"] && idx["queue/emails"] < idx["topic/order.created"] && idx["topic/order.created"] < idx["cron/tick"]) {
		t.Errorf("create order: %v", idx)
	}
	idx = order(Diff(res, nil))
	if !(idx["cron/tick"] < idx["topic/order.created"] && idx["topic/order.created"] < idx["queue/emails"] && idx["queue/emails"] < idx["app/jobs"]) {
		t.Errorf("delete order: %v", idx)
	}
}

func TestClassifyPlatform(t *testing.T) {
	cases := []struct {
		op     Op
		tier   Tier
		reason string
	}{
		{Op{Action: Delete, Address: "service/auth"}, TierIrreversible, "every user account, session and organization"},
		{Op{Action: Delete, Address: "service/analytics"}, TierIrreversible, "all collected analytics events"},
		{Op{Action: Delete, Address: "service/email"}, TierReversible, "email"},
		{Op{Action: Create, Address: "cron/tick"}, TierReversible, "cron tick"},
		{Op{Action: Delete, Address: "cron/tick"}, TierReversible, "cron"},
		{Op{Action: Update, Address: "cron/tick"}, TierReversible, "cron tick"},
		{Op{Action: Create, Address: "queue/emails"}, TierReversible, "queue emails"},
		{Op{Action: Update, Address: "queue/emails"}, TierReversible, "queue emails"},
		{Op{Action: Delete, Address: "queue/emails"}, TierIrreversible, "deletes queue \"emails\" and any jobs still waiting or dead in it"},
		{Op{Action: Create, Address: "topic/order.created"}, TierReversible, "topic order.created"},
		{Op{Action: Update, Address: "topic/order.created"}, TierReversible, "topic order.created"},
		{Op{Action: Delete, Address: "topic/order.created"}, TierReversible, "topic \"order.created\""},
		{Op{Action: Update, Address: "service/auth", Before: []byte(`{"methods":["email"]}`), After: []byte(`{"methods":["email","otp"]}`)}, TierReversible, "auth"},
	}
	for _, c := range cases {
		tier, reason := Classify(c.op)
		if tier != c.tier || !strings.Contains(reason, c.reason) {
			t.Errorf("%s %s: got %s %q, want %s containing %q", c.op.Action, c.op.Address, tier, reason, c.tier, c.reason)
		}
	}
}

// Database, KV, Files, Email and Analytics are always there: a desired
// state without one is refused, except the project's own deletion.
func TestAlwaysOn(t *testing.T) {
	all := AlwaysOnResources("shop")
	if len(all) != 6 {
		t.Fatalf("always-on resources: %v", all)
	}
	desired := map[string]Resource{KindProject: {Address: KindProject, Spec: json.RawMessage(`{}`)}}
	for k, v := range all {
		desired[k] = v
	}
	if m := AlwaysOnMissing(desired); len(m) != 0 {
		t.Fatalf("missing %v", m)
	}
	delete(desired, "service/valkey")
	delete(desired, "bucket/files")
	m := AlwaysOnMissing(desired)
	if len(m) != 2 || m[0] != "service/valkey" || m[1] != "bucket/files" {
		t.Fatalf("missing %v", m)
	}
	var pe *PreconditionError
	if err := ErrAlwaysOn(m); !errors.As(err, &pe) || !strings.Contains(err.Error(), "Delete all data") {
		t.Fatalf("refusal: %v", err)
	}
	if m := AlwaysOnMissing(map[string]Resource{"secret/X": {}}); m != nil {
		t.Fatalf("deleting the project: %v", m)
	}
}

// Delete all data is irreversible, so it is confirmed; it sorts after the
// services and buckets it empties, and its restore before they go.
func TestEmptiedOps(t *testing.T) {
	addr := EmptyAddress("postgres")
	spec := json.RawMessage(`{"version":3}`)
	for _, o := range []Op{{Action: Create, Address: addr, After: spec}, {Action: Update, Address: addr, Before: spec, After: spec}, {Action: Delete, Address: addr, Before: spec}} {
		if tier, why := Classify(o); tier != TierIrreversible || !strings.Contains(why, "the database") {
			t.Errorf("%s: %s %q", o.Action, tier, why)
		}
	}
	if !Unmanaged(addr) {
		t.Fatal("manifest plans keep it")
	}
	ops := []Op{{Action: Create, Address: addr}, {Action: Create, Address: "bucket/files"}, {Action: Create, Address: "service/postgres"}}
	SortOps(ops)
	if ops[2].Address != addr {
		t.Fatalf("create order %v", ops)
	}
	ops = []Op{{Action: Delete, Address: "service/postgres"}, {Action: Delete, Address: addr}}
	SortOps(ops)
	if ops[0].Address != addr {
		t.Fatalf("delete order %v", ops)
	}
	now := time.Now()
	at := func(d time.Duration) func(string, int64) (time.Time, bool) {
		return func(part string, v int64) (time.Time, bool) { return now.Add(-d), part == "postgres" && v == 3 }
	}
	restore := []Op{{Action: Delete, Address: addr, Before: spec}}
	if err := RestoreExpired(restore, at(6*24*time.Hour), now); err != nil {
		t.Fatalf("within 7 days: %v", err)
	}
	if err := RestoreExpired(restore, at(8*24*time.Hour), now); err == nil {
		t.Fatal("past 7 days the restore is refused")
	}
}
