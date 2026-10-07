package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
)

// Stats are a project's auth numbers.
type Stats struct {
	Users          int `json:"users"`
	VerifiedUsers  int `json:"verifiedUsers" doc:"Users whose email is confirmed"`
	BannedUsers    int `json:"bannedUsers"`
	Signups7d      int `json:"signups7d" doc:"Sign-ups in the last 7 days"`
	ActiveSessions int `json:"activeSessions" doc:"Sessions that haven't expired"`
	Organizations  int `json:"organizations"`
}

// Overview is a project's auth at a glance.
type Overview struct {
	Project       string          `json:"project"`
	Methods       []string        `json:"methods" doc:"Sign-in methods turned on in tiffin.config.ts"`
	Organizations bool            `json:"organizations" doc:"Whether teams (organizations) are on"`
	Endpoint      string          `json:"endpoint" doc:"Public base URL of the auth endpoint on the primary app host (TIFFIN_AUTH_URL)"`
	Hosts         []string        `json:"hosts" doc:"Every app host that serves /api/auth"`
	Social        map[string]bool `json:"social" doc:"For each sign-in provider turned on: whether it has keys (the project's own or the box-wide ones)"`
	// Providers says, for every sign-in provider, whether it is on and
	// where its keys come from.
	Providers []ProviderState `json:"providers"`
	// EmailVerification is the setting in effect and why.
	EmailVerification EmailVerificationState `json:"emailVerification"`
	// EmailBlocked: the box can't send email yet, so in production email
	// sign-up, magic links, one-time codes and resets are refused.
	EmailBlocked bool  `json:"emailBlocked" doc:"The box can't send email yet: in production, email + password sign-up, magic links, one-time codes and password resets are refused (EMAIL_NOT_SET_UP) until a mail service is connected. Previews use the dev inbox"`
	Stats        Stats `json:"stats"`
}

// ProviderState is one sign-in provider as a project sees it.
type ProviderState struct {
	ID   string `json:"id" doc:"Better Auth's provider ID, also the manifest method"`
	Name string `json:"name"`
	On   bool   `json:"on" doc:"Turned on in services.auth.methods"`
	Keys string `json:"keys" enum:"project,box,none" doc:"Where its keys come from: project (the project's own secrets, which win), box (the box-wide keys) or none (not set up: the button explains it)"`
	// BoxKeys says whether the box has keys for it, so turning it on works at once.
	BoxKeys bool `json:"boxKeys"`
	// Env is the prefix of the project's own secrets: <Env>_CLIENT_ID and <Env>_CLIENT_SECRET.
	Env string `json:"env"`
	// CallbackURL is the redirect URI the provider needs for the keys in use:
	// the box's one URL for box-wide keys, else the app's own (AppCallbackURL).
	CallbackURL string `json:"callbackUrl"`
	// AppCallbackURL is the one redirect URI for this app's own keys, on its
	// sign-in host; sign-ins on its other hosts and previews come back through it.
	AppCallbackURL string `json:"appCallbackUrl"`
	// CallbackConfirmed is the redirect URI last confirmed as registered with
	// the provider for the app's own keys ("" if none yet).
	CallbackConfirmed string `json:"callbackConfirmed"`
	// CallbackChanged: the app's own keys are in use and AppCallbackURL is no
	// longer the one confirmed (its sign-in host changed): update it with the provider.
	CallbackChanged bool `json:"callbackChanged"`
	// BoxConsentName is the name the provider shows with the box-wide keys, when the box owner noted it.
	BoxConsentName string `json:"boxConsentName,omitempty"`
	// TestURL starts a sign-in with this provider on the app's sign-in host and says who signed in.
	TestURL string `json:"testUrl"`
}

// EmailVerificationState says whether new users must confirm their address.
type EmailVerificationState struct {
	Required bool   `json:"required" doc:"New users confirm their email address before they can sign in"`
	Source   string `json:"source" enum:"manifest,relay,no-relay,no-email" doc:"manifest: auth.emailVerification sets it. Otherwise automatic: relay (on: mail leaves the box), no-relay (off: mail only reaches the dev inbox), no-email (off: the project has no email service)"`
}

// User is one account.
type User struct {
	ID               string     `json:"id"`
	Email            string     `json:"email"`
	Name             string     `json:"name"`
	Image            *string    `json:"image"`
	EmailVerified    bool       `json:"emailVerified"`
	Banned           bool       `json:"banned"`
	BanReason        *string    `json:"banReason"`
	BanExpires       *time.Time `json:"banExpires"`
	TwoFactorEnabled *bool      `json:"twoFactorEnabled,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
	LastSeenAt       *time.Time `json:"lastSeenAt,omitempty" doc:"Most recent session activity"`
}

// UserList is one page of users.
type UserList struct {
	Users  []User `json:"users"`
	Total  int    `json:"total" doc:"Users matching the search"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}

