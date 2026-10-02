// Package api is Tiffin's one public interface. Every operation is declared
// once here, with huma, and becomes an HTTP endpoint, an MCP tool
// (internal/mcp) and a CLI command (internal/cli) — all from the same
// OpenAPI document. The dashboard uses this API and nothing else.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/state"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

// Extension keys on operations, read by the MCP and CLI generators.
const (
	// ExtRisk is the operation's risk class: "read", "write" or "destructive".
	// Plan-driven operations are "destructive" because the plan they apply
	// may be; the plan's own tier decides what the token needs.
	ExtRisk = "x-tiffin-risk"
	// ExtCLI is the CLI command path, e.g. "tokens create".
	ExtCLI = "x-tiffin-cli"
)

// Risk classes for operations.
const (
	RiskRead        = "read"
	RiskWrite       = "write"
	RiskDestructive = "destructive"
)

// SessionHeader carries an agent's session label into the change log.
const SessionHeader = "X-Tiffin-Session"

// Deps are the services the API serves.
type Deps struct {
	DB      *state.DB
	Engine  *change.Engine
	Tokens  *tokens.Manager
	Version string
}

// API is the HTTP API.
type API struct {
	api  huma.API
	mux  *http.ServeMux
	deps Deps
}

// New builds the API. Deps may be zero-valued when only the OpenAPI
// document is needed (the CLI and MCP generators do this).
func New(d Deps) *API {
	mux := http.NewServeMux()
	cfg := huma.DefaultConfig("Tiffin", orDefault(d.Version, "dev"))
	cfg.Info.Description = "Your app in a box. Every mutation is a Change: plan it, review the risk, then apply it with the plan's hash."
	cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"bearer": {Type: "http", Scheme: "bearer", Description: "A Tiffin token (tfn_...). Set TIFFIN_TOKEN for the CLI."},
	}
	cfg.DocsPath = "/v1/docs"
	cfg.OpenAPIPath = "/v1/openapi"
	cfg.SchemasPath = "/v1/schemas"
	cfg.CreateHooks = nil // no $schema links injected into responses
	a := &API{api: humago.New(mux, cfg), mux: mux, deps: d}
	a.api.UseMiddleware(a.authenticate)
	a.register()
	return a
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// Handler serves the API.
func (a *API) Handler() http.Handler { return a.mux }

// OpenAPI returns the API description.
func (a *API) OpenAPI() *huma.OpenAPI { return a.api.OpenAPI() }

type ctxKey struct{}

// PrincipalFrom returns the authenticated caller, if any.
func PrincipalFrom(ctx context.Context) *tokens.Principal {
	p, _ := ctx.Value(ctxKey{}).(*tokens.Principal)
	return p
}

func (a *API) authenticate(ctx huma.Context, next func(huma.Context)) {
	if len(ctx.Operation().Security) == 0 {
		next(ctx)
		return
	}
	p, err := a.deps.Tokens.Authenticate(ctx.Context(), ctx.Header("Authorization"))
	if err != nil {
		_ = huma.WriteErr(a.api, ctx, http.StatusUnauthorized, "")
		return
	}
	if s := strings.TrimSpace(ctx.Header(SessionHeader)); s != "" && len(s) <= 128 {
		p.Session = s
	}
	next(huma.WithValue(ctx, ctxKey{}, p))
}

var bearer = []map[string][]string{{"bearer": {}}}

// op declares one operation with its risk class and CLI path.
func op(id, method, path, cli, risk, summary, desc string, tags ...string) huma.Operation {
	return huma.Operation{
		OperationID: id,
		Method:      method,
		Path:        path,
		Summary:     summary,
		Description: desc,
		Tags:        tags,
		Security:    bearer,
		Extensions:  map[string]any{ExtRisk: risk, ExtCLI: cli},
		Errors:      []int{400, 401, 403, 422},
	}
}

// wrap converts domain errors from h into Problems.
func wrap[I, O any](h func(context.Context, *I) (*O, error)) func(context.Context, *I) (*O, error) {
	return func(ctx context.Context, in *I) (*O, error) {
		out, err := h(ctx, in)
		if err != nil {
			return nil, toProblem(err)
		}
		return out, nil
	}
}

