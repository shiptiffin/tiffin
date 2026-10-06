//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// appsysIndex is a Hono app that runs ffmpeg, keeps a counter in a disk
// folder, answers after a long silence, streams with a long pause and
// counts uploads.
const appsysIndex = `import { Hono } from "hono";

const app = new Hono();
const enc = new TextEncoder();
app.get("/", (c) => c.text("media"));
app.get("/healthz", (c) => c.text("ok"));
app.get("/ffmpeg", (c) => c.text(Bun.spawnSync(["ffmpeg", "-version"]).stdout.toString().split("\n")[0]));
app.get("/count", async (c) => {
  const n = Number((await Bun.file("data/count.txt").text()).trim()) + 1;
  await Bun.write("data/count.txt", String(n));
  return c.text(String(n));
});
app.get("/slow", async (c) => {
  await Bun.sleep(Number(c.req.query("s")) * 1000);
  return c.text("slept " + (process.env.TIFFIN_DEPLOY ?? ""));
});
app.get("/stream", () => new Response(new ReadableStream({
  async start(ctl) {
    ctl.enqueue(enc.encode("start\n"));
    await Bun.sleep(130_000);
    ctl.enqueue(enc.encode("end\n"));
    ctl.close();
  },
}), { headers: { "content-type": "text/plain" } }));
app.post("/upload", async (c) => {
  let n = 0;
  for await (const chunk of c.req.raw.body!) n += chunk.length;
  await Bun.sleep(Number(c.req.query("s") ?? 0) * 1000);
  return c.text("got " + n);
});

// idleTimeout 0: Bun closes a request that sends nothing for 10s by default;
// maxRequestBodySize: Bun refuses bodies over 128 MB by default.
export default { port: Number(process.env.PORT ?? 3000), idleTimeout: 0, maxRequestBodySize: 4 * 1024 ** 3, fetch: app.fetch };
`

