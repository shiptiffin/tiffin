// Package ghfake is a stand-in GitHub for tests: the slice of the REST API
// and website a Tiffin box uses (the app manifest flow, app JWTs checked
// against the app's key, installation tokens, repositories, trees,
// commits, statuses, deployments, pull request comments, OAuth for
// installation checks), git smart HTTP for the repositories (behind
// installation tokens), and signed webhook deliveries. Repositories are
// real bare git repositories on disk.
package ghfake

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/cgi"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/mod/runtime/ghapp"
)

// Server is a fake GitHub.
type Server struct {
	// URL is where the server is reached (set it after starting the listener).
	URL string
	// HookURL receives webhook deliveries (default: the app manifest's hook URL).
	HookURL string
	// AppHookURL is the app's webhook URL as GitHub reports it (default:
	// the manifest's), for an app whose webhook points elsewhere.
	AppHookURL string
	// HookClient sends deliveries (default http.DefaultClient).
	HookClient *http.Client
	// Account owns new apps and installations.
	Account string
	// Installer is the access (full name → "read" or "write") of the person
	// who installs a public app through the website (nil: push to all).
	Installer map[string]string

	root    string
	backend string

	mu            sync.Mutex
	app           *appState
	codes         map[string]ghapp.Manifest // manifest code → manifest
	oauthCodes    map[string]userGrant      // oauth code → the person it signs in
	installations map[int64]*installation
	repos         map[string]*repo
	tokens        map[string]*tok
	nextID        int64
	// Calls the box made, for assertions: read them with Recorded, which
	// copies under the lock (the box keeps calling while a test reads).
	statuses           []Call
	deployments        []Call
	deploymentStatuses []Call
	comments           map[int64]*Comment
	clones             []string // repositories cloned (full names)
	deliveries         []Delivery
}

// Recorded is a copy of the calls the box made so far.
type Recorded struct {
	Statuses           []Call             `json:"statuses"`
	Deployments        []Call             `json:"deployments"`
	DeploymentStatuses []Call             `json:"deploymentStatuses"`
	Comments           map[int64]*Comment `json:"-"`
	Clones             []string           `json:"clones"`
	Deliveries         []Delivery         `json:"deliveries"`
}

// Recorded copies what the box has called so far.
func (s *Server) Recorded() Recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := Recorded{Statuses: slices.Clone(s.statuses), Deployments: slices.Clone(s.deployments), DeploymentStatuses: slices.Clone(s.deploymentStatuses),
		Comments: make(map[int64]*Comment, len(s.comments)), Clones: slices.Clone(s.clones), Deliveries: slices.Clone(s.deliveries)}
	for id, c := range s.comments {
		cp := *c
		r.Comments[id] = &cp
	}
	return r
}

type appState struct {
	id                     int64
	slug, name             string
	key                    *rsa.PrivateKey
	webhookSecret          string
	clientID, clientSecret string
	manifest               ghapp.Manifest
	public                 bool
}

type installation struct {
	id      int64
	account string
	repos   []string // nil: all repositories
}

type repo struct {
	id            int64
	fullName      string
	private       bool
	defaultBranch string
	bare, work    string
}

// userGrant is a person signed in through OAuth: the installation they
// came from and their access to its repositories (nil: push to all).
type userGrant struct {
	installation int64
	access       map[string]string // full name → "read" or "write"
}

type tok struct {
	installation int64
	user         *userGrant // a user token
	repos        []string   // nil: all the installation reaches
	readOnly     bool
	revoked      bool
	expires      time.Time
}

// Call is one write the box made.
type Call struct {
	Repo string         `json:"repo"`
	Path string         `json:"path"`
	Body map[string]any `json:"body"`
	At   time.Time      `json:"at"`
}

// Comment is a pull request comment.
type Comment struct {
	ID      int64  `json:"id"`
	Repo    string `json:"repo"`
	Issue   int    `json:"issue"`
	Body    string `json:"body"`
	Updates int    `json:"updates"`
}

// Delivery is a webhook sent to the box.
type Delivery struct {
	ID     string `json:"id"`
	Event  string `json:"event"`
	Status int    `json:"status"`
	Reply  string `json:"reply"`
}

