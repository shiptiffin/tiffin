package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/change/changetest"
	"github.com/btahir/tiffin/internal/edge"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/mod/postgres"
	"github.com/btahir/tiffin/internal/mod/runtime/srcpack"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
	"github.com/btahir/tiffin/internal/tokens"
)

// ---- fakes ----

type fakeCtr struct {
	spec    RunSpec
	srv     *http.Server
	running bool
}

// fakeEngine "runs" a container as an in-process HTTP server on its port
// that answers with its image and GREETING env, and logs each request in
// the json-file format nerdctl writes.
type fakeEngine struct {
	mu   sync.Mutex
	ctrs map[string]*fakeCtr
	// images is the image store: each name and the content it points at.
	// Names are kept as stored; commands read theirs the way nerdctl does
	// (dockerName).
	images map[string]string
	crash  map[string]bool
	runs   int
	// stuck: removing an exited container fails, as nerdctl rm did on a
	// box for a container crashing under its restart policy.
	stuck bool
	// names nerdctl's name store holds with no container behind them.
	leaked map[string]bool
	// trees: a copy of the source each built image came from (CopyOut).
	trees map[string]string
	// tasks: every RunTask (release command), in order.
	tasks []fakeTask
}

// fakeTask is one RunTask call, and how many containers had run before it.
type fakeTask struct {
	spec   RunSpec
	script string
	runs   int
}

// RunTask "runs" a release command: it fails when the image's source has a
// RELEASE_FAIL file and runs until stopped with RELEASE_HANG.
func (e *fakeEngine) RunTask(ctx context.Context, s RunSpec, script string, log io.Writer) (int, error) {
	e.mu.Lock()
	e.tasks = append(e.tasks, fakeTask{spec: s, script: script, runs: e.runs})
	tree := e.trees[dockerName(s.Image)]
	e.mu.Unlock()
	fmt.Fprintf(log, "migrating %s\n", s.Env["PGDATABASE"])
	switch {
	case exists(filepath.Join(tree, "RELEASE_FAIL")):
		fmt.Fprintln(log, `error: relation "notes" already exists`)
		return 1, nil
	case exists(filepath.Join(tree, "RELEASE_HANG")):
		<-ctx.Done()
		return -1, ctx.Err()
	}
	return 0, nil
}

func (e *fakeEngine) taskList() []fakeTask {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]fakeTask(nil), e.tasks...)
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{ctrs: map[string]*fakeCtr{}, images: map[string]string{}, crash: map[string]bool{}, leaked: map[string]bool{}, trees: map[string]string{}}
}

func (e *fakeEngine) setStuck(v bool) {
	e.mu.Lock()
	e.stuck = v
	e.mu.Unlock()
}

func (e *fakeEngine) leak(name string) {
	e.mu.Lock()
	e.leaked[name] = true
	e.mu.Unlock()
}

func appendLog(path, stream, text string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(map[string]any{"log": text + "\n", "stream": stream, "time": time.Now().UTC()})
	f.Write(append(b, '\n'))
}

func (e *fakeEngine) Run(ctx context.Context, s RunSpec) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, dup := e.ctrs[s.Name]; dup || e.leaked[s.Name] {
		return fmt.Errorf("start container %s: exit status 1: name-store error: name %q is already used", s.Name, s.Name)
	}
	e.runs++
	c := &fakeCtr{spec: s}
	e.ctrs[s.Name] = c
	if e.crash[s.Image] {
		appendLog(s.LogPath, "stderr", "boom: missing DATABASE_URL")
		return nil
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.Port))
	if err != nil {
		return err
	}
	c.running = true
	c.srv = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appendLog(s.LogPath, "stdout", "GET "+r.URL.Path)
		if r.Method == http.MethodPost { // an upload: say how much came
			n, _ := io.Copy(io.Discard, r.Body)
			fmt.Fprintf(w, "read %d ", n)
		}
		if d, err := time.ParseDuration(r.URL.Query().Get("sleep")); err == nil { // a long request
			time.Sleep(d)
		}
		fmt.Fprintf(w, "%s greeting=%s preview=%s", s.Image, s.Env["GREETING"], s.Env["TIFFIN_PREVIEW"])
	})}
	appendLog(s.LogPath, "stdout", "listening on "+s.Env["PORT"])
	go c.srv.Serve(ln)
	return nil
}

func (e *fakeEngine) Remove(ctx context.Context, name string, grace time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err // nerdctl runs under ctx: a cancelled one removes nothing
	}
	e.mu.Lock()
	c := e.ctrs[name]
	if c != nil && !c.running && e.stuck {
		e.mu.Unlock()
		return fmt.Errorf("nerdctl rm: exit status 1: cannot remove container %s", name)
	}
	delete(e.ctrs, name)
	e.mu.Unlock()
	if c != nil && c.srv != nil {
		sctx, cancel := context.WithTimeout(ctx, grace)
		defer cancel()
		_ = c.srv.Shutdown(sctx)
	}
	return nil
}

func (e *fakeEngine) Inspect(ctx context.Context, name string) (*Container, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	c := e.ctrs[name]
	if c == nil {
		return nil, nil
	}
	st, code := "exited", 1
	if c.running {
		st, code = "running", 0
	}
	return &Container{Name: name, Running: c.running, Status: st, ExitCode: code, Labels: c.spec.Labels}, nil
}

func (e *fakeEngine) List(ctx context.Context) ([]Container, error) {
	e.mu.Lock()
	names := make([]string, 0, len(e.ctrs))
	for n := range e.ctrs {
		names = append(names, n)
	}
	e.mu.Unlock()
	var out []Container
	for _, n := range names {
		if c, _ := e.Inspect(ctx, n); c != nil {
			out = append(out, *c)
		}
	}
	return out, nil
}

func (e *fakeEngine) running() []*fakeCtr {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []*fakeCtr
	for _, c := range e.ctrs {
		if c.running {
			out = append(out, c)
		}
	}
	return out
}

// image finds a stored image by name the way nerdctl's image commands do:
// the name as given, or as nerdctl reads it. Call with e.mu held.
func (e *fakeEngine) image(name string) (string, bool) {
	if c, ok := e.images[name]; ok {
		return name, c != ""
	}
	n := dockerName(name)
	return n, e.images[n] != ""
}

func (e *fakeEngine) ImageDigest(ctx context.Context, ref string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.image(ref); !ok {
		return "", fmt.Errorf("no image %s", ref)
	}
	return "sha256:" + strings.Repeat("a", 64), nil
}

func (e *fakeEngine) RemoveImage(ctx context.Context, ref string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	name, ok := e.image(ref)
	if !ok {
		return fmt.Errorf("nerdctl rmi: exit status 1: no such image: %s", ref)
	}
	delete(e.images, name)
	return nil
}

// TagImage resolves src by its normalized name only, as nerdctl tag does:
// a digest name nerdctl load stored ("import@sha256:...") or a short ID is
// not found.
func (e *fakeEngine) TagImage(ctx context.Context, src, ref string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	c := e.images[dockerName(src)]
	if c == "" {
		return fmt.Errorf("nerdctl tag: exit status 1: image %q: not found", dockerName(src))
	}
	e.images[dockerName(ref)] = c
	return nil
}

