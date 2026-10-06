//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// limitsIndex sleeps, streams a tick a second, and writes megabytes into
// its disk folder, saying which error it got.
const limitsIndex = `import { Hono } from "hono";
import { closeSync, openSync, writeSync } from "node:fs";

const app = new Hono();
const enc = new TextEncoder();
app.get("/", (c) => c.text("limits"));
app.get("/healthz", (c) => c.text("ok"));
app.get("/slow", async (c) => {
  await Bun.sleep(Number(c.req.query("s")) * 1000);
  return c.text("slept");
});
app.get("/stream", (c) => {
  const n = Number(c.req.query("n"));
  return new Response(new ReadableStream({
    async start(ctl) {
      for (let i = 0; i < n; i++) {
        ctl.enqueue(enc.encode("tick " + i + "\n"));
        await Bun.sleep(1000);
      }
      ctl.close();
    },
  }), { headers: { "content-type": "text/plain" } });
});
app.post("/write", (c) => {
  const mb = Number(c.req.query("mb"));
  const chunk = new Uint8Array(1 << 20).fill(7);
  let fd;
  try {
    fd = openSync("data/" + c.req.query("name"), "w");
    for (let i = 0; i < mb; i++) writeSync(fd, chunk);
    return c.text("wrote " + mb + " MB");
  } catch (e) {
    return c.text("failed: " + e.code, 507);
  } finally {
    if (fd !== undefined) closeSync(fd);
  }
});

export default { port: Number(process.env.PORT ?? 3000), idleTimeout: 0, fetch: app.fetch };
`

// TestRequestAndDiskLimits: a request past its app's time limit
// (timeoutSeconds) gets 504 and a stream past it is cut, while a stream
// under it is whole; a disk folder holds no more than its size (the app
// gets ENOSPC), its size must fit the project's storage limit, it grows
// without a deploy, and it cannot shrink below what it holds.
func TestRequestAndDiskLimits(t *testing.T) {
	start := time.Now()
	phase := phaseLogger(t)
	b := newCLIBox(t, "lim", "hello")
	phase("up", start)
	if opts := b.inBox("findmnt -n -o OPTIONS /var/lib/tiffin"); !strings.Contains(opts, "prjquota") {
		t.Fatalf("the data disk is mounted without project quotas: %s", opts)
	}

	app := filepath.Join(b.dir, "limits")
	src := filepath.Join(RepoRoot(), "templates", "hello-hono")
	if out, err := exec.Command("rsync", "-a", "--exclude", "node_modules", src+"/", app+"/").CombinedOutput(); err != nil {
		t.Fatalf("copy template: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(app, "index.ts"), []byte(limitsIndex), 0o644); err != nil {
		t.Fatal(err)
	}
	config := func(size string) {
		t.Helper()
		cfg := fmt.Sprintf(`import { defineConfig } from "tiffin-sdk";

export default defineConfig({
  project: "hello",
  apps: {
    api: { framework: "hono", healthcheck: "/healthz", timeoutSeconds: 20, disk: { data: %q } },
  },
});
`, size)
		if err := os.WriteFile(filepath.Join(app, "tiffin.config.ts"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	plan := func() (int, string) {
		t.Helper()
		return b.run("plan", app)
	}
	apply := func() {
		t.Helper()
		p := b.ok("plan", app)
		hash, _ := p["hash"].(string)
		b.ok("apply", app, "--confirm", hash, "-m", "e2e: limits")
	}
	config("50MB")
	apply()
	if code, out := b.run("storage", "quota", "set", "hello", "--max-bytes", fmt.Sprint(20<<20)); code == 0 || !strings.Contains(out, "more than project hello's storage limit") {
		t.Fatalf("a 20 MB storage limit under a 50 MB folder must be refused: exit %d %s", code, out)
	}
	b.ok("storage", "quota", "set", "hello", "--max-bytes", fmt.Sprint(50<<20))
	config("100MB")
	if code, out := plan(); code == 0 || !strings.Contains(out, "more than project hello's storage limit") {
		t.Fatalf("a 100 MB folder under a 50 MB storage limit must be refused: exit %d %s", code, out)
	}
	config("50MB")
	deploy(t, b, app)

	// ---- the request time limit ----
	p := time.Now()
	b.inBox(`sudo cp /var/lib/tiffin/platform/ca.crt /tmp/ca.crt && sudo chmod 644 /tmp/ca.crt`)
	curl := func(args string) string {
		return b.inBox(`curl -sS --cacert /tmp/ca.crt -w ' %{http_code} %{time_total}' --max-time 120 ` + args + ` 2>&1; echo " exit=$?"`)
	}
	u := "https://hello.tiffin.localhost:8443"
	slow := curl(u + "/slow?s=40")
	t.Logf("40 s request, 20 s limit: %q", slow)
	if !strings.Contains(slow, "time limit for one request (20s)") || !strings.Contains(slow, " 504 ") {
		t.Fatalf("a request past the limit must get 504: %q", slow)
	}
	under := curl("-N " + u + "/stream?n=12")
	t.Logf("12 s stream: %q", under)
	if strings.Count(under, "tick") != 12 || !strings.Contains(under, " 200 ") || !strings.HasSuffix(under, "exit=0") {
		t.Fatalf("a stream under the limit must be whole: %q", under)
	}
	over := curl("-N " + u + "/stream?n=40")
	t.Logf("40 s stream: %q", over)
	if n := strings.Count(over, "tick"); n < 15 || n > 22 || strings.HasSuffix(over, "exit=0") {
		t.Fatalf("a stream past the limit must be cut at about 20 s: %q", over)
	}
	phase("time limit", p)

	// ---- disk folder size ----
	p = time.Now()
	c := b.https()
	code, _, body := b.get(c, "POST", b.url("hello")+"/write?mb=60&name=a", nil)
	t.Logf("60 MB into a 50 MB folder: %d %q", code, body)
	if code != 507 || (body != "failed: ENOSPC" && body != "failed: EDQUOT") {
		t.Fatalf("writing past the folder's size must fail with ENOSPC/EDQUOT: %d %q", code, body)
	}
	var folders []any
	for i := 0; ; i++ {
		use := b.ok("projects", "usage", "hello")
		disk, _ := use["disk"].(map[string]any)
		folders, _ = disk["folders"].([]any)
		if len(folders) == 1 {
			break
		}
		if i > 30 {
			t.Fatalf("usage lists no disk folder: %v", use["disk"])
		}
		time.Sleep(2 * time.Second)
	}
	f, _ := folders[0].(map[string]any)
	if f["path"] != "data" || f["sizeBytes"] != float64(50<<20) || f["usedBytes"].(float64) < 49<<20 || f["enforced"] != true {
		t.Fatalf("folder usage: %v", f)
	}
	config("10MB")
	if code, out := plan(); code == 0 || !strings.Contains(out, "cannot shrink below what it holds") {
		t.Fatalf("shrinking below what it holds must be refused: exit %d %s", code, out)
	}
	b.ok("storage", "quota", "set", "hello", "--max-bytes", fmt.Sprint(200<<20))
	config("100MB")
	apply()
	code, _, body = b.get(c, "POST", b.url("hello")+"/write?mb=60&name=a", nil)
	if code != 200 || body != "wrote 60 MB" {
		t.Fatalf("after growing the folder (no deploy): %d %q", code, body)
	}
	phase("disk size", p)
	t.Logf("total %s", time.Since(start).Round(time.Second))
}
