package platform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// System is the toolbox Provisioners use to change the machine. Every helper
// is idempotent: it checks before it changes, and reports what it did.
type System struct {
	Log      func(string) // progress lines for people
	CacheDir string       // downloads, e.g. /var/lib/tiffin/cache

	// Provisioners run concurrently; apt is one-at-a-time (dpkg has one
	// lock) and the package index is refreshed once, only when needed.
	aptMu       sync.Mutex
	needsUpdate bool
	updated     bool
}

// NewSystem returns a System that logs to log.
func NewSystem(log func(string)) *System {
	if log == nil {
		log = func(string) {}
	}
	return &System{Log: log, CacheDir: "/var/lib/tiffin/cache"}
}

// Run runs a command and returns combined output, with the output in the error.
func (s *System) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive", "NEEDRESTART_MODE=a")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, tail(out.String(), 2000))
	}
	return out.String(), nil
}

// Sh runs a bash script.
func (s *System) Sh(ctx context.Context, script string) (string, error) {
	return s.Run(ctx, "bash", "-euo", "pipefail", "-c", script)
}

// Installed reports whether a Debian package is installed.
func (s *System) Installed(ctx context.Context, pkg string) bool {
	return s.InstalledVersion(ctx, pkg) != ""
}

// InstalledVersion is an installed Debian package's version, "" if it is
// not installed.
func (s *System) InstalledVersion(ctx context.Context, pkg string) string {
	out, err := exec.CommandContext(ctx, "dpkg-query", "-W", "-f=${Status}\t${Version}", pkg).Output()
	status, version, _ := strings.Cut(string(out), "\t")
	if err != nil || !strings.Contains(status, "install ok installed") {
		return ""
	}
	return strings.TrimSpace(version)
}

// aptWanted picks the packages Apt must install: missing ones, and pinned
// ones ("name=version") whose installed version is not the pin.
func aptWanted(pkgs []string, installed func(string) string) []string {
	var out []string
	for _, p := range pkgs {
		name, pin, pinned := strings.Cut(p, "=")
		if cur := installed(name); cur == "" || pinned && cur != pin {
			out = append(out, p)
		}
	}
	return out
}

// Apt installs Debian packages that are missing. A pinned package
// ("name=version") whose installed version differs is installed at the
// pin, so a release that moves a pin updates the box on its next tiffin up.
func (s *System) Apt(ctx context.Context, pkgs ...string) error {
	s.aptMu.Lock()
	defer s.aptMu.Unlock()
	missing := aptWanted(pkgs, func(name string) string { return s.InstalledVersion(ctx, name) })
	if len(missing) == 0 {
		return nil
	}
	s.Log("installing " + strings.Join(missing, ", "))
	install := append([]string{"-o", "DPkg::Lock::Timeout=600", "-o", "Acquire::Retries=5", "install", "-y", "--no-install-recommends", "-o", "Dpkg::Options::=--force-confold"}, missing...)
	if s.needsUpdate {
		if err := s.aptUpdate(ctx); err != nil {
			return err
		}
	}
	if _, err := s.Run(ctx, "apt-get", install...); err != nil {
		if s.updated {
			return err
		}
		// A fresh box may have a stale index: refresh once and retry.
		if uerr := s.aptUpdate(ctx); uerr != nil {
			return uerr
		}
		_, err = s.Run(ctx, "apt-get", install...)
		return err
	}
	return nil
}

func (s *System) aptUpdate(ctx context.Context) error {
	_, err := s.Run(ctx, "apt-get", "-o", "DPkg::Lock::Timeout=600", "-o", "Acquire::Retries=5", "update", "-y")
	if err == nil {
		s.updated, s.needsUpdate = true, false
	}
	return err
}

