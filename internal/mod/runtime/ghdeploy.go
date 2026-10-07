package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/mod/runtime/ghapp"
)

// Deploying from GitHub: webhooks (push → production, pull request →
// preview, closed → preview removed), a queue that coalesces rapid pushes
// per app environment, clones with short-lived single-repository tokens,
// and the signals left on GitHub (commit statuses, deployments, one pull
// request comment kept up to date).

// maxDeliveryAge rejects deliveries whose event is older than this
// (replays of captured deliveries).
const maxDeliveryAge = time.Hour

// ghJob is one deploy to make from GitHub.
type ghJob struct {
	Project, App, Preview string
	Repo                  string // owner/name
	SHA, Branch           string
	PR                    int
	PRTitle               string
	Message, Author       string
	Installation          int64
	Trigger               string // push, pull_request, redeploy
	By                    string
	Path                  string // folder inside the repository
	d                     *Deploy
}

func (j *ghJob) key() string { return envKey(j.Project, j.App, j.Preview) }

// ghQueue runs one GitHub deploy per app environment at a time and keeps
// only the newest waiting one: five quick pushes build twice, not five times.
type ghQueue struct {
	mu     sync.Mutex
	slots  map[string]*ghSlot
	closed map[string]bool // pull request previews closed while building
}

type ghSlot struct {
	running bool
	next    *ghJob
}

// enqueue records j's deploy (queued) and runs it when its environment is free.
func (r *rt) enqueue(ctx context.Context, j *ghJob) (*Deploy, error) {
	spec, err := r.appSpec(ctx, j.Project, j.App)
	if err != nil {
		return nil, err
	}
	d, err := r.newDeploy(ctx, j.Project, j.App, j.Preview, SourceGit, j.By, spec)
	if err != nil {
		return nil, err
	}
	d.Repo, d.Ref, d.Commit = r.ghEndpoints().RepoURL(j.Repo), j.Branch, j.SHA
	d.Message, d.Author, d.PullRequest, d.Trigger = firstLine(j.Message), j.Author, j.PR, j.Trigger
	if err := r.st.putDeploy(ctx, d); err != nil {
		return nil, err
	}
	j.d = d
	q := &r.gh.q
	q.mu.Lock()
	if q.slots == nil {
		q.slots, q.closed = map[string]*ghSlot{}, map[string]bool{}
	}
	if j.PR > 0 {
		delete(q.closed, j.key())
	}
	s := q.slots[j.key()]
	if s == nil {
		s = &ghSlot{}
		q.slots[j.key()] = s
	}
	var skipped *ghJob
	if s.running {
		skipped, s.next = s.next, j
	} else {
		s.running = true
		go r.runJobs(j)
	}
	q.mu.Unlock()
	if skipped != nil {
		r.skip(ctx, skipped, fmt.Sprintf("a newer commit (%s) arrived before this one was built; deploy %s builds it instead", short(j.SHA), d.ID))
	}
	return d, nil
}

// skip marks a waiting deploy as skipped.
func (r *rt) skip(ctx context.Context, old *ghJob, why string) {
	now := time.Now().UTC()
	d := old.d
	d.Status, d.FinishedAt, d.Error = StatusSkipped, &now, "skipped: "+why
	_ = r.st.putDeploy(ctx, d)
	if f, err := r.openBuildLog(d); err == nil {
		fmt.Fprintf(f, "==> skipped: %s\n", why)
		f.Close()
	}
}

// runJobs runs j, then whatever arrived for its environment meanwhile.
func (r *rt) runJobs(j *ghJob) {
	for j != nil {
		r.runJob(j)
		q := &r.gh.q
		q.mu.Lock()
		s := q.slots[j.key()]
		j, s.next = s.next, nil
		if j == nil {
			s.running = false
		}
		q.mu.Unlock()
	}
}

// failNow fails a deploy that never reached the pipeline.
func (r *rt) failNow(ctx context.Context, d *Deploy, err error) {
	hint := ""
	var se *stateError
	if errors.As(err, &se) {
		hint = se.hint
	}
	f, ferr := r.openBuildLog(d)
	if ferr != nil {
		r.fail(ctx, d, err, hint, io.Discard)
		return
	}
	defer f.Close()
	r.fail(ctx, d, err, hint, f)
}

