// Package tokens issues and checks platform API tokens until platform
// identity moves to Better Auth in M6.
//
// There is one owner (bootstrap) token per box. The owner mints scoped
// tokens for agents and other tools. A token can never mint a token with more
// power than itself, and agent tokens always expire and name their sponsor.
// Only a SHA-256 of each secret is stored.
package tokens

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/ids"
	"github.com/btahir/tiffin/internal/state"
)

// Scope is one permission. Scopes form a ladder: each implies the ones below.
type Scope string

const (
	ScopeRead              Scope = "read"               // see state, plans' inputs, changes, audit
	ScopePlan              Scope = "plan"               // compute plans (never writes)
	ScopeApplyReversible   Scope = "apply:reversible"   // apply plans whose risk is at most reversible
	ScopeApplyOutbound     Scope = "apply:outbound"     // ... at most outbound
	ScopeApplyIrreversible Scope = "apply:irreversible" // ... any risk
	ScopeTokens            Scope = "tokens"             // mint and revoke tokens (within own power)
	ScopeAll               Scope = "*"                  // owner
)

var ladder = []Scope{ScopeRead, ScopePlan, ScopeApplyReversible, ScopeApplyOutbound, ScopeApplyIrreversible}

// AllScopes lists every grantable scope, for docs and validation.
var AllScopes = []Scope{ScopeRead, ScopePlan, ScopeApplyReversible, ScopeApplyOutbound, ScopeApplyIrreversible, ScopeTokens, ScopeAll}

// Kinds of principal.
const (
	KindOwner = "owner"
	KindAgent = "agent"
	KindHuman = "human"
)

// Errors.
var (
	ErrUnauthenticated = errors.New("missing, unknown, expired or revoked token")
	ErrForbidden       = errors.New("forbidden")
	ErrInvalid         = errors.New("invalid token request")
	ErrNotFound        = errors.New("token not found")
)

// Principal is an authenticated caller.
type Principal struct {
	TokenID  string   `json:"tokenId"`
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	Scopes   []Scope  `json:"scopes"`
	Projects []string `json:"projects"` // ["*"] means all
	Sponsor  string   `json:"sponsor,omitempty"`
	// ExpiresAt is when the token stops working; nil means never. Tokens it
	// mints never outlive it.
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	Session   string     `json:"session,omitempty"` // agent session label, set per request
}

// Has reports whether p holds scope s, directly or via the ladder.
func (p *Principal) Has(s Scope) bool {
	if slices.Contains(p.Scopes, ScopeAll) || slices.Contains(p.Scopes, s) {
		return true
	}
	want := slices.Index(ladder, s)
	if want < 0 {
		return false
	}
	for _, have := range p.Scopes {
		if i := slices.Index(ladder, have); i >= want {
			return true
		}
	}
	return false
}

// CanProject reports whether p may act on project.
func (p *Principal) CanProject(project string) bool {
	return slices.Contains(p.Projects, "*") || slices.Contains(p.Projects, project)
}

// Require returns ErrForbidden (wrapped with a reason) unless p holds s for project.
// Pass project "" for box-wide operations.
func (p *Principal) Require(s Scope, project string) error {
	if !p.Has(s) {
		return fmt.Errorf("%w: token %q lacks scope %q", ErrForbidden, p.Name, s)
	}
	if project != "" && !p.CanProject(project) {
		return fmt.Errorf("%w: token %q is not allowed on project %q", ErrForbidden, p.Name, project)
	}
	return nil
}

// ScopeForTier is the scope needed to apply a plan of the given risk.
func ScopeForTier(t change.Tier) Scope {
	switch t {
	case change.TierRead, change.TierReversible:
		return ScopeApplyReversible
	case change.TierOutbound:
		return ScopeApplyOutbound
	default:
		return ScopeApplyIrreversible
	}
}

// Authorizer returns a change.Authorizer enforcing p's scopes and projects.
func (p *Principal) Authorizer() change.Authorizer {
	return func(plan *change.Plan) error {
		need := ScopeForTier(plan.Risk)
		if err := p.Require(need, plan.Project); err != nil {
			return fmt.Errorf("%s plan needs scope %q: %w", plan.Risk, need, err)
		}
		return nil
	}
}

