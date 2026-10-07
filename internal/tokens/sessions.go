package tokens

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

// Dashboard sessions are human tokens with a person (a passkey, a link, an
// emailed link, Google or GitHub each mint one; see login.go). Each keeps how
// and where it signed in (the client column) and when it was last used
// (last_used_at, written at most once a minute by Authenticate). People can
// see and end their own; owners and admins anyone's, except that only the
// owner ends the owner's. API keys are not sessions and are not listed here:
// a key made in a session is its own credential and outlives it (keys.go).

// Sign-in methods.
const (
	MethodLink    = "link"    // a one-time link someone made: an invite, an admin's link
	MethodEmail   = "email"   // a link the person asked for on the login page
	MethodPasskey = "passkey" // Touch ID, Face ID, Windows Hello…
	MethodGoogle  = "google"
	MethodGitHub  = "github"
	// MethodTerminal is the owner's own `tiffin login`: a link made with the
	// owner token for the owner.
	MethodTerminal = "terminal"
)

// SessionHistory is how far back ended and expired sessions are listed.
const SessionHistory = 30 * 24 * time.Hour

// Client is how and where a session signed in.
type Client struct {
	Method  string `json:"method,omitempty"`
	Device  string `json:"device,omitempty"`  // "Chrome on macOS"
	IP      string `json:"ip,omitempty"`      // the address it signed in from
	Country string `json:"country,omitempty"` // that address's country, "" when unknown
	// ConfirmedAt is when the person last confirmed it was them in this
	// session (a passkey), for actions that need a recent strong sign-in.
	ConfirmedAt *time.Time `json:"confirmedAt,omitempty"`
}

