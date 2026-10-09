package api_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shiptiffin/tiffin/internal/manifest"
)

const demoConfig = `import { defineConfig } from "@shiptiffin/sdk";

export default defineConfig({
  project: "demo",
  apps: {
    site: { framework: "static", path: "site", routes: ["demo"] },
    api: { framework: "hono", path: "api", routes: ["demo/api"], healthcheck: "/api/healthz" },
  },
  services: { valkey: {}, analytics: { retentionDays: 30 } },
  env: { GREETING: "Welcome to the box" },
});
`

// loadTS evaluates a config the way the CLI does and returns the raw
// manifest JSON it sends to the API.
func loadTS(t *testing.T, src string) json.RawMessage {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tiffin.config.ts")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, err := manifest.EvaluateJSON(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (e *env) applyManifest(m any) {
	e.t.Helper()
	_, plan, _ := e.call(e.owner, "POST", "/v1/plan", map[string]any{"manifest": m})
	if code, out, _ := e.call(e.owner, "POST", "/v1/apply", map[string]any{"manifest": m, "confirm": plan["hash"]}); code != 200 {
		e.t.Fatalf("apply: %d %v", code, out)
	}
}

// The dashboard flow: GET the manifest, edit it, plan. The plan must be
// identical to making the same edit in tiffin.config.ts (tiffin pull's
// output) and running tiffin plan.
func TestProjectManifestRoundTripAndEditParity(t *testing.T) {
	e := newEnv(t)
	e.applyManifest(loadTS(t, demoConfig))

	code, got, _ := e.call(e.owner, "GET", "/v1/projects/demo/manifest", nil)
	if code != 200 || got["version"].(float64) != 1 || got["project"] != "demo" {
		t.Fatalf("manifest: %d %v", code, got)
	}
	cfg, _ := got["config"].(string)
	for _, want := range []string{`project: "demo"`, `site: { framework: "static", path: "site", routes: ["demo"] }`, `analytics: { retentionDays: 30 }`, "tiffin apply --confirm"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config lacks %q:\n%s", want, cfg)
		}
	}
	// Unchanged, it plans to nothing.
	_, plan, _ := e.call(e.owner, "POST", "/v1/plan", map[string]any{"manifest": got["manifest"]})
	if ops, _ := plan["ops"].([]any); len(ops) != 0 {
		t.Fatalf("unchanged manifest plans ops: %v", plan)
	}
	// The pulled config, evaluated, plans to nothing too.
	_, plan, _ = e.call(e.owner, "POST", "/v1/plan", map[string]any{"manifest": loadTS(t, cfg)})
	if ops, _ := plan["ops"].([]any); len(ops) != 0 {
		t.Fatalf("pulled config plans ops: %v", plan)
	}

	// Dashboard edit: give Postgres an extension, add a worker app, change env.
	m := got["manifest"].(map[string]any)
	m["services"].(map[string]any)["postgres"] = map[string]any{"extensions": []string{"vector"}}
	m["apps"].(map[string]any)["jobs"] = map[string]any{"role": "worker"}
	m["env"].(map[string]any)["GREETING"] = "hi"
	_, dash, _ := e.call(e.owner, "POST", "/v1/plan", map[string]any{"manifest": m})

	// The same edit in the pulled tiffin.config.ts.
	edited := strings.Replace(cfg, "services: {", "services: { postgres: { extensions: [\"vector\"] },", 1)
	edited = strings.Replace(edited, "apps: {\n", "apps: {\n    jobs: { role: \"worker\" },\n", 1)
	edited = strings.Replace(edited, `"Welcome to the box"`, `"hi"`, 1)
	_, cli, _ := e.call(e.owner, "POST", "/v1/plan", map[string]any{"manifest": loadTS(t, edited)})

	if dash["hash"] == nil || dash["hash"] != cli["hash"] {
		t.Fatalf("dashboard plan %v\n!= CLI plan %v\nedited config:\n%s", dash, cli, edited)
	}
	dj, _ := json.Marshal(dash)
	cj, _ := json.Marshal(cli)
	if string(dj) != string(cj) {
		t.Fatalf("plans differ:\n%s\n%s", dj, cj)
	}
	if ops := dash["ops"].([]any); len(ops) != 3 {
		t.Fatalf("want update service/postgres, create app/jobs, update env/GREETING: %s", dj)
	}

	// Applying the dashboard edit moves the version; the manifest follows.
	if code, out, _ := e.call(e.owner, "POST", "/v1/apply", map[string]any{"manifest": m, "confirm": dash["hash"]}); code != 200 {
		t.Fatalf("apply edit: %d %v", code, out)
	}
	_, got, _ = e.call(e.owner, "GET", "/v1/projects/demo/manifest", nil)
	mm := got["manifest"].(map[string]any)
	if got["version"].(float64) != 2 || mm["services"].(map[string]any)["postgres"] == nil || mm["apps"].(map[string]any)["jobs"] == nil {
		t.Fatalf("after edit: %v", got)
	}
	if !strings.Contains(got["config"].(string), `jobs: { role: "worker" }`) {
		t.Fatalf("config after edit:\n%s", got["config"])
	}
}

