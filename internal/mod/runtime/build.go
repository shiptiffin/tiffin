package runtime

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/edge/switchboard"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/mod/budget"
	"github.com/btahir/tiffin/internal/mod/runtime/vercelcfg"
)

// BuildRequest is one build.
type BuildRequest struct {
	Deploy   *Deploy
	Spec     manifest.App
	SrcDir   string            // unpacked source (empty for prebuilt)
	WorkDir  string            // scratch space for this deploy
	Prebuilt string            // image tarball to import instead of building
	Env      map[string]string // env visible at build time: plain env, and a Next.js app's Server Actions key
	// RunEnv is the env the environment's instances get (services,
	// secrets). Railpack builds read it too, as BuildKit secrets; Env wins.
	RunEnv map[string]string
	// NextCache: the project has Valkey, so a Next.js app's adapter wires
	// the shared cache handlers.
	NextCache bool
	// Postgres: the project has Postgres, which apps that use the Workflow
	// DevKit run on.
	Postgres bool
	// Dir is the app's folder in SrcDir when SrcDir is its whole workspace
	// (a monorepo): installs run at the top, the app builds in Dir.
	Dir string
	// Vercel is the app's vercel.json (build settings), nil without one.
	Vercel *vercelcfg.Config
	// Export: a Next.js static export (output: "export"), served as files.
	Export bool
	// Launch is the full-stack framework the box sets the app up for
	// (SvelteKit, Nuxt, React Router; launch.go), nil for any other.
	Launch *launch
	// Dockerfile is the Dockerfile to build, relative to SrcDir, when the
	// app builds with one (builder "dockerfile", or one found in an app
	// folder Railpack has nothing for); "" builds with Railpack.
	Dockerfile string
	Log        io.Writer
}

// filesOnly reports whether the build only writes files the edge serves:
// a static site, a Next.js static export, or a framework's static output.
func (req BuildRequest) filesOnly() bool {
	return req.Export || req.Spec.Framework == manifest.FrameworkStatic || (req.Launch != nil && req.Launch.Files != nil)
}

// appDir is the app's own folder in the source.
func (req BuildRequest) appDir() string {
	return filepath.Join(req.SrcDir, filepath.FromSlash(req.Dir))
}

