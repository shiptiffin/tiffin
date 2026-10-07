package runtime

import (
	"os"
	"path/filepath"
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
