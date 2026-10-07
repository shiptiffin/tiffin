package api

import (
	"context"
	"hash/fnv"
	"net/http"
	"slices"

	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/state"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
)

// Enamels are the six project colours the dashboard paints a project's tier
// rim, sidebar swatch and share of the memory bar with. They are names, not
// values: the dashboard owns the actual colours (light and dark variants).
var Enamels = []string{"leaf", "teal", "indigo", "plum", "chilli", "turmeric"}

// DefaultEnamel is the colour a project has until someone picks one:
// stable for a name, so a project looks the same everywhere from the start.
func DefaultEnamel(project string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(project))
	return Enamels[h.Sum32()%uint32(len(Enamels))]
}

// Appearance is how the dashboard draws a project.
type Appearance struct {
	Project string `json:"project"`
	Enamel  string `json:"enamel" enum:"leaf,teal,indigo,plum,chilli,turmeric" doc:"The project's colour: its tier rim on the Box page, its swatch in the sidebar and its share of the memory bar"`
	Chosen  bool   `json:"chosen" doc:"False while the project still has its default colour (picked from its name)"`
}

// RenderedConfig is a manifest written out as tiffin.config.ts.
type RenderedConfig struct {
	Config string `json:"config" doc:"The manifest as a readable tiffin.config.ts, defaults left out (what tiffin pull writes)"`
}

const appearanceNS = "appearance"

func (a *API) appearance(ctx context.Context, project string) (Appearance, error) {
	return appearanceOf(ctx, a.deps.DB, project)
}

// EnamelOf is a project's colour: the one picked for it, else its default.
func EnamelOf(ctx context.Context, db *state.DB, project string) string {
	out, _ := appearanceOf(ctx, db, project)
	return out.Enamel
}

func appearanceOf(ctx context.Context, db *state.DB, project string) (Appearance, error) {
	out := Appearance{Project: project, Enamel: DefaultEnamel(project)}
	v, ok, err := db.KVGet(ctx, appearanceNS, project)
	if err != nil {
		return out, err
	}
	if ok && slices.Contains(Enamels, string(v)) {
		out.Enamel, out.Chosen = string(v), true
	}
	return out, nil
}

func (a *API) projectExists(ctx context.Context, project string) error {
	v, _, err := a.deps.DB.Load(ctx, project)
	if err != nil {
		return err
	}
	if v == 0 {
		return problem(404, "not_found", "project "+project+" does not exist")
	}
	return nil
}

func (a *API) registerAppearance() {
	api := a.api

	get := op("appearance-get", http.MethodGet, "/v1/projects/{project}/appearance", "projects appearance get", RiskRead, "Get a project's colour",
		"The enamel colour the dashboard draws the project with (leaf, teal, indigo, plum, chilli or turmeric). "+
			"Until someone picks one, it is chosen from the project's name.", "projects")
	get.Errors = append(get.Errors, 404)
	huma.Register(api, get, wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
	}) (*struct{ Body Appearance }, error) {
		if err := PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
			return nil, err
		}
		if err := a.projectExists(ctx, in.Project); err != nil {
			return nil, err
		}
		out, err := a.appearance(ctx, in.Project)
		return &struct{ Body Appearance }{out}, err
	}))

	set := op("appearance-set", http.MethodPut, "/v1/projects/{project}/appearance", "projects appearance set", RiskWrite, "Set a project's colour",
		"Picks the enamel colour the dashboard draws the project with. Cosmetic: it changes no resource and is not a Change.", "projects")
	set.Errors = append(set.Errors, 404)
	huma.Register(api, set, wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Body    struct {
			Enamel string `json:"enamel" enum:"leaf,teal,indigo,plum,chilli,turmeric" doc:"One of leaf, teal, indigo, plum, chilli, turmeric"`
		}
	}) (*struct{ Body Appearance }, error) {
		if err := PrincipalFrom(ctx).Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		if err := a.projectExists(ctx, in.Project); err != nil {
			return nil, err
		}
		if err := a.deps.DB.KVPut(ctx, appearanceNS, in.Project, []byte(in.Body.Enamel)); err != nil {
			return nil, err
		}
		out, err := a.appearance(ctx, in.Project)
		return &struct{ Body Appearance }{out}, err
	}))

	render := op("manifest-render", http.MethodPost, "/v1/manifest/render", "manifest render", RiskRead, "Write a manifest as tiffin.config.ts",
		"Validates a manifest and writes it out as a readable tiffin.config.ts, the same way GET /v1/projects/{project}/manifest does. "+
			"Use it to show the config file an edit would produce before you plan and apply it. Never writes anything.", "projects")
	huma.Register(api, render, wrap(func(ctx context.Context, in *struct{ Body planBody }) (*struct{ Body RenderedConfig }, error) {
		m, _, _, err := a.parseManifest(ctx, in.Body.Manifest)
		if err != nil {
			return nil, err
		}
		if err := PrincipalFrom(ctx).Require(tokens.ScopeRead, m.Project); err != nil {
			return nil, err
		}
		return &struct{ Body RenderedConfig }{RenderedConfig{Config: string(manifest.RenderConfig(m, ""))}}, nil
	}))
}
