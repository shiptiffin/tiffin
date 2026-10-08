package hetzner

import (
	"context"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
)

// CloseSetupAccess takes away what let the creator in to install the box:
// the firewall's SSH rule (port 22 is then closed to everyone) and the SSH
// key objects labelled for this box in the Hetzner project. HTTP, HTTPS and
// ping stay open. The key's line in root's authorized_keys must be removed
// on the server first (over that SSH connection); this is the API half.
//
// The control plane uses it after installing a managed box, so it keeps no
// way into the customer's server. The person can open SSH again in the
// Hetzner console (a firewall rule, a key in rescue mode).
func (p *Provider) CloseSetupAccess(ctx context.Context, progress func(string)) error {
	in, err := p.Inventory(ctx)
	if err != nil {
		return err
	}
	for _, fw := range in.Firewalls {
		var keep []hcloud.FirewallRule
		for _, r := range fw.Rules {
			if r.Direction == hcloud.FirewallRuleDirectionIn && r.Port != nil && *r.Port == "22" {
				continue
			}
			keep = append(keep, r)
		}
		if len(keep) == len(fw.Rules) {
			continue
		}
		progress("closing SSH on the firewall " + fw.Name)
		acts, _, err := p.c.Firewall.SetRules(ctx, fw, hcloud.FirewallSetRulesOpts{Rules: keep})
		if err != nil {
			return apiErr("close SSH on the firewall", err)
		}
		if err := p.wait(ctx, acts...); err != nil {
			return err
		}
	}
	for _, k := range in.SSHKeys {
		progress("deleting the setup SSH key " + k.Name + " from the Hetzner project")
		if _, err := p.c.SSHKey.Delete(ctx, k); err != nil && !hcloud.IsError(err, hcloud.ErrorCodeNotFound) {
			return apiErr("delete the setup SSH key", err)
		}
	}
	return nil
}

// SSHOpen reports whether the box's firewall still lets anyone reach port 22,
// and how many SSH key objects are labelled for it (both 0/false once
// CloseSetupAccess ran).
func (p *Provider) SSHOpen(ctx context.Context) (open bool, keys int, err error) {
	in, err := p.Inventory(ctx)
	if err != nil {
		return false, 0, err
	}
	for _, fw := range in.Firewalls {
		for _, r := range fw.Rules {
			if r.Direction == hcloud.FirewallRuleDirectionIn && r.Port != nil && *r.Port == "22" {
				open = true
			}
		}
	}
	return open, len(in.SSHKeys), nil
}
