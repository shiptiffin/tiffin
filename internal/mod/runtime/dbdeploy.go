package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/manifest"
	"github.com/shiptiffin/tiffin/internal/mod/budget"
	"github.com/shiptiffin/tiffin/internal/mod/postgres"
	"github.com/shiptiffin/tiffin/internal/mod/valkey"
	"github.com/shiptiffin/tiffin/internal/platform"
)

// The database side of deploys: an app's release command (its
// migrations) runs once in the new image before it takes traffic, each
// preview gets its own copy-on-write branch of the project's database, and
// every instance is told how many connections its pool may open.

// BranchStore makes and drops the database branches previews use: the
// postgres module on a box, a fake in tests.
type BranchStore interface {
	CreatePreviewBranch(ctx context.Context, p *platform.Platform, project, name, preview string) (*postgres.PGBranchCreated, error)
	DeleteBranch(ctx context.Context, p *platform.Platform, project, name string) error
	ListBranches(ctx context.Context, p *platform.Platform, project string) ([]postgres.PGBranch, error)
}

// ReadAccess hands builds read-only connections: the postgres and valkey
// modules on a box, a fake in tests.
type ReadAccess interface {
	PostgresReadEnv(ctx context.Context, p *platform.Platform, project, branch string) (map[string]string, error)
	ValkeyReadEnv(ctx context.Context, p *platform.Platform, project string) (map[string]string, error)
}

type boxReadAccess struct{}

func (boxReadAccess) PostgresReadEnv(ctx context.Context, p *platform.Platform, project, branch string) (map[string]string, error) {
	return postgres.ReadEnv(ctx, p, project, branch)
}

func (boxReadAccess) ValkeyReadEnv(ctx context.Context, p *platform.Platform, project string) (map[string]string, error) {
	return valkey.ReadEnv(ctx, p, project)
}

type pgBranches struct{}

func (pgBranches) CreatePreviewBranch(ctx context.Context, p *platform.Platform, project, name, preview string) (*postgres.PGBranchCreated, error) {
	return postgres.CreatePreviewBranch(ctx, p, project, name, preview)
}

func (pgBranches) DeleteBranch(ctx context.Context, p *platform.Platform, project, name string) error {
	return postgres.DeleteBranch(ctx, p, project, name)
}

func (pgBranches) ListBranches(ctx context.Context, p *platform.Platform, project string) ([]postgres.PGBranch, error) {
	return postgres.ListBranches(ctx, p, project)
}

// previewBranchName is the database branch of a preview: "pv-" and the
// preview's name, or, when that is longer than a branch name may be, its
// start and a short hash of the whole.
func previewBranchName(preview string) string {
	name := "pv-" + preview
	if len(name) <= 19 {
		return name
	}
	sum := sha256.Sum256([]byte(preview))
	return "pv-" + strings.TrimRight(preview[:9], "-") + "-" + hex.EncodeToString(sum[:])[:6]
}

// postgresSpec is the project's postgres service, or nil without one.
func (r *rt) postgresSpec(ctx context.Context, project string) *manifest.Postgres {
	_, res, err := r.p.DB.Load(ctx, project)
	if err != nil {
		return nil
	}
	rs, ok := res[change.KindService+"/postgres"]
	if !ok {
		return nil
	}
	var pg manifest.Postgres
	_ = json.Unmarshal(rs.Spec, &pg)
	return &pg
}

// previewBranch is the database branch a preview's apps use, or "" for
// production, a project without postgres, or previews on the shared
// database.
func (r *rt) previewBranch(ctx context.Context, project, preview string) string {
	if preview == "" {
		return ""
	}
	pg := r.postgresSpec(ctx, project)
	if pg == nil || pg.Previews == manifest.PreviewDBShared {
		return ""
	}
	return previewBranchName(preview)
}

// useBranch points env's database variables at branch: each one that still
// has the box's value for the main database (the app's own env and
// secrets win) gets the branch's.
func (r *rt) useBranch(ctx context.Context, project, branch string, env map[string]string) error {
	main, err := postgres.ConnEnv(ctx, r.p, project, "", false)
	if err != nil {
		return err
	}
	br, err := postgres.ConnEnv(ctx, r.p, project, branch, false)
	if err != nil {
		return err
	}
	for k, v := range br {
		if env[k] == main[k] {
			env[k] = v
		}
	}
	return nil
}

// ensurePreviewBranch makes a preview's database branch on its first
// deploy. It returns the branch, or "" when the preview uses production's
// database.
func (r *rt) ensurePreviewBranch(ctx context.Context, d *Deploy, log io.Writer) (string, error) {
	b := r.previewBranch(ctx, d.Project, d.Preview)
	if b == "" {
		return "", nil
	}
	out, err := r.opt.Branches.CreatePreviewBranch(ctx, r.p, d.Project, b, d.Preview)
	var pe *api.Problem
	switch {
	case errors.As(err, &pe) && pe.Code == "conflict":
		fmt.Fprintf(log, "==> database: the preview uses its branch %s\n", b)
	case err != nil:
		return "", &BuildError{Msg: "could not make the preview's database branch: " + err.Error(),
			Hint: "Each preview gets its own copy of the database (services.postgres.previews). Fix the cause and deploy again, " +
				"or set services.postgres.previews to \"shared\" to let previews use the production database."}
	default:
		fmt.Fprintf(log, "==> database: the preview gets branch %s, a copy-on-write copy of the production database (%d ms)\n", b, out.TotalMs)
	}
	return b, nil
}

