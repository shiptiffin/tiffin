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

// ErrKeySignIn refuses a dashboard sign-in link to an API key: a key acts for
// nobody, so there is no one to sign in as.
var ErrKeySignIn = fmt.Errorf("%w: an API key acts for nobody, so it can't sign in to the dashboard; use the owner token (`tiffin login`) or ask an owner or admin for a link", ErrForbidden)

// CreateLoginLink returns a one-time code that a browser exchanges for a
// dashboard session as the caller's own person: the owner token (and an
// owner session) signs in as the owner, a person's session as that person.
func (m *Manager) CreateLoginLink(ctx context.Context, by *Principal) (string, time.Time, error) {
	if by.Person == "" {
		return "", time.Time{}, ErrKeySignIn
	}
	return m.LoginLinkFor(ctx, by, by.Person)
}

// BootstrapTTL is the longest a bootstrap sign-in link lives.
const BootstrapTTL = 24 * time.Hour

// BootstrapLink makes the one sign-in link a box hands to whoever set it up
// for someone else (ShipTiffin's control plane, for a managed box): a
// one-time link that signs the owner in, made by the owner token, valid for
// ttl (at most BootstrapTTL). The box enforces both: the link works once and
// never after it expires, even if a copy of it leaks later. The owner token
// itself never leaves the box. Only the owner token may make one.
func (m *Manager) BootstrapLink(ctx context.Context, by *Principal, ttl time.Duration) (string, time.Time, error) {
	if by == nil || by.Kind != KindOwner {
		return "", time.Time{}, fmt.Errorf("%w: only the owner token makes a bootstrap sign-in link", ErrForbidden)
	}
	if ttl <= 0 || ttl > BootstrapTTL {
		return "", time.Time{}, fmt.Errorf("%w: a bootstrap link lasts at most %s", ErrInvalid, BootstrapTTL)
	}
	return m.insertLink(ctx, by.TokenID, OwnerPerson, ttl)
}

// HandoffDone reports whether the owner has ever signed in with a link
// (a bootstrap link, their own `tiffin login`, an emailed one): the box is
// in its owner's hands, and the control plane's one-time authority is over.
func (m *Manager) HandoffDone(ctx context.Context) (bool, error) {
	var n int
	err := m.db.SQL().QueryRowContext(ctx, `SELECT count(*) FROM login_links WHERE used_at IS NOT NULL AND (person = ? OR person IS NULL)`, OwnerPerson).Scan(&n)
	return n > 0, err
}

// ErrHandoffDone refuses a hand-off link once the owner has signed in.
var ErrHandoffDone = fmt.Errorf("%w: the owner has signed in already; no more hand-off links", ErrForbidden)

// HandoffLink makes a fresh bootstrap link (see BootstrapLink) with the
// box's own owner token, for a managed box's control plane to hand to the
// customer: when the dashboard first answers over HTTPS, or when they ask
// for a new one. Only until the owner first signs in (HandoffDone); and the
// earlier hand-off links that were never used stop working, so only the
// newest one does.
func (m *Manager) HandoffLink(ctx context.Context, ttl time.Duration) (string, time.Time, error) {
	if done, err := m.HandoffDone(ctx); err != nil {
		return "", time.Time{}, err
	} else if done {
		return "", time.Time{}, ErrHandoffDone
	}
	var ownerID string
	if err := m.db.SQL().QueryRowContext(ctx, `SELECT id FROM tokens WHERE kind = ? AND revoked_at IS NULL ORDER BY created_at DESC LIMIT 1`, KindOwner).Scan(&ownerID); err != nil {
		return "", time.Time{}, fmt.Errorf("the box has no owner token: %w", err)
	}
	now := m.now().UTC()
	rows, err := m.db.SQL().QueryContext(ctx, `SELECT l.hash, l.created_at, l.expires_at FROM login_links l JOIN tokens t ON t.id = l.created_by
		WHERE t.kind = ? AND l.used_at IS NULL AND (l.person = ? OR l.person IS NULL)`, KindOwner, OwnerPerson)
	if err != nil {
		return "", time.Time{}, err
	}
	var old [][]byte
	for rows.Next() {
		var h []byte
		var created, expires string
		if err := rows.Scan(&h, &created, &expires); err != nil {
			rows.Close()
			return "", time.Time{}, err
		}
		// Bootstrap links only (a day long), not a `tiffin login` link (minutes).
		if e := parseTS(expires); e.After(now) && e.Sub(parseTS(created)) > LoginLinkTTL {
			old = append(old, h)
		}
	}
	rows.Close()
	for _, h := range old {
		if _, err := m.db.SQL().ExecContext(ctx, `UPDATE login_links SET expires_at = ? WHERE hash = ?`, ts(&now), h); err != nil {
			return "", time.Time{}, err
		}
	}
	return m.BootstrapLink(ctx, &Principal{TokenID: ownerID, Kind: KindOwner}, ttl)
}

