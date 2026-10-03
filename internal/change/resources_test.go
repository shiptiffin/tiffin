package change

import (
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/manifest"
)

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
