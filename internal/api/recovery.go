package api

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/sshkeys"
)

// ServerKeysFile is the SSH key file Tiffin manages for root (tests point
// it elsewhere). A box on a server serves it as Deps.ServerKeys.
var ServerKeysFile = sshkeys.File

// SessionBox is what the login page may know about the box before anyone
// signs in: never who has which ways in.
type SessionBox struct {
	Managed bool   `json:"managed" doc:"ShipTiffin set this box up (its owner gets back in with ssh root@<host> tiffin login)"`
	Host    string `json:"host" doc:"The box's domain: its dashboard is dashboard.<host>, and it's the name to ssh to"`
}

// Recovery is how the signed-in person can get back in once their session
// ends.
type Recovery struct {
	Passkeys  int  `json:"passkeys" doc:"Your passkeys"`
	SSHKeys   int  `json:"sshKeys" doc:"SSH keys that log in to the server as root (they make a new sign-in link with tiffin login)"`
	Email     bool `json:"email" doc:"The box sends mail, so it can email a sign-in link"`
	Providers int  `json:"providers" doc:"Sign-in providers (Google, GitHub) with keys set"`
	// FirstPasskeyOpen: see tokens.FirstPasskeyOpen.
	FirstPasskeyOpen bool `json:"firstPasskeyOpen" doc:"You have no passkey and signed in within the last hour: you can add your first one without confirming it's you"`
}

// boxHost is the box's domain: the platform's, or the dashboard URL's host
// without its first label.
func (a *API) boxHost() string {
	if p := a.deps.Platform; p != nil && p.Domain != "" {
		return p.Domain
	}
	u, err := url.Parse(a.deps.PublicURL)
	if err != nil {
		return ""
	}
	if _, rest, ok := strings.Cut(u.Hostname(), "."); ok && strings.Contains(rest, ".") {
		return rest
	}
	return u.Hostname()
}

func (a *API) registerRecovery() {
	api := a.api

	sb := op("session-box", http.MethodGet, "/v1/session/box", "-", RiskRead, "Which box is this?",
		"Whether ShipTiffin set this box up, and its domain, so the login page can say how to get back in. "+
			"Never anyone's passkeys or keys.", "system")
	sb.Security = nil
	huma.Register(api, sb, wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body SessionBox }, error) {
		c, _ := platform.LoadManagedConfig()
		return &struct{ Body SessionBox }{SessionBox{Managed: c != nil, Host: a.boxHost()}}, nil
	}))

	huma.Register(api, op("me-recovery", http.MethodGet, "/v1/me/recovery", "-", RiskRead, "Your ways back in",
		"How you can sign in again when this session ends: your passkeys, the server's SSH keys, sign-in by email and by Google or GitHub. "+
			"People only (dashboard sessions, or the owner token); never API keys.", "system"),
		wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body Recovery }, error) {
			p := PrincipalFrom(ctx)
			if p == nil || p.Person == "" {
				return nil, problem(403, "forbidden", "an API key acts for nobody, so it has no ways to sign in")
			}
			var r Recovery
			has := false
			if m := a.deps.Passkeys; m != nil {
				l, err := m.Passkeys(ctx, p)
				if err != nil {
					return nil, passkeyErr(err)
				}
				r.Passkeys, has = len(l), len(l) > 0
			}
			if keys, err := (sshkeys.Store{Path: ServerKeysFile}).Read(); err == nil {
				r.SSHKeys = len(keys)
			}
			if bm := boxMailer(); bm != nil {
				r.Email = bm.BoxMailRelayed(ctx, a.deps.Platform)
			}
			r.Providers = len(oauthSignInConfigured(ctx))
			r.FirstPasskeyOpen = a.deps.Passkeys != nil && a.deps.Tokens.FirstPasskeyOpen(ctx, p, has)
			return &struct{ Body Recovery }{r}, nil
		}))
}
