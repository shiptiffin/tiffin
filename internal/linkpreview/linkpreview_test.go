package linkpreview

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func pngOf(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, color.NRGBA{20, 120, 200, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// app serves pages and files, recording the Host and User-Agent it saw.
type app struct {
	files map[string][]byte
	types map[string]string
	hdr   map[string]string // extra headers on /
	host  string
	ua    string
}

func (a *app) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		a.host, a.ua = r.Host, r.UserAgent()
		for k, v := range a.hdr {
			w.Header().Set(k, v)
		}
	}
	b, ok := a.files[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if ct := a.types[r.URL.Path]; ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Write(b)
}

func target(t *testing.T, a *app, page string, hosts ...string) Target {
	t.Helper()
	srv := httptest.NewServer(a)
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	pu, _ := url.Parse(page)
	if len(hosts) == 0 {
		hosts = []string{pu.Hostname()}
	}
	return Target{Client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		Base: base, Page: pu, Hosts: hosts,
		BoxHost: func(h string) bool { return strings.HasSuffix(h, ".shiptiffin.app") && h != "shop.shiptiffin.app" }}
}

func check(r *LinkReport, id string) LinkCheck {
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	return LinkCheck{}
}

const good = `<!doctype html><html lang="en"><head>
<title>Shop – fresh bread</title>
<meta name="description" content="Order bread from the corner bakery.">
<meta property="og:title" content="Shop – fresh bread daily">
<meta property="og:description" content="Fresh bread, every morning, from the bakery on the corner of your street.">
<meta property="og:site_name" content="Shop">
<meta property="og:image" content="https://shop.example/og.png">
<meta property="og:image:alt" content="A loaf">
<meta name="twitter:card" content="summary_large_image">
<link rel="canonical" href="https://shop.example/">
<link rel="icon" href="/icon.png">
</head><body><svg><title>not the page title</title></svg></body></html>`

func TestRunGoodPage(t *testing.T) {
	a := &app{files: map[string][]byte{"/": []byte(good), "/og.png": pngOf(t, 1200, 630), "/icon.png": pngOf(t, 32, 32)},
		types: map[string]string{"/": "text/html; charset=utf-8", "/og.png": "image/png", "/icon.png": "image/png"}}
	tg := target(t, a, "https://shop.example/", "shop.example", "shop.shiptiffin.app")
	r, th := Run(context.Background(), tg, "dep_1")
	if r.Status != StatusOK {
		t.Fatalf("status %s, checks %+v", r.Status, r.Checks)
	}
	if a.host != "shop.example" || !strings.Contains(a.ua, "Twitterbot") || !strings.Contains(a.ua, "facebookexternalhit") {
		t.Fatalf("app saw host %q ua %q", a.host, a.ua)
	}
	if r.Title != "Shop – fresh bread daily" || r.PageTitle != "Shop – fresh bread" || r.Description != "Fresh bread, every morning, from the bakery on the corner of your street." || r.MetaDesc == "" || r.SiteName != "Shop" {
		t.Fatalf("fields %+v", r)
	}
	if r.Image == nil || r.Image.Width != 1200 || r.Image.Height != 630 || !r.Image.Thumb || r.Image.Alt != "A loaf" || r.Image.Hash == "" {
		t.Fatalf("image %+v", r.Image)
	}
	if th == nil || th.MIME != "image/jpeg" || len(th.Data) == 0 || th.Hash != r.Image.Hash {
		t.Fatalf("thumb %+v", th)
	}
	if r.Icon != "https://shop.example/icon.png" {
		t.Fatalf("icon %q", r.Icon)
	}
	if len(r.Tags) < 10 || r.Tags[0].Name != "title" {
		t.Fatalf("tags %+v", r.Tags)
	}
	s := r.Summary()
	if s.Status != StatusOK || s.Image != "https://shop.example/og.png (1200×630)" || len(s.Problems) != 0 {
		t.Fatalf("summary %+v", s)
	}
}

func TestRunBarePage(t *testing.T) {
	a := &app{files: map[string][]byte{"/": []byte(`<html><head><title>Hi</title></head></html>`)},
		types: map[string]string{"/": "text/html"}, hdr: map[string]string{"X-Robots-Tag": "noindex"}}
	r, th := Run(context.Background(), target(t, a, "https://shop.example/", "shop.example", "shop.shiptiffin.app"), "dep_1")
	if r.Status != StatusBad || th != nil {
		t.Fatalf("status %s", r.Status)
	}
	want := map[string]string{"title": StatusWarn, "description": StatusWarn, "image": StatusBad, "twitter-card": StatusWarn,
		"canonical": StatusWarn, "robots": StatusBad, "icon": StatusWarn}
	for id, st := range want {
		if c := check(r, id); c.Status != st || (st != StatusOK && c.Fix == "" && id != "robots") {
			t.Errorf("%s: %+v, want %s", id, c, st)
		}
	}
	if c := check(r, "image-size"); c.ID != "" {
		t.Errorf("image-size checked without an image: %+v", c)
	}
}

func TestRunImageProblems(t *testing.T) {
	cases := []struct {
		name, img, want string
		files           map[string][]byte
		id              string
	}{
		{"relative", "/og.png", "relative path", map[string][]byte{"/og.png": nil}, "image"},
		{"localhost", "http://localhost:3000/og.png", "nobody else can reach", nil, "image"},
		{"other copy", "https://d-abc--shop.shiptiffin.app/og.png", "another copy of the app", nil, "image"},
		{"missing", "https://shop.example/gone.png", "answered 404", nil, "image"},
		{"svg", "https://shop.example/og.svg", "SVG", map[string][]byte{"/og.svg": []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"/>`)}, "image"},
		{"small", "https://shop.example/og.png", "too small for a large card", map[string][]byte{"/og.png": nil}, "image-size"},
		{"square", "https://shop.example/sq.png", "cropped", map[string][]byte{"/sq.png": nil}, "image-size"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := map[string][]byte{"/": []byte(`<title>x</title><meta property="og:image" content="` + c.img + `">`)}
			for p, b := range c.files {
				switch {
				case b != nil:
					files[p] = b
				case c.name == "small":
					files[p] = pngOf(t, 400, 210)
				case c.name == "square":
					files[p] = pngOf(t, 800, 800)
				default:
					files[p] = pngOf(t, 1200, 630)
				}
			}
			a := &app{files: files, types: map[string]string{"/": "text/html"}}
			r, _ := Run(context.Background(), target(t, a, "https://shop.example/"), "dep_1")
			got := check(r, c.id)
			if got.Status == StatusOK || !strings.Contains(got.Detail, c.want) {
				t.Fatalf("%s: %+v", c.id, got)
			}
		})
	}
}

func TestRunOutsideImageNotFetchedWithoutClient(t *testing.T) {
	a := &app{files: map[string][]byte{"/": []byte(`<title>x</title><meta property="og:image" content="https://cdn.example.net/og.png">`)},
		types: map[string]string{"/": "text/html"}}
	r, _ := Run(context.Background(), target(t, a, "https://shop.example/"), "dep_1")
	if c := check(r, "image"); c.Status != StatusWarn || !strings.Contains(c.Detail, "cdn.example.net") {
		t.Fatalf("%+v", c)
	}
}

func TestRunPreviewRelaxed(t *testing.T) {
	page := `<title>x</title><meta name="robots" content="noindex"><link rel="canonical" href="https://shop.example/">`
	a := &app{files: map[string][]byte{"/": []byte(page)}, types: map[string]string{"/": "text/html"}}
	tg := target(t, a, "https://pr-1--shop.shiptiffin.app/")
	tg.Preview = true
	r, _ := Run(context.Background(), tg, "dep_1")
	if c := check(r, "robots"); c.ID != "" {
		t.Fatalf("robots checked on a preview: %+v", c)
	}
	if c := check(r, "canonical"); c.Status != StatusOK {
		t.Fatalf("canonical: %+v", c)
	}
}

func TestRunUnreadable(t *testing.T) {
	a := &app{files: map[string][]byte{"/": []byte(`{"ok":true}`)}, types: map[string]string{"/": "application/json"}}
	r, _ := Run(context.Background(), target(t, a, "https://api.example/"), "dep_1")
	if r.Status != StatusUnreadable || !strings.Contains(r.Reason, "application/json") {
		t.Fatalf("%+v", r)
	}
	a = &app{files: map[string][]byte{}}
	r, _ = Run(context.Background(), target(t, a, "https://api.example/"), "dep_1")
	if r.Status != StatusUnreadable || !strings.Contains(r.Reason, "404") {
		t.Fatalf("%+v", r)
	}
	if s := r.Summary(); len(s.Problems) != 1 {
		t.Fatalf("summary %+v", s)
	}
}

func TestRunFollowsRedirectOnApp(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "https://shop.example/en", http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<title>English</title>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	base, _ := url.Parse(srv.URL)
	pu, _ := url.Parse("https://shop.example/")
	tg := Target{Client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		Base: base, Page: pu, Hosts: []string{"shop.example"}}
	r, _ := Run(context.Background(), tg, "dep_1")
	if r.PageTitle != "English" {
		t.Fatalf("%+v", r)
	}
}

func TestCompare(t *testing.T) {
	old := &LinkReport{Status: StatusOK, Title: "Shop", Description: "Bread", Card: "summary_large_image",
		Image:  &ShareImage{URL: "https://shop.example/og.png", Hash: "a", Width: 1200, Height: 630},
		Checks: []LinkCheck{{ID: "image", Label: "Share image", Status: StatusOK}, {ID: "robots", Label: "Search engines", Status: StatusOK}}}
	now := &LinkReport{Status: StatusBad, Title: "Shop!", Description: "Bread",
		Checks: []LinkCheck{{ID: "image", Label: "Share image", Status: StatusBad}, {ID: "robots", Label: "Search engines", Status: StatusBad}}}
	now.Compare(old, &LinkBaseline{Deploy: "dep_0", Version: 3})
	got := map[string]LinkChange{}
	for _, c := range now.Changes {
		got[c.Field] = c
	}
	if len(now.Changes) != 4 || got["Title"].Worse || !got["Share image"].Worse || got["Share image"].To != "" ||
		!got["X card"].Worse || !got["Search engines"].Worse || now.Compared.Version != 3 {
		t.Fatalf("%+v", now.Changes)
	}
	s := now.Summary()
	if !s.Worse || len(s.Changes) != 4 || !strings.Contains(strings.Join(s.Changes, "\n"), "Share image removed (was https://shop.example/og.png)") {
		t.Fatalf("%+v", s)
	}

	same := &LinkReport{Status: StatusOK, Title: "Shop", Description: "Bread", Card: "summary_large_image",
		Image: &ShareImage{URL: "https://shop.example/og.png", Hash: "b", Width: 1200, Height: 630}, Checks: old.Checks}
	same.Compare(old, &LinkBaseline{Deploy: "dep_0"})
	if len(same.Changes) != 1 || same.Changes[0].Worse || !strings.Contains(same.Changes[0].To, "new picture") {
		t.Fatalf("%+v", same.Changes)
	}
}

func TestPrivateHost(t *testing.T) {
	for h, want := range map[string]bool{"localhost": true, "app.localhost": true, "10.0.0.4": true, "127.0.0.1": true, "[::1]": true,
		"box.internal": true, "web": true, "shop.example": false, "1.1.1.1": false} {
		if privateHost(h) != want {
			t.Errorf("%s: want %v", h, want)
		}
	}
}

func TestOutsideClientRefusesPrivate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("x")) }))
	defer srv.Close()
	_, err := OutsideClient(nil).Get(srv.URL)
	if err == nil || !strings.Contains(err.Error(), "isn't public") {
		t.Fatalf("err %v", err)
	}
}

