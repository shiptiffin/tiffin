// Package ghapp is a small client for a GitHub App: the manifest flow that
// creates one (so a self-hosted box needs no shared secrets), app JWTs
// (RS256 with the app's key), installation tokens, the few REST calls a
// deploy needs (repositories, branches, trees, commits, commit statuses,
// deployments, pull request comments) and webhook verification.
//
// It talks to github.com by default and to GitHub Enterprise Server (or a
// test double) when given other endpoints. It has no state on disk: the
// caller stores the credentials (encrypted) and hands them back.
package ghapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Endpoints are where a GitHub lives: its REST API and its website.
type Endpoints struct {
	API string `json:"apiUrl"` // https://api.github.com, or https://ghe.example.com/api/v3
	Web string `json:"webUrl"` // https://github.com, or https://ghe.example.com
}

// GitHubCom is github.com.
var GitHubCom = Endpoints{API: "https://api.github.com", Web: "https://github.com"}

// IsGitHubCom reports whether e is github.com.
func (e Endpoints) IsGitHubCom() bool {
	return strings.TrimRight(e.Web, "/") == GitHubCom.Web
}

func (e Endpoints) api(path string) string { return strings.TrimRight(e.API, "/") + path }
func (e Endpoints) web(path string) string { return strings.TrimRight(e.Web, "/") + path }

// RepoURL is a repository's page (and, with ".git", its clone URL).
func (e Endpoints) RepoURL(fullName string) string { return e.web("/" + fullName) }

// Credentials are what GitHub hands back when an app is created from a
// manifest. Everything but the private key, the client secret and the
// webhook secret is public.
type Credentials struct {
	ID            int64     `json:"id"`
	Slug          string    `json:"slug"`
	Name          string    `json:"name"`
	Owner         string    `json:"owner"`     // account login the app belongs to
	OwnerType     string    `json:"ownerType"` // User or Organization
	HTMLURL       string    `json:"htmlUrl"`
	ClientID      string    `json:"clientId"`
	ClientSecret  string    `json:"clientSecret"`
	WebhookSecret string    `json:"webhookSecret"`
	PrivateKey    string    `json:"privateKey"` // PEM
	CreatedAt     time.Time `json:"createdAt"`
}

// Check reports whether the credentials are complete enough to work.
func (c *Credentials) Check() error {
	switch {
	case c == nil:
		return errors.New("no credentials")
	case c.ID <= 0:
		return errors.New("the app id is missing")
	case c.Slug == "":
		return errors.New("the app slug is missing")
	case c.WebhookSecret == "":
		return errors.New("the webhook secret is missing")
	}
	_, err := ParseKey(c.PrivateKey)
	return err
}

// Permissions the app asks for: read code, write the signals a deploy
// leaves on GitHub (commit statuses, deployments, one pull request comment).
var Permissions = map[string]string{
	"contents":      "read",
	"metadata":      "read",
	"pull_requests": "write",
	"statuses":      "write",
	"deployments":   "write",
}

// Events the app subscribes to. Installation events are always delivered
// to an app's webhook, without subscribing.
var Events = []string{"push", "pull_request"}

// Manifest is a GitHub App manifest (the manifest flow's "manifest" form
// field).
type Manifest struct {
	Name               string            `json:"name"`
	URL                string            `json:"url"`
	Description        string            `json:"description,omitempty"`
	HookAttributes     HookAttributes    `json:"hook_attributes"`
	RedirectURL        string            `json:"redirect_url"`
	CallbackURLs       []string          `json:"callback_urls,omitempty"`
	SetupURL           string            `json:"setup_url,omitempty"`
	SetupOnUpdate      bool              `json:"setup_on_update"`
	Public             bool              `json:"public"`
	DefaultPermissions map[string]string `json:"default_permissions"`
	DefaultEvents      []string          `json:"default_events"`
}

