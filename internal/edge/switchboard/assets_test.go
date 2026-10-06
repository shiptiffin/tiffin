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

func TestPrecompressedAssets(t *testing.T) {
	b := New(nil, nil)
	base := t.TempDir()
	dir := filepath.Join(base, "dep_1")
	js := strings.Repeat("console.log('hello from a chunk');\n", 200)
	writeRelease(t, dir, `[{"dir":".next/static","path":"/_next/static","immutable":["/_next/static/"]}]`,
		map[string]string{"_next/static/chunks/a.js": js, "_next/static/chunks/tiny.js": "x()", "_next/static/media/f.woff2": js})
	if err := Precompress(filepath.Join(dir, "www")); err != nil {
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
