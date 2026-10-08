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
