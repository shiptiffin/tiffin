package runtime

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/mod/runtime/ghapp"
	"github.com/btahir/tiffin/internal/mod/runtime/vercelcfg"
)

// writeFiles writes files (name → content) under dir.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for n, b := range files {
		p := filepath.Join(dir, filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(b), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// fakeTools puts stand-ins for railpack, buildctl and nerdctl in a folder:
// each appends its arguments (one per line, after a "--- <tool>" line) and
// the build env it was given to calls.log, then does what the test needs.
// "env: NAME=VALUE" lines are the env the build sees (railpack's --env
// arguments, buildctl's secrets as their ids, resolved); "procenv: " lines
// are variables of interest in the tool's own process environment, which
// an app's env must never reach.
func fakeTools(t *testing.T) (binDir, log string) {
	t.Helper()
	binDir = t.TempDir()
	log = filepath.Join(binDir, "calls.log")
	rec := `printf -- '--- %s\n' "$(basename "$0")" >> ` + log + `; for a in "$@"; do printf '%s\n' "$a" >> ` + log + `; done; ` +
		`env | grep -E '^(RAILPACK_|DATABASE_URL|API_KEY|PUBLIC_|NEXT_SERVER|NITRO_PRESET|GCP_BUILDPACKS|DOCKER_CONFIG|LD_PRELOAD|NERDCTL_TOML|TIFFIN_SECRET_)' | sed 's/^/procenv: /' | sort >> ` + log + "\n"
	railpackEnv := `prev=""; for a in "$@"; do if [ "$prev" = --env ]; then printf 'env: %s\n' "$a"; fi; prev="$a"; done | sort >> ` + log + "\n"
	buildctlEnv := `prev=""; for a in "$@"; do if [ "$prev" = --secret ]; then id="${a#id=}"; id="${id%%,*}"; v="${a##*,env=}"; printf 'env: %s=%s\n' "$id" "$(printenv "$v")"; fi; prev="$a"; done | sort >> ` + log + "\n"
	writeFiles(t, binDir, map[string]string{
		// railpack prepare writes a plan where --plan-out says.
		"railpack": "#!/bin/sh\n" + rec + railpackEnv + `while [ $# -gt 0 ]; do if [ "$1" = --plan-out ]; then echo '{"deploy":{}}' > "$2"; fi; shift; done` + "\n",
		"buildctl": "#!/bin/sh\n" + rec + buildctlEnv + "echo 'exporting manifest sha256:" + strings.Repeat("c", 64) + "'\n",
		// nerdctl run ... --volume <src>:/app ... <image> sh -c <script>: runs the script in src.
		"nerdctl": "#!/bin/sh\n" + rec + `src=""; prev=""; for a in "$@"; do if [ "$prev" = --volume ]; then case "$a" in *:/app) src="${a%:/app}";; esac; fi; prev="$a"; last="$a"; done; cd "$src" && sh -c "$last"` + "\n",
	})
	return binDir, log
}

func readCalls(t *testing.T, log string) string {
	t.Helper()
	b, _ := os.ReadFile(log)
	return string(b)
}

func buildReq(t *testing.T, spec manifest.App, files map[string]string) BuildRequest {
	t.Helper()
	src := t.TempDir()
	writeFiles(t, src, files)
	return BuildRequest{Deploy: &Deploy{ID: "dep_1", Project: "shop", App: "web"}, Spec: spec, SrcDir: src, WorkDir: t.TempDir(), Log: &bytes.Buffer{}}
}

func TestDockerfileOf(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"only/Dockerfile":      "FROM scratch",
		"js/Dockerfile":        "FROM oven/bun",
		"js/package.json":      `{}`,
		"py/Dockerfile":        "FROM python",
		"py/requirements.txt":  "fastapi",
		"other/docker/web.dfi": "FROM scratch",
	})
	for _, c := range []struct {
		name string
		spec manifest.App
		dir  string
		want string
	}{
		{"auto: a Dockerfile and nothing else", manifest.App{Framework: manifest.FrameworkBun}, "only", "only/Dockerfile"},
		{"auto: a package.json wins (Railpack)", manifest.App{Framework: manifest.FrameworkNext}, "js", ""},
		{"auto: a Python project wins (Railpack)", manifest.App{Framework: manifest.FrameworkBun}, "py", ""},
		{"auto: a static site stays files", manifest.App{Framework: manifest.FrameworkStatic}, "only", ""},
		{"dockerfile builder, even with a package.json", manifest.App{Builder: manifest.BuilderDockerfile}, "js", "js/Dockerfile"},
		{"dockerfile builder, its own path", manifest.App{Builder: manifest.BuilderDockerfile, Dockerfile: "docker/web.dfi"}, "other", "other/docker/web.dfi"},
		{"prebuilt never builds a Dockerfile", manifest.App{Builder: manifest.BuilderPrebuilt}, "only", ""},
	} {
		got, why := dockerfileOf(c.spec, dir, c.dir)
		if got != c.want || (got != "" && why == "") {
			t.Errorf("%s: %q (%q), want %q", c.name, got, why, c.want)
		}
	}
}