// inApp runs a shell command in the app's folder (the top, unless the
// source is a workspace).
func (req BuildRequest) inApp(cmd string) string {
	if req.Dir == "" {
		return cmd
	}
	return "cd " + shellQuote(req.Dir) + " && " + cmd
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// BuildResult is what a build produced: an image for container apps or a
// directory of files for static sites.
type BuildResult struct {
	Image      string
	Digest     string
	StaticRoot string
	SPA        bool
	// SPAPage is the page an SPA's unknown paths serve ("" for index.html).
	SPAPage string
	// Start replaces the image's own command (a Dockerfile or prebuilt
	// image whose app sets command).
	Start string
}

// Builder turns sources into something runnable.
type Builder interface {
	Build(ctx context.Context, req BuildRequest) (BuildResult, error)
}

// BuildError carries a hint for people and agents.
type BuildError struct {
	Msg, Hint string
}

func (e *BuildError) Error() string { return e.Msg }

// boxBuilder builds with Railpack + BuildKit on the box, static sites with
// Bun in a throwaway container, and imports prebuilt image tarballs.
type boxBuilder struct {
	eng       Engine
	staticDir string // where static deploys' files live
	cacheDir  string // static builds' caches, a folder per app ("": none)
	memoryMB  int    // cap for static build containers
	cgroupDir string // the build cgroup ("" → /sys/fs/cgroup/tiffin-build)
	binDir    string // where railpack, buildctl and nerdctl are ("" → /usr/local/bin)
}

// tool is the path of one of the build tools.
func (b *boxBuilder) tool(name string) string {
	return filepath.Join(orDefaultStr(b.binDir, "/usr/local/bin"), name)
}

// buildLimit is what one project's builds may use while it has a limit:
// its share of the box's CPUs, and memory past which the build slows down
// (memory.high: reclaimed and swapped, not killed). Zero: no limit.
type buildLimit struct {
	pct    int
	cpus   float64
	highMB int
}

const minBuildMB = budget.MinBuildMB

func buildLimitFor(project string) buildLimit {
	sh := budget.SharedLimit(project)
	if sh.Percent == 0 {
		return buildLimit{}
	}
	return buildLimit{pct: sh.Percent, cpus: sh.CPUs, highMB: max(minBuildMB, sh.MemoryMB)}
}

// staticBuildArgs are the extra nerdctl run flags for a static build.
func (l buildLimit) staticBuildArgs() []string {
	if l.cpus <= 0 {
		return nil
	}
	return []string{"--cpus", strconv.FormatFloat(l.cpus, 'f', -1, 64)}
}

// apply holds the build cgroup (every BuildKit step runs in it, one build at
// a time) to l, and returns what puts it back.
func (l buildLimit) apply(dir string) (undo func(), err error) {
	if l.cpus <= 0 {
		return func() {}, nil
	}
	write := func(name, v string) error { return os.WriteFile(filepath.Join(dir, name), []byte(v), 0o644) }
	undo = func() {
		_ = write("cpu.max", "max 100000")
		_ = write("memory.high", "max")
	}
	if err := write("cpu.max", fmt.Sprintf("%d 100000", max(1000, int(l.cpus*100000+0.5)))); err != nil {
		return func() {}, err
	}
	if err := write("memory.high", strconv.Itoa(l.highMB<<20)); err != nil {
		undo()
		return func() {}, err
	}
	return undo, nil
}

// limitBuild holds the build cgroup to a limited project's share while one
// of its BuildKit builds runs, and returns what lets it go. Builds run one
// at a time, so the shared build cgroup is this build's while it runs.
func (b *boxBuilder) limitBuild(project string, log io.Writer) (undo func()) {
	lim := buildLimitFor(project)
	if lim.cpus <= 0 {
		return func() {}
	}
	dir := b.cgroupDir
	if dir == "" {
		dir = filepath.Join("/sys/fs/cgroup", buildCgroup)
	}
	undo, err := lim.apply(dir)
	if err == nil {
		fmt.Fprint(log, lim.words(project))
	}
	return undo
}

func (l buildLimit) words(project string) string {
	return fmt.Sprintf("==> %s is limited to %d%% of the box: this build may use %s CPUs and slows down past %d MB\n",
		project, l.pct, strconv.FormatFloat(l.cpus, 'f', -1, 64), l.highMB)
}

func (b *boxBuilder) Build(ctx context.Context, req BuildRequest) (BuildResult, error) {
	res, err := b.build(ctx, req)
	if err == nil && res.StaticRoot != "" && res.SPA {
		res.SPAPage = spaPage(res.StaticRoot, req.Launch)
	}
	return res, err
}

func (b *boxBuilder) build(ctx context.Context, req BuildRequest) (BuildResult, error) {
	d := req.Deploy
	ref := imageRef(d.Project, d.App, d.ID)
	switch {
	case req.Prebuilt != "":
		fmt.Fprintf(req.Log, "==> importing prebuilt image (%s)\n", humanBytes(fileSize(req.Prebuilt)))
		if len(req.Spec.Packages) > 0 {
			fmt.Fprintf(req.Log, "==> note: packages (%s) are not added to a prebuilt image; install them where it is built\n", strings.Join(req.Spec.Packages, ", "))
		}
		if err := loadImage(ctx, b.eng, req.Prebuilt, ref, req.Log); err != nil {
			return BuildResult{}, &BuildError{Msg: err.Error(), Hint: "Pass a tarball from `docker save <image>` or `nerdctl save`, built for this box's CPU (" + hostArch() + ")."}
		}
		dg, _ := b.eng.ImageDigest(ctx, ref)
		return BuildResult{Image: ref, Digest: dg, Start: startOverride(req.Spec)}, nil
	case req.Spec.Builder == manifest.BuilderPrebuilt:
		return BuildResult{}, &BuildError{Msg: "this app takes prebuilt images only (builder \"prebuilt\"), so there is nothing to build from its source",
			Hint: "Deploy an image built elsewhere (tiffin deploy --prebuilt image.tar), or pick another builder in the app's Build and deploy settings."}
	case req.Dockerfile != "":
		return b.buildDockerfile(ctx, req, ref)
	case req.Export:
		fmt.Fprintf(req.Log, "==> Next.js static export (output: \"export\" in next.config): next build, then the edge serves the files (no container)\n")
		return b.buildFiles(ctx, req, ref, []string{orDefaultStr(req.Spec.Output, orDefaultStr(vercelOut(req.Vercel), "out"))}, false)
	case req.Launch != nil && req.Launch.Err != nil:
		return BuildResult{}, req.Launch.Err
	case req.Launch != nil && req.Launch.Files != nil && req.Spec.Framework != manifest.FrameworkStatic:
		// A server app whose build writes only files: built as it is, served as files.
		dirs := req.Launch.Files.Dirs
		if req.Spec.Output != "" {
			dirs = []string{req.Spec.Output}
		}
		return b.buildFiles(ctx, req, ref, dirs, spaFallback(req, readStaticfile(req.appDir())))
	case req.Spec.Framework == manifest.FrameworkStatic && railpackSite(req):
		fmt.Fprintf(req.Log, "==> the site builds with npm, pnpm or yarn: building with Railpack, then serving the files\n")
		dirs := []string{"dist", "build", "out", "public"}
		if l := req.Launch; l != nil && l.Files != nil {
			dirs = append(slices.Clone(l.Files.Dirs), dirs...)
		}
		sf := readStaticfile(req.appDir())
		if out := orDefaultStr(req.Spec.Output, orDefaultStr(vercelOut(req.Vercel), sf.root)); out != "" {
			dirs = []string{out}
		}
		return b.buildFiles(ctx, req, ref, dirs, spaFallback(req, sf))
	case req.Spec.Framework == manifest.FrameworkStatic:
		return b.buildStatic(ctx, req)
	default:
		return b.buildRailpack(ctx, req, ref)
	}
}

var manifestDigest = regexp.MustCompile(`exporting manifest (sha256:[0-9a-f]{64})`)

func (b *boxBuilder) buildRailpack(ctx context.Context, req BuildRequest, ref string) (BuildResult, error) {
	return b.railpack(ctx, req, ref, nil, "")
}

// railpack builds with Railpack. With files (directories relative to the
// app) it makes no image: BuildKit writes those directories of the built
// app to filesDest (filesDest/tiffin-out/<i>), and nothing else.
func (b *boxBuilder) railpack(ctx context.Context, req BuildRequest, ref string, files []string, filesDest string) (BuildResult, error) {
	d := req.Deploy
	planDir := filepath.Join(req.WorkDir, "plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		return BuildResult{}, err
	}
	env := map[string]string{
		"NEXT_TELEMETRY_DISABLED": "1",
		// Keep the JS heap inside the build's memory cap; swap covers spikes.
		"NODE_OPTIONS": "--max-old-space-size=" + strconv.Itoa(max(768, b.memoryMB-512)),
	}
	if !pinsBun(req.SrcDir) {
		env["RAILPACK_BUN_VERSION"] = BunVersion
	}
	var imageEnv map[string]string
	appDir := req.appDir()
	pm := packageManager(req.SrcDir)
	onNode := req.Spec.Runtime == manifest.RuntimeNode
	// The app builds and runs on one runtime. On Bun, scripts run under
	// --bun, so a tool whose bin asks for node (next build, vite) runs on
	// Bun too; runtime: node keeps both on Node (Railpack installs it either
	// way). Static sites and exports only run a build here.
	if packageScript(appDir, "build") != "" {
		if onNode {
			env["RAILPACK_BUILD_CMD"] = req.inApp(pm + " run build")
		} else {
			env["RAILPACK_BUILD_CMD"] = req.inApp("bun --bun run build")
		}
	}
	start := packageScript(appDir, "start")
	ln := req.Launch
	if ln != nil && ln.Files == nil {
		var err error
		if imageEnv, err = prepareLaunch(req, ln, env); err != nil {
			return BuildResult{}, err
		}
	}
	switch {
	case ln != nil && ln.Start != "":
		env["RAILPACK_START_CMD"] = req.inApp(ln.Start)
	case req.Spec.Framework == manifest.FrameworkNext && !req.Export:
		// Next.js runs as a long-lived server, unless the app chose its own start command.
		if args, ok := nextStartArgs(start); ok {
			// A path runs next in the runtime's own process, so SIGTERM reaches it
			// (after() work finishes); a workspace may keep next at its top, so
			// it goes by name there.
			next := "./node_modules/next/dist/bin/next"
			switch {
			case onNode && req.Dir != "":
				env["RAILPACK_START_CMD"] = req.inApp(execPackageBin("next", "dist/bin/next", " start"+args))
			case onNode:
				env["RAILPACK_START_CMD"] = req.inApp("node " + next + " start" + args)
			default:
				if req.Dir != "" {
					next = "next"
				}
				env["RAILPACK_START_CMD"] = req.inApp("bun --bun " + next + " start" + args)
			}
		} else if !onNode {
			env["RAILPACK_START_CMD"] = req.inApp("bun --bun run start")
		}
		imageEnv = prepareNext(req, env)
	case start != "" && !onNode:
		env["RAILPACK_START_CMD"] = req.inApp("bun --bun run start")
	case serverEntry(appDir, onNode) != "":
		env["RAILPACK_START_CMD"] = req.inApp(serverEntry(appDir, onNode))
	case !onNode && !req.Spec.Framework.IsPython() && bunEntry(appDir) != "":
		env["RAILPACK_START_CMD"] = req.inApp(bunEntry(appDir))
		fmt.Fprintf(req.Log, "==> start command: %s (no start script; the app's entry file)\n", bunEntry(appDir))
	}
	wfEnv, wfInstall, err := prepareWorkflow(req)
	if err != nil {
		return BuildResult{}, err
	}
	for k, v := range wfEnv {
		if imageEnv == nil {
			imageEnv = map[string]string{}
		}
		imageEnv[k] = v
	}
	if req.Dir != "" && env["RAILPACK_START_CMD"] == "" && start != "" {
		// A workspace: Railpack installs at its top; the app starts in its own folder.
		env["RAILPACK_START_CMD"] = req.inApp(pm + " run start")
	}
	if req.Spec.Framework.IsPython() {
		if err := preparePython(req, env); err != nil { // python.go
			return BuildResult{}, err
		}
	}
	if c := workspaceInstall(req.SrcDir, req.Dir, pm); c != "" {
		env["RAILPACK_INSTALL_CMD"] = c
		fmt.Fprintf(req.Log, "==> install: only %s and the workspace packages it uses (%s), not the whole workspace\n", req.Dir, pm)
	}
	if v := req.Vercel; v != nil {
		if v.InstallCommand != "" {
			env["RAILPACK_INSTALL_CMD"] = v.InstallCommand
		}
		if v.BuildCommand != "" {
			env["RAILPACK_BUILD_CMD"] = req.inApp(v.BuildCommand)
		}
	}
	// The app's own settings win over vercel.json and detection.
	if c := req.Spec.Install; c != "" {
		env["RAILPACK_INSTALL_CMD"] = c
		fmt.Fprintf(req.Log, "==> install command: %s (the app's settings)\n", c)
	}
	if c := req.Spec.Build; c != "" {
		env["RAILPACK_BUILD_CMD"] = req.inApp(c)
		fmt.Fprintf(req.Log, "==> build command: %s (the app's settings)\n", c)
	}
	if req.filesOnly() {
		env["RAILPACK_START_CMD"] = "true" // the image only carries the built files: it never runs
	}
	for _, from := range []map[string]string{req.RunEnv, req.Env} {
		for k, v := range from {
			env[k] = v
		}
	}
	if req.Spec.Command != "" && !req.filesOnly() {
		env["RAILPACK_START_CMD"] = req.inApp(req.Spec.Command) // the manifest's command wins over any default
	}
	if c := env["RAILPACK_START_CMD"]; c != "" {
		env["RAILPACK_START_CMD"] = execLast(c)
	}
	if len(req.Spec.Packages) > 0 {
		env[aptPackagesEnv] = aptPackages(env[aptPackagesEnv], req.Spec.Packages)
		fmt.Fprintf(req.Log, "==> installing %s in the image (packages)\n", strings.Join(req.Spec.Packages, ", "))
	}
	planPath := filepath.Join(planDir, "railpack-plan.json")
	args := []string{"prepare", req.SrcDir,
		"--plan-out", planPath,
		"--info-out", filepath.Join(req.WorkDir, "railpack-info.json")}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// Railpack (a root process on the host) gets the app's env as
	// arguments, never in its own environment, where names like PATH or
	// SSL_CERT_FILE would reconfigure it (see secretArgs).
	for _, k := range keys {
		if env[k] != "" { // Railpack skips empty ones too
			args = append(args, "--env", k+"="+env[k])
		}
	}
	fmt.Fprintf(req.Log, "==> planning the build (Railpack %s)\n", RailpackVersion)
	if err := runLogged(ctx, req.Log, req.SrcDir, b.tool("railpack"), args...); err != nil {
		return BuildResult{}, &BuildError{Msg: "Railpack could not plan a build for this app: " + err.Error(),
			Hint: planHint(req.Spec)}
	}
	if err := checkStartCommand(planPath, req, env["RAILPACK_START_CMD"]); err != nil {
		return BuildResult{}, err
	}
	if imageEnv != nil {
		if err := setDeployEnv(planPath, imageEnv); err != nil {
			return BuildResult{}, fmt.Errorf("add the adapter to the build plan: %w", err)
		}
	}
	if wfInstall != "" {
		if err := addBuildCommand(planPath, wfInstall); err != nil {
			return BuildResult{}, fmt.Errorf("add the Workflow DevKit's world to the build plan: %w", err)
		}
	}
	output := "type=image,name=" + ref + ",unpack=true"
	if files != nil {
		if err := filesOnlyPlan(planPath, files); err != nil {
			return BuildResult{}, fmt.Errorf("make the build plan write files only: %w", err)
		}
		output = "type=local,dest=" + filesDest
	}
	defer b.limitBuild(d.Project, req.Log)()
	if files != nil {
		fmt.Fprintf(req.Log, "==> building the site (BuildKit): only %s comes out, no image\n", strings.Join(files, ", "))
	} else {
		fmt.Fprintf(req.Log, "==> building the image (BuildKit)\n")
	}
	// Railpack mounts env vars into build steps as BuildKit secrets (so they
	// never land in image layers); their values travel in buildctl's env
	// under names of the box's own. Railpack prefixes each cache mount with
	// cache-key: the app's own namespace.
	bargs := []string{"build",
		"--progress", "plain",
		"--local", "context=" + req.SrcDir,
		"--local", "dockerfile=" + planDir,
		"--frontend", "gateway.v0",
		"--opt", "source=" + RailpackFrontend,
		"--opt", "build-arg:cache-key=" + cacheNamespace(d.Project, d.App),
		"--opt", "build-arg:secrets-hash=" + envHash(env, keys),
		"--output", output}
	sargs, senv := secretArgs(keys, env)
	bargs = append(bargs, sargs...)
	out := &buildOutput{}
	err = runLoggedEnv(ctx, io.MultiWriter(req.Log, out), req.SrcDir, senv, b.tool("buildctl"), bargs...)
	first, oom, dg := out.result()
	if err != nil {
		hint := "Read the build log (deploys build-log): the first error is usually the cause."
		msg := "the build failed: " + err.Error()
		if first != "" {
			msg = "the build failed: " + first
			hint = "That is the first error in the build log; fix it and deploy again (deploys build-log has the rest)."
		}
		if oom {
			hint = "The build ran out of memory. Build elsewhere and deploy with --prebuilt, or give the box more memory."
		} else {
			hint += onNodeHint(req.Spec)
		}
		return BuildResult{}, &BuildError{Msg: msg, Hint: hint}
	}
	if files != nil {
		return BuildResult{}, nil
	}
	if dg == "" {
		dg, _ = b.eng.ImageDigest(ctx, ref)
	}
	return BuildResult{Image: ref, Digest: dg}, nil
}

// buildStatic serves the files as they are, or runs `bun run build` first
// when package.json has a build script, then serves the output directory.
func (b *boxBuilder) buildStatic(ctx context.Context, req BuildRequest) (BuildResult, error) {
	d := req.Deploy
	appDir := req.appDir()
	sf := readStaticfile(appDir)
	install, build := "", ""
	if exists(filepath.Join(req.SrcDir, "package.json")) {
		install = "bun install"
		if c := workspaceInstall(req.SrcDir, req.Dir, "bun"); c != "" && packageManager(req.SrcDir) == "bun" {
			install = c
		}
	}
	if packageScript(appDir, "build") != "" {
		build = "bun run build"
	}
	if v := req.Vercel; v != nil {
		install, build = orDefaultStr(v.InstallCommand, install), orDefaultStr(v.BuildCommand, build)
		sf.root = orDefaultStr(v.OutputDirectory, sf.root)
	}
	install, build = orDefaultStr(req.Spec.Install, install), orDefaultStr(req.Spec.Build, build)
	sf.root = orDefaultStr(req.Spec.Output, sf.root)
	if build != "" {
		script := req.inApp(build)
		if install != "" {
			script = install + " && " + script
		}
		fmt.Fprintf(req.Log, "==> building the site (%s, Bun %s)\n", script, BunVersion)
		// A helper container (named, labelled, its tasks capped): one cut
		// short is removed below, as killing nerdctl leaves it running.
		name := helperName()
		args := append([]string{"--namespace", Namespace}, helperArgs(name, buildPids)...)
		args = append(args, "--network", "host",
			"--memory", strconv.Itoa(b.memoryMB)+"m",
			"--volume", req.SrcDir+":/app", "--workdir", "/app",
			"--env", "CI=true", "--env", "NODE_ENV=production")
		args = append(args, b.staticCaches(req)...)
		if lim := buildLimitFor(d.Project); lim.cpus > 0 {
			args = append(args, lim.staticBuildArgs()...)
			fmt.Fprint(req.Log, lim.words(d.Project))
		}
		keys := make([]string, 0, len(req.Env))
		for k := range req.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			args = append(args, "--env", k+"="+req.Env[k])
		}
		args = append(args, BunImage, "sh", "-c", script)
		if err := runLogged(ctx, req.Log, req.SrcDir, b.tool("nerdctl"), args...); err != nil {
			if ctx.Err() != nil {
				rctx, cancel := cleanupContext(ctx)
				_ = b.eng.Remove(rctx, name, time.Second)
				cancel()
			}
			return BuildResult{}, &BuildError{Msg: "the static build failed: " + err.Error(), Hint: "Run `" + script + "` locally to reproduce."}
		}
	}
	root := sf.root
	if l := req.Launch; root == "" && l != nil && l.Files != nil {
		root = firstWithEntry(appDir, l.Files.Dirs, req.Launch)
	}
	rootRel := staticRootOf(appDir, root, req.Launch)
	if rootRel == "" {
		return BuildResult{}, &BuildError{Msg: "no index.html found to serve",
			Hint: "Put index.html at the top of the app, in public/, dist/, build/ or out/, or name the folder: Output directory in the app's Build and deploy settings (output in tiffin.config.ts)."}
	}
	if rootRel == "." {
		_ = os.Remove(filepath.Join(appDir, vercelcfg.File)) // config, not content (as on Vercel)
	}
	return b.serveFiles(req, req.SrcDir, filepath.Join(appDir, rootRel), rootRel, spaFallback(req, sf))
}

