package runtime

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	goruntime "runtime"
	"strconv"
	"strings"

	"github.com/shiptiffin/tiffin/internal/mod/budget"
	"github.com/shiptiffin/tiffin/internal/platform"
)

// Paths on the box. Everything big lives on the XFS data disk.
const (
	DataDir       = "/var/lib/tiffin/runtime"    // deploy records' files: sources, build logs, static roots, git repos
	LogDir        = "/var/lib/tiffin/logs/apps"  // per-app container logs (rotated JSON lines)
	containerdDir = "/var/lib/tiffin/containerd" // containerd root (images, snapshots)
	buildkitDir   = "/var/lib/tiffin/buildkit"   // BuildKit state and build cache
	nerdctlDir    = "/var/lib/tiffin/nerdctl"    // nerdctl container metadata
	swapFile      = "/var/lib/tiffin/swapfile"   // lets `next build` survive a 4 GB box
	versionMarker = "/usr/local/lib/tiffin/nerdctl-full.version"
	buildCgroup   = "tiffin-build" // cgroup (v2) every build container runs in, memory-capped
	// buildPids caps the tasks (processes and threads) of all BuildKit's
	// build steps together, and of a static site's build: a build that
	// forks without end stops there, not at the box's limit.
	buildPids = 4096
	// Namespace is the containerd namespace for app images and containers.
	Namespace = "tiffin"
	// PortMin..PortMax are the localhost ports app instances listen on.
	PortMin = 20000
	PortMax = 29999
)

// bundleMembers are the parts of nerdctl-full we install.
var bundleMembers = []string{
	"bin/containerd", "bin/containerd-shim-runc-v2", "bin/ctr", "bin/runc",
	"bin/nerdctl", "bin/buildkitd", "bin/buildctl", "bin/tini", "libexec/cni",
	"share/doc/nerdctl/README.md",
}

func (m *Module) Provision(ctx context.Context, s *platform.System) error {
	arch := goruntime.GOARCH
	if nerdctlFullSHA256[arch] == "" {
		return fmt.Errorf("no pinned container runtime for %s", arch)
	}
	if err := s.Apt(ctx, "nftables", "git"); err != nil {
		return err
	}
	for _, d := range []string{DataDir, LogDir, containerdDir, buildkitDir, nerdctlDir, "/etc/containerd", "/etc/buildkit", "/etc/nerdctl", "/usr/local/lib/tiffin"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}

	// Container runtime: containerd, runc, BuildKit, nerdctl (one pinned bundle).
	if cur, _ := os.ReadFile(versionMarker); strings.TrimSpace(string(cur)) != NerdctlVersion || !exists("/usr/local/bin/nerdctl") {
		tgz, err := s.Fetch(ctx, nerdctlURL(arch), nerdctlFullSHA256[arch])
		if err != nil {
			return err
		}
		s.Log("installing containerd, runc, BuildKit and nerdctl (nerdctl-full " + NerdctlVersion + ")")
		if _, err := s.Run(ctx, "tar", append([]string{"-xzf", tgz, "-C", "/usr/local"}, bundleMembers...)...); err != nil {
			return err
		}
		if _, err := s.WriteFile(versionMarker, []byte(NerdctlVersion+"\n"), 0o644); err != nil {
			return err
		}
	}
	// Railpack: turns app sources into build plans.
	if out, err := exec.CommandContext(ctx, "/usr/local/bin/railpack", "--version").Output(); err != nil || !strings.Contains(string(out), RailpackVersion) {
		tgz, err := s.Fetch(ctx, railpackURL(arch), railpackSHA256[arch])
		if err != nil {
			return err
		}
		s.Log("installing Railpack " + RailpackVersion)
		if _, err := s.Run(ctx, "tar", "-xzf", tgz, "-C", "/usr/local/bin", "railpack"); err != nil {
			return err
		}
	}

	mem := memTotalMB()
	if err := ensureSwap(ctx, s, mem); err != nil {
		return err
	}
	// A changed config file restarts its service (containerd restarts keep
	// containers running: KillMode=process).
	units := []struct{ unit, content, conf, confBody string }{
		{"containerd.service", containerdUnit, "/etc/containerd/config.toml", containerdConfig},
		{"buildkit.service", buildkitUnit(buildMemoryMB(mem)), "/etc/buildkit/buildkitd.toml", buildkitConfig(buildCacheCap(diskBytes(buildkitDir)))},
		{"tiffin-runtime-firewall.service", firewallUnit, "/etc/tiffin/runtime.nft", firewallRules},
	}
	if _, err := s.WriteFile("/etc/nerdctl/nerdctl.toml", []byte(nerdctlConfig), 0o644); err != nil {
		return err
	}
	for _, u := range units {
		changed, err := s.WriteFile(u.conf, []byte(u.confBody), 0o644)
		if err != nil {
			return err
		}
		if err := s.Unit(ctx, u.unit, u.content); err != nil {
			return err
		}
		if changed {
			if _, err := s.Run(ctx, "systemctl", "restart", u.unit); err != nil {
				return err
			}
		}
	}
	_, err := s.Run(ctx, "/usr/local/bin/buildctl", "--timeout", "30", "debug", "workers")
	return err
}