// RedeemLoginLink spends a login code (once) and mints a dashboard session for
// the person it was made for: their own role, SessionTTL, sponsored by nobody
// (the session is theirs, not the sender's). It also says how the session
// signed in: MethodEmail for a link the person asked to be emailed,
// MethodTerminal for one the owner token made for the owner (the owner's own
// `tiffin login`), MethodLink for any other.
//
// Whoever made the link must still be allowed to (see linkAuthority): an
// invite outlives the session that sent it, but not its sender's access.
func (m *Manager) RedeemLoginLink(ctx context.Context, code string) (string, *Token, string, error) {
	code = strings.TrimSpace(code)
	if !strings.HasPrefix(code, loginPrefix) {
		return "", nil, "", ErrUnauthenticated
	}
	now := m.now().UTC()
	var createdBy string
	var personID sql.NullString
	// Mark used in the same statement that checks it, so a code works once.
	err := m.db.SQL().QueryRowContext(ctx, `UPDATE login_links SET used_at = ?
		WHERE hash = ? AND used_at IS NULL AND expires_at > ? RETURNING created_by, person`,
		ts(&now), hash(code), ts(&now)).Scan(&createdBy, &personID)
	if err != nil {
		return "", nil, "", ErrUnauthenticated
	}
	pid := personID.String
	if pid == "" {
		pid = OwnerPerson
	}
	person, err := m.GetPerson(ctx, pid)
	if err != nil || person.DisabledAt != nil {
		return "", nil, "", ErrUnauthenticated
	}
	if createdBy == emailLinkCreator {
		// Asked for by email: the person proved they read that inbox.
		secret, t, err := m.mintSession(ctx, person, MethodEmail)
		return secret, t, MethodEmail, err
	}
	creator, err := m.Get(ctx, createdBy)
	if err != nil || !m.linkAuthority(ctx, creator, person.ID) {
		return "", nil, "", ErrUnauthenticated
	}
	// The owner token signing the owner in (their own `tiffin login`) is as
	// strong as a passkey: that token adds passkeys and keys without asking.
	// A link made by a session (even the owner's) or for anyone else is not.
	method := MethodLink
	if creator.Kind == KindOwner && person.ID == OwnerPerson {
		method = MethodTerminal
	}
	secret, t, err := m.mintSession(ctx, person, method)
	return secret, t, method, err
}

// linkAuthority reports whether a link's maker may still sign target in when
// the link is spent:
//
//   - the owner token: while it isn't rotated;
//   - a session's link for its own person: while that session is open, so
//     ending a stolen session ends the links it made for itself;
//   - a session's link for someone else (an invite): while its person is
//     still an owner or admin, whether or not that session is still open;
//   - an API key's: while the key works and has full access to all projects.
//
// Only the owner signs the owner in.
func (m *Manager) linkAuthority(ctx context.Context, creator *Token, target string) bool {
	now := m.now().UTC()
	live := creator.RevokedAt == nil && (creator.ExpiresAt == nil || now.Before(*creator.ExpiresAt))
	switch {
	case creator.Kind == KindOwner:
		return live
	case creator.Kind == KindHuman && creator.Person != "":
		if creator.Person == target {
			return live
		}
		if target == OwnerPerson {
			return false
		}
		p, err := m.GetPerson(ctx, creator.Person)
		return err == nil && p.DisabledAt == nil && (p.Role == RoleOwner || p.Role == RoleAdmin)
	default: // an API key
		k := &Principal{Scopes: creator.Scopes, Projects: creator.Projects}
		return live && k.BoxAdmin() && target != OwnerPerson
	}
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
