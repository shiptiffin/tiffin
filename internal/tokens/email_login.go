package tokens

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/ids"
)

// Sign-in links people ask for by email ("Email me a sign-in link" on the
// dashboard's login page). They are ordinary login links, stored and spent
// the same way, but nobody sent them: their creator is emailLinkCreator and,
// once redeemed, they mint the same sponsor-less session a passkey gives.

// EmailLinkTTL is how long an emailed sign-in link stays valid.
const EmailLinkTTL = 15 * time.Minute

// emailLinkCreator marks login links a person asked for by email.
const emailLinkCreator = "via:email"

// NormEmail is how addresses are compared: trimmed and lowercased.
func NormEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// PersonByEmail returns the active person with that address (compared
// without case), or ErrPersonNotFound. When two people share an address the
// one added first wins.
func (m *Manager) PersonByEmail(ctx context.Context, email string) (*Person, error) {
	email = NormEmail(email)
	if email == "" {
		return nil, ErrPersonNotFound
	}
	p, err := scanPerson(m.db.SQL().QueryRowContext(ctx, personCols+` FROM people
		WHERE lower(email) = ? AND disabled_at IS NULL ORDER BY created_at LIMIT 1`, email))
	if err != nil {
		return nil, ErrPersonNotFound
	}
	return p, nil
}

// EmailLoginLink makes a one-time sign-in link for a person who asked for one
// by email. It expires after EmailLinkTTL, and any earlier unspent emailed
// link of theirs stops working, so only the newest email counts.
func (m *Manager) EmailLoginLink(ctx context.Context, personID string) (string, time.Time, error) {
	p, err := m.GetPerson(ctx, personID)
	if err != nil {
		return "", time.Time{}, err
	}
	if p.DisabledAt != nil {
		return "", time.Time{}, ErrPersonNotFound
	}
	var b [20]byte
	_, _ = rand.Read(b[:])
	code := loginPrefix + strings.ToLower(b32.EncodeToString(b[:]))
	now := m.now().UTC()
	exp := now.Add(EmailLinkTTL)
	tx, err := m.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE login_links SET used_at = ? WHERE person = ? AND created_by = ? AND used_at IS NULL`,
		ts(&now), personID, emailLinkCreator); err != nil {
		return "", time.Time{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO login_links(hash, created_by, created_at, expires_at, person) VALUES (?, ?, ?, ?, ?)`,
		hash(code), emailLinkCreator, ts(&now), ts(&exp), personID); err != nil {
		return "", time.Time{}, err
	}
	if err := tx.Commit(); err != nil {
		return "", time.Time{}, err
	}
	_ = m.db.Audit(ctx, emailLinkCreator, "login_link.email", personID, map[string]any{"expiresAt": exp})
	return code, exp, nil
}

// mintSession creates a dashboard session for a person with no sponsor
// (a passkey, or a link they asked for by email). via goes in the audit log.
func (m *Manager) mintSession(ctx context.Context, person *Person, via string) (string, *Token, error) {
	now := m.now().UTC()
	exp := now.Add(SessionTTL)
	t := &Token{ID: ids.New("tok"), Name: person.Name, Kind: KindHuman, Scopes: ScopesFor(person.Role), Projects: []string{"*"},
		CreatedAt: now, ExpiresAt: &exp, Person: person.ID}
	secret := newSecret()
	if err := m.insert(ctx, t, secret); err != nil {
		return "", nil, err
	}
	_ = m.db.Audit(ctx, t.ID, "token.create", t.ID, map[string]any{"name": t.Name, "kind": t.Kind, "scopes": t.Scopes, "projects": t.Projects, "expiresAt": t.ExpiresAt, "via": via})
	return secret, t, nil
}

// checkEmail validates an optional person address.
func checkEmail(email string) error {
	if email == "" {
		return nil
	}
	if a, err := mail.ParseAddress(email); err != nil || a.Address != email {
		return fmt.Errorf("%w: %q is not an email address", ErrInvalid, email)
	}
	return nil
}

// emailTaken reports whether another active person already has the address.
func (m *Manager) emailTaken(ctx context.Context, email, except string) (bool, error) {
	if email == "" {
		return false, nil
	}
	var n int
	err := m.db.SQL().QueryRowContext(ctx, `SELECT count(*) FROM people WHERE lower(email) = ? AND disabled_at IS NULL AND id != ?`,
		NormEmail(email), except).Scan(&n)
	return n > 0, err
}

// SetPersonEmail sets or clears a person's address (used for invites, sign-in
// links by email and new sign-in notices). People may set their own; box
// admins may set anyone's. An address belongs to one active person.
func (m *Manager) SetPersonEmail(ctx context.Context, by *Principal, id, email string) (*Person, error) {
	if !by.BoxAdmin() && (by.Person == "" || by.Person != id) {
		return nil, fmt.Errorf("%w: only an owner or an admin can change someone else's email", ErrForbidden)
	}
	email = strings.TrimSpace(email)
	if err := checkEmail(email); err != nil {
		return nil, err
	}
	p, err := m.GetPerson(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.DisabledAt != nil {
		return nil, ErrPersonNotFound
	}
	if taken, err := m.emailTaken(ctx, email, id); err != nil {
		return nil, err
	} else if taken {
		return nil, fmt.Errorf("%w: someone on this box already uses %s", ErrInvalid, email)
	}
	if _, err := m.db.SQL().ExecContext(ctx, `UPDATE people SET email = ? WHERE id = ?`, email, id); err != nil {
		return nil, err
	}
	p.Email = email
	_ = m.db.Audit(ctx, by.TokenID, "person.email", id, map[string]any{"set": email != ""})
	return p, nil
}