func TestDockerfileBuildInvocation(t *testing.T) {
	bin, log := fakeTools(t)
	b := &boxBuilder{eng: newFakeEngine(), binDir: bin, cgroupDir: t.TempDir()}
	spec := manifest.App{Framework: manifest.FrameworkBun, Builder: manifest.BuilderDockerfile, Dockerfile: "docker/api.Dockerfile", Target: "runner", Command: "./api serve"}
	req := buildReq(t, spec, map[string]string{"docker/api.Dockerfile": "FROM scratch", "main.go": "package main"})
	req.Dockerfile = "docker/api.Dockerfile"
	req.Env = map[string]string{"PUBLIC_SITE": "https://shop.example", nextKeyEnv: "s3cret-key"}
	req.RunEnv = map[string]string{"DATABASE_URL": "postgres://u:p4ss@db/shop", "API_KEY": "sk_live_123", "PUBLIC_SITE": "old"}
	res, err := b.Build(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Image != imageRef("shop", "web", "dep_1") || res.Digest != "sha256:"+strings.Repeat("c", 64) || res.Start != "./api serve" {
		t.Fatalf("result: %+v", res)
	}
	calls := readCalls(t, log)
	args := strings.Split(calls, "\n")
	for _, want := range []string{"--frontend", "dockerfile.v0", "context=" + req.SrcDir, "dockerfile=" + filepath.Join(req.SrcDir, "docker"),
		"filename=api.Dockerfile", "target=runner", "build-arg:PUBLIC_SITE=https://shop.example",
		"id=API_KEY,env=TIFFIN_SECRET_0", "id=DATABASE_URL,env=TIFFIN_SECRET_1", "id=" + nextKeyEnv + ",env=TIFFIN_SECRET_2",
		"env: API_KEY=sk_live_123", "env: DATABASE_URL=postgres://u:p4ss@db/shop",
		"cmdline=", "build-arg:BUILDKIT_CACHE_MOUNT_NS=" + cacheNamespace("shop", "web")} {
		if !slices.Contains(args, want) {
			t.Errorf("buildctl call lacks %q:\n%s", want, calls)
		}
	}
	// Secrets never reach the command line, nor build args; nothing else may widen the build.
	for _, bad := range []string{"build-arg:API_KEY", "build-arg:DATABASE_URL", "build-arg:" + nextKeyEnv, "--ssh", "--allow", "context:", "gateway.v0"} {
		if strings.Contains(strings.Join(slices.DeleteFunc(args, func(a string) bool { return strings.HasPrefix(a, "env: ") || strings.HasPrefix(a, "procenv: ") }), "\n"), bad) {
			t.Errorf("buildctl call has %q:\n%s", bad, calls)
		}
	}
	if strings.Contains(calls, "--- railpack") {
		t.Error("a Dockerfile build must not run Railpack")
	}
	logText := req.Log.(*bytes.Buffer).String()
	if !strings.Contains(logText, "build secrets (RUN --mount=type=secret,id=NAME,env=NAME): API_KEY, DATABASE_URL, "+nextKeyEnv) || strings.Contains(logText, "sk_live") {
		t.Errorf("build log: %s", logText)
	}
}

func TestDockerfileMustBeInTheSource(t *testing.T) {
	bin, _ := fakeTools(t)
	b := &boxBuilder{eng: newFakeEngine(), binDir: bin}
	outside := filepath.Join(t.TempDir(), "Dockerfile")
	writeFiles(t, filepath.Dir(outside), map[string]string{"Dockerfile": "FROM scratch"})
	for name, setup := range map[string]func(req *BuildRequest){
		"missing":       func(req *BuildRequest) { req.Dockerfile = "Dockerfile" },
		"escaping path": func(req *BuildRequest) { req.Dockerfile = "../Dockerfile" },
		"link out": func(req *BuildRequest) {
			req.Dockerfile = "Dockerfile"
			if err := os.Symlink(outside, filepath.Join(req.SrcDir, "Dockerfile")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		req := buildReq(t, manifest.App{Builder: manifest.BuilderDockerfile}, map[string]string{"x": "y"})
		setup(&req)
		_, err := b.Build(context.Background(), req)
		var be *BuildError
		if !errors.As(err, &be) || be.Hint == "" {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRailpackHonoursBuildSettings(t *testing.T) {
	bin, log := fakeTools(t)
	b := &boxBuilder{eng: newFakeEngine(), binDir: bin, memoryMB: 2048}
	spec := manifest.App{Framework: manifest.FrameworkBun, Install: "pnpm install --frozen-lockfile", Build: "pnpm run build:web", Command: "node dist/server.js"}
	req := buildReq(t, spec, map[string]string{
		"package.json": `{"scripts":{"build":"tsc","start":"node index.js"}}`,
		"vercel.json":  `{"installCommand":"npm ci","buildCommand":"npm run vercel-build"}`,
	})
	v, err := vercelcfg.Read(req.SrcDir)
	if err != nil || v == nil {
		t.Fatal(err)
	}
	req.Vercel = v
	if _, err := b.Build(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	calls := readCalls(t, log)
	for _, want := range []string{"env: RAILPACK_INSTALL_CMD=pnpm install --frozen-lockfile", "env: RAILPACK_BUILD_CMD=pnpm run build:web", "env: RAILPACK_START_CMD=exec node dist/server.js"} {
		if !strings.Contains(calls, want+"\n") {
			t.Errorf("railpack env lacks %q (the app's settings win over vercel.json):\n%s", want, calls)
		}
	}
}

// Railpack's default Node.js is "lts", which moves on its own: the box pins
// the major unless the app chose one.
func TestRailpackPinsNode(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"default": {"package.json": `{"scripts":{"start":"node index.js"}}`},
		"engines": {"package.json": `{"engines":{"node":"22"},"scripts":{"start":"node index.js"}}`},
		"nvmrc":   {"package.json": `{"scripts":{"start":"node index.js"}}`, ".nvmrc": "22\n"},
	} {
		bin, log := fakeTools(t)
		b := &boxBuilder{eng: newFakeEngine(), binDir: bin, memoryMB: 2048}
		req := buildReq(t, manifest.App{Framework: manifest.FrameworkBun}, files)
		if _, err := b.Build(context.Background(), req); err != nil {
			t.Fatal(name, err)
		}
		pinned := strings.Contains(readCalls(t, log), "env: RAILPACK_NODE_VERSION="+NodeVersion+"\n")
		if pinned != (name == "default") {
			t.Errorf("%s: RAILPACK_NODE_VERSION set = %v", name, pinned)
		}
	}
}

func TestStaticSiteHonoursBuildSettings(t *testing.T) {
	bin, log := fakeTools(t)
	b := &boxBuilder{eng: newFakeEngine(), binDir: bin, staticDir: t.TempDir(), memoryMB: 512}
	spec := manifest.App{Framework: manifest.FrameworkStatic, Install: "true", Build: "mkdir -p www && echo built > www/index.html", Output: "www"}
	req := buildReq(t, spec, map[string]string{"package.json": `{"scripts":{"build":"exit 1"}}`, "index.html": "not this one"})
	res, err := b.Build(context.Background(), req)
	if err != nil {
		t.Fatalf("%v\n%s", err, readCalls(t, log))
	}
	if got, _ := os.ReadFile(filepath.Join(res.StaticRoot, "index.html")); string(got) != "built\n" {
		t.Fatalf("served %q from %s", got, res.StaticRoot)
	}
	if calls := readCalls(t, log); !strings.Contains(calls, "\ntrue && mkdir -p www && echo built > www/index.html\n") {
		t.Errorf("the build ran something else:\n%s", calls)
	}

	// With no build, the output folder alone picks what is served.
	req = buildReq(t, manifest.App{Framework: manifest.FrameworkStatic, Output: "site"}, map[string]string{"index.html": "top", "site/index.html": "site"})
	req.Deploy.ID = "dep_2"
	if res, err = b.Build(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(res.StaticRoot, "index.html")); string(got) != "site" {
		t.Fatalf("served %q", got)
	}
}

func TestPrebuiltBuilderRefusesSource(t *testing.T) {
	b := &boxBuilder{eng: newFakeEngine()}
	req := buildReq(t, manifest.App{Builder: manifest.BuilderPrebuilt}, map[string]string{"package.json": `{}`})
	_, err := b.Build(context.Background(), req)
	var be *BuildError
	if !errors.As(err, &be) || !strings.Contains(be.Msg, "prebuilt") || !strings.Contains(be.Hint, "--prebuilt") {
		t.Fatalf("got %v", err)
	}
}

func TestDockerfileDeployRunsItsCommand(t *testing.T) {
	h := newHarness(t)
	api := h.mf.Apps["api"]
	api.Framework, api.Builder, api.Command, api.Release = manifest.FrameworkBun, manifest.BuilderDockerfile, "./server --port $PORT", "./migrate"
	h.mf.Apps["api"] = api
	h.apply()
	d := h.deploy("api", "", map[string]string{"Dockerfile": "FROM scratch\nCOPY . /srv\n", "package.json": `{"name":"api"}`})
	if d.Status != StatusLive || d.Builder != "dockerfile" || d.Dockerfile != "Dockerfile" || d.Start != api.Command || len(d.Assets) != 0 {
		t.Fatalf("deploy: %+v\n%s", d, h.buildLog("api", d.ID))
	}
	for _, c := range h.eng.running() {
		if c.spec.Labels["tiffin.app"] == "api" && c.spec.Command != api.Command {
			t.Errorf("instance %s runs %q, want the app's command", c.spec.Name, c.spec.Command)
		}
	}
	// The release runs in the image's own WORKDIR (no cd into a source folder).
	tasks := h.eng.taskList()
	if len(tasks) == 0 || tasks[len(tasks)-1].script != "./migrate" {
		t.Fatalf("release tasks: %+v", tasks)
	}

	// A worker with a Dockerfile and nothing else builds with it on its own.
	d = h.deploy("jobs", "", map[string]string{"Dockerfile": "FROM scratch\n", "main.py": "print(1)"})
	if d.Status != StatusLive || d.Builder != "dockerfile" || d.Start != "" {
		t.Fatalf("auto-detected Dockerfile: %+v\n%s", d, h.buildLog("jobs", d.ID))
	}
	if !strings.Contains(h.buildLog("jobs", d.ID), "builder: the Dockerfile Dockerfile (a Dockerfile and no package.json") {
		t.Errorf("the build log should say why:\n%s", h.buildLog("jobs", d.ID))
	}
}

func TestWatchMatch(t *testing.T) {
	for _, c := range []struct {
		patterns []string
		files    []string
		want     bool
	}{
		{[]string{"apps/web/**"}, []string{"apps/web/app/page.tsx"}, true},
		{[]string{"apps/web/**"}, []string{"apps/api/index.ts", "README.md"}, false},
		{[]string{"packages/ui"}, []string{"packages/ui/src/button.tsx"}, true},
		{[]string{"packages/ui"}, []string{"packages/ui-kit/x.ts"}, false},
		{[]string{"/apps/*"}, []string{"apps/web/deep/file.ts"}, true},
		{[]string{"*.lock"}, []string{"bun.lock"}, true},
		{[]string{"*.md"}, []string{"docs/guide/intro.md"}, true},
		{[]string{"apps/web/**", "!**/*.md"}, []string{"apps/web/README.md"}, false},
		{[]string{"apps/web/**", "!**/*.md"}, []string{"apps/web/README.md", "apps/web/page.tsx"}, true},
		{[]string{"!**/*.md", "apps/web/**"}, []string{"apps/web/README.md"}, true}, // the last match decides
		{[]string{"apps/web/**/*.ts?"}, []string{"apps/web/a/b/c.tsx"}, true},
		{[]string{"apps/web/**/*.ts?"}, []string{"apps/web/a/b/c.ts"}, false},
	} {
		if got := watchHits(c.patterns, c.files); got != c.want {
			t.Errorf("watchHits(%q, %q) = %v, want %v", c.patterns, c.files, got, c.want)
		}
	}
}

func TestWatchPathsFilterGitHubPushes(t *testing.T) {
	g := newGHHarness(t)
	g.connect()
	first, err := g.f.AddRepo("octo/mono", false, map[string]string{"web/index.html": "<h1>v1</h1>", "README.md": "mono"})
	if err != nil {
		t.Fatal(err)
	}
	site := g.mf.Apps["site"]
	site.Watch = []string{"web/**", "!**/*.md"}
	g.mf.Apps["site"] = site
	g.connectSite(manifest.Git{Repo: "octo/mono", Branch: "main", Path: "web", Previews: manifest.PreviewsSameRepo})

	push := func(before string) string {
		t.Helper()
		p, err := g.f.PushPayload("octo/mono", "main")
		if err != nil {
			t.Fatal(err)
		}
		p["before"] = before
		dl, err := g.f.Deliver("push", p)
		if err != nil || dl.Status != 202 {
			t.Fatalf("push: %v %+v", err, dl)
		}
		return dl.Reply
	}
	// Only the README changed: nothing to deploy.
	second, _ := g.f.Commit("octo/mono", "main", map[string]string{"README.md": "mono, better", "web/notes.md": "x"}, "Docs")
	if reply := push(first); !strings.Contains(reply, "skipped: nothing under the watch paths of shop/site changed") {
		t.Fatalf("reply: %s", reply)
	}
	// The site changed: it deploys.
	third, _ := g.f.Commit("octo/mono", "main", map[string]string{"web/index.html": "<h1>v2</h1>"}, "v2")
	if reply := push(second); !strings.Contains(reply, "deploying shop/site") {
		t.Fatalf("reply: %s", reply)
	}
	if d := g.waitFor("site", func(d *Deploy) bool { return d.Commit == third }); d.Status != StatusLive {
		t.Fatalf("deploy: %+v", d)
	}
	// A new branch (before is zeros) can't be compared: it deploys.
	if reply := push(strings.Repeat("0", 40)); !strings.Contains(reply, "deploying shop/site") {
		t.Fatalf("reply: %s", reply)
	}
}

func TestDockerRootsAndFolders(t *testing.T) {
	files := map[string]string{
		"package.json":               `{"name":"mono","workspaces":["apps/*"]}`,
		"apps/web/package.json":      `{"name":"web","dependencies":{"next":"16"}}`,
		"apps/web/docker/Dockerfile": "FROM node",
		"services/go-api/Dockerfile": "FROM golang",
		"services/go-api/main.go":    "package main",
		"node_modules/x/Dockerfile":  "FROM x",
		".github/Dockerfile":         "FROM x",
	}
	var tree []ghapp.TreeEntry
	for p := range files {
		tree = append(tree, ghapp.TreeEntry{Path: p, Type: "blob"})
	}
	roots := detectRoots(tree, func(p string) ([]byte, error) { return []byte(files[p]), nil })
	var docker []RepoRoot
	for _, r := range roots {
		if r.Builder == "dockerfile" {
			docker = append(docker, r)
		}
	}
	if len(docker) != 1 || docker[0].Path != "services/go-api" || !strings.Contains(docker[0].Why, "Dockerfile") {
		t.Fatalf("Dockerfile roots: %+v", roots)
	}
	got := repoFolders(tree)
	want := []string{"apps", "apps/web", "apps/web/docker", "services", "services/go-api"}
	if !slices.Equal(got, want) {
		t.Fatalf("folders: %q, want %q", got, want)
	}
}

func TestRedeployLiveBuildsItsSourceAgain(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, perr := h.r.redeployLive(ctx, "shop", "api", "tok_test"); perr == nil || perr.Status != 409 {
		t.Fatalf("nothing live yet: %v", perr)
	}
	first := h.deploy("api", "", map[string]string{"index.ts": "v1"})
	if first.Status != StatusLive {
		t.Fatal(first.Error)
	}
	// A build setting changes; the redeploy builds the same source with it.
	api := h.mf.Apps["api"]
	api.Build = "bun run build:prod"
	h.mf.Apps["api"] = api
	h.apply()
	nd, perr := h.r.redeployLive(ctx, "shop", "api", "tok_test")
	if perr != nil {
		t.Fatal(perr)
	}
	d := h.wait("api", nd.ID)
	if d.Status != StatusLive || d.Trigger != "redeploy" || d.ID == first.ID || !strings.Contains(h.buildLog("api", d.ID), "redeploy of "+first.ID) {
		t.Fatalf("redeploy: %+v\n%s", d, h.buildLog("api", d.ID))
	}
	h.bld.mu.Lock()
	n := len(h.bld.envs)
	h.bld.mu.Unlock()
	if n < 2 {
		t.Fatalf("the redeploy did not build: %d builds", n)
	}

	// A prebuilt app has nothing to build: source deploys are refused up front.
	api.Build, api.Builder = "", manifest.BuilderPrebuilt
	h.mf.Apps["api"] = api
	h.apply()
	if _, perr := h.r.checkDeployable(ctx, "shop", "api", "", false); perr == nil || perr.Status != 422 {
		t.Fatalf("source deploy of a prebuilt app: %v", perr)
	}
	if _, perr := h.r.checkDeployable(ctx, "shop", "api", "", true); perr != nil {
		t.Fatalf("image deploy of a prebuilt app: %v", perr)
	}
}

// An app in a workspace installs only itself, the workspace packages it
// uses and the root's dependencies, falling back to the whole workspace.
func TestWorkspaceInstall(t *testing.T) {
	write := func(t *testing.T, dir string, files map[string]string) string {
		for name, body := range files {
			p := filepath.Join(dir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	app := `{"name":"@acme/web"}`
	cases := []struct {
		name  string
		files map[string]string
		dir   string
		want  string
	}{
		{"pnpm with a lockfile", map[string]string{"pnpm-lock.yaml": "", "package.json": `{}`, "apps/web/package.json": app}, "apps/web",
			"pnpm install --frozen-lockfile --prefer-offline --filter {./apps/web}... || { echo tiffin: installing apps/web alone failed, so the whole workspace installs instead; pnpm install --frozen-lockfile --prefer-offline; }"},
		{"pnpm without one", map[string]string{"package.json": `{"packageManager":"pnpm@11.9.0"}`, "templates/bun/package.json": `{}`}, "templates/bun",
			"pnpm install --filter {./templates/bun}... || { echo tiffin: installing templates/bun alone failed, so the whole workspace installs instead; pnpm install; }"},
		{"bun", map[string]string{"bun.lock": "", "package.json": `{}`, "apps/web/package.json": app}, "apps/web",
			"bun install --frozen-lockfile --filter ./ --filter ./apps/web || { echo tiffin: installing apps/web alone failed, so the whole workspace installs instead; bun install --frozen-lockfile; }"},
		{"npm", map[string]string{"package-lock.json": "", "package.json": `{}`, "apps/web/package.json": app}, "apps/web",
			"npm install --workspace apps/web --include-workspace-root || { echo tiffin: installing apps/web alone failed, so the whole workspace installs instead; npm install; }"},
		{"yarn 4", map[string]string{"yarn.lock": "", ".yarnrc.yml": "", "package.json": `{"name":"acme"}`, "apps/web/package.json": app}, "apps/web",
			"yarn workspaces focus @acme/web acme || { echo tiffin: installing apps/web alone failed, so the whole workspace installs instead; yarn install --check-cache; }"},
		{"yarn 1 has no filter", map[string]string{"yarn.lock": "", "package.json": `{"packageManager":"yarn@1.22.22"}`, "apps/web/package.json": app}, "apps/web", ""},
		{"not in a workspace", map[string]string{"bun.lock": "", "package.json": `{}`}, "", ""},
		{"a folder the shell would split", map[string]string{"bun.lock": "", "package.json": `{}`, "apps/my web/package.json": app}, "apps/my web", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			top := write(t, t.TempDir(), c.files)
			if got := workspaceInstall(top, c.dir, packageManager(top)); got != c.want {
				t.Fatalf("got  %s\nwant %s", got, c.want)
			}
		})
	}
}
