//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// napApp is a small Bun server: it answers every path, /tick included (the
// cron's).
const napApp = `Bun.serve({
  port: Number(process.env.PORT ?? 3000),
  fetch(req) {
    return new Response("nap " + new URL(req.url).pathname + "\n");
  },
});
`

// TestSleep is the sleeping-apps acceptance test on a fresh box, with every
// opted-in project's idle time cut to 20 s (TIFFIN_SLEEP_AFTER):
//
//	naps (sleepAfter "1h") runs a Hello World Next.js app, a small Bun app
//	and a Bun worker that a cron calls every minute; awake runs the same Bun
//	app without sleepAfter → naps' web apps fall asleep and their memory is
//	freed → a request wakes each one (cold start: request held → first
//	byte, measured in the box) → Wake now starts a sleeping app → the cron's
//	next delivery wakes the sleeping worker → awake never sleeps.
func TestSleep(t *testing.T) {
	start := time.Now()
	phase := phaseLogger(t)
	b := newCLIBox(t, "sleep", "naps")
	b.inBox(`sudo install -d /etc/systemd/system/tiffin.service.d
printf '[Service]\nEnvironment=TIFFIN_SLEEP_AFTER=20s\n' | sudo tee /etc/systemd/system/tiffin.service.d/e2e-sleep.conf >/dev/null
sudo systemctl daemon-reload && sudo systemctl restart tiffin
for i in $(seq 1 100); do curl -sf http://127.0.0.1:7070/v1/health >/dev/null && break; sleep 0.3; done
sudo cp /var/lib/tiffin/platform/ca.crt /tmp/ca.crt && sudo chmod 644 /tmp/ca.crt`)
	phase("up", start)

	// ---- naps: Next.js, Bun, a Bun worker on a cron; awake: Bun ----
	p := time.Now()
	naps := filepath.Join(b.dir, "naps")
	web := filepath.Join(naps, "web")
	if out, err := exec.Command("rsync", "-a", "--exclude", "node_modules", "--exclude", ".next", "--exclude", "tiffin.config.ts",
		filepath.Join(RepoRoot(), "templates", "hello-next")+"/", web+"/").CombinedOutput(); err != nil {
		t.Fatalf("copy template: %v %s", err, out)
	}
	bunApp := func(dir string) {
		t.Helper()
		_ = os.MkdirAll(dir, 0o755)
		for name, body := range map[string]string{"index.ts": napApp, "package.json": `{"name":"nap","private":true,"type":"module","scripts":{"start":"bun index.ts"}}`} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	bunApp(filepath.Join(naps, "api"))
	cfg := `export default {
  project: "naps",
  sleepAfter: "1h",
  apps: {
    web: { path: "web", framework: "next", routes: ["naps"], healthcheck: "/api/health" },
    api: { path: "api", routes: ["naps-api"] },
    jobs: { path: "api", role: "worker" },
  },
  crons: { tick: { schedule: "* * * * *", app: "jobs", path: "/tick" } },
};
`
	if err := os.WriteFile(filepath.Join(naps, "tiffin.config.ts"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	awake := filepath.Join(b.dir, "awake")
	bunApp(awake)
	if err := os.WriteFile(filepath.Join(awake, "tiffin.config.ts"), []byte(`export default { project: "awake", apps: { api: { routes: ["awake"] } } };`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{naps, awake} {
		plan := b.ok("plan", d)
		b.ok("apply", d, "--confirm", plan["hash"].(string), "-m", "e2e sleep")
	}
	for _, app := range []string{"web", "api", "jobs"} {
		d := deployArgs(t, b, naps, "--app", app)
		t.Logf("naps/%s: build %.1fs, total %.1fs", app, d.BuildSecs, d.TotalSecs)
	}
	deploy(t, b, awake)
	awakeSince := time.Now()
	phase("deploys", p)

	env := func(project, app string) map[string]any {
		t.Helper()
		pr, _ := b.ok("apps", "status", project, app)["production"].(map[string]any)
		return pr
	}
	asleep := func(project, app string) bool { return env(project, app)["sleeping"] == true }
	waitFor := func(what string, d time.Duration, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(d)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("%s: not after %s", what, d)
			}
			time.Sleep(2 * time.Second)
		}
	}
	memMB := func(project string) (total float64, apps map[string][2]float64) {
		t.Helper()
		u := b.ok("projects", "usage", project)
		// Held by its processes: what the slice uses less the page cache the kernel drops when it needs to.
		m := u["memory"].(map[string]any)
		cache, _ := m["cacheBytes"].(float64)
		total = (m["usedBytes"].(float64) - cache) / (1 << 20)
		apps = map[string][2]float64{}
		list, _ := u["apps"].([]any)
		for _, x := range list {
			a := x.(map[string]any)
			if a["preview"] == nil || a["preview"] == "" {
				apps[a["app"].(string)+" "+fmt.Sprint(a["state"])] = [2]float64{a["instances"].(float64), a["memoryBytes"].(float64) / (1 << 20)}
			}
		}
		return total, apps
	}
	before, beforeApps := memMB("naps")
	t.Logf("naps awake: %.0f MB held %v", before, beforeApps)

	// ---- unused for 20 s: asleep, memory freed ----
	p = time.Now()
	waitFor("naps web and api asleep", 2*time.Minute, func() bool { return asleep("naps", "web") && asleep("naps", "api") })
	phase("fall asleep", p)
	after, afterApps := memMB("naps")
	t.Logf("naps asleep: %.0f MB held %v", after, afterApps)
	for _, app := range []string{"web", "api"} {
		if a, ok := afterApps[app+" asleep"]; !ok || a[0] != 0 || a[1] != 0 {
			t.Fatalf("%s asleep should run no copy and use no memory: %v", app, afterApps)
		}
	}
	if after > before/2 {
		t.Fatalf("memory not freed: %.0f MB awake, %.0f MB asleep", before, after)
	}
	if ps := b.inBox(`sudo /usr/local/bin/nerdctl --namespace tiffin ps --format '{{.Names}}' 2>/dev/null | grep -E '^tf\.naps\.(web|api)\.' || true`); ps != "" {
		t.Fatalf("containers still running while asleep:\n%s", ps)
	}

	// ---- a request wakes each one: cold start, then warm ----
	curl := func(u string) (code int, firstByte float64) {
		t.Helper()
		out := b.inBox(`curl -s -o /dev/null --max-time 120 -w '%{http_code} %{time_starttransfer}' --cacert /tmp/ca.crt ` + u)
		f := strings.Fields(out)
		if len(f) != 2 {
			t.Fatalf("curl %s: %q", u, out)
		}
		code, _ = strconv.Atoi(f[0])
		firstByte, _ = strconv.ParseFloat(f[1], 64)
		return code, firstByte
	}
	for _, c := range []struct{ app, name, url string }{
		{"api", "Bun", "https://naps-api.tiffin.localhost:8443/"},
		{"web", "Next.js (Hello World)", "https://naps.tiffin.localhost:8443/"},
	} {
		waitFor("naps "+c.app+" asleep", 2*time.Minute, func() bool { return asleep("naps", c.app) })
		code, cold := curl(c.url)
		if code != 200 {
			t.Fatalf("%s woken by a request: HTTP %d", c.app, code)
		}
		_, warm := curl(c.url)
		e := env("naps", c.app)
		w, _ := e["lastWake"].(map[string]any)
		if e["sleeping"] == true || w == nil || w["trigger"] != "request" {
			t.Fatalf("%s after a request: %v", c.app, e)
		}
		t.Logf("COLD START %s: %.2fs to first byte (curl in the box; box measured %.2fs, of which %.2fs starting the container), warm %.3fs",
			c.name, cold, w["firstByteSeconds"], w["startSeconds"], warm)
	}
	phase("wake on request", p)

	// ---- Wake now ----
	p = time.Now()
	waitFor("naps api asleep", 2*time.Minute, func() bool { return asleep("naps", "api") })
	woke := b.list("projects", "wake", "naps", "--app", "api")
	if len(woke) != 1 || woke[0]["woke"] != true || asleep("naps", "api") {
		t.Fatalf("wake now: %v", woke)
	}
	t.Logf("wake now: %v", woke[0]["wake"])
	phase("wake now", p)

	// ---- the cron's next delivery wakes the sleeping worker ----
	p = time.Now()
	waitFor("naps jobs asleep", 2*time.Minute, func() bool { return asleep("naps", "jobs") })
	slept := time.Now()
	waitFor("naps jobs woken by its cron", 2*time.Minute, func() bool {
		e := env("naps", "jobs")
		w, _ := e["lastWake"].(map[string]any)
		at, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(w["at"]))
		return e["sleeping"] != true && w["trigger"] == "delivery" && at.After(slept.Add(-5*time.Second))
	})
	waitFor("the cron tick completed", time.Minute, func() bool {
		cs := b.list("queue", "crons", "list", "naps")
		return len(cs) == 1 && cs[0]["lastState"] == "completed"
	})
	w := env("naps", "jobs")["lastWake"].(map[string]any)
	t.Logf("cron delivery woke the worker: started in %.2fs, then the tick completed", w["startSeconds"])
	phase("wake on cron", p)

	// ---- a project without sleepAfter never sleeps ----
	if e := env("awake", "api"); e["sleeping"] == true || len(e["instances"].([]any)) != 1 {
		t.Fatalf("awake slept: %v", e)
	}
	if code, _ := curl("https://awake.tiffin.localhost:8443/"); code != 200 {
		t.Fatalf("awake: HTTP %d", code)
	}
	t.Logf("awake: never slept, unused for %s", time.Since(awakeSince).Round(time.Second))
}
