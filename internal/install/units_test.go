package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