// HookAttributes is where the app's webhooks go.
type HookAttributes struct {
	URL    string `json:"url"`
	Active bool   `json:"active"`
}

// ManifestURLs are the box addresses a manifest points GitHub at.
type ManifestURLs struct {
	Home     string // the dashboard
	Webhook  string // POST deliveries here
	Callback string // GitHub redirects here with ?code= after creating the app
	Setup    string // GitHub redirects here after an installation
}

// NewManifest builds the manifest for a box's app.
func NewManifest(name string, u ManifestURLs) Manifest {
	return Manifest{
		Name:               name,
		URL:                u.Home,
		Description:        "Deploys this account's repositories to a Tiffin box: every push to the production branch, and a preview for each pull request.",
		HookAttributes:     HookAttributes{URL: u.Webhook, Active: true},
		RedirectURL:        u.Callback,
		CallbackURLs:       []string{u.Callback},
		SetupURL:           u.Setup,
		SetupOnUpdate:      true,
		Public:             false,
		DefaultPermissions: Permissions,
		DefaultEvents:      Events,
	}
}

var orgRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)

// ValidOwner reports whether s can be a GitHub account login.
func ValidOwner(s string) bool { return orgRe.MatchString(s) }

// NewAppURL is where the browser POSTs the manifest form: the account's
// app settings, or an organization's when org is set.
func (e Endpoints) NewAppURL(org, state string) string {
	p := "/settings/apps/new"
	if org != "" {
		p = "/organizations/" + url.PathEscape(org) + "/settings/apps/new"
	}
	return e.web(p) + "?state=" + url.QueryEscape(state)
}

// InstallURL is the app's "install on repositories" page.
func (e Endpoints) InstallURL(slug, state string) string {
	u := e.web("/apps/" + url.PathEscape(slug) + "/installations/new")
	if state != "" {
		u += "?state=" + url.QueryEscape(state)
	}
	return u
}

// AppSettingsURL is where the owner manages (or deletes) the app on GitHub.
func (e Endpoints) AppSettingsURL(c *Credentials) string {
	if c.OwnerType == "Organization" && c.Owner != "" {
		return e.web("/organizations/" + url.PathEscape(c.Owner) + "/settings/apps/" + url.PathEscape(c.Slug))
	}
	return e.web("/settings/apps/" + url.PathEscape(c.Slug))
}

// ---- the client ----

// APIError is an error response from GitHub.
type APIError struct {
	Status  int
	Message string
	Path    string
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	return fmt.Sprintf("GitHub %s: %d %s", e.Path, e.Status, msg)
}

// IsStatus reports whether err is a GitHub error with that status.
func IsStatus(err error, status int) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == status
}

// Client calls one GitHub.
type Client struct {
	E    Endpoints
	HTTP *http.Client
	// UserAgent is sent with every request (GitHub requires one).
	UserAgent string
	now       func() time.Time
}

// NewClient returns a client for e.
func NewClient(e Endpoints, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	return &Client{E: e, HTTP: hc, UserAgent: "tiffin", now: time.Now}
}

const maxResponse = 16 << 20

// do sends a request with auth ("Bearer <jwt>" or "token <t>", or none)
// and decodes a JSON response into out (when not nil).
func (c *Client) do(ctx context.Context, method, path, auth string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.E.api(path), rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", c.UserAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub %s: %w", path, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponse))
	if err != nil {
		return fmt.Errorf("GitHub %s: %w", path, err)
	}
	if res.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &e)
		return &APIError{Status: res.StatusCode, Message: e.Message, Path: path}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("GitHub %s: unexpected response: %w", path, err)
	}
	return nil
}

var codeRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)