// New returns a fake GitHub keeping repositories under root.
func New(root string) (*Server, error) {
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		return nil, fmt.Errorf("git: %w", err)
	}
	s := &Server{root: root, backend: filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend"), Account: "octo",
		codes: map[string]ghapp.Manifest{}, oauthCodes: map[string]userGrant{}, installations: map[int64]*installation{}, repos: map[string]*repo{},
		tokens: map[string]*tok{}, comments: map[int64]*Comment{}, nextID: 1000}
	for _, d := range []string{"bare", "work"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Endpoints are the fake's API and web URLs.
func (s *Server) Endpoints() ghapp.Endpoints {
	return ghapp.Endpoints{API: s.URL + "/api", Web: s.URL}
}

func (s *Server) id() int64 { s.nextID++; return s.nextID }

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// CreateApp registers an app directly (a shared app configured by hand)
// and returns its credentials.
func (s *Server) CreateApp(slug string, public bool) (ghapp.Credentials, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return ghapp.Credentials{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a := &appState{id: s.id(), slug: slug, name: slug, key: key, webhookSecret: randHex(20), clientID: "Iv1." + randHex(8), clientSecret: randHex(20), public: public}
	s.app = a
	return s.credsLocked(), nil
}

func (s *Server) credsLocked() ghapp.Credentials {
	a := s.app
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(a.key)})
	return ghapp.Credentials{ID: a.id, Slug: a.slug, Name: a.name, Owner: s.Account, OwnerType: "User", HTMLURL: s.URL + "/apps/" + a.slug,
		ClientID: a.clientID, ClientSecret: a.clientSecret, WebhookSecret: a.webhookSecret, PrivateKey: string(keyPEM)}
}

// Install installs the app on account for repos (nil: all) and returns the installation id.
func (s *Server) Install(account string, repos []string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := &installation{id: s.id(), account: account, repos: repos}
	s.installations[in.id] = in
	return in.id
}

// OAuthCode returns a code that signs in a person who can push to every
// repository of an installation (what GitHub sends after an install when
// the app requests user authorization).
func (s *Server) OAuthCode(installation int64) string { return s.UserCode(installation, nil) }

// UserCode returns a code that signs in a person with access (full name →
// "read" or "write") to some of an installation's repositories.
func (s *Server) UserCode(installation int64, access map[string]string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := randHex(10)
	s.oauthCodes[c] = userGrant{installation: installation, access: access}
	return c
}

// ReadTokens counts the read-only, single-repository tokens minted (clone
// tokens) and how many of them were revoked.
func (s *Server) ReadTokens() (minted, revoked int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tokens {
		if t.readOnly {
			minted++
			if t.revoked {
				revoked++
			}
		}
	}
	return minted, revoked
}

// ---- repositories ----

func (s *Server) git(dir string, args ...string) (string, error) {
	c := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Octo Cat", "-c", "user.email=octo@example.com", "-c", "init.defaultBranch=main", "-c", "commit.gpgsign=false"}, args...)...)
	c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := c.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// AddRepo creates a repository with files on its default branch "main"
// and returns the first commit.
func (s *Server) AddRepo(fullName string, private bool, files map[string]string) (string, error) {
	if !ghapp.ValidRepo(fullName) {
		return "", fmt.Errorf("bad repo %q", fullName)
	}
	flat := strings.ReplaceAll(fullName, "/", "__")
	r := &repo{fullName: fullName, private: private, defaultBranch: "main",
		bare: filepath.Join(s.root, "bare", flat+".git"), work: filepath.Join(s.root, "work", flat)}
	if err := os.MkdirAll(r.work, 0o755); err != nil {
		return "", err
	}
	if _, err := s.git(r.work, "init", "-q"); err != nil {
		return "", err
	}
	s.mu.Lock()
	r.id = s.id()
	s.repos[strings.ToLower(fullName)] = r
	s.mu.Unlock()
	sha, err := s.commitFiles(r, "main", files, "first commit")
	if err != nil {
		return "", err
	}
	if _, err := s.git(s.root, "clone", "-q", "--bare", r.work, r.bare); err != nil {
		return "", err
	}
	if _, err := s.git(r.bare, "config", "uploadpack.allowAnySHA1InWant", "true"); err != nil {
		return "", err
	}
	return sha, nil
}

func (s *Server) commitFiles(r *repo, branch string, files map[string]string, msg string) (string, error) {
	if out, _ := s.git(r.work, "branch", "--list", branch); out == "" {
		if head, _ := s.git(r.work, "rev-parse", "--verify", "-q", "HEAD"); head != "" {
			if _, err := s.git(r.work, "checkout", "-q", "-b", branch); err != nil {
				return "", err
			}
		}
	} else if _, err := s.git(r.work, "checkout", "-q", branch); err != nil {
		return "", err
	}
	for n, b := range files {
		p := filepath.Join(r.work, filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, []byte(b), 0o644); err != nil {
			return "", err
		}
	}
	if _, err := s.git(r.work, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := s.git(r.work, "commit", "-q", "--allow-empty", "-m", msg); err != nil {
		return "", err
	}
	return s.git(r.work, "rev-parse", "HEAD")
}

// Commit adds a commit with files to branch (created from the current
// branch when new), pushes it to the served repository and returns its SHA.
func (s *Server) Commit(fullName, branch string, files map[string]string, msg string) (string, error) {
	r := s.repo(fullName)
	if r == nil {
		return "", fmt.Errorf("no repo %s", fullName)
	}
	sha, err := s.commitFiles(r, branch, files, msg)
	if err != nil {
		return "", err
	}
	if _, err := s.git(r.work, "push", "-q", "-f", r.bare, branch+":refs/heads/"+branch); err != nil {
		return "", err
	}
	return sha, nil
}

func (s *Server) repo(fullName string) *repo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.repos[strings.ToLower(fullName)]
}

// ---- deliveries ----

// Deliver sends a signed webhook to the box and returns its status.
func (s *Server) Deliver(event string, payload any) (Delivery, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return Delivery{}, err
	}
	s.mu.Lock()
	secret, hook := "", s.HookURL
	if s.app != nil {
		secret = s.app.webhookSecret
		if hook == "" {
			hook = s.app.manifest.HookAttributes.URL
		}
	}
	s.mu.Unlock()
	return s.DeliverRaw(hook, event, randUUID(), body, ghapp.Sign(secret, body))
}