// Account is a way a user signs in (credential = email + password).
type Account struct {
	ProviderID string    `json:"providerId" doc:"credential, google, github, ..."`
	AccountID  string    `json:"accountId"`
	CreatedAt  time.Time `json:"createdAt"`
}

// Session is a signed-in device.
type Session struct {
	ID        string    `json:"id"`
	IPAddress *string   `json:"ipAddress"`
	UserAgent *string   `json:"userAgent"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Membership is a user's role in an organization.
type Membership struct {
	OrganizationID string    `json:"organizationId"`
	Name           string    `json:"name"`
	Slug           string    `json:"slug"`
	Role           string    `json:"role" doc:"owner, admin, member or viewer"`
	CreatedAt      time.Time `json:"createdAt"`
}

// APIKey is a key a user minted (the secret is never shown).
type APIKey struct {
	ID          string          `json:"id"`
	Name        *string         `json:"name"`
	Start       *string         `json:"start" doc:"First characters, to recognise it"`
	Prefix      *string         `json:"prefix"`
	Enabled     bool            `json:"enabled"`
	ExpiresAt   *time.Time      `json:"expiresAt"`
	CreatedAt   time.Time       `json:"createdAt"`
	LastRequest *time.Time      `json:"lastRequest"`
	Metadata    json.RawMessage `json:"metadata,omitempty" doc:"Includes maxRole when the key is capped below its owner's role"`
}

// UserDetail is everything about one user.
type UserDetail struct {
	User        User         `json:"user"`
	Accounts    []Account    `json:"accounts"`
	Sessions    []Session    `json:"sessions"`
	Memberships []Membership `json:"memberships"`
	Passkeys    int          `json:"passkeys"`
	APIKeys     []APIKey     `json:"apiKeys"`
}

// Org is one organization.
type Org struct {
	ID                 string          `json:"id"`
	Name               string          `json:"name"`
	Slug               string          `json:"slug"`
	Logo               *string         `json:"logo"`
	CreatedAt          time.Time       `json:"createdAt"`
	Metadata           json.RawMessage `json:"metadata,omitempty" doc:"{\"personal\":true} for each person's own org"`
	MemberCount        *int            `json:"memberCount,omitempty"`
	PendingInvitations *int            `json:"pendingInvitations,omitempty"`
}

// OrgList is one page of organizations.
type OrgList struct {
	Organizations []Org `json:"organizations"`
	Total         int   `json:"total"`
	Limit         int   `json:"limit"`
	Offset        int   `json:"offset"`
}

// Member is a person in an organization.
type Member struct {
	ID        string    `json:"id"`
	UserID    string    `json:"userId"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"createdAt"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Image     *string   `json:"image"`
}

// Invitation is an email invite.
type Invitation struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	Status    string    `json:"status" doc:"pending, accepted, rejected or canceled"`
	ExpiresAt time.Time `json:"expiresAt"`
	InviterID string    `json:"inviterId"`
	CreatedAt time.Time `json:"createdAt"`
}

// InviteLink is a shareable invite link (the token is never shown again).
type InviteLink struct {
	ID        string     `json:"id"`
	Role      string     `json:"role"`
	MaxUses   int        `json:"maxUses"`
	Uses      int        `json:"uses"`
	ExpiresAt time.Time  `json:"expiresAt"`
	CreatedBy string     `json:"createdBy"`
	CreatedAt time.Time  `json:"createdAt"`
	RevokedAt *time.Time `json:"revokedAt"`
}

// OrgDetail is one organization with its people and invites.
type OrgDetail struct {
	Organization Org          `json:"organization"`
	Members      []Member     `json:"members"`
	Invitations  []Invitation `json:"invitations"`
	InviteLinks  []InviteLink `json:"inviteLinks"`
}

// BanResult is the outcome of a ban.
type BanResult struct {
	Banned          bool `json:"banned"`
	SessionsRevoked int  `json:"sessionsRevoked,omitempty"`
}

// RevokeResult is the outcome of signing a user out everywhere.
type RevokeResult struct {
	SessionsRevoked int `json:"sessionsRevoked"`
}

type projectIn struct {
	Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
}

