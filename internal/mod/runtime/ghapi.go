package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/mod/runtime/ghapp"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

// GitHubStatus is the box's connection to GitHub.
type GitHubStatus struct {
	Connected     bool                 `json:"connected" doc:"The box has a working GitHub App"`
	Source        string               `json:"source,omitempty" enum:"box,configured,file," doc:"box: an app this box created (Connect GitHub). configured: an existing app given to the box (github use-app). file: set by the box's operator in its settings file"`
	Shared        bool                 `json:"shared" doc:"A public app other GitHub accounts install too: the box only acts for installations made from this box"`
	App           *GitHubAppInfo       `json:"app,omitempty"`
	Installations []GitHubInstallation `json:"installations" doc:"Accounts the app is installed on, that this box acts for"`
	Problem       string               `json:"problem,omitempty" doc:"Why the app does not work right now"`
	WebhookURL    string               `json:"webhookUrl" doc:"Where GitHub delivers events"`
	Reachable     bool                 `json:"reachable" doc:"GitHub can deliver to the box's address"`
	ReachableHint string               `json:"reachableHint,omitempty"`
	GitHubURL     string               `json:"githubUrl" doc:"The GitHub this box talks to"`
	CanManage     bool                 `json:"canManage" doc:"The caller can connect, install and disconnect (box admins)"`
	Events        []GitHubEvent        `json:"events" doc:"What the box did because of GitHub, newest first"`
}

// GitHubAppInfo is the box's GitHub App.
type GitHubAppInfo struct {
	ID          int64      `json:"id"`
	Slug        string     `json:"slug"`
	Name        string     `json:"name"`
	Owner       string     `json:"owner,omitempty"`
	HTMLURL     string     `json:"htmlUrl,omitempty"`
	SettingsURL string     `json:"settingsUrl,omitempty" doc:"Where to manage or delete the app on GitHub"`
	CreatedAt   *time.Time `json:"createdAt,omitempty"`
}

// GitHubInstallation is the app installed on one account.
type GitHubInstallation struct {
	ID           int64  `json:"id"`
	Account      string `json:"account"`
	AccountType  string `json:"accountType" doc:"User or Organization"`
	Repositories string `json:"repositories" enum:"all,selected" doc:"Every repository of the account, or the ones picked"`
	SettingsURL  string `json:"settingsUrl,omitempty" doc:"Where to change which repositories it reaches"`
	Suspended    bool   `json:"suspended,omitempty"`
}

// GitHubConnectStart is how the browser creates the box's app on GitHub.
type GitHubConnectStart struct {
	URL      string `json:"url" doc:"POST a form here from the browser, with one field, manifest"`
	Manifest string `json:"manifest" doc:"The app manifest (JSON) for the form's manifest field"`
	Name     string `json:"name" doc:"The app's name (the owner can change it on GitHub)"`
}

// GitHubLink is a page on GitHub to open in the browser.
type GitHubLink struct {
	URL string `json:"url"`
}

// GitHubRepo is a repository the box can deploy.
type GitHubRepo struct {
	FullName      string    `json:"fullName" example:"acme/shop"`
	Owner         string    `json:"owner"`
	Name          string    `json:"name"`
	Private       bool      `json:"private"`
	Fork          bool      `json:"fork,omitempty"`
	Archived      bool      `json:"archived,omitempty"`
	Description   string    `json:"description,omitempty"`
	DefaultBranch string    `json:"defaultBranch"`
	PushedAt      time.Time `json:"pushedAt"`
	URL           string    `json:"url"`
	Installation  int64     `json:"installation"`
	Connected     []string  `json:"connected" doc:"project/app already deploying from it"`
}

// GitHubRepoList is the repositories the box's app reaches.
type GitHubRepoList struct {
	Repos         []GitHubRepo `json:"repos"`
	Installations int          `json:"installations" doc:"Accounts searched"`
	Total         int          `json:"total" doc:"Repositories before the search filter and limit"`
}

// GitHubCommit is a commit's head line.
type GitHubCommit struct {
	SHA     string `json:"sha"`
	Message string `json:"message"`
	Author  string `json:"author"`
}

// RepoRoot is a folder of a repository that looks like an app.
type RepoRoot struct {
	Path      string `json:"path" doc:"Folder inside the repository; empty for the top"`
	Framework string `json:"framework" enum:"next,hono,bun,static,fastapi,python" doc:"How the box would build it"`
	// Preset is the framework as people know it, for a picker to show and
	// let them override (Vercel's Framework Preset).
	Preset    string `json:"preset,omitempty" example:"astro" doc:"The framework as people know it, with the same ids as templates list: nextjs, tanstack-start, astro, vite-react, vite, hono, html; empty for any other server or static build"`
	Name      string `json:"name,omitempty" doc:"The package name, if any"`
	Why       string `json:"why" doc:"What the guess is based on, in plain words"`
	Workspace bool   `json:"workspace,omitempty" doc:"A monorepo's top: its apps are in the folders below"`
	// Builder is "dockerfile" for a folder the box would build with its
	// Dockerfile (it has nothing else the box knows how to build).
	Builder string `json:"builder,omitempty" enum:"dockerfile," doc:"dockerfile: the folder has a Dockerfile and nothing else the box builds, so it builds with the Dockerfile (builder \"dockerfile\")"`
	// Unsupported names a framework the box can't run yet (SvelteKit,
	// Nuxt…): it needs a server the box doesn't set up for it, and serving
	// its build as files would fail. Framework is only a placeholder then.
	Unsupported string `json:"unsupported,omitempty" doc:"A framework the box can't run yet (e.g. SvelteKit); import a supported app instead"`
}

