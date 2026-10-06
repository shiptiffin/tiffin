package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/mod/postgres"
	"github.com/btahir/tiffin/internal/platform"
)

// fakeBranches stands in for the postgres module's branches.
type fakeBranches struct {
	mu      sync.Mutex
	made    map[string]postgres.PGBranch // project/name
	creates int
	fail    error
}

func (f *fakeBranches) CreatePreviewBranch(ctx context.Context, p *platform.Platform, project, name, preview string) (*postgres.PGBranchCreated, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return nil, f.fail
	}
	if _, ok := f.made[project+"/"+name]; ok {
		return nil, api.NewProblem(409, "conflict", fmt.Sprintf("branch %q already exists", name))
	}
	f.creates++
	b := postgres.PGBranch{Name: name, Database: postgres.BranchDatabase(project, name), From: "main", Preview: preview, CreatedAt: time.Now()}
	f.made[project+"/"+name] = b
	return &postgres.PGBranchCreated{PGBranch: b, TotalMs: 12}, nil
}

func (f *fakeBranches) DeleteBranch(ctx context.Context, p *platform.Platform, project, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.made[project+"/"+name]; !ok {
		return api.NewProblem(404, "not_found", "no branch "+name)
	}
	delete(f.made, project+"/"+name)
	return nil
}

func (f *fakeBranches) ListBranches(ctx context.Context, p *platform.Platform, project string) ([]postgres.PGBranch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []postgres.PGBranch
	for k, b := range f.made {
		if strings.HasPrefix(k, project+"/") {
			out = append(out, b)
		}
	}
	return out, nil
}

func (f *fakeBranches) has(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.made["shop/"+name]
	return ok
}

// withPostgres gives the harness project a database, and api a release
// command.
func (h *harness) withPostgres(previews manifest.PreviewDatabase) {
	h.mf.Services.Postgres = &manifest.Postgres{Previews: previews}
	api := h.mf.Apps["api"]
	api.Release = "bun run migrate.ts"
	h.mf.Apps["api"] = api
	h.apply()
}

// instanceEnv is the env of a running instance of app's environment.
func (h *harness) instanceEnv(app, preview string) map[string]string {
	h.t.Helper()
	for _, c := range h.eng.running() {
		if c.spec.Labels["tiffin.app"] == app && c.spec.Labels["tiffin.preview"] == preview {
			return c.spec.Env
		}
	}
	h.t.Fatalf("no running instance of %s %q", app, preview)
	return nil
}

