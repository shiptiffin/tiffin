package runtime

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/manifest"
	"github.com/shiptiffin/tiffin/internal/starters"
)

// apiCall serves the full API over the harness's platform.
func (h *harness) apiCall(t *testing.T) func(method, path, body string) (int, map[string]any) {
	owner, _, err := h.p.Tokens.Bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a := api.New(api.Deps{DB: h.p.DB, Engine: h.p.Engine, Tokens: h.p.Tokens, Platform: h.p})
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	return func(method, path, body string) (int, map[string]any) {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rd)
		req.Header.Set("Authorization", "Bearer "+owner)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
}

func (h *harness) buildLog(app, id string) string {
	d, _ := h.r.st.getDeploy(context.Background(), "shop", app, id)
	text, _ := h.r.readBuildLog(d, 0, 1<<20)
	return string(text)
}

// The API's enum (and so the CLI, MCP and dashboard types generated from
// it) takes every starter id.
func TestTemplateEnumMatchesStarters(t *testing.T) {
	f, _ := reflect.TypeFor[templateBody]().FieldByName("Template")
	if got, want := f.Tag.Get("enum"), strings.Join(starters.IDs(), ","); got != want {
		t.Fatalf("update templateBody's enum tag to %q (it is %q)", want, got)
	}
}

func TestDeployTemplate(t *testing.T) {
	h := newHarness(t)
	call := h.apiCall(t)

	code, list := call("GET", "/v1/templates", "")
	if code != 200 || len(list["templates"].([]any)) != len(starters.IDs()) {
		t.Fatalf("templates: %d %v", code, list)
	}

	// The static starter onto the static app.
	code, d := call("POST", "/v1/projects/shop/apps/site/deploys/template", `{"template":"static-site"}`)
	if code != 202 || d["status"] != StatusQueued || d["source"] != SourceTemplate || d["template"] != "static-site" {
		t.Fatalf("deploy template: %d %v", code, d)
	}
	live := h.wait("site", d["id"].(string))
	if live.Status != StatusLive {
		t.Fatalf("not live: %+v\n%s", live, h.buildLog("site", live.ID))
	}
	if code, body := h.get("shop.tiffin.localhost", "/"); code != 200 || !strings.Contains(body, "It's live.") {
		t.Fatalf("served: %d %s", code, body)
	}
	if log := h.buildLog("site", live.ID); !strings.Contains(log, "==> template static-site (HTML)") || !strings.Contains(log, "==> deploy "+live.ID) {
		t.Fatalf("build log:\n%s", log)
	}

	// Preconditions come back as 409s with a hint, before anything is queued.
	for _, c := range []struct{ app, tmpl, want string }{
		{"api", "nextjs", "framework hono"},
		{"nope", "guestbook", ""},
	} {
		code, prob := call("POST", "/v1/projects/shop/apps/"+c.app+"/deploys/template", `{"template":"`+c.tmpl+`"}`)
		if c.app == "nope" {
			if code != 404 {
				t.Errorf("unknown app: %d %v", code, prob)
			}
			continue
		}
		if code != 409 || !strings.Contains(prob["detail"].(string), c.want) || prob["hint"] == "" {
			t.Errorf("%s on %s: %d %v", c.tmpl, c.app, code, prob)
		}
	}
	if code, prob := call("POST", "/v1/projects/shop/apps/api/deploys/template", `{"template":"rails"}`); code != 422 {
		t.Fatalf("unknown template: %d %v", code, prob)
	}

	// Switch Postgres on, then the notes API deploys to the hono app.
	h.mf.Services.Postgres = &manifest.Postgres{}
	h.apply()
	code, d = call("POST", "/v1/projects/shop/apps/api/deploys/template?preview=try-it", `{"template":"hono"}`)
	if code != 202 || d["template"] != "hono" {
		t.Fatalf("hono: %d %v", code, d)
	}
	if got := h.wait("api", d["id"].(string)); got.Status != StatusLive || got.Preview != "try-it" || got.Image == "" {
		t.Fatalf("hono: %+v\n%s", got, h.buildLog("api", got.ID))
	}
}