// staticCacheDirs are the folders, relative to the app, where the tools a
// static site builds with keep work worth keeping between builds: Astro's
// optimized images, content layer and fonts, Vite's, and the general cache
// of babel, imagetools and the like.
var staticCacheDirs = []string{"node_modules/.astro", "node_modules/.vite", "node_modules/.cache", ".next/cache"}

// buildCacheDir is where static builds keep their caches, under the data
// directory: a folder per project and app, as Railpack's cache-key keeps
// BuildKit's cache mounts per app.
const buildCacheDir = "build-cache"

// maxStaticCache is how big an app's static build cache may grow; past it,
// the next build starts from an empty one.
const maxStaticCache = 2 << 30

// staticCaches are the nerdctl run flags that give a static build its
// app's caches: Bun's package cache and staticCacheDirs, each a folder of
// the box's mounted where the build looks for it.
func (b *boxBuilder) staticCaches(req BuildRequest) []string {
	if b.cacheDir == "" {
		return nil
	}
	d := req.Deploy
	dir := filepath.Join(b.cacheDir, d.Project, d.App)
	if size := dirSize(dir); size > maxStaticCache {
		fmt.Fprintf(req.Log, "==> build cache: %s is over %s, so this build starts it afresh\n", humanBytes(size), humanBytes(maxStaticCache))
		_ = os.RemoveAll(dir)
	}
	app := path.Join("/app", filepath.ToSlash(req.Dir))
	args := []string{"--env", "BUN_INSTALL_CACHE_DIR=/tiffin-cache/bun", "--volume", filepath.Join(dir, "bun") + ":/tiffin-cache/bun"}
	warm := exists(filepath.Join(dir, "bun"))
	for i, c := range staticCacheDirs {
		host := filepath.Join(dir, strconv.Itoa(i))
		if err := os.MkdirAll(host, 0o755); err != nil {
			return nil
		}
		args = append(args, "--volume", host+":"+path.Join(app, c))
	}
	if err := os.MkdirAll(filepath.Join(dir, "bun"), 0o755); err != nil {
		return nil
	}
	if warm {
		fmt.Fprintf(req.Log, "==> build cache: warm (packages, and %s from the last build)\n", strings.Join(staticCacheDirs, ", "))
	} else {
		fmt.Fprintf(req.Log, "==> build cache: cold (the first build of this app keeps packages and %s for the next)\n", strings.Join(staticCacheDirs, ", "))
	}
	return args
}

