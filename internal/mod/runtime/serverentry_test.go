package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerEntry(t *testing.T) {
	for _, c := range []struct {
		pkg    string
		onNode bool
		want   string
	}{
		{`{"dependencies":{"astro":"7","@astrojs/node":"11"},"scripts":{"build":"astro build"}}`, false, "bun ./dist/server/entry.mjs"},
		{`{"dependencies":{"astro":"7","@astrojs/node":"11"},"scripts":{"build":"astro build"}}`, true, "node ./dist/server/entry.mjs"},
		// A start script of its own wins.
		{`{"dependencies":{"astro":"7","@astrojs/node":"11"},"scripts":{"start":"node ./dist/server/entry.mjs"}}`, false, ""},
		// A static Astro site has no server.
		{`{"dependencies":{"astro":"7"},"scripts":{"build":"astro build"}}`, false, ""},
		{`{"dependencies":{"@tanstack/react-start":"1"},"scripts":{"build":"vite build"}}`, false, ""},
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(c.pkg), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := serverEntry(dir, c.onNode); got != c.want {
			t.Errorf("%s (node %v): %q, want %q", c.pkg, c.onNode, got, c.want)
		}
	}
}

func TestBunEntry(t *testing.T) {
	for _, c := range []struct {
		pkg   string
		files []string
		want  string
	}{
		// Hono's Bun starter: no start script, src/index.ts exports the app.
		{`{"scripts":{"dev":"bun run --hot src/index.ts"},"dependencies":{"hono":"4"}}`, []string{"src/index.ts"}, "bun ./src/index.ts"},
		{`{"main":"app/server.ts"}`, []string{"app/server.ts", "index.ts"}, "bun ./app/server.ts"},
		// main names a file that is not there: the usual entries.
		{`{"main":"dist/index.js"}`, []string{"index.ts"}, "bun ./index.ts"},
		// A start script, or a build (Railpack starts the output), wins.
		{`{"scripts":{"start":"bun run server.ts"}}`, []string{"index.ts"}, ""},
		{`{"scripts":{"build":"vite build"}}`, []string{"src/index.ts"}, ""},
		{`{"main":"../../etc/x; rm -rf /"}`, nil, ""},
		{`{}`, nil, ""},
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(c.pkg), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, f := range c.files {
			_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755)
			if err := os.WriteFile(filepath.Join(dir, f), []byte("export default {}"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got := bunEntry(dir); got != c.want {
			t.Errorf("%s %v: %q, want %q", c.pkg, c.files, got, c.want)
		}
	}
}

func TestCheckStartCommand(t *testing.T) {
	dir := t.TempDir()
	plan := filepath.Join(dir, "railpack-plan.json")
	write := func(s string) {
		if err := os.WriteFile(plan, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"deploy":{"startCommand":"exec bun --bun run start"}}`)
	if err := checkStartCommand(plan, BuildRequest{}, ""); err != nil {
		t.Fatal(err)
	}
	write(`{"deploy":{"variables":{}}}`)
	if err := checkStartCommand(plan, BuildRequest{}, ""); err == nil || !strings.Contains(err.Error(), "no start command") {
		t.Fatalf("no start command: %v", err)
	}
	if err := checkStartCommand(plan, BuildRequest{}, "bun ./src/index.ts"); err != nil {
		t.Fatalf("a start command given to Railpack: %v", err)
	}
	if err := checkStartCommand(plan, BuildRequest{Export: true}, ""); err != nil {
		t.Fatalf("a static export never runs: %v", err)
	}
}