// Actor converts p to a change actor.
func (p *Principal) Actor() change.Actor {
	kind := p.Kind
	if kind == KindOwner {
		kind = KindHuman
	}
	return change.Actor{Kind: kind, ID: p.TokenID, Name: p.Name, Session: p.Session}
}

// Token is a stored token's public metadata. The secret is never stored.
type Token struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	Scopes     []Scope    `json:"scopes"`
	Projects   []string   `json:"projects"`
	Sponsor    string     `json:"sponsor,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

// CreateRequest describes a new token.
type CreateRequest struct {
	Name     string        `json:"name"`
	Kind     string        `json:"kind"`     // "agent" (default) or "human"
	Scopes   []Scope       `json:"scopes"`   // default: read, plan, apply:reversible
	Projects []string      `json:"projects"` // default: the sponsor's projects
	TTL      time.Duration `json:"ttl"`      // default 30 days for agents; 0 = never for humans
}

// DefaultAgentTTL is how long agent tokens live unless told otherwise.
const DefaultAgentTTL = 30 * 24 * time.Hour

// MaxAgentTTL caps agent token lifetimes.
const MaxAgentTTL = 365 * 24 * time.Hour

// Manager issues and checks tokens.
type Manager struct {
	db  *state.DB
	now func() time.Time
}

// NewManager returns a manager over the state database.
func NewManager(db *state.DB) *Manager { return &Manager{db: db, now: time.Now} }

const secretPrefix = "tfn_"

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func newSecret() string {
	var b [25]byte // 200 bits
	_, _ = rand.Read(b[:])
	return secretPrefix + strings.ToLower(b32.EncodeToString(b[:]))
}

func hash(secret string) []byte {
	h := sha256.Sum256([]byte(secret))
	return h[:]
}

// Bootstrap creates the owner token if the box has none. It returns the new
// secret and true, or "" and false if an owner token already exists.
func (m *Manager) Bootstrap(ctx context.Context) (string, bool, error) {
	var n int
	if err := m.db.SQL().QueryRowContext(ctx,
		`SELECT count(*) FROM tokens WHERE kind = ? AND revoked_at IS NULL`, KindOwner).Scan(&n); err != nil {
		return "", false, err
	}
	if n > 0 {
		return "", false, nil
	}
	secret := newSecret()
	t := &Token{ID: ids.New("tok"), Name: "owner", Kind: KindOwner, Scopes: []Scope{ScopeAll}, Projects: []string{"*"}, CreatedAt: m.now().UTC()}
	if err := m.insert(ctx, t, secret); err != nil {
		return "", false, err
	}
	_ = m.db.Audit(ctx, "system", "token.bootstrap", t.ID, map[string]any{"name": t.Name})
	return secret, true, nil
}

// Create mints a token on behalf of by. The new token's scopes and projects
// must be a subset of by's, and by must hold the tokens scope.
func (m *Manager) Create(ctx context.Context, by *Principal, req CreateRequest) (string, *Token, error) {
	if err := by.Require(ScopeTokens, ""); err != nil {
		return "", nil, err
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 64 {
		return "", nil, fmt.Errorf("%w: name must be 1-64 characters", ErrInvalid)
	}
	if req.Kind == "" {
		req.Kind = KindAgent
	}
	if req.Kind != KindAgent && req.Kind != KindHuman {
		return "", nil, fmt.Errorf("%w: kind must be %q or %q", ErrInvalid, KindAgent, KindHuman)
	}
	// An agent minting a "human" token would launder its changes as a
	// human's in the change log and escape the agent expiry rule.
	if by.Kind == KindAgent && req.Kind != KindAgent {
		return "", nil, fmt.Errorf("%w: agent tokens can only mint agent tokens", ErrForbidden)
	}
	if len(req.Scopes) == 0 {
		req.Scopes = []Scope{ScopeRead, ScopePlan, ScopeApplyReversible}
	}
	for _, s := range req.Scopes {
		if !slices.Contains(AllScopes, s) {
			return "", nil, fmt.Errorf("%w: unknown scope %q", ErrInvalid, s)
		}
		if !by.Has(s) {
			return "", nil, fmt.Errorf("%w: cannot grant %q, which token %q does not hold", ErrForbidden, s, by.Name)
		}
	}
	if len(req.Projects) == 0 {
		req.Projects = slices.Clone(by.Projects)
	}
	for _, p := range req.Projects {
		if p == "*" && !slices.Contains(by.Projects, "*") {
			return "", nil, fmt.Errorf("%w: cannot grant all projects", ErrForbidden)
		}
		if p != "*" && !by.CanProject(p) {
			return "", nil, fmt.Errorf("%w: cannot grant project %q", ErrForbidden, p)
		}
	}
	if req.TTL < 0 {
		return "", nil, fmt.Errorf("%w: ttl must be positive", ErrInvalid)
	}
	if req.Kind == KindAgent {
		if req.TTL == 0 {
			req.TTL = DefaultAgentTTL
		}
		if req.TTL > MaxAgentTTL {
			return "", nil, fmt.Errorf("%w: agent tokens live at most %s", ErrInvalid, MaxAgentTTL)
		}
	}
	now := m.now().UTC()
	t := &Token{
		ID: ids.New("tok"), Name: req.Name, Kind: req.Kind,
		Scopes: dedupe(req.Scopes), Projects: dedupe(req.Projects),
		Sponsor: by.TokenID, CreatedAt: now,
	}
	if req.TTL > 0 {
		exp := now.Add(req.TTL)
		t.ExpiresAt = &exp
	}
	// A token never outlives its sponsor: expiry of the sponsor does not
	// cascade like revocation does, so clamp instead.
	if by.ExpiresAt != nil && (t.ExpiresAt == nil || t.ExpiresAt.After(*by.ExpiresAt)) {
		exp := by.ExpiresAt.UTC()
		t.ExpiresAt = &exp
	}
	secret := newSecret()
	if err := m.insert(ctx, t, secret); err != nil {
		return "", nil, err
	}
	_ = m.db.Audit(ctx, by.TokenID, "token.create", t.ID, map[string]any{"name": t.Name, "kind": t.Kind, "scopes": t.Scopes, "projects": t.Projects, "expiresAt": t.ExpiresAt})
	return secret, t, nil
}

func dedupe[T comparable](in []T) []T {
	var out []T
	for _, v := range in {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

func (m *Manager) insert(ctx context.Context, t *Token, secret string) error {
	scopes, _ := json.Marshal(t.Scopes)
	projects, _ := json.Marshal(t.Projects)
	_, err := m.db.SQL().ExecContext(ctx, `INSERT INTO tokens(id, name, kind, hash, scopes, projects, sponsor, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Name, t.Kind, hash(secret), string(scopes), string(projects), nullStr(t.Sponsor),
		ts(&t.CreatedAt), ts(t.ExpiresAt))
	return err
}