// ghTracker is what the box must still tell GitHub about a deploy (kept
// so a restart mid-deploy still reports the outcome).
type ghTracker struct {
	Repo         string `json:"repo"`
	SHA          string `json:"sha"`
	Installation int64  `json:"installation"`
	Deployment   int64  `json:"deployment,omitempty"`
	PR           int    `json:"pr,omitempty"`
	Context      string `json:"context"`
}

func statusContext(project, app string) string { return "tiffin/" + project + "/" + app }

func (r *rt) logURL(d *Deploy) string {
	return fmt.Sprintf("%s/projects/%s/apps/%s/deploys/%s", r.publicBase(), d.Project, d.App, d.ID)
}

// runJob builds and releases one GitHub deploy and reports it to GitHub.
func (r *rt) runJob(j *ghJob) {
	ctx := r.ctx
	d := j.d
	c, err := r.github(ctx)
	if err != nil {
		r.failNow(ctx, d, err)
		return
	}
	if j.Installation == 0 {
		if j.Installation, err = r.repoInstallation(ctx, c, j.Repo); err != nil {
			r.failNow(ctx, d, err)
			return
		}
	}
	tr := &ghTracker{Repo: j.Repo, SHA: j.SHA, Installation: j.Installation, PR: j.PR, Context: statusContext(j.Project, j.App)}
	r.reportStart(ctx, c, d, tr, j)
	raw, _ := json.Marshal(tr)
	_ = r.p.DB.KVPut(ctx, nsGitHubJobs, d.Project+"/"+d.App+"/"+d.ID, raw)

	done := make(chan struct{})
	r.startFrom(d, SourceGit, func(ctx context.Context, log io.Writer) (string, error) {
		return r.cloneGitHub(ctx, c, d, j, log)
	})
	go func() {
		defer close(done)
		for {
			cur, err := r.st.getDeploy(ctx, d.Project, d.App, d.ID)
			if err == nil && cur.Terminal() {
				*d = *cur
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(300 * time.Millisecond):
			}
		}
	}()
	<-done
	if ctx.Err() != nil {
		return // the box is stopping: the tracker reports after the restart
	}
	r.reportEnd(ctx, c, d, tr)
	_ = r.p.DB.KVDelete(ctx, nsGitHubJobs, d.Project+"/"+d.App+"/"+d.ID)
	// A pull request closed while its preview was building: remove it now.
	if j.PR > 0 {
		q := &r.gh.q
		q.mu.Lock()
		closed := q.closed[j.key()] && q.slots[j.key()].next == nil
		q.mu.Unlock()
		if closed {
			_ = r.deletePreview(ctx, j.Project, j.App, j.Preview)
		}
	}
}

// cloneGitHub fetches exactly the commit with a token that can only read
// this repository, revoked as soon as the clone is done. The token goes to
// git through its environment: never a file, never the command line.
func (r *rt) cloneGitHub(ctx context.Context, c *ghConn, d *Deploy, j *ghJob, log io.Writer) (string, error) {
	fmt.Fprintf(log, "==> GitHub %s %s (%s)%s\n", j.Repo, short(j.SHA), describeTrigger(j), messageNote(j.Message))
	tok, err := c.App.CloneToken(ctx, j.Installation, j.Repo)
	if err != nil {
		return "", &BuildError{Msg: "GitHub would not give the box access to " + j.Repo + ": " + err.Error(), Hint: "Check the app is still installed on the repository (Settings › Git)."}
	}
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = c.App.RevokeToken(rctx, tok)
	}()
	u, err := url.Parse(c.E.RepoURL(j.Repo) + ".git")
	if err != nil {
		return "", err
	}
	ips, err := r.hostIPs(ctx, u.Hostname())
	if err != nil {
		return "", &BuildError{Msg: err.Error(), Hint: "The box needs DNS and outbound HTTPS to reach GitHub."}
	}
	gs := &gitSource{URL: u, Ref: j.SHA, Path: j.Path, IPs: ips,
		Auth: "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+tok))}
	src, err := r.cloneSource(ctx, d, gs, log)
	if err != nil {
		var be *BuildError
		if errors.As(err, &be) && strings.Contains(be.Hint, "public") {
			be.Hint = "Check the app is installed on " + j.Repo + " and the commit still exists."
		}
		return "", err
	}
	if !strings.EqualFold(d.Commit, j.SHA) {
		return "", &BuildError{Msg: fmt.Sprintf("GitHub served commit %s instead of %s", short(d.Commit), short(j.SHA)), Hint: "Redeploy."}
	}
	return src, nil
}

