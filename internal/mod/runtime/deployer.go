package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/ids"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/mod/budget"
	"github.com/btahir/tiffin/internal/mod/email"
	"github.com/btahir/tiffin/internal/mod/runtime/srcpack"
)

// newDeploy records a queued deploy. Its source must already be on disk.
func (r *rt) newDeploy(ctx context.Context, project, app, preview, source, by string, spec *manifest.App) (*Deploy, error) {
	d := &Deploy{ID: ids.New("dep"), Project: project, App: app, Preview: preview, Status: StatusQueued, Source: source,
		Framework: string(spec.Framework), CreatedAt: time.Now().UTC(), CreatedBy: by}
	d.URL = r.deployURL(d, spec)
	if err := os.MkdirAll(r.workDir(d), 0o755); err != nil {
		return nil, err
	}
	return d, r.st.putDeploy(ctx, d)
}

func (r *rt) workDir(d *Deploy) string {
	return filepath.Join(r.opt.DataDir, "deploys", d.Project, d.App, d.ID)
}

func (r *rt) buildLogPath(d *Deploy) string { return filepath.Join(r.workDir(d), "build.log") }

// deployURL is where a deploy is (or will be) served.
func (r *rt) deployURL(d *Deploy, spec *manifest.App) string {
	if spec.Role == manifest.RoleWorker {
		return ""
	}
	if d.Preview != "" {
		return r.p.URL(previewHost(d.Preview, d.Project, d.App, spec, r.p.AppsDomain()))
	}
	host, prefix := r.splitRoute(appRoutes(d.Project, d.App, spec)[0])
	return r.p.URL(host) + prefix
}

// appRoutes is a web app's routes. Normalize gives every web app routes, so
// the fallback only covers a spec stored without any: <project>-<app>, the
// default of an app that is not its project's main app.
func appRoutes(project, app string, spec *manifest.App) []string {
	if len(spec.Routes) > 0 {
		return spec.Routes
	}
	return []string{manifest.DefaultName(project, app, "")}
}

// splitRoute turns "shop" → shop.<domain>, "shop/api" → (shop.<domain>, /api),
// "example.com/api" → (example.com, /api).
func (r *rt) splitRoute(route string) (host, prefix string) {
	host, rest, hasPath := strings.Cut(route, "/")
	if !strings.Contains(host, ".") {
		host = r.p.Host(host)
	}
	if hasPath && strings.Trim(rest, "/") != "" {
		prefix = "/" + strings.Trim(rest, "/")
	}
	return strings.ToLower(host), prefix
}

// previewHost is where a preview of an app is served:
// <preview>--<name>.<apps domain>, where name is the app's own name under the
// apps domain (its first route that is a plain name, such as shop or
// shop-docs) or, for an app served only on its own domains or paths,
// <project>-<app>. Production names are unique on the box, so preview names
// are too. A name longer than a DNS label (63) is cut and ends in a short
// hash of the whole, which keeps it unique.
func previewHost(preview, project, app string, spec *manifest.App, domain string) string {
	name := project + "-" + app
	for _, r := range spec.Routes {
		if !strings.ContainsAny(r, "./") {
			name = r
			break
		}
	}
	label := preview + "--" + name
	if len(label) > 63 {
		sum := sha256.Sum256([]byte(label))
		label = strings.TrimRight(label[:63-7], "-") + "-" + hex.EncodeToString(sum[:])[:6]
	}
	return label + "." + domain
}

// start runs a queued deploy's pipeline in the background.
func (r *rt) start(d *Deploy, src string, kind string) {
	r.startFrom(d, kind, func(context.Context, io.Writer) (string, error) { return src, nil })
}

