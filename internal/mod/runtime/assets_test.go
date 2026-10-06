package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/manifest"
)

func TestClientAssetsFindsKnownBuilds(t *testing.T) {
	dir := t.TempDir()
	pkg := func(body string) {
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	web := &manifest.App{Framework: manifest.FrameworkBun, Role: manifest.RoleWeb}
	cases := []struct {
		pkg  string
		want string // first dir
	}{
		{`{"dependencies":{"next":"16.3.8"}}`, ".next/static"},
		{`{"dependencies":{"nuxt":"4"}}`, ".output/public"},
		{`{"dependencies":{"@tanstack/react-start":"1"}}`, ".output/public"},
		{`{"devDependencies":{"@react-router/dev":"7"}}`, "build/client"},
		{`{"devDependencies":{"@sveltejs/adapter-node":"5"}}`, "build/client"},
		{`{"dependencies":{"@astrojs/node":"9"}}`, "dist/client"},
		{`{"dependencies":{"hono":"4"}}`, ""},
	}
	for _, c := range cases {
		pkg(c.pkg)
		got := clientAssets(dir, web)
		if (c.want == "") != (got == nil) || (got != nil && got[0].Dir != c.want) {
			t.Errorf("%s: got %+v, want %q", c.pkg, got, c.want)
		}
	}
	// framework next needs no package.json; the app's own setting wins; static and workers have none.
	if got := clientAssets("", &manifest.App{Framework: manifest.FrameworkNext}); !reflect.DeepEqual(got, nextAssets) {
		t.Errorf("next: %+v", got)
	}
	own := &manifest.App{Framework: manifest.FrameworkNext, Assets: &manifest.Assets{Dir: "web/dist", Path: "/static/"}}
	if got := clientAssets(dir, own); len(got) != 1 || got[0].Dir != "web/dist" || got[0].Path != "/static" || got[0].Immutable != nil {
		t.Errorf("own setting: %+v", got)
	}
	pkg(`{"dependencies":{"next":"16"}}`)
	for _, a := range []*manifest.App{{Framework: manifest.FrameworkStatic}, {Framework: manifest.FrameworkBun, Role: manifest.RoleWorker}} {
		if got := clientAssets(dir, a); got != nil {
			t.Errorf("%+v: %+v", a, got)
		}
	}
}

func TestRetireKeepsRecentReleases(t *testing.T) {
	now := time.Now()
	var l []Retired
	l = retire(l, "", "d1", now) // first deploy
	if l != nil {
		t.Fatalf("first deploy: %v", l)
	}
	l = retire(l, "d1", "d2", now)
	l = retire(l, "d2", "d2", now) // a restart retires nothing
	l = retire(l, "d2", "d3", now)
	if len(l) != 2 || l[0].Deploy != "d2" || l[1].Deploy != "d1" {
		t.Fatalf("got %v", l)
	}
	l = retire(l, "d3", "d1", now) // a rollback to d1: d1 is live, not retired
	if len(l) != 2 || l[0].Deploy != "d3" || l[1].Deploy != "d2" {
		t.Fatalf("rollback: %v", l)
	}
	l = retire(l, "d1", "d4", now)
	l = retire(l, "d4", "d5", now)
	if len(l) != maxRetired || l[0].Deploy != "d4" {
		t.Fatalf("cap: %v", l)
	}
	l = retire(l, "d5", "d6", now.Add(assetsKept+time.Minute))
	if len(l) != 1 || l[0].Deploy != "d5" {
		t.Fatalf("expiry: %v", l)
	}
}

func TestServeFileStaysInside(t *testing.T) {
	www := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(outside, []byte("secret"), 0o644)
	os.MkdirAll(filepath.Join(www, "assets"), 0o755)
	os.WriteFile(filepath.Join(www, "assets", "a.js"), []byte("js"), 0o644)
	os.Symlink(outside, filepath.Join(www, "assets", "leak.js"))
	os.Symlink("a.js", filepath.Join(www, "assets", "b.js"))
	get := func(p string) (bool, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		ok := serveFile(w, httptest.NewRequest("GET", p, nil), www, p, true)
		return ok, w
	}
	if ok, w := get("/assets/a.js"); !ok || w.Body.String() != "js" || !strings.Contains(w.Header().Get("Content-Type"), "javascript") ||
		w.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("a.js: %v %v %q", ok, w.Header(), w.Body)
	}
	if ok, _ := get("/assets/leak.js"); ok {
		t.Fatal("a link out of the directory must not be followed")
	}
	if ok, w := get("/assets/b.js"); !ok || w.Body.String() != "js" {
		t.Fatal("a link inside the directory is fine")
	}
	if ok, _ := get("/assets"); ok {
		t.Fatal("directories are not served")
	}
}

