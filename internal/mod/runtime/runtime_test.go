package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"os"
	"path/filepath"
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
	mu     sync.Mutex
	ctrs   map[string]*fakeCtr
	images map[string]bool
	crash  map[string]bool
	runs   int
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{ctrs: map[string]*fakeCtr{}, images: map[string]bool{}, crash: map[string]bool{}}
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
	if _, dup := e.ctrs[s.Name]; dup {
		return fmt.Errorf("container %s exists", s.Name)
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
		fmt.Fprintf(w, "%s greeting=%s preview=%s", s.Image, s.Env["GREETING"], s.Env["TIFFIN_PREVIEW"])
	})}
	appendLog(s.LogPath, "stdout", "listening on "+s.Env["PORT"])
	go c.srv.Serve(ln)
	return nil
}

func (e *fakeEngine) Remove(ctx context.Context, name string, grace time.Duration) error {
	e.mu.Lock()
	c := e.ctrs[name]
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
	st := "exited"
	if c.running {
		st = "running"
	}
	return &Container{Name: name, Running: c.running, Status: st, Labels: c.spec.Labels}, nil
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

func (e *fakeEngine) ImageDigest(ctx context.Context, ref string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.images[ref] {
		return "", fmt.Errorf("no image %s", ref)
	}
	return "sha256:" + strings.Repeat("a", 64), nil
}

func (e *fakeEngine) RemoveImage(ctx context.Context, ref string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.images, ref)
	return nil
}

func (e *fakeEngine) LoadImage(ctx context.Context, file, ref string, log io.Writer) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.images[ref] = true
	return nil
}

// fakeBuilder "builds" instantly; a FAIL file fails the build, a CRASH file
// produces an image whose containers die on start. Static apps use the real
// static path (no build script → files served as they are).
type fakeBuilder struct {
	eng    *fakeEngine
	static *boxBuilder
	builds atomic.Int32
}

func (b *fakeBuilder) Build(ctx context.Context, req BuildRequest) (BuildResult, error) {
	b.builds.Add(1)
	if req.Spec.Framework == manifest.FrameworkStatic {
		return b.static.Build(ctx, req)
	}
	if req.Prebuilt != "" {
		ref := imageRef(req.Deploy.Project, req.Deploy.App, req.Deploy.ID)
		return BuildResult{Image: ref}, b.eng.LoadImage(ctx, req.Prebuilt, ref, req.Log)
	}
	if exists(filepath.Join(req.SrcDir, "FAIL")) {
		fmt.Fprintln(req.Log, "error: Cannot find module 'hono'")
		return BuildResult{}, &BuildError{Msg: "the build failed: buildctl exited with status 1", Hint: "read the log"}
	}
	ref := imageRef(req.Deploy.Project, req.Deploy.App, req.Deploy.ID)
	b.eng.mu.Lock()
	b.eng.images[ref] = true
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
		Drain: 150 * time.Millisecond, StopGrace: time.Second, PreviewIdle: time.Hour, KeepImages: 2, Engine: eng}
	bld := &fakeBuilder{eng: eng, static: &boxBuilder{eng: eng, staticDir: filepath.Join(opt.DataDir, "static")}}
	opt.Builder = bld
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
	h := &harness{t: t, m: m, r: m.r, p: p, eng: eng, bld: bld, edge: fe, srv: httptest.NewServer(fe)}
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

func TestEnvChangeRestartsAndDeleteStops(t *testing.T) {
	h := newHarness(t)
	h.deploy("api", "", map[string]string{"index.ts": "v1"})
	before := h.state("api", "")
	// Reconcile with nothing changed is a no-op.
	h.apply()
	if st := h.state("api", ""); st.Instances[0].Name != before.Instances[0].Name {
		t.Fatal("reconcile without changes restarted the app")
	}
	// A secret changes the env: the app restarts with it.
	if err := h.p.Secrets.Set(context.Background(), "shop", "GREETING", "bonjour", "tok_test"); err != nil {
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
	if pv.Status != StatusLive || pv.URL != "https://feat-x--api.tiffin.localhost:8443" {
		t.Fatalf("preview %+v", pv)
	}
	if st := h.state("api", ""); st.Live != prod.ID {
		t.Fatal("a preview must not touch production")
	}
	host := "feat-x--api.tiffin.localhost"
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
	call := func(method, path, ct string, body io.Reader, accept string) (int, []byte) {
		req, _ := http.NewRequest(method, srv.URL+path, body)
		req.Header.Set("Authorization", "Bearer "+owner)
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
	code, body := call("POST", "/v1/projects/shop/apps/api/deploys", "application/gzip", bytes.NewReader(src), "")
	if code != 202 {
		t.Fatalf("upload: %d %s", code, body)
	}
	var d Deploy
	json.Unmarshal(body, &d)
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

func TestPickLoaded(t *testing.T) {
	out := "Loaded image: import@sha256:6a1e59aa2e1822c073f6e42f928554c0612c56cbd637c2cc0802348440aff9a0\nLoaded image: docker.io/me/app:v1\n"
	if got := pickLoaded(out); got != "docker.io/me/app:v1" {
		t.Fatalf("tagged: %q", got)
	}
	if got := pickLoaded("Loaded image: import@sha256:6a1e59aa2e1822c073f6e42f928554c0612c56cbd637c2cc0802348440aff9a0\n"); got != "6a1e59aa2e18" {
		t.Fatalf("untagged: %q", got)
	}
	if pickLoaded("nothing") != "" {
		t.Fatal("empty")
	}
}
