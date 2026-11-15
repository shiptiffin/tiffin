package runtime

import "github.com/shiptiffin/tiffin/internal/manifest"

// bindHost is what a JavaScript server app listens on unless its own env
// says otherwise. PORT is set per instance (deployer.go); HOST is the
// other variable servers read for where to bind (srvx, Nitro, SvelteKit,
// Astro, react-router-serve, Vite's preview, Elysia and the like), and
// a server that defaults to localhost listens on ::1 on some hosts, which
// the box's 127.0.0.1 checks never reach. Apps run on the host's network
// and the firewall keeps their ports local, so 0.0.0.0 shows no one more
// than 127.0.0.1 does. It goes in the image's env, so the app's own env
// (which the container gets on top) wins, and no existing app restarts.
const bindHost = "0.0.0.0"

// withBindHost adds HOST to the image env of a Railpack-built JavaScript
// web app (not Next.js, which reads HOSTNAME and not HOST; not Python;
// not a worker, which serves nothing).
func withBindHost(spec manifest.App, imageEnv map[string]string) map[string]string {
	if spec.Role == manifest.RoleWorker || spec.Framework == manifest.FrameworkNext || spec.Framework.IsPython() {
		return imageEnv
	}
	if imageEnv == nil {
		imageEnv = map[string]string{}
	}
	if imageEnv["HOST"] == "" {
		imageEnv["HOST"] = bindHost
	}
	return imageEnv
}