// buildMemoryMB is the memory cap for all build containers together: what
// is left after the platform, data services and running apps get ~1.25 GB.
// Swap is allowed beyond it, so big builds slow down instead of dying.
func buildMemoryMB(totalMB int) int {
	return max(1024, totalMB-1280)
}

func memTotalMB() int {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 4096
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) >= 2 && f[0] == "MemTotal:" {
			kb, _ := strconv.Atoi(f[1])
			return kb / 1024
		}
	}
	return 4096
}

// ensureSwap adds a 2 GiB swap file on the data disk to boxes under 8 GB
// without swap, so a `next build` spike swaps instead of being OOM-killed.
func ensureSwap(ctx context.Context, s *platform.System, memMB int) error {
	if memMB >= 8192 {
		return nil
	}
	if raw, err := os.ReadFile("/proc/swaps"); err == nil && len(strings.Split(strings.TrimSpace(string(raw)), "\n")) > 1 {
		return nil // some swap is already active
	}
	s.Log("adding a 2 GiB swap file for builds")
	_, err := s.Sh(ctx, fmt.Sprintf(`f=%s
[ -f "$f" ] || { dd if=/dev/zero of="$f.tmp" bs=1M count=2048 status=none && chmod 600 "$f.tmp" && mkswap "$f.tmp" >/dev/null && mv "$f.tmp" "$f"; }
swapon "$f" 2>/dev/null || true
grep -q "^$f " /etc/fstab || echo "$f none swap sw 0 0" >> /etc/fstab`, swapFile))
	return err
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

const containerdConfig = `# Managed by tiffin (runtime module). Images and snapshots live on the data disk.
version = 3
root = "` + containerdDir + `"
state = "/run/containerd"
`

// buildCacheCap is how much BuildKit's cache may hold: 15% of the data
// disk, at least 4 GiB and at most 20 GiB (an unknown disk: 4 GiB).
func buildCacheCap(disk int64) int64 {
	return min(max(disk*15/100, 4<<30), 20<<30)
}

// buildkitConfig is buildkitd.toml with the cache capped at limit bytes.
// Sources, cache mounts and git checkouts unused for a week go first and
// may take half of it; with less than 15% of the disk free, the cache
// shrinks further. BuildKit reads "MB" as MiB here.
//
// No build history: BuildKit's default keeps the last 50 builds' records
// (removing one only once it is past both 50 entries and 48 hours), and a
// record leases its build's result, image layers included, so neither an
// image sweep nor a destroyed project freed that disk. The box keeps its
// own build logs. maxEntries = 0 records none; maxAge removes records an
// older config kept.
func buildkitConfig(limit int64) string {
	mib := func(n int64) string { return strconv.FormatInt(n>>20, 10) + "MB" }
	return `# Managed by tiffin (runtime module). The cache cap follows the data disk (buildCacheCap).
root = "` + buildkitDir + `"

# No build history: its records pin each build's image layers on disk.
[history]
  maxAge = "1h"
  maxEntries = 0

[worker.oci]
  enabled = false

[worker.containerd]
  enabled = true
  namespace = "` + Namespace + `"
  # Every build container runs inside one memory-capped cgroup (see buildkit.service).
  defaultCgroupParent = "` + buildCgroup + `"
  max-parallelism = 2
  gc = true

  [[worker.containerd.gcpolicy]]
    keepDuration = "168h"
    maxUsedSpace = "` + mib(limit/2) + `"
    filters = ["type==source.local", "type==exec.cachemount", "type==source.git.checkout"]

  [[worker.containerd.gcpolicy]]
    all = true
    maxUsedSpace = "` + mib(limit) + `"
    minFreeSpace = "15%"
`
}

const nerdctlConfig = `# Managed by tiffin (runtime module).
namespace = "` + Namespace + `"
data_root = "` + nerdctlDir + `"
`

const containerdUnit = `# Managed by tiffin (runtime module). containerd ` + "from nerdctl-full " + NerdctlVersion + `.
[Unit]
Description=containerd container runtime (tiffin apps)
Documentation=https://containerd.io
After=network.target local-fs.target
RequiresMountsFor=/var/lib/tiffin

[Service]
ExecStartPre=-/sbin/modprobe overlay
ExecStart=/usr/local/bin/containerd --config /etc/containerd/config.toml
Type=notify
Delegate=yes
KillMode=process
Restart=always
RestartSec=5
LimitNPROC=infinity
LimitCORE=infinity
TasksMax=infinity
OOMScoreAdjust=-999

[Install]
WantedBy=multi-user.target
`

func buildkitUnit(capMB int) string {
	return `# Managed by tiffin (runtime module). BuildKit from nerdctl-full ` + NerdctlVersion + `.
[Unit]
Description=BuildKit (tiffin app builds)
After=containerd.service
Requires=containerd.service
RequiresMountsFor=/var/lib/tiffin

[Service]
# Build containers share one cgroup capped at ` + strconv.Itoa(capMB) + ` MiB (swap allowed beyond it)
# and ` + strconv.Itoa(buildPids) + ` tasks.
ExecStartPre=/bin/sh -c 'mkdir -p /sys/fs/cgroup/` + buildCgroup + ` && echo ` + strconv.Itoa(capMB) + `M > /sys/fs/cgroup/` + buildCgroup + `/memory.max && echo max > /sys/fs/cgroup/` + buildCgroup + `/memory.swap.max && echo ` + strconv.Itoa(buildPids) + ` > /sys/fs/cgroup/` + buildCgroup + `/pids.max'
ExecStart=/usr/local/bin/buildkitd --config /etc/buildkit/buildkitd.toml
Type=notify
Delegate=yes
KillMode=process
Restart=always
RestartSec=5
LimitNPROC=infinity
LimitCORE=infinity
TasksMax=infinity
Nice=10

[Install]
WantedBy=multi-user.target
`
}

// firewallRules keep app instance ports (which listen on all interfaces with
// host networking) reachable only from the box itself; the edge is the way
// in. And apps send mail through the box (SMTP_URL on 127.0.0.1:2525, which
// relays it), never straight to other servers' port 25: an app's containers
// run in the projects' slice, so connections from that cgroup to port 25
// off the box are refused. nft resolves the slice's cgroup when it loads the
// rules, so the unit starts the slice first.
const firewallRules = `# Managed by tiffin (runtime module).
table inet tiffin_runtime
delete table inet tiffin_runtime
table inet tiffin_runtime {
	chain input {
		type filter hook input priority filter; policy accept;
		iifname != "lo" tcp dport ` + "20000-29999" + ` drop
	}
	chain output {
		type filter hook output priority filter; policy accept;
		oifname != "lo" tcp dport 25 socket cgroupv2 level 2 "` + appsCgroup + `" reject with tcp reset
	}
}
`

// appsCgroup is the cgroup (v2, under the root) every app container runs
// in: budget.ParentSlice, which systemd nests in tiffin.slice.
const appsCgroup = "tiffin.slice/" + budget.ParentSlice

const firewallUnit = `# Managed by tiffin (runtime module).
[Unit]
Description=tiffin: app ports are reachable from the box only; apps send mail through the box
Wants=` + budget.ParentSlice + `
After=network.target ` + budget.ParentSlice + `

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/sbin/nft -f /etc/tiffin/runtime.nft
ExecStop=/usr/sbin/nft delete table inet tiffin_runtime

[Install]
WantedBy=multi-user.target
`
