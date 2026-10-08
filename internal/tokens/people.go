package tokens

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/ids"
	"github.com/ncruces/go-sqlite3"
)

// People are the humans who use the box's dashboard. Each has a role; their
// dashboard sessions get exactly that role's power. Agents are not people:
// they get tokens sponsored by a person.

// Roles, from most to least powerful.
const (
	RoleOwner  = "owner"  // everything, including people and the owner token
	RoleAdmin  = "admin"  // everything except changing the owner
	RoleMember = "member" // read, plan and apply reversible and outbound changes
	RoleViewer = "viewer" // read only
)

// Roles lists the assignable roles (owner is not assignable).
var Roles = []string{RoleAdmin, RoleMember, RoleViewer}

// OwnerPerson is the fixed ID of the box owner.
const OwnerPerson = "usr_owner"

// ScopesFor returns the scopes a role grants on every project.
func ScopesFor(role string) []Scope {
	switch role {
	case RoleOwner, RoleAdmin:
		return []Scope{ScopeAll}
	case RoleMember:
		return []Scope{ScopeRead, ScopePlan, ScopeApplyReversible, ScopeApplyOutbound}
	default:
		return []Scope{ScopeRead}
	}
}

// Person is one human.
type Person struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Email      string     `json:"email,omitempty"`
	Role       string     `json:"role" enum:"owner,admin,member,viewer"`
	CreatedAt  time.Time  `json:"createdAt"`
	DisabledAt *time.Time `json:"disabledAt,omitempty"`
}

// ErrPersonNotFound is returned for unknown or disabled people.
var ErrPersonNotFound = errors.New("person not found")

func (m *Manager) ensureOwnerPerson(ctx context.Context) error {
	now := m.now().UTC()
	_, err := m.db.SQL().ExecContext(ctx, `INSERT OR IGNORE INTO people(id, name, email, role, created_at, created_by) VALUES (?, ?, '', ?, ?, 'system')`,
		OwnerPerson, "Owner", RoleOwner, ts(&now))
	return err
}