func TestRunLengthsAndXTags(t *testing.T) {
	long := strings.Repeat("word ", 14) // 70 characters
	page := `<title>` + long + `</title><meta name="description" content="` + strings.Repeat("x", 170) + `">` +
		`<meta property="og:title" content="Hi"><meta property="og:description" content="Short.">` +
		`<meta name="twitter:title" content="On X"><meta property="og:image" content="https://shop.example/og.png">` +
		`<meta name="twitter:image" content="https://shop.example/x.png">`
	a := &app{files: map[string][]byte{"/": []byte(page), "/og.png": pngOf(t, 1200, 630)}, types: map[string]string{"/": "text/html"}}
	r, _ := Run(context.Background(), target(t, a, "https://shop.example/"), "dep_1")
	want := map[string]struct{ status, has string }{
		"title":            {StatusWarn, "too short"},
		"page-title":       {StatusWarn, "cut titles off"},
		"description":      {StatusOK, "short, but fine"},
		"meta-description": {StatusWarn, "170 characters"},
		"site-name":        {StatusOK, "shop.example instead"},
		"x-title":          {StatusOK, "Uses its own title: “On X”"},
		"x-description":    {StatusOK, "X uses og:description"},
		"x-image":          {StatusOK, "its own image, https://shop.example/x.png"},
	}
	for id, w := range want {
		if c := check(r, id); c.Status != w.status || !strings.Contains(c.Detail, w.has) {
			t.Errorf("%s: %+v, want %s with %q", id, c, w.status, w.has)
		}
	}
	if r.XTitle != "On X" || r.XDesc != "Short." {
		t.Fatalf("X shows %q / %q", r.XTitle, r.XDesc)
	}
	// Length problems are suggestions: never worse than a warning.
	for _, c := range r.Checks {
		if c.Status == StatusBad {
			t.Errorf("bad: %+v", c)
		}
	}
}
