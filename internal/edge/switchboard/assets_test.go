package switchboard

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
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

// writeRelease lays out an extracted release as the runtime leaves it.
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
	b := New(nil, nil)
	base := t.TempDir()
	next := `[{"dir":".next/static","path":"/_next/static","immutable":["/_next/static/"]},{"dir":"public","path":"/","liveOnly":true}]`
	writeRelease(t, filepath.Join(base, "dep_1"), next, map[string]string{"_next/static/chunks/old.js": "old", "robots.txt": "v1", "gone.txt": "v1"})
	writeRelease(t, filepath.Join(base, "dep_2"), next, map[string]string{"_next/static/chunks/new.js": "new", "robots.txt": "v2"})
	st := &Env{Project: "shop", App: "web", Live: "dep_2", Assets: base, Retired: []Retired{{Deploy: "dep_1", At: time.Now()}}}
	get := func(method, p, prefix string) (bool, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		return b.ServeAsset(w, httptest.NewRequest(method, p, nil), st, prefix), w
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
	if b.ServeAsset(w2, req, st, ""); w2.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: %d", w2.Code)
	}
	// A day later dep_1's files are gone from serving.
	st.Retired[0].At = time.Now().Add(-AssetsKept - time.Minute)
	if ok, _ := get("GET", "/_next/static/chunks/old.js", ""); ok {
		t.Error("a release retired over a day ago still serves")
	}
}

// Next.js 16.3+ immutable assets load without ?dpl from one shared path: a
// chunk two releases have is the live one's, a chunk only the retired
// release has is still found for a day; build manifests (with ?dpl) too.
// Nuxt's build manifest keeps its name, so it is revalidated and live only.
func TestServeAssetImmutableAndExceptions(t *testing.T) {
	b := New(nil, nil)
	base := t.TempDir()
	next := `[{"dir":".next/static","path":"/_next/static","immutable":["/_next/static/"]},{"dir":"public","path":"/","liveOnly":true}]`
	writeRelease(t, filepath.Join(base, "dep_1"), next, map[string]string{"_next/static/immutable/chunks/0cz1d0mv5g_q7.js": "same", "_next/static/immutable/chunks/1rj7ns8rte9vc.js": "only in 1"})
	writeRelease(t, filepath.Join(base, "dep_2"), next, map[string]string{"_next/static/immutable/chunks/0cz1d0mv5g_q7.js": "same", "_next/static/immutable/chunks/22i43cg4l4-dq.js": "only in 2"})
	nuxt := `[{"dir":".output/public","path":"/","immutable":["/_nuxt/","/_fonts/","!/_nuxt/builds/latest.json"]}]`
	writeRelease(t, filepath.Join(base, "nux_1"), nuxt, map[string]string{"_nuxt/B1x9Qa2c.js": "old chunk", "_nuxt/builds/latest.json": `{"id":"1"}`, "_nuxt/builds/meta/1.json": "{}"})
	writeRelease(t, filepath.Join(base, "nux_2"), nuxt, map[string]string{"_nuxt/C2y8Rb3d.js": "chunk", "_nuxt/builds/latest.json": `{"id":"2"}`, "_fonts/inter-4a2b9c1d.woff2": "font"})
	st := &Env{Live: "dep_2", Assets: base, Retired: []Retired{{Deploy: "dep_1", At: time.Now()}}}
	nst := &Env{Live: "nux_2", Assets: base, Retired: []Retired{{Deploy: "nux_1", At: time.Now()}}}
	for _, c := range []struct {
		st                *Env
		path, body, cache string
	}{
		{st, "/_next/static/immutable/chunks/0cz1d0mv5g_q7.js", "same", "immutable"},
		{st, "/_next/static/immutable/chunks/22i43cg4l4-dq.js", "only in 2", "immutable"},
		{st, "/_next/static/immutable/chunks/1rj7ns8rte9vc.js", "only in 1", "immutable"},
		{nst, "/_nuxt/C2y8Rb3d.js", "chunk", "immutable"},
		{nst, "/_nuxt/B1x9Qa2c.js", "old chunk", "immutable"},
		{nst, "/_fonts/inter-4a2b9c1d.woff2", "font", "immutable"},
		{nst, "/_nuxt/builds/latest.json", `{"id":"2"}`, "public, max-age=0, must-revalidate"},
		{nst, "/_nuxt/builds/meta/1.json", "{}", "immutable"}, // named by build ID
	} {
		w := httptest.NewRecorder()
		ok := b.ServeAsset(w, httptest.NewRequest("GET", c.path, nil), c.st, "")
		if !ok || w.Body.String() != c.body || !strings.Contains(w.Header().Get("Cache-Control"), c.cache) {
			t.Errorf("%s: %v %q %q", c.path, ok, w.Body.String(), w.Header().Get("Cache-Control"))
		}
	}
	// With latest.json gone from the live release, the retired one's is not served.
	os.Remove(filepath.Join(base, "nux_2", "www", "_nuxt", "builds", "latest.json"))
	if b.ServeAsset(httptest.NewRecorder(), httptest.NewRequest("GET", "/_nuxt/builds/latest.json", nil), nst, "") {
		t.Error("an earlier release's latest.json was served")
	}
}

