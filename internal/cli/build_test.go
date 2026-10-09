package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The linux build `tiffin up` makes from the source tree says what it is,
// as `make build` does: /v1/health must not report "dev".
func TestUpBuildIsStamped(t *testing.T) {
	args := buildArgs("/tmp/out", stamp{version: "v1.4.0-2-gabc1234", commit: "abc1234", date: "2026-10-06T08:00:00Z"})
	got := strings.Join(args, " ")
	for _, want := range []string{
		"build -trimpath -ldflags",
		"-X github.com/shiptiffin/tiffin/internal/version.Version=v1.4.0-2-gabc1234",
		"-X github.com/shiptiffin/tiffin/internal/version.Commit=abc1234",
		"-X github.com/shiptiffin/tiffin/internal/version.Date=2026-10-06T08:00:00Z",
		"-o /tmp/out ./cmd/tiffin",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("build args lack %q:\n%s", want, got)
		}
	}
	// -ldflags is one argument.
	if args[2] != "-ldflags" || !strings.HasPrefix(args[3], "-s -w -X ") {
		t.Fatalf("args: %q", args)
	}

	// From a git checkout: the describe output, the short commit, its date.
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_DATE=2026-10-01T12:00:00Z", "GIT_COMMITTER_DATE=2026-10-01T12:00:00Z",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	_ = os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644)
	git("add", "f")
	git("commit", "-q", "-m", "one")
	git("tag", "v1.4.0")
	s := gitStamp(context.Background(), dir)
	if s.version != "v1.4.0" || !regexp.MustCompile(`^[0-9a-f]{7,}$`).MatchString(s.commit) || s.date != "2026-10-01T12:00:00Z" {
		t.Fatalf("stamp: %+v", s)
	}
	_ = os.WriteFile(filepath.Join(dir, "f"), []byte("y"), 0o644)
	if s := gitStamp(context.Background(), dir); s.version != "v1.4.0-dirty" {
		t.Fatalf("dirty tree: %+v", s)
	}
	if s := gitStamp(context.Background(), t.TempDir()); s.version != "dev" || s.commit != "none" {
		t.Fatalf("outside git: %+v", s)
	}
}
