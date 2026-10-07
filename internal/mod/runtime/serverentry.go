package runtime

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// serverEntry is the start command for an app whose build writes its own
// server file but whose package.json has no start script: Astro with the
// @astrojs/node adapter (standalone mode), which `astro add node` sets up
// without one. "" when none applies. Railpack starts TanStack Start, Nuxt
// and SvelteKit builds itself, and sets HOST=0.0.0.0 for Astro's server.
func serverEntry(appDir string, onNode bool) string {
	if packageScript(appDir, "start") != "" || !packageDeps(appDir)["@astrojs/node"] || !packageDeps(appDir)["astro"] {
		return ""
	}
	if onNode {
		return "node ./dist/server/entry.mjs"
	}
	return "bun ./dist/server/entry.mjs"
}

// bunEntries are the files a Bun server starts from when its package.json
// names none: Bun serves a module's default export ({ fetch }), as Hono's
// and Elysia's Bun starters do (`bun run --hot src/index.ts` is only dev).
var bunEntries = []string{"index.ts", "index.tsx", "index.js", "src/index.ts", "src/index.tsx", "src/index.js", "server.ts", "server.js", "src/server.ts", "src/server.js"}

// bunEntry is the start command for a Bun app with neither a start nor a
// build script: its package.json main, else the first entry file found.
// Apps with a build script (TanStack Start, Nuxt, SvelteKit...) are left to
// Railpack, which starts their output itself.
func bunEntry(appDir string) string {
	if packageScript(appDir, "start") != "" || packageScript(appDir, "build") != "" {
		return ""
	}
	raw, err := readSrc(filepath.Join(appDir, "package.json"))
	if err != nil {
		return ""
	}
	var pkg struct {
		Main   string `json:"main"`
		Module string `json:"module"`
	}
	_ = json.Unmarshal(raw, &pkg)
	for _, f := range append([]string{pkg.Main, pkg.Module}, bunEntries...) {
		f = strings.TrimPrefix(path.Clean("/"+f), "/")
		if f == "" || f == "." || strings.ContainsAny(f, " \"'$`\\;&|<>") {
			continue
		}
		if fi, err := os.Stat(filepath.Join(appDir, f)); err == nil && fi.Mode().IsRegular() {
			return "bun ./" + f
		}
	}
	return ""
}
