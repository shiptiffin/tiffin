package storage

import (
	"os"
	"path/filepath"
	"testing"
)

// A box provisioned by an older build (or edited by hand) has the storage
// tree with modes the service user cannot traverse; provisioning again
// must repair it.
func TestFixLayoutRepairsExistingTree(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{dataDir(root), iamDir(root), trashDir(root)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(iamDir(root), "users.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The failure seen on a real box: parent 0700, children unreachable.
	for _, d := range []string{storageDir(root), dataDir(root)} {
		if err := os.Chmod(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	me, grp := os.Getuid(), os.Getgid()
	for i := 0; i < 2; i++ { // idempotent
		if err := fixLayout(root, me, grp, me, grp); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range layout(root) {
		fi, err := os.Stat(d.path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != d.mode {
			t.Errorf("%s: mode %v, want %v", d.path, fi.Mode().Perm(), d.mode)
		}
	}
	// The service user must be able to reach its dirs: group bits on the parent.
	if fi, _ := os.Stat(storageDir(root)); fi.Mode().Perm()&0o050 != 0o050 {
		t.Fatalf("storage dir not traversable by the service group: %v", fi.Mode())
	}
}
