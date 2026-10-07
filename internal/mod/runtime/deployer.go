package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/ids"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/mod/auth"
	"github.com/btahir/tiffin/internal/mod/budget"
	"github.com/btahir/tiffin/internal/mod/email"
	"github.com/btahir/tiffin/internal/mod/postgres"
	"github.com/btahir/tiffin/internal/mod/runtime/srcpack"
	"github.com/btahir/tiffin/internal/mod/valkey"
	"github.com/btahir/tiffin/internal/peer"
)

// newDeploy records a queued deploy. Its source must already be on disk.
func (r *rt) newDeploy(ctx context.Context, project, app, preview, source, by string, spec *manifest.App) (*Deploy, error) {
	d := &Deploy{ID: ids.New("dep"), Project: project, App: app, Preview: preview, Status: StatusQueued, Source: source,
		Framework: string(spec.Framework), CreatedAt: time.Now().UTC(), CreatedBy: by}
	d.URL = r.deployURL(d, spec)
	if preview == "" {
		versionMu.Lock()
		defer versionMu.Unlock() // until the deploy carrying the version is stored
		v, err := r.st.nextVersion(ctx, project, app)
		if err != nil {
			return nil, err
		}
		d.Version = v
	}
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
	r.startFrom(d, kind, func(context.Context, *Deploy, io.Writer) (string, error) { return src, nil })
}