func describeTrigger(j *ghJob) string {
	switch {
	case j.PR > 0:
		return fmt.Sprintf("pull request #%d", j.PR)
	case j.Trigger == "redeploy":
		return "redeploy of " + j.Branch
	default:
		return "push to " + j.Branch
	}
}

func messageNote(m string) string {
	if m = firstLine(m); m != "" {
		return ": " + ghapp.Clip(m, 72)
	}
	return ""
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return ghapp.Clip(s, 200)
}

func short(sha string) string { return sha[:min(7, len(sha))] }

// ---- reporting to GitHub ----

func (r *rt) ghWarn(d *Deploy, what string, err error) {
	if err == nil {
		return
	}
	if f, ferr := r.openBuildLog(d); ferr == nil {
		fmt.Fprintf(f, "==> note: could not %s on GitHub: %v\n", what, err)
		f.Close()
	}
	r.p.Log.Warn("github report", "deploy", d.ID, "what", what, "err", err)
}

func (r *rt) reportStart(ctx context.Context, c *ghConn, d *Deploy, tr *ghTracker, j *ghJob) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r.ghWarn(d, "set the commit status", c.App.SetStatus(ctx, tr.Installation, tr.Repo, tr.SHA, ghapp.Status{State: "pending", TargetURL: r.logURL(d),
		Description: "Building on " + r.p.Domain, Context: tr.Context}))
	env := "tiffin/" + d.Project + "/" + d.App
	if d.Preview != "" {
		env += "/" + d.Preview
	}
	id, err := c.App.CreateDeployment(ctx, tr.Installation, tr.Repo, ghapp.DeploymentRequest{Ref: tr.SHA, Environment: env,
		Description: describeTrigger(j), TransientEnvironment: d.Preview != "", ProductionEnvironment: d.Preview == ""})
	r.ghWarn(d, "create a deployment", err)
	if err == nil {
		tr.Deployment = id
		r.ghWarn(d, "update the deployment", c.App.SetDeploymentStatus(ctx, tr.Installation, tr.Repo, id, ghapp.DeploymentStatus{State: "in_progress", LogURL: r.logURL(d)}))
	}
	if tr.PR > 0 {
		r.ghWarn(d, "comment on the pull request", r.upsertComment(ctx, c, d, tr, prBuilding))
	}
}

// siteURL is where people see a live deploy: a production app's first
// route on a domain of its own (https://example.com, not
// web.<apps domain>) when it has one, else its URL.
func (r *rt) siteURL(ctx context.Context, d *Deploy) string {
	if d.Preview != "" || d.URL == "" {
		return d.URL
	}
	spec, err := r.appSpec(ctx, d.Project, d.App)
	if err != nil {
		return d.URL
	}
	return ownDomainURL(r.p.URL, r.p.AppsDomain(), appRoutes(d.Project, d.App, spec), d.URL)
}

// ownDomainURL is the URL of the first of routes on a domain of its own
// (not the apps domain nor under it), else fallback.
func ownDomainURL(url func(host string) string, appsDomain string, routes []string, fallback string) string {
	for _, rt := range routes {
		host, rest, _ := strings.Cut(rt, "/")
		host = strings.ToLower(host)
		if !strings.Contains(host, ".") || host == appsDomain || strings.HasSuffix(host, "."+appsDomain) {
			continue
		}
		if rest = strings.Trim(rest, "/"); rest != "" {
			return url(host) + "/" + rest
		}
		return url(host)
	}
	return fallback
}

func (r *rt) reportEnd(ctx context.Context, c *ghConn, d *Deploy, tr *ghTracker) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	site := r.siteURL(ctx, d)
	state, desc, target := "success", fmt.Sprintf("Live in %s", seconds(d.TotalSecs)), site
	dstate := "success"
	switch d.Status {
	case StatusLive:
	case StatusSkipped:
		state, desc, target, dstate = "success", "Skipped: a newer commit was deployed", r.logURL(d), "inactive"
	default:
		state, desc, target, dstate = "failure", "Failed: "+orDefaultStr(d.Error, d.Status), r.logURL(d), "failure"
	}
	if target == "" {
		target = r.logURL(d)
	}
	r.ghWarn(d, "set the commit status", c.App.SetStatus(ctx, tr.Installation, tr.Repo, tr.SHA, ghapp.Status{State: state, TargetURL: target, Description: desc, Context: tr.Context}))
	if tr.Deployment != 0 {
		ds := ghapp.DeploymentStatus{State: dstate, LogURL: r.logURL(d), Description: desc, AutoInactive: dstate == "success"}
		if dstate == "success" {
			ds.EnvironmentURL = site
		}
		r.ghWarn(d, "update the deployment", c.App.SetDeploymentStatus(ctx, tr.Installation, tr.Repo, tr.Deployment, ds))
	}
	if tr.PR > 0 {
		kind := prLive
		if d.Status != StatusLive {
			kind = prFailed
		}
		r.ghWarn(d, "comment on the pull request", r.upsertComment(ctx, c, d, tr, kind))
	}
}

