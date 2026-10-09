package auth

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/manifest"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// BoxProviderState is one sign-in provider's box-wide setup. Secrets are
// never included: SecretSet only says one is stored.
type BoxProviderState struct {
	ID   string `json:"id" doc:"Better Auth's provider ID, also the manifest method"`
	Name string `json:"name"`
	Set  bool   `json:"set" doc:"Box-wide keys are stored: projects that turn this method on use them unless they have their own"`
	BoxProvider
	SecretSet   bool   `json:"secretSet" doc:"Whether the client secret (Apple: the private key) is stored. It is never shown"`
	CallbackURL string `json:"callbackUrl" doc:"The one redirect URI to register with the provider, for every project on the box"`
	// UsedBy are the projects signing in with it on the box-wide keys.
	UsedBy []string `json:"usedBy"`
	// AppleSecretExpires is when the client secret the box made from the
	// Apple key expires; the box makes a new one a month before.
	AppleSecretExpires *time.Time `json:"appleSecretExpires,omitempty"`
}

// BoxProviders is the box's sign-in provider setup.
type BoxProviders struct {
	CallbackBase string             `json:"callbackBase" doc:"Every box-wide provider calls back to <callbackBase>/api/auth/callback/<provider>"`
	Providers    []BoxProviderState `json:"providers"`
}

func providerState(ctx context.Context, p *platform.Platform, prov Provider) (BoxProviderState, error) {
	st := BoxProviderState{ID: prov.ID, Name: prov.Name, CallbackURL: CallbackURL(p, prov.ID), UsedBy: []string{}}
	names, err := p.Secrets.List(ctx, secretsProject)
	if err != nil {
		return st, err
	}
	for _, s := range names {
		if s.Name == secretName(prov) {
			st.SecretSet = true
		}
	}
	box, err := boxProviders(ctx, p)
	if err != nil {
		return st, err
	}
	if b := box[prov.ID]; b != nil {
		st.Set, st.BoxProvider = true, *b
		if st.UsedBy, err = usedBy(ctx, p, prov.ID); err != nil {
			return st, err
		}
		if st.UsedBy == nil {
			st.UsedBy = []string{}
		}
		if prov.ID == manifest.AuthApple {
			if all, err := p.Secrets.All(ctx, secretsProject); err == nil {
				if exp := appleSecretExpiry(ctx, p, b.TeamID, b.KeyID, b.ClientID, all[applePrivateKey]); !exp.IsZero() {
					st.AppleSecretExpires = &exp
				}
			}
		}
	}
	return st, nil
}

type providerIn struct {
	Provider string `path:"provider" enum:"google,github,apple,microsoft,discord,facebook,twitter,linkedin,gitlab,slack,twitch,oidc" doc:"Better Auth's provider ID"`
}

func boxOnly(p *platform.Platform) error {
	if p == nil || p.DB == nil || p.Secrets == nil {
		return api.NewProblem(501, "internal", "sign-in providers are set on the box; point the CLI at a box (tiffin up)")
	}
	return nil
}

// resync rewrites the engine config now. The engine also notices the file
// changed within a second, so an engine that is down picks it up later.
func resync(ctx context.Context, p *platform.Platform) {
	reconcileMu.Lock()
	defer reconcileMu.Unlock()
	if _, err := syncConfig(ctx, p, defaultEngine); err != nil && p.Log != nil {
		p.Log.Warn("auth: engine config after a sign-in provider change", "err", err)
	}
}

// appKeysIn is a project's own keys for a provider. Empty fields keep what is stored.
type appKeysIn struct {
	Project  string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
	Provider string `path:"provider" enum:"google,github,apple,microsoft,discord,facebook,twitter,linkedin,gitlab,slack,twitch,oidc" doc:"Better Auth's provider ID"`
	Body     struct {
		ClientID     string `json:"clientId,omitempty" maxLength:"512" doc:"OAuth client ID (Apple: the Services ID); omit to keep the stored one"`
		ClientSecret string `json:"clientSecret,omitempty" maxLength:"4096" doc:"OAuth client secret; omit to keep the stored one. Not for Apple"`
		TenantID     string `json:"tenantId,omitempty" maxLength:"128" doc:"Microsoft: directory (tenant) ID; default common"`
		Issuer       string `json:"issuer,omitempty" maxLength:"512" doc:"OpenID Connect: the issuer URL. GitLab: a self-managed GitLab's URL"`
		Label        string `json:"label,omitempty" maxLength:"60" doc:"OpenID Connect: the button's name"`
		TeamID       string `json:"teamId,omitempty" maxLength:"20" doc:"Apple: Team ID"`
		KeyID        string `json:"keyId,omitempty" maxLength:"20" doc:"Apple: Key ID"`
		PrivateKey   string `json:"privateKey,omitempty" maxLength:"4096" doc:"Apple: the .p8 key file's text"`
		Intent       string `json:"intent,omitempty" maxLength:"500" doc:"Why, in one sentence. Shown in History"`
	}
}

