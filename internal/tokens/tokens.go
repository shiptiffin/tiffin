// Package tokens issues and checks platform API tokens.
//
// There is one owner (bootstrap) token per box. People get session tokens
// with their role's scopes; agents, scripts and CI get API keys (keys.go):
// some projects or all, full or read access. Only a SHA-256 of each secret
// is stored.
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
	Model     string     `json:"model,omitempty"`   // model an agent says it runs, set per request (self-reported)
	// Person is the human behind a human token (dashboard sessions), if any.
	Person string `json:"person,omitempty"`
	// PersonName and Role describe that person.
	PersonName string `json:"personName,omitempty"`
	Role       string `json:"role,omitempty" enum:"owner,admin,member,viewer,"`
	// Access is the caller's level: full (apply changes) or read (read and plan).
	Access string `json:"access,omitempty" enum:"full,read," doc:"full: can apply changes in its projects. read: can only read and plan."`
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

// BoxAdmin reports whether p administers the whole box: every scope on
// every project. Only box admins see all tokens and the audit log.
func (p *Principal) BoxAdmin() bool {
	return slices.Contains(p.Scopes, ScopeAll) && slices.Contains(p.Projects, "*")
}

// CanProject reports whether p may act on project.
func (p *Principal) CanProject(project string) bool {
	return slices.Contains(p.Projects, "*") || slices.Contains(p.Projects, project)
}

// Require returns ErrForbidden (wrapped with a reason) unless p holds s for project.
// Pass project "" for box-wide operations.
func (p *Principal) Require(s Scope, project string) error {
	if !p.Has(s) {
		return p.refusal(s)
	}
	if project != "" && !p.CanProject(project) {
		return p.projectRefusal(s, project)
	}
	return nil
}