// GitHubRepoDetail is what the box sees in a repository, for importing it.
type GitHubRepoDetail struct {
	FullName      string        `json:"fullName"`
	Private       bool          `json:"private"`
	DefaultBranch string        `json:"defaultBranch"`
	Branch        string        `json:"branch" doc:"The branch looked at"`
	Branches      []string      `json:"branches"`
	Commit        *GitHubCommit `json:"commit,omitempty" doc:"The branch's latest commit"`
	Roots         []RepoRoot    `json:"roots" doc:"Folders that look like apps, the most likely first"`
	Suggested     string        `json:"suggested" doc:"The folder to deploy, if unsure"`
	Truncated     bool          `json:"truncated,omitempty" doc:"The repository is very large; only part of it was looked at"`
	Folders       []string      `json:"folders" doc:"The repository's folders (up to 4 deep, at most 500; build output, dependencies and hidden folders left out), for picking the app's folder by hand when detection gets it wrong"`
	Connected     []string      `json:"connected" doc:"project/app already deploying from it"`
}

type ghConnectBody struct {
	Org  string `json:"org,omitempty" maxLength:"39" doc:"Create the app in this GitHub organization instead of your personal account"`
	Name string `json:"name,omitempty" maxLength:"34" doc:"The app's name on GitHub (unique across GitHub). Default tiffin-<box>"`
}

type ghUseAppBody struct {
	AppID         int64  `json:"appId" minimum:"1" doc:"The GitHub App's id"`
	PrivateKey    string `json:"privateKey" minLength:"100" maxLength:"16384" doc:"The app's private key (PEM)"`
	WebhookSecret string `json:"webhookSecret" minLength:"8" maxLength:"256" doc:"The app's webhook secret"`
	ClientID      string `json:"clientId,omitempty" maxLength:"64" doc:"The app's client id (needed for a public app)"`
	ClientSecret  string `json:"clientSecret,omitempty" maxLength:"256" doc:"The app's client secret (needed for a public app)"`
	Public        bool   `json:"public,omitempty" doc:"Other GitHub accounts install this app too (a shared, public app). The box then only acts for installations its admin makes from the box, proven with GitHub sign-in during the install (enable 'Request user authorization (OAuth) during installation' on the app, with <box>/v1/github/setup as its callback URL)."`
}

type ghDeployBody struct {
	Ref string `json:"ref,omitempty" maxLength:"200" doc:"Branch, tag or commit SHA to deploy. Default: the app's production branch (git.branch)"`
}

// requireAdmin refuses callers that are not box admins.
func requireAdmin(ctx context.Context) error {
	if pr := api.PrincipalFrom(ctx); pr == nil || !pr.BoxAdmin() {
		return problem(403, "forbidden", "connecting GitHub needs a box admin (a key with full access to all projects, or an owner or admin person)", "")
	}
	return nil
}

