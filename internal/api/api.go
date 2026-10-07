// Package api is Tiffin's one public interface. Every operation is declared
// once here, with huma, and becomes an HTTP endpoint, an MCP tool
// (internal/mcp) and a CLI command (internal/cli) — all from the same
// OpenAPI document. The dashboard uses this API and nothing else.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/passkeys"
	"github.com/btahir/tiffin/internal/platform"
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
	// ExtUntrusted marks operations whose output carries content written by
	// others (logs, rows, emails, files). MCP wraps it so agents treat it as
	// data, never as instructions.
	ExtUntrusted = "x-tiffin-untrusted"
	// ExtConfirm marks plan-driven operations: without a confirm hash they
	// only return the plan (status 428) and change nothing.
	ExtConfirm = "x-tiffin-confirm"
	// ExtOutbound marks operations that reach outside the box (fetching a
	// public git repository, say). MCP sets openWorldHint on them.
	ExtOutbound = "x-tiffin-outbound"
)

// Risk classes for operations.
const (
	RiskRead        = "read"
	RiskWrite       = "write"
	RiskDestructive = "destructive"
)

// SessionCookie holds a dashboard session token.
const SessionCookie = "tiffin_session"

// SessionHeader carries an agent's session label into the change log.
const SessionHeader = "X-Tiffin-Session"

// ModelHeader carries the model an agent says it runs (self-reported, shown
// beside its name in the change log).
const ModelHeader = "X-Tiffin-Model"

// Deps are the services the API serves.
type Deps struct {
	DB      *state.DB
	Engine  *change.Engine
	Tokens  *tokens.Manager
	Version string
	// Checks adds box-level health checks (disk, edge, services) to /v1/status.
	Checks func(ctx context.Context) []Check
	// PublicURL is where the dashboard is reached, for login links.
	PublicURL string
	// Platform is the box runtime (nil off-box and when only the spec is built).
	Platform *platform.Platform
	// Passkeys signs people in to the dashboard (nil off-box).
	Passkeys *passkeys.Manager
}

// API is the HTTP API.
type API struct {
	api   huma.API
	mux   *http.ServeMux
	deps  Deps
	idem  idemState
	bg    sync.WaitGroup // box mail sent in the background
	oauth *oauthFlow     // dashboard sign-in with Google or GitHub
}

// New builds the API. Deps may be zero-valued when only the OpenAPI
// document is needed (the CLI and MCP generators do this).
func New(d Deps) *API {
	mux := http.NewServeMux()
	cfg := huma.DefaultConfig("Tiffin", orDefault(d.Version, "dev"))
	// Modules define their own types; namespace them so two modules can both
	// have an "Alert" without colliding in the OpenAPI document.
	cfg.Components.Schemas = huma.NewMapRegistry("#/components/schemas/", schemaNamer)
	cfg.Info.Description = "Your app in a box. Every mutation is a Change: plan it, review the risk, then apply it with the plan's hash."
	cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"bearer": {Type: "http", Scheme: "bearer", Description: "A Tiffin token (tfn_...). Set TIFFIN_TOKEN for the CLI."},
	}
	cfg.DocsPath = "/v1/docs"
	cfg.OpenAPIPath = "/v1/openapi"
	cfg.SchemasPath = "/v1/schemas"
	cfg.CreateHooks = nil // no $schema links injected into responses
	a := &API{api: humago.New(mux, cfg), mux: mux, deps: d}
	a.api.UseMiddleware(a.authenticate, a.idempotent)
	a.register()
	a.registerIdempotency()
	a.registerBox()
	a.registerSecrets()
	a.registerPasskeys()
	a.registerPasskeySignIn()
	a.registerSessionConfirm()
	a.registerPeople()
	a.registerSessions()
	a.registerBoxMail()
	a.registerOAuthSignIn()
	a.registerAppearance()
	// Modules add their own operations; they become CLI commands and MCP tools too.
	for _, m := range platform.Modules() {
		if r, ok := m.(platform.APIRegistrar); ok {
			r.RegisterAPI(a.api, d.Platform)
		}
	}
	return a
}

