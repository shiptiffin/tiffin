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
		"static-site":   {"static", "site", []string{}},
		"hono-postgres": {"hono", "api", []string{"postgres"}},
		"guestbook":     {"hono", "guestbook", []string{"analytics", "postgres", "valkey"}},
		"next-postgres": {"next", "web", []string{"postgres"}},
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
		doc["project"] = "x"
		raw, _ := json.Marshal(doc)
		got, err := manifest.Parse(raw)
		if err != nil {
			t.Fatalf("%s fragment: %v\n%s", s.ID, err, frag)
		}
		own, _, err := manifest.Load(filepath.Join("files", s.ID, "tiffin.config.ts"), nil)
		if err != nil {
			t.Fatal(err)
		}
		own.Project = "x"
		a, _ := manifest.Canonical(got)
		b, _ := manifest.Canonical(own)
		if string(a) != string(b) {
			t.Errorf("%s: fragment manifest\n%s\n!= starter config\n%s", s.ID, a, b)
		}
	}
	g, ok := Get("guestbook")
	frag, _ := json.Marshal(g.Fragment)
	if !ok || string(frag) != `{"apps":{"guestbook":{"env":{"GREETING":"Welcome to the box"},"framework":"hono","healthcheck":"/api/healthz"}},"services":{"analytics":{},"postgres":{},"valkey":{}}}` {
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
			for _, f := range []string{"package.json", "bun.lock", ".gitignore"} {
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
