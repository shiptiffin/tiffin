//go:build e2e

package e2e

import (
	"bytes"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestNext deploys templates/hello-next (two instances, Valkey) with the
// box's Next.js adapter, then redeploys a change:
//
//	the page renders with the deploy's deploymentId and is not compressed by
//	Next.js → its chunks are served immutable → revalidateTag reaches both
//	instances (Valkey) → next/image optimizes (sharp on Bun) into the
//	box's image cache → a redeploy: a chunk only the old release had still
//	loads, a Server Action posted from a page of the old release runs on the
//	new one (stable key), the image cache survives, and the old release's
//	after() callback finishes during its shutdown.
func TestNext(t *testing.T) {
	start := time.Now()
	phase := phaseLogger(t)
	b := newCLIBox(t, "next", "hello-next")
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
	write("app/action/page.tsx", actionPage)
	write("app/action/badge.tsx", strings.Replace(badge, "VERSION", "release 1", 1))
	write("app/api/after/route.ts", afterRoute)
	img := image.NewRGBA(image.Rect(0, 0, 256, 256))
	for x := 0; x < 256; x++ {
		for y := 0; y < 256; y++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), uint8(x ^ y), 255})
		}
	}
	var pngBuf bytes.Buffer
	_ = png.Encode(&pngBuf, img)
	write("public/e2e.png", pngBuf.String())

	plan := b.ok("plan", app)
	hash, _ := plan["hash"].(string)
	b.ok("apply", app, "--confirm", hash, "-m", "e2e: hello-next")
	b.waitReady("app/web", "service/valkey")

	// ---- first deploy ----
	p := time.Now()
	d1 := deployArgs(t, b, app)
	phase("first deploy", p)
	t.Logf("first deploy: build %.1fs, total %.1fs", d1.BuildSecs, d1.TotalSecs)
	log1 := b.ok("deploys", "build-log", "hello-next", "web", d1.ID)
	t.Logf("build log (Tiffin lines):\n%s", grepLines(fmt.Sprint(log1["text"]), "==> Next.js", "==> client assets", "Applying modifyConfig"))
	c := b.https()
	site := b.url("hello-next")

	// ---- the page: deploymentId, no compression from Next.js ----
	code, _, home1 := b.get(c, "GET", site+"/", nil)
	if code != 200 || !strings.Contains(home1, `data-dpl-id="`+d1.ID+`"`) {
		t.Fatalf("GET /: %d, want data-dpl-id=%s\n%s", code, d1.ID, head(home1))
	}
	port := func() string {
		st := b.ok("apps", "status", "hello-next", "web")
		prod, _ := st["production"].(map[string]any)
		ins, _ := prod["instances"].([]any)
		in, _ := ins[0].(map[string]any)
		return fmt.Sprint(in["port"])
	}
	if hdr := b.inBox(`curl -s -D - -o /dev/null -H 'Accept-Encoding: gzip' http://127.0.0.1:` + port() + `/`); strings.Contains(strings.ToLower(hdr), "content-encoding") {
		t.Fatalf("Next.js compressed its response (compress must be off):\n%s", hdr)
	}

	// ---- chunks: served by the box, immutable ----
	chunks1 := chunkURLs(home1)
	if len(chunks1) == 0 {
		t.Fatalf("no /_next/static chunks in the page:\n%s", head(home1))
	}
	code, hdr, _ := b.get(c, "GET", site+chunks1[0], nil)
	if code != 200 || hdr.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("chunk %s: %d %q", chunks1[0], code, hdr.Get("Cache-Control"))
	}
	if n := b.inBox(`sudo find /var/lib/tiffin/runtime/assets/hello-next/web/prod/` + d1.ID + `/www/_next/static -type f | wc -l`); n == "0" {
		t.Fatal("the box has no copy of the build's client assets")
	}

	// ---- revalidateTag reaches both instances through Valkey ----
	p = time.Now()
	cachedPerInstance := func() map[string]string {
		seen := map[string]string{}
		for i := 0; i < 20 && len(seen) < 2; i++ {
			_, _, body := b.get(c, "GET", site+"/", nil)
			seen[match(body, `id="instance">([^<]*)<`)] = match(body, `id="cached">([^<]*)<`)
		}
		if len(seen) < 2 {
			t.Fatalf("requests never reached both instances: %v", seen)
		}
		return seen
	}
	before := cachedPerInstance()
	if before["0"] != before["1"] {
		t.Fatalf("instances do not share the cache: %v", before)
	}
	if code, _, body := b.get(c, "POST", site+"/api/revalidate", nil); code != 200 {
		t.Fatalf("revalidate: %d %s", code, body)
	}
	after := cachedPerInstance()
	if after["0"] != after["1"] || after["0"] == before["0"] {
		t.Fatalf("revalidateTag did not reach both instances: before %v, after %v", before, after)
	}
	phase("shared cache", p)

	// ---- next/image: sharp on Bun, cached in the box's directory ----
	imgURL := site + "/_next/image?url=%2Fe2e.png&w=64&q=75"
	getImage := func() http.Header {
		req, _ := http.NewRequest("GET", imgURL, nil)
		req.Header.Set("Accept", "image/webp,image/*")
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/webp" {
			t.Fatalf("image: %d %q\n%s", res.StatusCode, res.Header.Get("Content-Type"),
				b.inBox(`sudo sh -c 'tail -n 30 $(ls -t /var/lib/tiffin/logs/apps/hello-next/web/prod/*.log | head -2)'`))
		}
		return res.Header
	}
	getImage()
	if n := b.inBox(`sudo find /var/lib/tiffin/runtime/next-cache/hello-next/web/prod/images -type f | wc -l`); n == "0" {
		t.Fatal("no optimized image in the box's image cache directory")
	}

	// ---- an old tab: the action page and the home page of release 1 ----
	_, _, action1 := b.get(c, "GET", site+"/action", nil)
	form := formFields(t, action1)
	oldChunks := chunkURLs(action1)
	b.get(c, "GET", site+"/api/after", nil) // schedules an after() callback that outlives the request

	// ---- redeploy a change ----
	p = time.Now()
	page := filepath.Join(app, "app", "page.tsx")
	raw, _ := os.ReadFile(page)
	write("app/page.tsx", strings.Replace(string(raw), "Hello from Next.js on Tiffin", "Hello again from Next.js on Tiffin", 1))
	write("app/action/badge.tsx", strings.Replace(badge, "VERSION", "release 2", 1)) // a new client chunk
	d2 := deployArgs(t, b, app)
	phase("redeploy", p)
	t.Logf("redeploy: build %.1fs, total %.1fs", d2.BuildSecs, d2.TotalSecs)
	_, _, home2 := b.get(c, "GET", site+"/", nil)
	if !strings.Contains(home2, `data-dpl-id="`+d2.ID+`"`) || !strings.Contains(home2, "Hello again") {
		t.Fatalf("after the redeploy: %s", head(home2))
	}

	// A chunk only release 1 had: the app no longer has it, the box still serves it.
	_, _, action2 := b.get(c, "GET", site+"/action", nil)
	var gone string
	for _, u := range oldChunks {
		if !strings.Contains(action2, u) {
			if code := b.inBox(`curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:` + port() + u); code == "404" {
				gone = u
				break
			}
		}
	}
	if gone == "" {
		t.Fatalf("no chunk of release 1 is missing from release 2 (chunks %v)", oldChunks)
	}
	if code, hdr, _ := b.get(c, "GET", site+gone, nil); code != 200 || !strings.Contains(hdr.Get("Cache-Control"), "immutable") {
		t.Fatalf("old chunk %s after the redeploy: %d %q", gone, code, hdr.Get("Cache-Control"))
	}
	t.Logf("old chunk %s: 404 from the new release, 200 from the box", gone)

	// A Server Action posted from release 1's page (no JS: the form as it was) runs on release 2.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, f := range form {
		v := f[1]
		if f[0] == "msg" {
			v = "from-old-tab"
		}
		_ = mw.WriteField(f[0], v)
	}
	mw.Close()
	req, _ := http.NewRequest("POST", site+"/action", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", site)
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	want := fmt.Sprintf("from-old-tab (form from %s, run on %s)", d1.ID, d2.ID)
	if res.StatusCode != 200 {
		t.Fatalf("server action from the old page: %d", res.StatusCode)
	}
	ok := false
	for i := 0; i < 10 && !ok; i++ { // the result lives in the instance that ran it
		_, _, b2 := b.get(c, "GET", site+"/action", nil)
		ok = strings.Contains(b2, want)
	}
	if !ok {
		t.Fatalf("the old page's server action did not run on the new release (want %q)", want)
	}

	// The image cache outlived the release.
	if h := getImage(); h.Get("X-Nextjs-Cache") != "HIT" {
		t.Fatalf("image after the redeploy: X-Nextjs-Cache %q, want HIT", h.Get("X-Nextjs-Cache"))
	}

	// Release 1's after() callback (15 s) finished while it shut down.
	deadline := time.Now().Add(60 * time.Second)
	for !strings.Contains(b.inBox(`sudo grep -rh "after done" /var/lib/tiffin/logs/apps/hello-next/web/prod/ || true`), "after done "+d1.ID) {
		if time.Now().After(deadline) {
			t.Fatal("release 1's after() callback never finished: the old instance was stopped too soon")
		}
		time.Sleep(2 * time.Second)
	}
	phase("skew checks", p)
	t.Logf("TOTAL %s", time.Since(start).Round(time.Second))
}

