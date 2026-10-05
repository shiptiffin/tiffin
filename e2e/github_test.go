//go:build e2e

package e2e

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestGitHub is "connect GitHub, import a repo, push to deploy, a preview per
// pull request" on a real box, against a fake GitHub running inside the VM
// (internal/mod/runtime/ghapp/ghfake over HTTPS, with real git repositories
// served over smart HTTP behind installation tokens):
//
//	github connect → the browser steps of the manifest flow (form POST to
//	GitHub, back to the box's callback, GitHub's install page, back to the
//	box's setup page) → github status shows the installation → github repos /
//	github repo (folders and framework detected) → apply a manifest with
//	git: {repo, branch, path} → deploys github (first deploy) → a commit and
//	a signed push webhook deploy the new commit → commit statuses and
//	deployments on GitHub → a pull request builds a preview at its own
//	address with one comment → closing it removes the preview → a fork's
//	pull request builds nothing.
//
// TIFFIN_E2E_GH_INSTANCE / _DISK / _PORT reuse a named dev box (it is
// created when missing and left in place).
func TestGitHub(t *testing.T) {
	start := time.Now()
	phase := phaseLogger(t)
	b := newGHBox(t)
	phase("up", start)

	// ---- the fake GitHub inside the VM, trusted like a real CA ----
	p := time.Now()
	fake := filepath.Join(b.dir, "fakegithub")
	build := exec.Command("go", "build", "-tags", "e2e", "-o", fake, "./e2e/fakegithub")
	build.Dir = RepoRoot()
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+HostArch())
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fakegithub: %v\n%s", err, out)
	}
	if out, err := exec.Command("limactl", "copy", fake, b.instance+":/tmp/fakegithub").CombinedOutput(); err != nil {
		t.Fatalf("copy fakegithub: %v\n%s", err, out)
	}
	b.inBox(`sudo systemctl stop e2e-fakegithub 2>/dev/null; sudo systemctl reset-failed e2e-fakegithub 2>/dev/null; sudo rm -rf /var/lib/fakegithub; ` +
		`sudo systemd-run --unit e2e-fakegithub /tmp/fakegithub -addr 127.0.0.1:9443 -dir /var/lib/fakegithub ` +
		`-hook http://127.0.0.1:7070/v1/github/webhook -ca /tmp/fakegithub-ca.pem && sleep 1 && ` +
		`sudo cp /tmp/fakegithub-ca.pem /usr/local/share/ca-certificates/fakegithub.crt && sudo update-ca-certificates >/dev/null && ` +
		`echo '{"apiUrl":"https://127.0.0.1:9443/api","webUrl":"https://127.0.0.1:9443"}' | sudo tee /var/lib/tiffin/platform/github.json >/dev/null && ` +
		`sudo systemctl restart tiffin && for i in $(seq 60); do curl -sf http://127.0.0.1:7070/v1/health >/dev/null && break; sleep 0.5; done`)
	phase("fake github", p)

	// ---- Connect GitHub: the manifest flow, step by step as a browser ----
	p = time.Now()
	if st := b.ok("github", "status"); st["connected"] == true {
		b.ok("github", "disconnect") // a kept dev box from an earlier run
	}
	if st := b.ok("github", "status"); st["connected"] != false || st["reachable"] != true {
		t.Fatalf("status before connecting: %v", st)
	}
	start2 := b.ok("github", "connect")
	cb := b.ghBrowserPost(start2["url"].(string), start2["manifest"].(string)) // GitHub → the box's callback
	install := b.boxRedirect(cb)                                               // the box → GitHub's install page
	setup := b.ghBrowserGet(install)                                           // GitHub → the box's setup page
	back := b.boxRedirect(setup)                                               // the box → Settings › Git
	if u, _ := url.Parse(back); u.Path != "/settings/git" || u.Query().Get("installed") != "1" {
		t.Fatalf("after installing: %s", back)
	}
	st := b.ok("github", "status")
	ins, _ := st["installations"].([]any)
	if st["connected"] != true || st["source"] != "box" || len(ins) != 1 {
		t.Fatalf("status: %v", st)
	}
	t.Logf("connected: app %v, installed on %v", st["app"].(map[string]any)["slug"], ins[0].(map[string]any)["account"])
	phase("connect", p)

	// ---- import: list, look inside, apply with git, first deploy ----
	p = time.Now()
	first := b.fake("repo", map[string]any{"repo": "octo/shop", "private": true, "files": map[string]string{
		"README.md": "the shop", "web/index.html": "<h1>shop v1</h1>", "api/package.json": `{"name":"api","scripts":{"start":"bun index.ts"}}`,
		"api/index.ts": apiSource("api v1"),
	}})["sha"].(string)
	repos := b.ok("github", "repos", "--q", "shop")
	if rs := repos["repos"].([]any); len(rs) != 1 || rs[0].(map[string]any)["private"] != true {
		t.Fatalf("repos: %v", repos)
	}
	det := b.ok("github", "repo", "octo", "shop")
	if !strings.Contains(fmt.Sprint(det["roots"]), "path:web") || !strings.Contains(fmt.Sprint(det["roots"]), "framework:bun") {
		t.Fatalf("detected folders: %v", det["roots"])
	}
	b.apply("ghshop", `{"project":"ghshop","apps":{"site":{"framework":"static","routes":["ghshop"],"git":{"repo":"octo/shop","branch":"main","path":"web"}},`+
		`"api":{"framework":"bun","routes":["ghshop/api"],"git":{"repo":"octo/shop","branch":"main","path":"api","previews":"off"}}}}`)
	if pm := b.ok("projects", "manifest", "ghshop"); !strings.Contains(pm["config"].(string), `git: { repo: "octo/shop", path: "web" }`) {
		t.Fatalf("the git block round-trips through tiffin pull: %s", pm["config"])
	}
	d := b.ok("deploys", "github", "ghshop", "site")
	if d["commit"] != first || d["trigger"] != "redeploy" {
		t.Fatalf("deploys github: %v", d)
	}
	waitDeploy(t, b, "ghshop", "site", d["id"].(string), 5*time.Minute)
	// A container app from the same repository: cloned, built with Railpack, health-checked.
	da := b.ok("deploys", "github", "ghshop", "api")
	built := waitDeploy(t, b, "ghshop", "api", da["id"].(string), 15*time.Minute)
	t.Logf("api (Bun, Railpack) first deploy: build %vs, total %vs", orZero(built["buildSeconds"]), orZero(built["durationSeconds"]))
	c := b.https()
	if code, _, body := b.get(c, "GET", b.url("ghshop")+"/", nil); code != 200 || !strings.Contains(body, "shop v1") {
		t.Fatalf("first deploy: %d %s", code, body)
	}
	phase("import+deploy", p)

	// ---- push → deploy ----
	p = time.Now()
	sha2 := b.fake("commit", map[string]any{"repo": "octo/shop", "branch": "main", "message": "Say v2", "files": map[string]string{"web/index.html": "<h1>shop v2</h1>", "api/index.ts": apiSource("api v2")}})["sha"].(string)
	pushed := time.Now()
	if dl := b.fake("push", map[string]any{"repo": "octo/shop", "branch": "main"}); dl["status"] != float64(202) {
		t.Fatalf("push delivery: %v", dl)
	}
	live := b.waitCommit("ghshop", "site", sha2, 5*time.Minute)
	if live["message"] != "Say v2" || live["trigger"] != "push" {
		t.Fatalf("push deploy: %v", live)
	}
	if _, _, body := b.get(c, "GET", b.url("ghshop")+"/", nil); !strings.Contains(body, "shop v2") {
		t.Fatalf("after the push: %s", body)
	}
	apiLive := b.waitCommit("ghshop", "api", sha2, 15*time.Minute)
	if code, _, body := b.get(c, "GET", b.url("ghshop")+"/api", nil); code != 200 || !strings.Contains(body, "api v2") {
		t.Fatalf("api after the push: %d %s", code, body)
	}
	t.Logf("push → api (Bun) live in %s (build %vs)", time.Since(pushed).Round(100*time.Millisecond), orZero(apiLive["buildSeconds"]))
	t.Logf("push → live in %s (build %vs)", time.Since(pushed).Round(100*time.Millisecond), orZero(live["buildSeconds"]))
	time.Sleep(time.Second) // statuses are reported right after the deploy turns live
	state := b.fake("state", nil)
	var states []string
	for _, s := range state["statuses"].([]any) {
		s := s.(map[string]any)
		if strings.HasSuffix(s["path"].(string), sha2) && s["body"].(map[string]any)["context"] == "tiffin/ghshop/site" {
			states = append(states, s["body"].(map[string]any)["state"].(string))
		}
	}
	if strings.Join(states, ",") != "pending,success" {
		t.Fatalf("commit statuses for %s: %v", sha2, states)
	}
	phase("push", p)

	// ---- pull request → preview → closed → removed ----
	p = time.Now()
	b.fake("commit", map[string]any{"repo": "octo/shop", "branch": "feat", "message": "A feature", "files": map[string]string{"web/index.html": "<h1>feature preview</h1>"}})
	opened := time.Now()
	if dl := b.fake("pull", map[string]any{"repo": "octo/shop", "action": "opened", "number": 3, "branch": "feat"}); !strings.Contains(fmt.Sprint(dl["reply"]), "preview") {
		t.Fatalf("pull request delivery: %v", dl)
	}
	pv := b.waitPreview("ghshop", "site", "pr-3", 5*time.Minute)
	pvURL := fmt.Sprintf("https://pr-3--ghshop.tiffin.localhost:%d", b.port)
	if pv["url"] != pvURL {
		t.Fatalf("preview url: %v", pv["url"])
	}
	if code, _, body := b.get(c, "GET", pvURL+"/", nil); code != 200 || !strings.Contains(body, "feature preview") {
		t.Fatalf("preview: %d %s", code, body)
	}
	if _, _, body := b.get(c, "GET", b.url("ghshop")+"/", nil); !strings.Contains(body, "shop v2") {
		t.Fatalf("a preview must not touch production: %s", body)
	}
	t.Logf("pull request → preview live in %s", time.Since(opened).Round(100*time.Millisecond))
	time.Sleep(time.Second)
	comments := b.fake("state", nil)["comments"].([]any)
	if len(comments) != 1 || !strings.Contains(comments[0].(map[string]any)["body"].(string), pvURL) {
		t.Fatalf("one comment with the preview's address: %v", comments)
	}
	if dl := b.fake("pull", map[string]any{"repo": "octo/shop", "action": "closed", "number": 3, "branch": "feat"}); !strings.Contains(fmt.Sprint(dl["reply"]), "removed") {
		t.Fatalf("closed: %v", dl)
	}
	if prs := b.list("previews", "list", "ghshop", "site"); len(prs) != 0 {
		t.Fatalf("previews after closing: %v", prs)
	}
	if code, _, _ := b.get(c, "GET", pvURL+"/", nil); code == 200 {
		t.Fatal("the closed pull request's preview still answers")
	}
	comments = b.fake("state", nil)["comments"].([]any)
	if !strings.Contains(comments[0].(map[string]any)["body"].(string), "was removed") {
		t.Fatalf("comment after closing: %v", comments[0])
	}
	// A fork's pull request builds nothing (forks are off by default).
	if dl := b.fake("pull", map[string]any{"repo": "octo/shop", "action": "opened", "number": 4, "branch": "feat", "fork": true}); !strings.Contains(fmt.Sprint(dl["reply"]), "forks are off") {
		t.Fatalf("fork: %v", dl)
	}
	phase("pull request", p)

	// Every clone used a read-only token for one repository, revoked afterwards.
	state = b.fake("state", nil)
	t.Logf("fake GitHub saw %d statuses, %d deployments, %d deployment statuses, %v clones; %v tokens minted, %v revoked",
		len(state["statuses"].([]any)), len(state["deployments"].([]any)), len(state["deploymentStatuses"].([]any)), len(state["clones"].([]any)),
		state["tokens"], state["revokedTokens"])
	phase("total", start)
}