func TestReleaseCommand(t *testing.T) {
	h := newHarness(t)
	h.withPostgres("")
	v1 := h.deploy("api", "", map[string]string{"index.ts": "v1"})
	if v1.Status != StatusLive {
		t.Fatalf("v1: %s %s", v1.Status, v1.Error)
	}
	tasks := h.eng.taskList()
	if len(tasks) != 1 {
		t.Fatalf("release runs: %d", len(tasks))
	}
	rt := tasks[0]
	env := rt.spec.Env
	if rt.script != "bun run migrate.ts" || rt.spec.Image != v1.Image || rt.runs != 0 || rt.spec.MemoryMB != 256 || rt.spec.CgroupParent == "" ||
		env["TIFFIN_DEPLOY"] != v1.ID || !strings.Contains(env["DATABASE_URL"], ":5432/p_shop?") || env["DIRECT_DATABASE_URL"] != env["DATABASE_URL"] ||
		env["PGPORT"] != "5432" || env["DATABASE_POOL_MAX"] == "" || rt.spec.Labels["tiffin.release"] != "1" {
		t.Fatalf("release ran before any instance, in the new image, with the app's env (straight to Postgres) and limits: %+v (runs before: %d)", rt.spec, rt.runs)
	}
	log := h.buildLogText(v1)
	for _, want := range []string{"==> release: bun run migrate.ts", "migrating p_shop", "==> release done in"} {
		if !strings.Contains(log, want) {
			t.Errorf("build log lacks %q:\n%s", want, log)
		}
	}
	if strings.Index(log, "==> release done") > strings.Index(log, "==> starting") {
		t.Errorf("the release must finish before instances start:\n%s", log)
	}
	if ie := h.instanceEnv("api", ""); !strings.Contains(ie["DATABASE_URL"], ":6432/p_shop?") || !strings.Contains(ie["DIRECT_DATABASE_URL"], ":5432/p_shop?") ||
		ie["PGPORT"] != "6432" || ie["DATABASE_POOL_MAX"] != "20" {
		t.Errorf("instances: DATABASE_URL %q, DIRECT_DATABASE_URL %q, DATABASE_POOL_MAX %q", ie["DATABASE_URL"], ie["DIRECT_DATABASE_URL"], ie["DATABASE_POOL_MAX"])
	}

	// A failing release stops the deploy before its instances start; v1 serves.
	runs := h.eng.runs
	bad := h.deploy("api", "", map[string]string{"index.ts": "v2", "RELEASE_FAIL": "1"})
	if bad.Status != StatusFailed || !strings.Contains(bad.Error, "exited with code 1") || !strings.Contains(bad.Error, `relation "notes" already exists`) || bad.Hint != releaseHint {
		t.Fatalf("failed release: %s %q %q", bad.Status, bad.Error, bad.Hint)
	}
	if h.eng.runs != runs {
		t.Fatalf("a failed release must start no instance (%d → %d)", runs, h.eng.runs)
	}
	if code, body := h.get("shop.tiffin.localhost", "/api/healthz"); code != 200 || !strings.Contains(body, strings.ToLower(v1.ID)) {
		t.Fatalf("v1 must keep serving: %d %s", code, body)
	}
	if st := h.state("api", ""); st.Live != v1.ID {
		t.Fatalf("live %s", st.Live)
	}

	// A release that runs too long is stopped, and fails the deploy.
	h.r.opt.ReleaseTimeout = 200 * time.Millisecond
	slow := h.deploy("api", "", map[string]string{"index.ts": "v3", "RELEASE_HANG": "1"})
	if slow.Status != StatusFailed || !strings.Contains(slow.Error, "did not finish within 200ms") {
		t.Fatalf("slow release: %s %q", slow.Status, slow.Error)
	}
	h.r.opt.ReleaseTimeout = time.Minute

	// Rolling back does not run it.
	v4 := h.deploy("api", "", map[string]string{"index.ts": "v4"})
	n := len(h.eng.taskList())
	if _, err := h.r.rollback(context.Background(), "shop", "api", v1.ID); err != nil {
		t.Fatal(err)
	}
	if len(h.eng.taskList()) != n || v4.Status != StatusLive {
		t.Fatalf("rollback ran the release command (%d → %d)", n, len(h.eng.taskList()))
	}
	// Static apps and apps without one run nothing.
	h.deploy("jobs", "", map[string]string{"worker.ts": "w"})
	if len(h.eng.taskList()) != n {
		t.Fatal("an app without a release command ran one")
	}
}

