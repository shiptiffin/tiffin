package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/edge/switchboard"
	"github.com/shiptiffin/tiffin/internal/manifest"
)

// A static export builds with Railpack but makes no image: the plan's
// deploy stage becomes the built folders alone, written out by BuildKit's
// local exporter, then compressed ahead for the edge.
func TestStaticExportBuildsNoImage(t *testing.T) {
	bin, log := fakeTools(t)
	// buildctl --output type=local,dest=X writes what the plan's files step would.
	writeFiles(t, bin, map[string]string{"railpack": "#!/bin/sh\n" + `while [ $# -gt 0 ]; do if [ "$1" = --plan-out ]; then echo '{"steps":[{"name":"install"},{"name":"build"}],"deploy":{}}' > "$2"; fi; shift; done` + "\n",
		"buildctl": "#!/bin/sh\nprintf -- '--- buildctl\\n' >> " + log + `; for a in "$@"; do printf '%s\n' "$a" >> ` + log + `; case "$a" in type=local,dest=*) d="${a#type=local,dest=}"; mkdir -p "$d/tiffin-out/0/_next" && printf '<html>%0500d</html>' 0 > "$d/tiffin-out/0/index.html" && printf 'x%.0s' $(seq 1 3000) > "$d/tiffin-out/0/_next/app-B1x9Qa2c.js";; esac; done` + "\n"})
	b := &boxBuilder{eng: newFakeEngine(), binDir: bin, staticDir: t.TempDir(), memoryMB: 2048}
	req := buildReq(t, manifest.App{Framework: manifest.FrameworkNext}, map[string]string{
		"package.json":   `{"dependencies":{"next":"16.3.8"},"scripts":{"build":"next build"}}`,
		"next.config.ts": `export default { output: "export" }`,
	})
	req.Export = true
	res, err := b.Build(context.Background(), req)
	if err != nil {
		t.Fatalf("%v\n%s", err, readCalls(t, log))
	}
	if res.Image != "" || res.StaticRoot == "" {
		t.Fatalf("result: %+v", res)
	}
	for _, f := range []string{"index.html", "_next/app-B1x9Qa2c.js", "_next/app-B1x9Qa2c.js.zst", "_next/app-B1x9Qa2c.js.gz"} {
		if !exists(filepath.Join(res.StaticRoot, f)) {
			t.Errorf("served files lack %s", f)
		}
	}
	calls := strings.Split(readCalls(t, log), "\n")
	if !slices.Contains(calls, "type=local,dest="+filepath.Join(req.WorkDir, "files")) || slices.ContainsFunc(calls, func(a string) bool { return strings.HasPrefix(a, "type=image") }) {
		t.Errorf("buildctl output:\n%s", strings.Join(calls, "\n"))
	}
	logText := req.Log.(*bytes.Buffer).String()
	if !strings.Contains(logText, "only out comes out, no image") || !strings.Contains(logText, "compressed 1 text file ahead") {
		t.Errorf("build log:\n%s", logText)
	}
}

