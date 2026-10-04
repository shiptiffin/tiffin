//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// hogApp uses memory and CPU on request: /hog?mb=N holds N more MB,
// /free lets go, /burn?ms=&n= runs n busy processes in the container.
const hogApp = `const held: Uint8Array[] = [];
Bun.serve({
  port: Number(process.env.PORT ?? 3000),
  fetch(req) {
    const u = new URL(req.url);
    if (u.pathname === "/hog") {
      const mb = Number(u.searchParams.get("mb") ?? "64");
      for (let i = 0; i < mb; i++) { const b = new Uint8Array(1 << 20); b.fill(7); held.push(b); }
      return new Response("holding " + held.length + " MB\n");
    }
    if (u.pathname === "/free") { held.length = 0; Bun.gc(true); return new Response("freed\n"); }
    if (u.pathname === "/burn") {
      const ms = Number(u.searchParams.get("ms") ?? "10000"), n = Number(u.searchParams.get("n") ?? "1");
      for (let i = 0; i < n; i++) {
        Bun.spawn([process.execPath, "-e", "const e = Date.now() + " + ms + "; let x = 0; while (Date.now() < e) x++;"], { stdout: "ignore", stderr: "ignore" });
      }
      return new Response("burning\n");
    }
    return new Response("ok " + process.env.TIFFIN_PROJECT + " holding " + held.length + " MB\n");
  },
});
`

