package runtime

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Full-stack frameworks the box knows by their build output: SvelteKit,
// Nuxt, React Router (framework mode) and Astro. Each one builds with Railpack like
// any Bun or Node app; what the box adds is in its launch: the start command
// (exec'd, so the server gets SIGTERM and drains), the env its server reads
// behind the box's proxy, a smoke test after the health check, and, when
// the app's build writes only files (SvelteKit's adapter-static, Nuxt's
// `nuxt generate`, React Router with ssr: false), serving those files from
// the edge instead of running a server. Next.js keeps its own path
// (nextjs.go); TanStack Start starts from its start script.

// launch is what the box does for one app's framework, read from its
// source before the build.
type launch struct {
	ID   string // the preset id: sveltekit, nuxt, react-router, astro
	Name string // as people know it
	// Start is the start command, run in the app's folder; "" leaves the
	// app's own start script.
	Start string
	// Why says where the start command came from, for the build log.
	Why string
	// Build is env for the build only (adapter and preset choices).
	Build map[string]string
	// Run is env baked into the image for the server: proxy headers,
	// shutdown and keep-alive timeouts. The app's own env wins.
	Run map[string]string
	// Files: the build writes only files, served by the edge (no server).
	Files *launchFiles
	// Runner: write the box's Bun server for React Router into the source.
	Runner bool
	// Secret is env the box makes once per app and keeps (sealed): the
	// same value in every instance and preview, and in builds too with
	// SecretAtBuild.
	Secret string
	// SecretAtBuild: the build reads Secret too (Astro writes ASTRO_KEY
	// into its server build).
	SecretAtBuild bool
	// Warn is a warning for the deploy (it works, but not as well as it could).
	Warn string
	// Err fails the build before it starts, with a hint.
	Err *BuildError
}

// launchFiles is a build whose output is a site of files.
type launchFiles struct {
	Dirs []string // where the build writes them, relative to the app, first found wins
	SPA  bool     // unknown paths serve a page (client-side routing)
	// Page is the page they serve ("" for index.html): React Router's
	// __spa-fallback.html, SvelteKit's adapter-static fallback, Nuxt's 200.html.
	Page string
	Why  string
}

// Instance env the frameworks' servers read behind the box's proxy. The
// edge sets X-Forwarded-Proto, -Host and -For (the client's address only),
// and the switchboard keeps the Host header; it drains requests, then
// sends SIGTERM and kills after 30 s.
var (
	// SvelteKit (adapter-bun and adapter-node): without the headers its
	// origin check refuses every form action (403, "Cross-site POST form
	// submissions are forbidden"), as it assumes https://<Host>.
	kitRun = map[string]string{
		"PROTOCOL_HEADER": "x-forwarded-proto",
		"HOST_HEADER":     "x-forwarded-host",
		"ADDRESS_HEADER":  "x-forwarded-for",
		"XFF_DEPTH":       "1",
		// The edge and the app's memory cap bound uploads; SvelteKit's own
		// 512 KB default refuses ordinary file uploads.
		"BODY_SIZE_LIMIT":  "Infinity",
		"SHUTDOWN_TIMEOUT": "25",
	}
	// adapter-bun: Bun.serve's idle timeout (10 s by default) would close
	// connections the switchboard keeps for reuse (60 s).
	kitBunRun = map[string]string{"CONNECTION_IDLE_TIMEOUT": "0"}
	// adapter-node: Node's 5 s keep-alive is shorter than the switchboard's
	// 60 s, so a POST after a quiet spell could land on a closing socket.
	kitNodeRun = map[string]string{"KEEP_ALIVE_TIMEOUT": "65", "HEADERS_TIMEOUT": "66"}
	// Nuxt (Nitro's node-server output).
	nuxtRun = map[string]string{"NITRO_SHUTDOWN_TIMEOUT": "25000"}
	// React Router on the box's Bun server.
	rrRun = map[string]string{"SHUTDOWN_TIMEOUT": "25"}
	// Astro's @astrojs/node standalone server listens on localhost unless
	// HOST says otherwise, which Node may bind to ::1 alone, while the box
	// reaches instances at 127.0.0.1:$PORT. All interfaces is how every app
	// listens (the runtime firewall keeps app ports off the network). It
	// reads PORT, which every instance gets.
	astroRun = map[string]string{"HOST": "0.0.0.0"}
)