// Authenticate resolves a secret to a principal.
func (m *Manager) Authenticate(ctx context.Context, secret string) (*Principal, error) {
	secret = strings.TrimSpace(strings.TrimPrefix(secret, "Bearer "))
	if !strings.HasPrefix(secret, secretPrefix) {
		return nil, ErrUnauthenticated
	}
	row := m.db.SQL().QueryRowContext(ctx, tokenCols+` FROM tokens WHERE hash = ?`, hash(secret))
	t, err := scanToken(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthenticated
	} else if err != nil {
		return nil, err
	}
	now := m.now().UTC()
	if t.RevokedAt != nil || (t.ExpiresAt != nil && !now.Before(*t.ExpiresAt)) {
		return nil, ErrUnauthenticated
	}
	if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) > time.Minute {
		_, _ = m.db.SQL().ExecContext(ctx, `UPDATE tokens SET last_used_at = ? WHERE id = ?`, ts(&now), t.ID)
	}
	return &Principal{TokenID: t.ID, Name: t.Name, Kind: t.Kind, Scopes: t.Scopes, Projects: t.Projects, Sponsor: t.Sponsor, ExpiresAt: t.ExpiresAt}, nil
}

// Revoke revokes token id. Owners can revoke any token; others only tokens
// they sponsored (directly). The owner token cannot be revoked this way.
func (m *Manager) Revoke(ctx context.Context, by *Principal, id string) error {
	if err := by.Require(ScopeTokens, ""); err != nil {
		return err
	}
	t, err := m.Get(ctx, id)
	if err != nil {
		return err
	}
	if t.Kind == KindOwner {
		return fmt.Errorf("%w: the owner token is rotated with `tiffin token rotate-owner`, not revoked", ErrForbidden)
	}
	if !by.Has(ScopeAll) && t.Sponsor != by.TokenID {
		return fmt.Errorf("%w: token %q was not minted by %q", ErrForbidden, t.Name, by.Name)
	}
	if t.RevokedAt != nil {
		return nil
	}
	now := m.now().UTC()
	if _, err := m.db.SQL().ExecContext(ctx, `UPDATE tokens SET revoked_at = ? WHERE id = ?`, ts(&now), id); err != nil {
		return err
	}
	// Revoking a sponsor revokes everything it minted, transitively.
	rows, err := m.db.SQL().QueryContext(ctx, `SELECT id FROM tokens WHERE sponsor = ? AND revoked_at IS NULL`, id)
	if err != nil {
		return err
	}
	var children []string
	for rows.Next() {
		var c string
		if rows.Scan(&c) == nil {
			children = append(children, c)
		}
	}
	rows.Close()
	_ = m.db.Audit(ctx, by.TokenID, "token.revoke", id, map[string]any{"name": t.Name, "cascade": children})
	owner := &Principal{TokenID: by.TokenID, Name: by.Name, Scopes: []Scope{ScopeAll}, Projects: []string{"*"}}
	for _, c := range children {
		if err := m.Revoke(ctx, owner, c); err != nil {
			return err
		}
	}
	return nil
}