// writeRelease lays out an extracted release as ensureAssets leaves it.
func writeRelease(t *testing.T, dir string, meta string, files map[string]string) {
	t.Helper()
	for p, b := range files {
		f := filepath.Join(dir, "www", p)
		os.MkdirAll(filepath.Dir(f), 0o755)
		if err := os.WriteFile(f, []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(dir, "assets.json"), []byte(meta), 0o644)
}

func TestServeAssetAcrossReleases(t *testing.T) {
	r := &rt{opt: Options{DataDir: t.TempDir()}, assetMetas: map[string][]AssetDir{}}
	base := r.assetsDir("shop", "web", "")
	next := `[{"dir":".next/static","path":"/_next/static","immutable":["/_next/static/"]},{"dir":"public","path":"/","liveOnly":true}]`
	writeRelease(t, filepath.Join(base, "dep_1"), next, map[string]string{"_next/static/chunks/old.js": "old", "robots.txt": "v1", "gone.txt": "v1"})
	writeRelease(t, filepath.Join(base, "dep_2"), next, map[string]string{"_next/static/chunks/new.js": "new", "robots.txt": "v2"})
	st := &AppState{Project: "shop", App: "web", Live: "dep_2", Retired: []Retired{{Deploy: "dep_1", At: time.Now()}}}
	get := func(method, p, prefix string) (bool, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		return r.serveAsset(w, httptest.NewRequest(method, p, nil), st, prefix), w
	}
	for _, c := range []struct {
		method, path, prefix, body, cache string
	}{
		{"GET", "/_next/static/chunks/new.js", "", "new", "immutable"},
		{"GET", "/_next/static/chunks/old.js", "", "old", "immutable"}, // a page of dep_1 still finds it
		{"GET", "/robots.txt", "", "v2", "must-revalidate"},
		{"HEAD", "/robots.txt", "", "", "must-revalidate"},
		{"GET", "/gone.txt", "", "-", ""},                    // public files are the live release's only
		{"GET", "/_next/static/chunks/nope.js", "", "-", ""}, // the app says 404
		{"POST", "/robots.txt", "", "-", ""},
		{"GET", "/", "", "-", ""},
		{"GET", "/shop/robots.txt", "/shop", "v2", "must-revalidate"},
	} {
		ok, w := get(c.method, c.path, c.prefix)
		if c.body == "-" {
			if ok {
				t.Errorf("%s %s: served, want it passed to the app", c.method, c.path)
			}
			continue
		}
		if !ok || w.Body.String() != c.body || !strings.Contains(w.Header().Get("Cache-Control"), c.cache) {
			t.Errorf("%s %s: %v %q %q", c.method, c.path, ok, w.Body.String(), w.Header().Get("Cache-Control"))
		}
	}
	// A conditional request for an unchanged file is a 304.
	_, w := get("GET", "/robots.txt", "")
	req := httptest.NewRequest("GET", "/robots.txt", nil)
	req.Header.Set("If-None-Match", w.Header().Get("ETag"))
	w2 := httptest.NewRecorder()
	if r.serveAsset(w2, req, st, ""); w2.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: %d", w2.Code)
	}
	// A day later dep_1's files are gone from serving, and pruning removes them.
	st.Retired[0].At = time.Now().Add(-assetsKept - time.Minute)
	if ok, _ := get("GET", "/_next/static/chunks/old.js", ""); ok {
		t.Error("a release retired over a day ago still serves")
	}
}

