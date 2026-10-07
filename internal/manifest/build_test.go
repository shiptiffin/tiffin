package manifest

import (
	"strings"
	"testing"
)

func TestBuildOverridesNormalizeAndWarn(t *testing.T) {
	m := Normalize(&Manifest{Project: "p", Apps: map[string]App{
		"web":  {Builder: BuilderAuto, Dockerfile: "./Dockerfile", Watch: []string{"apps/web/**"}},
		"site": {Builder: BuilderStatic},
	}})
	if err := Validate(m); err != nil {
		t.Fatal(err)
	}
	if w := m.Apps["web"]; w.Builder != "" || w.Dockerfile != "" {
		// "auto" and the default Dockerfile are stored as absent.
		t.Fatalf("web = %+v", w)
	}
	if s := m.Apps["site"]; s.Framework != FrameworkStatic || s.Builder != "" {
		t.Fatalf("builder static should become framework static: %+v", s)
	}
	ws := Warnings(m)
	if len(ws) != 1 || !strings.Contains(ws[0], "apps.web.watch") {
		t.Fatalf("warnings = %q", ws)
	}
	// Normalize is idempotent.
	before := m.Apps["site"]
	Normalize(m)
	if m.Apps["site"].Framework != before.Framework {
		t.Fatal("not idempotent")
	}
}
