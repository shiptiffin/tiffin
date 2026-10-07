package tokens

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// Admins can't sign in as the owner: no link for the owner, and no ending the
// owner's sessions through the API key revoke.
func TestAdminCannotImpersonateOrEndOwner(t *testing.T) {
	m, owner, _ := setup(t)
	ctx := context.Background()
	ann, _ := m.AddPerson(ctx, owner, "Ann", "", RoleAdmin)
	_, annP := session(t, m, ann.ID, MethodPasskey)
	if _, _, err := m.LoginLinkFor(ctx, annP, OwnerPerson); !errors.Is(err, ErrOwnerLink) {
		t.Fatalf("admin's link for the owner: %v", err)
	}
	_, ownerP := session(t, m, OwnerPerson, MethodPasskey)
	if err := m.Revoke(ctx, annP, ownerP.TokenID); !errors.Is(err, ErrSessionRevoke) {
		t.Fatalf("admin revokes the owner's session as a key: %v", err)
	}
	if tok, _ := m.Get(ctx, ownerP.TokenID); tok.RevokedAt != nil {
		t.Fatal("the owner's session was ended")
	}
}

// A key or session minted by a maker revoked meanwhile (a request in flight
// when the owner revokes it) is refused, not left behind.
func TestCreationLosesToRevocation(t *testing.T) {
	m, owner, _ := setup(t)
	ctx := context.Background()
	ann, _ := m.AddPerson(ctx, owner, "Ann", "", RoleAdmin)

	// An admin key, revoked while it is making another key.
	ks, _, _ := m.CreateKey(ctx, owner, KeyRequest{Name: "admin", Projects: Projects{AllProjects}, Access: LevelFull})
	kp, _ := m.Authenticate(ctx, ks)
	if err := m.Revoke(ctx, owner, kp.TokenID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.CreateKey(ctx, kp, KeyRequest{Name: "child", Projects: Projects{AllProjects}, Access: LevelFull}); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("revoked key made a key: %v", err)
	}

	// An admin's session, demoted (sessions end) while it is making a key.
	_, annP := session(t, m, ann.ID, MethodPasskey)
	if _, err := m.UpdatePerson(ctx, owner, ann.ID, "", RoleViewer); err != nil {
		t.Fatal(err)
	}
	day := KeyRequest{Name: "day", Projects: Projects{AllProjects}, Access: LevelRead, TTL: 24 * time.Hour} // no sudo
	if _, _, err := m.CreateKey(ctx, annP, day); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("demoted admin's session made a key: %v", err)
	}
	// A sign-in that read her old role just before the demotion landed.
	stale := &Person{ID: ann.ID, Name: "Ann", Role: RoleAdmin}
	if _, _, err := m.mintSession(ctx, stale, MethodPasskey); !errors.Is(err, ErrRevokedMaker) {
		t.Fatalf("session minted with a role she lost: %v", err)
	}
	// A demoted admin's in-flight change to people is refused too.
	if _, err := m.UpdatePerson(ctx, annP, OwnerPerson, "Mallory", ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("ended session renamed someone: %v", err)
	}
}

// A rename racing a demotion never writes the old role back.
func TestRenameDoesNotUndoDemotion(t *testing.T) {
	m, owner, _ := setup(t)
	ctx := context.Background()
	ann, _ := m.AddPerson(ctx, owner, "Ann", "", RoleAdmin)
	for i := range 40 {
		if _, err := m.UpdatePerson(ctx, owner, ann.ID, "", RoleAdmin); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = m.UpdatePerson(ctx, owner, ann.ID, fmt.Sprintf("Ann %d", i), "") }()
		go func() { defer wg.Done(); _, _ = m.UpdatePerson(ctx, owner, ann.ID, "", RoleViewer) }()
		wg.Wait()
		if p, _ := m.GetPerson(ctx, ann.ID); p.Role != RoleViewer {
			t.Fatalf("round %d: role %q after a demotion and a rename", i, p.Role)
		}
	}
}

// Ending other sessions ends all of them, not just the newest 500, and any
// session can be ended by ID.
func TestEndOtherSessionsHasNoLimit(t *testing.T) {
	m, owner, _ := setup(t)
	ctx := context.Background()
	ann, _ := m.AddPerson(ctx, owner, "Ann", "", RoleMember)
	start := time.Now().Add(-time.Hour)
	var oldest, second string
	for i := range 502 {
		m.now = func() time.Time { return start.Add(time.Duration(i) * time.Second) }
		_, tok, _, err := m.SessionFor(ctx, ann.ID, MethodPasskey)
		if err != nil {
			t.Fatal(err)
		}
		switch i {
		case 0:
			oldest = tok.ID
		case 1:
			second = tok.ID
		}
	}
	m.now = time.Now
	_, newest := session(t, m, ann.ID, MethodPasskey)
	if _, err := m.EndSession(ctx, owner, second); err != nil {
		t.Fatalf("end the second oldest of 503: %v", err)
	}
	n, err := m.EndOtherSessions(ctx, newest, "")
	if err != nil || n != 501 {
		t.Fatalf("end others: %d %v", n, err)
	}
	if tok, _ := m.Get(ctx, oldest); tok.RevokedAt == nil {
		t.Fatal("the oldest session survived sign out everywhere else")
	}
	if tok, _ := m.Get(ctx, newest.TokenID); tok.RevokedAt != nil {
		t.Fatal("the current session was ended")
	}
}

