package tokens

import (
	"context"
	"errors"
	"testing"
	"time"
)

// session signs person in with method and returns its secret and principal.
func session(t *testing.T, m *Manager, person, method string) (string, *Principal) {
	t.Helper()
	ctx := context.Background()
	sess, tok, _, err := m.SessionFor(ctx, person, method)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetClient(ctx, tok.ID, Client{Method: method, Device: "Chrome on macOS"}); err != nil {
		t.Fatal(err)
	}
	p, err := m.Authenticate(ctx, sess)
	if err != nil {
		t.Fatal(err)
	}
	return sess, p
}

var allFull = KeyRequest{Name: "ci", Projects: Projects{AllProjects}, Access: LevelFull}

// A key made in a dashboard session lives as long as the person chose:
// the session expiring, ending or signing out does not touch it.
func TestSessionKeyOutlivesItsSession(t *testing.T) {
	m, owner, _ := setup(t)
	ctx := context.Background()
	ann, _ := m.AddPerson(ctx, owner, "Ann", "ann@example.com", RoleAdmin)

	_, p := session(t, m, ann.ID, MethodPasskey)
	never, k, err := m.CreateKey(ctx, p, allFull)
	if err != nil || k.ExpiresAt != nil {
		t.Fatalf("never-expiring key: %v %v", k, err)
	}
	req := allFull
	req.Name, req.TTL = "year", 365*24*time.Hour
	year, k, err := m.CreateKey(ctx, p, req)
	if err != nil || k.ExpiresAt == nil || k.ExpiresAt.Sub(time.Now()) < 364*24*time.Hour {
		t.Fatalf("1-year key is capped: %v %v", k, err)
	}

	// The session expires (12 hours); the keys keep working.
	m.now = func() time.Time { return time.Now().Add(SessionTTL + time.Hour) }
	if _, err := m.Authenticate(ctx, never); err != nil {
		t.Fatalf("key died with its session's expiry: %v", err)
	}
	if _, err := m.Authenticate(ctx, year); err != nil {
		t.Fatalf("1-year key died with its session's expiry: %v", err)
	}
	m.now = time.Now

	// Ending the session, signing out everywhere else or signing out (revoke) leaves keys alive.
	sess2, p2 := session(t, m, ann.ID, MethodGoogle)
	k2, _, err := m.CreateKey(ctx, p2, allFull)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.EndSession(ctx, owner, p2.TokenID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Authenticate(ctx, sess2); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("ended session still works: %v", err)
	}
	_, p3 := session(t, m, ann.ID, MethodEmail)
	k3, _, err := m.CreateKey(ctx, p3, allFull)
	if err != nil {
		t.Fatal(err)
	}
	_, p4 := session(t, m, ann.ID, MethodGitHub)
	if n, err := m.EndOtherSessions(ctx, p4, ""); err != nil || n == 0 {
		t.Fatalf("end others: %d %v", n, err)
	}
	if err := m.Revoke(ctx, owner, p4.TokenID); err != nil { // signing out
		t.Fatal(err)
	}
	for i, s := range []string{never, year, k2, k3} {
		if _, err := m.Authenticate(ctx, s); err != nil {
			t.Fatalf("key %d stopped when its session ended: %v", i, err)
		}
	}

	// Revoking a key on the API keys page still works, and takes keys it made.
	kp, _ := m.Authenticate(ctx, k2)
	grand, _, err := m.CreateKey(ctx, kp, KeyRequest{Name: "grand", Projects: Projects{AllProjects}, Access: LevelRead})
	if err != nil {
		t.Fatal(err)
	}
	gk, _ := m.Authenticate(ctx, grand)
	if err := m.Revoke(ctx, owner, kp.TokenID); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{k2, grand} {
		if _, err := m.Authenticate(ctx, s); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("revoked key (or its child %s) still works: %v", gk.Name, err)
		}
	}
}

