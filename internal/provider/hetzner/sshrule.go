package hetzner

import (
	"net"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
)

// The Cloud Firewall's SSH rule follows the owner around. Home addresses
// change (a new lease, travel, a phone hotspot), so every `tiffin up` adds
// the caller's current address before it connects, and keeps up to
// MaxSSHSources recent ones, each stamped with the day it was last used, in
// the rule's description. Addresses not used for SSHSourceTTL are dropped.
// With --ssh-from any the rule allows everyone: SSH still takes keys only
// and CrowdSec still bans brute force, so it is the way back in for a
// person who is locked out.
const (
	MaxSSHSources = 5
	SSHSourceTTL  = 30 * 24 * time.Hour
	sshDescPrefix = "tiffin ssh:"
	sshAnyDesc    = "tiffin ssh: anywhere (tiffin up --ssh-from any); keys only, CrowdSec bans brute force"
	maxDescLen    = 255 // Hetzner's limit for a rule description
)

// SSHSource is one address allowed to reach port 22.
type SSHSource struct {
	Prefix netip.Prefix
	Seen   time.Time // the day it was last used; zero when unknown
}

func (s SSHSource) String() string {
	if s.Prefix.IsSingleIP() {
		return s.Prefix.Addr().String()
	}
	return s.Prefix.String()
}

// SSHAccess is what the SSH rule allows after `up`.
type SSHAccess struct {
	Anywhere bool        `json:"anywhere"`
	Sources  []SSHSource `json:"-"`
	Current  []string    `json:"current"` // this computer's addresses
	Earlier  []string    `json:"earlier"` // still allowed from earlier runs
	Dropped  []string    `json:"dropped,omitempty"`
	Changed  bool        `json:"changed"`
}

// Summary is a plain sentence for people.
func (a SSHAccess) Summary() string {
	if a.Anywhere {
		return "SSH is allowed from anywhere (keys only; CrowdSec bans brute force)"
	}
	s := "SSH now allowed from " + strings.Join(a.Current, ", ") + " (your current address)"
	if len(a.Earlier) > 0 {
		s += " and " + strings.Join(a.Earlier, ", ") + " (used before)"
	}
	if len(a.Dropped) > 0 {
		s += "; no longer from " + strings.Join(a.Dropped, ", ")
	}
	return s
}

func ipnet(p netip.Prefix) net.IPNet {
	return net.IPNet{IP: p.Addr().AsSlice(), Mask: net.CIDRMask(p.Bits(), p.Addr().BitLen())}
}

func isAny(n net.IPNet) bool { ones, _ := n.Mask.Size(); return ones == 0 }

// existingSSH reads the SSH rule of a firewall Tiffin made (nil: none yet).
func existingSSH(fw *hcloud.Firewall) (sources []SSHSource, anywhere bool) {
	if fw == nil {
		return nil, false
	}
	for _, r := range fw.Rules {
		if r.Direction != hcloud.FirewallRuleDirectionIn || r.Port == nil || *r.Port != "22" {
			continue
		}
		seen := map[netip.Prefix]time.Time{}
		if r.Description != nil {
			if rest, ok := strings.CutPrefix(*r.Description, sshDescPrefix); ok {
				for _, f := range strings.Fields(rest) {
					addr, day, _ := strings.Cut(f, "@")
					p, err := parsePrefix(addr)
					t, terr := time.Parse("20060102", day)
					if err == nil && terr == nil {
						seen[p] = t
					}
				}
			}
		}
		for _, n := range r.SourceIPs {
			if isAny(n) {
				anywhere = true
				continue
			}
			ones, _ := n.Mask.Size()
			a, ok := netip.AddrFromSlice(n.IP)
			if !ok {
				continue
			}
			p := netip.PrefixFrom(a.Unmap(), ones).Masked()
			sources = append(sources, SSHSource{Prefix: p, Seen: seen[p]})
		}
	}
	return sources, anywhere
}

func parsePrefix(s string) (netip.Prefix, error) {
	if a, err := netip.ParseAddr(s); err == nil {
		return netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()), nil
	}
	p, err := netip.ParsePrefix(s)
	return p.Masked(), err
}

// planSSH merges this computer's addresses into what the rule allows.
func planSSH(fw *hcloud.Firewall, current []netip.Prefix, anywhere bool, now time.Time) SSHAccess {
	old, wasAny := existingSSH(fw)
	if anywhere {
		return SSHAccess{Anywhere: true, Changed: !wasAny || len(old) > 0}
	}
	day := now.UTC().Truncate(24 * time.Hour)
	acc := SSHAccess{}
	have := map[netip.Prefix]bool{}
	for _, c := range current {
		c = c.Masked()
		if have[c] {
			continue
		}
		have[c] = true
		s := SSHSource{Prefix: c, Seen: day}
		acc.Sources = append(acc.Sources, s)
		acc.Current = append(acc.Current, s.String())
	}
	var earlier []SSHSource
	for _, o := range old {
		if have[o.Prefix] {
			continue
		}
		have[o.Prefix] = true
		if o.Seen.IsZero() {
			o.Seen = day // made before stamps (or by hand): give it a month from now
		}
		if now.Sub(o.Seen) > SSHSourceTTL {
			acc.Dropped = append(acc.Dropped, o.String())
			continue
		}
		earlier = append(earlier, o)
	}
	sort.SliceStable(earlier, func(i, j int) bool { return earlier[i].Seen.After(earlier[j].Seen) })
	for _, e := range earlier {
		if len(acc.Sources) >= MaxSSHSources {
			acc.Dropped = append(acc.Dropped, e.String())
			continue
		}
		acc.Sources = append(acc.Sources, e)
		acc.Earlier = append(acc.Earlier, e.String())
	}
	// The description must fit Hetzner's limit: drop the oldest until it does.
	for len(sshDesc(acc.Sources)) > maxDescLen && len(acc.Sources) > len(acc.Current) {
		last := acc.Sources[len(acc.Sources)-1]
		acc.Sources = acc.Sources[:len(acc.Sources)-1]
		acc.Earlier = acc.Earlier[:len(acc.Earlier)-1]
		acc.Dropped = append(acc.Dropped, last.String())
	}
	acc.Changed = wasAny || !sameSources(old, acc.Sources)
	return acc
}

func sameSources(a, b []SSHSource) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[netip.Prefix]time.Time{}
	for _, s := range a {
		m[s.Prefix] = s.Seen
	}
	for _, s := range b {
		if t, ok := m[s.Prefix]; !ok || !t.Equal(s.Seen) {
			return false
		}
	}
	return true
}

func sshDesc(src []SSHSource) string {
	parts := []string{sshDescPrefix}
	for _, s := range src {
		parts = append(parts, s.String()+"@"+s.Seen.Format("20060102"))
	}
	return strings.Join(parts, " ")
}

// sshRule renders the SSH rule for an access plan.
func sshRule(a SSHAccess) hcloud.FirewallRule {
	r := hcloud.FirewallRule{Direction: hcloud.FirewallRuleDirectionIn, Protocol: hcloud.FirewallRuleProtocolTCP, Port: hcloud.Ptr("22")}
	if a.Anywhere {
		r.SourceIPs, r.Description = []net.IPNet{anyV4, anyV6}, hcloud.Ptr(sshAnyDesc)
		return r
	}
	for _, s := range a.Sources {
		r.SourceIPs = append(r.SourceIPs, ipnet(s.Prefix))
	}
	r.Description = hcloud.Ptr(sshDesc(a.Sources))
	return r
}