// nuxtSecretEnv is Nuxt's key for sessions and deriveSecret (4.6): never
// made by a build, and it must stay the same across deploys and instances.
const nuxtSecretEnv = "NUXT_APP_SECRET"

// astroKeyEnv is the key Astro encrypts server islands' props with. Without
// it each build makes its own, so a page of one release asking a server
// island of the next (a rolling deploy, a page cached in a tab) fails to
// decrypt. astro build reads it, and the server at run time.
const astroKeyEnv = "ASTRO_KEY"

// keptSecrets are the launches' Secrets: an instance gets the kept value of
// each, unless the app sets its own.
var keptSecrets = []string{nuxtSecretEnv, astroKeyEnv}

// React Router's server on Bun: the box writes it into the build context.
const (
	rrDir        = ".tiffin/react-router"
	rrRunnerPath = "/app/" + rrDir + "/serve.js"
)

//go:embed reactrouterserve.js
var rrRunnerJS string

// launchFor reads the app in appDir (the app's folder, the build context's
// top for single apps) and returns what the box does for its framework,
// nil for an app it starts like any other. build is the build command that
// runs (the app's settings, else vercel.json's), "" for its build script.
func launchFor(appDir string, onNode bool, build string) *launch {
	deps := packageDeps(appDir)
	start := packageScript(appDir, "start")
	if build == "" {
		build = packageScript(appDir, "build")
	} else if m := runScriptRe.FindStringSubmatch(build); m != nil && packageScript(appDir, m[1]) != "" {
		build = packageScript(appDir, m[1]) // `npm run generate`: what the script runs
	}
	switch {
	case deps["@sveltejs/kit"]:
		return svelteKitLaunch(appDir, deps, start, onNode)
	case deps["nuxt"]:
		return nuxtLaunch(appDir, start, build, onNode)
	case deps["@react-router/dev"]:
		return reactRouterLaunch(appDir, deps, start, onNode)
	case deps["astro"]:
		return astroLaunch(appDir, deps, start, onNode)
	}
	return nil
}

// runScriptRe is a command that runs one of package.json's scripts (`bun
// build` is Bun's bundler and `npm build` no script, so those need run).
var runScriptRe = regexp.MustCompile(`^(?:(?:bun(?: --bun)?|npm) run|(?:pnpm|yarn)(?: run)?) ([\w:.-]+)$`)

// --- SvelteKit -------------------------------------------------------------

var (
	kitConfigs   = []string{"vite.config.ts", "vite.config.js", "vite.config.mts", "vite.config.mjs", "svelte.config.js", "svelte.config.ts", "svelte.config.mjs"}
	kitAdapterRe = regexp.MustCompile(`@sveltejs/adapter-([a-z-]+)`)
	kitOutRe     = regexp.MustCompile("\\b(?:out|pages)\\s*:\\s*[\"'`]([^\"'`]+)[\"'`]")
	kitFallback  = regexp.MustCompile("\\bfallback\\s*:\\s*[\"'`]([^\"'`]+)[\"'`]")
	// The start scripts a SvelteKit app writes for its own build, which the
	// box replaces with the same command, exec'd.
	kitStartRe = regexp.MustCompile(`^(?:bun|node)(?: --bun)? (?:\./)?build(?:/index\.js)?/?$`)
)

// readConfig is the first of names in dir that exists, comments removed.
func readConfig(dir string, names []string) string {
	for _, n := range names {
		if raw, err := readSrc(filepath.Join(dir, n)); err == nil {
			return string(jsComments.ReplaceAll(raw, []byte("$1")))
		}
	}
	return ""
}

// kitConfig is the SvelteKit config that sets the adapter: SvelteKit 3
// configures it in vite.config, SvelteKit 2 in svelte.config next to an
// ordinary vite.config, so the first file that names an adapter wins, else
// the first that exists.
func kitConfig(dir string) string {
	first := ""
	for _, n := range kitConfigs {
		cfg := readConfig(dir, []string{n})
		if kitAdapterRe.MatchString(cfg) {
			return cfg
		}
		if first == "" {
			first = cfg
		}
	}
	return first
}

