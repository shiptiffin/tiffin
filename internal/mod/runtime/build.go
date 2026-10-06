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
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/mod/budget"
)

// BuildRequest is one build.
type BuildRequest struct {
	Deploy   *Deploy
	Spec     manifest.App
	SrcDir   string            // unpacked source (empty for prebuilt)
	WorkDir  string            // scratch space for this deploy
	Prebuilt string            // image tarball to import instead of building
	Env      map[string]string // env visible at build time: plain env, and a Next.js app's Server Actions key
	// NextCache: the project has Valkey, so a Next.js app's adapter wires
	// the shared cache handlers.
	NextCache bool
	Log       io.Writer
}

// BuildResult is what a build produced: an image for container apps or a
// directory of files for static sites.
type BuildResult struct {
	Image      string
	Digest     string
	StaticRoot string
	SPA        bool
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
	memoryMB  int    // cap for static build containers
	cgroupDir string // the build cgroup ("" → /sys/fs/cgroup/tiffin-build)
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

func (l buildLimit) words(project string) string {
	return fmt.Sprintf("==> %s is limited to %d%% of the box: this build may use %s CPUs and slows down past %d MB\n",
		project, l.pct, strconv.FormatFloat(l.cpus, 'f', -1, 64), l.highMB)
}

func (b *boxBuilder) Build(ctx context.Context, req BuildRequest) (BuildResult, error) {
	d := req.Deploy
	ref := imageRef(d.Project, d.App, d.ID)
	switch {
	case req.Prebuilt != "":
		fmt.Fprintf(req.Log, "==> importing prebuilt image (%s)\n", humanBytes(fileSize(req.Prebuilt)))
		if err := loadImage(ctx, b.eng, req.Prebuilt, ref, req.Log); err != nil {
			return BuildResult{}, &BuildError{Msg: err.Error(), Hint: "Pass a tarball from `docker save <image>` or `nerdctl save`, built for this box's CPU (" + hostArch() + ")."}
		}
		dg, _ := b.eng.ImageDigest(ctx, ref)
		return BuildResult{Image: ref, Digest: dg}, nil
	case req.Spec.Framework == manifest.FrameworkStatic:
		return b.buildStatic(ctx, req)
	default:
		return b.buildRailpack(ctx, req, ref)
	}
}

var manifestDigest = regexp.MustCompile(`exporting manifest (sha256:[0-9a-f]{64})`)

func (b *boxBuilder) buildRailpack(ctx context.Context, req BuildRequest, ref string) (BuildResult, error) {
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
	if req.Spec.Framework == manifest.FrameworkNext {
		// Next.js runs on Bun as a long-lived server, unless the app chose its own start command.
		if start := packageScript(req.SrcDir, "start"); start == "" || start == "next start" {
			env["RAILPACK_START_CMD"] = "bun --bun next start"
		}
		imageEnv = prepareNext(req, env)
	}
	for k, v := range req.Env {
		env[k] = v
	}
	if req.Spec.Command != "" {
		env["RAILPACK_START_CMD"] = req.Spec.Command // the manifest's command wins over any default
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
	// Names on the command line, values in the environment (Railpack and
	// buildctl read them there), so `ps` never shows a secret.
	var extra []string
	for _, k := range keys {
		if env[k] != "" { // Railpack skips empty ones too
			args = append(args, "--env", k)
		}
		extra = append(extra, k+"="+env[k])
	}
	fmt.Fprintf(req.Log, "==> planning the build (Railpack %s)\n", RailpackVersion)
	if err := runLoggedEnv(ctx, req.Log, req.SrcDir, extra, "/usr/local/bin/railpack", args...); err != nil {
		return BuildResult{}, &BuildError{Msg: "Railpack could not plan a build for this app: " + err.Error(),
			Hint: "Make sure the app has a package.json with a start script (or an index.ts), and a lockfile. See the build log for details."}
	}
	if imageEnv != nil {
		if err := setDeployEnv(planPath, imageEnv); err != nil {
			return BuildResult{}, fmt.Errorf("add the adapter to the build plan: %w", err)
		}
	}
	// A limited project's build counts against its share. Builds run one at
	// a time, so the shared build cgroup is this build's while it runs.
	if lim := buildLimitFor(d.Project); lim.cpus > 0 {
		dir := b.cgroupDir
		if dir == "" {
			dir = filepath.Join("/sys/fs/cgroup", buildCgroup)
		}
		undo, err := lim.apply(dir)
		defer undo()
		if err == nil {
			fmt.Fprint(req.Log, lim.words(d.Project))
		}
	}
	fmt.Fprintf(req.Log, "==> building the image (BuildKit)\n")
	// Railpack mounts env vars into build steps as BuildKit secrets (so they
	// never land in image layers); their values travel in buildctl's env.
	bargs := []string{"build",
		"--progress", "plain",
		"--local", "context=" + req.SrcDir,
		"--local", "dockerfile=" + planDir,
		"--frontend", "gateway.v0",
		"--opt", "source=" + RailpackFrontend,
		"--opt", "build-arg:cache-key=" + d.Project + "-" + d.App,
		"--opt", "build-arg:secrets-hash=" + envHash(env, keys),
		"--output", "type=image,name=" + ref + ",unpack=true"}
	for _, k := range keys {
		bargs = append(bargs, "--secret", "id="+k+",env="+k)
	}
	var out strings.Builder
	w := io.MultiWriter(req.Log, &out)
	err := runLoggedEnv(ctx, w, req.SrcDir, extra, "/usr/local/bin/buildctl", bargs...)
	if err != nil {
		hint := "Read the build log (deploys build-log): the first error is usually the cause."
		msg := "the build failed: " + err.Error()
		if first := firstBuildError(out.String()); first != "" {
			msg = "the build failed: " + first
			hint = "That is the first error in the build log; fix it and deploy again (deploys build-log has the rest)."
		}
		if strings.Contains(out.String(), "exit code: 137") || strings.Contains(out.String(), "Killed") {
			hint = "The build ran out of memory. Build elsewhere and deploy with --prebuilt, or give the box more memory."
		}
		return BuildResult{}, &BuildError{Msg: msg, Hint: hint}
	}
	dg := ""
	if m := manifestDigest.FindStringSubmatch(out.String()); m != nil {
		dg = m[1]
	} else {
		dg, _ = b.eng.ImageDigest(ctx, ref)
	}
	return BuildResult{Image: ref, Digest: dg}, nil
}

// buildStatic serves the files as they are, or runs `bun run build` first
// when package.json has a build script, then serves the output directory.
func (b *boxBuilder) buildStatic(ctx context.Context, req BuildRequest) (BuildResult, error) {
	d := req.Deploy
	sf := readStaticfile(req.SrcDir)
	if packageScript(req.SrcDir, "build") != "" {
		fmt.Fprintf(req.Log, "==> building the site (bun install && bun run build, Bun %s)\n", BunVersion)
		args := []string{"--namespace", Namespace, "run", "--rm", "--network", "host",
			"--memory", strconv.Itoa(b.memoryMB) + "m",
			"--volume", req.SrcDir + ":/app", "--workdir", "/app",
			"--env", "CI=true", "--env", "NODE_ENV=production"}
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
		args = append(args, BunImage, "sh", "-c", "bun install && bun run build")
		if err := runLogged(ctx, req.Log, req.SrcDir, "/usr/local/bin/nerdctl", args...); err != nil {
			return BuildResult{}, &BuildError{Msg: "the static build failed: " + err.Error(), Hint: "Run `bun install && bun run build` locally to reproduce."}
		}
	}
	rootRel := staticRootOf(req.SrcDir, sf.root)
	if rootRel == "" {
		return BuildResult{}, &BuildError{Msg: "no index.html found to serve",
			Hint: "Put index.html at the top of the app, in public/, dist/, build/ or out/, or name the folder in a Staticfile (root: <dir>)."}
	}
	dest := filepath.Join(b.staticDir, d.Project, d.App, d.ID)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return BuildResult{}, err
	}
	if err := os.Rename(filepath.Join(req.SrcDir, rootRel), dest); err != nil {
		return BuildResult{}, err
	}
	n, size := countFiles(dest)
	fmt.Fprintf(req.Log, "==> serving %d files (%s) from %s/\n", n, humanBytes(size), rootRel)
	return BuildResult{StaticRoot: dest, SPA: sf.spa}, nil
}

type staticfile struct {
	root string
	spa  bool
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
			sf.spa = v == "true"
		}
	}
	return sf
}

// staticRootOf picks the directory to serve, relative to dir ("." for dir itself).
func staticRootOf(dir, configured string) string {
	cands := []string{"dist", "build", "out", "public", "."}
	if configured != "" {
		cands = []string{configured}
	}
	for _, c := range cands {
		c = filepath.Clean(c)
		if strings.HasPrefix(c, "..") || filepath.IsAbs(c) {
			continue
		}
		if fi, err := os.Stat(filepath.Join(dir, c, "index.html")); err == nil && fi.Mode().IsRegular() {
			return c
		}
	}
	return ""
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
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(buildkitPrefix.ReplaceAllString(sc.Text(), ""))
		if line == "" || !buildErrorLine.MatchString(line) || buildErrorNoise.MatchString(line) {
			continue
		}
		if len(line) > 300 {
			line = line[:300] + "…"
		}
		return line
	}
	return ""
}

// configFiles are tiffin's own config: the box reads them, apps never import
// them, and they import tiffin-sdk, which a type-checking build (next build)
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