const actionPage = `import Image from "next/image";
import Badge from "./badge";

export const dynamic = "force-dynamic";

let last = "";

export default function ActionPage() {
  const from = process.env.TIFFIN_DEPLOY ?? "local";
  async function echo(fd: FormData) {
    "use server";
    last = ` + "`${fd.get(\"msg\")} (form from ${from}, run on ${process.env.TIFFIN_DEPLOY ?? \"local\"})`" + `;
  }
  return (
    <main>
      <form action={echo}>
        <input name="msg" defaultValue="hi" />
        <button type="submit">Send</button>
      </form>
      <p id="last">{last}</p>
      <Image src="/e2e.png" alt="" width={64} height={64} />
      <Badge />
    </main>
  );
}
`

// badge is a client component: changing it changes the page's chunks.
const badge = `"use client";

import { useState } from "react";

export default function Badge() {
  const [n, setN] = useState(0);
  return <button onClick={() => setN(n + 1)}>VERSION: {n}</button>;
}
`

const afterRoute = `import { after } from "next/server";

export const dynamic = "force-dynamic";

// Work that outlives the response: it waits for the shutdown signal, then
// takes 15 seconds more (Next.js waits for it before exiting).
export function GET() {
  const deploy = process.env.TIFFIN_DEPLOY;
  after(async () => {
    await new Promise((r) => process.once("SIGTERM", r));
    await new Promise((r) => setTimeout(r, 15_000));
    console.log("after done " + deploy);
  });
  return new Response("scheduled");
}
`