type pageIn struct {
	Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
	Search  string `query:"search" maxLength:"200" doc:"Match email or name (users), name or slug (orgs), or an exact id"`
	Limit   int    `query:"limit" minimum:"1" maximum:"200" default:"50"`
	Offset  int    `query:"offset" minimum:"0" default:"0"`
}

type userIn struct {
	Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
	ID      string `path:"id" pattern:"^[A-Za-z0-9_-]{1,64}$" doc:"User ID"`
}

const tag = "auth"

// RegisterAPI adds the Auth pages' operations: users, organizations and
// account actions. App sign-in itself happens at /api/auth on app hosts.
func (m *Module) RegisterAPI(a huma.API, p *platform.Platform) {
	m.registerProviders(a, p)
	m.registerAppKeys(a, p)

	get := api.Op("auth-get", http.MethodGet, "/v1/projects/{project}/auth", "auth show", api.RiskRead,
		"Show a project's auth", "Sign-in methods, the endpoint URL, whether OAuth apps are set up, and user, session and organization counts.", tag)
	get.Errors = append(get.Errors, 404, 503)
	huma.Register(a, get, api.Wrap(func(ctx context.Context, in *projectIn) (*struct{ Body *Overview }, error) {
		res, err := authProject(ctx, p, in.Project, tokens.ScopeRead)
		if err != nil {
			return nil, err
		}
		o := overview(p, in.Project, res)
		if err := call(ctx, http.MethodGet, in.Project, "/stats", nil, nil, &o.Stats); err != nil {
			return nil, err
		}
		return &struct{ Body *Overview }{o}, nil
	}))

	ul := api.Op("auth-users-list", http.MethodGet, "/v1/projects/{project}/auth/users", "auth users list", api.RiskRead,
		"List a project's users", "Newest first. Search matches email or name (case-insensitive) or an exact user ID.", tag)
	ul.Errors = append(ul.Errors, 404, 503)
	huma.Register(a, api.Untrusted(ul), api.Wrap(func(ctx context.Context, in *pageIn) (*struct{ Body *UserList }, error) {
		if _, err := authProject(ctx, p, in.Project, tokens.ScopeRead); err != nil {
			return nil, err
		}
		var out UserList
		return &struct{ Body *UserList }{&out}, call(ctx, http.MethodGet, in.Project, "/users", pageQuery(in), nil, &out)
	}))

	ug := api.Op("auth-user-get", http.MethodGet, "/v1/projects/{project}/auth/users/{id}", "auth users get", api.RiskRead,
		"Show one user", "A user with how they sign in, their signed-in sessions, organizations, passkey count and API keys (never secrets).", tag)
	ug.Errors = append(ug.Errors, 404, 503)
	huma.Register(a, api.Untrusted(ug), api.Wrap(func(ctx context.Context, in *userIn) (*struct{ Body *UserDetail }, error) {
		if _, err := authProject(ctx, p, in.Project, tokens.ScopeRead); err != nil {
			return nil, err
		}
		var out UserDetail
		return &struct{ Body *UserDetail }{&out}, call(ctx, http.MethodGet, in.Project, "/users/"+url.PathEscape(in.ID), nil, nil, &out)
	}))

	ban := api.Op("auth-user-ban", http.MethodPost, "/v1/projects/{project}/auth/users/{id}/ban", "auth users ban", api.RiskWrite,
		"Suspend a user", "Signs the user out everywhere and blocks new sign-ins and their API keys until unbanned (or until expiresAt). Undo with auth users unban.", tag)
	ban.Errors = append(ban.Errors, 404, 503)
	huma.Register(a, ban, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		ID      string `path:"id" pattern:"^[A-Za-z0-9_-]{1,64}$" doc:"User ID"`
		Body    struct {
			Reason    string     `json:"reason,omitempty" maxLength:"500" doc:"Why, for your records"`
			ExpiresAt *time.Time `json:"expiresAt,omitempty" doc:"Lift the ban automatically at this time"`
		}
	}) (*struct{ Body *BanResult }, error) {
		if _, err := authProject(ctx, p, in.Project, tokens.ScopeApplyReversible); err != nil {
			return nil, err
		}
		var out BanResult
		if err := call(ctx, http.MethodPost, in.Project, "/users/"+url.PathEscape(in.ID)+"/ban", nil, in.Body, &out); err != nil {
			return nil, err
		}
		audit(ctx, p, "auth.user_ban", in.Project, map[string]any{"user": in.ID, "reason": in.Body.Reason, "expiresAt": in.Body.ExpiresAt})
		return &struct{ Body *BanResult }{&out}, nil
	}))

	unban := api.Op("auth-user-unban", http.MethodPost, "/v1/projects/{project}/auth/users/{id}/unban", "auth users unban", api.RiskWrite,
		"Lift a user's suspension", "The user can sign in again.", tag)
	unban.Errors = append(unban.Errors, 404, 503)
	huma.Register(a, unban, api.Wrap(func(ctx context.Context, in *userIn) (*struct{ Body *BanResult }, error) {
		if _, err := authProject(ctx, p, in.Project, tokens.ScopeApplyReversible); err != nil {
			return nil, err
		}
		var out BanResult
		if err := call(ctx, http.MethodPost, in.Project, "/users/"+url.PathEscape(in.ID)+"/unban", nil, nil, &out); err != nil {
			return nil, err
		}
		audit(ctx, p, "auth.user_unban", in.Project, map[string]any{"user": in.ID})
		return &struct{ Body *BanResult }{&out}, nil
	}))

	rv := api.Op("auth-user-sessions-revoke", http.MethodPost, "/v1/projects/{project}/auth/users/{id}/sessions/revoke", "auth users signout", api.RiskWrite,
		"Sign a user out everywhere", "Ends every session of the user. They can sign in again; their API keys keep working (ban them to stop those).", tag)
	rv.Errors = append(rv.Errors, 404, 503)
	huma.Register(a, rv, api.Wrap(func(ctx context.Context, in *userIn) (*struct{ Body *RevokeResult }, error) {
		if _, err := authProject(ctx, p, in.Project, tokens.ScopeApplyReversible); err != nil {
			return nil, err
		}
		var out RevokeResult
		if err := call(ctx, http.MethodPost, in.Project, "/users/"+url.PathEscape(in.ID)+"/revoke-sessions", nil, nil, &out); err != nil {
			return nil, err
		}
		audit(ctx, p, "auth.user_signout", in.Project, map[string]any{"user": in.ID, "sessions": out.SessionsRevoked})
		return &struct{ Body *RevokeResult }{&out}, nil
	}))

	ol := api.Op("auth-orgs-list", http.MethodGet, "/v1/projects/{project}/auth/orgs", "auth orgs list", api.RiskRead,
		"List a project's organizations", "Newest first, with member and pending-invitation counts. Each person's own org has metadata {\"personal\":true}.", tag)
	ol.Errors = append(ol.Errors, 404, 503)
	huma.Register(a, api.Untrusted(ol), api.Wrap(func(ctx context.Context, in *pageIn) (*struct{ Body *OrgList }, error) {
		if _, err := authProject(ctx, p, in.Project, tokens.ScopeRead); err != nil {
			return nil, err
		}
		var out OrgList
		return &struct{ Body *OrgList }{&out}, call(ctx, http.MethodGet, in.Project, "/orgs", pageQuery(in), nil, &out)
	}))

	og := api.Op("auth-org-get", http.MethodGet, "/v1/projects/{project}/auth/orgs/{id}", "auth orgs get", api.RiskRead,
		"Show one organization", "Members with roles, email invitations and invite links (tokens are never shown).", tag)
	og.Errors = append(og.Errors, 404, 503)
	huma.Register(a, api.Untrusted(og), api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		ID      string `path:"id" pattern:"^[A-Za-z0-9_-]{1,64}$" doc:"Organization ID"`
	}) (*struct{ Body *OrgDetail }, error) {
		if _, err := authProject(ctx, p, in.Project, tokens.ScopeRead); err != nil {
			return nil, err
		}
		var out OrgDetail
		return &struct{ Body *OrgDetail }{&out}, call(ctx, http.MethodGet, in.Project, "/orgs/"+url.PathEscape(in.ID), nil, nil, &out)
	}))
}

