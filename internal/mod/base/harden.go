package base

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/btahir/tiffin/internal/platform"
)

// Server hardening runs only on real servers (`tiffin up --provider hetzner`
// or `--provider ssh` write /etc/tiffin/server.json). A local VM is reached
// only from this computer, so it is left as Lima made it.
//
// The firewall (nftables, default-deny inbound except 22/80/443) and
// CrowdSec (which also bans SSH brute force on servers) live in the protect
// module; this is the rest of a sane Ubuntu server.

// Files the hardening manages.
const (
	autoUpgradesFile = "/etc/apt/apt.conf.d/20auto-upgrades"
	unattendedFile   = "/etc/apt/apt.conf.d/52tiffin-unattended-upgrades"
	sshdDropIn       = "/etc/ssh/sshd_config.d/10-tiffin.conf"
	journaldDropIn   = "/etc/systemd/journald.conf.d/50-tiffin.conf"
	swapFile         = "/swapfile"
	swapSysctl       = "/etc/sysctl.d/92-tiffin-swap.conf"
	rebootRequired   = "/var/run/reboot-required"
)

const autoUpgrades = `// Managed by tiffin provision: refresh the package lists and install
// security updates every day.
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APT::Periodic::AutocleanInterval "7";
`

// unattendedConfig keeps Ubuntu's default origins (security updates only)
// and sets the reboot policy.
func unattendedConfig(window string) string {
	reboot := `Unattended-Upgrade::Automatic-Reboot "false";
`
	if window != "" {
		reboot = fmt.Sprintf(`Unattended-Upgrade::Automatic-Reboot "true";
Unattended-Upgrade::Automatic-Reboot-WithUsers "true";
Unattended-Upgrade::Automatic-Reboot-Time %q;
`, window)
	}
	return `// Managed by tiffin provision (set the window with: tiffin up --reboot-window 04:00).
// Security updates install daily. When one needs a reboot (a new kernel),
// it happens only in the reboot window; with none, status says a reboot is waiting.
Unattended-Upgrade::Remove-Unused-Kernel-Packages "true";
Unattended-Upgrade::Remove-Unused-Dependencies "true";
` + reboot
}

const sshdConfig = `# Managed by tiffin provision: keys only.
# Ubuntu reads the first value it finds, and this file comes first.
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitEmptyPasswords no
PermitRootLogin prohibit-password
MaxAuthTries 4
LoginGraceTime 30
X11Forwarding no
`

const journaldConfig = `# Managed by tiffin provision: keep logs from filling the disk.
[Journal]
SystemMaxUse=1G
SystemKeepFree=2G
MaxRetentionSec=1month
`

