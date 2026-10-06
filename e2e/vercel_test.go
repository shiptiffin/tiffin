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

// TestVercel deploys apps the way they run on Vercel, with no changes for
// the box (e2e/testdata/vercel):
//
//   - web: a Next.js static export (output: "export") in a pnpm workspace,
//     using a workspace package → built with next build at the workspace's
//     top, served by the edge with no container; clean URLs and 404.html
//   - mapi: a server app in the same workspace, using the same package →
//     installed at the top, started in its folder
//   - docs: a static site with a vercel.json → its headers win over the
//     edge's defaults, redirects keep the query, rewrites, cleanUrls
//   - api: a Bun app with a vercel.json cron → listed with its origin, and
//     called with GET, CRON_SECRET as a bearer token and vercel-cron/1.0;
//     its headers and redirects apply at the edge too
func TestVercel(t *testing.T) {
	start := time.Now()
	phase := phaseLogger(t)
	b := newCLIBox(t, "vercel", "vx")
	phase("up", start)

	root := filepath.Join(b.dir, "vx")
	if out, err := exec.Command("cp", "-R", filepath.Join(RepoRoot(), "e2e", "testdata", "vercel"), root).CombinedOutput(); err != nil {
		t.Fatalf("copy fixtures: %v %s", err, out)
	}
	cfg := `{"project":"vx","apps":{
	  "web":{"framework":"next","path":"mono/apps/web","routes":["vx-web"]},
	  "mapi":{"framework":"bun","path":"mono/apps/api","routes":["vx-mapi"]},
	  "docs":{"framework":"static","path":"docs","routes":["vx-docs"]},
	  "api":{"framework":"bun","path":"api","routes":["vx-api"]}}}`
	if err := os.WriteFile(filepath.Join(root, "tiffin.config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := b.ok("plan", root)
	hash, _ := plan["hash"].(string)
	b.ok("apply", root, "--confirm", hash, "-m", "e2e: vercel apps")
	b.waitReady("app/web", "app/mapi", "app/docs", "app/api")
	b.ok("secrets", "set", "vx", "CRON_SECRET", "--value", "e2e-cron-secret")

	c := b.https()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	type want struct {
		path, code, body, loc string
		hdr                   map[string]string
	}
	check := func(host string, ws []want) {
		t.Helper()
		for _, w := range ws {
			code, h, body := b.get(c, "GET", b.url(host)+w.path, nil)
			bad := fmt.Sprint(code) != w.code || !strings.Contains(body, w.body) || h.Get("Location") != w.loc
			for k, v := range w.hdr {
				bad = bad || h.Get(k) != v
			}
			if bad {
				t.Errorf("%s%s: %d Location %q body %.200q headers %v; want %s %q %q %v", host, w.path, code, h.Get("Location"), body, h, w.code, w.loc, w.body, w.hdr)
			}
		}
	}
	buildLog := func(app, id string) string {
		return fmt.Sprint(b.ok("deploys", "build-log", "vx", app, id)["text"])
	}

	// ---- web: a Next.js static export in a pnpm workspace ----
	p := time.Now()
	d := deployArgs(t, b, root, "--app", "web")
	phase("web deploy", p)
	log := buildLog("web", d.ID)
	t.Logf("web build log (Tiffin lines):\n%s", grepLines(log, "==> workspace", "==> Next.js", "==> serving", "==> planning"))
	for _, s := range []string{"==> workspace: the app is apps/web/", "Next.js static export", "==> serving"} {
		if !strings.Contains(log, s) {
			t.Errorf("web build log lacks %q", s)
		}
	}
	rec := b.ok("deploys", "get", "vx", "web", d.ID)
	if rec["framework"] != "next+export" || rec["dir"] != "apps/web" || rec["image"] != nil || rec["staticRoot"] == nil {
		t.Errorf("web deploy: framework %v dir %v image %v staticRoot %v", rec["framework"], rec["dir"], rec["image"], rec["staticRoot"])
	}
	if names := b.inBox("sudo nerdctl --namespace tiffin ps -a --format '{{.Names}}'"); strings.Contains(names, "tf.vx.web.") {
		t.Errorf("a static export runs a container: %s", names)
	}
	check("vx-web", []want{
		{path: "/", code: "200", body: "hello from a workspace package"},
		{path: "/about", code: "308", loc: "/about/"},
		{path: "/about/", code: "200", body: "vx-about"},
		{path: "/legacy", code: "200", body: "vx-legacy"},
		{path: "/nope", code: "404", body: "vx-missing"},
	})

	// ---- mapi: a server app in the same workspace ----
	p = time.Now()
	d = deployArgs(t, b, root, "--app", "mapi")
	phase("mapi deploy", p)
	if log = buildLog("mapi", d.ID); !strings.Contains(log, "==> workspace: the app is apps/api/") {
		t.Errorf("mapi build log lacks the workspace:\n%s", log)
	}
	check("vx-mapi", []want{{path: "/x", code: "200", body: "vx-mono-api /x hello from a workspace package"}})

	// ---- docs: a static site with vercel.json ----
	p = time.Now()
	d = deployArgs(t, b, root, "--app", "docs")
	phase("docs deploy", p)
	log = buildLog("docs", d.ID)
	t.Logf("docs build log (Tiffin lines):\n%s", grepLines(log, "==> vercel.json"))
	if !strings.Contains(log, "==> vercel.json: 2 header rules, 1 redirect, 1 rewrite, cleanUrls") || !strings.Contains(log, "not used by the box: regions") {
		t.Errorf("docs build log does not say what vercel.json gave:\n%s", log)
	}
	csp := "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; frame-ancestors 'self'"
	check("vx-docs", []want{
		{path: "/", code: "200", body: "vx-docs-home", hdr: map[string]string{"Content-Security-Policy": csp, "X-Frame-Options": "SAMEORIGIN",
			"Cross-Origin-Opener-Policy": "same-origin", "Permissions-Policy": "camera=(self), microphone=(), geolocation=()", "X-Content-Type-Options": "nosniff"}},
		{path: "/about", code: "200", body: "vx-docs-about"},
		{path: "/about.html", code: "308", loc: "/about"},
		{path: "/old/hello?ref=1", code: "307", loc: "/new/hello?ref=1"},
		{path: "/app/settings/billing", code: "200", body: "vx-docs-app"},
		{path: "/assets/app.js", code: "200", body: "vx", hdr: map[string]string{"Cache-Control": "public, max-age=600"}},
		{path: "/nope", code: "404", body: "vx-docs-missing"},
		{path: "/vercel.json", code: "404"},
	})

	// ---- api: a Bun app with a vercel.json cron ----
	p = time.Now()
	d = deployArgs(t, b, root, "--app", "api")
	phase("api deploy", p)
	log = buildLog("api", d.ID)
	t.Logf("api build log (Tiffin lines):\n%s", grepLines(log, "==> vercel.json"))
	if !strings.Contains(log, "==> vercel.json crons: api-cron-ping (GET /api/cron/ping, 0 5 * * * UTC)") || !strings.Contains(log, "rewrites (static sites only") {
		t.Errorf("api build log does not say what vercel.json gave:\n%s", log)
	}
	check("vx-api", []want{
		{path: "/x", code: "200", body: "vx-api /x", hdr: map[string]string{"X-Api": "vx"}},
		{path: "/v1/a/b", code: "308", loc: "/v2/a/b"},
	})
	var cron map[string]any
	for _, cr := range b.list("queue", "crons", "list", "vx") {
		if cr["name"] == "api-cron-ping" {
			cron = cr
		}
	}
	if cron == nil || cron["origin"] != "vercel.json" || cron["method"] != "GET" || cron["target"] != "api:/api/cron/ping" || cron["timezone"] != "UTC" {
		t.Fatalf("vercel.json cron: %v", cron)
	}
	job, _ := b.ok("queue", "crons", "trigger", "vx", "api-cron-ping")["job"].(string)
	deadline := time.Now().Add(2 * time.Minute)
	for {
		j := b.ok("queue", "jobs", "get", "vx", job)
		if j["state"] == "completed" {
			break
		}
		if time.Now().After(deadline) || j["state"] == "dead" {
			t.Fatalf("cron job %s: %v (%v)", job, j["state"], j["lastError"])
		}
		time.Sleep(time.Second)
	}
	_, _, body := b.get(c, "GET", b.url("vx-api")+"/seen", nil)
	var seen []map[string]any
	_ = json.Unmarshal([]byte(body), &seen)
	if len(seen) != 1 || seen[0]["method"] != "GET" || seen[0]["auth"] != "Bearer e2e-cron-secret" || seen[0]["ua"] != "vercel-cron/1.0" {
		t.Fatalf("the cron path saw %s", body)
	}
	t.Logf("cron call: %s", body)
	phase("total", start)
}
