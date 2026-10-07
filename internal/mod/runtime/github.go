package runtime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btahir/tiffin/internal/mod/runtime/ghapp"
)

// Connecting the box to GitHub. A box gets its own GitHub App through the
// manifest flow (Settings › Git › Connect GitHub): the box describes the app,
// the owner confirms it on GitHub, GitHub hands the box the app's key and
// webhook secret, and the box keeps them sealed with its own key. A hosted
// box can instead be pointed at one shared app (tiffin github use-app, or a
// settings file the operator writes); everything after that runs the same
// code. A shared (public) app is installed by many accounts, so such a box
// only acts for installations its admin proved access to when installing.

const (
	nsGitHub           = "github"
	nsGitHubDeliveries = "github/deliveries"
	nsGitHubJobs       = "github/jobs"

	// Connection sources.
	ghSourceBox        = "box"        // created by this box through the manifest flow
	ghSourceConfigured = "configured" // an existing app given to the box (github use-app)
	ghSourceFile       = "file"       // the operator's settings file

	ghStateTTL = time.Hour
)

// ghSettingsPath is the operator's settings file: GitHub Enterprise
// endpoints and/or a shared app. TIFFIN_GITHUB_CONFIG overrides it.
func (r *rt) ghSettingsPath() string {
	if p := os.Getenv("TIFFIN_GITHUB_CONFIG"); p != "" {
		return p
	}
	home := ""
	if r.p != nil {
		home = r.p.Home
	}
	return filepath.Join(home, "github.json")
}

// ghSettings is the settings file. Every field is optional.
//
//	{
//	  "apiUrl": "https://api.github.com", "webUrl": "https://github.com",
//	  "app": { "id": 123, "privateKeyFile": "/etc/tiffin/github-app.pem",
//	           "webhookSecretFile": "...", "clientId": "Iv1...", "clientSecretFile": "...",
//	           "public": true }
//	}
type ghSettings struct {
	ghapp.Endpoints
	App *struct {
		ID                int64  `json:"id"`
		Slug              string `json:"slug"`
		PrivateKey        string `json:"privateKey"`
		PrivateKeyFile    string `json:"privateKeyFile"`
		WebhookSecret     string `json:"webhookSecret"`
		WebhookSecretFile string `json:"webhookSecretFile"`
		ClientID          string `json:"clientId"`
		ClientSecret      string `json:"clientSecret"`
		ClientSecretFile  string `json:"clientSecretFile"`
		Public            bool   `json:"public"`
	} `json:"app"`
}

// ghStored is what the box keeps (sealed) for an app it created or was given.
type ghStored struct {
	Source string            `json:"source"`
	Public bool              `json:"public"`
	Cred   ghapp.Credentials `json:"cred"`
}

// ghConn is a working connection to GitHub.
type ghConn struct {
	Source string
	Public bool
	E      ghapp.Endpoints
	App    *ghapp.App
	Cred   ghapp.Credentials
	stamp  string
}

// ghState is the runtime's GitHub bookkeeping in memory.
type ghState struct {
	mu    sync.Mutex
	conn  *ghConn
	repos map[int64]cachedRepos
	// HTTP is the client for GitHub (tests and GitHub Enterprise with a
	// private CA set their own).
	HTTP *http.Client
	q    ghQueue
	// inflight are the deliveries being handled (by deliveryKey).
	inflight map[string]bool
	// reporting: failed final reports are being retried.
	reporting atomic.Bool
}

type cachedRepos struct {
	at    time.Time
	repos []ghapp.Repo
}