// CopyOut copies directories of the source an image was built from.
func (e *fakeEngine) CopyOut(ctx context.Context, image string, dirs []string, dest string) error {
	e.mu.Lock()
	tree := e.trees[dockerName(image)]
	e.mu.Unlock()
	if tree == "" {
		return fmt.Errorf("nerdctl run: exit status 1: image %q: not found", image)
	}
	for i, d := range dirs {
		if src := filepath.Join(tree, d); exists(src) {
			if err := os.CopyFS(filepath.Join(dest, strconv.Itoa(i)), os.DirFS(src)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *fakeEngine) ImageConfig(ctx context.Context, ref string) (string, string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.image(ref); !ok {
		return "", "", fmt.Errorf("no image %s", ref)
	}
	return "/app", "", nil
}

// SeedDirs copies directories of the source an image was built from.
func (e *fakeEngine) SeedDirs(ctx context.Context, image, user string, dirs []string, dest string) error {
	if err := e.CopyOut(ctx, image, dirs, dest); err != nil {
		return err
	}
	for i := range dirs {
		if err := os.MkdirAll(filepath.Join(dest, strconv.Itoa(i)), 0o755); err != nil {
			return err
		}
	}
	return nil
}

// LoadImage stores the names containerd's import (as nerdctl load drives
// it) takes from the tarball, replacing images of the same name, then
// settles them as the box does.
func (e *fakeEngine) LoadImage(ctx context.Context, tarball io.Reader, ref string, log io.Writer) error {
	names, content, err := containerdLoad(tarball)
	if err != nil {
		return fmt.Errorf("load image: %w", err)
	}
	e.mu.Lock()
	for _, n := range names {
		e.images[n] = content
	}
	e.mu.Unlock()
	has := func(name string) bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		_, ok := e.image(name)
		return ok
	}
	return settleLoad(names, ref, has, func(name string) error { return e.RemoveImage(ctx, name) }, log)
}

// fakeBuilder "builds" instantly; a FAIL file fails the build, a CRASH file
// produces an image whose containers die on start. Static apps use the real
// static path (no build script → files served as they are).
type fakeBuilder struct {
	eng    *fakeEngine
	static *boxBuilder
	builds atomic.Int32
	// warmBlock makes warm-up builds run until they are stopped, once.
	warmBlock atomic.Bool
	// failAll fails every build.
	failAll atomic.Bool
	mu      sync.Mutex
	envs    map[string]map[string]string // deploy ID → its build env
}

func (b *fakeBuilder) Build(ctx context.Context, req BuildRequest) (BuildResult, error) {
	b.builds.Add(1)
	b.mu.Lock()
	if b.envs == nil {
		b.envs = map[string]map[string]string{}
	}
	b.envs[req.Deploy.ID] = req.Env
	b.mu.Unlock()
	if b.failAll.Load() {
		return BuildResult{}, &BuildError{Msg: "the build failed", Hint: "read the log"}
	}
	if req.Deploy.ID == "warmup" && b.warmBlock.CompareAndSwap(true, false) {
		<-ctx.Done()
		return BuildResult{}, ctx.Err()
	}
	if req.Spec.Framework == manifest.FrameworkStatic {
		return b.static.Build(ctx, req)
	}
	if req.Prebuilt != "" {
		ref := imageRef(req.Deploy.Project, req.Deploy.App, req.Deploy.ID)
		return BuildResult{Image: ref}, loadImage(ctx, b.eng, req.Prebuilt, ref, req.Log)
	}
	if exists(filepath.Join(req.SrcDir, "FAIL")) {
		fmt.Fprintln(req.Log, "error: Cannot find module 'hono'")
		return BuildResult{}, &BuildError{Msg: "the build failed: buildctl exited with status 1", Hint: "read the log"}
	}
	ref := imageRef(req.Deploy.Project, req.Deploy.App, req.Deploy.ID)
	tree := filepath.Join(filepath.Dir(req.WorkDir), req.Deploy.ID+"-image")
	if req.SrcDir != "" {
		if err := os.CopyFS(tree, os.DirFS(req.SrcDir)); err != nil {
			return BuildResult{}, err
		}
	}
	b.eng.mu.Lock()
	b.eng.trees[ref] = tree
	b.eng.images[ref] = "built:" + ref
	if exists(filepath.Join(req.SrcDir, "CRASH")) {
		b.eng.crash[ref] = true
	}
	b.eng.mu.Unlock()
	fmt.Fprintln(req.Log, "#1 building", ref)
	return BuildResult{Image: ref, Digest: "sha256:" + strings.Repeat("b", 64)}, nil
}

// fakeEdge is a tiny reverse proxy standing in for Caddy: it serves whatever
// routes were loaded last, like the real edge does after a reload.
type fakeEdge struct {
	mu     sync.Mutex
	routes []edge.Route
	loads  int
}

func (e *fakeEdge) SetRoutes(rs []edge.Route) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.routes = rs
	e.loads++
	return nil
}

func (e *fakeEdge) find(host, path string) *edge.Route {
	e.mu.Lock()
	defer e.mu.Unlock()
	var best *edge.Route
	for i := range e.routes {
		r := &e.routes[i]
		if r.Host != host {
			continue
		}
		if r.PathPrefix != "" && path != r.PathPrefix && !strings.HasPrefix(path, r.PathPrefix+"/") {
			continue
		}
		if best == nil || len(r.PathPrefix) > len(best.PathPrefix) {
			best = r
		}
	}
	return best
}

var rr atomic.Int64

func (e *fakeEdge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rt := e.find(r.Host, r.URL.Path)
	if rt == nil {
		http.Error(w, "no route", 404)
		return
	}
	if rt.FileRoot != "" {
		http.FileServer(http.Dir(rt.FileRoot)).ServeHTTP(w, r)
		return
	}
	ups := rt.Upstreams
	if len(ups) == 0 {
		ups = []string{rt.Upstream}
	}
	up := ups[int(rr.Add(1))%len(ups)]
	(&httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.Out.URL.Scheme, pr.Out.URL.Host = "http", up
		pr.Out.Host = r.Host
	}}).ServeHTTP(w, r)
}

// ---- harness ----

type harness struct {
	t    *testing.T
	m    *Module
	r    *rt
	p    *platform.Platform
	eng  *fakeEngine
	bld  *fakeBuilder
	pgb  *fakeBranches
	edge *fakeEdge
	srv  *httptest.Server // the fake edge
	mf   *manifest.Manifest
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	db, err := state.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	sec, err := platform.OpenSecrets(db, dir)
	if err != nil {
		t.Fatal(err)
	}
	fe := &fakeEdge{}
	p := &platform.Platform{DB: db, Engine: change.NewEngine(db), Tokens: tokens.NewManager(db), Secrets: sec, Home: dir,
		Domain: "tiffin.localhost", PublicURL: "https://dashboard.tiffin.localhost:8443", Edge: fe,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	eng := newFakeEngine()
	opt := Options{DataDir: filepath.Join(dir, "runtime"), LogDir: filepath.Join(dir, "logs"), HealthTimeout: 3 * time.Second,
		Drain: 2 * time.Second, StopGrace: time.Second, PreviewIdle: time.Hour, KeepImages: 2, Engine: eng}
	bld := &fakeBuilder{eng: eng, static: &boxBuilder{eng: eng, staticDir: filepath.Join(opt.DataDir, "static")}}
	opt.Builder = bld
	pgb := &fakeBranches{made: map[string]postgres.PGBranch{}}
	opt.Branches = pgb
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	// The registered module instance: the platform finds routes and the API
	// finds operations through the registry.
	var m *Module
	for _, mod := range platform.Modules() {
		if rm, ok := mod.(*Module); ok {
			m = rm
		}
	}
	if err := m.start(ctx, p, opt); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, c := range eng.running() {
			c.srv.Close()
		}
	})
	h := &harness{t: t, m: m, r: m.r, p: p, eng: eng, bld: bld, pgb: pgb, edge: fe, srv: httptest.NewServer(fe)}
	t.Cleanup(h.srv.Close)
	h.mf = changetest.M("shop", func(mf *manifest.Manifest) {
		mf.Services = manifest.Services{}
		mf.Apps = map[string]manifest.App{
			"api":  {Path: ".", Framework: manifest.FrameworkHono, Role: manifest.RoleWeb, Routes: []string{"shop/api"}, Instances: 2, MemoryMB: 256, Healthcheck: "/healthz"},
			"site": {Path: ".", Framework: manifest.FrameworkStatic, Role: manifest.RoleWeb, Routes: []string{"shop"}, Instances: 1, MemoryMB: 128, Healthcheck: "/"},
			"jobs": {Path: ".", Framework: manifest.FrameworkBun, Role: manifest.RoleWorker, Instances: 1, MemoryMB: 128},
		}
		mf.Env = map[string]string{"GREETING": "hello"}
	})
	h.apply()
	return h
}

func (h *harness) apply() {
	h.t.Helper()
	changetest.Converge(h.t, h.p.Engine, h.mf)
	// The platform's reconciler is not running in tests: converge by hand.
	_, res, _ := h.p.DB.Load(context.Background(), "shop")
	for _, name := range []string{"api", "site", "jobs"} {
		var spec json.RawMessage
		if rs, ok := res["app/"+name]; ok {
			spec = rs.Spec
		}
		if err := h.m.Reconcile(context.Background(), h.p, "shop", "app/"+name, spec); err != nil {
			h.t.Fatalf("reconcile %s: %v", name, err)
		}
	}
}

// source writes files into a temp dir and returns it packed as a .tar.gz path.
func (h *harness) source(files map[string]string) string {
	h.t.Helper()
	dir := h.t.TempDir()
	for n, b := range files {
		p := filepath.Join(dir, n)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(b), 0o644); err != nil {
			h.t.Fatal(err)
		}
	}
	out := filepath.Join(h.t.TempDir(), "src.tgz")
	f, _ := os.Create(out)
	if _, err := srcpack.Pack(dir, f); err != nil {
		h.t.Fatal(err)
	}
	f.Close()
	return out
}

