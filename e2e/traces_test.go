//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTracesAndVitals deploys templates/hello-next with an instrumentation.ts
// (@vercel/otel's registerOTel) and analytics on:
//
//	a slow route that fetches another route of the app → its trace (kept as
//	slow) is listed by the CLI with the Next.js server span and the fetch
//	span under it, and its ID is the request ID the edge gave the request
//	→ the edge log line carries the same trace_id → a fast request is not
//	kept (10% sample, unless its ID falls in it) → Web Vitals beacons on the
//	app's own origin show up as p75 per metric and page.
func TestTracesAndVitals(t *testing.T) {
	start := time.Now()
	phase := phaseLogger(t)
	b := newCLIBox(t, "traces", "hello-next")
	phase("up", start)

	app := filepath.Join(b.dir, "hello-next")
	if out, err := exec.Command("rsync", "-a", "--exclude", "node_modules", "--exclude", ".next",
		filepath.Join(RepoRoot(), "templates", "hello-next")+"/", app+"/").CombinedOutput(); err != nil {
		t.Fatalf("copy template: %v %s", err, out)
	}
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(app, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("tiffin.config.ts", `import { defineConfig } from "tiffin-sdk";
export default defineConfig({
  project: "hello-next",
  apps: { web: { framework: "next", memoryMB: 512, healthcheck: "/api/health" } },
  services: { analytics: {} },
});
`)
	write("instrumentation.ts", `import { registerOTel } from "@vercel/otel";

export function register() {
  registerOTel({ serviceName: "web" });
}
`)
	write("app/api/ping/route.ts", `export const dynamic = "force-dynamic";
export function GET() {
  return Response.json({ pong: true });
}
`)
	write("app/api/slow/route.ts", `export const dynamic = "force-dynamic";
export async function GET(req: Request) {
  const res = await fetch("http://127.0.0.1:" + process.env.PORT + "/api/ping", { cache: "no-store" });
  await res.json();
  await new Promise((r) => setTimeout(r, 1200));
  return Response.json({ requestId: req.headers.get("x-request-id"), traceparent: req.headers.get("traceparent") });
}
`)
	add := exec.Command("bun", "add", "@vercel/otel@2", "@opentelemetry/api@1", "@opentelemetry/api-logs@0", "@opentelemetry/sdk-logs@0",
		"@opentelemetry/resources@2", "@opentelemetry/sdk-metrics@2", "@opentelemetry/sdk-trace-base@2", "@opentelemetry/instrumentation@0")
	add.Dir = app
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("bun add: %v\n%s", err, out)
	}
	plan := b.ok("plan", app)
	b.ok("apply", app, "--confirm", plan["hash"].(string), "-m", "e2e: traces")
	b.waitReady("app/web")
	p := time.Now()
	d := deployArgs(t, b, app)
	phase("deploy", p)
	t.Logf("deploy %s: build %.1fs", d.ID, d.BuildSecs)

	c := b.https()
	site := b.url("hello-next")
	eventually := func(what string, wait time.Duration, f func() bool) {
		t.Helper()
		deadline := time.Now().Add(wait)
		for !f() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out after %s waiting for %s", wait, what)
			}
			time.Sleep(2 * time.Second)
		}
	}

	// ---- a slow request: kept, with its spans ----
	p = time.Now()
	code, _, body := b.get(c, "GET", site+"/api/slow", nil)
	var seen struct{ RequestID, Traceparent string }
	if err := json.Unmarshal([]byte(body), &seen); code != 200 || err != nil || len(seen.RequestID) != 36 {
		t.Fatalf("GET /api/slow: %d %s", code, body)
	}
	traceID := strings.ReplaceAll(seen.RequestID, "-", "")
	if !strings.HasPrefix(seen.Traceparent, "00-"+traceID+"-") {
		t.Fatalf("the app's traceparent %q does not carry the request ID %s", seen.Traceparent, seen.RequestID)
	}
	for i := 0; i < 5; i++ {
		b.get(c, "GET", site+"/api/ping", nil)
	}
	var trace map[string]any
	eventually("the slow request's trace", 2*time.Minute, func() bool {
		for _, x := range b.list("traces", "list", "--project", "hello-next", "--since", "1h") {
			if x["traceId"] == traceID {
				trace = x
				return true
			}
		}
		return false
	})
	t.Logf("trace: %v", trace)
	if trace["kept"] != "slow" || trace["durationMs"].(float64) < 1200 || !strings.Contains(fmt.Sprint(trace["name"]), "/api/slow") || trace["app"] != "web" {
		t.Fatalf("slow trace summary: %v", trace)
	}
	// Exported spans arrive in batches: wait for the fetch's.
	var spans []any
	eventually("the fetch span", time.Minute, func() bool {
		got := b.ok("traces", "get", seen.RequestID, "--project", "hello-next") // the request ID works as a trace ID
		spans, _ = got["spans"].([]any)
		for _, s := range spans {
			if m := s.(map[string]any); m["kind"] == "client" && strings.HasPrefix(fmt.Sprint(m["name"]), "fetch") {
				return true
			}
		}
		return false
	})
	var server, fetch map[string]any
	var tree []string
	for _, s := range spans {
		m := s.(map[string]any)
		tree = append(tree, fmt.Sprintf("%s%s [%s %.1fms]", strings.Repeat("  ", int(m["depth"].(float64))), m["name"], m["kind"], m["durationMs"]))
		if server == nil && m["kind"] == "server" {
			server = m
		}
		if fetch == nil && m["kind"] == "client" && strings.HasPrefix(fmt.Sprint(m["name"]), "fetch") {
			fetch = m
		}
	}
	t.Logf("spans:\n%s", strings.Join(tree, "\n"))
	if server == nil || server["depth"].(float64) != 0 || fetch["depth"].(float64) < 1 || fetch["durationMs"].(float64) <= 0 ||
		fetch["offsetMs"].(float64) > server["durationMs"].(float64) {
		t.Fatalf("want a root server span with the fetch under it:\n%s", strings.Join(tree, "\n"))
	}
	phase("trace", p)

	// ---- the edge line of the request carries its trace ID ----
	eventually("the edge log line with trace_id", time.Minute, func() bool {
		out := b.ok("logs", "query", "--project", "hello-next", "--query", "trace_id:"+traceID, "--since", "1h")
		return out["count"] != nil && out["count"].(float64) >= 1 && strings.Contains(fmt.Sprint(out["rows"]), "/api/slow")
	})

	// ---- fast requests: only the sampled share is kept ----
	pings := 0
	for _, x := range b.list("traces", "list", "--project", "hello-next", "--since", "1h", "--limit", "100") {
		if strings.Contains(fmt.Sprint(x["name"]), "/api/ping") && x["kept"] == "sampled" {
			pings++
		}
	}
	t.Logf("fast /api/ping traces kept as sampled: %d of 5", pings)
	if pings > 5 {
		t.Fatalf("too many fast traces kept: %d", pings)
	}
	size := b.inBox(`sudo python3 -c "import sqlite3; c=sqlite3.connect('/var/lib/tiffin/observe/traces.db'); print(c.execute('select count(*), sum(spans), sum(bytes) from traces').fetchone())"`)
	t.Logf("traces.db: (traces, spans, bytes) = %s; file %s", size, b.inBox(`sudo du -h /var/lib/tiffin/observe/traces.db | cut -f1`))

	// ---- Web Vitals on the app's own origin ----
	p = time.Now()
	for i, lcp := range []int{1200, 1800, 2400, 3100} {
		req, _ := http.NewRequest("POST", site+"/_tiffin/vitals",
			strings.NewReader(fmt.Sprintf(`{"path":"/products/%d","metrics":{"LCP":%d,"CLS":0.0%d,"INP":%d,"TTFB":90}}`, 100+i, lcp, i, 80+i*40)))
		req.Header.Set("Content-Type", "text/plain;charset=UTF-8") // what sendBeacon sends
		// A browser's user agent: Go's own is a bot's, and bots' beacons are dropped.
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36")
		res, err := c.Do(req)
		if err != nil || res.StatusCode != 204 {
			t.Fatalf("vitals beacon: %v %v", err, res)
		}
		res.Body.Close()
	}
	if code, _, _ := b.get(c, "POST", b.url("t")+"/_tiffin/vitals", strings.NewReader(`{"path":"/","metrics":{"LCP":1}}`)); code != 404 {
		t.Fatalf("a beacon to a host no app serves: %d", code)
	}
	v := b.ok("analytics", "vitals", "--project", "hello-next", "--period", "today")
	t.Logf("vitals: %v", v)
	metrics := map[string]map[string]any{}
	for _, m := range v["metrics"].([]any) {
		mm := m.(map[string]any)
		metrics[mm["name"].(string)] = mm
	}
	lcp := metrics["LCP"]
	if lcp == nil || lcp["samples"].(float64) != 4 || lcp["p75"].(float64) < 2300 || lcp["p75"].(float64) > 2500 || lcp["rating"] != "good" {
		t.Fatalf("LCP: %v", lcp)
	}
	pages := v["pages"].([]any)
	if len(pages) != 1 || pages[0].(map[string]any)["path"] != "/products/[id]" {
		t.Fatalf("pages: %v", pages)
	}
	phase("vitals", p)
}