// serveFiles moves a build's output (from, inside root) to where the edge
// serves it.
func (b *boxBuilder) serveFiles(req BuildRequest, root, from, name string, spa bool) (BuildResult, error) {
	d := req.Deploy
	from, err := confinedDir(root, from)
	if err != nil {
		return BuildResult{}, &BuildError{Msg: "the built files can't be served: " + err.Error(),
			Hint: "Links in the output may only point to files of the site itself."}
	}
	dest := filepath.Join(b.staticDir, d.Project, d.App, d.ID)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return BuildResult{}, err
	}
	if err := os.Rename(from, dest); err != nil {
		return BuildResult{}, err
	}
	if err := indexFromPage(dest, req.Launch); err != nil {
		return BuildResult{}, err
	}
	n, size := countFiles(dest)
	fmt.Fprintf(req.Log, "==> serving %d files (%s) from %s/\n", n, humanBytes(size), name)
	finishStatic(dest, req.Log)
	return BuildResult{StaticRoot: dest, SPA: spa}, nil
}

// buildFiles builds with Railpack (the app's package manager and Node.js)
// and serves the first of dirs (relative to the app) that has an
// index.html. No image is made: BuildKit hands over those directories
// alone (filesOnlyPlan), so there are no layers to compress and unpack.
func (b *boxBuilder) buildFiles(ctx context.Context, req BuildRequest, ref string, dirs []string, spa bool) (BuildResult, error) {
	in := make([]string, len(dirs))
	for i, dir := range dirs {
		in[i] = path.Join(req.Dir, dir)
	}
	tmp := filepath.Join(req.WorkDir, "files")
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return BuildResult{}, err
	}
	if _, err := b.railpack(ctx, req, ref, in, tmp); err != nil {
		return BuildResult{}, err
	}
	out := filepath.Join(tmp, "tiffin-out")
	if err := plainTree(out); err != nil {
		return BuildResult{}, fmt.Errorf("the built files: %w", err)
	}
	for i, dir := range dirs {
		if from := filepath.Join(out, strconv.Itoa(i)); hasEntry(from, req.Launch) {
			return b.serveFiles(req, out, from, dir, spa)
		}
	}
	return BuildResult{}, &BuildError{Msg: "the build wrote no index.html in " + strings.Join(dirs, ", "),
		Hint: "A Next.js static export writes out/ (or distDir); name another folder as the Output directory in the app's Build and deploy settings (output in tiffin.config.ts)."}
}

