// Package base provisions what every box needs before any service:
// common packages, the data-disk layout and sane kernel settings.
package base

import (
	"context"
	"os"

	"github.com/btahir/tiffin/internal/platform"
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
	_, err = s.Run(ctx, "sysctl", "--system")
	return err
}