func seconds(s float64) string {
	if s < 1 {
		return "under a second"
	}
	if s < 90 {
		return fmt.Sprintf("%.0f s", s)
	}
	return fmt.Sprintf("%.0f min %.0f s", float64(int(s)/60), float64(int(s)%60))
}

const (
	prBuilding = "building"
	prLive     = "live"
	prFailed   = "failed"
	prRemoved  = "removed"
	prExpired  = "expired"
)

type prComment struct {
	Comment    int64 `json:"comment"`
	Deployment int64 `json:"deployment,omitempty"`
	Install    int64 `json:"installation"`
}

func prKey(project, app string, pr int) string {
	return fmt.Sprintf("comment/%s/%s/%d", project, app, pr)
}

// commentBody is the one comment a preview keeps up to date.
func (r *rt) commentBody(d *Deploy, kind string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<!-- tiffin:%s/%s -->\n", d.Project, d.App)
	logs := "[logs](" + r.logURL(d) + ")"
	commit := "`" + short(d.Commit) + "`"
	switch kind {
	case prBuilding:
		fmt.Fprintf(&b, "**Preview** of `%s` is building commit %s… · %s", d.App, commit, logs)
	case prLive:
		fmt.Fprintf(&b, "**Preview** of `%s`: %s · built in %s · %s · commit %s", d.App, d.URL, seconds(d.BuildSecs), logs, commit)
	case prFailed:
		fmt.Fprintf(&b, "**Preview** of `%s` failed for commit %s: %s · %s", d.App, commit, ghapp.Clip(orDefaultStr(d.Error, d.Status), 300), logs)
		if d.Hint != "" {
			fmt.Fprintf(&b, "\n\n%s", ghapp.Clip(d.Hint, 300))
		}
	case prRemoved:
		fmt.Fprintf(&b, "**Preview** of `%s` was removed: the pull request is closed.", d.App)
	case prExpired:
		fmt.Fprintf(&b, "**Preview** of `%s` was removed after %s without visits or deploys. Push to the pull request to build it again.",
			d.App, expireWords(r.opt.PreviewExpire))
	}
	fmt.Fprintf(&b, "\n\n<sub>Deployed by Tiffin on %s</sub>", r.p.Domain)
	return b.String()
}

func (r *rt) upsertComment(ctx context.Context, c *ghConn, d *Deploy, tr *ghTracker, kind string) error {
	k := prKey(d.Project, d.App, tr.PR) + "@" + tr.Repo
	var pc prComment
	if raw, ok, _ := r.p.DB.KVGet(ctx, nsGitHub, k); ok {
		_ = json.Unmarshal(raw, &pc)
	}
	id, err := c.App.UpsertComment(ctx, tr.Installation, tr.Repo, tr.PR, pc.Comment, r.commentBody(d, kind))
	if err != nil {
		return err
	}
	pc.Comment, pc.Install = id, tr.Installation
	if tr.Deployment != 0 {
		pc.Deployment = tr.Deployment
	}
	raw, _ := json.Marshal(pc)
	return r.p.DB.KVPut(ctx, nsGitHub, k, raw)
}

// resumeReports tells GitHub how deploys a restart interrupted ended.
func (r *rt) resumeReports(ctx context.Context) {
	all, err := r.p.DB.KVList(ctx, nsGitHubJobs)
	if err != nil || len(all) == 0 {
		return
	}
	c, err := r.github(ctx)
	if err != nil {
		return
	}
	for k, raw := range all {
		parts := strings.Split(k, "/")
		var tr ghTracker
		if len(parts) != 3 || json.Unmarshal(raw, &tr) != nil {
			_ = r.p.DB.KVDelete(ctx, nsGitHubJobs, k)
			continue
		}
		d, err := r.st.getDeploy(ctx, parts[0], parts[1], parts[2])
		if err == nil && d.Terminal() {
			r.reportEnd(ctx, c, d, &tr)
		}
		if err != nil || d.Terminal() {
			_ = r.p.DB.KVDelete(ctx, nsGitHubJobs, k)
		}
	}
}

