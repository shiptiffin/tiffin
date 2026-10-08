package tokens

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The bootstrap link is the only sign-in a control plane gets for a box it
// set up: one use, and dead after its time on the box itself, so a copy that
// leaks later opens nothing.
func TestBootstrapLink(t *testing.T) {
	m, owner, _ := setup(t)
	ctx := context.Background()
	code, exp, err := m.BootstrapLink(ctx, owner, BootstrapTTL)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(exp); d < 23*time.Hour || d > 24*time.Hour+time.Minute {
		t.Fatalf("expires in %s", d)
	}
	if _, _, err := m.BootstrapLink(ctx, owner, 25*time.Hour); !errors.Is(err, ErrInvalid) {
		t.Fatalf("longer than a day: %v", err)
	}
	// A session or key can't make one.
	sess := &Principal{TokenID: "tok_x", Kind: KindHuman, Person: OwnerPerson, Scopes: owner.Scopes, Projects: owner.Projects}
	if _, _, err := m.BootstrapLink(ctx, sess, time.Hour); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a session made a bootstrap link: %v", err)
	}

	// Expired on the box: refused even though nobody used it.
	m.now = func() time.Time { return time.Now().Add(BootstrapTTL + time.Minute) }
	if _, _, _, err := m.RedeemLoginLink(ctx, code); err == nil {
		t.Fatal("an expired bootstrap link signed someone in")
	}
	m.now = time.Now

	// Within its day it signs the owner in, strongly (it can add a passkey), once.
	code, _, _ = m.BootstrapLink(ctx, owner, BootstrapTTL)
	m.now = func() time.Time { return time.Now().Add(23 * time.Hour) }
	_, tok, method, err := m.RedeemLoginLink(ctx, code)
	if err != nil || tok.Person != OwnerPerson || method != MethodTerminal {
		t.Fatalf("redeem: %+v %s %v", tok, method, err)
	}
	if _, _, _, err := m.RedeemLoginLink(ctx, code); err == nil {
		t.Fatal("a bootstrap link worked twice")
	}
}

// The control plane's hand-off: a fresh link on request (the newest one
// only), until the owner first signs in; then never again.
func TestHandoffLink(t *testing.T) {
	m, owner, _ := setup(t)
	ctx := context.Background()
	if done, err := m.HandoffDone(ctx); err != nil || done {
		t.Fatalf("done before any sign-in: %v %v", done, err)
	}
	first, _, err := m.BootstrapLink(ctx, owner, BootstrapTTL) // the one made at setup
	if err != nil {
		t.Fatal(err)
	}
	terminal, _, _ := m.CreateLoginLink(ctx, owner) // the owner's own `tiffin login`: untouched
	second, exp, err := m.HandoffLink(ctx, BootstrapTTL)
	if err != nil || time.Until(exp) < 23*time.Hour {
		t.Fatalf("handoff: %v %s", err, exp)
	}
	if _, _, _, err := m.RedeemLoginLink(ctx, first); err == nil {
		t.Fatal("an older hand-off link still works")
	}
	if _, _, _, err := m.RedeemLoginLink(ctx, second); err != nil {
		t.Fatalf("the newest hand-off link: %v", err)
	}
	if done, _ := m.HandoffDone(ctx); !done {
		t.Fatal("the owner signed in: the hand-off is done")
	}
	if _, _, err := m.HandoffLink(ctx, BootstrapTTL); !errors.Is(err, ErrHandoffDone) {
		t.Fatalf("a hand-off link after the owner signed in: %v", err)
	}
	if _, _, _, err := m.RedeemLoginLink(ctx, terminal); err != nil {
		t.Fatalf("the owner's own login link: %v", err)
	}
}