// authProject authorizes the caller and loads a project that has auth.
func authProject(ctx context.Context, p *platform.Platform, project string, scope tokens.Scope) (map[string]change.Resource, error) {
	if err := api.PrincipalFrom(ctx).Require(scope, project); err != nil {
		return nil, err
	}
	if p == nil || p.DB == nil {
		return nil, api.NewProblem(501, "internal", "auth runs on the box; point the CLI at a box (tiffin up)")
	}
	v, res, err := p.DB.Load(ctx, project)
	if err != nil {
		return nil, err
	}
	if v == 0 {
		return nil, api.NewProblem(404, "not_found", "project "+project+" does not exist")
	}
	if !hasAuth(res) {
		return nil, api.NewProblem(404, "not_found", "auth isn't turned on for project "+project+": add `auth: {}` to services in tiffin.config.ts and apply")
	}
	return res, nil
}

func overview(p *platform.Platform, project string, res map[string]change.Resource) *Overview {
	var a manifest.Auth
	_ = json.Unmarshal(res[change.KindService+"/auth"].Spec, &a)
	if len(a.Methods) == 0 {
		a.Methods = manifest.DefaultAuthMethods
	}
	o := &Overview{Project: project, Methods: a.Methods, Organizations: a.Organizations, Hosts: []string{}, Social: map[string]bool{}}
	for _, h := range webHosts(p, res) {
		o.Hosts = append(o.Hosts, h.Host)
	}
	primary := p.Host(project)
	if len(o.Hosts) > 0 {
		primary = o.Hosts[0]
	}
	o.Endpoint = p.URL(primary) + PathPrefix
	_, hasEmail := res[change.KindService+"/email"]
	o.EmailVerification.Required, o.EmailVerification.Source = EmailVerification(context.Background(), p, project, &a, hasEmail)
	o.EmailBlocked = EmailBlocked(context.Background(), p, project, hasEmail)
	all := make([]string, 0, len(Providers))
	for _, prov := range Providers {
		all = append(all, prov.ID)
	}
	src, err := KeySources(context.Background(), p, project, all)
	if err != nil && p.Log != nil {
		p.Log.Warn("auth: sign-in provider keys", "project", project, "err", err)
	}
	var box map[string]*BoxProvider
	if p.Secrets != nil {
		box, _ = boxProviders(context.Background(), p)
	}
	o.Providers = []ProviderState{}
	for _, prov := range Providers {
		st := ProviderState{ID: prov.ID, Name: prov.Name, On: contains(a.Methods, prov.ID), Keys: KeysNone, BoxKeys: box[prov.ID] != nil, Env: prov.Env}
		if k := src[prov.ID]; k != "" {
			st.Keys = k
		}
		st.AppCallbackURL = AppCallbackURL(p, project, res, prov.ID)
		st.CallbackURL = st.AppCallbackURL
		st.TestURL = p.URL(signInHost(p, project, res)) + PathPrefix + "/tiffin/test-sign-in?provider=" + prov.ID
		if b := box[prov.ID]; b != nil {
			st.BoxConsentName = b.ConsentName
		}
		if st.Keys == KeysBox {
			st.CallbackURL = CallbackURL(p, prov.ID)
		}
		if st.Keys == KeysProject && p.DB != nil {
			st.CallbackConfirmed = confirmedCallback(context.Background(), p, project, prov.ID)
			if st.CallbackConfirmed == "" {
				// Keys set some other way (CLI, Environment Variables): take the
				// address as it is now, so a later change shows.
				_ = confirmCallback(context.Background(), p, project, prov.ID, st.AppCallbackURL)
				st.CallbackConfirmed = st.AppCallbackURL
			}
			st.CallbackChanged = st.CallbackConfirmed != st.AppCallbackURL
		}
		if st.On {
			o.Social[prov.ID] = st.Keys != KeysNone
		}
		o.Providers = append(o.Providers, st)
	}
	return o
}