// dropPreviewBranch deletes a preview's database branch once no app of the
// project has that preview any more.
func (r *rt) dropPreviewBranch(ctx context.Context, project, preview string) {
	if r.postgresSpec(ctx, project) == nil {
		return
	}
	if r.previewInUse(ctx, project, preview) {
		return
	}
	b := previewBranchName(preview)
	err := r.opt.Branches.DeleteBranch(ctx, r.p, project, b)
	var pe *api.Problem
	if errors.As(err, &pe) && pe.Status == 404 {
		return
	}
	if err != nil {
		r.p.Log.Warn("runtime: delete a preview's database branch", "project", project, "preview", preview, "branch", b, "err", err)
		return
	}
	r.p.Log.Info("preview database branch deleted", "project", project, "preview", preview, "branch", b)
}

// previewInUse reports whether an app of project has a preview of this
// name, or a deploy to one is under way.
func (r *rt) previewInUse(ctx context.Context, project, preview string) bool {
	states, err := r.st.allStates(ctx)
	if err != nil {
		return true
	}
	for _, s := range states {
		if s.Project == project && s.Preview == preview {
			return true
		}
	}
	_, res, err := r.p.DB.Load(ctx, project)
	if err != nil {
		return true
	}
	for addr := range res {
		if change.Kind(addr) != change.KindApp {
			continue
		}
		ds, err := r.st.listDeploys(ctx, project, change.Name(addr), preview)
		if err != nil {
			return true
		}
		for _, d := range ds {
			if !d.Terminal() {
				return true
			}
		}
	}
	return false
}

// branchGrace is how old a preview's branch must be before the sweep may
// delete it for having no preview: its first deploy makes it before the
// preview exists.
const branchGrace = time.Hour

// sweepPreviewBranches deletes preview branches whose preview is gone (a
// first deploy that failed, a restart between deleting a preview and its
// branch).
func (r *rt) sweepPreviewBranches(ctx context.Context) {
	projects, err := r.p.DB.ListProjects(ctx)
	if err != nil {
		return
	}
	for _, pr := range projects {
		if r.postgresSpec(ctx, pr) == nil {
			continue
		}
		bs, err := r.opt.Branches.ListBranches(ctx, r.p, pr)
		if err != nil {
			continue
		}
		for _, b := range bs {
			if b.Preview == "" || time.Since(b.CreatedAt) < branchGrace || r.previewInUse(ctx, pr, b.Preview) {
				continue
			}
			if err := r.opt.Branches.DeleteBranch(ctx, r.p, pr, b.Name); err == nil {
				r.p.Log.Info("preview database branch deleted: its preview is gone", "project", pr, "branch", b.Name)
			}
		}
	}
}

// ---- the release command ----

// releaseError is a release command that failed: the deploy stops before
// its instances start.
type releaseError struct{ msg string }

func (e *releaseError) Error() string { return e.msg }

const releaseHint = "The running version keeps serving: nothing was switched. The deploy log has the command's output; fix it and deploy again. " +
	"A migration that stopped part-way may have applied some steps, so keep migrations transactional or safe to run again."

