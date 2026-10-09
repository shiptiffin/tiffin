package passkeys

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// owner is the person whose passkeys are in play (each person has their own).
type owner struct {
	id    string
	creds []webauthn.Credential
}

func (o *owner) WebAuthnID() []byte                         { return []byte(o.id) }
func (o *owner) WebAuthnName() string                       { return o.id }
func (o *owner) WebAuthnDisplayName() string                { return o.id }
func (o *owner) WebAuthnCredentials() []webauthn.Credential { return o.creds }

func personOf(p *tokens.Principal) string {
	if p.Person != "" {
		return p.Person
	}
	return tokens.OwnerPerson
}

// passkeyHolder: a passkey belongs to a person. Any person's dashboard
// session manages its own passkeys (they sign that person in); a box-admin
// token with no person (the owner's CLI token) manages the owner's. Agents
// never hold passkeys.
func passkeyHolder(p *tokens.Principal) error {
	if p == nil || p.Kind == tokens.KindAgent || (p.Person == "" && !p.BoxAdmin()) {
		return ErrNoPerson
	}
	return nil
}

// Passkey is a registered passkey's metadata.
type Passkey struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	CreatedAt time.Time  `json:"createdAt"`
	LastUsed  *time.Time `json:"lastUsed,omitempty"`
}

func (m *Manager) owner(ctx context.Context, person string) (*owner, error) {
	rows, err := m.db.SQL().QueryContext(ctx, `SELECT credential FROM passkeys WHERE coalesce(person, ?) = ?`, tokens.OwnerPerson, person)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	o := &owner{id: person}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var c webauthn.Credential
		if json.Unmarshal([]byte(raw), &c) == nil {
			o.creds = append(o.creds, c)
		}
	}
	return o, rows.Err()
}

// Passkeys lists the caller's registered passkeys.
func (m *Manager) Passkeys(ctx context.Context, by *tokens.Principal) ([]Passkey, error) {
	if err := passkeyHolder(by); err != nil {
		return nil, err
	}
	rows, err := m.db.SQL().QueryContext(ctx, `SELECT id, name, created_at, last_used FROM passkeys WHERE coalesce(person, ?) = ? ORDER BY created_at`, tokens.OwnerPerson, personOf(by))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Passkey{}
	for rows.Next() {
		var id []byte
		var p Passkey
		var created string
		var used sql.NullString
		if err := rows.Scan(&id, &p.Name, &created, &used); err != nil {
			return nil, err
		}
		p.ID = base64.RawURLEncoding.EncodeToString(id)
		p.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		if used.Valid {
			t, _ := time.Parse(time.RFC3339Nano, used.String)
			p.LastUsed = &t
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePasskey removes one passkey and returns what it was.
func (m *Manager) DeletePasskey(ctx context.Context, by *tokens.Principal, id string) (*Passkey, error) {
	if err := passkeyHolder(by); err != nil {
		return nil, err
	}
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return nil, ErrNotFound
	}
	pk := Passkey{ID: id}
	var created string
	err = m.db.SQL().QueryRowContext(ctx, `DELETE FROM passkeys WHERE id = ? AND coalesce(person, ?) = ? RETURNING name, created_at`,
		raw, tokens.OwnerPerson, personOf(by)).Scan(&pk.Name, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	pk.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	_ = m.db.Audit(ctx, by.TokenID, "passkey.delete", id, map[string]any{"summary": holderName(by) + " removed the passkey " + pk.Name,
		"name": pk.Name, "person": personOf(by)})
	return &pk, nil
}

// holderName names whoever acts on passkeys, for the audit log.
func holderName(by *tokens.Principal) string {
	if by.PersonName != "" {
		return by.PersonName
	}
	return by.Name
}

// sessions hold in-flight WebAuthn ceremonies (challenge state) in the kv table.
func (m *Manager) saveSession(ctx context.Context, key string, s *webauthn.SessionData) error {
	b, _ := json.Marshal(s)
	return m.db.KVPut(ctx, "webauthn", key, b)
}

func (m *Manager) takeSession(ctx context.Context, key string) (*webauthn.SessionData, error) {
	b, ok, err := m.db.KVGet(ctx, "webauthn", key)
	if err != nil || !ok {
		return nil, errors.New("no ceremony in progress; start again")
	}
	_ = m.db.KVDelete(ctx, "webauthn", key)
	var s webauthn.SessionData
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	if !s.Expires.IsZero() && time.Now().After(s.Expires) {
		return nil, errors.New("the passkey prompt expired; start again")
	}
	return &s, nil
}

// BeginRegistration starts adding a passkey (human only).
func (m *Manager) BeginRegistration(ctx context.Context, by *tokens.Principal) (*protocol.CredentialCreation, error) {
	if err := passkeyHolder(by); err != nil {
		return nil, err
	}
	o, err := m.owner(ctx, personOf(by))
	if err != nil {
		return nil, err
	}
	var exclude []protocol.CredentialDescriptor
	for _, c := range o.creds {
		exclude = append(exclude, c.Descriptor())
	}
	cc, s, err := m.wa.BeginRegistration(o, webauthn.WithExclusions(exclude))
	if err != nil {
		return nil, err
	}
	return cc, m.saveSession(ctx, "register:"+by.TokenID, s)
}

// FinishRegistration stores the new passkey.
func (m *Manager) FinishRegistration(ctx context.Context, by *tokens.Principal, name string, response json.RawMessage) (*Passkey, error) {
	if err := passkeyHolder(by); err != nil {
		return nil, err
	}
	s, err := m.takeSession(ctx, "register:"+by.TokenID)
	if err != nil {
		return nil, err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(response)
	if err != nil {
		return nil, fmt.Errorf("passkey response: %w", err)
	}
	o, err := m.owner(ctx, personOf(by))
	if err != nil {
		return nil, err
	}
	cred, err := m.wa.CreateCredential(o, *s, parsed)
	if err != nil {
		return nil, fmt.Errorf("passkey rejected: %w", err)
	}
	raw, _ := json.Marshal(cred)
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Passkey"
	}
	now := m.now().UTC()
	if _, err := m.db.SQL().ExecContext(ctx, `INSERT INTO passkeys(id, name, credential, created_at, person) VALUES (?, ?, ?, ?, ?)`,
		cred.ID, name, string(raw), ts(now), personOf(by)); err != nil {
		return nil, err
	}
	id := base64.RawURLEncoding.EncodeToString(cred.ID)
	_ = m.db.Audit(ctx, by.TokenID, "passkey.add", id, map[string]any{"summary": holderName(by) + " added the passkey " + name,
		"name": name, "person": personOf(by)})
	return &Passkey{ID: id, Name: name, CreatedAt: now}, nil
}