// Changing an address cancels the person's unspent sign-in links, and a link
// asked for at an address they no longer have is never made.
func TestEmailChangeCancelsLinks(t *testing.T) {
	m, owner, _ := setup(t)
	ctx := context.Background()
	ann, _ := m.AddPerson(ctx, owner, "Ann", "ann@example.com", RoleMember)
	emailed, _, err := m.EmailLoginLink(ctx, ann.ID, "ann@example.com")
	if err != nil {
		t.Fatal(err)
	}
	invite, _, _ := m.LoginLinkFor(ctx, owner, ann.ID)
	// The same address in other letters changes nothing.
	if _, err := m.SetPersonEmail(ctx, owner, ann.ID, "Ann@Example.com"); err != nil {
		t.Fatal(err)
	}
	keep, _, _ := m.EmailLoginLink(ctx, ann.ID, "ann@example.com")
	if _, err := m.SetPersonEmail(ctx, owner, ann.ID, "ann@example.org"); err != nil {
		t.Fatal(err)
	}
	for what, code := range map[string]string{"emailed": emailed, "invite": invite, "newest emailed": keep} {
		if _, _, _, err := m.RedeemLoginLink(ctx, code); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("%s link to the old address still works: %v", what, err)
		}
	}
	if _, _, err := m.EmailLoginLink(ctx, ann.ID, "ann@example.com"); !errors.Is(err, ErrPersonNotFound) {
		t.Fatalf("link for the old address: %v", err)
	}
	if _, _, err := m.EmailLoginLink(ctx, ann.ID, "ANN@example.org"); err != nil {
		t.Fatalf("link for the new address: %v", err)
	}
}

// Two invites with one address at once: only one person gets it.
func TestEmailUniqueUnderRace(t *testing.T) {
	m, owner, _ := setup(t)
	ctx := context.Background()
	for i := range 20 {
		addr := fmt.Sprintf("p%d@example.com", i)
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for j := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[j] = m.AddPerson(ctx, owner, fmt.Sprintf("P%d-%d", i, j), addr, RoleMember)
			}()
		}
		wg.Wait()
		if (errs[0] == nil) == (errs[1] == nil) {
			t.Fatalf("round %d: %v / %v", i, errs[0], errs[1])
		}
		for _, err := range errs {
			if err != nil && !errors.Is(err, ErrInvalid) {
				t.Fatalf("round %d: %v", i, err)
			}
		}
	}
	// A removed person's address is free again.
	p, _ := m.PersonByEmail(ctx, "p0@example.com")
	if err := m.RemovePerson(ctx, owner, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddPerson(ctx, owner, "Again", "P0@example.com", RoleViewer); err != nil {
		t.Fatalf("reuse a removed person's address: %v", err)
	}
}

// An invite lasts its 7 days whatever happens to the session that sent it,
// works when an admin key sent it, and stops when its sender loses admin or
// an owner ends the sending session.
func TestInviteLifecycle(t *testing.T) {
	m, owner, _ := setup(t)
	ctx := context.Background()
	ann, _ := m.AddPerson(ctx, owner, "Ann", "", RoleAdmin)
	bob, _ := m.AddPerson(ctx, owner, "Bob", "", RoleMember)
	redeems := func(what, code string, want bool) {
		t.Helper()
		_, tok, method, err := m.RedeemLoginLink(ctx, code)
		if want && (err != nil || tok.Person != bob.ID || tok.Sponsor != "" || method != MethodLink) {
			t.Fatalf("%s: %v %+v %q", what, err, tok, method)
		}
		if !want && !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("%s: redeemed (%v)", what, err)
		}
	}

	// Sent from a session that signed out, then expired.
	_, annP := session(t, m, ann.ID, MethodPasskey)
	inv, exp, _ := m.LoginLinkFor(ctx, annP, bob.ID)
	if time.Until(exp) < 6*24*time.Hour {
		t.Fatalf("invite expiry %v", exp)
	}
	if err := m.SignOut(ctx, annP); err != nil {
		t.Fatal(err)
	}
	m.now = func() time.Time { return time.Now().Add(SessionTTL + time.Hour) }
	redeems("invite from a signed-out, expired session", inv, true)
	m.now = time.Now

	// Sent with an admin key.
	ks, _, _ := m.CreateKey(ctx, owner, KeyRequest{Name: "admin", Projects: Projects{AllProjects}, Access: LevelFull})
	kp, _ := m.Authenticate(ctx, ks)
	inv, _, err := m.LoginLinkFor(ctx, kp, bob.ID)
	if err != nil {
		t.Fatal(err)
	}
	redeems("invite from an admin key", inv, true)
	inv, _, _ = m.LoginLinkFor(ctx, kp, bob.ID)
	_ = m.Revoke(ctx, owner, kp.TokenID)
	redeems("invite from a revoked key", inv, false)

	// An owner ends the session that sent it (a stolen one, say).
	_, annP = session(t, m, ann.ID, MethodPasskey)
	inv, _, _ = m.LoginLinkFor(ctx, annP, bob.ID)
	if _, err := m.EndSession(ctx, owner, annP.TokenID); err != nil {
		t.Fatal(err)
	}
	redeems("invite from an ended session", inv, false)

	// The sender is demoted.
	_, annP = session(t, m, ann.ID, MethodPasskey)
	inv, _, _ = m.LoginLinkFor(ctx, annP, bob.ID)
	if _, err := m.UpdatePerson(ctx, owner, ann.ID, "", RoleMember); err != nil {
		t.Fatal(err)
	}
	redeems("invite from a demoted admin", inv, false)

	// A session's link for itself dies with the session.
	_, bobP := session(t, m, bob.ID, MethodPasskey)
	self, _, _ := m.CreateLoginLink(ctx, bobP)
	if err := m.SignOut(ctx, bobP); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := m.RedeemLoginLink(ctx, self); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("a signed-out session's own link: %v", err)
	}
}