func TestClientAssetsThroughDeploys(t *testing.T) {
	h := newHarness(t)
	src := func(v string, extra map[string]string) map[string]string {
		files := map[string]string{
			"package.json":                         `{"devDependencies":{"@react-router/dev":"7"}}`,
			"build/client/assets/app-" + v + ".js": "app " + v,
			"build/client/robots.txt":              "robots " + v,
		}
		for k, b := range extra {
			files[k] = b
		}
		return files
	}
	d1 := h.deploy("api", "", src("1", map[string]string{"build/client/only-v1.txt": "v1"}))
	if d1.Status != StatusLive || len(d1.Assets) != 1 || d1.Assets[0].Dir != "build/client" {
		t.Fatalf("d1: %s %+v", d1.Status, d1.Assets)
	}
	host := "shop.tiffin.localhost"
	if code, body := h.get(host, "/api/assets/app-1.js"); code != 200 || body != "app 1" {
		t.Fatalf("v1 asset: %d %q", code, body)
	}
	d2 := h.deploy("api", "", src("2", nil))
	for path, want := range map[string]string{
		"/api/assets/app-2.js": "app 2",
		"/api/assets/app-1.js": "app 1",    // the previous release's hashed file
		"/api/robots.txt":      "robots 2", // the live release's
	} {
		if code, body := h.get(host, path); code != 200 || body != want {
			t.Errorf("%s: %d %q, want %q", path, code, body, want)
		}
	}
	// A file only the previous release had, outside its hashed assets, is the app's to answer.
	if _, body := h.get(host, "/api/only-v1.txt"); !strings.Contains(body, "greeting=") {
		t.Errorf("only-v1.txt: %q, want the app's answer", body)
	}
	if st := h.state("api", ""); st.Live != d2.ID || len(st.Retired) != 1 || st.Retired[0].Deploy != d1.ID {
		t.Fatalf("state: %+v", st)
	}
	// Pruning keeps both; once d1 retired over a day ago, it goes.
	h.r.pruneAssets(context.Background())
	d1dir := filepath.Join(h.r.assetsDir("shop", "api", ""), d1.ID)
	if !exists(d1dir) {
		t.Fatal("pruned a retired release's assets within the day")
	}
	st := h.state("api", "")
	st.Retired[0].At = time.Now().Add(-assetsKept - time.Minute)
	if err := h.r.st.putState(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(d1dir, time.Now().Add(-2*time.Hour), time.Now().Add(-2*time.Hour))
	h.r.pruneAssets(context.Background())
	if exists(d1dir) || !exists(filepath.Join(h.r.assetsDir("shop", "api", ""), d2.ID)) {
		t.Fatal("prune must remove the expired release and keep the live one")
	}
	// A rollback to d1 copies its assets out of its image again.
	if _, err := h.r.rollback(context.Background(), "shop", "api", d1.ID); err != nil {
		t.Fatal(err)
	}
	if code, body := h.get(host, "/api/assets/app-1.js"); code != 200 || body != "app 1" {
		t.Fatalf("after rollback: %d %q", code, body)
	}
	// Deleting the app forgets its files.
	h.mf.Apps = map[string]manifest.App{"site": h.mf.Apps["site"], "jobs": h.mf.Apps["jobs"]}
	h.apply()
	if exists(filepath.Join(h.r.opt.DataDir, "assets", "shop", "api")) {
		t.Fatal("assets of a deleted app are kept")
	}
}
