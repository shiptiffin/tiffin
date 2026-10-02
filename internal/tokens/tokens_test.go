package tokens

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/state"
)

func setup(t *testing.T) (*Manager, *Principal, string) {
	t.Helper()
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	m := NewManager(db)
	secret, created, err := m.Bootstrap(context.Background())
	if err != nil || !created {
		t.Fatalf("bootstrap: %v %v", created, err)
	}
	owner, err := m.Authenticate(context.Background(), secret)
	if err != nil {
		t.Fatal(err)
	}
	return m, owner, secret
}

func TestBootstrapOnce(t *testing.T) {
	m, owner, secret := setup(t)
	if owner.Kind != KindOwner || !owner.Has(ScopeApplyIrreversible) || !owner.CanProject("anything") {
		t.Fatalf("owner principal: %+v", owner)
	}
	if len(secret) < 40 || secret[:4] != "tfn_" {
		t.Fatalf("secret format: %q", secret)
	}
	if s, created, err := m.Bootstrap(context.Background()); s != "" || created || err != nil {
		t.Fatalf("second bootstrap must be a no-op: %q %v %v", s, created, err)
	}
}

func TestAuthenticateRejects(t *testing.T) {
	m, _, secret := setup(t)
	ctx := context.Background()
	for _, bad := range []string{"", "tfn_nope", "Bearer x", secret + "x", secret[:len(secret)-1]} {
		if _, err := m.Authenticate(ctx, bad); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := m.Authenticate(ctx, "Bearer "+secret); err != nil {
		t.Errorf("bearer prefix should be accepted: %v", err)
	}
}

func TestScopeLadderAndPolicy(t *testing.T) {
	m, owner, _ := setup(t)
	ctx := context.Background()
	sec, tok, err := m.Create(ctx, owner, CreateRequest{Name: "claude", Projects: []string{"shop"}})
	if err != nil {
		t.Fatal(err)
	}
	if tok.Kind != KindAgent || tok.ExpiresAt == nil || tok.Sponsor != owner.TokenID {
		t.Fatalf("agent defaults: %+v", tok)
	}
	agent, err := m.Authenticate(ctx, sec)
	if err != nil {
		t.Fatal(err)
	}
	if !agent.Has(ScopeRead) || !agent.Has(ScopePlan) || agent.Has(ScopeApplyOutbound) || agent.Has(ScopeTokens) {
		t.Fatalf("agent scopes wrong: %v", agent.Scopes)
	}
	auth := agent.Authorizer()
	if err := auth(&change.Plan{Project: "shop", Risk: change.TierReversible}); err != nil {
		t.Errorf("reversible on own project should pass: %v", err)
	}
	if err := auth(&change.Plan{Project: "shop", Risk: change.TierIrreversible}); !errors.Is(err, ErrForbidden) {
		t.Errorf("irreversible must be forbidden: %v", err)
	}
	if err := auth(&change.Plan{Project: "blog", Risk: change.TierReversible}); !errors.Is(err, ErrForbidden) {
		t.Errorf("other project must be forbidden: %v", err)
	}
	if err := agent.Require(ScopeRead, "blog"); !errors.Is(err, ErrForbidden) {
		t.Errorf("read on other project must be forbidden: %v", err)
	}
	if a := agent.Actor(); a.Kind != "agent" || a.ID != tok.ID || a.Name != "claude" {
		t.Errorf("actor: %+v", a)
	}
}

func TestNoEscalation(t *testing.T) {
	m, owner, _ := setup(t)
	ctx := context.Background()
	// A delegating agent: may mint tokens, but only within its own power.
	sec, _, err := m.Create(ctx, owner, CreateRequest{Name: "lead", Scopes: []Scope{ScopeTokens, ScopeApplyReversible}, Projects: []string{"shop"}})
	if err != nil {
		t.Fatal(err)
	}
	lead, _ := m.Authenticate(ctx, sec)
	cases := []CreateRequest{
		{Name: "x", Scopes: []Scope{ScopeApplyIrreversible}},
		{Name: "x", Scopes: []Scope{ScopeAll}},
		{Name: "x", Projects: []string{"blog"}},
		{Name: "x", Projects: []string{"*"}},
	}
	for _, c := range cases {
		if _, _, err := m.Create(ctx, lead, c); !errors.Is(err, ErrForbidden) {
			t.Errorf("%+v: want forbidden, got %v", c, err)
		}
	}
	// Within power is fine; defaults inherit the sponsor's projects.
	_, child, err := m.Create(ctx, lead, CreateRequest{Name: "helper", Scopes: []Scope{ScopeRead}})
	if err != nil || len(child.Projects) != 1 || child.Projects[0] != "shop" || child.Sponsor != lead.TokenID {
		t.Fatalf("child: %v %+v", err, child)
	}
	// A plain agent without the tokens scope cannot mint at all.
	csec, _, _ := m.Create(ctx, owner, CreateRequest{Name: "plain"})
	plain, _ := m.Authenticate(ctx, csec)
	if _, _, err := m.Create(ctx, plain, CreateRequest{Name: "y"}); !errors.Is(err, ErrForbidden) {
		t.Errorf("no tokens scope: %v", err)
	}
	for _, bad := range []CreateRequest{{Name: ""}, {Name: "x", Scopes: []Scope{"admin"}}, {Name: "x", Kind: "robot"}, {Name: "x", TTL: 400 * 24 * time.Hour}} {
		if _, _, err := m.Create(ctx, owner, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: want invalid, got %v", bad, err)
		}
	}
}

func TestExpiryAndRevokeCascade(t *testing.T) {
	m, owner, ownerSecret := setup(t)
	ctx := context.Background()
	sec, tok, _ := m.Create(ctx, owner, CreateRequest{Name: "short", TTL: time.Hour})
	m.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := m.Authenticate(ctx, sec); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expired token accepted: %v", err)
	}
	m.now = time.Now
	_ = tok

	lsec, lead, _ := m.Create(ctx, owner, CreateRequest{Name: "lead", Scopes: []Scope{ScopeTokens, ScopeRead}})
	leadP, _ := m.Authenticate(ctx, lsec)
	csec, _, _ := m.Create(ctx, leadP, CreateRequest{Name: "child", Scopes: []Scope{ScopeRead}})
	gsecP, _ := m.Authenticate(ctx, csec)
	if gsecP == nil {
		t.Fatal("child should authenticate")
	}
	// Someone who did not sponsor the token cannot revoke it.
	osec, _, _ := m.Create(ctx, owner, CreateRequest{Name: "other", Scopes: []Scope{ScopeTokens, ScopeRead}})
	other, _ := m.Authenticate(ctx, osec)
	if err := m.Revoke(ctx, other, lead.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-sponsor revoke: %v", err)
	}
	if err := m.Revoke(ctx, owner, lead.ID); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{lsec, csec} {
		if _, err := m.Authenticate(ctx, s); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("revoked (or cascaded) token still works: %v", err)
		}
	}
	if err := m.Revoke(ctx, owner, owner.TokenID); !errors.Is(err, ErrForbidden) {
		t.Errorf("owner revoke must be refused: %v", err)
	}
	// Rotation replaces the owner secret.
	newSecret, err := m.RotateOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Authenticate(ctx, ownerSecret); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("old owner secret still valid")
	}
	if _, err := m.Authenticate(ctx, newSecret); err != nil {
		t.Errorf("new owner secret: %v", err)
	}
	active, _ := m.List(ctx, false)
	all, _ := m.List(ctx, true)
	if len(all) <= len(active) {
		t.Errorf("list: active=%d all=%d", len(active), len(all))
	}
}