func TestProjectManifestErrors(t *testing.T) {
	e := newEnv(t)
	if code, prob, _ := e.call(e.owner, "GET", "/v1/projects/nope/manifest", nil); code != 404 || prob["code"] != "not_found" {
		t.Fatalf("missing project: %d %v", code, prob)
	}
	e.applyManifest(map[string]any{"project": "shop"})
	e.applyManifest(map[string]any{"project": "blog"})
	tok := e.key([]string{"blog"}, "read")
	if code, _, _ := e.call(tok, "GET", "/v1/projects/shop/manifest", nil); code != 403 {
		t.Fatalf("other project: %d", code)
	}
	if code, out, _ := e.call(tok, "GET", "/v1/projects/blog/manifest", nil); code != 200 || out["config"] == "" {
		t.Fatalf("own project: %d %v", code, out)
	}
}

// An app that sets no routes keeps the address it already has on the box:
// one applied before addresses were named after the project stays at its
// app name (with a warning), and a new app gets the new default.
func TestDefaultAddressesKeepOldOnes(t *testing.T) {
	e := newEnv(t)
	routes := func(project, app string) []any {
		t.Helper()
		_, got, _ := e.call(e.owner, "GET", "/v1/projects/"+project+"/manifest", nil)
		return got["manifest"].(map[string]any)["apps"].(map[string]any)[app].(map[string]any)["routes"].([]any)
	}
	// What a box from before stored: the app's own name.
	e.applyManifest(map[string]any{"project": "shop", "apps": map[string]any{"web": map[string]any{"routes": []string{"web"}}}})
	plain := map[string]any{"project": "shop", "apps": map[string]any{"web": map[string]any{}}}
	code, plan, _ := e.call(e.owner, "POST", "/v1/plan", map[string]any{"manifest": plain})
	warn, _ := json.Marshal(plan["warnings"])
	if code != 200 || len(plan["ops"].([]any)) != 0 || !strings.Contains(string(warn), `keeps its old address \"web\"`) || !strings.Contains(string(warn), `routes: [\"shop\"]`) {
		t.Fatalf("old address: %d %v", code, plan)
	}
	plain["apps"].(map[string]any)["docs"] = map[string]any{}
	e.applyManifest(plain)
	if r := routes("shop", "web"); len(r) != 1 || r[0] != "web" {
		t.Fatalf("web moved: %v", r)
	}
	if r := routes("shop", "docs"); len(r) != 1 || r[0] != "shop-docs" {
		t.Fatalf("docs: %v", r)
	}
	// A new project gets the new defaults and no warning.
	blog := map[string]any{"project": "blog", "apps": map[string]any{"web": map[string]any{}}}
	if code, plan, _ = e.call(e.owner, "POST", "/v1/plan", map[string]any{"manifest": blog}); code != 200 || plan["warnings"] != nil {
		t.Fatalf("new project: %d %v", code, plan)
	}
	e.applyManifest(blog)
	if r := routes("blog", "web"); len(r) != 1 || r[0] != "blog" {
		t.Fatalf("blog: %v", r)
	}
}
