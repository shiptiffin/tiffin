package ghapp_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/mod/runtime/ghapp"
	"github.com/btahir/tiffin/internal/mod/runtime/ghapp/ghfake"
)

func fake(t *testing.T) (*ghfake.Server, *ghapp.Client) {
	t.Helper()
	f, err := ghfake.New(t.TempDir())
	if err != nil {
		t.Skip("git not available: ", err)
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f, ghapp.NewClient(f.Endpoints(), srv.Client())
}

func TestManifestFlowAndAppCalls(t *testing.T) {
	f, c := fake(t)
	ctx := context.Background()
	m := ghapp.NewManifest("tiffin-box", ghapp.ManifestURLs{Home: "https://dash.example.com", Webhook: "https://dash.example.com/v1/github/webhook",
		Callback: "https://dash.example.com/v1/github/manifest/callback", Setup: "https://dash.example.com/v1/github/setup"})
	if m.Public || m.DefaultPermissions["contents"] != "read" || m.DefaultPermissions["statuses"] != "write" || len(m.DefaultEvents) != 2 {
		t.Fatalf("manifest: %+v", m)
	}
	// The browser posts the manifest form; GitHub redirects back with a code.
	raw, _ := json.Marshal(m)
	nc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := nc.PostForm(c.E.NewAppURL("", "st4te"), url.Values{"manifest": {string(raw)}})
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := url.Parse(res.Header.Get("Location"))
	if res.StatusCode != 302 || loc.Query().Get("state") != "st4te" || !strings.HasPrefix(loc.String(), m.RedirectURL) {
		t.Fatalf("redirect: %d %s", res.StatusCode, loc)
	}
	cred, err := c.ConvertManifest(ctx, loc.Query().Get("code"))
	if err != nil {
		t.Fatal(err)
	}
	if cred.Slug != "tiffin-box" || cred.WebhookSecret == "" || cred.Check() != nil {
		t.Fatalf("credentials: %+v", cred)
	}
	if _, err := c.ConvertManifest(ctx, loc.Query().Get("code")); !ghapp.IsStatus(err, 404) {
		t.Fatalf("a code works once: %v", err)
	}
	if _, err := c.ConvertManifest(ctx, "../evil"); err == nil {
		t.Fatal("bad codes must be refused before calling GitHub")
	}

	a, err := c.NewApp(*cred)
	if err != nil {
		t.Fatal(err)
	}
	// The JWT is RS256 with iss = app id and a ≤10 minute life; the fake checks the signature.
	jwt, _ := a.JWT()
	claims, _ := base64.RawURLEncoding.DecodeString(strings.Split(jwt, ".")[1])
	if !strings.Contains(string(claims), `"iss":"`) {
		t.Fatalf("claims: %s", claims)
	}
	if info, err := a.Info(ctx); err != nil || info.Slug != "tiffin-box" {
		t.Fatalf("info: %v %+v", err, info)
	}
	// A JWT signed with another key is refused.
	other, _ := f.CreateApp("impostor", false)
	if _, err := a.Info(ctx); !ghapp.IsStatus(err, 401) {
		t.Fatalf("a JWT from another key must be refused: %v", err)
	}
	b, _ := c.NewApp(other)

	sha, err := f.AddRepo("octo/site", true, map[string]string{"package.json": `{"dependencies":{"next":"15"}}`, "web/index.html": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.AddRepo("octo/other", false, map[string]string{"README.md": "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.AddRepo("acme/secret", true, map[string]string{"README.md": "x"}); err != nil {
		t.Fatal(err)
	}
	inst := f.Install("octo", nil)
	if got, err := b.RepoInstallation(ctx, "octo/site"); err != nil || got != inst {
		t.Fatalf("repo installation: %v %d", err, got)
	}
	ins, err := b.Installations(ctx)
	if err != nil || len(ins) != 1 || ins[0].Account.Login != "octo" || ins[0].RepositorySelection != "all" {
		t.Fatalf("installations: %v %+v", err, ins)
	}
	repos, err := b.Repos(ctx, inst)
	if err != nil || len(repos) != 2 || repos[0].FullName != "octo/other" || !repos[1].Private {
		t.Fatalf("repos (private included, other accounts excluded): %v %+v", err, repos)
	}
	t1, _ := b.InstallationToken(ctx, inst)
	t2, _ := b.InstallationToken(ctx, inst)
	if t1 == "" || t1 != t2 {
		t.Fatal("installation tokens are cached")
	}
	if br, err := b.Branches(ctx, inst, "octo/site"); err != nil || len(br) != 1 || br[0] != "main" {
		t.Fatalf("branches: %v %v", err, br)
	}
	tree, _, err := b.Tree(ctx, inst, "octo/site", "main")
	if err != nil || len(tree) < 3 {
		t.Fatalf("tree: %v %+v", err, tree)
	}
	if body, err := b.File(ctx, inst, "octo/site", "main", "package.json"); err != nil || !strings.Contains(string(body), "next") {
		t.Fatalf("file: %v %s", err, body)
	}
	if cm, err := b.Commit(ctx, inst, "octo/site", "main"); err != nil || cm.SHA != sha || cm.Message != "first commit" {
		t.Fatalf("commit: %v %+v", err, cm)
	}
	if err := b.SetStatus(ctx, inst, "octo/site", sha, ghapp.Status{State: "pending", Context: "tiffin/web", Description: strings.Repeat("x", 300)}); err != nil {
		t.Fatal(err)
	}
	if d := f.Recorded().Statuses[0].Body["description"].(string); len([]rune(d)) != 140 {
		t.Fatalf("descriptions are clipped to 140: %d", len(d))
	}
	id, err := b.CreateDeployment(ctx, inst, "octo/site", ghapp.DeploymentRequest{Ref: sha, Environment: "production", ProductionEnvironment: true})
	if err != nil || id == 0 {
		t.Fatal(err)
	}
	if f.Recorded().Deployments[0].Body["required_contexts"] == nil {
		t.Fatal("deployments must not wait for other checks")
	}
	if err := b.SetDeploymentStatus(ctx, inst, "octo/site", id, ghapp.DeploymentStatus{State: "success", EnvironmentURL: "https://x"}); err != nil {
		t.Fatal(err)
	}
	cid, err := b.UpsertComment(ctx, inst, "octo/site", 7, 0, "one")
	if err != nil {
		t.Fatal(err)
	}
	if again, err := b.UpsertComment(ctx, inst, "octo/site", 7, cid, "two"); err != nil || again != cid || f.Recorded().Comments[cid].Body != "two" {
		t.Fatalf("update in place: %v %d %+v", err, again, f.Recorded().Comments[cid])
	}
	if fresh, err := b.UpsertComment(ctx, inst, "octo/site", 7, 99999999, "three"); err != nil || fresh == cid {
		t.Fatalf("a deleted comment is recreated: %v %d", err, fresh)
	}
	if _, err := b.Repo(ctx, inst, "acme/secret"); !ghapp.IsStatus(err, 404) {
		t.Fatalf("another account's repo is out of reach: %v", err)
	}

	// A clone token reads one repository and nothing else, and can be revoked.
	ct, err := b.CloneToken(ctx, inst, "octo/site")
	if err != nil {
		t.Fatal(err)
	}
	get := func(tok, path string) int {
		req, _ := http.NewRequest("GET", c.E.API+path, nil)
		req.Header.Set("Authorization", "token "+tok)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if get(ct, "/repos/octo/site") != 200 || get(ct, "/repos/octo/other") != 404 {
		t.Fatal("clone tokens reach only their repository")
	}
	if err := b.RevokeToken(ctx, ct); err != nil || get(ct, "/repos/octo/site") != 401 {
		t.Fatalf("revoked: %v", err)
	}

	// Installation checks for a shared app: the OAuth code proves which of
	// the installation's repositories the person can push to.
	cred2 := other
	b2, _ := c.NewApp(cred2)
	got, err := b2.UserRepos(ctx, f.UserCode(inst, map[string]string{"octo/site": "write", "octo/other": "read"}), inst)
	if err != nil || len(got) != 1 || got[0].FullName != "octo/site" || got[0].ID == 0 {
		t.Fatalf("user repos (push access only): %v %+v", err, got)
	}
	if got, err := b2.UserRepos(ctx, f.OAuthCode(inst), inst); err != nil || len(got) != 2 {
		t.Fatalf("user repos (push everywhere): %v %+v", err, got)
	}
	if _, err := b2.UserRepos(ctx, f.OAuthCode(inst), inst+1); err == nil {
		t.Fatal("another installation must fail")
	}
	if _, err := b2.UserRepos(ctx, "nope", inst); err == nil {
		t.Fatal("a bad code must fail")
	}
}

func TestVerify(t *testing.T) {
	body := []byte(`{"zen":"Keep it logically awesome."}`)
	sig := ghapp.Sign("s3cret", body)
	if err := ghapp.Verify("s3cret", body, sig); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ secret, sig string }{
		{"s3cret", ""},
		{"s3cret", "sha1=" + sig[7:]},
		{"s3cret", "sha256=zz"},
		{"s3cret", "sha256=" + strings.Repeat("0", 64)},
		{"other", sig},
		{"", sig},
	} {
		if ghapp.Verify(c.secret, body, c.sig) == nil {
			t.Errorf("accepted %q with %q", c.sig, c.secret)
		}
	}
	if ghapp.Verify("s3cret", append(body, ' '), sig) == nil {
		t.Error("a changed body must not verify")
	}
}

func TestEventParsing(t *testing.T) {
	push, err := ghapp.Parse[ghapp.PushEvent]([]byte(`{"ref":"refs/heads/feat/x","after":"` + strings.Repeat("a", 40) + `","repository":{"full_name":"o/r","pushed_at":1700000000},"installation":{"id":5}}`))
	if err != nil || push.Branch() != "feat/x" || push.Repository.PushedAt.Unix() != 1700000000 || push.Installation.ID != 5 {
		t.Fatalf("push: %v %+v", err, push)
	}
	if tag, _ := ghapp.Parse[ghapp.PushEvent]([]byte(`{"ref":"refs/tags/v1"}`)); tag.Branch() != "" {
		t.Fatal("tags have no branch")
	}
	pr, err := ghapp.Parse[ghapp.PullRequestEvent]([]byte(`{"action":"opened","number":3,"pull_request":{"updated_at":"2026-10-03T10:00:00Z","head":{"sha":"x","repo":{"full_name":"stranger/r"}},"base":{"repo":{"full_name":"o/r"}}}}`))
	if err != nil || !pr.FromFork() || pr.PullRequest.UpdatedAt.Year() != 2026 {
		t.Fatalf("pr: %v %+v", err, pr)
	}
	same, _ := ghapp.Parse[ghapp.PullRequestEvent]([]byte(`{"pull_request":{"head":{"repo":{"full_name":"O/R"}},"base":{"repo":{"full_name":"o/r"}}}}`))
	if same.FromFork() {
		t.Fatal("same repository, different case, is not a fork")
	}
	gone, _ := ghapp.Parse[ghapp.PullRequestEvent]([]byte(`{"pull_request":{"head":{"repo":null},"base":{"repo":{"full_name":"o/r"}}}}`))
	if !gone.FromFork() {
		t.Fatal("a deleted head repository counts as a fork")
	}
	for s, want := range map[string]bool{"o/r": true, "my-org/my.repo_1": true, "o/..": false, "o/r.git": false, "-o/r": false, "o": false, "o/r/x": false} {
		if ghapp.ValidRepo(s) != want {
			t.Errorf("ValidRepo(%q) != %v", s, want)
		}
	}
	if !ghapp.ValidSHA(strings.Repeat("a", 40)) || ghapp.ValidSHA("HEAD") {
		t.Error("ValidSHA")
	}
	if ghapp.Clip("  héllo world ", 5) != "héll…" {
		t.Errorf("clip: %q", ghapp.Clip("  héllo world ", 5))
	}
}

func TestURLs(t *testing.T) {
	e := ghapp.GitHubCom
	if got := e.NewAppURL("", "s"); got != "https://github.com/settings/apps/new?state=s" {
		t.Error(got)
	}
	if got := e.NewAppURL("acme", "s"); got != "https://github.com/organizations/acme/settings/apps/new?state=s" {
		t.Error(got)
	}
	if got := e.InstallURL("tiffin-box", ""); got != "https://github.com/apps/tiffin-box/installations/new" {
		t.Error(got)
	}
	if got := e.AppSettingsURL(&ghapp.Credentials{Slug: "x", Owner: "acme", OwnerType: "Organization"}); got != "https://github.com/organizations/acme/settings/apps/x" {
		t.Error(got)
	}
	if !e.IsGitHubCom() || (ghapp.Endpoints{Web: "https://ghe.corp"}).IsGitHubCom() {
		t.Error("IsGitHubCom")
	}
}
