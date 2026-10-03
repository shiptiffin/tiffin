package approvals

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/tokens"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
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

// DeletePasskey removes one passkey.
func (m *Manager) DeletePasskey(ctx context.Context, by *tokens.Principal, id string) error {
	if err := humanOnly(by); err != nil {
		return err
	}
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return ErrNotFound
	}
	res, err := m.db.SQL().ExecContext(ctx, `DELETE FROM passkeys WHERE id = ? AND coalesce(person, ?) = ?`, raw, tokens.OwnerPerson, personOf(by))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_ = m.db.Audit(ctx, by.TokenID, "passkey.delete", id, nil)
	return nil
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
	if err := humanOnly(by); err != nil {
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
	if err := humanOnly(by); err != nil {
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
	_ = m.db.Audit(ctx, by.TokenID, "passkey.add", name, nil)
	return &Passkey{ID: base64.RawURLEncoding.EncodeToString(cred.ID), Name: name, CreatedAt: now}, nil
}

// BeginApproval starts the passkey assertion for approval id. The challenge
// is stored with that approval only.
func (m *Manager) BeginApproval(ctx context.Context, by *tokens.Principal, id string) (*protocol.CredentialAssertion, error) {
	if err := humanOnly(by); err != nil {
		return nil, err
	}
	a, err := m.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if a.Status != Pending {
		return nil, ErrNotPending
	}
	o, err := m.owner(ctx, personOf(by))
	if err != nil {
		return nil, err
	}
	if len(o.creds) == 0 {
		return nil, ErrNoPasskey
	}
	ca, s, err := m.wa.BeginLogin(o)
	if err != nil {
		return nil, err
	}
	return ca, m.saveSession(ctx, "approve:"+id, s)
}

// FinishApproval verifies the passkey assertion and approves.
func (m *Manager) FinishApproval(ctx context.Context, by *tokens.Principal, id string, response json.RawMessage) (*Approval, error) {
	if err := humanOnly(by); err != nil {
		return nil, err
	}
	s, err := m.takeSession(ctx, "approve:"+id)
	if err != nil {
		return nil, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(response)
	if err != nil {
		return nil, fmt.Errorf("passkey response: %w", err)
	}
	o, err := m.owner(ctx, personOf(by))
	if err != nil {
		return nil, err
	}
	cred, err := m.wa.ValidateLogin(o, *s, parsed)
	if err != nil {
		return nil, fmt.Errorf("passkey check failed: %w", err)
	}
	now := m.now().UTC()
	raw, _ := json.Marshal(cred)
	_, _ = m.db.SQL().ExecContext(ctx, `UPDATE passkeys SET credential = ?, last_used = ? WHERE id = ?`, string(raw), ts(now), cred.ID)
	res, err := m.db.SQL().ExecContext(ctx, `UPDATE approvals SET status = ?, decided_at = ?, decided_by = ?
		WHERE id = ? AND status = ? AND expires_at > ?`, Approved, ts(now), by.TokenID, id, Pending, ts(now))
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, ErrNotPending
	}
	_ = m.db.Audit(ctx, by.TokenID, "approval.approve", id, map[string]any{"passkey": base64.RawURLEncoding.EncodeToString(cred.ID)})
	return m.Get(ctx, id)
}