func readSecretFile(inline, file string) (string, error) {
	if inline != "" || file == "" {
		return strings.TrimSpace(inline), nil
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// loadSettings reads the settings file (absent is fine).
func (r *rt) loadSettings() (*ghSettings, string, error) {
	path := r.ghSettingsPath()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &ghSettings{Endpoints: ghapp.GitHubCom}, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	var s ghSettings
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, "", fmt.Errorf("%s: %w", path, err)
	}
	if s.API == "" {
		s.API = ghapp.GitHubCom.API
	}
	if s.Web == "" {
		s.Web = ghapp.GitHubCom.Web
	}
	for _, u := range []string{s.API, s.Web} {
		if pu, err := url.Parse(u); err != nil || pu.Scheme != "https" || pu.Host == "" {
			return nil, "", fmt.Errorf("%s: GitHub URLs must be https:// URLs (got %q)", path, u)
		}
	}
	sum := sha256.Sum256(raw)
	return &s, hex.EncodeToString(sum[:8]), nil
}

// endpoints are the GitHub this box talks to.
func (r *rt) ghEndpoints() ghapp.Endpoints {
	s, _, err := r.loadSettings()
	if err != nil {
		return ghapp.GitHubCom
	}
	return s.Endpoints
}

func (r *rt) ghClient(e ghapp.Endpoints) *ghapp.Client {
	c := ghapp.NewClient(e, r.gh.HTTP)
	c.UserAgent = "tiffin/" + orDefaultStr(r.p.Version, "dev")
	return c
}

// errNoGitHub means the box is not connected to GitHub.
var errNoGitHub = &stateError{"this box is not connected to GitHub", "Connect it in the dashboard: Settings › Git › Connect GitHub (or tiffin github connect)."}

// github returns the box's GitHub connection (errNoGitHub when there is none).
func (r *rt) github(ctx context.Context) (*ghConn, error) {
	s, fileStamp, err := r.loadSettings()
	if err != nil {
		return nil, err
	}
	var src ghStored
	stamp := ""
	if s.App != nil {
		key, err := readSecretFile(s.App.PrivateKey, s.App.PrivateKeyFile)
		if err != nil {
			return nil, fmt.Errorf("github settings: private key: %w", err)
		}
		hook, err := readSecretFile(s.App.WebhookSecret, s.App.WebhookSecretFile)
		if err != nil {
			return nil, fmt.Errorf("github settings: webhook secret: %w", err)
		}
		cs, err := readSecretFile(s.App.ClientSecret, s.App.ClientSecretFile)
		if err != nil {
			return nil, fmt.Errorf("github settings: client secret: %w", err)
		}
		src = ghStored{Source: ghSourceFile, Public: s.App.Public, Cred: ghapp.Credentials{ID: s.App.ID, Slug: s.App.Slug, PrivateKey: key,
			WebhookSecret: hook, ClientID: s.App.ClientID, ClientSecret: cs}}
		sum := sha256.Sum256([]byte(fileStamp + key + hook + cs))
		stamp = "file:" + hex.EncodeToString(sum[:8])
	} else {
		sealed, ok, err := r.p.DB.KVGet(ctx, nsGitHub, "app")
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errNoGitHub
		}
		sum := sha256.Sum256(sealed)
		stamp = "kv:" + fileStamp + hex.EncodeToString(sum[:8])
		r.gh.mu.Lock()
		cur := r.gh.conn
		r.gh.mu.Unlock()
		if cur != nil && cur.stamp == stamp {
			return cur, nil
		}
		plain, err := r.p.Secrets.Open(sealed)
		if err != nil {
			return nil, fmt.Errorf("github app credentials: %w", err)
		}
		if err := json.Unmarshal(plain, &src); err != nil {
			return nil, err
		}
	}
	r.gh.mu.Lock()
	cur := r.gh.conn
	r.gh.mu.Unlock()
	if cur != nil && cur.stamp == stamp {
		return cur, nil
	}
	if src.Cred.ID <= 0 || src.Cred.PrivateKey == "" || src.Cred.WebhookSecret == "" {
		return nil, &stateError{"the GitHub App settings are incomplete (they need the app id, private key and webhook secret)", "Fix " + r.ghSettingsPath() + ", or run tiffin github use-app again."}
	}
	app, err := r.ghClient(s.Endpoints).NewApp(src.Cred)
	if err != nil {
		return nil, fmt.Errorf("github app: %w", err)
	}
	c := &ghConn{Source: src.Source, Public: src.Public, E: s.Endpoints, App: app, Cred: src.Cred, stamp: stamp}
	r.gh.mu.Lock()
	r.gh.conn, r.gh.repos = c, map[int64]cachedRepos{}
	r.gh.mu.Unlock()
	return c, nil
}

// storeApp seals and keeps an app's credentials.
func (r *rt) storeApp(ctx context.Context, st ghStored) error {
	plain, err := json.Marshal(st)
	if err != nil {
		return err
	}
	sealed, err := r.p.Secrets.Seal(plain)
	if err != nil {
		return err
	}
	if err := r.p.DB.KVPut(ctx, nsGitHub, "app", sealed); err != nil {
		return err
	}
	_ = r.p.DB.KVDelete(ctx, nsGitHub, "bound")
	return nil
}

// ---- one-time states for the browser round trips ----

type ghPending struct {
	Kind string    `json:"kind"` // manifest or install
	By   string    `json:"by"`
	Org  string    `json:"org,omitempty"`
	At   time.Time `json:"at"`
}

func (r *rt) issueState(ctx context.Context, p ghPending) (string, error) {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	n := hex.EncodeToString(b)
	p.At = time.Now().UTC()
	raw, _ := json.Marshal(p)
	if err := r.p.DB.KVPut(ctx, nsGitHub, "state/"+n, raw); err != nil {
		return "", err
	}
	// Forget states nobody came back with.
	if all, err := r.p.DB.KVList(ctx, nsGitHub); err == nil {
		for k, v := range all {
			var old ghPending
			if strings.HasPrefix(k, "state/") && json.Unmarshal(v, &old) == nil && time.Since(old.At) > ghStateTTL {
				_ = r.p.DB.KVDelete(ctx, nsGitHub, k)
			}
		}
	}
	return n, nil
}

