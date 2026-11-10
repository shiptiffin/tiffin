package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shiptiffin/tiffin/internal/manifest"
)

func TestLaunchFor(t *testing.T) {
	for _, c := range []struct {
		name   string
		files  map[string]string
		node   bool
		id     string
		start  string // "" with files set: served as files
		files_ string // first output folder, for files
		page   string
		env    string // one Run key the launch must set
		build  string // one Build key
		build_ string // the build command that runs ("" for the build script)
		warn   bool
		err    bool
	}{
		{name: "SvelteKit 3, adapter-bun", files: map[string]string{
			"package.json":   `{"devDependencies":{"@sveltejs/kit":"3.0.0","@sveltejs/adapter-bun":"1.0.0"},"scripts":{"build":"vite build","start":"bun ./build"}}`,
			"vite.config.ts": `import adapter from "@sveltejs/adapter-bun"; export default { plugins: [sveltekit({ adapter: adapter() })] }`,
		}, id: "sveltekit", start: "bun ./build/index.js", env: "CONNECTION_IDLE_TIMEOUT"},
		{name: "SvelteKit, adapter-node with its own out", files: map[string]string{
			"package.json":     `{"devDependencies":{"@sveltejs/kit":"2.40.0","@sveltejs/adapter-node":"5.0.0"}}`,
			"svelte.config.js": `import adapter from '@sveltejs/adapter-node'; export default { kit: { adapter: adapter({ out: 'server' }) } }`,
		}, id: "sveltekit", start: "bun ./server/index.js", env: "KEEP_ALIVE_TIMEOUT"},
		{name: "SvelteKit 2, adapter-node in svelte.config next to vite.config", files: map[string]string{
			"package.json":     `{"devDependencies":{"@sveltejs/kit":"2.40.0","@sveltejs/adapter-node":"5.0.0"}}`,
			"vite.config.ts":   `import { sveltekit } from '@sveltejs/kit/vite'; export default { plugins: [sveltekit()] }`,
			"svelte.config.js": `import adapter from '@sveltejs/adapter-node'; export default { kit: { adapter: adapter({ out: 'server' }) } }`,
		}, id: "sveltekit", start: "bun ./server/index.js", env: "KEEP_ALIVE_TIMEOUT"},
		{name: "SvelteKit 2, adapter-static in svelte.config next to vite.config", files: map[string]string{
			"package.json":     `{"devDependencies":{"@sveltejs/kit":"2.40.0","@sveltejs/adapter-static":"3.0.0"}}`,
			"vite.config.js":   `import { sveltekit } from '@sveltejs/kit/vite'; export default { plugins: [sveltekit()] }`,
			"svelte.config.js": `import adapter from '@sveltejs/adapter-static'; export default { kit: { adapter: adapter({ pages: 'site', fallback: '200.html' }) } }`,
		}, id: "sveltekit", files_: "site", page: "/200.html"},
		{name: "SvelteKit, adapter-node on Node.js", files: map[string]string{
			"package.json": `{"devDependencies":{"@sveltejs/kit":"3.0.0","@sveltejs/adapter-node":"6.0.0"}}`,
		}, node: true, id: "sveltekit", start: "node ./build/index.js", env: "PROTOCOL_HEADER"},
		{name: "SvelteKit, adapter-auto becomes adapter-node", files: map[string]string{
			"package.json":   `{"devDependencies":{"@sveltejs/kit":"3.0.0","@sveltejs/adapter-auto":"8.0.0"}}`,
			"vite.config.js": `import adapter from '@sveltejs/adapter-auto';`,
		}, id: "sveltekit", start: "bun ./build/index.js", build: "GCP_BUILDPACKS", warn: true},
		{name: "SvelteKit, adapter-static with a fallback", files: map[string]string{
			"package.json": `{"devDependencies":{"@sveltejs/kit":"3.0.0","@sveltejs/adapter-static":"3.0.0"}}`,
			"vite.config.ts": `import adapter from '@sveltejs/adapter-static'; // adapter({ fallback: 'index.html' })
sveltekit({ adapter: adapter({ fallback: '200.html' }) })`,
		}, id: "sveltekit", files_: "build", page: "/200.html"},
		{name: "SvelteKit, adapter-bun on Node.js fails", files: map[string]string{
			"package.json": `{"devDependencies":{"@sveltejs/kit":"3.0.0","@sveltejs/adapter-bun":"1.0.0"}}`,
		}, node: true, id: "sveltekit", err: true},
		{name: "SvelteKit, adapter-vercel fails", files: map[string]string{
			"package.json":   `{"devDependencies":{"@sveltejs/kit":"3.0.0","@sveltejs/adapter-vercel":"6.0.0"}}`,
			"vite.config.ts": `import adapter from '@sveltejs/adapter-vercel';`,
		}, id: "sveltekit", err: true},
		{name: "SvelteKit, a custom server keeps its start script", files: map[string]string{
			"package.json": `{"devDependencies":{"@sveltejs/kit":"3.0.0","@sveltejs/adapter-node":"6.0.0"},"scripts":{"start":"node server.js"}}`,
		}, id: "sveltekit", start: "", env: "PROTOCOL_HEADER"},
		{name: "Nuxt", files: map[string]string{
			"package.json": `{"dependencies":{"nuxt":"4.6.0"},"scripts":{"build":"nuxt build"}}`,
		}, id: "nuxt", start: "bun .output/server/index.mjs", env: "NITRO_SHUTDOWN_TIMEOUT", build: "NITRO_PRESET"},
		{name: "Nuxt on Node.js, its usual start script", files: map[string]string{
			"package.json": `{"dependencies":{"nuxt":"4.6.0"},"scripts":{"build":"nuxt build","start":"node .output/server/index.mjs"}}`,
		}, node: true, id: "nuxt", start: "node .output/server/index.mjs"},
		{name: "Nuxt generate", files: map[string]string{
			"package.json": `{"dependencies":{"nuxt":"4.6.0"},"scripts":{"build":"nuxt generate"}}`,
		}, id: "nuxt", files_: ".output/public"},
		{name: "Nuxt, its build setting runs nuxt generate", files: map[string]string{
			"package.json": `{"dependencies":{"nuxt":"4.6.0"},"scripts":{"build":"nuxt build"}}`,
		}, build_: "nuxt generate", id: "nuxt", files_: ".output/public"},
		{name: "Nuxt, its build setting runs the generate script", files: map[string]string{
			"package.json": `{"dependencies":{"nuxt":"4.6.0"},"scripts":{"build":"nuxt build","generate":"nuxt generate"}}`,
		}, build_: "npm run generate", id: "nuxt", files_: ".output/public"},
		{name: "Nuxt, its build setting runs nuxt build over a generate script", files: map[string]string{
			"package.json": `{"dependencies":{"nuxt":"4.6.0"},"scripts":{"build":"nuxt generate"}}`,
		}, build_: "bun run nuxt:build", id: "nuxt", start: "bun .output/server/index.mjs"},
		{name: "Nuxt generate, ssr: false", files: map[string]string{
			"package.json":   `{"dependencies":{"nuxt":"4.6.0"},"scripts":{"build":"nuxt generate"}}`,
			"nuxt.config.ts": `export default defineNuxtConfig({ ssr: false })`,
		}, id: "nuxt", files_: ".output/public", page: "/200.html"},
		{name: "React Router 8 on Bun", files: map[string]string{
			"package.json": `{"dependencies":{"react-router":"8.4.0","@react-router/node":"8.4.0","@react-router/serve":"8.4.0"},"devDependencies":{"@react-router/dev":"8.4.0"},"scripts":{"start":"react-router-serve ./build/server/index.js"}}`,
		}, id: "react-router", start: "bun " + rrRunnerPath + " ./build/server/index.js", env: "SHUTDOWN_TIMEOUT"},
		{name: "React Router 7 before the leak fix, on Node.js", files: map[string]string{
			"package.json": `{"dependencies":{"react-router":"^7.9.2","@react-router/serve":"^7.9.2"},"devDependencies":{"@react-router/dev":"^7.9.2"},"scripts":{"start":"react-router-serve ./build/server/index.js"}}`,
		}, node: true, id: "react-router", start: "node ./node_modules/@react-router/serve/bin.cjs ./build/server/index.js", warn: true},
		{name: "React Router with its own build folder", files: map[string]string{
			"package.json":           `{"dependencies":{"react-router":"8.4.0"},"devDependencies":{"@react-router/dev":"8.4.0"}}`,
			"react-router.config.ts": `export default { buildDirectory: "out" }`,
		}, id: "react-router", start: "bun " + rrRunnerPath + " ./out/server/index.js"},
		{name: "React Router SPA mode", files: map[string]string{
			"package.json":           `{"dependencies":{"react-router":"8.4.0"},"devDependencies":{"@react-router/dev":"8.4.0"}}`,
			"react-router.config.ts": "export default {\n  // ssr: true,\n  ssr: false,\n}",
		}, id: "react-router", files_: "build/client"},
		{name: "Astro with @astrojs/node on Bun", files: map[string]string{
			"package.json":     `{"dependencies":{"astro":"7.4.7","@astrojs/node":"11.1.11"},"scripts":{"build":"astro build","preview":"astro preview"}}`,
			"astro.config.mjs": `import node from '@astrojs/node'; export default defineConfig({ adapter: node({ mode: 'standalone' }) })`,
		}, id: "astro", start: "bun ./dist/server/entry.mjs"},
		{name: "Astro with @astrojs/node on Node.js", files: map[string]string{
			"package.json": `{"dependencies":{"astro":"7.4.7","@astrojs/node":"11.1.11"},"scripts":{"build":"astro build"}}`,
		}, node: true, id: "astro", start: "node ./dist/server/entry.mjs"},
		{name: "Astro with its own outDir", files: map[string]string{
			"package.json":    `{"dependencies":{"astro":"7.4.7","@astrojs/node":"11.1.11"}}`,
			"astro.config.ts": "export default defineConfig({\n  // outDir: './old',\n  outDir: './build',\n})",
		}, id: "astro", start: "bun ./build/server/entry.mjs"},
		{name: "Astro, its usual start script runs the entry", files: map[string]string{
			"package.json": `{"dependencies":{"astro":"7.4.7","@astrojs/node":"11.1.11"},"scripts":{"start":"node ./dist/server/entry.mjs"}}`,
		}, node: true, id: "astro", start: "node ./dist/server/entry.mjs"},
		{name: "Astro, a dev-server start script is not a server", files: map[string]string{
			"package.json": `{"dependencies":{"astro":"7.4.7","@astrojs/node":"11.1.11"},"scripts":{"start":"astro dev"}}`,
		}, id: "astro", start: "bun ./dist/server/entry.mjs"},
		{name: "Astro, a custom server keeps its start script", files: map[string]string{
			"package.json": `{"dependencies":{"astro":"7.4.7","@astrojs/node":"11.1.11","express":"5"},"scripts":{"start":"node server.mjs"}}`,
		}, id: "astro", start: ""},
		{name: "Astro without an adapter is files", files: map[string]string{
			"package.json": `{"dependencies":{"astro":"7.4.7"},"scripts":{"build":"astro build","start":"astro preview"}}`,
		}, id: "astro", files_: "dist"},
		{name: "Astro without an adapter, its own outDir", files: map[string]string{
			"package.json":     `{"dependencies":{"astro":"7.4.7"},"scripts":{"build":"astro build"}}`,
			"astro.config.mjs": `export default defineConfig({ outDir: "public_html" })`,
		}, id: "astro", files_: "public_html"},
		{name: "Astro with another host's adapter fails", files: map[string]string{
			"package.json": `{"dependencies":{"astro":"7.4.7","@astrojs/vercel":"9"},"scripts":{"build":"astro build"}}`,
		}, id: "astro", err: true},
		{name: "TanStack Start on Nitro 3, Bun", files: map[string]string{
			"package.json": `{"dependencies":{"@tanstack/react-start":"1.169.4","nitro":"3.0.260903-beta"},"scripts":{"build":"vite build","start":"bun .output/server/index.mjs"}}`,
		}, id: "tanstack-start", start: "bun .output/server/index.mjs", env: "SERVER_SHUTDOWN_TIMEOUT"},
		{name: "TanStack Start on Nitro 3, Node.js, no start script", files: map[string]string{
			"package.json": `{"dependencies":{"@tanstack/react-start":"1.169.4"},"devDependencies":{"nitro":"3.0.260903-beta"},"scripts":{"build":"vite build"}}`,
		}, node: true, id: "tanstack-start", start: "node .output/server/index.mjs", env: "NITRO_SHUTDOWN_TIMEOUT"},
		{name: "TanStack Start on Nitro 2 (nitropack), node start script", files: map[string]string{
			"package.json": `{"dependencies":{"@tanstack/react-start":"1.120.0","nitropack":"2.12.0"},"scripts":{"start":"node .output/server/index.mjs"}}`,
		}, id: "tanstack-start", start: "bun .output/server/index.mjs", env: "NITRO_SHUTDOWN_TIMEOUT"},
		{name: "TanStack Start for Solid", files: map[string]string{
			"package.json": `{"dependencies":{"@tanstack/solid-start":"1.169.4","nitro":"3.0.260903-beta"}}`,
		}, id: "tanstack-start", start: "bun .output/server/index.mjs", env: "SERVER_SHUTDOWN_TIMEOUT"},
		{name: "TanStack Start, its own server keeps its start script", files: map[string]string{
			"package.json": `{"dependencies":{"@tanstack/react-start":"1.169.4"},"scripts":{"start":"bun run server.ts"}}`,
		}, id: "tanstack-start", start: ""},
		{name: "TanStack Start, Nitro and its own server script", files: map[string]string{
			"package.json": `{"dependencies":{"@tanstack/react-start":"1.169.4","nitro":"3.0.260903-beta"},"scripts":{"start":"bun run server.ts"}}`,
		}, id: "tanstack-start", start: "", env: "SERVER_SHUTDOWN_TIMEOUT"},
		{name: "TanStack Start with neither Nitro nor a start script", files: map[string]string{
			"package.json": `{"dependencies":{"@tanstack/react-start":"1.169.4"},"scripts":{"build":"vite build"}}`,
		}, id: "tanstack-start", start: "", warn: true},
		{name: "a Hono app is none of them", files: map[string]string{"package.json": `{"dependencies":{"hono":"4"}}`}},
	} {
		dir := t.TempDir()
		writeFiles(t, dir, c.files)
		l := launchFor(dir, c.node, c.build_)
		if c.id == "" {
			if l != nil {
				t.Errorf("%s: got %+v, want none", c.name, l)
			}
			continue
		}
		if l == nil || l.ID != c.id {
			t.Errorf("%s: got %+v, want %s", c.name, l, c.id)
			continue
		}
		if (l.Err != nil) != c.err {
			t.Errorf("%s: error %v, want error %v", c.name, l.Err, c.err)
		}
		if c.err {
			continue
		}
		if c.files_ != "" {
			if l.Files == nil || l.Files.Dirs[0] != c.files_ || l.Files.Page != c.page {
				t.Errorf("%s: files %+v, want %s (page %q)", c.name, l.Files, c.files_, c.page)
			}
			continue
		}
		if l.Files != nil || l.Start != c.start {
			t.Errorf("%s: start %q (files %+v), want %q", c.name, l.Start, l.Files, c.start)
		}
		if c.env != "" && l.Run[c.env] == "" {
			t.Errorf("%s: run env %v lacks %s", c.name, l.Run, c.env)
		}
		if c.build != "" && l.Build[c.build] == "" {
			t.Errorf("%s: build env %v lacks %s", c.name, l.Build, c.build)
		}
		if (l.Warn != "") != c.warn {
			t.Errorf("%s: warning %q, want one: %v", c.name, l.Warn, c.warn)
		}
	}
}

