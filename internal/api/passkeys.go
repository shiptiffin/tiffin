package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/shiptiffin/tiffin/internal/passkeys"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

func (a *API) passkeysMgr() (*passkeys.Manager, error) {
	if a.deps.Passkeys == nil {
		return nil, NewProblem(501, "internal", "passkeys are only available on a box")
	}
	return a.deps.Passkeys, nil
}

func passkeyErr(err error) error {
	switch {
	case errors.Is(err, passkeys.ErrNotFound):
		return NewProblem(404, "not_found", err.Error())
	case errors.Is(err, passkeys.ErrNoPerson), errors.Is(err, passkeys.ErrCloned):
		return NewProblem(403, "forbidden", err.Error())
	}
	return err
}

// registerPasskeys adds the operations people use to manage the passkeys
// they sign in to the dashboard with. Adding one needs a browser, so those
// two are dashboard-only (no CLI command or MCP tool).
func (a *API) registerPasskeys() {
	api := a.api
	huma.Register(api, op("passkeys-list", http.MethodGet, "/v1/passkeys", "passkeys list", RiskRead, "List passkeys",
		"Your passkeys: they sign you in to the dashboard. People only (dashboard sessions, or the owner's own token); never API keys.", "passkeys"),
		wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body []passkeys.Passkey }, error) {
			m, err := a.passkeysMgr()
			if err != nil {
				return nil, err
			}
			l, err := m.Passkeys(ctx, PrincipalFrom(ctx))
			return &struct{ Body []passkeys.Passkey }{l}, passkeyErr(err)
		}))

	rb := op("passkey-register-begin", http.MethodPost, "/v1/passkeys/register", "-", RiskWrite, "Start adding a passkey",
		"Returns WebAuthn creation options for a discoverable passkey (resident key and user verification required), so it can sign you in without a username. "+
			"Any person's dashboard session; never API keys (the dashboard calls this). A passkey signs in for good, so in a dashboard session adding one "+
			"needs a sign-in with a passkey, Google, GitHub or an emailed link in the last 10 minutes, or a confirmation with one of your passkeys "+
			"(POST /v1/session/confirm): reauth_required otherwise.", "passkeys")
	huma.Register(api, rb, wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body any }, error) {
		m, err := a.passkeysMgr()
		if err != nil {
			return nil, err
		}
		p := PrincipalFrom(ctx)
		if err := a.deps.Tokens.RequireSudo(ctx, p, tokens.ErrReauthPasskey); err != nil {
			return nil, err
		}
		opts, err := m.BeginRegistration(ctx, p)
		if err != nil {
			return nil, passkeyErr(err)
		}
		return &struct{ Body any }{opts}, nil
	}))

	rf := op("passkey-register-finish", http.MethodPost, "/v1/passkeys", "-", RiskWrite, "Finish adding a passkey",
		"Stores the passkey from navigator.credentials.create(). It signs this person in to the dashboard from then on. "+
			"Needs the same recent sign-in as starting (reauth_required otherwise), and emails the person a \"New passkey\" notice "+
			"with the browser, address and country it came from.", "passkeys")
	rf.Middlewares = huma.Middlewares{withClientIP}
	huma.Register(api, rf, wrap(func(ctx context.Context, in *struct {
		Body struct {
			Name       string          `json:"name,omitempty" maxLength:"64"`
			Credential json.RawMessage `json:"credential"`
		}
	}) (*struct{ Body *passkeys.Passkey }, error) {
		m, err := a.passkeysMgr()
		if err != nil {
			return nil, err
		}
		p := PrincipalFrom(ctx)
		if err := a.deps.Tokens.RequireSudo(ctx, p, tokens.ErrReauthPasskey); err != nil {
			return nil, err
		}
		pk, err := m.FinishRegistration(ctx, p, in.Body.Name, in.Body.Credential)
		if err != nil {
			return nil, passkeyErr(err)
		}
		a.passkeyNotice(ctx, p, BoxMailNewPasskey, pk)
		return &struct{ Body *passkeys.Passkey }{pk}, nil
	}))

	del := op("passkey-delete", http.MethodDelete, "/v1/passkeys/{id}", "passkeys delete", RiskDestructive, "Remove a passkey",
		"Removes one of your passkeys immediately: it no longer signs you in. The person is emailed a \"Passkey removed\" notice.", "passkeys")
	del.Middlewares = huma.Middlewares{withClientIP}
	huma.Register(api, del, wrap(func(ctx context.Context, in *struct {
		ID string `path:"id" maxLength:"256"`
	}) (*struct{}, error) {
		m, err := a.passkeysMgr()
		if err != nil {
			return nil, err
		}
		p := PrincipalFrom(ctx)
		pk, err := m.DeletePasskey(ctx, p, in.ID)
		if err != nil {
			return nil, passkeyErr(err)
		}
		a.passkeyNotice(ctx, p, BoxMailPasskeyRemoved, pk)
		return &struct{}{}, nil
	}))
}