func (h *harness) deploy(app, preview string, files map[string]string) *Deploy {
	h.t.Helper()
	ctx := context.Background()
	spec, perr := h.r.checkDeployable(ctx, "shop", app, preview, false)
	if perr != nil {
		h.t.Fatal(perr)
	}
	d, err := h.r.newDeploy(ctx, "shop", app, preview, SourceUpload, "tok_test", spec)
	if err != nil {
		h.t.Fatal(err)
	}
	src := filepath.Join(h.r.workDir(d), "source.tgz")
	if err := os.Rename(h.source(files), src); err != nil {
		h.t.Fatal(err)
	}
	h.r.start(d, src, SourceUpload)
	return h.wait(app, d.ID)
}

func (h *harness) wait(app, id string) *Deploy {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		d, err := h.r.st.getDeploy(context.Background(), "shop", app, id)
		if err != nil {
			h.t.Fatal(err)
		}
		if d.Terminal() {
			return d
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatalf("deploy %s did not finish", id)
	return nil
}

func (h *harness) get(host, path string) (int, string) {
	h.t.Helper()
	req, _ := http.NewRequest("GET", h.srv.URL+path, nil)
	req.Host = host
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func (h *harness) state(app, preview string) *AppState {
	st, err := h.r.st.getState(context.Background(), "shop", app, preview)
	if err != nil {
		h.t.Fatal(err)
	}
	return st
}

// ---- tests ----

func TestDeployPromoteAndRoutes(t *testing.T) {
	h := newHarness(t)
	d := h.deploy("api", "", map[string]string{"index.ts": "v1"})
	if d.Status != StatusLive {
		t.Fatalf("status %s: %s", d.Status, d.Error)
	}
	if d.URL != "https://shop.tiffin.localhost:8443/api" {
		t.Errorf("url %q", d.URL)
	}
	if d.Digest == "" || d.BuiltAt == nil || d.LiveAt == nil || d.TotalSecs < 0 {
		t.Errorf("deploy record incomplete: %+v", d)
	}
	st := h.state("api", "")
	if len(st.Instances) != 2 || st.Live != d.ID {
		t.Fatalf("state %+v", st)
	}
	code, body := h.get("shop.tiffin.localhost", "/api/healthz")
	if code != 200 || !strings.Contains(body, strings.ToLower(d.ID)) || !strings.Contains(body, "greeting=hello") {
		t.Fatalf("edge: %d %s", code, body)
	}
	// Workers get no routes.
	w := h.deploy("jobs", "", map[string]string{"worker.ts": "w"})
	if w.Status != StatusLive || w.URL != "" {
		t.Fatalf("worker %+v", w)
	}
	for _, r := range h.edge.routes {
		if strings.Contains(r.Host, "jobs") {
			t.Fatalf("worker got a route: %+v", r)
		}
	}
	// The build log tells the story.
	text, _ := h.r.readBuildLog(d, 0, 1<<20)
	for _, want := range []string{"==> source:", "#1 building", "==> starting 2 instance(s)", "==> live in"} {
		if !strings.Contains(string(text), want) {
			t.Errorf("build log lacks %q:\n%s", want, text)
		}
	}
}

// Browsers watch jobs and runs at /_tiffin/runs/... on every app host: the
// switchboard keeps that path for the queue module, never the app, also on
// hosts where apps only serve paths and on static sites.
func TestLivePathNeverReachesTheApp(t *testing.T) {
	h := newHarness(t)
	h.deploy("api", "", map[string]string{"index.ts": "v1"}) // served at shop.tiffin.localhost/api
	code, body := h.get("shop.tiffin.localhost", "/_tiffin/runs/run_1/events")
	// No queue module is linked into this test, so the switchboard says so.
	if code != 404 || !strings.Contains(body, "queue module") {
		t.Fatalf("live path on a path-only host: %d %s", code, body)
	}
	h2 := newHarness(t)
	h2.deploy("site", "", map[string]string{"index.html": "<h1>hi</h1>"})
	if code, body := h2.get("shop.tiffin.localhost", "/_tiffin/runs/run_1/events"); code != 404 || !strings.Contains(body, "queue module") {
		t.Fatalf("live path on a static site: %d %s", code, body)
	}
	if code, body := h2.get("shop.tiffin.localhost", "/"); code != 200 || !strings.Contains(body, "hi") {
		t.Fatalf("static site: %d %s", code, body)
	}
}

func TestZeroDowntimeRedeployAndRollback(t *testing.T) {
	h := newHarness(t)
	v1 := h.deploy("api", "", map[string]string{"index.ts": "v1"})
	stop := make(chan struct{})
	var ok, failed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if code, _ := h.get("shop.tiffin.localhost", "/api/x"); code == 200 {
					ok.Add(1)
				} else {
					failed.Add(1)
				}
			}
		}()
	}
	time.Sleep(100 * time.Millisecond)
	h.edge.mu.Lock()
	loads := h.edge.loads
	h.edge.mu.Unlock()
	v2 := h.deploy("api", "", map[string]string{"index.ts": "v2"})
	time.Sleep(400 * time.Millisecond) // past the drain: old instances are gone
	if v2.Status != StatusLive {
		t.Fatalf("v2 %s %s", v2.Status, v2.Error)
	}
	_, body := h.get("shop.tiffin.localhost", "/api/")
	if !strings.Contains(body, strings.ToLower(v2.ID)) {
		t.Fatalf("v2 not serving: %s", body)
	}
	if old, _ := h.r.st.getDeploy(context.Background(), "shop", "api", v1.ID); old.Status != StatusSuperseded {
		t.Fatalf("v1 is %s, want superseded", old.Status)
	}
	// Roll back under the same load.
	if _, err := h.r.rollback(context.Background(), "shop", "api", v1.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	close(stop)
	wg.Wait()
	_, body = h.get("shop.tiffin.localhost", "/api/")
	if !strings.Contains(body, strings.ToLower(v1.ID)) {
		t.Fatalf("rollback: v1 not serving: %s", body)
	}
	if cur, _ := h.r.st.getDeploy(context.Background(), "shop", "api", v2.ID); cur.Status != StatusRolledBack {
		t.Fatalf("v2 is %s, want rolled_back", cur.Status)
	}
	h.edge.mu.Lock()
	if h.edge.loads != loads {
		t.Errorf("the edge reloaded %d times for a redeploy and a rollback; instance switches must not reload it", h.edge.loads-loads)
	}
	h.edge.mu.Unlock()
	if failed.Load() != 0 || ok.Load() < 50 {
		t.Fatalf("requests under load: %d ok, %d failed", ok.Load(), failed.Load())
	}
	t.Logf("%d requests, 0 failed, across a deploy and a rollback", ok.Load())
	if n := len(h.eng.running()); n != 2 {
		t.Fatalf("%d containers running, want 2 (old ones must be stopped)", n)
	}
	// Rolling back to the live deploy is a no-op; to a failed one is refused.
	if d, err := h.r.rollback(context.Background(), "shop", "api", v1.ID); err != nil || d.ID != v1.ID {
		t.Fatalf("rollback to live: %v", err)
	}
}

func TestFailedDeploysKeepTheOldVersion(t *testing.T) {
	h := newHarness(t)
	v1 := h.deploy("api", "", map[string]string{"index.ts": "v1"})
	bad := h.deploy("api", "", map[string]string{"index.ts": "v2", "FAIL": ""})
	if bad.Status != StatusFailed || !strings.Contains(bad.Error, "build failed") || bad.Hint == "" {
		t.Fatalf("build failure: %+v", bad)
	}
	crash := h.deploy("api", "", map[string]string{"index.ts": "v3", "CRASH": ""})
	if crash.Status != StatusFailed || !strings.Contains(crash.Error, "boom: missing DATABASE_URL") {
		t.Fatalf("crash: %+v", crash)
	}
	if st := h.state("api", ""); st.Live != v1.ID || len(st.Instances) != 2 {
		t.Fatalf("v1 must keep serving: %+v", st)
	}
	if _, body := h.get("shop.tiffin.localhost", "/api/"); !strings.Contains(body, strings.ToLower(v1.ID)) {
		t.Fatalf("serving %s", body)
	}
	if _, err := h.r.rollback(context.Background(), "shop", "api", bad.ID); err == nil {
		t.Fatal("rollback to a failed deploy must be refused")
	}
	text, _ := h.r.readBuildLog(bad, 0, 1<<20)
	if !strings.Contains(string(text), "Cannot find module") || !strings.Contains(string(text), "FAILED") {
		t.Fatalf("build log: %s", text)
	}
}

func (h *harness) check(name string) *platform.Check {
	for _, c := range h.r.checks(context.Background()) {
		if c.Name == name {
			return &c
		}
	}
	return nil
}

