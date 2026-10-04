//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// budget is the target for up -> checks -> down. It is only logged, never
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

	// ---- the core flow on the box: state on the XFS disk, plan, apply, serve ----
	p = time.Now()
	if err := box.CopyIn(filepath.Join(RepoRoot(), "examples", "hello", "tiffin.config.ts"), "/tmp/tiffin.config.ts"); err != nil {
		t.Fatal(err)
	}
	const env = "sudo TIFFIN_HOME=/var/lib/tiffin/platform"
	planOut := sh(env + " /tmp/tiffin plan /tmp/tiffin.config.ts")
	hash := jsonField(t, planOut, "hash")
	if len(hash) != 64 {
		t.Fatalf("plan hash %q from:\n%s", hash, planOut)
	}
	if out, _, err := box.Exec("sh", "-c", env+" /tmp/tiffin apply /tmp/tiffin.config.ts"); err == nil || !strings.Contains(out, "confirm_required") {
		t.Errorf("apply without confirm must exit non-zero with confirm_required, got err=%v\n%s", err, out)
	}
	if got := jsonField(t, sh(env+" /tmp/tiffin apply /tmp/tiffin.config.ts --confirm "+hash[:12]+" -m e2e"), "applied"); got != "true" {
		t.Errorf("apply on box: applied=%s", got)
	}
	if got := sh("sudo findmnt -no FSTYPE -T /var/lib/tiffin/platform/state.db"); got != "xfs" {
		t.Errorf("state.db not on the XFS disk: %q", got)
	}
	if got := sh("sudo stat -c %a /var/lib/tiffin/platform/owner-token"); got != "600" {
		t.Errorf("owner-token mode %s, want 600", got)
	}
	if got := jsonField(t, sh(env+" /tmp/tiffin doctor"), "ok"); got != "true" {
		t.Errorf("doctor on box not ok")
	}
	health := sh(`set -e
` + env + ` sh -c '/tmp/tiffin serve --addr 127.0.0.1:7070 >/tmp/serve.log 2>&1 &'
for i in $(seq 1 50); do curl -fsS http://127.0.0.1:7070/v1/health && exit 0; sleep 0.1; done
cat /tmp/serve.log; exit 1`)
	if jsonField(t, health, "status") != "ok" {
		t.Errorf("served health: %s", health)
	}
	tok := sh("sudo cat /var/lib/tiffin/platform/owner-token")
	projects := sh(`curl -fsS -H "Authorization: Bearer ` + tok + `" http://127.0.0.1:7070/v1/projects`)
	if !strings.Contains(projects, `"name":"hello"`) {
		t.Errorf("served projects: %s", projects)
	}
	sh("sudo pkill -x tiffin || true")
	phase("m0-flow", p)

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

// jsonField extracts a top-level field from a JSON document as a string.
func jsonField(t *testing.T, doc, field string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, doc)
	}
	return fmt.Sprint(m[field])
}