// DeliverRaw sends a delivery exactly as given (for signature and replay tests).
func (s *Server) DeliverRaw(hook, event, id string, body []byte, sig string) (Delivery, error) {
	req, err := http.NewRequest(http.MethodPost, hook, bytes.NewReader(body))
	if err != nil {
		return Delivery{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", id)
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("User-Agent", "GitHub-Hookshot/fake")
	hc := s.HookClient
	if hc == nil {
		hc = http.DefaultClient
	}
	res, err := hc.Do(req)
	if err != nil {
		return Delivery{}, err
	}
	defer res.Body.Close()
	reply, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	d := Delivery{ID: id, Event: event, Status: res.StatusCode, Reply: string(reply)}
	s.mu.Lock()
	s.deliveries = append(s.deliveries, d)
	s.mu.Unlock()
	return d, nil
}

func randUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func (s *Server) installationFor(fullName string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]int64, 0, len(s.installations))
	for id := range s.installations {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		if s.reachesLocked(s.installations[id], fullName) {
			return id
		}
	}
	return 0
}

func (s *Server) reachesLocked(in *installation, fullName string) bool {
	if in.repos == nil {
		return strings.EqualFold(strings.Split(fullName, "/")[0], in.account)
	}
	for _, r := range in.repos {
		if strings.EqualFold(r, fullName) {
			return true
		}
	}
	return false
}

func (s *Server) repoJSON(r *repo, unixPushed bool) map[string]any {
	owner, name, _ := strings.Cut(r.fullName, "/")
	m := map[string]any{"id": r.id, "full_name": r.fullName, "name": name, "private": r.private, "default_branch": r.defaultBranch,
		"owner": map[string]any{"login": owner, "type": "User"}, "html_url": s.URL + "/" + r.fullName, "pushed_at": time.Now().UTC().Format(time.RFC3339)}
	if unixPushed {
		m["pushed_at"] = time.Now().Unix()
	}
	return m
}

// PushPayload builds a push event for the head of branch.
func (s *Server) PushPayload(fullName, branch string) (map[string]any, error) {
	r := s.repo(fullName)
	if r == nil {
		return nil, fmt.Errorf("no repo %s", fullName)
	}
	sha, err := s.git(r.bare, "rev-parse", "refs/heads/"+branch)
	if err != nil {
		return nil, err
	}
	msg, _ := s.git(r.bare, "log", "-1", "--format=%B", sha)
	return map[string]any{
		"ref": "refs/heads/" + branch, "before": strings.Repeat("0", 40), "after": sha,
		"head_commit":  map[string]any{"id": sha, "message": msg, "timestamp": time.Now().UTC().Format(time.RFC3339), "author": map[string]any{"name": "Octo Cat", "username": "octocat"}},
		"repository":   s.repoJSON(r, true),
		"sender":       map[string]any{"login": "octocat", "type": "User"},
		"installation": map[string]any{"id": s.installationFor(fullName)},
	}, nil
}

// Push delivers a push event for the head of branch.
func (s *Server) Push(fullName, branch string) (Delivery, error) {
	p, err := s.PushPayload(fullName, branch)
	if err != nil {
		return Delivery{}, err
	}
	return s.Deliver("push", p)
}

