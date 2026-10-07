package runtime

import (
	"bytes"
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// One blob at many paths downloads small but checks out big: the commit's
// tree is measured before anything is written.
func TestCloneRefusesACheckoutOverTheLimit(t *testing.T) {
	big := strings.Repeat("a", 300<<10) // compresses to almost nothing
	files := map[string]string{}
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		files[n+".txt"] = big
	}
	repo, _, ca := gitRepoServer(t, files)
	gitAllowPrivate, gitExtraConfig, cloneLimit = true, []string{"http.sslCAInfo=" + ca}, 1<<20
	t.Cleanup(func() { gitAllowPrivate, gitExtraConfig, cloneLimit = false, nil, maxCloneSize })
	loop := func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	gs, err := checkGitSource(context.Background(), repo, "", "", loop)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "clone")
	_, err = fetchGit(context.Background(), gs, dir, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "would be larger") {
		t.Fatalf("clone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); err == nil {
		t.Error("the checkout was written before it was refused")
	}
}

// git's progress lines are capped however long a server makes one.
func TestGitProgressLinesAreCapped(t *testing.T) {
	var out bytes.Buffer
	lw := &lineWriter{w: &out}
	_, _ = lw.Write(bytes.Repeat([]byte("x"), 1<<20))
	if len(lw.buf) > maxProgressLine {
		t.Fatalf("held %d bytes of one line", len(lw.buf))
	}
	_, _ = lw.Write([]byte("\n"))
	if out.Len() > maxProgressLine+1 {
		t.Errorf("wrote %d bytes", out.Len())
	}
}
