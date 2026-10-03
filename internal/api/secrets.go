package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
)

type secretPath struct {
	Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
	Name    string `path:"name" pattern:"^[A-Z_][A-Z0-9_]{0,127}$" doc:"Secret name (an env var name)"`
}

func (a *API) secrets() (*platform.Secrets, error) {
	if a.deps.Platform == nil || a.deps.Platform.Secrets == nil {
		return nil, NewProblem(501, "internal", "secrets are only available on a box")
	}
	return a.deps.Platform.Secrets, nil
}

func (a *API) registerSecrets() {
	api := a.api
	huma.Register(api, op("secrets-list", http.MethodGet, "/v1/projects/{project}/secrets", "secrets list", RiskRead, "List secrets",
		"Secret names for a project. Values are never shown; apps get them as environment variables.", "secrets"),
		wrap(func(ctx context.Context, in *struct {
			Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$"`
		}) (*struct{ Body []platform.SecretInfo }, error) {
			if err := PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
				return nil, err
			}
			s, err := a.secrets()
			if err != nil {
				return nil, err
			}
			list, err := s.List(ctx, in.Project)
			return &struct{ Body []platform.SecretInfo }{list}, err
		}))

	set := op("secret-set", http.MethodPut, "/v1/projects/{project}/secrets/{name}", "secrets set", RiskWrite, "Set a secret",
		"Encrypts and stores a secret env var (age, with the box's own key) and restarts the project's apps with it. "+
			"The value is never returned or logged.", "secrets")
	huma.Register(api, set, wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Name    string `path:"name" pattern:"^[A-Z_][A-Z0-9_]{0,127}$" doc:"Secret name (an env var name)"`
		Body    struct {
			Value string `json:"value" maxLength:"65536" doc:"The secret value"`
		}
	}) (*struct{ Body platform.SecretInfo }, error) {
		p := PrincipalFrom(ctx)
		if err := p.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		s, err := a.secrets()
		if err != nil {
			return nil, err
		}
		if err := s.Set(ctx, in.Project, in.Name, in.Body.Value, p.TokenID); err != nil {
			if errors.Is(err, platform.ErrSecretName) {
				return nil, NewProblem(422, "validation", err.Error())
			}
			return nil, err
		}
		_ = a.deps.DB.Audit(ctx, p.TokenID, "secret.set", in.Project+"/"+in.Name, map[string]any{"session": p.Session})
		a.deps.Platform.ReconcileProject(in.Project)
		return &struct{ Body platform.SecretInfo }{platform.SecretInfo{Name: in.Name, UpdatedBy: p.TokenID}}, nil
	}))

	del := op("secret-delete", http.MethodDelete, "/v1/projects/{project}/secrets/{name}", "secrets delete", RiskDestructive, "Delete a secret",
		"Deletes a secret immediately and restarts the project's apps without it. The value cannot be recovered.", "secrets")
	del.Errors = append(del.Errors, 404)
	huma.Register(api, del, wrap(func(ctx context.Context, in *secretPath) (*struct{}, error) {
		p := PrincipalFrom(ctx)
		if err := p.Require(tokens.ScopeApplyIrreversible, in.Project); err != nil {
			return nil, err
		}
		s, err := a.secrets()
		if err != nil {
			return nil, err
		}
		ok, err := s.Delete(ctx, in.Project, in.Name)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, NewProblem(404, "not_found", "no secret "+in.Name+" in "+in.Project)
		}
		_ = a.deps.DB.Audit(ctx, p.TokenID, "secret.delete", in.Project+"/"+in.Name, map[string]any{"session": p.Session})
		a.deps.Platform.ReconcileProject(in.Project)
		return &struct{}{}, nil
	}))
}
