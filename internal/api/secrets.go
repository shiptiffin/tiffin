package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/manifest"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// SecretSet is a stored secret and what happens next.
type SecretSet struct {
	platform.SecretInfo
	Change string `json:"change,omitempty" doc:"The change that set it (undo it with change_undo to put back the previous value); empty when the value was already this"`
	Note   string `json:"note,omitempty" doc:"What happens next, and whether the secret replaces a value the box sets"`
}

// SecretsCopied says which secrets a copy wrote and which it left alone.
type SecretsCopied struct {
	Copied  []string `json:"copied"`
	Skipped []string `json:"skipped" doc:"Already set in this project (pass overwrite to replace them)"`
	Change  string   `json:"change,omitempty" doc:"The change that copied them (undo it with change_undo)"`
}

// SecretDeleted is the change that deleted a secret.
type SecretDeleted struct {
	Change string `json:"change" doc:"The change that deleted it; change_undo puts the secret back"`
}

func (a *API) secrets() (*platform.Platform, error) {
	if a.deps.Platform == nil || a.deps.Platform.Secrets == nil {
		return nil, NewProblem(501, "internal", "secrets are only available on a box")
	}
	return a.deps.Platform, nil
}

// secretChange plans a change to a project's secrets and applies it at once
// (the caller's own request is the review), recorded in History with intent.
func (a *API) secretChange(ctx context.Context, project string, set map[string]string, del []string, intent string) (*change.Change, error) {
	pr := PrincipalFrom(ctx)
	plat, err := a.secrets()
	if err != nil {
		return nil, err
	}
	plan, err := plat.PlanSecrets(ctx, project, set, del, pr.TokenID)
	switch {
	case errors.Is(err, platform.ErrSecretName):
		return nil, NewProblem(422, "validation", err.Error())
	case errors.Is(err, platform.ErrNoSecret):
		return nil, NewProblem(404, "not_found", err.Error())
	case err != nil:
		return nil, err
	}
	res, err := a.apply(ctx, pr, plan, plan.Hash, intent)
	if err != nil {
		return nil, err
	}
	return res.Body.Change, nil
}

