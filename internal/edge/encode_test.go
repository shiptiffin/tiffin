package edge

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

var bigText = strings.Repeat("Tiffin serves this text compressed. ", 120) // ~4 KB

// fetch sends GET u with Accept-Encoding ae ("" = identity) and returns the
// response with its body decoded.
func fetch(t *testing.T, c *http.Client, u, ae string, hdr ...string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", u, nil)
	if ae == "" {
		ae = "identity" // otherwise Go asks for gzip and decodes it out of sight
	}
	req.Header.Set("Accept-Encoding", ae)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
	defer resp.Body.Close()
	return resp, string(decode(t, resp.Header.Get("Content-Encoding"), resp.Body))
}

func decode(t *testing.T, enc string, r io.Reader) []byte {
	t.Helper()
	var err error
	switch enc {
	case "gzip":
		r, err = gzip.NewReader(r)
	case "zstd":
		var d *zstd.Decoder
		d, err = zstd.NewReader(r)
		r = d
	case "":
	default:
		t.Fatalf("unexpected Content-Encoding %q", enc)
	}
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("decode %s: %v", enc, err)
	}
	return b
}

func startEdge(t *testing.T, cfg Config) *http.Client {
	t.Helper()
	e, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Stop() })
	ca, err := e.RootCAPEM()
	if err != nil {
		t.Fatal(err)
	}
	return client(t, ca, cfg.HTTPSPort)
}

func TestCompression(t *testing.T) {
	isolate(t)
	release := make(chan struct{}, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/small":
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, "short")
		case "/png":
			w.Header().Set("Content-Type", "image/png")
			io.WriteString(w, bigText)
		case "/gzipped": // the app compressed it itself
			var b bytes.Buffer
			zw := gzip.NewWriter(&b)
			io.WriteString(zw, bigText)
			zw.Close()
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Encoding", "gzip")
			w.Write(b.Bytes())
		case "/stream", "/trickle", "/sse": // a first part, then the rest only once the client has the first
			ct, first := "text/plain; charset=utf-8", bigText+"\n"
			switch r.URL.Path {
			case "/trickle":
				first = "tick\n"
			case "/sse":
				ct, first = "text/event-stream", "data: hello\n\n"
			}
			w.Header().Set("Content-Type", ct)
			io.WriteString(w, first)
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-time.After(5 * time.Second):
			}
			io.WriteString(w, "the end\n")
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, bigText)
		}
	}))
	t.Cleanup(up.Close)
	cfg := testConfig(t, addr(up))
	cfg.Routes = []Route{
		{Host: "shop.tiffin.localhost", Upstream: addr(up)},
		{Host: "s3.tiffin.localhost", Upstream: addr(up), NoCompress: true},
	}
	c := startEdge(t, cfg)
	port := ":" + strconv.Itoa(cfg.HTTPSPort)
	app, dash := "https://shop.tiffin.localhost"+port, "https://dashboard.tiffin.localhost"+port

	for _, tc := range []struct{ url, ae, want string }{
		{app + "/", "gzip", "gzip"},
		{app + "/", "zstd", "zstd"},
		{app + "/", "gzip, deflate, br, zstd", "zstd"},
		{app + "/", "zstd;q=0.5, gzip", "gzip"},
		{app + "/", "", ""},
		{dash + "/", "gzip, zstd", "zstd"},
		{app + "/small", "gzip, zstd", ""},
		{app + "/png", "gzip, zstd", ""},
		{"https://s3.tiffin.localhost" + port + "/", "gzip, zstd", ""},
	} {
		resp, body := fetch(t, c, tc.url, tc.ae)
		if got := resp.Header.Get("Content-Encoding"); got != tc.want {
			t.Errorf("%s (Accept-Encoding %q): Content-Encoding %q, want %q", tc.url, tc.ae, got, tc.want)
		}
		if resp.StatusCode != 200 || (body != bigText && body != "short") {
			t.Errorf("%s: %d, body of %d bytes", tc.url, resp.StatusCode, len(body))
		}
		if tc.want != "" && resp.Header.Get("Vary") != "Accept-Encoding" {
			t.Errorf("%s: Vary %q", tc.url, resp.Header.Values("Vary"))
		}
	}
	// Already compressed upstream: passed through, not compressed twice.
	if resp, body := fetch(t, c, app+"/gzipped", "gzip, zstd"); resp.Header.Get("Content-Encoding") != "gzip" || body != bigText {
		t.Errorf("upstream gzip: %q, body of %d bytes", resp.Header.Get("Content-Encoding"), len(body))
	}

	// Streams: the first part arrives (decoded) while the upstream still
	// holds the rest, so every flush makes it through the compressor.
	for _, tc := range []struct{ path, ae, enc, first string }{
		{"/stream", "gzip", "gzip", bigText},
		{"/stream", "zstd", "zstd", bigText},
		{"/trickle", "gzip, zstd", "", "tick"}, // first write under the minimum: sent as is
		{"/sse", "gzip, zstd", "zstd", "data: hello"},
	} {
		req, _ := http.NewRequest("GET", app+tc.path, nil)
		req.Header.Set("Accept-Encoding", tc.ae)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if got := resp.Header.Get("Content-Encoding"); got != tc.enc {
			t.Errorf("%s %s: Content-Encoding %q, want %q", tc.path, tc.ae, got, tc.enc)
		}
		var r io.Reader = resp.Body
		switch tc.enc {
		case "gzip":
			r, err = gzip.NewReader(r)
		case "zstd":
			var d *zstd.Decoder
			d, err = zstd.NewReader(r)
			r = d
		}
		if err != nil {
			t.Fatalf("%s %s: %v", tc.path, tc.ae, err)
		}
		got := make(chan string, 1)
		go func() {
			line, _ := bufio.NewReader(r).ReadString('\n')
			got <- line
		}()
		select {
		case line := <-got:
			if strings.TrimSpace(line) != strings.TrimSpace(tc.first) {
				t.Errorf("%s %s: first line %q", tc.path, tc.ae, line)
			}
		case <-time.After(3 * time.Second):
			t.Errorf("%s %s: the first part did not arrive while the upstream held the rest", tc.path, tc.ae)
		}
		release <- struct{}{}
		resp.Body.Close()
	}
}

