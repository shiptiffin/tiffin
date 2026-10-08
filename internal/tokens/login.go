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

// The hand-off is closed once and for good: a kv row, so pruning old
// sessions never reopens it.
const handoffNS, handoffKey = "tokens", "handoff_done"

// HandoffDone reports whether the owner has signed in for real: a session
// of the owner's (from a bootstrap link, their own `tiffin login`, an emailed
// link, a passkey) has made an authenticated request after the one that
// created it. Only then is the box in its owner's hands and the control
// plane's one-time authority over. A redeemed link whose answer never reached
// the browser doesn't count, so a replacement link can still be made.
func (m *Manager) HandoffDone(ctx context.Context) (bool, error) {
	return handoffDone(ctx, m.db.SQL())
}

// dbtx is what both *sql.DB and *sql.Tx offer.
type dbtx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func handoffDone(ctx context.Context, q dbtx) (bool, error) {
	var n int
	// The second count: boxes whose owner signed in before the marker existed.
	err := q.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM kv WHERE ns = ? AND key = ?)
		+ (SELECT count(*) FROM tokens WHERE kind = ? AND person = ? AND last_used_at IS NOT NULL)`,
		handoffNS, handoffKey, KindHuman, OwnerPerson).Scan(&n)
	return n > 0, err
}

// expireHandoffLinks ends every unspent bootstrap (hand-off) link: the
// owner token's links for the owner that last longer than a `tiffin login`
// one (those are the owner's own, minutes long, and stay).
func (m *Manager) expireHandoffLinks(ctx context.Context, q dbtx, now time.Time) error {
	rows, err := q.QueryContext(ctx, `SELECT l.hash, l.created_at, l.expires_at FROM login_links l JOIN tokens t ON t.id = l.created_by
		WHERE t.kind = ? AND l.used_at IS NULL AND (l.person = ? OR l.person IS NULL)`, KindOwner, OwnerPerson)
	if err != nil {
		return err
	}
	var old [][]byte
	for rows.Next() {
		var h []byte
		var created, expires string
		if err := rows.Scan(&h, &created, &expires); err != nil {
			rows.Close()
			return err
		}
		if e := parseTS(expires); e.After(now) && e.Sub(parseTS(created)) > LoginLinkTTL {
			old = append(old, h)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, h := range old {
		if _, err := q.ExecContext(ctx, `UPDATE login_links SET expires_at = ? WHERE hash = ?`, ts(&now), h); err != nil {
			return err
		}
	}
	return nil
}

// closeHandoff records the owner's first real sign-in (session id's first
// authenticated request after the one that made it) and ends every
// outstanding hand-off link, in one transaction. Hand-off links are minted
// in a transaction too (HandoffLink), and writers serialize: a link minted
// just before this is ended by it, one asked for after it is refused.
func (m *Manager) closeHandoff(ctx context.Context, id string, now time.Time) error {
	tx, err := m.db.SQL().BeginTx(ctx, nil) // immediate: writers serialize here
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE tokens SET last_used_at = ? WHERE id = ?`, ts(&now), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO kv(ns, key, value) VALUES (?, ?, ?) ON CONFLICT(ns, key) DO NOTHING`,
		handoffNS, handoffKey, []byte(id+" "+ts(&now).(string))); err != nil {
		return err
	}
	if err := m.expireHandoffLinks(ctx, tx, now); err != nil {
		return err
	}
	return tx.Commit()
}

// ErrHandoffDone refuses a hand-off link once the owner has signed in.
var ErrHandoffDone = fmt.Errorf("%w: the owner has signed in already; no more hand-off links", ErrForbidden)

// HandoffLink makes a fresh bootstrap link (see BootstrapLink) with the
// box's own owner token, for a managed box's control plane to hand to the
// customer: when the dashboard first answers over HTTPS, or when they ask
// for a new one. Only until the owner first signs in (HandoffDone); and the
// earlier hand-off links that were never used stop working, so only the
// newest one does. The check and the mint are one transaction, serialized
// with closeHandoff, so no link outlives the hand-off.
func (m *Manager) HandoffLink(ctx context.Context, ttl time.Duration) (string, time.Time, error) {
	if ttl <= 0 || ttl > BootstrapTTL {
		return "", time.Time{}, fmt.Errorf("%w: a bootstrap link lasts at most %s", ErrInvalid, BootstrapTTL)
	}
	tx, err := m.db.SQL().BeginTx(ctx, nil) // immediate: writers serialize here
	if err != nil {
		return "", time.Time{}, err
	}
	defer tx.Rollback()
	if done, err := handoffDone(ctx, tx); err != nil {
		return "", time.Time{}, err
	} else if done {
		return "", time.Time{}, ErrHandoffDone
	}
	var ownerID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM tokens WHERE kind = ? AND revoked_at IS NULL ORDER BY created_at DESC LIMIT 1`, KindOwner).Scan(&ownerID); err != nil {
		return "", time.Time{}, fmt.Errorf("the box has no owner token: %w", err)
	}
	now := m.now().UTC()
	if err := m.expireHandoffLinks(ctx, tx, now); err != nil {
		return "", time.Time{}, err
	}
	code, exp, err := m.addLink(ctx, tx, ownerID, OwnerPerson, ttl)
	if err != nil {
		return "", time.Time{}, err
	}
	if err := tx.Commit(); err != nil {
		return "", time.Time{}, err
	}
	_ = m.db.Audit(ctx, ownerID, "login_link.create", OwnerPerson, map[string]any{"expiresAt": exp, "handoff": true})
	return code, exp, nil
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
//
// Spending the link and creating the session are one transaction: a link is
// never used up without the session it was for.
func (m *Manager) RedeemLoginLink(ctx context.Context, code string) (string, *Token, string, error) {
	code = strings.TrimSpace(code)
	if !strings.HasPrefix(code, loginPrefix) {
		return "", nil, "", ErrUnauthenticated
	}
	now := m.now().UTC()
	var createdBy string
	var personID sql.NullString
	if err := m.db.SQL().QueryRowContext(ctx, `SELECT created_by, person FROM login_links WHERE hash = ? AND used_at IS NULL AND expires_at > ?`,
		hash(code), ts(&now)).Scan(&createdBy, &personID); err != nil {
		return "", nil, "", ErrUnauthenticated
	}
	// A link refused for who it's for or who made it is spent all the same.
	burn := func() {
		_, _ = m.db.SQL().ExecContext(ctx, `UPDATE login_links SET used_at = ? WHERE hash = ? AND used_at IS NULL`, ts(&now), hash(code))
	}
	pid := personID.String
	if pid == "" {
		pid = OwnerPerson
	}
	person, err := m.GetPerson(ctx, pid)
	if err != nil || person.DisabledAt != nil {
		burn()
		return "", nil, "", ErrUnauthenticated
	}
	method := MethodEmail // asked for by email: the person proved they read that inbox
	if createdBy != emailLinkCreator {
		creator, err := m.Get(ctx, createdBy)
		if err != nil || !m.linkAuthority(ctx, creator, person.ID) {
			burn()
			return "", nil, "", ErrUnauthenticated
		}
		// The owner token signing the owner in (their own `tiffin login`) is as
		// strong as a passkey: that token adds passkeys and keys without asking.
		// A link made by a session (even the owner's) or for anyone else is not.
		method = MethodLink
		if creator.Kind == KindOwner && person.ID == OwnerPerson {
			method = MethodTerminal
		}
	}
	tx, err := m.db.SQL().BeginTx(ctx, nil) // immediate: writers serialize here
	if err != nil {
		return "", nil, "", err
	}
	defer tx.Rollback()
	// Mark used in the statement that checks it, so a code works once.
	res, err := tx.ExecContext(ctx, `UPDATE login_links SET used_at = ?
		WHERE hash = ? AND used_at IS NULL AND expires_at > ? AND created_by = ?`,
		ts(&now), hash(code), ts(&now), createdBy)
	if err != nil {
		return "", nil, "", err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return "", nil, "", ErrUnauthenticated
	}
	secret, t := m.newSession(person)
	if err := m.insert(ctx, tx, t, secret, person.Role); err != nil {
		return "", nil, "", err
	}
	if err := tx.Commit(); err != nil {
		return "", nil, "", err
	}
	m.sessionMinted(ctx, t, method)
	return secret, t, method, nil
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
