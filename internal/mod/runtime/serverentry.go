package runtime

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