// A server build gets its framework's start command (exec'd), build env
// and the server env in the image; React Router on Bun gets the box's
// server in its source.
func TestLaunchRailpackBuild(t *testing.T) {
	for _, c := range []struct {
		name  string
		files map[string]string
		start string
		env   string // in the Railpack env
		image string // in the image's env
	}{
		{"SvelteKit", map[string]string{
			"package.json": `{"devDependencies":{"@sveltejs/kit":"3.0.0","@sveltejs/adapter-bun":"1.0.0"},"scripts":{"build":"vite build","start":"bun ./build"}}`,
		}, "exec bun ./build/index.js", "RAILPACK_BUILD_CMD=bun --bun run build", "PROTOCOL_HEADER"},
		{"Nuxt", map[string]string{
			"package.json": `{"dependencies":{"nuxt":"4.6.0"},"scripts":{"build":"nuxt build"}}`,
		}, "exec bun .output/server/index.mjs", "NITRO_PRESET=node-server", "NITRO_SHUTDOWN_TIMEOUT"},
		{"TanStack Start", map[string]string{
			"package.json": `{"dependencies":{"@tanstack/react-start":"1.169.4","nitro":"3.0.260903-beta"},"scripts":{"build":"vite build","start":"bun .output/server/index.mjs"}}`,
		}, "exec bun .output/server/index.mjs", "RAILPACK_BUILD_CMD=bun --bun run build", "SERVER_SHUTDOWN_TIMEOUT"},
		{"React Router", map[string]string{
			"package.json": `{"dependencies":{"react-router":"8.4.0"},"devDependencies":{"@react-router/dev":"8.4.0"},"scripts":{"build":"react-router build"}}`,
		}, "exec bun " + rrRunnerPath + " ./build/server/index.js", "RAILPACK_BUILD_CMD=bun --bun run build", "SHUTDOWN_TIMEOUT"},
		{"Astro", map[string]string{
			"package.json": `{"dependencies":{"astro":"7.4.7","@astrojs/node":"11.1.11"},"scripts":{"build":"astro build"}}`,
		}, "exec bun ./dist/server/entry.mjs", "RAILPACK_BUILD_CMD=bun --bun run build", "HOST"},
	} {
		bin, log := fakeTools(t)
		b := &boxBuilder{eng: newFakeEngine(), binDir: bin, memoryMB: 2048}
		req := buildReq(t, manifest.App{Framework: manifest.FrameworkBun}, c.files)
		req.Launch = launchFor(req.SrcDir, false, "")
		if _, err := b.Build(context.Background(), req); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		calls := readCalls(t, log)
		for _, want := range []string{"env: RAILPACK_START_CMD=" + c.start, "env: " + c.env} {
			if !strings.Contains(calls, want+"\n") {
				t.Errorf("%s: railpack env lacks %q:\n%s", c.name, want, calls)
			}
		}
		raw, _ := os.ReadFile(filepath.Join(req.WorkDir, "plan", "railpack-plan.json"))
		var plan struct {
			Deploy struct {
				Variables map[string]string `json:"variables"`
			} `json:"deploy"`
		}
		_ = json.Unmarshal(raw, &plan)
		if plan.Deploy.Variables[c.image] == "" {
			t.Errorf("%s: the image's env %v lacks %s", c.name, plan.Deploy.Variables, c.image)
		}
		runner := exists(filepath.Join(req.SrcDir, ".tiffin", "react-router", "serve.js"))
		if runner != (c.name == "React Router") {
			t.Errorf("%s: the box's React Router server written: %v", c.name, runner)
		}
	}
}

