package runtime

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/manifest"
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

func TestSmokeNext(t *testing.T) {
	notFound := http.StatusNotFound
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			return
		}
		w.WriteHeader(notFound)
	}))
	defer srv.Close()
	port, _ := strconv.Atoi(srv.URL[strings.LastIndexByte(srv.URL, ':')+1:])
	in := Instance{Name: "web-1", Port: port}
	spec := &manifest.App{Framework: manifest.FrameworkNext}
	if err := smokeNext(context.Background(), in, spec, filepath.Join(t.TempDir(), "log")); err != nil {
		t.Fatalf("a healthy app failed the smoke test: %v", err)
	}
	notFound = http.StatusInternalServerError
	err := smokeNext(context.Background(), in, spec, filepath.Join(t.TempDir(), "log"))
	var he *healthError
	if !errors.As(err, &he) || !strings.Contains(he.msg, "doesn't exist") || !strings.Contains(he.hint, "Node.js") {
		t.Fatalf("a broken not-found page should fail with the Node.js hint, got %v", err)
	}
	spec.Runtime = manifest.RuntimeNode
	if err := smokeNext(context.Background(), in, spec, filepath.Join(t.TempDir(), "log")); err == nil || strings.Contains(err.(*healthError).hint, "switch the app to Node.js") {
		t.Fatalf("an app already on Node gets no Node.js hint, got %v", err)
	}
}
