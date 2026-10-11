package hetzner

import (
	"context"
	"errors"
	"net"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/hetznercloud/hcloud-go/v2/hcloud/schema"
	"github.com/shiptiffin/tiffin/internal/provider/hetzner/hetznertest"
)

// fakeSSHRules lists the fake firewall's rules for port 22 as "port sources".
func fakeSSHRules(f *hetznertest.Fake) []string {
	var out []string
	for _, r := range f.Firewall().Rules {
		if r.Direction == "in" && r.Protocol == "tcp" && r.Port != nil && (*r.Port == "22" || *r.Port == "any" || strings.Contains(*r.Port, "-")) {
			out = append(out, *r.Port+" "+strings.Join(r.SourceIPs, ","))
		}
	}
	return out
}

func TestFinishSetupAccess(t *testing.T) {
	for _, open := range []bool{false, true} {
		t.Run(map[bool]string{false: "no owner keys", true: "owner keys"}[open], func(t *testing.T) {
			f := hetznertest.New()
			defer f.Close()
			// A key the customer uploaded themselves (no labels): never touched.
			f.AddKey("laptop", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl laptop")
			p := newProvider(t, f, func(c *Config) { c.Owner = "box_abc" })
			ctx := context.Background()
			if _, err := p.Ensure(ctx, quiet); err != nil {
				t.Fatal(err)
			}
			pol := AccessPolicy{SSHOpen: open, SetupKey: p.Key.ID}
			if err := p.AccessMatches(ctx, pol); !errors.Is(err, ErrAccessMismatch) || !strings.Contains(err.Error(), "setup SSH key") {
				t.Fatalf("before: %v", err)
			}
			if err := p.FinishSetupAccess(ctx, pol, quiet); err != nil {
				t.Fatal(err)
			}
			if err := p.AccessMatches(ctx, pol); err != nil {
				t.Fatal(err)
			}
			got := fakeSSHRules(f)
			if open && !slices.Equal(got, []string{"22 0.0.0.0/0,::/0"}) || !open && len(got) != 0 {
				t.Fatalf("SSH rules: %v", got)
			}
			var web bool
			for _, r := range f.Firewall().Rules {
				web = web || r.Port != nil && *r.Port == "443"
			}
			if !web {
				t.Fatalf("HTTPS must stay open: %+v", f.Firewall().Rules)
			}
			if _, _, _, keys := f.Count(); keys != 1 {
				t.Fatalf("the customer's own key must stay: %d keys", keys)
			}
			// The other policy doesn't match.
			if err := p.AccessMatches(ctx, AccessPolicy{SSHOpen: !open}); !errors.Is(err, ErrAccessMismatch) {
				t.Fatalf("the opposite policy matched: %v", err)
			}
			// Idempotent.
			before := len(f.Mutations)
			if err := p.FinishSetupAccess(ctx, pol, quiet); err != nil {
				t.Fatal(err)
			}
			if len(f.Mutations) != before {
				t.Fatalf("a second run changed things: %v", f.Mutations[before:])
			}
			if s, _, _, _ := f.Count(); s != 1 {
				t.Fatalf("the server must stay: %d", s)
			}
		})
	}
}

// Port 22 reached through a range, "any" port or several rules is still
// port 22: the check sees it and finishing takes it away.
func TestAccessMatchesSeesEveryWayTo22(t *testing.T) {
	f := hetznertest.New()
	defer f.Close()
	p := newProvider(t, f, func(c *Config) { c.Owner = "box_abc" })
	ctx := context.Background()
	if _, err := p.Ensure(ctx, quiet); err != nil {
		t.Fatal(err)
	}
	pol := AccessPolicy{SetupKey: p.Key.ID}
	if err := p.FinishSetupAccess(ctx, pol, quiet); err != nil {
		t.Fatal(err)
	}
	any4 := []string{"0.0.0.0/0"}
	for _, r := range []schema.FirewallRule{
		{Direction: "in", Protocol: "tcp", Port: hcloud.Ptr("1-1024"), SourceIPs: any4},
		{Direction: "in", Protocol: "tcp", Port: hcloud.Ptr("any"), SourceIPs: []string{"192.0.2.0/24"}},
		{Direction: "in", Protocol: "tcp", Port: hcloud.Ptr("20-22"), SourceIPs: any4},
	} {
		f.AddFirewallRule(r)
		if err := p.AccessMatches(ctx, pol); !errors.Is(err, ErrAccessMismatch) {
			t.Fatalf("port %s from %v not seen: %v", *r.Port, r.SourceIPs, err)
		}
		if err := p.FinishSetupAccess(ctx, pol, quiet); err != nil {
			t.Fatal(err)
		}
		if err := p.AccessMatches(ctx, pol); err != nil {
			t.Fatalf("after finishing: %v", err)
		}
	}
	// What doesn't reach TCP 22 is not SSH.
	for _, r := range []schema.FirewallRule{
		{Direction: "in", Protocol: "udp", Port: hcloud.Ptr("22"), SourceIPs: any4},
		{Direction: "in", Protocol: "tcp", Port: hcloud.Ptr("23-80"), SourceIPs: any4},
		{Direction: "out", Protocol: "tcp", Port: hcloud.Ptr("22"), DestinationIPs: any4},
	} {
		f.AddFirewallRule(r)
	}
	if err := p.AccessMatches(ctx, pol); err != nil {
		t.Fatal(err)
	}
	// Open: one rule from anywhere, not two, and not a v4-only one.
	open := AccessPolicy{SSHOpen: true}
	f.AddFirewallRule(schema.FirewallRule{Direction: "in", Protocol: "tcp", Port: hcloud.Ptr("22"), SourceIPs: any4})
	if err := p.AccessMatches(ctx, open); !errors.Is(err, ErrAccessMismatch) {
		t.Fatalf("a v4-only rule is not the owner's rule: %v", err)
	}
	if err := p.FinishSetupAccess(ctx, open, quiet); err != nil {
		t.Fatal(err)
	}
	if err := p.AccessMatches(ctx, open); err != nil {
		t.Fatal(err)
	}
	f.AddFirewallRule(schema.FirewallRule{Direction: "in", Protocol: "tcp", Port: hcloud.Ptr("22"), SourceIPs: []string{"198.51.100.7/32"}})
	if err := p.AccessMatches(ctx, open); !errors.Is(err, ErrAccessMismatch) || !strings.Contains(err.Error(), "it has 2") {
		t.Fatalf("two SSH rules: %v", err)
	}
}

func TestReaches22(t *testing.T) {
	any4 := []net.IPNet{anyV4}
	rule := func(proto hcloud.FirewallRuleProtocol, port *string) hcloud.FirewallRule {
		return hcloud.FirewallRule{Direction: hcloud.FirewallRuleDirectionIn, Protocol: proto, Port: port, SourceIPs: any4}
	}
	tcp := hcloud.FirewallRuleProtocolTCP
	for _, c := range []struct {
		r    hcloud.FirewallRule
		want bool
	}{
		{rule(tcp, hcloud.Ptr("22")), true},
		{rule(tcp, hcloud.Ptr("1-65535")), true},
		{rule(tcp, hcloud.Ptr("22-22")), true},
		{rule(tcp, hcloud.Ptr("any")), true},
		{rule(tcp, nil), true},
		{rule(tcp, hcloud.Ptr("weird")), true},
		{rule(tcp, hcloud.Ptr("23-25")), false},
		{rule(tcp, hcloud.Ptr("443")), false},
		{rule(hcloud.FirewallRuleProtocolUDP, hcloud.Ptr("22")), false},
		{rule(hcloud.FirewallRuleProtocolICMP, nil), false},
		{hcloud.FirewallRule{Direction: hcloud.FirewallRuleDirectionOut, Protocol: tcp, Port: hcloud.Ptr("22"), DestinationIPs: any4}, false},
	} {
		if got := reaches22(c.r); got != c.want {
			port := "nil"
			if c.r.Port != nil {
				port = *c.r.Port
			}
			t.Errorf("%s %s %s: %v", c.r.Direction, c.r.Protocol, port, got)
		}
	}
}

// Opening SSH later adds the owner's rule and keeps every other one,
// including the owner's own; running it again changes nothing.
func TestOpenSSH(t *testing.T) {
	f := hetznertest.New()
	defer f.Close()
	p := newProvider(t, f, func(c *Config) { c.Owner = "box_abc" })
	ctx := context.Background()
	if err := p.OpenSSH(ctx, quiet); err == nil || !strings.Contains(err.Error(), "Hetzner console") {
		t.Fatalf("no firewall: %v", err)
	}
	if _, err := p.Ensure(ctx, quiet); err != nil {
		t.Fatal(err)
	}
	if err := p.FinishSetupAccess(ctx, AccessPolicy{SetupKey: p.Key.ID}, quiet); err != nil {
		t.Fatal(err)
	}
	f.AddFirewallRule(schema.FirewallRule{Direction: "in", Protocol: "tcp", Port: hcloud.Ptr("8080"), SourceIPs: []string{"192.0.2.0/24"}})
	n := len(f.Firewall().Rules)
	if err := p.OpenSSH(ctx, quiet); err != nil {
		t.Fatal(err)
	}
	if got := fakeSSHRules(f); !slices.Equal(got, []string{"22 0.0.0.0/0,::/0"}) || len(f.Firewall().Rules) != n+1 {
		t.Fatalf("SSH rules %v, %d rules (was %d)", got, len(f.Firewall().Rules), n)
	}
	if err := p.AccessMatches(ctx, AccessPolicy{SSHOpen: true}); err != nil {
		t.Fatal(err)
	}
	before := len(f.Mutations)
	if err := p.OpenSSH(ctx, quiet); err != nil || len(f.Mutations) != before {
		t.Fatalf("again: %v %v", err, f.Mutations[before:])
	}
}

// A managed box's anywhere rule is the owner's way back in: `tiffin up` or
// an adoption from the CLI keeps it unless the person names addresses.
// (A box made with tiffin up keeps today's behaviour: see
// TestSSHRuleFollowsTheOwner.)
func TestCLIKeepsAManagedBoxsAnywhereRule(t *testing.T) {
	f := hetznertest.New()
	defer f.Close()
	ctx := context.Background()
	managed := newProvider(t, f, func(c *Config) { c.Owner = "box_abc" })
	if _, err := managed.Ensure(ctx, quiet); err != nil {
		t.Fatal(err)
	}
	if err := managed.FinishSetupAccess(ctx, AccessPolicy{SSHOpen: true, SetupKey: managed.Key.ID}, quiet); err != nil {
		t.Fatal(err)
	}
	// The owner runs tiffin up for the box from their laptop, with their own
	// key (uploaded to the project), from their current address.
	pub, err := os.ReadFile(managed.cfg.KeyPath + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	f.AddKey("laptop", string(pub))
	own := newProvider(t, f, func(c *Config) { c.KeyPath, c.OwnKey = managed.cfg.KeyPath, true })
	if _, err := own.Ensure(ctx, quiet); err != nil {
		t.Fatal(err)
	}
	if got := fakeSSHRules(f); !slices.Equal(got, []string{"22 0.0.0.0/0,::/0"}) || !own.SSH.Anywhere {
		t.Fatalf("tiffin up narrowed the owner's SSH rule: %v", got)
	}
	if ap, err := own.PlanAdopt(ctx, managed.Server.Name, false); err != nil || !strings.Contains(strings.Join(ap.Changes, "\n"), "from anywhere") {
		t.Fatalf("adopt plan: %+v %v", ap, err)
	}
	// Naming addresses (--ssh-from) narrows it, on purpose.
	own.NarrowSSH = true
	if _, err := own.Ensure(ctx, quiet); err != nil {
		t.Fatal(err)
	}
	if got := fakeSSHRules(f); !slices.Equal(got, []string{"22 198.51.100.7/32"}) {
		t.Fatalf("--ssh-from: %v", got)
	}
}
