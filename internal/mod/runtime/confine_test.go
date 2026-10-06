package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfinedDir(t *testing.T) {
	src := t.TempDir()
	dist := filepath.Join(src, "dist")
	must(t, os.MkdirAll(filepath.Join(dist, "v2"), 0o755))
	must(t, os.WriteFile(filepath.Join(dist, "v2", "index.html"), nil, 0o644))
	must(t, os.Symlink("v2", filepath.Join(dist, "latest")))
	if _, err := confinedDir(src, dist); err != nil {
		t.Fatalf("a link inside the site: %v", err)
	}
	secret := filepath.Join(t.TempDir(), "secrets.key")
	must(t, os.WriteFile(secret, []byte("x"), 0o600))
	must(t, os.Symlink(secret, filepath.Join(dist, "leak")))
	if _, err := confinedDir(src, dist); err == nil {
		t.Fatal("a link to a host file was accepted")
	}
	must(t, os.Remove(filepath.Join(dist, "leak")))
	must(t, os.Symlink("../../..", filepath.Join(dist, "v2", "up")))
	if _, err := confinedDir(src, dist); err == nil {
		t.Fatal("a relative link out of the site was accepted")
	}
	// The output folder itself leading out of the source.
	other := t.TempDir()
	must(t, os.Symlink(other, filepath.Join(src, "out")))
	if _, err := confinedDir(src, filepath.Join(src, "out")); err == nil {
		t.Fatal("an output folder linked out of the source was accepted")
	}
}

func TestPlainTree(t *testing.T) {
	dir := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(dir, "0", "a"), 0o755))
	must(t, os.WriteFile(filepath.Join(dir, "0", "a", "f.js"), nil, 0o644))
	if err := plainTree(dir); err != nil {
		t.Fatal(err)
	}
	must(t, os.Symlink("/etc", filepath.Join(dir, "1")))
	if err := plainTree(dir); err == nil {
		t.Fatal("a planted link was accepted")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
