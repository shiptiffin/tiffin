package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
}

// Container is what the engine reports about one container.
type Container struct {
	Name     string            `json:"name"`
	Running  bool              `json:"running"`
	Status   string            `json:"status"`
	ExitCode int               `json:"exitCode,omitempty"` // of its last run, once it exited
	Labels   map[string]string `json:"labels,omitempty"`
}

// Engine runs app containers. The box uses nerdctl over containerd; tests
// use a fake that serves HTTP in-process.
type Engine interface {
	Run(ctx context.Context, spec RunSpec) error
	// Remove stops (SIGTERM, then SIGKILL after grace) and deletes a container.
	Remove(ctx context.Context, name string, grace time.Duration) error
	Inspect(ctx context.Context, name string) (*Container, error) // nil if missing
	List(ctx context.Context) ([]Container, error)                // tiffin app containers
	ImageDigest(ctx context.Context, ref string) (string, error)
	RemoveImage(ctx context.Context, ref string) error
	// LoadImage imports the one image of a docker/OCI image tarball whose
	// names were stripped (see loadImage) and names it ref, by digest.
	LoadImage(ctx context.Context, tarball io.Reader, ref string, log io.Writer) error
	// TagImage gives an image in the store another name (no layers copied).
	TagImage(ctx context.Context, src, ref string) error
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
	// Values travel in nerdctl's own environment (`-e NAME` reads it), never
	// on the command line where `ps` would show them.
	c := n.cmd(ctx)
	envKeys := make([]string, 0, len(s.Env))
	for k := range s.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		args = append(args, "--env", k)
		c.Env = append(c.Env, k+"="+s.Env[k])
	}
	args = append(args, s.Image)
	c.Args = append(c.Args, args...)
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	if err := c.Run(); err != nil {
		return fmt.Errorf("start container %s: %w: %s", s.Name, err, strings.TrimSpace(lastLines(out.String(), 5)))
	}
	return nil
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

func (n *nerdctl) RemoveImage(ctx context.Context, ref string) error {
	_, err := n.run(ctx, "rmi", ref)
	return err
}

func (n *nerdctl) TagImage(ctx context.Context, src, ref string) error {
	_, err := n.run(ctx, "tag", src, ref)
	return err
}

// LoadImage reads an image tarball with its names stripped (see loadImage),
// so containerd knows the image only by digest (import@sha256:...). It is
// named ref by that digest, and the digest name is dropped.
func (n *nerdctl) LoadImage(ctx context.Context, tarball io.Reader, ref string, log io.Writer) error {
	c := n.cmd(ctx, "load")
	c.Stdin = tarball
	var out bytes.Buffer
	c.Stdout = io.MultiWriter(&out, log)
	c.Stderr = io.MultiWriter(&out, log)
	if err := c.Run(); err != nil {
		return fmt.Errorf("load image: %w", err)
	}
	names, err := loadedImages(out.String())
	if err != nil {
		return err
	}
	drop := func() {
		for _, name := range names {
			if _, err := n.run(ctx, "rmi", name); err != nil {
				fmt.Fprintf(log, "could not drop the loaded name %s: %v\n", name, err)
			}
		}
	}
	switch {
	case len(names) == 0:
		return fmt.Errorf("the tarball contained no image (nerdctl load said: %s)", strings.TrimSpace(lastLines(out.String(), 3)))
	case len(names) > 1:
		drop()
		return fmt.Errorf("the tarball holds %d images; save only the app's image", len(names))
	}
	id := strings.TrimPrefix(names[0], loadedPrefix)[:12]
	if _, err := n.run(ctx, "tag", id, ref); err != nil {
		return err
	}
	drop()
	fmt.Fprintf(log, "loaded image %s as %s\n", id, ref)
	return nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// loadedPrefix is how nerdctl load names an image the tarball does not.
const loadedPrefix = "import@sha256:"

// loadedImages lists the images nerdctl load imported, by their digest
// names. Any other name means the tarball still named an image, which a
// stripped one cannot: it is refused.
func loadedImages(out string) ([]string, error) {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		_, name, ok := strings.Cut(line, "Loaded image: ")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if dg, ok := strings.CutPrefix(name, loadedPrefix); !ok || len(dg) < 12 {
			return nil, fmt.Errorf("the tarball named an image (%s); images are loaded by digest only", name)
		}
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names, nil
}