// ConvertManifest exchanges the code GitHub sent back after creating an
// app from a manifest for the app's credentials. The code works once and
// for one hour.
func (c *Client) ConvertManifest(ctx context.Context, code string) (*Credentials, error) {
	if !codeRe.MatchString(code) {
		return nil, errors.New("the code from GitHub is not valid")
	}
	var r struct {
		ID    int64  `json:"id"`
		Slug  string `json:"slug"`
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"owner"`
		HTMLURL       string `json:"html_url"`
		ClientID      string `json:"client_id"`
		ClientSecret  string `json:"client_secret"`
		WebhookSecret string `json:"webhook_secret"`
		PEM           string `json:"pem"`
	}
	if err := c.do(ctx, http.MethodPost, "/app-manifests/"+code+"/conversions", "", nil, &r); err != nil {
		return nil, err
	}
	cred := &Credentials{ID: r.ID, Slug: r.Slug, Name: r.Name, Owner: r.Owner.Login, OwnerType: r.Owner.Type, HTMLURL: r.HTMLURL,
		ClientID: r.ClientID, ClientSecret: r.ClientSecret, WebhookSecret: r.WebhookSecret, PrivateKey: r.PEM, CreatedAt: c.now().UTC()}
	if err := cred.Check(); err != nil {
		return nil, fmt.Errorf("GitHub returned incomplete app credentials: %w", err)
	}
	return cred, nil
}

// ---- the app ----

// App acts as one GitHub App.
type App struct {
	c    *Client
	cred Credentials
	key  *rsa.PrivateKey

	mu     sync.Mutex
	tokens map[int64]token
}

type token struct {
	value   string
	expires time.Time
}

// NewApp returns the app for cred.
func (c *Client) NewApp(cred Credentials) (*App, error) {
	key, err := ParseKey(cred.PrivateKey)
	if err != nil {
		return nil, err
	}
	return &App{c: c, cred: cred, key: key, tokens: map[int64]token{}}, nil
}

// Credentials returns the app's credentials.
func (a *App) Credentials() Credentials { return a.cred }

// Endpoints returns the GitHub the app lives on.
func (a *App) Endpoints() Endpoints { return a.c.E }

// ParseKey reads an RSA private key in PEM (PKCS #1, as GitHub issues
// them, or PKCS #8).
func ParseKey(s string) (*rsa.PrivateKey, error) {
	b, _ := pem.Decode([]byte(strings.TrimSpace(s)))
	if b == nil {
		return nil, errors.New("the private key is not PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(b.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, errors.New("the private key is not an RSA key")
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("the private key is not an RSA key")
	}
	return rk, nil
}

// JWT signs a 9-minute app token (RS256), issued a minute in the past to
// allow for clock drift, as GitHub recommends.
func (a *App) JWT() (string, error) {
	now := a.c.now()
	head := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": strconv.FormatInt(a.cred.ID, 10),
	})
	signing := head + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func (a *App) asApp(ctx context.Context, method, path string, body, out any) error {
	jwt, err := a.JWT()
	if err != nil {
		return err
	}
	return a.c.do(ctx, method, path, "Bearer "+jwt, body, out)
}

// Info is the app as GitHub shows it.
type Info struct {
	ID      int64  `json:"id"`
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	HTMLURL string `json:"html_url"`
	Owner   struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"owner"`
}

// Info fetches the app (checks the key works).
func (a *App) Info(ctx context.Context) (*Info, error) {
	var i Info
	return &i, a.asApp(ctx, http.MethodGet, "/app", nil, &i)
}

// Installation is the app installed on one account.
type Installation struct {
	ID      int64 `json:"id"`
	Account struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"account"`
	RepositorySelection string     `json:"repository_selection"` // all or selected
	HTMLURL             string     `json:"html_url"`
	SuspendedAt         *time.Time `json:"suspended_at"`
}

// Installations lists the accounts the app is installed on.
func (a *App) Installations(ctx context.Context) ([]Installation, error) {
	var out []Installation
	for page := 1; page <= 10; page++ {
		var batch []Installation
		if err := a.asApp(ctx, http.MethodGet, fmt.Sprintf("/app/installations?per_page=100&page=%d", page), nil, &batch); err != nil {
			return nil, err
		}
		out = append(out, batch...)
		if len(batch) < 100 {
			break
		}
	}
	return out, nil
}

// HookConfig is where GitHub sends the app's webhooks.
type HookConfig struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
}

// HookConfig reads the app's webhook settings (404 when the app has no
// webhook).
func (a *App) HookConfig(ctx context.Context) (*HookConfig, error) {
	var h HookConfig
	return &h, a.asApp(ctx, http.MethodGet, "/app/hook/config", nil, &h)
}

// HookDelivery is one webhook GitHub sent (or tried to send).
type HookDelivery struct {
	GUID        string    `json:"guid"`
	DeliveredAt time.Time `json:"delivered_at"`
	Redelivery  bool      `json:"redelivery"`
	Status      string    `json:"status"`
	StatusCode  int       `json:"status_code"`
	Event       string    `json:"event"`
	Action      string    `json:"action"`
}

// HookDeliveries lists the app's latest webhook deliveries, newest first.
func (a *App) HookDeliveries(ctx context.Context, n int) ([]HookDelivery, error) {
	var out []HookDelivery
	return out, a.asApp(ctx, http.MethodGet, fmt.Sprintf("/app/hook/deliveries?per_page=%d", n), nil, &out)
}

var repoRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})/[A-Za-z0-9._-]{1,100}$`)

// ValidRepo reports whether s is an "owner/name" repository.
func ValidRepo(s string) bool {
	if !repoRe.MatchString(s) {
		return false
	}
	name := s[strings.IndexByte(s, '/')+1:]
	return name != "." && name != ".." && !strings.HasSuffix(name, ".git")
}

func repoPath(fullName string) (string, error) {
	if !ValidRepo(fullName) {
		return "", fmt.Errorf("%q is not an owner/name repository", fullName)
	}
	return "/repos/" + fullName, nil
}

// RepoInstallation finds the installation that can reach a repository.
func (a *App) RepoInstallation(ctx context.Context, fullName string) (int64, error) {
	p, err := repoPath(fullName)
	if err != nil {
		return 0, err
	}
	var i Installation
	if err := a.asApp(ctx, http.MethodGet, p+"/installation", nil, &i); err != nil {
		return 0, err
	}
	return i.ID, nil
}

// InstallationToken returns a token for an installation with all the
// app's permissions, cached until five minutes before it expires.
func (a *App) InstallationToken(ctx context.Context, installation int64) (string, error) {
	a.mu.Lock()
	t, ok := a.tokens[installation]
	a.mu.Unlock()
	if ok && a.c.now().Before(t.expires.Add(-5*time.Minute)) {
		return t.value, nil
	}
	v, exp, err := a.mintToken(ctx, installation, nil)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	a.tokens[installation] = token{v, exp}
	a.mu.Unlock()
	return v, nil
}

// CloneToken mints a short-lived token that can only read one
// repository's contents. Revoke it once the clone is done.
func (a *App) CloneToken(ctx context.Context, installation int64, fullName string) (string, error) {
	if !ValidRepo(fullName) {
		return "", fmt.Errorf("%q is not an owner/name repository", fullName)
	}
	name := fullName[strings.IndexByte(fullName, '/')+1:]
	v, _, err := a.mintToken(ctx, installation, map[string]any{
		"repositories": []string{name},
		"permissions":  map[string]string{"contents": "read"},
	})
	return v, err
}

func (a *App) mintToken(ctx context.Context, installation int64, body map[string]any) (string, time.Time, error) {
	var r struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	var b any
	if body != nil {
		b = body
	}
	if err := a.asApp(ctx, http.MethodPost, fmt.Sprintf("/app/installations/%d/access_tokens", installation), b, &r); err != nil {
		return "", time.Time{}, err
	}
	if r.Token == "" {
		return "", time.Time{}, errors.New("GitHub returned no installation token")
	}
	if r.ExpiresAt.IsZero() {
		r.ExpiresAt = a.c.now().Add(time.Hour)
	}
	return r.Token, r.ExpiresAt, nil
}

// RevokeToken revokes an installation token (best effort).
func (a *App) RevokeToken(ctx context.Context, tok string) error {
	return a.c.do(ctx, http.MethodDelete, "/installation/token", "token "+tok, nil, nil)
}

// Forget drops cached tokens (an installation was removed).
func (a *App) Forget(installation int64) {
	a.mu.Lock()
	delete(a.tokens, installation)
	a.mu.Unlock()
}

func (a *App) asInstallation(ctx context.Context, installation int64, method, path string, body, out any) error {
	tok, err := a.InstallationToken(ctx, installation)
	if err != nil {
		return err
	}
	err = a.c.do(ctx, method, path, "token "+tok, body, out)
	if IsStatus(err, http.StatusUnauthorized) {
		a.Forget(installation) // revoked or expired early: one fresh try
		if tok, err = a.InstallationToken(ctx, installation); err != nil {
			return err
		}
		err = a.c.do(ctx, method, path, "token "+tok, body, out)
	}
	return err
}

// Repo is one repository the app can reach.
type Repo struct {
	ID            int64     `json:"id"`
	FullName      string    `json:"full_name"`
	Name          string    `json:"name"`
	Owner         Account   `json:"owner"`
	Private       bool      `json:"private"`
	Fork          bool      `json:"fork"`
	Archived      bool      `json:"archived"`
	Description   string    `json:"description"`
	DefaultBranch string    `json:"default_branch"`
	HTMLURL       string    `json:"html_url"`
	PushedAt      time.Time `json:"pushed_at"`
}

// Account is a user or organization.
type Account struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

// MaxRepos caps how many repositories one installation lists.
const MaxRepos = 1000

// Repos lists the repositories an installation can reach (at most MaxRepos).
func (a *App) Repos(ctx context.Context, installation int64) ([]Repo, error) {
	var out []Repo
	for page := 1; len(out) < MaxRepos; page++ {
		var r struct {
			Total int    `json:"total_count"`
			Repos []Repo `json:"repositories"`
		}
		if err := a.asInstallation(ctx, installation, http.MethodGet, fmt.Sprintf("/installation/repositories?per_page=100&page=%d", page), nil, &r); err != nil {
			return nil, err
		}
		out = append(out, r.Repos...)
		if len(r.Repos) < 100 || len(out) >= r.Total {
			break
		}
	}
	return out, nil
}

// Repo fetches one repository.
func (a *App) Repo(ctx context.Context, installation int64, fullName string) (*Repo, error) {
	p, err := repoPath(fullName)
	if err != nil {
		return nil, err
	}
	var r Repo
	return &r, a.asInstallation(ctx, installation, http.MethodGet, p, nil, &r)
}

// Branches lists a repository's branch names (at most 300).
func (a *App) Branches(ctx context.Context, installation int64, fullName string) ([]string, error) {
	p, err := repoPath(fullName)
	if err != nil {
		return nil, err
	}
	var out []string
	for page := 1; page <= 3; page++ {
		var batch []struct {
			Name string `json:"name"`
		}
		if err := a.asInstallation(ctx, installation, http.MethodGet, fmt.Sprintf("%s/branches?per_page=100&page=%d", p, page), nil, &batch); err != nil {
			return nil, err
		}
		for _, b := range batch {
			out = append(out, b.Name)
		}
		if len(batch) < 100 {
			break
		}
	}
	return out, nil
}

// TreeEntry is one file or directory of a repository.
type TreeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"` // blob or tree
	Size int64  `json:"size"`
}