// Session is one dashboard sign-in.
type Session struct {
	ID         string     `json:"id" doc:"The session's token ID"`
	Person     string     `json:"person"`
	Method     string     `json:"method" enum:"link,email,passkey,google,github,terminal," doc:"How it signed in: link (a one-time link someone made: an invite or an admin's link), email (a link asked for on the login page), passkey, google, github, or terminal (the owner's own tiffin login, with the owner token). Empty for sessions from before this was kept."`
	Device     string     `json:"device" doc:"The browser and system, in words: \"Chrome on macOS\""`
	IP         string     `json:"ip,omitempty" doc:"The address it signed in from"`
	Country    string     `json:"country,omitempty" doc:"The country that address is in, when known"`
	CreatedAt  time.Time  `json:"createdAt" doc:"When it signed in"`
	LastSeenAt *time.Time `json:"lastSeenAt,omitempty" doc:"When it was last used (to the minute)"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	EndedAt    *time.Time `json:"endedAt,omitempty" doc:"When someone signed it out; absent while it is open or once it simply expired"`
	State      string     `json:"state" enum:"active,ended,expired"`
	Current    bool       `json:"current" doc:"The session making this request (this browser)"`
}

// SetClient records how and where session id signed in.
func (m *Manager) SetClient(ctx context.Context, id string, c Client) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = m.db.SQL().ExecContext(ctx, `UPDATE tokens SET client = ? WHERE id = ? AND kind = ?`, string(b), id, KindHuman)
	return err
}

// SessionClient returns how and where session id signed in.
func (m *Manager) SessionClient(ctx context.Context, id string) (*Client, error) {
	var client sql.NullString
	if err := m.db.SQL().QueryRowContext(ctx, `SELECT client FROM tokens WHERE id = ? AND kind = ?`, id, KindHuman).Scan(&client); err != nil {
		return nil, err
	}
	var c Client
	if client.Valid {
		_ = json.Unmarshal([]byte(client.String), &c)
	}
	return &c, nil
}

// SessionPerson returns the person whose sessions by asks about ("" means by's
// own), checking that by may see them or, with end, end them.
func (m *Manager) SessionPerson(ctx context.Context, by *Principal, person string, end bool) (*Person, error) {
	if person == "" {
		person = by.Person
	}
	if person == "" {
		return nil, fmt.Errorf("%w: sessions belong to people, and this key acts for nobody; name a person", ErrInvalid)
	}
	if person != by.Person && !by.BoxAdmin() {
		return nil, fmt.Errorf("%w: only an owner or an admin can see or end someone else's sessions", ErrForbidden)
	}
	p, err := m.GetPerson(ctx, person)
	if err != nil {
		return nil, err
	}
	if end && p.Role == RoleOwner && by.Person != OwnerPerson {
		return nil, fmt.Errorf("%w: only the owner can end the owner's sessions", ErrForbidden)
	}
	return p, nil
}

// Sessions lists a person's dashboard sessions ("" means by's own): open ones
// first, most recently used first; with history, also those that ended or
// expired in the last 30 days.
func (m *Manager) Sessions(ctx context.Context, by *Principal, person string, history bool) ([]*Session, error) {
	p, err := m.SessionPerson(ctx, by, person, false)
	if err != nil {
		return nil, err
	}
	now := m.now().UTC()
	q := `SELECT id, created_at, expires_at, revoked_at, last_used_at, client FROM tokens
		WHERE person = ? AND kind = ? AND created_at >= ?`
	if !history {
		q += ` AND revoked_at IS NULL`
	}
	since := now.Add(-SessionHistory)
	rows, err := m.db.SQL().QueryContext(ctx, q+` ORDER BY created_at DESC LIMIT 500`, p.ID, KindHuman, ts(&since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Session{}
	for rows.Next() {
		var s Session
		var created string
		var expires, revoked, used, client sql.NullString
		if err := rows.Scan(&s.ID, &created, &expires, &revoked, &used, &client); err != nil {
			return nil, err
		}
		s.Person = p.ID
		s.CreatedAt = parseTS(created)
		if e := parseNullTS(expires); e != nil {
			s.ExpiresAt = *e
		}
		s.EndedAt = parseNullTS(revoked)
		s.LastSeenAt = parseNullTS(used)
		if client.Valid {
			var c Client
			_ = json.Unmarshal([]byte(client.String), &c)
			s.Method, s.Device, s.IP, s.Country = c.Method, c.Device, c.IP, c.Country
		}
		if s.Device == "" {
			s.Device = "A browser"
		}
		switch {
		case s.EndedAt != nil:
			s.State = "ended"
		case !s.ExpiresAt.IsZero() && !now.Before(s.ExpiresAt):
			s.State = "expired"
		default:
			s.State = "active"
		}
		if !history && s.State != "active" {
			continue
		}
		s.Current = s.ID == by.TokenID
		out = append(out, &s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	seen := func(s *Session) time.Time {
		if s.LastSeenAt != nil {
			return *s.LastSeenAt
		}
		return s.CreatedAt
	}
	slices.SortStableFunc(out, func(a, b *Session) int {
		if (a.State == "active") != (b.State == "active") {
			if a.State == "active" {
				return -1
			}
			return 1
		}
		if a.State == "active" {
			return seen(b).Compare(seen(a))
		}
		return b.CreatedAt.Compare(a.CreatedAt)
	})
	return out, nil
}

// ErrSessionNotFound is returned for unknown, ended or expired sessions.
var ErrSessionNotFound = errors.New("no open session with that ID")

// EndSession signs one session out at once. API keys made in it keep
// working: they are revoked on the API keys page. It returns the session.
func (m *Manager) EndSession(ctx context.Context, by *Principal, id string) (*Session, error) {
	var person string
	err := m.db.SQL().QueryRowContext(ctx, `SELECT person FROM tokens WHERE id = ? AND kind = ? AND person IS NOT NULL`, id, KindHuman).Scan(&person)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSessionNotFound
	} else if err != nil {
		return nil, err
	}
	p, err := m.SessionPerson(ctx, by, person, true)
	if err != nil {
		if errors.Is(err, ErrPersonNotFound) {
			return nil, ErrSessionNotFound
		}
		return nil, err
	}
	open, err := m.Sessions(ctx, &Principal{TokenID: by.TokenID, Person: p.ID}, p.ID, false)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(open, func(s *Session) bool { return s.ID == id })
	if i < 0 {
		return nil, ErrSessionNotFound
	}
	s := open[i]
	if err := m.revokeSessions(ctx, []string{id}); err != nil {
		return nil, err
	}
	now := m.now().UTC()
	s.EndedAt, s.State = &now, "ended"
	_ = m.db.Audit(ctx, by.TokenID, "session.end", id, map[string]any{
		"summary": fmt.Sprintf("%s ended a session of %s (%s)", byName(by), p.Name, s.Device),
		"person":  p.ID, "device": s.Device, "method": s.Method, "self": s.Current,
	})
	return s, nil
}

// EndOtherSessions signs out every open session of a person ("" means by's
// own) except the one making the request. API keys keep working. It returns
// how many sessions ended.
func (m *Manager) EndOtherSessions(ctx context.Context, by *Principal, person string) (int, error) {
	p, err := m.SessionPerson(ctx, by, person, true)
	if err != nil {
		return 0, err
	}
	open, err := m.Sessions(ctx, &Principal{TokenID: by.TokenID, Person: p.ID}, p.ID, false)
	if err != nil {
		return 0, err
	}
	var ids []string
	for _, s := range open {
		if s.ID != by.TokenID {
			ids = append(ids, s.ID)
		}
	}
	if len(ids) == 0 {
		return 0, nil
	}
	if err := m.revokeSessions(ctx, ids); err != nil {
		return 0, err
	}
	summary := fmt.Sprintf("%s signed out %s everywhere else", byName(by), p.Name)
	if p.ID == by.Person {
		summary = byName(by) + " signed out everywhere else"
	}
	_ = m.db.Audit(ctx, by.TokenID, "session.end_others", p.ID, map[string]any{
		"summary": summary, "sessions": ids, "kept": by.TokenID,
	})
	return len(ids), nil
}

// revokeSessions revokes the sessions only: not the API keys made in them,
// nor anyone's session signed in with a link they sent.
func (m *Manager) revokeSessions(ctx context.Context, ids []string) error {
	list, _ := json.Marshal(ids)
	now := m.now().UTC()
	_, err := m.db.SQL().ExecContext(ctx, `UPDATE tokens SET revoked_at = ?1
		WHERE id IN (SELECT value FROM json_each(?2)) AND kind = 'human' AND revoked_at IS NULL`, ts(&now), string(list))
	return err
}

// IsSession reports whether p is a person's dashboard session (not an API
// key, and not the owner token).
func (p *Principal) IsSession() bool { return p.Kind == KindHuman && p.Person != "" }

// Strong sign-in methods: proof the person holds a passkey, their Google or
// GitHub account, or their inbox; or, for the owner, the owner token (their
// own `tiffin login`), which can add passkeys and keys without asking anyway.
// A link someone else made (an invite, an admin's link, a link made with an
// API key or from a session) is not one.
var strongMethods = []string{MethodPasskey, MethodGoogle, MethodGitHub, MethodEmail, MethodTerminal}

// SudoWindow is how recent a strong sign-in (or a confirmation with a
// passkey) must be for actions that need one, like creating a long-lived key.
const SudoWindow = 10 * time.Minute

// StrongAt is when session id last proved who is behind it: its sign-in, if
// that was strong, or its latest confirmation. Zero for neither.
func (m *Manager) StrongAt(ctx context.Context, id string) (time.Time, error) {
	var created string
	var client sql.NullString
	err := m.db.SQL().QueryRowContext(ctx, `SELECT created_at, client FROM tokens WHERE id = ? AND kind = ? AND revoked_at IS NULL`, id, KindHuman).Scan(&created, &client)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrSessionNotFound
	} else if err != nil {
		return time.Time{}, err
	}
	var c Client
	if client.Valid {
		_ = json.Unmarshal([]byte(client.String), &c)
	}
	var at time.Time
	if slices.Contains(strongMethods, c.Method) {
		at = parseTS(created)
	}
	if c.ConfirmedAt != nil && c.ConfirmedAt.After(at) {
		at = *c.ConfirmedAt
	}
	return at, nil
}

// Confirmed reports whether session id proved who is behind it within SudoWindow.
func (m *Manager) Confirmed(ctx context.Context, id string) bool {
	at, err := m.StrongAt(ctx, id)
	return err == nil && !at.IsZero() && m.now().Sub(at) <= SudoWindow
}

// reauthError is a refusal that asks for a recent strong sign-in: it matches
// ErrReauth (403 reauth_required) but says what it was for.
type reauthError string

func (e reauthError) Error() string        { return string(e) }
func (e reauthError) Is(target error) bool { return target == ErrReauth }

// ErrReauthPasskey asks the person to confirm it's them before adding a
// passkey: a new passkey signs in for good and passes every later
// confirmation, so a stolen session must not be able to add its own.
var ErrReauthPasskey error = reauthError("confirm it's you first: adding a passkey needs a sign-in with a passkey, Google, GitHub or an emailed link in the last 10 minutes")

// ErrReauthEmail asks the person to confirm it's them before changing an
// email address: sign-in links people ask for go there, and such a link is a
// strong sign-in, so a stolen session must not point it at another inbox.
var ErrReauthEmail error = reauthError("confirm it's you first: changing an email address needs a sign-in with a passkey, Google, GitHub or an emailed link in the last 10 minutes")

// RequireSudo returns refusal (an ErrReauth) when p is a dashboard session
// that hasn't proved who is behind it within SudoWindow. API keys and the
// owner token are never asked.
func (m *Manager) RequireSudo(ctx context.Context, p *Principal, refusal error) error {
	if p == nil || !p.IsSession() || m.Confirmed(ctx, p.TokenID) {
		return nil
	}
	return refusal
}

// ConfirmSession records that the person behind session by just proved it
// was them (a passkey of theirs), and returns until when that counts.
func (m *Manager) ConfirmSession(ctx context.Context, by *Principal, person string) (time.Time, error) {
	if !by.IsSession() {
		return time.Time{}, fmt.Errorf("%w: only a dashboard session can be confirmed; API keys never need it", ErrInvalid)
	}
	if person != by.Person {
		return time.Time{}, fmt.Errorf("%w: that passkey belongs to someone else", ErrForbidden)
	}
	now := m.now().UTC()
	if _, err := m.db.SQL().ExecContext(ctx, `UPDATE tokens SET client = json_set(coalesce(client, '{}'), '$.confirmedAt', ?)
		WHERE id = ? AND kind = ? AND revoked_at IS NULL`, now.Format(time.RFC3339Nano), by.TokenID, KindHuman); err != nil {
		return time.Time{}, err
	}
	return now.Add(SudoWindow), nil
}

func byName(by *Principal) string {
	if by.PersonName != "" {
		return by.PersonName
	}
	if by.Kind == KindOwner {
		return "The owner"
	}
	return by.Name
}
