package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/btahir/tiffin/internal/passkeys"
	"github.com/danielgtaylor/huma/v2"
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

	huma.Register(api, op("passkey-register-begin", http.MethodPost, "/v1/passkeys/register", "-", RiskWrite, "Start adding a passkey",
		"Returns WebAuthn creation options for a discoverable passkey (resident key and user verification required), so it can sign you in without a username. Any person's dashboard session; never API keys (the dashboard calls this).", "passkeys"),
		wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body any }, error) {
			m, err := a.passkeysMgr()
			if err != nil {
				return nil, err
			}
			opts, err := m.BeginRegistration(ctx, PrincipalFrom(ctx))
			if err != nil {
				return nil, passkeyErr(err)
			}
			return &struct{ Body any }{opts}, nil
		}))

	huma.Register(api, op("passkey-register-finish", http.MethodPost, "/v1/passkeys", "-", RiskWrite, "Finish adding a passkey",
		"Stores the passkey from navigator.credentials.create(). It signs this person in to the dashboard from then on.", "passkeys"),
		wrap(func(ctx context.Context, in *struct {
			Body struct {
				Name       string          `json:"name,omitempty" maxLength:"64"`
				Credential json.RawMessage `json:"credential"`
			}
		}) (*struct{ Body *passkeys.Passkey }, error) {
			m, err := a.passkeysMgr()
			if err != nil {
				return nil, err
			}
			pk, err := m.FinishRegistration(ctx, PrincipalFrom(ctx), in.Body.Name, in.Body.Credential)
			if err != nil {
				return nil, passkeyErr(err)
			}
			return &struct{ Body *passkeys.Passkey }{pk}, nil
		}))

	del := op("passkey-delete", http.MethodDelete, "/v1/passkeys/{id}", "passkeys delete", RiskDestructive, "Remove a passkey",
		"Removes one of your passkeys immediately: it no longer signs you in.", "passkeys")
	huma.Register(api, del, wrap(func(ctx context.Context, in *struct {
		ID string `path:"id" maxLength:"256"`
	}) (*struct{}, error) {
		m, err := a.passkeysMgr()
		if err != nil {
			return nil, err
		}
		return &struct{}{}, passkeyErr(m.DeletePasskey(ctx, PrincipalFrom(ctx), in.ID))
	}))
}