// TestBudget is the project-budgets acceptance test on a fresh 3 GB box:
//
//	guestbook (memoryMB 256, cpus 0.5) and two automatic projects, shop and
//	blog → usage reports budgets and limits → a budget too big for the box
//	is refused at plan time → guestbook's memory hog is OOM-killed inside
//	its slice (pressure "oom") while shop keeps serving → its CPU is held to
//	half a core → shop alone bursts to both cores, shop and blog together
//	get one each → shop bursts past its fair share of memory, then blog
//	takes its share and nobody is killed (shop is swapped out) → a budget
//	change applies live with zero failed requests and no restart → the box
//	default share caps automatic projects.
func TestBudget(t *testing.T) {
	start := time.Now()
	phase := phaseLogger(t)
	b := newCLIBox(t, "budget", "guestbook")
	phase("up", start)

	p := time.Now()
	mk := func(project, route, resources string) string {
		dir := filepath.Join(b.dir, project)
		_ = os.MkdirAll(dir, 0o755)
		cfg := fmt.Sprintf("export default {\n  project: %q,\n%s  apps: { hog: { routes: [%q] } },\n};\n", project, resources, route)
		for name, body := range map[string]string{"index.ts": hogApp, "tiffin.config.ts": cfg,
			"package.json": `{"name":"hog","private":true,"type":"module","scripts":{"start":"bun index.ts"}}`} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	applyDir := func(dir string) {
		t.Helper()
		plan := b.ok("plan", dir)
		b.ok("apply", dir, "--confirm", plan["hash"].(string), "-m", "e2e budget")
	}
	gb := mk("guestbook", "gb", "  resources: { memoryMB: 256, cpus: 0.5 },\n")
	for _, d := range []string{gb, mk("shop", "shop", ""), mk("blog", "blog", "")} {
		applyDir(d)
		deploy(t, b, d)
	}
	// Automatic limits follow what the other projects run; the budget
	// module re-resolves them every 15 seconds.
	time.Sleep(16 * time.Second)
	phase("3 projects", p)

	usage := func(project string) map[string]any { return b.ok("projects", "usage", project) }
	num := func(m map[string]any, path ...string) float64 {
		var v any = m
		for _, k := range path {
			v = v.(map[string]any)[k]
		}
		f, _ := v.(float64)
		return f
	}
	settings := b.ok("box", "settings", "get")
	pool := num(settings, "appMemoryMB")
	t.Logf("box: %v MB, %v CPUs, Tiffin keeps %v MB, %v MB for apps", settings["memoryMB"], settings["cpus"], settings["reserveMB"], pool)
	if pool < 1000 || settings["defaultMaxSharePercent"].(float64) != 100 {
		t.Fatalf("settings: %v", settings)
	}

	// ---- budgets and limits as /usage reports them ----
	u := usage("guestbook")
	if u["budget"].(map[string]any)["auto"] != false || num(u, "budget", "memoryMB") != 256 || u["limitSource"] != "project" ||
		num(u, "memory", "limitBytes") != 256<<20 || num(u, "cpu", "limitCpus") != 0.5 {
		t.Fatalf("guestbook usage: %v", u)
	}
	u = usage("shop")
	// Automatic: the pool minus 128 MB for each of the two copies the others run.
	if u["budget"].(map[string]any)["auto"] != true || u["limitSource"] != "automatic" || num(u, "memory", "limitBytes") != (pool-256)*(1<<20) ||
		u["cpu"].(map[string]any)["limitCpus"] != nil || num(u, "memory", "headroomBytes") < 500<<20 {
		t.Fatalf("shop usage: %v", u)
	}
	t.Logf("shop: limit %.0f MB, protected %.0f MB, headroom %.0f MB", num(u, "memory", "limitBytes")/(1<<20),
		num(u, "memory", "protectedBytes")/(1<<20), num(u, "memory", "headroomBytes")/(1<<20))
	res := b.ok("box", "resources")
	if ps, _ := res["projects"].([]any); len(ps) != 3 {
		t.Fatalf("box resources projects: %v", res["projects"])
	}

	// ---- a budget that cannot fit is refused at plan time ----
	big := filepath.Join(b.dir, "big.json")
	_ = os.WriteFile(big, []byte(`{"project":"shop","resources":{"memoryMB":`+strconv.Itoa(int(pool))+`},"apps":{"hog":{"routes":["shop"]}}}`), 0o644)
	if code, out := b.run("plan", big); code == 0 || !strings.Contains(out, "do not fit this box") || !strings.Contains(out, "Tiffin keeps") {
		t.Fatalf("over-budget plan: %d %s", code, out)
	}

	// ---- guestbook's memory hog dies inside its own slice ----
	p = time.Now()
	b.inBox(`sudo cp /var/lib/tiffin/platform/ca.crt /tmp/ca.crt && sudo chmod 644 /tmp/ca.crt`)
	curl := func(host, path string) string {
		return b.inBox(fmt.Sprintf(`curl -s --max-time 60 --cacert /tmp/ca.crt -o /dev/null -w '%%{http_code}' 'https://%s.tiffin.localhost:8443%s' || true`, host, path))
	}
	hog := curl("gb", "/hog?mb=400")
	if hog == "200" {
		t.Fatal("guestbook held 400 MB inside a 256 MB budget")
	}
	if s := curl("shop", "/"); s != "200" {
		t.Fatalf("shop while guestbook was killed: %s", s)
	}
	kern := b.inBox(`sudo journalctl -k --no-pager | grep oom-kill: | tail -1`)
	if !strings.Contains(kern, "oom_memcg=/tiffin.slice/tiffin-p.slice/tiffin-p-guestbook.slice") {
		t.Fatalf("the OOM kill was not guestbook's own limit: %s", kern)
	}
	waitHTTP := func(host string) {
		t.Helper()
		for i := 0; i < 60; i++ {
			if curl(host, "/") == "200" {
				return
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("%s did not come back", host)
	}
	waitHTTP("gb")
	if u := usage("guestbook"); u["memory"].(map[string]any)["pressure"] != "oom" {
		t.Fatalf("guestbook pressure: %v", u["memory"])
	}
	if u := usage("shop"); u["memory"].(map[string]any)["pressure"] != "none" {
		t.Fatalf("shop pressure: %v", u["memory"])
	}
	phase("oom", p)

	// ---- CPU: quota, burst, fair share ----
	p = time.Now()
	cpu := func(projects ...string) map[string]int {
		t.Helper()
		hosts := map[string]string{"guestbook": "gb", "shop": "shop", "blog": "blog"}
		var start, read strings.Builder
		for _, pr := range projects {
			fmt.Fprintf(&start, "curl -s --cacert /tmp/ca.crt -o /dev/null 'https://%s.tiffin.localhost:8443/burn?ms=12000&n=2'\n", hosts[pr])
			fmt.Fprintf(&read, "awk '/usage_usec/{print $2}' /sys/fs/cgroup/tiffin.slice/tiffin-p.slice/tiffin-p-%s.slice/cpu.stat\n", pr)
		}
		out := b.inBox(start.String() + "sleep 2\n" + read.String() + "sleep 8\n" + read.String())
		f := strings.Fields(out)
		got := map[string]int{}
		for i, pr := range projects {
			a, _ := strconv.Atoi(f[i])
			z, _ := strconv.Atoi(f[i+len(projects)])
			got[pr] = (z - a) / 80000 // percent of one core over 8 s
		}
		time.Sleep(3 * time.Second) // let the burners finish
		return got
	}
	if g := cpu("guestbook"); g["guestbook"] < 40 || g["guestbook"] > 60 {
		t.Fatalf("guestbook's 0.5 CPU cap: %v", g)
	}
	alone := cpu("shop")
	both := cpu("shop", "blog")
	t.Logf("cpu: guestbook capped, shop alone %d%%, shop+blog %d%% / %d%%", alone["shop"], both["shop"], both["blog"])
	if alone["shop"] < 170 || both["shop"] < 80 || both["shop"] > 120 || both["blog"] < 80 || both["blog"] > 120 {
		t.Fatalf("automatic projects must burst alone and share fairly: alone %v, both %v", alone, both)
	}
	phase("cpu", p)

	// ---- memory: burst past the fair share, then give it back without kills ----
	p = time.Now()
	kills := func(pr string) string {
		return b.inBox(`awk '/oom_kill /{print $2}' /sys/fs/cgroup/tiffin.slice/tiffin-p.slice/tiffin-p-` + pr + `.slice/memory.events`)
	}
	before := kills("shop") + "/" + kills("blog")
	for range 5 {
		if s := curl("shop", "/hog?mb=200"); s != "200" {
			t.Fatalf("shop burst: %s", s)
		}
	}
	for range 7 {
		if s := curl("blog", "/hog?mb=100"); s != "200" {
			t.Fatalf("blog taking its share: %s", s)
		}
	}
	if after := kills("shop") + "/" + kills("blog"); after != before {
		t.Fatalf("OOM kills under contention: %s -> %s", before, after)
	}
	shop, blog := usage("shop"), usage("blog")
	t.Logf("contention: shop %.0f MB + %.0f MB swapped, blog %.0f MB", num(shop, "memory", "usedBytes")/(1<<20),
		num(shop, "memory", "swapBytes")/(1<<20), num(blog, "memory", "usedBytes")/(1<<20))
	if num(shop, "memory", "swapBytes") == 0 || num(blog, "memory", "usedBytes") < 650<<20 || shop["memory"].(map[string]any)["pressure"] != "some" {
		t.Fatalf("shop (over its share) should have been swapped out, not blog: shop %v blog %v", shop["memory"], blog["memory"])
	}
	curl("shop", "/free")
	curl("blog", "/free")
	phase("memory", p)

	// ---- a budget change applies live: no restart, no failed request ----
	p = time.Now()
	ctrs := b.inBox(`sudo /usr/local/bin/nerdctl --namespace tiffin ps --format '{{.Names}}' | sort`)
	b.inBox(`rm -f /tmp/live.out; nohup bash -c 'end=$((SECONDS+20)); while [ $SECONDS -lt $end ]; do curl -s --max-time 5 --cacert /tmp/ca.crt -o /dev/null -w "%{http_code}\n" https://gb.tiffin.localhost:8443/ >> /tmp/live.out; sleep 0.1; done' >/dev/null 2>&1 &`)
	time.Sleep(2 * time.Second)
	cfg := filepath.Join(gb, "tiffin.config.ts")
	raw, _ := os.ReadFile(cfg)
	_ = os.WriteFile(cfg, []byte(strings.Replace(string(raw), "memoryMB: 256, cpus: 0.5", "memoryMB: 384, cpus: 1", 1)), 0o644)
	plan := b.ok("plan", gb)
	if ops := plan["ops"].([]any); len(ops) != 1 || ops[0].(map[string]any)["address"] != "project" || plan["risk"] != "reversible" {
		t.Fatalf("budget plan: %v", plan)
	}
	b.ok("apply", gb, "--confirm", plan["hash"].(string))
	time.Sleep(20 * time.Second)
	codes := b.inBox(`sort /tmp/live.out | uniq -c`)
	if strings.Count(codes, "\n") != 0 || !strings.HasSuffix(codes, " 200") {
		t.Fatalf("requests during the budget change: %s", codes)
	}
	if got := b.inBox(`cat /sys/fs/cgroup/tiffin.slice/tiffin-p.slice/tiffin-p-guestbook.slice/memory.max /sys/fs/cgroup/tiffin.slice/tiffin-p.slice/tiffin-p-guestbook.slice/cpu.max`); got != "402653184\n100000 100000" {
		t.Fatalf("new limits: %q", got)
	}
	if after := b.inBox(`sudo /usr/local/bin/nerdctl --namespace tiffin ps --format '{{.Names}}' | sort`); after != ctrs {
		t.Fatalf("containers restarted: %s -> %s", ctrs, after)
	}
	t.Logf("budget change: %s, containers unchanged", strings.TrimSpace(codes))
	phase("live change", p)

	// ---- the box default share caps projects that set nothing ----
	set := b.ok("box", "settings", "set", "--body", `{"defaultMaxSharePercent":25}`)
	u = usage("shop")
	if u["limitSource"] != "box default" || num(u, "memory", "limitBytes") != num(set, "defaultMemoryMB")*(1<<20) || num(u, "cpu", "limitCpus") != num(set, "defaultCpus") {
		t.Fatalf("box default: %v / %v", set, u)
	}
	b.ok("box", "settings", "set", "--body", `{"defaultMaxSharePercent":100}`)
	if u := usage("shop"); u["limitSource"] != "automatic" {
		t.Fatalf("back to automatic: %v", u)
	}
	phase("total", start)
}
