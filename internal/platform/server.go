package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
)

// ServerConfigPath is where `tiffin up` records that a box runs on a real
// server (Hetzner, or any Ubuntu host over SSH) rather than a local VM. Its
// absence means a local box: no server hardening, no owner allowlist.
var ServerConfigPath = "/etc/tiffin/server.json"

// ServerConfig describes a box on a real server. `tiffin up` writes it
// before provisioning; provisioners read it to harden the machine.
type ServerConfig struct {
	// Provider is how the machine was made: "hetzner" or "ssh".
	Provider string `json:"provider"`
	// Name is the box's name on the owner's computer.
	Name string `json:"name"`
	// PublicIP and PublicIPv6 are the addresses people reach the box on.
	PublicIP   string `json:"publicIP,omitempty"`
	PublicIPv6 string `json:"publicIPv6,omitempty"`
	// OwnerIPs are the addresses the owner's CLI connected from (an IP, or
	// a /64 for IPv6). CrowdSec never bans them.
	OwnerIPs []string `json:"ownerIPs,omitempty"`
	// RebootWindow is the time of day ("HH:MM", server time, UTC on
	// Hetzner) when unattended upgrades may reboot for a kernel update.
	// Empty: never reboot automatically; status says when one is waiting.
	RebootWindow string `json:"rebootWindow,omitempty"`
}

var rebootWindowRE = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// ValidRebootWindow checks a reboot window ("" means off).
func ValidRebootWindow(s string) error {
	if s == "" || rebootWindowRE.MatchString(s) {
		return nil
	}
	return fmt.Errorf("reboot window %q must be a time of day like 04:00 (24-hour, server time)", s)
}

// Validate checks the config before it is written to a box.
func (c *ServerConfig) Validate() error {
	if c.Provider != "hetzner" && c.Provider != "ssh" {
		return fmt.Errorf("unknown server provider %q", c.Provider)
	}
	if err := ValidRebootWindow(c.RebootWindow); err != nil {
		return err
	}
	for _, ip := range []string{c.PublicIP, c.PublicIPv6} {
		if ip != "" {
			if _, err := netip.ParseAddr(ip); err != nil {
				return fmt.Errorf("public IP %q: %w", ip, err)
			}
		}
	}
	for _, ip := range c.OwnerIPs {
		if _, err := ParseIPOrPrefix(ip); err != nil {
			return err
		}
	}
	return nil
}

// ParseIPOrPrefix parses an address ("1.2.3.4") or a range ("2001:db8::/64").
func ParseIPOrPrefix(s string) (netip.Prefix, error) {
	if a, err := netip.ParseAddr(s); err == nil {
		a = a.Unmap()
		return netip.PrefixFrom(a, a.BitLen()), nil
	}
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%q is not an IP address or range", s)
	}
	return p.Masked(), nil
}

// LoadServerConfig reads the server config. It returns nil, nil on a local
// box (no file).
func LoadServerConfig() (*ServerConfig, error) {
	raw, err := os.ReadFile(ServerConfigPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var c ServerConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", ServerConfigPath, err)
	}
	return &c, nil
}

// SaveServerConfig writes the server config (root only, 0644: nothing secret).
func SaveServerConfig(c *ServerConfig) error {
	if err := c.Validate(); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(c, "", "  ")
	if err := os.MkdirAll(filepath.Dir(ServerConfigPath), 0o755); err != nil {
		return err
	}
	tmp := ServerConfigPath + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, ServerConfigPath)
}

// OwnerWatcher is implemented by modules that act on the address the box's
// owner connects from (protect: keep it out of CrowdSec bans).
type OwnerWatcher interface {
	OwnerSeen(ctx context.Context, p *Platform, ip string)
}

// OwnerSeen tells every OwnerWatcher that the owner's CLI authenticated
// from ip. It must be cheap: watchers deduplicate and work in the background.
func (p *Platform) OwnerSeen(ctx context.Context, ip string) {
	if p == nil || ip == "" {
		return
	}
	for _, m := range Modules() {
		if w, ok := m.(OwnerWatcher); ok {
			w.OwnerSeen(ctx, p, ip)
		}
	}
}