// svelteKitAdapter is the adapter the app's config imports (bun, node,
// static, auto...), else the one it depends on.
func svelteKitAdapter(cfg string, deps map[string]bool) string {
	if m := kitAdapterRe.FindStringSubmatch(cfg); m != nil {
		return m[1]
	}
	for _, a := range []string{"bun", "node", "static"} {
		if deps["@sveltejs/adapter-"+a] {
			return a
		}
	}
	return "auto"
}

// kitOutDir is the folder SvelteKit's adapter writes (its out option,
// adapter-static's pages), build unless the config names one.
func kitOutDir(cfg string) string {
	if m := kitOutRe.FindStringSubmatch(cfg); m != nil && safeRel(m[1]) {
		return strings.TrimSuffix(strings.TrimPrefix(m[1], "./"), "/")
	}
	return "build"
}

func svelteKitLaunch(appDir string, deps map[string]bool, start string, onNode bool) *launch {
	cfg := kitConfig(appDir)
	adapter := svelteKitAdapter(cfg, deps)
	out := kitOutDir(cfg)
	l := &launch{ID: "sveltekit", Name: "SvelteKit"}
	ownStart := start != "" && !kitStartRe.MatchString(start)
	switch adapter {
	case "static":
		f := &launchFiles{Dirs: []string{out}, Why: "SvelteKit with adapter-static writes the site to " + out + "/"}
		if m := kitFallback.FindStringSubmatch(cfg); m != nil && safeRel(m[1]) {
			f.SPA, f.Page = true, "/"+strings.TrimPrefix(m[1], "/")
			f.Why += "; paths without a page serve " + m[1]
		}
		l.Files = f
		return l
	case "bun":
		if onNode {
			l.Err = &BuildError{Msg: "SvelteKit's adapter-bun builds a Bun server, but the app runs on Node.js (runtime: \"node\")",
				Hint: "Remove runtime: \"node\" (Bun is the default), or use @sveltejs/adapter-node."}
			return l
		}
		l.Run = merge(kitRun, kitBunRun)
		l.Start, l.Why = "bun ./"+out+"/index.js", "adapter-bun's server"
	case "node", "auto":
		l.Run = merge(kitRun, kitNodeRun)
		rt := "bun"
		if onNode {
			rt = "node"
		}
		l.Start, l.Why = rt+" ./"+out+"/index.js", "adapter-node's server, on "+map[bool]string{true: "Node.js", false: "Bun"}[onNode]
		if adapter == "auto" {
			// adapter-auto knows no box: told it builds for Cloud Run, it
			// installs adapter-node and uses that.
			l.Build = map[string]string{"GCP_BUILDPACKS": "true"}
			l.Why = "adapter-auto picks adapter-node here; on " + map[bool]string{true: "Node.js", false: "Bun"}[onNode]
			if !onNode {
				l.Warn = "SvelteKit uses adapter-auto, which installs adapter-node during each build. @sveltejs/adapter-bun (SvelteKit 3) gives a leaner Bun server: bun add -D @sveltejs/adapter-bun and use it in vite.config."
			}
		}
	default:
		l.Err = &BuildError{Msg: "SvelteKit's adapter-" + adapter + " builds for another host, not a server the box runs",
			Hint: "Use @sveltejs/adapter-bun (SvelteKit 3), @sveltejs/adapter-node, or @sveltejs/adapter-static for a site of files."}
		return l
	}
	if ownStart {
		l.Start, l.Why = "", "the app's start script"
	}
	return l
}

// --- Nuxt ------------------------------------------------------------------

var (
	nuxtConfigs = []string{"nuxt.config.ts", "nuxt.config.js", "nuxt.config.mjs", "nuxt.config.mts"}
	nuxtSSROff  = regexp.MustCompile(`\bssr\s*:\s*false\b`)
	// The start scripts a Nuxt app writes for its node-server build.
	nuxtStartRe = regexp.MustCompile(`^(?:node|bun)(?: --bun)? (?:\./)?\.output/server/index\.mjs$|^nuxt (?:start|preview)$`)
)

