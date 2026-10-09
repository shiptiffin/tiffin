package auth

import (
	"context"
	"strings"

	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/manifest"
	"github.com/shiptiffin/tiffin/internal/platform"
)

// A project's own provider keys ("this app's own keys"): kept as the
// project's secrets (encrypted, changes in History with undo), and used
// instead of the box-wide keys. People then see the app's own name on the
// provider's sign-in screen, never the box's.
//
// The provider needs ONE redirect URI for the app, on its sign-in host
// (AppCallbackURL): sign-ins started on the app's other hosts go out with
// that redirect URI and come back through it, the way box-wide sign-ins
// come back through the dashboard host. When that host changes (a custom
// domain added, say), the redirect URI changes, and the Auth page says so
// until someone confirms they updated it with the provider.

const nsAppCallback = "auth/app-callback" // key: <project>/<provider> → the redirect URI last confirmed

// secretNames are every secret a project may set for a provider.
func secretNames(prov Provider) []string {
	e := prov.Env
	names := []string{e + "_CLIENT_ID", e + "_CLIENT_SECRET"}
	switch prov.ID {
	case manifest.AuthApple:
		names = append(names, e+"_TEAM_ID", e+"_KEY_ID", e+"_PRIVATE_KEY")
	case manifest.AuthMicrosoft:
		names = append(names, e+"_TENANT_ID")
	case manifest.AuthGitLab:
		names = append(names, e+"_ISSUER")
	case manifest.AuthOIDC:
		names = append(names, e+"_ISSUER", e+"_NAME")
	}
	return names
}

// appKeySecrets turns what someone typed into the project secrets to set
// and delete, after checking that the result is complete. have says which
// secrets the project already has (an empty field keeps those).
func appKeySecrets(prov Provider, in ProviderInput, have func(string) bool) (set map[string]string, del []string, err error) {
	e := prov.Env
	check := in
	keep := func(field *string, name string) {
		if strings.TrimSpace(*field) == "" && have(name) {
			*field = "kept" // validation only; the stored value stays
		}
	}
	keep(&check.ClientID, e+"_CLIENT_ID")
	if prov.ID == manifest.AuthApple {
		keep(&check.TeamID, e+"_TEAM_ID")
		keep(&check.KeyID, e+"_KEY_ID")
	}
	stored := have(e+"_CLIENT_SECRET") || (prov.ID == manifest.AuthApple && have(e+"_PRIVATE_KEY"))
	if prov.ID == manifest.AuthOIDC {
		keep(&check.Issuer, e+"_ISSUER")
	}
	if err := validateInput(prov, &check, stored); err != nil {
		return nil, nil, err
	}
	set = map[string]string{}
	put := func(name, v string) {
		if v = strings.TrimSpace(v); v != "" {
			set[name] = v
		}
	}
	put(e+"_CLIENT_ID", in.ClientID)
	switch prov.ID {
	case manifest.AuthApple:
		put(e+"_TEAM_ID", in.TeamID)
		put(e+"_KEY_ID", in.KeyID)
		if strings.TrimSpace(in.PrivateKey) != "" {
			set[e+"_PRIVATE_KEY"] = strings.TrimSpace(in.PrivateKey)
		}
		// The box signs the client secret from the key: a ready-made one would win.
		if have(e + "_CLIENT_SECRET") {
			del = append(del, e+"_CLIENT_SECRET")
		}
	case manifest.AuthMicrosoft:
		put(e+"_TENANT_ID", in.TenantID)
		put(e+"_CLIENT_SECRET", in.ClientSecret)
	case manifest.AuthGitLab:
		put(e+"_ISSUER", strings.TrimRight(in.Issuer, "/"))
		put(e+"_CLIENT_SECRET", in.ClientSecret)
	case manifest.AuthOIDC:
		put(e+"_ISSUER", strings.TrimRight(in.Issuer, "/"))
		put(e+"_NAME", in.Label)
		put(e+"_CLIENT_SECRET", in.ClientSecret)
	default:
		put(e+"_CLIENT_SECRET", in.ClientSecret)
	}
	return set, del, nil
}

// applySecrets changes a project's secrets as the caller, in one change.
func applySecrets(ctx context.Context, p *platform.Platform, project string, set map[string]string, del []string, intent string) (*change.Change, error) {
	pr := api.PrincipalFrom(ctx)
	plan, err := p.PlanSecrets(ctx, project, set, del, pr.TokenID)
	if err != nil {
		return nil, err
	}
	c, err := p.Engine.Apply(ctx, change.ApplyRequest{Plan: plan, Confirm: plan.Hash, Actor: pr.Actor(), Intent: intent, Authorize: pr.Authorizer()})
	if err != nil {
		return nil, err
	}
	if c != nil {
		p.AfterApply(c)
	}
	return c, nil
}

func callbackKey(project, provider string) string { return project + "/" + provider }

// confirmedCallback is the redirect URI someone last confirmed is
// registered with the provider for the project's own keys.
func confirmedCallback(ctx context.Context, p *platform.Platform, project, provider string) string {
	raw, ok, err := p.DB.KVGet(ctx, nsAppCallback, callbackKey(project, provider))
	if err != nil || !ok {
		return ""
	}
	return string(raw)
}

func confirmCallback(ctx context.Context, p *platform.Platform, project, provider, url string) error {
	return p.DB.KVPut(ctx, nsAppCallback, callbackKey(project, provider), []byte(url))
}

// appProviderState is one provider's state for a project, after a change.
func appProviderState(p *platform.Platform, project string, res map[string]change.Resource, id string) ProviderState {
	for _, st := range overview(p, project, res).Providers {
		if st.ID == id {
			return st
		}
	}
	return ProviderState{ID: id}
}