// takeState returns and forgets a state (single use).
func (r *rt) takeState(ctx context.Context, n, kind string) (*ghPending, bool) {
	if len(n) != 40 {
		return nil, false
	}
	if _, err := hex.DecodeString(n); err != nil {
		return nil, false
	}
	raw, ok, err := r.p.DB.KVGet(ctx, nsGitHub, "state/"+n)
	if err != nil || !ok {
		return nil, false
	}
	_ = r.p.DB.KVDelete(ctx, nsGitHub, "state/"+n)
	var p ghPending
	if json.Unmarshal(raw, &p) != nil || p.Kind != kind || time.Since(p.At) > ghStateTTL {
		return nil, false
	}
	return &p, true
}

// ---- installations ----

// ghBound is what a box on a shared app may act on: per installation, the
// repositories (ids) its admin proved they can push to when installing.
type ghBound map[int64][]int64

func (r *rt) bound(ctx context.Context) ghBound {
	raw, ok, _ := r.p.DB.KVGet(ctx, nsGitHub, "bound")
	b := ghBound{}
	if ok {
		_ = json.Unmarshal(raw, &b)
	}
	return b
}

func (r *rt) setBound(ctx context.Context, b ghBound) error {
	raw, _ := json.Marshal(b)
	return r.p.DB.KVPut(ctx, nsGitHub, "bound", raw)
}

// installations lists the installations this box acts for: all of its
// own app's, or the bound ones of a shared app.
func (r *rt) installations(ctx context.Context, c *ghConn) ([]ghapp.Installation, error) {
	all, err := c.App.Installations(ctx)
	if err != nil {
		return nil, err
	}
	if !c.Public {
		return all, nil
	}
	ok := r.bound(ctx)
	var out []ghapp.Installation
	for _, in := range all {
		if _, b := ok[in.ID]; b {
			out = append(out, in)
		}
	}
	return out, nil
}

// usable reports whether this box may act for an installation at all.
func (r *rt) usable(ctx context.Context, c *ghConn, installation int64) bool {
	if installation <= 0 {
		return false
	}
	if !c.Public {
		return true
	}
	_, ok := r.bound(ctx)[installation]
	return ok
}

// repoUsable reports whether this box may act on a repository (by id)
// through an installation: a shared app's box only on bound repositories.
func (r *rt) repoUsable(ctx context.Context, c *ghConn, installation, repo int64) bool {
	if installation <= 0 {
		return false
	}
	return !c.Public || slices.Contains(r.bound(ctx)[installation], repo)
}

// repoInstallation finds the installation this box uses for a repository.
func (r *rt) repoInstallation(ctx context.Context, c *ghConn, repo string) (int64, error) {
	id, err := c.App.RepoInstallation(ctx, repo)
	if ghapp.IsStatus(err, http.StatusNotFound) {
		return 0, &stateError{"the box's GitHub App is not installed on " + repo, "Install it on the repository: Settings › Git › Install on repositories."}
	}
	if err != nil {
		return 0, err
	}
	denied := &stateError{"this box has not been given access to " + repo, "Install the app on it from this box (Settings › Git › Install on repositories), signed in to GitHub as someone who can push to it."}
	if !c.Public {
		return id, nil
	}
	if !r.usable(ctx, c, id) {
		return 0, denied
	}
	rp, err := c.App.Repo(ctx, id, repo)
	if err != nil {
		return 0, err
	}
	if !r.repoUsable(ctx, c, id, rp.ID) {
		return 0, denied
	}
	return id, nil
}

// ---- where GitHub reaches the box ----

func (r *rt) publicBase() string { return strings.TrimRight(r.p.PublicURL, "/") }

func (r *rt) ghURLs() ghapp.ManifestURLs {
	b := r.publicBase()
	return ghapp.ManifestURLs{Home: b, Webhook: b + "/v1/github/webhook", Callback: b + "/v1/github/manifest/callback", Setup: b + "/v1/github/setup"}
}

// reachable says whether GitHub can deliver webhooks to the box's
// address: github.com cannot reach a .localhost name or a private address
// (GitHub Enterprise on the same network can).
func (r *rt) reachable(e ghapp.Endpoints) (bool, string) {
	u, err := url.Parse(r.p.PublicURL)
	if err != nil || u.Hostname() == "" {
		return false, "The box has no public address yet."
	}
	if !e.IsGitHubCom() {
		return true, ""
	}
	h := strings.ToLower(u.Hostname())
	private := h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") ||
		strings.HasSuffix(h, ".internal") || strings.HasSuffix(h, ".test") || strings.HasSuffix(h, ".lan") || !strings.Contains(h, ".")
	if ip, err := netip.ParseAddr(h); err == nil {
		private = !publicIP(ip)
	}
	if private {
		return false, fmt.Sprintf("GitHub can't reach %s from the internet, so pushes would never arrive. Give the box a public domain first (Settings › Your box › Moving it).", u.Host)
	}
	return true, ""
}