// startFrom runs a queued deploy in the background: fetch gets its source
// (a git clone, say) and returns the source archive, then the pipeline runs.
// Both write to the deploy's build log; a failure in either fails the deploy.
func (r *rt) startFrom(d *Deploy, kind string, fetch func(ctx context.Context, log io.Writer) (string, error)) {
	go func() {
		ctx := r.ctx
		log, err := os.OpenFile(r.buildLogPath(d), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			r.fail(ctx, d, err, "", io.Discard)
			return
		}
		defer log.Close()
		fmt.Fprintf(log, "==> deploy %s of %s/%s%s, queued %s\n", d.ID, d.Project, d.App, previewSuffix(d.Preview), d.CreatedAt.Format(time.RFC3339))
		src, err := fetch(ctx, log)
		if err == nil {
			err = r.pipeline(ctx, d, src, kind, log)
		}
		if err != nil {
			hint := ""
			var be *BuildError
			if errors.As(err, &be) {
				hint = be.Hint
			}
			var he *healthError
			if errors.As(err, &he) {
				hint = he.hint
			}
			var se *startError
			if errors.As(err, &se) {
				hint = startHint
			}
			r.fail(ctx, d, err, hint, log)
		}
	}()
}

func (r *rt) pipeline(ctx context.Context, d *Deploy, src, kind string, log io.Writer) error {
	// One build at a time keeps a small box responsive. A deploy goes
	// before the build warm-up: it stops a running warm-up and takes the slot.
	if err := r.acquireBuild(ctx, d.Project+"/"+d.App+previewSuffix(d.Preview), log); err != nil {
		return err
	}
	released := false
	release := func() {
		if !released {
			released = true
			r.releaseBuild()
		}
	}
	defer release()
	spec, err := r.appSpec(ctx, d.Project, d.App)
	if err != nil {
		return fmt.Errorf("app %s is no longer in project %s", d.App, d.Project)
	}
	d.Status = StatusBuilding
	_ = r.st.putDeploy(ctx, d)
	began := time.Now()
	req := BuildRequest{Deploy: d, Spec: *spec, WorkDir: r.workDir(d), Log: log}
	req.Env, _ = r.plainEnv(ctx, d.Project, d.App)
	switch kind {
	case SourcePrebuilt:
		req.Prebuilt = src
	default:
		req.SrcDir = filepath.Join(r.workDir(d), "src")
		f, err := os.Open(src)
		if err != nil {
			return err
		}
		st, err := srcpack.Extract(f, req.SrcDir, srcpack.DefaultLimits)
		f.Close()
		if err != nil {
			return &BuildError{Msg: "could not unpack the source: " + err.Error(), Hint: "Upload a gzipped tar of the app directory (tiffin deploy does this)."}
		}
		fmt.Fprintf(log, "==> source: %d files, %s\n", st.Files, humanBytes(st.Bytes))
		dropConfig(req.SrcDir, log)
	}
	bctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	res, err := r.bld.Build(bctx, req)
	cancel()
	// Sources are not needed once built (static sites moved their output away).
	_ = os.RemoveAll(filepath.Join(r.workDir(d), "src"))
	_ = os.Remove(src)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	d.BuiltAt = &now
	d.BuildSecs = round1(time.Since(began).Seconds())
	d.Image, d.Digest, d.StaticRoot = res.Image, res.Digest, res.StaticRoot
	if res.SPA {
		d.Framework = string(manifest.FrameworkStatic) + "+spa"
	}
	fmt.Fprintf(log, "==> built in %.1fs%s\n", d.BuildSecs, digestNote(d.Digest))
	release()

	d.Status = StatusStarting
	_ = r.st.putDeploy(ctx, d)
	if err := r.promote(ctx, d, spec, modeDeploy, log); err != nil {
		return err
	}
	fmt.Fprintf(log, "==> live in %.1fs total%s\n", d.TotalSecs, urlNote(d.URL))
	r.gc(ctx, d.Project, d.App, d.Preview)
	return nil
}

func (r *rt) fail(ctx context.Context, d *Deploy, err error, hint string, log io.Writer) {
	now := time.Now().UTC()
	d.Status, d.Error, d.Hint = StatusFailed, err.Error(), hint
	d.FinishedAt = &now
	d.TotalSecs = round1(now.Sub(d.CreatedAt).Seconds())
	_ = r.st.putDeploy(ctx, d)
	fmt.Fprintf(log, "==> FAILED: %s\n", err)
	if hint != "" {
		fmt.Fprintf(log, "    hint: %s\n", hint)
	}
	r.p.Log.Info("deploy failed", "deploy", d.ID, "project", d.Project, "app", d.App, "err", err)
}

