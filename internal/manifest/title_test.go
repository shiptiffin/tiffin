package manifest

import (
	"strings"
	"testing"
)

func TestTitle(t *testing.T) {
	for _, ok := range []string{"", "Shop storefront", "  padded  ", "Café · 東京 ✨", strings.Repeat("é", MaxTitle)} {
		if why := TitleProblem(ok); why != "" {
			t.Errorf("%q refused: %s", ok, why)
		}
	}
	for _, bad := range []string{strings.Repeat("x", MaxTitle+1), "a\nb", "a\tb", "a\x00b", "a\u202Eb", "a\u2066b", "a\u2028b", "a\x7fb", "\xff"} {
		if TitleProblem(bad) == "" {
			t.Errorf("%q accepted", bad)
		}
	}
}

// The title is trimmed when normalized, checked by Validate, and written
// into the config right after the project.
func TestTitleInManifest(t *testing.T) {
	m := Normalize(&Manifest{Project: "website", Title: "  ShipTiffin website "})
	if m.Title != "ShipTiffin website" {
		t.Fatalf("normalized title %q", m.Title)
	}
	if err := Validate(m); err != nil {
		t.Fatal(err)
	}
	cfg := string(RenderConfig(m, ""))
	if !strings.Contains(cfg, `project: "website", title: "ShipTiffin website"`) {
		t.Fatalf("config:\n%s", cfg)
	}
	err := Validate(Normalize(&Manifest{Project: "website", Title: "two\nlines"}))
	if err == nil || !strings.Contains(err.Error(), "/title:") {
		t.Fatalf("bad title: %v", err)
	}
}
