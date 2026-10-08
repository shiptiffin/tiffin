package tokens

import (
	"context"
	"errors"
	"sync"
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
	sess, _, _, err := m.RedeemLoginLink(ctx, second)
	if err != nil {
		t.Fatalf("the newest hand-off link: %v", err)
	}
	if done, _ := m.HandoffDone(ctx); done {
		t.Fatal("done before the new session's first request")
	}
	if _, err := m.Authenticate(ctx, sess); err != nil {
		t.Fatal(err)
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

// A failed session write leaves the link unspent: the owner isn't locked out.
func TestRedeemFailureKeepsTheLink(t *testing.T) {
	m, _, _ := setup(t)
	ctx := context.Background()
	code, _, err := m.HandoffLink(ctx, BootstrapTTL)
	if err != nil {
		t.Fatal(err)
	}
	db := m.db.SQL()
	if _, err := db.Exec(`CREATE TRIGGER no_sessions BEFORE INSERT ON tokens WHEN NEW.kind = 'human' BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := m.RedeemLoginLink(ctx, code); err == nil {
		t.Fatal("redeemed without a session")
	}
	if done, _ := m.HandoffDone(ctx); done {
		t.Fatal("a failed sign-in closed the hand-off")
	}
	if _, err := db.Exec(`DROP TRIGGER no_sessions`); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := m.RedeemLoginLink(ctx, code); err != nil {
		t.Fatalf("the same link after the failure: %v", err)
	}
}

// The answer to a redemption is lost: the session exists but never made a
// request, so the owner never signed in and a replacement link still works.
// Their first real request closes the hand-off and ends every hand-off link.
func TestHandoffRecoversFromALostAnswer(t *testing.T) {
	m, _, _ := setup(t)
	ctx := context.Background()
	code, _, _ := m.HandoffLink(ctx, BootstrapTTL)
	sess, _, _, err := m.RedeemLoginLink(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Inspect(ctx, sess); err != nil { // the sign-in request itself
		t.Fatal(err)
	}
	if done, _ := m.HandoffDone(ctx); done {
		t.Fatal("done though the browser never got its session")
	}
	again, _, err := m.HandoffLink(ctx, BootstrapTTL)
	if err != nil {
		t.Fatalf("replacement link: %v", err)
	}
	spare, _, err := m.HandoffLink(ctx, BootstrapTTL)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := m.RedeemLoginLink(ctx, again); err == nil {
		t.Fatal("an older hand-off link still works")
	}
	// The lost session turns up after all (or a new one is used): done, and
	// the outstanding link is ended with it.
	if _, err := m.Authenticate(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if done, _ := m.HandoffDone(ctx); !done {
		t.Fatal("not done after the owner's first request")
	}
	if _, _, _, err := m.RedeemLoginLink(ctx, spare); err == nil {
		t.Fatal("a hand-off link outlived the hand-off")
	}
	if _, _, err := m.HandoffLink(ctx, BootstrapTTL); !errors.Is(err, ErrHandoffDone) {
		t.Fatalf("a hand-off link after the hand-off: %v", err)
	}
	// Pruning old sessions doesn't reopen it.
	if _, err := m.db.SQL().Exec(`DELETE FROM tokens WHERE kind = 'human'`); err != nil {
		t.Fatal(err)
	}
	if done, _ := m.HandoffDone(ctx); !done {
		t.Fatal("the hand-off reopened")
	}
}

// Minting a hand-off link races the owner's first request: whichever way it
// goes, no hand-off link is left that works after the hand-off.
func TestHandoffMintRacesSignIn(t *testing.T) {
	for i := range 20 {
		m, _, _ := setup(t)
		ctx := context.Background()
		code, _, _ := m.HandoffLink(ctx, BootstrapTTL)
		sess, _, _, err := m.RedeemLoginLink(ctx, code)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var minted string
		wg.Add(2)
		go func() {
			defer wg.Done()
			if c, _, err := m.HandoffLink(ctx, BootstrapTTL); err == nil {
				minted = c
			} else if !errors.Is(err, ErrHandoffDone) {
				t.Errorf("mint: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := m.Authenticate(ctx, sess); err != nil {
				t.Errorf("authenticate: %v", err)
			}
		}()
		wg.Wait()
		if done, _ := m.HandoffDone(ctx); !done {
			t.Fatalf("round %d: not done", i)
		}
		if minted != "" {
			if _, _, _, err := m.RedeemLoginLink(ctx, minted); err == nil {
				t.Fatalf("round %d: a link minted during the hand-off still signs the owner in", i)
			}
		}
	}
}