// gitRepoServer serves a git repository over HTTPS (git http-backend) and
// returns its URL, the commit and a CA file that trusts the server.
func gitRepoServer(t *testing.T, files map[string]string) (string, string, string) {
	t.Helper()
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skip("git not installed")
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend not found")
	}
	work, root := t.TempDir(), t.TempDir()
	for n, b := range files {
		p := filepath.Join(work, n)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main"}, args...)...)
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(work, "init", "-q")
	git(work, "add", ".")
	git(work, "commit", "-q", "-m", "first")
	sha := git(work, "rev-parse", "HEAD")
	git(work, "tag", "v1")
	git(root, "clone", "-q", "--bare", work, filepath.Join(root, "site.git"))
	srv := httptest.NewTLSServer(&cgi.Handler{Path: backend, Root: "/git",
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}})
	t.Cleanup(srv.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	return srv.URL + "/git/site.git", sha, ca
}

func TestDeployFromGit(t *testing.T) {
	repo, sha, ca := gitRepoServer(t, map[string]string{
		"README.md":       "a monorepo",
		"web/index.html":  "<h1>from git</h1>",
		"web/.gitignore":  "secret.txt\n",
		"apps/x/main.txt": "not a site",
	})
	gitAllowPrivate, gitExtraConfig = true, []string{"http.sslCAInfo=" + ca}
	t.Cleanup(func() { gitAllowPrivate, gitExtraConfig = false, nil })
	h := newHarness(t)
	call := h.apiCall(t)

	code, d := call("POST", "/v1/projects/shop/apps/site/deploys/git", `{"url":"`+repo+`","path":"web"}`)
	if code != 202 || d["source"] != SourceGit || d["repo"] != repo {
		t.Fatalf("deploy git: %d %v", code, d)
	}
	got := h.wait("site", d["id"].(string))
	log := h.buildLog("site", got.ID)
	if got.Status != StatusLive || got.Commit != sha {
		t.Fatalf("git deploy: %+v\n%s", got, log)
	}
	for _, want := range []string{"==> cloning " + repo + " (default branch, depth 1)", "==> cloned commit " + sha[:12], "==> source: 2 files"} {
		if !strings.Contains(log, want) {
			t.Errorf("build log lacks %q:\n%s", want, log)
		}
	}
	if code, body := h.get("shop.tiffin.localhost", "/"); code != 200 || body != "<h1>from git</h1>" {
		t.Fatalf("served: %d %s", code, body)
	}

	// A tag works; failures fail the deploy with a hint, never the app.
	for _, c := range []struct{ body, status, want string }{
		{`{"url":"` + repo + `","ref":"v1","path":"web"}`, StatusLive, ""},
		{`{"url":"` + repo + `","ref":"nope"}`, StatusFailed, "no such branch, tag or commit"},
		{`{"url":"` + repo + `","path":"missing"}`, StatusFailed, `path "missing" is not a directory`},
		{`{"url":"` + strings.Replace(repo, "site.git", "gone.git", 1) + `"}`, StatusFailed, "repository not found"},
	} {
		code, d := call("POST", "/v1/projects/shop/apps/site/deploys/git", c.body)
		if code != 202 {
			t.Fatalf("%s: %d %v", c.body, code, d)
		}
		got := h.wait("site", d["id"].(string))
		if got.Status != c.status || !strings.Contains(got.Error, c.want) || (c.status == StatusFailed && got.Hint == "") {
			t.Errorf("%s: %s %q hint %q\n%s", c.body, got.Status, got.Error, got.Hint, h.buildLog("site", got.ID))
		}
	}
	if code, body := h.get("shop.tiffin.localhost", "/"); code != 200 || body != "<h1>from git</h1>" {
		t.Fatalf("failed deploys must leave the live one serving: %d %s", code, body)
	}

	// Bad URLs are refused up front.
	gitAllowPrivate = false
	for _, u := range []string{"http://github.com/a/b", "https://user:pw@github.com/a/b", "file:///etc", repo} {
		if code, prob := call("POST", "/v1/projects/shop/apps/site/deploys/git", `{"url":"`+u+`"}`); code != 422 || prob["hint"] == "" {
			t.Errorf("%s: %d %v", u, code, prob)
		}
	}
}

