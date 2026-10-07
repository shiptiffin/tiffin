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
// owner ends the owner's. API keys are not sessions and are not listed here.

// Sign-in methods.
const (
	MethodLink    = "link"    // a one-time link from an admin, an invite or `tiffin login`
	MethodEmail   = "email"   // a link the person asked for on the login page
	MethodPasskey = "passkey" // Touch ID, Face ID, Windows Hello…
	MethodGoogle  = "google"
	MethodGitHub  = "github"
)

// SessionHistory is how far back ended and expired sessions are listed.
const SessionHistory = 30 * 24 * time.Hour

// Client is how and where a session signed in.
type Client struct {
	Method  string `json:"method,omitempty"`
	Device  string `json:"device,omitempty"`  // "Chrome on macOS"
	IP      string `json:"ip,omitempty"`      // the address it signed in from
	Country string `json:"country,omitempty"` // that address's country, "" when unknown
}

// Session is one dashboard sign-in.
type Session struct {
	ID         string     `json:"id" doc:"The session's token ID"`
	Person     string     `json:"person"`
	Method     string     `json:"method" enum:"link,email,passkey,google,github," doc:"How it signed in: link (a one-time link from an admin, an invite or tiffin login), email (a link asked for on the login page), passkey, google or github. Empty for sessions from before this was kept."`
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

// EndSession signs one session out at once, with any API keys it made (and
// keys those made): a stolen session's keys go with it. It returns the session.
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
	keys, err := m.revokeSessions(ctx, []string{id})
	if err != nil {
		return nil, err
	}
	now := m.now().UTC()
	s.EndedAt, s.State = &now, "ended"
	_ = m.db.Audit(ctx, by.TokenID, "session.end", id, map[string]any{
		"summary": fmt.Sprintf("%s ended a session of %s (%s)", byName(by), p.Name, s.Device),
		"person":  p.ID, "device": s.Device, "method": s.Method, "keys": keys, "self": s.Current,
	})
	return s, nil
}

// EndOtherSessions signs out every open session of a person ("" means by's
// own) except the one making the request, with the API keys they made. It
// returns how many sessions ended.
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
	keys, err := m.revokeSessions(ctx, ids)
	if err != nil {
		return 0, err
	}
	summary := fmt.Sprintf("%s signed out %s everywhere else", byName(by), p.Name)
	if p.ID == by.Person {
		summary = byName(by) + " signed out everywhere else"
	}
	_ = m.db.Audit(ctx, by.TokenID, "session.end_others", p.ID, map[string]any{
		"summary": summary, "sessions": ids, "keys": keys, "kept": by.TokenID,
	})
	return len(ids), nil
}

// revokeSessions revokes the sessions and every API key below them (keys
// they made, keys those made). It stops at other sessions: a person signed in
// with a link someone else sent keeps their session. It returns how many
// keys went too.
func (m *Manager) revokeSessions(ctx context.Context, ids []string) (int64, error) {
	roots, _ := json.Marshal(ids)
	now := m.now().UTC()
	res, err := m.db.SQL().ExecContext(ctx, `WITH RECURSIVE tree(id) AS (
			SELECT value FROM json_each(?1)
			UNION SELECT t.id FROM tokens t JOIN tree ON t.sponsor = tree.id
				WHERE NOT (t.kind = 'human' AND t.person IS NOT NULL))
		UPDATE tokens SET revoked_at = ?2 WHERE id IN (SELECT id FROM tree) AND revoked_at IS NULL`, string(roots), ts(&now))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return max(n-int64(len(ids)), 0), nil
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
