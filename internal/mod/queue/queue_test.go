package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

const proj = "shop"

func TestSignVerify(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	body := []byte(`{"a":1}`)
	h := Sign("s3cret", now, body)
	if !strings.HasPrefix(h, "t=1700000000,v1=") {
		t.Fatalf("header %q", h)
	}
	if !Verify("s3cret", h, body, now, time.Minute) {
		t.Fatal("valid signature rejected")
	}
	for name, ok := range map[string]bool{
		"wrong secret": Verify("other", h, body, now, time.Minute),
		"tampered":     Verify("s3cret", h, []byte(`{"a":2}`), now, time.Minute),
		"too old":      Verify("s3cret", h, body, now.Add(10*time.Minute), time.Minute),
		"garbage":      Verify("s3cret", "v1=abc", body, now, time.Minute),
	} {
		if ok {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestDeliverSignedAndAcked(t *testing.T) {
	e := newEngine(t, nil)
	a := newApp(t, e.Engine, proj)
	a.handle("/q/emails", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"sent":true}`)
	})
	e.configure(proj, QueueConfig{Name: "emails", URL: a.url("/q/emails")})
	res := e.send(proj, SendRequest{Name: "emails", Payload: json.RawMessage(`{"to":"a@example.com"}`), By: "test"})
	if len(res.Jobs) != 1 || res.Topic || !strings.Contains(res.Message, "enqueued") {
		t.Fatalf("send result %+v", res)
	}
	j := e.waitState(proj, res.Jobs[0], stateCompleted, 10*time.Second)
	if string(j.Output) != `{"sent": true}` && string(j.Output) != `{"sent":true}` {
		t.Errorf("output %s", j.Output)
	}
	if len(j.Attempts) != 1 || j.Attempts[0].Outcome != "ok" || j.Attempts[0].Status != 200 {
		t.Errorf("attempts %+v", j.Attempts)
	}
	d := a.deliveries()
	if len(d) != 1 || d[0].Type != "job" || d[0].Queue != "emails" || string(d[0].Payload) != `{"to":"a@example.com"}` || d[0].Attempt != 1 {
		t.Errorf("delivery %+v payload %s", d, d[0].Payload)
	}
	if a.badSig.Load() != 0 {
		t.Error("bad signatures")
	}
}

func TestDefaultTargetNeedsApp(t *testing.T) {
	e := newEngine(t, nil)
	_, err := e.Send(context.Background(), proj, SendRequest{Name: "emails"})
	qe, ok := err.(*Error)
	if !ok || qe.Code != "validation" || !strings.Contains(qe.Hint, "queue configure") {
		t.Fatalf("err %v", err)
	}
	// With a sending app the job targets it at /queues/<name>.
	res := e.send(proj, SendRequest{Name: "emails", FromApp: "web"})
	if j := e.job(proj, res.Jobs[0]); j.Target != "web:/queues/emails" {
		t.Errorf("target %s", j.Target)
	}
}

func TestRetryThenSucceed(t *testing.T) {
	e := newEngine(t, nil)
	a := newApp(t, e.Engine, proj)
	var n atomic.Int64
	a.handle("/q", func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) <= 2 {
			http.Error(w, `{"error":"database is busy"}`, 500)
			return
		}
		w.WriteHeader(204)
	})
	e.configure(proj, QueueConfig{Name: "work", URL: a.url("/q")})
	res := e.send(proj, SendRequest{Name: "work"})
	j := e.waitState(proj, res.Jobs[0], stateCompleted, 10*time.Second)
	if j.Attempt != 3 || len(j.Attempts) != 3 {
		t.Fatalf("attempt %d, attempts %+v", j.Attempt, j.Attempts)
	}
	if j.Attempts[0].Outcome != "retry" || j.Attempts[0].Status != 500 || j.Attempts[0].Error != "HTTP 500: database is busy" {
		t.Errorf("first attempt %+v", j.Attempts[0])
	}
	d := a.deliveries()
	if d[2].Attempt != 3 || d[2].AttemptID != 3 {
		t.Errorf("third delivery attempt %d id %d", d[2].Attempt, d[2].AttemptID)
	}
}

func TestNonRetryableGoesToDLQAndReplays(t *testing.T) {
	e := newEngine(t, nil)
	a := newApp(t, e.Engine, proj)
	var fixed atomic.Bool
	a.handle("/status", func(w http.ResponseWriter, r *http.Request) {
		if fixed.Load() {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(StatusNonRetrying)
		fmt.Fprint(w, `{"error":{"message":"unknown customer"}}`)
	})
	a.handle("/header", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(HeaderNonRetry, "true")
		http.Error(w, "nope", 400)
	})
	e.configure(proj, QueueConfig{Name: "a", URL: a.url("/status")})
	e.configure(proj, QueueConfig{Name: "b", URL: a.url("/header")})
	ja := e.send(proj, SendRequest{Name: "a"}).Jobs[0]
	jb := e.send(proj, SendRequest{Name: "b"}).Jobs[0]
	da := e.waitState(proj, ja, stateDead, 10*time.Second)
	db := e.waitState(proj, jb, stateDead, 10*time.Second)
	if da.Attempt != 1 || da.LastError != "HTTP 489: unknown customer" {
		t.Errorf("a: %+v", da)
	}
	if db.Attempt != 1 || !strings.Contains(db.LastError, "nope") {
		t.Errorf("b: %+v", db)
	}
	dead, _ := e.ListJobs(context.Background(), proj, ListFilter{State: stateDead})
	if len(dead) != 2 {
		t.Fatalf("dlq has %d", len(dead))
	}
	dry, err := e.ReplayDead(context.Background(), proj, "a", 0, true)
	if err != nil || dry.Count != 1 || len(dry.Replayed) != 0 || !strings.Contains(dry.Message, "dry run") {
		t.Fatalf("dry run %+v %v", dry, err)
	}
	if e.job(proj, ja).State != stateDead {
		t.Fatal("dry run replayed")
	}
	fixed.Store(true)
	res, err := e.ReplayDead(context.Background(), proj, "a", 0, false)
	if err != nil || len(res.Replayed) != 1 {
		t.Fatalf("replay %+v %v", res, err)
	}
	j := e.waitState(proj, ja, stateCompleted, 10*time.Second)
	if j.Attempt != 1 || len(j.Attempts) != 2 {
		t.Errorf("after replay attempt %d attempts %d", j.Attempt, len(j.Attempts))
	}
}

func TestMaxAttemptsAndRetryAfter(t *testing.T) {
	e := newEngine(t, nil)
	a := newApp(t, e.Engine, proj)
	var times []time.Time
	var mu sync.Mutex
	a.handle("/busy", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(503)
	})
	e.configure(proj, QueueConfig{Name: "busy", URL: a.url("/busy"), MaxAttempts: 3})
	id := e.send(proj, SendRequest{Name: "busy"}).Jobs[0]
	j := e.waitState(proj, id, stateDead, 15*time.Second)
	if j.Attempt != 3 || !strings.Contains(j.LastError, "gave up after 3 attempts") {
		t.Errorf("dead job %+v", j)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(times) != 3 {
		t.Fatalf("%d attempts", len(times))
	}
	for i := 1; i < 3; i++ {
		if gap := times[i].Sub(times[i-1]); gap < 900*time.Millisecond {
			t.Errorf("Retry-After ignored: gap %s", gap)
		}
	}
}

func TestLeaseAndHeartbeat(t *testing.T) {
	e := newEngine(t, nil)
	a := newApp(t, e.Engine, proj)
	var calls atomic.Int64
	a.handle("/slow", func(w http.ResponseWriter, r *http.Request) {
		// First attempt hangs past the lease; the box cuts it off.
		if calls.Add(1) == 1 {
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
			return
		}
		w.WriteHeader(200)
	})
	var b deliveryBody
	a.handle("/beat", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&b)
		id, _ := ParseJobID(b.ID)
		for i := 0; i < 8; i++ { // 2.4s of work with a 1s lease
			time.Sleep(300 * time.Millisecond)
			if _, err := e.Heartbeat(r.Context(), proj, id, b.AttemptID); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		}
		w.WriteHeader(200)
	})
	e.configure(proj, QueueConfig{Name: "slow", URL: a.url("/slow"), LeaseS: 1})
	e.configure(proj, QueueConfig{Name: "beat", URL: a.url("/beat"), LeaseS: 1})
	slow := e.send(proj, SendRequest{Name: "slow"}).Jobs[0]
	beat := e.send(proj, SendRequest{Name: "beat"}).Jobs[0]
	j := e.waitState(proj, slow, stateCompleted, 10*time.Second)
	if len(j.Attempts) != 2 || !strings.Contains(j.Attempts[0].Error, "within the 1s lease") {
		t.Errorf("slow attempts %+v", j.Attempts)
	}
	j = e.waitState(proj, beat, stateCompleted, 10*time.Second)
	if len(j.Attempts) != 1 || j.Attempts[0].DurationMS < 2000 {
		t.Errorf("heartbeat attempts %+v", j.Attempts)
	}
	// A finished attempt can't be extended.
	id, _ := ParseJobID(beat)
	if _, err := e.Heartbeat(context.Background(), proj, id, 1); err == nil || err.(*Error).Code != "conflict" {
		t.Errorf("heartbeat after finish: %v", err)
	}
}

func TestPerKeyAndQueueConcurrency(t *testing.T) {
	e := newEngine(t, nil)
	a := newApp(t, e.Engine, proj)
	var mu sync.Mutex
	running := map[string]int{}
	peak := map[string]int{}
	total, peakTotal := 0, 0
	a.handle("/c", func(w http.ResponseWriter, r *http.Request) {
		var b deliveryBody
		_ = json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		running[b.Key]++
		total++
		peak[b.Key] = max(peak[b.Key], running[b.Key])
		peakTotal = max(peakTotal, total)
		mu.Unlock()
		time.Sleep(60 * time.Millisecond)
		mu.Lock()
		running[b.Key]--
		total--
		mu.Unlock()
		w.WriteHeader(200)
	})
	e.configure(proj, QueueConfig{Name: "c", URL: a.url("/c"), KeyConcurrency: 2, Concurrency: 5})
	var ids []string
	for i := 0; i < 60; i++ {
		ids = append(ids, e.send(proj, SendRequest{Name: "c", Key: fmt.Sprintf("tenant-%d", i%4)}).Jobs[0])
	}
	start := time.Now()
	for _, id := range ids {
		e.waitState(proj, id, stateCompleted, 30*time.Second)
	}
	mu.Lock()
	defer mu.Unlock()
	for k, p := range peak {
		if p > 2 {
			t.Errorf("key %s ran %d at once (limit 2)", k, p)
		}
	}
	if peakTotal > 5 {
		t.Errorf("queue ran %d at once (limit 5)", peakTotal)
	}
	if peakTotal < 4 {
		t.Errorf("only %d ran at once; limits are too strict", peakTotal)
	}
	t.Logf("60 jobs, 4 keys, keyConcurrency 2, concurrency 5: peak %d, %s", peakTotal, time.Since(start).Round(time.Millisecond))
}

func TestFIFOGroup(t *testing.T) {
	e := newEngine(t, nil)
	a := newApp(t, e.Engine, proj)
	var mu sync.Mutex
	var order []int
	failed := map[int]bool{}
	a.handle("/f", func(w http.ResponseWriter, r *http.Request) {
		var b deliveryBody
		_ = json.NewDecoder(r.Body).Decode(&b)
		var n int
		_ = json.Unmarshal(b.Payload, &n)
		mu.Lock()
		defer mu.Unlock()
		if n == 3 && !failed[n] { // the head fails once: the group waits for it
			failed[n] = true
			w.WriteHeader(500)
			return
		}
		order = append(order, n)
		w.WriteHeader(200)
	})
	e.configure(proj, QueueConfig{Name: "f", URL: a.url("/f")})
	var ids []string
	for i := 0; i < 12; i++ {
		ids = append(ids, e.send(proj, SendRequest{Name: "f", GroupKey: "order-7", Payload: json.RawMessage(fmt.Sprint(i))}).Jobs[0])
	}
	// An unrelated group is not held up.
	other := e.send(proj, SendRequest{Name: "f", GroupKey: "order-8", Payload: json.RawMessage("100")}).Jobs[0]
	e.waitState(proj, other, stateCompleted, 10*time.Second)
	for _, id := range ids {
		e.waitState(proj, id, stateCompleted, 20*time.Second)
	}
	mu.Lock()
	defer mu.Unlock()
	var got []int
	for _, n := range order {
		if n != 100 {
			got = append(got, n)
		}
	}
	for i, n := range got {
		if n != i {
			t.Fatalf("order %v", got)
		}
	}
}

func TestRateLimit(t *testing.T) {
	e := newEngine(t, nil)
	a := newApp(t, e.Engine, proj)
	var mu sync.Mutex
	var starts []time.Time
	a.handle("/r", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		w.WriteHeader(200)
	})
	e.configure(proj, QueueConfig{Name: "r", URL: a.url("/r"), RateLimit: 5, RatePeriodS: 1})
	var ids []string
	t0 := time.Now()
	for i := 0; i < 12; i++ {
		ids = append(ids, e.send(proj, SendRequest{Name: "r", Key: "api"}).Jobs[0])
	}
	for _, id := range ids {
		e.waitState(proj, id, stateCompleted, 20*time.Second)
	}
	mu.Lock()
	defer mu.Unlock()
	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
	// At most 5 in any second: 12 jobs need more than 2s.
	if el := starts[len(starts)-1].Sub(t0); el < 1900*time.Millisecond {
		t.Errorf("12 jobs at 5/s finished in %s", el)
	}
	for i := range starts {
		n := 0
		for k := i; k < len(starts) && starts[k].Sub(starts[i]) < time.Second; k++ {
			n++
		}
		if n > 5 {
			t.Errorf("%d starts within one second (limit 5/s)", n)
		}
	}
}

func TestTopicsFanOut(t *testing.T) {
	e := newEngine(t, nil)
	a := newApp(t, e.Engine, proj)
	ctx := context.Background()
	if _, err := e.Subscribe(ctx, proj, "order.created", Subscription{Name: "mailer", URL: a.url("/mail")}); err != nil {
		t.Fatal(err)
	}
	a.handle("/stock", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	if _, err := e.Subscribe(ctx, proj, "order.created", Subscription{Name: "stock", URL: a.url("/stock")}); err != nil {
		t.Fatal(err)
	}
	e.configure(proj, QueueConfig{Name: "order.created", MaxAttempts: 2})
	res := e.send(proj, SendRequest{Name: "order.created", Payload: json.RawMessage(`{"order":7}`)})
	if !res.Topic || len(res.Jobs) != 2 {
		t.Fatalf("publish %+v", res)
	}
	mail := e.waitState(proj, res.Jobs[0], stateCompleted, 10*time.Second)
	stock := e.waitState(proj, res.Jobs[1], stateDead, 10*time.Second)
	if mail.Subscription != "mailer" || stock.Subscription != "stock" || stock.Attempt != 2 {
		t.Errorf("mail %+v stock %+v", mail, stock)
	}
	ts, _ := e.Topics(ctx, proj)
	if len(ts) != 1 || len(ts[0].Subscriptions) != 2 {
		t.Errorf("topics %+v", ts)
	}
	if err := e.Unsubscribe(ctx, proj, "order.created", "stock"); err != nil {
		t.Fatal(err)
	}
	if err := e.Unsubscribe(ctx, proj, "order.created", "stock"); err == nil {
		t.Error("unsubscribing twice succeeded")
	}
	if res := e.send(proj, SendRequest{Name: "order.created"}); len(res.Jobs) != 1 {
		t.Errorf("after unsubscribe %+v", res)
	}
}

func TestDedupeDelayCancelAndPurge(t *testing.T) {
	e := newEngine(t, nil)
	a := newApp(t, e.Engine, proj)
	e.configure(proj, QueueConfig{Name: "d", URL: a.url("/d")})
	first := e.send(proj, SendRequest{Name: "d", Dedupe: "welcome-42", Delay: 1200 * time.Millisecond})
	again := e.send(proj, SendRequest{Name: "d", Dedupe: "welcome-42"})
	if !again.Deduplicated || again.Jobs[0] != first.Jobs[0] {
		t.Fatalf("dedupe %+v vs %+v", again, first)
	}
	if j := e.job(proj, first.Jobs[0]); j.State != stateScheduled {
		t.Errorf("delayed job is %s", j.State)
	}
	sent := time.Now()
	e.waitState(proj, first.Jobs[0], stateCompleted, 10*time.Second)
	if el := time.Since(sent); el < time.Second {
		t.Errorf("delayed job ran after %s", el)
	}

	// Cancel a waiting job and a running one.
	a.handle("/hang", func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	e.configure(proj, QueueConfig{Name: "hang", URL: a.url("/hang"), LeaseS: 60})
	running := e.send(proj, SendRequest{Name: "hang"}).Jobs[0]
	later := e.send(proj, SendRequest{Name: "hang", Delay: time.Hour}).Jobs[0]
	e.waitState(proj, running, stateRunning, 5*time.Second)
	ctx := context.Background()
	for _, id := range []string{running, later} {
		n, _ := ParseJobID(id)
		if _, err := e.CancelJob(ctx, proj, n); err != nil {
			t.Fatal(err)
		}
	}
	j := e.waitState(proj, running, stateCancelled, 5*time.Second)
	eventually(t, 5*time.Second, "cancelled attempt recorded", func() bool {
		j = e.job(proj, running)
		return len(j.Attempts) == 1 && j.Attempts[0].Outcome == "cancelled"
	})
	n, _ := ParseJobID(later)
	if _, err := e.CancelJob(ctx, proj, n); err == nil {
		t.Error("cancelled twice")
	}

	// Purge is two-step.
	for i := 0; i < 3; i++ {
		e.send(proj, SendRequest{Name: "hang", Delay: time.Hour})
	}
	p1, err := e.Purge(ctx, proj, "hang", false, "")
	if err != nil || p1.Purged || p1.Count != 3 || p1.Confirm == "" {
		t.Fatalf("count %+v %v", p1, err)
	}
	p2, err := e.Purge(ctx, proj, "hang", false, p1.Confirm)
	if err != nil || !p2.Purged || p2.Count != 3 {
		t.Fatalf("purge %+v %v", p2, err)
	}
	st, _ := e.Stats(ctx, proj, "hang")
	if len(st) != 1 || st[0].Scheduled != 0 {
		t.Errorf("stats after purge %+v", st)
	}
}

func TestPauseResumeAndStats(t *testing.T) {
	e := newEngine(t, nil)
	a := newApp(t, e.Engine, proj)
	ctx := context.Background()
	e.configure(proj, QueueConfig{Name: "p", URL: a.url("/p")})
	if _, err := e.SetPaused(ctx, proj, "p", true); err != nil {
		t.Fatal(err)
	}
	id := e.send(proj, SendRequest{Name: "p"}).Jobs[0]
	eventually(t, 5*time.Second, "job parked by pause", func() bool {
		return e.job(proj, id).WaitingFor == "the queue is paused"
	})
	time.Sleep(300 * time.Millisecond)
	if len(a.deliveries()) != 0 {
		t.Fatal("paused queue delivered")
	}
	st, _ := e.Stats(ctx, proj, "p")
	if !st[0].Paused || st[0].Queued != 1 {
		t.Errorf("stats %+v", st[0])
	}
	if _, err := e.SetPaused(ctx, proj, "p", false); err != nil {
		t.Fatal(err)
	}
	e.waitState(proj, id, stateCompleted, 5*time.Second)
	st, _ = e.Stats(ctx, proj, "")
	if len(st) != 1 || st[0].Completed1h != 1 || st[0].Throughput1m != 1 || st[0].FailureRate != 0 {
		t.Errorf("stats %+v", st)
	}
}

// A process killed mid-delivery leaves the job "running" and its limit slot
// held. The next owner fails that attempt and retries the job.
func TestCrashRecovery(t *testing.T) {
	dsn := newDB(t)
	a1 := startEngine(t, dsn, nil)
	app := newApp(t, a1.Engine, proj)
	a1.configure(proj, QueueConfig{Name: "k", URL: app.url("/k"), KeyConcurrency: 1})
	ctx := context.Background()
	// Freeze the first engine (no worker), then fake what a crash leaves.
	res := a1.send(proj, SendRequest{Name: "k", Key: "x", Delay: time.Hour})
	n, _ := ParseJobID(res.Jobs[0])
	if _, err := a1.pool.Exec(ctx, `UPDATE tq_jobs SET state = 'running', attempt = 1, total_attempts = 1, started_at = now(),
		lease_until = now() + interval '1 hour' WHERE id = $1`, n); err != nil {
		t.Fatal(err)
	}
	if _, err := a1.pool.Exec(ctx, `INSERT INTO tq_slots (slot, job_id) VALUES ('k:shop/k/x', $1)`, n); err != nil {
		t.Fatal(err)
	}
	keys := a1.cfg.Keys
	a1.Close()
	// A second engine on the same database: the single-owner lock was
	// released when the first one's connection closed.
	b := startEngine(t, dsn, func(c *Config) { c.Keys = keys })
	j := b.waitState(proj, res.Jobs[0], stateCompleted, 10*time.Second)
	if len(j.Attempts) != 2 || !strings.Contains(j.Attempts[0].Error, "restarted") {
		t.Errorf("attempts %+v", j.Attempts)
	}
}

func TestSingleOwner(t *testing.T) {
	dsn := newDB(t)
	a1 := startEngine(t, dsn, nil)
	cfg := a1.cfg
	e2, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	e2.Start(context.Background())
	defer e2.Close()
	select {
	case <-e2.Ready():
		t.Fatal("second engine started while the first holds the lock")
	case <-time.After(1500 * time.Millisecond):
	}
	a1.Close()
	select {
	case <-e2.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("second engine never took over")
	}
}

func TestCron(t *testing.T) {
	e := newEngine(t, func(c *Config) {})
	a := newApp(t, e.Engine, proj)
	ctx := context.Background()
	if err := e.ReconcileCron(ctx, proj, "nightly", json.RawMessage(`{"schedule":"0 3 * * *","app":"jobs","path":"/jobs/nightly"}`)); err != nil {
		t.Fatal(err)
	}
	cs, _ := e.Crons(ctx, proj)
	if len(cs) != 1 || cs[0].Target != "jobs:/jobs/nightly" || cs[0].NextAt.UTC().Hour() != 3 {
		t.Fatalf("crons %+v", cs)
	}
	_ = a
	if _, err := e.pool.Exec(ctx, `UPDATE tq_crons SET next_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "cron tick", func() bool {
		cs, _ = e.Crons(ctx, proj)
		return cs[0].LastJob != ""
	})
	if cs[0].NextAt.Before(time.Now()) {
		t.Error("next tick not advanced")
	}
	j := e.job(proj, cs[0].LastJob)
	if j.Kind != kindCron || j.Cron != "nightly" {
		t.Errorf("cron job %+v", j)
	}
	// Unchanged schedule keeps next_at; a changed one restarts.
	before := cs[0].NextAt
	_ = e.ReconcileCron(ctx, proj, "nightly", json.RawMessage(`{"schedule":"0 3 * * *","app":"jobs"}`))
	cs, _ = e.Crons(ctx, proj)
	if !cs[0].NextAt.Equal(before) || cs[0].Target != "jobs:/cron/nightly" {
		t.Errorf("reconcile moved next tick: %+v", cs[0])
	}
	id, err := e.TriggerCron(ctx, proj, "nightly", "test")
	if err != nil || id == "" {
		t.Fatal(err)
	}
	if err := e.ReconcileCron(ctx, proj, "nightly", nil); err != nil {
		t.Fatal(err)
	}
	if cs, _ = e.Crons(ctx, proj); len(cs) != 0 {
		t.Errorf("deleted cron still listed")
	}
	if _, err := e.TriggerCron(ctx, proj, "nightly", "test"); err == nil {
		t.Error("triggered a deleted cron")
	}
	if err := e.ReconcileCron(ctx, proj, "bad", json.RawMessage(`{"schedule":"every day","app":"jobs"}`)); err == nil {
		t.Error("bad schedule accepted")
	}
}

// sendTx: rows committed with the app's transaction become jobs; rolled
// back ones never do; a crash between enqueue and delete never duplicates.
func TestOutbox(t *testing.T) {
	e := newEngine(t, nil)
	ctx := context.Background()
	appDSN := newDB(t)
	pool, err := pgxpoolNew(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, OutboxDDL); err != nil {
		t.Fatal(err)
	}
	insert := func(commit bool, name string) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, outboxInsert, name, `{"order":1}`, `{"delaySeconds":3600,"key":"k"}`, "web"); err != nil {
			t.Fatal(err)
		}
		if commit {
			_ = tx.Commit(ctx)
		} else {
			_ = tx.Rollback(ctx)
		}
	}
	insert(true, "emails")
	insert(false, "emails")
	insert(true, "Not A Name")
	n, err := e.DrainOutbox(ctx, proj, pool)
	if err != nil || n != 2 {
		t.Fatalf("drained %d %v", n, err)
	}
	js, _ := e.ListJobs(ctx, proj, ListFilter{})
	if len(js) != 2 {
		t.Fatalf("jobs %+v", js)
	}
	var good, bad Job
	for _, j := range js {
		if j.State == stateDead {
			bad = j
		} else {
			good = j
		}
	}
	if good.Queue != "emails" || good.Key != "k" || good.State != stateScheduled || good.Target != "web:/queues/emails" || !strings.HasPrefix(good.Dedupe, "outbox:") {
		t.Errorf("job %+v", good)
	}
	if !strings.Contains(bad.LastError, "sendTx rejected") {
		t.Errorf("bad row %+v", bad)
	}
	// Crash after enqueue, before delete: the row comes back, dedupe holds.
	if _, err := pool.Exec(ctx, `INSERT INTO tiffin.outbox (uid, name, options, app) SELECT $1::uuid, 'emails', '{"delaySeconds":3600}', 'web'`,
		strings.TrimPrefix(good.Dedupe, "outbox:")); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.DrainOutbox(ctx, proj, pool); n != 1 {
		t.Fatalf("redrain %d", n)
	}
	if js, _ := e.ListJobs(ctx, proj, ListFilter{}); len(js) != 2 {
		t.Errorf("duplicate after redrain: %d jobs", len(js))
	}
}
