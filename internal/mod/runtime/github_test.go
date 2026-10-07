package runtime

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/mod/runtime/ghapp"
	"github.com/btahir/tiffin/internal/mod/runtime/ghapp/ghfake"
	"github.com/btahir/tiffin/internal/tokens"
)

// ghHarness is a box (the runtime harness behind the full API) and a fake
// GitHub that delivers webhooks to it.
type ghHarness struct {
	*harness
	f     *ghfake.Server
	api   *httptest.Server
	owner string
	nc    *http.Client // follows no redirects
}

func newGHHarness(t *testing.T) *ghHarness {
	t.Helper()
	f, err := ghfake.New(t.TempDir())
	if err != nil {
		t.Skip("git not available: ", err)
	}
	if _, err := os.Stat(filepath.Join(strings.TrimSpace(gitExecPath(t)), "git-http-backend")); err != nil {
		t.Skip("git-http-backend not found")
	}
	gh := httptest.NewTLSServer(f)
	t.Cleanup(gh.Close)
	f.URL = gh.URL
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: gh.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	gitExtraConfig = []string{"http.sslCAInfo=" + ca}
	t.Cleanup(func() { gitExtraConfig = nil })
	settings := filepath.Join(t.TempDir(), "github.json")
	raw, _ := json.Marshal(f.Endpoints())
	if err := os.WriteFile(settings, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TIFFIN_GITHUB_CONFIG", settings)

	h := newHarness(t)
	h.r.gh.HTTP = gh.Client()
	owner, _, err := h.p.Tokens.Bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a := api.New(api.Deps{DB: h.p.DB, Engine: h.p.Engine, Tokens: h.p.Tokens, Platform: h.p})
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	f.HookURL = srv.URL + "/v1/github/webhook"
	nc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: gh.Client().Transport}
	return &ghHarness{harness: h, f: f, api: srv, owner: owner, nc: nc}
}

func gitExecPath(t *testing.T) string {
	out, err := execCommand("git", "--exec-path")
	if err != nil {
		t.Skip("git not installed")
	}
	return out
}

func (g *ghHarness) call(method, path, body string) (int, map[string]any) {
	g.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, g.api.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+g.owner)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		g.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// browse follows one redirect hop by hand: GitHub pages and the box's
