package runtime

import "regexp"

// TanStack Start (React and Solid). Its Vite build has no server of its own;
// the Nitro plugin (nitro/vite, Nitro 3; nitropack, Nitro 2) writes one to
// .output/server/index.mjs, with the client files in .output/public, and
// runs on Node.js and on Bun. Without Nitro the build writes dist/server and
// dist/client, which the app starts itself (its own start script).

const tanstackNitroEntry = ".output/server/index.mjs"

// Instance env for Nitro's server. Nitro 3 serves with srvx, which drains
// on SIGTERM for SERVER_SHUTDOWN_TIMEOUT seconds (5 by default); Nitro 2
// reads NITRO_SHUTDOWN_TIMEOUT in milliseconds (30 s by default). Both
// are set below the 30 s the box waits. HOST and PORT need nothing: both
// read PORT, and listen on every interface without HOST.
var tanstackRun = map[string]string{
	"SERVER_SHUTDOWN_TIMEOUT": "25",
	"NITRO_SHUTDOWN_TIMEOUT":  "25000",
}

// tanstackStartRe is the start script a TanStack Start app on Nitro writes
// for its own build, which the box replaces with the same command, exec'd.
var tanstackStartRe = regexp.MustCompile(`^(?:node|bun)(?: --bun)? (?:\./)?\.output/server/index\.mjs$`)

func tanstackLaunch(deps map[string]bool, start string, onNode bool) *launch {
	l := &launch{ID: "tanstack-start", Name: "TanStack Start"}
	nitro := deps["nitro"] || deps["nitropack"]
	switch {
	case start != "" && !tanstackStartRe.MatchString(start):
		// A server of its own (the Bun reference server, srvx, a custom entry).
		l.Why = "the app's start script"
		if nitro {
			l.Run = tanstackRun
		}
	case nitro:
		l.Run = tanstackRun
		rt := "bun"
		if onNode {
			rt = "node"
		}
		l.Start, l.Why = rt+" "+tanstackNitroEntry, "Nitro's server output, on "+map[bool]string{true: "Node.js", false: "Bun"}[onNode]
	default:
		l.Why = "the build's own start"
		l.Warn = "TanStack Start without Nitro builds no server of its own, and the app has no start script, so the box may find nothing to start. Add Nitro (bun add nitro, then nitro() after tanstackStart() in vite.config): vite build then writes a server to .output/server/index.mjs, which the box starts. Or give the app a start script for a server of its own."
	}
	return l
}