// startFrom runs a queued deploy in the background: fetch gets its source
// (a git clone, say) and returns the source archive, then the pipeline runs.
// Both write to the deploy's build log; a failure in either fails the deploy.
// The pipeline (fetch too) works on its own copy of d: the caller keeps d as
// queued, to answer with or poll by, while the pipeline changes its copy.
// The returned channel closes when the pipeline is done (the deploy's
// record is terminal by then).
func (r *rt) startFrom(queued *Deploy, kind string, fetch func(ctx context.Context, d *Deploy, log io.Writer) (string, error)) <-chan struct{} {
	d := new(*queued)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		if done := r.opt.pipelineDone; done != nil {
			defer done(d.ID)
		}
		ctx := r.ctx
		log, err := r.openBuildLog(d)
		if err != nil {
			r.fail(ctx, d, err, "", io.Discard)
			return
		}
		defer log.Close()
		fmt.Fprintf(log, "==> deploy %s of %s/%s%s, queued %s\n", d.ID, d.Project, d.App, previewSuffix(d.Preview), d.CreatedAt.Format(time.RFC3339))
		src, err := fetch(ctx, d, log)
		if err == nil {
			err = r.pipeline(ctx, d, src, kind, log)
		}
		if errors.Is(err, errPreviewRetired) {
			now := time.Now().UTC()
			d.Status, d.Error, d.FinishedAt = StatusSkipped, "skipped: "+err.Error(), &now
			d.TotalSecs = round1(now.Sub(d.CreatedAt).Seconds())
			r.dropFailedImage(ctx, d)
			_ = r.st.putDeploy(ctx, d)
			fmt.Fprintf(log, "==> skipped: %s\n", err)
			return
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
			var re *releaseError
			if errors.As(err, &re) {
				hint = releaseHint
			}
			var ste *stateError
			if errors.As(err, &ste) {
				hint = ste.hint
			}
			r.fail(ctx, d, err, hint, log)
		}
	}()
	return finished
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
	if req.Env, err = r.plainEnv(ctx, d.Project, d.App); err != nil {
		return err
	}
	all, err := r.p.ProjectEnv(ctx, d.Project, d.App)
	if err == nil {
		// Env built into browser code is public: the build gets it from
		// every source, secrets included, and the deploy remembers it.
		pub := r.publicEnv(d.Project, d.App, d.Preview, spec, all)
		for k, v := range pub {
			req.Env[k] = v
		}
		d.PublicEnv = publicEnvHash(pub)
		if len(pub) > 0 {
			fmt.Fprintf(log, "==> browser env: %s (built into the client code; a change rebuilds the app)\n", publicEnvNames(pub))
		}
	}
	// A preview's database branch exists before its build, which reads it.
	branch := ""
	if spec.Framework != manifest.FrameworkStatic && kind != SourcePrebuilt {
		if err != nil {
			return err
		}
		if branch, err = r.ensurePreviewBranch(ctx, d, log); err != nil {
			return err
		}
		if req.RunEnv, err = r.buildRunEnv(ctx, d, spec, all, log); err != nil {
			return err
		}
	}
	if spec.Framework == manifest.FrameworkNext {
		// The same key at build and run time, deploy after deploy.
		if err != nil {
			return err
		}
		if req.Env[nextKeyEnv], err = r.nextActionsKey(ctx, d.Project, d.App, all); err != nil {
			return err
		}
		req.NextCache = r.hasService(ctx, d.Project, "valkey")
		req.Postgres = r.hasService(ctx, d.Project, "postgres")
	}
	// The build's deadline covers unpacking its source too.
	bctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	switch kind {
	case SourcePrebuilt:
		req.Prebuilt = src
	default:
		req.SrcDir = filepath.Join(r.workDir(d), "src")
		f, err := os.Open(src)
		if err != nil {
			return err
		}
		st, err := srcpack.Extract(ctxReader{bctx, f}, req.SrcDir, srcpack.DefaultLimits)
		f.Close()
		if err != nil {
			return &BuildError{Msg: "could not unpack the source: " + err.Error(), Hint: "Upload a gzipped tar of the app directory (tiffin deploy does this)."}
		}
		fmt.Fprintf(log, "==> source: %d files, %s\n", st.Files, humanBytes(st.Bytes))
		dropConfig(req.SrcDir, log)
		if err := r.readApp(d, spec, &req, log); err != nil {
			return err
		}
		if l := req.Launch; l != nil && l.Secret != "" && l.Files == nil {
			// Made once and kept, for every build, instance and preview.
			if _, err := r.appKey(ctx, d.Project, d.App, l.Secret, all); err != nil {
				return err
			}
		}
	}
	if req.Dockerfile != "" {
		spec.Builder = manifest.BuilderDockerfile // this deploy's hints and checks
	}
	d.Assets = nil
	if !req.Export && (req.Dockerfile == "" || spec.Assets != nil) {
		// A Dockerfile lays its image out its own way: the box serves its
		// client files only when the app names their folder (assets).
		for _, a := range clientAssets(req.appDir(), spec) {
			a.Dir = path.Join(d.Dir, a.Dir)
			d.Assets = append(d.Assets, a)
		}
	}
	res, err := r.bld.Build(bctx, req)
	cancel()
	// The unpacked sources are not needed once built (static sites moved
	// their output away). The archive stays while the build is kept: a
	// change to the env built into browser code rebuilds from it.
	_ = os.RemoveAll(filepath.Join(r.workDir(d), "src"))
	if keep := filepath.Join(r.workDir(d), sourceFile); kind == SourcePrebuilt || (src != keep && os.Rename(src, keep) != nil) {
		_ = os.Remove(src)
	}
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	d.BuiltAt = &now
	d.BuildSecs = round1(time.Since(began).Seconds())
	d.Image, d.Digest, d.StaticRoot, d.Start, d.SPAPage = res.Image, res.Digest, res.StaticRoot, res.Start, res.SPAPage
	if d.StaticRoot != "" {
		r.keepHashed(d, log)
	}
	if res.SPA {
		d.Framework = string(manifest.FrameworkStatic) + "+spa"
	}
	fmt.Fprintf(log, "==> built in %.1fs%s\n", d.BuildSecs, digestNote(d.Digest))
	release()
	if d.Preview != "" && r.previewRetired(envKey(d.Project, d.App, d.Preview)) != "" {
		return errPreviewRetired // its pull request closed while it built: never live
	}

	d.Status = StatusStarting
	_ = r.st.putDeploy(ctx, d)
	// From here to the switch, one deploy of an environment at a time: its
	// release command and switch never overlap another's, and a deploy a
	// newer one overtook (it went live meanwhile) stops before either.
	unlock := r.lock(deployLock(d.Project, d.App, d.Preview))
	defer unlock()
	if err := r.current(ctx, d); err != nil {
		return err
	}
	if d.StaticRoot == "" {
		if kind == SourcePrebuilt {
			if branch, err = r.ensurePreviewBranch(ctx, d, log); err != nil {
				return err
			}
		}
		if spec, err = r.runnable(ctx, d.Project, d.App); err != nil {
			return err // deleted or stopped while it built: no release command
		}
		if err := r.runRelease(ctx, d, spec, branch, log); err != nil {
			return err
		}
	}
	if err := r.promote(ctx, d, modeDeploy, log); err != nil {
		return err
	}
	fmt.Fprintf(log, "==> live in %.1fs total%s\n", d.TotalSecs, urlNote(d.URL))
	if d.Preview == "" {
		_ = r.syncCrons(ctx, d.Project, d.App, log)
		r.refreshIconLater(d.Project, d.App, d.ID)
	}
	r.gc(ctx, d.Project, d.App, d.Preview)
	return nil
}

func (r *rt) fail(ctx context.Context, d *Deploy, err error, hint string, log io.Writer) {
	var oe *overtakenError
	if errors.As(err, &oe) {
		r.dropFailedImage(ctx, d)
		r.markSkipped(ctx, d, oe.Error(), log)
		return
	}
	now := time.Now().UTC()
	d.Status, d.Error, d.Hint = StatusFailed, err.Error(), hint
	d.FinishedAt = &now
	d.TotalSecs = round1(now.Sub(d.CreatedAt).Seconds())
	r.dropFailedImage(ctx, d)
	_ = r.st.putDeploy(ctx, d)
	fmt.Fprintf(log, "==> FAILED: %s\n", err)
	if hint != "" {
		fmt.Fprintf(log, "    hint: %s\n", hint)
	}
	r.p.Log.Info("deploy failed", "deploy", d.ID, "project", d.Project, "app", d.App, "err", err)
}