func pageQuery(in *pageIn) url.Values {
	q := url.Values{"limit": {strconv.Itoa(max(in.Limit, 1))}, "offset": {strconv.Itoa(in.Offset)}}
	if in.Search != "" {
		q.Set("search", in.Search)
	}
	return q
}

// call runs an engine admin request for a project, mapping engine errors to problems.
func call(ctx context.Context, method, project, path string, q url.Values, body, out any) error {
	err := defaultEngine.do(ctx, method, "/projects/"+url.PathEscape(project)+path, q, body, out)
	if err == nil {
		return nil
	}
	var ee *EngineError
	switch {
	case errors.Is(err, ErrEngineDown):
		pr := api.NewProblem(503, "internal", err.Error())
		return pr
	case errors.As(err, &ee) && ee.Status == 404:
		return api.NewProblem(404, "not_found", ee.Message)
	case errors.As(err, &ee) && ee.Status == 422:
		return api.NewProblem(422, "validation", ee.Message)
	}
	return err
}

func audit(ctx context.Context, p *platform.Platform, action, project string, detail map[string]any) {
	pr := api.PrincipalFrom(ctx)
	if pr == nil || p == nil || p.DB == nil {
		return
	}
	detail["project"] = project
	if pr.Session != "" {
		detail["session"] = pr.Session
	}
	_ = p.DB.Audit(ctx, pr.TokenID, action, project, detail)
}