func TestPrecompressedAssets(t *testing.T) {
	b := New(nil, nil)
	base := t.TempDir()
	dir := filepath.Join(base, "dep_1")
	js := strings.Repeat("console.log('hello from a chunk');\n", 200)
	writeRelease(t, dir, `[{"dir":".next/static","path":"/_next/static","immutable":["/_next/static/"]}]`,
		map[string]string{"_next/static/chunks/a.js": js, "_next/static/chunks/tiny.js": "x()", "_next/static/media/f.woff2": js})
	if _, err := Precompress(filepath.Join(dir, "www")); err != nil {
		t.Fatal(err)
	}
	for f, want := range map[string]bool{"chunks/a.js.zst": true, "chunks/a.js.gz": true, "chunks/tiny.js.gz": false, "media/f.woff2.gz": false} {
		if got := exists(filepath.Join(dir, "www", "_next", "static", f)); got != want {
			t.Errorf("%s written: %v, want %v", f, got, want)
		}
	}
	st := &Env{Project: "shop", App: "web", Live: "dep_1", Assets: base}
	for _, c := range []struct{ ae, enc string }{
		{"gzip, deflate, br, zstd", "zstd"}, {"gzip", "gzip"}, {"zstd;q=0, gzip", "gzip"}, {"", ""}, {"br", ""},
	} {
		req := httptest.NewRequest("GET", "/_next/static/chunks/a.js", nil)
		req.Header.Set("Accept-Encoding", c.ae)
		w := httptest.NewRecorder()
		if !b.ServeAsset(w, req, st, "") {
			t.Fatal("not served")
		}
		h := w.Header()
		if h.Get("Content-Encoding") != c.enc || h.Get("Vary") != "Accept-Encoding" || !strings.HasPrefix(h.Get("Content-Type"), "text/javascript") {
			t.Errorf("Accept-Encoding %q: encoding %q vary %q type %q", c.ae, h.Get("Content-Encoding"), h.Get("Vary"), h.Get("Content-Type"))
		}
		body := w.Body.Bytes()
		switch c.enc {
		case "gzip":
			zr, _ := gzip.NewReader(bytes.NewReader(body))
			body, _ = io.ReadAll(zr)
		case "zstd":
			zr, _ := zstd.NewReader(nil)
			body, _ = zr.DecodeAll(body, nil)
		}
		if string(body) != js {
			t.Errorf("Accept-Encoding %q: the body does not decode to the file", c.ae)
		}
	}
}

func TestTraceparentFromRequestID(t *testing.T) {
	h := http.Header{"X-Request-Id": {"0B7C4E2A-9F1D-4C3B-8A6E-2D5F7E9A1B3C"}}
	tp := traceparent(h)
	if !regexp.MustCompile(`^00-0b7c4e2a9f1d4c3b8a6e2d5f7e9a1b3c-[0-9a-f]{16}-01$`).MatchString(tp) {
		t.Fatalf("traceparent %q", tp)
	}
	h.Set("Traceparent", "00-11111111111111111111111111111111-2222222222222222-01")
	if traceparent(h) != "" {
		t.Fatal("a request's own trace context is kept")
	}
	for _, id := range []string{"", "abc", "00000000-0000-0000-0000-000000000000", "zz7c4e2a-9f1d-4c3b-8a6e-2d5f7e9a1b3c"} {
		if tp := traceparent(http.Header{"X-Request-Id": {id}}); tp != "" {
			t.Fatalf("request ID %q gave %q", id, tp)
		}
	}
}

