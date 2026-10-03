package tokens

import (
	"context"
	"crypto/rand"
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
	var b [20]byte
	_, _ = rand.Read(b[:])
	code := loginPrefix + strings.ToLower(b32.EncodeToString(b[:]))
	now := m.now().UTC()
	exp := now.Add(LoginLinkTTL)
	if _, err := m.db.SQL().ExecContext(ctx, `INSERT INTO login_links(hash, created_by, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		hash(code), by.TokenID, ts(&now), ts(&exp)); err != nil {
		return "", time.Time{}, err
	}
	_ = m.db.Audit(ctx, by.TokenID, "login_link.create", "dashboard", map[string]any{"expiresAt": exp})
	return code, exp, nil
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
	// Mark used in the same statement that checks it, so a code works once.
	err := m.db.SQL().QueryRowContext(ctx, `UPDATE login_links SET used_at = ?
		WHERE hash = ? AND used_at IS NULL AND expires_at > ? RETURNING created_by`,
		ts(&now), hash(code), ts(&now)).Scan(&createdBy)
	if err != nil {
		return "", nil, ErrUnauthenticated
	}
	creator, err := m.Get(ctx, createdBy)
	if err != nil || creator.RevokedAt != nil || (creator.ExpiresAt != nil && !now.Before(*creator.ExpiresAt)) {
		return "", nil, ErrUnauthenticated
	}
	by := &Principal{TokenID: creator.ID, Name: creator.Name, Kind: creator.Kind, Scopes: creator.Scopes, Projects: creator.Projects, ExpiresAt: creator.ExpiresAt}
	return m.Create(ctx, by, CreateRequest{Name: "dashboard session", Kind: KindHuman, Scopes: creator.Scopes, Projects: creator.Projects, TTL: SessionTTL})
}