// Tree lists every file of a commit, branch or tag (GitHub truncates very
// large trees; truncated says so).
func (a *App) Tree(ctx context.Context, installation int64, fullName, ref string) ([]TreeEntry, bool, error) {
	p, err := repoPath(fullName)
	if err != nil {
		return nil, false, err
	}
	var r struct {
		Tree      []TreeEntry `json:"tree"`
		Truncated bool        `json:"truncated"`
	}
	err = a.asInstallation(ctx, installation, http.MethodGet, p+"/git/trees/"+url.PathEscape(ref)+"?recursive=1", nil, &r)
	return r.Tree, r.Truncated, err
}

// File reads one file (at most 1 MB) at a ref.
func (a *App) File(ctx context.Context, installation int64, fullName, ref, path string) ([]byte, error) {
	p, err := repoPath(fullName)
	if err != nil {
		return nil, err
	}
	var segs []string
	for _, s := range strings.Split(strings.Trim(path, "/"), "/") {
		segs = append(segs, url.PathEscape(s))
	}
	var r struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		Type     string `json:"type"`
	}
	if err := a.asInstallation(ctx, installation, http.MethodGet, p+"/contents/"+strings.Join(segs, "/")+"?ref="+url.QueryEscape(ref), nil, &r); err != nil {
		return nil, err
	}
	if r.Type != "file" || r.Encoding != "base64" {
		return nil, fmt.Errorf("%s is not a file", path)
	}
	return base64.StdEncoding.DecodeString(strings.ReplaceAll(r.Content, "\n", ""))
}