// TestAppSystem: what band-maker, podframes and satoshi-bench need, on a
// fresh box. An app with packages: ["ffmpeg"] runs ffmpeg at request time;
// its disk folder starts from the repository's file and keeps what the app
// writes across a deploy, while a preview and a duplicate get their own;
// a response after 2+ minutes of silence, a stream with a 2+ minute pause
// and a 500 MB upload answered a minute later all succeed through the edge
// (with the WAF on for a multipart upload too), and a request under way
// finishes on the old release through a deploy.
func TestAppSystem(t *testing.T) {
	start := time.Now()
	phase := phaseLogger(t)
	b := newCLIBox(t, "sys", "hello")
	phase("up", start)

	app := filepath.Join(b.dir, "media")
	src := filepath.Join(RepoRoot(), "templates", "hello-hono")
	if out, err := exec.Command("rsync", "-a", "--exclude", "node_modules", src+"/", app+"/").CombinedOutput(); err != nil {
		t.Fatalf("copy template: %v %s", err, out)
	}
	write := func(name, body string) {
		t.Helper()
		_ = os.MkdirAll(filepath.Dir(filepath.Join(app, name)), 0o755)
		if err := os.WriteFile(filepath.Join(app, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.ts", appsysIndex)
	write("data/count.txt", "41\n")
	write("tiffin.config.ts", `import { defineConfig } from "tiffin-sdk";

export default defineConfig({
  project: "hello",
  apps: {
    api: { framework: "hono", healthcheck: "/healthz", packages: ["ffmpeg"], disk: ["data"] },
  },
});
`)
	plan := b.ok("plan", app)
	hash, _ := plan["hash"].(string)
	b.ok("apply", app, "--confirm", hash, "-m", "e2e: packages and disk")

	// ---- packages ----
	p := time.Now()
	d1 := deploy(t, b, app)
	phase("deploy with ffmpeg", p)
	t.Logf("first deploy with ffmpeg: build %.1fs, total %.1fs", d1.BuildSecs, d1.TotalSecs)
	c := b.https()
	if code, _, body := b.get(c, "GET", b.url("hello")+"/ffmpeg", nil); code != 200 || !strings.HasPrefix(body, "ffmpeg version") {
		t.Fatalf("ffmpeg at request time: %d %q", code, body)
	}

	// ---- disk folder: seeded once, kept across a deploy ----
	count := func(host string) string {
		t.Helper()
		code, _, body := b.get(c, "GET", b.url(host)+"/count", nil)
		if code != 200 {
			t.Fatalf("count on %s: %d %s", host, code, body)
		}
		return body
	}
	if got := count("hello"); got != "42" {
		t.Fatalf("first count %q, want 42 (41 from the repository)", got)
	}
	if got := b.inBox("sudo cat /var/lib/tiffin/runtime/disks/hello/api/prod/data/count.txt"); got != "42" {
		t.Fatalf("on the box: %q", got)
	}

	// ---- long requests and a big upload, under way while a deploy happens ----
	p = time.Now()
	b.inBox(`sudo cp /var/lib/tiffin/platform/ca.crt /tmp/ca.crt && sudo chmod 644 /tmp/ca.crt
head -c 500M /dev/urandom > /tmp/big
u=https://hello.tiffin.localhost:8443
c="curl -sS --cacert /tmp/ca.crt -w ' %{http_code}' --max-time 900"
nohup bash -c "$c $u/slow?s=130" > /tmp/slow.out 2>&1 &
nohup bash -c "$c -N $u/stream" > /tmp/stream.out 2>&1 &
nohup bash -c "$c --http1.1 -H 'content-type: application/octet-stream' --data-binary @/tmp/big '$u/upload?s=70'" > /tmp/up1.out 2>&1 &
nohup bash -c "$c --http2 -H 'content-type: application/octet-stream' --data-binary @/tmp/big '$u/upload?s=70'" > /tmp/up2.out 2>&1 &
echo started`)
	time.Sleep(5 * time.Second)
	write("index.ts", strings.Replace(appsysIndex, `"slept "`, `"slept (v2) "`, 1))
	d2 := deploy(t, b, app)
	if got := count("hello"); got != "43" {
		t.Fatalf("count after a deploy %q, want 43: the folder must keep what the app wrote", got)
	}
	for i := 0; i < 200 && strings.Count(b.inBox("cat /tmp/slow.out /tmp/stream.out /tmp/up1.out /tmp/up2.out"), " 200") < 4; i++ {
		time.Sleep(time.Second)
	}
	slow, stream := b.inBox("cat /tmp/slow.out"), b.inBox("cat /tmp/stream.out")
	up1, up2 := b.inBox("cat /tmp/up1.out"), b.inBox("cat /tmp/up2.out")
	t.Logf("slow: %q\nstream: %q\nupload h1: %q\nupload h2: %q", slow, stream, up1, up2)
	if slow != "slept "+d1.ID+" 200" {
		t.Fatalf("a 130 s request through a deploy must finish on the release it began on (%s, then %s): %q", d1.ID, d2.ID, slow)
	}
	if stream != "start\nend\n 200" {
		t.Fatalf("a stream with a 130 s pause: %q", stream)
	}
	want := "got 524288000 200"
	if up1 != want || up2 != want {
		t.Fatalf("500 MB uploads answered 70 s later: %q %q", up1, up2)
	}
	phase("long requests", p)

	// ---- the WAF passes a big multipart upload ----
	p = time.Now()
	b.ok("protect", "set", "--body", `{"waf":true}`)
	time.Sleep(3 * time.Second)
	up := b.inBox(`head -c 60M /tmp/big > /tmp/mid; curl -sS --cacert /tmp/ca.crt -w ' %{http_code}' -F file=@/tmp/mid 'https://hello.tiffin.localhost:8443/upload?s=70'`)
	b.ok("protect", "set", "--body", `{"waf":false}`)
	if !strings.HasPrefix(up, "got 629") || !strings.HasSuffix(up, " 200") {
		t.Fatalf("60 MB multipart upload behind the WAF, answered 70 s later: %q", up)
	}
	phase("waf upload", p)

	// ---- a preview and a duplicate get their own folders ----
	p = time.Now()
	deployArgs(t, b, app, "--preview", "pr-1")
	if got := count("pr-1--hello"); got != "42" {
		t.Fatalf("preview count %q, want 42 (its own folder, from its image)", got)
	}
	if got := count("hello"); got != "44" {
		t.Fatalf("production count %q after the preview, want 44", got)
	}
	dup := b.ok("projects", "duplicate", "hello", "hello-copy")
	if dup["status"] != "done" || dup["healthy"] != true {
		t.Fatalf("duplicate: %v", dup)
	}
	if got := count("hello-copy"); got != "45" {
		t.Fatalf("the copy's count %q, want 45 (production's folder came along)", got)
	}
	if got := count("hello"); got != "45" {
		t.Fatalf("production count %q, want 45: the copy writes its own folder", got)
	}
	for i := 0; ; i++ { // measured in the background
		use := b.ok("projects", "usage", "hello")
		disk, _ := use["disk"].(map[string]any)
		if n, _ := disk["filesBytes"].(float64); n >= 2 {
			break
		}
		if i > 30 {
			t.Fatalf("usage counts no disk folder: %v", use["disk"])
		}
		time.Sleep(2 * time.Second)
	}
	phase("preview+duplicate", p)
	t.Logf("total %s", time.Since(start).Round(time.Second))
}
