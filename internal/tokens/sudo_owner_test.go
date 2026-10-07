package tokens

import (
	"context"
	"errors"
	"testing"
)

// Only a link the owner token made for the owner (the owner's own `tiffin
// login`) signs in as MethodTerminal, a strong sign-in. Links made by an API
// key, by a session (the owner's too) or for anyone else stay MethodLink.
func TestOwnerTerminalLinkIsStrong(t *testing.T) {
	m, owner, _ := setup(t)
	ctx := context.Background()
	ann, _ := m.AddPerson(ctx, owner, "Ann", "", RoleAdmin)
	redeem := func(code string) (*Principal, string) {
		t.Helper()
		sec, tok, method, err := m.RedeemLoginLink(ctx, code)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.SetClient(ctx, tok.ID, Client{Method: method}); err != nil {
			t.Fatal(err)
		}
		p, err := m.Authenticate(ctx, sec)
		if err != nil {
			t.Fatal(err)
		}
		return p, method
	}

	// `tiffin login` with the owner token: strong, so sudo passes.
	code, _, err := m.CreateLoginLink(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	mine, method := redeem(code)
	if method != MethodTerminal || mine.Person != OwnerPerson || !m.Confirmed(ctx, mine.TokenID) {
		t.Fatalf("owner token's own link: method %q, person %q, confirmed %v", method, mine.Person, m.Confirmed(ctx, mine.TokenID))
	}
	if err := m.RequireSudo(ctx, mine, ErrReauthPasskey); err != nil {
		t.Fatalf("owner's terminal sign-in asked to confirm: %v", err)
	}
	// So is a link the owner token makes for the owner through People.
	code, _, _ = m.LoginLinkFor(ctx, owner, OwnerPerson)
	if _, method := redeem(code); method != MethodTerminal {
		t.Fatalf("owner token's link for the owner via People: %q", method)
	}

	// None of these count.
	weak := func(what, code string) {
		t.Helper()
		p, method := redeem(code)
		if method != MethodLink || m.Confirmed(ctx, p.TokenID) {
			t.Fatalf("%s: method %q, confirmed %v", what, method, m.Confirmed(ctx, p.TokenID))
		}
		if err := m.RequireSudo(ctx, p, ErrReauthEmail); !errors.Is(err, ErrReauth) {
			t.Fatalf("%s: sudo not asked: %v", what, err)
		}
	}
	// The owner token's invite for someone else.
	code, _, _ = m.LoginLinkFor(ctx, owner, ann.ID)
	weak("owner token's link for Ann", code)
	// A full-access key acts for nobody: it gets no `tiffin login` link.
	keySecret, _, err := m.CreateKey(ctx, owner, KeyRequest{Name: "ci", Projects: Projects{AllProjects}, Access: LevelFull})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := m.Authenticate(ctx, keySecret)
	if _, _, err := m.CreateLoginLink(ctx, key); !errors.Is(err, ErrKeySignIn) {
		t.Fatalf("an admin key's tiffin login: %v", err)
	}
	// The owner's own session minting a link for itself: a stolen session
	// must not renew its own sudo this way.
	code, _, err = m.CreateLoginLink(ctx, mine)
	if err != nil {
		t.Fatal(err)
	}
	weak("the owner's session's link", code)
	// Nobody but the owner makes a link for the owner: not an admin's
	// session, not an admin key.
	annSess, _ := session(t, m, ann.ID, MethodPasskey)
	annP, _ := m.Authenticate(ctx, annSess)
	for what, by := range map[string]*Principal{"admin session": annP, "admin key": key} {
		if _, _, err := m.LoginLinkFor(ctx, by, OwnerPerson); !errors.Is(err, ErrOwnerLink) {
			t.Fatalf("%s makes a link for the owner: %v", what, err)
		}
	}
}

// An API key can't change anyone's email address (emailed sign-in links go
// there); the owner token can. Only the owner changes the owner's, even from
// a confirmed admin session.
func TestEmailChangeByKeyOrForOwner(t *testing.T) {
	m, owner, _ := setup(t)
	ctx := context.Background()
	ann, _ := m.AddPerson(ctx, owner, "Ann", "ann@example.com", RoleAdmin)
	if _, err := m.SetPersonEmail(ctx, owner, OwnerPerson, "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	keySecret, _, err := m.CreateKey(ctx, owner, KeyRequest{Name: "ci", Projects: Projects{AllProjects}, Access: LevelFull})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := m.Authenticate(ctx, keySecret)
	for _, who := range []string{OwnerPerson, ann.ID} {
		for _, addr := range []string{"mallory@example.net", ""} {
			if _, err := m.SetPersonEmail(ctx, key, who, addr); !errors.Is(err, ErrEmailByKey) || !errors.Is(err, ErrForbidden) {
				t.Fatalf("admin key sets %s's email to %q: %v", who, addr, err)
			}
		}
	}
	// Setting the address it already has changes nothing, so isn't refused.
	if _, err := m.SetPersonEmail(ctx, key, ann.ID, "ANN@example.com"); err != nil {
		t.Fatalf("same address by key: %v", err)
	}

	// A confirmed admin session may change Ann's, never the owner's.
	_, annP := session(t, m, ann.ID, MethodPasskey)
	if _, err := m.SetPersonEmail(ctx, annP, OwnerPerson, "mallory@example.net"); !errors.Is(err, ErrOwnerEmail) {
		t.Fatalf("admin session sets the owner's email: %v", err)
	}
	if p, err := m.SetPersonEmail(ctx, annP, ann.ID, "ann@example.org"); err != nil || p.Email != "ann@example.org" {
		t.Fatalf("admin session sets own email: %v %v", p, err)
	}
	// The owner, in a confirmed session or with the owner token.
	_, ownerP := session(t, m, OwnerPerson, MethodTerminal)
	if p, err := m.SetPersonEmail(ctx, ownerP, OwnerPerson, "me@example.com"); err != nil || p.Email != "me@example.com" {
		t.Fatalf("owner session sets own email: %v %v", p, err)
	}
	if p, err := m.SetPersonEmail(ctx, owner, ann.ID, "ann@example.com"); err != nil || p.Email != "ann@example.com" {
		t.Fatalf("owner token sets Ann's email: %v %v", p, err)
	}
	if got, _ := m.GetPerson(ctx, OwnerPerson); got.Email != "me@example.com" {
		t.Fatalf("owner's email: %q", got.Email)
	}
}
