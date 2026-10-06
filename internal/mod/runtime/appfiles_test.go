package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/edge"
)

func TestVercelJSONDeploys(t *testing.T) {
	h := newHarness(t)
	routeFor := func(host, prefix string) *edge.Route {
		for i, r := range h.edge.routes {
			if r.Host == host && r.PathPrefix == prefix {
				return &h.edge.routes[i]
			}
		}
		t.Fatalf("no route %s%s in %+v", host, prefix, h.edge.routes)
		return nil
	}

	// A static site: every rule, no crons (nothing to call), vercel.json not served.
	d := h.deploy("site", "", map[string]string{
		"index.html": "home",
		"vercel.json": `{"cleanUrls": true, "regions": ["iad1"],
			"headers": [{"source": "/(.*)", "headers": [{"key": "X-Frame-Options", "value": "SAMEORIGIN"}]}],
			"rewrites": [{"source": "/app/(.*)", "destination": "/index.html"}],
			"crons": [{"path": "/api/cron", "schedule": "0 1 * * *"}]}`,
	})
	if d.Status != StatusLive || d.Vercel == nil {
		t.Fatalf("site: %s %s %+v", d.Status, d.Error, d.Vercel)
	}
	v := d.Vercel
	if v.Crons != nil || v.Rules == nil || !v.Rules.CleanURLs || len(v.Rules.Rewrites) != 1 ||
		!strings.Contains(strings.Join(v.Ignored, ","), "regions") || !strings.Contains(strings.Join(v.Ignored, ","), "crons (a static site") {
		t.Errorf("site vercel.json: %+v %+v", v, v.Rules)
	}
	if r := routeFor("shop.tiffin.localhost", ""); r.Rules == nil || len(r.Rules.Headers) != 1 || !r.Rules.CleanURLs {
		t.Errorf("site route rules: %+v", r.Rules)
	}
	if code, _ := h.get("shop.tiffin.localhost", "/vercel.json"); code != 404 {
		t.Errorf("vercel.json is served: %d", code)
	}
	log := h.buildLogText(d)
	for _, want := range []string{"==> vercel.json: 1 header rule, 1 rewrite, cleanUrls", "==> vercel.json: not used by the box: regions, crons (a static site"} {
		if !strings.Contains(log, want) {
			t.Errorf("build log lacks %q:\n%s", want, log)
		}
	}

	// An app: headers and redirects at the edge, crons kept for the queue,
	// static-only rules left out.
	d = h.deploy("api", "", map[string]string{
		"index.ts": "v1",
		"vercel.json": `{"buildCommand": "bun run build:all",
			"crons": [{"path": "/api/cron/digest", "schedule": "0 5 * * *"}],
			"headers": [{"source": "/(.*)", "headers": [{"key": "X-Api", "value": "1"}]}],
			"redirects": [{"source": "/old", "destination": "/new"}],
			"rewrites": [{"source": "/(.*)", "destination": "/"}], "trailingSlash": false}`,
	})
	if d.Status != StatusLive {
		t.Fatalf("api: %s %s", d.Status, d.Error)
	}
	v = d.Vercel
	if v == nil || len(v.Crons) != 1 || v.Crons[0].Name != "api-cron-digest" || v.BuildCommand != "bun run build:all" ||
		v.Rules.Rewrites != nil || v.Rules.TrailingSlash != nil || len(v.Rules.Redirects) != 1 {
		t.Fatalf("api vercel.json: %+v", v)
	}
	if r := routeFor("shop.tiffin.localhost", "/api"); r.Rules == nil || len(r.Rules.Headers) != 1 || len(r.Rules.Redirects) != 1 {
		t.Errorf("api route rules: %+v", r.Rules)
	}

	// A broken vercel.json fails the deploy and says why; the live one stays.
	bad := h.deploy("api", "", map[string]string{"index.ts": "v2", "vercel.json": `{"headers": [`})
	if bad.Status != StatusFailed || !strings.Contains(bad.Error, "vercel.json") || !strings.Contains(bad.Hint, "vercel.json") {
		t.Errorf("broken vercel.json: %s %q %q", bad.Status, bad.Error, bad.Hint)
	}
	if h.state("api", "").Live != d.ID {
		t.Errorf("a failed deploy replaced the live one")
	}
}

func (h *harness) buildLogText(d *Deploy) string {
	h.t.Helper()
	text, _ := h.r.readBuildLog(d, 0, 1<<20)
	return string(text)
}

func TestNextExport(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("package.json", `{"dependencies": {"next": "16.3.8"}}`)
	for body, want := range map[string]bool{
		`const c = { output: "export", trailingSlash: true }; export default c;`:  true,
		"export default { output: 'export' }":                                     true,
		"module.exports = {\n  output:`export`,\n}":                               true,
		`export default { output: "standalone" }`:                                 false,
		"export default {\n  // output: 'export',\n}":                             false,
		"export default { /* output: \"export\" */ }":                             false,
		"const site = 'https://example.com'; export default { output: 'export' }": true,
	} {
		_ = os.Remove(filepath.Join(dir, "next.config.mjs"))
		write("next.config.ts", body)
		if got := nextExport(dir); got != want {
			t.Errorf("%q: %v, want %v", body, got, want)
		}
	}
	write("package.json", `{"dependencies": {"react": "19"}}`)
	if nextExport(dir) {
		t.Error("an export without next in package.json")
	}
}

func TestPackageManagerAndDir(t *testing.T) {
	for files, want := range map[string]string{
		"pnpm-lock.yaml":    "pnpm",
		"yarn.lock":         "yarn",
		"package-lock.json": "npm",
		"bun.lock":          "bun",
		"":                  "bun",
	} {
		dir := t.TempDir()
		if files != "" {
			os.WriteFile(filepath.Join(dir, files), nil, 0o644)
		}
		if got := packageManager(dir); got != want {
			t.Errorf("%q: %s, want %s", files, got, want)
		}
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"packageManager": "pnpm@11.1.1"}`), 0o644)
	if got := packageManager(dir); got != "pnpm" {
		t.Errorf("packageManager field: %s", got)
	}
	for in, want := range map[string]string{"": "", ".": "", "apps/web": "apps/web", "/apps/web/": "apps/web", "../x": "!", "a/../../b": "!", "a//b": "!", "a/./b": "!"} {
		got, ok := cleanDir(in)
		if !ok {
			got = "!"
		}
		if got != want {
			t.Errorf("cleanDir(%q) = %q, want %q", in, got, want)
		}
	}
}