// ---- webhooks ----

// connectedApp is one app wired to a repository.
type connectedApp struct {
	Project, App string
	Git          manifest.Git
	// Watch are the app's watch paths: a push or pull request that
	// changes none of their files does not deploy it.
	Watch []string
}

// appsFor finds the apps connected to a repository.
func (r *rt) appsFor(ctx context.Context, repo string) ([]connectedApp, error) {
	projects, err := r.p.DB.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	var out []connectedApp
	for _, pr := range projects {
		_, res, err := r.p.DB.Load(ctx, pr)
		if err != nil {
			return nil, err
		}
		for addr, rs := range res {
			if change.Kind(addr) != change.KindApp {
				continue
			}
			var a manifest.App
			if json.Unmarshal(rs.Spec, &a) != nil || a.Git == nil || !strings.EqualFold(a.Git.Repo, repo) {
				continue
			}
			out = append(out, connectedApp{Project: pr, App: change.Name(addr), Git: *a.Git, Watch: a.Watch})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Project+"/"+out[i].App < out[j].Project+"/"+out[j].App })
	return out, nil
}

// handleWebhook serves POST /v1/github/webhook.
func (r *rt) handleWebhook(w http.ResponseWriter, req *http.Request) {
	reply := func(status int, msg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": msg})
	}
	if req.Method != http.MethodPost {
		reply(http.StatusMethodNotAllowed, "deliveries are POSTed")
		return
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, ghapp.MaxPayload+1))
	if err != nil || len(body) > ghapp.MaxPayload {
		reply(http.StatusRequestEntityTooLarge, "the delivery is too large")
		return
	}
	ctx := req.Context()
	c, err := r.github(ctx)
	if err != nil {
		reply(http.StatusGone, "this box is not connected to a GitHub App")
		return
	}
	if err := ghapp.Verify(c.Cred.WebhookSecret, body, req.Header.Get("X-Hub-Signature-256")); err != nil {
		r.p.Log.Warn("github webhook refused", "err", err, "from", req.RemoteAddr)
		reply(http.StatusUnauthorized, "signature mismatch")
		return
	}
	id, event := req.Header.Get("X-GitHub-Delivery"), req.Header.Get("X-GitHub-Event")
	if !ghapp.ValidDelivery(id) || event == "" {
		reply(http.StatusBadRequest, "missing X-GitHub-Delivery or X-GitHub-Event")
		return
	}
	if dup, err := r.seen(ctx, id); err != nil {
		reply(http.StatusInternalServerError, err.Error())
		return
	} else if dup {
		reply(http.StatusOK, "already handled this delivery")
		return
	}
	status, msg := r.dispatchEvent(context.WithoutCancel(ctx), c, event, body)
	reply(status, msg)
}

func stale(t time.Time) bool {
	if t.IsZero() {
		return false
	}
	age := time.Since(t)
	return age > maxDeliveryAge || age < -5*time.Minute
}

// dispatchEvent handles a verified delivery and says what it did.
func (r *rt) dispatchEvent(ctx context.Context, c *ghConn, event string, body []byte) (int, string) {
	switch event {
	case "ping":
		r.logEvent(ctx, GitHubEvent{Event: "ping", Summary: "GitHub can reach the box", OK: true})
		return http.StatusOK, "pong"
	case "installation", "installation_repositories":
		ev, err := ghapp.Parse[ghapp.InstallationEvent](body)
		if err != nil {
			return http.StatusBadRequest, "bad payload"
		}
		r.gh.mu.Lock()
		r.gh.repos = map[int64]cachedRepos{}
		r.gh.mu.Unlock()
		c.App.Forget(ev.Installation.ID)
		if ev.Action == "deleted" && c.Public {
			ids := r.bound(ctx)
			ids = slicesDelete(ids, ev.Installation.ID)
			_ = r.setBound(ctx, ids)
		}
		if c.Public && !r.usable(ctx, c, ev.Installation.ID) {
			return http.StatusAccepted, "not an installation of this box"
		}
		r.logEvent(ctx, GitHubEvent{Event: event, Summary: installSummary(event, ev), OK: true})
		return http.StatusAccepted, "noted"
	case "push":
		return r.onPush(ctx, c, body)
	case "pull_request":
		return r.onPullRequest(ctx, c, body)
	}
	return http.StatusAccepted, "ignored event " + event
}