// Prerendered pages are found once, at extraction, and answered at the
// paths the frameworks' own servers answer them at, from the live release.
func TestPrerenderedPages(t *testing.T) {
	www := t.TempDir()
	for f, b := range map[string]string{"index.html": "home", "about/index.html": "about", "blog.html": "blog flat", "blog/index.html": "blog dir",
		"404.html": "nope", "_shell.html": "shell", "assets/x.html": "hashed dir", "robots.txt": "r", "sub/deep/index.html": "deep"} {
		os.MkdirAll(filepath.Dir(filepath.Join(www, f)), 0o755)
		os.WriteFile(filepath.Join(www, f), []byte(b), 0o644)
	}
	dirs := []AssetDir{{Dir: ".output/public", Path: "/", Immutable: []string{"/assets/"}, Pages: true}}
	got := Pages(www, dirs)
	want := map[string]string{"/": "index.html", "/about": "about/index.html", "/about/": "about/index.html", "/blog": "blog.html", "/blog/": "blog/index.html",
		"/sub/deep": "sub/deep/index.html", "/sub/deep/": "sub/deep/index.html"}
	if len(got) != len(want) {
		t.Fatalf("pages: %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q", k, got[k], v)
		}
	}
	if p := Pages(www, []AssetDir{{Dir: "public", Path: "/"}}); len(p) != 0 {
		t.Errorf("a folder without Pages has pages: %v", p)
	}

	b := New(nil, nil)
	base := t.TempDir()
	meta := `[{"dir":".output/public","path":"/","immutable":["/assets/"],"pages":true}]`
	writeRelease(t, filepath.Join(base, "dep_1"), meta, map[string]string{"old.html": "old page"})
	os.WriteFile(filepath.Join(base, "dep_1", PagesFile), []byte(`{"/old":"old.html"}`), 0o644)
	writeRelease(t, filepath.Join(base, "dep_2"), meta, map[string]string{"index.html": "home", "about/index.html": "about"})
	os.WriteFile(filepath.Join(base, "dep_2", PagesFile), []byte(`{"/":"index.html","/about":"about/index.html","/about/":"about/index.html"}`), 0o644)
	st := &Env{Live: "dep_2", Assets: base, Retired: []Retired{{Deploy: "dep_1", At: time.Now()}}}
	for _, c := range []struct{ method, path, prefix, body string }{
		{"GET", "/", "", "home"},
		{"GET", "/about", "", "about"},
		{"GET", "/about/", "", "about"},
		{"HEAD", "/about", "", ""},
		{"GET", "/shop", "/shop", "home"},
		{"GET", "/shop/about", "/shop", "about"},
		{"GET", "/old", "", "-"},            // pages are the live release's only
		{"GET", "/about//", "", "-"},        // not a clean path
		{"GET", "/nope/", "", "-"},          // the app's
		{"POST", "/about", "", "-"},         // only reads
		{"GET", "/contact", "", "-"},        // no file: the app's
		{"GET", "/about/../about", "", "-"}, // never cleaned for the app
	} {
		w := httptest.NewRecorder()
		ok := b.ServeAsset(w, httptest.NewRequest(c.method, c.path, nil), st, c.prefix)
		if c.body == "-" {
			if ok {
				t.Errorf("%s %s: served %q, want it passed to the app", c.method, c.path, w.Body.String())
			}
			continue
		}
		if !ok || w.Body.String() != c.body || w.Header().Get("Cache-Control") != "public, max-age=0, must-revalidate" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
			t.Errorf("%s %s: %v %q %v", c.method, c.path, ok, w.Body.String(), w.Header())
		}
	}
}

// Precompression is bounded: a file over the cap, or past the budget,
// stays as it is, and a link the build left at a copy's name is replaced,
// never written through.
func TestPrecompressIsBounded(t *testing.T) {
	defer func(m, b int64) { maxPrecompress, precompressBudget = m, b }(maxPrecompress, precompressBudget)
	maxPrecompress, precompressBudget = 64<<10, 100<<10
	dir := t.TempDir()
	text := func(n int) []byte { return bytes.Repeat([]byte("let a = 1;\n"), n/11) }
	outside := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{"big.js": text(80 << 10), "a.js": text(60 << 10), "b.js": text(60 << 10)} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(dir, "a.js.gz")); err != nil {
		t.Fatal(err)
	}
	n, err := Precompress(dir)
	if err != nil || n != 1 {
		t.Fatalf("compressed %d: %v", n, err)
	}
	if exists(filepath.Join(dir, "big.js.zst")) {
		t.Error("a file over the cap was compressed")
	}
	if got, _ := os.ReadFile(outside); string(got) != "keep" {
		t.Errorf("wrote through a link: %q", got)
	}
	if fi, err := os.Lstat(filepath.Join(dir, "a.js.gz")); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("a.js.gz: %v %v", fi, err)
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".precompress-") {
			t.Errorf("left %s", e.Name())
		}
	}
}
