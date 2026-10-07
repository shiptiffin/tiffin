package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/manifest"
)

func TestPrepareNextWritesTheAdapter(t *testing.T) {
	src := t.TempDir()
	env := map[string]string{}
	req := BuildRequest{Deploy: &Deploy{ID: "dep_01TEST"}, SrcDir: src, NextCache: true, Log: io.Discard, Env: map[string]string{}}
	img := prepareNext(req, env)
	if env[nextAdapterEnv] != nextAdapterPath || img[nextAdapterEnv] != nextAdapterPath {
		t.Fatalf("env %v, image env %v", env, img)
	}
	for _, f := range []string{"adapter.js", "package.json", "cache-handler.js", "use-cache.js", "store.js", "resp.js"} {
		if !exists(filepath.Join(src, nextDir, f)) {
			t.Errorf("%s not written", f)
		}
	}
	// The app's own adapter wins.
	src2 := t.TempDir()
	req2 := BuildRequest{Deploy: &Deploy{ID: "dep_2"}, SrcDir: src2, Log: io.Discard, Env: map[string]string{nextAdapterEnv: "./mine.js"}}
	if img := prepareNext(req2, map[string]string{}); img != nil || exists(filepath.Join(src2, nextDir)) {
		t.Fatal("the box's adapter must stay out when the app sets its own")
	}

	// The adapter itself, run the way Next.js runs it.
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found: adapter logic not exercised")
	}
	script := `const a = (await import(process.argv[1])).default;
const out = [
  a.modifyConfig({ compress: true, images: { maximumDiskCacheSize: undefined }, cacheHandlers: {} }),
  a.modifyConfig({ deploymentId: "mine", cacheHandler: "/my.js", cacheMaxMemorySize: 5, cacheHandlers: { default: "/d.js" }, images: { maximumDiskCacheSize: 7 } }),
];
console.log(JSON.stringify({ name: a.name, out }));`
	cmd := exec.Command(node, "--input-type=module", "-e", script, filepath.Join(src, nextDir, "adapter.js"))
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, raw)
	}
	var res struct {
		Name string           `json:"name"`
		Out  []map[string]any `json:"out"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	real, _ := filepath.EvalSymlinks(src)
	here := filepath.Join(real, nextDir)
	plain, mine := res.Out[0], res.Out[1]
	hs, _ := plain["cacheHandlers"].(map[string]any)
	if res.Name != "tiffin" || plain["compress"] != false || plain["poweredByHeader"] != false || plain["deploymentId"] != "dep_01TEST" ||
		plain["cacheHandler"] != filepath.Join(here, "cache-handler.js") || plain["cacheMaxMemorySize"] != 0.0 ||
		hs["default"] != filepath.Join(here, "use-cache.js") || hs["remote"] != filepath.Join(here, "use-cache.js") ||
		plain["images"].(map[string]any)["maximumDiskCacheSize"] != float64(nextImageCacheBytes) {
		t.Errorf("defaults: %v", plain)
	}
	hs, _ = mine["cacheHandlers"].(map[string]any)
	if mine["deploymentId"] != "mine" || mine["cacheHandler"] != "/my.js" || mine["cacheMaxMemorySize"] != 5.0 ||
		hs["default"] != "/d.js" || mine["images"].(map[string]any)["maximumDiskCacheSize"] != 7.0 || mine["compress"] != false {
		t.Errorf("the app's own config must win: %v", mine)
	}
}

// Next.js 16.3+ builds get immutable assets, and `next start` follows what
// the build decided (Next.js turns them off for webpack builds).
func TestNextAdapterImmutableAssets(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found: adapter logic not exercised")
	}
	src := t.TempDir()
	if err := writeNextAdapter(src, nextBox{DeploymentID: "dep_1"}); err != nil {
		t.Fatal(err)
	}
	on, off, none := t.TempDir(), t.TempDir(), t.TempDir()
	for dir, v := range map[string]bool{on: true, off: false} {
		os.MkdirAll(filepath.Join(dir, ".next"), 0o755)
		os.WriteFile(filepath.Join(dir, ".next", "required-server-files.json"), []byte(`{"config":{"supportsImmutableAssets":`+strconv.FormatBool(v)+`}}`), 0o644)
	}
	script := `const a = (await import(process.argv[1])).default;
const [on, off, none] = process.argv.slice(2);
const v = (cfg, ctx) => a.modifyConfig({ distDir: ".next", images: {}, ...cfg }, ctx).supportsImmutableAssets ?? null;
const build = { phase: "phase-production-build", nextVersion: "16.3.8", projectDir: none };
const start = (dir, nextVersion = "16.4.0") => ({ phase: "phase-production-server", nextVersion, projectDir: dir });
console.log(JSON.stringify([
  v({}, build),
  v({}, { ...build, nextVersion: "16.2.4" }),
  v({ supportsImmutableAssets: false }, build),
  v({ experimental: { supportsImmutableAssets: false } }, build),
  v({}, start(on)),
  v({}, start(off)),
  v({}, start(none)),
  v({}, start(on, "16.5.0-canary.2")),
  v({}, { phase: "phase-development-server", nextVersion: "16.3.8", projectDir: on }),
]));`
	raw, err := exec.Command(node, "--input-type=module", "-e", script, filepath.Join(src, nextDir, "adapter.js"), on, off, none).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, raw)
	}
	want := `[true,null,false,null,true,false,null,true,null]`
	if got := strings.TrimSpace(string(raw)); got != want {
		t.Errorf("supportsImmutableAssets: %s, want %s\n(build 16.3, build 16.2, user false, user experimental false, start after an immutable build, after a build without, without a build, canary, dev)", got, want)
	}
}

func TestSetDeployEnvKeepsThePlan(t *testing.T) {
	p := filepath.Join(t.TempDir(), "plan.json")
	os.WriteFile(p, []byte(`{"steps":[{"name":"build"}],"deploy":{"startCommand":"bun --bun next start","variables":{"A":"1"}}}`), 0o644)
	if err := setDeployEnv(p, map[string]string{nextAdapterEnv: nextAdapterPath}); err != nil {
		t.Fatal(err)
	}
	var plan struct {
		Steps  []map[string]any `json:"steps"`
		Deploy struct {
			Start string            `json:"startCommand"`
			Vars  map[string]string `json:"variables"`
		} `json:"deploy"`
	}
	raw, _ := os.ReadFile(p)
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 1 || plan.Deploy.Start != "bun --bun next start" || plan.Deploy.Vars["A"] != "1" || plan.Deploy.Vars[nextAdapterEnv] != nextAdapterPath {
		t.Fatalf("plan: %s", raw)
	}
}

func TestNextAppsGetAStableKeyAndAnImageCache(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	api := h.mf.Apps["api"]
	api.Framework = manifest.FrameworkNext
	h.mf.Apps["api"] = api
	h.apply()
	d1 := h.deploy("api", "", map[string]string{"package.json": `{}`})
	if d1.Status != StatusLive {
		t.Fatalf("deploy: %s %s", d1.Status, d1.Error)
	}
	key := func() string {
		for _, c := range h.eng.running() {
			if c.spec.Labels["tiffin.app"] == "api" {
				return c.spec.Env[nextKeyEnv]
			}
		}
		return ""
	}
	k1 := key()
	if b, err := base64.StdEncoding.DecodeString(k1); err != nil || len(b) != 32 {
		t.Fatalf("key %q is not 32 bytes of base64", k1)
	}
	h.deploy("api", "", map[string]string{"package.json": `{}`})
	pv := h.deploy("api", "pr-1", map[string]string{"package.json": `{}`})
	if pv.Status != StatusLive {
		t.Fatal(pv.Error)
	}
	var mounts []string
	for _, c := range h.eng.running() {
		if c.spec.Labels["tiffin.app"] != "api" {
			continue
		}
		if c.spec.Env[nextKeyEnv] != k1 {
			t.Fatal("the key changed across deploys or previews")
		}
		mounts = append(mounts, c.spec.Mounts...)
	}
	want := []string{h.r.nextCacheDir("shop", "api", "") + ":" + nextImageCache, h.r.nextCacheDir("shop", "api", "pr-1") + ":" + nextImageCache}
	if !strings.Contains(strings.Join(mounts, " "), want[0]) || !strings.Contains(strings.Join(mounts, " "), want[1]) {
		t.Fatalf("mounts %v, want %v", mounts, want)
	}
	// The app's own key wins, at run time and build time.
	got, err := h.r.nextActionsKey(ctx, "shop", "api", map[string]string{nextKeyEnv: "mine"})
	if err != nil || got != "mine" {
		t.Fatalf("own key: %q %v", got, err)
	}
	// Deleting the preview drops its image cache; destroying the project drops the key.
	if err := h.r.deletePreview(ctx, "shop", "api", "pr-1"); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Dir(h.r.nextCacheDir("shop", "api", "pr-1"))) || !exists(h.r.nextCacheDir("shop", "api", "")) {
		t.Fatal("deleting a preview must drop its image cache only")
	}
	if err := h.m.ProjectDeleted(ctx, h.p, "shop"); err != nil {
		t.Fatal(err)
	}
	if kv, _ := h.p.DB.KVList(ctx, nsNextKeys); len(kv) != 0 {
		t.Fatalf("keys left: %v", kv)
	}
}

func TestNextStartCommand(t *testing.T) {
	for script, want := range map[string]string{
		"":                                     "",
		"next start":                           "",
		"bun --bun next start":                 "",
		"bunx next start -p $PORT":             " -p $PORT",
		"next start --port=${PORT} -H 0.0.0.0": " --port=${PORT} -H 0.0.0.0",
	} {
		if got, ok := nextStartArgs(script); !ok || got != want {
			t.Errorf("%q: %q %v, want %q", script, got, ok, want)
		}
	}
	for _, script := range []string{"node server.js", "next start && echo hi", "NODE_ENV=x next start", "next build && next start"} {
		if _, ok := nextStartArgs(script); ok {
			t.Errorf("%q taken for a plain next start", script)
		}
	}
	for cmd, want := range map[string]string{
		"bun --bun next start":                                "exec bun --bun next start",
		"cd 'web' && bun --bun next start":                    "cd 'web' && exec bun --bun next start",
		"node node_modules/next/dist/bin/next start -p $PORT": "exec node node_modules/next/dist/bin/next start -p $PORT",
		"true":                           "true",
		"exec bun x.ts":                  "exec bun x.ts",
		"bun migrate.ts && bun start.ts": "bun migrate.ts && bun start.ts",
		"PORT=1 bun x.ts":                "PORT=1 bun x.ts",
		"bun x.ts | tee log":             "bun x.ts | tee log",
	} {
		if got := execLast(cmd); got != want {
			t.Errorf("execLast(%q) = %q, want %q", cmd, got, want)
		}
	}
}

func TestNextOrigin(t *testing.T) {
	prod := map[string]string{}
	nextOrigin(prod, "https://shop.example.com/store", "https://shop.example.com/store", false)
	if prod["VERCEL_PROJECT_PRODUCTION_URL"] != "shop.example.com" || prod["VERCEL_ENV"] != "" || prod["VERCEL_BRANCH_URL"] != "" {
		t.Errorf("production: %v", prod)
	}
	pv := map[string]string{}
	nextOrigin(pv, "https://shop.tiffin.localhost:8443", "https://pr-1--shop.tiffin.localhost:8443", true)
	if pv["VERCEL_PROJECT_PRODUCTION_URL"] != "shop.tiffin.localhost:8443" || pv["VERCEL_ENV"] != "preview" || pv["VERCEL_BRANCH_URL"] != "pr-1--shop.tiffin.localhost:8443" {
		t.Errorf("preview: %v", pv)
	}
	for _, k := range []string{"VERCEL", "VERCEL_URL"} {
		if _, ok := pv[k]; ok {
			t.Errorf("%s set: libraries take it to mean the app runs on Vercel", k)
		}
	}
	own := map[string]string{"VERCEL_PROJECT_PRODUCTION_URL": "mine.example", "VERCEL_ENV": "production"}
	nextOrigin(own, "https://shop.example.com", "https://pr-1--shop.example.com", true)
	if own["VERCEL_PROJECT_PRODUCTION_URL"] != "mine.example" || own["VERCEL_ENV"] != "production" || own["VERCEL_BRANCH_URL"] != "" {
		t.Errorf("the app's own values must win: %v", own)
	}
}

// fakeFiles stands in for the storage module's files host.
type fakeFiles struct{ got *http.Request }

func (f *fakeFiles) ServeFile(w http.ResponseWriter, r *http.Request) {
	f.got = r
	w.Header().Set("Content-Type", "image/webp")
	_, _ = io.WriteString(w, "webp")
}

func TestServeBucketImage(t *testing.T) {
	h := newHarness(t)
	ff := &fakeFiles{}
	defer func(f func() fileServer) { findFileServer = f }(findFileServer)
	findFileServer = func() fileServer { return ff }
	files := h.p.URL(h.p.Host("files"))
	st := &AppState{Project: "shop", App: "web", Live: "dep_1"}
	get := func(target, accept string) (bool, *httptest.ResponseRecorder) {
		ff.got = nil
		req := httptest.NewRequest("GET", target, nil)
		req.Header.Set("Accept", accept)
		w := httptest.NewRecorder()
		return h.r.serveBucketImage(w, req, st, ""), w
	}
	ok, w := get("/_next/image?url="+url.QueryEscape(files+"/shop/media/a.jpg?X-Sig=1")+"&w=700&q=80", "image/avif,image/webp,*/*")
	if !ok || ff.got == nil || w.Body.String() != "webp" || w.Header().Get("Vary") != "Accept" {
		t.Fatalf("a bucket image: served %v, %q", ok, w.Body.String())
	}
	q := ff.got.URL.Query()
	if ff.got.URL.Path != "/shop/media/a.jpg" || ff.got.Host != strings.TrimPrefix(files, "https://") ||
		q.Get("w") != "750" || q.Get("q") != "75" || q.Get("f") != "webp" || q.Get("X-Sig") != "1" {
		t.Errorf("transform request: %s %s", ff.got.Host, ff.got.URL)
	}
	if get("/_next/image?url="+url.QueryEscape(files+"/shop/media/a.jpg")+"&w=64&q=75", "image/jpeg"); ff.got.URL.Query().Get("f") != "original" {
		t.Errorf("without image/webp in Accept: %s", ff.got.URL)
	}
	for _, target := range []string{
		"/_next/image?url=" + url.QueryEscape(files+"/other/media/a.jpg") + "&w=64&q=75", // another project's files
		"/_next/image?url=" + url.QueryEscape("https://example.com/a.jpg") + "&w=64&q=75",
		"/_next/image?url=%2Fhero.jpg&w=64&q=75",
		"/next/image?url=" + url.QueryEscape(files+"/shop/media/a.jpg"),
	} {
		if ok, _ := get(target, "image/webp"); ok {
			t.Errorf("%s: served, want it passed to the app", target)
		}
	}
}