func TestPreviewDatabaseBranch(t *testing.T) {
	h := newHarness(t)
	h.withPostgres("")
	prod := h.deploy("api", "", map[string]string{"index.ts": "v1"})
	if prod.Status != StatusLive {
		t.Fatal(prod.Error)
	}
	pv := h.deploy("api", "pr-7", map[string]string{"index.ts": "feature"})
	if pv.Status != StatusLive {
		t.Fatalf("preview: %s", pv.Error)
	}
	b, ok := h.pgb.made["shop/pv-pr-7"]
	if !ok || b.Preview != "pr-7" || h.pgb.creates != 1 {
		t.Fatalf("branch: %+v (%d made)", h.pgb.made, h.pgb.creates)
	}
	tasks := h.eng.taskList()
	if got := tasks[len(tasks)-1].spec.Env; !strings.Contains(got["DATABASE_URL"], ":5432/p_shop__pv_pr_7?") || got["PGDATABASE"] != "p_shop__pv_pr_7" {
		t.Fatalf("the preview's release must migrate its branch: %v", got["DATABASE_URL"])
	}
	ie := h.instanceEnv("api", "pr-7")
	if !strings.Contains(ie["DATABASE_URL"], ":6432/p_shop__pv_pr_7?") || !strings.Contains(ie["DIRECT_DATABASE_URL"], ":5432/p_shop__pv_pr_7?") || ie["DATABASE_POOL_MAX"] != "5" {
		t.Fatalf("preview instance env: %s %s pool %s", ie["DATABASE_URL"], ie["DIRECT_DATABASE_URL"], ie["DATABASE_POOL_MAX"])
	}
	if pe := h.instanceEnv("api", ""); !strings.Contains(pe["DATABASE_URL"], "/p_shop?") {
		t.Fatalf("production keeps its database: %s", pe["DATABASE_URL"])
	}
	if log := h.buildLogText(pv); !strings.Contains(log, "gets branch pv-pr-7") {
		t.Errorf("build log:\n%s", log)
	}
	// The next deploy and another app's preview of the same name use it.
	h.deploy("api", "pr-7", map[string]string{"index.ts": "feature 2"})
	w := h.deploy("jobs", "pr-7", map[string]string{"worker.ts": "w"})
	if h.pgb.creates != 1 || w.Status != StatusLive || !strings.Contains(h.instanceEnv("jobs", "pr-7")["DATABASE_URL"], "/p_shop__pv_pr_7?") {
		t.Fatalf("one branch per preview name: %d made, worker %s", h.pgb.creates, w.Status)
	}
	// Deleted with the last app preview of that name.
	ctx := context.Background()
	if err := h.r.deletePreview(ctx, "shop", "api", "pr-7"); err != nil {
		t.Fatal(err)
	}
	if !h.pgb.has("pv-pr-7") {
		t.Fatal("jobs' preview still uses the branch")
	}
	if err := h.r.deletePreview(ctx, "shop", "jobs", "pr-7"); err != nil {
		t.Fatal(err)
	}
	if h.pgb.has("pv-pr-7") {
		t.Fatal("the branch outlived its preview")
	}

	// The app's own DATABASE_URL wins over the branch's.
	h.mf.Env["DATABASE_URL"] = "postgresql://elsewhere/db"
	h.apply()
	h.deploy("api", "own", map[string]string{"index.ts": "x"})
	if got := h.instanceEnv("api", "own")["DATABASE_URL"]; got != "postgresql://elsewhere/db" {
		t.Fatalf("own DATABASE_URL: %s", got)
	}
	delete(h.mf.Env, "DATABASE_URL")

	// "shared": previews use production's database and skip the release.
	h.withPostgres(manifest.PreviewDBShared)
	n := len(h.eng.taskList())
	sh := h.deploy("api", "pr-8", map[string]string{"index.ts": "shared"})
	if sh.Status != StatusLive || h.pgb.has("pv-pr-8") || len(h.eng.taskList()) != n {
		t.Fatalf("shared preview: %s, branch %v, release ran %v", sh.Status, h.pgb.has("pv-pr-8"), len(h.eng.taskList()) != n)
	}
	if log := h.buildLogText(sh); !strings.Contains(log, "==> release: skipped") {
		t.Errorf("build log:\n%s", log)
	}
	if got := h.instanceEnv("api", "pr-8")["DATABASE_URL"]; !strings.Contains(got, "/p_shop?") {
		t.Fatalf("shared preview DATABASE_URL: %s", got)
	}

	// A branch that cannot be made fails the preview's deploy, saying how out.
	h.withPostgres("")
	h.pgb.fail = errors.New("project shop is read-only")
	f := h.deploy("api", "pr-9", map[string]string{"index.ts": "x"})
	if f.Status != StatusFailed || !strings.Contains(f.Error, "read-only") || !strings.Contains(f.Hint, `"shared"`) {
		t.Fatalf("branch failure: %s %q %q", f.Status, f.Error, f.Hint)
	}
	h.pgb.fail = nil

	// The sweep deletes old branches whose preview is gone, never live ones.
	h.pgb.made["shop/pv-gone"] = postgres.PGBranch{Name: "pv-gone", Preview: "gone", CreatedAt: time.Now().Add(-2 * time.Hour)}
	h.pgb.made["shop/pv-new"] = postgres.PGBranch{Name: "pv-new", Preview: "new", CreatedAt: time.Now()}
	h.pgb.made["shop/manual"] = postgres.PGBranch{Name: "manual", CreatedAt: time.Now().Add(-48 * time.Hour)}
	h.deploy("api", "kept", map[string]string{"index.ts": "x"})
	h.pgb.mu.Lock()
	b = h.pgb.made["shop/pv-kept"]
	b.CreatedAt = time.Now().Add(-2 * time.Hour)
	h.pgb.made["shop/pv-kept"] = b
	h.pgb.mu.Unlock()
	h.r.sweepPreviewBranches(ctx)
	if h.pgb.has("pv-gone") || !h.pgb.has("pv-new") || !h.pgb.has("manual") || !h.pgb.has("pv-kept") {
		t.Fatalf("sweep: %v", h.pgb.made)
	}
}

