package portable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/shiptiffin/tiffin/internal/mod/datakit"
	"github.com/shiptiffin/tiffin/internal/mod/runtime"
	"github.com/shiptiffin/tiffin/internal/mod/valkey"
	"github.com/shiptiffin/tiffin/internal/state"
)

// ---- app containers (nerdctl, the runtime's containerd namespace) ----

const nerdctlBin = "/usr/local/bin/nerdctl"

func nerdctl(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, nerdctlBin, append([]string{"--namespace", runtime.Namespace}, args...)...)
	cmd.Env = append(os.Environ(), "CONTAINERD_NAMESPACE="+runtime.Namespace,
		"HOME="+filepath.Join(runtime.DataDir, "home"), "DOCKER_CONFIG="+filepath.Join(runtime.DataDir, "home", ".docker"))
	cmd.WaitDelay = 10 * time.Second
	return cmd
}

func nerdctlRun(ctx context.Context, args ...string) (string, error) {
	out, err := nerdctl(ctx, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("nerdctl %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// appContainers lists the runtime's app containers (running ones only, or all).
func appContainers(ctx context.Context, all bool) ([]string, error) {
	if _, err := os.Stat(nerdctlBin); err != nil {
		return nil, nil // no runtime on this box
	}
	args := []string{"ps", "--filter", "label=tiffin.project", "--format", "{{.Names}}"}
	if all {
		args = append(args, "--all")
	}
	out, err := nerdctlRun(ctx, args...)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			names = append(names, l)
		}
	}
	return names, nil
}

// pauseApps freezes running app containers (cgroup freezer); resume thaws them.
func pauseApps(ctx context.Context) (n int, resume func(), err error) {
	names, err := appContainers(ctx, false)
	if err != nil || len(names) == 0 {
		return 0, func() {}, err
	}
	if _, err := nerdctlRun(ctx, append([]string{"pause"}, names...)...); err != nil {
		// Some may have paused: thaw whatever did.
		_, _ = nerdctlRun(context.WithoutCancel(ctx), append([]string{"unpause"}, names...)...)
		return 0, func() {}, err
	}
	return len(names), func() {
		uctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, _ = nerdctlRun(uctx, append([]string{"unpause"}, names...)...)
	}, nil
}

// removeApps stops and removes every app container (import --replace).
func removeApps(ctx context.Context) (int, error) {
	names, err := appContainers(ctx, true)
	if err != nil || len(names) == 0 {
		return 0, err
	}
	_, err = nerdctlRun(ctx, append([]string{"rm", "--force"}, names...)...)
	return len(names), err
}

// freezeUnit freezes a systemd service's cgroup; thaw undoes it.
func freezeUnit(ctx context.Context, unit string) (thaw func(), err error) {
	if _, err := datakit.Run(ctx, "systemctl", "is-active", "--quiet", unit); err != nil {
		return func() {}, nil // not running: nothing writes
	}
	if _, err := datakit.Run(ctx, "systemctl", "freeze", unit); err != nil {
		return func() {}, err
	}
	return func() {
		tctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, _ = datakit.Run(tctx, "systemctl", "thaw", unit)
	}, nil
}

// ---- images ----

// liveImage is the image a live app environment runs.
type liveImage struct {
	Ref     string
	Project string
	App     string
	Preview string
	Deploy  string
}

// liveImages lists the images of every app environment's live deploy, from
// the runtime's records in the state database.
func liveImages(ctx context.Context, db *state.DB) ([]liveImage, error) {
	states, err := db.KVList(ctx, nsRuntimeState)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []liveImage
	for _, raw := range states {
		var st runtime.AppState
		if json.Unmarshal(raw, &st) != nil || st.Live == "" {
			continue
		}
		b, ok, err := db.KVGet(ctx, nsRuntimeDeploys(st.Project, st.App), st.Live)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		var d runtime.Deploy
		if json.Unmarshal(b, &d) != nil || d.Image == "" || seen[d.Image] {
			continue
		}
		seen[d.Image] = true
		out = append(out, liveImage{Ref: d.Image, Project: st.Project, App: st.App, Preview: st.Preview, Deploy: d.ID})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out, nil
}

// The runtime module's KV namespaces (internal/mod/runtime/model.go).
const nsRuntimeState = "runtime/state"

func nsRuntimeDeploys(project, app string) string { return "runtime/deploys/" + project + "/" + app }

// presentImages keeps the refs that exist in containerd.
func presentImages(ctx context.Context, imgs []liveImage) []liveImage {
	var out []liveImage
	for _, im := range imgs {
		if _, err := nerdctlRun(ctx, "image", "inspect", "--format", "{{.ID}}", im.Ref); err == nil {
			out = append(out, im)
		}
	}
	return out
}

// saveImages streams `nerdctl save` of refs (shared layers once) into w.
func saveImages(ctx context.Context, refs []string, w func(io.Reader) error) error {
	cmd := nerdctl(ctx, append([]string{"save"}, refs...)...)
	var errb tailBuffer
	cmd.Stderr = &errb
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	werr := w(out)
	if werr != nil {
		_ = cmd.Process.Kill()
	}
	_, _ = io.Copy(io.Discard, out)
	if err := cmd.Wait(); err != nil && werr == nil {
		return fmt.Errorf("nerdctl save: %w: %s", err, errb.String())
	}
	return werr
}

// loadImages pipes an image archive into `nerdctl load`.
func loadImages(ctx context.Context, r io.Reader) error {
	cmd := nerdctl(ctx, "load")
	var errb tailBuffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = r, &errb, &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("nerdctl load: %w: %s", err, errb.String())
	}
	return nil
}

// ---- Valkey ----

// bgsave starts a background save (the snapshot is the fork moment) and
// returns a wait func that blocks until it is on disk.
func bgsave(ctx context.Context) (wait func(context.Context) error, err error) {
	c, err := valkey.Admin(ctx)
	if err != nil {
		return nil, err
	}
	before, err := c.Int(ctx, "LASTSAVE")
	if err != nil {
		c.Close()
		return nil, err
	}
	if _, err := c.Do(ctx, "BGSAVE"); err != nil && !strings.Contains(err.Error(), "in progress") {
		c.Close()
		return nil, err
	}
	return func(ctx context.Context) error {
		defer c.Close()
		deadline := time.Now().Add(10 * time.Minute)
		for {
			raw, err := c.String(ctx, "INFO", "persistence")
			if err != nil {
				return err
			}
			info := map[string]string{}
			for _, l := range strings.Split(raw, "\n") {
				if k, v, ok := strings.Cut(strings.TrimSpace(l), ":"); ok {
					info[k] = v
				}
			}
			last, _ := strconv.ParseInt(info["rdb_last_save_time"], 10, 64)
			if info["rdb_bgsave_in_progress"] == "0" && last >= before && (last > before || info["rdb_last_bgsave_status"] == "ok") {
				if info["rdb_last_bgsave_status"] != "ok" {
					return errors.New("valkey BGSAVE failed (see journalctl -u " + valkey.UnitName + ")")
				}
				return nil
			}
			if time.Now().After(deadline) {
				return errors.New("valkey BGSAVE did not finish in 10 minutes")
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}, nil
}

func valkeyKeys(ctx context.Context) int {
	c, err := valkey.Admin(ctx)
	if err != nil {
		return 0
	}
	defer c.Close()
	n, _ := c.Int(ctx, "DBSIZE")
	return int(n)
}

// ---- files ----

// reflinkCopy copies src to dst sharing extents (instant on XFS). It fails
// where the filesystem cannot share extents.
func reflinkCopy(ctx context.Context, src, dst string) error {
	_, err := datakit.Run(ctx, "cp", "-a", "--reflink=always", src, dst)
	return err
}

// canReflink reports whether dir's filesystem shares extents.
func canReflink(ctx context.Context, dir string) bool {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false
	}
	a := filepath.Join(dir, ".reflink-probe")
	b := a + "2"
	defer os.Remove(a)
	defer os.Remove(b)
	if err := os.WriteFile(a, []byte("probe"), 0o600); err != nil {
		return false
	}
	return reflinkCopy(ctx, a, b) == nil
}

// freeBytes is the space available to root on path's filesystem.
func freeBytes(path string) int64 {
	var st syscall.Statfs_t
	for p := path; ; p = filepath.Dir(p) {
		if err := syscall.Statfs(p, &st); err == nil {
			return int64(st.Bfree) * int64(st.Bsize)
		}
		if p == filepath.Dir(p) {
			return -1
		}
	}
}
