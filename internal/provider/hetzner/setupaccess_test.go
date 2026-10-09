package hetzner

import (
	"context"
	"testing"

	"github.com/shiptiffin/tiffin/internal/provider/hetzner/hetznertest"
)

func TestCloseSetupAccess(t *testing.T) {
	f := hetznertest.New()
	defer f.Close()
	p := newProvider(t, f, nil)
	ctx := context.Background()
	if _, err := p.Ensure(ctx, quiet); err != nil {
		t.Fatal(err)
	}
	if open, keys, err := p.SSHOpen(ctx); err != nil || !open || keys != 1 {
		t.Fatalf("after up: open=%v keys=%d err=%v", open, keys, err)
	}
	if err := p.CloseSetupAccess(ctx, quiet); err != nil {
		t.Fatal(err)
	}
	if open, keys, err := p.SSHOpen(ctx); err != nil || open || keys != 0 {
		t.Fatalf("after close: open=%v keys=%d err=%v", open, keys, err)
	}
	fw := f.Firewall()
	var web bool
	for _, r := range fw.Rules {
		if r.Port != nil && *r.Port == "443" {
			web = true
		}
	}
	if !web {
		t.Fatalf("HTTPS must stay open: %+v", fw.Rules)
	}
	// Idempotent.
	before := len(f.Mutations)
	if err := p.CloseSetupAccess(ctx, quiet); err != nil {
		t.Fatal(err)
	}
	if len(f.Mutations) != before {
		t.Fatalf("a second close changed things: %v", f.Mutations[before:])
	}
	if s, _, _, _ := f.Count(); s != 1 {
		t.Fatalf("the server must stay: %d", s)
	}
}