// newGHBox is newCLIBox, optionally on a named dev box that is kept.
func newGHBox(t *testing.T) *cliBox {
	t.Helper()
	inst := os.Getenv("TIFFIN_E2E_GH_INSTANCE")
	if inst == "" {
		return newCLIBox(t, "github", "ghshop")
	}
	RequireLima(t)
	dir := t.TempDir()
	b := &cliBox{t: t, dir: dir, project: "ghshop", instance: inst}
	fmt.Sscan(os.Getenv("TIFFIN_E2E_GH_PORT"), &b.port)
	b.cli = buildTiffin(t, dir, "", "")
	bin := buildTiffin(t, dir, "linux", "0.0.1-github")
	b.env = append(os.Environ(),
		"TIFFIN_CONFIG_DIR="+filepath.Join(dir, "config"),
		"TIFFIN_LIMA_INSTANCE="+inst, "TIFFIN_LIMA_DISK="+os.Getenv("TIFFIN_E2E_GH_DISK"), fmt.Sprintf("TIFFIN_LIMA_PORT=%d", b.port),
		"TIFFIN_LIMA_MEMORY=3GiB", "TIFFIN_HOME=", "TIFFIN_URL=", "TIFFIN_TOKEN=")
	b.ok("up", "--binary", bin)
	return b
}