const (
	modeDeploy   = "deploy"
	modeRollback = "rollback"
	modeRestart  = "restart"
	modeWake     = "wake"
)

// promote makes d serve its app environment with zero downtime: start new
// instances, wait until healthy, switch the edge to them, let the old ones
// drain, then stop them. Any failure before the switch leaves the old
// instances serving.
func (r *rt) promote(ctx context.Context, d *Deploy, spec *manifest.App, mode string, log io.Writer) error {
	unlock := r.lock(envKey(d.Project, d.App, d.Preview))
	defer unlock()
	return r.promoteLocked(ctx, d, spec, mode, log)
}

func (r *rt) promoteLocked(ctx context.Context, d *Deploy, spec *manifest.App, mode string, log io.Writer) error {
	if r.stopped(ctx, d.Project) {
		return &stateError{"project " + d.Project + " is stopped, so its apps do not start (was it moved to another box?)",
			"Start it again with `tiffin projects start " + d.Project + "`, then deploy."}
	}
	st, err := r.st.getState(ctx, d.Project, d.App, d.Preview)
	if err != nil {
		return err
	}
	prev := *st
	env, hash, err := r.instanceEnv(ctx, d.Project, d.App, d.Preview, spec)
	if err != nil {
		return err
	}
	var started []Instance
	if d.StaticRoot == "" {
		n := max(1, spec.Instances)
		if d.Preview != "" {
			n = 1 // previews run one instance
		}
		if mode != modeWake {
			if spec.MemoryMB > 0 {
				fmt.Fprintf(log, "==> starting %d instance(s) (at most %d MiB each, within project %s's share of the box)\n", n, spec.MemoryMB, d.Project)
			} else {
				fmt.Fprintf(log, "==> starting %d instance(s) (sharing project %s's memory)\n", n, d.Project)
			}
		}
		started, err = r.startInstances(ctx, st, d, spec, n, env)
		if err != nil {
			// Keep the serials it used, so the next start takes new names
			// (a first deploy has no state to keep them in; claimName copes).
			sctx, cancel := cleanupContext(ctx)
			if fresh, gerr := r.st.getState(sctx, d.Project, d.App, d.Preview); gerr == nil && !fresh.UpdatedAt.IsZero() && fresh.Serial < st.Serial {
				fresh.Serial = st.Serial
				_ = r.st.putState(sctx, fresh)
			}
			cancel()
			return err
		}
	}
	if d.StaticRoot != "" {
		// The edge serves a stable path; point it at this deploy's files.
		if err := swapSymlink(r.staticLink(d.Project, d.App, d.Preview), d.StaticRoot); err != nil {
			return err
		}
	}
	// The switch: from here on new requests go to the new instances.
	st.Live, st.Instances, st.Hash, st.Stopped, st.Sleeping = d.ID, started, hash, false, false
	if err := r.st.putState(ctx, st); err != nil {
		r.removeInstances(ctx, started)
		return err
	}
	// The edge only reloads when hosts, paths or file roots change (a first
	// deploy, a new route); instance switches stay inside the switchboard.
	if err := r.refreshIfNeeded(ctx); err != nil {
		_ = r.st.putState(ctx, &prev)
		_ = r.p.RefreshRoutes(ctx)
		r.removeInstances(ctx, started)
		return fmt.Errorf("switch edge routes: %w", err)
	}
	now := time.Now().UTC()
	if prev.Live != "" && prev.Live != d.ID {
		if old, err := r.st.getDeploy(ctx, d.Project, d.App, prev.Live); err == nil && (old.Status == StatusLive || old.Status == StatusStopped) {
			old.Status = StatusSuperseded
			if mode == modeRollback {
				old.Status = StatusRolledBack
			}
			_ = r.st.putDeploy(ctx, old)
		}
	}
	d.Status, d.Error, d.Hint = StatusLive, "", ""
	// Went live: when this version took over (a deploy or a rollback to it).
	// Restarts, rescales and wakes keep the time, so every page quotes one.
	if d.LiveAt == nil || mode == modeDeploy || mode == modeRollback {
		d.LiveAt = &now
	}
	if d.FinishedAt == nil || mode == modeDeploy {
		d.FinishedAt = &now
		d.TotalSecs = round1(now.Sub(d.CreatedAt).Seconds())
	}
	_ = r.st.putDeploy(ctx, d)
	if len(prev.Instances) > 0 {
		old := prev.Instances
		// Workflow runs pinned to the old release keep its instances running
		// (no public traffic) until the queue says they are done.
		if prev.Live != "" && prev.Live != d.ID && d.Preview == "" && r.pinned(ctx, d.Project, d.App, prev.Live) {
			st.Draining = append(st.Draining, DrainSet{Release: prev.Live, Instances: old, Since: now})
			if err := r.st.putState(ctx, st); err == nil {
				fmt.Fprintf(log, "==> switched traffic; release %s has workflow runs pinned to it, so its %d instance(s) keep running without traffic until they finish\n", prev.Live, len(old))
				return nil
			}
		}
		if mode != modeWake && log != io.Discard {
			fmt.Fprintf(log, "==> switched traffic; draining %d old instance(s)\n", len(prev.Instances))
		}
		// Old instances stop once their in-flight requests are done.
		go r.drainThenRemove(old)
	}
	return nil
}

