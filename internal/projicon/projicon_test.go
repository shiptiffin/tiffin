package projicon

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// pngOf draws a w×h PNG: a filled square of c, transparent around it
// when inset is set.
func pngOf(t testing.TB, w, h int, c color.NRGBA, inset bool) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			if inset && (x < w/4 || x >= w*3/4 || y < h/4 || y >= h*3/4) {
				continue
			}
			img.SetNRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// icoOf builds an ICO with a 16 px 32-bit bitmap and a 48 px PNG inside.
func icoOf(t testing.TB) []byte {
	t.Helper()
	big := pngOf(t, 48, 48, color.NRGBA{200, 30, 30, 255}, false)
	// 16×16 BGRA bitmap, bottom-up, height doubled, plus an AND mask.
	w := 16
	var dib bytes.Buffer
	hdr := make([]byte, 40)
	binary.LittleEndian.PutUint32(hdr[0:], 40)
	binary.LittleEndian.PutUint32(hdr[4:], uint32(w))
	binary.LittleEndian.PutUint32(hdr[8:], uint32(2*w))
	binary.LittleEndian.PutUint16(hdr[12:], 1)
	binary.LittleEndian.PutUint16(hdr[14:], 32)
	dib.Write(hdr)
	for range w * w {
		dib.Write([]byte{30, 30, 200, 255}) // BGRA: red
	}
	dib.Write(make([]byte, 4*w)) // mask rows (4 bytes each), all opaque
	entries := [][]byte{dib.Bytes(), big}
	sizes := []int{16, 48}
	bpp := []int{32, 32}
	var out bytes.Buffer
	head := []byte{0, 0, 1, 0, byte(len(entries)), 0}
	out.Write(head)
	off := 6 + 16*len(entries)
	for i, e := range entries {
		d := make([]byte, 16)
		d[0], d[1] = byte(sizes[i]), byte(sizes[i])
		binary.LittleEndian.PutUint16(d[4:], 1)
		binary.LittleEndian.PutUint16(d[6:], uint16(bpp[i]))
		binary.LittleEndian.PutUint32(d[8:], uint32(len(e)))
		binary.LittleEndian.PutUint32(d[12:], uint32(off))
		out.Write(d)
		off += len(e)
	}
	for _, e := range entries {
		out.Write(e)
	}
	return out.Bytes()
}

const goodSVG = `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><style>@media (prefers-color-scheme: dark){path{fill:#fff}}</style><path d="M0 0h32v32H0z" fill="#111"/></svg>`

// app is a test app: paths to (content type, body), plus special cases.
type app struct {
	files map[string][2]string
	slow  map[string]bool
	hits  map[string]int
	mu    sync.Mutex
}