// CompareLimit is the most files GitHub's compare lists: a comparison
// that reaches it may have changed more.
const CompareLimit = 300

// ChangedFiles lists the files that differ between base and head (as
// GitHub compares them: from their merge base), renamed files under both
// names. complete is false when GitHub listed only part of them.
func (a *App) ChangedFiles(ctx context.Context, installation int64, fullName, base, head string) (files []string, complete bool, err error) {
	p, err := repoPath(fullName)
	if err != nil {
		return nil, false, err
	}
	var r struct {
		Files []struct {
			Filename         string `json:"filename"`
			PreviousFilename string `json:"previous_filename"`
		} `json:"files"`
	}
	if err := a.asInstallation(ctx, installation, http.MethodGet, p+"/compare/"+url.PathEscape(base)+"..."+url.PathEscape(head)+"?per_page=1", nil, &r); err != nil {
		return nil, false, err
	}
	for _, f := range r.Files {
		files = append(files, f.Filename)
		if f.PreviousFilename != "" {
			files = append(files, f.PreviousFilename)
		}
	}
	return files, len(r.Files) < CompareLimit, nil
}

// Compare says where head stands against base: "identical", "ahead"
// (head has commits base lacks, base is in its history), "behind" (head
// is in base's history) or "diverged".
func (a *App) Compare(ctx context.Context, installation int64, fullName, base, head string) (string, error) {
	p, err := repoPath(fullName)
	if err != nil {
		return "", err
	}
	var r struct {
		Status string `json:"status"`
	}
	if err := a.asInstallation(ctx, installation, http.MethodGet, p+"/compare/"+url.PathEscape(base)+"..."+url.PathEscape(head)+"?per_page=1", nil, &r); err != nil {
		return "", err
	}
	if r.Status == "" {
		return "", errors.New("GitHub's comparison has no status")
	}
	return r.Status, nil
}

