package runtime

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/mod/runtime/srcpack"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

// The box hosts one bare git repository per project, served over git's
// smart HTTP protocol (git http-backend) at /v1/git/<project>.git. A push
// runs a post-receive hook that calls back into the runtime, which deploys
// every app of the pushed tiffin.config.ts and streams progress to the
// pusher's terminal ("remote: ...").

const gitBackend = "/usr/lib/git-core/git-http-backend"

var repoName = regexp.MustCompile(`^([a-z][a-z0-9-]{0,39})\.git$`)

func (m *Module) registerGit(a huma.API) {
	h := func(hctx huma.Context) {
		req, w := humago.Unwrap(hctx)
		m.serveGit(w, req, hctx.Param("repo"))
	}
	for _, o := range []struct{ method, path string }{
		{http.MethodGet, "/v1/git/{repo}/info/refs"},
		{http.MethodPost, "/v1/git/{repo}/git-upload-pack"},
		{http.MethodPost, "/v1/git/{repo}/git-receive-pack"},
	} {
		a.Adapter().Handle(&huma.Operation{Method: o.method, Path: o.path}, h)
	}
}

func (m *Module) serveGit(w http.ResponseWriter, req *http.Request, repo string) {
	r, err := m.rt()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	mm := repoName.FindStringSubmatch(repo)
	if mm == nil {
		http.Error(w, "repository names are <project>.git", http.StatusNotFound)
		return
	}
	project := mm[1]
	pr, err := r.p.Tokens.Authenticate(req.Context(), gitToken(req))
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="Tiffin (password: a Tiffin token)"`)
		http.Error(w, "authentication required: use any username and a Tiffin token as the password (tiffin git-remote --add sets up a credential helper)", http.StatusUnauthorized)
		return
	}
	push := strings.HasSuffix(req.URL.Path, "/git-receive-pack") || req.URL.Query().Get("service") == "git-receive-pack"
	scope := tokens.ScopeRead
	if push {
		scope = tokens.ScopeApplyReversible
	}
	if err := pr.Require(scope, project); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if v, _, err := r.p.DB.Load(req.Context(), project); err != nil || v == 0 {
		http.Error(w, "project "+project+" does not exist on this box: run tiffin apply first", http.StatusNotFound)
		return
	}
	dir, err := r.ensureRepo(project)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	env := []string{"GIT_PROJECT_ROOT=" + filepath.Dir(dir), "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=" + pr.TokenID, "GIT_HTTP_MAX_REQUEST_BUFFER=1000M"}
	if push && strings.HasSuffix(req.URL.Path, "/git-receive-pack") {
		nonce := r.hooks.issue(project, pr.TokenID)
		env = append(env, "TIFFIN_HOOK_URL=http://"+r.actAddr+"/_tiffin/git-hook?t="+nonce)
	}
	h := &cgi.Handler{Path: gitBackend, Root: "/v1/git", Env: env, Stderr: io.Discard}
	h.ServeHTTP(w, req)
}

// gitToken reads a token from Basic auth (password) or a Bearer header.
func gitToken(req *http.Request) string {
	auth := req.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Basic ") {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(auth, "Basic "))
		if err == nil {
			_, pass, _ := strings.Cut(string(raw), ":")
			return pass
		}
	}
	return auth
}

const postReceive = `#!/bin/sh
# Managed by tiffin: deploy what was pushed. Output reaches the pusher as "remote: ...".
[ -n "$TIFFIN_HOOK_URL" ] || exit 0
exec curl -sS -N --max-time 3600 -H "Content-Type: text/plain" --data-binary @- "$TIFFIN_HOOK_URL"
`

func (r *rt) gitDir(project string) string {
	return filepath.Join(r.opt.DataDir, "git", project+".git")
}

func (r *rt) ensureRepo(project string) (string, error) {
	dir := r.gitDir(project)
	if !exists(filepath.Join(dir, "HEAD")) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		if out, err := exec.Command("git", "init", "--bare", "--initial-branch=main", dir).CombinedOutput(); err != nil {
			return "", fmt.Errorf("git init: %v: %s", err, out)
		}
		_ = exec.Command("git", "--git-dir", dir, "config", "http.receivepack", "true").Run()
	}
	hook := filepath.Join(dir, "hooks", "post-receive")
	if cur, err := os.ReadFile(hook); err != nil || string(cur) != postReceive {
		if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(hook, []byte(postReceive), 0o755); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// hookTokens are single-use nonces tying a post-receive hook call to the
// authenticated push that triggered it.
type hookTokens struct {
	mu sync.Mutex
	m  map[string]hookGrant
}

type hookGrant struct {
	project, token string
	expires        time.Time
}

func newHookTokens() *hookTokens { return &hookTokens{m: map[string]hookGrant{}} }

func (h *hookTokens) issue(project, token string) string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	n := hex.EncodeToString(b)
	h.mu.Lock()
	defer h.mu.Unlock()
	for k, g := range h.m {
		if time.Now().After(g.expires) {
			delete(h.m, k)
		}
	}
	h.m[n] = hookGrant{project: project, token: token, expires: time.Now().Add(time.Hour)}
	return n
}

func (h *hookTokens) take(n string) (hookGrant, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	g, ok := h.m[n]
	delete(h.m, n)
	return g, ok && time.Now().Before(g.expires)
}

// handleGitHook deploys a push: main/master → production, any other branch
// → a preview named after the branch. It streams progress as plain text.
func (r *rt) handleGitHook(w http.ResponseWriter, req *http.Request) {
	g, ok := r.hooks.take(req.URL.Query().Get("t"))
	if !ok || req.Method != http.MethodPost {
		http.Error(w, "unknown hook call", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	rc := http.NewResponseController(w)
	say := func(format string, args ...any) {
		fmt.Fprintf(w, format+"\n", args...)
		_ = rc.Flush()
	}
	sc := bufio.NewScanner(req.Body)
	type update struct{ rev, branch string }
	var updates []update
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 3 || strings.Trim(f[1], "0") == "" || !strings.HasPrefix(f[2], "refs/heads/") {
			continue // deletions and tags deploy nothing
		}
		updates = append(updates, update{rev: f[1], branch: strings.TrimPrefix(f[2], "refs/heads/")})
	}
	ctx := r.ctx
	for _, u := range updates {
		preview := ""
		if u.branch != "main" && u.branch != "master" {
			preview = branchPreview(u.branch)
		}
		r.deployPush(ctx, say, g, u.rev, u.branch, preview)
	}
}

func branchPreview(branch string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(branch) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 30 {
		s = strings.Trim(s[:30], "-")
	}
	if s == "" {
		s = "branch"
	}
	return s
}

func (r *rt) deployPush(ctx context.Context, say func(string, ...any), g hookGrant, rev, branch, preview string) {
	short := rev[:min(7, len(rev))]
	target := "production"
	if preview != "" {
		target = "preview " + preview
	}
	say("tiffin: %s %s → %s", branch, short, target)
	tmp, err := os.MkdirTemp(r.opt.DataDir, "push-")
	if err != nil {
		say("tiffin: %v", err)
		return
	}
	defer os.RemoveAll(tmp)
	arch := exec.CommandContext(ctx, "git", "--git-dir", r.gitDir(g.project), "archive", "--format=tar.gz", rev)
	out, _ := arch.StdoutPipe()
	if err := arch.Start(); err != nil {
		say("tiffin: git archive: %v", err)
		return
	}
	_, xerr := srcpack.Extract(out, filepath.Join(tmp, "tree"), srcpack.DefaultLimits)
	if err := arch.Wait(); err != nil || xerr != nil {
		say("tiffin: could not read the pushed tree: %v %v", err, xerr)
		return
	}
	root := filepath.Join(tmp, "tree")
	cfgPath, err := manifest.FindConfig(root)
	if err != nil {
		say("tiffin: no tiffin.config.ts at the top of the repository; nothing to deploy")
		return
	}
	raw, err := manifest.EvaluateJSON(cfgPath, map[string]string{})
	if err != nil {
		say("tiffin: tiffin.config.ts: %v", err)
		return
	}
	var mf struct {
		Project string                  `json:"project"`
		Apps    map[string]manifest.App `json:"apps"`
	}
	if err := json.Unmarshal(raw, &mf); err != nil {
		say("tiffin: tiffin.config.ts: %v", err)
		return
	}
	if mf.Project != g.project {
		say("tiffin: this repository is for project %s but tiffin.config.ts says %q; nothing deployed", g.project, mf.Project)
		return
	}
	var started []*Deploy
	for name, a := range mf.Apps {
		spec, perr := r.checkDeployable(ctx, g.project, name, preview, false)
		if perr != nil {
			say("tiffin: %s: %s %s", name, perr.Detail, perr.Hint)
			continue
		}
		dir := filepath.Join(root, filepath.FromSlash(orDot(a.Path)))
		if rel, err := filepath.Rel(root, dir); err != nil || strings.HasPrefix(rel, "..") {
			say("tiffin: %s: path %q is outside the repository", name, a.Path)
			continue
		}
		d, err := r.newDeploy(ctx, g.project, name, preview, SourceGit, g.token, spec)
		if err != nil {
			say("tiffin: %s: %v", name, err)
			continue
		}
		d.Commit = rev
		src := filepath.Join(r.workDir(d), "source.tgz")
		f, err := os.Create(src)
		if err == nil {
			var st srcpack.Stats
			st, err = srcpack.Pack(dir, f)
			f.Close()
			d.SourceBytes = st.Bytes
		}
		if err != nil {
			say("tiffin: %s: %v", name, err)
			r.fail(ctx, d, err, "", io.Discard)
			continue
		}
		_ = r.st.putDeploy(ctx, d)
		_ = r.p.DB.Audit(ctx, g.token, "deploy.create", g.project+"/"+name, map[string]any{"deploy": d.ID, "preview": preview, "source": SourceGit, "commit": rev})
		r.start(d, src, SourceGit)
		say("tiffin: %s: deploy %s queued", name, d.ID)
		started = append(started, d)
	}
	// Stream each deploy's build log in turn, then its outcome.
	for _, d := range started {
		var off int64
		for {
			cur, err := r.st.getDeploy(ctx, d.Project, d.App, d.ID)
			if err != nil {
				break
			}
			text, noff := r.readBuildLog(cur, off, 1<<20)
			off = noff
			for _, line := range strings.Split(strings.TrimRight(string(text), "\n"), "\n") {
				if line != "" {
					say("%s | %s", d.App, line)
				}
			}
			if cur.Terminal() && len(text) == 0 {
				if cur.Status == StatusLive {
					say("tiffin: %s is live%s", d.App, urlNote(cur.URL))
				} else {
					say("tiffin: %s %s: %s", d.App, cur.Status, cur.Error)
				}
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(400 * time.Millisecond):
			}
		}
	}
}

func orDot(p string) string {
	if p == "" {
		return "."
	}
	return p
}
