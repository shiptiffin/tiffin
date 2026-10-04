package hetzner

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/provider/hetzner/hetznertest"
)

// sshSources reads the fake firewall's SSH rule.
func sshSources(t *testing.T, f *hetznertest.Fake) ([]string, string) {
	t.Helper()
	for _, r := range f.Firewall().Rules {
		if r.Port != nil && *r.Port == "22" {
			return r.SourceIPs, *r.Description
		}
	}
	t.Fatal("no SSH rule")
	return nil, ""
}

func TestSSHRuleFollowsTheOwner(t *testing.T) {
	f := hetznertest.New()
	defer f.Close()
	ctx := context.Background()
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	base := newProvider(t, f, nil)
	up := func(from ...string) *Provider {
		t.Helper()
		p := newProvider(t, f, func(c *Config) {
			c.KeyPath, c.KnownHosts = base.cfg.KeyPath, base.cfg.KnownHosts
			c.Now = func() time.Time { return now }
			c.SSHFrom, c.SSHAnywhere = nil, false
			for _, s := range from {
				if s == "any" {
					c.SSHAnywhere = true
				} else {
					c.SSHFrom = append(c.SSHFrom, netip.MustParsePrefix(s))
				}
			}
		})
		if _, err := p.Ensure(ctx, quiet); err != nil {
			t.Fatal(err)
		}
		return p
	}

	p := up("198.51.100.7/32")
	if got := p.SSH.Summary(); got != "SSH now allowed from 198.51.100.7 (your current address)" {
		t.Fatalf("summary %q", got)
	}

	// Next day, new home IP: the old one stays (the rule is updated before SSH).
	now = now.Add(24 * time.Hour)
	p = up("203.0.113.7/32", "2001:db8:5:6::/64")
	src, desc := sshSources(t, f)
	if !slices.Equal(src, []string{"203.0.113.7/32", "2001:db8:5:6::/64", "198.51.100.7/32"}) {
		t.Fatalf("sources %v", src)
	}
	if !strings.Contains(desc, "198.51.100.7@20261004") || !strings.Contains(desc, "203.0.113.7@20261005") {
		t.Fatalf("description %q", desc)
	}
	if s := p.SSH.Summary(); !strings.Contains(s, "203.0.113.7, 2001:db8:5:6::/64 (your current address) and 198.51.100.7 (used before)") {
		t.Fatalf("summary %q", s)
	}

	// Same address again the same day: nothing to change.
	before := len(f.Mutations)
	if p = up("203.0.113.7/32", "2001:db8:5:6::/64"); p.SSH.Changed || len(f.Mutations) != before {
		t.Fatalf("no change expected: %v", f.Mutations[before:])
	}

	// At most five; the least recently used go first.
	for i := 1; i <= 4; i++ {
		now = now.Add(time.Hour * 24)
		up(fmt.Sprintf("192.0.2.%d/32", i))
	}
	src, _ = sshSources(t, f)
	if len(src) != MaxSSHSources || src[0] != "192.0.2.4/32" || slices.Contains(src, "198.51.100.7/32") {
		t.Fatalf("after many networks: %v", src)
	}

	// An address not used for 30 days is dropped.
	now = now.Add(31 * 24 * time.Hour)
	p = up("192.0.2.4/32")
	if src, _ = sshSources(t, f); !slices.Equal(src, []string{"192.0.2.4/32"}) || len(p.SSH.Dropped) != 4 {
		t.Fatalf("stale addresses must go: %v dropped %v", src, p.SSH.Dropped)
	}

	// Locked out? --ssh-from any opens it to everyone (keys only).
	p = up("any")
	if src, desc = sshSources(t, f); !slices.Equal(src, []string{"0.0.0.0/0", "::/0"}) || !strings.Contains(desc, "anywhere") || !p.SSH.Anywhere {
		t.Fatalf("any: %v %q", src, desc)
	}
	// And back to this computer's address.
	if up("192.0.2.9/32"); true {
		if src, _ = sshSources(t, f); !slices.Equal(src, []string{"192.0.2.9/32"}) {
			t.Fatalf("narrowed again: %v", src)
		}
	}
}
