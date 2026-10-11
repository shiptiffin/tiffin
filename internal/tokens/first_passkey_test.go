package tokens

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A first passkey may be added within FirstPasskeyWindow of a strong
// sign-in; anything else keeps SudoWindow. Links someone else made never
// count, and keys and the owner token are never asked.
func TestFirstPasskeyWindow(t *testing.T) {
	m, owner, _ := setup(t)
	ctx := context.Background()
	ann, _ := m.AddPerson(ctx, owner, "Ann", "ann@example.com", RoleAdmin)
	_, strong := session(t, m, OwnerPerson, MethodTerminal)
	_, link := session(t, m, ann.ID, MethodLink)
	at := func(d time.Duration) { m.now = func() time.Time { return time.Now().Add(d) } }
	t.Cleanup(func() { m.now = time.Now })

	for _, c := range []struct {
		after      time.Duration
		p          *Principal
		hasPasskey bool
		ok         bool
	}{
		{0, strong, false, true},
		{30 * time.Minute, strong, false, true},
		{FirstPasskeyWindow + time.Minute, strong, false, false},
		{5 * time.Minute, strong, true, true},
		{11 * time.Minute, strong, true, false},
		{0, link, false, false},
	} {
		at(c.after)
		err := m.RequirePasskeySudo(ctx, c.p, c.hasPasskey)
		if c.ok != (err == nil) || (err != nil && !errors.Is(err, ErrReauth)) {
			t.Errorf("%s after %s (has a passkey: %v): %v", c.p.Name, c.after, c.hasPasskey, err)
		}
		if open := m.FirstPasskeyOpen(ctx, c.p, c.hasPasskey); open != (c.ok && !c.hasPasskey) {
			t.Errorf("open %s after %s: %v", c.p.Name, c.after, open)
		}
	}
	at(2 * FirstPasskeyWindow)
	if err := m.RequirePasskeySudo(ctx, owner, false); err != nil || m.FirstPasskeyOpen(ctx, owner, false) {
		t.Fatalf("the owner token is never asked, and is no session: %v", err)
	}
}