// startInstances starts n containers for d and waits for all to be healthy.
// On failure every container it started is removed again, and the serials
// it used stay used: a container that could not be removed never blocks
// the next start's name.
func (r *rt) startInstances(ctx context.Context, st *AppState, d *Deploy, spec *manifest.App, n int, env map[string]string) ([]Instance, error) {
	var out []Instance
	for i := 0; i < n; i++ {
		in, err := r.runInstance(ctx, st, d, spec, i, env)
		if err != nil {
			r.removeInstances(ctx, out)
			return nil, err
		}
		out = append(out, in)
	}
	// Health checks run in parallel.
	var wg sync.WaitGroup
	errs := make([]error, len(out))
	for i, in := range out {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = r.waitHealthy(ctx, in, spec, r.logFile(d.Project, d.App, d.Preview, d.ID, serialOf(in.Name)))
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		r.removeInstances(ctx, out)
		return nil, err
	}
	return out, nil
}

// nameTries bounds how many serials one instance start goes through when
// names are taken by containers that cannot be removed.
const nameTries = 10

// runInstance starts instance i of d under the next free name. A container
// that holds the name but serves nothing (a crashed start whose removal
// failed) is removed first, or skipped when it will not go.
func (r *rt) runInstance(ctx context.Context, st *AppState, d *Deploy, app *manifest.App, i int, env map[string]string) (Instance, error) {
	var err error
	for try := 0; try < nameTries; try++ {
		st.Serial++
		name := containerName(d.Project, d.App, d.Preview, st.Serial)
		if !r.claimName(ctx, name) {
			err = fmt.Errorf("container name %s is taken by a container that could not be removed", name)
			continue
		}
		var port int
		port, err = r.allocPort(name)
		if err != nil {
			return Instance{}, err
		}
		ienv := make(map[string]string, len(env)+3)
		for k, v := range env {
			ienv[k] = v
		}
		ienv["PORT"] = strconv.Itoa(port)
		ienv["TIFFIN_DEPLOY"] = d.ID
		ienv["TIFFIN_INSTANCE"] = strconv.Itoa(i)
		logPath := r.logFile(d.Project, d.App, d.Preview, d.ID, st.Serial)
		_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
		spec := RunSpec{Name: name, Image: d.Image, Port: port, MemoryMB: app.MemoryMB, Env: ienv, LogPath: logPath,
			CgroupParent: budget.Slice(d.Project),
			Labels:       map[string]string{"tiffin.project": d.Project, "tiffin.app": d.App, "tiffin.preview": d.Preview, "tiffin.deploy": d.ID, "tiffin.port": strconv.Itoa(port)}}
		if err = r.eng.Run(ctx, spec); err == nil {
			return Instance{Name: name, Port: port, Deploy: d.ID}, nil
		}
		// A run that failed may still have created the container.
		r.removeInstances(ctx, []Instance{{Name: name, Port: port}})
		if !nameTaken(err) {
			return Instance{}, &startError{msg: err.Error()}
		}
	}
	return Instance{}, &startError{msg: err.Error()}
}