// RequireBox is Require for box-wide reports, which mix every project's
// data (the disk breakdown, the box's resources, backups and restore
// drills): s on all projects.
func (p *Principal) RequireBox(s Scope) error {
	if err := p.Require(s, ""); err != nil {
		return err
	}
	if !p.CanProject("*") {
		return fmt.Errorf("%w: this key (%q) is limited to some projects; this box-wide report needs a key for all projects", ErrForbidden, p.Name)
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
			return err // it says what the key reaches
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
	return change.Actor{Kind: kind, ID: p.TokenID, Name: p.Name, Session: p.Session, Model: p.Model}
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
	Person     string     `json:"person,omitempty"`
	// Grants are an API key's {projects, level} pairs (see keys.go); empty
	// for tokens made before keys, which have only Scopes and Projects.
	Grants []Grant `json:"grants,omitempty"`
}

// CreateRequest describes a new token.
type CreateRequest struct {
	Person   string        `json:"person,omitempty"` // the human this token acts for (dashboard sessions)
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
	secret := newSecret()
	t := &Token{ID: ids.New("tok"), Name: "owner", Kind: KindOwner, Scopes: []Scope{ScopeAll}, Projects: []string{"*"}, CreatedAt: m.now().UTC()}
	scopes, _ := json.Marshal(t.Scopes)
	projects, _ := json.Marshal(t.Projects)
	// One statement, so processes opening a fresh box at once cannot each
	// see "no owner" and mint one.
	res, err := m.db.SQL().ExecContext(ctx, `INSERT INTO tokens(id, name, kind, hash, scopes, projects, created_at)
		SELECT ?, ?, ?, ?, ?, ?, ?
		WHERE NOT EXISTS (SELECT 1 FROM tokens WHERE kind = ? AND revoked_at IS NULL)`,
		t.ID, t.Name, t.Kind, hash(secret), string(scopes), string(projects), ts(&t.CreatedAt), KindOwner)
	if err != nil {
		return "", false, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return "", false, err
	}
	_ = m.db.Audit(ctx, "system", "token.bootstrap", t.ID, map[string]any{"name": t.Name})
	if err := m.ensureOwnerPerson(ctx); err != nil {
		return "", false, err
	}
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
		Sponsor: by.TokenID, CreatedAt: now, Person: req.Person,
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
	if err := m.insert(ctx, m.db.SQL(), t, secret, ""); err != nil {
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

// ErrRevokedMaker refuses a credential whose maker (the session or key
// creating it) was revoked, or whose person was removed or changed role,
// while it was being made.
var ErrRevokedMaker = fmt.Errorf("%w: the session or key making this was signed out or revoked, or the person's access changed; sign in again", ErrUnauthenticated)

// insert stores t, but only if what it rests on still holds when it lands:
// its sponsor (which, coming from Authenticate, exists) is not revoked and, for a person's token, the person is active
// (with role, when role is set). It is one statement, and revocations are one
// statement too (Revoke, endSessions), so a credential made while its maker
// is revoked is either caught by the revocation or never stored.
func (m *Manager) insert(ctx context.Context, q dbtx, t *Token, secret, role string) error {
	scopes, _ := json.Marshal(t.Scopes)
	projects, _ := json.Marshal(t.Projects)
	var grants any
	if len(t.Grants) > 0 {
		b, _ := json.Marshal(t.Grants)
		grants = string(b)
	}
	res, err := q.ExecContext(ctx, `INSERT INTO tokens(id, name, kind, hash, scopes, projects, sponsor, created_at, expires_at, person, grants)
		SELECT ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11
		WHERE NOT EXISTS (SELECT 1 FROM tokens WHERE id = ?7 AND revoked_at IS NOT NULL)
		AND (?10 IS NULL OR EXISTS (SELECT 1 FROM people WHERE id = ?10 AND disabled_at IS NULL AND (?12 = '' OR role = ?12)))`,
		t.ID, t.Name, t.Kind, hash(secret), string(scopes), string(projects), nullStr(t.Sponsor),
		ts(&t.CreatedAt), ts(t.ExpiresAt), nullStr(t.Person), grants, role)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrRevokedMaker
	}
	return nil
}

// Authenticate resolves a secret to a principal, as a use of it: an owner
// session's first one closes the managed hand-off (closeHandoff).
func (m *Manager) Authenticate(ctx context.Context, secret string) (*Principal, error) {
	return m.authenticate(ctx, secret, true)
}

// Inspect resolves a secret like Authenticate without counting as its use:
// for the request that just created the session, whose answer may never
// reach the browser.
func (m *Manager) Inspect(ctx context.Context, secret string) (*Principal, error) {
	return m.authenticate(ctx, secret, false)
}

func (m *Manager) authenticate(ctx context.Context, secret string, use bool) (*Principal, error) {
	secret = strings.TrimSpace(strings.TrimPrefix(secret, "Bearer "))
	if !strings.HasPrefix(secret, secretPrefix) {
		return nil, ErrUnauthenticated
	}
	// One query for the token and its person (the owner token's is the owner).
	var pName, pRole, pDisabled sql.NullString
	row := m.db.SQL().QueryRowContext(ctx, tokenColsT+`, p.name, p.role, p.disabled_at FROM tokens t
		LEFT JOIN people p ON p.id = CASE WHEN t.kind = ? THEN ? ELSE t.person END WHERE t.hash = ?`, KindOwner, OwnerPerson, hash(secret))
	t, err := scanToken(row, &pName, &pRole, &pDisabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthenticated
	} else if err != nil {
		return nil, err
	}
	now := m.now().UTC()
	if t.RevokedAt != nil || (t.ExpiresAt != nil && !now.Before(*t.ExpiresAt)) {
		return nil, ErrUnauthenticated
	}
	switch {
	case !use:
	case t.LastUsedAt == nil && t.Kind == KindHuman && t.Person == OwnerPerson:
		// The owner is signed in for real: the hand-off is over. If that
		// can't be recorded now, the next request tries again.
		_ = m.closeHandoff(ctx, t.ID, now)
	case t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) > time.Minute:
		_, _ = m.db.SQL().ExecContext(ctx, `UPDATE tokens SET last_used_at = ? WHERE id = ?`, ts(&now), t.ID)
	}
	person := t.Person
	if t.Kind == KindOwner {
		person = OwnerPerson
	}
	pr := &Principal{TokenID: t.ID, Name: t.Name, Kind: t.Kind, Scopes: t.Scopes, Projects: t.Projects, Sponsor: t.Sponsor, ExpiresAt: t.ExpiresAt, Person: person}
	pr.Access = pr.access()
	if person != "" {
		if !pRole.Valid && person == OwnerPerson { // a box from before people: make the owner's row
			if pp, err := m.GetPerson(ctx, person); err == nil {
				pName.String, pRole.String = pp.Name, pp.Role
			}
		}
		if pDisabled.Valid {
			return nil, ErrUnauthenticated
		}
		pr.PersonName, pr.Role = pName.String, pRole.String
	}
	return pr, nil
}

// ErrSessionRevoke refuses ending a dashboard session as if it were an API
// key: sessions end through EndSession, which keeps the owner's to the owner.
var ErrSessionRevoke = fmt.Errorf("%w: that is a dashboard sign-in, not an API key; end it on Sign-ins or People (DELETE /v1/sessions/{id})", ErrForbidden)

// Revoke revokes API key id and every key it made, transitively, in one
// statement. Owners can revoke any key; others only keys they sponsored
// (directly). The owner token cannot be revoked this way, nor a dashboard
// session (see EndSession and SignOut).
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
	if t.Kind == KindHuman && t.Person != "" {
		return ErrSessionRevoke
	}
	if !by.BoxAdmin() && t.Sponsor != by.TokenID {
		return fmt.Errorf("%w: token %q was not minted by %q", ErrForbidden, t.Name, by.Name)
	}
	if t.RevokedAt != nil {
		return nil
	}
	now := m.now().UTC()
	rows, err := m.db.SQL().QueryContext(ctx, `WITH RECURSIVE tree(id) AS (
			SELECT ?2 UNION SELECT t.id FROM tokens t JOIN tree ON t.sponsor = tree.id)
		UPDATE tokens SET revoked_at = ?1 WHERE id IN (SELECT id FROM tree) AND revoked_at IS NULL RETURNING id`, ts(&now), id)
	if err != nil {
		return err
	}
	var cascade []string
	for rows.Next() {
		var c string
		if rows.Scan(&c) == nil && c != id {
			cascade = append(cascade, c)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_ = m.db.Audit(ctx, by.TokenID, "token.revoke", id, map[string]any{"name": t.Name, "cascade": cascade})
	return nil
}

// SignOut ends the dashboard session p is (signing out); for anything else it
// does nothing. Keys made in it keep working.
func (m *Manager) SignOut(ctx context.Context, p *Principal) error {
	if !p.IsSession() {
		return nil
	}
	if err := m.revokeSessions(ctx, []string{p.TokenID}, false); err != nil {
		return err
	}
	_ = m.db.Audit(ctx, p.TokenID, "token.revoke", p.TokenID, map[string]any{"name": p.Name})
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
	return m.list(ctx, includeRevoked, false)
}

// ListKeys is List without dashboard sessions: the API keys (and the owner
// token), filtered in SQL so accumulated sessions cost nothing.
func (m *Manager) ListKeys(ctx context.Context, includeRevoked bool) ([]*Token, error) {
	return m.list(ctx, includeRevoked, true)
}

func (m *Manager) list(ctx context.Context, includeRevoked, keys bool) ([]*Token, error) {
	q := tokenCols + ` FROM tokens WHERE 1`
	if !includeRevoked {
		q += ` AND revoked_at IS NULL`
	}
	if keys {
		q += ` AND NOT (kind = 'human' AND person IS NOT NULL)`
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

const tokenCols = `SELECT id, name, kind, scopes, projects, sponsor, created_at, expires_at, revoked_at, last_used_at, person, grants`

// tokenColsT is tokenCols for a query where tokens is "t".
const tokenColsT = `SELECT t.id, t.name, t.kind, t.scopes, t.projects, t.sponsor, t.created_at, t.expires_at, t.revoked_at, t.last_used_at, t.person, t.grants`

type scanner interface{ Scan(dest ...any) error }

// scanToken reads tokenCols, then any extra columns into extra.
func scanToken(r scanner, extra ...any) (*Token, error) {
	var t Token
	var scopes, projects, created string
	var sponsor, expires, revoked, used, person, grants sql.NullString
	if err := r.Scan(append([]any{&t.ID, &t.Name, &t.Kind, &scopes, &projects, &sponsor, &created, &expires, &revoked, &used, &person, &grants}, extra...)...); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(scopes), &t.Scopes)
	_ = json.Unmarshal([]byte(projects), &t.Projects)
	t.Sponsor = sponsor.String
	t.Person = person.String
	t.CreatedAt = parseTS(created)
	t.ExpiresAt = parseNullTS(expires)
	t.RevokedAt = parseNullTS(revoked)
	t.LastUsedAt = parseNullTS(used)
	if grants.Valid {
		_ = json.Unmarshal([]byte(grants.String), &t.Grants)
	}
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