func TestCheckGitSource(t *testing.T) {
	dns := map[string][]string{
		"github.com":       {"140.82.112.3"},
		"internal.corp":    {"10.0.0.5"},
		"mixed.example":    {"93.184.216.34", "127.0.0.1"},
		"v6.example":       {"2606:4700::6810:84e5"},
		"nat64.example":    {"64:ff9b::a00:1"},
		"cgnat.example":    {"100.64.1.1"},
		"metadata.example": {"169.254.169.254"},
	}
	resolve := func(_ context.Context, host string) ([]netip.Addr, error) {
		var out []netip.Addr
		for _, s := range dns[host] {
			out = append(out, netip.MustParseAddr(s))
		}
		return out, nil
	}
	ok := []struct{ url, ref, path, want, wantPath string }{
		{"https://github.com/octocat/Spoon-Knife", "", "", "https://github.com/octocat/Spoon-Knife", ""},
		{"https://GitHub.com:443/a/b.git", "main", "./apps/web/", "https://github.com/a/b.git", "apps/web"},
		{"https://v6.example/a/b", "v1.2.3", ".", "https://v6.example/a/b", ""},
		{"https://140.82.112.3/a/b", "0123456789abcdef0123456789abcdef01234567", "", "https://140.82.112.3/a/b", ""},
	}
	for _, c := range ok {
		g, err := checkGitSource(context.Background(), c.url, c.ref, c.path, resolve)
		if err != nil {
			t.Errorf("%s: %v", c.url, err)
			continue
		}
		if g.String() != c.want || g.Path != c.wantPath || g.Ref != c.ref || len(g.IPs) == 0 {
			t.Errorf("%s: %+v", c.url, g)
		}
	}
	bad := []struct{ url, ref, path, want string }{
		{"", "", "", "required"},
		{"http://github.com/a/b", "", "", "only https://"},
		{"git@github.com:a/b.git", "", "", "https://host/owner/repo"},
		{"ssh://github.com/a/b", "", "", "only https://"},
		{"file:///srv/repo", "", "", "only https://"},
		{"https://tok:x-oauth@github.com/a/b", "", "", "credentials"},
		{"https://github.com", "", "", "repository path"},
		{"https://github.com:8443/a/b", "", "", "port"},
		{"https://github.com/a/b?x=1", "", "", "query"},
		{"https://localhost/a/b", "", "", "not public"},
		{"https://dashboard.tiffin.localhost/a", "", "", "not public"},
		{"https://127.0.0.1/a/b", "", "", "not a public address"},
		{"https://[::1]/a/b", "", "", "not a public address"},
		{"https://169.254.169.254/latest", "", "", "not a public address"},
		{"https://internal.corp/a/b", "", "", "10.0.0.5"},
		{"https://mixed.example/a/b", "", "", "127.0.0.1"},
		{"https://nat64.example/a/b", "", "", "not a public address"},
		{"https://cgnat.example/a/b", "", "", "not a public address"},
		{"https://metadata.example/a/b", "", "", "not a public address"},
		{"https://nowhere.example/a/b", "", "", "cannot resolve"},
		{"https://github.com/a/b", "--upload-pack=x", "", "ref must be"},
		{"https://github.com/a/b", "a..b", "", "ref must be"},
		{"https://github.com/a/b", "", "../etc", "inside the repository"},
		{"https://github.com/a/b", "", "/etc", "inside the repository"},
	}
	for _, c := range bad {
		_, err := checkGitSource(context.Background(), c.url, c.ref, c.path, resolve)
		var ue *gitURLError
		if err == nil || !strings.Contains(err.Error(), c.want) || !errorsAs(err, &ue) {
			t.Errorf("%s ref=%q path=%q: got %v, want %q", c.url, c.ref, c.path, err, c.want)
		}
	}
}

func errorsAs(err error, ue **gitURLError) bool {
	e, ok := err.(*gitURLError)
	*ue = e
	return ok
}

func TestLineWriterKeepsFinalProgress(t *testing.T) {
	var b strings.Builder
	lw := &lineWriter{w: &b}
	_, _ = io.WriteString(lw, "Receiving objects:  10%\rReceiving objects: 100% (3/3), done.\nResolving deltas: 0%\rResolving deltas: 100%\n")
	if got := b.String(); got != "Receiving objects: 100% (3/3), done.\nResolving deltas: 100%\n" {
		t.Fatalf("%q", got)
	}
}