// claimName makes sure no container holds name, removing a leftover that no
// app environment owns. It reports false when the name stays taken.
func (r *rt) claimName(ctx context.Context, name string) bool {
	r.mu.Lock()
	owned := false
	for _, n := range r.ports {
		owned = owned || n == name
	}
	r.mu.Unlock()
	if owned {
		return false
	}
	c, err := r.eng.Inspect(ctx, name)
	if err == nil && c == nil {
		return true
	}
	r.p.Log.Warn("runtime: removing a leftover container that holds a new instance's name", "container", name)
	rctx, cancel := cleanupContext(ctx)
	defer cancel()
	if err := r.eng.Remove(rctx, name, r.opt.StopGrace); err != nil {
		r.p.Log.Error("remove leftover container", "name", name, "err", err)
		return false
	}
	c, err = r.eng.Inspect(rctx, name)
	return err == nil && c == nil
}

// nameTaken reports whether a container start failed on its name: nerdctl
// keeps names in a store of its own, which can outlive the container.
func nameTaken(err error) bool {
	s := err.Error()
	return strings.Contains(s, "already used") || strings.Contains(s, "already in use") || strings.Contains(s, "already exists")
}

// startError is a container the engine would not start.
type startError struct{ msg string }

func (e *startError) Error() string { return e.msg }

const startHint = "The running version keeps serving. Try again in a minute; tiffin status shows the runtime's containers."

// cleanupContext outlives ctx: removing what a failed or abandoned start left
// must happen even when the request that started it is gone.
func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
}

func (r *rt) removeInstances(ctx context.Context, ins []Instance) {
	ctx, cancel := cleanupContext(ctx)
	defer cancel()
	var wg sync.WaitGroup
	for _, in := range ins {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.eng.Remove(ctx, in.Name, r.opt.StopGrace); err != nil {
				r.p.Log.Error("remove container", "name", in.Name, "err", err)
			}
			r.freePort(in.Port)
			r.st.cache.forget([]Instance{in})
		}()
	}
	wg.Wait()
}

func containerName(project, app, preview string, serial int) string {
	env := "prod"
	if preview != "" {
		env = "pr-" + preview
	}
	return fmt.Sprintf("tf.%s.%s.%s.%d", project, app, env, serial)
}

func serialOf(name string) int {
	i := strings.LastIndex(name, ".")
	n, _ := strconv.Atoi(name[i+1:])
	return n
}

// healthError explains why new instances never became healthy.
type healthError struct {
	msg, hint string
}

func (e *healthError) Error() string { return e.msg }