// dropFailedImage removes the image a failed deploy built: it never served
// and cannot be rolled back to, and an app that keeps failing would
// otherwise keep one image per attempt until a deploy succeeds. Its
// instances were removed already; an image a leftover container still uses
// is refused by the engine and stays for the hourly sweep. Static files
// stay for gc: the edge's link may point at them already.
func (r *rt) dropFailedImage(ctx context.Context, d *Deploy) {
	if d.Image == "" {
		return
	}
	if st, err := r.st.getState(ctx, d.Project, d.App, d.Preview); err != nil || st.Live == d.ID {
		return
	}
	cctx, cancel := cleanupContext(ctx)
	defer cancel()
	if err := r.eng.RemoveImage(cctx, d.Image); err != nil && !noSuchImage(err) {
		r.p.Log.Warn("runtime: remove a failed deploy's image (the hourly sweep tries again)", "deploy", d.ID, "image", d.Image, "err", err)
		return
	}
	d.Image = ""
}

const (
	modeDeploy   = "deploy"
	modeRollback = "rollback"
	modeRestart  = "restart"
	modeWake     = "wake"
)

// deployLock names the lock a deploy of an app environment holds from its
// build's end to its switch (release command included).
func deployLock(project, app, preview string) string {
	return "deploy " + envKey(project, app, preview)
}

// overtakenError is a deploy a newer deploy of its environment overtook:
// that one went live first, so this one never does (it is skipped).
type overtakenError struct{ id, live string }

func (e *overtakenError) Error() string {
	return "deploy " + e.live + ", which is newer, went live first"
}

// current returns an overtakenError when a deploy made after d is live in
// d's environment. Deploy IDs sort by creation time. A rollback to an
// older deploy does not count: a deploy made after it still goes live.
func (r *rt) current(ctx context.Context, d *Deploy) error {
	st, err := r.st.getState(ctx, d.Project, d.App, d.Preview)
	if err != nil {
		return err
	}
	if st.Live != "" && st.Live > d.ID {
		return &overtakenError{id: d.ID, live: st.Live}
	}
	return nil
}

// markSkipped ends a deploy that will not go live, without failing it.
func (r *rt) markSkipped(ctx context.Context, d *Deploy, why string, log io.Writer) {
	now := time.Now().UTC()
	d.Status, d.FinishedAt, d.Error, d.Hint = StatusSkipped, &now, "skipped: "+why, ""
	d.TotalSecs = round1(now.Sub(d.CreatedAt).Seconds())
	_ = r.st.putDeploy(ctx, d)
	fmt.Fprintf(log, "==> skipped: %s\n", why)
}

