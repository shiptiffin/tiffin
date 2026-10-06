package install

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// ErrRolledBack means the new build failed its health check and the
// previous build is serving again.
var ErrRolledBack = errors.New("new build was unhealthy; rolled back to the previous build")

// Updater switches builds on the box. Fields are overridable for tests.
type Updater struct {
	VersionsDir string
	BinLink     string
	HealthURL   string
	// StateDB is the platform state database, snapshotted before a switch
	// and put back on rollback (a new build may migrate it past what the
	// previous one opens). Empty: not touched.
	StateDB string
	Timeout time.Duration // how long a new build has to become healthy
	Restart func(ctx context.Context) error
	Stop    func(ctx context.Context) error
	// RestoreUnits puts the unit files of the build before back when the
	// update changed them (see UnitsBackup), before that build starts again.
	// StopRemoved then stops the units it removed (the edge's) once that
	// build serves: until then their sockets hold connections for it.
	RestoreUnits func(ctx context.Context) error
	StopRemoved  func(ctx context.Context)
	// Restarts counts the times systemd restarted the service after a
	// crash: a new build that keeps crashing is rolled back without
	// waiting out Timeout.
	Restarts func(ctx context.Context) int
	Logs     func(ctx context.Context) string // recent service logs, for error reports
	Progress func(string)
}

// NewUpdater returns the updater for a real box (systemd).
func NewUpdater() *Updater {
	return &Updater{
		VersionsDir: VersionsDir,
		BinLink:     BinLink,
		HealthURL:   "http://" + APIAddr + "/v1/health",
		StateDB:     filepath.Join(Home, "state.db"),
		// Health says ok only once every module started, which takes a while.
		Timeout: 90 * time.Second,
		Stop: func(ctx context.Context) error {
			if out, err := exec.CommandContext(ctx, "systemctl", "stop", "tiffin").CombinedOutput(); err != nil {
				return fmt.Errorf("systemctl stop tiffin: %w: %s", err, out)
			}
			return nil
		},
		Restart: func(ctx context.Context) error {
			// A build that crash-loops trips systemd's start limit, and the unit
			// then refuses a plain restart: clear it first, and retry a little,
			// since a rollback must not give up on the build that worked.
			if err := startEdge(ctx); err != nil {
				return err
			}
			var last error
			for i := 0; i < 3; i++ {
				_ = exec.CommandContext(ctx, "systemctl", "reset-failed", "tiffin").Run()
				out, err := exec.CommandContext(ctx, "systemctl", "restart", "tiffin").CombinedOutput()
				if err == nil {
					return nil
				}
				last = fmt.Errorf("systemctl restart tiffin: %w: %s", err, out)
				select {
				case <-ctx.Done():
					return last
				case <-time.After(2 * time.Second):
				}
			}
			return last
		},
		RestoreUnits: func(ctx context.Context) error {
			return restoreUnits(ctx, systemctl, UnitsBackup, UnitDir)
		},
		StopRemoved: func(ctx context.Context) {
			for _, u := range Units {
				if !fileExists(filepath.Join(UnitDir, u)) {
					_ = systemctl(ctx, "stop", u)
				}
			}
		},
		Restarts: func(ctx context.Context) int {
			out, _ := exec.CommandContext(ctx, "systemctl", "show", "-p", "NRestarts", "--value", "tiffin").Output()
			n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
			return n
		},
		Logs: func(ctx context.Context) string {
			out, _ := exec.CommandContext(ctx, "journalctl", "-u", "tiffin", "-n", "20", "--no-pager", "-o", "cat").CombinedOutput()
			return string(out)
		},
		Progress: func(string) {},
	}
}

func systemctl(ctx context.Context, args ...string) error {
	if out, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return nil
}