// waitHealthy waits until a web instance passes its health check (healthOK),
// or a worker stays up. A container that exits fails fast, with its last log
// lines in the error.
func (r *rt) waitHealthy(ctx context.Context, in Instance, spec *manifest.App, logPath string) error {
	deadline := time.Now().Add(r.opt.HealthTimeout)
	path := spec.Healthcheck
	if path == "" || !strings.HasPrefix(path, "/") {
		path = "/" + strings.TrimPrefix(path, "/")
	}
	url := fmt.Sprintf("http://127.0.0.1:%d%s", in.Port, path)
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	worker := spec.Role == manifest.RoleWorker
	upSince := time.Now()
	lastInspect := time.Time{}
	lastStatus := ""
	var exited *Container // seen exited, even if its restart policy brought it back
	for {
		if time.Since(lastInspect) > time.Second {
			lastInspect = time.Now()
			c, err := r.eng.Inspect(ctx, in.Name)
			if err == nil && (c == nil || (!c.Running && c.Status != "restarting" && c.Status != "created")) {
				return exitedError(in.Name, c, logPath)
			}
			if err == nil && c.Status == "restarting" {
				exited = c
			}
		}
		if worker {
			// Workers need not serve HTTP; one that stays up for 3s (or answers) is healthy.
			if conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", in.Port), time.Second); err == nil {
				conn.Close()
				return nil
			}
			if time.Since(upSince) > 3*time.Second {
				if c, err := r.eng.Inspect(ctx, in.Name); err == nil && c != nil && c.Running {
					return nil
				}
			}
		} else {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			req.Header.Set("User-Agent", "tiffin-healthcheck")
			res, err := client.Do(req)
			if err == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
				res.Body.Close()
				if healthOK(path, res.StatusCode) {
					return nil
				}
				lastStatus = fmt.Sprintf("HTTP %d", res.StatusCode)
			} else {
				lastStatus = "not answering"
			}
		}
		if time.Now().After(deadline) {
			if exited != nil {
				return exitedError(in.Name, exited, logPath)
			}
			return &healthError{msg: fmt.Sprintf("instance %s did not pass its health check (GET %s: %s) within %s. Last log lines:\n%s",
				in.Name, path, lastStatus, r.opt.HealthTimeout, tailLog(logPath, 15)),
				hint: "Make sure the app listens on the port in $PORT and answers " + path + healthWant(path) + " (set healthcheck in tiffin.config.ts)."}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// exitedError explains an instance whose process exited on start (a syntax
// error, a missing module or env var): its exit code and last log lines.
func exitedError(name string, c *Container, logPath string) *healthError {
	how := "exited"
	if c != nil && c.ExitCode != 0 {
		how = fmt.Sprintf("exited with code %d", c.ExitCode)
	}
	return &healthError{msg: fmt.Sprintf("instance %s %s before it was healthy. Last log lines:\n%s", name, how, tailLog(logPath, 15)),
		hint: "The app " + how + " on start: its last log lines say why (a syntax error, a missing module, env var or secret). Fix that and deploy again."}
}

// healthOK reports whether a health check's status counts as healthy. The
// default path "/" passes on any status below 500: an app with no page at /
// (a 404) still started. A path the app names is its health endpoint, so it
// must answer 2xx or 3xx; a 404 there means the check is wrong or missing.
func healthOK(path string, status int) bool {
	if path == manifest.DefaultHealthcheck {
		return status < 500
	}
	return status >= 200 && status < 400
}

// healthWant says what healthOK wants from path, for hints.
func healthWant(path string) string {
	if path == manifest.DefaultHealthcheck {
		return " with a status below 500"
	}
	return " with a 2xx or 3xx status"
}

// instanceEnv is the env every instance of an app environment gets, and its
// hash: when the hash changes the app restarts.
func (r *rt) instanceEnv(ctx context.Context, project, app, preview string, spec *manifest.App) (map[string]string, string, error) {
	env, err := r.p.ProjectEnv(ctx, project, app)
	if err != nil {
		return nil, "", err
	}
	if env["NO_COLOR"] == "" {
		env["NO_COLOR"] = "1" // logs are files, not terminals
	}
	if env["NODE_ENV"] == "" {
		env["NODE_ENV"] = "production"
	}
	if env["NODE_OPTIONS"] == "" && spec.MemoryMB > 0 {
		// Keep V8's heap inside the container's memory cap.
		env["NODE_OPTIONS"] = "--max-old-space-size=" + strconv.Itoa(max(64, spec.MemoryMB*3/4))
	}
	if preview != "" {
		env["TIFFIN_PREVIEW"] = preview
		// Previews never send real mail: their SMTP user routes to the dev inbox.
		if u := env["SMTP_URL"]; u != "" {
			env["SMTP_URL"] = email.PreviewSMTPURL(u, preview)
		}
	}
	if spec.Role != manifest.RoleWorker {
		d := &Deploy{App: app, Preview: preview}
		env["TIFFIN_URL"] = r.deployURL(d, spec)
	}
	h := sha256.New()
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%s\x00", k, env[k])
	}
	instances := spec.Instances
	if preview != "" {
		instances = 1
	}
	fmt.Fprintf(h, "instances=%d memory=%d role=%s health=%s", instances, spec.MemoryMB, spec.Role, spec.Healthcheck)
	// Containers started before project slices existed restart into theirs
	// once (with zero downtime) when the hash changes.
	fmt.Fprintf(h, " slice=%s", budget.Slice(project))
	return env, hex.EncodeToString(h.Sum(nil))[:16], nil
}