// React Router with ssr: false runs no server: built with Railpack, its
// client folder is served as files, with __spa-fallback.html for paths
// without a file when it prerendered the home page.
func TestLaunchFilesFromAServerApp(t *testing.T) {
	bin, log := fakeTools(t)
	// buildctl --output type=local,dest=X writes what the plan's files step would.
	writeFiles(t, bin, map[string]string{"railpack": "#!/bin/sh\n" + `for a in "$@"; do case "$a" in RAILPACK_START_CMD=*) printf 'env: %s\n' "$a" >> ` + log + `;; esac; done; while [ $# -gt 0 ]; do if [ "$1" = --plan-out ]; then echo '{"steps":[{"name":"install"},{"name":"build"}],"deploy":{}}' > "$2"; fi; shift; done` + "\n",
		"buildctl": "#!/bin/sh\n" + `for a in "$@"; do case "$a" in type=local,dest=*) d="${a#type=local,dest=}"; mkdir -p "$d/tiffin-out/0/assets" && echo home > "$d/tiffin-out/0/index.html" && echo shell > "$d/tiffin-out/0/__spa-fallback.html" && echo js > "$d/tiffin-out/0/assets/a.js";; esac; done` + "\n"})
	b := &boxBuilder{eng: newFakeEngine(), binDir: bin, staticDir: t.TempDir(), memoryMB: 2048}
	req := buildReq(t, manifest.App{Framework: manifest.FrameworkBun}, map[string]string{
		"package.json":           `{"dependencies":{"react-router":"8.4.0"},"devDependencies":{"@react-router/dev":"8.4.0"},"scripts":{"build":"react-router build"}}`,
		"react-router.config.ts": `export default { ssr: false, prerender: ["/"] }`,
	})
	req.Launch = launchFor(req.SrcDir, false, "")
	res, err := b.Build(context.Background(), req)
	if err != nil {
		t.Fatalf("%v\n%s", err, readCalls(t, log))
	}
	if res.StaticRoot == "" || !res.SPA || res.SPAPage != "/__spa-fallback.html" || res.Image != "" {
		t.Fatalf("got %+v", res)
	}
	if got, _ := os.ReadFile(filepath.Join(res.StaticRoot, "index.html")); string(got) != "home\n" {
		t.Errorf("served %q", got)
	}
	if calls := readCalls(t, log); !strings.Contains(calls, "env: RAILPACK_START_CMD=true\n") {
		t.Errorf("a files-only build's image never runs:\n%s", calls)
	}
}

