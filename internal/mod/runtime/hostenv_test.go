package runtime

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/manifest"
)

// evilEnv is app env whose names root host tools read as their own
// settings: credential helpers, config files, the loader, PATH.
var evilEnv = map[string]string{
	"DOCKER_CONFIG": "/evil/auth",
	"LD_PRELOAD":    "/evil/x.so",
	"NERDCTL_TOML":  "/evil/nerdctl.toml",
	"PATH":          "/evil/bin",
	"API_KEY":       "sk_live_123",
}

// An app's env never reaches the environment of railpack, buildctl or
// nerdctl, which run as root on the host; the build still sees it.
func TestAppEnvNeverReachesHostTools(t *testing.T) {
	for name, spec := range map[string]manifest.App{
		"railpack":   {Framework: manifest.FrameworkBun},
		"dockerfile": {Framework: manifest.FrameworkBun, Builder: manifest.BuilderDockerfile},
		"static":     {Framework: manifest.FrameworkStatic, Build: "mkdir -p out && echo hi > out/index.html", Output: "out"},
	} {
		bin, log := fakeTools(t)
		b := &boxBuilder{eng: newFakeEngine(), binDir: bin, staticDir: t.TempDir(), cgroupDir: t.TempDir(), memoryMB: 1024}
		req := buildReq(t, spec, map[string]string{"Dockerfile": "FROM scratch", "package.json": `{"scripts":{"start":"bun index.ts"}}`, "index.ts": ""})
		if spec.Builder == manifest.BuilderDockerfile {
			req.Dockerfile = "Dockerfile"
		}
		req.Env = map[string]string{"PUBLIC_SITE": "https://shop.example"}
		req.RunEnv = map[string]string{}
		for k, v := range evilEnv {
			if spec.Framework == manifest.FrameworkStatic {
				req.Env[k] = v
			} else {
				req.RunEnv[k] = v
			}
		}
		if _, err := b.Build(context.Background(), req); err != nil {
			t.Fatalf("%s: %v\n%s", name, err, readCalls(t, log))
		}
		calls := readCalls(t, log)
		for _, line := range strings.Split(calls, "\n") {
			// Values may travel under the box's own names (TIFFIN_SECRET_<n>).
			if strings.HasPrefix(line, "procenv: ") && !strings.HasPrefix(line, "procenv: TIFFIN_SECRET_") && (strings.Contains(line, "/evil") || strings.Contains(line, "sk_live")) {
				t.Errorf("%s: a host tool's own environment has the app's %q:\n%s", name, strings.TrimPrefix(line, "procenv: "), calls)
			}
		}
		if spec.Framework != manifest.FrameworkStatic && !strings.Contains(calls, "env: API_KEY=sk_live_123\n") {
			t.Errorf("%s: the build no longer sees the app's env:\n%s", name, calls)
		}
	}
}

// Containers get the app's env as arguments; nerdctl's own environment
// (handed to the OCI hooks runc runs on the host) stays the box's.
func TestContainerEnvNeverReachesNerdctlEnv(t *testing.T) {
	n := &nerdctl{bin: "/bin/true"}
	c := n.withSpec(context.Background(), []string{"run"}, RunSpec{Env: evilEnv})
	for _, e := range c.Env {
		if strings.Contains(e, "/evil") || strings.Contains(e, "sk_live") {
			t.Errorf("nerdctl's environment has the app's %q", e)
		}
	}
	for k, v := range evilEnv {
		if !slices.Contains(c.Args, k+"="+v) {
			t.Errorf("the container lacks %s=%s: %q", k, v, c.Args)
		}
	}
}
