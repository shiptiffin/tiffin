//go:build e2e

// Package e2e boots real Lima VMs ("boxes") for Tiffin end-to-end tests.
//
// Run with `make e2e` (go test -tags e2e ./e2e/...). Tests skip themselves
// when limactl is not installed. Set TIFFIN_E2E_KEEP=1 to leave the VM and its
// data disk in place after a test (for debugging); the next run's sweep
// removes anything older than two hours.
package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	namePrefix = "tiffin-e2e-"
	// Lima labels an additional disk "lima-<name>" and XFS labels are limited
	// to 12 characters, so a disk name can be at most 7 characters long.
	diskPrefix = "e2e"
	diskSize   = "10GiB"
	// StaleAfter is the age after which Sweep deletes leftover e2e instances.
	StaleAfter = 2 * time.Hour
)

// Box is one Lima VM with an XFS data disk mounted at /var/lib/tiffin.
type Box struct {
	Name string // Lima instance name
	Disk string // Lima additional-disk name

	t       testing.TB
	downed  bool
	started time.Time
}

// RequireLima skips the test when limactl is not on PATH.
func RequireLima(t testing.TB) {
	t.Helper()
	if _, err := exec.LookPath("limactl"); err != nil {
		t.Skip("limactl not found on PATH; skipping e2e box test")
	}
}

// RepoRoot returns the repository root (the parent of the e2e directory).
func RepoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(filepath.Dir(file))
}

// HostArch returns the Go architecture name (arm64 or amd64) the VM runs.
func HostArch() string { return runtime.GOARCH }

// newName returns a unique instance name that embeds its creation time (base36
// unix seconds) so Sweep can tell how old a leftover is.
func newName() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s%s-%s", namePrefix, strconv.FormatInt(time.Now().Unix(), 36), hex.EncodeToString(b[:]))
}

// newDiskName returns a disk name of exactly 7 characters: "e2e" + 4 hex digits.
func newDiskName() string {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return diskPrefix + hex.EncodeToString(b[:])
}

func isE2EDisk(name string) bool {
	if len(name) != len(diskPrefix)+4 || !strings.HasPrefix(name, diskPrefix) {
		return false
	}
	_, err := hex.DecodeString(name[len(diskPrefix):])
	return err == nil
}

// Up creates the data disk and the VM, waits until it is ready and registers a
// t.Cleanup that destroys both (unless TIFFIN_E2E_KEEP=1). label is optional
// and only appears in logs.
func Up(t testing.TB, label string) *Box {
	t.Helper()
	RequireLima(t)

	b := &Box{Name: newName(), t: t, started: time.Now()}
	b.Disk = newDiskName()
	if label == "" {
		label = "box"
	}
	t.Logf("[%s] up: instance %s, disk %s", label, b.Name, b.Disk)

	t.Cleanup(func() {
		if os.Getenv("TIFFIN_E2E_KEEP") == "1" {
			t.Logf("TIFFIN_E2E_KEEP=1: leaving %s and %s in place", b.Name, b.Disk)
			return
		}
		b.Down()
	})

	// 1. the data disk. Raw format so Lima can mkfs.xfs it on first boot.
	b.must("limactl", "disk", "create", b.Disk, "--size", diskSize, "--format", "raw", "--tty=false")

	// 2. the VM, from (the image download happens inside `create`, hence the long timeout) the checked-in template with this instance's disk swapped in.
	tmpl := filepath.Join(RepoRoot(), "e2e", "lima", "tiffin-box.yaml")
	setExpr := fmt.Sprintf(`.additionalDisks = [{"name": %q, "format": true, "fsType": "xfs"}]`, b.Disk)
	if runtime.GOOS != "darwin" {
		setExpr += ` | .vmType = "qemu"`
	}
	b.mustTimeout(25*time.Minute, "limactl", "create", "--name", b.Name, "--tty=false", "--set", setExpr, tmpl)

	// 3. boot and wait for readiness (first run also downloads the image).
	if err := b.run(15*time.Minute, "limactl", "start", b.Name, "--tty=false", "--timeout", "14m"); err != nil {
		b.dumpLogs()
		t.Fatalf("limactl start %s: %v", b.Name, err)
	}
	return b
}

// Exec runs a command inside the VM through `limactl shell`.
func (b *Box) Exec(cmd ...string) (stdout, stderr string, err error) {
	return b.ExecTimeout(5*time.Minute, cmd...)
}

// ExecTimeout is Exec with an explicit timeout.
func (b *Box) ExecTimeout(timeout time.Duration, cmd ...string) (stdout, stderr string, err error) {
	args := append([]string{"shell", "--workdir", "/", b.Name, "--"}, cmd...)
	return runCmd(timeout, "limactl", args...)
}

// CopyIn copies a local file into the VM.
func (b *Box) CopyIn(local, remote string) error {
	_, stderr, err := runCmd(5*time.Minute, "limactl", "copy", local, b.Name+":"+remote)
	if err != nil {
		return fmt.Errorf("limactl copy %s -> %s:%s: %w\n%s", local, b.Name, remote, err, stderr)
	}
	return nil
}

// Down stops and deletes the VM and its data disk. It is idempotent and never
// fails the test; leftovers are reported through t.Errorf by AssertGone.
func (b *Box) Down() {
	if b.downed {
		return
	}
	b.downed = true
	b.t.Logf("down: deleting %s and %s", b.Name, b.Disk)
	// `limactl delete -f` kills a running instance, so no separate stop is needed.
	if exists, _ := instanceExists(b.Name); exists {
		if _, stderr, err := runCmd(3*time.Minute, "limactl", "delete", "-f", "--tty=false", b.Name); err != nil {
			b.t.Logf("limactl delete %s: %v\n%s", b.Name, err, stderr)
		}
	}
	if exists, _ := diskExists(b.Disk); exists {
		if _, stderr, err := runCmd(time.Minute, "limactl", "disk", "delete", "-f", "--tty=false", b.Disk); err != nil {
			b.t.Logf("limactl disk delete %s: %v\n%s", b.Disk, err, stderr)
		}
	}
}

