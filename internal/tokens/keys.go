package tokens

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/ids"
	"github.com/danielgtaylor/huma/v2"
)

// API keys are what people and agents see: a key reaches some projects
// ("all", which includes projects created later, or a list) at an access
// level ("full" or "read"). A key's grants are stored as a list of
// {projects, level} pairs so new levels (say "deploy") can come later
// without a migration; underneath, each level maps onto scopes:
//
//	read → read, plan
//	full → read, plan and every apply tier (reversible, outbound, irreversible)
//
// Full access to all projects is the box admin (scope "*"): it can also
// manage keys, people, box settings and exports/imports. No other key can
// manage keys.
//
// Tokens minted before keys existed keep their exact scopes; they are shown
// as the nearest key (with a note when they can do less than that).

// Access levels.
const (
	LevelRead = "read"
	LevelFull = "full"
)

// AllProjects is the projects value meaning every project, now and later.
const AllProjects = "*"

// Grant is one {projects, level} pair of a key.
type Grant struct {
	Projects []string `json:"projects"` // ["*"] means all, including later ones
	Level    string   `json:"level"`
}

// Projects is a key's reach: "all" in JSON, or a list of project slugs.
// In Go, all is []string{"*"}.
type Projects []string

// All reports whether p means every project.
func (p Projects) All() bool { return slices.Contains(p, AllProjects) }

// MarshalJSON writes "all" or the list.
func (p Projects) MarshalJSON() ([]byte, error) {
	if p.All() {
		return []byte(`"all"`), nil
	}
	return json.Marshal([]string(p))
}

// UnmarshalJSON reads "all" or a list.
func (p *Projects) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		if s != "all" {
			return fmt.Errorf(`projects must be "all" or a list of project names`)
		}
		*p = Projects{AllProjects}
		return nil
	}
	var l []string
	if err := json.Unmarshal(b, &l); err != nil {
		return fmt.Errorf(`projects must be "all" or a list of project names`)
	}
	*p = l
	return nil
}

// Schema implements huma.SchemaProvider: "all" or a list of slugs.
func (Projects) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{
		Description: `"all" (every project, including ones created later) or a list of project names`,
		OneOf: []*huma.Schema{
			{Type: "string", Enum: []any{"all"}},
			{Type: "array", MinItems: ptr(1), MaxItems: ptr(100), Items: &huma.Schema{Type: "string", Pattern: projectPattern}},
		},
	}
}

func ptr(i int) *int { return &i }

const projectPattern = `^[a-z][a-z0-9-]{0,39}$`

var projectRE = regexp.MustCompile(projectPattern)