// Commit is a commit's head line.
type Commit struct {
	SHA     string `json:"sha"`
	Message string `json:"message"`
	Author  string `json:"author"` // GitHub login, or the git author name
}

// Commit resolves a branch, tag or SHA to its commit.
func (a *App) Commit(ctx context.Context, installation int64, fullName, ref string) (*Commit, error) {
	p, err := repoPath(fullName)
	if err != nil {
		return nil, err
	}
	var r struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message string `json:"message"`
			Author  struct {
				Name string `json:"name"`
			} `json:"author"`
		} `json:"commit"`
		Author *struct {
			Login string `json:"login"`
		} `json:"author"`
	}
	if err := a.asInstallation(ctx, installation, http.MethodGet, p+"/commits/"+url.PathEscape(ref), nil, &r); err != nil {
		return nil, err
	}
	c := &Commit{SHA: r.SHA, Message: r.Commit.Message, Author: r.Commit.Author.Name}
	if r.Author != nil && r.Author.Login != "" {
		c.Author = r.Author.Login
	}
	return c, nil
}

// Status is a commit status.
type Status struct {
	State       string `json:"state"` // pending, success, failure, error
	TargetURL   string `json:"target_url,omitempty"`
	Description string `json:"description,omitempty"`
	Context     string `json:"context"`
}

