package hetzner

import (
	"context"
	"strings"
	"testing"

	"github.com/shiptiffin/tiffin/internal/provider/hetzner/hetznertest"
)

// With Owner set the provider sees only what it made for that owner: a box
// the customer made themselves under the same name is invisible, so it is
// never reused or deleted.
func TestOwnerScopesEverything(t *testing.T) {
	f := hetznertest.New()
	defer f.Close()
	theirs := f.AddLabelledServer("shop", map[string]string{LabelKind: "box", LabelBox: "shop"})
	p := newProvider(t, f, func(c *Config) { c.Owner = "box_abc" })
	ctx := context.Background()
	in, err := p.Inventory(ctx)
	if err != nil || !in.Empty() {
		t.Fatalf("inventory sees the customer's own box: %+v %v", in, err)
	}
	if _, err := p.DestroyAll(ctx, true, quiet); err != nil {
		t.Fatal(err)
	}
	if s := f.ServerByName("shop"); s == nil || s.ID != theirs {
		t.Fatal("DestroyAll deleted a server it did not make")
	}
	if l := p.Labels(); l[LabelOwner] != "box_abc" || l[LabelBox] != "shop" {
		t.Fatalf("labels %v", l)
	}
	// Without an owner (tiffin up / down) the name is the selector, as before.
	if in, _ := newProvider(t, f, nil).Inventory(ctx); len(in.Servers) != 1 {
		t.Fatalf("tiffin's own view: %d servers", len(in.Servers))
	}
}

func TestEnsureRunningPowersOn(t *testing.T) {
	f := hetznertest.New()
	defer f.Close()
	f.AddLabelledServer("shop", map[string]string{LabelKind: "box", LabelBox: "shop", LabelOwner: "box_abc"})
	f.SetServerStatus("shop", "off")
	p := newProvider(t, f, func(c *Config) { c.Owner = "box_abc" })
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // a cancelled request still gets the server back on through ChangeType's own context
	if err := p.EnsureRunning(context.WithoutCancel(ctx), 0); err == nil {
		// d=0 times out at once: proves the bound is applied.
		t.Fatal("a zero bound must time out")
	}
	if err := p.EnsureRunning(context.Background(), 30e9); err != nil {
		t.Fatal(err)
	}
	if s := f.ServerByName("shop"); s.Status != "running" {
		t.Fatalf("status %s", s.Status)
	}
}

// For a control plane the firewall's SSH source is its setup worker, so the
// step never calls it the person's address; `tiffin up` still does.
func TestFirewallStepSaysWhoseAddress(t *testing.T) {
	ctx := context.Background()
	steps := func(owner string) string {
		f := hetznertest.New()
		defer f.Close()
		var said []string
		p := newProvider(t, f, func(c *Config) { c.Owner = owner })
		if _, err := p.Ensure(ctx, func(s string) { said = append(said, s) }); err != nil {
			t.Fatal(err)
		}
		for _, s := range said {
			if strings.HasPrefix(s, "creating the firewall") {
				return s
			}
		}
		t.Fatalf("no firewall step in %q", said)
		return ""
	}
	if s := steps("box_abc"); s != "creating the firewall: SSH allowed only from the setup worker's address; HTTP/HTTPS from anywhere" {
		t.Fatalf("managed: %q", s)
	}
	if s := steps(""); s != "creating the firewall: SSH now allowed from 198.51.100.7 (your current address); HTTP/HTTPS from anywhere" {
		t.Fatalf("tiffin up: %q", s)
	}
}
