package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
)

// Invite is a person plus their one-time sign-in link.
type Invite struct {
	Person    *tokens.Person `json:"person"`
	URL       string         `json:"url" doc:"One-time sign-in link to send them"`
	ExpiresAt time.Time      `json:"expiresAt"`
}

func peopleErr(err error) error {
	if errors.Is(err, tokens.ErrPersonNotFound) {
		return NewProblem(404, "not_found", err.Error())
	}
	return err
}

func (a *API) registerPeople() {
	api := a.api
	link := func(ctx context.Context, p *tokens.Principal, person *tokens.Person) (*Invite, error) {
		code, exp, err := a.deps.Tokens.LoginLinkFor(ctx, p, person.ID)
		if err != nil {
			return nil, peopleErr(err)
		}
		return &Invite{Person: person, URL: strings.TrimRight(a.deps.PublicURL, "/") + "/login#" + code, ExpiresAt: exp}, nil
	}

	huma.Register(api, op("people-list", http.MethodGet, "/v1/people", "people list", RiskRead, "List people",
		"The humans who use this box's dashboard, with their roles (owner, admin, member, viewer).", "people"),
		wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body []*tokens.Person }, error) {
			if err := PrincipalFrom(ctx).Require(tokens.ScopeRead, ""); err != nil {
				return nil, err
			}
			l, err := a.deps.Tokens.People(ctx)
			return &struct{ Body []*tokens.Person }{l}, err
		}))

	huma.Register(api, op("person-add", http.MethodPost, "/v1/people", "people add", RiskWrite, "Invite a person",
		"Adds a person with a role and returns a one-time sign-in link (valid 7 days) to send them. "+
			"Roles: admin (everything but the owner), member (plan and apply reversible and outbound changes), viewer (read only).", "people"),
		wrap(func(ctx context.Context, in *struct {
			Body struct {
				Name  string `json:"name" minLength:"1" maxLength:"64"`
				Email string `json:"email,omitempty" maxLength:"254"`
				Role  string `json:"role" enum:"admin,member,viewer"`
			}
		}) (*struct{ Body *Invite }, error) {
			p := PrincipalFrom(ctx)
			person, err := a.deps.Tokens.AddPerson(ctx, p, in.Body.Name, in.Body.Email, in.Body.Role)
			if err != nil {
				return nil, err
			}
			inv, err := link(ctx, p, person)
			return &struct{ Body *Invite }{inv}, err
		}))

	huma.Register(api, op("person-update", http.MethodPatch, "/v1/people/{id}", "people update", RiskWrite, "Change a person",
		"Renames someone or changes their role. Their open sessions end so the new role applies at once.", "people"),
		wrap(func(ctx context.Context, in *struct {
			ID   string `path:"id" maxLength:"40"`
			Body struct {
				Name string `json:"name,omitempty" maxLength:"64"`
				Role string `json:"role,omitempty" enum:"admin,member,viewer,"`
			}
		}) (*struct{ Body *tokens.Person }, error) {
			p, err := a.deps.Tokens.UpdatePerson(ctx, PrincipalFrom(ctx), in.ID, in.Body.Name, in.Body.Role)
			return &struct{ Body *tokens.Person }{p}, peopleErr(err)
		}))

	huma.Register(api, op("person-remove", http.MethodDelete, "/v1/people/{id}", "people remove", RiskDestructive, "Remove a person",
		"Removes someone's access and ends their sessions. Their past changes stay in the log.", "people"),
		wrap(func(ctx context.Context, in *struct {
			ID string `path:"id" maxLength:"40"`
		}) (*struct{}, error) {
			return &struct{}{}, peopleErr(a.deps.Tokens.RemovePerson(ctx, PrincipalFrom(ctx), in.ID))
		}))

	huma.Register(api, op("person-login-link", http.MethodPost, "/v1/people/{id}/login-link", "people login-link", RiskWrite, "Make a sign-in link",
		"A fresh one-time sign-in link for someone (for example if their invite expired).", "people"),
		wrap(func(ctx context.Context, in *struct {
			ID string `path:"id" maxLength:"40"`
		}) (*struct{ Body *Invite }, error) {
			p := PrincipalFrom(ctx)
			person, err := a.deps.Tokens.GetPerson(ctx, in.ID)
			if err != nil {
				return nil, peopleErr(err)
			}
			inv, err := link(ctx, p, person)
			return &struct{ Body *Invite }{inv}, err
		}))
}
