package hetzner

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
)

// A control plane installs a managed box over SSH with a key of its own
// (uploaded as a key object) through a firewall rule that lets only its
// setup worker reach port 22. Once the install is done that access goes:
// the key's line on the server first (over that SSH connection; see
// internal/cloud), then the API half here. Port 22 then follows an explicit
// policy: open to everyone when the owner's own keys were installed (sshd
// takes keys only, CrowdSec bans brute force), closed otherwise. HTTP, HTTPS
// and ping stay open. The person can change it in the Hetzner console.

// AccessPolicy is what a managed box's firewall and key objects end up as
// after its setup.
type AccessPolicy struct {
	// SSHOpen: exactly one rule reaches port 22, TCP 22 from anywhere.
	// Otherwise no rule reaches it.
	SSHOpen bool
	// SetupKey is the ID of the setup's SSH key object: it is deleted.
	SetupKey int64
}

// sshOwnerDesc describes the anywhere rule a managed box keeps for its owner.
const sshOwnerDesc = "tiffin ssh: anywhere, for the owner's own SSH keys (keys only; CrowdSec bans brute force)"

// ownerSSHRule is the one SSH rule of a managed box whose owner has keys.
func ownerSSHRule() hcloud.FirewallRule {
	r := sshRule(SSHAccess{Anywhere: true})
	r.Description = hcloud.Ptr(sshOwnerDesc)
	return r
}

// FinishSetupAccess applies pol to what is labelled for this box: every
// inbound rule that reaches port 22 (the port, a range holding it, or any
// port) goes, the owner's anywhere rule comes in when pol.SSHOpen, and the
// setup key object is deleted. Nothing without the box's labels is touched.
// Running it again changes nothing.
func (p *Provider) FinishSetupAccess(ctx context.Context, pol AccessPolicy, progress func(string)) error {
	in, err := p.Inventory(ctx)
	if err != nil {
		return err
	}
	for _, fw := range in.Firewalls {
		var rules []hcloud.FirewallRule
		for _, r := range fw.Rules {
			if !reaches22(r) {
				rules = append(rules, r)
			}
		}
		if pol.SSHOpen {
			rules = append(rules, ownerSSHRule())
		}
		if sameRules(fw.Rules, rules) {
			continue
		}
		if pol.SSHOpen {
			progress("opening SSH to the owner's keys only on the firewall " + fw.Name)
		} else {
			progress("closing SSH on the firewall " + fw.Name)
		}
		acts, _, err := p.c.Firewall.SetRules(ctx, fw, hcloud.FirewallSetRulesOpts{Rules: rules})
		if err != nil {
			return apiErr("set the firewall's SSH rule", err)
		}
		if err := p.wait(ctx, acts...); err != nil {
			return err
		}
	}
	for _, k := range in.SSHKeys {
		if k.ID != pol.SetupKey {
			continue
		}
		progress("deleting the setup SSH key " + k.Name + " from the Hetzner project")
		if _, err := p.c.SSHKey.Delete(ctx, k); err != nil && !hcloud.IsError(err, hcloud.ErrorCodeNotFound) {
			return apiErr("delete the setup SSH key", err)
		}
	}
	return nil
}

// ErrAccessMismatch: the box's firewall or key objects are not what the
// policy says.
var ErrAccessMismatch = errors.New("the setup access isn't closed as it should be")

// AccessMatches checks what is labelled for this box against pol: the
// server sits behind a labelled firewall; the rules reaching port 22 are
// exactly the owner's anywhere rule (pol.SSHOpen) or none, whatever their
// port spelling; and no SSH key object is left (the setup's included).
func (p *Provider) AccessMatches(ctx context.Context, pol AccessPolicy) error {
	in, err := p.Inventory(ctx)
	if err != nil {
		return err
	}
	var bad []string
	for _, s := range in.Servers {
		applied := false
		for _, fw := range in.Firewalls {
			applied = applied || firewallApplied(fw, s.ID)
		}
		if !applied {
			bad = append(bad, "the server "+s.Name+" has no firewall of ours, so every port is open")
		}
	}
	var ssh []hcloud.FirewallRule
	for _, fw := range in.Firewalls {
		for _, r := range fw.Rules {
			if reaches22(r) {
				ssh = append(ssh, r)
			}
		}
	}
	switch {
	case pol.SSHOpen && (len(ssh) != 1 || !anywhere22(ssh[0])):
		bad = append(bad, fmt.Sprintf("port 22 should have one rule, from anywhere; it has %d (%s)", len(ssh), rulesWords(ssh)))
	case !pol.SSHOpen && len(ssh) > 0:
		bad = append(bad, fmt.Sprintf("port 22 should be closed; %d rules reach it (%s)", len(ssh), rulesWords(ssh)))
	}
	for _, k := range in.SSHKeys {
		what := "an SSH key object " + k.Name + " is left"
		if k.ID == pol.SetupKey {
			what = "the setup SSH key " + k.Name + " is still in the project"
		}
		bad = append(bad, what)
	}
	if len(bad) > 0 {
		return fmt.Errorf("%w: %s", ErrAccessMismatch, strings.Join(bad, "; "))
	}
	return nil
}

