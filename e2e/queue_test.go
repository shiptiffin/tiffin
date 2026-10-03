//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestQueue is M7's acceptance on a fresh box: the queues-worker template is
// deployed as a worker app and driven through the CLI.
//
//   - per-key concurrency holds under load
//   - kill -9 the app mid-job: the attempt fails and the job is retried
//   - kill -9 tiffin: running and queued jobs are recovered, nothing is lost
//   - cron fires on schedule
//   - a workflow sleeping across a redeploy completes
func TestQueue(t *testing.T) {
	b := newCLIBox(t, "queue", "jobs")
	src := filepath.Join(RepoRoot(), "templates", "queues-worker")
	app := filepath.Join(b.dir, "worker")
	if out, err := exec.Command("cp", "-R", src, app).CombinedOutput(); err != nil {
		t.Fatalf("copy template: %v\n%s", err, out)
	}
	_ = os.RemoveAll(filepath.Join(app, "node_modules"))
	b.apply("jobs", `{"project":"jobs","apps":{"worker":{"role":"worker"}},"crons":{"tick":{"schedule":"* * * * *","app":"worker","path":"/cron/tick"}}}`)
	b.waitReady("app/worker", "cron/tick")
	deploy := func() {
		t.Helper()
		cmd := exec.Command(b.cli, "deploy", app)
		cmd.Env, cmd.Dir = b.env, app
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("deploy: %v\n%s", err, out)
		}
	}
	deploy()
	b.ok("queue", "configure", "jobs", "work", "--app", "worker", "--key-concurrency", "2", "--lease-seconds", "10", "--max-attempts", "8")

	send := func(body string) string {
		t.Helper()
		res := b.ok("queue", "send", "jobs", "--body", body)
		jobs, _ := res["jobs"].([]any)
		if len(jobs) != 1 {
			t.Fatalf("send: %v", res)
		}
		return jobs[0].(string)
	}
	job := func(id string) map[string]any { return b.ok("queue", "jobs", "get", "jobs", id) }
	waitJob := func(id, state string, d time.Duration) map[string]any {
		t.Helper()
		var j map[string]any
		deadline := time.Now().Add(d)
		for time.Now().Before(deadline) {
			j = job(id)
			if j["state"] == state {
				return j
			}
			if j["state"] == "dead" && state != "dead" {
				t.Fatalf("%s died: %v", id, j["lastError"])
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("%s: wanted %s, is %v (%v)", id, state, j["state"], j["lastError"])
		return nil
	}

	// 1. Per-key concurrency under load: 60 jobs, 3 keys, at most 2 per key.
	start := time.Now()
	var ids []string
	for i := 0; i < 60; i++ {
		ids = append(ids, send(fmt.Sprintf(`{"name":"work","key":"tenant-%d","payload":{"ms":400}}`, i%3)))
	}
	peak := 0
	for _, id := range ids {
		j := waitJob(id, "completed", 3*time.Minute)
		out, _ := j["output"].(map[string]any)
		if n, _ := out["concurrentForKey"].(float64); int(n) > peak {
			peak = int(n)
		}
	}
	if peak > 2 {
		t.Errorf("a key ran %d jobs at once (keyConcurrency 2)", peak)
	}
	stats := b.list("queue", "stats", "jobs", "--queue", "work")
	t.Logf("60 jobs × 400ms over 3 keys (keyConcurrency 2): %s, peak per key %d, stats %v", time.Since(start).Round(time.Second), peak, stats[0])

	// 2. kill -9 the app mid-job: the attempt fails, the job is retried and completes.
	id := send(`{"name":"work","payload":{"ms":15000}}`)
	waitJob(id, "running", time.Minute)
	time.Sleep(2 * time.Second)
	b.inBox(`sudo pkill -9 -f '[b]un.*index.ts'`)
	j := waitJob(id, "completed", 3*time.Minute)
	atts, _ := j["attempts"].([]any)
	if len(atts) < 2 {
		t.Errorf("killed app: attempts %v", atts)
	} else {
		t.Logf("app killed mid-job: attempt 1 %v, then completed on attempt %d", atts[0].(map[string]any)["error"], len(atts))
	}

	// 3. kill -9 tiffin with one job running and 20 queued: nothing is lost.
	long := send(`{"name":"work","payload":{"ms":8000}}`)
	waitJob(long, "running", time.Minute)
	var burst []string
	for i := 0; i < 20; i++ {
		burst = append(burst, send(fmt.Sprintf(`{"name":"work","key":"burst-%d","payload":{"ms":200}}`, i%4)))
	}
	b.inBox(`sudo systemctl kill -s KILL tiffin`)
	time.Sleep(3 * time.Second)
	j = waitJob(long, "completed", 3*time.Minute)
	atts, _ = j["attempts"].([]any)
	restarted := false
	for _, a := range atts {
		restarted = restarted || strings.Contains(fmt.Sprint(a.(map[string]any)["error"]), "restarted")
	}
	if !restarted {
		t.Errorf("tiffin killed mid-job: attempts %v", atts)
	}
	for _, id := range burst {
		waitJob(id, "completed", 3*time.Minute)
	}
	t.Logf("tiffin killed with 1 running + 20 queued jobs: all 21 completed")

	// 4. Cron: "* * * * *" ticks every minute.
	deadline := time.Now().Add(150 * time.Second)
	for {
		cs := b.list("queue", "crons", "list", "jobs")
		if len(cs) == 1 && cs[0]["lastState"] == "completed" {
			t.Logf("cron tick: job %v completed, next %v", cs[0]["lastJob"], cs[0]["nextAt"])
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cron never completed: %v", cs)
		}
		time.Sleep(3 * time.Second)
	}

	// 5. A workflow sleeping across a redeploy completes.
	run := b.ok("workflows", "start", "jobs", "--workflow", "nap", "--app", "worker", "--body", `{"input":{"sleep":"45s"}}`)
	runID, _ := run["id"].(string)
	pinned, _ := run["release"].(string)
	var r map[string]any
	waitRun := func(state string, d time.Duration) {
		t.Helper()
		deadline := time.Now().Add(d)
		for time.Now().Before(deadline) {
			r = b.ok("workflows", "runs", "get", "jobs", runID)
			if r["state"] == state {
				return
			}
			if r["state"] == "failed" {
				t.Fatalf("run failed: %v", r["error"])
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("run %s: wanted %s, is %v", runID, state, r["state"])
	}
	waitRun("waiting", 2*time.Minute)
	deploy()
	waitRun("completed", 4*time.Minute)
	out, _ := r["output"].(map[string]any)
	before, _ := out["before"].(map[string]any)
	after, _ := out["after"].(map[string]any)
	tl, _ := json.Marshal(r["timeline"])
	switch {
	case before["deploy"] == after["deploy"]:
		t.Logf("workflow slept across a redeploy and finished on its pinned release %s (deploy %v)", pinned, after["deploy"])
	case strings.Contains(string(tl), "moved to release"):
		t.Logf("workflow finished after the redeploy; the runtime stopped release %s, so the run moved to %v (timeline records it)", pinned, after["deploy"])
	default:
		t.Errorf("run changed release without a timeline entry: before %v after %v", before, after)
	}
}
