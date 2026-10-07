package tokens

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestEmailLoginLink(t *testing.T) {
	m, owner, _ := setup(t)
	ctx := context.Background()
	maya, err := m.AddPerson(ctx, owner, "Maya", "maya@example.com", RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddPerson(ctx, owner, "Other", "MAYA@example.com", RoleViewer); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a second person with the same address: %v", err)
	}
	if p, err := m.PersonByEmail(ctx, "  Maya@Example.com "); err != nil || p.ID != maya.ID {
		t.Fatalf("lookup: %v %v", p, err)
	}
	if _, err := m.PersonByEmail(ctx, "nobody@example.com"); !errors.Is(err, ErrPersonNotFound) {
		t.Fatalf("unknown address: %v", err)
	}

	first, exp, err := m.EmailLoginLink(ctx, maya.ID)
	if err != nil || time.Until(exp) > EmailLinkTTL || time.Until(exp) < EmailLinkTTL-time.Minute {
		t.Fatalf("link: %v %v", exp, err)
	}
	second, _, err := m.EmailLoginLink(ctx, maya.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := m.RedeemLoginLink(ctx, first); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("an older emailed link must stop working: %v", err)
	}
	secret, tok, _, err := m.RedeemLoginLink(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Person != maya.ID || tok.Sponsor != "" || tok.Kind != KindHuman {
		t.Fatalf("session: %+v", tok)
	}
	p, err := m.Authenticate(ctx, secret)
	if err != nil || p.Has(ScopeApplyIrreversible) || !p.Has(ScopeApplyOutbound) {
		t.Fatalf("member session: %+v %v", p, err)
	}
	if _, _, _, err := m.RedeemLoginLink(ctx, second); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("a link works once: %v", err)
	}

	// Expired links don't work.
	third, _, _ := m.EmailLoginLink(ctx, maya.ID)
	m.now = func() time.Time { return time.Now().Add(EmailLinkTTL + time.Second) }
	if _, _, _, err := m.RedeemLoginLink(ctx, third); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expired link: %v", err)
	}
	m.now = time.Now

	// A removed person's link does nothing.
	fourth, _, _ := m.EmailLoginLink(ctx, maya.ID)
	if err := m.RemovePerson(ctx, owner, maya.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := m.RedeemLoginLink(ctx, fourth); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("removed person: %v", err)
	}
	if _, _, err := m.EmailLoginLink(ctx, maya.ID); !errors.Is(err, ErrPersonNotFound) {
		t.Fatalf("removed person gets no link: %v", err)
	}
}

func TestSetPersonEmail(t *testing.T) {
	m, owner, _ := setup(t)
	ctx := context.Background()
	ann, _ := m.AddPerson(ctx, owner, "Ann", "", RoleMember)
	bob, _ := m.AddPerson(ctx, owner, "Bob", "bob@example.com", RoleMember)
	sec, _, _, err := m.SessionFor(ctx, ann.ID, "test")
	if err != nil {
		t.Fatal(err)
	}
	annP, _ := m.Authenticate(ctx, sec)
	// A session that hasn't proved who is behind it lately can't change it.
	if _, err := m.SetPersonEmail(ctx, annP, ann.ID, "ann@example.com"); !errors.Is(err, ErrReauth) || !errors.Is(err, ErrReauthEmail) {
		t.Fatalf("set own email without confirming: %v", err)
	}
	if _, err := m.ConfirmSession(ctx, annP, ann.ID); err != nil {
		t.Fatal(err)
	}
	if p, err := m.SetPersonEmail(ctx, annP, ann.ID, "ann@example.com"); err != nil || p.Email != "ann@example.com" {
		t.Fatalf("set own email: %v %v", p, err)
	}
	if _, err := m.SetPersonEmail(ctx, annP, bob.ID, "x@example.com"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a member changing someone else's email: %v", err)
	}
	if _, err := m.SetPersonEmail(ctx, owner, ann.ID, "BOB@example.com"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("taken address: %v", err)
	}
	if _, err := m.SetPersonEmail(ctx, owner, ann.ID, "not an address"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad address: %v", err)
	}
	if p, err := m.SetPersonEmail(ctx, owner, OwnerPerson, "owner@example.com"); err != nil || p.Email != "owner@example.com" {
		t.Fatalf("owner email: %v %v", p, err)
	}
}