func nuxtLaunch(appDir, start, build string, onNode bool) *launch {
	l := &launch{ID: "nuxt", Name: "Nuxt"}
	cfg := readConfig(appDir, nuxtConfigs)
	if strings.Contains(build, "nuxt generate") || strings.Contains(build, "nuxi generate") {
		f := &launchFiles{Dirs: []string{".output/public", "dist"}, Why: "nuxt generate writes the site to .output/public/"}
		if nuxtSSROff.MatchString(cfg) {
			f.SPA, f.Page = true, "/200.html"
			f.Why += " (ssr: false: paths without a page serve 200.html)"
		}
		l.Files = f
		return l
	}
	// Nitro's node-server output runs on Node.js and on Bun. Never its bun
	// preset (Nitro 2): it buffers request bodies and has no graceful
	// shutdown.
	l.Build = map[string]string{"NITRO_PRESET": "node-server"}
	l.Run = nuxtRun
	l.Secret = nuxtSecretEnv
	rt := "bun"
	if onNode {
		rt = "node"
	}
	l.Start, l.Why = rt+" .output/server/index.mjs", "Nitro's node-server output, on "+map[bool]string{true: "Node.js", false: "Bun"}[onNode]
	if start != "" && !nuxtStartRe.MatchString(start) {
		l.Start, l.Why = "", "the app's start script"
	}
	return l
}

// --- React Router ----------------------------------------------------------

var (
	rrConfigs = []string{"react-router.config.ts", "react-router.config.js", "react-router.config.mts", "react-router.config.mjs"}
	rrSSROff  = regexp.MustCompile(`\bssr\s*:\s*false\b`)
	rrBuildRe = regexp.MustCompile("\\bbuildDirectory\\s*:\\s*[\"'`]([^\"'`]+)[\"'`]")
	// react-router-serve <server build>, as the templates' start script has it.
	rrStartRe = regexp.MustCompile(`^(?:bunx? (?:--bun )?|npx )?react-router-serve ((?:\./)?[\w./-]+\.(?:m?js))$`)
)

func reactRouterLaunch(appDir string, deps map[string]bool, start string, onNode bool) *launch {
	l := &launch{ID: "react-router", Name: "React Router"}
	cfg := readConfig(appDir, rrConfigs)
	dir := "build"
	if m := rrBuildRe.FindStringSubmatch(cfg); m != nil && safeRel(m[1]) {
		dir = strings.TrimPrefix(m[1], "./")
	}
	if v, old := rrBeforeFix(appDir); old {
		l.Warn = "React Router " + v + " leaks memory when it streams responses (fixed in 8.4.0 and 7.18.4): upgrade react-router and @react-router/* to 8.4.0 or later (7.18.4 on v7)."
	}
	if rrSSROff.MatchString(cfg) {
		// SPA mode: index.html, or __spa-fallback.html when / is prerendered.
		l.Files = &launchFiles{Dirs: []string{path.Join(dir, "client")}, SPA: true,
			Why: "React Router with ssr: false writes the site to " + path.Join(dir, "client") + "/"}
		return l
	}
	server := "./" + path.Join(dir, "server/index.js")
	if start != "" {
		m := rrStartRe.FindStringSubmatch(start)
		if m == nil {
			l.Why = "the app's start script"
			return l
		}
		server = "./" + strings.TrimPrefix(m[1], "./")
	}
	if onNode {
		if !deps["@react-router/serve"] {
			l.Why = "the app's start script"
			return l
		}
		l.Start, l.Why = "node ./node_modules/@react-router/serve/bin.cjs "+server, "react-router-serve on Node.js"
		return l
	}
	// react-router-serve (Express) manages about 340 requests a second on
	// Bun; Bun.serve with React Router's handler, ~6,700 at 135 MB.
	l.Runner, l.Run = true, rrRun
	l.Start, l.Why = "bun "+rrRunnerPath+" "+server, "the box's Bun server for React Router (Bun.serve and React Router's request handler)"
	return l
}