// ---- inputs and outputs ----

// ManifestJSON is a manifest as sent by clients: any subset of the fields in
// GET /v1/schema/manifest. The server validates and applies defaults.
type ManifestJSON json.RawMessage

func (m ManifestJSON) MarshalJSON() ([]byte, error) {
	if len(m) == 0 {
		return []byte("null"), nil
	}
	return m, nil
}

func (m *ManifestJSON) UnmarshalJSON(b []byte) error {
	*m = append((*m)[:0], b...)
	return nil
}

// Schema implements huma.SchemaProvider. The full schema lives at
// /v1/schema/manifest; the MCP generator inlines it into tool inputs.
func (ManifestJSON) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{
		Type:                 "object",
		Description:          "The project manifest (the evaluated tiffin.config.ts). Only `project` is required; everything else has defaults. Full schema: GET /v1/schema/manifest.",
		AdditionalProperties: true,
		Extensions:           map[string]any{"x-tiffin-manifest": true},
	}
}

type planBody struct {
	Manifest ManifestJSON `json:"manifest" required:"true"`
}

type applyBody struct {
	Manifest ManifestJSON `json:"manifest" required:"true"`
	Confirm  string       `json:"confirm,omitempty" doc:"The plan hash (or its first 8+ characters) you reviewed. Without it nothing is applied and the plan comes back with status 428."`
	Intent   string       `json:"intent,omitempty" maxLength:"500" doc:"Why you are making this change, in one sentence. Shown in the activity timeline."`
}

type undoBody struct {
	Confirm string `json:"confirm,omitempty" doc:"The undo plan's hash (or its first 8+ characters). Without it the undo plan comes back with status 428."`
	Intent  string `json:"intent,omitempty" maxLength:"500" doc:"Why you are undoing, in one sentence."`
}

// ApplyResult is the outcome of a confirmed apply or undo.
type ApplyResult struct {
	Applied bool           `json:"applied" doc:"False when the plan was empty (nothing to do)."`
	Change  *change.Change `json:"change,omitempty"`
	Plan    *change.Plan   `json:"plan"`
}

// ProjectSummary is one row of the project list.
type ProjectSummary struct {
	Name      string `json:"name"`
	Version   int64  `json:"version"`
	Resources int    `json:"resources"`
}

// ProjectState is a project's current resources.
type ProjectState struct {
	Name      string            `json:"name"`
	Version   int64             `json:"version"`
	Resources []change.Resource `json:"resources"`
}

// Health is the unauthenticated liveness report.
type Health struct {
	Status  string `json:"status" example:"ok"`
	Version string `json:"version"`
}

// CreatedToken carries the secret exactly once.
type CreatedToken struct {
	Secret string        `json:"secret" doc:"The token secret. Shown once; store it now."`
	Token  *tokens.Token `json:"token"`
}

type tokenCreateBody struct {
	Name     string   `json:"name" minLength:"1" maxLength:"64" doc:"A name you will recognise in the activity timeline, e.g. claude-code."`
	Kind     string   `json:"kind,omitempty" enum:"agent,human" doc:"Default agent. Agent tokens always expire."`
	Scopes   []string `json:"scopes,omitempty" doc:"Default read, plan, apply:reversible. One of read, plan, apply:reversible, apply:outbound, apply:irreversible, tokens, *. You can only grant scopes you hold."`
	Projects []string `json:"projects,omitempty" doc:"Projects the token may touch. Default: all of yours. \"*\" means all."`
	TTLHours int      `json:"ttlHours,omitempty" minimum:"0" maximum:"8760" doc:"Lifetime in hours. Default 720 (30 days) for agents; 0 means never for humans."`
}