// RotateOwner revokes every owner token and issues a new one. Only callable
// with local access to the box (the CLI on the box, not the API).
func (m *Manager) RotateOwner(ctx context.Context) (string, error) {
	now := m.now().UTC()
	if _, err := m.db.SQL().ExecContext(ctx, `UPDATE tokens SET revoked_at = ? WHERE kind = ? AND revoked_at IS NULL`, ts(&now), KindOwner); err != nil {
		return "", err
	}
	secret, _, err := m.Bootstrap(ctx)
	return secret, err
}

// Get returns one token's metadata.
func (m *Manager) Get(ctx context.Context, id string) (*Token, error) {
	t, err := scanToken(m.db.SQL().QueryRowContext(ctx, tokenCols+` FROM tokens WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// List returns tokens, newest first. Revoked tokens are included only if asked.
func (m *Manager) List(ctx context.Context, includeRevoked bool) ([]*Token, error) {
	q := tokenCols + ` FROM tokens`
	if !includeRevoked {
		q += ` WHERE revoked_at IS NULL`
	}
	rows, err := m.db.SQL().QueryContext(ctx, q+` ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Token
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

const tokenCols = `SELECT id, name, kind, scopes, projects, sponsor, created_at, expires_at, revoked_at, last_used_at`

type scanner interface{ Scan(dest ...any) error }

func scanToken(r scanner) (*Token, error) {
	var t Token
	var scopes, projects, created string
	var sponsor, expires, revoked, used sql.NullString
	if err := r.Scan(&t.ID, &t.Name, &t.Kind, &scopes, &projects, &sponsor, &created, &expires, &revoked, &used); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(scopes), &t.Scopes)
	_ = json.Unmarshal([]byte(projects), &t.Projects)
	t.Sponsor = sponsor.String
	t.CreatedAt = parseTS(created)
	t.ExpiresAt = parseNullTS(expires)
	t.RevokedAt = parseNullTS(revoked)
	t.LastUsedAt = parseNullTS(used)
	return &t, nil
}

func ts(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func parseNullTS(s sql.NullString) *time.Time {
	if !s.Valid {
		return nil
	}
	t := parseTS(s.String)
	return &t
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