// AptRepo adds a signed apt repository (key from keyURL, dearmored) once.
func (s *System) AptRepo(ctx context.Context, name, keyURL, line string) error {
	list := "/etc/apt/sources.list.d/" + name + ".list"
	key := "/etc/apt/keyrings/" + name + ".gpg"
	want := strings.ReplaceAll(line, "{key}", key) + "\n"
	if cur, err := os.ReadFile(list); err == nil && string(cur) == want {
		return nil
	}
	s.Log("adding apt repository " + name)
	if err := os.MkdirAll("/etc/apt/keyrings", 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(os.TempDir(), name+".asc")
	if err := s.download(ctx, keyURL, tmp); err != nil {
		return err
	}
	if _, err := s.Run(ctx, "gpg", "--batch", "--yes", "--dearmor", "-o", key, tmp); err != nil {
		return err
	}
	if err := os.WriteFile(list, []byte(want), 0o644); err != nil {
		return err
	}
	s.aptMu.Lock()
	s.needsUpdate, s.updated = true, false // the next Apt refreshes the index once
	s.aptMu.Unlock()
	return nil
}

// Fetch downloads url to the cache, verifying sha256 (hex), and returns the
// cached path. An empty sha256 is refused: pin every download.
func (s *System) Fetch(ctx context.Context, url, sum string) (string, error) {
	if len(sum) != 64 {
		return "", fmt.Errorf("fetch %s: a sha256 pin is required", url)
	}
	if err := os.MkdirAll(s.CacheDir, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(s.CacheDir, sum[:16]+"-"+filepath.Base(url))
	if got, err := fileSHA(dest); err == nil && got == sum {
		return dest, nil
	}
	s.Log("downloading " + filepath.Base(url))
	tmp := dest + ".part"
	if err := s.download(ctx, url, tmp); err != nil {
		return "", err
	}
	got, err := fileSHA(tmp)
	if err != nil {
		return "", err
	}
	if got != sum {
		os.Remove(tmp)
		return "", fmt.Errorf("fetch %s: sha256 %s, want %s", url, got, sum)
	}
	return dest, os.Rename(tmp, dest)
}

func (s *System) download(ctx context.Context, url, dest string) error {
	// Boxes fetch many things at once on first provision; a slow mirror or a
	// TLS handshake timeout should not fail the whole install.
	var err error
	for attempt := 1; attempt <= 4; attempt++ {
		if err = s.downloadOnce(ctx, url, dest); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return err
		}
		s.Log(fmt.Sprintf("retrying %s (%v)", filepath.Base(url), err))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt*attempt) * time.Second):
		}
	}
	return err
}

var dlClient = &http.Client{Transport: &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	DialContext:           (&net.Dialer{Timeout: 20 * time.Second}).DialContext,
	TLSHandshakeTimeout:   30 * time.Second,
	ResponseHeaderTimeout: 60 * time.Second,
	ForceAttemptHTTP2:     true,
}}

func (s *System) downloadOnce(ctx context.Context, url, dest string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	res, err := dlClient.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("download %s: HTTP %d", url, res.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, res.Body); err != nil {
		f.Close()
		return fmt.Errorf("download %s: %w", url, err)
	}
	return f.Close()
}

// Untar extracts a .tar.gz into dir (created), stripping leading components.
func (s *System) Untar(ctx context.Context, archive, dir string, strip int) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	_, err := s.Run(ctx, "tar", "-xzf", archive, "-C", dir, fmt.Sprintf("--strip-components=%d", strip))
	return err
}

// WriteFile writes data if it differs, and reports whether it changed.
func (s *System) WriteFile(path string, data []byte, mode os.FileMode) (bool, error) {
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, data) {
		return false, os.Chmod(path, mode)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, path)
}

// Unit installs a systemd unit, enables it and (re)starts it when the unit
// changed or it is not running.
func (s *System) Unit(ctx context.Context, name, content string) error {
	changed, err := s.WriteFile("/etc/systemd/system/"+name, []byte(content), 0o644)
	if err != nil {
		return err
	}
	if changed {
		if _, err := s.Run(ctx, "systemctl", "daemon-reload"); err != nil {
			return err
		}
	}
	if _, err := s.Run(ctx, "systemctl", "enable", name); err != nil {
		return err
	}
	active := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", name).Run() == nil
	if changed || !active {
		s.Log("starting " + strings.TrimSuffix(name, ".service"))
		_, err = s.Run(ctx, "systemctl", "restart", name)
	}
	return err
}

// User ensures a system user exists.
func (s *System) User(ctx context.Context, name, home string) error {
	if exec.CommandContext(ctx, "id", name).Run() == nil {
		return nil
	}
	_, err := s.Run(ctx, "useradd", "--system", "--home-dir", home, "--no-create-home", "--shell", "/usr/sbin/nologin", name)
	return err
}

// WaitTCP waits until addr accepts connections.
func (s *System) WaitTCP(ctx context.Context, addr string, d time.Duration) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if c, err := (&netDialer).DialContext(ctx, "tcp", addr); err == nil {
			c.Close()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("timed out waiting for " + addr)
}

var netDialer = net.Dialer{Timeout: time.Second}

func fileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func tail(s string, n int) string {
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}

// MemoryMB is the machine's RAM (MemTotal), read now: provisioners size
// services from it, so a resized box is retuned on the next tiffin up.
// 0 when it cannot be read (not Linux).
func MemoryMB() int {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(line, "MemTotal:"); ok {
			if f := strings.Fields(rest); len(f) > 0 {
				kb, _ := strconv.Atoi(f[0])
				return kb / 1024
			}
		}
	}
	return 0
}