func schemaNamer(t reflect.Type, hint string) string {
	name := huma.DefaultSchemaNamer(t, hint)
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Map {
		t = t.Elem()
	}
	if strings.HasSuffix(t.PkgPath(), "/internal/manifest") && !strings.HasPrefix(name, "Manifest") {
		// Manifest types (App, Queue, Auth...) would collide with module types.
		return "Manifest" + name
	}
	if pkg := t.PkgPath(); strings.Contains(pkg, "/internal/mod/") {
		mod := pkg[strings.LastIndex(pkg, "/")+1:]
		if mod != "" && !strings.HasPrefix(strings.ToLower(name), mod) {
			name = strings.ToUpper(mod[:1]) + mod[1:] + name
		}
	}
	return name
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
	cred := ctx.Header("Authorization")
	if cred == "" {
		// The dashboard authenticates with an HttpOnly, SameSite=Strict cookie.
		// Apps on the box are the same site as the dashboard, so the browser
		// sends it with their requests too: changes must come from the
		// dashboard's own pages.
		if c, err := huma.ReadCookie(ctx, SessionCookie); err == nil {
			if !sameOrigin(ctx) {
				_ = huma.WriteErr(a.api, ctx, http.StatusForbidden, "This request came from another site; use the dashboard or an API key.")
				return
			}
			cred = c.Value
		}
	}
	p, err := a.deps.Tokens.Authenticate(ctx.Context(), cred)
	if err != nil {
		_ = huma.WriteErr(a.api, ctx, http.StatusUnauthorized, "")
		return
	}
	if s := strings.TrimSpace(ctx.Header(SessionHeader)); s != "" && len(s) <= 128 {
		p.Session = s
	}
	if m := strings.TrimSpace(ctx.Header(ModelHeader)); m != "" && len(m) <= 64 && p.Kind == "agent" {
		p.Model = m
	}
	if p.Kind == tokens.KindOwner && a.deps.Platform != nil {
		// On a server, the address the owner's CLI uses is never banned.
		a.deps.Platform.OwnerSeen(ctx.Context(), clientIP(ctx.RemoteAddr(), ctx.Header("X-Forwarded-For")))
	}
	next(huma.WithValue(ctx, ctxKey{}, p))
}

var bearer = []map[string][]string{{"bearer": {}}}

// Op declares an operation for modules: id, method, path, CLI words, risk
// class (RiskRead/RiskWrite/RiskDestructive), summary, description, tags.
func Op(id, method, path, cli, risk, summary, desc string, tags ...string) huma.Operation {
	return op(id, method, path, cli, risk, summary, desc, tags...)
}

// Untrusted marks an operation's output as untrusted data (see ExtUntrusted).
func Untrusted(o huma.Operation) huma.Operation {
	o.Extensions[ExtUntrusted] = true
	return o
}

// IsUntrusted reports whether an operation's output is untrusted data.
func IsUntrusted(o *huma.Operation) bool { v, _ := o.Extensions[ExtUntrusted].(bool); return v }

// Outbound marks an operation that reaches outside the box (see ExtOutbound).
func Outbound(o huma.Operation) huma.Operation {
	o.Extensions[ExtOutbound] = true
	return o
}

// IsOutbound reports whether an operation reaches outside the box.
func IsOutbound(o *huma.Operation) bool { v, _ := o.Extensions[ExtOutbound].(bool); return v }

// Wrap converts domain errors returned by a module handler into Problems.
func Wrap[I, O any](h func(context.Context, *I) (*O, error)) func(context.Context, *I) (*O, error) {
	return wrap(h)
}

// NewProblem builds an API problem with a stable code (see Problem.Code).
func NewProblem(status int, code, detail string) *Problem { return problem(status, code, detail) }

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

type destroyBody struct {
	Confirm string `json:"confirm,omitempty" doc:"The hash of the plan that deletes the project (or its first 8+ characters), which you reviewed. Without it nothing is deleted and the plan comes back with status 428."`
	Intent  string `json:"intent,omitempty" maxLength:"500" doc:"Why you are deleting the project, in one sentence."`
}

// ApplyResult is the outcome of a confirmed apply or undo.
type ApplyResult struct {
	Applied bool           `json:"applied" doc:"False when the plan was empty (nothing to do)."`
	Change  *change.Change `json:"change,omitempty" doc:"The applied change; its plan is change.plan."`
	Plan    *change.Plan   `json:"plan,omitempty" doc:"The (empty) plan, only when nothing was applied."`
}

// ProjectSummary is one row of the project list.
type ProjectSummary struct {
	Name      string   `json:"name"`
	Version   int64    `json:"version"`
	Resources int      `json:"resources"`
	Failing   []string `json:"failing,omitempty" doc:"Resources whose last reconcile failed, with the first line of the reason"`
}

// truncate shortens s to at most n bytes, marking the cut.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ProjectState is a project's current resources.
type ProjectState struct {
	Name      string            `json:"name"`
	Version   int64             `json:"version"`
	Resources []change.Resource `json:"resources"`
	Secrets   []string          `json:"secrets,omitempty" doc:"Names of the project's secrets (values are never shown)"`
	// Status is each resource's live state on the machine (pending, ready, failed),
	// and for apps whether production has a release.
	Status map[string]state.ResourceStatus `json:"status,omitempty"`
}

// ProjectManifest is a project's current manifest, rebuilt from its stored
// resources.
type ProjectManifest struct {
	Project  string             `json:"project"`
	Version  int64              `json:"version" doc:"The project version this manifest describes. A plan of this manifest is empty until the project changes."`
	Manifest *manifest.Manifest `json:"manifest" doc:"The canonical, fully defaulted manifest. Edit it (add an app, switch a service on or off, change env) and send it to plan, then apply with the plan's hash."`
	Config   string             `json:"config" doc:"The same manifest as a readable tiffin.config.ts with defaults left out: what tiffin pull writes."`
}