// A deploy whose server dies on boot (a syntax error) must not wedge the app,
// even when its containers cannot be removed at once (as on a box, where
// nerdctl rm failed): the next deploy, a restart and a rollback work, status
// names the leftovers and the sweep removes them once they go.
func TestCrashOnBootDoesNotWedgeTheApp(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	v1 := h.deploy("api", "", map[string]string{"index.ts": "v1"})
	h.eng.setStuck(true)
	crash := h.deploy("api", "", map[string]string{"index.ts": "v2", "CRASH": ""})
	if crash.Status != StatusFailed || !strings.Contains(crash.Error, "exited with code 1") || strings.Contains(crash.Hint, "$PORT") {
		t.Fatalf("crash: %+v", crash)
	}
	// Fresh leftovers are named but not a failure: the sweep usually takes them.
	c := h.check("containers")
	if c == nil || !c.OK || !strings.Contains(c.Detail, "removing 2 leftover container(s)") {
		t.Fatalf("status must name the leftovers: %+v", c)
	}
	h.r.mu.Lock()
	for n := range h.r.orphanAt {
		h.r.orphanAt[n] = time.Now().Add(-11 * time.Minute)
	}
	h.r.mu.Unlock()
	if c = h.check("containers"); c == nil || c.OK || !strings.Contains(c.Detail, "2 container(s) no app runs, for over 10 minutes") {
		t.Fatalf("leftovers that stay must fail status: %+v", c)
	}
	v3 := h.deploy("api", "", map[string]string{"index.ts": "v3"})
	if v3.Status != StatusLive {
		t.Fatalf("deploy after a crash: %s %s", v3.Status, v3.Error)
	}
	// A box that ran older code did not keep the crashed start's serials:
	// its next names are the leftovers' (and the live instances').
	st := h.state("api", "")
	st.Serial = 2
	if err := h.r.st.putState(ctx, st); err != nil {
		t.Fatal(err)
	}
	if _, err := h.r.restart(ctx, "shop", "api", ""); err != nil {
		t.Fatalf("restart after a crash: %v", err)
	}
	// nerdctl's name store can hold a name with no container behind it.
	h.eng.leak(containerName("shop", "api", "", h.state("api", "").Serial+1))
	if _, err := h.r.rollback(ctx, "shop", "api", v1.ID); err != nil {
		t.Fatalf("rollback after a crash: %v", err)
	}
	time.Sleep(300 * time.Millisecond) // the replaced instances drain
	if _, body := h.get("shop.tiffin.localhost", "/api/"); !strings.Contains(body, strings.ToLower(v1.ID)) {
		t.Fatalf("rollback: v1 not serving: %s", body)
	}
	h.eng.setStuck(false)
	h.r.removeOrphans(ctx)
	if c := h.check("containers"); c != nil {
		t.Fatalf("leftovers survived the sweep: %+v", c)
	}
	for _, in := range h.state("api", "").Instances {
		if c, _ := h.eng.Inspect(ctx, in.Name); c == nil || !c.Running {
			t.Fatalf("live instance %s was removed", in.Name)
		}
	}
	// An engine that will not start a container is a 503 with a hint, not a bare 500.
	if p, ok := h.r.toProblem(&startError{"name-store error"}, "app api").(*api.Problem); !ok || p.Status != 503 || p.Hint == "" {
		t.Fatalf("start error: %+v", p)
	}
}

// A deploy waiting for the build slot says whose build it waits for.
func TestQueuedDeploySaysWhatItWaitsFor(t *testing.T) {
	h := newHarness(t)
	if err := h.r.acquireBuild(context.Background(), "shop/site", io.Discard); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		h.r.releaseBuild()
	}()
	d := h.deploy("api", "", map[string]string{"index.ts": "v1"})
	text, _ := h.r.readBuildLog(d, 0, 1<<20)
	if d.Status != StatusLive || !strings.Contains(string(text), "waiting for another build (shop/site) to finish") {
		t.Fatalf("%s: %s", d.Status, text)
	}
}

// Cleaning up a failed start happens even when the request that started it is gone.
func TestRemoveInstancesOutlivesItsContext(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.eng.crash["img"] = true
	spec := RunSpec{Name: "tf.shop.api.prod.90", Image: "img", LogPath: filepath.Join(t.TempDir(), "log"),
		Labels: map[string]string{"tiffin.project": "shop", "tiffin.app": "api"}}
	if err := h.eng.Run(ctx, spec); err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	h.r.removeInstances(cctx, []Instance{{Name: spec.Name}})
	if c, _ := h.eng.Inspect(ctx, spec.Name); c != nil {
		t.Fatal("a cancelled context left the container behind")
	}
}