func TestPreviewBranchName(t *testing.T) {
	for in, want := range map[string]string{"pr-12": "pv-pr-12", "7": "pv-7", "feature-login-": "pv-feature-login-"} {
		if got := previewBranchName(in); got != want {
			t.Errorf("%s → %s, want %s", in, got, want)
		}
	}
	long := previewBranchName("a-very-long-preview-name-30chr")
	if len(long) > 19 || !postgres.BranchPattern.MatchString(long) || long == previewBranchName("a-very-long-preview-name-30chs") {
		t.Fatalf("long name %q", long)
	}
}

func TestPoolMax(t *testing.T) {
	// Client connections to the pooler: generous, and inside the project's
	// client limit there even during a deploy (every instance twice).
	for _, c := range []struct{ instances, want int }{{1, 20}, {6, 20}, {12, 20}, {25, 10}, {600, 1}} {
		if got := poolMax(c.instances, false); got != c.want {
			t.Errorf("poolMax(%d) = %d, want %d", c.instances, got, c.want)
		}
		if got := 2 * c.instances * poolMax(c.instances, false); c.instances < 250 && got > postgres.PoolerClientLimit/2 {
			t.Errorf("%d instances open %d client connections during a deploy", c.instances, got)
		}
	}
	if got := poolMax(3, true); got != postgres.PoolMaxPreview {
		t.Errorf("preview poolMax = %d", got)
	}
	apps := map[string]manifest.App{"web": {Instances: 4}, "api": {Instances: 2}, "site": {Framework: manifest.FrameworkStatic, Instances: 1}}
	if w := poolWarning("shop", postgres.PoolerClientLimit, apps, nil, nil); w != "" {
		t.Fatalf("the box's own sizing fits: %q", w)
	}
	// An app's own value that overruns the pooler's client limit.
	apps["web"] = manifest.App{Instances: 4, Env: map[string]string{"DATABASE_POOL_MAX": "200"}}
	w := poolWarning("shop", postgres.PoolerClientLimit, apps, nil, nil)
	if !strings.Contains(w, "can open 1680 (api 2 × 20, web 4 × 200, doubled)") || !strings.Contains(w, "may hold 1000 at the connection pooler") {
		t.Fatalf("overrun warning: %q", w)
	}
}