// A delegating agent must not mint tokens that outlive it, and must not mint
// "human" tokens (which never need to expire and are logged as humans).
func TestMintedTokensCannotOutliveOrOutrankSponsor(t *testing.T) {
	m, owner, _ := setup(t)
	ctx := context.Background()
	sec, lead, err := m.Create(ctx, owner, CreateRequest{Name: "lead", Scopes: []Scope{ScopeTokens, ScopeRead}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	leadP, _ := m.Authenticate(ctx, sec)
	if _, _, err := m.Create(ctx, leadP, CreateRequest{Name: "forever", Kind: KindHuman, Scopes: []Scope{ScopeRead}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("agent minted a human token: %v", err)
	}
	_, child, err := m.Create(ctx, leadP, CreateRequest{Name: "child", Scopes: []Scope{ScopeRead}, TTL: 300 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if child.ExpiresAt == nil || child.ExpiresAt.After(*lead.ExpiresAt) {
		t.Fatalf("child expires %v, after sponsor %v", child.ExpiresAt, lead.ExpiresAt)
	}
	// An expiring human token likewise cannot mint a never-expiring one.
	hsec, h, _ := m.Create(ctx, owner, CreateRequest{Name: "temp-human", Kind: KindHuman, Scopes: []Scope{ScopeTokens, ScopeRead}, TTL: time.Hour})
	hp, _ := m.Authenticate(ctx, hsec)
	_, hc, err := m.Create(ctx, hp, CreateRequest{Name: "h-child", Kind: KindHuman, Scopes: []Scope{ScopeRead}})
	if err != nil {
		t.Fatal(err)
	}
	if hc.ExpiresAt == nil || hc.ExpiresAt.After(*h.ExpiresAt) {
		t.Fatalf("human child expires %v, after sponsor %v", hc.ExpiresAt, h.ExpiresAt)
	}
}