// PullRequestPayload builds a pull_request event: head is the branch with
// the change; fork makes it come from "<fork owner>/<name>".
func (s *Server) PullRequestPayload(fullName, action string, number int, head string, fork bool) (map[string]any, error) {
	r := s.repo(fullName)
	if r == nil {
		return nil, fmt.Errorf("no repo %s", fullName)
	}
	sha, err := s.git(r.bare, "rev-parse", "refs/heads/"+head)
	if err != nil {
		return nil, err
	}
	base, _ := s.git(r.bare, "rev-parse", "refs/heads/"+r.defaultBranch)
	headRepo := s.repoJSON(r, false)
	if fork {
		_, name, _ := strings.Cut(r.fullName, "/")
		headRepo = map[string]any{"full_name": "stranger/" + name, "owner": map[string]any{"login": "stranger"}}
	}
	state := "open"
	if action == "closed" {
		state = "closed"
	}
	return map[string]any{
		"action": action, "number": number,
		"pull_request": map[string]any{
			"number": number, "title": "Change " + head, "html_url": fmt.Sprintf("%s/%s/pull/%d", s.URL, r.fullName, number), "state": state,
			"updated_at": time.Now().UTC().Format(time.RFC3339), "user": map[string]any{"login": "octocat"},
			"head": map[string]any{"ref": head, "sha": sha, "repo": headRepo},
			"base": map[string]any{"ref": r.defaultBranch, "sha": base, "repo": s.repoJSON(r, false)},
		},
		"repository":   s.repoJSON(r, false),
		"sender":       map[string]any{"login": "octocat", "type": "User"},
		"installation": map[string]any{"id": s.installationFor(fullName)},
	}, nil
}

// PullRequest delivers a pull_request event.
func (s *Server) PullRequest(fullName, action string, number int, head string, fork bool) (Delivery, error) {
	p, err := s.PullRequestPayload(fullName, action, number, head, fork)
	if err != nil {
		return Delivery{}, err
	}
	return s.Deliver("pull_request", p)
}

// ---- HTTP ----

var (
	reposPath = regexp.MustCompile(`^/api/repos/([A-Za-z0-9-]+)/([A-Za-z0-9._-]+)(/.*)?$`)
	gitPath   = regexp.MustCompile(`^/([A-Za-z0-9-]+)/([A-Za-z0-9._-]+)\.git(/.*)$`)
)

// ServeHTTP serves the fake.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/_fake/"):
		s.control(w, r)
	case gitPath.MatchString(p):
		s.serveGit(w, r)
	case strings.HasPrefix(p, "/api/"):
		s.serveAPI(w, r)
	default:
		s.serveWeb(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"message": msg})
}

func (s *Server) serveWeb(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case r.Method == http.MethodPost && (p == "/settings/apps/new" || strings.HasSuffix(p, "/settings/apps/new")):
		var m ghapp.Manifest
		if err := json.Unmarshal([]byte(r.FormValue("manifest")), &m); err != nil || m.Name == "" || m.RedirectURL == "" {
			http.Error(w, "invalid manifest", http.StatusUnprocessableEntity)
			return
		}
		code := randHex(10)
		s.mu.Lock()
		s.codes[code] = m
		s.mu.Unlock()
		http.Redirect(w, r, m.RedirectURL+"?code="+code+"&state="+url.QueryEscape(r.URL.Query().Get("state")), http.StatusFound)
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/apps/") && strings.HasSuffix(p, "/installations/new"):
		s.mu.Lock()
		a := s.app
		s.mu.Unlock()
		if a == nil {
			http.NotFound(w, r)
			return
		}
		id := s.Install(s.Account, nil)
		q := url.Values{"installation_id": {strconv.FormatInt(id, 10)}, "setup_action": {"install"}}
		if st := r.URL.Query().Get("state"); st != "" {
			q.Set("state", st)
		}
		if a.public {
			s.mu.Lock()
			access := s.Installer
			s.mu.Unlock()
			q.Set("code", s.UserCode(id, access))
		}
		target := a.manifest.SetupURL
		if target == "" {
			target = r.URL.Query().Get("redirect") // a hand-configured app: the test says where
		}
		http.Redirect(w, r, target+"?"+q.Encode(), http.StatusFound)
	case r.Method == http.MethodPost && p == "/login/oauth/access_token":
		s.mu.Lock()
		a := s.app
		grant, ok := s.oauthCodes[r.FormValue("code")]
		delete(s.oauthCodes, r.FormValue("code"))
		s.mu.Unlock()
		if a == nil || !ok || r.FormValue("client_id") != a.clientID || r.FormValue("client_secret") != a.clientSecret {
			writeJSON(w, 200, map[string]string{"error": "bad_verification_code"})
			return
		}
		t := "ghu_" + randHex(16)
		s.mu.Lock()
		s.tokens[t] = &tok{installation: grant.installation, user: &grant, expires: time.Now().Add(time.Hour)}
		s.mu.Unlock()
		writeJSON(w, 200, map[string]string{"access_token": t, "token_type": "bearer"})
	default:
		http.NotFound(w, r)
	}
}