// filesStep is the step filesOnlyPlan adds to a Railpack plan.
const filesStep = "tiffin:files"

// filesScript copies each directory it is given (relative to /app, where
// the build ran) that exists to /tiffin-out/<its index>, following links
// the way nerdctl CopyOut does.
const filesScript = `mkdir -p /tiffin-out || exit 1; i=0; for d in "$@"; do if [ -d "$d" ]; then mkdir -p /tiffin-out/$i && cp -RL "$d"/. /tiffin-out/$i/ || exit 1; fi; i=$((i+1)); done`

// filesOnlyPlan rewrites a Railpack plan so its result is dirs of the built
// app and nothing else: a step after the build copies them to /tiffin-out,
// and the deploy stage is that folder on an empty base. BuildKit then never
// runs the runtime image's steps nor exports layers.
func filesOnlyPlan(planPath string, dirs []string) error {
	raw, err := os.ReadFile(planPath)
	if err != nil {
		return err
	}
	var plan map[string]any
	if err := json.Unmarshal(raw, &plan); err != nil {
		return err
	}
	steps, _ := plan["steps"].([]any)
	from := ""
	for _, s := range steps {
		if step, _ := s.(map[string]any); step != nil && step["name"] == "build" {
			from = "build"
		}
	}
	if from == "" {
		return fmt.Errorf("the plan has no build step")
	}
	cmd := "sh -c " + shellQuote(filesScript) + " sh"
	for _, d := range dirs {
		cmd += " " + shellQuote(d)
	}
	plan["steps"] = append(steps, map[string]any{
		"name":     filesStep,
		"inputs":   []any{map[string]any{"step": from}},
		"commands": []any{map[string]any{"cmd": cmd, "customName": "copy the built files out"}},
	})
	plan["deploy"] = map[string]any{
		"base":   map[string]any{},
		"inputs": []any{map[string]any{"step": filesStep, "include": []any{"/tiffin-out"}}},
	}
	out, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(planPath, out, 0o644)
}

// vercelOut is vercel.json's outputDirectory ("" without one).
func vercelOut(v *vercelcfg.Config) string {
	if v == nil {
		return ""
	}
	return v.OutputDirectory
}

