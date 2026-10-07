package install

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/mod/runtime"
)

// An update that added units (the edge's own, on the first update to that
// layout) is undone on rollback: the old main unit is back, the added units
// are stopped, disabled and gone, and that happens before the previous
// build starts again.
func TestRestoreUnits(t *testing.T) {
	dir := t.TempDir()
	backup, units := filepath.Join(dir, "backup"), filepath.Join(dir, "units")
	_ = os.MkdirAll(backup, 0o700)
	_ = os.MkdirAll(units, 0o755)
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(backup, "tiffin.service"), "old: serve --edge")
	write(filepath.Join(units, "tiffin.service"), "new: serve --edge-external")
	write(filepath.Join(units, "tiffin-edge.service"), "edge")
	write(filepath.Join(units, "tiffin-edge.socket"), "socket")
	var calls []string
	sysctl := func(_ context.Context, args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		return nil
	}
	if err := restoreUnits(context.Background(), sysctl, backup, units); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(units, "tiffin.service")); string(b) != "old: serve --edge" {
		t.Fatalf("main unit: %q", b)
	}
	for _, u := range []string{"tiffin-edge.service", "tiffin-edge.socket"} {
		if fileExists(filepath.Join(units, u)) {
			t.Errorf("%s is still there", u)
		}
	}
	want := "stop tiffin|disable tiffin-edge.service|disable tiffin-edge.socket|daemon-reload"
	if got := strings.Join(calls, "|"); got != want {
		t.Fatalf("systemctl calls:\n got %s\nwant %s", got, want)
	}
	// A first install saved nothing: nothing to restore.
	calls = nil
	if err := restoreUnits(context.Background(), sysctl, filepath.Join(dir, "none"), units); err != nil || calls != nil {
		t.Fatalf("no backup: %v %v", err, calls)
	}
}

func TestRollbackRestoresUnitsBeforeThePreviousBuildStarts(t *testing.T) {
	u, _, dir := setup(t)
	ctx := context.Background()
	if err := u.Update(ctx, build(t, dir, "v1", "build one")); err != nil {
		t.Fatal(err)
	}
	var order []string
	u.RestoreUnits = func(context.Context) error { order = append(order, "units"); return nil }
	u.StopRemoved = func(context.Context) { order = append(order, "stop removed") }
	restart := u.Restart
	u.Restart = func(ctx context.Context) error {
		order = append(order, "restart "+current(t, u))
		return restart(ctx)
	}
	if err := u.Update(ctx, build(t, dir, "bad", "BROKEN two")); !errors.Is(err, ErrRolledBack) {
		t.Fatalf("want a rollback, got %v", err)
	}
	if got := strings.Join(order, ", "); got != "restart BROKEN two, units, restart build one, stop removed" {
		t.Fatalf("order: %s", got)
	}
}

// A build that keeps crashing is rolled back without waiting out Timeout.
func TestCrashLoopRollsBackEarly(t *testing.T) {
	u, _, dir := setup(t)
	ctx := context.Background()
	if err := u.Update(ctx, build(t, dir, "v1", "build one")); err != nil {
		t.Fatal(err)
	}
	u.Timeout = time.Minute
	n := 0
	u.Restarts = func(context.Context) int {
		if strings.HasPrefix(current(t, u), "BROKEN") {
			n++
		}
		return n
	}
	began := time.Now()
	err := u.Update(ctx, build(t, dir, "bad", "BROKEN two"))
	if !errors.Is(err, ErrRolledBack) {
		t.Fatalf("got %v after %s", err, time.Since(began))
	}
	if time.Since(began) > 10*time.Second {
		t.Fatalf("waited %s for a crash-looping build", time.Since(began))
	}
}

// ssh down stops what serves the box's sites and runs its apps, not only
// the control plane, and says when something kept running.
func TestStopScript(t *testing.T) {
	if appNamespace != runtime.Namespace {
		t.Fatalf("namespace %q is not the runtime's %q", appNamespace, runtime.Namespace)
	}
	run := func(stoppable bool) (string, string, error) {
		tmp := t.TempDir()
		log := filepath.Join(tmp, "log")
		stopped := filepath.Join(tmp, "stopped")
		nerd := filepath.Join(tmp, "nerdctl")
		stop := ""
		if stoppable {
			stop = "touch " + stopped
		}
		fake := "#!/bin/bash\necho \"nerdctl $*\" >> " + log + "\ncase \"$3\" in\n  ps) [ -e " + stopped + " ] || echo c1; ;;\n  stop) " + stop + " ;;\nesac\n"
		if err := os.WriteFile(nerd, []byte(fake), 0o755); err != nil {
			t.Fatal(err)
		}
		shims := "sudo() { if [ \"$1\" = rm ]; then return 0; fi; \"$@\"; }\nsystemctl() { echo \"systemctl $*\" >> " + log + "; [ \"$1\" != is-active ] || return 3; }\n"
		var errb strings.Builder
		cmd := exec.Command("bash", "-c", shims+stopScript(nerd))
		cmd.Stderr = &errb
		err := cmd.Run()
		raw, _ := os.ReadFile(log)
		return string(raw), errb.String(), err
	}
	log, _, err := run(true)
	if err != nil {
		t.Fatalf("stop failed: %v\n%s", err, log)
	}
	for _, want := range []string{"systemctl disable --now tiffin.service tiffin-edge.service tiffin-edge.socket", "nerdctl --namespace tiffin stop c1"} {
		if !strings.Contains(log, want) {
			t.Errorf("stop did not run %q:\n%s", want, log)
		}
	}
	if _, stderr, err := run(false); err == nil || !strings.Contains(stderr, "app-containers") {
		t.Fatalf("a container that kept running must fail the stop: %v %s", err, stderr)
	}
}