func slicesDelete(ids []int64, id int64) []int64 {
	out := ids[:0]
	for _, x := range ids {
		if x != id {
			out = append(out, x)
		}
	}
	return out
}

func installSummary(event string, ev *ghapp.InstallationEvent) string {
	who := ev.Installation.Account.Login
	switch {
	case event == "installation" && ev.Action == "created":
		return "Installed on " + who
	case event == "installation" && ev.Action == "deleted":
		return "Uninstalled from " + who
	case event == "installation" && ev.Action == "suspend":
		return "Suspended on " + who
	case event == "installation_repositories":
		return "Repositories changed for " + who
	}
	return "Installation " + ev.Action + " on " + who
}

func (r *rt) onPush(ctx context.Context, c *ghConn, body []byte) (int, string) {
	ev, err := ghapp.Parse[ghapp.PushEvent](body)
	if err != nil {
		return http.StatusBadRequest, "bad payload"
	}
	repo := ev.Repository.FullName
	if !ghapp.ValidRepo(repo) {
		return http.StatusBadRequest, "bad repository"
	}
	if stale(ev.Repository.PushedAt.Time) {
		return http.StatusUnprocessableEntity, "stale delivery: the push is more than an hour old"
	}
	branch := ev.Branch()
	if branch == "" || ev.Deleted || !ghapp.ValidSHA(ev.After) {
		return http.StatusAccepted, "nothing to deploy (tag, deletion or no commit)"
	}
	if !r.usable(ctx, c, ev.Installation.ID) {
		return http.StatusAccepted, "not an installation of this box"
	}
	apps, err := r.appsFor(ctx, repo)
	if err != nil {
		return http.StatusInternalServerError, err.Error()
	}
	msg, author := "", ev.Sender.Login
	if hc := ev.HeadCommit; hc != nil {
		msg = hc.Message
		author = orDefaultStr(hc.Author.Username, orDefaultStr(hc.Author.Name, author))
	}
	if strings.Contains(msg, "[skip deploy]") || strings.Contains(msg, "[skip ci]") {
		r.logEvent(ctx, GitHubEvent{Event: "push", Repo: repo, Summary: fmt.Sprintf("Push to %s (%s) asked to skip deploys", branch, short(ev.After)), OK: true})
		return http.StatusAccepted, "skipped: the commit message asks to skip deploys"
	}
	var started, names, unchanged []string
	changed := r.changedFiles(ctx, c, ev.Installation.ID, repo, ev.Before, ev.After)
	for _, a := range apps {
		if a.Git.Branch != branch {
			continue
		}
		if !changed.touches(a.Watch) {
			unchanged = append(unchanged, a.Project+"/"+a.App)
			continue
		}
		d, err := r.enqueue(ctx, &ghJob{Project: a.Project, App: a.App, Repo: repo, SHA: ev.After, Branch: branch, Message: msg, Author: author,
			Installation: ev.Installation.ID, Trigger: "push", By: "github:" + ev.Sender.Login, Path: a.Git.Path})
		if err != nil {
			r.logEvent(ctx, GitHubEvent{Event: "push", Repo: repo, Summary: fmt.Sprintf("%s/%s: %v", a.Project, a.App, err)})
			continue
		}
		started = append(started, a.Project+"/"+a.App+" "+d.ID)
		names = append(names, a.Project+"/"+a.App)
	}
	if len(unchanged) > 0 {
		r.logEvent(ctx, GitHubEvent{Event: "push", Repo: repo, Summary: fmt.Sprintf("Push to %s (%s): nothing under the watch paths of %s changed, so it was not deployed", branch, short(ev.After), andList(unchanged)), OK: true})
	}
	if len(started) == 0 {
		if len(unchanged) > 0 {
			return http.StatusAccepted, "skipped: nothing under the watch paths of " + strings.Join(unchanged, ", ") + " changed"
		}
		r.logEvent(ctx, GitHubEvent{Event: "push", Repo: repo, Summary: fmt.Sprintf("Push to %s (%s): no app deploys from this branch", branch, short(ev.After)), OK: true})
		return http.StatusAccepted, "no app deploys from " + branch
	}
	r.logEvent(ctx, GitHubEvent{Event: "push", Repo: repo, Summary: fmt.Sprintf("Push to %s (%s) by %s: deploying %s", branch, short(ev.After), author, andList(names)), OK: true})
	return http.StatusAccepted, "deploying " + strings.Join(started, ", ")
}