// Destroying a project removes every container labelled for it, including
// one a crashed start left behind.
func TestDestroyRemovesDeadContainers(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.deploy("api", "", map[string]string{"index.ts": "v1"})
	h.eng.setStuck(true)
	h.deploy("api", "", map[string]string{"index.ts": "v2", "CRASH": ""})
	h.eng.setStuck(false)
	if cs, _ := h.eng.List(ctx); len(cs) != 4 {
		t.Fatalf("want 2 live and 2 dead containers, have %d", len(cs))
	}
	for _, app := range []string{"api", "site", "jobs"} {
		if err := h.m.Reconcile(ctx, h.p, "shop", "app/"+app, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.m.ProjectDeleted(ctx, h.p, "shop"); err != nil {
		t.Fatal(err)
	}
	if cs, _ := h.eng.List(ctx); len(cs) != 0 {
		t.Fatalf("containers survived the destroy: %+v", cs)
	}
	if c := h.check("runtime"); c == nil || !c.OK || !strings.Contains(c.Detail, "0 container(s)") {
		t.Fatalf("runtime check: %+v", c)
	}
}

func TestEnvChangeRestartsAndDeleteStops(t *testing.T) {
	h := newHarness(t)
	d := h.deploy("api", "", map[string]string{"index.ts": "v1"})
	before := h.state("api", "")
	wentLive := func() time.Time {
		got, err := h.r.st.getDeploy(context.Background(), "shop", "api", d.ID)
		if err != nil || got.LiveAt == nil {
			t.Fatalf("deploy %v: %+v", err, got)
		}
		return *got.LiveAt
	}
	liveAt := wentLive()
	// Reconcile with nothing changed is a no-op.
	h.apply()
	if st := h.state("api", ""); st.Instances[0].Name != before.Instances[0].Name {
		t.Fatal("reconcile without changes restarted the app")
	}
	// A secret changes the env: the app restarts with it.
	if _, err := h.p.SetSecrets(context.Background(), "shop", map[string]string{"GREETING": "bonjour"}, "greet in French"); err != nil {
		t.Fatal(err)
	}
	h.apply()
	after := h.state("api", "")
	if after.Hash == before.Hash || after.Instances[0].Name == before.Instances[0].Name {
		t.Fatalf("secret change did not restart: %+v", after)
	}
	if _, body := h.get("shop.tiffin.localhost", "/api/"); !strings.Contains(body, "greeting=bonjour") {
		t.Fatalf("new env not served: %s", body)
	}
	// A restart is not a new go-live: the version keeps the time it went live.
	if !wentLive().Equal(liveAt) {
		t.Fatalf("restart moved liveAt from %v to %v", liveAt, wentLive())
	}
	// Instance count change.
	a := h.mf.Apps["api"]
	a.Instances = 3
	h.mf.Apps["api"] = a
	h.apply()
	if n := len(h.state("api", "").Instances); n != 3 {
		t.Fatalf("instances %d, want 3", n)
	}
	// Deleting the app stops it but keeps the deploy for undo.
	delete(h.mf.Apps, "api")
	h.apply()
	time.Sleep(300 * time.Millisecond)
	st := h.state("api", "")
	if !st.Stopped || len(st.Instances) != 0 || len(h.eng.running()) != 0 {
		t.Fatalf("delete did not stop: %+v (%d running)", st, len(h.eng.running()))
	}
	if code, _ := h.get("shop.tiffin.localhost", "/api/"); code != 404 {
		t.Fatalf("deleted app still routed: %d", code)
	}
	live, _ := h.r.st.getDeploy(context.Background(), "shop", "api", st.Live)
	if live.Status != StatusStopped {
		t.Fatalf("deploy %s", live.Status)
	}
	// Bring it back (as an undo would).
	h.mf.Apps["api"] = a
	h.apply()
	if code, body := h.get("shop.tiffin.localhost", "/api/"); code != 200 || !strings.Contains(body, "bonjour") {
		t.Fatalf("restore: %d %s", code, body)
	}
}

func TestStaticSite(t *testing.T) {
	h := newHarness(t)
	d := h.deploy("site", "", map[string]string{"public/index.html": "<h1>hi</h1>", "README.md": "x"})
	if d.Status != StatusLive || d.StaticRoot == "" || d.Image != "" {
		t.Fatalf("static: %+v", d)
	}
	code, body := h.get("shop.tiffin.localhost", "/")
	if code != 200 || !strings.Contains(body, "<h1>hi</h1>") {
		t.Fatalf("static serve: %d %s", code, body)
	}
	if code, _ := h.get("shop.tiffin.localhost", "/README.md"); code != 404 {
		t.Fatalf("only the served root is public, got %d", code)
	}
	if n := len(h.eng.running()); n != 0 {
		t.Fatalf("static sites run no containers, got %d", n)
	}
	d2 := h.deploy("site", "", map[string]string{"index.html": "v2"})
	if _, body := h.get("shop.tiffin.localhost", "/"); body != "v2" {
		t.Fatalf("redeploy: %s", body)
	}
	if _, err := h.r.rollback(context.Background(), "shop", "site", d.ID); err != nil {
		t.Fatal(err)
	}
	if _, body := h.get("shop.tiffin.localhost", "/"); !strings.Contains(body, "hi") {
		t.Fatalf("static rollback: %s", body)
	}
	_ = d2
	bad := h.deploy("site", "", map[string]string{"notes.txt": "no html"})
	if bad.Status != StatusFailed || !strings.Contains(bad.Hint, "index.html") {
		t.Fatalf("no index: %+v", bad)
	}
}

func TestPreviewSleepsAndWakes(t *testing.T) {
	h := newHarness(t)
	prod := h.deploy("api", "", map[string]string{"index.ts": "prod"})
	pv := h.deploy("api", "feat-x", map[string]string{"index.ts": "preview"})
	// api serves a path (shop/api), so its previews use <project>-<app>.
	if pv.Status != StatusLive || pv.URL != "https://feat-x--shop-api.tiffin.localhost:8443" {
		t.Fatalf("preview %+v", pv)
	}
	if st := h.state("api", ""); st.Live != prod.ID {
		t.Fatal("a preview must not touch production")
	}
	host := "feat-x--shop-api.tiffin.localhost"
	code, body := h.get(host, "/")
	if code != 200 || !strings.Contains(body, "preview=feat-x") {
		t.Fatalf("preview via activator: %d %s", code, body)
	}
	// Idle → asleep.
	h.r.opt.PreviewIdle = time.Millisecond
	time.Sleep(5 * time.Millisecond)
	h.r.sleepIdlePreviews(context.Background())
	st := h.state("api", "feat-x")
	if !st.Sleeping || len(st.Instances) != 0 {
		t.Fatalf("not asleep: %+v", st)
	}
	runs := h.eng.runs
	// The next request wakes it.
	h.r.opt.PreviewIdle = time.Hour
	code, body = h.get(host, "/hello")
	if code != 200 || !strings.Contains(body, strings.ToLower(pv.ID)) {
		t.Fatalf("wake: %d %s", code, body)
	}
	if h.eng.runs != runs+1 || h.state("api", "feat-x").Sleeping {
		t.Fatal("wake did not start one instance")
	}
	// Previews are listed and deletable.
	rt, err := h.r.appRuntime(context.Background(), "shop", "api")
	if err != nil || len(rt.Previews) != 1 || rt.Prod.Live.ID != prod.ID {
		t.Fatalf("runtime %+v %v", rt, err)
	}
	if err := h.r.deletePreview(context.Background(), "shop", "api", "feat-x"); err != nil {
		t.Fatal(err)
	}
	if code, _ := h.get(host, "/"); code != 404 {
		t.Fatalf("deleted preview still served: %d", code)
	}
	if err := h.r.deletePreview(context.Background(), "shop", "api", "feat-x"); err == nil {
		t.Fatal("deleting twice must be not found")
	}
}

func TestLogs(t *testing.T) {
	h := newHarness(t)
	d := h.deploy("api", "", map[string]string{"index.ts": "v1"})
	f := h.r.follow("shop", "api", "", "")
	for i := 0; i < 3; i++ {
		h.get("shop.tiffin.localhost", fmt.Sprintf("/api/req-%d", i))
	}
	lines := h.r.readLogs("shop", "api", "", "", time.Time{}, 0)
	var reqs int
	for _, l := range lines {
		if strings.HasPrefix(l.Text, "GET /api/req-") {
			reqs++
		}
		if l.Deploy != d.ID || !strings.HasPrefix(l.Instance, "tf.shop.api.prod.") {
			t.Fatalf("line attribution: %+v", l)
		}
	}
	if reqs != 3 {
		t.Fatalf("want 3 request lines, got %d in %+v", reqs, lines)
	}
	if got := h.r.readLogs("shop", "api", "", "", time.Time{}, 2); len(got) != 2 || got[1].Text != lines[len(lines)-1].Text {
		t.Fatalf("limit keeps the newest lines: %+v", got)
	}
	if got := h.r.readLogs("shop", "api", "", "dep_OTHER", time.Time{}, 0); len(got) != 0 {
		t.Fatal("deploy filter")
	}
	polled := f.poll()
	if len(polled) < 3 {
		t.Fatalf("follow saw %d lines", len(polled))
	}
	if again := f.poll(); len(again) != 0 {
		t.Fatalf("follow repeated lines: %+v", again)
	}
	since, err := parseSince("5m", time.Now())
	if err != nil || time.Since(since) < 4*time.Minute {
		t.Fatal("parseSince duration")
	}
	if _, err := parseSince("yesterday", time.Now()); err == nil {
		t.Fatal("parseSince garbage")
	}
}

func TestRouteSplittingAndConflicts(t *testing.T) {
	h := newHarness(t)
	cases := map[string][2]string{
		"shop":            {"shop.tiffin.localhost", ""},
		"shop/api":        {"shop.tiffin.localhost", "/api"},
		"shop/api/":       {"shop.tiffin.localhost", "/api"},
		"Example.com":     {"example.com", ""},
		"example.com/v1/": {"example.com", "/v1"},
	}
	for in, want := range cases {
		host, prefix := h.r.splitRoute(in)
		if host != want[0] || prefix != want[1] {
			t.Errorf("splitRoute(%q) = %q %q", in, host, prefix)
		}
	}
	// Two apps claiming one route: the first wins, a check reports it.
	a := h.mf.Apps["api"]
	a.Routes = []string{"shop"}
	h.mf.Apps["api"] = a
	h.apply()
	h.deploy("api", "", map[string]string{"index.ts": "v1"})
	h.deploy("site", "", map[string]string{"index.html": "x"})
	_, conflicts := h.r.routes(context.Background())
	if len(conflicts) != 1 || conflicts[0].Key != "shop.tiffin.localhost" {
		t.Fatalf("conflicts %+v", conflicts)
	}
	var bad bool
	for _, c := range h.r.checks(context.Background()) {
		if c.Name == "routes" && !c.OK {
			bad = true
		}
	}
	if !bad {
		t.Fatal("route conflict not reported in checks")
	}
}

// TestAppsDomainHosts: with a separate apps domain, app routes, previews
// and the clash hint use it; the dashboard keeps the box domain.
func TestAppsDomainHosts(t *testing.T) {
	h := newHarness(t)
	h.p.Reach.AppsDomain = "example.app"
	if host, prefix := h.r.splitRoute("shop/api"); host != "shop.example.app" || prefix != "/api" {
		t.Errorf("splitRoute = %s %s", host, prefix)
	}
	if host, _ := h.r.splitRoute("example.com"); host != "example.com" {
		t.Errorf("custom route = %s", host)
	}
	web := &manifest.App{Routes: []string{"news"}}
	if u := h.r.deployURL(&Deploy{Project: "news", App: "web", Preview: "pr-7"}, web); u != "https://pr-7--news.example.app:8443" {
		t.Errorf("preview URL = %s", u)
	}
	if u := h.r.deployURL(&Deploy{Project: "news", App: "web"}, web); u != "https://news.example.app:8443" {
		t.Errorf("production URL = %s", u)
	}
	err := h.m.CheckPlan(context.Background(), h.p, "blog", map[string]change.Resource{"app/shop": {Address: "app/shop", Spec: json.RawMessage(`{"routes":["shop"]}`)}})
	var prob *api.Problem
	if !errors.As(err, &prob) || !strings.Contains(prob.Hint, "<project>.example.app") || !strings.Contains(prob.Hint, "blog-shop.example.app") {
		t.Errorf("clash hint: %v", err)
	}
	if h.p.DashboardHost() != "dashboard.tiffin.localhost" {
		t.Errorf("dashboard = %s", h.p.DashboardHost())
	}
}

// TestPreviewHost: previews are named after the app's own address, and a
// name too long for DNS is cut and made unique with a hash.
func TestPreviewHost(t *testing.T) {
	for _, c := range []struct {
		routes []string
		want   string
	}{
		{[]string{"shop"}, "pr-7--shop.x.test"},
		{[]string{"shop-docs"}, "pr-7--shop-docs.x.test"},
		{[]string{"example.com", "api"}, "pr-7--api.x.test"},
		{[]string{"example.com", "shop/docs"}, "pr-7--shop-docs.x.test"},
	} {
		if got := previewHost("pr-7", "shop", "docs", &manifest.App{Routes: c.routes}, "x.test"); got != c.want {
			t.Errorf("%v: %s, want %s", c.routes, got, c.want)
		}
	}
	long := strings.Repeat("p", 40)
	a := previewHost(strings.Repeat("b", 30), long, "one", &manifest.App{}, "x.test")
	b := previewHost(strings.Repeat("b", 30), long, "two", &manifest.App{}, "x.test")
	if label, _, _ := strings.Cut(a, "."); len(label) > 63 || a == b || !strings.HasPrefix(a, strings.Repeat("b", 30)+"--ppp") {
		t.Errorf("long names: %s %s", a, b)
	}
}

func TestGCKeepsRollbackTargets(t *testing.T) {
	h := newHarness(t)
	var ds []*Deploy
	for i := 0; i < 5; i++ {
		ds = append(ds, h.deploy("api", "", map[string]string{"index.ts": fmt.Sprint(i)}))
	}
	// KeepImages is 2 in tests: deploys 0..1 lose their images, 2..3 keep them.
	for i, d := range ds {
		cur, _ := h.r.st.getDeploy(context.Background(), "shop", "api", d.ID)
		keep := i >= 2
		if (cur.Image != "") != keep {
			t.Errorf("deploy %d (%s): image %q, keep=%v", i, cur.Status, cur.Image, keep)
		}
	}
	if _, err := h.r.rollback(context.Background(), "shop", "api", ds[0].ID); err == nil {
		t.Fatal("rollback to a cleaned-up deploy must be refused")
	}
}

// A preview keeps only its live build; deleting it removes that one too.
func TestPreviewKeepsOnlyItsLatestBuild(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	image := func(id string) string {
		d, _ := h.r.st.getDeploy(ctx, "shop", "api", id)
		return d.Image
	}
	pv1 := h.deploy("api", "feat-x", map[string]string{"index.ts": "1"})
	pv2 := h.deploy("api", "feat-x", map[string]string{"index.ts": "2"})
	if image(pv1.ID) != "" || image(pv2.ID) == "" {
		t.Fatalf("images: earlier build %q, live build %q", image(pv1.ID), image(pv2.ID))
	}
	if _, err := h.r.rollback(ctx, "shop", "api", pv1.ID); err == nil || !strings.Contains(err.Error(), "previews keep only their latest build") {
		t.Fatalf("rollback of a preview: %v", err)
	}
	if err := h.r.deletePreview(ctx, "shop", "api", "feat-x"); err != nil {
		t.Fatal(err)
	}
	if image(pv2.ID) != "" || h.imageCount() != 0 {
		t.Fatalf("a deleted preview keeps its image: %q, %d images", image(pv2.ID), h.imageCount())
	}
}

func (h *harness) imageCount() int {
	h.eng.mu.Lock()
	defer h.eng.mu.Unlock()
	n := 0
	for _, c := range h.eng.images {
		if c != "" {
			n++
		}
	}
	return n
}

// gc never removes the image of a release that workflow runs are pinned to,
// however many deploys come after it.
func TestGCKeepsPinnedReleases(t *testing.T) {
	h := newHarness(t)
	defer pins.set()
	ctx := context.Background()
	v1 := h.deploy("api", "", map[string]string{"index.ts": "v1"})
	pins.set(v1.ID)
	for i := range 4 {
		h.deploy("api", "", map[string]string{"index.ts": fmt.Sprint(i)})
	}
	if d, _ := h.r.st.getDeploy(ctx, "shop", "api", v1.ID); d.Image == "" {
		t.Fatal("the image of a pinned release was removed")
	}
	pins.set()
	h.r.reapDrained(ctx)
	h.deploy("api", "", map[string]string{"index.ts": "last"})
	if d, _ := h.r.st.getDeploy(ctx, "shop", "api", v1.ID); d.Image != "" {
		t.Fatal("an unpinned old release keeps its image")
	}
}

// Previews nobody requested or deployed to for PreviewExpire are deleted.
func TestUnusedPreviewsAreDeleted(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.deploy("api", "", map[string]string{"index.ts": "prod"})
	pv := h.deploy("api", "feat-x", map[string]string{"index.ts": "preview"})
	h.r.opt.PreviewExpire = time.Hour
	h.r.sleep(ctx, "shop", "api", "feat-x")
	h.r.expirePreviews(ctx)
	if h.state("api", "feat-x").Live != pv.ID {
		t.Fatal("a preview used within PreviewExpire was deleted")
	}
	// The clock is the sleeping state's last write, so it outlives a restart
	// (the in-memory last request is gone).
	h.r.mu.Lock()
	h.r.lastSeen = map[string]time.Time{}
	h.r.mu.Unlock()
	h.r.opt.PreviewExpire = 50 * time.Millisecond
	time.Sleep(60 * time.Millisecond)
	h.r.expirePreviews(ctx)
	if st := h.state("api", "feat-x"); st.Live != "" {
		t.Fatalf("unused preview kept: %+v", st)
	}
	if code, _ := h.get("feat-x--shop-api.tiffin.localhost", "/"); code != 404 {
		t.Fatalf("expired preview still served: %d", code)
	}
	if d, _ := h.r.st.getDeploy(ctx, "shop", "api", pv.ID); d.Status != StatusStopped || d.Image != "" {
		t.Fatalf("expired preview's deploy: %s, image %q", d.Status, d.Image)
	}
	if st := h.state("api", ""); st.Live == "" || len(st.Instances) != 2 {
		t.Fatalf("production touched: %+v", st)
	}
}

func TestRecoverMarksInterruptedDeploysFailed(t *testing.T) {
	h := newHarness(t)
	spec, _ := h.r.appSpec(context.Background(), "shop", "api")
	d, _ := h.r.newDeploy(context.Background(), "shop", "api", "", SourceUpload, "tok", spec)
	d.Status = StatusBuilding
	h.r.st.putDeploy(context.Background(), d)
	if err := h.r.recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := h.r.st.getDeploy(context.Background(), "shop", "api", d.ID)
	if got.Status != StatusFailed || !strings.Contains(got.Error, "interrupted") {
		t.Fatalf("%+v", got)
	}
}

// A container a crash left outside the saved states (started before its
// deploy was recorded) is removed on start; recorded instances stay.
func TestRecoverRemovesOrphanedContainers(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if d := h.deploy("api", "", map[string]string{"index.ts": "v1"}); d.Status != StatusLive {
		t.Fatalf("deploy: %s %s", d.Status, d.Error)
	}
	orphan := RunSpec{Name: "shop-api-99-0", Image: "img", Port: 0, LogPath: filepath.Join(t.TempDir(), "log"),
		Labels: map[string]string{"tiffin.project": "shop", "tiffin.app": "api"}}
	h.eng.crash["img"] = true // never listens: nothing to serve
	if err := h.eng.Run(ctx, orphan); err != nil {
		t.Fatal(err)
	}
	if err := h.r.recover(ctx); err != nil {
		t.Fatal(err)
	}
	if c, _ := h.eng.Inspect(ctx, orphan.Name); c != nil {
		t.Fatal("orphaned container survived the restart")
	}
	for _, in := range h.state("api", "").Instances {
		if c, _ := h.eng.Inspect(ctx, in.Name); c == nil || !c.Running {
			t.Fatalf("live instance %s was removed", in.Name)
		}
	}
}

// TestAPI drives the operations over HTTP, including the streaming upload.
func TestAPI(t *testing.T) {
	h := newHarness(t)
	owner, _, err := h.p.Tokens.Bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a := api.New(api.Deps{DB: h.p.DB, Engine: h.p.Engine, Tokens: h.p.Tokens, Platform: h.p})
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	key := "" // Idempotency-Key for the next call
	call := func(method, path, ct string, body io.Reader, accept string) (int, []byte) {
		req, _ := http.NewRequest(method, srv.URL+path, body)
		req.Header.Set("Authorization", "Bearer "+owner)
		if key != "" {
			req.Header.Set(api.IdempotencyHeader, key)
			key = ""
		}
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, b
	}
	src, _ := os.ReadFile(h.source(map[string]string{"index.ts": "upload"}))
	key = "deploy-key-000000001"
	code, body := call("POST", "/v1/projects/shop/apps/api/deploys", "application/gzip", bytes.NewReader(src), "")
	if code != 202 {
		t.Fatalf("upload: %d %s", code, body)
	}
	var d Deploy
	json.Unmarshal(body, &d)
	// Sent again with its key (the connection dropped before the answer):
	// the same deploy, not a second one (the list below has two, not three).
	key = "deploy-key-000000001"
	if code, again := call("POST", "/v1/projects/shop/apps/api/deploys", "application/gzip", strings.NewReader("cut off"), ""); code != 202 || !strings.Contains(string(again), `"id":"`+d.ID+`"`) {
		t.Fatalf("upload sent again: %d %s, want deploy %s", code, again, d.ID)
	}
	if d.Status != StatusQueued || d.Source != SourceUpload || d.SourceBytes == 0 {
		t.Fatalf("queued deploy: %s", body)
	}
	// Follow the build log until done (SSE).
	code, body = call("GET", "/v1/projects/shop/apps/api/deploys/"+d.ID+"/build-log?follow=true", "", nil, "text/event-stream")
	if code != 200 || !strings.Contains(string(body), "event: done") || !strings.Contains(string(body), `"status":"live"`) {
		t.Fatalf("follow: %d %s", code, body)
	}
	code, body = call("GET", "/v1/projects/shop/apps/api/deploys/"+d.ID, "", nil, "")
	if code != 200 || !strings.Contains(string(body), `"status":"live"`) {
		t.Fatalf("get: %d %s", code, body)
	}
	// Inline files (the MCP path).
	code, body = call("POST", "/v1/projects/shop/apps/api/deploys?preview=pr-1", "application/json", strings.NewReader(`{"files":{"index.ts":"inline"}}`), "")
	if code != 202 || !strings.Contains(string(body), `"source":"files"`) {
		t.Fatalf("inline: %d %s", code, body)
	}
	json.Unmarshal(body, &d)
	h.wait("api", d.ID)
	code, body = call("POST", "/v1/projects/shop/apps/api/deploys", "application/json", strings.NewReader(`{"files":{"../evil":"x"}}`), "")
	if code != 422 {
		t.Fatalf("escape: %d %s", code, body)
	}
	// Unknown app: 404 with a hint.
	code, body = call("POST", "/v1/projects/shop/apps/nope/deploys", "application/gzip", bytes.NewReader(src), "")
	if code != 404 || !strings.Contains(string(body), "tiffin apply") {
		t.Fatalf("unknown app: %d %s", code, body)
	}
	code, body = call("GET", "/v1/projects/shop/apps/api/deploys?all=true", "", nil, "")
	var list DeployList
	json.Unmarshal(body, &list)
	if code != 200 || len(list.Deploys) != 2 {
		t.Fatalf("list: %d %s", code, body)
	}
	code, body = call("GET", "/v1/projects/shop/apps/api/runtime", "", nil, "")
	if code != 200 || !strings.Contains(string(body), `"previews":[{"preview":"pr-1"`) {
		t.Fatalf("runtime: %d %s", code, body)
	}
	h.get("shop.tiffin.localhost", "/api/logged")
	code, body = call("GET", "/v1/projects/shop/apps/api/logs?limit=50", "", nil, "")
	if code != 200 || !strings.Contains(string(body), "GET /api/logged") {
		t.Fatalf("logs: %d %s", code, body)
	}
	code, body = call("POST", "/v1/projects/shop/apps/api/restart", "", nil, "")
	if code != 200 {
		t.Fatalf("restart: %d %s", code, body)
	}
	code, body = call("DELETE", "/v1/projects/shop/apps/api/previews/pr-1", "", nil, "")
	if code != 204 {
		t.Fatalf("preview delete: %d %s", code, body)
	}
	code, body = call("POST", "/v1/projects/shop/apps/api/deploys", "text/html", strings.NewReader("x"), "")
	if code != 415 {
		t.Fatalf("content type: %d %s", code, body)
	}
}

func TestPreviewMailGoesToTheDevInbox(t *testing.T) {
	h := newHarness(t)
	h.mf.Env["SMTP_URL"] = "smtp://p_shop:secret@127.0.0.1:2525"
	h.apply()
	h.deploy("api", "", map[string]string{"index.ts": "prod"})
	h.deploy("api", "mail-test", map[string]string{"index.ts": "pv"})
	got := map[string]string{}
	h.eng.mu.Lock()
	for _, c := range h.eng.ctrs {
		got[c.spec.Env["TIFFIN_PREVIEW"]] = c.spec.Env["SMTP_URL"]
	}
	h.eng.mu.Unlock()
	if got[""] != "smtp://p_shop:secret@127.0.0.1:2525" {
		t.Errorf("production SMTP_URL changed: %q", got[""])
	}
	if !strings.Contains(got["mail-test"], "p_shop+mail-test:secret@") {
		t.Errorf("preview SMTP_URL not routed to the dev inbox: %q", got["mail-test"])
	}
	if v, _, _ := h.p.DB.KVGet(context.Background(), "runtime", "host-ip"); string(v) != "127.0.0.1" {
		t.Errorf("host-ip %q", v)
	}
}

// Auth on previews: the auth module learns their hosts from PreviewHosts,
// and a preview's auth env names its own host.
func TestPreviewAuth(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.mf.Env["TIFFIN_AUTH_URL"] = "https://shop-api.tiffin.localhost:8443/api/auth"
	h.mf.Env["TIFFIN_AUTH_HOST"] = "shop-api.tiffin.localhost"
	h.apply()
	h.deploy("api", "", map[string]string{"index.ts": "prod"})
	if hosts, err := h.m.PreviewHosts(ctx, h.p, "shop"); err != nil || len(hosts) != 0 {
		t.Fatalf("no previews yet: %v %v", hosts, err)
	}
	h.deploy("api", "feat-a", map[string]string{"index.ts": "pv"})
	const host = "feat-a--shop-api.tiffin.localhost"
	if hosts, _ := h.m.PreviewHosts(ctx, h.p, "shop"); len(hosts) != 1 || hosts[host] != "api" {
		t.Fatalf("preview hosts: %v", hosts)
	}
	got := map[string]map[string]string{}
	h.eng.mu.Lock()
	for _, c := range h.eng.ctrs {
		got[c.spec.Env["TIFFIN_PREVIEW"]] = c.spec.Env
	}
	h.eng.mu.Unlock()
	if got[""]["TIFFIN_AUTH_HOST"] != "shop-api.tiffin.localhost" {
		t.Errorf("production auth env changed: %v", got[""])
	}
	if e := got["feat-a"]; e["TIFFIN_AUTH_URL"] != "https://"+host+":8443/api/auth" || e["TIFFIN_AUTH_HOST"] != host {
		t.Errorf("preview auth env: %v", e)
	}
	if err := h.r.deletePreview(ctx, "shop", "api", "feat-a"); err != nil {
		t.Fatal(err)
	}
	if hosts, _ := h.m.PreviewHosts(ctx, h.p, "shop"); len(hosts) != 0 {
		t.Fatalf("a deleted preview is still listed: %v", hosts)
	}
}

func TestGitHelpers(t *testing.T) {
	for in, want := range map[string]string{"feature/Login-Fix": "feature-login-fix", "--x--": "x", "___": "branch",
		strings.Repeat("a", 40): strings.Repeat("a", 30)} {
		if got := branchPreview(in); got != want {
			t.Errorf("branchPreview(%q) = %q, want %q", in, got, want)
		}
	}
	h := newHookTokens()
	n := h.issue("shop", "tok_1")
	if g, ok := h.take(n); !ok || g.project != "shop" || g.token != "tok_1" {
		t.Fatal("hook token")
	}
	if _, ok := h.take(n); ok {
		t.Fatal("hook tokens are single-use")
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("anything", "tfn_secret")
	if gitToken(req) != "tfn_secret" {
		t.Fatal("basic auth password is the token")
	}
}

// pinTest stands in for the queue module's PinnedReleases.
type pinTest struct {
	mu   sync.Mutex
	rels []string
}

func (*pinTest) Name() string { return "zz-pin-test" }
func (p *pinTest) PinnedReleases(ctx context.Context, _ *platform.Platform, project, app string) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.rels...), nil
}
func (p *pinTest) set(r ...string) { p.mu.Lock(); p.rels = r; p.mu.Unlock() }

var pins = &pinTest{}

func init() { platform.Register(pins) }

func TestPinnedReleasesDrainBeforeStop(t *testing.T) {
	h := newHarness(t)
	defer pins.set()
	ctx := context.Background()
	v1 := h.deploy("api", "", map[string]string{"index.ts": "v1"})
	if rel, _ := h.m.CurrentRelease(ctx, h.p, "shop", "api"); rel != v1.ID {
		t.Fatalf("current %q", rel)
	}
	pins.set(v1.ID) // a workflow run started on v1
	v2 := h.deploy("api", "", map[string]string{"index.ts": "v2"})
	time.Sleep(300 * time.Millisecond)
	st := h.state("api", "")
	if len(st.Draining) != 1 || st.Draining[0].Release != v1.ID || len(st.Draining[0].Instances) != 2 {
		t.Fatalf("v1 must keep running for its pinned run: %+v", st)
	}
	if n := len(h.eng.running()); n != 4 {
		t.Fatalf("%d containers, want 4 (2 live + 2 draining)", n)
	}
	// Public traffic goes to v2 only; the queue still reaches v1.
	if _, body := h.get("shop.tiffin.localhost", "/api/"); !strings.Contains(body, strings.ToLower(v2.ID)) {
		t.Fatalf("public: %s", body)
	}
	u, err := h.m.AppEndpoint(ctx, h.p, "shop", "api", v1.ID)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.Get(u + "/turn")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(b), strings.ToLower(v1.ID)) {
		t.Fatalf("pinned endpoint served %s", b)
	}
	if u, _ := h.m.AppEndpoint(ctx, h.p, "shop", "api", ""); u == "" {
		t.Fatal("current endpoint")
	}
	if _, err := h.m.AppEndpoint(ctx, h.p, "shop", "api", "dep_GONE"); err == nil || !strings.Contains(err.Error(), "release gone") {
		t.Fatalf("unknown release: %v", err)
	}
	// Still pinned: the reaper keeps it. Unpinned: it stops.
	h.r.reapDrained(ctx)
	if len(h.state("api", "").Draining) != 1 {
		t.Fatal("reaped while pinned")
	}
	pins.set()
	h.r.reapDrained(ctx)
	if st := h.state("api", ""); len(st.Draining) != 0 || len(h.eng.running()) != 2 {
		t.Fatalf("not drained: %+v, %d running", st, len(h.eng.running()))
	}
	if _, err := h.m.AppEndpoint(ctx, h.p, "shop", "api", v1.ID); err == nil || !strings.Contains(err.Error(), "release gone") {
		t.Fatalf("drained release: %v", err)
	}
}