func TestSmokeName(t *testing.T) {
	bun := &manifest.App{Framework: manifest.FrameworkBun}
	if smokeName(bun, &Deploy{}) != "" || smokeName(bun, &Deploy{Launch: "nuxt"}) != "Nuxt" || smokeName(bun, &Deploy{Launch: "tanstack-start"}) != "TanStack Start" ||
		smokeName(&manifest.App{Framework: manifest.FrameworkAstro}, &Deploy{Launch: "astro"}) != "Astro" ||
		smokeName(&manifest.App{Framework: manifest.FrameworkNext}, &Deploy{}) != "Next.js" {
		t.Error("smoke tests: Next.js and the launched frameworks only")
	}
}

// SvelteKit's adapter-static SPA (a fallback page, nothing prerendered)
// writes no index.html: the fallback is the site's entry, and / serves it.
func TestLaunchFilesWithOnlyAFallbackPage(t *testing.T) {
	bin, log := fakeTools(t)
	writeFiles(t, bin, map[string]string{"railpack": "#!/bin/sh\n" + `while [ $# -gt 0 ]; do if [ "$1" = --plan-out ]; then echo '{"steps":[{"name":"install"},{"name":"build"}],"deploy":{}}' > "$2"; fi; shift; done` + "\n",
		"buildctl": "#!/bin/sh\n" + `for a in "$@"; do case "$a" in type=local,dest=*) d="${a#type=local,dest=}"; mkdir -p "$d/tiffin-out/0/_app" && echo shell > "$d/tiffin-out/0/200.html" && echo js > "$d/tiffin-out/0/_app/a.js";; esac; done` + "\n"})
	b := &boxBuilder{eng: newFakeEngine(), binDir: bin, staticDir: t.TempDir(), memoryMB: 2048}
	req := buildReq(t, manifest.App{Framework: manifest.FrameworkBun}, map[string]string{
		"package.json":     `{"devDependencies":{"@sveltejs/kit":"2.40.0","@sveltejs/adapter-static":"3.0.0"},"scripts":{"build":"vite build"}}`,
		"vite.config.ts":   `import { sveltekit } from '@sveltejs/kit/vite'; export default { plugins: [sveltekit()] }`,
		"svelte.config.js": `import adapter from '@sveltejs/adapter-static'; export default { kit: { adapter: adapter({ fallback: '200.html' }) } }`,
	})
	req.Launch = launchFor(req.SrcDir, false, "")
	res, err := b.Build(context.Background(), req)
	if err != nil {
		t.Fatalf("%v\n%s", err, readCalls(t, log))
	}
	if res.StaticRoot == "" || !res.SPA || res.SPAPage != "/200.html" {
		t.Fatalf("got %+v", res)
	}
	if got, _ := os.ReadFile(filepath.Join(res.StaticRoot, "index.html")); string(got) != "shell\n" {
		t.Errorf("/ serves %q, want the fallback page", got)
	}
}