// Health is the unauthenticated liveness report.
type Health struct {
	Status  string `json:"status" example:"ok" enum:"ok,starting"`
	Version string `json:"version"`
	Build   string `json:"build,omitempty" doc:"SHA-256 of the running binary; self-update uses it to know the new build is the one answering"`
}

var (
	buildOnce sync.Once
	buildSum  string
)

// selfBuild hashes the running executable once, on first use.
func selfBuild() string {
	buildOnce.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			return
		}
		f, err := os.Open(exe)
		if err != nil {
			return
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err == nil {
			buildSum = hex.EncodeToString(h.Sum(nil))
		}
	})
	return buildSum
}

// CreatedKey carries the secret exactly once.
type CreatedKey struct {
	Secret string      `json:"secret" doc:"The key's secret (tfn_...). Shown once; store it now."`
	Key    *tokens.Key `json:"key"`
}

type keyCreateBody struct {
	Name          string          `json:"name" minLength:"1" maxLength:"64" doc:"A name you will recognise in History, e.g. claude-code or ci."`
	Projects      tokens.Projects `json:"projects" required:"true"`
	Access        string          `json:"access" enum:"full,read" doc:"full: read, plan and apply any change in those projects, irreversible ones included (deleting data). read: read and plan only."`
	ExpiresInDays *int            `json:"expiresInDays,omitempty" nullable:"true" enum:"0,1,30,90,365" doc:"How long it works: 1, 30, 90 (the default) or 365 days, or 0 for a key that never expires. Created in a dashboard session, anything but a read-only key for 1 day needs a sign-in with a passkey, Google, GitHub or an emailed link in the last 10 minutes."`
}