func (a *app) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	if a.hits == nil {
		a.hits = map[string]int{}
	}
	a.hits[r.URL.Path]++
	a.mu.Unlock()
	if a.slow[r.URL.Path] {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		return
	}
	f, ok := a.files[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(f[0], "redirect:") {
		http.Redirect(w, r, strings.TrimPrefix(f[0], "redirect:"), http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", f[0])
	_, _ = w.Write([]byte(f[1]))
}

func serve(t *testing.T, a *app) Fetcher {
	t.Helper()
	srv := httptest.NewServer(a)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return Fetcher{
		Client:  &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		Base:    u,
		Hosts:   []string{"shop.box.test"},
		Timeout: 300 * time.Millisecond,
	}
}

func page(head string) [2]string {
	return [2]string{"text/html; charset=utf-8", "<!doctype html><html><head>" + head + "</head><body><link rel=icon href=/late.png></body></html>"}
}

func TestInfer(t *testing.T) {
	red := color.NRGBA{220, 40, 40, 255}
	png16 := string(pngOf(t, 16, 16, red, false))
	png180 := string(pngOf(t, 180, 180, red, false))
	png512 := string(pngOf(t, 512, 512, red, false))
	huge := strings.Repeat("x", MaxBytes+10)

	cases := []struct {
		name       string
		files      map[string][2]string
		slow       []string
		wantSource string // "" for none
		wantSVG    bool
		wantRaster bool
		reason     string
		none       bool
	}{
		{
			name: "svg icon with an apple-touch-icon twin",
			files: map[string][2]string{
				"/":            page(`<link rel="icon" href="/favicon.ico" sizes="32x32"><link rel="icon" href="/icon.svg" type="image/svg+xml"><link rel="apple-touch-icon" href="/apple.png">`),
				"/icon.svg":    {"image/svg+xml", goodSVG},
				"/apple.png":   {"image/png", png180},
				"/favicon.ico": {"image/x-icon", string(icoOf(t))},
			},
			wantSource: "/icon.svg", wantSVG: true, wantRaster: true,
		},
		{
			name: "manifest icons, largest wins",
			files: map[string][2]string{
				"/":                 page(`<link rel="manifest" href="/site.webmanifest">`),
				"/site.webmanifest": {"application/manifest+json", `{"icons":[{"src":"/i192.png","sizes":"192x192"},{"src":"i512.png","sizes":"512x512","purpose":"any maskable"},{"src":"/mono.png","sizes":"1024x1024","purpose":"monochrome"}]}`},
				"/i192.png":         {"image/png", png180},
				"/i512.png":         {"image/png", png512},
			},
			wantSource: "/i512.png",
		},
		{
			name: "only /favicon.ico (an ICO with a bitmap and a PNG inside)",
			files: map[string][2]string{
				"/":            page(`<title>x</title>`),
				"/favicon.ico": {"image/vnd.microsoft.icon", string(icoOf(t))},
			},
			wantSource: "/favicon.ico",
		},
		{
			name: "oversized icon is skipped for the next one",
			files: map[string][2]string{
				"/":          page(`<link rel="icon" href="/big.png" sizes="512x512"><link rel="icon" href="/small.png" sizes="16x16">`),
				"/big.png":   {"image/png", huge},
				"/small.png": {"image/png", png16},
			},
			wantSource: "/small.png",
		},
		{
			name: "wrong MIME: an HTML page at the icon's address",
			files: map[string][2]string{
				"/":            page(`<link rel="icon" href="/icon.png">`),
				"/icon.png":    {"text/html", png180},
				"/favicon.ico": {"text/html", "<html>not found</html>"},
			},
			reason: "not an image", none: true,
		},
		{
			name: "image MIME but not an image",
			files: map[string][2]string{
				"/":         page(`<link rel="icon" href="/icon.png">`),
				"/icon.png": {"image/png", "<html><script>alert(1)</script></html>"},
			},
			reason: "not a PNG", none: true,
		},
		{
			name: "malicious SVG is cleaned, not refused",
			files: map[string][2]string{
				"/": page(`<link rel="icon" href="/evil.svg">`),
				"/evil.svg": {"image/svg+xml", `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" viewBox="0 0 10 10" onload="alert(1)">` +
					`<script>alert(2)</script><foreignObject><iframe src="https://evil.test"/></foreignObject>` +
					`<image href="https://evil.test/x.png"/><use xlink:href="https://evil.test/s.svg#a"/><a href="javascript:alert(3)"><rect width="10" height="10"/></a>` +
					`<rect width="5" height="5" fill="url(https://evil.test/p)" style="fill:url(#ok)"/><style>@import url(https://evil.test/c.css);</style>` +
					`<animate attributeName="href" to="javascript:alert(4)"/><circle r="4" fill="red"/></svg>`},
			},
			wantSource: "/evil.svg", wantSVG: true,
		},
		{
			name: "redirects on the app are followed; public host maps to the app",
			files: map[string][2]string{
				"/":                {"redirect:/home", ""},
				"/home":            page(`<link rel="icon" href="https://shop.box.test/brand/icon.png">`),
				"/brand/icon.png":  {"redirect:/static/icon.png", ""},
				"/static/icon.png": {"image/png", png180},
			},
			wantSource: "/static/icon.png",
		},
		{
			name: "redirect off the app is refused; links to other sites are ignored",
			files: map[string][2]string{
				"/":            page(`<link rel="icon" href="https://cdn.other.test/icon.png"><link rel="icon" href="/go.png">`),
				"/go.png":      {"redirect:https://evil.test/icon.png", ""},
				"/favicon.ico": {"image/png", png16},
			},
			wantSource: "/favicon.ico",
		},
		{
			name: "a slow icon times out and the next is used",
			files: map[string][2]string{
				"/":            page(`<link rel="icon" href="/slow.svg">`),
				"/favicon.ico": {"image/png", png16},
			},
			slow:       []string{"/slow.svg"},
			wantSource: "/favicon.ico",
		},
		{
			name: "inline data: URL",
			files: map[string][2]string{
				"/": page(`<link rel="icon" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 8 8'%3E%3Ccircle r='4' cx='4' cy='4'/%3E%3C/svg%3E">`),
			},
			wantSource: "inline", wantSVG: true,
		},
		{
			name:   "an API's home page isn't HTML",
			files:  map[string][2]string{"/": {"application/json", `{"ok":true}`}, "/favicon.ico": {"image/png", png16}},
			reason: "isn't HTML", none: true,
		},
		{
			name:   "home page times out",
			files:  map[string][2]string{},
			slow:   []string{"/"},
			reason: "did not answer",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &app{files: c.files, slow: map[string]bool{}}
			for _, s := range c.slow {
				a.slow[s] = true
			}
			res, err := Infer(context.Background(), serve(t, a))
			if err != nil {
				t.Fatal(err)
			}
			if c.wantSource == "" {
				if res.Icon != nil {
					t.Fatalf("want no icon, got one from %s", res.Source)
				}
				if !strings.Contains(res.Reason, c.reason) {
					t.Fatalf("reason %q, want it to mention %q", res.Reason, c.reason)
				}
				if res.None != c.none {
					t.Fatalf("None = %v, want %v", res.None, c.none)
				}
				return
			}
			if res.Icon == nil {
				t.Fatalf("no icon: %s", res.Reason)
			}
			if res.Source != c.wantSource {
				t.Fatalf("source %q, want %q", res.Source, c.wantSource)
			}
			if res.Icon.SVG() != c.wantSVG {
				t.Fatalf("svg = %v, want %v", res.Icon.SVG(), c.wantSVG)
			}
			if (res.Raster != nil) != c.wantRaster {
				t.Fatalf("raster twin = %v, want %v", res.Raster != nil, c.wantRaster)
			}
			if a.hits["/late.png"] > 0 {
				t.Fatal("read links past <body>")
			}
			if res.Icon.SVG() {
				for _, bad := range []string{"script", "onload", "foreignObject", "iframe", "evil", "javascript", "@import", "animate", "<image", "<a "} {
					if strings.Contains(string(res.Icon.Data), bad) {
						t.Fatalf("sanitised SVG still has %q:\n%s", bad, res.Icon.Data)
					}
				}
			} else if Sniff(res.Icon.Data) != "png" {
				t.Fatal("raster icons are stored as PNG")
			}
		})
	}
}

func TestInferStopsWhenGood(t *testing.T) {
	a := &app{files: map[string][2]string{
		"/":            page(`<link rel="icon" href="/a.svg"><link rel="apple-touch-icon" href="/b.png"><link rel="icon" href="/c.png" sizes="32x32">`),
		"/a.svg":       {"image/svg+xml", goodSVG},
		"/b.png":       {"image/png", string(pngOf(t, 180, 180, color.NRGBA{0, 0, 0, 255}, false))},
		"/c.png":       {"image/png", string(pngOf(t, 32, 32, color.NRGBA{0, 0, 0, 255}, false))},
		"/favicon.ico": {"image/png", string(pngOf(t, 16, 16, color.NRGBA{0, 0, 0, 255}, false))},
	}}
	if _, err := Infer(context.Background(), serve(t, a)); err != nil {
		t.Fatal(err)
	}
	if a.hits["/c.png"] != 0 || a.hits["/favicon.ico"] != 0 {
		t.Fatalf("kept fetching after an SVG and a big raster: %v", a.hits)
	}
}

func TestNormalize(t *testing.T) {
	big := pngOf(t, 1024, 512, color.NRGBA{0, 0, 0, 255}, false)
	img, err := Normalize(big)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := png.DecodeConfig(bytes.NewReader(img.Data))
	if cfg.Width != storeSide || cfg.Height != storeSide || img.Width != 1024 || img.Height != 512 {
		t.Fatalf("stored %dx%d from %dx%d", cfg.Width, cfg.Height, img.Width, img.Height)
	}
	// A dark mark on a transparent ground is "dark"; white is "light".
	if img, _ := Normalize(pngOf(t, 64, 64, color.NRGBA{0, 0, 0, 255}, true)); img.Tone != "dark" {
		t.Fatalf("tone %q, want dark", img.Tone)
	}
	if img, _ := Normalize(pngOf(t, 64, 64, color.NRGBA{255, 255, 255, 255}, true)); img.Tone != "light" {
		t.Fatalf("tone %q, want light", img.Tone)
	}
	if img, _ := Normalize(pngOf(t, 64, 64, color.NRGBA{255, 255, 255, 255}, false)); img.Tone != "" {
		t.Fatalf("opaque icon tone %q, want none", img.Tone)
	}
	// Decompression bombs: a tiny file declaring a huge image.
	bomb := pngOf(t, 1, 1, color.NRGBA{}, false)
	binary.BigEndian.PutUint32(bomb[16:], 30000)
	binary.BigEndian.PutUint32(bomb[20:], 30000)
	if _, err := Normalize(bomb); err == nil {
		t.Fatal("accepted a 30000 px image")
	}
	if _, err := Normalize(bytes.Repeat([]byte{1}, MaxBytes+1)); err != ErrTooLarge {
		t.Fatalf("oversized: %v", err)
	}
	if _, err := Normalize([]byte("GIF89a garbage")); err == nil {
		t.Fatal("accepted a broken GIF")
	}
	ico, err := Normalize(icoOf(t))
	if err != nil || ico.Width != 48 {
		t.Fatalf("ico: %v, width %d (want the 48 px image)", err, ico.Width)
	}
	e, err := EmailPNG(ico.Data)
	if err != nil {
		t.Fatal(err)
	}
	if cfg, _ := png.DecodeConfig(bytes.NewReader(e)); cfg.Width != EmailSide {
		t.Fatalf("email png is %d px", cfg.Width)
	}
}

func TestSanitizeSVG(t *testing.T) {
	for _, bad := range []string{
		`<!DOCTYPE svg [<!ENTITY a "aaaa">]><svg xmlns="http://www.w3.org/2000/svg">&a;</svg>`,
		`<html><svg/></html>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><g>`,
	} {
		if _, _, _, err := SanitizeSVG([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	out, w, h, err := SanitizeSVG([]byte(goodSVG))
	if err != nil || w != 32 || h != 32 {
		t.Fatalf("good svg: %v %dx%d", err, w, h)
	}
	if !strings.Contains(string(out), "prefers-color-scheme") || !strings.Contains(string(out), `fill="#111"`) {
		t.Fatalf("lost the drawing: %s", out)
	}
	out, _, _, _ = SanitizeSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><style>.a{fill:url(https://x.test/a)}</style><rect style="fill:u\72l(https://x.test)"/></svg>`))
	if strings.Contains(string(out), "x.test") {
		t.Fatalf("kept an outside reference: %s", out)
	}
}

func TestMonogram(t *testing.T) {
	for in, want := range map[string]string{"shop": "S", "my-shop": "MS", "a-b-c": "AB", "x1": "X", "9lives": "9"} {
		if got := Letters(in); got != want {
			t.Errorf("Letters(%q) = %q, want %q", in, got, want)
		}
	}
	for e := range enamels {
		g, l := MonogramColors(e)
		if c := Contrast(g, l); c < 4.5 {
			t.Errorf("%s: letters on ground are %.2f:1, want ≥ 4.5", e, c)
		}
	}
	b, err := MonogramPNG("my-shop", "teal", EmailSide)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil || img.Bounds().Dx() != EmailSide {
		t.Fatalf("monogram png: %v", err)
	}
	if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
		t.Fatal("corners should be transparent (rounded tile)")
	}
	g, l := MonogramColors("teal")
	inked := 0
	for y := range EmailSide {
		for x := range EmailSide {
			if c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA); c == l {
				inked++
			}
		}
	}
	if inked < 200 || g == l {
		t.Fatalf("letters not drawn (%d pixels)", inked)
	}
}

func TestStore(t *testing.T) {
	kv := newMemKV()
	ctx := context.Background()
	img, _ := Normalize(pngOf(t, 64, 64, color.NRGBA{1, 2, 3, 255}, false))
	now := time.Now()
	_, changed, err := SaveInferred(ctx, kv, "shop", "web", "dep_1", Result{Icon: img, Source: "/a.png"}, now)
	if err != nil || !changed {
		t.Fatalf("first save: %v %v", changed, err)
	}
	r, changed, _ := SaveInferred(ctx, kv, "shop", "web", "dep_2", Result{Icon: img, Source: "/a.png"}, now.Add(time.Hour))
	if changed || !r.Inferred.UpdatedAt.Equal(now.UTC()) {
		t.Fatal("same image should not replace the icon")
	}
	if s, which := r.Showing(); s == nil || which != "inferred" {
		t.Fatalf("showing %v", which)
	}
	// An app that didn't answer keeps its icon; one that answered without one loses it.
	r, _, _ = SaveInferred(ctx, kv, "shop", "web", "dep_3", Result{Reason: "did not answer"}, now)
	if r.Inferred == nil {
		t.Fatal("lost the icon on a failed check")
	}
	r, _, _ = SaveInferred(ctx, kv, "shop", "web", "dep_4", Result{Reason: "none", None: true}, now)
	if r.Inferred != nil {
		t.Fatal("kept an icon the app no longer has")
	}
	id, _ := PublicID(ctx, kv, "shop")
	id2, _ := PublicID(ctx, kv, "shop")
	if id == "" || id != id2 || strings.Contains(id, "shop") {
		t.Fatalf("public id %q / %q", id, id2)
	}
	if p, _ := ByPublicID(ctx, kv, id); p != "shop" {
		t.Fatalf("by id: %q", p)
	}
	_ = Delete(ctx, kv, "shop")
	if p, _ := ByPublicID(ctx, kv, id); p != "" {
		t.Fatal("public id survived the project")
	}
}

type memKV struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newMemKV() *memKV { return &memKV{m: map[string][]byte{}} }

func (k *memKV) KVGet(_ context.Context, ns, key string) ([]byte, bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.m[ns+"/"+key]
	return v, ok, nil
}

func (k *memKV) KVPut(_ context.Context, ns, key string, v []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.m[ns+"/"+key] = v
	return nil
}

func (k *memKV) KVDelete(_ context.Context, ns, key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.m, ns+"/"+key)
	return nil
}
