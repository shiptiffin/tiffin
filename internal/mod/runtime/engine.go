package runtime

import (
	"bytes"
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
	"time"
)

// RunSpec describes one app instance container.
type RunSpec struct {
	Name     string
	Image    string
	Port     int
	MemoryMB int
	Env      map[string]string
	Labels   map[string]string
	LogPath  string
	Mounts   []string // host:container[:ro]
	// CgroupParent is the systemd slice the container runs in (its
	// project's budget); "" leaves nerdctl's default.
	CgroupParent string
	// Command, when set, runs instead of the image's entrypoint and
	// command, with /bin/sh -c (a Dockerfile or prebuilt image whose app
	// sets command).
	Command string
}

// Container is what the engine reports about one container.
type Container struct {
	Name     string            `json:"name"`
	Running  bool              `json:"running"`
	Status   string            `json:"status"`
	ExitCode int               `json:"exitCode,omitempty"` // of its last run, once it exited
	Labels   map[string]string `json:"labels,omitempty"`
}

// Image is one name in the image store.
type Image struct {
	Name    string    // as stored: docker.io/tiffin/<project>-<app>:<deploy>, docker.io/oven/bun:..., import@sha256:...
	Digest  string    // what it points at (index or manifest)
	Created time.Time // when this box stored the name (zero: unknown)
	Size    int64     // unpacked size, layers it shares with other images included
}

// Engine runs app containers. The box uses nerdctl over containerd; tests
// use a fake that serves HTTP in-process.
type Engine interface {
	Run(ctx context.Context, spec RunSpec) error
	// RunTask runs script (/bin/sh -c) once in a new container of the
	// spec's image, waits for it and returns its exit code; output goes to
	// log. The spec's port, restart policy and log file do not apply.
	RunTask(ctx context.Context, spec RunSpec, script string, log io.Writer) (int, error)
	// Remove stops (SIGTERM, then SIGKILL after grace) and deletes a container.
	Remove(ctx context.Context, name string, grace time.Duration) error
	// Stop stops a container the same way but keeps it, filesystem and
	// config included, for Start (a sleeping app's instances).
	Stop(ctx context.Context, name string, grace time.Duration) error
	// Start starts a stopped container again, with the config it was
	// created with.
	Start(ctx context.Context, name string) error
	Inspect(ctx context.Context, name string) (*Container, error) // nil if missing
	List(ctx context.Context) ([]Container, error)                // tiffin app containers
	ImageDigest(ctx context.Context, ref string) (string, error)
	// RemoveImage deletes one image name (the content goes once no other
	// name holds it). It refuses an image a container was created from.
	RemoveImage(ctx context.Context, ref string) error
	// Images lists every image name in the namespace, as stored.
	Images(ctx context.Context) ([]Image, error)
	// UsedImages names the images the namespace's containers (running or
	// not, app instances or anything else) were created from.
	UsedImages(ctx context.Context) (map[string]bool, error)
	// LoadImage imports a docker/OCI image tarball whose every image is
	// named ref (see loadImage) and makes sure ref is the one name it left
	// in the store.
	LoadImage(ctx context.Context, tarball io.Reader, ref string, log io.Writer) error
	// TagImage gives an image in the store another name (no layers copied).
	// src is a name, read the way nerdctl reads one (dockerName).
	TagImage(ctx context.Context, src, ref string) error
	// CopyOut copies directories of an image (relative to its working
	// directory) to dest/0, dest/1, ... in order, as plain files and
	// directories (links followed inside the image); missing ones are skipped.
	CopyOut(ctx context.Context, image string, dirs []string, dest string) error
	// ImageConfig reports the working directory and user an image runs with.
	ImageConfig(ctx context.Context, ref string) (workDir, user string, err error)
	// SeedDirs makes dest/0, dest/1, ... from directories of an image
	// (relative to its working directory) as `cp -a` copies them, or empty
	// where the image has none, owned by user ("": root).
	SeedDirs(ctx context.Context, image, user string, dirs []string, dest string) error
}

// nerdctl drives containerd through the pinned nerdctl CLI.
type nerdctl struct {
	bin string
}

func newNerdctl() *nerdctl { return &nerdctl{bin: "/usr/local/bin/nerdctl"} }

func (n *nerdctl) cmd(ctx context.Context, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, n.bin, append([]string{"--namespace", Namespace}, args...)...)
	c.Env = append(toolEnv(), "CONTAINERD_NAMESPACE="+Namespace)
	return c
}

// toolHome is HOME for the CLIs the runtime drives: the service runs with
// ProtectHome, and buildctl/nerdctl keep registry auth config under HOME.
var toolHome = filepath.Join(DataDir, "home")