func (a *API) register() {
	api := a.api

	h := op("health", http.MethodGet, "/v1/health", "health", RiskRead, "Check the box is up",
		"Unauthenticated liveness check. On a box, status is starting until every module has started.", "system")
	h.Security = nil
	huma.Register(api, h, func(ctx context.Context, _ *struct{}) (*struct{ Body Health }, error) {
		status := "ok"
		if a.deps.Platform != nil && !a.deps.Platform.Started() {
			status = "starting"
		}
		return &struct{ Body Health }{Health{Status: status, Version: orDefault(a.deps.Version, "dev"), Build: selfBuild()}}, nil
	})

	s := op("schema-manifest", http.MethodGet, "/v1/schema/manifest", "schema manifest", RiskRead, "Get the manifest JSON Schema",
		"The JSON Schema for tiffin.config.ts, with a description and default for every field.", "system")
	s.Security = nil
	huma.Register(api, s, func(ctx context.Context, _ *struct{}) (*struct{ Body json.RawMessage }, error) {
		return &struct{ Body json.RawMessage }{json.RawMessage(manifest.Schema())}, nil
	})

	huma.Register(api, op("whoami", http.MethodGet, "/v1/whoami", "whoami", RiskRead, "Show the current token",
		"Who you are: key name, kind, access (full or read) and projects.", "system"),
		wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body *tokens.Principal }, error) {
			return &struct{ Body *tokens.Principal }{PrincipalFrom(ctx)}, nil
		}))

	huma.Register(api, op("projects-list", http.MethodGet, "/v1/projects", "projects list", RiskRead, "List projects",
		"Projects on this box that your token can see, with their current version and any resource that failed to converge "+
			"(failing: an app whose new instances did not pass their health check, say; project_get has the details).", "projects"),
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
				ps := ProjectSummary{Name: n, Version: v, Resources: len(res)}
				if st, err := a.statuses(ctx, n); err == nil {
					for _, addr := range slices.Sorted(maps.Keys(st)) {
						if rs := st[addr]; rs.State == "failed" {
							msg, _, _ := strings.Cut(rs.Message, "\n")
							ps.Failing = append(ps.Failing, addr+": "+truncate(msg, 200))
						}
					}
				}
				out = append(out, ps)
			}
			return &struct{ Body []ProjectSummary }{out}, nil
		}))

	type projectPath struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
	}
	huma.Register(api, Untrusted(op("project-get", http.MethodGet, "/v1/projects/{project}", "projects get", RiskRead, "Get a project's state",
		"The project's current resources and version, and each resource's live state (pending, ready or failed, with the reason: "+
			"a failed app's message ends with its last log lines). An app's release says whether production has one: ready means its config "+
			"is applied; an app whose last deploy failed with none live reads failed, naming that deploy.", "projects")),
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
				if change.Kind(r.Address) == change.KindSecret {
					out.Secrets = append(out.Secrets, change.Name(r.Address))
					continue
				}
				out.Resources = append(out.Resources, r)
			}
			slices.Sort(out.Secrets)
			sort.Slice(out.Resources, func(i, j int) bool { return out.Resources[i].Address < out.Resources[j].Address })
			if st, err := a.statuses(ctx, in.Project); err == nil && len(st) > 0 {
				out.Status = st
			}
			return &struct{ Body ProjectState }{out}, nil
		}))

	pm := op("project-manifest", http.MethodGet, "/v1/projects/{project}/manifest", "projects manifest", RiskRead, "Get a project's manifest",
		"The project's current desired state as a manifest, rebuilt from its resources (works for every project, however it was created), "+
			"plus the same thing as a readable tiffin.config.ts. To change the project without a config file: edit `manifest` "+
			"(add an app, add services.postgres, change env...), send it to plan, review the ops and risk, then apply it with the plan's hash. "+
			"The plan is exactly what the same edit to tiffin.config.ts would give.", "projects")
	pm.Errors = append(pm.Errors, 404)
	huma.Register(api, pm, wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
	}) (*struct{ Body ProjectManifest }, error) {
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
		m, err := change.ManifestFromResources(in.Project, res)
		if err != nil {
			return nil, err
		}
		note := fmt.Sprintf("Project %s at version %d, pulled from the box. After an edit:\n"+
			"tiffin plan                    # what would change, and how risky it is\n"+
			"tiffin apply --confirm <hash>", in.Project, v)
		return &struct{ Body ProjectManifest }{ProjectManifest{Project: in.Project, Version: v, Manifest: m,
			Config: string(manifest.RenderConfig(m, note))}}, nil
	}))

	huma.Register(api, op("plan", http.MethodPost, "/v1/plan", "plan", RiskRead, "Plan a manifest",
		"Dry run: what applying this manifest would change, the risk of each step and the plan hash. Never writes. "+
			"`warnings` lists things the manifest probably did not mean (auth without email, env that replaces what the box sets): fix them before applying. "+
			"A project `resources` budget that cannot fit this box (more CPUs than it has, memoryMB budgets adding up to more than it keeps for apps) is refused with 422 and a hint.", "changes"),
		wrap(func(ctx context.Context, in *struct{ Body planBody }) (*struct{ Body *change.Plan }, error) {
			m, desired, older, err := a.parseManifest(ctx, in.Body.Manifest)
			if err != nil {
				return nil, err
			}
			if err := PrincipalFrom(ctx).Require(tokens.ScopePlan, m.Project); err != nil {
				return nil, err
			}
			if err := a.checkPlan(ctx, m.Project, desired); err != nil {
				return nil, err
			}
			p, err := a.deps.Engine.Plan(ctx, m.Project, desired)
			if err != nil {
				return nil, err
			}
			p.Warnings = append(manifest.Warnings(m), older...)
			a.boxWarnings(ctx, p, desired)
			a.failedWarning(ctx, p)
			return &struct{ Body *change.Plan }{p}, nil
		}))

	ap := op("apply", http.MethodPost, "/v1/apply", "apply", RiskDestructive, "Apply a manifest",
		"Plans the manifest and applies it if `confirm` matches the plan hash. "+
			"Without `confirm` (or with a stale one) nothing changes: you get status 428 with the plan to review. "+
			"Applying needs a key with full access to the project; a read key can only plan.", "changes")
	ap.Errors = append(ap.Errors, 409, 428)
	ap.Extensions[ExtConfirm] = true
	huma.Register(api, ap,
		wrap(func(ctx context.Context, in *struct{ Body applyBody }) (*struct{ Body ApplyResult }, error) {
			m, desired, older, err := a.parseManifest(ctx, in.Body.Manifest)
			if err != nil {
				return nil, err
			}
			p := PrincipalFrom(ctx)
			if err := p.Require(tokens.ScopePlan, m.Project); err != nil {
				return nil, err
			}
			if err := a.checkPlan(ctx, m.Project, desired); err != nil {
				return nil, err
			}
			plan, err := a.deps.Engine.Plan(ctx, m.Project, desired)
			if err != nil {
				return nil, err
			}
			plan.Warnings = append(manifest.Warnings(m), older...)
			a.boxWarnings(ctx, plan, desired)
			return a.apply(ctx, p, plan, in.Body.Confirm, in.Body.Intent)
		}))

	de := op("project-destroy", http.MethodPost, "/v1/projects/{project}/destroy", "projects destroy", RiskDestructive, "Destroy a project",
		"Plans deleting every resource of a project (its apps, databases, buckets, auth users, secrets, everything) and applies it with confirm. "+
			"The plan lists each secret it deletes (secret/NAME), so nothing is left behind for a new project of the same name. "+
			"Irreversible: databases keep a 7-day snapshot and buckets a 7-day trash, then they are gone. Without confirm you get the plan with status 428.", "projects")
	de.Errors = append(de.Errors, 404, 409, 428)
	de.Extensions[ExtConfirm] = true
	huma.Register(api, de, wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Body    destroyBody
	}) (*struct{ Body ApplyResult }, error) {
		p := PrincipalFrom(ctx)
		if err := p.Require(tokens.ScopePlan, in.Project); err != nil {
			return nil, err
		}
		v, _, err := a.deps.DB.Load(ctx, in.Project)
		if err != nil {
			return nil, err
		}
		if v == 0 {
			return nil, problem(404, "not_found", "project "+in.Project+" does not exist")
		}
		plan, err := a.deps.Engine.PlanDelete(ctx, in.Project)
		if err != nil {
			return nil, err
		}
		intent := in.Body.Intent
		if intent == "" {
			intent = "destroy project " + in.Project
		}
		return a.apply(ctx, p, plan, in.Body.Confirm, intent)
	}))

	type changesQuery struct {
		Project string `query:"project" doc:"Only this project"`
		Limit   int    `query:"limit" minimum:"1" maximum:"200" default:"50" doc:"Maximum changes to return"`
		Before  string `query:"before" pattern:"^(chg_[0-9A-Z]{26})?$" doc:"Only changes older than this change ID (for paging: pass the last ID you got)"`
	}
	huma.Register(api, Untrusted(op("changes-list", http.MethodGet, "/v1/changes", "changes list", RiskRead, "List changes",
		"The change log, newest first: who changed what, why, the risk and whether it was undone.", "changes")),
		wrap(func(ctx context.Context, in *changesQuery) (*struct{ Body []*change.Change }, error) {
			p := PrincipalFrom(ctx)
			if err := p.Require(tokens.ScopeRead, in.Project); err != nil {
				return nil, err
			}
			// A token scoped to some projects lists each of them, so busy
			// projects it cannot see don't use up the limit.
			projects := []string{in.Project}
			if in.Project == "" && !p.CanProject("*") {
				projects = p.Projects
			}
			var before int64
			if in.Before != "" {
				seq, err := a.deps.DB.ChangeSeq(ctx, in.Before)
				if err != nil {
					return nil, err
				}
				before = seq
			}
			out := []*change.Change{}
			for _, proj := range projects {
				cs, err := a.deps.DB.ListChanges(ctx, change.ListFilter{Project: proj, Limit: in.Limit, Before: before})
				if err != nil {
					return nil, err
				}
				for _, c := range cs {
					if p.CanProject(c.Project) {
						out = append(out, c)
					}
				}
			}
			if len(projects) > 1 {
				slices.SortStableFunc(out, func(x, y *change.Change) int {
					if c := y.At.Compare(x.At); c != 0 {
						return c
					}
					return strings.Compare(y.ID, x.ID)
				})
				out = out[:min(len(out), max(in.Limit, 1))]
			}
			return &struct{ Body []*change.Change }{out}, nil
		}))

	type changePath struct {
		ID string `path:"id" pattern:"^chg_[0-9A-Z]{26}$" doc:"Change ID"`
	}
	huma.Register(api, Untrusted(op("change-get", http.MethodGet, "/v1/changes/{id}", "changes get", RiskRead, "Get a change",
		"One change with its full plan and inverse.", "changes")),
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
	un.Extensions[ExtConfirm] = true
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
			if !p.CanProject(c.Project) {
				return nil, change.ErrNotFound // as change-get: don't reveal other projects' changes
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

	huma.Register(api, op("tokens-list", http.MethodGet, "/v1/tokens", "tokens list", RiskRead, "List API keys",
		"API keys on this box (secrets are never shown): what projects each reaches, full or read access, and when it expires. "+
			"Dashboard sign-in sessions are not listed. Only a key with full access to all projects (or an owner or admin person) can list keys.", "tokens"),
		wrap(func(ctx context.Context, in *struct {
			Revoked bool `query:"revoked" doc:"Include revoked keys"`
		}) (*struct{ Body []*tokens.Key }, error) {
			if !PrincipalFrom(ctx).BoxAdmin() {
				return nil, tokens.ErrNotAdmin
			}
			all, err := a.deps.Tokens.List(ctx, in.Revoked)
			if err != nil {
				return nil, err
			}
			out := []*tokens.Key{}
			for _, t := range all {
				if t.Kind == tokens.KindHuman && t.Person != "" {
					continue // a dashboard session, not a key
				}
				out = append(out, t.AsKey())
			}
			return &struct{ Body []*tokens.Key }{out}, nil
		}))

	tc := op("token-create", http.MethodPost, "/v1/tokens", "tokens create", RiskWrite, "Create an API key",
		"Creates an API key for an agent, a script or CI. `projects` is \"all\" (every project, including ones created later) or a list; "+
			"`access` is full (read, plan and apply any change there, deleting data included) or read (read and plan only). "+
			"A key with full access to all projects is the box admin: it also manages keys, people, box settings and exports/imports. "+
			"It works for 90 days unless `expiresInDays` says otherwise (1, 30, 365, or 0 for never). "+
			"A key made in a dashboard session is its own credential: signing out or the session expiring does not touch it. "+
			"Made there, any key but a read-only one for a day needs a recent strong sign-in (reauth_required otherwise), "+
			"and the person is emailed about it. A key made by another key never outlives that key. "+
			"Changes made with a key are recorded in History under its name and can be undone. The secret is returned once. "+
			"Only a key with full access to all projects (or an owner or admin person) can create keys.", "tokens")
	tc.Middlewares = huma.Middlewares{withClientIP}
	huma.Register(api, tc, wrap(func(ctx context.Context, in *struct{ Body keyCreateBody }) (*struct{ Body CreatedKey }, error) {
		req := tokens.KeyRequest{Name: in.Body.Name, Projects: in.Body.Projects, Access: in.Body.Access, TTL: tokens.DefaultKeyTTL}
		if d := in.Body.ExpiresInDays; d != nil {
			if !slices.Contains([]int{0, 1, 30, 90, 365}, *d) {
				return nil, problem(422, "validation", "expiresInDays must be 1, 30, 90, 365 or 0 (never)")
			}
			req.TTL = time.Duration(*d) * 24 * time.Hour
		}
		p := PrincipalFrom(ctx)
		secret, k, err := a.deps.Tokens.CreateKey(ctx, p, req)
		if err != nil {
			return nil, err
		}
		if p.IsSession() {
			a.keyCreated(ctx, p, k)
		}
		return &struct{ Body CreatedKey }{CreatedKey{Secret: secret, Key: k}}, nil
	}))

	rv := op("token-revoke", http.MethodDelete, "/v1/tokens/{id}", "tokens revoke", RiskDestructive, "Revoke an API key",
		"Revokes an API key at once (and any key it created). Only a key with full access to all projects (or an owner or admin person) can revoke keys.", "tokens")
	rv.Errors = append(rv.Errors, 404)
	huma.Register(api, rv,
		wrap(func(ctx context.Context, in *struct {
			ID string `path:"id" pattern:"^tok_[0-9A-Z]{26}$" doc:"Token ID"`
		}) (*struct{}, error) {
			p := PrincipalFrom(ctx)
			if !p.BoxAdmin() {
				return nil, tokens.ErrNotAdmin
			}
			return &struct{}{}, a.deps.Tokens.Revoke(ctx, p, in.ID)
		}))

	huma.Register(api, Untrusted(op("audit-list", http.MethodGet, "/v1/audit", "audit list", RiskRead, "List audit events",
		"Security events that are not changes: keys created and revoked, sign-ins, people invited. Box admins only (a key with full access to all projects, or an owner or admin person).", "system")),
		wrap(func(ctx context.Context, in *struct {
			Limit int `query:"limit" minimum:"1" maximum:"500" default:"50"`
		}) (*struct{ Body []state.AuditEvent }, error) {
			if p := PrincipalFrom(ctx); !p.BoxAdmin() {
				return nil, fmt.Errorf("%w: the audit log needs a key with full access to all projects", tokens.ErrForbidden)
			}
			ev, err := a.deps.DB.AuditLog(ctx, in.Limit)
			if ev == nil {
				ev = []state.AuditEvent{}
			}
			return &struct{ Body []state.AuditEvent }{ev}, err
		}))
}

func (a *API) registerBox() {
	api := a.api
	started := time.Now()

	huma.Register(api, op("status", http.MethodGet, "/v1/status", "status", RiskRead, "Show box status",
		"Box health: version, uptime, host and every health check (state, disk, edge, services). Works even when app services are down.", "system"),
		wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body StatusReport }, error) {
			if err := PrincipalFrom(ctx).Require(tokens.ScopeRead, ""); err != nil {
				return nil, err
			}
			return &struct{ Body StatusReport }{a.Status(ctx, started)}, nil
		}))

	huma.Register(api, op("login-link-create", http.MethodPost, "/v1/login-links", "login", RiskWrite, "Create a dashboard login link",
		"A one-time link (valid 10 minutes) that signs a browser into the dashboard with your power. Box admins only.", "system"),
		wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body LoginLink }, error) {
			code, exp, err := a.deps.Tokens.CreateLoginLink(ctx, PrincipalFrom(ctx))
			if err != nil {
				return nil, err
			}
			base := strings.TrimRight(orDefault(a.deps.PublicURL, ""), "/")
			return &struct{ Body LoginLink }{LoginLink{URL: base + "/login#" + code, Code: code, ExpiresAt: exp}}, nil
		}))

	sc := op("session-create", http.MethodPost, "/v1/session", "-", RiskWrite, "Start a dashboard session",
		"Exchanges a one-time login code for a session cookie. Used by the dashboard's login page.", "system")
	sc.Security = nil
	sc.Middlewares = huma.Middlewares{withClientIP}
	huma.Register(api, sc, wrap(func(ctx context.Context, in *struct {
		Device string `cookie:"tiffin_device"`
		UA     string `header:"User-Agent"`
		Body   struct {
			Code string `json:"code" minLength:"8" maxLength:"128"`
		}
	}) (*struct {
		SetCookie []http.Cookie `header:"Set-Cookie"`
		Body      *tokens.Principal
	}, error) {
		secret, t, err := a.deps.Tokens.RedeemLoginLink(ctx, in.Body.Code)
		if err != nil {
			return nil, problem(401, "unauthenticated", "this login link is invalid, used or expired; run `tiffin login` for a new one")
		}
		p, err := a.deps.Tokens.Authenticate(ctx, secret)
		if err != nil {
			return nil, err
		}
		out := &struct {
			SetCookie []http.Cookie `header:"Set-Cookie"`
			Body      *tokens.Principal
		}{Body: p}
		out.SetCookie = []http.Cookie{{Name: SessionCookie, Value: secret, Path: "/", HttpOnly: true, Secure: true,
			SameSite: http.SameSiteStrictMode, Expires: *t.ExpiresAt}}
		if p.Person != "" {
			// A link nobody sent (no sponsor) is one the person asked for by email.
			method, how := tokens.MethodLink, "a sign-in link"
			if t.Sponsor == "" {
				method, how = tokens.MethodEmail, "a link from their email"
			}
			ip := clientIPFrom(ctx)
			if a.deps.DB != nil {
				_ = a.deps.DB.Audit(ctx, t.ID, "session.link", p.Person,
					map[string]any{"summary": orDefault(p.PersonName, p.Name) + " signed in with " + how, "method": method, "ip": ip})
			}
			if dc := a.signedIn(ctx, t.ID, p.Person, in.Device, in.UA, ip, method, "Sign-in link"); dc != nil {
				out.SetCookie = append(out.SetCookie, *dc)
			}
		}
		return out, nil
	}))

	huma.Register(api, op("session-delete", http.MethodDelete, "/v1/session", "-", RiskWrite, "End the dashboard session",
		"Revokes the current session token and clears the cookie.", "system"),
		wrap(func(ctx context.Context, _ *struct{}) (*struct {
			SetCookie http.Cookie `header:"Set-Cookie"`
		}, error) {
			p := PrincipalFrom(ctx)
			if p.Kind == tokens.KindHuman && p.Person != "" {
				owner := &tokens.Principal{TokenID: p.TokenID, Name: p.Name, Scopes: []tokens.Scope{tokens.ScopeAll}, Projects: []string{"*"}}
				_ = a.deps.Tokens.Revoke(ctx, owner, p.TokenID)
			}
			return &struct {
				SetCookie http.Cookie `header:"Set-Cookie"`
			}{http.Cookie{Name: SessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: -1}}, nil
		}))
}