// runRelease runs the app's release command once for d, before it takes
// traffic: a one-off container of the new image with the app's env (a
// preview's own database branch), its memory cap, project slice and disk
// folders, its output in the deploy log. Releases of one app environment
// run one at a time: the caller holds the environment's deployLock. A
// preview without its own branch skips it.
func (r *rt) runRelease(ctx context.Context, d *Deploy, spec *manifest.App, branch string, log io.Writer) error {
	cmd := strings.TrimSpace(spec.Release)
	if cmd == "" || d.Image == "" || d.StaticRoot != "" {
		return nil
	}
	if d.Preview != "" && branch == "" {
		fmt.Fprintf(log, "==> release: skipped: this preview has no database of its own (services.postgres.previews is \"shared\", or there is no postgres service), so it does not run `%s`\n", cmd)
		return nil
	}
	env, _, err := r.instanceEnv(ctx, d.Project, d.App, d.Preview, spec)
	if err != nil {
		return err
	}
	env["TIFFIN_DEPLOY"] = d.ID
	r.releaseDirect(ctx, d.Project, branch, env)
	mounts, err := r.diskMounts(ctx, d, spec, log)
	if err != nil {
		return err
	}
	name := containerName(d.Project, d.App, d.Preview, 0) + "-release"
	if !r.claimName(ctx, name) {
		return &releaseError{"container name " + name + " is taken by a container that could not be removed"}
	}
	// A port in the table marks the container as owned, so the orphan
	// sweep leaves a long release alone.
	port, err := r.allocPort(name)
	if err != nil {
		return err
	}
	defer r.freePort(port, name)
	script := cmd
	if d.Dir != "" && d.Builder == "" { // a Dockerfile's image runs in its own WORKDIR
		script = "cd " + shellQuote(d.Dir) + " && " + cmd
	}
	rs := RunSpec{Name: name, Image: d.Image, MemoryMB: spec.MemoryMB, Env: env, CgroupParent: budget.Slice(d.Project), Mounts: mounts,
		Labels: map[string]string{"tiffin.project": d.Project, "tiffin.app": d.App, "tiffin.preview": d.Preview, "tiffin.deploy": d.ID, "tiffin.release": "1"}}
	where := "the production database"
	if branch != "" {
		where = "database branch " + branch
	}
	fmt.Fprintf(log, "==> release: %s (once, before the new version takes traffic; %s)\n", cmd, where)
	began := time.Now()
	tctx, cancel := context.WithTimeout(ctx, r.opt.ReleaseTimeout)
	defer cancel()
	var out tailBuffer
	code, err := r.eng.RunTask(tctx, rs, script, io.MultiWriter(log, &out))
	d.ReleaseSecs = round1(time.Since(began).Seconds())
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case tctx.Err() != nil:
		return &releaseError{fmt.Sprintf("the release command (%s) did not finish within %s and was stopped", cmd, r.opt.ReleaseTimeout)}
	case err != nil:
		return &releaseError{fmt.Sprintf("the release command (%s) could not run: %v", cmd, err)}
	case code != 0:
		return &releaseError{fmt.Sprintf("the release command (%s) exited with code %d. Last lines:\n%s", cmd, code, lastLines(out.String(), 10))}
	}
	fmt.Fprintf(log, "==> release done in %.1fs\n", d.ReleaseSecs)
	return nil
}

// releaseDirect points the release command's DATABASE_URL straight at
// Postgres: migration tools hold session state (advisory locks, SET) that
// a transaction pooler would spread over several server connections. A
// DATABASE_URL the app sets itself is left alone.
func (r *rt) releaseDirect(ctx context.Context, project, branch string, env map[string]string) {
	if r.postgresSpec(ctx, project) == nil {
		return
	}
	box, err := postgres.ConnEnv(ctx, r.p, project, branch, false)
	if err != nil || env["DATABASE_URL"] != box["DATABASE_URL"] {
		return
	}
	env["DATABASE_URL"] = box["DIRECT_DATABASE_URL"]
	if env["PGPORT"] == box["PGPORT"] {
		env["PGPORT"] = strconv.Itoa(postgres.Port)
	}
}

// tailBuffer keeps the last 8 KiB written to it.
type tailBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(p) >= 8<<10 {
		t.b = append(t.b[:0], p[len(p)-8<<10:]...)
		return len(p), nil
	}
	t.b = append(t.b, p...)
	if over := len(t.b) - 8<<10; over > 0 {
		t.b = append(t.b[:0], t.b[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}

// ---- connection budget ----

// poolMax is how many connections one instance's pool should open. They
// are client connections to the pooler, which shares the project's server
// connections among them, so the number only has to stay inside the
// project's client limit there: every instance counts twice during a
// deploy (the old ones drain), and previews get a few.
func poolMax(instances int, preview bool) int {
	if preview {
		return postgres.PoolMaxPreview
	}
	return max(1, min(postgres.PoolMaxProduction, postgres.PoolerClientLimit/2/(2*max(1, instances))))
}

// projectInstances counts the production instances of a project's apps
// that can open database connections (all but static sites).
func projectInstances(apps map[string]manifest.App) int {
	n := 0
	for _, a := range apps {
		if a.Framework != manifest.FrameworkStatic {
			n += max(1, a.Instances)
		}
	}
	return n
}

// appSpecs are a project's applied app specs.
func (r *rt) appSpecs(ctx context.Context, project string) map[string]manifest.App {
	out := map[string]manifest.App{}
	_, res, err := r.p.DB.Load(ctx, project)
	if err != nil {
		return out
	}
	for addr, rs := range res {
		if change.Kind(addr) != change.KindApp {
			continue
		}
		var a manifest.App
		if json.Unmarshal(rs.Spec, &a) == nil {
			out[change.Name(addr)] = a
		}
	}
	return out
}

// setPoolMax gives env DATABASE_POOL_MAX for a project with postgres,
// unless the app's env or secrets set it.
func (r *rt) setPoolMax(ctx context.Context, project, preview string, env map[string]string) {
	if env["DATABASE_POOL_MAX"] != "" || r.postgresSpec(ctx, project) == nil {
		return
	}
	env["DATABASE_POOL_MAX"] = strconv.Itoa(poolMax(projectInstances(r.appSpecs(ctx, project)), preview != ""))
}
