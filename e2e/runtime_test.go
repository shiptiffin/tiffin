//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestRuntime is the M2 acceptance test on a fresh box, through the CLI:
//
//	deploy hello-hono → HTTPS 200 → redeploy a change and roll back while
//	hey (keep-alive) and a curl loop (a new HTTP/1.1 or HTTP/2 connection per request) hammer the app (zero failed requests) → logs show
//	the requests → a secret restarts the app with the new env → a preview
//	sleeps and wakes → git push deploys → deleting the app stops it.
func TestRuntime(t *testing.T) {
	start := time.Now()
	phase := phaseLogger(t)
	b := newCLIBox(t, "rt", "hello")
	phase("up", start)

	// The template, copied so the test can edit it.
	src := filepath.Join(RepoRoot(), "templates", "hello-hono")
	app := filepath.Join(b.dir, "hello-hono")
	if out, err := exec.Command("rsync", "-a", "--exclude", "node_modules", src+"/", app+"/").CombinedOutput(); err != nil {
		t.Fatalf("copy template: %v %s", err, out)
	}
	applyDir := func(intent string) {
		t.Helper()
		plan := b.ok("plan", app)
		hash, _ := plan["hash"].(string)
		b.ok("apply", app, "--confirm", hash, "-m", intent)
	}
	applyDir("e2e: hello-hono")

	// ---- deploy → HTTPS ----
	p := time.Now()
	d1 := deploy(t, b, app)
	phase("first deploy", p)
	t.Logf("first deploy: build %.1fs, total %.1fs (cold: base images and Bun downloaded)", d1.BuildSecs, d1.TotalSecs)
	c := b.https()
	code, _, body := b.get(c, "GET", b.url("api")+"/", nil)
	if code != 200 || !strings.Contains(body, `"hello":"world"`) || !strings.Contains(body, d1.ID) {
		t.Fatalf("GET api: %d %s", code, body)
	}

	// ---- redeploy + rollback under load: zero failed requests ----
	p = time.Now()
	// The edge's per-IP rate limit (protect module, 300 requests/10s) would
	// answer this single-IP flood with 429s; measure the runtime, not the
	// limiter: lift the app limit for the load phase, then restore it.
	b.ok("protect", "set", "--body", `{"limits":{"app":{"requests":0,"windowSeconds":10}}}`)
	b.inBox(`sudo apt-get install -y -qq hey >/dev/null 2>&1 || true; command -v hey >/dev/null`)
	b.inBox(`sudo cp /var/lib/tiffin/platform/ca.crt /tmp/ca.crt && sudo chmod 644 /tmp/ca.crt
rm -f /tmp/hey.out /tmp/curl.out
nohup hey -z 50s -c 8 -host api.tiffin.localhost https://api.tiffin.localhost:8443/ > /tmp/hey.out 2>&1 &
nohup bash -c 'end=$((SECONDS+50)); while [ $SECONDS -lt $end ]; do for v in --http1.1 --http2; do curl $v -s -o /dev/null -w "%{http_code} %{errormsg}\n" --max-time 10 --cacert /tmp/ca.crt https://api.tiffin.localhost:8443/ | sed "s/^/$(date +%T.%N) /"; done; done > /tmp/curl.out' >/dev/null 2>&1 &
echo started`)
	time.Sleep(3 * time.Second)
	idx := filepath.Join(app, "index.ts")
	raw, _ := os.ReadFile(idx)
	if err := os.WriteFile(idx, []byte(strings.Replace(string(raw), `hello: "world"`, `hello: "v2"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	d2 := deploy(t, b, app)
	t.Logf("redeploy: build %.1fs, total %.1fs", d2.BuildSecs, d2.TotalSecs)
	if _, _, body := b.get(c, "GET", b.url("api")+"/", nil); !strings.Contains(body, `"hello":"v2"`) {
		t.Fatalf("after redeploy: %s", body)
	}
	time.Sleep(5 * time.Second)
	rb := time.Now()
	rolled := b.ok("rollback", "api", "--project", "hello")
	t.Logf("rollback took %s", time.Since(rb).Round(100*time.Millisecond))
	if rolled["id"] != d1.ID || rolled["status"] != "live" {
		t.Fatalf("rollback: %v", rolled)
	}
	if _, _, body := b.get(c, "GET", b.url("api")+"/", nil); !strings.Contains(body, `"hello":"world"`) {
		t.Fatalf("after rollback the previous version must serve: %s", body)
	}
	if got := deployStatus(t, b, d2.ID); got != "rolled_back" {
		t.Fatalf("v2 is %s, want rolled_back", got)
	}
	// Let the load finish.
	for i := 0; i < 90 && !strings.Contains(b.inBox("cat /tmp/hey.out"), "Status code distribution"); i++ {
		time.Sleep(time.Second)
	}
	time.Sleep(2 * time.Second)
	hey := b.inBox("cat /tmp/hey.out")
	curls := b.inBox("cut -d' ' -f2- /tmp/curl.out | sort | uniq -c")
	t.Logf("hey:\n%s\ncurl loop (new connection per request):\n%s", section(hey, "Summary:", "Response time histogram:")+section(hey, "Status code distribution:", ""), curls)
	if strings.Contains(hey, "Error distribution") || !regexp.MustCompile(`\[200\]\s+\d+ responses`).MatchString(hey) || regexp.MustCompile(`\[[13-9]\d\d\]`).MatchString(section(hey, "Status code distribution:", "")) {
		t.Fatalf("hey saw failures during deploy/rollback:\n%s", hey)
	}
	for _, line := range strings.Split(curls, "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[1] != "200" {
			t.Fatalf("curl loop saw failures:\n%s\nfailed requests:\n%s\nedge log:\n%s", curls,
				b.inBox(`grep -v " 200 $" /tmp/curl.out | head -20`),
				b.inBox(`sudo journalctl -u tiffin --since "-3 min" --no-pager | grep -iE "edge|caddy|route|tls|error" | tail -30`))
		}
	}
	b.ok("protect", "set", "--body", `{"limits":{"app":{"requests":300,"windowSeconds":10}}}`)
	phase("deploy+rollback under load", p)

	// ---- logs show requests ----
	p = time.Now()
	b.get(c, "GET", b.url("api")+"/e2e-logged", nil)
	time.Sleep(time.Second)
	logs := b.ok("logs", "api", "--project", "hello", "--since", "10m", "--limit", "2000")
	lines, _ := logs["lines"].([]any)
	var seen bool
	for _, l := range lines {
		if m, _ := l.(map[string]any); strings.Contains(fmt.Sprint(m["text"]), "GET /e2e-logged") {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("logs do not show the request (%d lines)", len(lines))
	}
	phase("logs", p)

	// ---- a secret restarts the app with the new env ----
	p = time.Now()
	b.ok("secrets", "set", "hello", "GREETING", "--value", "hi from a secret")
	waitBody(t, b, b.url("api")+"/", `"message":"hi from a secret"`, 60*time.Second)
	phase("secret restart", p)

	// ---- preview: deploy, sleep, wake ----
	p = time.Now()
	pv := deployArgs(t, b, app, "--preview", "pr-1")
	if code, _, body := b.get(c, "GET", b.url("pr-1--api")+"/", nil); code != 200 || !strings.Contains(body, pv.ID) {
		t.Fatalf("preview: %d %s", code, body)
	}
	if st := b.ok("previews", "sleep", "hello", "api", "pr-1"); st["sleeping"] != true {
		t.Fatalf("sleep: %v", st)
	}
	wake := time.Now()
	if code, _, body := b.get(c, "GET", b.url("pr-1--api")+"/", nil); code != 200 || !strings.Contains(body, pv.ID) {
		t.Fatalf("wake: %d %s", code, body)
	}
	t.Logf("sleeping preview answered its first request in %s", time.Since(wake).Round(10*time.Millisecond))
	phase("preview", p)

	// ---- git push deploys ----
	p = time.Now()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.email=e2e@tiffin", "-c", "user.name=e2e"}, args...)...)
		cmd.Dir, cmd.Env = app, b.env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q", "-b", "main")
	git("add", "-A")
	git("commit", "-qm", "e2e")
	cmd := exec.Command(b.cli, "git-remote", "--add", "--project", "hello")
	cmd.Dir, cmd.Env = app, b.env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git-remote: %v %s", err, out)
	}
	pushed := git("push", "tiffin", "main")
	if !strings.Contains(pushed, "tiffin: api is live") {
		t.Fatalf("git push did not deploy:\n%s", pushed)
	}
	if _, _, body := b.get(c, "GET", b.url("api")+"/", nil); !strings.Contains(body, `"hello":"v2"`) {
		t.Fatalf("after git push: %s", body)
	}
	phase("git push", p)

	// ---- a workflow sleeping across a redeploy finishes on its release ----
	p = time.Now()
	worker := filepath.Join(b.dir, "worker")
	if out, err := exec.Command("rsync", "-a", "--exclude", "node_modules", filepath.Join(RepoRoot(), "templates", "queues-worker")+"/", worker+"/").CombinedOutput(); err != nil {
		t.Fatalf("copy queues-worker: %v %s", err, out)
	}
	b.apply("jobs", `{"project":"jobs","apps":{"worker":{"role":"worker"}}}`)
	b.project = "jobs"
	b.waitReady("app/worker")
	b.project = "hello"
	w1 := deployArgs(t, b, worker, "--app", "worker")
	// The queue needs the platform Postgres; give it time on a busy small box.
	var run map[string]any
	for start := time.Now(); ; {
		code, out := b.run("workflows", "start", "jobs", "--workflow", "nap", "--app", "worker", "--body", `{"input":{"sleep":"40s"}}`)
		if code == 0 {
			_ = json.Unmarshal([]byte(out), &run)
			break
		}
		if time.Since(start) > 3*time.Minute {
			t.Fatalf("workflows start: exit %d\n%s\npostgres:\n%s", code, out,
				b.inBox(`systemctl --no-pager status 'tiffin-postgres*' 2>&1 | tail -20; free -m`))
		}
		time.Sleep(3 * time.Second)
	}
	runID, _ := run["id"].(string)
	if run["release"] != w1.ID {
		t.Fatalf("run not pinned to the current release %s: %v", w1.ID, run)
	}
	waitRun := func(state string, d time.Duration) map[string]any {
		t.Helper()
		deadline := time.Now().Add(d)
		for {
			r := b.ok("workflows", "runs", "get", "jobs", runID)
			if r["state"] == state {
				return r
			}
			if r["state"] == "failed" || time.Now().After(deadline) {
				t.Fatalf("run %s: wanted %s, got %v (%v)", runID, state, r["state"], r["error"])
			}
			time.Sleep(time.Second)
		}
	}
	waitRun("waiting", 2*time.Minute)
	w2 := deployArgs(t, b, worker, "--app", "worker")
	rt := b.ok("apps", "status", "jobs", "worker")
	prod, _ := rt["production"].(map[string]any)
	if dr, _ := prod["draining"].([]any); len(dr) != 1 {
		t.Fatalf("release %s must keep running for its pinned run: %v", w1.ID, prod)
	}
	r := waitRun("completed", 3*time.Minute)
	out, _ := r["output"].(map[string]any)
	before, _ := out["before"].(map[string]any)
	after, _ := out["after"].(map[string]any)
	if before["deploy"] != w1.ID || after["deploy"] != w1.ID {
		t.Fatalf("run must finish on release %s; before %v after %v (redeployed as %s)", w1.ID, before, after, w2.ID)
	}
	t.Logf("workflow slept across the deploy of %s and finished on its release %s", w2.ID, w1.ID)
	deadline := time.Now().Add(90 * time.Second)
	for {
		rt := b.ok("apps", "status", "jobs", "worker")
		prod, _ := rt["production"].(map[string]any)
		if dr, _ := prod["draining"].([]any); len(dr) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("old release still running after its run finished: %v", prod)
		}
		time.Sleep(2 * time.Second)
	}
	phase("pinned workflow", p)

	// ---- deleting the app stops it ----
	p = time.Now()
	cfg := filepath.Join(app, "tiffin.config.ts")
	if err := os.WriteFile(cfg, []byte(`import { defineConfig } from "tiffin-sdk";
export default defineConfig({ project: "hello" });
`), 0o644); err != nil {
		t.Fatal(err)
	}
	applyDir("e2e: delete the app")
	deadline = time.Now().Add(time.Minute)
	for {
		n := b.inBox(`sudo nerdctl -n tiffin ps -q --filter label=tiffin.project=hello | wc -l`)
		code, _, _ := b.get(c, "GET", b.url("api")+"/", nil)
		if n == "0" && code == 404 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("app not stopped: %s containers, HTTP %d", n, code)
		}
		time.Sleep(time.Second)
	}
	if imgs := b.inBox(`sudo nerdctl -n tiffin images -q | wc -l`); imgs == "0" {
		t.Fatal("images must be kept for undo")
	}
	phase("delete stops", p)
	t.Logf("TOTAL %s", time.Since(start).Round(time.Second))
}

type e2eDeploy struct {
	ID        string  `json:"id"`
	Status    string  `json:"status"`
	URL       string  `json:"url"`
	Error     string  `json:"error"`
	BuildSecs float64 `json:"buildSeconds"`
	TotalSecs float64 `json:"durationSeconds"`
}

func deploy(t *testing.T, b *cliBox, dir string) e2eDeploy { return deployArgs(t, b, dir) }

func deployArgs(t *testing.T, b *cliBox, dir string, extra ...string) e2eDeploy {
	t.Helper()
	code, out := b.run(append([]string{"deploy", dir}, extra...)...)
	var res struct {
		OK      bool              `json:"ok"`
		Deploys []e2eDeploy       `json:"deploys"`
		Checks  map[string]string `json:"checks"`
	}
	_ = json.Unmarshal([]byte(out), &res)
	if code != 0 || !res.OK || len(res.Deploys) != 1 || res.Deploys[0].Status != "live" {
		t.Fatalf("deploy %v: exit %d\n%s", extra, code, out)
	}
	d := res.Deploys[0]
	if d.URL != "" && res.Checks[d.ID] != "HTTP 200" {
		t.Fatalf("deploy check: %v", res.Checks)
	}
	return d
}

func deployStatus(t *testing.T, b *cliBox, id string) string {
	t.Helper()
	d := b.ok("deploys", "get", "hello", "api", id)
	s, _ := d["status"].(string)
	return s
}

func waitBody(t *testing.T, b *cliBox, u, want string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		_, _, body := b.get(b.https(), "GET", u, nil)
		if strings.Contains(body, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never served %q; last: %s", u, want, body)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// section returns the text from the line containing from up to (not
// including) the line containing to ("" = the end).
func section(s, from, to string) string {
	i := strings.Index(s, from)
	if i < 0 {
		return ""
	}
	s = s[i:]
	if to != "" {
		if j := strings.Index(s, to); j > 0 {
			s = s[:j]
		}
	}
	return s
}