func TestPublicEnvAndAliases(t *testing.T) {
	h := newHarness(t)
	next := &manifest.App{Framework: manifest.FrameworkNext, Role: manifest.RoleWeb, Routes: []string{"shop"}}
	all := map[string]string{"TIFFIN_PUBLIC_SENTRY_DSN": "https://k@errors/1", "NEXT_PUBLIC_A": "a", "VITE_B": "b", "PUBLIC_C": "c", "SECRET": "s", "S3_PUBLIC_ENDPOINT": "x"}
	pub := h.r.publicEnv("shop", "web", "", next, all)
	if len(pub) != 5 || pub["NEXT_PUBLIC_SENTRY_DSN"] != "https://k@errors/1" || pub["NEXT_PUBLIC_TIFFIN_URL"] != "https://shop.tiffin.localhost:8443" || pub["SECRET"] != "" {
		t.Fatalf("next public env: %v", pub)
	}
	if pv := h.r.publicEnv("shop", "web", "pr-1", next, all); pv["NEXT_PUBLIC_TIFFIN_URL"] != "https://pr-1--shop.tiffin.localhost:8443" {
		t.Fatalf("preview URL alias: %v", pv)
	}
	all["NEXT_PUBLIC_SENTRY_DSN"] = "mine"
	if pub := h.r.publicEnv("shop", "web", "", next, all); pub["NEXT_PUBLIC_SENTRY_DSN"] != "mine" {
		t.Fatal("the app's own value wins")
	}
	if pub := h.r.publicEnv("shop", "api", "", &manifest.App{Framework: manifest.FrameworkHono}, all); pub["NEXT_PUBLIC_TIFFIN_URL"] != "" || len(pub) != 4 {
		t.Fatalf("aliases are for Next.js only: %v", pub)
	}
	if publicEnvHash(nil) == "" || publicEnvHash(map[string]string{"A": "1"}) == publicEnvHash(map[string]string{"A": "2"}) {
		t.Fatal("hash")
	}
}

func TestPublicEnvChangeRebuilds(t *testing.T) {
	h := newHarness(t)
	h.mf.Env["NEXT_PUBLIC_GREETING"] = "one"
	h.apply()
	ctx := context.Background()
	sealed, _ := h.p.Secrets.SealSpec("public-key", "test")
	v, _, _ := h.p.DB.Load(ctx, "shop")
	if err := h.p.DB.Commit(ctx, &change.Change{ID: "chg_01J00000000000000000000099", Project: "shop", Version: v + 1,
		Plan: change.Plan{Project: "shop", BaseVersion: v, Ops: []change.Op{{Action: change.Create, Address: "secret/VITE_KEY", After: sealed}}}}); err != nil {
		t.Fatal(err)
	}
	v1 := h.deploy("api", "", map[string]string{"index.ts": "v1"})
	if v1.Status != StatusLive || v1.PublicEnv == "" {
		t.Fatalf("v1: %s %q", v1.Status, v1.PublicEnv)
	}
	h.bld.mu.Lock()
	env := h.bld.envs[v1.ID]
	h.bld.mu.Unlock()
	if env["NEXT_PUBLIC_GREETING"] != "one" || env["VITE_KEY"] != "public-key" {
		t.Fatalf("build env lacks the public values (secrets included): %v", env)
	}
	if !exists(filepath.Join(h.r.workDir(v1), sourceFile)) {
		t.Fatal("the live deploy keeps its source")
	}
	builds := h.bld.builds.Load()

	// A plain env change restarts: no build.
	h.mf.Env["GREETING"] = "hi"
	h.apply()
	if h.bld.builds.Load() != builds || h.state("api", "").Live != v1.ID {
		t.Fatal("a server-only env change must not rebuild")
	}

	// A browser-visible one rebuilds from the kept source and goes live.
	h.mf.Env["NEXT_PUBLIC_GREETING"] = "two"
	h.apply()
	rb := h.waitRebuild("api", v1.ID)
	if rb.Status != StatusLive || rb.Trigger != "env" || rb.CreatedBy != "box" || h.bld.builds.Load() != builds+1 {
		t.Fatalf("rebuild: %+v", rb)
	}
	h.bld.mu.Lock()
	env = h.bld.envs[rb.ID]
	h.bld.mu.Unlock()
	if env["NEXT_PUBLIC_GREETING"] != "two" {
		t.Fatalf("rebuild env: %v", env)
	}
	if log := h.buildLogText(rb); !strings.Contains(log, "==> rebuild of "+v1.ID+": env built into browser code changed (NEXT_PUBLIC_GREETING, VITE_KEY)") {
		t.Errorf("build log:\n%s", log)
	}
	if code, body := h.get("shop.tiffin.localhost", "/api/healthz"); code != 200 || !strings.Contains(body, strings.ToLower(rb.ID)) {
		t.Fatalf("the rebuild serves: %d %s", code, body)
	}
	// Converging again changes nothing.
	h.apply()
	if h.bld.builds.Load() != builds+1 {
		t.Fatal("rebuilt twice")
	}

	// A rebuild that fails is not retried: the app restarts with the new
	// server env instead, and the next deploy carries the value.
	h.bld.failAll.Store(true)
	h.mf.Env["NEXT_PUBLIC_GREETING"] = "three"
	h.apply()
	bad := h.waitRebuild("api", rb.ID)
	if bad.Status != StatusFailed {
		t.Fatalf("rebuild should fail: %s", bad.Status)
	}
	h.apply()
	if h.bld.builds.Load() != builds+2 {
		t.Fatalf("a failed rebuild was retried (%d builds)", h.bld.builds.Load()-builds)
	}
	waitFor(t, func() bool { return h.instanceEnv("api", "")["NEXT_PUBLIC_GREETING"] == "three" })
	if h.state("api", "").Live != rb.ID {
		t.Fatal("the failed rebuild must not go live")
	}
}