func prPreview(n int) string { return fmt.Sprintf("pr-%d", n) }

func (r *rt) onPullRequest(ctx context.Context, c *ghConn, body []byte) (int, string) {
	ev, err := ghapp.Parse[ghapp.PullRequestEvent](body)
	if err != nil {
		return http.StatusBadRequest, "bad payload"
	}
	repo := ev.PullRequest.Base.Repo.FullName
	n := ev.PullRequest.Number
	if n == 0 {
		n = ev.Number
	}
	if !ghapp.ValidRepo(repo) || n <= 0 {
		return http.StatusBadRequest, "bad repository or pull request"
	}
	if stale(ev.PullRequest.UpdatedAt.Time) {
		return http.StatusUnprocessableEntity, "stale delivery: the pull request event is more than an hour old"
	}
	if !r.usable(ctx, c, ev.Installation.ID) {
		return http.StatusAccepted, "not an installation of this box"
	}
	apps, err := r.appsFor(ctx, repo)
	if err != nil {
		return http.StatusInternalServerError, err.Error()
	}
	preview := prPreview(n)
	var did []string                                        // the reply to GitHub (with deploy ids)
	var built, skipped, removed, failed, unwatched []string // for people
	who := ev.PullRequest.User.Login
	switch ev.Action {
	case "opened", "reopened", "synchronize", "ready_for_review":
		sha := ev.PullRequest.Head.SHA
		if !ghapp.ValidSHA(sha) {
			return http.StatusBadRequest, "bad head commit"
		}
		changed := r.changedFiles(ctx, c, ev.Installation.ID, repo, ev.PullRequest.Base.SHA, sha)
		for _, a := range apps {
			switch {
			case a.Git.Previews == manifest.PreviewsOff:
				continue
			case !changed.touches(a.Watch):
				did = append(did, a.Project+"/"+a.App+": not built (nothing under its watch paths changed)")
				unwatched = append(unwatched, a.Project+"/"+a.App)
				continue
			case ev.FromFork() && a.Git.Previews != manifest.PreviewsForks:
				did = append(did, a.Project+"/"+a.App+": not built (pull requests from forks are off)")
				skipped = append(skipped, a.Project+"/"+a.App)
				continue
			}
			if _, perr := r.checkDeployable(ctx, a.Project, a.App, preview, false); perr != nil {
				did = append(did, a.Project+"/"+a.App+": "+perr.Detail)
				failed = append(failed, a.Project+"/"+a.App+" ("+perr.Detail+")")
				continue
			}
			d, err := r.enqueue(ctx, &ghJob{Project: a.Project, App: a.App, Preview: preview, Repo: repo, SHA: sha, Branch: ev.PullRequest.Head.Ref,
				PR: n, PRTitle: ev.PullRequest.Title, Message: ev.PullRequest.Title, Author: ev.PullRequest.User.Login,
				Installation: ev.Installation.ID, Trigger: "pull_request", By: "github:" + ev.Sender.Login, Path: a.Git.Path})
			if err != nil {
				did = append(did, a.Project+"/"+a.App+": "+err.Error())
				failed = append(failed, a.Project+"/"+a.App+" ("+err.Error()+")")
				continue
			}
			did = append(did, a.Project+"/"+a.App+" preview "+d.ID)
			built = append(built, a.Project+"/"+a.App)
		}
	case "closed":
		for _, a := range apps {
			key := envKey(a.Project, a.App, preview)
			q := &r.gh.q
			q.mu.Lock()
			if q.closed == nil {
				q.slots, q.closed = map[string]*ghSlot{}, map[string]bool{}
			}
			q.closed[key] = true
			var dropped *ghJob
			if s := q.slots[key]; s != nil && s.next != nil {
				dropped, s.next = s.next, nil
			}
			q.mu.Unlock()
			if dropped != nil {
				r.skip(ctx, dropped, "the pull request was closed before it was built")
			}
			// Not found: nothing went live (one still building is removed when it finishes).
			err := r.deletePreview(ctx, a.Project, a.App, preview)
			q.mu.Lock()
			building := q.slots[key] != nil && q.slots[key].running
			q.mu.Unlock()
			switch {
			case err == nil || building:
				did = append(did, a.Project+"/"+a.App+" preview "+preview+" removed")
				removed = append(removed, a.Project+"/"+a.App)
			case !errors.Is(err, errNotFound):
				did = append(did, a.Project+"/"+a.App+": "+err.Error())
				failed = append(failed, a.Project+"/"+a.App+" ("+err.Error()+")")
				continue
			}
			r.closeReport(ctx, c, a.Project, a.App, repo, n, prRemoved)
		}
	default:
		return http.StatusAccepted, "nothing to do for " + ev.Action
	}
	summary := fmt.Sprintf("Pull request #%d %s", n, ev.Action)
	if who != "" && ev.Action == "opened" {
		summary += " by " + who
	}
	var parts []string
	if len(built) > 0 {
		parts = append(parts, "building a preview of "+andList(built))
	}
	if len(removed) > 0 {
		parts = append(parts, "removed the preview of "+andList(removed))
	}
	if len(skipped) > 0 {
		parts = append(parts, "no preview of "+andList(skipped)+": it comes from a fork, and previews of forks are off")
	}
	if len(unwatched) > 0 {
		parts = append(parts, "no preview of "+andList(unwatched)+": nothing under its watch paths changed")
	}
	if len(failed) > 0 {
		parts = append(parts, "could not preview "+andList(failed))
	}
	if len(parts) == 0 {
		parts = append(parts, "nothing to do")
	}
	summary += ": " + strings.Join(parts, "; ")
	r.logEvent(ctx, GitHubEvent{Event: "pull_request", Repo: repo, Summary: summary, OK: len(failed) == 0})
	if len(did) == 0 {
		return http.StatusAccepted, "no app previews " + repo
	}
	return http.StatusAccepted, strings.Join(did, "; ")
}