var otherPMs = regexp.MustCompile(`\b(npm|npx|pnpm|yarn)\b`)

// railpackSite reports whether a static site's build needs npm, pnpm or
// yarn, which the Bun image that builds static sites does not have: its
// vercel.json commands use one, or it builds in a workspace managed by one.
func railpackSite(req BuildRequest) bool {
	install, build := req.Spec.Install, req.Spec.Build
	if v := req.Vercel; v != nil {
		install, build = orDefaultStr(install, v.InstallCommand), orDefaultStr(build, v.BuildCommand)
	}
	if otherPMs.MatchString(install + " " + build) {
		return true
	}
	return req.Dir != "" && packageManager(req.SrcDir) != "bun" && packageScript(req.appDir(), "build") != ""
}

// workspaceInstall is the install command for an app in a workspace
// (monorepo) at dir, the app's folder in top: it installs the app, the
// workspace packages it depends on and the workspace's own (root)
// dependencies, not every other package. If that install fails, the whole
// workspace installs as Railpack would, and the log says so. "": no
// workspace, or a package manager that cannot install one package (Yarn 1).
//
//	pnpm  pnpm install --filter {./apps/web}...   (the root comes along)
//	bun   bun install --filter ./ --filter ./apps/web   (workspace deps come along)
//	npm   npm install --workspace apps/web --include-workspace-root
//	yarn  yarn workspaces focus web root   (Yarn 2+)
func workspaceInstall(top, dir, pm string) string {
	// Railpack drops quotes from an install command, so only plain words
	// go in it (a folder or name with other characters installs whole).
	if dir == "" || dir == "." || !plainWord.MatchString(dir) {
		return ""
	}
	locked := func(f string) bool { return exists(filepath.Join(top, f)) }
	var filtered, full string
	switch pm {
	case "pnpm":
		flags := ""
		if locked("pnpm-lock.yaml") {
			flags = " --frozen-lockfile --prefer-offline"
		}
		filtered = "pnpm install" + flags + " --filter {./" + dir + "}..."
		full = "pnpm install" + flags
	case "bun":
		flags := ""
		if locked("bun.lock") || locked("bun.lockb") {
			flags = " --frozen-lockfile"
		}
		filtered = "bun install" + flags + " --filter ./ --filter ./" + dir
		full = "bun install" + flags
	case "npm":
		filtered = "npm install --workspace " + dir + " --include-workspace-root"
		full = "npm install"
	case "yarn":
		name := packageName(filepath.Join(top, filepath.FromSlash(dir)))
		if !yarnBerry(top) || !plainWord.MatchString(name) {
			return ""
		}
		filtered = "yarn workspaces focus " + name
		if root := packageName(top); plainWord.MatchString(root) {
			filtered += " " + root
		}
		full = "yarn install --check-cache"
	default:
		return ""
	}
	return filtered + " || { echo tiffin: installing " + dir + " alone failed, so the whole workspace installs instead; " + full + "; }"
}

// plainWord is a folder or package name a shell reads as one word as it is.
var plainWord = regexp.MustCompile(`^[A-Za-z0-9@][A-Za-z0-9._/@+-]*$`)

// yarnBerry reports whether a Yarn workspace uses Yarn 2 or later.
func yarnBerry(top string) bool {
	if exists(filepath.Join(top, ".yarnrc.yml")) {
		return true
	}
	raw, _ := os.ReadFile(filepath.Join(top, "package.json"))
	var pkg struct {
		PackageManager string `json:"packageManager"`
	}
	_ = json.Unmarshal(raw, &pkg)
	pm, v, _ := strings.Cut(pkg.PackageManager, "@")
	return pm == "yarn" && v != "" && !strings.HasPrefix(v, "1.")
}

// packageName is the name in dir's package.json ("" without one).
func packageName(dir string) string {
	raw, _ := os.ReadFile(filepath.Join(dir, "package.json"))
	var pkg struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(raw, &pkg)
	return pkg.Name
}

// packageManager is the command that runs a workspace's scripts, by its
// lockfile or package.json "packageManager".
func packageManager(dir string) string {
	for _, l := range [][2]string{{"pnpm-lock.yaml", "pnpm"}, {"yarn.lock", "yarn"}, {"package-lock.json", "npm"}, {"bun.lock", "bun"}, {"bun.lockb", "bun"}} {
		if exists(filepath.Join(dir, l[0])) {
			return l[1]
		}
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "package.json"))
	var pkg struct {
		PackageManager string `json:"packageManager"`
	}
	_ = json.Unmarshal(raw, &pkg)
	if pm, _, _ := strings.Cut(pkg.PackageManager, "@"); pm == "pnpm" || pm == "yarn" || pm == "npm" {
		return pm
	}
	return "bun"
}

var (
	nextConfigs = []string{"next.config.js", "next.config.mjs", "next.config.ts", "next.config.mts", "next.config.cjs"}
	exportRe    = regexp.MustCompile("\\boutput\\s*:\\s*[\"'`]export[\"'`]")
	jsComments  = regexp.MustCompile(`(?s)/\*.*?\*/|(^|[^:])//[^\n]*`)
)

// nextExport reports whether the Next.js app in dir builds a static export:
// its next.config sets output: "export".
func nextExport(dir string) bool {
	if !packageDeps(dir)["next"] {
		return false
	}
	for _, n := range nextConfigs {
		if raw, err := os.ReadFile(filepath.Join(dir, n)); err == nil {
			return exportRe.Match(jsComments.ReplaceAll(raw, []byte("$1")))
		}
	}
	return false
}

type staticfile struct {
	root   string
	spa    bool
	spaSet bool // index_fallback was written, either way
}

// readStaticfile reads Railpack's Staticfile (root:, index_fallback:).
func readStaticfile(dir string) staticfile {
	var sf staticfile
	f, err := os.Open(filepath.Join(dir, "Staticfile"))
	if err != nil {
		return sf
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch strings.TrimSpace(k) {
		case "root":
			sf.root = v
		case "index_fallback":
			sf.spa, sf.spaSet = v == "true", true
		}
	}
	return sf
}