func changeID(c *change.Change) string {
	if c == nil {
		return ""
	}
	return c.ID
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
			plat, err := a.secrets()
			if err != nil {
				return nil, err
			}
			list, err := plat.Secrets.List(ctx, in.Project)
			return &struct{ Body []platform.SecretInfo }{list}, err
		}))

	set := op("secret-set", http.MethodPut, "/v1/projects/{project}/secrets/{name}", "secrets set", RiskWrite, "Set a secret",
		"Encrypts and stores a secret env var (age, with the box's own key) and restarts the project's apps with it. "+
			"The value is never returned or logged. Setting it is a change in History (with your intent) that change_undo reverts, "+
			"putting back the previous value; the change log keeps values only encrypted. "+
			"The restart runs in the background: if the new instances fail their health check "+
			"the old ones keep serving and project_get shows the app failed, with the reason. "+
			"A secret named like a variable the box sets (DATABASE_URL, REDIS_URL, S3_*, SMTP_URL, TIFFIN_*...) replaces the box's value; the result's note says so.", "secrets")
	huma.Register(api, set, wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Name    string `path:"name" pattern:"^[A-Z_][A-Z0-9_]{0,127}$" doc:"Secret name (an env var name)"`
		Body    struct {
			Value  string `json:"value" maxLength:"65536" doc:"The secret value"`
			Intent string `json:"intent,omitempty" maxLength:"500" doc:"Why you are setting it, in one sentence. Shown in History."`
		}
	}) (*struct{ Body SecretSet }, error) {
		p := PrincipalFrom(ctx)
		if err := p.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		intent := orDefault(in.Body.Intent, "set secret "+in.Name)
		c, err := a.secretChange(ctx, in.Project, map[string]string{in.Name: in.Body.Value}, nil, intent)
		if err != nil {
			return nil, err
		}
		out := SecretSet{SecretInfo: platform.SecretInfo{Name: in.Name, UpdatedAt: time.Now().UTC(), UpdatedBy: p.TokenID}, Change: changeID(c),
			Note: "The project's apps restart with it in the background; project_get shows each app's state."}
		if c == nil {
			out.Note = "Unchanged: " + in.Name + " already has this value."
		}
		if manifest.SetByBox(in.Name) {
			out.Note = "The box already gives apps " + in.Name + "; this secret replaces that value. Delete the secret to go back to the box's. " + out.Note
		}
		if c != nil && manifest.BuildInlined(in.Name) {
			out.Note += " " + in.Name + " is built into browser code, so it is public, and web apps rebuild from their live source to pick it up."
		}
		return &struct{ Body SecretSet }{out}, nil
	}))

	cp := op("secrets-copy", http.MethodPost, "/v1/projects/{project}/secrets/copy", "secrets copy", RiskWrite, "Copy secrets from another project",
		"Copies secrets (e.g. OPENAI_API_KEY) from another project on this box into this one, inside the box: the values never "+
			"leave it, so you can reuse a key without asking the person to paste it again. Copies the named secrets, or all of them "+
			"when names is empty. Existing secrets are kept unless overwrite is true. Needs full access to both projects, because "+
			"this project's apps can read what it receives. Restarts this project's apps with them. One change in History; "+
			"change_undo reverts it.", "secrets")
	cp.Errors = append(cp.Errors, 404)
	huma.Register(api, cp, wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project to copy into"`
		Body    struct {
			From      string   `json:"from" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project to copy from"`
			Names     []string `json:"names,omitempty" maxItems:"200" doc:"Secret names to copy; empty copies all"`
			Overwrite bool     `json:"overwrite,omitempty" doc:"Replace secrets this project already has with the same name"`
			Intent    string   `json:"intent,omitempty" maxLength:"500" doc:"Why you are copying them, in one sentence. Shown in History."`
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
		plat, err := a.secrets()
		if err != nil {
			return nil, err
		}
		src, err := plat.Secrets.All(ctx, in.Body.From)
		if err != nil {
			return nil, err
		}
		have, err := plat.Secrets.All(ctx, in.Project)
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
		set := map[string]string{}
		for _, n := range names {
			v, ok := src[n]
			if !ok {
				return nil, NewProblem(404, "not_found", "no secret "+n+" in "+in.Body.From)
			}
			if _, exists := have[n]; exists && !in.Body.Overwrite {
				out.Skipped = append(out.Skipped, n)
				continue
			}
			set[n] = v
			out.Copied = append(out.Copied, n)
		}
		if len(set) > 0 {
			intent := orDefault(in.Body.Intent, "copy "+strings.Join(out.Copied, ", ")+" from "+in.Body.From)
			c, err := a.secretChange(ctx, in.Project, set, nil, intent)
			if err != nil {
				return nil, err
			}
			out.Change = changeID(c)
		}
		return &struct{ Body SecretsCopied }{out}, nil
	}))

	del := op("secret-delete", http.MethodDelete, "/v1/projects/{project}/secrets/{name}", "secrets delete", RiskWrite, "Delete a secret",
		"Deletes a secret and restarts the project's apps without it. It is a change in History: change_undo puts the secret back "+
			"with its value (kept encrypted in the change log).", "secrets")
	del.Errors = append(del.Errors, 404)
	huma.Register(api, del, wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Name    string `path:"name" pattern:"^[A-Z_][A-Z0-9_]{0,127}$" doc:"Secret name (an env var name)"`
		Intent  string `query:"intent" maxLength:"500" doc:"Why you are deleting it, in one sentence. Shown in History."`
	}) (*struct{ Body SecretDeleted }, error) {
		if err := PrincipalFrom(ctx).Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		c, err := a.secretChange(ctx, in.Project, nil, []string{in.Name}, orDefault(in.Intent, "delete secret "+in.Name))
		if err != nil {
			return nil, err
		}
		return &struct{ Body SecretDeleted }{SecretDeleted{Change: changeID(c)}}, nil
	}))
}