// SetStatus sets a commit status.
func (a *App) SetStatus(ctx context.Context, installation int64, fullName, sha string, s Status) error {
	p, err := repoPath(fullName)
	if err != nil {
		return err
	}
	s.Description = Clip(s.Description, 140)
	return a.retry(ctx, func() error {
		return a.asInstallation(ctx, installation, http.MethodPost, p+"/statuses/"+url.PathEscape(sha), s, nil)
	})
}

// retryDelays space out the retries of a write that is safe to repeat.
var retryDelays = []time.Duration{time.Second, 4 * time.Second}

// retry runs a write that is safe to repeat (a commit status, a deployment
// status: the latest wins) again when GitHub fails it with a 5xx or the
// request doesn't arrive, so one hiccup doesn't leave a commit pending
// forever.
func (a *App) retry(ctx context.Context, f func() error) error {
	err := f()
	for _, d := range retryDelays {
		var ae *APIError
		if err == nil || errors.As(err, &ae) && ae.Status < 500 || ctx.Err() != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(d):
		}
		err = f()
	}
	return err
}

// DeploymentRequest creates a GitHub deployment.
type DeploymentRequest struct {
	Ref                   string   `json:"ref"`
	Environment           string   `json:"environment"`
	Description           string   `json:"description,omitempty"`
	AutoMerge             bool     `json:"auto_merge"`
	RequiredContexts      []string `json:"required_contexts"`
	TransientEnvironment  bool     `json:"transient_environment"`
	ProductionEnvironment bool     `json:"production_environment"`
}

// CreateDeployment creates a deployment of a commit and returns its id.
func (a *App) CreateDeployment(ctx context.Context, installation int64, fullName string, d DeploymentRequest) (int64, error) {
	p, err := repoPath(fullName)
	if err != nil {
		return 0, err
	}
	if d.RequiredContexts == nil {
		d.RequiredContexts = []string{} // don't wait for other checks: the box already decided to deploy
	}
	d.Description = Clip(d.Description, 140)
	var r struct {
		ID int64 `json:"id"`
	}
	if err := a.asInstallation(ctx, installation, http.MethodPost, p+"/deployments", d, &r); err != nil {
		return 0, err
	}
	return r.ID, nil
}

// DeploymentStatus is one step of a deployment.
type DeploymentStatus struct {
	State          string `json:"state"` // in_progress, success, failure, error, inactive
	EnvironmentURL string `json:"environment_url,omitempty"`
	LogURL         string `json:"log_url,omitempty"`
	Description    string `json:"description,omitempty"`
	AutoInactive   bool   `json:"auto_inactive"`
}

// SetDeploymentStatus records a deployment's state.
func (a *App) SetDeploymentStatus(ctx context.Context, installation int64, fullName string, id int64, s DeploymentStatus) error {
	p, err := repoPath(fullName)
	if err != nil {
		return err
	}
	s.Description = Clip(s.Description, 140)
	return a.retry(ctx, func() error {
		return a.asInstallation(ctx, installation, http.MethodPost, fmt.Sprintf("%s/deployments/%d/statuses", p, id), s, nil)
	})
}