func TestFilesOnlyPlan(t *testing.T) {
	plan := filepath.Join(t.TempDir(), "plan.json")
	writeFiles(t, filepath.Dir(plan), map[string]string{"plan.json": `{"steps":[{"name":"install"},{"name":"build","caches":["next"]},{"name":"packages:apt:runtime"}],
		"deploy":{"base":{"step":"packages:apt:runtime"},"inputs":[{"step":"build","include":["."]}],"startCommand":"true"},"secrets":["A"]}`})
	if err := filesOnlyPlan(plan, []string{"site/out", "it's"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(plan)
	var p struct {
		Steps []struct {
			Name     string           `json:"name"`
			Inputs   []map[string]any `json:"inputs"`
			Commands []map[string]any `json:"commands"`
		} `json:"steps"`
		Deploy  map[string]any `json:"deploy"`
		Secrets []string       `json:"secrets"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	last := p.Steps[len(p.Steps)-1]
	if len(p.Steps) != 4 || last.Name != filesStep || last.Inputs[0]["step"] != "build" || len(p.Secrets) != 1 {
		t.Fatalf("steps: %s", raw)
	}
	if cmd := last.Commands[0]["cmd"].(string); !strings.HasPrefix(cmd, "sh -c '") || !strings.HasSuffix(cmd, ` sh 'site/out' 'it'\''s'`) {
		t.Errorf("cmd: %s", cmd)
	}
	dep, _ := json.Marshal(p.Deploy)
	if string(dep) != `{"base":{},"inputs":[{"include":["/tiffin-out"],"step":"tiffin:files"}]}` {
		t.Errorf("deploy: %s", dep)
	}
	writeFiles(t, filepath.Dir(plan), map[string]string{"plan.json": `{"steps":[{"name":"install"}],"deploy":{}}`})
	if err := filesOnlyPlan(plan, []string{"out"}); err == nil {
		t.Error("a plan with no build step must fail")
	}
}

// Static builds on Bun keep their app's package cache and framework caches
// in a folder of the box's, between builds.
func TestStaticBuildCaches(t *testing.T) {
	bin, log := fakeTools(t)
	cache := t.TempDir()
	b := &boxBuilder{eng: newFakeEngine(), binDir: bin, staticDir: t.TempDir(), cacheDir: cache, memoryMB: 512}
	spec := manifest.App{Framework: manifest.FrameworkStatic, Install: "true", Build: "mkdir -p dist dist/.vite && echo built > dist/index.html && echo '{}' > dist/.vite/manifest.json"}
	req := buildReq(t, spec, map[string]string{"package.json": `{"scripts":{"build":"vite build"}}`})
	res, err := b.Build(context.Background(), req)
	if err != nil {
		t.Fatalf("%v\n%s", err, readCalls(t, log))
	}
	calls := strings.Split(readCalls(t, log), "\n")
	appCache := filepath.Join(cache, "shop", "web")
	for _, want := range []string{"BUN_INSTALL_CACHE_DIR=/tiffin-cache/bun", filepath.Join(appCache, "bun") + ":/tiffin-cache/bun",
		filepath.Join(appCache, "0") + ":/app/node_modules/.astro", filepath.Join(appCache, "1") + ":/app/node_modules/.vite"} {
		if !slices.Contains(calls, want) {
			t.Errorf("nerdctl run lacks %q:\n%s", want, strings.Join(calls, "\n"))
		}
	}
	if exists(filepath.Join(res.StaticRoot, ".vite")) {
		t.Error(".vite/ is served")
	}
	if !strings.Contains(req.Log.(*bytes.Buffer).String(), "build cache: cold") {
		t.Errorf("log: %s", req.Log.(*bytes.Buffer).String())
	}
	// The next build finds it warm; a workspace app mounts in its folder.
	req = buildReq(t, spec, map[string]string{"package.json": `{"workspaces":["site"]}`, "site/package.json": `{"scripts":{"build":"vite build"}}`})
	req.Dir, req.Deploy.ID = "site", "dep_2"
	req.Spec.Build = "mkdir -p dist && echo built > dist/index.html"
	if _, err := b.Build(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if calls := readCalls(t, log); !strings.Contains(calls, filepath.Join(appCache, "0")+":/app/site/node_modules/.astro\n") || !strings.Contains(req.Log.(*bytes.Buffer).String(), "build cache: warm") {
		t.Errorf("workspace build:\n%s\n%s", calls, req.Log.(*bytes.Buffer).String())
	}
}

// A new release of a static site keeps the previous release's hashed
// files it lacks (and their compressed copies) for a day after that
// release stopped being live, so tabs opened before a deploy keep loading
// their chunks.
func TestKeepHashedFiles(t *testing.T) {
	prev, root := t.TempDir(), t.TempDir()
	writeFiles(t, prev, map[string]string{
		"index.html": "old", "assets/index-B1x9Qa2c.js": "old chunk", "assets/index-B1x9Qa2c.js.zst": "z", "assets/index-B1x9Qa2c.js.gz": "g",
		"assets/lazy-C2y8Rb3d.js": "carried", "assets/ancient-D3z7Sc4e.js": "too old", "robots.txt": "old", "assets/logo.svg": "named",
	})
	writeFiles(t, root, map[string]string{"index.html": "new", "assets/index-E4a6Td5f.js": "new chunk"})
	now := time.Now()
	carried := map[string]time.Time{"assets/lazy-C2y8Rb3d.js": now.Add(-time.Hour), "assets/ancient-D3z7Sc4e.js": now.Add(-assetsKept - time.Minute)}
	kept := map[string]time.Time{}
	if n := keepHashedFiles(prev, root, carried, now, kept); n != 2 {
		t.Fatalf("kept %d: %v", n, kept)
	}
	for f, want := range map[string]string{"index.html": "new", "assets/index-B1x9Qa2c.js": "old chunk", "assets/index-B1x9Qa2c.js.zst": "z", "assets/lazy-C2y8Rb3d.js": "carried"} {
		if got, _ := os.ReadFile(filepath.Join(root, f)); string(got) != want {
			t.Errorf("%s: %q, want %q", f, got, want)
		}
	}
	for _, f := range []string{"assets/ancient-D3z7Sc4e.js", "robots.txt", "assets/logo.svg"} {
		if exists(filepath.Join(root, f)) {
			t.Errorf("%s was kept", f)
		}
	}
	if !kept["assets/index-B1x9Qa2c.js"].Equal(now) || !kept["assets/lazy-C2y8Rb3d.js"].Equal(now.Add(-time.Hour)) {
		t.Errorf("end times: %v", kept)
	}
}

// Through deploys: v2 of a static site still answers v1's chunk.
func TestStaticDeployKeepsPreviousChunks(t *testing.T) {
	h := newHarness(t)
	h.deploy("site", "", map[string]string{"index.html": "v1", "assets/app-B1x9Qa2c.js": "v1 chunk"})
	d2 := h.deploy("site", "", map[string]string{"index.html": "v2", "assets/app-C2y8Rb3d.js": "v2 chunk"})
	if got, _ := os.ReadFile(filepath.Join(d2.StaticRoot, "assets", "app-B1x9Qa2c.js")); string(got) != "v1 chunk" {
		t.Fatalf("v1 chunk in v2: %q\n%s", got, h.buildLog("site", d2.ID))
	}
	if !exists(filepath.Join(h.r.workDir(d2), carriedFile)) {
		t.Error("no record of the kept files")
	}
}

// Prerendered pages of a framework whose server answers them from its
// files are answered by the box, also while the app sleeps.
func TestPrerenderedPagesWithoutWaking(t *testing.T) {
	h := newHarness(t)
	h.sleepy("1h")
	d := h.deploy("api", "", map[string]string{
		"package.json":                           `{"dependencies":{"@tanstack/react-start":"1"}}`,
		".output/public/about/index.html":        "<p>about</p>",
		".output/public/docs.html":               "<p>docs</p>",
		".output/public/assets/main-B1x9Qa2c.js": "js",
		".output/public/_shell.html":             "shell",
		".output/public/.vite/manifest.json":     "{}",
	})
	if d.Status != StatusLive || len(d.Assets) != 2 || !d.Assets[0].Pages {
		t.Fatalf("deploy: %s %+v", d.Status, d.Assets)
	}
	h.asleep("api")
	host := "shop.tiffin.localhost"
	runs := h.eng.runs
	for path, want := range map[string]string{"/api/about": "<p>about</p>", "/api/about/": "<p>about</p>", "/api/docs": "<p>docs</p>", "/api/about/index.html": "<p>about</p>"} {
		if code, body := h.get(host, path); code != 200 || body != want {
			t.Errorf("%s: %d %q", path, code, body)
		}
	}
	if st := h.state("api", ""); !st.Sleeping || h.eng.runs != runs {
		t.Fatalf("a prerendered page woke the app: %+v", st)
	}
	// Anything else is the app's: it wakes for it.
	for _, p := range []string{"/api/_shell", "/api/.vite/manifest.json"} {
		if _, body := h.get(host, p); !strings.Contains(body, "greeting=") {
			t.Errorf("%s: %q, want the app's answer", p, body)
		}
	}
	pages := map[string]string{}
	raw, _ := os.ReadFile(filepath.Join(h.r.assetsDir("shop", "api", ""), d.ID, switchboard.PagesFile))
	_ = json.Unmarshal(raw, &pages)
	if len(pages) != 3 || pages["/docs"] != "docs.html" {
		t.Errorf("pages: %v", pages)
	}
}

func TestMoveInto(t *testing.T) {
	a, b := t.TempDir(), filepath.Join(t.TempDir(), "www")
	writeFiles(t, a, map[string]string{"x/1.txt": "a1", "same.txt": "a"})
	if err := moveInto(a, b); err != nil {
		t.Fatal(err)
	}
	c := t.TempDir()
	writeFiles(t, c, map[string]string{"x/2.txt": "c2", "same.txt": "c", "y.txt": "y"})
	if err := moveInto(c, b); err != nil {
		t.Fatal(err)
	}
	for f, want := range map[string]string{"x/1.txt": "a1", "x/2.txt": "c2", "same.txt": "a", "y.txt": "y"} {
		if got, _ := os.ReadFile(filepath.Join(b, f)); string(got) != want {
			t.Errorf("%s: %q, want %q", f, got, want)
		}
	}
}

func TestOwnDomainURL(t *testing.T) {
	url := func(h string) string { return "https://" + h }
	for _, c := range []struct {
		routes []string
		want   string
	}{
		{[]string{"website", "shiptiffin.com"}, "https://shiptiffin.com"},
		{[]string{"shop", "shop.shiptiffin.app", "Example.com/store/"}, "https://example.com/store"},
		{[]string{"website"}, "fallback"},
		{[]string{"shiptiffin.app/docs"}, "fallback"},
	} {
		if got := ownDomainURL(url, "shiptiffin.app", c.routes, "fallback"); got != c.want {
			t.Errorf("%v: %s, want %s", c.routes, got, c.want)
		}
	}
}
