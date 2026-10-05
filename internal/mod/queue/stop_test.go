package queue

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// A stopped project's jobs wait queued (no attempts spent, other projects
// unaffected) and its crons do not fire; starting it delivers the jobs and
// the crons continue from their next tick.
func TestStoppedProjectHoldsJobsAndCrons(t *testing.T) {
	e := newEngine(t, nil)
	a := newApp(t, e.Engine, proj)
	other := newApp(t, e.Engine, "other")
	ctx := context.Background()
	e.configure(proj, QueueConfig{Name: "q", URL: a.url("/q"), MaxAttempts: 1})
	e.configure("other", QueueConfig{Name: "q", URL: other.url("/q")})
	if err := e.ReconcileCron(ctx, proj, "nightly", json.RawMessage(`{"schedule":"0 3 * * *","app":"jobs"}`)); err != nil {
		t.Fatal(err)
	}
	for range 2 { // idempotent: the runtime calls it on every reconcile
		if err := e.SetProjectStopped(ctx, proj, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.pool.Exec(ctx, `UPDATE tq_crons SET next_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	id := e.send(proj, SendRequest{Name: "q"}).Jobs[0]
	e.waitState("other", e.send("other", SendRequest{Name: "q"}).Jobs[0], stateCompleted, 5*time.Second)
	eventually(t, 5*time.Second, "job held by the stop", func() bool {
		return e.job(proj, id).WaitingFor == "the project is stopped"
	})
	time.Sleep(1500 * time.Millisecond) // the cron loop ticks every second
	j := e.job(proj, id)
	if len(a.deliveries()) != 0 || j.State != stateQueued || j.Attempt != 0 {
		t.Fatalf("stopped project delivered: %d deliveries, job %+v", len(a.deliveries()), j)
	}
	if cs, _ := e.Crons(ctx, proj); cs[0].LastJob != "" {
		t.Fatalf("stopped project's cron fired: %+v", cs[0])
	}
	if _, err := e.RetryJob(ctx, proj, mustID(t, id)); err == nil {
		t.Error("retried a job held by the stop")
	}

	if err := e.SetProjectStopped(ctx, proj, false); err != nil {
		t.Fatal(err)
	}
	e.waitState(proj, id, stateCompleted, 5*time.Second)
	cs, _ := e.Crons(ctx, proj)
	if cs[0].LastJob != "" || !cs[0].NextAt.After(time.Now()) {
		t.Errorf("cron after start: %+v (a tick inside the stop is skipped, the next one is ahead)", cs[0])
	}
}

// An attempt that fails because the project was stopped while it ran does
// not count: the job waits and runs again once the project starts.
func TestStopDuringAttemptDoesNotCount(t *testing.T) {
	e := newEngine(t, nil)
	a := newApp(t, e.Engine, proj)
	ctx := context.Background()
	release := make(chan int)
	a.handle("/q", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(<-release) })
	e.configure(proj, QueueConfig{Name: "q", URL: a.url("/q"), MaxAttempts: 1})
	id := e.send(proj, SendRequest{Name: "q"}).Jobs[0]
	e.waitState(proj, id, stateRunning, 5*time.Second)
	if err := e.SetProjectStopped(ctx, proj, true); err != nil {
		t.Fatal(err)
	}
	release <- http.StatusBadGateway // the app went down under it
	eventually(t, 5*time.Second, "job held by the stop", func() bool {
		return e.job(proj, id).WaitingFor == "the project is stopped"
	})
	if j := e.job(proj, id); j.State != stateQueued || j.Attempt != 0 || j.Attempts[0].Outcome != outcomeInterrupted {
		t.Fatalf("job after a stop mid-attempt: %+v", j)
	}
	if err := e.SetProjectStopped(ctx, proj, false); err != nil {
		t.Fatal(err)
	}
	release <- http.StatusOK
	if j := e.waitState(proj, id, stateCompleted, 5*time.Second); j.Attempt != 1 {
		t.Errorf("attempts after start: %+v", j)
	}
}

func mustID(t *testing.T, id string) int64 {
	t.Helper()
	n, err := ParseJobID(id)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
