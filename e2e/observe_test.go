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

// TestObserve is the observability acceptance test, driven through the CLI on a fresh
// box: known fixture traffic (people, bots, assets, prefetches, SPA
// navigations, custom events) → analytics shows exact counts with bots
// filtered, and realtime agrees with the rollups → an error sent with the
// Sentry protocol becomes an issue → logs query returns the edge lines →
// a disk alert (threshold lowered) lands in the webhook and the dev inbox.
//
// Pageviews come from the edge access log when the runtime can serve the
// app (a static deploy); on a box without the runtime the same page loads
// are sent as tracker beacons instead, and the test says so.
func TestObserve(t *testing.T) {
	RequireLima(t)
	start := time.Now()
	phase := func(name string, since time.Time) {
		t.Logf("PHASE %-16s %s", name, time.Since(since).Round(100*time.Millisecond))
	}
	dir := t.TempDir()
	cli := buildTiffin(t, dir, "", "")
	bin := buildTiffin(t, dir, "linux", "0.0.1-obs")

	instance, disk, port := newName(), newDiskName(), freePort(t)
	env := append(os.Environ(),
		"TIFFIN_CONFIG_DIR="+filepath.Join(dir, "config"),
		"TIFFIN_LIMA_INSTANCE="+instance, "TIFFIN_LIMA_DISK="+disk, fmt.Sprintf("TIFFIN_LIMA_PORT=%d", port),
		"TIFFIN_LIMA_MEMORY=2GiB", "TIFFIN_HOME=", "TIFFIN_URL=", "TIFFIN_TOKEN=")
	t.Cleanup(func() {
		if os.Getenv("TIFFIN_E2E_KEEP") != "1" {
			_ = exec.Command("limactl", "delete", "-f", instance).Run()
			_ = exec.Command("limactl", "disk", "delete", "-f", disk).Run()
		}
	})
	run := func(args ...string) (int, string) {
		t.Helper()
		cmd := exec.Command(cli, args...)
		cmd.Env, cmd.Dir = env, dir
		out, err := cmd.Output()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("tiffin %v: %v", args, err)
		}
		return code, string(out)
	}
	ok := func(args ...string) map[string]any {
		t.Helper()
		code, out := run(args...)
		if code != 0 {
			t.Fatalf("tiffin %s: exit %d\n%s", strings.Join(args, " "), code, out)
		}
		var m map[string]any
		_ = json.Unmarshal([]byte(out), &m)
		return m
	}
	okList := func(args ...string) []any {
		t.Helper()
		code, out := run(args...)
		if code != 0 {
			t.Fatalf("tiffin %s: exit %d\n%s", strings.Join(args, " "), code, out)
		}
		var l []any
		if json.Unmarshal([]byte(out), &l) == nil {
			return l
		}
		var pg struct{ Items []any } // a paged list: its first page
		_ = json.Unmarshal([]byte(out), &pg)
		return pg.Items
	}
	inBox := func(script string) string {
		t.Helper()
		out, err := exec.Command("limactl", "shell", "--workdir", "/", instance, "--", "sudo", "bash", "-euo", "pipefail", "-c", script).CombinedOutput()
		if err != nil {
			t.Fatalf("in box: %v\n%s\n%s", err, script, out)
		}
		return strings.TrimSpace(string(out))
	}
	eventually := func(what string, d time.Duration, f func() bool) {
		t.Helper()
		deadline := time.Now().Add(d)
		for !f() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out after %s waiting for %s", d, what)
			}
			time.Sleep(2 * time.Second)
		}
	}

	// ---- box + project ----
	p := time.Now()
	ok("up", "--binary", bin)
	// A folder of its own: the CLI refuses a config beside its settings (dir/config).
	proj := filepath.Join(dir, "project")
	_ = os.MkdirAll(proj, 0o755)
	cfg := filepath.Join(proj, "tiffin.config.ts")
	if err := os.WriteFile(cfg, []byte(`import { defineConfig } from "@shiptiffin/sdk";
export default defineConfig({
  project: "shop",
  apps: { web: { framework: "static", routes: ["shop"] } },
  services: { analytics: { retentionDays: 30 }, email: {} },
});
`), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := ok("plan", cfg)
	ok("apply", cfg, "--confirm", plan["hash"].(string)[:12], "-m", "e2e observe")
	phase("up+apply", p)

	// ---- fixture app: a static site through the runtime, when there is one ----
	p = time.Now()
	viaEdge := false
	if code, _ := run("deploys", "create", "--help"); code == 0 {
		files := map[string]any{"files": map[string]string{
			"index.html":   "<!doctype html><title>Shop</title><h1>Shop</h1>",
			"pricing.html": "<!doctype html><title>Pricing</title><h1>Pricing</h1>",
		}}
		raw, _ := json.Marshal(files)
		d := ok("deploys", "create", "shop", "web", "--body", string(raw))
		id, _ := d["id"].(string)
		eventually("the static deploy to go live", 5*time.Minute, func() bool {
			_, out := run("deploys", "get", "shop", "web", id)
			return strings.Contains(out, `"live"`)
		})
		viaEdge = inBox(`curl -sk -o /dev/null -w '%{http_code}' --resolve shop.tiffin.localhost:8443:127.0.0.1 https://shop.tiffin.localhost:8443/`) == "200"
	}
	if viaEdge {
		t.Log("pageviews: from the edge access log (static deploy)")
	} else {
		t.Log("pageviews: the runtime cannot serve the app on this build; page loads are sent as tracker beacons")
	}
	phase("fixture app", p)

	// ---- fixture traffic, all from inside the box ----
	p = time.Now()
	const chrome = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.%d.0 Safari/537.36"
	var sh strings.Builder
	sh.WriteString(`set +e
page() { curl -sk -o /dev/null --resolve shop.tiffin.localhost:8443:127.0.0.1 -A "$1" -H 'Accept-Language: en-GB' "${@:3}" "https://shop.tiffin.localhost:8443$2"; }
beacon() { curl -sk -o /dev/null -w '%{http_code}\n' --resolve t.tiffin.localhost:8443:127.0.0.1 -A "$1" -H 'Accept-Language: en-GB' -H 'Content-Type: text/plain;charset=UTF-8' --data "$2" https://t.tiffin.localhost:8443/e; }
doc=(-H 'Sec-Fetch-Dest: document' -H 'Sec-Fetch-Mode: navigate')
`)
	load := func(ua, path, ref string) {
		if viaEdge {
			fmt.Fprintf(&sh, "page %q %q \"${doc[@]}\" -H %q\n", ua, path, "Referer: "+ref)
		} else {
			fmt.Fprintf(&sh, "beacon %q %q\n", ua, fmt.Sprintf(`{"n":"pageview","u":"https://shop.tiffin.localhost:8443%s","r":%q}`, path, ref))
		}
	}
	// Six people: 1 and 2 bounce, 3-6 also open /pricing. 10 page loads.
	for i := 1; i <= 6; i++ {
		ua := fmt.Sprintf(chrome, i)
		load(ua, "/?utm_source=launch&secret=abc", "https://news.ycombinator.com/")
		if i >= 3 {
			load(ua, "/pricing.html", "https://shop.tiffin.localhost:8443/")
		}
	}
	// Bots with document headers, assets and a prefetch: none count.
	for _, ua := range []string{"Googlebot/2.1 (+http://www.google.com/bot.html)", "Mozilla/5.0 (compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm)",
		"curl/8.5.0", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/129.0.0.0 Safari/537.36"} {
		load(ua, "/", "")
	}
	fmt.Fprintf(&sh, "page %q /index.html -H 'Sec-Fetch-Dest: script'\n", fmt.Sprintf(chrome, 1))
	fmt.Fprintf(&sh, "page %q /pricing.html \"${doc[@]}\" -H 'Sec-Purpose: prefetch'\n", fmt.Sprintf(chrome, 2))
	// Visitor 3 navigates client-side (11th pageview) and two people sign up.
	fmt.Fprintf(&sh, "beacon %q %q\n", fmt.Sprintf(chrome, 3), `{"n":"pageview","u":"https://shop.tiffin.localhost:8443/docs","r":null}`)
	fmt.Fprintf(&sh, "beacon %q %q\n", fmt.Sprintf(chrome, 3), `{"n":"Signup","u":"https://shop.tiffin.localhost:8443/pricing.html","p":{"plan":"pro"}}`)
	fmt.Fprintf(&sh, "beacon %q %q\n", fmt.Sprintf(chrome, 5), `{"n":"Signup","u":"https://shop.tiffin.localhost:8443/pricing.html","p":{"plan":"free"}}`)
	fmt.Fprintf(&sh, "beacon %q %q\n", "python-requests/2.31", `{"n":"Signup","u":"https://shop.tiffin.localhost:8443/"}`)
	inBox(sh.String())
	phase("traffic", p)

	// ---- analytics: exact counts, bots filtered, realtime == rollups ----
	p = time.Now()
	var o map[string]any
	eventually("analytics to count the fixture traffic", time.Minute, func() bool {
		o = ok("analytics", "overview", "--project", "shop", "--period", "today")
		tot, _ := o["totals"].(map[string]any)
		return tot != nil && tot["pageviews"].(float64) >= 11
	})
	tot := o["totals"].(map[string]any)
	want := map[string]float64{"visitors": 6, "pageviews": 11, "sessions": 6, "bounceRate": 0.333, "events": 2}
	for k, v := range want {
		if tot[k].(float64) != v {
			t.Errorf("analytics %s = %v, want %v (totals %v)", k, tot[k], v, tot)
		}
	}
	if s := fmt.Sprint(o["sources"]); !strings.Contains(s, "launch") {
		t.Errorf("sources %s", s)
	}
	if s := fmt.Sprint(o["pages"]); strings.Contains(s, "secret") || !strings.Contains(s, "/pricing.html") {
		t.Errorf("pages %s", s)
	}
	rt := ok("analytics", "realtime", "--project", "shop")
	if rt["pageviews30m"].(float64) != tot["pageviews"].(float64) || rt["visitorsNow"].(float64) != tot["visitors"].(float64) || rt["events30m"].(float64) != tot["events"].(float64) {
		t.Errorf("realtime %v disagrees with rollups %v", rt, tot)
	}
	ev := ok("analytics", "events", "--project", "shop", "--period", "today")
	if s := fmt.Sprint(ev["events"]); !strings.Contains(s, "Signup") || !strings.Contains(s, "count:2") {
		t.Errorf("events %s", s)
	}
	st := ok("status")
	if s := fmt.Sprint(st["checks"]); !strings.Contains(s, "5 bots dropped") {
		t.Errorf("want 5 bots dropped in status: %s", s)
	}
	t.Logf("analytics today: %v", tot)
	phase("analytics", p)

	// ---- errors over the Sentry protocol ----
	p = time.Now()
	ing := ok("observe", "ingest", "--project", "shop", "--app", "web")
	dsn := ing["sentryDsn"].(string) // http://<key>@127.0.0.1:4318/<id>
	key := strings.TrimPrefix(strings.SplitN(dsn, "@", 2)[0], "http://")
	id := dsn[strings.LastIndex(dsn, "/")+1:]
	evt := `{"event_id":"%s","platform":"node","exception":{"values":[{"type":"TypeError","value":"Cannot read properties of undefined (reading 'id')","stacktrace":{"frames":[{"filename":"/app/src/cart.ts","function":"addItem","lineno":%d,"in_app":true}]}}]}}`
	var env2 strings.Builder
	for i, line := range []int{10, 14} {
		e := fmt.Sprintf(evt, fmt.Sprintf("%032d", i+1), line)
		fmt.Fprintf(&env2, "printf '%%s\\n%%s\\n%%s\\n' %q %q %q | curl -sf -o /dev/null -H 'X-Sentry-Auth: Sentry sentry_version=7, sentry_key=%s' --data-binary @- http://127.0.0.1:4318/api/%s/envelope/\n",
			`{"dsn":"`+dsn+`"}`, fmt.Sprintf(`{"type":"event","length":%d}`, len(e)), e, key, id)
	}
	inBox(env2.String())
	issues := okList("issues", "list", "--project", "shop")
	if len(issues) != 1 || issues[0].(map[string]any)["count"].(float64) != 2 || issues[0].(map[string]any)["culprit"] != "addItem (/app/src/cart.ts:14)" {
		t.Fatalf("issues: %v", issues)
	}
	iss := ok("issues", "get", issues[0].(map[string]any)["id"].(string))
	if len(iss["events"].([]any)) != 2 {
		t.Fatalf("issue events: %v", iss)
	}
	phase("errors", p)

	// ---- logs ----
	p = time.Now()
	var logs map[string]any
	eventually("edge lines in the project's logs", time.Minute, func() bool {
		logs = ok("logs", "query", "--project", "shop", "--query", "source:edge or source:errors", "--limit", "200")
		return logs["count"].(float64) > 0 && strings.Contains(fmt.Sprint(logs["rows"]), "source:errors")
	})
	box := ok("logs", "query", "--query", "unit:tiffin.service", "--limit", "5")
	if box["count"].(float64) == 0 {
		t.Fatalf("box logs: %v", box)
	}
	t.Logf("logs: %v project rows, %v box rows", logs["count"], box["count"])
	phase("logs", p)

	// ---- disk alert → webhook and dev inbox ----
	p = time.Now()
	inBox(`cat > /tmp/hook.py <<'EOF'
import http.server
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get('Content-Length', 0)))
        with open('/tmp/hook.jsonl', 'ab') as f:
            f.write(body + b'\n')
        self.send_response(204)
        self.end_headers()
    def log_message(self, *a):
        pass
http.server.HTTPServer(('127.0.0.1', 9099), H).serve_forever()
EOF
systemd-run --unit=obs-hook python3 /tmp/hook.py >/dev/null 2>&1; sleep 1`)
	ok("observe", "settings", "set", "--webhook", "http://127.0.0.1:9099/hook", "--email-project", "shop")
	ok("alerts", "rules", "put", "disk-full", "--body", `{"kind":"disk","threshold":1}`)
	eventually("the disk alert to fire", 90*time.Second, func() bool {
		a := ok("alerts", "list")
		return strings.Contains(fmt.Sprint(a["firing"]), "disk-full")
	})
	hook := inBox(`cat /tmp/hook.jsonl 2>/dev/null || true`)
	if !strings.Contains(hook, `"rule":"disk-full"`) || !strings.Contains(hook, `"state":"firing"`) {
		t.Fatalf("webhook got: %s", hook)
	}
	_, inbox := run("email", "messages", "list", "shop")
	if !strings.Contains(inbox, "Firing: disk-full") {
		t.Fatalf("dev inbox: %s", inbox)
	}
	ok("alerts", "rules", "put", "disk-full", "--body", `{"kind":"disk","threshold":85}`)
	eventually("the disk alert to resolve", 90*time.Second, func() bool {
		return strings.Contains(inBox(`cat /tmp/hook.jsonl`), `"state":"resolved"`)
	})
	phase("alerts", p)

	// ---- box health and metrics ----
	ov := ok("observe", "overview")
	if s := fmt.Sprint(ov["stores"]); !strings.Contains(s, "logs:ok") || !strings.Contains(s, "metrics:ok") {
		t.Fatalf("overview stores: %s", s)
	}
	if viaEdge {
		eventually("per-app request metrics", 90*time.Second, func() bool {
			_, out := run("observe", "apps", "--project", "shop", "--since", "30m")
			return strings.Contains(out, `"app": "web"`)
		})
	}

	ok("down", "--confirm", "local")
	(&Box{Name: instance, Disk: disk, t: t}).AssertGone()
	t.Logf("TOTAL %s", time.Since(start).Round(time.Second))
}
