// Package lima runs a Tiffin box as a local Lima VM (Apple Virtualization on
// macOS): Ubuntu 24.04, 2 vCPU / 4 GiB, and an XFS data disk at
// /var/lib/tiffin. It shells out to limactl.
package lima

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/provider"
)

//go:embed box.yaml
var template []byte

// HTTPSPort is the port the box serves HTTPS on, forwarded to the Mac's 127.0.0.1.
const HTTPSPort = 8443

// Provider is a Lima-backed box. Instance and Disk default to "tiffin".
type Provider struct {
	Instance string
	Disk     string // at most 7 characters: XFS labels "lima-<disk>" max 12
	DiskSize string // default "20GiB"
	Port     int    // host port forwarded to the box's HTTPS port; default 8443
}

// New returns the local box provider. TIFFIN_LIMA_INSTANCE, TIFFIN_LIMA_DISK
// and TIFFIN_LIMA_PORT override the defaults (tests use them to run a second
// box beside yours).
func New() *Provider {
	p := &Provider{Instance: "tiffin", Disk: "tiffin", DiskSize: "20GiB", Port: HTTPSPort}
	if v := os.Getenv("TIFFIN_LIMA_INSTANCE"); v != "" {
		p.Instance = v
	}
	if v := os.Getenv("TIFFIN_LIMA_DISK"); v != "" {
		p.Disk = v
	}
	if v, err := strconv.Atoi(os.Getenv("TIFFIN_LIMA_PORT")); err == nil && v > 0 {
		p.Port = v
	}
	return p
}

func (p *Provider) Name() string  { return "local" }
func (p *Provider) HostPort() int { return p.Port }

// Available reports whether limactl is installed.
func Available() error {
	if _, err := exec.LookPath("limactl"); err != nil {
		return fmt.Errorf("%w: limactl not found; install Lima (brew install lima)", provider.ErrUnavailable)
	}
	return nil
}

func (p *Provider) State(ctx context.Context) (provider.State, error) {
	if err := Available(); err != nil {
		return provider.StateAbsent, err
	}
	out, _, err := run(ctx, time.Minute, "limactl", "list", "--json")
	if err != nil {
		return provider.StateAbsent, err
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var inst struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		}
		if json.Unmarshal([]byte(line), &inst) == nil && inst.Name == p.Instance {
			if inst.Status == "Running" {
				return provider.StateRunning, nil
			}
			return provider.StateStopped, nil
		}
	}
	return provider.StateAbsent, nil
}

func (p *Provider) Up(ctx context.Context, progress func(string)) (provider.Machine, error) {
	if err := Available(); err != nil {
		return nil, err
	}
	st, err := p.State(ctx)
	if err != nil {
		return nil, err
	}
	if st == provider.StateAbsent {
		if !p.diskExists(ctx) {
			progress("creating the " + p.DiskSize + " XFS data disk")
			if _, stderr, err := run(ctx, 2*time.Minute, "limactl", "disk", "create", p.Disk, "--size", p.DiskSize, "--format", "raw", "--tty=false"); err != nil {
				return nil, fmt.Errorf("create disk: %w\n%s", err, stderr)
			}
		}
		dir, err := os.MkdirTemp("", "tiffin-lima-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
		tmpl := filepath.Join(dir, "tiffin.yaml")
		if err := os.WriteFile(tmpl, template, 0o600); err != nil {
			return nil, err
		}
		set := fmt.Sprintf(`.additionalDisks = [{"name": %q, "format": true, "fsType": "xfs"}] | .portForwards[0].hostPort = %d`, p.Disk, p.Port)
		if v := os.Getenv("TIFFIN_LIMA_MEMORY"); v != "" { // e.g. "2GiB" for small dev boxes
			set += fmt.Sprintf(` | .memory = %q`, v)
		}
		if v := os.Getenv("TIFFIN_LIMA_CPUS"); v != "" {
			set += " | .cpus = " + v
		}
		if v := os.Getenv("TIFFIN_LIMA_UBUNTU"); v == "26.04" { // try the next LTS; 24.04 stays the default
			set += ` | .base = [] | .images = [{"location": "https://cloud-images.ubuntu.com/releases/resolute/release/ubuntu-26.04-server-cloudimg-arm64.img", "arch": "aarch64"}, {"location": "https://cloud-images.ubuntu.com/releases/resolute/release/ubuntu-26.04-server-cloudimg-amd64.img", "arch": "x86_64"}]`
		} else if v != "" && v != "24.04" {
			return nil, fmt.Errorf("TIFFIN_LIMA_UBUNTU=%s: use 24.04 or 26.04", v)
		}
		if runtime.GOOS != "darwin" {
			set += ` | .vmType = "qemu"`
		}
		progress("creating the Ubuntu 24.04 VM (the first time downloads a ~600 MB image)")
		if _, stderr, err := run(ctx, 25*time.Minute, "limactl", "create", "--name", p.Instance, "--tty=false", "--set", set, tmpl); err != nil {
			return nil, fmt.Errorf("create VM: %w\n%s", err, tail(stderr))
		}
		st = provider.StateStopped
	}
	if st == provider.StateStopped {
		progress("booting the VM")
		if _, stderr, err := run(ctx, 15*time.Minute, "limactl", "start", p.Instance, "--tty=false", "--timeout", "14m"); err != nil {
			return nil, fmt.Errorf("start VM: %w\n%s", err, tail(stderr))
		}
	}
	return &machine{p: p}, nil
}

func (p *Provider) Destroy(ctx context.Context) error {
	if err := Available(); err != nil {
		return err
	}
	if st, _ := p.State(ctx); st != provider.StateAbsent {
		if _, stderr, err := run(ctx, 3*time.Minute, "limactl", "delete", "-f", "--tty=false", p.Instance); err != nil {
			return fmt.Errorf("delete VM: %w\n%s", err, stderr)
		}
	}
	if p.diskExists(ctx) {
		if _, stderr, err := run(ctx, time.Minute, "limactl", "disk", "delete", "-f", "--tty=false", p.Disk); err != nil {
			return fmt.Errorf("delete disk: %w\n%s", err, stderr)
		}
	}
	return nil
}

func (p *Provider) diskExists(ctx context.Context) bool {
	out, _, err := run(ctx, time.Minute, "limactl", "disk", "list", "--json")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var d struct {
			Name string `json:"name"`
		}
		if json.Unmarshal([]byte(line), &d) == nil && d.Name == p.Disk {
			return true
		}
	}
	return false
}

type machine struct{ p *Provider }

func (m *machine) Arch() string { return runtime.GOARCH } // vz runs the host's architecture

func (m *machine) Exec(ctx context.Context, script string) (string, string, error) {
	return run(ctx, 10*time.Minute, "limactl", "shell", "--workdir", "/", m.p.Instance, "--", "bash", "-c", script)
}

func (m *machine) Copy(ctx context.Context, local, remote string) error {
	if _, stderr, err := run(ctx, 5*time.Minute, "limactl", "copy", local, m.p.Instance+":"+remote); err != nil {
		return fmt.Errorf("copy %s: %w\n%s", local, err, stderr)
	}
	return nil
}

func run(ctx context.Context, timeout time.Duration, name string, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
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

func tail(s string) string {
	if len(s) > 3000 {
		return "..." + s[len(s)-3000:]
	}
	return s
}