// GetPerson returns one person.
func (m *Manager) GetPerson(ctx context.Context, id string) (*Person, error) {
	if id == OwnerPerson {
		if err := m.ensureOwnerPerson(ctx); err != nil {
			return nil, err
		}
	}
	p, err := scanPerson(m.db.SQL().QueryRowContext(ctx, personCols+` FROM people WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPersonNotFound
	}
	return p, err
}

// People lists everyone, owner first.
func (m *Manager) People(ctx context.Context) ([]*Person, error) {
	if err := m.ensureOwnerPerson(ctx); err != nil {
		return nil, err
	}
	rows, err := m.db.SQL().QueryContext(ctx, personCols+` FROM people ORDER BY role = 'owner' DESC, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Person{}
	for rows.Next() {
		p, err := scanPerson(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AddPerson invites someone. Only box admins can; nobody can create another owner.
func (m *Manager) AddPerson(ctx context.Context, by *Principal, name, email, role string) (*Person, error) {
	if !by.BoxAdmin() {
		return nil, fmt.Errorf("%w: only an owner, an admin or a key with full access to all projects can add people", ErrForbidden)
	}
	name, email = strings.TrimSpace(name), strings.TrimSpace(email)
	if name == "" || len(name) > 64 {
		return nil, fmt.Errorf("%w: name must be 1-64 characters", ErrInvalid)
	}
	if err := checkEmail(email); err != nil {
		return nil, err
	}
	if !slices.Contains(Roles, role) {
		return nil, fmt.Errorf("%w: role must be admin, member or viewer", ErrInvalid)
	}
	now := m.now().UTC()
	p := &Person{ID: ids.New("usr"), Name: name, Email: email, Role: role, CreatedAt: now}
	// The people_email index keeps an address to one active person, even
	// when two invites race.
	if _, err := m.db.SQL().ExecContext(ctx, `INSERT INTO people(id, name, email, role, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Email, p.Role, ts(&now), by.TokenID); err != nil {
		return nil, emailErr(err, email)
	}
	_ = m.db.Audit(ctx, by.TokenID, "person.add", p.ID, map[string]any{"name": name, "role": role})
	return p, nil
}

// emailErr turns a clash on the people_email index into ErrInvalid.
func emailErr(err error, email string) error {
	if errors.Is(err, sqlite3.CONSTRAINT_UNIQUE) {
		return fmt.Errorf("%w: someone on this box already uses %s", ErrInvalid, email)
	}
	return err
}

// stillLive returns ErrUnauthenticated if by's token was revoked, read inside
// tx: a change racing the revocation of whoever makes it loses.
func stillLive(ctx context.Context, tx *sql.Tx, by *Principal) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tokens WHERE id = ? AND revoked_at IS NOT NULL`, by.TokenID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrUnauthenticated
	}
	return nil
}

func personTx(ctx context.Context, tx *sql.Tx, id string) (*Person, error) {
	p, err := scanPerson(tx.QueryRowContext(ctx, personCols+` FROM people WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPersonNotFound
	}
	return p, err
}

// UpdatePerson renames someone or changes their role (an empty name or role
// leaves it as it is). A role change ends their open sessions, so the new role
// applies at once; a demotion also revokes every key they created (and keys
// those keys made), which may hold the old role's power. The read, the checks
// and the writes are one transaction, so a rename can't write back a role
// that a demotion just took away.
func (m *Manager) UpdatePerson(ctx context.Context, by *Principal, id, name, role string) (*Person, error) {
	if !by.BoxAdmin() {
		return nil, fmt.Errorf("%w: only an owner, an admin or a key with full access to all projects can change people", ErrForbidden)
	}
	name = strings.TrimSpace(name)
	if len(name) > 64 {
		return nil, fmt.Errorf("%w: name must be 1-64 characters", ErrInvalid)
	}
	if id == OwnerPerson {
		if err := m.ensureOwnerPerson(ctx); err != nil {
			return nil, err
		}
	}
	tx, err := m.db.SQL().BeginTx(ctx, nil) // immediate: writers serialize here
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := stillLive(ctx, tx, by); err != nil {
		return nil, err
	}
	p, err := personTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	changed, demoted := false, false
	if role != "" && role != p.Role {
		if p.Role == RoleOwner {
			return nil, fmt.Errorf("%w: the owner's role cannot change", ErrForbidden)
		}
		if !slices.Contains(Roles, role) {
			return nil, fmt.Errorf("%w: role must be admin, member or viewer", ErrInvalid)
		}
		changed, demoted = true, slices.Index(Roles, role) > slices.Index(Roles, p.Role)
		p.Role = role
		if _, err := tx.ExecContext(ctx, `UPDATE people SET role = ? WHERE id = ?`, role, id); err != nil {
			return nil, err
		}
	}
	if name != "" {
		p.Name = name
		if _, err := tx.ExecContext(ctx, `UPDATE people SET name = ? WHERE id = ?`, name, id); err != nil {
			return nil, err
		}
	}
	if changed {
		if err := m.endSessions(ctx, tx, id, demoted); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	_ = m.db.Audit(ctx, by.TokenID, "person.update", id, map[string]any{"name": p.Name, "role": p.Role})
	return p, nil
}

// RemovePerson disables someone, ends their sessions and revokes every key
// they created (and keys those keys made). The owner stays.
func (m *Manager) RemovePerson(ctx context.Context, by *Principal, id string) error {
	if !by.BoxAdmin() {
		return fmt.Errorf("%w: only an owner, an admin or a key with full access to all projects can remove people", ErrForbidden)
	}
	if id == OwnerPerson {
		return fmt.Errorf("%w: the owner cannot be removed", ErrForbidden)
	}
	now := m.now().UTC()
	tx, err := m.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := stillLive(ctx, tx, by); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE people SET disabled_at = ? WHERE id = ? AND disabled_at IS NULL`, ts(&now), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrPersonNotFound
	}
	if err := m.endSessions(ctx, tx, id, true); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_ = m.db.Audit(ctx, by.TokenID, "person.remove", id, nil)
	return nil
}

// endSessions revokes a person's sessions and, with keys, every token those
// sessions (current or past) sponsored, transitively.
func (m *Manager) endSessions(ctx context.Context, tx *sql.Tx, person string, keys bool) error {
	now := m.now().UTC()
	q := `UPDATE tokens SET revoked_at = ?1 WHERE person = ?2 AND revoked_at IS NULL`
	if keys {
		q = `WITH RECURSIVE tree(id) AS (
				SELECT id FROM tokens WHERE person = ?2
				UNION SELECT t.id FROM tokens t JOIN tree ON t.sponsor = tree.id)
			UPDATE tokens SET revoked_at = ?1 WHERE id IN (SELECT id FROM tree) AND revoked_at IS NULL`
	}
	_, err := tx.ExecContext(ctx, q, ts(&now), person)
	return err
}

// ErrOwnerLink refuses a sign-in link for the owner to anyone but the owner:
// it would sign them in as the owner.
var ErrOwnerLink = fmt.Errorf("%w: only the owner can make a sign-in link for the owner", ErrForbidden)

// LoginLinkFor creates a one-time sign-in link for a person (an invite, or
// a fresh link for someone who lost theirs). Box admins only, and only the
// owner (the owner token or an owner session) for the owner. A link for
// someone else lasts 7 days and works while its maker stays an owner or admin
// (see linkAuthority); one for yourself lasts LoginLinkTTL.
func (m *Manager) LoginLinkFor(ctx context.Context, by *Principal, person string) (string, time.Time, error) {
	if !by.BoxAdmin() {
		return "", time.Time{}, fmt.Errorf("%w: only an owner, an admin or a key with full access to all projects can create sign-in links", ErrForbidden)
	}
	if person == OwnerPerson && by.Person != OwnerPerson {
		return "", time.Time{}, ErrOwnerLink
	}
	p, err := m.GetPerson(ctx, person)
	if err != nil {
		return "", time.Time{}, err
	}
	if p.DisabledAt != nil {
		return "", time.Time{}, ErrPersonNotFound
	}
	ttl := LoginLinkTTL
	if person != by.Person {
		ttl = 7 * 24 * time.Hour // invites wait for the invitee
	}
	return m.insertLink(ctx, by.TokenID, person, ttl)
}

// insertLink stores a one-time sign-in link made by token creator for person.
func (m *Manager) insertLink(ctx context.Context, creator, person string, ttl time.Duration) (string, time.Time, error) {
	code, exp, err := m.addLink(ctx, m.db.SQL(), creator, person, ttl)
	if err != nil {
		return "", time.Time{}, err
	}
	_ = m.db.Audit(ctx, creator, "login_link.create", person, map[string]any{"expiresAt": exp})
	return code, exp, nil
}

// addLink stores a new sign-in link with q (the database or a transaction).
func (m *Manager) addLink(ctx context.Context, q dbtx, creator, person string, ttl time.Duration) (string, time.Time, error) {
	var b [20]byte
	_, _ = rand.Read(b[:])
	code := loginPrefix + strings.ToLower(b32.EncodeToString(b[:]))
	now := m.now().UTC()
	exp := now.Add(ttl)
	if _, err := q.ExecContext(ctx, `INSERT INTO login_links(hash, created_by, created_at, expires_at, person) VALUES (?, ?, ?, ?, ?)`,
		hash(code), creator, ts(&now), ts(&exp), person); err != nil {
		return "", time.Time{}, err
	}
	return code, exp, nil
}

const personCols = `SELECT id, name, email, role, created_at, disabled_at`

func scanPerson(r scanner) (*Person, error) {
	var p Person
	var created string
	var disabled sql.NullString
	if err := r.Scan(&p.ID, &p.Name, &p.Email, &p.Role, &created, &disabled); err != nil {
		return nil, err
	}
	p.CreatedAt = parseTS(created)
	p.DisabledAt = parseNullTS(disabled)
	return &p, nil
}
