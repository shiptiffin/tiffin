package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ErrRolledBack means the new build failed its health check and the
// previous build is serving again.
var ErrRolledBack = errors.New("new build was unhealthy; rolled back to the previous build")

// Updater switches builds on the box. Fields are overridable for tests.
type Updater struct {
	VersionsDir string
	BinLink     string
	HealthURL   string
	Timeout     time.Duration // how long a new build has to become healthy
	Restart     func(ctx context.Context) error
	Logs        func(ctx context.Context) string // recent service logs, for error reports
	Progress    func(string)
}

// NewUpdater returns the updater for a real box (systemd).
func NewUpdater() *Updater {
	return &Updater{
		VersionsDir: VersionsDir,
		BinLink:     BinLink,
		HealthURL:   "http://" + APIAddr + "/v1/health",
		Timeout:     30 * time.Second,
		Restart: func(ctx context.Context) error {
			out, err := exec.CommandContext(ctx, "systemctl", "restart", "tiffin").CombinedOutput()
			if err != nil {
				return fmt.Errorf("systemctl restart tiffin: %w: %s", err, out)
			}
			return nil
		},
		Logs: func(ctx context.Context) string {
			out, _ := exec.CommandContext(ctx, "journalctl", "-u", "tiffin", "-n", "20", "--no-pager", "-o", "cat").CombinedOutput()
			return string(out)
		},
		Progress: func(string) {},
	}
}

// Update installs newBin as the current build. It is atomic: the symlink
// flips in one rename, and if the new build does not answer /v1/health with
// its own build hash within Timeout, the previous build is restored.
func (u *Updater) Update(ctx context.Context, newBin string) error {
	sum, err := FileSHA(newBin)
	if err != nil {
		return err
	}
	dir := filepath.Join(u.VersionsDir, sum[:12])
	target := filepath.Join(dir, "tiffin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := copyFile(newBin, target+".tmp", 0o755); err != nil {
		return err
	}
	if err := os.Rename(target+".tmp", target); err != nil {
		return err
	}
	prev, _ := os.Readlink(u.BinLink)
	if prev == target {
		u.Progress("build " + sum[:12] + " is already current; making sure it runs")
		if err := u.Restart(ctx); err != nil {
			return err
		}
		return u.waitHealthy(ctx, sum)
	}
	if err := swapLink(u.BinLink, target); err != nil {
		return err
	}
	u.Progress("switched to build " + sum[:12] + "; restarting")
	if err := u.Restart(ctx); err == nil {
		if err = u.waitHealthy(ctx, sum); err == nil {
			u.prune(target)
			return nil
		}
	}
	logs := ""
	if u.Logs != nil {
		logs = u.Logs(ctx)
	}
	if prev == "" {
		return fmt.Errorf("new build %s did not become healthy and there is no previous build to roll back to\n%s", sum[:12], logs)
	}
	u.Progress("build " + sum[:12] + " is unhealthy; rolling back")
	if err := swapLink(u.BinLink, prev); err != nil {
		return fmt.Errorf("rollback failed: %w", err)
	}
	if err := u.Restart(ctx); err != nil {
		return fmt.Errorf("rollback restart failed: %w", err)
	}
	prevSum, _ := FileSHA(prev)
	if err := u.waitHealthy(ctx, prevSum); err != nil {
		return fmt.Errorf("rolled back, but the previous build is not healthy either: %w", err)
	}
	return fmt.Errorf("%w (build %s)\n%s", ErrRolledBack, sum[:12], logs)
}

// waitHealthy polls /v1/health until it reports build == sum.
func (u *Updater) waitHealthy(ctx context.Context, sum string) error {
	deadline := time.Now().Add(u.Timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	var last string
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.HealthURL, nil)
		if res, err := client.Do(req); err == nil {
			body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
			res.Body.Close()
			var h struct {
				Status string `json:"status"`
				Build  string `json:"build"`
			}
			_ = json.Unmarshal(body, &h)
			if res.StatusCode == 200 && h.Status == "ok" && h.Build == sum {
				return nil
			}
			last = fmt.Sprintf("health %d build %.12s", res.StatusCode, h.Build)
		} else {
			last = err.Error()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("not healthy after %s (last: %s)", u.Timeout, last)
}

// prune keeps the newest KeepBuilds builds (by modification time), never
// removing current.
func (u *Updater) prune(current string) {
	entries, err := os.ReadDir(u.VersionsDir)
	if err != nil {
		return
	}
	type build struct {
		path string
		mod  time.Time
	}
	var builds []build
	for _, e := range entries {
		p := filepath.Join(u.VersionsDir, e.Name(), "tiffin")
		if fi, err := os.Stat(p); err == nil {
			builds = append(builds, build{p, fi.ModTime()})
		}
	}
	sort.Slice(builds, func(i, j int) bool { return builds[i].mod.After(builds[j].mod) })
	for i, b := range builds {
		if i >= KeepBuilds && b.path != current {
			_ = os.RemoveAll(filepath.Dir(b.path))
		}
	}
}

// swapLink points link at target with a single atomic rename.
func swapLink(link, target string) error {
	tmp := link + ".new"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, link)
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Short returns the first 12 characters of a build hash.
func Short(sum string) string { return strings.TrimSpace(sum)[:min(12, len(strings.TrimSpace(sum)))] }
