package runtime

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/btahir/tiffin/internal/manifest"
)

// Apps that use Vercel's Workflow DevKit (the `workflow` package) run
// unchanged. In production the box runs them on the DevKit's Postgres world
// (@workflow/world-postgres) on the project's database: it installs the world
// into .tiffin/workflow in the build (unless the app depends on it), points
// WORKFLOW_TARGET_WORLD at it in the image, and adds an instrumentation file
// that, when the server starts, brings the world's tables up to date and
// starts its queue worker. Every instance of every running release works the
// one queue in Postgres, so durable sleeps and retries outlive deploys and
// restarts. Previews keep the DevKit's local world: their runs stay inside
// the instance, apart from production's.
const (
	workflowDir      = ".tiffin/workflow"
	workflowWorldEnv = "WORKFLOW_TARGET_WORLD"
	workflowWorldPkg = "@workflow/world-postgres"
	// workflowAppInstr is what an app's own instrumentation file is renamed
	// to; the box's instrumentation imports it.
	workflowAppInstr = "instrumentation-app"
)

//go:embed workflowsetup.mjs
var workflowSetupJS string

//go:embed workflowinstr.js
var workflowInstrJS string

// workflowDeps reads package.json: the version range of `workflow` ("" when
// the app does not use the DevKit), and whether the app depends on the
// Postgres world itself.
func workflowDeps(dir string) (version string, ownWorld bool) {
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return "", false
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	_ = json.Unmarshal(raw, &pkg)
	dep := func(name string) string {
		if v := pkg.Dependencies[name]; v != "" {
			return v
		}
		return pkg.DevDependencies[name]
	}
	return dep("workflow"), dep(workflowWorldPkg) != ""
}

var majorVersion = regexp.MustCompile(`^[\^~=v<>\s]*(\d+)\.`)

// prepareWorkflow wires a production Next.js build that uses the Workflow
// DevKit to the project's Postgres. It returns the env the image needs and
// the command that installs the world in the build ("" when the app brings
// its own).
func prepareWorkflow(req BuildRequest) (map[string]string, string, error) {
	version, own := workflowDeps(req.appDir())
	switch {
	case version == "":
		return nil, "", nil
	case req.Env[workflowWorldEnv] != "":
		fmt.Fprintf(req.Log, "==> Workflow DevKit: the app sets %s, so the box leaves its world alone\n", workflowWorldEnv)
		return nil, "", nil
	case req.Deploy.Preview != "":
		fmt.Fprintf(req.Log, "==> Workflow DevKit: previews run workflows in the DevKit's local world, inside the instance and apart from production's runs\n")
		return nil, "", nil
	case req.Spec.Framework != manifest.FrameworkNext:
		fmt.Fprintf(req.Log, "==> Workflow DevKit: the box sets up its Postgres world for Next.js apps only; set %s and start the world yourself (workflow-sdk.dev/worlds/postgres)\n", workflowWorldEnv)
		return nil, "", nil
	case !req.Postgres:
		return nil, "", &BuildError{Msg: "this app uses the Workflow DevKit, which runs on the project's Postgres, and the project has none",
			Hint: "Add `postgres: {}` to services in tiffin.config.ts, plan and apply, then deploy again. (Or set " + workflowWorldEnv + " to a world of your own.)"}
	}
	dir := filepath.Join(req.SrcDir, workflowDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "", err
	}
	world, install, which := workflowWorldPkg, "", "the app's "+workflowWorldPkg
	if !own {
		m := majorVersion.FindStringSubmatch(version)
		pin := ""
		if m != nil {
			pin = WorkflowPostgresWorld[m[1]]
		}
		if pin == "" {
			return nil, "", &BuildError{Msg: fmt.Sprintf("the box has no Postgres world for workflow %q", version),
				Hint: "Pin workflow to a 4.x or 5.x version, or add " + workflowWorldPkg + " (the release that matches your workflow version) to the app's dependencies."}
		}
		pkg := fmt.Sprintf(`{"private":true,"dependencies":{%q:%q}}`+"\n", workflowWorldPkg, pin)
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0o644); err != nil {
			return nil, "", err
		}
		world = "/app/" + workflowDir + "/node_modules/" + workflowWorldPkg
		install = "sh -c 'cd " + workflowDir + " && bun install --production --no-progress'"
		which = workflowWorldPkg + " " + pin
	}
	if err := os.WriteFile(filepath.Join(dir, "setup.mjs"), []byte(workflowSetupJS), 0o644); err != nil {
		return nil, "", err
	}
	if err := writeWorkflowInstrumentation(req.appDir(), world); err != nil {
		return nil, "", err
	}
	if err := keepInBuild(req.SrcDir, workflowDir); err != nil {
		return nil, "", err
	}
	fmt.Fprintf(req.Log, "==> Workflow DevKit: runs on the project's Postgres (%s), started with the server; previews use the local world\n", which)
	return map[string]string{workflowWorldEnv: world}, install, nil
}

// writeWorkflowInstrumentation adds the instrumentation file that starts the
// world. An app's own instrumentation file keeps working: it is renamed and
// the box's file calls it.
func writeWorkflowInstrumentation(srcDir, world string) error {
	dir := srcDir
	if !exists(filepath.Join(srcDir, "app")) && !exists(filepath.Join(srcDir, "pages")) &&
		(exists(filepath.Join(srcDir, "src", "app")) || exists(filepath.Join(srcDir, "src", "pages"))) {
		dir = filepath.Join(srcDir, "src")
	}
	app := "const app = {};"
	for _, ext := range []string{".ts", ".tsx", ".js", ".jsx", ".mjs"} {
		p := filepath.Join(dir, "instrumentation"+ext)
		if !exists(p) {
			continue
		}
		if err := os.Rename(p, filepath.Join(dir, workflowAppInstr+ext)); err != nil {
			return err
		}
		// Copied out of the namespace: Turbopack rejects reading an export
		// the module does not have.
		app = `import * as appModule from "./` + workflowAppInstr + ext + `"; const app = { ...appModule };`
		break
	}
	q, _ := json.Marshal(world)
	js := strings.Replace(workflowInstrJS, "/*APP*/ const app = {};", app, 1)
	js = strings.Replace(js, `/*WORLD*/ ""`, string(q), 1)
	return os.WriteFile(filepath.Join(dir, "instrumentation.js"), []byte(js), 0o644)
}

// addBuildCommand runs cmd first in the build step of a Railpack plan.
func addBuildCommand(planPath, cmd string) error {
	raw, err := os.ReadFile(planPath)
	if err != nil {
		return err
	}
	var plan map[string]any
	if err := json.Unmarshal(raw, &plan); err != nil {
		return err
	}
	steps, _ := plan["steps"].([]any)
	for _, s := range steps {
		step, _ := s.(map[string]any)
		if step["name"] != "build" {
			continue
		}
		cmds, _ := step["commands"].([]any)
		step["commands"] = append([]any{map[string]any{"cmd": cmd}}, cmds...)
		out, err := json.MarshalIndent(plan, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(planPath, out, 0o644)
	}
	return fmt.Errorf("the build plan has no build step")
}

// workflowQueueRoute matches the Workflow DevKit's queue routes: they run
// workflow and step code for whoever calls them. On the box only the world
// calls them, over loopback, so the switchboard turns public requests away.
// (Webhook routes stay public.)
var workflowQueueRoute = regexp.MustCompile(`/\.well-known/workflow/v\d+/(flow|step)(/|$)`)
