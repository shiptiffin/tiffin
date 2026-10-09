// Package base provisions what every box needs before any service:
// common packages, the data-disk layout and sane kernel settings.
package base

import (
	"context"
	"os"
	"strings"

	"github.com/shiptiffin/tiffin/internal/platform"
)

func init() { platform.Register(&Module{}) }

// Module is the base module.
type Module struct{}

func (*Module) Name() string { return "base" }
func (*Module) Order() int   { return 0 }

// Dirs on the data disk every module can rely on.
var Dirs = []string{
	"/var/lib/tiffin/cache",
	"/var/lib/tiffin/backups",
	"/var/lib/tiffin/apps",
	"/var/lib/tiffin/builds",
	"/var/lib/tiffin/logs",
}

func (*Module) Provision(ctx context.Context, s *platform.System) error {
	if err := fixDataMount(ctx, s); err != nil {
		return err
	}
	if err := s.Apt(ctx, "ca-certificates", "curl", "gnupg", "git", "jq", "xfsprogs", "tar", "gzip", "unzip"); err != nil {
		return err
	}
	for _, d := range Dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	// Containers and many connections need more than the defaults.
	_, err := s.WriteFile("/etc/sysctl.d/90-tiffin.conf", []byte(
		"fs.inotify.max_user_watches=524288\nfs.inotify.max_user_instances=1024\nnet.core.somaxconn=4096\nvm.overcommit_memory=1\nnet.ipv4.ip_forward=1\n"), 0o644)
	if err != nil {
		return err
	}
	if _, err = s.Run(ctx, "sysctl", "--system"); err != nil {
		return err
	}
	if _, err := s.WriteFile(needrestartFile, []byte(needrestartConfig), 0o644); err != nil {
		return err
	}
	srv, err := platform.LoadServerConfig()
	if err != nil {
		return err
	}
	if srv != nil {
		return harden(ctx, s, srv)
	}
	return nil
}

// needrestart (on Ubuntu servers by default) restarts services after
// unattended upgrades replace their libraries. Never the box's own: a bare
// restart of Postgres fails every query in flight, and the box restarts
// them itself, in order, with the pooler paused (`tiffin maintenance`).
const (
	needrestartFile   = "/etc/needrestart/conf.d/50-tiffin.conf"
	needrestartConfig = `# Managed by tiffin provision: the box restarts its own services (tiffin maintenance show).
$nrconf{override_rc}->{qr(^tiffin)} = 0;
$nrconf{override_rc}->{qr(^valkey)} = 0;
$nrconf{override_rc}->{qr(^containerd)} = 0;
$nrconf{override_rc}->{qr(^buildkit)} = 0;
`
)

// Checks reports the server hardening (real servers only).
func (*Module) Checks(ctx context.Context, _ *platform.Platform) []platform.Check {
	srv, err := platform.LoadServerConfig()
	if err != nil {
		return []platform.Check{{Name: "server", OK: false, Detail: err.Error()}}
	}
	if srv == nil {
		return nil
	}
	return serverChecks(ctx, srv)
}

// fixDataMount migrates boxes made before the data disk was mounted by
// label: their fstab bind-mounted Lima's /mnt/lima-<disk>, which Lima mounts
// only after boot, so after a reboot services saw an empty /var/lib/tiffin.
func fixDataMount(ctx context.Context, s *platform.System) error {
	raw, err := os.ReadFile("/etc/fstab")
	if err != nil {
		return nil // not a Lima-style box
	}
	var out []string
	changed := false
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		f := strings.Fields(line)
		if len(f) >= 4 && f[1] == "/var/lib/tiffin" && f[2] == "none" && strings.Contains(f[3], "bind") && strings.HasPrefix(f[0], "/mnt/lima-") {
			label := strings.TrimPrefix(f[0], "/mnt/")
			out = append(out, "LABEL="+label+" /var/lib/tiffin xfs defaults,nofail,x-systemd.device-timeout=60s 0 2")
			changed = true
			continue
		}
		out = append(out, line)
	}
	if !changed {
		return nil
	}
	s.Log("mounting the data disk by label at boot")
	if _, err := s.WriteFile("/etc/fstab", []byte(strings.Join(out, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	_, err = s.Run(ctx, "systemctl", "daemon-reload")
	return err
}