// waitRebuild waits for a deploy of app newer than after to finish.
func (h *harness) waitRebuild(app, after string) *Deploy {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		ds, _ := h.r.st.listDeploys(context.Background(), "shop", app, "")
		if len(ds) > 0 && ds[0].ID > after && ds[0].Terminal() {
			return ds[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatalf("no deploy after %s", after)
	return nil
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestPlanWarnings(t *testing.T) {
	appSpec := func(a manifest.App) json.RawMessage { b, _ := json.Marshal(a); return b }
	plan := &change.Plan{Project: "shop", Ops: []change.Op{
		{Action: change.Update, Address: "env/NEXT_PUBLIC_API", Before: json.RawMessage(`"a"`), After: json.RawMessage(`"b"`)},
		{Action: change.Update, Address: "app/site", Before: appSpec(manifest.App{Framework: "static", Env: map[string]string{"VITE_X": "1"}}),
			After: appSpec(manifest.App{Framework: "static", Env: map[string]string{"VITE_X": "2"}})},
	}}
	desired := map[string]change.Resource{
		"app/web":             {Spec: appSpec(manifest.App{Framework: "next", Instances: 2})},
		"app/site":            {Spec: appSpec(manifest.App{Framework: "static"})},
		"app/jobs":            {Spec: appSpec(manifest.App{Role: manifest.RoleWorker})},
		"env/NEXT_PUBLIC_API": {Spec: json.RawMessage(`"b"`)},
	}
	w := (&Module{}).PlanWarnings(context.Background(), nil, "shop", plan, desired)
	if len(w) != 1 || !strings.Contains(w[0], "NEXT_PUBLIC_API, VITE_X changes") || !strings.Contains(w[0], "so site, web rebuild") {
		t.Fatalf("warnings: %q", w)
	}
	if w := inlinedChanges(&change.Plan{Ops: []change.Op{{Action: change.Update, Address: "env/GREETING"}}}, nil); w != "" {
		t.Fatalf("plain env: %q", w)
	}
}
