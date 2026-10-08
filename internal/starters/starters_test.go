package starters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/btahir/tiffin/internal/manifest"
)

func TestList(t *testing.T) {
	all, err := List()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		framework, app string
		services       []string
	}{
		"nextjs":         {"next", "web", []string{}},
		"tanstack-start": {"bun", "web", []string{}},
		"sveltekit":      {"bun", "web", []string{}},
		"react-router":   {"bun", "web", []string{}},
		"nuxt":           {"bun", "web", []string{}},
		"astro":          {"static", "site", []string{}},
		"vite-react":     {"static", "site", []string{}},
		"hono":           {"hono", "api", []string{}},
		"fastapi":        {"fastapi", "api", []string{}},
		"static-site":    {"static", "site", []string{}},
		"guestbook":      {"hono", "guestbook", []string{}},
	}
	if len(all) != len(want) {
		t.Fatalf("%d starters", len(all))
	}
	for _, s := range all {
		w, ok := want[s.ID]
		if !ok {
			t.Fatalf("unexpected starter %s", s.ID)
		}
		if s.Framework != w.framework || s.App != w.app || !slices.Equal(s.Services, w.services) || s.Name == "" || s.Description == "" || s.Files < 2 {
			t.Errorf("%s: %+v", s.ID, s)
		}
		// The fragment merged into a bare project is a valid manifest that
		// plans exactly the starter's own config.
		frag, _ := json.Marshal(s.Fragment)
		var doc map[string]any
		_ = json.Unmarshal(frag, &doc)
		own, _, err := manifest.Load(filepath.Join("files", s.ID, "tiffin.config.ts"), nil)
		if err != nil {
			t.Fatal(err)
		}
		doc["project"] = own.Project // addresses are named after the project
		raw, _ := json.Marshal(doc)
		got, err := manifest.Parse(raw)
		if err != nil {
			t.Fatalf("%s fragment: %v\n%s", s.ID, err, frag)
		}
		a, _ := manifest.Canonical(got)
		b, _ := manifest.Canonical(own)
		if string(a) != string(b) {
			t.Errorf("%s: fragment manifest\n%s\n!= starter config\n%s", s.ID, a, b)
		}
	}
	g, ok := Get("guestbook")
	frag, _ := json.Marshal(g.Fragment)
	if !ok || string(frag) != `{"apps":{"guestbook":{"env":{"GREETING":"Welcome to the box"},"framework":"hono","healthcheck":"/api/healthz"}}}` {
		t.Fatalf("guestbook fragment: %s", frag)
	}
	if _, ok := Get("nope"); ok {
		t.Fatal("unknown starter found")
	}
}

func TestWriteTo(t *testing.T) {
	for _, id := range IDs() {
		dir := t.TempDir()
		if err := WriteTo(id, dir); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, "tiffin.config.ts")); err != nil {
			t.Errorf("%s: %v", id, err)
		}
		if id != "static-site" {
			// Built starters ship their lockfile, so a box installs exactly what was tested.
			files := []string{"package.json", "bun.lock", ".gitignore"}
			if id == "fastapi" {
				files = []string{"pyproject.toml", "uv.lock", ".python-version", ".gitignore"}
			}
			for _, f := range files {
				if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
					t.Errorf("%s: %v", id, err)
				}
			}
		}
	}
	if err := WriteTo("nope", t.TempDir()); err == nil {
		t.Fatal("unknown starter written")
	}
}

// Each kind offers its frameworks with exactly one default, every listed
// starter names a preset with a display name, and the file to edit exists.
func TestKindsAndPresets(t *testing.T) {
	all, err := List()
	if err != nil {
		t.Fatal(err)
	}
	defaults := map[string][]string{}
	listed := map[string][]string{}
	for _, s := range all {
		if s.Preset == "" || s.PresetName == "" || s.Name != s.PresetName || s.Edit == "" {
			t.Errorf("%s: %+v", s.ID, s)
		}
		if !slices.Contains([]string{"web", "static", "api"}, s.Kind) {
			t.Errorf("%s: kind %q", s.ID, s.Kind)
		}
		if _, err := os.Stat(filepath.Join("files", s.ID, filepath.FromSlash(s.Edit))); err != nil {
			t.Errorf("%s: edit file: %v", s.ID, err)
		}
		// A static site runs no container; everything else does.
		if (s.Kind == "static") != (s.Framework == "static") {
			t.Errorf("%s: kind %s with framework %s", s.ID, s.Kind, s.Framework)
		}
		if !s.Listed {
			if s.Default {
				t.Errorf("%s: an unlisted starter can't be a default", s.ID)
			}
			continue
		}
		listed[s.Kind] = append(listed[s.Kind], s.Preset)
		if s.Default {
			defaults[s.Kind] = append(defaults[s.Kind], s.Preset)
		}
	}
	wantListed := map[string][]string{
		"web":    {"nextjs", "tanstack-start", "sveltekit", "react-router", "nuxt"},
		"static": {"astro", "vite-react"},
		"api":    {"hono", "fastapi"},
	}
	wantDefault := map[string][]string{"web": {"nextjs"}, "static": {"astro"}, "api": {"hono"}}
	for k, w := range wantListed {
		if !slices.Equal(listed[k], w) {
			t.Errorf("%s lists %v, want %v (the default first)", k, listed[k], w)
		}
		if !slices.Equal(defaults[k], wantDefault[k]) {
			t.Errorf("%s defaults %v, want %v", k, defaults[k], wantDefault[k])
		}
	}
}

// No build output or installed packages ride along in the binary.
func TestNoBuildOutput(t *testing.T) {
	for _, id := range IDs() {
		for _, d := range []string{"node_modules", "dist", "build", ".output", ".next", ".nuxt", ".astro", ".tanstack", ".nitro", ".svelte-kit", ".react-router"} {
			if _, err := os.Stat(filepath.Join("files", id, d)); err == nil {
				t.Errorf("%s ships %s", id, d)
			}
		}
	}
}
