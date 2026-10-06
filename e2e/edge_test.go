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

// steadyApp answers every path with its version; the edge test deploys v1
// and then v2.
const steadyApp = `Bun.serve({
  port: Number(process.env.PORT ?? 3000),
  fetch(req) {
    return new Response("steady VERSION " + new URL(req.url).pathname + "\n");
  },
});
`

type loadResult struct {
	Requests     int      `json:"requests"`
	OK           int      `json:"ok"`
	Failed       int      `json:"failed"`
	Errors       []string `json:"errors"`
	MaxLatencyMs float64  `json:"maxLatencyMs"`
	MaxGapMs     float64  `json:"maxGapMs"`
	Seconds      float64  `json:"seconds"`
}

// TestEdgeProcess is the edge's acceptance test: the HTTPS edge (Caddy and
// the switchboard) runs as its own service, so under steady load on an app
// (8 workers in the box, half HTTP/1.1, half HTTP/2) not one request fails
// while the control plane (unit tiffin) is restarted, killed with SIGKILL,
// updated to a new build, stopped for a while, or restarted in the middle of
// a deploy, nor during a blue/green deploy. A restart of the edge itself
// hands its ports over through systemd's socket; its numbers are reported.
// While the control plane is down a sleeping app answers 503 with
// Retry-After, and wakes once it is back.
func TestEdgeProcess(t *testing.T) {
	start := time.Now()
	phase := phaseLogger(t)
	b := newCLIBox(t, "edge", "steady")
	healthy := healthyScript
	// Opted-in apps sleep after 20 s here (as in TestSleep).
	b.inBox(`sudo install -d /etc/systemd/system/tiffin.service.d
printf '[Service]\nEnvironment=TIFFIN_SLEEP_AFTER=20s\n' | sudo tee /etc/systemd/system/tiffin.service.d/e2e-sleep.conf >/dev/null
sudo systemctl daemon-reload && sudo systemctl restart tiffin
` + healthy + `
sudo cp /var/lib/tiffin/platform/ca.crt /tmp/ca.crt && sudo chmod 644 /tmp/ca.crt`)
	if got := b.inBox(`systemctl is-active tiffin tiffin-edge tiffin-edge.socket | tr '\n' ' '`); got != "active active active" {
		t.Fatalf("units: %q", got)
	}
	edgePID := func() string { return b.inBox(`systemctl show -p MainPID --value tiffin-edge`) }
	tiffinPID := func() string { return b.inBox(`systemctl show -p MainPID --value tiffin`) }

	// ---- the apps: steady (never sleeps) and naps (sleeps after 20 s) ----
	p := time.Now()
	app := func(name, version, cfg string) string {
		t.Helper()
		dir := filepath.Join(b.dir, name)
		_ = os.MkdirAll(dir, 0o755)
		for f, body := range map[string]string{
			"index.ts":         strings.ReplaceAll(steadyApp, "VERSION", version),
			"package.json":     `{"name":"` + name + `","private":true,"type":"module","scripts":{"start":"bun index.ts"}}`,
			"tiffin.config.ts": cfg,
		} {
			if err := os.WriteFile(filepath.Join(dir, f), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	steady := app("steady", "v1", `export default { project: "steady", apps: { web: { instances: 2, routes: ["steady"] } } };`+"\n")
	naps := app("naps", "v1", `export default { project: "naps", sleepAfter: "1h", apps: { web: { routes: ["naps"] } } };`+"\n")
	for _, d := range []string{steady, naps} {
		plan := b.ok("plan", d)
		b.ok("apply", d, "--confirm", plan["hash"].(string), "-m", "e2e edge")
		deploy(t, b, d)
	}
	phase("deploys", p)

	// ---- the load generator and the next build ----
	p = time.Now()
	installLoadgen(t, b)
	next := buildTiffin(t, b.dir, "linux", "0.0.2-edge")
	phase("tools", p)

	loads := &steadyLoads{t: t, b: b, phase: phase}
	load := loads.run
	noFailures := func(name string, r loadResult) {
		t.Helper()
		if r.Failed > 0 {
			t.Errorf("%s: %d of %d requests failed", name, r.Failed, r.Requests)
		}
	}
	keepsEdge := func(name, before string) {
		t.Helper()
		if after := edgePID(); after != before {
			t.Errorf("%s restarted the edge (pid %s → %s)", name, before, after)
		}
	}

	// ---- (0) baseline ----
	noFailures("baseline", load("baseline", func() { time.Sleep(5 * time.Second) }))

	// ---- (a) systemctl restart tiffin ----
	pid := edgePID()
	noFailures("restart tiffin", load("restart tiffin", func() {
		b.inBox("sudo systemctl restart tiffin\n" + healthy)
	}))
	keepsEdge("restart tiffin", pid)

	// ---- (b) kill -9 of tiffin's main process (systemd starts it again) ----
	noFailures("kill -9 tiffin", load("kill -9 tiffin", func() {
		old := tiffinPID()
		b.inBox("sudo kill -9 " + old)
		b.inBox(`for i in $(seq 1 100); do p=$(systemctl show -p MainPID --value tiffin); [ "$p" != 0 ] && [ "$p" != ` + old + ` ] && break; sleep 0.2; done
` + healthy)
	}))
	keepsEdge("kill -9 tiffin", pid)

	// ---- (c) an update to a new build: tiffin up --binary (copy, provision, self-update) ----
	noFailures("update (tiffin up)", load("update (tiffin up)", func() {
		b.ok("up", "--binary", next)
		b.inBox(healthy)
	}))
	if v := b.ok("health")["version"]; v != "0.0.2-edge" {
		t.Fatalf("after the update: version %v", v)
	}
	keepsEdge("update", pid)

	// ---- (e) a blue/green deploy under load ----
	noFailures("deploy v2", load("deploy v2", func() {
		_ = os.WriteFile(filepath.Join(steady, "index.ts"), []byte(strings.ReplaceAll(steadyApp, "VERSION", "v2")), 0o644)
		deploy(t, b, steady)
	}))
	if body := b.inBox(`curl -s --cacert /tmp/ca.crt https://steady.tiffin.localhost:8443/x`); !strings.Contains(body, "steady v2") {
		t.Fatalf("after the deploy: %q", body)
	}
	keepsEdge("deploy", pid)

	// ---- (a+e) tiffin restarted in the middle of a deploy ----
	noFailures("restart during deploy", load("restart during deploy", func() {
		_ = os.WriteFile(filepath.Join(steady, "index.ts"), []byte(strings.ReplaceAll(steadyApp, "VERSION", "v3")), 0o644)
		done := make(chan struct{})
		go func() { defer close(done); b.run("deploy", steady) }() // interrupted: it may fail
		deadline := time.Now().Add(3 * time.Minute)
		for {
			ds, _ := b.ok("deploys", "list", "steady", "web")["deploys"].([]any)
			if len(ds) > 0 {
				if d, _ := ds[0].(map[string]any); d["status"] == "building" || d["status"] == "starting" {
					break
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("the deploy never started: %v", ds)
			}
			time.Sleep(200 * time.Millisecond)
		}
		b.inBox("sudo systemctl restart tiffin\n" + healthy)
		<-done
	}))
	keepsEdge("restart during deploy", pid)
	if body := b.inBox(`curl -s --cacert /tmp/ca.crt https://steady.tiffin.localhost:8443/x`); !strings.Contains(body, "steady v") {
		t.Fatalf("after the interrupted deploy: %q", body)
	}

	// ---- the control plane stopped: apps keep serving, a sleeping app says 503 ----
	p = time.Now()
	deadline := time.Now().Add(2 * time.Minute)
	for b.ok("apps", "status", "naps", "web")["production"].(map[string]any)["sleeping"] != true {
		if time.Now().After(deadline) {
			t.Fatal("naps never fell asleep")
		}
		time.Sleep(2 * time.Second)
	}
	phase("naps asleep", p)
	noFailures("tiffin stopped 10s", load("tiffin stopped 10s", func() {
		b.inBox("sudo systemctl stop tiffin")
		out := b.inBox(`curl -s -o /dev/null -D - --cacert /tmp/ca.crt https://naps.tiffin.localhost:8443/ | tr -d '\r'`)
		if !strings.Contains(out, " 503") || !strings.Contains(strings.ToLower(out), "retry-after: 5") {
			t.Errorf("a sleeping app without the control plane: %q", out)
		}
		time.Sleep(10 * time.Second)
		b.inBox("sudo systemctl start tiffin\n" + healthy)
	}))
	keepsEdge("tiffin stopped", pid)
	if body := b.inBox(`curl -s --max-time 60 --cacert /tmp/ca.crt https://naps.tiffin.localhost:8443/hi`); !strings.Contains(body, "steady v1 /hi") {
		t.Fatalf("a sleeping app once the control plane is back: %q", body)
	}

	// ---- (d) systemctl restart tiffin-edge: the ports go through the socket ----
	r := load("restart tiffin-edge", func() {
		b.inBox("sudo systemctl restart tiffin-edge")
	})
	if after := edgePID(); after == pid {
		t.Errorf("the edge did not restart (pid %s)", pid)
	}
	if r.Failed > r.Requests/100 {
		t.Errorf("restart tiffin-edge: %d of %d requests failed", r.Failed, r.Requests)
	}
	for _, l := range loads.report {
		t.Logf("SUMMARY %s", l)
	}
	t.Logf("TOTAL %s", time.Since(start).Round(time.Second))
}

// healthyScript waits until tiffin answers health with ok.
const healthyScript = `up=; for i in $(seq 1 300); do curl -s http://127.0.0.1:7070/v1/health | grep -q '"status":"ok"' && { up=1; break; }; sleep 0.2; done
[ -n "$up" ] || { echo "tiffin not healthy" >&2; exit 1; }`

// installLoadgen builds e2e/loadgen for the box, copies it to /tmp/loadgen
// and turns the per-IP app limit off: one client IP sends ~1,000 requests a
// second.
func installLoadgen(t *testing.T, b *cliBox) {
	t.Helper()
	gen := filepath.Join(b.dir, "loadgen")
	build := exec.Command("go", "build", "-tags", "e2e", "-o", gen, "./e2e/loadgen")
	build.Dir, build.Env = RepoRoot(), append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+HostArch())
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build loadgen: %v\n%s", err, out)
	}
	if out, err := exec.Command("limactl", "copy", gen, b.instance+":/tmp/loadgen").CombinedOutput(); err != nil {
		t.Fatalf("copy loadgen: %v\n%s", err, out)
	}
	b.inBox("chmod 755 /tmp/loadgen")
	b.ok("protect", "set", "--body", `{"limits":{"app":{"requests":0,"windowSeconds":10}}}`)
}

// steadyLoads runs the load generator in the box against steady.<domain>
// around a disruption and keeps a line per run for the summary.
type steadyLoads struct {
	t      *testing.T
	b      *cliBox
	phase  func(string, time.Time)
	report []string
}

func (l *steadyLoads) run(name string, disrupt func()) loadResult {
	t, b := l.t, l.b
	t.Helper()
	p := time.Now()
	id := strings.NewReplacer(" ", "-", "(", "", ")", "", "+", "").Replace(name)
	b.inBox(fmt.Sprintf(`sudo rm -f /tmp/load-%[1]s.*
sudo systemd-run --unit e2e-load-%[1]s /tmp/loadgen -url https://steady.tiffin.localhost:8443/ -ca /tmp/ca.crt -want steady -stop /tmp/load-%[1]s.stop -out /tmp/load-%[1]s.json >/dev/null 2>&1`, id))
	time.Sleep(3 * time.Second)
	disrupt()
	time.Sleep(5 * time.Second)
	b.inBox("sudo touch /tmp/load-" + id + ".stop")
	raw := b.inBox(`for i in $(seq 1 300); do [ -s /tmp/load-` + id + `.json ] && break; sleep 0.2; done
cat /tmp/load-` + id + `.json 2>/dev/null || { systemctl status --no-pager e2e-load-` + id + `; sudo journalctl --no-pager -u e2e-load-` + id + ` | tail -20; } 2>&1`)
	var r loadResult
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("%s: load result: %v\n%s", name, err, raw)
	}
	line := fmt.Sprintf("%-28s %6d requests  %3d failed  max latency %7.1f ms  max gap %7.1f ms  (%4.1fs)", name, r.Requests, r.Failed, r.MaxLatencyMs, r.MaxGapMs, r.Seconds)
	l.report = append(l.report, line)
	t.Logf("LOAD %s", line)
	for _, e := range r.Errors {
		t.Logf("  %s: %s", name, e)
	}
	if r.OK == 0 {
		t.Fatalf("%s: no request succeeded", name)
	}
	l.phase(name, p)
	return r
}