// plainEnv is the non-secret env visible at build time: the project's env
// resources and the app's own env (no secrets, no service credentials).
func (r *rt) plainEnv(ctx context.Context, project, app string) (map[string]string, error) {
	_, res, err := r.p.DB.Load(ctx, project)
	if err != nil {
		return nil, err
	}
	env := map[string]string{"TIFFIN_PROJECT": project, "TIFFIN_APP": app}
	for addr, rs := range res {
		if change.Kind(addr) == change.KindEnv {
			var v string
			if json.Unmarshal(rs.Spec, &v) == nil {
				env[change.Name(addr)] = v
			}
		}
	}
	if rs, ok := res[change.KindApp+"/"+app]; ok {
		var a manifest.App
		if json.Unmarshal(rs.Spec, &a) == nil {
			for k, v := range a.Env {
				env[k] = v
			}
		}
	}
	return env, nil
}

// converge brings one app environment in line with its spec: restart on a
// config change, replace instances that disappeared, wake a stopped app.
func (r *rt) converge(ctx context.Context, project, app, preview string, spec *manifest.App) error {
	unlock := r.lock(envKey(project, app, preview))
	defer unlock()
	st, err := r.st.getState(ctx, project, app, preview)
	if err != nil || st.Live == "" {
		return err
	}
	d, err := r.st.getDeploy(ctx, project, app, st.Live)
	if err != nil {
		return fmt.Errorf("live deploy %s: %w", st.Live, err)
	}
	if !d.Terminal() {
		return nil
	}
	if d.StaticRoot != "" {
		if st.Stopped {
			return r.promoteLocked(ctx, d, spec, modeRestart, io.Discard)
		}
		return nil
	}
	if st.Sleeping {
		return nil // a sleeping preview starts with the current config when it wakes
	}
	_, hash, err := r.instanceEnv(ctx, project, app, preview, spec)
	if err != nil {
		return err
	}
	want := max(1, spec.Instances)
	if preview != "" {
		want = 1
	}
	healthy := !st.Stopped && st.Hash == hash && len(st.Instances) == want
	if healthy {
		for _, in := range st.Instances {
			c, err := r.eng.Inspect(ctx, in.Name)
			if err != nil {
				return err
			}
			if c == nil {
				healthy = false // gone (removed by hand?): replace
				break
			}
		}
	}
	if healthy {
		return nil
	}
	reason := "config changed"
	switch {
	case st.Stopped:
		reason = "app is back"
	case len(st.Instances) != want:
		reason = fmt.Sprintf("instances %d → %d", len(st.Instances), want)
	}
	r.p.Log.Info("restarting app", "project", project, "app", app, "preview", preview, "reason", reason)
	return r.promoteLocked(ctx, d, spec, modeRestart, io.Discard)
}

// stopEnv stops an app environment (its app was deleted). Deploys and images
// are kept: undoing the delete starts the same deploy again.
func (r *rt) stopEnv(ctx context.Context, st *AppState) error {
	unlock := r.lock(envKey(st.Project, st.App, st.Preview))
	defer unlock()
	st, err := r.st.getState(ctx, st.Project, st.App, st.Preview)
	if err != nil {
		return err
	}
	if st.Stopped && len(st.Instances) == 0 && len(st.Draining) == 0 {
		return nil
	}
	ins := st.Instances
	for _, ds := range st.Draining {
		ins = append(ins, ds.Instances...)
	}
	st.Instances, st.Draining, st.Stopped = nil, nil, true
	if err := r.st.putState(ctx, st); err != nil {
		return err
	}
	_ = r.refreshIfNeeded(ctx)
	r.removeInstances(ctx, ins)
	if d, err := r.st.getDeploy(ctx, st.Project, st.App, st.Live); err == nil && d.Status == StatusLive {
		d.Status = StatusStopped
		_ = r.st.putDeploy(ctx, d)
	}
	return nil
}

