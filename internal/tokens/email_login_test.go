package tokens

import (
	"context"
	"errors"
	"strings"
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

	first, exp, err := m.EmailLoginLink(ctx, maya.ID, "maya@example.com")
	if err != nil || time.Until(exp) > EmailLinkTTL || time.Until(exp) < EmailLinkTTL-time.Minute {
		t.Fatalf("link: %v %v", exp, err)
	}
	second, _, err := m.EmailLoginLink(ctx, maya.ID, "maya@example.com")
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
	third, _, _ := m.EmailLoginLink(ctx, maya.ID, "maya@example.com")
	m.now = func() time.Time { return time.Now().Add(EmailLinkTTL + time.Second) }
	if _, _, _, err := m.RedeemLoginLink(ctx, third); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expired link: %v", err)
	}
	m.now = time.Now

	// A removed person's link does nothing.
	fourth, _, _ := m.EmailLoginLink(ctx, maya.ID, "maya@example.com")
	if err := m.RemovePerson(ctx, owner, maya.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := m.RedeemLoginLink(ctx, fourth); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("removed person: %v", err)
	}
	if _, _, err := m.EmailLoginLink(ctx, maya.ID, "maya@example.com"); !errors.Is(err, ErrPersonNotFound) {
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

// Sessions that ended or expired before Sign-ins' history go when someone
// signs in; open ones, recent ones and ones that made keys stay.
func TestPruneSessions(t *testing.T) {
	m, owner, _ := setup(t)
	ctx := context.Background()
	maya, err := m.AddPerson(ctx, owner, "Maya", "maya@example.com", RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for range 4 {
		_, tok, err := m.mintSession(ctx, maya, MethodEmail)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, tok.ID)
	}
	old := time.Now().Add(-SessionHistory - time.Hour).UTC().Format(time.RFC3339Nano)
	recent := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	db := m.db.SQL()
	for q, args := range map[string][]any{
		`UPDATE tokens SET revoked_at = ? WHERE id = ?`:  {old, ids[0]},    // ended long ago: goes
		`UPDATE tokens SET expires_at = ? WHERE id = ?`:  {old, ids[1]},    // expired long ago, but made a key: stays
		`UPDATE tokens SET revoked_at = ?  WHERE id = ?`: {recent, ids[2]}, // ended an hour ago: stays
	} {
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO tokens(id, name, kind, hash, scopes, projects, sponsor, created_at) VALUES ('tok_child', 'ci', 'agent', x'01', '[]', '[]', ?, ?)`, ids[1], recent); err != nil {
		t.Fatal(err)
	}
	if n, err := m.PruneSessions(ctx); err != nil || n != 1 {
		t.Fatalf("pruned %d, %v; want 1", n, err)
	}
	var left []string
	rows, _ := db.QueryContext(ctx, `SELECT id FROM tokens WHERE person = ? ORDER BY created_at`, maya.ID)
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		left = append(left, id)
	}
	rows.Close()
	if strings.Join(left, " ") != strings.Join(ids[1:], " ") {
		t.Fatalf("left %v, want %v", left, ids[1:])
	}
}