// Sudo mode: in a dashboard session, a key that lasts longer than a day or
// has full access needs a strong sign-in (passkey, Google, GitHub, emailed
// link) or a passkey confirmation in the last 10 minutes.
func TestSessionKeyNeedsRecentStrongSignIn(t *testing.T) {
	m, owner, _ := setup(t)
	ctx := context.Background()
	ann, _ := m.AddPerson(ctx, owner, "Ann", "", RoleAdmin)
	bob, _ := m.AddPerson(ctx, owner, "Bob", "", RoleAdmin)
	day := 24 * time.Hour
	read := func(ttl time.Duration) KeyRequest {
		return KeyRequest{Name: "r", Projects: Projects{AllProjects}, Access: LevelRead, TTL: ttl}
	}
	full := func(ttl time.Duration) KeyRequest {
		return KeyRequest{Name: "f", Projects: Projects{"shop"}, Access: LevelFull, TTL: ttl}
	}

	// A link someone else made is not a strong sign-in.
	_, link := session(t, m, ann.ID, MethodLink)
	for _, req := range []KeyRequest{full(day), full(0), read(0), read(30 * day), read(day + time.Minute)} {
		if _, _, err := m.CreateKey(ctx, link, req); !errors.Is(err, ErrReauth) {
			t.Fatalf("link session made %s/%s without confirming: %v", req.Access, req.TTL, err)
		}
	}
	if _, k, err := m.CreateKey(ctx, link, read(day)); err != nil || k.ExpiresAt == nil {
		t.Fatalf("a read key for a day needs no confirmation: %v %v", k, err)
	}

	// Confirming with one's own passkey opens 10 minutes; someone else's doesn't.
	if _, err := m.ConfirmSession(ctx, link, bob.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("confirmed with Bob's passkey: %v", err)
	}
	until, err := m.ConfirmSession(ctx, link, ann.ID)
	if err != nil || until.Sub(time.Now()) > SudoWindow+time.Second {
		t.Fatalf("confirm: %v %v", until, err)
	}
	if _, _, err := m.CreateKey(ctx, link, full(0)); err != nil {
		t.Fatalf("confirmed session: %v", err)
	}
	m.now = func() time.Time { return time.Now().Add(SudoWindow + time.Minute) }
	if _, _, err := m.CreateKey(ctx, link, full(0)); !errors.Is(err, ErrReauth) {
		t.Fatalf("confirmation lasted past 10 minutes: %v", err)
	}
	m.now = time.Now

	// Strong sign-ins count for 10 minutes from signing in.
	for _, method := range []string{MethodPasskey, MethodGoogle, MethodGitHub, MethodEmail} {
		_, p := session(t, m, ann.ID, method)
		if _, _, err := m.CreateKey(ctx, p, full(0)); err != nil {
			t.Fatalf("%s sign-in: %v", method, err)
		}
		m.now = func() time.Time { return time.Now().Add(SudoWindow + time.Minute) }
		if _, _, err := m.CreateKey(ctx, p, full(0)); !errors.Is(err, ErrReauth) {
			t.Fatalf("%s sign-in still counted after 10 minutes: %v", method, err)
		}
		m.now = time.Now
	}

	// Keys and the owner token never need it: they are not sessions.
	if _, _, err := m.CreateKey(ctx, owner, full(0)); err != nil {
		t.Fatalf("owner token: %v", err)
	}
	if _, err := m.ConfirmSession(ctx, owner, OwnerPerson); !errors.Is(err, ErrInvalid) {
		t.Fatalf("owner token confirmed: %v", err)
	}
}

// Keys made by keys (agents delegating) never outlive the key that made them.
func TestKeyMadeByKeyIsCapped(t *testing.T) {
	m, owner, _ := setup(t)
	ctx := context.Background()
	parent, _, err := m.CreateKey(ctx, owner, KeyRequest{Name: "agent", Projects: Projects{AllProjects}, Access: LevelFull, TTL: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	pp, _ := m.Authenticate(ctx, parent)
	for _, ttl := range []time.Duration{0, 90 * 24 * time.Hour, 365 * 24 * time.Hour} {
		_, k, err := m.CreateKey(ctx, pp, KeyRequest{Name: "sub", Projects: Projects{"shop"}, Access: LevelRead, TTL: ttl})
		if err != nil || k.ExpiresAt == nil || !k.ExpiresAt.Equal(*pp.ExpiresAt) {
			t.Fatalf("ttl %s: child expires %v, parent %v: %v", ttl, k.ExpiresAt, pp.ExpiresAt, err)
		}
	}
	_, k, _ := m.CreateKey(ctx, pp, KeyRequest{Name: "short", Projects: Projects{"shop"}, Access: LevelRead, TTL: 24 * time.Hour})
	if k.ExpiresAt == nil || k.ExpiresAt.After(*pp.ExpiresAt) || k.ExpiresAt.Sub(time.Now()) > 25*time.Hour {
		t.Fatalf("shorter child: %v", k.ExpiresAt)
	}
}