func TestPlanRefusesAnotherProjectsRoute(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	app := func(spec string) map[string]change.Resource {
		return map[string]change.Resource{"app/shop": {Address: "app/shop", Spec: json.RawMessage(spec)}}
	}
	// "shop" (the address an app named shop had before addresses were named
	// after the project) is site's in project shop.
	err := h.m.CheckPlan(ctx, h.p, "blog", app(`{"framework":"bun","routes":["shop"]}`))
	var prob *api.Problem
	if !errors.As(err, &prob) || prob.Status != 422 || !strings.Contains(prob.Detail, "shop/site") ||
		!strings.Contains(prob.Hint, `routes: ["blog-shop"]`) || prob.Errors[0].Path != "/apps/shop/routes" {
		t.Fatalf("clash: %v %+v", err, prob)
	}
	// A path under it is a different route; workers have none; own routes are
	// fine, and so is a spec without routes (served at <project>-<app>).
	for _, spec := range []string{`{"routes":["shop/blog"]}`, `{"role":"worker"}`, `{"routes":["blog"]}`, `{"framework":"bun"}`} {
		if err := h.m.CheckPlan(ctx, h.p, "blog", app(spec)); err != nil {
			t.Fatalf("%s: %v", spec, err)
		}
	}
	if err := h.m.CheckPlan(ctx, h.p, "shop", map[string]change.Resource{"app/site": {Address: "app/site", Spec: json.RawMessage(`{"routes":["shop"]}`)}}); err != nil {
		t.Fatalf("own route: %v", err)
	}
	// The box's own names (the dashboard, file links) are never an app's.
	for _, r := range []string{"dashboard", "files"} {
		if err := h.m.CheckPlan(ctx, h.p, "blog", app(`{"routes":["`+r+`"]}`)); !errors.As(err, &prob) || prob.Status != 422 {
			t.Fatalf("%s: %v", r, err)
		}
	}
}

