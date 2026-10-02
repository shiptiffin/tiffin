//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// budget is the M1 target for up -> checks -> down. It is only logged, never
// enforced: the first boot also downloads a ~600 MB cloud image.
const budget = 5 * time.Minute

func TestSmoke(t *testing.T) {
	RequireLima(t)
	start := time.Now()
	phase := func(name string, since time.Time) {
		t.Logf("PHASE %-14s %s", name, time.Since(since).Round(100*time.Millisecond))
	}

	// ---- up ----
	p := time.Now()
	box := Up(t, "smoke")
	phase("up", p)

	sh := func(script string) string {
		t.Helper()
		out, errOut, err := box.Exec("sh", "-c", script)
		if err != nil {
			t.Fatalf("exec %q: %v\nstdout: %s\nstderr: %s", script, err, out, errOut)
		}
		return strings.TrimSpace(out)
	}

	// ---- checks ----
	p = time.Now()

	osr := sh("cat /etc/os-release")
	if !strings.Contains(osr, `VERSION_ID="24.04"`) || !strings.Contains(osr, "ID=ubuntu") {
		t.Errorf("expected Ubuntu 24.04, got:\n%s", osr)
	}

	wantArch := map[string]string{"arm64": "aarch64", "amd64": "x86_64"}[HostArch()]
	if got := sh("uname -m"); got != wantArch {
		t.Errorf("guest arch = %q, want %q (host %s)", got, wantArch, HostArch())
	}

	if got := sh("findmnt -no FSTYPE /var/lib/tiffin"); got != "xfs" {
		t.Errorf("/var/lib/tiffin fstype = %q, want xfs", got)
	}

	// reflink: a clone of a file on the XFS disk must succeed and keep content.
	out := sh(`set -e
d=/var/lib/tiffin/.e2e-reflink
sudo mkdir -p "$d"
sudo sh -c "echo tiffin > $d/a"
sudo cp --reflink=always "$d/a" "$d/b"
sudo cat "$d/b"
sudo rm -rf "$d"`)
	if out != "tiffin" {
		t.Errorf("reflink copy content = %q, want %q", out, "tiffin")
	}
	phase("checks", p)

	// ---- build + run tiffin in the guest ----
	p = time.Now()
	bin := filepath.Join(t.TempDir(), "tiffin")
	build := exec.Command("go", "build", "-trimpath",
		"-ldflags", "-X github.com/btahir/tiffin/internal/version.Version=0.0.0-e2e",
		"-o", bin, "./cmd/tiffin")
	build.Dir = RepoRoot()
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+HostArch())
	if o, err := build.CombinedOutput(); err != nil {
		t.Fatalf("cross-build tiffin: %v\n%s", err, o)
	}
	phase("cross-build", p)

	p = time.Now()
	if err := box.CopyIn(bin, "/tmp/tiffin"); err != nil {
		t.Fatal(err)
	}
	sh("chmod +x /tmp/tiffin")
	if got := sh("/tmp/tiffin version"); !strings.Contains(got, "0.0.0-e2e") || !strings.Contains(strings.ToLower(got), "tiffin") {
		t.Errorf("`tiffin version` = %q, want it to mention tiffin and 0.0.0-e2e", got)
	}
	phase("copy+version", p)

	// ---- down ----
	p = time.Now()
	box.Down()
	phase("down", p)
	box.AssertGone()

	total := time.Since(start)
	t.Logf("TOTAL up->checks->down: %s (budget %s)", total.Round(time.Second), budget)
	if total > budget {
		t.Logf("WARNING: over the %s budget (first boot downloads the image; re-run to see warm timing)", budget)
	}
}
