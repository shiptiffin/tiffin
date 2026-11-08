package tokens

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Every dashboard sign-in uses a persisted, bounded session. A week of
// ordinary access must not extend the ten-minute window for sensitive work.
func TestDashboardSessionLifetime(t *testing.T) {
	for _, method := range []string{MethodLink, MethodEmail, MethodTerminal, MethodPasskey, MethodGoogle, MethodGitHub} {
		t.Run(method, func(t *testing.T) {
			m, owner, _ := setup(t)
			ctx := context.Background()
			start := time.Now().UTC()
			m.now = func() time.Time { return start }
			person, err := m.AddPerson(ctx, owner, "Maya", "maya@example.com", RoleMember)
			if err != nil {
				t.Fatal(err)
			}
			var secret string
			var tok *Token
			switch method {
			case MethodLink, MethodTerminal, MethodEmail:
				var code string
				if method == MethodEmail {
					code, _, err = m.EmailLoginLink(ctx, person.ID, person.Email)
				} else {
					target := person.ID
					if method == MethodTerminal {
						target = OwnerPerson
					}
					code, _, err = m.LoginLinkFor(ctx, owner, target)
				}
				if err == nil {
					secret, tok, _, err = m.RedeemLoginLink(ctx, code)
				}
			default:
				secret, tok, _, err = m.SessionFor(ctx, person.ID, method)
			}
			if err != nil {
				t.Fatal(err)
			}
			end := start.Add(7 * 24 * time.Hour)
			if tok.ExpiresAt == nil || !tok.ExpiresAt.Equal(end) {
				t.Fatalf("session must expire seven days after sign-in: %v", tok.ExpiresAt)
			}
			if err := m.SetClient(ctx, tok.ID, Client{Method: method}); err != nil {
				t.Fatal(err)
			}
			// A fresh manager reads the same stored expiry after a restart.
			restarted := NewManager(m.db)
			for _, elapsed := range []time.Duration{24 * time.Hour, 6 * 24 * time.Hour, 7*24*time.Hour - time.Nanosecond} {
				restarted.now = func() time.Time { return start.Add(elapsed) }
				p, err := restarted.Authenticate(ctx, secret)
				if err != nil {
					t.Fatalf("session stopped working after %s: %v", elapsed, err)
				}
				if !p.ExpiresAt.Equal(end) {
					t.Fatal("ordinary access changed the absolute expiry")
				}
				if err := restarted.RequireSudo(ctx, p, ErrReauthEmail); !errors.Is(err, ErrReauth) {
					t.Fatalf("old session bypassed recent confirmation: %v", err)
				}
			}
			restarted.now = func() time.Time { return end }
			if _, err := restarted.Authenticate(ctx, secret); !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("session works at its expiry: %v", err)
			}
		})
	}
}