// checkJWT verifies an app JWT (RS256, signed with the app's key, not expired).
func (s *Server) checkJWT(r *http.Request) bool {
	t, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return false
	}
	parts := strings.Split(t, ".")
	if len(parts) != 3 {
		return false
	}
	s.mu.Lock()
	a := s.app
	s.mu.Unlock()
	if a == nil {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(&a.key.PublicKey, crypto.SHA256, sum[:], sig) != nil {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var c struct {
		Iat, Exp int64
		Iss      string
	}
	if json.Unmarshal(raw, &c) != nil {
		return false
	}
	now := time.Now().Unix()
	return c.Iss == strconv.FormatInt(a.id, 10) && c.Exp > now && c.Iat <= now && c.Exp-c.Iat <= 600
}

// token returns the installation token in the request, if valid.
func (s *Server) token(r *http.Request) (string, *tok) {
	v, ok := strings.CutPrefix(r.Header.Get("Authorization"), "token ")
	if !ok {
		return "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tokens[v]
	if t == nil || t.revoked || time.Now().After(t.expires) {
		return "", nil
	}
	return v, t
}

func (s *Server) tokReaches(t *tok, fullName string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := s.installations[t.installation]
	if in == nil || !s.reachesLocked(in, fullName) {
		return false
	}
	if t.repos != nil {
		_, name, _ := strings.Cut(fullName, "/")
		for _, n := range t.repos {
			if strings.EqualFold(n, name) {
				return true
			}
		}
		return false
	}
	return true
}

func (s *Server) record(list *[]Call, repo, path string, r *http.Request) map[string]any {
	var body map[string]any
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
	s.mu.Lock()
	*list = append(*list, Call{Repo: repo, Path: path, Body: body, At: time.Now()})
	s.mu.Unlock()
	return body
}

func (s *Server) serveAPI(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/api")
	if m := regexp.MustCompile(`^/app-manifests/([A-Za-z0-9_-]+)/conversions$`).FindStringSubmatch(p); m != nil && r.Method == http.MethodPost {
		s.mu.Lock()
		man, ok := s.codes[m[1]]
		delete(s.codes, m[1])
		s.mu.Unlock()
		if !ok {
			fail(w, 404, "Not Found")
			return
		}
		if _, err := s.CreateApp(slugify(man.Name), man.Public); err != nil {
			fail(w, 500, err.Error())
			return
		}
		s.mu.Lock()
		s.app.name, s.app.manifest = man.Name, man
		c := s.credsLocked()
		s.mu.Unlock()
		writeJSON(w, 201, map[string]any{"id": c.ID, "slug": c.Slug, "name": c.Name, "owner": map[string]any{"login": c.Owner, "type": "User"},
			"html_url": c.HTMLURL, "client_id": c.ClientID, "client_secret": c.ClientSecret, "webhook_secret": c.WebhookSecret, "pem": c.PrivateKey})
		return
	}
	// App-authenticated routes.
	switch {
	case p == "/app" || p == "/app/installations" || strings.HasPrefix(p, "/app/hook/") || strings.HasPrefix(p, "/app/installations/") || strings.HasSuffix(p, "/installation") && strings.HasPrefix(p, "/repos/"):
		if !s.checkJWT(r) {
			fail(w, 401, "A JSON web token could not be decoded")
			return
		}
		s.serveAppAPI(w, r, p)
		return
	case p == "/installation/token" && r.Method == http.MethodDelete:
		v, t := s.token(r)
		if t == nil {
			fail(w, 401, "Bad credentials")
			return
		}
		s.mu.Lock()
		s.tokens[v].revoked = true
		s.mu.Unlock()
		w.WriteHeader(204)
		return
	case strings.HasPrefix(p, "/applications/") && r.Method == http.MethodDelete:
		var b struct {
			AccessToken string `json:"access_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		s.mu.Lock()
		if t := s.tokens[b.AccessToken]; t != nil {
			t.revoked = true
		}
		s.mu.Unlock()
		w.WriteHeader(204)
		return
	}
	_, t := s.token(r)
	if t == nil {
		fail(w, 401, "Bad credentials")
		return
	}
	if m := regexp.MustCompile(`^/user/installations/(\d+)/repositories$`).FindStringSubmatch(p); m != nil && t.user != nil {
		id, _ := strconv.ParseInt(m[1], 10, 64)
		if id != t.installation {
			fail(w, 404, "Not Found")
			return
		}
		out := []any{}
		s.mu.Lock()
		names := make([]string, 0, len(s.repos))
		for n := range s.repos {
			names = append(names, n)
		}
		s.mu.Unlock()
		sort.Strings(names)
		for _, n := range names {
			rp := s.repo(n)
			if !s.tokReaches(t, rp.fullName) {
				continue
			}
			access := "write"
			if t.user.access != nil {
				access = t.user.access[rp.fullName]
			}
			if access == "" {
				continue
			}
			j := s.repoJSON(rp, false)
			j["permissions"] = map[string]bool{"admin": false, "push": access == "write", "pull": true}
			out = append(out, j)
		}
		writeJSON(w, 200, map[string]any{"total_count": len(out), "repositories": out})
		return
	}
	if strings.HasPrefix(p, "/user/installations") {
		s.mu.Lock()
		in := s.installations[t.installation]
		s.mu.Unlock()
		out := []any{}
		if in != nil {
			out = append(out, s.installationJSON(in))
		}
		writeJSON(w, 200, map[string]any{"total_count": len(out), "installations": out})
		return
	}
	if p == "/installation/repositories" {
		out := []any{}
		s.mu.Lock()
		names := make([]string, 0, len(s.repos))
		for n := range s.repos {
			names = append(names, n)
		}
		s.mu.Unlock()
		sort.Strings(names)
		for _, n := range names {
			rp := s.repo(n)
			if s.tokReaches(t, rp.fullName) {
				out = append(out, s.repoJSON(rp, false))
			}
		}
		writeJSON(w, 200, map[string]any{"total_count": len(out), "repositories": out})
		return
	}
	m := reposPath.FindStringSubmatch(r.URL.Path)
	if m == nil {
		fail(w, 404, "Not Found")
		return
	}
	full, rest := m[1]+"/"+m[2], m[3]
	rp := s.repo(full)
	if rp == nil || !s.tokReaches(t, full) {
		fail(w, 404, "Not Found")
		return
	}
	if t.readOnly && r.Method != http.MethodGet {
		fail(w, 403, "Resource not accessible by integration")
		return
	}
	s.serveRepoAPI(w, r, rp, rest)
}

func (s *Server) installationJSON(in *installation) map[string]any {
	sel := "all"
	if in.repos != nil {
		sel = "selected"
	}
	return map[string]any{"id": in.id, "account": map[string]any{"login": in.account, "type": "User"}, "repository_selection": sel,
		"html_url": fmt.Sprintf("%s/settings/installations/%d", s.URL, in.id)}
}

func (s *Server) serveAppAPI(w http.ResponseWriter, r *http.Request, p string) {
	switch {
	case p == "/app":
		s.mu.Lock()
		c := s.credsLocked()
		s.mu.Unlock()
		writeJSON(w, 200, map[string]any{"id": c.ID, "slug": c.Slug, "name": c.Name, "html_url": c.HTMLURL, "owner": map[string]any{"login": c.Owner, "type": "User"}})
	case p == "/app/hook/config":
		s.mu.Lock()
		u := s.AppHookURL
		if u == "" && s.app != nil {
			u = s.app.manifest.HookAttributes.URL
		}
		s.mu.Unlock()
		writeJSON(w, 200, map[string]any{"url": u, "content_type": "json", "insecure_ssl": "0", "secret": "********"})
	case p == "/app/hook/deliveries":
		s.mu.Lock()
		out := []any{}
		for i := len(s.deliveries) - 1; i >= 0 && len(out) < 30; i-- {
			d := s.deliveries[i]
			out = append(out, map[string]any{"guid": d.ID, "event": d.Event, "status_code": d.Status, "status": http.StatusText(d.Status),
				"delivered_at": time.Now().UTC().Format(time.RFC3339), "redelivery": false})
		}
		s.mu.Unlock()
		writeJSON(w, 200, out)
	case p == "/app/installations":
		s.mu.Lock()
		out := []any{}
		ids := []int64{}
		for id := range s.installations {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			out = append(out, s.installationJSON(s.installations[id]))
		}
		s.mu.Unlock()
		writeJSON(w, 200, out)
	case strings.HasPrefix(p, "/repos/"):
		full := strings.TrimSuffix(strings.TrimPrefix(p, "/repos/"), "/installation")
		id := s.installationFor(full)
		if id == 0 {
			fail(w, 404, "Not Found")
			return
		}
		s.mu.Lock()
		in := s.installationJSON(s.installations[id])
		s.mu.Unlock()
		writeJSON(w, 200, in)
	default:
		m := regexp.MustCompile(`^/app/installations/(\d+)/access_tokens$`).FindStringSubmatch(p)
		if m == nil || r.Method != http.MethodPost {
			fail(w, 404, "Not Found")
			return
		}
		id, _ := strconv.ParseInt(m[1], 10, 64)
		s.mu.Lock()
		_, ok := s.installations[id]
		s.mu.Unlock()
		if !ok {
			fail(w, 404, "Not Found")
			return
		}
		var body struct {
			Repositories []string          `json:"repositories"`
			Permissions  map[string]string `json:"permissions"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
		t := &tok{installation: id, repos: body.Repositories, expires: time.Now().Add(time.Hour)}
		if body.Permissions != nil {
			t.readOnly = true
			for _, v := range body.Permissions {
				if v != "read" {
					t.readOnly = false
				}
			}
		}
		v := "ghs_" + randHex(16)
		s.mu.Lock()
		s.tokens[v] = t
		s.mu.Unlock()
		writeJSON(w, 201, map[string]any{"token": v, "expires_at": t.expires.UTC().Format(time.RFC3339)})
	}
}

func (s *Server) serveRepoAPI(w http.ResponseWriter, r *http.Request, rp *repo, rest string) {
	switch {
	case rest == "" && r.Method == http.MethodGet:
		writeJSON(w, 200, s.repoJSON(rp, false))
	case rest == "/branches":
		out, _ := s.git(rp.bare, "for-each-ref", "--format=%(refname:short)", "refs/heads")
		list := []any{}
		for _, b := range strings.Fields(out) {
			list = append(list, map[string]string{"name": b})
		}
		writeJSON(w, 200, list)
	case strings.HasPrefix(rest, "/git/trees/"):
		ref, _ := url.PathUnescape(strings.TrimPrefix(rest, "/git/trees/"))
		out, err := s.git(rp.bare, "ls-tree", "-r", "-t", "--long", ref)
		if err != nil {
			fail(w, 404, "Not Found")
			return
		}
		tree := []any{}
		for _, line := range strings.Split(out, "\n") {
			meta, path, ok := strings.Cut(line, "\t")
			f := strings.Fields(meta)
			if !ok || len(f) < 4 {
				continue
			}
			size, _ := strconv.ParseInt(f[3], 10, 64)
			tree = append(tree, map[string]any{"path": path, "type": f[1], "size": size})
		}
		writeJSON(w, 200, map[string]any{"tree": tree, "truncated": false})
	case strings.HasPrefix(rest, "/contents/"):
		path, _ := url.PathUnescape(strings.TrimPrefix(rest, "/contents/"))
		ref := r.URL.Query().Get("ref")
		if ref == "" {
			ref = rp.defaultBranch
		}
		out, err := s.git(rp.bare, "show", ref+":"+path)
		if err != nil {
			fail(w, 404, "Not Found")
			return
		}
		writeJSON(w, 200, map[string]any{"type": "file", "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(out + "\n"))})
	case strings.HasPrefix(rest, "/compare/"):
		spec, _ := url.PathUnescape(strings.TrimPrefix(rest, "/compare/"))
		base, head, ok := strings.Cut(spec, "...")
		out, err := s.git(rp.bare, "diff", "--name-only", base+"..."+head)
		if !ok || err != nil {
			fail(w, 404, "Not Found")
			return
		}
		files := []any{}
		for _, f := range strings.Fields(out) {
			files = append(files, map[string]any{"filename": f, "status": "modified"})
		}
		writeJSON(w, 200, map[string]any{"status": "ahead", "files": files})
	case strings.HasPrefix(rest, "/commits/"):
		ref, _ := url.PathUnescape(strings.TrimPrefix(rest, "/commits/"))
		out, err := s.git(rp.bare, "log", "-1", "--format=%H%x00%an%x00%B", ref)
		if err != nil {
			fail(w, 404, "No commit found")
			return
		}
		f := strings.SplitN(out, "\x00", 3)
		writeJSON(w, 200, map[string]any{"sha": f[0], "commit": map[string]any{"message": strings.TrimSpace(f[2]), "author": map[string]any{"name": f[1]}}, "author": map[string]any{"login": "octocat"}})
	case strings.HasPrefix(rest, "/statuses/") && r.Method == http.MethodPost:
		s.record(&s.statuses, rp.fullName, rest, r)
		writeJSON(w, 201, map[string]any{"id": s.nextIDLocked()})
	case rest == "/deployments" && r.Method == http.MethodPost:
		s.record(&s.deployments, rp.fullName, rest, r)
		writeJSON(w, 201, map[string]any{"id": s.nextIDLocked()})
	case strings.HasPrefix(rest, "/deployments/") && strings.HasSuffix(rest, "/statuses") && r.Method == http.MethodPost:
		s.record(&s.deploymentStatuses, rp.fullName, rest, r)
		writeJSON(w, 201, map[string]any{"id": s.nextIDLocked()})
	case strings.HasPrefix(rest, "/issues/comments/") && r.Method == http.MethodPatch:
		id, _ := strconv.ParseInt(strings.TrimPrefix(rest, "/issues/comments/"), 10, 64)
		var b struct{ Body string }
		_ = json.NewDecoder(r.Body).Decode(&b)
		s.mu.Lock()
		c := s.comments[id]
		if c != nil {
			c.Body, c.Updates = b.Body, c.Updates+1
		}
		s.mu.Unlock()
		if c == nil {
			fail(w, 404, "Not Found")
			return
		}
		writeJSON(w, 200, map[string]any{"id": id})
	case strings.HasPrefix(rest, "/issues/") && strings.HasSuffix(rest, "/comments") && r.Method == http.MethodPost:
		n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rest, "/issues/"), "/comments"))
		var b struct{ Body string }
		_ = json.NewDecoder(r.Body).Decode(&b)
		id := s.nextIDLocked()
		s.mu.Lock()
		s.comments[id] = &Comment{ID: id, Repo: rp.fullName, Issue: n, Body: b.Body}
		s.mu.Unlock()
		writeJSON(w, 201, map[string]any{"id": id})
	default:
		fail(w, 404, "Not Found")
	}
}

func (s *Server) nextIDLocked() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id()
}

// serveGit serves a repository over smart HTTP to a token that reaches it.
func (s *Server) serveGit(w http.ResponseWriter, r *http.Request) {
	m := gitPath.FindStringSubmatch(r.URL.Path)
	full := m[1] + "/" + m[2]
	rp := s.repo(full)
	_, pass, _ := r.BasicAuth()
	s.mu.Lock()
	t := s.tokens[pass]
	ok := t != nil && !t.revoked && time.Now().Before(t.expires)
	s.mu.Unlock()
	if rp == nil || !ok || !s.tokReaches(t, full) || strings.HasSuffix(r.URL.Path, "git-receive-pack") || r.URL.Query().Get("service") == "git-receive-pack" {
		w.Header().Set("WWW-Authenticate", `Basic realm="GitHub"`)
		http.Error(w, "Repository not found.", http.StatusUnauthorized)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/info/refs") {
		s.mu.Lock()
		s.clones = append(s.clones, rp.fullName)
		s.mu.Unlock()
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/" + filepath.Base(rp.bare) + m[3]
	(&cgi.Handler{Path: s.backend, Root: "/", Env: []string{"GIT_PROJECT_ROOT=" + filepath.Dir(rp.bare), "GIT_HTTP_EXPORT_ALL=1"}}).ServeHTTP(w, r2)
}

func slugify(s string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(s) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' {
			b.WriteRune(c)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// ---- control (for an out-of-process fake) ----

// control serves /_fake/... so a test driving a fake in another process
// (a VM) can add repositories, commit, deliver events and read what the
// box did.
func (s *Server) control(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Repo    string            `json:"repo"`
		Private bool              `json:"private"`
		Branch  string            `json:"branch"`
		Files   map[string]string `json:"files"`
		Message string            `json:"message"`
		Action  string            `json:"action"`
		Number  int               `json:"number"`
		Fork    bool              `json:"fork"`
		Account string            `json:"account"`
		Repos   []string          `json:"repos"`
	}
	if r.Method == http.MethodPost {
		if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&in); err != nil {
			fail(w, 400, err.Error())
			return
		}
	}
	var out any
	var err error
	switch r.URL.Path {
	case "/_fake/repo":
		var sha string
		sha, err = s.AddRepo(in.Repo, in.Private, in.Files)
		out = map[string]string{"sha": sha}
	case "/_fake/commit":
		var sha string
		sha, err = s.Commit(in.Repo, in.Branch, in.Files, in.Message)
		out = map[string]string{"sha": sha}
	case "/_fake/push":
		out, err = s.Push(in.Repo, in.Branch)
	case "/_fake/pull":
		out, err = s.PullRequest(in.Repo, in.Action, in.Number, in.Branch, in.Fork)
	case "/_fake/install":
		out = map[string]int64{"id": s.Install(in.Account, in.Repos)}
	case "/_fake/state":
		rec := s.Recorded()
		comments := []*Comment{}
		for _, c := range rec.Comments {
			comments = append(comments, c)
		}
		sort.Slice(comments, func(i, j int) bool { return comments[i].ID < comments[j].ID })
		s.mu.Lock()
		app := map[string]any{}
		if s.app != nil {
			app = map[string]any{"id": s.app.id, "slug": s.app.slug, "name": s.app.name, "manifest": s.app.manifest}
		}
		revoked := 0
		for _, t := range s.tokens {
			if t.revoked {
				revoked++
			}
		}
		out = map[string]any{"app": app, "statuses": rec.Statuses, "deployments": rec.Deployments, "deploymentStatuses": rec.DeploymentStatuses,
			"comments": comments, "clones": rec.Clones, "deliveries": rec.Deliveries, "tokens": len(s.tokens), "revokedTokens": revoked}
		s.mu.Unlock()
	default:
		fail(w, 404, "Not Found")
		return
	}
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	writeJSON(w, 200, out)
}