// UpsertComment updates comment id on a pull request (or creates one when
// id is 0 or the comment is gone) and returns its id.
func (a *App) UpsertComment(ctx context.Context, installation int64, fullName string, pr int, id int64, body string) (int64, error) {
	p, err := repoPath(fullName)
	if err != nil {
		return 0, err
	}
	var r struct {
		ID int64 `json:"id"`
	}
	if id != 0 {
		err := a.asInstallation(ctx, installation, http.MethodPatch, fmt.Sprintf("%s/issues/comments/%d", p, id), map[string]string{"body": body}, &r)
		if err == nil {
			return id, nil
		}
		if !IsStatus(err, http.StatusNotFound) {
			return 0, err
		}
	}
	if err := a.asInstallation(ctx, installation, http.MethodPost, fmt.Sprintf("%s/issues/%d/comments", p, pr), map[string]string{"body": body}, &r); err != nil {
		return 0, err
	}
	return r.ID, nil
}

// Clip shortens s to n characters (GitHub caps descriptions at 140).
func Clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// ---- installing a shared app ----

// UserRepos checks what a person who just installed the app may do in
// one installation: it exchanges the OAuth code GitHub sends with the
// post-install redirect (when the app requests user authorization during
// installation) for a user token, lists the installation's repositories
// that person can push to and revokes the token. A box on a shared app
// acts only on those repositories: seeing an installation proves little
// (a collaborator on one repository sees the whole organization's
// installation), so the box never reaches the rest of it.
func (a *App) UserRepos(ctx context.Context, code string, installation int64) ([]Repo, error) {
	if !codeRe.MatchString(code) {
		return nil, errors.New("the code from GitHub is not valid")
	}
	if a.cred.ClientID == "" || a.cred.ClientSecret == "" {
		return nil, errors.New("the app's client id and secret are not configured")
	}
	form := url.Values{"client_id": {a.cred.ClientID}, "client_secret": {a.cred.ClientSecret}, "code": {code}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.c.E.web("/login/oauth/access_token"), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", a.c.UserAgent)
	res, err := a.c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub OAuth: %w", err)
	}
	defer res.Body.Close()
	var tr struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&tr); err != nil {
		return nil, fmt.Errorf("GitHub OAuth: unexpected response: %w", err)
	}
	if tr.AccessToken == "" {
		return nil, fmt.Errorf("GitHub OAuth: %s", orStr(tr.Description, orStr(tr.Error, "no token")))
	}
	defer a.revokeUserToken(context.WithoutCancel(ctx), tr.AccessToken)
	var out []Repo
	for page := 1; page <= MaxRepos/100; page++ {
		var r struct {
			Repos []struct {
				Repo
				Permissions struct {
					Admin bool `json:"admin"`
					Push  bool `json:"push"`
				} `json:"permissions"`
			} `json:"repositories"`
		}
		if err := a.c.do(ctx, http.MethodGet, fmt.Sprintf("/user/installations/%d/repositories?per_page=100&page=%d", installation, page), "token "+tr.AccessToken, nil, &r); err != nil {
			return nil, err
		}
		for _, rp := range r.Repos {
			if rp.Permissions.Admin || rp.Permissions.Push {
				out = append(out, rp.Repo)
			}
		}
		if len(r.Repos) < 100 {
			break
		}
	}
	return out, nil
}

// revokeUserToken drops a user token at once: the box only needed it to look.
func (a *App) revokeUserToken(ctx context.Context, tok string) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"access_token": tok})
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, a.c.E.api("/applications/"+url.PathEscape(a.cred.ClientID)+"/token"), bytes.NewReader(body))
	if err != nil {
		return
	}
	req.SetBasicAuth(a.cred.ClientID, a.cred.ClientSecret)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", a.c.UserAgent)
	if res, err := a.c.HTTP.Do(req); err == nil {
		res.Body.Close()
	}
}

func orStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
