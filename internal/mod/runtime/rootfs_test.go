package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRootfsManifest(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"app/server.js": "listen()", "app/node_modules/a/index.js": "a", "var/log/apt/history.log": "x", "var/cache/ldconfig/aux-cache": "x"})
	if err := os.Symlink("a", filepath.Join(root, "app/node_modules/b")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "app/server.js"), 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := rootfsManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(m, "\n")
	if strings.Contains(got, "var/log/apt") || strings.Contains(got, "aux-cache") {
		t.Errorf("package manager logs are in the manifest:\n%s", got)
	}
	for _, want := range []string{
		"app/server.js\tf\t755\t",
		"\t8\t" + sha256Hex("listen()") + "\t",
		"app/node_modules/b\tl\t",
		"\ta\t",
		"app/node_modules\td\t",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("manifest lacks %q:\n%s", want, got)
		}
	}

	// Times are not part of it.
	old := time.Unix(1e9, 0)
	if err := os.Chtimes(filepath.Join(root, "app/server.js"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(root, "app"), old, old); err != nil {
		t.Fatal(err)
	}
	again, _ := rootfsManifest(root)
	if n, d := manifestDiff(m, again); n != 0 {
		t.Errorf("times changed the manifest: %v", d)
	}
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestManifestDiff(t *testing.T) {
	a := []string{"x\tf\t644\t0:0\t1\tabc\t", "y\td\t755\t0:0\t0\t\t", "z\tl\t777\t0:0\t0\tx\t"}
	b := []string{"x\tf\t644\t0:0\t1\tabd\t", "y\td\t700\t0:0\t0\t\t", "w\tf\t644\t0:0\t0\te3b0\t"}
	n, d := manifestDiff(a, b)
	want := []string{"w: only in today's image", "x: content differ", "y: mode differ", "z: only in the new image"}
	if n != 4 || strings.Join(d, "|") != strings.Join(want, "|") {
		t.Errorf("diff: %d %q", n, d)
	}
}

func TestGateImage(t *testing.T) {
	e := newFakeEngine()
	same, other, legacy := t.TempDir(), t.TempDir(), t.TempDir()
	for _, dir := range []string{same, legacy} {
		writeFiles(t, dir, map[string]string{"app/index.js": "1", "app/node_modules/x/index.js": "x"})
	}
	writeFiles(t, other, map[string]string{"app/index.js": "1", "app/node_modules/x/index.js": "changed by next build"})
	e.trees = map[string]string{dockerName("new:1"): same, dockerName("new:2"): other, dockerName("old:1"): legacy}
	ctx := context.Background()
	if ok, why, err := gateImage(ctx, e, "new:1", "old:1"); err != nil || !ok {
		t.Errorf("same files: %v %q %v", ok, why, err)
	}
	ok, why, err := gateImage(ctx, e, "new:2", "old:1")
	if err != nil || ok || why != "1 paths differ: app/node_modules/x/index.js: size, content differ" {
		t.Errorf("different files: %v %q %v", ok, why, err)
	}
	if _, _, err := gateImage(ctx, e, "missing", "old:1"); err == nil {
		t.Error("a missing image passed")
	}
}