// runnable returns an app's spec as it is now, or a stateError when the app
// may not run: its project is stopped, or the app was deleted.
func (r *rt) runnable(ctx context.Context, project, app string) (*manifest.App, error) {
	_, res, err := r.p.DB.Load(ctx, project)
	if err != nil {
		return nil, err
	}
	if _, ok := res[change.KindStopped]; ok {
		return nil, &stateError{"project " + project + " is stopped, so its apps do not start (was it moved to another box?)",
			"Start it again with `tiffin projects start " + project + "`, then deploy."}
	}
	rs, ok := res[change.KindApp+"/"+app]
	if !ok {
		return nil, &stateError{"app " + app + " was deleted from project " + project + ", so it does not start",
			"Add apps." + app + " to tiffin.config.ts again and apply, then deploy."}
	}
	var a manifest.App
	if err := json.Unmarshal(rs.Spec, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// promote makes d serve its app environment with zero downtime: start new
// instances, wait until healthy, switch the edge to them, let the old ones
// drain, then stop them. Any failure before the switch leaves the old
// instances serving. It starts d with the app's settings as they are now
// (a deploy's build may have taken minutes), and not at all once the app
// is deleted or its project stopped.
func (r *rt) promote(ctx context.Context, d *Deploy, mode string, log io.Writer) error {
	unlock := r.lock(envKey(d.Project, d.App, d.Preview))
	defer unlock()
	spec, err := r.runnable(ctx, d.Project, d.App)
	if err != nil {
		return err
	}
	if d.Builder != "" {
		spec.Builder = manifest.Builder(d.Builder) // this deploy's hints and checks
	}
	return r.promoteLocked(ctx, d, spec, mode, log)
}

func (r *rt) promoteLocked(ctx context.Context, d *Deploy, spec *manifest.App, mode string, log io.Writer) error {
	if _, err := r.runnable(ctx, d.Project, d.App); err != nil {
		return err
	}
	st, err := r.st.getState(ctx, d.Project, d.App, d.Preview)
	if err != nil {
		return err
	}
	switch {
	case mode == modeDeploy && st.Live != "" && st.Live > d.ID:
		// A newer deploy went live while this one started (an import, a
		// release that took no build slot): never put an older one over it.
		return &overtakenError{id: d.ID, live: st.Live}
	case (mode == modeRestart || mode == modeWake) && st.Live != d.ID:
		return &stateError{"deploy " + d.ID + " is no longer live (" + st.Live + " is)", "Try again: it restarts the live deploy."}
	}
	prev := *st
	env, hash, err := r.instanceEnv(ctx, d.Project, d.App, d.Preview, spec)
	if err != nil {
		return err
	}
	var (
		started []Instance
		fp      string
		// parked: the stopped instances a sleep kept; reused: this wake
		// started them again instead of new ones.
		parked = st.Parked
		reused bool
	)
	if d.StaticRoot == "" {
		r.ensureAssets(ctx, d, log)
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
		var mounts []string
		mounts, err = r.diskMounts(ctx, d, spec, log)
		fp = runPrint(d.ID, hash, env, mounts)
		if err == nil && mode == modeWake && len(parked) == n && st.Print == fp && parkedFor(parked, d.ID) {
			if uerr := r.unpark(ctx, d, spec, parked); uerr == nil {
				started, reused = parked, true
			} else {
				r.p.Log.Warn("runtime: a sleeping app's kept containers did not start; starting new ones", "project", d.Project, "app", d.App, "preview", d.Preview, "err", uerr)
				bad := parked
				st.Parked, parked = nil, nil
				_ = r.st.putState(ctx, st)
				r.removeInstances(ctx, bad)
			}
		}
		if err == nil && !reused {
			// A wake starts the release that passed its checks at deploy.
			started, err = r.startInstances(ctx, st, d, spec, n, env, mounts, mode != modeWake)
		}
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
	st.Retired = retire(st.Retired, prev.Live, d.ID, time.Now().UTC())
	st.Live, st.Instances, st.Hash, st.Print, st.Stopped, st.Sleeping, st.SleptAt, st.Parked = d.ID, started, hash, fp, false, false, nil, nil
	// Deploys, rollbacks, restarts and wakes count as activity.
	r.touch(envKey(d.Project, d.App, d.Preview))
	undo := func() {
		if reused {
			r.park(ctx, started) // still asleep: kept for the next wake
		} else {
			r.removeInstances(ctx, started)
		}
	}
	if err := r.st.putState(ctx, st); err != nil {
		undo()
		return err
	}
	// Deleted or stopped while it started: the stop that change made read
	// the app's states before this one was saved, so it left this start
	// alone. Undo it here. (A stop that reads them after this save waits
	// for the lock, then stops it itself.)
	if _, err := r.runnable(ctx, d.Project, d.App); err != nil {
		if prev.UpdatedAt.IsZero() {
			_ = r.st.deleteState(ctx, &prev)
		} else {
			_ = r.st.putState(ctx, &prev)
		}
		_ = r.refreshIfNeeded(ctx)
		undo()
		return err
	}
	// The edge only reloads when hosts, paths or file roots change (a first
	// deploy, a new route); instance switches stay inside the switchboard.
	if err := r.refreshIfNeeded(ctx); err != nil {
		_ = r.st.putState(ctx, &prev)
		_ = r.p.RefreshRoutes(ctx)
		undo()
		return fmt.Errorf("switch edge routes: %w", err)
	}
	if len(parked) > 0 && !reused {
		// A sleeping app's kept containers of another release or config.
		go r.removeInstances(r.ctx, parked)
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
		r.drains.Go(func() { r.drainThenRemove(old) })
	}
	return nil
}

// runPrint fingerprints what an app environment's instances start with: the
// release, the full env (DATABASE_POOL_MAX included, which hash leaves out),
// the mounts and hash (count, memory, role, health check, slice, disks).
func runPrint(deploy, hash string, env map[string]string, mounts []string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00", deploy, hash)
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%s\x00", k, env[k])
	}
	for _, m := range mounts {
		fmt.Fprintf(h, "mount=%s\x00", m)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// parkedFor reports whether every parked instance runs release id.
func parkedFor(ins []Instance, id string) bool {
	for _, in := range ins {
		if in.Deploy != id {
			return false
		}
	}
	return true
}

// startInstances starts n containers for d and waits for all to be healthy.
// On failure every container it started is removed again, and the serials
// it used stay used: a container that could not be removed never blocks
// the next start's name.
func (r *rt) startInstances(ctx context.Context, st *AppState, d *Deploy, spec *manifest.App, n int, env map[string]string, mounts []string, smoke bool) ([]Instance, error) {
	var out []Instance
	for i := 0; i < n; i++ {
		in, err := r.runInstance(ctx, st, d, spec, i, env, mounts)
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
			logPath := r.logFile(d.Project, d.App, d.Preview, d.ID, serialOf(in.Name))
			errs[i] = r.waitHealthy(ctx, in, spec, logPath)
			if errs[i] == nil && smoke && i == 0 && spec.Role != manifest.RoleWorker {
				if name := smokeName(spec, d); name != "" {
					errs[i] = smokeSSR(ctx, r.appClient(15*time.Second), in, spec, name, logPath)
				}
			}
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
func (r *rt) runInstance(ctx context.Context, st *AppState, d *Deploy, app *manifest.App, i int, env map[string]string, mounts []string) (Instance, error) {
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
			Labels:       map[string]string{"tiffin.project": d.Project, "tiffin.app": d.App, "tiffin.preview": d.Preview, "tiffin.deploy": d.ID, "tiffin.port": strconv.Itoa(port)},
			Mounts:       append([]string(nil), mounts...),
			Command:      d.Start}
		if app.Framework == manifest.FrameworkNext && d.Builder == "" {
			// Optimized images outlive the release; the environment's instances share them.
			dir := r.nextCacheDir(d.Project, d.App, d.Preview)
			if err := os.MkdirAll(dir, 0o755); err == nil {
				spec.Mounts = append(spec.Mounts, dir+":"+path.Join("/app", d.Dir, strings.TrimPrefix(nextImageCache, "/app/")))
			}
		}
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
	r.removeInstancesGrace(ctx, ins, r.opt.StopGrace)
}

func (r *rt) removeInstancesGrace(ctx context.Context, ins []Instance, grace time.Duration) {
	ctx, cancel := cleanupContext(ctx)
	defer cancel()
	var wg sync.WaitGroup
	for _, in := range ins {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.eng.Remove(ctx, in.Name, grace); err != nil {
				r.p.Log.Error("remove container", "name", in.Name, "err", err)
			}
			r.freePort(in.Port)
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
	client, dialer := r.appClient(3*time.Second), r.appDialer(time.Second)
	worker := spec.Role == manifest.RoleWorker
	upSince := time.Now()
	// The first inspect waits a second: it runs nerdctl, which would delay
	// the first checks of a start that is over in a few hundred ms (a
	// process that exits at once is still caught then).
	lastInspect := upSince
	lastStatus := ""
	var exited *Container // seen exited, even if its restart policy brought it back
	for {
		if time.Since(lastInspect) > time.Second {
			lastInspect = time.Now()
			c, err := r.eng.Inspect(ctx, in.Name)
			if err == nil && (c == nil || (!c.Running && c.Status != "restarting" && c.Status != "created")) {
				return exitedError(in.Name, c, logPath, spec)
			}
			if err == nil && c.Status == "restarting" {
				exited = c
			}
		}
		if worker {
			// Workers need not serve HTTP; one that stays up for 3s (or answers) is healthy.
			conn, err := dialer.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", in.Port))
			if err == nil {
				conn.Close()
				return nil
			}
			if foreign(err) {
				return portTakenError(in, err)
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
			} else if foreign(err) {
				return portTakenError(in, err)
			} else {
				lastStatus = "not answering"
			}
		}
		if time.Now().After(deadline) {
			if exited != nil {
				return exitedError(in.Name, exited, logPath, spec)
			}
			return &healthError{msg: fmt.Sprintf("instance %s did not pass its health check (GET %s: %s) within %s. Last log lines:\n%s",
				in.Name, path, lastStatus, r.opt.HealthTimeout, tailLog(logPath, 15)),
				hint: "Make sure the app listens on the port in $PORT and answers " + path + healthWant(path) + " (set healthcheck in tiffin.config.ts)." + imageHint(*spec) + onNodeHint(*spec)}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(healthPoll(time.Since(upSince))):
		}
	}
}

// healthPoll is how long waitHealthy waits between checks: 10ms for the
// first 2s, which is when a wake (a request waiting) or a deploy is usually
// done, then 200ms. A check before the app listens is a refused connect on
// localhost, a few microseconds; 200 of them cost no measurable CPU.
func healthPoll(since time.Duration) time.Duration {
	if since < 2*time.Second {
		return 10 * time.Millisecond
	}
	return 200 * time.Millisecond
}

// smokeName is the framework a deploy's first instance is smoke-tested
// for after its health check, "" for none: Next.js, and the full-stack
// frameworks the box launches (SvelteKit, Nuxt, React Router).
func smokeName(spec *manifest.App, d *Deploy) string {
	switch {
	case spec.Framework == manifest.FrameworkNext:
		return "Next.js"
	case d.Launch != "":
		return launchNames[d.Launch]
	}
	return ""
}

// launchNames are the frameworks a Deploy's Launch names.
var launchNames = map[string]string{"sveltekit": "SvelteKit", "nuxt": "Nuxt", "react-router": "React Router"}

// smokeSSR asks a server-rendering instance that passed its health check
// for two pages that take different paths through its framework: the home
// page and a page that doesn't exist (its not-found render). A 5xx on
// either stops the deploy, which is how a runtime incompatibility (a Bun
// release, a library leaning on Node internals) shows before the new
// version takes traffic.
func smokeSSR(ctx context.Context, client *http.Client, in Instance, spec *manifest.App, framework, logPath string) error {
	for _, p := range []string{"/", "/_tiffin/smoke-" + strconv.FormatInt(time.Now().UnixNano(), 36)} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", in.Port, p), nil)
		req.Header.Set("User-Agent", "tiffin-healthcheck")
		res, err := client.Do(req)
		if err != nil {
			return &healthError{msg: fmt.Sprintf("instance %s passed its health check but GET %s failed: %v. Last log lines:\n%s", in.Name, p, err, tailLog(logPath, 15)),
				hint: "The app stopped answering after it started." + onNodeHint(*spec)}
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		if res.StatusCode >= 500 {
			what := "the home page"
			if p != "/" {
				what = "a page that doesn't exist (" + framework + "'s not-found page)"
			}
			return &healthError{msg: fmt.Sprintf("instance %s passed its health check but %s answered HTTP %d. Last log lines:\n%s", in.Name, what, res.StatusCode, tailLog(logPath, 15)),
				hint: "The new version keeps the old one serving until this works; the log lines usually say why." + onNodeHint(*spec)}
		}
	}
	return nil
}

// foreign reports an error from an instance's port answered by a process
// of another project (internal/peer).
func foreign(err error) bool {
	var fe *peer.ForeignError
	return errors.As(err, &fe)
}

// portTakenError is an instance whose port another app holds: the instance
// cannot listen on it, so waiting for its health check is pointless (a
// wake then starts new instances on other ports).
func portTakenError(in Instance, err error) error {
	return &healthError{msg: fmt.Sprintf("instance %s cannot serve on port %d: %v", in.Name, in.Port, err),
		hint: "Another app on the box took the port while this one was stopped; starting the app again gives it a new port."}
}

// exitedError explains an instance whose process exited on start (a syntax
// error, a missing module or env var): its exit code and last log lines.
func exitedError(name string, c *Container, logPath string, spec *manifest.App) *healthError {
	how := "exited"
	if c != nil && c.ExitCode != 0 {
		how = fmt.Sprintf("exited with code %d", c.ExitCode)
	}
	return &healthError{msg: fmt.Sprintf("instance %s %s before it was healthy. Last log lines:\n%s", name, how, tailLog(logPath, 15)),
		hint: "The app " + how + " on start: its last log lines say why (a syntax error, a missing module, env var or secret). Fix that and deploy again." + onNodeHint(*spec)}
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
		// Keep V8's heap inside the container's memory cap. Bun ignores the
		// flag; its knobs (--smol, BUN_JSC_forceRAMSize) made no measurable
		// difference to a Next.js app under load (examples/next-showcase/bench).
		env["NODE_OPTIONS"] = "--max-old-space-size=" + strconv.Itoa(max(64, spec.MemoryMB*3/4))
	}
	if preview != "" {
		env["TIFFIN_PREVIEW"] = preview
		// Previews never send real mail: their SMTP user routes to the dev inbox.
		if u := env["SMTP_URL"]; u != "" {
			env["SMTP_URL"] = email.PreviewSMTPURL(u, preview)
		}
		// Nor do they touch production's database, unless the project says so.
		if b := r.previewBranch(ctx, project, preview); b != "" {
			if err := r.useBranch(ctx, project, b, env); err != nil {
				return nil, "", err
			}
		}
	}
	if spec.Role != manifest.RoleWorker {
		d := &Deploy{Project: project, App: app, Preview: preview}
		env["TIFFIN_URL"] = r.deployURL(d, spec)
		if preview != "" {
			auth.PreviewEnv(env, env["TIFFIN_URL"])
		}
		if spec.Framework == manifest.FrameworkNext {
			nextOrigin(env, r.deployURL(&Deploy{Project: project, App: app}, spec), env["TIFFIN_URL"], preview != "")
		}
	}
	addNextAliases(env, spec)
	if spec.Framework == manifest.FrameworkNext {
		if env[nextKeyEnv], err = r.nextActionsKey(ctx, project, app, env); err != nil {
			return nil, "", err
		}
	}
	if spec.Framework == manifest.FrameworkBun && env[nuxtSecretEnv] == "" {
		// Kept since a build found Nuxt (launch.go); the app's own wins.
		if v, ok, err := r.storedKey(ctx, project, app, nuxtSecretEnv); err != nil {
			return nil, "", err
		} else if ok {
			env[nuxtSecretEnv] = v
		}
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
	if len(spec.Disk) > 0 {
		fmt.Fprintf(h, " disk=%q", spec.Disk.Paths()) // a new folder mounts on a restart; a new size needs none
	}
	// Outside the hash: a new budget (another app, a new limit) applies at
	// the next start rather than restarting every app of the project.
	r.setPoolMax(ctx, project, preview, env)
	return env, hex.EncodeToString(h.Sum(nil))[:16], nil
}

// instanceOnlyEnv are defaults the box gives instances (production mode,
// their memory cap); a build has its own, unless the app sets them.
var instanceOnlyEnv = []string{"NODE_ENV", "NODE_OPTIONS"}

// buildRunEnv is the env a Railpack build reads besides its plain env: what
// the environment's instances get (services, secrets, a preview's database
// branch, TIFFIN_URL), as on Vercel, where builds get the project's env, so
// generateStaticParams and prerenders can query the database. Railpack hands
// it to build steps as BuildKit secrets: it never lands in the image. own is
// the project's env (ProjectEnv).
func (r *rt) buildRunEnv(ctx context.Context, d *Deploy, spec *manifest.App, own map[string]string, log io.Writer) (map[string]string, error) {
	env, _, err := r.instanceEnv(ctx, d.Project, d.App, d.Preview, spec)
	if err != nil {
		return nil, err
	}
	for _, k := range instanceOnlyEnv {
		if own[k] == "" {
			delete(env, k)
		}
	}
	// The database (a preview's: its branch) and Valkey through read-only
	// users, where the env still holds the box's connection: a build reads
	// data but never writes to it.
	if r.hasService(ctx, d.Project, "postgres") {
		branch := r.previewBranch(ctx, d.Project, d.Preview)
		box, err := postgres.ConnEnv(ctx, r.p, d.Project, branch, false)
		if err != nil {
			return nil, err
		}
		if env["DATABASE_URL"] == box["DATABASE_URL"] {
			ro, err := r.opt.ReadAccess.PostgresReadEnv(ctx, r.p, d.Project, branch)
			if err != nil {
				return nil, err
			}
			for k, v := range ro {
				if env[k] == box[k] {
					env[k] = v
				}
			}
		}
	}
	if r.hasService(ctx, d.Project, "valkey") {
		// Each of the box's credentials (the URL, the REST tokens) is swapped
		// for its read-only twin on its own: an app that set one of them
		// itself keeps its own, and the others are still read-only.
		box, err := valkey.ConnEnv(ctx, r.p, d.Project, false)
		if err != nil {
			return nil, err
		}
		rest, err := valkey.RESTEnv(ctx, r.p, d.Project)
		if err != nil {
			return nil, err
		}
		maps.Copy(box, rest)
		ro, err := r.opt.ReadAccess.ValkeyReadEnv(ctx, r.p, d.Project)
		if err != nil {
			return nil, err
		}
		for k, v := range ro {
			if env[k] == box[k] {
				env[k] = v
			}
		}
	}
	var svc []string
	for _, k := range []string{"DATABASE_URL", "REDIS_URL", "S3_ENDPOINT", "TIFFIN_FILES_URL", "TIFFIN_URL"} {
		if env[k] != "" {
			svc = append(svc, k)
		}
	}
	if len(svc) > 0 {
		fmt.Fprintf(log, "==> build env: the app's env, secrets and services as its instances get them (%s), as build secrets, never in the image; prerenders read live data\n", strings.Join(svc, ", "))
	}
	return env, nil
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
	// A sleeping production app is rebuilt too (the deploy wakes it); a
	// sleeping preview picks the change up at its next deploy.
	if !st.Stopped && (!st.Sleeping || preview == "") && r.rebuildIfStale(ctx, d, spec) {
		return nil // the rebuild goes live with the new env
	}
	if d.StaticRoot != "" {
		if st.Stopped {
			return r.promoteLocked(ctx, d, spec, modeRestart, io.Discard)
		}
		return nil
	}
	if st.Sleeping {
		return nil // a sleeping app starts with the current config when it wakes
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

// stopEnv stops an app environment (its app was deleted, or its project
// stopped). The live deploy and its image are kept: undoing the delete (or
// starting the project) starts the same deploy again. A deleted app keeps
// it for deletedAppKeep (see sweepImages).
func (r *rt) stopEnv(ctx context.Context, st *AppState) error {
	unlock := r.lock(envKey(st.Project, st.App, st.Preview))
	defer unlock()
	st, err := r.st.getState(ctx, st.Project, st.App, st.Preview)
	if err != nil {
		return err
	}
	if st.Stopped && len(st.Instances) == 0 && len(st.Draining) == 0 && len(st.Parked) == 0 {
		return nil
	}
	ins := slices.Concat(st.Instances, st.Parked)
	for _, ds := range st.Draining {
		ins = append(ins, ds.Instances...)
	}
	// Not asleep: a start brings it back awake.
	st.Instances, st.Draining, st.Stopped, st.Sleeping, st.SleptAt, st.Parked = nil, nil, true, false, nil, nil
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

// rollback makes an earlier deploy live again. It checks the deploy under
// its environment's lock, which gc holds while it removes builds: a build
// it finds is still there when it starts.
func (r *rt) rollback(ctx context.Context, project, app, id string) (*Deploy, error) {
	d, err := r.st.getDeploy(ctx, project, app, id)
	if err != nil {
		return nil, err
	}
	unlock := r.lock(envKey(project, app, d.Preview))
	defer unlock()
	if d, err = r.st.getDeploy(ctx, project, app, id); err != nil {
		return nil, err
	}
	spec, err := r.runnable(ctx, project, app)
	if err != nil {
		return nil, err
	}
	if d.Builder != "" {
		spec.Builder = manifest.Builder(d.Builder)
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
	log, _ := r.openBuildLog(d)
	var w io.Writer = io.Discard
	if log != nil {
		defer log.Close()
		w = log
		fmt.Fprintf(w, "==> rollback to %s requested %s\n", d.ID, time.Now().UTC().Format(time.RFC3339))
	}
	if err := r.promoteLocked(ctx, d, spec, modeRollback, w); err != nil {
		return nil, err
	}
	if d.Preview == "" {
		_ = r.syncCrons(ctx, project, app, w)
		r.refreshIconLater(project, app, d.ID)
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

// gc removes images, source archives, static files and work dirs of old
// deploys of an app environment: everything keptDeploys does not keep.
func (r *rt) gc(ctx context.Context, project, app, preview string) {
	r.gcKeeping(ctx, project, app, preview, r.rollbackTargets(preview))
}

// rollbackTargets is how many earlier builds an environment keeps to roll
// back to: KeepImages for production, none for a preview.
func (r *rt) rollbackTargets(preview string) int {
	if preview != "" {
		return 0
	}
	return r.opt.KeepImages
}

// gcKeeping is gc with the environment's newest rollbacks rollback targets.
// It holds the environment's lock: what it keeps (the live deploy above
// all) cannot change between choosing and removing, and a rollback checks
// its target under the same lock.
func (r *rt) gcKeeping(ctx context.Context, project, app, preview string, rollbacks int) {
	unlock := r.lock(envKey(project, app, preview))
	defer unlock()
	r.gcLocked(ctx, project, app, preview, rollbacks)
}

// gcLocked is gcKeeping for a caller holding the environment's lock.
func (r *rt) gcLocked(ctx context.Context, project, app, preview string, rollbacks int) {
	ds, err := r.st.listDeploys(ctx, project, app, preview)
	if err != nil {
		return
	}
	keep := r.keptDeploys(ctx, project, app, preview, ds, rollbacks)
	for i, d := range ds {
		if keep[d.ID] {
			continue
		}
		r.dropBuild(ctx, d)
		if i >= 50 { // keep the newest 50 records (and their build logs)
			_ = os.RemoveAll(r.workDir(d))
			_ = r.st.deleteDeploy(ctx, d)
			continue
		}
		_ = r.st.putDeploy(ctx, d)
	}
	r.pruneLogs(project, app, preview, ds)
}

// keptDeploys picks the deploys of one app environment (ds, newest first)
// whose builds stay: unfinished ones, the environment's live deploy (also
// once its app is deleted and it is stopped: an undo starts it again), the
// newest rollbacks rollback targets, and releases still draining or pinned
// for workflow runs.
func (r *rt) keptDeploys(ctx context.Context, project, app, preview string, ds []*Deploy, rollbacks int) map[string]bool {
	keep := map[string]bool{}
	if st, err := r.st.getState(ctx, project, app, preview); err == nil {
		if st.Live != "" {
			keep[st.Live] = true
		}
		for _, dr := range st.Draining {
			keep[dr.Release] = true
		}
	}
	if preview == "" {
		rels, ok := r.pinnedReleases(ctx, project, app)
		if !ok {
			rollbacks = len(ds) // no answer on workflow pins: keep every rollback target
		}
		for _, rel := range rels {
			keep[rel] = true
		}
	}
	kept := 0
	for _, d := range ds {
		switch {
		case !d.Terminal() || d.Status == StatusLive:
			keep[d.ID] = true
		case d.Rollbackable() && kept < rollbacks:
			kept++
			keep[d.ID] = true
		}
	}
	return keep
}

// dropBuild removes what a deploy's build left (source archive, image,
// static files) and clears them from the record; the caller saves it. An
// image that would not go stays in the record, for the next gc or the
// hourly sweep.
func (r *rt) dropBuild(ctx context.Context, d *Deploy) {
	_ = os.Remove(filepath.Join(r.workDir(d), sourceFile))
	if d.Image != "" {
		if err := r.eng.RemoveImage(ctx, d.Image); err != nil && !noSuchImage(err) {
			r.p.Log.Warn("runtime: remove an image (the hourly sweep tries again)", "image", d.Image, "err", err)
		} else {
			d.Image = ""
		}
	}
	if d.StaticRoot != "" {
		_ = os.RemoveAll(d.StaticRoot)
		d.StaticRoot = ""
	}
}

func noSuchImage(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "no such image") || strings.Contains(s, "not found")
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

// ctxReader is r that stops reading once ctx is done.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
