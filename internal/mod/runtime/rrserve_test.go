package runtime

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// The box's React Router server caches the build's hashed assets for good
// and revalidates the app's own public/assets/ files, which keep their
// names across releases.
func TestRRServerCacheHeaders(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun not found")
	}
	app := t.TempDir()
	writeFiles(t, app, map[string]string{
		"serve.js":                          rrRunnerJS,
		"package.json":                      `{"type":"module"}`,
		"build/server/index.js":             `export default { fetch: () => new Response("app") };`,
		"build/client/assets/a-B1x9Qa2c.js": "js",
		"build/client/assets/logo.svg":      "<svg/>",
		"public/assets/logo.svg":            "<svg/>",
	})
	port := freePort(t)
	cmd := exec.Command(bun, filepath.Join(app, "serve.js"), "build/server/index.js")
	cmd.Dir = app
	cmd.Env = append(os.Environ(), "PORT="+strconv.Itoa(port), "HOST=127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	get := func(p string) *http.Response {
		for range 100 {
			if res, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, p)); err == nil {
				res.Body.Close()
				return res
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("no answer for %s", p)
		return nil
	}
	for p, want := range map[string]string{
		"/assets/a-B1x9Qa2c.js": "public, max-age=31536000, immutable",
		"/assets/logo.svg":      "public, max-age=0, must-revalidate",
	} {
		if got := get(p).Header.Get("Cache-Control"); got != want {
			t.Errorf("%s: Cache-Control %q, want %q", p, got, want)
		}
	}
}
