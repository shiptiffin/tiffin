package runtime

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestGuessFramework(t *testing.T) {
	for _, c := range []struct {
		pkg, framework, unsupported string
		html                        bool
	}{
		{`{"dependencies":{"next":"16"}}`, "next", "", false},
		{`{"devDependencies":{"@sveltejs/kit":"2","vite":"7"},"scripts":{"build":"vite build"}}`, "bun", "SvelteKit", false},
		{`{"devDependencies":{"@react-router/dev":"7","vite":"7"},"scripts":{"build":"react-router build","start":"react-router-serve ./build/server/index.js"}}`, "bun", "React Router (framework mode)", false},
		{`{"dependencies":{"nuxt":"4"},"scripts":{"build":"nuxt build"}}`, "bun", "Nuxt", false},
		{`{"dependencies":{"astro":"5","@astrojs/node":"9"},"scripts":{"build":"astro build"}}`, "bun", "Astro with server rendering", false},
		{`{"dependencies":{"astro":"5"},"scripts":{"build":"astro build"}}`, "static", "", false},
		{`{"dependencies":{"react":"19","react-router":"7"},"devDependencies":{"vite":"7"},"scripts":{"build":"vite build"}}`, "static", "", true},
		{`{"dependencies":{"react-scripts":"5"},"scripts":{"build":"react-scripts build","start":"react-scripts start"}}`, "static", "", true},
		{`{"dependencies":{"hono":"4"}}`, "hono", "", false},
		{`{"dependencies":{"express":"5"},"scripts":{"start":"node server.js"}}`, "bun", "", false},
	} {
		g := guessFramework([]byte(c.pkg), c.html)
		if g.Framework != c.framework || g.Unsupported != c.unsupported {
			t.Errorf("%s: got %s/%q (%s), want %s/%q", c.pkg, g.Framework, g.Unsupported, g.Why, c.framework, c.unsupported)
		}
	}
}

func TestSPAFallback(t *testing.T) {
	dir := t.TempDir()
	req := BuildRequest{SrcDir: dir, Log: io.Discard}
	write := func(name, s string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("package.json", `{"dependencies":{"react-router-dom":"7"}}`)
	if !spaFallback(req, readStaticfile(dir)) {
		t.Error("a client-routed site should fall back to index.html")
	}
	write("Staticfile", "index_fallback: false\n")
	if spaFallback(req, readStaticfile(dir)) {
		t.Error("index_fallback: false must win over the router")
	}
	write("Staticfile", "root: dist\n")
	write("package.json", `{"dependencies":{"react":"19"}}`)
	if spaFallback(req, readStaticfile(dir)) {
		t.Error("no router, no Staticfile: no fallback")
	}
}