// --- Astro -----------------------------------------------------------------

var (
	astroConfigs  = []string{"astro.config.mjs", "astro.config.ts", "astro.config.js", "astro.config.mts", "astro.config.cjs", "astro.config.cts"}
	astroOutDirRe = regexp.MustCompile("\\boutDir\\s*:\\s*[\"'`]([^\"'`]+)[\"'`]")
	// The start scripts an Astro app writes for its standalone server, which
	// the box replaces with the same command, exec'd.
	astroStartRe = regexp.MustCompile(`^(?:node|bun)(?: --bun)? (?:\./)?([\w./-]+)/server/entry\.mjs$`)
	// Start scripts that are Astro's own CLI: the dev server or a local
	// preview, never what serves a site in production.
	astroCLIStartRe = regexp.MustCompile(`^(?:(?:bunx?|npx|pnpm|yarn) (?:--bun )?)?astro (?:dev|preview|start)\b`)
)

// astroLaunch: with @astrojs/node (standalone mode, which `astro add node`
// sets up without a start script) the box starts dist/server/entry.mjs
// with HOST=0.0.0.0, on Node.js unless the app sets runtime (readApp: the
// adapter supports Node.js only), and keeps one ASTRO_KEY for its builds
// and instances; the edge serves its prerendered pages and /_astro/ files
// (assets.go). Without a server adapter Astro writes the site to files,
// which the edge serves. Another host's adapter (Vercel, Netlify,
// Cloudflare) builds a server the box cannot run.
func astroLaunch(appDir string, deps map[string]bool, start string, onNode bool) *launch {
	l := &launch{ID: "astro", Name: "Astro"}
	ownStart := start != "" && !astroCLIStartRe.MatchString(start)
	if !deps["@astrojs/node"] {
		for _, a := range astroServer {
			if deps[a] {
				l.Err = &BuildError{Msg: "Astro's " + a + " adapter builds for another host, not a server the box runs",
					Hint: "Use @astrojs/node (npx astro add node, standalone mode) for pages rendered on request, or remove the adapter to build the site to files."}
				return l
			}
		}
		if ownStart {
			// A server of the app's own (Express serving dist/, say).
			l.Why = "the app's start script"
			return l
		}
		out := astroOutDir(appDir, "")
		l.Files = &launchFiles{Dirs: []string{out}, Why: "Astro without a server adapter writes the site to " + out + "/"}
		return l
	}
	l.Run = astroRun
	l.Secret, l.SecretAtBuild = astroKeyEnv, true
	if m := astroStartRe.FindStringSubmatch(start); ownStart && (m == nil || !safeRel(m[1])) {
		l.Why = "the app's start script"
		return l
	}
	out := astroOutDir(appDir, start)
	rt := map[bool]string{true: "node", false: "bun"}[onNode]
	l.Start, l.Why = rt+" ./"+out+"/server/entry.mjs", "@astrojs/node's standalone server, on "+map[bool]string{true: "Node.js", false: "Bun"}[onNode]
	return l
}

// astroOutDir is where an Astro build writes: its config's outDir, else the
// folder its start script runs <outDir>/server/entry.mjs from, else dist.
// The launch starts the server from it and the edge serves <outDir>/client.
func astroOutDir(appDir, start string) string {
	out := "dist"
	if m := astroOutDirRe.FindStringSubmatch(readConfig(appDir, astroConfigs)); m != nil && safeRel(m[1]) {
		out = strings.TrimSuffix(strings.TrimPrefix(m[1], "./"), "/")
	}
	if m := astroStartRe.FindStringSubmatch(start); m != nil && safeRel(m[1]) {
		out = strings.TrimSuffix(strings.TrimPrefix(m[1], "./"), "/")
	}
	return out
}

// astroNodeServer reports whether the app in appDir is an Astro app with
// @astrojs/node, and whether its config (as written) imports it.
func astroNodeServer(appDir string) (deps, inConfig bool) {
	pd := packageDeps(appDir)
	if !pd["astro"] || !pd["@astrojs/node"] {
		return false, false
	}
	return true, strings.Contains(readConfig(appDir, astroConfigs), "@astrojs/node")
}