var (
	chunkRe = regexp.MustCompile(`/_next/static/[^"?\s]+\.js`)
	inputRe = regexp.MustCompile(`<input[^>]*>`)
	attrRe  = regexp.MustCompile(`([\w-]+)="([^"]*)"`)
)

func chunkURLs(page string) []string {
	seen := map[string]bool{}
	var out []string
	for _, u := range chunkRe.FindAllString(page, -1) {
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

// formFields returns the inputs (name, value) of the page's first form.
func formFields(t *testing.T, page string) [][2]string {
	t.Helper()
	i, j := strings.Index(page, "<form"), strings.Index(page, "</form>")
	if i < 0 || j < i {
		t.Fatalf("no form in the page:\n%s", head(page))
	}
	var out [][2]string
	for _, in := range inputRe.FindAllString(page[i:j], -1) {
		attrs := map[string]string{}
		for _, m := range attrRe.FindAllStringSubmatch(in, -1) {
			attrs[m[1]] = html.UnescapeString(m[2])
		}
		if attrs["name"] != "" {
			out = append(out, [2]string{attrs["name"], attrs["value"]})
		}
	}
	return out
}

func match(s, re string) string {
	if m := regexp.MustCompile(re).FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

func head(s string) string {
	if len(s) > 1500 {
		return s[:1500] + "…"
	}
	return s
}

func grepLines(s string, subs ...string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		for _, sub := range subs {
			if strings.Contains(l, sub) {
				out = append(out, l)
				break
			}
		}
	}
	return strings.Join(out, "\n")
}
