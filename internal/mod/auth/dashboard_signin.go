package auth

import (
	"context"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/platform"
)

// Dashboard sign-in with Google or GitHub uses the box-wide keys and their
// one callback URL (CallbackURL): the API's flow gets them from here, and
// DashboardHandler gives it the first look at each callback.

type dashboardKeys struct{ p *platform.Platform }

var dashboardProviders = []string{manifest.AuthGoogle, manifest.AuthGitHub}

func (d dashboardKeys) Configured(ctx context.Context) []string {
	box, err := boxProviders(ctx, d.p)
	if err != nil {
		return nil
	}
	var out []string
	for _, id := range dashboardProviders {
		if box[id] != nil && box[id].ClientID != "" {
			out = append(out, id)
		}
	}
	return out
}

func (d dashboardKeys) Client(ctx context.Context, provider string) (api.OAuthClient, bool, error) {
	prov, ok := ProviderByID(provider)
	if !ok || (provider != manifest.AuthGoogle && provider != manifest.AuthGitHub) {
		return api.OAuthClient{}, false, nil
	}
	box, err := boxProviders(ctx, d.p)
	if err != nil {
		return api.OAuthClient{}, false, err
	}
	b := box[provider]
	if b == nil || b.ClientID == "" {
		return api.OAuthClient{}, false, nil
	}
	all, err := d.p.Secrets.All(ctx, secretsProject)
	if err != nil {
		return api.OAuthClient{}, false, err
	}
	secret := all[secretName(prov)]
	if secret == "" {
		return api.OAuthClient{}, false, nil
	}
	return api.OAuthClient{ClientID: b.ClientID, ClientSecret: secret, RedirectURL: CallbackURL(d.p, provider)}, true, nil
}

// registerDashboardSignIn hands the box-wide Google and GitHub keys to the
// dashboard's own sign-in (on a box only).
func registerDashboardSignIn(p *platform.Platform) {
	if p == nil || p.DB == nil || p.Secrets == nil {
		return
	}
	api.SetDashboardOAuth(dashboardKeys{p})
}