// callbacks (the box's public URL is a test server here).
func (g *ghHarness) browse(method, u string, form url.Values) *url.URL {
	g.t.Helper()
	var res *http.Response
	var err error
	if method == http.MethodPost {
		res, err = g.nc.PostForm(u, form)
	} else {
		res, err = g.nc.Get(u)
	}
	if err != nil {
		g.t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusFound {
		g.t.Fatalf("%s %s: %d, want a redirect", method, u, res.StatusCode)
	}
	loc, _ := url.Parse(res.Header.Get("Location"))
	return loc
}

// onBox rewrites a URL on the box's public address to the test API server.
func (g *ghHarness) onBox(u *url.URL) string {
	return g.api.URL + u.Path + "?" + u.RawQuery
}

// connect runs the manifest flow end to end, as a browser would.
func (g *ghHarness) connect() {
	g.t.Helper()
	code, start := g.call("POST", "/v1/github/connect", `{}`)
	if code != 200 || start["name"] != "tiffin" {
		g.t.Fatalf("connect: %d %v", code, start)
	}
	var m ghapp.Manifest
	if err := json.Unmarshal([]byte(start["manifest"].(string)), &m); err != nil {
		g.t.Fatal(err)
	}
	if m.HookAttributes.URL != "https://dashboard.tiffin.localhost:8443/v1/github/webhook" || m.RedirectURL != "https://dashboard.tiffin.localhost:8443/v1/github/manifest/callback" || m.Public {
		g.t.Fatalf("manifest: %+v", m)
	}
	cb := g.browse(http.MethodPost, start["url"].(string), url.Values{"manifest": {start["manifest"].(string)}})
	install := g.browse(http.MethodGet, g.onBox(cb), nil)
	if !strings.HasPrefix(install.String(), g.f.URL+"/apps/tiffin/installations/new") {
		g.t.Fatalf("after creating the app the owner installs it: %s", install)
	}
	setup := g.browse(http.MethodGet, install.String(), nil)
	back := g.browse(http.MethodGet, g.onBox(setup), nil)
	if back.Path != "/settings/git" || back.Query().Get("installed") != "1" {
		g.t.Fatalf("back on the dashboard: %s", back)
	}
	// The state was single use.
	if again := g.browse(http.MethodGet, g.onBox(cb), nil); again.Query().Get("error") == "" {
		g.t.Fatalf("a used state must not work twice: %s", again)
	}
}

// connectSite wires the harness's static app to a repository.
func (g *ghHarness) connectSite(gitBlock manifest.Git) {
	a := g.mf.Apps["site"]
	a.Git = &gitBlock
	g.mf.Apps["site"] = a
	g.apply()
}

func (g *ghHarness) waitFor(app string, cond func(*Deploy) bool) *Deploy {
	g.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		ds, _ := g.r.st.listDeploys(context.Background(), "shop", app, "*")
		for _, d := range ds {
			if cond(d) && d.Terminal() {
				return d
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	g.t.Fatalf("no matching deploy of %s", app)
	return nil
}

func (g *ghHarness) settle() {
	// Reports to GitHub happen right after a deploy turns terminal.
	time.Sleep(300 * time.Millisecond)
}

func TestGitHubConnectPushAndPreviews(t *testing.T) {
	g := newGHHarness(t)
	ctx := context.Background()
	if code, st := g.call("GET", "/v1/github", ""); code != 200 || st["connected"] != false || st["reachable"] != true {
		t.Fatalf("status before connecting: %d %v", code, st)
	}
	g.connect()
	code, st := g.call("GET", "/v1/github", "")
	ins, _ := st["installations"].([]any)
	if code != 200 || st["connected"] != true || st["source"] != "box" || len(ins) != 1 || st["canManage"] != true || st["problem"] != nil {
		t.Fatalf("status: %d %v", code, st)
	}
	if hook, _ := st["webhook"].(map[string]any); hook == nil || hook["url"] != st["webhookUrl"] || hook["problem"] != nil {
		t.Fatalf("webhook as GitHub has it: %v", st["webhook"])
	}
	// An app whose webhook points elsewhere (one made by hand) gets no pushes: the status says so.
	g.f.AppHookURL = "https://example.com/v1/github/webhook"
	_, st = g.call("GET", "/v1/github", "")
	if p, _ := st["problem"].(string); !strings.Contains(p, "GitHub sends the app's events to https://example.com/v1/github/webhook, not to this box") {
		t.Fatalf("a webhook pointing elsewhere: %v", st)
	}
	g.f.AppHookURL = ""

	first, err := g.f.AddRepo("octo/shop", true, map[string]string{"README.md": "shop", "web/index.html": "<h1>v1</h1>",
		"apps/api/package.json": `{"name":"api","dependencies":{"hono":"4"}}`, "package.json": `{"workspaces":["apps/*"]}`})
	if err != nil {
		t.Fatal(err)
	}
	// Import: the repo is listed (private too), and its folders detected.
	code, list := g.call("GET", "/v1/github/repos?q=SHOP", "")
	repos, _ := list["repos"].([]any)
	if code != 200 || len(repos) != 1 || repos[0].(map[string]any)["private"] != true {
		t.Fatalf("repos: %d %v", code, list)
	}
	code, det := g.call("GET", "/v1/github/repos/octo/shop", "")
	if code != 200 || det["defaultBranch"] != "main" || det["commit"].(map[string]any)["sha"] != first {
		t.Fatalf("inspect: %d %v", code, det)
	}
	roots := det["roots"].([]any)
	byPath := map[string]map[string]any{}
	for _, r := range roots {
		byPath[r.(map[string]any)["path"].(string)] = r.(map[string]any)
	}
	if byPath["web"]["framework"] != "static" || byPath["apps/api"]["framework"] != "hono" || byPath[""]["workspace"] != true || det["suggested"] == "" {
		t.Fatalf("roots: %v", roots)
	}

	g.connectSite(manifest.Git{Repo: "octo/shop", Branch: "main", Path: "web", Previews: manifest.PreviewsSameRepo})
	if code, list = g.call("GET", "/v1/github/repos?refresh=true", ""); code != 200 || list["repos"].([]any)[0].(map[string]any)["connected"].([]any)[0] != "shop/site" {
		t.Fatalf("connected apps are marked: %v", list)
	}

	// The first deploy after importing (Redeploy).
	code, d := g.call("POST", "/v1/projects/shop/apps/site/deploys/github", "")
	if code != 202 || d["trigger"] != "redeploy" || d["commit"] != first || d["message"] != "first commit" {
		t.Fatalf("deploy github: %d %v", code, d)
	}
	live := g.wait("site", d["id"].(string))
	if live.Status != StatusLive {
		t.Fatalf("not live: %+v\n%s", live, g.buildLog("site", live.ID))
	}
	if code, body := g.get("shop.tiffin.localhost", "/"); code != 200 || body != "<h1>v1</h1>" {
		t.Fatalf("served: %d %q", code, body)
	}

	// A push to main deploys; the commit gets pending → success, a
	// deployment, and the clone token is revoked afterwards.
	sha2, _ := g.f.Commit("octo/shop", "main", map[string]string{"web/index.html": "<h1>v2</h1>"}, "Make it v2\n\nlonger body")
	dl, err := g.f.Push("octo/shop", "main")
	if err != nil || dl.Status != 202 || !strings.Contains(dl.Reply, "deploying shop/site") {
		t.Fatalf("push delivery: %v %+v", err, dl)
	}
	d2 := g.waitFor("site", func(d *Deploy) bool { return d.Commit == sha2 })
	if d2.Status != StatusLive || d2.Message != "Make it v2" || d2.Trigger != "push" || d2.Author != "octocat" || !strings.HasPrefix(d2.CreatedBy, "github:") {
		t.Fatalf("push deploy: %+v\n%s", d2, g.buildLog("site", d2.ID))
	}
	if _, body := g.get("shop.tiffin.localhost", "/"); body != "<h1>v2</h1>" {
		t.Fatalf("v2 not served: %q", body)
	}
	// GitHub's own list of deliveries, newest first, is in the status.
	_, st = g.call("GET", "/v1/github", "")
	if ds, _ := st["webhook"].(map[string]any)["deliveries"].([]any); len(ds) == 0 || ds[0].(map[string]any)["event"] != "push" || ds[0].(map[string]any)["statusCode"] != float64(202) {
		t.Fatalf("GitHub's deliveries: %v", st["webhook"])
	}
	g.settle()
	var states []string
	for _, s := range g.f.Recorded().Statuses {
		if strings.HasSuffix(s.Path, sha2) {
			states = append(states, s.Body["state"].(string))
			if s.Body["context"] != "tiffin/shop/site" {
				t.Fatalf("context: %v", s.Body)
			}
		}
	}
	if strings.Join(states, ",") != "pending,success" {
		t.Fatalf("statuses for %s: %v", sha2, states)
	}
	ds := g.f.Recorded().DeploymentStatuses
	if last := ds[len(ds)-1].Body; last["state"] != "success" || last["environment_url"] != "https://shop.tiffin.localhost:8443" ||
		!strings.Contains(last["log_url"].(string), "/projects/shop/apps/site/deploys/"+d2.ID) {
		t.Fatalf("deployment status: %v", last)
	}
	code, stt := g.call("GET", "/v1/github", "")
	if evs := stt["events"].([]any); code != 200 || !strings.Contains(evs[0].(map[string]any)["summary"].(string), "Push to main") {
		t.Fatalf("events: %v", stt["events"])
	}

	// Rapid pushes coalesce: with the builder busy, three pushes build the
	// first and the last; the middle one is skipped.
	g.r.build <- struct{}{} // hold the builder
	var shas []string
	for i, v := range []string{"a", "b", "c"} {
		sha, _ := g.f.Commit("octo/shop", "main", map[string]string{"web/index.html": "<h1>" + v + "</h1>"}, "rapid "+v)
		shas = append(shas, sha)
		if dl, _ := g.f.Push("octo/shop", "main"); dl.Status != 202 {
			t.Fatalf("push %d: %+v", i, dl)
		}
	}
	<-g.r.build
	dc := g.waitFor("site", func(d *Deploy) bool { return d.Commit == shas[2] })
	db := g.waitFor("site", func(d *Deploy) bool { return d.Commit == shas[1] })
	da := g.waitFor("site", func(d *Deploy) bool { return d.Commit == shas[0] })
	// a may also be skipped: by the time it starts, main may have moved past it.
	if da.Status != StatusLive && da.Status != StatusSuperseded && da.Status != StatusSkipped || db.Status != StatusSkipped || dc.Status != StatusLive {
		t.Fatalf("coalescing: a=%s b=%s c=%s", da.Status, db.Status, dc.Status)
	}
	if _, body := g.get("shop.tiffin.localhost", "/"); body != "<h1>c</h1>" {
		t.Fatalf("the newest push wins: %q", body)
	}

	// Pull requests: a preview at its own address and one comment kept up to date.
	g.f.Commit("octo/shop", "feat", map[string]string{"web/index.html": "<h1>feature</h1>"}, "feature")
	if dl, _ := g.f.PullRequest("octo/shop", "opened", 7, "feat", false); dl.Status != 202 || !strings.Contains(dl.Reply, "preview") {
		t.Fatalf("pr opened: %+v", dl)
	}
	pv := g.waitFor("site", func(d *Deploy) bool { return d.Preview == "pr-7" })
	if pv.Status != StatusLive || pv.PullRequest != 7 || pv.URL != "https://pr-7--shop.tiffin.localhost:8443" {
		t.Fatalf("preview: %+v\n%s", pv, g.buildLog("site", pv.ID))
	}
	g.settle()
	for _, s := range g.f.Recorded().Statuses {
		if strings.HasSuffix(s.Path, pv.Commit) && s.Body["context"] != "tiffin/shop/site/preview" {
			t.Fatalf("a preview has its own status context: %v", s.Body)
		}
	}
	if _, body := g.get("pr-7--shop.tiffin.localhost", "/"); body != "<h1>feature</h1>" {
		t.Fatalf("preview serves: %q", body)
	}
	g.settle()
	// The one pull request comment, as it is now.
	comment := func() *ghfake.Comment {
		t.Helper()
		cs := g.f.Recorded().Comments
		if len(cs) != 1 {
			t.Fatalf("one comment: %+v", cs)
		}
		for _, c := range cs {
			return c
		}
		return nil
	}
	cm := comment()
	if cm.Issue != 7 || !strings.Contains(cm.Body, "https://pr-7--shop.tiffin.localhost:8443") || !strings.Contains(cm.Body, "built in") || !strings.Contains(cm.Body, "[logs](") {
		t.Fatalf("comment: %+v", cm)
	}
	sha4, _ := g.f.Commit("octo/shop", "feat", map[string]string{"web/index.html": "<h1>feature 2</h1>"}, "feature 2")
	g.f.PullRequest("octo/shop", "synchronize", 7, "feat", false)
	pv2 := g.waitFor("site", func(d *Deploy) bool { return d.Commit == sha4 })
	g.settle()
	cm = comment()
	if pv2.Status != StatusLive || cm.Updates < 2 || !strings.Contains(cm.Body, short(sha4)) {
		t.Fatalf("updated in place: %+v %+v", pv2, cm)
	}
	if st := g.state("site", ""); st.Live != dc.ID {
		t.Fatal("previews never touch production")
	}

	// Forks are not built by default.
	if dl, _ := g.f.PullRequest("octo/shop", "opened", 8, "feat", true); !strings.Contains(dl.Reply, "forks are off") {
		t.Fatalf("fork pr: %+v", dl)
	}
	if ds, _ := g.r.st.listDeploys(ctx, "shop", "site", "pr-8"); len(ds) != 0 {
		t.Fatal("a fork's pull request must not build")
	}

	// Closing removes the preview and says so in the comment.
	if dl, _ := g.f.PullRequest("octo/shop", "closed", 7, "feat", false); !strings.Contains(dl.Reply, "removed") {
		t.Fatalf("pr closed: %+v", dl)
	}
	if code, _ := g.get("pr-7--shop.tiffin.localhost", "/"); code != 404 {
		t.Fatalf("closed preview still served: %d", code)
	}
	if cm = comment(); !strings.Contains(cm.Body, "was removed") {
		t.Fatalf("comment after close: %s", cm.Body)
	}
	ds = g.f.Recorded().DeploymentStatuses
	if last := ds[len(ds)-1].Body; last["state"] != "inactive" {
		t.Fatalf("the preview deployment is retired: %v", last)
	}

	// Every token the box minted for a clone was revoked.
	clones := 0
	for _, r := range g.f.Recorded().Clones {
		if r == "octo/shop" {
			clones++
		}
	}
	if clones < 5 {
		t.Fatalf("clones: %v", g.f.Recorded().Clones)
	}
	if minted, revoked := g.f.ReadTokens(); minted < clones || revoked != minted {
		t.Fatalf("clone tokens: %d minted, %d revoked", minted, revoked)
	}

	// Disconnecting stops deliveries.
	if code, out := g.call("DELETE", "/v1/github", ""); code != 200 || !strings.Contains(out["url"].(string), "/settings/apps/tiffin") {
		t.Fatalf("disconnect: %d %v", code, out)
	}
	if dl, _ := g.f.Push("octo/shop", "main"); dl.Status != http.StatusGone {
		t.Fatalf("after disconnecting: %+v", dl)
	}
	if code, p := g.call("POST", "/v1/projects/shop/apps/site/deploys/github", ""); code != 409 || p["hint"] == "" {
		t.Fatalf("redeploy without GitHub: %d %v", code, p)
	}
}

func TestGitHubWebhookSecurity(t *testing.T) {
	g := newGHHarness(t)
	g.connect()
	g.f.AddRepo("octo/shop", false, map[string]string{"web/index.html": "x"})
	g.connectSite(manifest.Git{Repo: "octo/shop", Branch: "release", Path: "web", Previews: manifest.PreviewsOff})
	payload, _ := g.f.PushPayload("octo/shop", "main")
	body, _ := json.Marshal(payload)
	var secret string
	{
		c, err := g.r.github(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		secret = c.Cred.WebhookSecret
	}
	send := func(id string, b []byte, sig string) ghfake.Delivery {
		d, err := g.f.DeliverRaw(g.f.HookURL, "push", id, b, sig)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	if d := send("aaaaaaaa-1", body, ""); d.Status != 401 {
		t.Fatalf("unsigned: %+v", d)
	}
	if d := send("aaaaaaaa-2", body, ghapp.Sign("wrong", body)); d.Status != 401 {
		t.Fatalf("wrong secret: %+v", d)
	}
	tampered := []byte(strings.Replace(string(body), "refs/heads/main", "refs/heads/release", 1))
	if d := send("aaaaaaaa-3", tampered, ghapp.Sign(secret, body)); d.Status != 401 {
		t.Fatalf("tampered body: %+v", d)
	}
	if d := send("aaaaaaaa-4", body, ghapp.Sign(secret, body)); d.Status != 202 || !strings.Contains(d.Reply, "no app deploys from main") {
		t.Fatalf("signed: %+v", d)
	}
	if d := send("aaaaaaaa-4", body, ghapp.Sign(secret, body)); d.Status != 200 || !strings.Contains(d.Reply, "already handled") {
		t.Fatalf("replayed id: %+v", d)
	}
	// The delivery id header is not signed: a replay under a fresh id is the same delivery.
	if d := send("bbbbbbbb-4", body, ghapp.Sign(secret, body)); d.Status != 200 || !strings.Contains(d.Reply, "already handled") {
		t.Fatalf("replayed body under a new id: %+v", d)
	}
	// A delivery that failed on the box's side is not "handled": GitHub's
	// redelivery tries again. One being handled right now is a duplicate.
	{
		ctx := context.Background()
		dup, done, err := g.r.claimDelivery(ctx, "k1")
		if err != nil || dup {
			t.Fatalf("first claim: %v %v", dup, err)
		}
		if dup2, _, _ := g.r.claimDelivery(ctx, "k1"); !dup2 {
			t.Fatal("a delivery being handled must not be handled twice at once")
		}
		done(false)
		dup, done, _ = g.r.claimDelivery(ctx, "k1")
		if dup {
			t.Fatal("a failed delivery must be handled again")
		}
		done(true)
		if dup, _, _ = g.r.claimDelivery(ctx, "k1"); !dup {
			t.Fatal("a handled delivery is a duplicate")
		}
	}
	payload["repository"].(map[string]any)["pushed_at"] = time.Now().Add(-3 * time.Hour).Unix()
	old, _ := json.Marshal(payload)
	if d := send("aaaaaaaa-5", old, ghapp.Sign(secret, old)); d.Status != 422 || !strings.Contains(d.Reply, "stale") {
		t.Fatalf("old delivery: %+v", d)
	}
	if d := send("not a guid!", body, ghapp.Sign(secret, body)); d.Status != 400 {
		t.Fatalf("bad delivery id: %+v", d)
	}
	// previews: off builds no pull requests.
	if dl, _ := g.f.PullRequest("octo/shop", "opened", 1, "main", false); strings.Contains(dl.Reply, "preview dep_") {
		t.Fatalf("previews off: %+v", dl)
	}
	// Keys that are not box admins cannot connect or disconnect.
	ctx := context.Background()
	owner, err := g.p.Tokens.Authenticate(ctx, g.owner)
	if err != nil {
		t.Fatal(err)
	}
	key, _, err := g.p.Tokens.CreateKey(ctx, owner, tokens.KeyRequest{Name: "shop-agent", Projects: tokens.Projects{"shop"}, Access: "full"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ method, path string }{{"POST", "/v1/github/connect"}, {"DELETE", "/v1/github"}, {"POST", "/v1/github/install"}} {
		req, _ := http.NewRequest(c.method, g.api.URL+c.path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil || res.StatusCode != 403 {
			t.Fatalf("a project key on %s %s: %v %v", c.method, c.path, err, res.StatusCode)
		}
		res.Body.Close()
	}
}

func execCommand(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return string(out), err
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestGitHubSharedAppBindsOnlyProvenInstallations(t *testing.T) {
	g := newGHHarness(t)
	cred, err := g.f.CreateApp("shiptest", true)
	if err != nil {
		t.Fatal(err)
	}
	other := g.f.Install("someone-else", nil) // another customer of the shared app
	body, _ := json.Marshal(map[string]any{"appId": cred.ID, "privateKey": cred.PrivateKey, "webhookSecret": cred.WebhookSecret, "public": true})
	if code, p := g.call("PUT", "/v1/github/app", string(body)); code != 422 || !strings.Contains(p["detail"].(string), "client id") {
		t.Fatalf("public apps need the client id: %d %v", code, p)
	}
	body, _ = json.Marshal(map[string]any{"appId": cred.ID, "privateKey": cred.PrivateKey, "webhookSecret": cred.WebhookSecret, "public": true,
		"clientId": cred.ClientID, "clientSecret": cred.ClientSecret})
	code, st := g.call("PUT", "/v1/github/app", string(body))
	if code != 200 || st["source"] != "configured" || st["shared"] != true || len(st["installations"].([]any)) != 0 {
		t.Fatalf("use-app: %d %v", code, st)
	}
	// Someone else's installation is invisible, and its deliveries ignored.
	g.f.AddRepo("someone-else/app", true, map[string]string{"index.html": "x"})
	g.connectSite(manifest.Git{Repo: "someone-else/app", Branch: "main", Previews: manifest.PreviewsSameRepo})
	if dl, _ := g.f.Push("someone-else/app", "main"); !strings.Contains(dl.Reply, "not an installation of this box") {
		t.Fatalf("foreign installation: %+v", dl)
	}
	// A forged setup link (a valid state, someone else's installation id) is refused.
	code, link := g.call("POST", "/v1/github/install", "")
	if code != 200 {
		t.Fatalf("install: %d %v", code, link)
	}
	lu, _ := url.Parse(link["url"].(string))
	forged := g.api.URL + "/v1/github/setup?" + url.Values{"installation_id": {itoa(other)}, "setup_action": {"install"},
		"state": {lu.Query().Get("state")}, "code": {g.f.OAuthCode(g.f.Install("me", nil))}}.Encode()
	if back := g.browse(http.MethodGet, forged, nil); back.Query().Get("error") == "" {
		t.Fatalf("forged setup accepted: %s", back)
	}
	// The real install from the box binds the new installation: only the
	// repositories the installer can push to, though the installation (all
	// of octo's repositories) reaches more.
	g.f.AddRepo("octo/web", true, map[string]string{"index.html": "web"})
	g.f.AddRepo("octo/payroll", true, map[string]string{"index.html": "pay"})
	g.f.Installer = map[string]string{"octo/web": "write", "octo/payroll": "read"}
	if code, link = g.call("POST", "/v1/github/install", ""); code != 200 {
		t.Fatalf("install: %d %v", code, link)
	}
	setup := g.browse(http.MethodGet, link["url"].(string)+"&redirect="+url.QueryEscape(g.api.URL+"/v1/github/setup"), nil)
	if back := g.browse(http.MethodGet, setup.String(), nil); back.Query().Get("installed") != "1" {
		t.Fatalf("install: %s", back)
	}
	_, st = g.call("GET", "/v1/github", "")
	if ins := st["installations"].([]any); len(ins) != 1 || ins[0].(map[string]any)["id"] == float64(other) {
		t.Fatalf("installations: %v", ins)
	}
	if code, list := g.call("GET", "/v1/github/repos?refresh=true", ""); code != 200 || len(list["repos"].([]any)) != 1 ||
		list["repos"].([]any)[0].(map[string]any)["fullName"] != "octo/web" {
		t.Fatalf("only the repositories the installer can push to: %d %v", code, list)
	}
	if code, p := g.call("GET", "/v1/github/repos/octo/payroll", ""); code != 409 || !strings.Contains(p["detail"].(string), "not been given access") {
		t.Fatalf("inspect a repository the installer can only read: %d %v", code, p)
	}
	g.connectSite(manifest.Git{Repo: "octo/payroll", Branch: "main", Previews: manifest.PreviewsSameRepo})
	if code, p := g.call("POST", "/v1/projects/shop/apps/site/deploys/github", ""); code != 409 {
		t.Fatalf("deploy a repository the installer can only read: %d %v", code, p)
	}
	if dl, _ := g.f.Push("octo/payroll", "main"); !strings.Contains(dl.Reply, "not an installation of this box") {
		t.Fatalf("push to a repository the installer can only read: %+v", dl)
	}
	if code, d := g.call("GET", "/v1/github/repos/octo/web", ""); code != 200 || d["fullName"] != "octo/web" {
		t.Fatalf("inspect a bound repository: %d %v", code, d)
	}
	if code, _ := g.call("DELETE", "/v1/github", ""); code != 200 {
		t.Fatal("disconnect")
	}
}

func TestDetectRoots(t *testing.T) {
	files := map[string]string{
		"package.json":                `{"name":"mono","workspaces":["apps/*"]}`,
		"apps/web/package.json":       `{"name":"web","dependencies":{"next":"15","react":"19"}}`,
		"apps/api/package.json":       `{"name":"api","dependencies":{"hono":"4"}}`,
		"apps/docs/package.json":      `{"name":"docs","devDependencies":{"vite":"6"},"scripts":{"build":"vite build"}}`,
		"apps/docs/index.html":        "<html>",
		"apps/worker/package.json":    `{"name":"worker","scripts":{"start":"bun run index.ts"}}`,
		"site/index.html":             "<html>",
		"node_modules/x/package.json": `{}`,
		"apps/web/public/index.html":  "<html>",
	}
	var tree []ghapp.TreeEntry
	for p := range files {
		tree = append(tree, ghapp.TreeEntry{Path: p, Type: "blob"})
	}
	roots := detectRoots(tree, func(p string) ([]byte, error) { return []byte(files[p]), nil })
	got := map[string]string{}
	for _, r := range roots {
		got[r.Path] = r.Framework
	}
	want := map[string]string{"": "bun", "apps/web": "next", "apps/api": "hono", "apps/docs": "static", "apps/worker": "bun", "site": "static"}
	for p, f := range want {
		if got[p] != f {
			t.Errorf("%q: %q, want %q (%+v)", p, got[p], f, roots)
		}
	}
	if len(got) != len(want) {
		t.Errorf("roots: %+v", roots)
	}
	if roots[len(roots)-1].Path != "" || !roots[len(roots)-1].Workspace || roots[0].Path != "site" {
		t.Errorf("order (apps first, the monorepo's top last): %+v", roots)
	}
}

// A push delivery that arrives late (GitHub's queue, a redelivery, or a
// captured one replayed) never deploys an older commit over a newer one.
func TestGitHubLatePushNeverRollsBack(t *testing.T) {
	g := newGHHarness(t)
	ctx := context.Background()
	g.connect()
	first, err := g.f.AddRepo("octo/shop", false, map[string]string{"web/index.html": "<h1>v1</h1>"})
	if err != nil {
		t.Fatal(err)
	}
	g.connectSite(manifest.Git{Repo: "octo/shop", Branch: "main", Path: "web", Previews: manifest.PreviewsOff})
	late, _ := g.f.PushPayload("octo/shop", "main") // the push of the first commit, delivered later
	sha2, _ := g.f.Commit("octo/shop", "main", map[string]string{"web/index.html": "<h1>v2</h1>"}, "v2")
	if dl, _ := g.f.Push("octo/shop", "main"); dl.Status != 202 || !strings.Contains(dl.Reply, "deploying") {
		t.Fatalf("push: %+v", dl)
	}
	if d := g.waitFor("site", func(d *Deploy) bool { return d.Commit == sha2 }); d.Status != StatusLive {
		t.Fatalf("v2: %+v", d)
	}
	if dl, _ := g.f.Deliver("push", late); dl.Status != 202 || !strings.Contains(dl.Reply, "no longer the head of main") {
		t.Fatalf("a late push must not deploy: %+v", dl)
	}
	// One already queued when the branch moved on is skipped when its turn comes.
	d, err := g.r.enqueue(ctx, &ghJob{Project: "shop", App: "site", Repo: "octo/shop", SHA: first, Branch: "main", Trigger: "push", By: "test", Path: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if got := g.waitFor("site", func(x *Deploy) bool { return x.ID == d.ID }); got.Status != StatusSkipped || !strings.Contains(got.Error, "no longer the head") {
		t.Fatalf("queued late push: %+v", got)
	}
	if _, body := g.get("shop.tiffin.localhost", "/"); body != "<h1>v2</h1>" {
		t.Fatalf("still v2: %q", body)
	}
	if ds, _ := g.r.st.listDeploys(ctx, "shop", "site", ""); len(ds) != 2 {
		t.Fatalf("deploys: %d", len(ds))
	}
}

// A final report GitHub refuses (rate limited, down) is kept and retried,
// not dropped: the commit would stay "pending" for good.
func TestGitHubRetriesFailedReports(t *testing.T) {
	g := newGHHarness(t)
	ctx := context.Background()
	g.connect()
	g.f.AddRepo("octo/shop", false, map[string]string{"web/index.html": "<h1>v1</h1>"})
	g.connectSite(manifest.Git{Repo: "octo/shop", Branch: "main", Path: "web", Previews: manifest.PreviewsOff})
	sha, _ := g.f.Commit("octo/shop", "main", map[string]string{"web/index.html": "<h1>v2</h1>"}, "v2")
	g.f.SetRateLimited(true)
	if dl, _ := g.f.Push("octo/shop", "main"); dl.Status != 202 {
		t.Fatalf("push: %+v", dl)
	}
	d := g.waitFor("site", func(d *Deploy) bool { return d.Commit == sha })
	if d.Status != StatusLive {
		t.Fatalf("deploy: %+v", d)
	}
	var tr ghTracker
	deadline := time.Now().Add(5 * time.Second)
	for !tr.Ended && time.Now().Before(deadline) {
		raw, _, _ := g.p.DB.KVGet(ctx, nsGitHubJobs, trackerKey(d))
		_ = json.Unmarshal(raw, &tr)
		time.Sleep(20 * time.Millisecond)
	}
	if !tr.Ended || tr.Tries != 1 || tr.Next.Before(time.Now()) {
		t.Fatalf("the failed report is kept for a retry: %+v", tr)
	}
	success := func() bool {
		for _, s := range g.f.Recorded().Statuses {
			if strings.HasSuffix(s.Path, sha) && s.Body["state"] == "success" {
				return true
			}
		}
		return false
	}
	if success() {
		t.Fatal("GitHub refused every write")
	}
	g.r.resumeReports(ctx, false) // not due yet
	g.f.SetRateLimited(false)
	g.r.resumeReports(ctx, false)
	if success() {
		t.Fatal("retried before its time")
	}
	tr.Next = time.Now().Add(-time.Second)
	raw, _ := json.Marshal(tr)
	_ = g.p.DB.KVPut(ctx, nsGitHubJobs, trackerKey(d), raw)
	g.r.resumeReports(ctx, false)
	if !success() {
		t.Fatalf("retried report: %+v", g.f.Recorded().Statuses)
	}
	if _, ok, _ := g.p.DB.KVGet(ctx, nsGitHubJobs, trackerKey(d)); ok {
		t.Fatal("a delivered report is forgotten")
	}
}