// onNodeHint is the way out for an app that fails on Bun: Node.js. None
// for apps already on Node or served as files.
func onNodeHint(spec manifest.App) string {
	if spec.Runtime == manifest.RuntimeNode || spec.Framework == manifest.FrameworkStatic || spec.Builder == manifest.BuilderDockerfile || spec.Builder == manifest.BuilderPrebuilt || spec.Framework.IsPython() {
		return ""
	}
	return " If it works on Node.js, switch the app to Node.js (its Runtime setting, or runtime: \"node\" in tiffin.config.ts) and deploy again."
}

// clientRouters are routers that draw pages in the browser: a site built
// with one has paths no file answers, so a refresh on /about needs
// index.html.
var clientRouters = []string{"react-router", "react-router-dom", "@tanstack/react-router", "vue-router", "wouter", "preact-router", "@solidjs/router", "svelte-spa-router", "@angular/router"}

// spaFallback reports whether unknown paths serve index.html: the
// Staticfile's index_fallback when it says, else whether package.json uses a
// client-side router. It notes an automatic fallback in the build log.
func spaFallback(req BuildRequest, sf staticfile) bool {
	if sf.spaSet {
		return sf.spa
	}
	if l := req.Launch; l != nil && l.Files != nil {
		return l.Files.SPA // the framework's own setting (ssr: false, a fallback page)
	}
	raw, err := os.ReadFile(filepath.Join(req.appDir(), "package.json"))
	if err != nil {
		return false
	}
	var pj struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal(raw, &pj) != nil {
		return false
	}
	for _, r := range clientRouters {
		_, a := pj.Dependencies[r]
		_, b := pj.DevDependencies[r]
		if a || b {
			fmt.Fprintf(req.Log, "==> %s draws pages in the browser: paths without a file serve index.html (index_fallback: false in a Staticfile turns this off)\n", r)
			return true
		}
	}
	return false
}

// firstWithEntry is the first of dirs (relative to dir) with an entry
// page (see hasEntry), "" for none.
func firstWithEntry(dir string, dirs []string, l *launch) string {
	for _, d := range dirs {
		if hasEntry(filepath.Join(dir, filepath.FromSlash(d)), l) {
			return d
		}
	}
	return ""
}

// hasEntry reports whether a built site in dir has a page to start from:
// index.html, or the framework's SPA page (an adapter-static fallback such
// as 200.html, which SvelteKit writes without an index.html when it
// prerenders nothing).
func hasEntry(dir string, l *launch) bool {
	if isFile(filepath.Join(dir, "index.html")) {
		return true
	}
	return l != nil && l.Files != nil && l.Files.Page != "" && isFile(filepath.Join(dir, filepath.FromSlash(l.Files.Page)))
}

// indexFromPage gives a site with an SPA page but no index.html one, a
// copy of the page: the edge answers / with index.html (a folder without
// one is a 404), and without a prerendered home page the SPA page is it.
func indexFromPage(dir string, l *launch) error {
	if isFile(filepath.Join(dir, "index.html")) || !hasEntry(dir, l) {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(l.Files.Page)))
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "index.html"), raw, 0o644)
}

// isFile reports whether p is a file (a link to one counts).
func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// spaPage is the page a single-page app built to root serves for paths
// without a file, when it is not /index.html: the framework's own (an
// adapter-static fallback, Nuxt's 200.html), or React Router's
// __spa-fallback.html, which it writes when it prerenders the home page.
func spaPage(root string, l *launch) string {
	var pages []string
	if l != nil && l.Files != nil && l.Files.Page != "" {
		pages = append(pages, l.Files.Page)
	}
	for _, p := range append(pages, "/__spa-fallback.html") {
		if exists(filepath.Join(root, filepath.FromSlash(p))) {
			return p
		}
	}
	return ""
}

// staticRootOf picks the directory to serve, relative to dir ("." for dir
// itself): the first with an entry page (see hasEntry).
func staticRootOf(dir, configured string, l *launch) string {
	cands := []string{"dist", "build", "out", "public", "."}
	if configured != "" {
		cands = []string{configured}
	}
	for _, c := range cands {
		c = filepath.Clean(c)
		if strings.HasPrefix(c, "..") || filepath.IsAbs(c) {
			continue
		}
		if hasEntry(filepath.Join(dir, c), l) {
			return c
		}
	}
	return ""
}

// nextStartRe is a start script that only starts Next.js, with options.
var nextStartRe = regexp.MustCompile(`^(?:bunx? (?:--bun )?|npx )?next start((?: (?:-p|--port|-H|--hostname|--keepAliveTimeout)[ =][\w.:${}-]+)*)$`)

