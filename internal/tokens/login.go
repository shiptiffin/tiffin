package tokens

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// LoginLinkTTL is how long a one-time dashboard login link stays valid.
const LoginLinkTTL = 10 * time.Minute

// SessionTTL is how long a dashboard session lasts.
const SessionTTL = 12 * time.Hour

const loginPrefix = "tfl_"

// CreateLoginLink returns a one-time code that a browser exchanges for a
// dashboard session. Only box admins can create one: the session it yields
// acts with the creator's full power.
func (m *Manager) CreateLoginLink(ctx context.Context, by *Principal) (string, time.Time, error) {
	if !by.BoxAdmin() {
		return "", time.Time{}, fmt.Errorf("%w: only a box admin can create dashboard login links", ErrForbidden)
	}
	person := by.Person
	if person == "" {
		person = OwnerPerson // a box-admin CLI token signs in as the owner
	}
	return m.LoginLinkFor(ctx, &Principal{TokenID: by.TokenID, Name: by.Name, Kind: KindOwner, Scopes: []Scope{ScopeAll}, Projects: []string{"*"}, Person: person}, person)
}

// RedeemLoginLink spends a login code (once) and mints a dashboard session:
// a human token with the creator's scopes that expires after SessionTTL.
func (m *Manager) RedeemLoginLink(ctx context.Context, code string) (string, *Token, error) {
	code = strings.TrimSpace(code)
	if !strings.HasPrefix(code, loginPrefix) {
		return "", nil, ErrUnauthenticated
	}
	now := m.now().UTC()
	var createdBy string
	var personID sql.NullString
	// Mark used in the same statement that checks it, so a code works once.
	err := m.db.SQL().QueryRowContext(ctx, `UPDATE login_links SET used_at = ?
		WHERE hash = ? AND used_at IS NULL AND expires_at > ? RETURNING created_by, person`,
		ts(&now), hash(code), ts(&now)).Scan(&createdBy, &personID)
	if err != nil {
		return "", nil, ErrUnauthenticated
	}
	if createdBy == emailLinkCreator {
		// Asked for by email: the person proved they read that inbox, so
		// they get their own role's session, sponsored by nobody.
		person, err := m.GetPerson(ctx, personID.String)
		if err != nil || person.DisabledAt != nil {
			return "", nil, ErrUnauthenticated
		}
		return m.mintSession(ctx, person, "email")
	}
	creator, err := m.Get(ctx, createdBy)
	if err != nil || creator.RevokedAt != nil || (creator.ExpiresAt != nil && !now.Before(*creator.ExpiresAt)) {
		return "", nil, ErrUnauthenticated
	}
	pid := personID.String
	if pid == "" {
		pid = OwnerPerson
	}
	person, err := m.GetPerson(ctx, pid)
	if err != nil || person.DisabledAt != nil {
		return "", nil, ErrUnauthenticated
	}
	by := &Principal{TokenID: creator.ID, Name: creator.Name, Kind: creator.Kind, Scopes: creator.Scopes, Projects: creator.Projects, ExpiresAt: creator.ExpiresAt}
	// The session gets the person's role, never more than whoever sent the link.
	return m.Create(ctx, by, CreateRequest{Person: person.ID, Name: person.Name, Kind: KindHuman, Scopes: ScopesFor(person.Role), Projects: []string{"*"}, TTL: SessionTTL})
}

// SessionFor mints a dashboard session for a person who proved who they are
// with a passkey: the same session a redeemed login link gives (a human token
// named after them, their role's scopes on every project, SessionTTL), with
// no sponsor because nobody sent a link. Removed people get ErrPersonNotFound.
// via is recorded in the audit log ("passkey").
func (m *Manager) SessionFor(ctx context.Context, personID, via string) (string, *Token, *Person, error) {
	person, err := m.GetPerson(ctx, personID)
	if err != nil {
		return "", nil, nil, err
	}
	if person.DisabledAt != nil {
		return "", nil, nil, ErrPersonNotFound
	}
	secret, t, err := m.mintSession(ctx, person, via)
	if err != nil {
		return "", nil, nil, err
	}
	return secret, t, person, nil
}
