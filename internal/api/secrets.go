package api

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
)

type secretPath struct {
	Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
	Name    string `path:"name" pattern:"^[A-Z_][A-Z0-9_]{0,127}$" doc:"Secret name (an env var name)"`
}

// SecretsCopied says which secrets a copy wrote and which it left alone.
type SecretsCopied struct {
	Copied  []string `json:"copied"`
	Skipped []string `json:"skipped" doc:"Already set in this project (pass overwrite to replace them)"`
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

	cp := op("secrets-copy", http.MethodPost, "/v1/projects/{project}/secrets/copy", "secrets copy", RiskWrite, "Copy secrets from another project",
		"Copies secrets (e.g. OPENAI_API_KEY) from another project on this box into this one, inside the box: the values never "+
			"leave it, so you can reuse a key without asking the person to paste it again. Copies the named secrets, or all of them "+
			"when names is empty. Existing secrets are kept unless overwrite is true. Needs full access to both projects, because "+
			"this project's apps can read what it receives. Restarts this project's apps with them.", "secrets")
	cp.Errors = append(cp.Errors, 404)
	huma.Register(api, cp, wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project to copy into"`
		Body    struct {
			From      string   `json:"from" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project to copy from"`
			Names     []string `json:"names,omitempty" maxItems:"200" doc:"Secret names to copy; empty copies all"`
			Overwrite bool     `json:"overwrite,omitempty" doc:"Replace secrets this project already has with the same name"`
		}
	}) (*struct{ Body SecretsCopied }, error) {
		p := PrincipalFrom(ctx)
		for _, pr := range []string{in.Body.From, in.Project} {
			if err := p.Require(tokens.ScopeApplyReversible, pr); err != nil {
				return nil, err
			}
		}
		if in.Body.From == in.Project {
			return nil, NewProblem(422, "validation", "from and project are the same")
		}
		s, err := a.secrets()
		if err != nil {
			return nil, err
		}
		src, err := s.All(ctx, in.Body.From)
		if err != nil {
			return nil, err
		}
		have, err := s.All(ctx, in.Project)
		if err != nil {
			return nil, err
		}
		names := in.Body.Names
		if len(names) == 0 {
			for n := range src {
				names = append(names, n)
			}
			slices.Sort(names)
		}
		out := SecretsCopied{Copied: []string{}, Skipped: []string{}}
		for _, n := range names {
			v, ok := src[n]
			if !ok {
				return nil, NewProblem(404, "not_found", "no secret "+n+" in "+in.Body.From)
			}
			if _, exists := have[n]; exists && !in.Body.Overwrite {
				out.Skipped = append(out.Skipped, n)
				continue
			}
			if err := s.Set(ctx, in.Project, n, v, p.TokenID); err != nil {
				return nil, err
			}
			out.Copied = append(out.Copied, n)
		}
		if len(out.Copied) > 0 {
			_ = a.deps.DB.Audit(ctx, p.TokenID, "secret.copy", in.Body.From+" -> "+in.Project, map[string]any{"names": out.Copied, "session": p.Session})
			a.deps.Platform.ReconcileProject(in.Project)
		}
		return &struct{ Body SecretsCopied }{out}, nil
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