func TestStaticFiles(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	js := "console.log(" + strconv.Quote(bigText) + ");"
	files := map[string]string{
		"index.html":                 "<!doctype html><title>Shop</title>" + bigText,
		"assets/index-B1x9Qa2c.js":   js,
		"assets/style.css":           "body{}" + bigText,
		"_next/static/chunks/app.js": js,
		"logo.png":                   bigText,
	}
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A precompressed sidecar is sent as is.
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	io.WriteString(zw, "precompressed "+js)
	zw.Close()
	if err := os.WriteFile(filepath.Join(root, "assets", "index-B1x9Qa2c.js.gz"), gz.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	up := upstreamServer(t, "platform")
	cfg := testConfig(t, addr(up))
	cfg.Routes = []Route{
		{Host: "site.tiffin.localhost", FileRoot: root},
		{Host: "spa.tiffin.localhost", FileRoot: root, SPA: true},
	}
	c := startEdge(t, cfg)
	port := ":" + strconv.Itoa(cfg.HTTPSPort)
	site, spa := "https://site.tiffin.localhost"+port, "https://spa.tiffin.localhost"+port
	const immutable = "public, max-age=31536000, immutable"

	for _, tc := range []struct {
		url, ae          string
		code             int
		cache, enc, body string
	}{
		{site + "/", "gzip, zstd", 200, "no-cache", "zstd", files["index.html"]},
		{site + "/assets/style.css", "gzip", 200, "no-cache", "gzip", files["assets/style.css"]},
		{site + "/assets/index-B1x9Qa2c.js", "zstd", 200, immutable, "zstd", js},
		{site + "/assets/index-B1x9Qa2c.js", "gzip, zstd", 200, immutable, "gzip", "precompressed " + js},
		{site + "/_next/static/chunks/app.js", "", 200, immutable, "", js},
		{site + "/logo.png", "gzip, zstd", 200, "no-cache", "", bigText},
		{site + "/assets/index-0000ffff.js", "", 404, "no-cache", "", ""}, // not there (yet): never kept
		{spa + "/assets/index-0000ffff.js", "", 200, "no-cache", "", files["index.html"]},
		{spa + "/settings", "", 200, "no-cache", "", files["index.html"]},
	} {
		resp, body := fetch(t, c, tc.url, tc.ae)
		if resp.StatusCode != tc.code || resp.Header.Get("Cache-Control") != tc.cache || resp.Header.Get("Content-Encoding") != tc.enc {
			t.Errorf("%s (%q): %d Cache-Control %q Content-Encoding %q; want %d %q %q", tc.url, tc.ae,
				resp.StatusCode, resp.Header.Get("Cache-Control"), resp.Header.Get("Content-Encoding"), tc.code, tc.cache, tc.enc)
		}
		if tc.code == 200 && body != tc.body {
			t.Errorf("%s (%q): body of %d bytes differs", tc.url, tc.ae, len(body))
		}
	}

	// no-cache revalidates: the ETag (with the encoding's suffix) answers 304.
	for _, tc := range []struct{ path, ae, cache string }{
		{"/", "zstd", "no-cache"},
		{"/", "", "no-cache"},
		{"/assets/index-B1x9Qa2c.js", "zstd", immutable},
	} {
		resp, _ := fetch(t, c, site+tc.path, tc.ae)
		etag := resp.Header.Get("Etag")
		if etag == "" {
			t.Fatalf("%s: no ETag", tc.path)
		}
		resp, _ = fetch(t, c, site+tc.path, tc.ae, "If-None-Match", etag)
		if resp.StatusCode != 304 || resp.Header.Get("Cache-Control") != tc.cache {
			t.Errorf("%s (%q) If-None-Match %s: %d Cache-Control %q", tc.path, tc.ae, etag, resp.StatusCode, resp.Header.Get("Cache-Control"))
		}
	}
}

func TestHashedAsset(t *testing.T) {
	for p, want := range map[string]bool{
		"/assets/index-B1x9Qa2c.js":                true, // Vite
		"/assets/index-B1x9Qa2c.css":               true,
		"/static/js/main.8f3a2b1c.chunk.js":        true, // create-react-app / webpack
		"/main.5f2a9c1e7b3d4a60.js":                true, // Angular
		"/_next/static/chunks/pages/index.js":      true, // the directory says so
		"/docs/_next/static/x/_buildManifest.js":   true,
		"/_app/immutable/entry/start.js":           true,
		"/_astro/index.astro_astro_type_script.js": true,
		"/fonts/inter-latin-4a2b9c1d.woff2":        true,
		"/":                                        false,
		"/index.html":                              false,
		"/index-B1x9Qa2c.html":                     false, // HTML never
		"/assets/style.css":                        false,
		"/assets/my-component.js":                  false, // a word
		"/img/photo-20240115.jpg":                  false, // a date
		"/js/jquery-3.7.1.min.js":                  false,
		"/js/jquery.dataTables.min.js":             false,
		"/fonts/roboto-v30-latin-regular.woff2":    false,
		"/assets/index-BxQaZyWk.js":                false, // no digit: revalidated, which is safe
		"/a8f3b2c1d9e0":                            false, // no extension
		"/app.8f3a2b1c":                            false,
		"/8f3a2b1c.js":                             false, // the name itself, not a hash after it
		"/assets/chunk-8f3a2b1c/index.js":          false, // only the file name counts
		"/assets/index-B1x9Qa2c.js.map":            true,
		"/data/report-v2-final.json":               false,
		"/assets/vendor.1a2b3c4d5e6f7a8b9c0d.js":   true,
		"/assets/runtime~main.8f3a2b1c.js":         true,
	} {
		if got := hashedAsset(p); got != want {
			t.Errorf("hashedAsset(%q) = %v, want %v", p, got, want)
		}
	}
}
