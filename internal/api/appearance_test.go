package api_test

import (
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/api"
)

// A project's colour: stable default from its name, then whatever someone picks.
func TestProjectAppearance(t *testing.T) {
	e := newEnv(t)
	e.applyManifest(shop)

	code, got, _ := e.call(e.owner, "GET", "/v1/projects/shop/appearance", nil)
	if code != 200 || got["enamel"] != api.DefaultEnamel("shop") || got["chosen"] != false {
		t.Fatalf("default: %d %v", code, got)
	}
	// Stable and spread out: the dashboard computes the same default (lib/enamel.ts).
	if got := api.DefaultEnamel("shop"); got != "turmeric" {
		t.Fatalf("default for shop: %s", got)
	}

	code, got, _ = e.call(e.owner, "PUT", "/v1/projects/shop/appearance", map[string]any{"enamel": "kokum"})
	if code != 200 || got["enamel"] != "kokum" || got["chosen"] != true {
		t.Fatalf("set: %d %v", code, got)
	}
	if _, got, _ = e.call(e.owner, "GET", "/v1/projects/shop/appearance", nil); got["enamel"] != "kokum" {
		t.Fatalf("after set: %v", got)
	}

	// Not a colour, not a project, not allowed.
	if code, _, _ := e.call(e.owner, "PUT", "/v1/projects/shop/appearance", map[string]any{"enamel": "purple"}); code != 422 {
		t.Fatalf("bad enamel: %d", code)
	}
	if code, _, _ := e.call(e.owner, "GET", "/v1/projects/nope/appearance", nil); code != 404 {
		t.Fatalf("missing project: %d", code)
	}
	reader := e.key("all", "read")
	if code, _, _ := e.call(reader, "PUT", "/v1/projects/shop/appearance", map[string]any{"enamel": "leaf"}); code != 403 {
		t.Fatalf("read-only token set a colour: %d", code)
	}
}

// The dashboard shows the config file an edit would write before planning it.
func TestManifestRender(t *testing.T) {
	e := newEnv(t)
	code, got, _ := e.call(e.owner, "POST", "/v1/manifest/render", map[string]any{"manifest": shop})
	cfg, _ := got["config"].(string)
	if code != 200 || !strings.Contains(cfg, `project: "shop"`) || !strings.Contains(cfg, "defineConfig(") || strings.Contains(cfg, "//") {
		t.Fatalf("render: %d %q", code, cfg)
	}
	if code, _, _ := e.call(e.owner, "POST", "/v1/manifest/render", map[string]any{"manifest": map[string]any{"apps": map[string]any{}}}); code != 422 {
		t.Fatalf("invalid manifest: %d", code)
	}
}