func toolEnv() []string {
	_ = os.MkdirAll(filepath.Join(toolHome, ".docker"), 0o700)
	return append(os.Environ(), "HOME="+toolHome, "DOCKER_CONFIG="+filepath.Join(toolHome, ".docker"))
}

func (n *nerdctl) run(ctx context.Context, args ...string) (string, error) {
	var out bytes.Buffer
	c := n.cmd(ctx, args...)
	c.Stdout, c.Stderr = &out, &out
	if err := c.Run(); err != nil {
		return out.String(), fmt.Errorf("nerdctl %s: %w: %s", args[0], err, strings.TrimSpace(lastLines(out.String(), 5)))
	}
	return out.String(), nil
}

func (n *nerdctl) Run(ctx context.Context, s RunSpec) error {
	args := []string{"run", "--detach", "--name", s.Name,
		"--network", "host", // apps reach box services on localhost; the firewall keeps app ports local
		"--init", // tini as PID 1: signals reach the app, zombies are reaped
		"--restart", "unless-stopped",
		"--stop-timeout", "10",
		"--log-driver", "json-file",
		"--log-opt", "log-path=" + s.LogPath,
		"--log-opt", "max-size=5m",
		"--log-opt", "max-file=3",
	}
	if s.Command != "" {
		args = append(args, "--entrypoint", "/bin/sh")
	}
	c := n.withSpec(ctx, args, s)
	c.Args = append(c.Args, s.Image)
	if s.Command != "" {
		c.Args = append(c.Args, "-c", execLast(s.Command)) // exec: the app gets tini's signals
	}
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	if err := c.Run(); err != nil {
		return fmt.Errorf("start container %s: %w: %s", s.Name, err, strings.TrimSpace(lastLines(out.String(), 5)))
	}
	return nil
}

// withSpec is a nerdctl command of args plus the spec's memory cap, cgroup,
// labels, mounts and env. The env travels as --env NAME=VALUE arguments,
// never in nerdctl's own environment: nerdctl runs as root on the host and
// reads variables such as DOCKER_CONFIG, NERDCTL_TOML or
// CONTAINERD_NAMESPACE, and hands its environment to the OCI hooks runc
// runs on the host. The caller appends the image and what follows it.
func (n *nerdctl) withSpec(ctx context.Context, args []string, s RunSpec) *exec.Cmd {
	if s.MemoryMB > 0 {
		args = append(args, "--memory", strconv.Itoa(s.MemoryMB)+"m", "--memory-swap", strconv.Itoa(s.MemoryMB)+"m")
	}
	if s.CgroupParent != "" {
		args = append(args, "--cgroup-parent", s.CgroupParent)
	}
	keys := make([]string, 0, len(s.Labels))
	for k := range s.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--label", k+"="+s.Labels[k])
	}
	for _, m := range s.Mounts {
		args = append(args, "--volume", m)
	}
	c := n.cmd(ctx)
	envKeys := make([]string, 0, len(s.Env))
	for k := range s.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		args = append(args, "--env", k+"="+s.Env[k])
	}
	c.Args = append(c.Args, args...)
	return c
}

// RunTask runs script with /bin/sh in a new container of the spec's image
// and waits for it, writing its output to log. It reports the script's exit
// code. A task cut short by ctx is removed.
func (n *nerdctl) RunTask(ctx context.Context, s RunSpec, script string, log io.Writer) (int, error) {
	c := n.withSpec(ctx, []string{"run", "--rm", "--name", s.Name, "--network", "host", "--init", "--entrypoint", "/bin/sh"}, s)
	c.Args = append(c.Args, s.Image, "-c", script)
	c.Stdout, c.Stderr = log, log
	err := c.Run()
	if ctx.Err() != nil {
		rctx, cancel := cleanupContext(ctx)
		defer cancel()
		_ = n.Remove(rctx, s.Name, time.Second)
		return -1, ctx.Err()
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	return 0, err
}

// Remove stops and deletes a container. An inspect that fails is no proof
// the container is gone, so removal goes ahead anyway; rm is retried, since
// containerd's restart monitor can restart a crashing container under it.
func (n *nerdctl) Remove(ctx context.Context, name string, grace time.Duration) error {
	if c, err := n.Inspect(ctx, name); err == nil && c == nil {
		return nil
	}
	secs := strconv.Itoa(max(1, int(grace.Seconds())))
	_, _ = n.run(ctx, "stop", "--time", secs, name)
	var err error
	for try := 0; try < 3; try++ {
		if _, err = n.run(ctx, "rm", "--force", name); err == nil || strings.Contains(err.Error(), "no such container") {
			return nil
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Second):
		}
	}
	return err
}

