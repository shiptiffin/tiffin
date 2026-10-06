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

// TestWorkflowDevKit deploys e2e/workflow-devkit, a Next.js app that uses
// Vercel's Workflow DevKit as it would on Vercel (withWorkflow, "use
// workflow", sleep, FatalError), to a project with Postgres:
//
//	the box runs it on @workflow/world-postgres → the queue routes are not
//	public → a FatalError fails its run after one attempt → a run sleeps
//	across a redeploy and finishes on the new release, each step once.
func TestWorkflowDevKit(t *testing.T) {
	start := time.Now()
	phase := phaseLogger(t)
	b := newCLIBox(t, "devkit", "devkit")
	phase("up", start)

	app := filepath.Join(b.dir, "devkit")
	if out, err := exec.Command("rsync", "-a", "--exclude", "node_modules", "--exclude", ".next",
		filepath.Join(RepoRoot(), "e2e", "workflow-devkit")+"/", app+"/").CombinedOutput(); err != nil {
		t.Fatalf("copy app: %v %s", err, out)
	}
	config := `import { defineConfig } from "@shiptiffin/sdk";

export default defineConfig({
  project: "devkit",
  apps: { web: { framework: "next", instances: 2, memoryMB: 512 } },
  services: { postgres: {} },
});
`
	if err := os.WriteFile(filepath.Join(app, "tiffin.config.ts"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := b.ok("plan", app)
	hash, _ := plan["hash"].(string)
	b.ok("apply", app, "--confirm", hash, "-m", "e2e: workflow devkit")
	b.waitReady("app/web", "service/postgres")

	p := time.Now()
	d1 := deployArgs(t, b, app)
	phase("first deploy", p)
	log1 := fmt.Sprint(b.ok("deploys", "build-log", "devkit", "web", d1.ID)["text"])
	t.Logf("build log (Tiffin lines):\n%s", grepLines(log1, "==> Workflow DevKit", "==> Next.js", "world-postgres@"))
	if !strings.Contains(log1, "runs on the project's Postgres (@workflow/world-postgres 4.3.9)") {
		t.Fatal("the build did not set up the Postgres world")
	}
	c := b.https()
	site := b.url("devkit")

	// ---- the queue routes run code for whoever calls them: not public ----
	for _, route := range []string{"flow", "step"} {
		if code, _, body := b.get(c, "POST", site+"/.well-known/workflow/v1/"+route, strings.NewReader("{}")); code != 404 || !strings.Contains(body, "404 page not found") {
			t.Fatalf("public POST to the %s route: %d %s", route, code, head(body))
		}
	}

	post := func(body string) string {
		t.Helper()
		code, _, out := b.get(c, "POST", site+"/api/runs", strings.NewReader(body))
		var r struct{ RunID string }
		_ = json.Unmarshal([]byte(out), &r)
		if code != 200 || r.RunID == "" {
			t.Fatalf("start %s: %d %s", body, code, head(out))
		}
		return r.RunID
	}
	type stepRow struct{ Step, Release string }
	steps := func(tag string) []stepRow {
		t.Helper()
		_, _, out := b.get(c, "GET", site+"/api/steps?tag="+tag, nil)
		var rows []stepRow
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("steps: %s", head(out))
		}
		return rows
	}
	type runState struct {
		Status string
		Value  []string
	}
	run := func(id string) runState {
		t.Helper()
		_, _, out := b.get(c, "GET", site+"/api/runs?id="+id, nil)
		var r runState
		_ = json.Unmarshal([]byte(out), &r)
		return r
	}
	logs := func() string {
		return b.inBox(`sudo sh -c 'tail -n 40 $(ls -t /var/lib/tiffin/logs/apps/devkit/web/prod/*.log | head -2)'`)
	}
	waitRun := func(id, status string, d time.Duration) runState {
		t.Helper()
		deadline := time.Now().Add(d)
		for {
			r := run(id)
			if r.Status == status {
				return r
			}
			if time.Now().After(deadline) {
				t.Fatalf("run %s is %q, want %q\n%s", id, r.Status, status, logs())
			}
			time.Sleep(2 * time.Second)
		}
	}

	// ---- FatalError: the run fails, the step is not retried ----
	fatal := post(`{"kind":"fatal","tag":"fatal"}`)
	waitRun(fatal, "failed", 2*time.Minute)
	time.Sleep(5 * time.Second) // a retry would have run by now
	if rows := steps("fatal"); len(rows) != 1 {
		t.Fatalf("the failing step ran %d times, want once: %v", len(rows), rows)
	}

	// ---- a durable sleep across a redeploy ----
	// The sleep outlasts the second build, the switch and the old release's
	// shutdown, so step two can only run on the new release.
	const wait = 4 * time.Minute
	slept := time.Now()
	id := post(fmt.Sprintf(`{"tag":"sleep","wait":"%ds"}`, int(wait.Seconds())))
	for i := 0; len(steps("sleep")) == 0; i++ {
		if i > 60 {
			t.Fatalf("step one never ran\n%s", logs())
		}
		time.Sleep(time.Second)
	}
	p = time.Now()
	d2 := deployArgs(t, b, app)
	phase("redeploy", p)
	if rows := steps("sleep"); len(rows) != 1 || run(id).Status != "running" {
		t.Fatalf("the sleep ended before the redeploy was live (%s after it began); lengthen it: %v", time.Since(slept).Round(time.Second), rows)
	}
	r := waitRun(id, "completed", wait+2*time.Minute)
	phase("sleep done", slept)
	rows := steps("sleep")
	want := []stepRow{{"one", d1.ID}, {"two", d2.ID}}
	if fmt.Sprint(rows) != fmt.Sprint(want) || fmt.Sprint(r.Value) != fmt.Sprint([]string{"one@" + d1.ID, "two@" + d2.ID}) {
		t.Fatalf("steps %v (want %v), value %v", rows, want, r.Value)
	}

	// The DevKit's own CLI reads the runs from the box's database.
	st := b.ok("apps", "status", "devkit", "web")
	prod, _ := st["production"].(map[string]any)
	ins, _ := prod["instances"].([]any)
	in, _ := ins[0].(map[string]any)
	out := b.inBox(fmt.Sprintf(`sudo /usr/local/bin/nerdctl --namespace tiffin exec %s sh -c 'cd /app && WORKFLOW_POSTGRES_URL=$DIRECT_DATABASE_URL node_modules/.bin/workflow inspect runs --backend $WORKFLOW_TARGET_WORLD 2>&1 | tail -n 8' || true`, in["name"]))
	t.Logf("workflow inspect runs, in the instance:\n%s", out)
}