func harden(ctx context.Context, s *platform.System, c *platform.ServerConfig) error {
	s.Log("hardening the server (" + c.Provider + ")")
	steps := []struct {
		name string
		fn   func(context.Context, *platform.System, *platform.ServerConfig) error
	}{
		{"security updates", hardenUpdates},
		{"ssh", hardenSSH},
		{"time sync", hardenTime},
		{"swap", hardenSwap},
		{"journald", hardenJournald},
	}
	var errs []string
	for _, st := range steps {
		if err := st.fn(ctx, s, c); err != nil {
			errs = append(errs, st.name+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("server hardening: %s", strings.Join(errs, "; "))
	}
	return nil
}

func hardenUpdates(ctx context.Context, s *platform.System, c *platform.ServerConfig) error {
	if err := s.Apt(ctx, "unattended-upgrades"); err != nil {
		return err
	}
	if _, err := s.WriteFile(autoUpgradesFile, []byte(autoUpgrades), 0o644); err != nil {
		return err
	}
	if _, err := s.WriteFile(unattendedFile, []byte(unattendedConfig(c.RebootWindow)), 0o644); err != nil {
		return err
	}
	_, err := s.Run(ctx, "systemctl", "enable", "--now", "unattended-upgrades.service", "apt-daily.timer", "apt-daily-upgrade.timer")
	return err
}

func hardenSSH(ctx context.Context, s *platform.System, _ *platform.ServerConfig) error {
	if _, err := os.Stat("/etc/ssh/sshd_config"); err != nil {
		return nil // no OpenSSH server
	}
	if raw, err := os.ReadFile("/etc/ssh/sshd_config"); err == nil && !strings.Contains(string(raw), "sshd_config.d/*.conf") {
		return fmt.Errorf("/etc/ssh/sshd_config does not include sshd_config.d; set PasswordAuthentication no there yourself")
	}
	changed, err := s.WriteFile(sshdDropIn, []byte(sshdConfig), 0o644)
	if err != nil {
		return err
	}
	// Never leave a config sshd refuses: that would lock everyone out.
	if _, err := s.Run(ctx, "sshd", "-t"); err != nil {
		_ = os.Remove(sshdDropIn)
		return fmt.Errorf("sshd rejected the hardened config (removed it): %w", err)
	}
	if changed {
		s.Log("ssh: keys only, no passwords")
		_, err = s.Run(ctx, "systemctl", "try-reload-or-restart", "ssh.service")
	}
	return err
}

func hardenTime(ctx context.Context, s *platform.System, _ *platform.ServerConfig) error {
	if exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "chrony").Run() == nil {
		return nil
	}
	if err := s.Apt(ctx, "systemd-timesyncd"); err != nil {
		return err
	}
	_, err := s.Run(ctx, "timedatectl", "set-ntp", "true")
	return err
}

// swapSize is half the RAM, between 1 and 4 GiB: room for a burst (a build,
// a migration) without the OOM killer, small enough not to thrash.
func swapSize(ramBytes uint64) uint64 {
	const gib = 1 << 30
	return min(max(ramBytes/2, gib), 4*gib)
}

func hardenSwap(ctx context.Context, s *platform.System, _ *platform.ServerConfig) error {
	if _, err := s.WriteFile(swapSysctl, []byte("# Managed by tiffin provision: use swap only under pressure.\nvm.swappiness=10\n"), 0o644); err != nil {
		return err
	}
	if _, err := s.Run(ctx, "sysctl", "--load="+swapSysctl); err != nil {
		return err
	}
	out, err := s.Run(ctx, "swapon", "--show", "--noheadings")
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "" {
		return nil // the server already has swap
	}
	ram, err := memTotal()
	if err != nil {
		return err
	}
	size := swapSize(ram)
	avail, err := s.Run(ctx, "df", "--output=avail", "-B1", "/")
	if err != nil {
		return err
	}
	f := strings.Fields(avail)
	free, _ := strconv.ParseUint(f[len(f)-1], 10, 64)
	if free < size+8<<30 {
		s.Log(fmt.Sprintf("swap: skipped, only %d GiB free on the root disk", free>>30))
		return nil
	}
	s.Log(fmt.Sprintf("swap: adding a %d GiB swap file", size>>30))
	if _, err := s.Sh(ctx, fmt.Sprintf(`swapoff %[1]s 2>/dev/null || true
rm -f %[1]s
fallocate -l %[2]d %[1]s
chmod 600 %[1]s
mkswap %[1]s >/dev/null
swapon %[1]s
grep -q '^%[1]s ' /etc/fstab || echo '%[1]s none swap sw 0 0' >> /etc/fstab`, swapFile, size)); err != nil {
		return err
	}
	return nil
}

func hardenJournald(ctx context.Context, s *platform.System, _ *platform.ServerConfig) error {
	changed, err := s.WriteFile(journaldDropIn, []byte(journaldConfig), 0o644)
	if err != nil || !changed {
		return err
	}
	_, err = s.Run(ctx, "systemctl", "restart", "systemd-journald")
	return err
}

// serverChecks report the hardening in /v1/status (servers only).
func serverChecks(ctx context.Context, c *platform.ServerConfig) []platform.Check {
	var parts []string
	ok := true
	if raw, err := os.ReadFile(autoUpgradesFile); err == nil && strings.Contains(string(raw), `Unattended-Upgrade "1"`) {
		parts = append(parts, "security updates install daily")
	} else {
		ok = false
		parts = append(parts, "automatic security updates are off")
	}
	if c.RebootWindow != "" {
		parts = append(parts, "kernel updates reboot at "+c.RebootWindow)
	} else {
		parts = append(parts, "automatic reboots off")
	}
	if raw, err := os.ReadFile(rebootRequired + ".pkgs"); err == nil || fileExists(rebootRequired) {
		pk := strings.Fields(string(raw))
		what := "an update"
		if len(pk) > 0 {
			what = strings.Join(pk[:min(3, len(pk))], ", ")
		}
		if c.RebootWindow != "" {
			parts = append(parts, what+" waits for the next reboot window")
		} else {
			parts = append(parts, "a reboot is waiting for "+what+" (reboot the server, or set tiffin up --reboot-window 04:00)")
		}
	}
	if raw, err := os.ReadFile(sshdDropIn); err == nil && strings.Contains(string(raw), "PasswordAuthentication no") {
		parts = append(parts, "SSH keys only")
	} else {
		ok = false
		parts = append(parts, "SSH password login is not turned off")
	}
	if out, err := exec.CommandContext(ctx, "timedatectl", "show", "-p", "NTPSynchronized", "--value").Output(); err == nil && strings.TrimSpace(string(out)) == "no" {
		parts = append(parts, "the clock is not synchronized yet")
	}
	if out, err := exec.CommandContext(ctx, "swapon", "--show", "--noheadings", "--bytes", "--raw").Output(); err == nil {
		var total int64
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if f := strings.Fields(line); len(f) >= 3 {
				n, _ := strconv.ParseInt(f[2], 10, 64)
				total += n
			}
		}
		if total > 0 {
			parts = append(parts, fmt.Sprintf("%.1f GiB swap", float64(total)/(1<<30)))
		}
	}
	return []platform.Check{{Name: "server", OK: ok, Detail: strings.Join(parts, "; ")}}
}

// memTotal reads the machine's RAM from /proc/meminfo.
func memTotal() (uint64, error) {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(line, "MemTotal:"); ok {
			if f := strings.Fields(rest); len(f) > 0 {
				kb, err := strconv.ParseUint(f[0], 10, 64)
				return kb << 10, err
			}
		}
	}
	return 0, fmt.Errorf("no MemTotal in /proc/meminfo")
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
