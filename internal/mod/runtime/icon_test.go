package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/projicon"
)

func testPNG(t *testing.T, side int, c color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, side, side))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

// waitIcon polls until the project's inferred icon matches ok.
func waitIcon(t *testing.T, h *harness, ok func(*projicon.Record) bool) *projicon.Record {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		r, err := projicon.Get(context.Background(), h.p.DB, "shop")
		if err != nil {
			t.Fatal(err)
		}
		if ok(r) {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("icon never settled: %+v (check %+v)", r.Inferred, r.Checked)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestProjectIcon(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><circle cx="8" cy="8" r="8" fill="#e33"/><script>alert(1)</script></svg>`
	apple := string(testPNG(t, 180, color.NRGBA{230, 50, 50, 255}))

	// A production deploy of the app at the project's address: the box finds its icon.
	h.deploy("site", "", map[string]string{
		"index.html": `<!doctype html><head><link rel="icon" href="/icon.svg"><link rel="apple-touch-icon" href="https://shop.tiffin.localhost/apple.png"></head><body>hi</body>`,
		"icon.svg":   svg,
		"apple.png":  apple,
	})
	r := waitIcon(t, h, func(r *projicon.Record) bool { return r.Inferred != nil })
	if r.Inferred.MIME != "image/svg+xml" || r.Inferred.Source != "/icon.svg" || r.Inferred.App != "site" || len(r.Inferred.Raster) == 0 {
		t.Fatalf("inferred: %+v", r.Inferred)
	}
	if strings.Contains(string(r.Inferred.Data), "script") {
		t.Fatal("stored SVG still has its script")
	}
	first := r.Inferred.UpdatedAt

	// The same icon on the next deploy changes nothing; a new one replaces it.
	h.deploy("site", "", map[string]string{"index.html": `<link rel="icon" href="/icon.svg"><link rel="apple-touch-icon" href="/apple.png">`, "icon.svg": svg, "apple.png": apple})
	r = waitIcon(t, h, func(r *projicon.Record) bool { return r.Checked != nil && r.Checked.At.After(first) })
	if !r.Inferred.UpdatedAt.Equal(first) {
		t.Fatal("an unchanged icon was replaced")
	}
	h.deploy("site", "", map[string]string{"index.html": `<p>no icon links</p>`, "favicon.ico": string(testPNG(t, 64, color.NRGBA{0, 90, 200, 255}))})
	r = waitIcon(t, h, func(r *projicon.Record) bool { return r.Inferred != nil && r.Inferred.Source == "/favicon.ico" })
	if r.Inferred.MIME != "image/png" {
		t.Fatalf("favicon: %+v", r.Inferred)
	}

	// A deploy of an API at a path (shop/api) doesn't touch the icon.
	if got := iconApp("shop", h.r.appSpecs(ctx, "shop"), h.r.splitRoute); got != "site" {
		t.Fatalf("icon app %q, want site", got)
	}
	if got := iconApp("shop", map[string]manifest.App{"b": {Routes: []string{"x.example.com"}}, "a": {Role: manifest.RoleWorker}}, h.r.splitRoute); got != "b" {
		t.Fatalf("icon app %q, want the first web app", got)
	}

	// The API.
	owner, _, err := h.p.Tokens.Bootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.New(api.Deps{DB: h.p.DB, Engine: h.p.Engine, Tokens: h.p.Tokens, Platform: h.p}).Handler())
	defer srv.Close()
	call := func(method, path string, body any, auth bool) (*http.Response, []byte) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rd)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if auth {
			req.Header.Set("Authorization", "Bearer "+owner)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res, b
	}
	info := func(b []byte) IconInfo {
		var i IconInfo
		if err := json.Unmarshal(b, &i); err != nil {
			t.Fatalf("%v: %s", err, b)
		}
		return i
	}
	res, b := call("GET", "/v1/projects/shop/icon", nil, true)
	i := info(b)
	if res.StatusCode != 200 || i.Showing != "inferred" || i.Image == nil || i.Letters != "S" || !strings.HasPrefix(i.PublicURL, "https://dashboard.tiffin.localhost:8443/v1/icons/") {
		t.Fatalf("get: %d %s", res.StatusCode, b)
	}
	if res, _ := call("GET", "/v1/projects/shop/icon", nil, false); res.StatusCode != 401 {
		t.Fatalf("icon info without a token: %d", res.StatusCode)
	}
	// The image, cached by its hash.
	res, b = call("GET", i.Image.URL, nil, true)
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/png" || !strings.Contains(res.Header.Get("Cache-Control"), "immutable") || projicon.Sniff(b) != "png" {
		t.Fatalf("image: %d %v", res.StatusCode, res.Header)
	}
	req, _ := http.NewRequest("GET", srv.URL+i.Image.URL, nil)
	req.Header.Set("Authorization", "Bearer "+owner)
	req.Header.Set("If-None-Match", res.Header.Get("ETag"))
	if res, _ := http.DefaultClient.Do(req); res.StatusCode != 304 {
		t.Fatalf("If-None-Match: %d", res.StatusCode)
	}

	// Upload: an SVG with a PNG twin; a bad file is refused with a reason.
	res, b = call("PUT", "/v1/projects/shop/icon", map[string]any{"image": base64.StdEncoding.EncodeToString([]byte("<html>nope</html>"))}, true)
	if res.StatusCode != 422 || !strings.Contains(string(b), "not a PNG") {
		t.Fatalf("bad upload: %d %s", res.StatusCode, b)
	}
	res, b = call("PUT", "/v1/projects/shop/icon", map[string]any{"image": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("a"), projicon.MaxBytes+1))}, true)
	if res.StatusCode != 413 {
		t.Fatalf("oversized upload: %d %s", res.StatusCode, b)
	}
	res, b = call("PUT", "/v1/projects/shop/icon", map[string]any{
		"image": base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 4 4" onload="x()"><rect width="4" height="4"/></svg>`)),
		"png":   base64.StdEncoding.EncodeToString(testPNG(t, 128, color.NRGBA{0, 0, 0, 255})),
	}, true)
	i = info(b)
	if res.StatusCode != 200 || i.Showing != "upload" || i.Image.MIME != "image/svg+xml" || i.Found == nil {
		t.Fatalf("upload: %d %s", res.StatusCode, b)
	}
	res, b = call("GET", i.Image.URL, nil, true)
	if !strings.Contains(res.Header.Get("Content-Security-Policy"), "sandbox") || strings.Contains(string(b), "onload") {
		t.Fatalf("svg served: %v %s", res.Header, b)
	}

	// The public PNG: no token, no project name, 192 px.
	pub := strings.TrimPrefix(i.PublicURL, "https://dashboard.tiffin.localhost:8443")
	res, b = call("GET", pub, nil, false)
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/png" || strings.Contains(pub, "shop") {
		t.Fatalf("public: %d %s", res.StatusCode, pub)
	}
	if cfg, err := png.DecodeConfig(bytes.NewReader(b)); err != nil || cfg.Width != projicon.EmailSide {
		t.Fatalf("public png: %v %d", err, cfg.Width)
	}
	if res, _ := call("GET", "/v1/icons/aaaaaaaaaaaaaaaaaaaaaaaa.png", nil, false); res.StatusCode != 404 {
		t.Fatalf("unknown public id: %d", res.StatusCode)
	}

	// Letters, then back to the app's icon.
	res, b = call("POST", "/v1/projects/shop/icon/reset", map[string]any{"use": "letter"}, true)
	if i = info(b); res.StatusCode != 200 || i.Showing != "letter" || i.Image != nil || i.Found == nil {
		t.Fatalf("letter: %d %s", res.StatusCode, b)
	}
	if res, _ := call("GET", "/v1/projects/shop/icon/image", nil, true); res.StatusCode != 404 {
		t.Fatalf("letters have no image: %d", res.StatusCode)
	}
	if res, b := call("GET", pub, nil, false); res.StatusCode != 200 || len(b) == 0 {
		t.Fatalf("public letters: %d", res.StatusCode)
	}
	res, b = call("POST", "/v1/projects/shop/icon/reset", map[string]any{"use": "app"}, true)
	if i = info(b); res.StatusCode != 200 || i.Showing != "inferred" || i.Image.Source != "/favicon.ico" {
		t.Fatalf("app: %d %s", res.StatusCode, b)
	}
	if res, _ := call("GET", "/v1/projects/nope/icon", nil, true); res.StatusCode != 404 {
		t.Fatalf("missing project: %d", res.StatusCode)
	}

	// Deleting the project forgets its icon and its public address.
	if err := h.m.ProjectDeleted(ctx, h.p, "shop"); err != nil {
		t.Fatal(err)
	}
	if res, _ := call("GET", pub, nil, false); res.StatusCode != 404 {
		t.Fatalf("public icon after delete: %d", res.StatusCode)
	}
}
