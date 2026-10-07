package runtime

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/mod/runtime/ghapp"
)

func TestGuessFramework(t *testing.T) {
	for _, c := range []struct {
		pkg, framework, unsupported string
		html                        bool
	}{
		{`{"dependencies":{"next":"16"}}`, "next", "", false},
		{`{"devDependencies":{"@sveltejs/kit":"3","vite":"8"},"scripts":{"build":"vite build"}}`, "bun", "", false},
		{`{"devDependencies":{"@sveltejs/kit":"3","@sveltejs/adapter-static":"3","vite":"8"},"scripts":{"build":"vite build"}}`, "static", "", false},
		{`{"devDependencies":{"@react-router/dev":"8","vite":"8"},"scripts":{"build":"react-router build","start":"react-router-serve ./build/server/index.js"}}`, "bun", "", false},
		{`{"dependencies":{"nuxt":"4"},"scripts":{"build":"nuxt build"}}`, "bun", "", false},
		{`{"dependencies":{"nuxt":"4"},"scripts":{"build":"nuxt generate"}}`, "static", "", false},
		{`{"devDependencies":{"@remix-run/dev":"2"},"scripts":{"build":"remix vite:build"}}`, "bun", "Remix", false},
		{`{"dependencies":{"astro":"7","@astrojs/node":"11"},"scripts":{"build":"astro build"}}`, "bun", "", false},
		{`{"dependencies":{"astro":"7","@astrojs/vercel":"9"},"scripts":{"build":"astro build"}}`, "bun", "Astro with the @astrojs/vercel adapter", false},
		{`{"dependencies":{"@tanstack/react-start":"1","nitro":"3"},"scripts":{"build":"vite build","start":"node .output/server/index.mjs"}}`, "bun", "", false},
		{`{"dependencies":{"@tanstack/solid-start":"1"},"scripts":{"build":"vite build"}}`, "bun", "TanStack Start for Solid", false},
		{`{"dependencies":{"astro":"5"},"scripts":{"build":"astro build"}}`, "static", "", false},
		{`{"dependencies":{"react":"19","react-router":"7"},"devDependencies":{"vite":"7"},"scripts":{"build":"vite build"}}`, "static", "", true},
		{`{"dependencies":{"react-scripts":"5"},"scripts":{"build":"react-scripts build","start":"react-scripts start"}}`, "static", "", true},
		{`{"dependencies":{"hono":"4"}}`, "hono", "", false},
		{`{"dependencies":{"express":"5"},"scripts":{"start":"node server.js"}}`, "bun", "", false},
	} {
		g := guessFramework([]byte(c.pkg), c.html)
		if g.Framework != c.framework || g.Unsupported != c.unsupported {
			t.Errorf("%s: got %s/%q (%s), want %s/%q", c.pkg, g.Framework, g.Unsupported, g.Why, c.framework, c.unsupported)
		}
	}
}

// The preset is the framework as people know it, with the starters' ids, so
// a picker can show it and offer the rest.
func TestGuessPreset(t *testing.T) {
	for pkg, want := range map[string]string{
		`{"dependencies":{"next":"16"}}`:                                                                  "nextjs",
		`{"dependencies":{"@tanstack/react-start":"1"}}`:                                                  "tanstack-start",
		`{"dependencies":{"astro":"7"},"scripts":{"build":"astro build"}}`:                                "astro",
		`{"dependencies":{"astro":"7","@astrojs/node":"11"}}`:                                             "astro",
		`{"dependencies":{"react":"19"},"devDependencies":{"vite":"8"},"scripts":{"build":"vite build"}}`: "vite-react",
		`{"dependencies":{"vue":"3"},"devDependencies":{"vite":"8"},"scripts":{"build":"vite build"}}`:    "vite",
		`{"dependencies":{"hono":"4"}}`:                                                                   "hono",
		`{"dependencies":{"express":"5"},"scripts":{"start":"node server.js"}}`:                           "",
		`{"devDependencies":{"@sveltejs/kit":"3"}}`:                                                       "sveltekit",
		`{"dependencies":{"nuxt":"4"},"scripts":{"build":"nuxt build"}}`:                                  "nuxt",
		`{"devDependencies":{"@react-router/dev":"8"}}`:                                                   "react-router",
	} {
		if g := guessFramework([]byte(pkg), false); g.Preset != want {
			t.Errorf("%s: preset %q, want %q", pkg, g.Preset, want)
		}
	}
	// A folder of files with no package.json is plain HTML.
	tree := []ghapp.TreeEntry{{Path: "site/index.html", Type: "blob"}}
	if r := detectRoots(tree, func(string) ([]byte, error) { return nil, os.ErrNotExist }); len(r) != 1 || r[0].Preset != "html" || r[0].Framework != "static" {
		t.Fatalf("plain html: %+v", r)
	}
}