func (a *API) register() {
	api := a.api

	h := op("health", http.MethodGet, "/v1/health", "health", RiskRead, "Check the box is up", "Unauthenticated liveness check.", "system")
	h.Security = nil
	huma.Register(api, h, func(ctx context.Context, _ *struct{}) (*struct{ Body Health }, error) {
		return &struct{ Body Health }{Health{Status: "ok", Version: orDefault(a.deps.Version, "dev")}}, nil
	})

	s := op("schema-manifest", http.MethodGet, "/v1/schema/manifest", "schema manifest", RiskRead, "Get the manifest JSON Schema",
		"The JSON Schema for tiffin.config.ts, with a description and default for every field.", "system")
	s.Security = nil
	huma.Register(api, s, func(ctx context.Context, _ *struct{}) (*struct{ Body json.RawMessage }, error) {
		return &struct{ Body json.RawMessage }{json.RawMessage(manifest.Schema())}, nil
	})

	huma.Register(api, op("whoami", http.MethodGet, "/v1/whoami", "whoami", RiskRead, "Show the current token",
		"Who you are: token name, kind, scopes and projects.", "system"),
		wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body *tokens.Principal }, error) {
			return &struct{ Body *tokens.Principal }{PrincipalFrom(ctx)}, nil
		}))

	huma.Register(api, op("projects-list", http.MethodGet, "/v1/projects", "projects list", RiskRead, "List projects",
		"Projects on this box that your token can see, with their current version.", "projects"),
		wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body []ProjectSummary }, error) {
			p := PrincipalFrom(ctx)
			if err := p.Require(tokens.ScopeRead, ""); err != nil {
				return nil, err
			}
			names, err := a.deps.DB.ListProjects(ctx)
			if err != nil {
				return nil, err
			}
			out := []ProjectSummary{}
			for _, n := range names {
				if !p.CanProject(n) {
					continue
				}
				v, res, err := a.deps.DB.Load(ctx, n)
				if err != nil {
					return nil, err
				}
				out = append(out, ProjectSummary{Name: n, Version: v, Resources: len(res)})
			}
			return &struct{ Body []ProjectSummary }{out}, nil
		}))

	type projectPath struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
	}
	huma.Register(api, op("project-get", http.MethodGet, "/v1/projects/{project}", "projects get", RiskRead, "Get a project's state",
		"The project's current resources and version.", "projects"),
		wrap(func(ctx context.Context, in *projectPath) (*struct{ Body ProjectState }, error) {
			if err := PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
				return nil, err
			}
			v, res, err := a.deps.DB.Load(ctx, in.Project)
			if err != nil {
				return nil, err
			}
			if v == 0 {
				return nil, problem(404, "not_found", "project "+in.Project+" does not exist")
			}
			out := ProjectState{Name: in.Project, Version: v, Resources: make([]change.Resource, 0, len(res))}
			for _, r := range res {
				out.Resources = append(out.Resources, r)
			}
			sort.Slice(out.Resources, func(i, j int) bool { return out.Resources[i].Address < out.Resources[j].Address })
			return &struct{ Body ProjectState }{out}, nil
		}))

	huma.Register(api, op("plan", http.MethodPost, "/v1/plan", "plan", RiskRead, "Plan a manifest",
		"Dry run: what applying this manifest would change, the risk of each step and the plan hash. Never writes.", "changes"),
		wrap(func(ctx context.Context, in *struct{ Body planBody }) (*struct{ Body *change.Plan }, error) {
			m, desired, err := parseManifest(in.Body.Manifest)
			if err != nil {
				return nil, err
			}
			if err := PrincipalFrom(ctx).Require(tokens.ScopePlan, m.Project); err != nil {
				return nil, err
			}
			p, err := a.deps.Engine.Plan(ctx, m.Project, desired)
			if err != nil {
				return nil, err
			}
			return &struct{ Body *change.Plan }{p}, nil
		}))

	ap := op("apply", http.MethodPost, "/v1/apply", "apply", RiskDestructive, "Apply a manifest",
		"Plans the manifest and applies it if `confirm` matches the plan hash and your token's scopes cover the plan's risk. "+
			"Without `confirm` (or with a stale one) nothing changes: you get status 428 with the plan to review. "+
			"Risk tiers: reversible needs apply:reversible; outbound needs apply:outbound; irreversible needs apply:irreversible.", "changes")
	ap.Errors = append(ap.Errors, 409, 428)
	huma.Register(api, ap,
		wrap(func(ctx context.Context, in *struct{ Body applyBody }) (*struct{ Body ApplyResult }, error) {
			m, desired, err := parseManifest(in.Body.Manifest)
			if err != nil {
				return nil, err
			}
			p := PrincipalFrom(ctx)
			if err := p.Require(tokens.ScopePlan, m.Project); err != nil {
				return nil, err
			}
			plan, err := a.deps.Engine.Plan(ctx, m.Project, desired)
			if err != nil {
				return nil, err
			}
			return a.apply(ctx, p, plan, in.Body.Confirm, in.Body.Intent)
		}))

	type changesQuery struct {
		Project string `query:"project" doc:"Only this project"`
		Limit   int    `query:"limit" minimum:"1" maximum:"200" default:"50" doc:"Maximum changes to return"`
	}
	huma.Register(api, op("changes-list", http.MethodGet, "/v1/changes", "changes list", RiskRead, "List changes",
		"The change log, newest first: who changed what, why, the risk and whether it was undone.", "changes"),
		wrap(func(ctx context.Context, in *changesQuery) (*struct{ Body []*change.Change }, error) {
			p := PrincipalFrom(ctx)
			if err := p.Require(tokens.ScopeRead, in.Project); err != nil {
				return nil, err
			}
			cs, err := a.deps.DB.ListChanges(ctx, change.ListFilter{Project: in.Project, Limit: in.Limit})
			if err != nil {
				return nil, err
			}
			out := []*change.Change{}
			for _, c := range cs {
				if p.CanProject(c.Project) {
					out = append(out, c)
				}
			}
			return &struct{ Body []*change.Change }{out}, nil
		}))

	type changePath struct {
		ID string `path:"id" pattern:"^chg_[0-9A-Z]{26}$" doc:"Change ID"`
	}
	huma.Register(api, op("change-get", http.MethodGet, "/v1/changes/{id}", "changes get", RiskRead, "Get a change",
		"One change with its full plan and inverse.", "changes"),
		wrap(func(ctx context.Context, in *changePath) (*struct{ Body *change.Change }, error) {
			c, err := a.deps.DB.GetChange(ctx, in.ID)
			if err != nil {
				return nil, err
			}
			if err := PrincipalFrom(ctx).Require(tokens.ScopeRead, c.Project); err != nil {
				return nil, change.ErrNotFound // don't reveal other projects' changes
			}
			return &struct{ Body *change.Change }{c}, nil
		}))

	un := op("change-undo", http.MethodPost, "/v1/changes/{id}/undo", "changes undo", RiskDestructive, "Undo a change",
		"Reverts a change by applying its inverse, if nothing it touched has changed since. "+
			"Without `confirm` you get status 428 with the undo plan to review.", "changes")
	un.Errors = append(un.Errors, 404, 409, 428)
	huma.Register(api, un,
		wrap(func(ctx context.Context, in *struct {
			ID   string `path:"id" pattern:"^chg_[0-9A-Z]{26}$" doc:"Change ID"`
			Body undoBody
		}) (*struct{ Body ApplyResult }, error) {
			p := PrincipalFrom(ctx)
			c, err := a.deps.DB.GetChange(ctx, in.ID)
			if err != nil {
				return nil, err
			}
			if err := p.Require(tokens.ScopePlan, c.Project); err != nil {
				return nil, err
			}
			plan, err := a.deps.Engine.PlanUndo(ctx, in.ID)
			if err != nil {
				return nil, err
			}
			intent := in.Body.Intent
			if intent == "" {
				intent = "undo " + in.ID + ": " + c.Intent
			}
			return a.apply(ctx, p, plan, in.Body.Confirm, intent)
		}))

	huma.Register(api, op("tokens-list", http.MethodGet, "/v1/tokens", "tokens list", RiskRead, "List tokens",
		"Active tokens (secrets are never shown).", "tokens"),
		wrap(func(ctx context.Context, in *struct {
			Revoked bool `query:"revoked" doc:"Include revoked tokens"`
		}) (*struct{ Body []*tokens.Token }, error) {
			if err := PrincipalFrom(ctx).Require(tokens.ScopeTokens, ""); err != nil {
				return nil, err
			}
			ts, err := a.deps.Tokens.List(ctx, in.Revoked)
			if ts == nil {
				ts = []*tokens.Token{}
			}
			return &struct{ Body []*tokens.Token }{ts}, err
		}))

	huma.Register(api, op("token-create", http.MethodPost, "/v1/tokens", "tokens create", RiskWrite, "Create a token",
		"Mints a scoped token, e.g. for an agent. You can only grant scopes and projects you hold. The secret is returned once.", "tokens"),
		wrap(func(ctx context.Context, in *struct{ Body tokenCreateBody }) (*struct{ Body CreatedToken }, error) {
			req := tokens.CreateRequest{Name: in.Body.Name, Kind: in.Body.Kind, Projects: in.Body.Projects}
			for _, s := range in.Body.Scopes {
				req.Scopes = append(req.Scopes, tokens.Scope(s))
			}
			req.TTL = hours(in.Body.TTLHours)
			secret, t, err := a.deps.Tokens.Create(ctx, PrincipalFrom(ctx), req)
			if err != nil {
				return nil, err
			}
			return &struct{ Body CreatedToken }{CreatedToken{Secret: secret, Token: t}}, nil
		}))

	rv := op("token-revoke", http.MethodDelete, "/v1/tokens/{id}", "tokens revoke", RiskDestructive, "Revoke a token",
		"Revokes a token and every token it minted.", "tokens")
	rv.Errors = append(rv.Errors, 404)
	huma.Register(api, rv,
		wrap(func(ctx context.Context, in *struct {
			ID string `path:"id" pattern:"^tok_[0-9A-Z]{26}$" doc:"Token ID"`
		}) (*struct{}, error) {
			return &struct{}{}, a.deps.Tokens.Revoke(ctx, PrincipalFrom(ctx), in.ID)
		}))

	huma.Register(api, op("audit-list", http.MethodGet, "/v1/audit", "audit list", RiskRead, "List audit events",
		"Security events that are not changes: tokens minted and revoked.", "system"),
		wrap(func(ctx context.Context, in *struct {
			Limit int `query:"limit" minimum:"1" maximum:"500" default:"50"`
		}) (*struct{ Body []state.AuditEvent }, error) {
			if err := PrincipalFrom(ctx).Require(tokens.ScopeTokens, ""); err != nil {
				return nil, err
			}
			ev, err := a.deps.DB.AuditLog(ctx, in.Limit)
			if ev == nil {
				ev = []state.AuditEvent{}
			}
			return &struct{ Body []state.AuditEvent }{ev}, err
		}))
}