// A workspace app on Node.js starts a package's bin with node itself (no
// resident npx launcher), found where the workspace hoisted it, even when
// the package exports nothing but its package.json.
func TestExecPackageBin(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not found")
	}
	ws := t.TempDir()
	writeFiles(t, ws, map[string]string{
		"node_modules/@scope/serve/package.json": `{"name":"@scope/serve","exports":{"./package.json":"./package.json"}}`,
		"node_modules/@scope/serve/bin.cjs":      `console.log("args", process.argv.slice(2).join(" "))`,
		"apps/web/package.json":                  `{}`,
	})
	cmd := "cd apps/web && " + execPackageBin("@scope/serve", "bin.cjs", " ./build/server/index.js")
	if execLast(cmd) != cmd {
		t.Errorf("execLast changed %q", cmd)
	}
	c := exec.Command("bash", "-c", cmd)
	c.Dir = ws
	out, err := c.CombinedOutput()
	if err != nil || string(out) != "args ./build/server/index.js\n" {
		t.Fatalf("%v: %s", err, out)
	}
}

// framework "astro", and Astro declared the old way (framework "bun",
// runtime "node", its own command): the build reads the same launch, the
// app's command wins over the box's start command, and the image's env
// has HOST=0.0.0.0 for @astrojs/node's server either way.
func TestReadAppAstro(t *testing.T) {
	const pkg = `{"dependencies":{"astro":"7.4.7","@astrojs/node":"11.1.11"},"scripts":{"build":"astro build"}}`
	for _, c := range []struct {
		name   string
		spec   manifest.App
		files  map[string]string
		start  string // RAILPACK_START_CMD, "" for files
		files_ string
		note   string
		node   bool // builds on Node.js
	}{
		{name: "framework astro: Node.js by default", spec: manifest.App{Framework: manifest.FrameworkAstro},
			files: map[string]string{"package.json": pkg}, start: "exec node ./dist/server/entry.mjs", note: "==> runtime: Node.js", node: true},
		{name: "framework astro on Node.js", spec: manifest.App{Framework: manifest.FrameworkAstro, Runtime: manifest.RuntimeNode},
			files: map[string]string{"package.json": pkg}, start: "exec node ./dist/server/entry.mjs", node: true},
		{name: "framework astro, runtime bun wins", spec: manifest.App{Framework: manifest.FrameworkAstro, Runtime: manifest.RuntimeBun},
			files: map[string]string{"package.json": pkg}, start: "exec bun ./dist/server/entry.mjs"},
		{name: "detected under bun: Node.js by default", spec: manifest.App{Framework: manifest.FrameworkBun},
			files: map[string]string{"package.json": pkg}, start: "exec node ./dist/server/entry.mjs", node: true},
		{name: "its own outDir", spec: manifest.App{Framework: manifest.FrameworkAstro},
			files: map[string]string{"package.json": pkg, "astro.config.mjs": `export default { outDir: "./out/" }`}, start: "exec node ./out/server/entry.mjs", node: true},
		{name: "declared the old way", spec: manifest.App{Framework: manifest.FrameworkBun, Runtime: manifest.RuntimeNode, Command: "node dist/server/entry.mjs"},
			files: map[string]string{"package.json": pkg}, start: "exec node dist/server/entry.mjs", note: "the app's command (node dist/server/entry.mjs)", node: true},
		{name: "framework astro, no adapter", spec: manifest.App{Framework: manifest.FrameworkAstro},
			files: map[string]string{"package.json": `{"dependencies":{"astro":"7.4.7"},"scripts":{"build":"astro build"}}`}, files_: "dist"},
		{name: "framework astro without astro", spec: manifest.App{Framework: manifest.FrameworkAstro},
			files: map[string]string{"package.json": `{"scripts":{"start":"node server.js"}}`}, note: `framework is "astro" but ./package.json does not list astro`},
	} {
		req := buildReq(t, c.spec, c.files)
		d := req.Deploy
		var log bytes.Buffer
		if err := (&rt{}).readApp(d, &req.Spec, &req, &log); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if c.note != "" {
			out := log.String()
			if req.Launch != nil {
				out += req.Launch.logLine()
			}
			if !strings.Contains(out, c.note) {
				t.Errorf("%s: log %q lacks %q", c.name, out, c.note)
			}
		}
		if c.files_ != "" {
			if req.Launch == nil || req.Launch.Files == nil || req.Launch.Files.Dirs[0] != c.files_ || d.Launch != "" {
				t.Errorf("%s: launch %+v (deploy launch %q), want files in %s", c.name, req.Launch, d.Launch, c.files_)
			}
			continue
		}
		if c.start == "" {
			if req.Launch != nil {
				t.Errorf("%s: launch %+v, want none", c.name, req.Launch)
			}
			continue
		}
		if d.Launch != "astro" {
			t.Errorf("%s: deploy launch %q, want astro (smoke-tested)", c.name, d.Launch)
		}
		if onNode := req.Spec.Runtime == manifest.RuntimeNode; onNode != c.node {
			t.Errorf("%s: builds on Node.js %v, want %v", c.name, onNode, c.node)
		}
		if req.Launch.Secret != astroKeyEnv || !req.Launch.SecretAtBuild {
			t.Errorf("%s: launch keeps no ASTRO_KEY for its builds: %+v", c.name, req.Launch)
		}
		bin, calls := fakeTools(t)
		b := &boxBuilder{eng: newFakeEngine(), binDir: bin, memoryMB: 2048}
		if _, err := b.Build(context.Background(), req); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := readCalls(t, calls); !strings.Contains(got, "env: RAILPACK_START_CMD="+c.start+"\n") {
			t.Errorf("%s: railpack env lacks start %q:\n%s", c.name, c.start, got)
		}
		raw, _ := os.ReadFile(filepath.Join(req.WorkDir, "plan", "railpack-plan.json"))
		var plan struct {
			Deploy struct {
				Variables map[string]string `json:"variables"`
			} `json:"deploy"`
		}
		_ = json.Unmarshal(raw, &plan)
		if plan.Deploy.Variables["HOST"] != "0.0.0.0" {
			t.Errorf("%s: the image's env %v lacks HOST=0.0.0.0", c.name, plan.Deploy.Variables)
		}
		build := "bun --bun run build"
		if c.node {
			build = "bun run build" // the package manager runs the script; astro's bin asks for node
		}
		if got := readCalls(t, calls); !strings.Contains(got, "env: RAILPACK_BUILD_CMD="+build+"\n") {
			t.Errorf("%s: railpack env lacks build %q:\n%s", c.name, build, got)
		}
	}
}

