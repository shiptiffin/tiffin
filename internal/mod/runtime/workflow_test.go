package runtime

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/manifest"
)

func TestPrepareWorkflow(t *testing.T) {
	app := func(t *testing.T, pkg string, files ...string) string {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0o644)
		for _, f := range files {
			os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755)
			os.WriteFile(filepath.Join(dir, f), []byte("export function register() {}\n"), 0o644)
		}
		return dir
	}
	req := func(dir string) BuildRequest {
		return BuildRequest{Deploy: &Deploy{ID: "dep_1"}, Spec: manifest.App{Framework: manifest.FrameworkNext},
			SrcDir: dir, Env: map[string]string{}, Postgres: true, Log: io.Discard}
	}
	const uses = `{"dependencies":{"next":"16.3.8","workflow":"^4.2.0"}}`

	// Production: the pinned world is installed, the image points at it, the
	// instrumentation starts it and keeps the app's own (src layout) running.
	dir := app(t, uses, "src/app/page.js", "src/instrumentation.ts")
	env, install, err := prepareWorkflow(req(dir))
	world := "/app/.tiffin/workflow/node_modules/@workflow/world-postgres"
	if err != nil || env[workflowWorldEnv] != world || !strings.Contains(install, "bun install") {
		t.Fatalf("env %v, install %q, err %v", env, install, err)
	}
	pkg, _ := os.ReadFile(filepath.Join(dir, workflowDir, "package.json"))
	instr, _ := os.ReadFile(filepath.Join(dir, "src", "instrumentation.js"))
	if !strings.Contains(string(pkg), `"@workflow/world-postgres":"4.3.9"`) || !exists(filepath.Join(dir, workflowDir, "setup.mjs")) ||
		!exists(filepath.Join(dir, "src", "instrumentation-app.ts")) || exists(filepath.Join(dir, "src", "instrumentation.ts")) ||
		!strings.Contains(string(instr), `import * as appModule from "./instrumentation-app.ts"; const app = { ...appModule };`) || !strings.Contains(string(instr), `const world = "`+world+`";`) {
		t.Fatalf("package.json %s\ninstrumentation.js:\n%s", pkg, instr)
	}

	// The app's own Postgres world is used as it is.
	dir = app(t, `{"dependencies":{"workflow":"5.0.1","@workflow/world-postgres":"5.0.1"}}`, "app/page.js")
	env, install, err = prepareWorkflow(req(dir))
	instr, _ = os.ReadFile(filepath.Join(dir, "instrumentation.js"))
	if err != nil || env[workflowWorldEnv] != workflowWorldPkg || install != "" || exists(filepath.Join(dir, workflowDir, "package.json")) ||
		!strings.Contains(string(instr), "const app = {};") {
		t.Fatalf("env %v, install %q, err %v\n%s", env, install, err, instr)
	}

	// Left alone: no DevKit, the app's own world, previews, other frameworks.
	for name, r := range map[string]BuildRequest{
		"no devkit": req(app(t, `{"dependencies":{"next":"16.3.8"}}`)),
		"own world": func() BuildRequest { r := req(app(t, uses)); r.Env[workflowWorldEnv] = "./world.js"; return r }(),
		"preview":   func() BuildRequest { r := req(app(t, uses)); r.Deploy.Preview = "feat"; return r }(),
		"hono":      func() BuildRequest { r := req(app(t, uses)); r.Spec.Framework = manifest.FrameworkBun; return r }(),
	} {
		env, install, err := prepareWorkflow(r)
		if env != nil || install != "" || err != nil || exists(filepath.Join(r.SrcDir, workflowDir)) {
			t.Errorf("%s: env %v, install %q, err %v", name, env, install, err)
		}
	}

	// Errors that say what to do: no Postgres, a version without a world.
	r := req(app(t, uses))
	r.Postgres = false
	var be *BuildError
	if _, _, err := prepareWorkflow(r); !errors.As(err, &be) || !strings.Contains(be.Hint, "postgres: {}") {
		t.Errorf("no postgres: %v", err)
	}
	if _, _, err := prepareWorkflow(req(app(t, `{"dependencies":{"workflow":"latest"}}`))); !errors.As(err, &be) {
		t.Errorf("unknown version: %v", err)
	}
}

func TestWorkflowInstrumentationCallsTheApp(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"type":"module"}`), 0o644)
	os.WriteFile(filepath.Join(dir, "instrumentation.js"), []byte(`export async function register() { globalThis.ran = "app"; }
export function onRequestError(e) { globalThis.reported = e; }
`), 0o644)
	if err := writeWorkflowInstrumentation(dir, "/app/world"); err != nil {
		t.Fatal(err)
	}
	// Another world is set, so only the app's own register runs.
	cmd := exec.Command(node, "--input-type=module", "-e", `const m = await import(process.argv[1]);
await m.register(); m.onRequestError("boom");
console.log(globalThis.ran, globalThis.reported);`, filepath.Join(dir, "instrumentation.js"))
	cmd.Env = append(os.Environ(), "NEXT_RUNTIME=nodejs", "WORKFLOW_TARGET_WORLD=local")
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "app boom" {
		t.Fatalf("%v: %s", err, out)
	}
}

func TestAddBuildCommand(t *testing.T) {
	p := filepath.Join(t.TempDir(), "plan.json")
	os.WriteFile(p, []byte(`{"steps":[{"name":"install","commands":[{"cmd":"npm ci"}]},{"name":"build","commands":[{"cmd":"npm run build"}]}]}`), 0o644)
	if err := addBuildCommand(p, "sh -c 'echo hi'"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	s := string(raw)
	if strings.Index(s, "echo hi") > strings.Index(s, "npm run build") || strings.Index(s, "echo hi") < strings.Index(s, "npm ci") {
		t.Fatalf("the command must run first in the build step:\n%s", s)
	}
	os.WriteFile(p, []byte(`{"steps":[]}`), 0o644)
	if addBuildCommand(p, "x") == nil {
		t.Fatal("a plan without a build step must be an error")
	}
}

func TestWorkflowQueueRoute(t *testing.T) {
	for path, want := range map[string]bool{
		"/.well-known/workflow/v1/flow":           true,
		"/.well-known/workflow/v1/step":           true,
		"/docs/.well-known/workflow/v1/step":      true, // basePath
		"/.well-known/workflow/v1/webhook/abc":    false,
		"/.well-known/workflow/v1/flowers":        false,
		"/.well-known/security.txt":               false,
		"/api/.well-known/workflow/v2/flow/extra": true,
	} {
		if got := workflowQueueRoute.MatchString(path); got != want {
			t.Errorf("%s: %v, want %v", path, got, want)
		}
	}
}