type appProviderIn struct {
	Project  string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
	Provider string `path:"provider" enum:"google,github,apple,microsoft,discord,facebook,twitter,linkedin,gitlab,slack,twitch,oidc" doc:"Better Auth's provider ID"`
}

func (*Module) registerAppKeys(a huma.API, p *platform.Platform) {
	const ptag = "auth"
	set := api.Op("auth-keys-set", http.MethodPut, "/v1/projects/{project}/auth/providers/{provider}/keys", "auth keys set", api.RiskWrite,
		"Use this app's own keys for a sign-in provider",
		"Stores the provider's client ID and secret as the project's own secrets (encrypted, one change in History that change_undo reverts) and uses them "+
			"instead of the box-wide keys, so people see the app's own name on the provider's sign-in screen. Register appCallbackUrl from auth show with the "+
			"provider: it is the only redirect URI the app needs, for every host and preview. Empty fields keep the stored values.", ptag)
	set.Errors = append(set.Errors, 404, 501)
	huma.Register(a, set, api.Wrap(func(ctx context.Context, in *appKeysIn) (*struct{ Body ProviderState }, error) {
		res, err := authProject(ctx, p, in.Project, tokens.ScopeApplyReversible)
		if err != nil {
			return nil, err
		}
		if err := boxOnly(p); err != nil {
			return nil, err
		}
		prov, _ := ProviderByID(in.Provider)
		names, err := p.Secrets.List(ctx, in.Project)
		if err != nil {
			return nil, err
		}
		have := map[string]bool{}
		for _, s := range names {
			have[s.Name] = true
		}
		b := in.Body
		pin := ProviderInput{BoxProvider: BoxProvider{ClientID: b.ClientID, TenantID: b.TenantID, Issuer: b.Issuer, Label: b.Label, TeamID: b.TeamID, KeyID: b.KeyID},
			ClientSecret: b.ClientSecret, PrivateKey: b.PrivateKey}
		sec, del, err := appKeySecrets(prov, pin, func(n string) bool { return have[n] })
		if err != nil {
			return nil, api.NewProblem(422, "validation", err.Error())
		}
		if prov.ID == manifest.AuthApple && sec["APPLE_PRIVATE_KEY"] != "" {
			if _, err := ParseAppleKey(sec["APPLE_PRIVATE_KEY"]); err != nil {
				return nil, api.NewProblem(422, "validation", "private key: "+err.Error())
			}
		}
		intent := b.Intent
		if intent == "" {
			intent = "Use " + in.Project + "'s own " + prov.Name + " keys for sign-in"
		}
		if _, err := applySecrets(ctx, p, in.Project, sec, del, intent); err != nil {
			return nil, err
		}
		// Saved from the guided setup, which showed this redirect URI to register.
		if err := confirmCallback(ctx, p, in.Project, prov.ID, AppCallbackURL(p, in.Project, res, prov.ID)); err != nil {
			return nil, err
		}
		resync(ctx, p)
		return &struct{ Body ProviderState }{appProviderState(p, in.Project, res, prov.ID)}, nil
	}))

	rm := api.Op("auth-keys-remove", http.MethodDelete, "/v1/projects/{project}/auth/providers/{provider}/keys", "auth keys remove", api.RiskWrite,
		"Stop using this app's own keys for a sign-in provider",
		"Deletes the project's own secrets for the provider (one change in History that change_undo reverts). The provider then uses the box-wide keys if the box has them, "+
			"else its button says it isn't set up. People who signed in with it keep their accounts.", ptag)
	rm.Errors = append(rm.Errors, 404, 501)
	huma.Register(a, rm, api.Wrap(func(ctx context.Context, in *appProviderIn) (*struct{ Body ProviderState }, error) {
		res, err := authProject(ctx, p, in.Project, tokens.ScopeApplyReversible)
		if err != nil {
			return nil, err
		}
		if err := boxOnly(p); err != nil {
			return nil, err
		}
		prov, _ := ProviderByID(in.Provider)
		names, err := p.Secrets.List(ctx, in.Project)
		if err != nil {
			return nil, err
		}
		var del []string
		for _, s := range names {
			if contains(secretNames(prov), s.Name) {
				del = append(del, s.Name)
			}
		}
		if len(del) == 0 {
			return nil, api.NewProblem(404, "not_found", in.Project+" has no keys of its own for "+prov.Name)
		}
		if _, err := applySecrets(ctx, p, in.Project, nil, del, "Stop using "+in.Project+"'s own "+prov.Name+" keys"); err != nil {
			return nil, err
		}
		_ = p.DB.KVDelete(ctx, nsAppCallback, callbackKey(in.Project, prov.ID))
		resync(ctx, p)
		return &struct{ Body ProviderState }{appProviderState(p, in.Project, res, prov.ID)}, nil
	}))

	cf := api.Op("auth-callback-confirm", http.MethodPost, "/v1/projects/{project}/auth/providers/{provider}/callback-confirm", "auth callback confirm", api.RiskWrite,
		"Confirm the app's redirect URI is registered",
		"Records that the app's current redirect URI (appCallbackUrl in auth show) is registered with the provider, which clears callbackChanged. "+
			"It changes when the app's sign-in host changes, for example when it gets a custom domain.", ptag)
	cf.Errors = append(cf.Errors, 404, 501)
	huma.Register(a, cf, api.Wrap(func(ctx context.Context, in *appProviderIn) (*struct{ Body ProviderState }, error) {
		res, err := authProject(ctx, p, in.Project, tokens.ScopeApplyReversible)
		if err != nil {
			return nil, err
		}
		if err := boxOnly(p); err != nil {
			return nil, err
		}
		url := AppCallbackURL(p, in.Project, res, in.Provider)
		if err := confirmCallback(ctx, p, in.Project, in.Provider, url); err != nil {
			return nil, err
		}
		audit(ctx, p, "auth.callback_confirm", in.Project, map[string]any{"provider": in.Provider, "url": url})
		return &struct{ Body ProviderState }{appProviderState(p, in.Project, res, in.Provider)}, nil
	}))
}

