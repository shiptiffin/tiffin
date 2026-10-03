package tokens

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"slices"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/ids"
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
	if !by.BoxAdmin() || by.Kind == KindAgent {
		return nil, fmt.Errorf("%w: only an owner or admin can add people", ErrForbidden)
	}
	name, email = strings.TrimSpace(name), strings.TrimSpace(email)
	if name == "" || len(name) > 64 {
		return nil, fmt.Errorf("%w: name must be 1-64 characters", ErrInvalid)
	}
	if email != "" {
		if a, err := mail.ParseAddress(email); err != nil || a.Address != email {
			return nil, fmt.Errorf("%w: %q is not an email address", ErrInvalid, email)
		}
	}
	if !slices.Contains(Roles, role) {
		return nil, fmt.Errorf("%w: role must be admin, member or viewer", ErrInvalid)
	}
	now := m.now().UTC()
	p := &Person{ID: ids.New("usr"), Name: name, Email: email, Role: role, CreatedAt: now}
	if _, err := m.db.SQL().ExecContext(ctx, `INSERT INTO people(id, name, email, role, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Email, p.Role, ts(&now), by.TokenID); err != nil {
		return nil, err
	}
	_ = m.db.Audit(ctx, by.TokenID, "person.add", p.ID, map[string]any{"name": name, "role": role})
	return p, nil
}

// UpdatePerson renames someone or changes their role. Their open sessions
// end, so the new role applies at once.
func (m *Manager) UpdatePerson(ctx context.Context, by *Principal, id, name, role string) (*Person, error) {
	if !by.BoxAdmin() || by.Kind == KindAgent {
		return nil, fmt.Errorf("%w: only an owner or admin can change people", ErrForbidden)
	}
	p, err := m.GetPerson(ctx, id)
	if err != nil {
		return nil, err
	}
	if role != "" && role != p.Role {
		if p.Role == RoleOwner {
			return nil, fmt.Errorf("%w: the owner's role cannot change", ErrForbidden)
		}
		if !slices.Contains(Roles, role) {
			return nil, fmt.Errorf("%w: role must be admin, member or viewer", ErrInvalid)
		}
		p.Role = role
		m.endSessions(ctx, id)
	}
	if n := strings.TrimSpace(name); n != "" {
		if len(n) > 64 {
			return nil, fmt.Errorf("%w: name must be 1-64 characters", ErrInvalid)
		}
		p.Name = n
	}
	if _, err := m.db.SQL().ExecContext(ctx, `UPDATE people SET name = ?, role = ? WHERE id = ?`, p.Name, p.Role, id); err != nil {
		return nil, err
	}
	_ = m.db.Audit(ctx, by.TokenID, "person.update", id, map[string]any{"name": p.Name, "role": p.Role})
	return p, nil
}

// RemovePerson disables someone and ends their sessions. The owner stays.
func (m *Manager) RemovePerson(ctx context.Context, by *Principal, id string) error {
	if !by.BoxAdmin() || by.Kind == KindAgent {
		return fmt.Errorf("%w: only an owner or admin can remove people", ErrForbidden)
	}
	if id == OwnerPerson {
		return fmt.Errorf("%w: the owner cannot be removed", ErrForbidden)
	}
	now := m.now().UTC()
	res, err := m.db.SQL().ExecContext(ctx, `UPDATE people SET disabled_at = ? WHERE id = ? AND disabled_at IS NULL`, ts(&now), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrPersonNotFound
	}
	m.endSessions(ctx, id)
	_ = m.db.Audit(ctx, by.TokenID, "person.remove", id, nil)
	return nil
}

func (m *Manager) endSessions(ctx context.Context, person string) {
	now := m.now().UTC()
	_, _ = m.db.SQL().ExecContext(ctx, `UPDATE tokens SET revoked_at = ? WHERE person = ? AND revoked_at IS NULL`, ts(&now), person)
}

// LoginLinkFor creates a one-time sign-in link for a person (an invite, or
// a fresh link for someone who lost theirs). Box admins only.
func (m *Manager) LoginLinkFor(ctx context.Context, by *Principal, person string) (string, time.Time, error) {
	if !by.BoxAdmin() || by.Kind == KindAgent {
		return "", time.Time{}, fmt.Errorf("%w: only an owner or admin can create sign-in links", ErrForbidden)
	}
	p, err := m.GetPerson(ctx, person)
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
	ttl := LoginLinkTTL
	if person != by.Person {
		ttl = 7 * 24 * time.Hour // invites wait for the invitee
	}
	exp := now.Add(ttl)
	if _, err := m.db.SQL().ExecContext(ctx, `INSERT INTO login_links(hash, created_by, created_at, expires_at, person) VALUES (?, ?, ?, ?, ?)`,
		hash(code), by.TokenID, ts(&now), ts(&exp), person); err != nil {
		return "", time.Time{}, err
	}
	_ = m.db.Audit(ctx, by.TokenID, "login_link.create", person, map[string]any{"expiresAt": exp})
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