// LoginLink is a one-time dashboard login.
type LoginLink struct {
	URL       string    `json:"url" doc:"Open this in a browser"`
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Check is one health check.
type Check = platform.Check

// StatusReport is the box's health.
type StatusReport struct {
	OK      bool      `json:"ok"`
	Version string    `json:"version"`
	Started time.Time `json:"started"`
	Uptime  string    `json:"uptime"`
	Host    struct {
		Hostname string `json:"hostname"`
		OS       string `json:"os"`
		Arch     string `json:"arch"`
	} `json:"host"`
	Checks []Check `json:"checks"`
}

// Status computes the status report. The public /status page uses it too.
func (a *API) Status(ctx context.Context, started time.Time) StatusReport {
	r := StatusReport{OK: true, Version: orDefault(a.deps.Version, "dev"), Started: started.UTC(), Uptime: time.Since(started).Round(time.Second).String()}
	r.Host.Hostname, _ = os.Hostname()
	r.Host.OS, r.Host.Arch = runtime.GOOS, runtime.GOARCH
	if _, err := a.deps.DB.ListProjects(ctx); err != nil {
		r.Checks = append(r.Checks, Check{Name: "state", OK: false, Detail: err.Error()})
	} else {
		r.Checks = append(r.Checks, Check{Name: "state", OK: true, Detail: "platform state readable"})
	}
	if a.deps.Platform != nil {
		r.Checks = append(r.Checks, a.deps.Platform.Checks(ctx)...)
	}
	if a.deps.Checks != nil {
		r.Checks = append(r.Checks, a.deps.Checks(ctx)...)
	}
	for _, c := range r.Checks {
		r.OK = r.OK && c.OK
	}
	return r
}

func (a *API) apply(ctx context.Context, p *tokens.Principal, plan *change.Plan, confirm, intent string) (*struct{ Body ApplyResult }, error) {
	c, err := a.deps.Engine.Apply(ctx, change.ApplyRequest{
		Plan: plan, Confirm: confirm, Actor: p.Actor(), Intent: intent, Authorize: p.Authorizer(),
	})
	if err != nil {
		return nil, err
	}
	if c != nil && a.deps.Platform != nil {
		a.deps.Platform.AfterApply(c)
	}
	r := ApplyResult{Applied: c != nil, Change: c}
	if c == nil {
		r.Plan = plan // with a change, the plan is change.plan: don't send it twice
		// Nothing to change, but what failed to converge is tried again: an
		// apply is how people ask the box to match its state.
		if pl := a.deps.Platform; pl != nil && p.Require(tokens.ScopeApplyReversible, plan.Project) == nil {
			if failed := pl.Failed(ctx, plan.Project); len(failed) > 0 {
				pl.ReconcileProject(plan.Project)
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("Nothing to change, but %d failed to converge; retrying now (see tiffin projects get %s): %s",
					len(failed), plan.Project, strings.Join(failed, "; ")))
			}
		}
	}
	return &struct{ Body ApplyResult }{r}, nil
}

