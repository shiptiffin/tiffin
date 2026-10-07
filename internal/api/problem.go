package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
)

// Problem is every error the API returns (RFC 9457 problem+json), with
// fields agents can act on: a stable code, field errors, the plan that needs
// confirming and a one-line hint for what to do next.
type Problem struct {
	Type   string       `json:"type,omitempty" doc:"URI identifying the problem type"`
	Title  string       `json:"title" doc:"Short summary"`
	Status int          `json:"status" doc:"HTTP status code"`
	Code   string       `json:"code" doc:"Stable machine-readable code" enum:"bad_request,validation,unauthenticated,forbidden,rate_limited,not_found,conflict,precondition,confirm_required,reauth_required,plan_mismatch,internal"`
	Detail string       `json:"detail,omitempty" doc:"What went wrong"`
	Hint   string       `json:"hint,omitempty" doc:"What to do next"`
	Errors []FieldError `json:"errors,omitempty" doc:"Per-field problems"`
	Plan   *change.Plan `json:"plan,omitempty" doc:"The plan to review and confirm (confirm_required, plan_mismatch), or the plan the key may not apply (forbidden)"`
}

// FieldError points at one bad input field.
type FieldError struct {
	Path    string `json:"path" doc:"JSON pointer or parameter location"`
	Message string `json:"message"`
}

func (p *Problem) Error() string {
	if p.Detail != "" {
		return p.Detail
	}
	return p.Title
}

// GetStatus implements huma.StatusError.
func (p *Problem) GetStatus() int { return p.Status }

// ContentType implements huma.ContentTypeFilter.
func (p *Problem) ContentType(string) string { return "application/problem+json" }

func problem(status int, code, detail string) *Problem {
	return &Problem{Type: "https://shiptiffin.com/errors/" + code, Title: http.StatusText(status), Status: status, Code: code, Detail: detail}
}

func init() {
	// Route huma's own errors (bad params, body validation) through Problem
	// so every error has the same shape.
	huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
		code := "bad_request"
		switch status {
		case http.StatusUnprocessableEntity:
			code = "validation"
		case http.StatusUnauthorized:
			code = "unauthenticated"
		case http.StatusForbidden:
			code = "forbidden"
		case http.StatusNotFound:
			code = "not_found"
		case http.StatusTooManyRequests:
			code = "rate_limited"
		case http.StatusInternalServerError:
			code = "internal"
		}
		p := problem(status, code, msg)
		for i, e := range errs {
			if e == nil {
				continue
			}
			if len(p.Errors) == maxProblemErrors {
				p.Errors = append(p.Errors, FieldError{Message: fmt.Sprintf("and up to %d more", len(errs)-i)})
				break
			}
			var d *huma.ErrorDetail
			if errors.As(e, &d) {
				p.Errors = append(p.Errors, FieldError{Path: d.Location, Message: d.Message})
			} else {
				p.Errors = append(p.Errors, FieldError{Message: e.Error()})
			}
		}
		return p
	}
}

// keyHint is what to do about a key that cannot reach something.
const keyHint = "each API key reaches only its projects, at full or read access; ask the box owner to do it, or for a key that reaches it"

// toProblem maps domain errors to API problems.
func toProblem(err error) error {
	if err == nil {
		return nil
	}
	var (
		p   *Problem
		ve  *manifest.ValidationError
		ee  *manifest.EvalError
		cr  *change.ConfirmRequiredError
		de  *change.DeniedError
		pe  *change.PreconditionError
		hse huma.StatusError
	)
	switch {
	case errors.As(err, &p):
		return p
	case errors.As(err, &ve):
		out := problem(422, "validation", "the manifest is invalid")
		for _, f := range ve.Errors {
			out.Errors = append(out.Errors, FieldError{Path: "/manifest" + f.Path, Message: f.Message})
		}
		out.Hint = "fix the listed fields; GET /v1/schema/manifest describes every field"
		return out
	case errors.As(err, &ee):
		return problem(422, "validation", ee.Message)
	case errors.As(err, &cr):
		code, hint := "confirm_required", fmt.Sprintf("review the plan, then repeat the call with confirm=%q", cr.Plan.Hash[:12])
		if cr.Mismatch {
			code, hint = "plan_mismatch", fmt.Sprintf("the plan changed since you reviewed it; review this plan and confirm %q", cr.Plan.Hash[:12])
		}
		out := problem(428, code, cr.Error())
		out.Plan, out.Hint = cr.Plan, hint
		return out
	case errors.As(err, &de):
		out := problem(403, "forbidden", de.Reason)
		out.Plan = de.Plan
		out.Hint = keyHint
		return out
	case errors.Is(err, change.ErrConflict):
		out := problem(409, "conflict", err.Error())
		out.Hint = "plan again and confirm the new hash"
		return out
	case errors.As(err, &pe):
		return problem(409, "precondition", pe.Error())
	case errors.Is(err, change.ErrNotFound), errors.Is(err, tokens.ErrNotFound):
		return problem(404, "not_found", err.Error())
	case errors.Is(err, tokens.ErrUnauthenticated):
		out := problem(401, "unauthenticated", err.Error())
		out.Hint = "send Authorization: Bearer <token> (TIFFIN_TOKEN for the CLI)"
		return out
	case errors.Is(err, tokens.ErrReauthPasskey), errors.Is(err, tokens.ErrReauthEmail):
		out := problem(403, "reauth_required", err.Error())
		out.Hint = "confirm with one of your passkeys (POST /v1/session/confirm), or sign in again with a passkey, Google, GitHub or an emailed link, then repeat the call"
		return out
	case errors.Is(err, tokens.ErrReauth):
		out := problem(403, "reauth_required", err.Error())
		out.Hint = "confirm with a passkey (POST /v1/session/confirm), or sign in again with a passkey, Google, GitHub or an emailed link, then repeat the call; or make a read-only key that lasts a day"
		return out
	case errors.Is(err, tokens.ErrEmailByKey):
		out := problem(403, "forbidden", err.Error())
		out.Hint = "change it in the dashboard (Settings › People), or with the owner token (tiffin people email)"
		return out
	case errors.Is(err, tokens.ErrOwnerEmail):
		out := problem(403, "forbidden", err.Error())
		out.Hint = "ask the owner to change it"
		return out
	case errors.Is(err, tokens.ErrForbidden):
		out := problem(403, "forbidden", err.Error())
		out.Hint = keyHint
		return out
	case errors.Is(err, tokens.ErrInvalid):
		return problem(422, "validation", err.Error())
	case errors.As(err, &hse):
		return hse
	}
	slog.Error("internal error", "err", err)
	return problem(500, "internal", "internal error")
}
