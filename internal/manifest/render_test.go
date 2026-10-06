package manifest

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every valid config in the repo renders to a tiffin.config.ts that
// evaluates back to the same canonical manifest.
func TestRenderConfigRoundTrip(t *testing.T) {
	var cfgs []string
	for _, pat := range []string{
		"testdata/*/tiffin.config.ts",
		"../../examples/*/tiffin.config.ts",
		"../../templates/*/tiffin.config.ts",
		"../starters/files/*/tiffin.config.ts",
	} {
		m, _ := filepath.Glob(pat)
		for _, p := range m {
			if !strings.HasPrefix(filepath.Base(filepath.Dir(p)), "err-") {
				cfgs = append(cfgs, p)
			}
		}
	}
	if len(cfgs) < 15 {
		t.Fatalf("only %d configs", len(cfgs))
	}
	for _, cfg := range cfgs {
		t.Run(filepath.Base(filepath.Dir(cfg)), func(t *testing.T) {
			var env map[string]string
			if b, err := os.ReadFile(filepath.Join(filepath.Dir(cfg), "env.json")); err == nil {
				_ = json.Unmarshal(b, &env)
			}
			m, want, err := Load(cfg, env)
			if err != nil {
				t.Fatal(err)
			}
			src := RenderConfig(m, "pulled for a test")
			out := filepath.Join(t.TempDir(), "tiffin.config.ts")
			if err := os.WriteFile(out, src, 0o644); err != nil {
				t.Fatal(err)
			}
			_, got, err := Load(out, nil)
			if err != nil {
				t.Fatalf("rendered config does not evaluate: %v\n%s", err, src)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("round trip differs\n--- config ---\n%s\n--- got ---\n%s\n--- want ---\n%s", src, got, want)
			}
		})
	}
}

// The rendered config reads like one a person wrote: defaults left out,
// one line per app and service.
func TestRenderConfigReadable(t *testing.T) {
	m, err := Parse([]byte(`{
		"project": "demo",
		"apps": {
			"site": {"framework": "static", "path": "site", "routes": ["demo"]},
			"api": {"framework": "hono", "path": "api", "routes": ["demo/api"], "healthcheck": "/api/healthz"},
			"jobs": {"role": "worker", "memoryMB": 256, "env": {"MODE": "fast"}}
		},
		"services": {"postgres": {}, "valkey": {}, "analytics": {}, "auth": {"methods": ["passkey"], "organizations": false}, "storage": {"buckets": {"user-files": {"public": true}, "private": {}}}},
		"env": {"GREETING": "Welcome to the box", "QUOTE": "x\"y"},
		"queues": {"emails": {"app": "jobs", "rateLimit": 10}},
		"crons": {"nightly": {"schedule": "@daily", "app": "jobs"}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `import { defineConfig } from "@shiptiffin/sdk";

// Pulled from the box.
export default defineConfig({
  project: "demo",
  apps: {
    api: { framework: "hono", path: "api", routes: ["demo/api"], healthcheck: "/api/healthz" },
    jobs: { role: "worker", memoryMB: 256, env: { MODE: "fast" } },
    site: { framework: "static", path: "site", routes: ["demo"] },
  },
  services: {
    postgres: {},
    valkey: {},
    storage: { buckets: { private: {}, "user-files": { public: true } } },
    auth: { methods: ["passkey"], organizations: false },
    analytics: {},
  },
  env: { GREETING: "Welcome to the box", QUOTE: "x\"y" },
  queues: {
    emails: { app: "jobs", rateLimit: 10 },
  },
  crons: {
    nightly: { schedule: "@daily", app: "jobs" },
  },
});
`
	if got := string(RenderConfig(m, "Pulled from the box.")); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}
