package passkeys

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/passkeys/passkeytest"
	"github.com/btahir/tiffin/internal/state"
	"github.com/btahir/tiffin/internal/tokens"
)

const (
	testRP     = "dashboard.tiffin.localhost"
	testOrigin = "https://dashboard.tiffin.localhost:8443"
)

var ownerP = &tokens.Principal{TokenID: "tok_owner", Name: "owner", Kind: tokens.KindOwner, Scopes: []tokens.Scope{tokens.ScopeAll}, Projects: []string{"*"}, Person: tokens.OwnerPerson}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	m, err := New(db, testRP, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// register adds a passkey for p through the real ceremony.
func register(t *testing.T, m *Manager, a *passkeytest.Authenticator, p *tokens.Principal) *passkeytest.Credential {
	t.Helper()
	cc, err := m.BeginRegistration(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	resp, cred, err := a.Create(mustJSON(t, cc))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.FinishRegistration(t.Context(), p, "Laptop", resp); err != nil {
		t.Fatal(err)
	}
	return cred
}

func signIn(t *testing.T, m *Manager, a *passkeytest.Authenticator, c *passkeytest.Credential) (json.RawMessage, *SignIn, error) {
	t.Helper()
	opts, err := m.BeginLogin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := a.Get(mustJSON(t, opts), c)
	if err != nil {
		t.Fatal(err)
	}
	who, err := m.FinishLogin(t.Context(), resp)
	return resp, who, err
}

func TestRegistrationAsksForDiscoverablePasskeys(t *testing.T) {
	m := newTestManager(t)
	cc, err := m.BeginRegistration(t.Context(), ownerP)
	if err != nil {
		t.Fatal(err)
	}
	sel := cc.Response.AuthenticatorSelection
	if sel.ResidentKey != "required" || sel.RequireResidentKey == nil || !*sel.RequireResidentKey || sel.UserVerification != "required" {
		t.Fatalf("authenticator selection: %+v", sel)
	}
}

func TestPasskeySignIn(t *testing.T) {
	m := newTestManager(t)
	a := passkeytest.New(testOrigin)
	cred := register(t, m, a, ownerP)

	opts, err := m.BeginLogin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	r := opts.Response
	if len(r.AllowedCredentials) != 0 || r.UserVerification != "required" || r.RelyingPartyID != testRP || r.Timeout != 120000 || len(r.Challenge) < 16 {
		t.Fatalf("options: %+v", r)
	}

	resp, who, err := signIn(t, m, a, cred)
	if err != nil {
		t.Fatal(err)
	}
	if who.Person != tokens.OwnerPerson || who.PasskeyName != "Laptop" || who.PasskeyID == "" {
		t.Fatalf("signed in as %+v", who)
	}
	// The stored sign count advanced and last_used is set.
	l, _ := m.Passkeys(t.Context(), ownerP)
	if len(l) != 1 || l[0].LastUsed == nil {
		t.Fatalf("passkeys after sign-in: %+v", l)
	}
	// The same assertion cannot be replayed: its challenge is spent.
	if _, err := m.FinishLogin(t.Context(), resp); !errors.Is(err, ErrLoginExpired) {
		t.Fatalf("replay: %v", err)
	}
	// It still works again with a fresh challenge.
	if _, _, err := signIn(t, m, a, cred); err != nil {
		t.Fatalf("second sign-in: %v", err)
	}
}

func TestPasskeySignInRefusals(t *testing.T) {
	m := newTestManager(t)
	a := passkeytest.New(testOrigin)
	cred := register(t, m, a, ownerP)

	t.Run("unknown credential", func(t *testing.T) {
		other := newTestManager(t) // registered on another box
		stranger := register(t, other, a, ownerP)
		if _, _, err := signIn(t, m, a, stranger); !errors.Is(err, ErrUnknownPasskey) {
			t.Fatalf("unknown passkey: %v", err)
		}
	})
	t.Run("user handle of someone else", func(t *testing.T) {
		forged := *cred
		forged.UserHandle = []byte("usr_someone")
		if _, _, err := signIn(t, m, a, &forged); !errors.Is(err, ErrUnknownPasskey) {
			t.Fatalf("forged user handle: %v", err)
		}
	})
	t.Run("no user verification", func(t *testing.T) {
		a.NoUV = true
		defer func() { a.NoUV = false }()
		if _, _, err := signIn(t, m, a, cred); !errors.Is(err, ErrLoginFailed) {
			t.Fatalf("no UV: %v", err)
		}
	})
	t.Run("wrong origin", func(t *testing.T) {
		evil := *a
		evil.Origin = "https://evil.example"
		if _, _, err := signIn(t, m, &evil, cred); !errors.Is(err, ErrLoginFailed) {
			t.Fatalf("wrong origin: %v", err)
		}
	})
	t.Run("counter went backwards", func(t *testing.T) {
		if _, _, err := signIn(t, m, a, cred); err != nil {
			t.Fatal(err)
		}
		clone := *cred
		clone.Counter -= 2 // a copy of the key that lags behind
		if _, _, err := signIn(t, m, a, &clone); !errors.Is(err, ErrCloned) {
			t.Fatalf("cloned: %v", err)
		}
	})
	t.Run("expired challenge", func(t *testing.T) {
		opts, err := m.BeginLogin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		resp, _ := a.Get(mustJSON(t, opts), cred)
		m.now = func() time.Time { return time.Now().Add(LoginChallengeTTL + time.Second) }
		defer func() { m.now = time.Now }()
		if _, err := m.FinishLogin(t.Context(), resp); !errors.Is(err, ErrLoginExpired) {
			t.Fatalf("expired: %v", err)
		}
	})
	t.Run("garbage", func(t *testing.T) {
		if _, err := m.FinishLogin(t.Context(), json.RawMessage(`{"id":"x"}`)); !errors.Is(err, ErrLoginFailed) {
			t.Fatalf("garbage: %v", err)
		}
	})
}

// Challenges cost the box nothing until a passkey signs one, so callers
// asking for many (from many addresses) can't crowd out anyone's sign-in;
// forged or tampered ones are refused.
func TestLoginChallengesAreStateless(t *testing.T) {
	m := newTestManager(t)
	a := passkeytest.New(testOrigin)
	cred := register(t, m, a, ownerP)
	for range 5000 {
		if _, err := m.BeginLogin(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if len(m.spent) != 0 {
		t.Fatalf("challenges kept: %d", len(m.spent))
	}
	if _, _, err := signIn(t, m, a, cred); err != nil {
		t.Fatalf("sign-in after a flood: %v", err)
	}
	// Another box's (another key's) challenge, or a tampered one, is refused.
	other := newTestManager(t)
	opts, _ := other.BeginLogin(t.Context())
	resp, _ := a.Get(mustJSON(t, opts), cred)
	if _, err := m.FinishLogin(t.Context(), resp); !errors.Is(err, ErrLoginExpired) {
		t.Fatalf("foreign challenge: %v", err)
	}
	opts, _ = m.BeginLogin(t.Context())
	opts.Response.Challenge[0] ^= 0xff // a later expiry
	resp, _ = a.Get(mustJSON(t, opts), cred)
	if _, err := m.FinishLogin(t.Context(), resp); !errors.Is(err, ErrLoginExpired) {
		t.Fatalf("tampered challenge: %v", err)
	}
	// Spent challenges are forgotten once expired.
	m.now = func() time.Time { return time.Now().Add(LoginChallengeTTL + time.Second) }
	defer func() { m.now = time.Now }()
	if !m.spend("x", m.now().Add(time.Minute)) || len(m.spent) != 1 {
		t.Fatalf("spent not pruned: %d", len(m.spent))
	}
}

func TestPasskeyHolders(t *testing.T) {
	m := newTestManager(t)
	member := &tokens.Principal{TokenID: "tok_m", Kind: tokens.KindHuman, Scopes: tokens.ScopesFor(tokens.RoleMember), Projects: []string{"*"}, Person: "usr_member"}
	if _, err := m.BeginRegistration(t.Context(), member); err != nil {
		t.Fatalf("member registering: %v", err)
	}
	agent := &tokens.Principal{TokenID: "tok_a", Kind: tokens.KindAgent, Scopes: []tokens.Scope{tokens.ScopeAll}, Projects: []string{"*"}}
	if _, err := m.BeginRegistration(t.Context(), agent); !errors.Is(err, ErrNoPerson) {
		t.Fatalf("agent registering: %v", err)
	}
	// A human token with no person and less than box-admin power would
	// otherwise fall through to the owner's passkeys.
	loose := &tokens.Principal{TokenID: "tok_h", Kind: tokens.KindHuman, Scopes: []tokens.Scope{tokens.ScopeRead}, Projects: []string{"*"}}
	if _, err := m.BeginRegistration(t.Context(), loose); !errors.Is(err, ErrNoPerson) {
		t.Fatalf("personless human registering: %v", err)
	}
	if _, err := m.Passkeys(t.Context(), loose); !errors.Is(err, ErrNoPerson) {
		t.Fatalf("personless human listing: %v", err)
	}
	// A member's passkey signs them in.
	a := passkeytest.New(testOrigin)
	cred := register(t, m, a, member)
	if _, who, err := signIn(t, m, a, cred); err != nil || who.Person != "usr_member" {
		t.Fatalf("member sign-in: %+v %v", who, err)
	}
}