// restoreUnits puts back the unit files saved in backup (by Install, before
// it wrote new ones) with tiffin stopped; the units the backup lacks, which
// the update added, are disabled and removed (StopRemoved stops them once
// the previous build serves). A backup without the main unit (a first
// install) restores nothing.
func restoreUnits(ctx context.Context, sysctl func(context.Context, ...string) error, backup, unitDir string) error {
	if _, err := os.Stat(filepath.Join(backup, Units[0])); err != nil {
		return nil
	}
	_ = sysctl(ctx, "stop", "tiffin")
	for _, u := range Units {
		if src := filepath.Join(backup, u); fileExists(src) {
			if err := copyFile(src, filepath.Join(unitDir, u), 0o644); err != nil {
				return err
			}
			continue
		}
		_ = sysctl(ctx, "disable", u)
		if err := os.Remove(filepath.Join(unitDir, u)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return sysctl(ctx, "daemon-reload")
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// startEdge starts the edge's units the first time, on a box whose tiffin
// still ran the edge itself, and stops that tiffin. The socket shares the
// ports with it (SO_REUSEPORT, as Caddy listens), so it binds them first:
// from then on new connections that reach it wait for the new edge rather
// than being refused while the old tiffin stops. If it cannot, tiffin
// stops first. Later restarts of tiffin leave the edge running.
func startEdge(ctx context.Context) error {
	if _, err := os.Stat(EdgeUnits + ".service"); err != nil {
		return nil
	}
	if exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "tiffin-edge.socket").Run() == nil {
		return nil
	}
	start := func() error {
		return systemctl(ctx, "start", "tiffin-edge.socket", "tiffin-edge.service")
	}
	err := start()
	_ = systemctl(ctx, "stop", "tiffin")
	if err != nil {
		_ = systemctl(ctx, "reset-failed", "tiffin-edge.socket", "tiffin-edge.service")
		return start()
	}
	return nil
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
	// The state as the previous build left it, for a rollback. Writes in the
	// moment between this snapshot and the restart are lost if it rolls back.
	snap := ""
	if _, err := os.Stat(u.StateDB); prev != "" && u.StateDB != "" && err == nil {
		snap = filepath.Join(dir, "state-before.db")
		if err := snapshotState(ctx, u.StateDB, snap); err != nil {
			return fmt.Errorf("snapshot the state database before switching: %w", err)
		}
	}
	if err := swapLink(u.BinLink, target); err != nil {
		return err
	}
	u.Progress("switched to build " + sum[:12] + "; restarting")
	if err := u.Restart(ctx); err == nil {
		if err = u.waitHealthy(ctx, sum); err == nil {
			if snap != "" {
				_ = os.Remove(snap)
			}
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
	if u.RestoreUnits != nil {
		if err := u.RestoreUnits(ctx); err != nil {
			return fmt.Errorf("rollback: restore the unit files: %w", err)
		}
	}
	if snap != "" {
		// Stopped first, so the new build cannot write the state again; the
		// link already points back, so systemd only ever starts the previous
		// build, which refuses a newer schema without writing.
		if u.Stop != nil {
			if err := u.Stop(ctx); err != nil {
				return fmt.Errorf("rollback: %w; the state from before the update is in %s", err, snap)
			}
		}
		if err := restoreState(snap, u.StateDB); err != nil {
			return fmt.Errorf("rollback: restore the state database: %w; the state from before the update is in %s", err, snap)
		}
	}
	// Health decides, not the restart's exit code: systemd (Restart=always)
	// starts the previous build on its own even if this call was refused.
	restartErr := u.Restart(ctx)
	prevSum, _ := FileSHA(prev)
	err = u.waitHealthy(ctx, prevSum)
	if u.StopRemoved != nil {
		u.StopRemoved(ctx)
	}
	if err != nil {
		if restartErr != nil {
			return fmt.Errorf("rollback restart failed: %w; and the previous build is not healthy: %v", restartErr, err)
		}
		return fmt.Errorf("rolled back, but the previous build is not healthy either: %w", err)
	}
	return fmt.Errorf("%w (build %s)\n%s", ErrRolledBack, sum[:12], logs)
}

// waitHealthy polls /v1/health until it reports build == sum.
func (u *Updater) waitHealthy(ctx context.Context, sum string) error {
	deadline := time.Now().Add(u.Timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	var last string
	base := -1
	for time.Now().Before(deadline) {
		if u.Restarts != nil {
			n := u.Restarts(ctx)
			if base < 0 {
				base = n
			} else if n-base >= 3 {
				return fmt.Errorf("it crashed %d times while starting (last: %s)", n-base, last)
			}
		}
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

// snapshotState writes a consistent copy of the live state database (it
// holds sealed secrets: owner-only from the start).
func snapshotState(ctx context.Context, db, dst string) error {
	_ = os.Remove(dst)
	if err := os.WriteFile(dst, nil, 0o600); err != nil {
		return err
	}
	conn, err := sql.Open("sqlite3", "file:"+(&url.URL{Path: db}).EscapedPath()+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.ExecContext(ctx, `VACUUM INTO ?`, dst)
	return err
}

// restoreState puts the snapshot back as the state database, with the
// newer build's WAL removed so it is not replayed onto it.
func restoreState(snap, db string) error {
	if err := copyFile(snap, db+".rollback", 0o600); err != nil {
		return err
	}
	for _, s := range []string{"-wal", "-shm"} {
		if err := os.Remove(db + s); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return os.Rename(db+".rollback", db)
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