// An Astro app with @astrojs/node declared static: its build is a server,
// not files. The deploy says to set framework "astro", before the build
// when astro.config uses the adapter, after it when the build wrote
// dist/server/entry.mjs and no index.html.
func TestStaticAstroServerSaysSo(t *testing.T) {
	const pkg = `{"dependencies":{"astro":"7.4.7","@astrojs/node":"11.1.11"},"scripts":{"build":"astro build"}}`
	req := buildReq(t, manifest.App{Framework: manifest.FrameworkStatic}, map[string]string{
		"package.json":     pkg,
		"astro.config.mjs": `import node from "@astrojs/node"; export default { adapter: node({ mode: "standalone" }) }`,
	})
	err := (&rt{}).readApp(req.Deploy, &req.Spec, &req, &bytes.Buffer{})
	var be *BuildError
	if !errors.As(err, &be) || !strings.Contains(be.Hint, `framework: "astro"`) || !strings.Contains(be.Hint, "output") {
		t.Fatalf("got %v", err)
	}
	// In package.json but not in the config: a static build, as before.
	req = buildReq(t, manifest.App{Framework: manifest.FrameworkStatic}, map[string]string{"package.json": pkg, "astro.config.mjs": `export default {}`})
	if err := (&rt{}).readApp(req.Deploy, &req.Spec, &req, &bytes.Buffer{}); err != nil {
		t.Fatalf("static Astro: %v", err)
	}
	// The build wrote a server: said after the build.
	bin, _ := fakeTools(t)
	b := &boxBuilder{eng: newFakeEngine(), binDir: bin, staticDir: t.TempDir(), memoryMB: 512}
	req = buildReq(t, manifest.App{Framework: manifest.FrameworkStatic, Build: "mkdir -p dist/server dist/client && echo x > dist/server/entry.mjs && echo y > dist/client/index.html"},
		map[string]string{"package.json": pkg})
	_, err = b.Build(context.Background(), req)
	if !errors.As(err, &be) || !strings.Contains(be.Msg, "@astrojs/node") || !strings.Contains(be.Hint, `framework: "astro"`) {
		t.Fatalf("after the build: %v", err)
	}
	// A plain site with no index.html keeps the usual error.
	req = buildReq(t, manifest.App{Framework: manifest.FrameworkStatic, Build: "mkdir -p dist && echo x > dist/app.js"}, map[string]string{"package.json": `{}`})
	if _, err = b.Build(context.Background(), req); !errors.As(err, &be) || strings.Contains(be.Msg, "astro") {
		t.Fatalf("plain site: %v", err)
	}
}