// OpenSSH adds the owner's anywhere rule for port 22 to the box's labelled
// firewall, keeping every other rule (the owner's own included). It is the
// way to open SSH later on a box set up without keys. Running it again
// changes nothing.
func (p *Provider) OpenSSH(ctx context.Context, progress func(string)) error {
	in, err := p.Inventory(ctx)
	if err != nil {
		return err
	}
	if len(in.Firewalls) == 0 {
		return errors.New("the box's firewall is gone from your Hetzner project; open port 22 in the Hetzner console instead (Firewalls › your server's firewall › add an inbound rule for TCP 22)")
	}
	fw := in.Firewalls[0]
	for _, r := range fw.Rules {
		if anywhere22(r) {
			progress("the firewall " + fw.Name + " already lets SSH in from anywhere")
			return nil
		}
	}
	progress("adding the SSH rule to the firewall " + fw.Name)
	rules := append(append([]hcloud.FirewallRule(nil), fw.Rules...), ownerSSHRule())
	acts, _, err := p.c.Firewall.SetRules(ctx, fw, hcloud.FirewallSetRulesOpts{Rules: rules})
	if err != nil {
		return apiErr("add the SSH rule to the firewall", err)
	}
	if err := p.wait(ctx, acts...); err != nil {
		return err
	}
	// Read it back: the rule must be there now.
	if in, err = p.Inventory(ctx); err != nil {
		return err
	}
	for _, f := range in.Firewalls {
		if f.ID != fw.ID {
			continue
		}
		for _, r := range f.Rules {
			if anywhere22(r) {
				return nil
			}
		}
	}
	return errors.New("the SSH rule isn't on the firewall after adding it")
}

// reaches22 reports whether an inbound rule lets some address reach TCP
// port 22: the port itself, a range holding it, or every port. A port it
// can't read counts.
func reaches22(r hcloud.FirewallRule) bool {
	if r.Direction != hcloud.FirewallRuleDirectionIn || len(r.SourceIPs) == 0 {
		return false
	}
	if r.Protocol != hcloud.FirewallRuleProtocolTCP && r.Protocol != "" {
		return false // UDP, ICMP, ESP and GRE never carry SSH
	}
	if r.Port == nil {
		return true
	}
	port := strings.TrimSpace(*r.Port)
	if port == "" || port == "any" {
		return true
	}
	lo, hi, isRange := strings.Cut(port, "-")
	if !isRange {
		hi = lo
	}
	a, err1 := strconv.Atoi(strings.TrimSpace(lo))
	b, err2 := strconv.Atoi(strings.TrimSpace(hi))
	if err1 != nil || err2 != nil {
		return true
	}
	return a <= 22 && 22 <= b
}

// anywhere22 is the owner's rule: TCP port 22 from every IPv4 and IPv6 address.
func anywhere22(r hcloud.FirewallRule) bool {
	if !reaches22(r) || r.Port == nil || strings.TrimSpace(*r.Port) != "22" || r.Protocol != hcloud.FirewallRuleProtocolTCP {
		return false
	}
	var v4, v6 bool
	for _, n := range r.SourceIPs {
		if !isAny(n) {
			return false
		}
		if n.IP.To4() != nil {
			v4 = true
		} else {
			v6 = true
		}
	}
	return v4 && v6
}

func rulesWords(rs []hcloud.FirewallRule) string {
	if len(rs) == 0 {
		return "none"
	}
	var out []string
	for _, r := range rs {
		port := "any port"
		if r.Port != nil {
			port = "port " + *r.Port
		}
		var src []string
		for _, n := range r.SourceIPs {
			src = append(src, n.String())
		}
		out = append(out, string(r.Protocol)+" "+port+" from "+strings.Join(src, ", "))
	}
	return strings.Join(out, "; ")
}