// failedWarning tells a plan with nothing to change about resources that
// failed to converge, which an apply retries.
// boxWarnings adds what the box's modules see in a plan (see
// platform.PlanWarner).
func (a *API) boxWarnings(ctx context.Context, plan *change.Plan, desired map[string]change.Resource) {
	if a.deps.Platform != nil {
		plan.Warnings = append(plan.Warnings, a.deps.Platform.PlanWarnings(ctx, plan, desired)...)
	}
}

func (a *API) failedWarning(ctx context.Context, plan *change.Plan) {
	if a.deps.Platform == nil || !plan.Empty() {
		return
	}
	if failed := a.deps.Platform.Failed(ctx, plan.Project); len(failed) > 0 {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("Nothing to change, but %d failed to converge; tiffin apply retries them: %s",
			len(failed), strings.Join(failed, "; ")))
	}
}

// parseManifest parses a manifest for planning on this box: an app that sets
// no routes keeps the address a default already gave it here (see
// manifest.ParseOnBox). The returned warnings say which apps keep an app-name
// address from before addresses were named after the project.
func (a *API) parseManifest(ctx context.Context, raw ManifestJSON) (*manifest.Manifest, map[string]change.Resource, []string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil, nil, problem(422, "validation", "manifest is required")
	}
	var onBox manifest.OnBox
	if a.deps.DB != nil {
		onBox = func(project string) (map[string][]string, error) {
			_, res, err := a.deps.DB.Load(ctx, project)
			if err != nil {
				return nil, err
			}
			return change.AppRoutes(res), nil
		}
	}
	m, older, err := manifest.ParseOnBox(raw, onBox)
	if err != nil {
		return nil, nil, nil, err
	}
	var warn []string
	for _, app := range older {
		def := manifest.DefaultRoute(m, app)
		warn = append(warn, fmt.Sprintf("app %s keeps its old address %q, from before addresses were named after the project (a new app like it gets %q); "+
			"set routes: [%q] on it to keep that for good, or routes: [%q] to move it", app, app, def, app, def))
	}
	desired, err := change.Resources(m)
	return m, desired, warn, err
}