func TestSPAFallback(t *testing.T) {
	dir := t.TempDir()
	req := BuildRequest{SrcDir: dir, Log: io.Discard}
	write := func(name, s string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("package.json", `{"dependencies":{"react-router-dom":"7"}}`)
	if !spaFallback(req, readStaticfile(dir)) {
		t.Error("a client-routed site should fall back to index.html")
	}
	write("Staticfile", "index_fallback: false\n")
	if spaFallback(req, readStaticfile(dir)) {
		t.Error("index_fallback: false must win over the router")
	}
	write("Staticfile", "root: dist\n")
	write("package.json", `{"dependencies":{"react":"19"}}`)
	if spaFallback(req, readStaticfile(dir)) {
		t.Error("no router, no Staticfile: no fallback")
	}
}

func TestSmokeNext(t *testing.T) {
	notFound := http.StatusNotFound
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			return
		}
		w.WriteHeader(notFound)
	}))
	defer srv.Close()
	port, _ := strconv.Atoi(srv.URL[strings.LastIndexByte(srv.URL, ':')+1:])
	in := Instance{Name: "web-1", Port: port}
	spec := &manifest.App{Framework: manifest.FrameworkNext}
	if err := smokeSSR(context.Background(), in, spec, "Next.js", filepath.Join(t.TempDir(), "log")); err != nil {
		t.Fatalf("a healthy app failed the smoke test: %v", err)
	}
	notFound = http.StatusInternalServerError
	err := smokeSSR(context.Background(), in, spec, "Next.js", filepath.Join(t.TempDir(), "log"))
	var he *healthError
	if !errors.As(err, &he) || !strings.Contains(he.msg, "doesn't exist") || !strings.Contains(he.hint, "Node.js") {
		t.Fatalf("a broken not-found page should fail with the Node.js hint, got %v", err)
	}
	spec.Runtime = manifest.RuntimeNode
	if err := smokeSSR(context.Background(), in, spec, "Next.js", filepath.Join(t.TempDir(), "log")); err == nil || strings.Contains(err.(*healthError).hint, "switch the app to Node.js") {
		t.Fatalf("an app already on Node gets no Node.js hint, got %v", err)
	}
}

func TestNextBefore162(t *testing.T) {
	for spec, old := range map[string]bool{"16.3.8": false, "^16.2.0": false, "16.1.4": true, "^15.5.0": true, "~14.2": true, ">=17": false, "latest": false, "canary": false} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"dependencies":{"next":"`+spec+`"}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, got := nextBefore162(dir); got != old {
			t.Errorf("next %q: older than 16.2 = %v, want %v", spec, got, old)
		}
	}
}

func TestPnpmWorkspaceNeedsPackages(t *testing.T) {
	files := map[string]string{
		"package.json":        `{"devDependencies":{"vite":"7"},"scripts":{"build":"vite build"}}`,
		"index.html":          "<!doctype html>",
		"pnpm-workspace.yaml": "allowBuilds:\n  esbuild: true\n",
	}
	read := func(p string) ([]byte, error) { return []byte(files[p]), nil }
	tree := []ghapp.TreeEntry{{Path: "package.json", Type: "blob"}, {Path: "index.html", Type: "blob"}, {Path: "pnpm-workspace.yaml", Type: "blob"}}
	if r := detectRoots(tree, read); len(r) != 1 || r[0].Workspace || r[0].Framework != "static" {
		t.Fatalf("settings-only pnpm-workspace.yaml is a single app: %+v", r)
	}
	files["pnpm-workspace.yaml"] = "packages:\n  - apps/*\n"
	if r := detectRoots(tree, read); len(r) != 1 || !r[0].Workspace {
		t.Fatalf("packages: makes it a monorepo top: %+v", r)
	}
}