func (*Module) registerProviders(a huma.API, p *platform.Platform) {
	const ptag = "auth"
	huma.Register(a, api.Op("auth-providers-list", http.MethodGet, "/v1/auth/providers", "auth providers list", api.RiskRead,
		"List the box's sign-in providers",
		"Each sign-in provider (Google, GitHub, Apple...) with whether box-wide keys are set, its settings (never the secret), the one callback URL to register with it, "+
			"and the projects using it. A project that turns a method on uses these keys unless it has its own <PROVIDER>_CLIENT_ID and <PROVIDER>_CLIENT_SECRET secrets.", ptag),
		api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body *BoxProviders }, error) {
			if err := boxOnly(p); err != nil {
				return nil, err
			}
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, ""); err != nil {
				return nil, err
			}
			out := &BoxProviders{CallbackBase: ProxyURL(p), Providers: []BoxProviderState{}}
			for _, prov := range Providers {
				st, err := providerState(ctx, p, prov)
				if err != nil {
					return nil, err
				}
				out.Providers = append(out.Providers, st)
			}
			return &struct{ Body *BoxProviders }{out}, nil
		}))

	set := api.Op("auth-provider-set", http.MethodPut, "/v1/auth/providers/{provider}", "auth providers set", api.RiskWrite,
		"Set a sign-in provider's box-wide keys",
		"Stores the provider's client ID and secret for every project on the box (the secret encrypted, never shown again). Omit the secret to keep the stored one. "+
			"Register the callback URL from auth providers list with the provider. Apple takes the Team ID, Key ID, Services ID (clientId) and the .p8 key; the box makes "+
			"and renews the client secret itself. Box admins only.", ptag)
	set.Errors = append(set.Errors, 501)
	huma.Register(a, set, api.Wrap(func(ctx context.Context, in *struct {
		Provider string `path:"provider" enum:"google,github,apple,microsoft,discord,facebook,twitter,linkedin,gitlab,slack,twitch,oidc" doc:"Better Auth's provider ID"`
		Body     struct {
			ClientID     string `json:"clientId" minLength:"1" maxLength:"512" doc:"OAuth client ID. Apple: the Services ID, like com.example.signin"`
			ClientSecret string `json:"clientSecret,omitempty" maxLength:"4096" doc:"OAuth client secret; omit to keep the stored one. Not for Apple"`
			TenantID     string `json:"tenantId,omitempty" maxLength:"128" doc:"Microsoft: directory (tenant) ID, or common (default), organizations or consumers"`
			Issuer       string `json:"issuer,omitempty" maxLength:"512" doc:"OpenID Connect: the issuer URL (required). GitLab: a self-managed GitLab's URL"`
			Label        string `json:"label,omitempty" maxLength:"60" doc:"OpenID Connect: the button's name, e.g. Okta"`
			TeamID       string `json:"teamId,omitempty" maxLength:"20" doc:"Apple: Team ID"`
			KeyID        string `json:"keyId,omitempty" maxLength:"20" doc:"Apple: Key ID"`
			PrivateKey   string `json:"privateKey,omitempty" maxLength:"4096" doc:"Apple: the .p8 key file's text; omit to keep the stored one"`
			ConsentName  string `json:"consentName,omitempty" maxLength:"100" doc:"The OAuth app's name as the provider shows it when people sign in, e.g. Acme Labs. Shown to projects choosing between these keys and their own"`
		}
	}) (*struct{ Body BoxProviderState }, error) {
		if err := boxOnly(p); err != nil {
			return nil, err
		}
		pr := api.PrincipalFrom(ctx)
		if !pr.BoxAdmin() {
			return nil, fmt.Errorf("%w: sign-in providers are set by the box owner", tokens.ErrForbidden)
		}
		prov, ok := ProviderByID(in.Provider)
		if !ok {
			return nil, api.NewProblem(404, "not_found", "no sign-in provider "+in.Provider)
		}
		b := in.Body
		pin := ProviderInput{BoxProvider: BoxProvider{ClientID: b.ClientID, TenantID: b.TenantID, Issuer: b.Issuer, Label: b.Label, TeamID: b.TeamID, KeyID: b.KeyID, ConsentName: b.ConsentName},
			ClientSecret: b.ClientSecret, PrivateKey: b.PrivateKey}
		before, _ := providerState(ctx, p, prov)
		if err := setProvider(ctx, p, prov, pin, pr.TokenID); err != nil {
			return nil, api.NewProblem(422, "validation", err.Error())
		}
		detail := map[string]any{"provider": prov.ID, "clientId": pin.ClientID, "replaced": before.Set,
			"secretChanged": b.ClientSecret != "" || b.PrivateKey != "", "session": pr.Session}
		_ = p.DB.Audit(ctx, pr.TokenID, "auth.provider.set", prov.ID, detail)
		resync(ctx, p)
		st, err := providerState(ctx, p, prov)
		if err != nil {
			return nil, err
		}
		return &struct{ Body BoxProviderState }{st}, nil
	}))

	del := api.Op("auth-provider-delete", http.MethodDelete, "/v1/auth/providers/{provider}", "auth providers remove", api.RiskDestructive,
		"Remove a sign-in provider's box-wide keys",
		"Deletes the box-wide keys. Projects that sign in with this provider on them (usedBy in auth providers list) stop offering it until they get keys again: "+
			"set them here or as the project's own secrets. Box admins only.", ptag)
	del.Errors = append(del.Errors, 404, 501)
	huma.Register(a, del, api.Wrap(func(ctx context.Context, in *providerIn) (*struct{ Body BoxProviderState }, error) {
		if err := boxOnly(p); err != nil {
			return nil, err
		}
		pr := api.PrincipalFrom(ctx)
		if !pr.BoxAdmin() {
			return nil, fmt.Errorf("%w: sign-in providers are set by the box owner", tokens.ErrForbidden)
		}
		prov, ok := ProviderByID(in.Provider)
		if !ok {
			return nil, api.NewProblem(404, "not_found", "no sign-in provider "+in.Provider)
		}
		before, err := providerState(ctx, p, prov)
		if err != nil {
			return nil, err
		}
		had, err := removeProvider(ctx, p, prov)
		if err != nil {
			return nil, err
		}
		if !had {
			return nil, api.NewProblem(404, "not_found", prov.Name+" has no box-wide keys")
		}
		_ = p.DB.Audit(ctx, pr.TokenID, "auth.provider.remove", prov.ID, map[string]any{"provider": prov.ID, "clientId": before.ClientID, "usedBy": before.UsedBy, "session": pr.Session})
		resync(ctx, p)
		st, err := providerState(ctx, p, prov)
		if err != nil {
			return nil, err
		}
		return &struct{ Body BoxProviderState }{st}, nil
	}))
}
