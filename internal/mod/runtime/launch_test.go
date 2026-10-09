package runtime

import (
	"context"
	"encoding/json"
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
		{"React Router", map[string]string{
			"package.json": `{"dependencies":{"react-router":"8.4.0"},"devDependencies":{"@react-router/dev":"8.4.0"},"scripts":{"build":"react-router build"}}`,
		}, "exec bun " + rrRunnerPath + " ./build/server/index.js", "RAILPACK_BUILD_CMD=bun --bun run build", "SHUTDOWN_TIMEOUT"},
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
	if smokeName(bun, &Deploy{}) != "" || smokeName(bun, &Deploy{Launch: "nuxt"}) != "Nuxt" ||
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