// fake calls the fake GitHub's control API inside the VM.
func (b *cliBox) fake(what string, body any) map[string]any {
	b.t.Helper()
	method, data := "GET", ""
	if body != nil {
		raw, _ := json.Marshal(body)
		method, data = "POST", base64.StdEncoding.EncodeToString(raw)
	}
	out := b.inBox(fmt.Sprintf(`echo %q | base64 -d | curl -sS --cacert /tmp/fakegithub-ca.pem -X %s --data-binary @- https://127.0.0.1:9443/_fake/%s`, data, method, what))
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		b.t.Fatalf("fake %s: %s", what, out)
	}
	if msg, ok := m["message"]; ok && len(m) == 1 {
		b.t.Fatalf("fake %s: %v", what, msg)
	}
	return m
}

// ghBrowserPost submits the manifest form to GitHub (inside the VM, where
// the fake runs) and returns where GitHub redirects the browser.
func (b *cliBox) ghBrowserPost(u, manifest string) string {
	b.t.Helper()
	m := base64.StdEncoding.EncodeToString([]byte(manifest))
	return b.inBox(fmt.Sprintf(`echo %s | base64 -d > /tmp/manifest.json && curl -sS --cacert /tmp/fakegithub-ca.pem -o /dev/null -w '%%{redirect_url}' --data-urlencode manifest@/tmp/manifest.json %q`, m, u))
}