func (a *API) apply(ctx context.Context, p *tokens.Principal, plan *change.Plan, confirm, intent string) (*struct{ Body ApplyResult }, error) {
	c, err := a.deps.Engine.Apply(ctx, change.ApplyRequest{
		Plan: plan, Confirm: confirm, Actor: p.Actor(), Intent: intent, Authorize: p.Authorizer(),
	})
	if err != nil {
		return nil, err
	}
	return &struct{ Body ApplyResult }{ApplyResult{Applied: c != nil, Change: c, Plan: plan}}, nil
}

func parseManifest(raw ManifestJSON) (*manifest.Manifest, map[string]change.Resource, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil, problem(422, "validation", "manifest is required")
	}
	m, err := manifest.Parse(raw)
	if err != nil {
		return nil, nil, err
	}
	desired, err := change.Resources(m)
	return m, desired, err
}

// RiskOf returns an operation's risk class.
func RiskOf(o *huma.Operation) string {
	if r, ok := o.Extensions[ExtRisk].(string); ok {
		return r
	}
	return RiskDestructive
}

// CLIPath returns an operation's CLI command words.
func CLIPath(o *huma.Operation) []string {
	if c, ok := o.Extensions[ExtCLI].(string); ok && c != "" {
		return strings.Fields(c)
	}
	return []string{o.OperationID}
}

// Operations returns every operation, sorted by ID.
func (a *API) Operations() []*huma.Operation {
	var ops []*huma.Operation
	for _, item := range a.OpenAPI().Paths {
		for _, o := range []*huma.Operation{item.Get, item.Post, item.Put, item.Patch, item.Delete} {
			if o != nil {
				ops = append(ops, o)
			}
		}
	}
	slices.SortFunc(ops, func(x, y *huma.Operation) int { return strings.Compare(x.OperationID, y.OperationID) })
	return ops
}

func hours(h int) time.Duration { return time.Duration(h) * time.Hour }