// AssertGone fails the test if the VM or the disk still exists.
func (b *Box) AssertGone() {
	b.t.Helper()
	if ok, err := instanceExists(b.Name); err != nil || ok {
		b.t.Errorf("instance %s still exists (err=%v)", b.Name, err)
	}
	if ok, err := diskExists(b.Disk); err != nil || ok {
		b.t.Errorf("disk %s still exists (err=%v)", b.Disk, err)
	}
}

// ---- helpers ----

func (b *Box) must(name string, args ...string) {
	b.t.Helper()
	b.mustTimeout(5*time.Minute, name, args...)
}

func (b *Box) mustTimeout(timeout time.Duration, name string, args ...string) {
	b.t.Helper()
	if err := b.run(timeout, name, args...); err != nil {
		b.t.Fatalf("%s %s: %v", name, strings.Join(args, " "), err)
	}
}

func (b *Box) run(timeout time.Duration, name string, args ...string) error {
	stdout, stderr, err := runCmd(timeout, name, args...)
	if err != nil {
		return fmt.Errorf("%w\nstdout:\n%s\nstderr:\n%s", err, tail(stdout, 4000), tail(stderr, 4000))
	}
	return nil
}

// dumpLogs prints the tail of Lima's hostagent logs; they usually explain a failed boot.
func (b *Box) dumpLogs() {
	dir, err := instanceDir(b.Name)
	if err != nil || dir == "" {
		return
	}
	for _, f := range []string{"ha.stderr.log", "serial.log", "serialv.log"} {
		if data, err := os.ReadFile(filepath.Join(dir, f)); err == nil {
			b.t.Logf("--- %s (tail) ---\n%s", f, tail(string(data), 3000))
		}
	}
}

func runCmd(timeout time.Duration, name string, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("timed out after %s: %w", timeout, err)
	}
	return out.String(), errb.String(), err
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

type limaInstance struct {
	Name   string `json:"name"`
	Dir    string `json:"dir"`
	Status string `json:"status"`
}

type limaDisk struct {
	Name string `json:"name"`
	Dir  string `json:"dir"`
}

// listJSON decodes limactl's newline-delimited (or array) JSON output.
func listJSON[T any](args ...string) ([]T, error) {
	stdout, stderr, err := runCmd(time.Minute, "limactl", args...)
	if err != nil {
		return nil, fmt.Errorf("limactl %s: %w\n%s", strings.Join(args, " "), err, stderr)
	}
	stdout = strings.TrimSpace(stdout)
	if stdout == "" {
		return nil, nil
	}
	if strings.HasPrefix(stdout, "[") {
		var all []T
		return all, json.Unmarshal([]byte(stdout), &all)
	}
	var all []T
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var v T
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			return nil, fmt.Errorf("decode %q: %w", line, err)
		}
		all = append(all, v)
	}
	return all, nil
}

func instances() ([]limaInstance, error) { return listJSON[limaInstance]("list", "--json") }
func disks() ([]limaDisk, error)         { return listJSON[limaDisk]("disk", "list", "--json") }

func instanceExists(name string) (bool, error) {
	all, err := instances()
	for _, i := range all {
		if i.Name == name {
			return true, err
		}
	}
	return false, err
}

func diskExists(name string) (bool, error) {
	all, err := disks()
	for _, d := range all {
		if d.Name == name {
			return true, err
		}
	}
	return false, err
}

func instanceDir(name string) (string, error) {
	all, err := instances()
	for _, i := range all {
		if i.Name == name {
			return i.Dir, err
		}
	}
	return "", err
}

// createdAt extracts the creation time embedded in a name made by newName.
func createdAt(name string) (time.Time, bool) {
	rest, ok := strings.CutPrefix(name, namePrefix)
	if !ok {
		return time.Time{}, false
	}
	stamp, _, _ := strings.Cut(rest, "-")
	secs, err := strconv.ParseInt(stamp, 36, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(secs, 0), true
}

// Sweep deletes tiffin-e2e-* instances and e2eXXXX disks older than maxAge, left behind
// by crashed or interrupted runs. Names whose age can't be read are judged by
// their directory's modification time.
func Sweep(logf func(format string, args ...any), maxAge time.Duration) {
	if _, err := exec.LookPath("limactl"); err != nil {
		return
	}
	stale := func(name, dir string) bool {
		if !strings.HasPrefix(name, namePrefix) && !isE2EDisk(name) {
			return false
		}
		if ts, ok := createdAt(name); ok {
			return time.Since(ts) > maxAge
		}
		if fi, err := os.Stat(dir); err == nil {
			return time.Since(fi.ModTime()) > maxAge
		}
		return false
	}
	if all, err := instances(); err == nil {
		for _, i := range all {
			if stale(i.Name, i.Dir) {
				logf("sweep: deleting stale instance %s", i.Name)
				_, _, _ = runCmd(3*time.Minute, "limactl", "delete", "-f", "--tty=false", i.Name)
			}
		}
	}
	if all, err := disks(); err == nil {
		for _, d := range all {
			if stale(d.Name, d.Dir) {
				logf("sweep: deleting stale disk %s", d.Name)
				_, _, _ = runCmd(time.Minute, "limactl", "disk", "delete", "-f", "--tty=false", d.Name)
			}
		}
	}
}
