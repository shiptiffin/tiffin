package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
)

// Dashboard sessions: where each person is signed in, and signing them out.
// The model is in internal/tokens/sessions.go.

// Browser is one browser the box recognises for a person: signing in from it
// sends no new sign-in notice.
type Browser struct {
	Device    string    `json:"device" doc:"The browser and system, in words: \"Chrome on macOS\""`
	FirstSeen time.Time `json:"firstSeen" doc:"Its first sign-in"`
	LastSeen  time.Time `json:"lastSeen" doc:"Its latest sign-in"`
	Current   bool      `json:"current" doc:"The browser making this request"`
}

// SessionsEnded says how many sessions were signed out.
type SessionsEnded struct {
	Ended int `json:"ended" doc:"How many sessions were signed out"`
}

func sessionErr(err error) error {
	switch {
	case errors.Is(err, tokens.ErrSessionNotFound), errors.Is(err, tokens.ErrPersonNotFound):
		return NewProblem(404, "not_found", err.Error())
	}
	return err
}

func (a *API) registerSessions() {
	api := a.api
	huma.Register(api, op("sessions-list", http.MethodGet, "/v1/sessions", "sessions list", RiskRead, "List sign-in sessions",
		"Where a person is signed in to the dashboard: each session's browser, how it signed in (link, email, passkey, google, github), "+
			"when, from which address and country, and when it was last used. current marks the session making the request. "+
			"Yours by default; owners and admins can name anyone. With history=true, also sessions that ended or expired in the last 30 days. "+
			"API keys are not sessions: see keys list.", "sessions"),
		wrap(func(ctx context.Context, in *struct {
			Person  string `query:"person" maxLength:"40" doc:"Whose sessions; default yours"`
			History bool   `query:"history" doc:"Also list sessions that ended or expired in the last 30 days"`
		}) (*struct{ Body []*tokens.Session }, error) {
			l, err := a.deps.Tokens.Sessions(ctx, PrincipalFrom(ctx), in.Person, in.History)
			return &struct{ Body []*tokens.Session }{l}, sessionErr(err)
		}))

	huma.Register(api, op("session-end", http.MethodDelete, "/v1/sessions/{id}", "sessions end", RiskWrite, "Sign out a session",
		"Signs one session out at once: its next request is refused. API keys made in that session stop working too. "+
			"Your own sessions, or anyone's for owners and admins (only the owner can end the owner's).", "sessions"),
		wrap(func(ctx context.Context, in *struct {
			ID string `path:"id" maxLength:"40"`
		}) (*struct{ Body *tokens.Session }, error) {
			s, err := a.deps.Tokens.EndSession(ctx, PrincipalFrom(ctx), in.ID)
			return &struct{ Body *tokens.Session }{s}, sessionErr(err)
		}))

	huma.Register(api, op("sessions-end-others", http.MethodPost, "/v1/sessions/end-others", "sessions end-others", RiskWrite, "Sign out everywhere else",
		"Signs out every open session of a person except the one making the request, with the API keys those sessions made. "+
			"Yours by default; owners and admins can name anyone (all of that person's sessions end). Only the owner can end the owner's.", "sessions"),
		wrap(func(ctx context.Context, in *struct {
			Person string `query:"person" maxLength:"40" doc:"Whose sessions; default yours"`
		}) (*struct{ Body SessionsEnded }, error) {
			n, err := a.deps.Tokens.EndOtherSessions(ctx, PrincipalFrom(ctx), in.Person)
			return &struct{ Body SessionsEnded }{SessionsEnded{Ended: n}}, sessionErr(err)
		}))

	huma.Register(api, op("sessions-browsers", http.MethodGet, "/v1/sessions/browsers", "sessions browsers", RiskRead, "List recognised browsers",
		"The browsers (up to 20) a person has signed in from. Signing in from one of these sends no new sign-in notice; "+
			"from any other, the box emails them. Yours by default; owners and admins can name anyone.", "sessions"),
		wrap(func(ctx context.Context, in *struct {
			Person string `query:"person" maxLength:"40" doc:"Whose browsers; default yours"`
			Device string `cookie:"tiffin_device"`
		}) (*struct{ Body []Browser }, error) {
			p, err := a.deps.Tokens.SessionPerson(ctx, PrincipalFrom(ctx), in.Person, false)
			if err != nil {
				return nil, sessionErr(err)
			}
			out := []Browser{}
			if a.deps.DB == nil {
				return &struct{ Body []Browser }{out}, nil
			}
			var list []knownDevice
			if raw, ok, _ := a.deps.DB.KVGet(ctx, devicesNS, p.ID); ok {
				_ = json.Unmarshal(raw, &list)
			}
			mine := ""
			if deviceIDRE.MatchString(in.Device) {
				sum := sha256.Sum256([]byte(in.Device))
				mine = hex.EncodeToString(sum[:12])
			}
			for _, d := range list {
				out = append(out, Browser{Device: d.Label, FirstSeen: d.First, LastSeen: d.Last, Current: d.Hash == mine})
			}
			sort.SliceStable(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
			return &struct{ Body []Browser }{out}, nil
		}))
}
