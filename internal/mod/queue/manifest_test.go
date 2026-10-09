package queue

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/change"
)

func queueSpec(t *testing.T, q any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func count(t *testing.T, e *Engine, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestReconcileQueue: the manifest's queue becomes the engine's configuration,
// reconciling twice changes nothing, an operator's pause survives, and a
// delete purges what is waiting.
func TestReconcileQueue(t *testing.T) {
	e := newEngine(t, nil)
	ctx := context.Background()
	spec := json.RawMessage(`{"app":"jobs","path":"/queues/emails","concurrency":3,"keyConcurrency":1,"rateLimit":10,"ratePeriodSeconds":60,"maxAttempts":4,"leaseSeconds":30}`)
	if err := e.ReconcileQueue(ctx, proj, "emails", spec); err != nil {
		t.Fatal(err)
	}
	c, _ := e.queueConfig(ctx, e.pool, proj, "emails")
	want := QueueConfig{Name: "emails", App: "jobs", Path: "/queues/emails", Concurrency: 3, KeyConcurrency: 1, RateLimit: 10,
		RatePeriodS: 60, MaxAttempts: 4, LeaseS: 30, Configured: true}
	if c != want {
		t.Fatalf("config %+v, want %+v", c, want)
	}
	// Idempotent: no write the second time.
	var at1, at2 time.Time
	q := `SELECT updated_at FROM tq_queues WHERE project = $1 AND name = 'emails'`
	_ = e.pool.QueryRow(ctx, q, proj).Scan(&at1)
	if err := e.ReconcileQueue(ctx, proj, "emails", spec); err != nil {
		t.Fatal(err)
	}
	_ = e.pool.QueryRow(ctx, q, proj).Scan(&at2)
	if !at1.Equal(at2) {
		t.Errorf("second reconcile rewrote the queue (%s -> %s)", at1, at2)
	}
	// A short spec gets the manifest defaults, not the engine's.
	if err := e.ReconcileQueue(ctx, proj, "plain", json.RawMessage(`{"app":"jobs"}`)); err != nil {
		t.Fatal(err)
	}
	c, _ = e.queueConfig(ctx, e.pool, proj, "plain")
	if c.Path != "/queues/plain" || c.MaxAttempts != 10 || c.LeaseS != 60 || c.RateLimit != 0 || c.RatePeriodS != 0 {
		t.Errorf("defaults %+v", c)
	}
	// Pause is the operator's: a changed spec keeps it, other fields follow the manifest.
	if _, err := e.SetPaused(ctx, proj, "emails", true); err != nil {
		t.Fatal(err)
	}
	if err := e.ReconcileQueue(ctx, proj, "emails", json.RawMessage(`{"app":"web","path":"/q","concurrency":1,"maxAttempts":2,"leaseSeconds":5}`)); err != nil {
		t.Fatal(err)
	}
	c, _ = e.queueConfig(ctx, e.pool, proj, "emails")
	if !c.Paused || c.App != "web" || c.Path != "/q" || c.Concurrency != 1 || c.KeyConcurrency != 0 || c.RateLimit != 0 || c.MaxAttempts != 2 {
		t.Errorf("after update %+v", c)
	}
	// Bad specs fail loudly.
	if err := e.ReconcileQueue(ctx, proj, "emails", json.RawMessage(`{"app":"jobs","maxAttempts":101}`)); err == nil {
		t.Error("maxAttempts 101 accepted")
	}
	if err := e.ReconcileQueue(ctx, proj, "emails", json.RawMessage(`{"path":"/x"}`)); err == nil {
		t.Error("queue without app accepted")
	}

	// Delete: waiting jobs go, other queues are untouched, the config is removed.
	e.send(proj, SendRequest{Name: "emails", Delay: time.Hour})
	e.send(proj, SendRequest{Name: "emails"}) // paused: stays queued
	e.send(proj, SendRequest{Name: "plain", App: "jobs", Delay: time.Hour})
	if n := count(t, e.Engine, `SELECT count(*) FROM tq_jobs WHERE project = $1 AND queue = 'emails'`, proj); n != 2 {
		t.Fatalf("jobs before delete: %d", n)
	}
	if err := e.ReconcileQueue(ctx, proj, "emails", nil); err != nil {
		t.Fatal(err)
	}
	if n := count(t, e.Engine, `SELECT count(*) FROM tq_jobs WHERE project = $1 AND queue = 'emails'`, proj); n != 0 {
		t.Errorf("%d job(s) left after delete", n)
	}
	if c, _ := e.queueConfig(ctx, e.pool, proj, "emails"); c.Configured {
		t.Errorf("config left after delete: %+v", c)
	}
	if n := count(t, e.Engine, `SELECT count(*) FROM tq_jobs WHERE project = $1 AND queue = 'plain'`, proj); n != 1 {
		t.Errorf("other queue lost its job: %d", n)
	}
	// Deleting what is not there is fine.
	if err := e.ReconcileQueue(ctx, proj, "emails", nil); err != nil {
		t.Errorf("second delete: %v", err)
	}
}

// TestReconcileQueueDeletePurgesDead: dead-lettered jobs are deleted with the queue.
func TestReconcileQueueDeletePurgesDead(t *testing.T) {
	var a *app
	e := newEngine(t, func(c *Config) {
		c.Endpoint = func(ctx context.Context, project, name, release string) (string, error) { return a.srv.URL, nil }
	})
	a = newApp(t, e.Engine, proj)
	a.handle("/queues/doomed", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(489) })
	ctx := context.Background()
	if err := e.ReconcileQueue(ctx, proj, "doomed", json.RawMessage(`{"app":"jobs"}`)); err != nil {
		t.Fatal(err)
	}
	res := e.send(proj, SendRequest{Name: "doomed"})
	e.waitState(proj, res.Jobs[0], stateDead, 10*time.Second)
	if err := e.ReconcileQueue(ctx, proj, "doomed", nil); err != nil {
		t.Fatal(err)
	}
	if n := count(t, e.Engine, `SELECT count(*) FROM tq_jobs WHERE project = $1 AND queue = 'doomed'`, proj); n != 0 {
		t.Errorf("dead job survived the queue: %d", n)
	}
}

// TestReconcileTopic: subscribers follow the manifest and deliver to the
// subscribing queue's app and path.
func TestReconcileTopic(t *testing.T) {
	e := newEngine(t, nil)
	ctx := context.Background()
	for _, n := range []string{"emails", "audit"} {
		if err := e.ReconcileQueue(ctx, proj, n, queueSpec(t, map[string]any{"app": "jobs", "path": "/queues/" + n, "maxAttempts": 8, "leaseSeconds": 60})); err != nil {
			t.Fatal(err)
		}
	}
	subs := func(topic string) map[string]string {
		ts, err := e.Topics(ctx, proj)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, tp := range ts {
			if tp.Name == topic {
				for _, s := range tp.Subscriptions {
					got[s.Name] = s.App + s.Path
				}
				return got
			}
		}
		return nil
	}
	spec := json.RawMessage(`{"subscribers":["audit","emails"]}`)
	if err := e.ReconcileTopic(ctx, proj, "order.created", spec); err != nil {
		t.Fatal(err)
	}
	if got := subs("order.created"); len(got) != 2 || got["emails"] != "jobs/queues/emails" || got["audit"] != "jobs/queues/audit" {
		t.Fatalf("subscriptions %v", got)
	}
	// The topic fans out: one job per subscriber, aimed at the queue's route.
	res := e.send(proj, SendRequest{Name: "order.created", Payload: json.RawMessage(`{"order":1}`), Delay: time.Hour})
	if !res.Topic || len(res.Jobs) != 2 {
		t.Fatalf("send %+v", res)
	}
	if n := count(t, e.Engine, `SELECT count(*) FROM tq_jobs WHERE project = $1 AND topic = 'order.created' AND path IN ('/queues/emails', '/queues/audit') AND app = 'jobs'`, proj); n != 2 {
		t.Errorf("jobs not aimed at the queues' routes: %d", n)
	}
	// Idempotent, and a queue's new path reaches its subscription.
	if err := e.ReconcileTopic(ctx, proj, "order.created", spec); err != nil {
		t.Fatal(err)
	}
	if err := e.ReconcileQueue(ctx, proj, "emails", json.RawMessage(`{"app":"worker","path":"/mail"}`)); err != nil {
		t.Fatal(err)
	}
	if err := e.ReconcileTopic(ctx, proj, "order.created", spec); err != nil {
		t.Fatal(err)
	}
	if got := subs("order.created"); len(got) != 2 || got["emails"] != "worker/mail" {
		t.Errorf("after queue change %v", got)
	}
	// A dropped subscriber goes; an empty list keeps the topic but has no subscribers.
	if err := e.ReconcileTopic(ctx, proj, "order.created", json.RawMessage(`{"subscribers":["audit"]}`)); err != nil {
		t.Fatal(err)
	}
	if got := subs("order.created"); len(got) != 1 || got["audit"] == "" {
		t.Errorf("after dropping emails %v", got)
	}
	if err := e.ReconcileTopic(ctx, proj, "user.deleted", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if got := subs("user.deleted"); got == nil || len(got) != 0 {
		t.Errorf("empty topic %v", got)
	}
	if res := e.send(proj, SendRequest{Name: "user.deleted"}); !res.Topic || len(res.Jobs) != 0 {
		t.Errorf("send to empty topic %+v", res)
	}
	// A subscriber that is not a configured queue is an error (retried later).
	err := e.ReconcileTopic(ctx, proj, "order.shipped", json.RawMessage(`{"subscribers":["nope"]}`))
	if err == nil || !strings.Contains(err.Error(), `"nope" is not configured`) {
		t.Errorf("unknown subscriber: %v", err)
	}
	// Delete removes every subscriber.
	if err := e.ReconcileTopic(ctx, proj, "order.created", nil); err != nil {
		t.Fatal(err)
	}
	if got := subs("order.created"); len(got) != 0 {
		t.Errorf("after delete %v", got)
	}
	if err := e.ReconcileTopic(ctx, proj, "order.created", nil); err != nil {
		t.Errorf("second delete: %v", err)
	}
}

// TestModuleReconcileKinds goes through the platform entry point: the kinds
// are claimed, addresses route to queue/topic/cron, and a module whose engine
// is not up yet fails (the platform marks the resource failed and retries).
func TestModuleReconcileKinds(t *testing.T) {
	m := &Module{}
	kinds := strings.Join(m.Kinds(), ",")
	if kinds != "queue,topic,cron" {
		t.Errorf("kinds %q", kinds)
	}
	ctx := context.Background()
	m.setStatus("waiting for the platform Postgres", false)
	err := m.Reconcile(ctx, nil, proj, "queue/emails", json.RawMessage(`{"app":"jobs"}`))
	if err == nil || !strings.Contains(err.Error(), "not running yet") {
		t.Errorf("engine down: %v", err)
	}

	e := newEngine(t, nil)
	m.eng = e.Engine
	for _, r := range [][2]string{
		{change.KindQueue + "/emails", `{"app":"jobs"}`},
		{change.KindTopic + "/order.created", `{"subscribers":["emails"]}`},
		{change.KindCron + "/nightly", `{"schedule":"@daily","app":"jobs"}`},
	} {
		addr, spec := r[0], r[1]
		if err := m.Reconcile(ctx, nil, proj, addr, json.RawMessage(spec)); err != nil {
			t.Fatalf("%s: %v", addr, err)
		}
	}
	ts, _ := e.Topics(ctx, proj)
	cs, _ := e.Crons(ctx, proj)
	c, _ := e.queueConfig(ctx, e.pool, proj, "emails")
	if len(ts) != 1 || len(ts[0].Subscriptions) != 1 || len(cs) != 1 || !c.Configured {
		t.Errorf("topics %+v crons %+v queue %+v", ts, cs, c)
	}
	for _, addr := range []string{"topic/order.created", "queue/emails", "cron/nightly"} {
		if err := m.Reconcile(ctx, nil, proj, addr, nil); err != nil {
			t.Fatalf("delete %s: %v", addr, err)
		}
	}
	ts, _ = e.Topics(ctx, proj)
	cs, _ = e.Crons(ctx, proj)
	c, _ = e.queueConfig(ctx, e.pool, proj, "emails")
	if len(ts[0].Subscriptions) != 0 || len(cs) != 0 || c.Configured {
		t.Errorf("after delete: topics %+v crons %+v queue %+v", ts, cs, c)
	}
}