// Key is an API key as shown to people and agents.
type Key struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Projects   Projects   `json:"projects"`
	Access     string     `json:"access" enum:"full,read" doc:"full: read, plan and apply any change, irreversible ones included. read: read and plan only."`
	Admin      bool       `json:"admin" doc:"Full access to all projects: this key also manages keys, people, box settings and exports/imports"`
	ExpiresAt  *time.Time `json:"expiresAt" doc:"When it stops working; null means never"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
	Note       string     `json:"note,omitempty" doc:"For keys made before API keys existed whose permissions are narrower than their access suggests"`
}

// KeyRequest creates a key.
type KeyRequest struct {
	Name     string
	Projects Projects
	Access   string
	// TTL is how long it lives; 0 means it never expires.
	TTL time.Duration
}

// levelScopes maps a level on some projects onto scopes.
func levelScopes(level string, all bool) []Scope {
	switch level {
	case LevelFull:
		if all {
			return []Scope{ScopeAll}
		}
		return []Scope{ScopeRead, ScopePlan, ScopeApplyReversible, ScopeApplyOutbound, ScopeApplyIrreversible}
	default:
		return []Scope{ScopeRead, ScopePlan}
	}
}

// grantScopes flattens grants into scopes and projects. Keys made through
// the API have one grant, for which this is exact.
func grantScopes(gs []Grant) ([]Scope, []string) {
	var scopes []Scope
	var projects []string
	for _, g := range gs {
		scopes = append(scopes, levelScopes(g.Level, slices.Contains(g.Projects, AllProjects))...)
		projects = append(projects, g.Projects...)
	}
	return dedupe(scopes), dedupe(projects)
}

// AsKey shows a token as an API key.
func (t *Token) AsKey() *Key {
	k := &Key{ID: t.ID, Name: t.Name, ExpiresAt: t.ExpiresAt, LastUsedAt: t.LastUsedAt, CreatedAt: t.CreatedAt, RevokedAt: t.RevokedAt}
	gs := t.Grants
	if len(gs) == 0 {
		gs = []Grant{nearestGrant(t.Scopes, t.Projects)}
		k.Note = legacyNote(t.Scopes)
	}
	k.Projects, k.Access = Projects(gs[0].Projects), gs[0].Level
	p := &Principal{Scopes: t.Scopes, Projects: t.Projects}
	k.Admin = p.BoxAdmin()
	return k
}

func nearestGrant(scopes []Scope, projects []string) Grant {
	p := &Principal{Scopes: scopes, Projects: projects}
	g := Grant{Projects: slices.Clone(projects), Level: LevelRead}
	if p.Has(ScopeApplyReversible) {
		g.Level = LevelFull
	}
	if slices.Contains(projects, AllProjects) {
		g.Projects = []string{AllProjects}
	}
	return g
}

func legacyNote(scopes []Scope) string {
	p := &Principal{Scopes: scopes}
	switch {
	case p.Has(ScopeApplyIrreversible):
		return ""
	case p.Has(ScopeApplyOutbound):
		return "an older key: it applies reversible and outbound changes but not irreversible ones (deleting data)"
	case p.Has(ScopeApplyReversible):
		return "an older key: it applies reversible changes only"
	case !p.Has(ScopePlan):
		return "an older key: it reads but cannot plan"
	}
	return ""
}

// Access is the key access level of p: full or read.
func (p *Principal) access() string {
	if p.Has(ScopeApplyReversible) {
		return LevelFull
	}
	return LevelRead
}

// ErrNotAdmin refuses key management to keys that are not the box admin.
var ErrNotAdmin = fmt.Errorf("%w: only a key with full access to all projects (or an owner or admin person) can manage keys", ErrForbidden)

// CreateKey mints an API key. Only the box admin may.
func (m *Manager) CreateKey(ctx context.Context, by *Principal, req KeyRequest) (string, *Key, error) {
	if !by.BoxAdmin() {
		return "", nil, ErrNotAdmin
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 64 {
		return "", nil, fmt.Errorf("%w: name must be 1-64 characters", ErrInvalid)
	}
	if req.Access != LevelFull && req.Access != LevelRead {
		return "", nil, fmt.Errorf(`%w: access must be "full" or "read"`, ErrInvalid)
	}
	projects := dedupe([]string(req.Projects))
	if len(projects) == 0 {
		return "", nil, fmt.Errorf(`%w: projects must be "all" or a list of project names`, ErrInvalid)
	}
	if slices.Contains(projects, AllProjects) {
		projects = []string{AllProjects}
	}
	for _, p := range projects {
		if p != AllProjects && !projectRE.MatchString(p) {
			return "", nil, fmt.Errorf("%w: %q is not a project name", ErrInvalid, p)
		}
	}
	if req.TTL < 0 || req.TTL > MaxAgentTTL {
		return "", nil, fmt.Errorf("%w: a key lives at most %s", ErrInvalid, MaxAgentTTL)
	}
	grants := []Grant{{Projects: projects, Level: req.Access}}
	scopes, ps := grantScopes(grants)
	now := m.now().UTC()
	t := &Token{ID: ids.New("tok"), Name: req.Name, Kind: KindAgent, Scopes: scopes, Projects: ps, Grants: grants,
		Sponsor: by.TokenID, CreatedAt: now}
	if req.TTL > 0 {
		exp := now.Add(req.TTL)
		t.ExpiresAt = &exp
	}
	if by.ExpiresAt != nil && (t.ExpiresAt == nil || t.ExpiresAt.After(*by.ExpiresAt)) {
		exp := by.ExpiresAt.UTC()
		t.ExpiresAt = &exp
	}
	secret := newSecret()
	if err := m.insert(ctx, t, secret); err != nil {
		return "", nil, err
	}
	_ = m.db.Audit(ctx, by.TokenID, "token.create", t.ID, map[string]any{"name": t.Name, "projects": projects, "access": req.Access, "expiresAt": t.ExpiresAt})
	return secret, t.AsKey(), nil
}

// refusal explains, in plain words, why p lacks scope s.
func (p *Principal) refusal(s Scope) error {
	switch {
	case s == ScopeTokens || s == ScopeAll:
		return ErrNotAdmin
	case !p.Has(ScopeApplyReversible) && slices.Index(ladder, s) >= slices.Index(ladder, ScopeApplyReversible):
		return fmt.Errorf("%w: this key (%q) is read only", ErrForbidden, p.Name)
	case !p.Has(ScopePlan) && s == ScopePlan:
		return fmt.Errorf("%w: this key (%q) can read but not plan", ErrForbidden, p.Name)
	case slices.Contains(ladder, s):
		return fmt.Errorf("%w: this key (%q) cannot apply %s changes (it needs %q)", ErrForbidden, p.Name, tierWords[s], s)
	}
	return fmt.Errorf("%w: this key (%q) lacks scope %q", ErrForbidden, p.Name, s)
}

var tierWords = map[Scope]string{ScopeApplyOutbound: "outbound", ScopeApplyIrreversible: "irreversible", ScopeRead: "read"}

// projectRefusal explains that project is outside p's reach.
func (p *Principal) projectRefusal(project string) error {
	var ps []string
	for _, x := range p.Projects {
		if x != AllProjects {
			ps = append(ps, x)
		}
	}
	verb := "change"
	if !p.Has(ScopeApplyReversible) {
		verb = "read"
	}
	reach := "no project"
	if len(ps) > 0 {
		reach = strings.Join(ps, ", ")
	}
	return fmt.Errorf("%w: this key (%q) can only %s %s, not %s", ErrForbidden, p.Name, verb, reach, project)
}