// rollback makes an earlier deploy live again.
func (r *rt) rollback(ctx context.Context, project, app, id string) (*Deploy, error) {
	d, err := r.st.getDeploy(ctx, project, app, id)
	if err != nil {
		return nil, err
	}
	spec, err := r.appSpec(ctx, project, app)
	if err != nil {
		return nil, err
	}
	if d.Status == StatusLive {
		return d, nil
	}
	if !d.Rollbackable() {
		if d.Preview != "" && d.Status != StatusFailed && d.Status != StatusSkipped {
			return nil, &stateError{fmt.Sprintf("deploy %s was an earlier build of preview %s, and previews keep only their latest build", d.ID, d.Preview),
				"Deploy that version to the preview again: tiffin deploy --app " + app + " --preview " + d.Preview}
		}
		return nil, &stateError{fmt.Sprintf("deploy %s is %s and cannot be rolled back to", d.ID, d.Status),
			"Pick a deploy whose status is superseded or rolled_back: tiffin deploys list " + project + " " + app}
	}
	if d.Image != "" && !r.imageExists(ctx, d.Image) {
		return nil, &stateError{fmt.Sprintf("the image of deploy %s was cleaned up", d.ID), "Deploy that version again."}
	}
	log, _ := os.OpenFile(r.buildLogPath(d), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	var w io.Writer = io.Discard
	if log != nil {
		defer log.Close()
		w = log
		fmt.Fprintf(w, "==> rollback to %s requested %s\n", d.ID, time.Now().UTC().Format(time.RFC3339))
	}
	if err := r.promote(ctx, d, spec, modeRollback, w); err != nil {
		return nil, err
	}
	return d, nil
}

func (r *rt) imageExists(ctx context.Context, ref string) bool {
	_, err := r.eng.ImageDigest(ctx, ref)
	return err == nil
}

// stateError is a request that does not fit the current state (409).
type stateError struct{ msg, hint string }

func (e *stateError) Error() string { return e.msg }

// gc removes images, static files and work dirs of old deploys. It keeps
// the live deploy, production's newest KeepImages rollback targets (a
// preview keeps only its live build) and any release still running or
// pinned for workflow runs.
func (r *rt) gc(ctx context.Context, project, app, preview string) {
	ds, err := r.st.listDeploys(ctx, project, app, preview)
	if err != nil {
		return
	}
	keep, busy := r.opt.KeepImages, map[string]bool{}
	if preview != "" {
		keep = 0
	} else {
		if st, err := r.st.getState(ctx, project, app, ""); err == nil {
			for _, dr := range st.Draining {
				busy[dr.Release] = true
			}
		}
		rels, _ := r.pinnedReleases(ctx, project, app)
		for _, rel := range rels {
			busy[rel] = true
		}
	}
	kept := 0
	for i, d := range ds {
		if !d.Terminal() || d.Status == StatusLive {
			continue
		}
		if d.Rollbackable() && kept < keep {
			kept++
			continue
		}
		if busy[d.ID] {
			continue
		}
		if d.Image != "" {
			_ = r.eng.RemoveImage(ctx, d.Image)
			d.Image = ""
		}
		if d.StaticRoot != "" {
			_ = os.RemoveAll(d.StaticRoot)
			d.StaticRoot = ""
		}
		if i >= 50 { // keep the newest 50 records (and their build logs)
			_ = os.RemoveAll(r.workDir(d))
			_ = r.st.deleteDeploy(ctx, d)
			continue
		}
		_ = r.st.putDeploy(ctx, d)
	}
	r.pruneLogs(project, app, preview, ds)
}

func previewSuffix(p string) string {
	if p == "" {
		return ""
	}
	return " (preview " + p + ")"
}

func digestNote(dg string) string {
	if dg == "" {
		return ""
	}
	return ", image " + dg
}

func urlNote(u string) string {
	if u == "" {
		return ""
	}
	return ": " + u
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }
