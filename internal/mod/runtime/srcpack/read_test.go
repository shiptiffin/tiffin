package srcpack

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A tree's files are read only when plain and small: never a FIFO (which
// would block), a device, or through a link where links are not allowed.
func TestReadFileRefusesWhatIsNotPlain(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skip(err)
	}
	if err := os.Symlink("/dev/zero", filepath.Join(dir, "zero")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ok.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("ok.json", filepath.Join(dir, "link.json")); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, c := range []struct {
			name  string
			links bool
			ok    bool
		}{
			{"fifo", true, false}, {"zero", true, false}, {"zero", false, false},
			{"ok.json", false, true}, {"link.json", true, true}, {"link.json", false, false},
		} {
			raw, err := ReadFile(filepath.Join(dir, c.name), c.links)
			if (err == nil) != c.ok || (c.ok && string(raw) != "{}") {
				t.Errorf("%s (links %v): %q %v", c.name, c.links, raw, err)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ReadFile blocked")
	}
}

// A workspace file or an ignore file that is a link to an endless device
// is not read: the box would read until it runs out of memory.
func TestCloneLinksToDevicesAreNotRead(t *testing.T) {
	top := t.TempDir()
	if err := os.MkdirAll(filepath.Join(top, "apps", "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(top, "apps", "web", "package.json"), []byte(`{"dependencies":{"x":"workspace:*"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, l := range []string{"pnpm-workspace.yaml", "package.json", ".gitignore", "apps/web/.tiffinignore"} {
		if err := os.Symlink("/dev/urandom", filepath.Join(top, filepath.FromSlash(l))); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, _, ok := WorkspaceRoot(filepath.Join(top, "apps", "web"), top); ok {
			t.Error("a link to /dev/urandom made a workspace")
		}
		var out bytes.Buffer
		if _, err := Pack(filepath.Join(top, "apps", "web"), &out); err != nil && !strings.Contains(err.Error(), "plain") {
			t.Error(err)
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("reading the tree did not end")
	}
}