// appName is the default name of a box's app: tiffin-<box>.
func (r *rt) appName() string {
	d := strings.ToLower(r.p.Domain)
	labels := strings.Split(d, ".")
	if len(labels) > 1 {
		labels = labels[:len(labels)-1]
	}
	var b strings.Builder
	b.WriteString("tiffin")
	for _, l := range labels {
		if l == "" || l == "tiffin" {
			continue
		}
		b.WriteByte('-')
		b.WriteString(l)
	}
	n := strings.Trim(b.String(), "-")
	if len(n) > 34 {
		n = strings.Trim(n[:34], "-")
	}
	return n
}

// ---- the deliveries and events log ----

// GitHubEvent is one thing the box did because of GitHub.
type GitHubEvent struct {
	At      time.Time `json:"at"`
	Event   string    `json:"event" doc:"push, pull_request, installation, connect..."`
	Repo    string    `json:"repo,omitempty"`
	Summary string    `json:"summary" doc:"What happened, in plain words"`
	OK      bool      `json:"ok"`
}

const maxGHEvents = 30

func (r *rt) logEvent(ctx context.Context, ev GitHubEvent) {
	ev.At = time.Now().UTC()
	r.gh.mu.Lock()
	defer r.gh.mu.Unlock()
	raw, _, _ := r.p.DB.KVGet(ctx, nsGitHub, "log")
	var evs []GitHubEvent
	_ = json.Unmarshal(raw, &evs)
	evs = append([]GitHubEvent{ev}, evs...)
	if len(evs) > maxGHEvents {
		evs = evs[:maxGHEvents]
	}
	b, _ := json.Marshal(evs)
	_ = r.p.DB.KVPut(ctx, nsGitHub, "log", b)
	if r.p.Log != nil {
		r.p.Log.Info("github", "event", ev.Event, "repo", ev.Repo, "summary", ev.Summary)
	}
}

func (r *rt) events(ctx context.Context) []GitHubEvent {
	raw, _, _ := r.p.DB.KVGet(ctx, nsGitHub, "log")
	evs := []GitHubEvent{}
	_ = json.Unmarshal(raw, &evs)
	return evs
}

// deliveryKey identifies a delivery by its signed content: a replay
// under a fresh X-GitHub-Delivery id (the header is not signed) is the same
// delivery, and GitHub's own redeliveries carry the same body.
func deliveryKey(event string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(event + "\n"))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// claimDelivery reports whether a delivery was already handled or is being
// handled right now; otherwise it claims it. done(true) records it as
// handled; done(false) lets a redelivery try again (it failed on the box's
// side, a database error say, and GitHub shows it as failed).
func (r *rt) claimDelivery(ctx context.Context, key string) (dup bool, done func(handled bool), err error) {
	r.gh.mu.Lock()
	if r.gh.inflight[key] {
		r.gh.mu.Unlock()
		return true, nil, nil
	}
	if r.gh.inflight == nil {
		r.gh.inflight = map[string]bool{}
	}
	r.gh.inflight[key] = true
	r.gh.mu.Unlock()
	release := func() {
		r.gh.mu.Lock()
		delete(r.gh.inflight, key)
		r.gh.mu.Unlock()
	}
	if _, ok, err := r.p.DB.KVGet(ctx, nsGitHubDeliveries, key); err != nil || ok {
		release()
		return ok, nil, err
	}
	return false, func(handled bool) {
		defer release()
		if !handled {
			return
		}
		now := time.Now().UTC()
		_ = r.p.DB.KVPut(ctx, nsGitHubDeliveries, key, []byte(now.Format(time.RFC3339)))
		// Keep a day of deliveries; older replays fail the age check anyway.
		if now.Unix()%16 == 0 {
			if all, err := r.p.DB.KVList(ctx, nsGitHubDeliveries); err == nil {
				for k, v := range all {
					if t, err := time.Parse(time.RFC3339, string(v)); err != nil || now.Sub(t) > 24*time.Hour {
						_ = r.p.DB.KVDelete(ctx, nsGitHubDeliveries, k)
					}
				}
			}
		}
	}, nil
}

// hostIPs resolves a GitHub host for the pinned clone.
func (r *rt) hostIPs(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip}, nil
	}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := r.resolve(rctx, host)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf("cannot resolve %s: %v", host, err)
	}
	return ips, nil
}