// closeReport updates the comment and retires the deployment of a closed
// pull request's preview.
func (r *rt) closeReport(ctx context.Context, c *ghConn, project, app, repo string, n int, kind string) {
	k := prKey(project, app, n) + "@" + repo
	raw, ok, _ := r.p.DB.KVGet(ctx, nsGitHub, k)
	if !ok {
		return
	}
	var pc prComment
	if json.Unmarshal(raw, &pc) != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	d := &Deploy{Project: project, App: app}
	if pc.Comment != 0 {
		_, _ = c.App.UpsertComment(ctx, pc.Install, repo, n, pc.Comment, r.commentBody(d, kind))
	}
	if pc.Deployment != 0 {
		_ = c.App.SetDeploymentStatus(ctx, pc.Install, repo, pc.Deployment, ghapp.DeploymentStatus{State: "inactive", Description: "Preview removed"})
	}
	_ = r.p.DB.KVDelete(ctx, nsGitHub, k)
}

// expireReport tells the pull request its preview was deleted for lack of use.
func (r *rt) expireReport(ctx context.Context, project, app string, n int) {
	c, err := r.github(ctx)
	if err != nil {
		return
	}
	kv, err := r.p.DB.KVList(ctx, nsGitHub)
	if err != nil {
		return
	}
	for k := range kv {
		if repo, ok := strings.CutPrefix(k, prKey(project, app, n)+"@"); ok {
			r.closeReport(ctx, c, project, app, repo, n, prExpired)
		}
	}
}

// expireWords is a duration in days ("7 days"), or as Go writes it when shorter.
func expireWords(d time.Duration) string {
	if days := int(d.Hours() / 24); days >= 1 && d%(24*time.Hour) == 0 {
		if days == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", days)
	}
	return d.String()
}

// andList joins names for a sentence: "a", "a and b", "a, b and c".
func andList(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

// changedFiles is what changed between base and head, fetched from GitHub
// only if an app has watch paths. A base of zeros (a new branch) or a
// failed comparison counts as unknown: every app deploys.
func (r *rt) changedFiles(ctx context.Context, c *ghConn, installation int64, repo, base, head string) *changeSet {
	return &changeSet{load: func() ([]string, bool) {
		if !ghapp.ValidSHA(base) || strings.Trim(base, "0") == "" || base == head {
			return nil, false
		}
		files, complete, err := c.App.ChangedFiles(ctx, installation, repo, base, head)
		if err != nil {
			r.p.Log.Warn("github: could not list the changed files; deploying apps with watch paths anyway", "repo", repo, "err", err)
			return nil, false
		}
		return files, complete
	}}
}
