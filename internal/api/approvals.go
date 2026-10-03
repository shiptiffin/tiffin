package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/btahir/tiffin/internal/approvals"
	"github.com/danielgtaylor/huma/v2"
)

func (a *API) approvalsMgr() (*approvals.Manager, error) {
	if a.deps.Approvals == nil {
		return nil, NewProblem(501, "internal", "approvals are only available on a box")
	}
	return a.deps.Approvals, nil
}

func approvalErr(err error) error {
	switch {
	case errors.Is(err, approvals.ErrNotFound):
		return NewProblem(404, "not_found", err.Error())
	case errors.Is(err, approvals.ErrNotPending), errors.Is(err, approvals.ErrInvalid):
		return NewProblem(409, "precondition", err.Error())
	case errors.Is(err, approvals.ErrHumanOnly), errors.Is(err, approvals.ErrNoPerson), errors.Is(err, approvals.ErrCloned):
		return NewProblem(403, "forbidden", err.Error())
	case errors.Is(err, approvals.ErrNoPasskey):
		return NewProblem(409, "precondition", err.Error())
	}
	return err
}

func (a *API) registerApprovals() {
	api := a.api
	huma.Register(api, op("approvals-list", http.MethodGet, "/v1/approvals", "approvals list", RiskRead, "List approval requests",
		"Plans agents asked a human to approve (irreversible or outbound changes beyond their token). Box admins see all.", "approvals"),
		wrap(func(ctx context.Context, in *struct {
			Status string `query:"status" enum:"pending,approved,rejected,used,expired," doc:"Only this status"`
		}) (*struct{ Body []*approvals.Approval }, error) {
			m, err := a.approvalsMgr()
			if err != nil {
				return nil, err
			}
			p := PrincipalFrom(ctx)
			list, err := m.List(ctx, in.Status, 100)
			if err != nil {
				return nil, err
			}
			out := []*approvals.Approval{}
			for _, ap := range list {
				if p.BoxAdmin() || ap.RequestedBy == p.TokenID {
					out = append(out, ap)
				}
			}
			return &struct{ Body []*approvals.Approval }{out}, nil
		}))

	huma.Register(api, op("approval-get", http.MethodGet, "/v1/approvals/{id}", "approvals get", RiskRead, "Get an approval request",
		"One approval request with its full plan. Agents poll this to learn when a human decided.", "approvals"),
		wrap(func(ctx context.Context, in *struct {
			ID string `path:"id" pattern:"^apr_[0-9A-Z]{26}$"`
		}) (*struct{ Body *approvals.Approval }, error) {
			m, err := a.approvalsMgr()
			if err != nil {
				return nil, err
			}
			ap, err := m.Get(ctx, in.ID)
			if err != nil {
				return nil, approvalErr(err)
			}
			if p := PrincipalFrom(ctx); !p.BoxAdmin() && ap.RequestedBy != p.TokenID {
				return nil, NewProblem(404, "not_found", "approval not found")
			}
			return &struct{ Body *approvals.Approval }{ap}, nil
		}))

	huma.Register(api, op("approval-begin", http.MethodPost, "/v1/approvals/{id}/begin", "approvals begin", RiskWrite, "Start approving with a passkey",
		"Returns WebAuthn assertion options bound to this approval. Humans only (the dashboard calls this).", "approvals"),
		wrap(func(ctx context.Context, in *struct {
			ID string `path:"id" pattern:"^apr_[0-9A-Z]{26}$"`
		}) (*struct{ Body any }, error) {
			m, err := a.approvalsMgr()
			if err != nil {
				return nil, err
			}
			opts, err := m.BeginApproval(ctx, PrincipalFrom(ctx), in.ID)
			if err != nil {
				return nil, approvalErr(err)
			}
			return &struct{ Body any }{opts}, nil
		}))

	huma.Register(api, op("approval-approve", http.MethodPost, "/v1/approvals/{id}/approve", "approvals approve", RiskWrite, "Approve with a passkey",
		"Verifies the passkey assertion and approves the plan. Humans only.", "approvals"),
		wrap(func(ctx context.Context, in *struct {
			ID   string `path:"id" pattern:"^apr_[0-9A-Z]{26}$"`
			Body struct {
				Credential json.RawMessage `json:"credential" doc:"The PublicKeyCredential from navigator.credentials.get()"`
			}
		}) (*struct{ Body *approvals.Approval }, error) {
			m, err := a.approvalsMgr()
			if err != nil {
				return nil, err
			}
			ap, err := m.FinishApproval(ctx, PrincipalFrom(ctx), in.ID, in.Body.Credential)
			if err != nil {
				return nil, approvalErr(err)
			}
			return &struct{ Body *approvals.Approval }{ap}, nil
		}))

	huma.Register(api, op("approval-reject", http.MethodPost, "/v1/approvals/{id}/reject", "approvals reject", RiskWrite, "Reject an approval request",
		"Declines the plan; the agent sees the reason. Humans only.", "approvals"),
		wrap(func(ctx context.Context, in *struct {
			ID   string `path:"id" pattern:"^apr_[0-9A-Z]{26}$"`
			Body struct {
				Reason string `json:"reason,omitempty" maxLength:"500"`
			}
		}) (*struct{ Body *approvals.Approval }, error) {
			m, err := a.approvalsMgr()
			if err != nil {
				return nil, err
			}
			ap, err := m.Reject(ctx, in.ID, PrincipalFrom(ctx), in.Body.Reason)
			if err != nil {
				return nil, approvalErr(err)
			}
			return &struct{ Body *approvals.Approval }{ap}, nil
		}))

	huma.Register(api, op("passkeys-list", http.MethodGet, "/v1/passkeys", "passkeys list", RiskRead, "List passkeys",
		"Your passkeys. They sign you in to the dashboard; owners and admins also approve plans with them.", "approvals"),
		wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body []approvals.Passkey }, error) {
			m, err := a.approvalsMgr()
			if err != nil {
				return nil, err
			}
			l, err := m.Passkeys(ctx, PrincipalFrom(ctx))
			return &struct{ Body []approvals.Passkey }{l}, approvalErr(err)
		}))

	huma.Register(api, op("passkey-register-begin", http.MethodPost, "/v1/passkeys/register", "passkeys register-begin", RiskWrite, "Start adding a passkey",
		"Returns WebAuthn creation options for a discoverable passkey (resident key and user verification required), so it can sign you in without a username. Any person's dashboard session; never agents (the dashboard calls this).", "approvals"),
		wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body any }, error) {
			m, err := a.approvalsMgr()
			if err != nil {
				return nil, err
			}
			opts, err := m.BeginRegistration(ctx, PrincipalFrom(ctx))
			if err != nil {
				return nil, approvalErr(err)
			}
			return &struct{ Body any }{opts}, nil
		}))

	huma.Register(api, op("passkey-register-finish", http.MethodPost, "/v1/passkeys", "passkeys register-finish", RiskWrite, "Finish adding a passkey",
		"Stores the passkey from navigator.credentials.create(). It signs this person in from then on (and approves plans if they are an owner or admin).", "approvals"),
		wrap(func(ctx context.Context, in *struct {
			Body struct {
				Name       string          `json:"name,omitempty" maxLength:"64"`
				Credential json.RawMessage `json:"credential"`
			}
		}) (*struct{ Body *approvals.Passkey }, error) {
			m, err := a.approvalsMgr()
			if err != nil {
				return nil, err
			}
			pk, err := m.FinishRegistration(ctx, PrincipalFrom(ctx), in.Body.Name, in.Body.Credential)
			if err != nil {
				return nil, approvalErr(err)
			}
			return &struct{ Body *approvals.Passkey }{pk}, nil
		}))

	del := op("passkey-delete", http.MethodDelete, "/v1/passkeys/{id}", "passkeys delete", RiskDestructive, "Remove a passkey",
		"Removes one of your passkeys immediately: it no longer signs you in or approves plans.", "approvals")
	huma.Register(api, del, wrap(func(ctx context.Context, in *struct {
		ID string `path:"id" maxLength:"256"`
	}) (*struct{}, error) {
		m, err := a.approvalsMgr()
		if err != nil {
			return nil, err
		}
		return &struct{}{}, approvalErr(m.DeletePasskey(ctx, PrincipalFrom(ctx), in.ID))
	}))
}
