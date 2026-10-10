//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestUpDown is the box lifecycle acceptance test, driven entirely through the CLI the
// way a person or agent would use it: `tiffin up` → HTTPS → doctor →
// self-update (good, then broken with automatic rollback) → `tiffin down`.
// It runs a second, throwaway box beside your own (its own instance,
// disk, host port and config dir), so it never touches ~/.tiffin.
func TestUpDown(t *testing.T) {
	RequireLima(t)
	start := time.Now()
	phase := func(name string, since time.Time) {
		t.Logf("PHASE %-14s %s", name, time.Since(since).Round(100*time.Millisecond))
	}
	dir := t.TempDir()
	cli := buildTiffin(t, dir, "", "")              // host CLI
	good := buildTiffin(t, dir, "linux", "0.0.1-a") // what `up` installs
	next := buildTiffin(t, dir, "linux", "0.0.2-b") // the update
	broken := filepath.Join(dir, "broken")
	// A build that installs fine (provision succeeds) but cannot serve: the
	// installed build switches to it, sees it unhealthy and rolls back.
	if err := os.WriteFile(broken, []byte("#!/bin/sh\n[ \"$1\" = provision ] && exit 0\necho broken >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	instance, disk, port := newName(), newDiskName(), freePort(t)
	env := append(os.Environ(),
		"TIFFIN_CONFIG_DIR="+filepath.Join(dir, "config"),
		"TIFFIN_LIMA_INSTANCE="+instance, "TIFFIN_LIMA_DISK="+disk, fmt.Sprintf("TIFFIN_LIMA_PORT=%d", port),
		"TIFFIN_HOME=", "TIFFIN_URL=", "TIFFIN_TOKEN=")
	t.Cleanup(func() {
		if os.Getenv("TIFFIN_E2E_KEEP") != "1" {
			_ = exec.Command("limactl", "delete", "-f", instance).Run()
			_ = exec.Command("limactl", "disk", "delete", "-f", disk).Run()
		}
	})
	run := func(args ...string) (int, map[string]any, string) {
		t.Helper()
		cmd := exec.Command(cli, args...)
		cmd.Env = env
		out, err := cmd.Output()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("tiffin %v: %v", args, err)
		}
		var m map[string]any
		_ = json.Unmarshal(out, &m)
		return code, m, string(out)
	}

	// ---- up ----
	p := time.Now()
	code, up, raw := run("up", "--provider", "local", "--binary", good)
	if code != 0 {
		t.Fatalf("up: exit %d\n%s", code, raw)
	}
	wantURL := fmt.Sprintf("https://dashboard.tiffin.localhost:%d", port)
	if up["url"] != wantURL || !strings.HasPrefix(fmt.Sprint(up["login"]), wantURL+"/login#tfl_") {
		t.Fatalf("up output: %s", raw)
	}
	phase("up", p)

	// ---- HTTPS, status, doctor (all through the box's own CA) ----
	p = time.Now()
	if _, h, raw := run("health"); h["version"] != "0.0.1-a" {
		t.Fatalf("health over HTTPS: %s", raw)
	}
	if _, st, raw := run("status"); st["ok"] != true {
		t.Fatalf("status: %s", raw)
	}
	if code, d, raw := run("doctor"); code != 0 || d["ok"] != true {
		t.Fatalf("doctor: %d %s", code, raw)
	}
	if _, w, raw := run("whoami"); w["kind"] != "owner" {
		t.Fatalf("whoami: %s", raw)
	}
	phase("https+doctor", p)

	// ---- reboot: state, CA and services survive ----
	p = time.Now()
	m := filepath.Join(dir, "reboot", "tiffin.config.ts")
	_ = os.MkdirAll(filepath.Dir(m), 0o755)
	_ = os.WriteFile(m, []byte(`export default { project: "keepme", env: { A: "1" } }`), 0o644)
	_, plan, _ := run("plan", m)
	if code, _, raw := run("apply", m, "--confirm", fmt.Sprint(plan["hash"])); code != 0 {
		t.Fatalf("apply before reboot: %s", raw)
	}
	caBefore, _ := os.ReadFile(filepath.Join(dir, "config", "boxes", "local", "ca.crt"))
	for _, args := range [][]string{{"stop", instance}, {"start", instance, "--tty=false"}} {
		if out, err := exec.Command("limactl", args...).CombinedOutput(); err != nil {
			t.Fatalf("limactl %v: %v\n%s", args, err, out)
		}
	}
	var healthy bool
	for i := 0; i < 60 && !healthy; i++ {
		if code, h, _ := run("health"); code == 0 && h["status"] == "ok" {
			healthy = true
		} else {
			time.Sleep(2 * time.Second)
		}
	}
	if !healthy {
		t.Fatal("box not healthy after reboot")
	}
	if code, _, raw := run("projects", "get", "keepme"); code != 0 {
		t.Fatalf("project lost across reboot: %s", raw)
	}
	caAfter, _ := exec.Command("limactl", "shell", instance, "--", "sudo", "cat", "/var/lib/tiffin/platform/ca.crt").Output()
	if string(caAfter) != string(caBefore) {
		t.Fatal("box CA changed across reboot (data disk not mounted at boot?)")
	}
	phase("reboot", p)

	// ---- self-update round trip ----
	p = time.Now()
	if code, _, raw := run("up", "--provider", "local", "--binary", next); code != 0 {
		t.Fatalf("update: %d %s", code, raw)
	}
	if _, h, raw := run("health"); h["version"] != "0.0.2-b" {
		t.Fatalf("after update: %s", raw)
	}
	code, _, raw = run("up", "--provider", "local", "--binary", broken)
	if code == 0 || !strings.Contains(raw, "rolled back") {
		t.Fatalf("broken update must fail with a rollback: %d %s", code, raw)
	}
	if _, h, raw := run("health"); h["version"] != "0.0.2-b" {
		t.Fatalf("after rollback the previous build must serve: %s", raw)
	}
	phase("self-update", p)

	// ---- down ----
	p = time.Now()
	if code, _, raw := run("down"); code != 4 {
		t.Fatalf("down without confirm must exit 4: %d %s", code, raw)
	}
	if code, _, raw := run("down", "--confirm", "local"); code != 0 {
		t.Fatalf("down: %d %s", code, raw)
	}
	b := &Box{Name: instance, Disk: disk, t: t}
	b.AssertGone()
	phase("down", p)

	total := time.Since(start)
	t.Logf("TOTAL up->https->update->down: %s (budget %s)", total.Round(time.Second), budget)
	if total > budget {
		t.Errorf("over the %s budget", budget)
	}
}

func buildTiffin(t *testing.T, dir, goos, version string) string {
	t.Helper()
	return buildTiffinFrom(t, RepoRoot(), dir, goos, version)
}

// buildTiffinFrom builds the tiffin source tree at src, with any extra
// -X link flags.
func buildTiffinFrom(t *testing.T, src, dir, goos, version string, xflags ...string) string {
	t.Helper()
	name := "tiffin-host"
	env := os.Environ()
	if goos != "" {
		name = "tiffin-" + goos + "-" + version
		env = append(env, "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+HostArch())
	}
	out := filepath.Join(dir, name)
	args := []string{"build", "-trimpath", "-o", out}
	if version != "" {
		xflags = append(xflags, "-X github.com/shiptiffin/tiffin/internal/version.Version="+version)
	}
	if len(xflags) > 0 {
		args = append(args, "-ldflags", strings.Join(xflags, " "))
	}
	cmd := exec.Command("go", append(args, "./cmd/tiffin")...)
	cmd.Dir, cmd.Env = src, env
	if o, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, o)
	}
	return out
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