func TestFirstBuildErrorAndDropConfig(t *testing.T) {
	out := "#26 0.123 $ next build\n#26 4.805   Running TypeScript ...\n" +
		"#26 6.406 tiffin.config.ts(1,30): error TS2307: Cannot find module 'tiffin-sdk' or its corresponding type declarations.\n" +
		"#26 6.433 error: script \"build\" exited with code 1\n#26 ERROR: process \"bun run build\" did not complete successfully: exit code: 1\n"
	if got := firstBuildError(out); !strings.HasPrefix(got, "tiffin.config.ts(1,30): error TS2307") {
		t.Fatalf("first error = %q", got)
	}
	if got := firstBuildError("#5 ERROR: process \"bun install\" did not complete successfully: exit code: 1\nerror: failed to solve: x\n"); got != "" {
		t.Fatalf("wrappers only: %q", got)
	}
	dir := t.TempDir()
	for _, f := range []string{"tiffin.config.ts", "index.ts"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var log bytes.Buffer
	dropConfig(dir, &log)
	if _, err := os.Stat(filepath.Join(dir, "tiffin.config.ts")); !os.IsNotExist(err) {
		t.Fatal("config not dropped")
	}
	if _, err := os.Stat(filepath.Join(dir, "index.ts")); err != nil || !strings.Contains(log.String(), "left out of the build") {
		t.Fatalf("index.ts kept? %v; log %q", err, log.String())
	}
}

// The build warm-up builds the Next.js starter once per tool version, drops
// the image and remembers it did.
func TestWarmUpBuildsOnce(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	before := h.bld.builds.Load()
	h.r.warmUp(ctx)
	if got := h.bld.builds.Load() - before; got != 1 {
		t.Fatalf("warm-up ran %d builds, want 1", got)
	}
	if _, ok, _ := h.p.DB.KVGet(ctx, "runtime", warmUpKey); !ok {
		t.Fatal("warm-up not remembered")
	}
	h.eng.mu.Lock()
	kept := h.eng.images[imageRef("tiffin-warmup", "web", "warmup")] != ""
	h.eng.mu.Unlock()
	if kept {
		t.Fatal("the warm-up image should be removed")
	}
	h.r.warmUp(ctx)
	if got := h.bld.builds.Load() - before; got != 1 {
		t.Fatalf("a second warm-up built again (%d builds)", got)
	}
	if len(h.r.build) != 0 {
		t.Fatal("warm-up kept the build slot")
	}
}

// A deploy never waits for the warm-up: it stops a running warm-up and
// builds at once; the warm-up tries again once the box is quiet. And the
// warm-up does not start while resources are still converging (a box that
// just started or imported has its apps to start first).
func TestWarmUpYieldsToDeploys(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	h.r.warm.poll, h.r.warm.quiet = 10*time.Millisecond, 200*time.Millisecond

	// Converging: the warm-up waits.
	_ = h.p.DB.SetResourceStatus(ctx, "shop", "app/site", platform.StatePending, "imported; converging")
	h.bld.warmBlock.Store(true)
	before := h.bld.builds.Load()
	done := make(chan struct{})
	go func() { h.r.warmUp(ctx); close(done) }()
	time.Sleep(100 * time.Millisecond)
	if h.bld.builds.Load() != before || len(h.r.build) != 0 {
		t.Fatal("the warm-up started while the box was converging")
	}
	_ = h.p.DB.SetResourceStatus(ctx, "shop", "app/site", platform.StateReady, "")
	holding := func() bool {
		h.r.warm.mu.Lock()
		defer h.r.warm.mu.Unlock()
		return h.r.warm.cancel != nil
	}
	for i := 0; !holding(); i++ {
		if i > 200 {
			t.Fatal("the warm-up never started")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A deploy comes in while the warm-up holds the slot: it goes first.
	began := time.Now()
	d := h.deploy("site", "", map[string]string{"index.html": "<h1>hi</h1>"})
	if d.Status != StatusLive {
		t.Fatalf("deploy: %s %s", d.Status, d.Error)
	}
	if waited := time.Since(began); waited > 5*time.Second {
		t.Fatalf("the deploy waited %s for the warm-up", waited)
	}
	if _, ok, _ := h.p.DB.KVGet(ctx, "runtime", warmUpKey); ok {
		t.Fatal("a stopped warm-up counted as warm")
	}

	// Once the box is quiet again, the warm-up runs to the end.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the warm-up did not finish after the deploy")
	}
	if _, ok, _ := h.p.DB.KVGet(ctx, "runtime", warmUpKey); !ok {
		t.Fatal("the warm-up did not run again after yielding")
	}
	if len(h.r.build) != 0 {
		t.Fatal("the build slot is still held")
	}
}