// nextStartArgs reports whether a start script ("" for none) only starts
// Next.js, and the options it passes. The box then starts Next.js itself,
// on Bun and without `bun run` in between.
func nextStartArgs(script string) (string, bool) {
	if script == "" {
		return "", true
	}
	m := nextStartRe.FindStringSubmatch(script)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// execLast makes the shell that runs a start command (Railpack's bash -c)
// replace itself with the command's last step, so the app is tini's direct
// child and gets its signals (SIGTERM: Next.js finishes requests and
// after() work before it exits). A command with shell syntax beyond
// `cd <dir> && <command>` stays as it is.
func execLast(cmd string) string {
	prefix, last := "", cmd
	if i := strings.LastIndex(cmd, " && "); i >= 0 && strings.HasPrefix(cmd, "cd ") && !strings.ContainsAny(cmd[:i], ";|&\n") {
		prefix, last = cmd[:i+4], cmd[i+4:]
	}
	f := strings.Fields(last)
	if len(f) == 0 || last == "true" || f[0] == "exec" || strings.Contains(f[0], "=") || strings.ContainsAny(last, ";&|<>()`\n") {
		return cmd
	}
	return prefix + "exec " + last
}

// checkStartCommand fails a build Railpack planned without a start
// command (none given, none detected) before it runs: the image would
// build (minutes) and then exit at once with no output.
func checkStartCommand(planPath string, req BuildRequest, given string) error {
	if given != "" || req.filesOnly() {
		return nil
	}
	raw, err := os.ReadFile(planPath)
	if err != nil {
		return nil
	}
	var plan struct {
		Deploy struct {
			StartCommand string `json:"startCommand"`
		} `json:"deploy"`
	}
	if json.Unmarshal(raw, &plan) != nil || strings.TrimSpace(plan.Deploy.StartCommand) != "" {
		return nil
	}
	return &BuildError{Msg: "no start command: the app's package.json has no start script and Railpack found no file to run",
		Hint: "Add a start script to package.json (\"start\": \"bun src/index.ts\", say), or set the app's start command (command in tiffin.config.ts, or Start command in its Build and deploy settings), and deploy again."}
}

func packageScript(dir, name string) string {
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return ""
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	_ = json.Unmarshal(raw, &pkg)
	return strings.TrimSpace(pkg.Scripts[name])
}

// aptPackagesEnv makes Railpack install Debian packages in the image it
// runs (the deploy stage), not only where it builds.
const aptPackagesEnv = "RAILPACK_DEPLOY_APT_PACKAGES"

// aptPackages adds the manifest's packages to a RAILPACK_DEPLOY_APT_PACKAGES
// value (space-separated). "..." keeps the packages Railpack adds itself.
func aptPackages(cur string, pkgs []string) string {
	list := strings.Fields(cur)
	if len(list) == 0 {
		list = []string{"..."}
	}
	for _, p := range pkgs {
		if !slices.Contains(list, p) {
			list = append(list, p)
		}
	}
	return strings.Join(list, " ")
}

// pinsBun reports whether the app chose a Bun version itself.
func pinsBun(dir string) bool {
	if exists(filepath.Join(dir, ".bun-version")) || exists(filepath.Join(dir, "mise.toml")) || exists(filepath.Join(dir, ".tool-versions")) {
		return true
	}
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		PackageManager string            `json:"packageManager"`
		Engines        map[string]string `json:"engines"`
	}
	_ = json.Unmarshal(raw, &pkg)
	return strings.HasPrefix(pkg.PackageManager, "bun@") || pkg.Engines["bun"] != ""
}

// runLogged runs a command with its output streamed to w.
var (
	// BuildKit prefixes each line of a step with "#<step> <seconds> ".
	buildkitPrefix = regexp.MustCompile(`^#\d+ \d+(\.\d+)? `)
	// Lines that name the cause (compiler and bundler errors), not the
	// wrappers that only say a step failed.
	buildErrorLine  = regexp.MustCompile(`error TS\d+|Type error:|Module not found|SyntaxError|Cannot find module|npm ERR!|^error: |^Error: |\berror\[`)
	buildErrorNoise = regexp.MustCompile(`^error: (script ".*" exited|failed to solve)|did not complete successfully|exited with code`)
)

// firstBuildError finds the first line of build output that names a cause,
// so a failed deploy says why without reading the whole log.
func firstBuildError(out string) string {
	o := &buildOutput{}
	_, _ = o.Write([]byte(out))
	first, _, _ := o.result()
	return first
}

// errorLine is line, trimmed, when it names a cause; "" otherwise.
func errorLine(line string) string {
	line = strings.TrimSpace(buildkitPrefix.ReplaceAllString(line, ""))
	if line == "" || !buildErrorLine.MatchString(line) || buildErrorNoise.MatchString(line) {
		return ""
	}
	if len(line) > 300 {
		line = line[:300] + "…"
	}
	return line
}

// configFiles are tiffin's own config: the box reads them, apps never import
// them, and they import @shiptiffin/sdk, which a type-checking build (next build)
// cannot resolve. Builds leave them out.
var configFiles = []string{"tiffin.config.ts", "tiffin.config.mts", "tiffin.config.js", "tiffin.config.mjs"}

// dropConfig removes tiffin.config.* from the top of an unpacked source tree.
func dropConfig(srcDir string, log io.Writer) {
	for _, name := range configFiles {
		if err := os.Remove(filepath.Join(srcDir, name)); err == nil {
			fmt.Fprintf(log, "==> %s is read by tiffin, not built into the app: left out of the build\n", name)
		}
	}
}

func runLogged(ctx context.Context, w io.Writer, dir, name string, args ...string) error {
	return runLoggedEnv(ctx, w, dir, nil, name, args...)
}

// runLoggedEnv is runLogged with extra variables in the command's
// environment: only names the box picks, never an app's (see secretArgs).
func runLoggedEnv(ctx context.Context, w io.Writer, dir string, env []string, name string, args ...string) error {
	c := exec.CommandContext(ctx, name, args...)
	c.Dir = dir
	c.Env = append(toolEnv(), env...)
	c.Stdout, c.Stderr = w, w
	c.WaitDelay = 5 * time.Second
	if err := c.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%s: %w", filepath.Base(name), ctx.Err())
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return fmt.Errorf("%s exited with status %d", filepath.Base(name), ee.ExitCode())
		}
		return err
	}
	return nil
}

func fileSize(p string) int64 {
	fi, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return fi.Size()
}

func countFiles(dir string) (n int, size int64) {
	_ = filepath.Walk(dir, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			n++
			size += fi.Size()
		}
		return nil
	})
	return n, size
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func hostArch() string { return goruntime.GOARCH }

func envHash(env map[string]string, keys []string) string {
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%s\x00", k, env[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// finishStatic readies a static site's files for the edge: it drops what
// is not the site's to serve (Vite's .vite/ build manifest) and writes a
// zstd and a gzip copy of each text file, which the edge sends as they are
// instead of compressing every response.
func finishStatic(root string, log io.Writer) {
	if exists(filepath.Join(root, ".vite")) {
		_ = os.RemoveAll(filepath.Join(root, ".vite"))
		fmt.Fprintf(log, "==> .vite/ (Vite's build manifest) is left out: it is not served\n")
	}
	began := time.Now()
	n, err := switchboard.Precompress(root)
	if err != nil {
		fmt.Fprintf(log, "==> note: could not compress the files ahead (%v); the edge compresses them as it sends them\n", err)
		return
	}
	if n > 0 {
		files := "files"
		if n == 1 {
			files = "file"
		}
		fmt.Fprintf(log, "==> compressed %d text %s ahead (zstd and gzip) in %.1fs\n", n, files, time.Since(began).Seconds())
	}
}