// statuses is a project's resource statuses as its status API reports them:
// on a box, with what modules add (an app's release; see
// platform.StatusReporter). Apply's own convergence check reads the stored ones.
func (a *API) statuses(ctx context.Context, project string) (map[string]state.ResourceStatus, error) {
	if a.deps.Platform != nil {
		return a.deps.Platform.ResourceStatuses(ctx, project)
	}
	return a.deps.DB.ResourceStatuses(ctx, project)
}

// checkPlan lets box modules refuse a desired state the machine cannot run.
func (a *API) checkPlan(ctx context.Context, project string, desired map[string]change.Resource) error {
	if a.deps.Platform == nil {
		return nil
	}
	return a.deps.Platform.CheckPlan(ctx, project, desired)
}

// RiskOf returns an operation's risk class.
func RiskOf(o *huma.Operation) string {
	if r, ok := o.Extensions[ExtRisk].(string); ok {
		return r
	}
	return RiskDestructive
}

// Confirmable reports whether an operation is plan-driven: called without a
// confirm hash it changes nothing and returns the plan.
func Confirmable(o *huma.Operation) bool {
	v, _ := o.Extensions[ExtConfirm].(bool)
	return v
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

// sameOrigin reports whether a cookie-authenticated request may act: reads
// always, changes only from the dashboard's own origin. Browsers say where a
// request came from (Sec-Fetch-Site, or else Origin); clients that say
// neither are not browsers, which carry no cookie of their own accord.
func sameOrigin(ctx huma.Context) bool {
	switch ctx.Method() {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if s := ctx.Header("Sec-Fetch-Site"); s != "" {
		return s == "same-origin" || s == "none"
	}
	o := ctx.Header("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && strings.EqualFold(u.Host, ctx.Host())
}