// rrVersionRe reads major.minor.patch from the start of a version range.
var rrVersionRe = regexp.MustCompile(`^\s*(?:[\^~]|>=?)?\s*v?(\d+)\.(\d+)(?:\.(\d+))?`)

// rrBeforeFix reports React Router's version when package.json pins one
// with the streaming memory leak (@react-router/node before 8.4.0, or
// 7.18.4 on v7).
func rrBeforeFix(dir string) (string, bool) {
	raw, err := readSrc(filepath.Join(dir, "package.json"))
	if err != nil {
		return "", false
	}
	var pj struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if json.Unmarshal(raw, &pj) != nil {
		return "", false
	}
	v := pj.Dependencies["react-router"]
	m := rrVersionRe.FindStringSubmatch(v)
	if m == nil {
		return "", false
	}
	n := func(s string) int { i, _ := strconv.Atoi(s); return i }
	major, minor, patch := n(m[1]), n(m[2]), n(m[3])
	old := (major == 8 && minor < 4) || (major == 7 && (minor < 18 || (minor == 18 && patch < 4))) || major < 7
	return strings.TrimSpace(v), old
}

// writeRRRunner writes the box's React Router server into the build context.
func writeRRRunner(srcDir string) error {
	dir := filepath.Join(srcDir, rrDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "serve.js"), []byte(rrRunnerJS), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"type":"module"}`+"\n"), 0o644); err != nil {
		return err
	}
	return keepInBuild(srcDir, rrDir)
}

// --- shared ----------------------------------------------------------------

// safeRel reports whether p is a plain relative folder (no .., not
// absolute, nothing a shell reads specially).
func safeRel(p string) bool {
	p = strings.TrimPrefix(p, "./")
	return p != "" && !strings.HasPrefix(p, "/") && !strings.Contains(p, "..") && plainWord.MatchString(p)
}

// execPackageBin runs a package's bin with node, wherever the workspace
// keeps the package (the app's node_modules or one above it, as Node
// resolves it), exec'd: node is the container's process, without the
// npx launcher (npm exec), which stays resident at about 85 MB next to the
// app. bin is relative to the package; args start with a space.
func execPackageBin(pkg, bin, args string) string {
	return `exec node "$(node -p "require('path').join(require.resolve('` + pkg + `/package.json'), '../` + bin + `')")"` + args
}

func merge(ms ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// logLine is the build log's line for a launch.
func (l *launch) logLine() string {
	switch {
	case l.Files != nil:
		return fmt.Sprintf("==> %s: %s, which the edge serves (no server)\n", l.Name, l.Files.Why)
	case l.Start != "":
		return fmt.Sprintf("==> %s: start command %s (%s); proxy and shutdown settings for its server are set, the app's env wins\n", l.Name, l.Start, l.Why)
	}
	return fmt.Sprintf("==> %s: started by %s\n", l.Name, l.Why)
}

// prepareLaunch sets a server build up for its framework: the build env
// (into env), the box's React Router server (into the source), warnings on
// the deploy. It returns the env the image gets for the server.
func prepareLaunch(req BuildRequest, l *launch, env map[string]string) (map[string]string, error) {
	fmt.Fprint(req.Log, l.logLine())
	if l.Warn != "" {
		fmt.Fprintf(req.Log, "==> warning: %s\n", l.Warn)
		req.Deploy.Warnings = append(req.Deploy.Warnings, l.Warn)
	}
	for k, v := range l.Build {
		env[k] = v
	}
	if l.Runner {
		if err := writeRRRunner(req.SrcDir); err != nil {
			return nil, fmt.Errorf("add the box's React Router server to the build: %w", err)
		}
	}
	if req.Dir != "" && strings.HasPrefix(l.Start, "node ./node_modules/@react-router/serve/") {
		// A workspace may keep the package at its top: found from the app's folder.
		l.Start = execPackageBin("@react-router/serve", "bin.cjs", " "+strings.Fields(l.Start)[2])
	}
	if len(l.Run) == 0 {
		return nil, nil
	}
	return merge(l.Run), nil
}