func (m *Module) registerGitHubOps(a huma.API) {
	st := api.Outbound(api.Op("github-status", http.MethodGet, "/v1/github", "github status", api.RiskRead, "Show the GitHub connection",
		"Whether the box is connected to GitHub (its GitHub App), the accounts the app is installed on, the webhook address, "+
			"whether GitHub can reach the box, and the last things the box did because of GitHub (pushes deployed, previews made or removed).", "github"))
	huma.Register(a, st, api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body GitHubStatus }, error) {
		r, err := m.rt()
		if err != nil {
			return nil, unavailable(err)
		}
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeRead, ""); err != nil {
			return nil, err
		}
		out := r.githubStatus(ctx)
		out.CanManage = pr.BoxAdmin()
		return &struct{ Body GitHubStatus }{out}, nil
	}))

	cn := api.Op("github-connect", http.MethodPost, "/v1/github/connect", "github connect", api.RiskWrite, "Start connecting GitHub",
		"Starts creating the box's own GitHub App (the manifest flow): returns a GitHub address and an app manifest that a browser "+
			"POSTs there as a form field named manifest. The owner confirms on GitHub, GitHub sends them back to the box, the box keeps "+
			"the app's key sealed, and they go on to pick repositories. It needs a person in a browser; agents should ask the owner to "+
			"click Connect GitHub in Settings › Git. Connecting again replaces the app. Box admins only.", "github")
	cn.Errors = append(cn.Errors, 409)
	huma.Register(a, cn, api.Wrap(func(ctx context.Context, in *struct{ Body ghConnectBody }) (*struct{ Body GitHubConnectStart }, error) {
		r, err := m.rt()
		if err != nil {
			return nil, unavailable(err)
		}
		if err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		s, _, err := r.loadSettings()
		if err != nil {
			return nil, problem(409, "precondition", err.Error(), "Fix the box's GitHub settings file.")
		}
		if s.App != nil {
			return nil, problem(409, "precondition", "this box uses a GitHub App set by its operator", "Install it on your repositories instead: github install.")
		}
		if ok, why := r.reachable(s.Endpoints); !ok {
			return nil, problem(409, "precondition", why, "Set a public domain for the box, then connect GitHub.")
		}
		if in.Body.Org != "" && !ghapp.ValidOwner(in.Body.Org) {
			return nil, problem(422, "validation", "org must be a GitHub organization login", "")
		}
		name := strings.TrimSpace(in.Body.Name)
		if name == "" {
			name = r.appName()
		}
		state, err := r.issueState(ctx, ghPending{Kind: "manifest", By: api.PrincipalFrom(ctx).TokenID, Org: in.Body.Org})
		if err != nil {
			return nil, err
		}
		raw, _ := json.Marshal(ghapp.NewManifest(name, r.ghURLs()))
		_ = r.p.DB.Audit(ctx, api.PrincipalFrom(ctx).TokenID, "github.connect", "github", map[string]any{"org": in.Body.Org, "name": name})
		return &struct{ Body GitHubConnectStart }{GitHubConnectStart{URL: s.NewAppURL(in.Body.Org, state), Manifest: string(raw), Name: name}}, nil
	}))

	ins := api.Op("github-install", http.MethodPost, "/v1/github/install", "github install", api.RiskWrite, "Install the GitHub App on repositories",
		"Returns the GitHub page where the owner picks which accounts and repositories the box's app may reach (open it in a browser). "+
			"GitHub sends them back to the box afterwards. Box admins only.", "github")
	ins.Errors = append(ins.Errors, 409)
	huma.Register(a, ins, api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body GitHubLink }, error) {
		r, err := m.rt()
		if err != nil {
			return nil, unavailable(err)
		}
		if err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		c, err := r.github(ctx)
		if err != nil {
			return nil, r.toProblem(err, "GitHub App")
		}
		slug := c.Cred.Slug
		if slug == "" {
			info, err := c.App.Info(ctx)
			if err != nil {
				return nil, problem(502, "internal", "GitHub: "+err.Error(), "Check the app's id and key.")
			}
			slug = info.Slug
		}
		state, err := r.issueState(ctx, ghPending{Kind: "install", By: api.PrincipalFrom(ctx).TokenID})
		if err != nil {
			return nil, err
		}
		return &struct{ Body GitHubLink }{GitHubLink{URL: c.E.InstallURL(slug, state)}}, nil
	}))

	use := api.Outbound(api.Op("github-use-app", http.MethodPut, "/v1/github/app", "github use-app", api.RiskWrite, "Use an existing GitHub App",
		"Points the box at a GitHub App that already exists (a hosted service's shared app, or one you made by hand) instead of "+
			"creating its own: the app id, its private key and webhook secret (and, for a public app, its client id and secret). The box "+
			"checks the key with GitHub, then keeps everything sealed with its own key. The app's webhook must point at "+
			"<box>/v1/github/webhook. Box admins only.", "github"))
	use.Errors = append(use.Errors, 409)
	huma.Register(a, use, api.Wrap(func(ctx context.Context, in *struct{ Body ghUseAppBody }) (*struct{ Body GitHubStatus }, error) {
		r, err := m.rt()
		if err != nil {
			return nil, unavailable(err)
		}
		if err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		s, _, err := r.loadSettings()
		if err != nil {
			return nil, problem(409, "precondition", err.Error(), "")
		}
		if s.App != nil {
			return nil, problem(409, "precondition", "this box's GitHub App is set by its operator in "+r.ghSettingsPath(), "Change it there.")
		}
		b := in.Body
		if b.Public && (b.ClientID == "" || b.ClientSecret == "") {
			return nil, problem(422, "validation", "a public app needs its client id and client secret", "The box uses them to check who installs the app.")
		}
		cred := ghapp.Credentials{ID: b.AppID, PrivateKey: strings.TrimSpace(b.PrivateKey), WebhookSecret: b.WebhookSecret, ClientID: b.ClientID, ClientSecret: b.ClientSecret}
		app, err := r.ghClient(s.Endpoints).NewApp(cred)
		if err != nil {
			return nil, problem(422, "validation", err.Error(), "Paste the .pem file GitHub gave you when you generated the key.")
		}
		info, err := app.Info(ctx)
		if err != nil {
			return nil, problem(422, "validation", "GitHub did not accept the app id and key: "+err.Error(), "Check the app id, and that the key belongs to that app.")
		}
		cred.Slug, cred.Name, cred.Owner, cred.OwnerType, cred.HTMLURL, cred.CreatedAt = info.Slug, info.Name, info.Owner.Login, info.Owner.Type, info.HTMLURL, time.Now().UTC()
		if err := r.storeApp(ctx, ghStored{Source: ghSourceConfigured, Public: b.Public, Cred: cred}); err != nil {
			return nil, err
		}
		pr := api.PrincipalFrom(ctx)
		_ = r.p.DB.Audit(ctx, pr.TokenID, "github.use-app", "github", map[string]any{"app": info.Slug, "id": b.AppID, "public": b.Public})
		r.logEvent(ctx, GitHubEvent{Event: "connect", Summary: "Using the GitHub App " + info.Slug, OK: true})
		out := r.githubStatus(ctx)
		out.CanManage = true
		return &struct{ Body GitHubStatus }{out}, nil
	}))

	dc := api.Op("github-disconnect", http.MethodDelete, "/v1/github", "github disconnect", api.RiskDestructive, "Disconnect GitHub",
		"Forgets the box's GitHub App (its key and webhook secret): pushes stop deploying and previews stop. Apps keep their "+
			"repository settings, and what runs keeps running. The app itself stays on GitHub until you delete it there "+
			"(settingsUrl). Connecting again creates a new app. Box admins only.", "github")
	dc.Errors = append(dc.Errors, 409)
	huma.Register(a, dc, api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body GitHubLink }, error) {
		r, err := m.rt()
		if err != nil {
			return nil, unavailable(err)
		}
		if err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		s, _, _ := r.loadSettings()
		if s != nil && s.App != nil {
			return nil, problem(409, "precondition", "this box's GitHub App is set by its operator in "+r.ghSettingsPath(), "Remove it there.")
		}
		out := GitHubLink{}
		if c, err := r.github(ctx); err == nil {
			out.URL = c.E.AppSettingsURL(&c.Cred)
		}
		for _, k := range []string{"app", "bound"} {
			if err := r.p.DB.KVDelete(ctx, nsGitHub, k); err != nil {
				return nil, err
			}
		}
		r.gh.mu.Lock()
		r.gh.conn = nil
		r.gh.mu.Unlock()
		_ = r.p.DB.Audit(ctx, api.PrincipalFrom(ctx).TokenID, "github.disconnect", "github", nil)
		r.logEvent(ctx, GitHubEvent{Event: "disconnect", Summary: "Disconnected from GitHub", OK: true})
		return &struct{ Body GitHubLink }{out}, nil
	}))

	rl := api.Outbound(api.Op("github-repos", http.MethodGet, "/v1/github/repos", "github repos", api.RiskRead, "List GitHub repositories",
		"Repositories the box's GitHub App can reach, most recently pushed first, with their default "+
			"branch and which apps already deploy from them. Filter with q. To deploy one: github repo for its folders and framework, "+
			"then add an app with git: {repo, branch, path} to the project's manifest, plan, apply, and deploys github.", "github"))
	rl.Errors = append(rl.Errors, 409)
	huma.Register(a, rl, api.Wrap(func(ctx context.Context, in *struct {
		Q       string `query:"q" maxLength:"100" doc:"Only repositories whose owner/name contains this"`
		Limit   int    `query:"limit" minimum:"1" maximum:"1000" default:"100" doc:"Maximum repositories to return"`
		Refresh bool   `query:"refresh" doc:"Ask GitHub again instead of using the list from the last minute"`
	}) (*struct{ Body GitHubRepoList }, error) {
		r, err := m.rt()
		if err != nil {
			return nil, unavailable(err)
		}
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeApplyReversible, ""); err != nil {
			return nil, err
		}
		c, err := r.github(ctx)
		if err != nil {
			return nil, r.toProblem(err, "GitHub App")
		}
		out, err := r.listRepos(ctx, c, in.Q, in.Limit, in.Refresh)
		if err != nil {
			return nil, problem(502, "internal", "GitHub: "+err.Error(), "Try again in a moment.")
		}
		return &struct{ Body GitHubRepoList }{*out}, nil
	}))

	rd := api.Outbound(api.Op("github-repo", http.MethodGet, "/v1/github/repos/{owner}/{repo}", "github repo", api.RiskRead, "Look inside a GitHub repository",
		"What the box sees in a repository before importing it: branches, the latest commit, and the folders that look like apps "+
			"(package.json, index.html) with the framework the box would use for each (next, hono, bun or static), most likely first. "+
			"Monorepos list each app's folder; use it as git.path.", "github"))
	rd.Errors = append(rd.Errors, 404, 409)
	huma.Register(a, rd, api.Wrap(func(ctx context.Context, in *struct {
		Owner  string `path:"owner" pattern:"^[A-Za-z0-9][A-Za-z0-9-]{0,38}$" doc:"Account that owns the repository"`
		Repo   string `path:"repo" pattern:"^[A-Za-z0-9._-]{1,100}$" doc:"Repository name"`
		Branch string `query:"branch" maxLength:"200" doc:"Branch to look at. Default: the repository's default branch"`
	}) (*struct{ Body GitHubRepoDetail }, error) {
		r, err := m.rt()
		if err != nil {
			return nil, unavailable(err)
		}
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeApplyReversible, ""); err != nil {
			return nil, err
		}
		c, err := r.github(ctx)
		if err != nil {
			return nil, r.toProblem(err, "GitHub App")
		}
		full := in.Owner + "/" + in.Repo
		if !ghapp.ValidRepo(full) {
			return nil, problem(422, "validation", "not a repository name", "")
		}
		out, err := r.inspectRepo(ctx, c, full, in.Branch)
		if err != nil {
			return nil, r.toProblem(err, "repository "+full)
		}
		return &struct{ Body GitHubRepoDetail }{*out}, nil
	}))

	gd := api.Outbound(api.Op("deploy-github", http.MethodPost, "/v1/projects/{project}/apps/{app}/deploys/github", "deploys github", api.RiskWrite, "Deploy from GitHub",
		"Deploys an app connected to a GitHub repository (git in its manifest) now: the latest commit of its production branch, "+
			"or ref. Pushes deploy on their own; this is Redeploy, or the first deploy after importing a repository. The box clones "+
			"exactly that commit with a short-lived read-only token, builds and releases it through the regular pipeline, and marks the "+
			"commit on GitHub. Returns at once with the queued deploy; poll deploys get until status is live or failed.", "apps"))
	gd.DefaultStatus = http.StatusAccepted
	gd.Errors = append(gd.Errors, 404, 409)
	huma.Register(a, gd, api.Wrap(func(ctx context.Context, in *struct {
		Project string        `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		App     string        `path:"app" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"App name"`
		Preview string        `query:"preview" pattern:"^[a-z0-9][a-z0-9-]{0,29}$" doc:"Deploy as a preview with this name instead of production"`
		Body    *ghDeployBody `required:"false"`
	}) (*struct{ Body *Deploy }, error) {
		r, err := m.rt()
		if err != nil {
			return nil, unavailable(err)
		}
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		spec, perr := r.checkDeployable(ctx, in.Project, in.App, in.Preview, false)
		if perr != nil {
			return nil, perr
		}
		if spec.Git == nil {
			return nil, problem(409, "precondition", "app "+in.App+" is not connected to a GitHub repository",
				fmt.Sprintf("Add git: {repo: \"owner/name\"} to apps.%s in the manifest (projects manifest, plan, apply), or import it from GitHub in the dashboard.", in.App))
		}
		ref := spec.Git.Branch
		if in.Body != nil && strings.TrimSpace(in.Body.Ref) != "" {
			ref = strings.TrimSpace(in.Body.Ref)
			if !refRe.MatchString(ref) || strings.Contains(ref, "..") {
				return nil, problem(422, "validation", "ref must be a branch, tag or commit SHA", "")
			}
		}
		c, err := r.github(ctx)
		if err != nil {
			return nil, r.toProblem(err, "GitHub App")
		}
		inst, err := r.repoInstallation(ctx, c, spec.Git.Repo)
		if err != nil {
			return nil, r.toProblem(err, "repository "+spec.Git.Repo)
		}
		cm, err := c.App.Commit(ctx, inst, spec.Git.Repo, ref)
		if ghapp.IsStatus(err, 404) || ghapp.IsStatus(err, 422) {
			return nil, problem(422, "validation", fmt.Sprintf("%s has no branch, tag or commit %q", spec.Git.Repo, ref), "Check git.branch, or pass another ref.")
		}
		if err != nil {
			return nil, problem(502, "internal", "GitHub: "+err.Error(), "Try again in a moment.")
		}
		branch := spec.Git.Branch
		if ref != spec.Git.Branch && !ghapp.ValidSHA(ref) {
			branch = ref
		}
		d, err := r.enqueue(ctx, &ghJob{Project: in.Project, App: in.App, Preview: in.Preview, Repo: spec.Git.Repo, SHA: cm.SHA, Branch: branch,
			Message: cm.Message, Author: cm.Author, Installation: inst, Trigger: "redeploy", By: pr.TokenID, Path: spec.Git.Path})
		if err != nil {
			return nil, err
		}
		_ = r.p.DB.Audit(ctx, pr.TokenID, "deploy.create", in.Project+"/"+in.App, map[string]any{"deploy": d.ID, "preview": in.Preview, "source": "github",
			"repo": spec.Git.Repo, "commit": cm.SHA, "session": pr.Session})
		return &struct{ Body *Deploy }{d}, nil
	}))

	// GitHub's own calls: deliveries and the browser coming back. They
	// carry no Tiffin credentials (the webhook is signed; the browser
	// round trips carry one-time states), so they bypass the API's auth.
	hook := func(hctx huma.Context) {
		req, w := humago.Unwrap(hctx)
		r, err := m.rt()
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		r.handleWebhook(w, req)
	}
	a.Adapter().Handle(&huma.Operation{Method: http.MethodPost, Path: "/v1/github/webhook"}, hook)
	a.Adapter().Handle(&huma.Operation{Method: http.MethodGet, Path: "/v1/github/manifest/callback"}, func(hctx huma.Context) {
		req, w := humago.Unwrap(hctx)
		r, err := m.rt()
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		r.handleManifestCallback(w, req)
	})
	a.Adapter().Handle(&huma.Operation{Method: http.MethodGet, Path: "/v1/github/setup"}, func(hctx huma.Context) {
		req, w := humago.Unwrap(hctx)
		r, err := m.rt()
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		r.handleSetup(w, req)
	})
}

// back sends the browser to the dashboard's Git settings with a note.
func (r *rt) back(w http.ResponseWriter, req *http.Request, q url.Values) {
	http.Redirect(w, req, r.publicBase()+"/settings/git?"+q.Encode(), http.StatusFound)
}

// handleManifestCallback is where GitHub sends the browser after the app
// was created: exchange the code for the credentials, then go install it.
func (r *rt) handleManifestCallback(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	q := req.URL.Query()
	p, ok := r.takeState(ctx, q.Get("state"), "manifest")
	if !ok {
		r.back(w, req, url.Values{"error": {"That link has expired or was already used. Click Connect GitHub again."}})
		return
	}
	c := r.ghClient(r.ghEndpoints())
	cred, err := c.ConvertManifest(ctx, q.Get("code"))
	if err != nil {
		r.logEvent(ctx, GitHubEvent{Event: "connect", Summary: "GitHub did not hand over the new app: " + err.Error()})
		r.back(w, req, url.Values{"error": {"GitHub did not hand over the new app (" + err.Error() + "). Click Connect GitHub again."}})
		return
	}
	if err := r.storeApp(ctx, ghStored{Source: ghSourceBox, Cred: *cred}); err != nil {
		r.back(w, req, url.Values{"error": {err.Error()}})
		return
	}
	_ = r.p.DB.Audit(ctx, p.By, "github.connected", "github", map[string]any{"app": cred.Slug, "id": cred.ID, "owner": cred.Owner})
	r.logEvent(ctx, GitHubEvent{Event: "connect", Summary: fmt.Sprintf("Created the GitHub App %s for %s", cred.Slug, cred.Owner), OK: true})
	state, err := r.issueState(ctx, ghPending{Kind: "install", By: p.By})
	if err != nil {
		r.back(w, req, url.Values{"connected": {"1"}})
		return
	}
	http.Redirect(w, req, c.E.InstallURL(cred.Slug, state), http.StatusFound)
}

// handleSetup is where GitHub sends the browser after an installation.
// For the box's own app every installation is the owner's; for a shared
// app the box binds the installation only when the one-time state is its
// own and GitHub sign-in proves the person can reach the installation.
func (r *rt) handleSetup(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	q := req.URL.Query()
	c, err := r.github(ctx)
	if err != nil {
		r.back(w, req, url.Values{"error": {"This box is not connected to GitHub any more."}})
		return
	}
	r.gh.mu.Lock()
	r.gh.repos = map[int64]cachedRepos{}
	r.gh.mu.Unlock()
	if q.Get("setup_action") == "request" {
		r.back(w, req, url.Values{"requested": {"1"}})
		return
	}
	id, _ := strconv.ParseInt(q.Get("installation_id"), 10, 64)
	if !c.Public {
		r.logEvent(ctx, GitHubEvent{Event: "installation", Summary: "Installed or updated on GitHub", OK: true})
		r.back(w, req, url.Values{"installed": {"1"}})
		return
	}
	p, ok := r.takeState(ctx, q.Get("state"), "install")
	if !ok || id <= 0 {
		r.back(w, req, url.Values{"error": {"That install link has expired. Click Install on repositories again."}})
		return
	}
	mine, err := c.App.UserInstallations(ctx, q.Get("code"))
	if err != nil {
		r.back(w, req, url.Values{"error": {"GitHub sign-in could not confirm the installation (" + err.Error() + ")."}})
		return
	}
	if !slices.ContainsFunc(mine, func(in ghapp.Installation) bool { return in.ID == id }) {
		r.back(w, req, url.Values{"error": {"You can't reach that installation on GitHub, so this box won't use it."}})
		return
	}
	if err := r.setBound(ctx, append(r.bound(ctx), id)); err != nil {
		r.back(w, req, url.Values{"error": {err.Error()}})
		return
	}
	_ = r.p.DB.Audit(ctx, p.By, "github.installed", "github", map[string]any{"installation": id})
	r.logEvent(ctx, GitHubEvent{Event: "installation", Summary: fmt.Sprintf("Installation %d now deploys to this box", id), OK: true})
	r.back(w, req, url.Values{"installed": {"1"}})
}

// githubStatus describes the connection (it calls GitHub, briefly).
func (r *rt) githubStatus(ctx context.Context) GitHubStatus {
	e := r.ghEndpoints()
	out := GitHubStatus{Installations: []GitHubInstallation{}, WebhookURL: r.ghURLs().Webhook, GitHubURL: e.Web, Events: r.events(ctx)}
	out.Reachable, out.ReachableHint = r.reachable(e)
	c, err := r.github(ctx)
	if errors.Is(err, errNoGitHub) {
		return out
	}
	if err != nil {
		out.Problem = err.Error()
		return out
	}
	out.Connected, out.Source, out.Shared = true, c.Source, c.Public
	cr := c.Cred
	info := &GitHubAppInfo{ID: cr.ID, Slug: cr.Slug, Name: cr.Name, Owner: cr.Owner, HTMLURL: cr.HTMLURL}
	if !cr.CreatedAt.IsZero() {
		t := cr.CreatedAt
		info.CreatedAt = &t
	}
	out.App = info
	gctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if info.Slug == "" {
		if i, err := c.App.Info(gctx); err == nil {
			info.Slug, info.Name, info.Owner, info.HTMLURL = i.Slug, i.Name, i.Owner.Login, i.HTMLURL
		}
	}
	if info.Slug != "" {
		info.SettingsURL = c.E.AppSettingsURL(&ghapp.Credentials{Slug: info.Slug, Owner: info.Owner, OwnerType: cr.OwnerType})
	}
	ins, err := r.installations(gctx, c)
	if err != nil {
		out.Problem = "GitHub: " + err.Error()
		return out
	}
	for _, in := range ins {
		out.Installations = append(out.Installations, GitHubInstallation{ID: in.ID, Account: in.Account.Login, AccountType: in.Account.Type,
			Repositories: orDefaultStr(in.RepositorySelection, "all"), SettingsURL: in.HTMLURL, Suspended: in.SuspendedAt != nil})
	}
	return out
}

// connectedIndex maps repositories to the apps deploying from them.
func (r *rt) connectedIndex(ctx context.Context) map[string][]string {
	out := map[string][]string{}
	projects, err := r.p.DB.ListProjects(ctx)
	if err != nil {
		return out
	}
	for _, pr := range projects {
		_, res, err := r.p.DB.Load(ctx, pr)
		if err != nil {
			continue
		}
		for addr, rs := range res {
			if !strings.HasPrefix(addr, "app/") {
				continue
			}
			var a manifest.App
			if json.Unmarshal(rs.Spec, &a) == nil && a.Git != nil {
				k := strings.ToLower(a.Git.Repo)
				out[k] = append(out[k], pr+"/"+strings.TrimPrefix(addr, "app/"))
			}
		}
	}
	for _, v := range out {
		sort.Strings(v)
	}
	return out
}

// listRepos lists the repositories of every installation the box acts for.
func (r *rt) listRepos(ctx context.Context, c *ghConn, q string, limit int, refresh bool) (*GitHubRepoList, error) {
	ins, err := r.installations(ctx, c)
	if err != nil {
		return nil, err
	}
	type res struct {
		id    int64
		repos []ghapp.Repo
		err   error
	}
	results := make([]res, len(ins))
	var wg sync.WaitGroup
	for i, in := range ins {
		r.gh.mu.Lock()
		cached, ok := r.gh.repos[in.ID]
		r.gh.mu.Unlock()
		if ok && !refresh && time.Since(cached.at) < time.Minute {
			results[i] = res{id: in.ID, repos: cached.repos}
			continue
		}
		wg.Add(1)
		go func(i int, id int64) {
			defer wg.Done()
			repos, err := c.App.Repos(ctx, id)
			results[i] = res{id: id, repos: repos, err: err}
			if err == nil {
				r.gh.mu.Lock()
				if r.gh.repos == nil {
					r.gh.repos = map[int64]cachedRepos{}
				}
				r.gh.repos[id] = cachedRepos{at: time.Now(), repos: repos}
				r.gh.mu.Unlock()
			}
		}(i, in.ID)
	}
	wg.Wait()
	idx := r.connectedIndex(ctx)
	out := &GitHubRepoList{Repos: []GitHubRepo{}, Installations: len(ins)}
	seen := map[string]bool{}
	q = strings.ToLower(strings.TrimSpace(q))
	for _, rs := range results {
		if rs.err != nil {
			return nil, rs.err
		}
		for _, g := range rs.repos {
			k := strings.ToLower(g.FullName)
			if seen[k] {
				continue
			}
			seen[k] = true
			out.Total++
			if q != "" && !strings.Contains(k, q) {
				continue
			}
			out.Repos = append(out.Repos, GitHubRepo{FullName: g.FullName, Owner: g.Owner.Login, Name: g.Name, Private: g.Private, Fork: g.Fork,
				Archived: g.Archived, Description: ghapp.Clip(g.Description, 200), DefaultBranch: g.DefaultBranch, PushedAt: g.PushedAt,
				URL: orDefaultStr(g.HTMLURL, c.E.RepoURL(g.FullName)), Installation: rs.id, Connected: orEmpty(idx[k])})
		}
	}
	sort.SliceStable(out.Repos, func(i, j int) bool { return out.Repos[i].PushedAt.After(out.Repos[j].PushedAt) })
	if len(out.Repos) > limit {
		out.Repos = out.Repos[:limit]
	}
	return out, nil
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// inspectRepo looks at a repository's branches and folders.
func (r *rt) inspectRepo(ctx context.Context, c *ghConn, full, branch string) (*GitHubRepoDetail, error) {
	inst, err := r.repoInstallation(ctx, c, full)
	if err != nil {
		return nil, err
	}
	repo, err := c.App.Repo(ctx, inst, full)
	if ghapp.IsStatus(err, 404) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	if branch == "" {
		branch = repo.DefaultBranch
	}
	out := &GitHubRepoDetail{FullName: repo.FullName, Private: repo.Private, DefaultBranch: repo.DefaultBranch, Branch: branch, Roots: []RepoRoot{}, Folders: []string{},
		Connected: orEmpty(r.connectedIndex(ctx)[strings.ToLower(repo.FullName)])}
	if out.Branches, err = c.App.Branches(ctx, inst, full); err != nil {
		return nil, err
	}
	if out.Branches == nil {
		out.Branches = []string{}
	}
	cm, err := c.App.Commit(ctx, inst, full, branch)
	if ghapp.IsStatus(err, 404) || ghapp.IsStatus(err, 409) || ghapp.IsStatus(err, 422) {
		// An empty repository, or no such branch.
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	out.Commit = &GitHubCommit{SHA: cm.SHA, Message: firstLine(cm.Message), Author: cm.Author}
	tree, truncated, err := c.App.Tree(ctx, inst, full, cm.SHA)
	if err != nil {
		return nil, err
	}
	out.Truncated = truncated
	out.Roots = detectRoots(tree, func(p string) ([]byte, error) { return c.App.File(ctx, inst, full, cm.SHA, p) })
	out.Folders = repoFolders(tree)
	if len(out.Roots) > 0 {
		out.Suggested = out.Roots[0].Path
	}
	return out, nil
}

var skipDirs = map[string]bool{"node_modules": true, ".git": true, "vendor": true, "dist": true, "build": true, ".next": true, "out": true,
	"coverage": true, ".turbo": true, ".vercel": true, "test": true, "tests": true, "__tests__": true, "fixtures": true,
	"venv": true, "__pycache__": true, "site-packages": true}

// detectRoots finds the folders of a repository that look like apps and
// guesses each one's framework. read fetches a file's content.
func detectRoots(tree []ghapp.TreeEntry, read func(string) ([]byte, error)) []RepoRoot {
	pkgs, htmls := map[string]bool{}, map[string]bool{}
	workspaceFiles, pnpmFiles := map[string]bool{}, map[string]bool{}
	py := pyFiles{} // Python apps (pydetect.go)
	for _, e := range tree {
		if e.Type != "blob" {
			continue
		}
		dir, base := path.Split(e.Path)
		dir = strings.TrimSuffix(dir, "/")
		segs := strings.Split(dir, "/")
		if dir == "" {
			segs = nil
		}
		if len(segs) > 4 || slices.ContainsFunc(segs, func(s string) bool { return skipDirs[s] || strings.HasPrefix(s, ".") }) {
			continue
		}
		switch base {
		case "package.json":
			pkgs[dir] = true
		case "index.html":
			htmls[dir] = true
		case "turbo.json", "lerna.json", "nx.json":
			workspaceFiles[dir] = true
		case "pnpm-workspace.yaml":
			pnpmFiles[dir] = true
		}
		py.add(dir, base)
	}
	type cand struct {
		dir     string
		pkg, py bool
	}
	var cands []cand
	for d := range pkgs {
		cands = append(cands, cand{dir: d, pkg: true})
	}
	pyDirs := py.dirs()
	for d := range pyDirs {
		if !pkgs[d] {
			cands = append(cands, cand{dir: d, py: true})
		}
	}
	for d := range htmls {
		if pkgs[d] || hasPkgAbove(d, pkgs) || pyDirs[d] || hasPkgAbove(d, pyDirs) {
			continue // built by its package (or a Python app's templates)
		}
		cands = append(cands, cand{dir: d})
	}
	sort.Slice(cands, func(i, j int) bool {
		di, dj := depth(cands[i].dir), depth(cands[j].dir)
		if di != dj {
			return di < dj
		}
		return cands[i].dir < cands[j].dir
	})
	if len(cands) > 24 {
		cands = cands[:24]
	}
	var out []RepoRoot
	for _, cd := range cands {
		if cd.py {
			out = append(out, guessPython(cd.dir, py[cd.dir], read))
			continue
		}
		if !cd.pkg {
			out = append(out, RepoRoot{Path: cd.dir, Framework: string(manifest.FrameworkStatic), Preset: "html", Why: "index.html and no package.json: served as files"})
			continue
		}
		file := "package.json"
		if cd.dir != "" {
			file = cd.dir + "/package.json"
		}
		raw, err := read(file)
		root := RepoRoot{Path: cd.dir, Framework: string(manifest.FrameworkBun)}
		if err != nil {
			root.Why = "package.json (could not be read: guessing a Bun server)"
			out = append(out, root)
			continue
		}
		g := guessFramework(raw, htmls[cd.dir])
		if g.Framework == string(manifest.FrameworkBun) && g.Unsupported == "" && !g.Workspace && pyDirs[cd.dir] {
			g = guessPython(cd.dir, py[cd.dir], read) // a Python app with a package.json for its tooling
		}
		root.Framework, root.Preset, root.Name, root.Why, root.Workspace, root.Unsupported = g.Framework, g.Preset, g.Name, g.Why, g.Workspace, g.Unsupported
		if workspaceFiles[cd.dir] || (pnpmFiles[cd.dir] && pnpmWorkspace(read, cd.dir)) {
			root.Workspace = true
		}
		if root.Workspace && root.Unsupported == "" {
			root.Why = "the top of a monorepo: its apps are in the folders below"
		}
		out = append(out, root)
	}
	out = append(out, dockerRoots(tree, out)...)
	// The most likely app first: not a monorepo's top, shallowest.
	sort.SliceStable(out, func(i, j int) bool { return !out[i].Workspace && out[j].Workspace })
	if len(out) == 0 {
		out = []RepoRoot{}
	}
	return out
}

// pnpmWorkspace reports whether dir's pnpm-workspace.yaml lists packages:
// since pnpm 10 the file also carries settings (allowBuilds…) for a
// single-package repository.
func pnpmWorkspace(read func(string) ([]byte, error), dir string) bool {
	file := "pnpm-workspace.yaml"
	if dir != "" {
		file = dir + "/" + file
	}
	raw, err := read(file)
	return err == nil && pnpmPackagesRe.Match(raw)
}

var pnpmPackagesRe = regexp.MustCompile(`(?m)^packages:`)

func depth(dir string) int {
	if dir == "" {
		return 0
	}
	return strings.Count(dir, "/") + 1
}

func hasPkgAbove(dir string, pkgs map[string]bool) bool {
	for d := dir; d != "" && d != "."; {
		i := strings.LastIndexByte(d, '/')
		if i < 0 {
			d = ""
		} else {
			d = d[:i]
		}
		if pkgs[d] && d != "" {
			return true
		}
	}
	return false
}

// fullStack are frameworks that build on Vite (or their own bundler) but
// need their own server: checked before the "Vite builds it to files" rule,
// which would otherwise serve their build output as a static site.
var fullStack = []struct{ dep, name string }{
	{"@sveltejs/kit", "SvelteKit"},
	{"nuxt", "Nuxt"},
	{"@react-router/dev", "React Router (framework mode)"},
	{"@remix-run/dev", "Remix"},
	{"@tanstack/solid-start", "TanStack Start for Solid"},
	{"@solidjs/start", "SolidStart"},
}

// astroServer are Astro's server adapters for other hosts: with one, Astro
// renders on a server the box doesn't run. @astrojs/node is the one it does.
var astroServer = []string{"@astrojs/vercel", "@astrojs/netlify", "@astrojs/cloudflare"}

// guessFramework reads a package.json.
func guessFramework(raw []byte, hasHTML bool) RepoRoot {
	var pj struct {
		Name            string            `json:"name"`
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
		Scripts         map[string]string `json:"scripts"`
		Workspaces      json.RawMessage   `json:"workspaces"`
	}
	if err := json.Unmarshal(raw, &pj); err != nil {
		return RepoRoot{Framework: string(manifest.FrameworkBun), Why: "package.json is not valid JSON: guessing a Bun server"}
	}
	has := func(dep string) bool {
		_, a := pj.Dependencies[dep]
		_, b := pj.DevDependencies[dep]
		return a || b
	}
	out := RepoRoot{Name: pj.Name, Workspace: len(pj.Workspaces) > 0 && string(pj.Workspaces) != "null"}
	is := func(f manifest.Framework, preset, why string) RepoRoot {
		out.Framework, out.Preset, out.Why = string(f), preset, why
		return out
	}
	soon := func(name, dep string) RepoRoot {
		out.Framework, out.Unsupported = string(manifest.FrameworkBun), name
		out.Why = name + " (" + dep + " in package.json): not supported yet, coming soon"
		return out
	}
	if has("next") {
		return is(manifest.FrameworkNext, "nextjs", "Next.js (next in package.json)")
	}
	// TanStack Start builds a server (Nitro's .output/server, or dist/server
	// with srvx) that runs on Bun like any other; the box serves its client assets.
	if has("@tanstack/react-start") {
		return is(manifest.FrameworkBun, "tanstack-start", "TanStack Start (@tanstack/react-start in package.json): its server runs on Bun")
	}
	for _, f := range fullStack {
		if has(f.dep) {
			return soon(f.name, f.dep)
		}
	}
	if has("astro") {
		if has("@astrojs/node") {
			return is(manifest.FrameworkBun, "astro", "Astro with server rendering (@astrojs/node): its standalone server runs on Bun")
		}
		for _, a := range astroServer {
			if has(a) {
				return soon("Astro with the "+a+" adapter", a)
			}
		}
	}
	if has("hono") {
		return is(manifest.FrameworkHono, "hono", "Hono (hono in package.json)")
	}
	for _, d := range []string{"vite", "astro", "react-scripts", "@11ty/eleventy", "gatsby", "parcel", "@docusaurus/core", "vitepress"} {
		if has(d) && pj.Scripts["build"] != "" {
			preset := map[string]string{"vite": "vite", "astro": "astro"}[d]
			if d == "vite" && has("react") {
				preset = "vite-react"
			}
			return is(manifest.FrameworkStatic, preset, "a static site ("+d+" builds it to files)")
		}
	}
	if pj.Scripts["start"] == "" && pj.Scripts["build"] != "" && hasHTML {
		return is(manifest.FrameworkStatic, "", "a static site (a build script and index.html)")
	}
	return is(manifest.FrameworkBun, "", "a Bun or Node server (package.json without Next.js or Hono)")
}