// An Astro server app keeps one ASTRO_KEY across builds, instances and
// previews, as Next.js keeps its Server Actions key; it gets its URL as
// PUBLIC_TIFFIN_URL at build and run time, and its hosts at build time.
func TestAstroAppsGetAKeyAndTheirURLs(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	api := h.mf.Apps["api"]
	api.Framework = manifest.FrameworkAstro
	h.mf.Apps["api"] = api
	h.apply()
	src := map[string]string{"package.json": `{"dependencies":{"astro":"7.4.7","@astrojs/node":"11.1.11"},"scripts":{"build":"astro build"}}`}
	d1 := h.deploy("api", "", src)
	if d1.Status != StatusLive || d1.Launch != "astro" {
		t.Fatalf("deploy: %s %s %q", d1.Status, d1.Error, d1.Launch)
	}
	build := func(d *Deploy) map[string]string {
		h.bld.mu.Lock()
		defer h.bld.mu.Unlock()
		return h.bld.envs[d.ID]
	}
	k1 := build(d1)[astroKeyEnv]
	if b, err := base64.StdEncoding.DecodeString(k1); err != nil || len(b) != 32 {
		t.Fatalf("build key %q is not 32 bytes of base64", k1)
	}
	if got := build(d1)["PUBLIC_TIFFIN_URL"]; got != "https://shop.tiffin.localhost:8443/api" {
		t.Errorf("build PUBLIC_TIFFIN_URL %q", got)
	}
	if got := build(d1)[hostsEnv]; got != "shop.tiffin.localhost" {
		t.Errorf("build %s %q", hostsEnv, got)
	}
	d2 := h.deploy("api", "", src)
	pv := h.deploy("api", "pr-1", src)
	if d2.Status != StatusLive || pv.Status != StatusLive {
		t.Fatal(d2.Error, pv.Error)
	}
	if build(d2)[astroKeyEnv] != k1 || build(pv)[astroKeyEnv] != k1 {
		t.Fatal("the build key changed across deploys or previews")
	}
	spec, _ := h.r.appSpec(ctx, "shop", "api")
	pvHost := previewHost("pr-1", "shop", "api", spec, h.p.AppsDomain())
	if got := build(pv)[hostsEnv]; got != pvHost {
		t.Errorf("preview build %s %q, want %s", hostsEnv, got, pvHost)
	}
	if got := build(pv)["PUBLIC_TIFFIN_URL"]; got != "https://"+pvHost+":8443" {
		t.Errorf("preview build PUBLIC_TIFFIN_URL %q", got)
	}
	n := 0
	for _, c := range h.eng.running() {
		if c.spec.Labels["tiffin.app"] != "api" || (c.spec.Labels["tiffin.deploy"] != d2.ID && c.spec.Labels["tiffin.deploy"] != pv.ID) {
			continue // the first deploy's instances may still be draining
		}
		n++
		if c.spec.Env[astroKeyEnv] != k1 {
			t.Errorf("%s: instance key %q", c.spec.Name, c.spec.Env[astroKeyEnv])
		}
		if c.spec.Env["PUBLIC_TIFFIN_URL"] == "" || c.spec.Env["PUBLIC_TIFFIN_URL"] != c.spec.Env["TIFFIN_URL"] {
			t.Errorf("%s: PUBLIC_TIFFIN_URL %q, TIFFIN_URL %q", c.spec.Name, c.spec.Env["PUBLIC_TIFFIN_URL"], c.spec.Env["TIFFIN_URL"])
		}
		if c.spec.Env[hostsEnv] != "" {
			t.Errorf("%s: %s is for builds only", c.spec.Name, hostsEnv)
		}
	}
	if n != 3 {
		t.Fatalf("%d instances", n)
	}
	// The app's own key wins, at build and run time.
	api.Env = map[string]string{astroKeyEnv: "mine"}
	h.mf.Apps["api"] = api
	h.apply()
	d3 := h.deploy("api", "", src)
	if build(d3)[astroKeyEnv] != "mine" {
		t.Errorf("own key at build: %q", build(d3)[astroKeyEnv])
	}
	env, _, err := h.r.instanceEnv(ctx, "shop", "api", "", &api)
	if err != nil || env[astroKeyEnv] != "mine" {
		t.Errorf("own key at run time: %q %v", env[astroKeyEnv], err)
	}
}

