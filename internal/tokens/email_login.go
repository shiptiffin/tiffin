package tokens

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
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
// by email at addr. It expires after EmailLinkTTL, and any earlier unspent
// emailed link of theirs stops working, so only the newest email counts. It
// is made only while addr is still their address (ErrPersonNotFound
// otherwise), checked in the same transaction, and a change of address
// cancels it (SetPersonEmail): a link never outlives the inbox it went to.
func (m *Manager) EmailLoginLink(ctx context.Context, personID, addr string) (string, time.Time, error) {
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
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM people WHERE id = ? AND disabled_at IS NULL AND email != '' AND lower(email) = ?`,
		personID, NormEmail(addr)).Scan(&n); err != nil {
		return "", time.Time{}, err
	}
	if n == 0 {
		return "", time.Time{}, ErrPersonNotFound
	}
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

// CancelEmailLinks stops every unspent emailed sign-in link of a person, e.g.
// one whose mail stayed in the box's dev inbox instead of reaching them.
func (m *Manager) CancelEmailLinks(ctx context.Context, personID string) error {
	now := m.now().UTC()
	_, err := m.db.SQL().ExecContext(ctx, `UPDATE login_links SET used_at = ? WHERE person = ? AND created_by = ? AND used_at IS NULL`,
		ts(&now), personID, emailLinkCreator)
	return err
}

// mintSession creates a dashboard session for a person with no sponsor
// (a passkey, or a link they asked for by email). via goes in the audit log.
func (m *Manager) mintSession(ctx context.Context, person *Person, via string) (string, *Token, error) {
	now := m.now().UTC()
	exp := now.Add(SessionTTL)
	t := &Token{ID: ids.New("tok"), Name: person.Name, Kind: KindHuman, Scopes: ScopesFor(person.Role), Projects: []string{"*"},
		CreatedAt: now, ExpiresAt: &exp, Person: person.ID}
	secret := newSecret()
	// Only while they still have that role: a session minted as a demotion
	// lands would otherwise keep the old role's power.
	if err := m.insert(ctx, t, secret, person.Role); err != nil {
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

// ErrEmailByKey refuses an email change from an API key: emailed sign-in
// links go to the address, so a key that could repoint someone's (the
// owner's included) could take over their account. People change addresses
// in the dashboard, where it needs a recent strong sign-in; the owner token
// may still change any.
var ErrEmailByKey = fmt.Errorf("%w: an API key can't change an email address (emailed sign-in links go there); change it in the dashboard, or with the owner token", ErrForbidden)

// ErrOwnerEmail refuses a change to the owner's address by anyone else.
var ErrOwnerEmail = fmt.Errorf("%w: only the owner can change the owner's email", ErrForbidden)

// SetPersonEmail sets or clears a person's address (used for invites, sign-in
// links by email and new sign-in notices). People may set their own; box
// admins may set anyone's but the owner's. An address belongs to one active
// person. A new address is set only from a dashboard session (after a recent
// strong sign-in) or with the owner token: never by an API key. Changing it
// cancels every unspent sign-in link of theirs in the same transaction, since
// those went (or would be shown) to the old address.
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
	// A new address gets that person's emailed sign-in links (a strong
	// sign-in), and the address hears about new keys and passkeys: only the
	// owner token, or a dashboard session with a recent strong sign-in, may
	// change it, and only the owner the owner's.
	if NormEmail(email) != NormEmail(p.Email) {
		switch {
		case by.Kind == KindOwner:
		case !by.IsSession():
			return nil, ErrEmailByKey
		case id == OwnerPerson && by.Person != OwnerPerson:
			return nil, ErrOwnerEmail
		default:
			if err := m.RequireSudo(ctx, by, ErrReauthEmail); err != nil {
				return nil, err
			}
		}
	}
	now := m.now().UTC()
	tx, err := m.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var old string
	err = tx.QueryRowContext(ctx, `SELECT email FROM people WHERE id = ? AND disabled_at IS NULL`, id).Scan(&old)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPersonNotFound
	} else if err != nil {
		return nil, err
	}
	if old != p.Email {
		// Changed while this was checked: the checks above were for another address.
		return nil, fmt.Errorf("%w: %s's email changed meanwhile; try again", ErrInvalid, p.Name)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE people SET email = ? WHERE id = ?`, email, id); err != nil {
		return nil, emailErr(err, email)
	}
	if NormEmail(old) != NormEmail(email) {
		if _, err := tx.ExecContext(ctx, `UPDATE login_links SET used_at = ? WHERE person = ? AND used_at IS NULL`, ts(&now), id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	p.Email = email
	_ = m.db.Audit(ctx, by.TokenID, "person.email", id, map[string]any{"set": email != ""})
	return p, nil
}