// ghBrowserGet opens a GitHub page and returns its redirect.
func (b *cliBox) ghBrowserGet(u string) string {
	b.t.Helper()
	return b.inBox(fmt.Sprintf(`curl -sS --cacert /tmp/fakegithub-ca.pem -o /dev/null -w '%%{redirect_url}' %q`, u))
}

// boxRedirect opens a page on the box (from the Mac, like the browser) and
// returns where it redirects.
func (b *cliBox) boxRedirect(u string) string {
	b.t.Helper()
	c := b.https()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := c.Get(u)
	if err != nil {
		b.t.Fatalf("GET %s: %v", u, err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusFound {
		b.t.Fatalf("GET %s: %d, want a redirect", u, res.StatusCode)
	}
	return res.Header.Get("Location")
}

// waitCommit waits for the deploy of a commit to go live.
func (b *cliBox) waitCommit(project, app, sha string, d time.Duration) map[string]any {
	b.t.Helper()
	return b.waitDeployWhere(project, app, d, func(x map[string]any) bool { return x["commit"] == sha && x["preview"] == nil })
}

func (b *cliBox) waitPreview(project, app, preview string, d time.Duration) map[string]any {
	b.t.Helper()
	return b.waitDeployWhere(project, app, d, func(x map[string]any) bool { return x["preview"] == preview })
}

func (b *cliBox) waitDeployWhere(project, app string, d time.Duration, match func(map[string]any) bool) map[string]any {
	b.t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		list := b.ok("deploys", "list", project, app, "--all")
		for _, x := range list["deploys"].([]any) {
			x := x.(map[string]any)
			if !match(x) {
				continue
			}
			switch x["status"] {
			case "live":
				return x
			case "failed":
				log := b.ok("deploys", "build-log", project, app, x["id"].(string))
				b.t.Fatalf("deploy %s failed: %v\n%s", x["id"], x["error"], log["text"])
			}
		}
		time.Sleep(time.Second)
	}
	b.t.Fatalf("no matching deploy of %s/%s went live", project, app)
	return nil
}

// apiSource is a tiny Bun server that answers with text.
func apiSource(text string) string {
	return `Bun.serve({ port: Number(process.env.PORT ?? 3000), fetch: () => new Response(` + strconv.Quote(text) + `) });
console.log("listening");
`
}