// @shiptiffin/astro's graceful shutdown has Astro write the adapter's entry
// as server/astro-entry.mjs and writes server/entry.mjs itself, a wrapper
// that imports it. The box still starts <outDir>/server/entry.mjs (the
// wrapper, so SIGTERM drains), and still knows the build for a server.
func TestAstroIntegrationShutdownLayout(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"package.json":                `{"dependencies":{"astro":"7.4.7","@astrojs/node":"11.1.11","@shiptiffin/astro":"0.1.0"},"scripts":{"build":"astro build","start":"node ./dist/server/entry.mjs"}}`,
		"astro.config.mjs":            `import tiffin from "@shiptiffin/astro"; export default defineConfig({ integrations: [tiffin()] })`,
		"dist/server/entry.mjs":       "// Written by @shiptiffin/astro\nconst app = await import(\"./astro-entry.mjs\");\n",
		"dist/server/astro-entry.mjs": "export const options = { mode: \"standalone\" };\n",
		"dist/client/index.html":      "<h1>prerendered</h1>",
	})
	for _, onNode := range []bool{true, false} {
		l := launchFor(dir, onNode, "")
		want := map[bool]string{true: "node", false: "bun"}[onNode] + " ./dist/server/entry.mjs"
		if l == nil || l.ID != "astro" || l.Files != nil || l.Start != want {
			t.Fatalf("onNode %v: launch %+v, want start %q", onNode, l, want)
		}
	}
	if !astroServerBuilt(dir, []string{"dist"}) {
		t.Error("a build with the integration's entry.mjs wrapper is not seen as a server build")
	}
}