// Stop stops a container and keeps it. Its restart policy (unless-stopped)
// leaves it stopped, across reboots too, until Start.
func (n *nerdctl) Stop(ctx context.Context, name string, grace time.Duration) error {
	_, err := n.run(ctx, "stop", "--time", strconv.Itoa(max(1, int(grace.Seconds()))), name)
	return err
}

// Start starts a stopped container: no image unpack, snapshot or container
// to create, which halves a wake's start on a small box (about 0.35 s
// instead of 0.65 s before the app's own boot).
func (n *nerdctl) Start(ctx context.Context, name string) error {
	_, err := n.run(ctx, "start", name)
	return err
}

type nerdctlInspect struct {
	Name  string `json:"Name"`
	State struct {
		Status   string `json:"Status"`
		Running  bool   `json:"Running"`
		ExitCode int    `json:"ExitCode"`
	} `json:"State"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

func (n *nerdctl) Inspect(ctx context.Context, name string) (*Container, error) {
	out, err := n.run(ctx, "container", "inspect", name)
	if err != nil {
		if strings.Contains(out, "no such") || strings.Contains(err.Error(), "no such") {
			return nil, nil
		}
		return nil, err
	}
	var res []nerdctlInspect
	if err := json.Unmarshal([]byte(out), &res); err != nil || len(res) == 0 {
		return nil, nil
	}
	r := res[0]
	return &Container{Name: strings.TrimPrefix(r.Name, "/"), Running: r.State.Running, Status: r.State.Status, ExitCode: r.State.ExitCode, Labels: r.Config.Labels}, nil
}

func (n *nerdctl) List(ctx context.Context) ([]Container, error) {
	out, err := n.run(ctx, "ps", "--all", "--filter", "label=tiffin.app", "--format", "{{.Names}}")
	if err != nil {
		return nil, err
	}
	var cs []Container
	for _, name := range strings.Fields(out) {
		if c, err := n.Inspect(ctx, name); err == nil && c != nil {
			cs = append(cs, *c)
		}
	}
	return cs, nil
}

func (n *nerdctl) ImageDigest(ctx context.Context, ref string) (string, error) {
	out, err := n.run(ctx, "image", "inspect", "--format", "{{json .RepoDigests}}", ref)
	if err != nil {
		return "", err
	}
	var ds []string
	if json.Unmarshal([]byte(strings.TrimSpace(out)), &ds) == nil {
		for _, d := range ds {
			if _, dg, ok := strings.Cut(d, "@"); ok {
				return dg, nil
			}
		}
	}
	// Fall back to the image ID (config digest).
	id, err := n.run(ctx, "image", "inspect", "--format", "{{.ID}}", ref)
	return strings.TrimSpace(id), err
}

// RemoveImage deletes a name with nerdctl rmi, which refuses an image a
// container (running or stopped) uses and waits for containerd's garbage
// collection. nerdctl cannot address a digest name a load stored
// ("import@sha256:..."): those go through ctr, which deletes the name
// exactly as stored, after the same check that no container uses it.
func (n *nerdctl) RemoveImage(ctx context.Context, ref string) error {
	if !isLoadDigestName(ref) {
		_, err := n.run(ctx, "rmi", ref)
		return err
	}
	used, err := n.UsedImages(ctx)
	if err != nil {
		return err
	}
	if used[ref] {
		return fmt.Errorf("image %s is used by a container", ref)
	}
	var out bytes.Buffer
	c := exec.CommandContext(ctx, ctrBin, "--namespace", Namespace, "images", "rm", "--sync", ref)
	c.Stdout, c.Stderr = &out, &out
	if err := c.Run(); err != nil {
		return fmt.Errorf("ctr images rm %s: %w: %s", ref, err, strings.TrimSpace(lastLines(out.String(), 3)))
	}
	return nil
}

// ctrBin is containerd's own CLI (from the same nerdctl-full bundle).
const ctrBin = "/usr/local/bin/ctr"

// Images lists the namespace's image names (nerdctl image ls, one JSON
// object per line). Sizes come as nerdctl prints them ("544.5MB").
func (n *nerdctl) Images(ctx context.Context) ([]Image, error) {
	out, err := n.run(ctx, "image", "ls", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	return parseImageList(out), nil
}

func parseImageList(out string) []Image {
	var imgs []Image
	for _, line := range strings.Split(out, "\n") {
		var row struct{ Name, Digest, CreatedAt, Size string }
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &row) != nil || row.Name == "" {
			continue
		}
		created, _ := time.Parse("2006-01-02 15:04:05 -0700 MST", row.CreatedAt)
		imgs = append(imgs, Image{Name: row.Name, Digest: row.Digest, Created: created, Size: parseSize(row.Size)})
	}
	return imgs
}

// UsedImages reads the image of every container in the namespace.
func (n *nerdctl) UsedImages(ctx context.Context) (map[string]bool, error) {
	out, err := n.run(ctx, "ps", "--all", "--format", "{{.Image}}")
	if err != nil {
		return nil, err
	}
	used := map[string]bool{}
	for _, name := range strings.Fields(out) {
		used[name] = true
	}
	return used, nil
}

// parseSize reads a size as nerdctl and buildctl print them: decimal
// units (go-units HumanSize: "544.5MB", "1.551GB", "0B") or binary ones
// ("175.3MiB"). Unreadable: 0.
func parseSize(s string) int64 {
	s = strings.TrimSpace(s)
	i := strings.IndexFunc(s, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
	if i <= 0 {
		return 0
	}
	v, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0
	}
	mult := map[string]float64{"B": 1, "kB": 1e3, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12,
		"KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40}[strings.TrimSpace(s[i:])]
	return int64(v * mult)
}

func (n *nerdctl) TagImage(ctx context.Context, src, ref string) error {
	_, err := n.run(ctx, "tag", src, ref)
	return err
}

// CopyOut runs cp in a throwaway container of the image, without network.
// The directories travel as arguments, never inside the script.
func (n *nerdctl) CopyOut(ctx context.Context, image string, dirs []string, dest string) error {
	const script = `i=0; for d in "$@"; do if [ -d "$d" ]; then mkdir -p /tiffin-out/$i && cp -RL "$d"/. /tiffin-out/$i/ || exit 1; fi; i=$((i+1)); done`
	args := append([]string{"run", "--rm", "--network", "none", "--user", "0:0", "--memory", "256m",
		"--volume", dest + ":/tiffin-out", "--entrypoint", "/bin/sh", image, "-c", script, "sh"}, dirs...)
	_, err := n.run(ctx, args...)
	return err
}

func (n *nerdctl) ImageConfig(ctx context.Context, ref string) (string, string, error) {
	out, err := n.run(ctx, "image", "inspect", "--format", "{{json .Config}}", ref)
	if err != nil {
		return "", "", err
	}
	var c struct{ WorkingDir, User string }
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &c); err != nil {
		return "", "", fmt.Errorf("image %s: %w", ref, err)
	}
	return c.WorkingDir, c.User, nil
}

// SeedDirs runs cp in a throwaway container of the image as root, without
// network, then hands the copies to the image's user. The directories and
// the user travel as arguments, never inside the script.
func (n *nerdctl) SeedDirs(ctx context.Context, image, user string, dirs []string, dest string) error {
	const script = `u="$1"; shift; i=0; for d in "$@"; do mkdir -p /tiffin-out/$i || exit 1; ` +
		`if [ -d "$d" ]; then cp -a "$d"/. /tiffin-out/$i/ || exit 1; fi; ` +
		`if [ -n "$u" ]; then chown -R "$u" /tiffin-out/$i || exit 1; fi; i=$((i+1)); done`
	args := append([]string{"run", "--rm", "--network", "none", "--user", "0:0", "--memory", "256m",
		"--volume", dest + ":/tiffin-out", "--entrypoint", "/bin/sh", image, "-c", script, "sh", user}, dirs...)
	_, err := n.run(ctx, args...)
	return err
}

// LoadImage reads an image tarball whose every image is named ref (see
// loadImage), then checks what the load stored: ref, plus the digest name
// nerdctl gives the tarball's index, which is dropped (settleLoad).
func (n *nerdctl) LoadImage(ctx context.Context, tarball io.Reader, ref string, log io.Writer) error {
	c := n.cmd(ctx, "load")
	c.Stdin = tarball
	var out bytes.Buffer
	c.Stdout = io.MultiWriter(&out, log)
	c.Stderr = io.MultiWriter(&out, log)
	if err := c.Run(); err != nil {
		return fmt.Errorf("load image: %w", err)
	}
	has := func(name string) bool {
		_, err := n.run(ctx, "image", "inspect", "--format", "{{.ID}}", name)
		return err == nil
	}
	// Through RemoveImage: nerdctl rmi cannot address the digest name.
	remove := func(name string) error { return n.RemoveImage(ctx, name) }
	if err := settleLoad(loadedNames(out.String()), ref, has, remove, log); err != nil {
		return fmt.Errorf("%w (nerdctl load said: %s)", err, strings.TrimSpace(lastLines(out.String(), 3)))
	}
	return nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
