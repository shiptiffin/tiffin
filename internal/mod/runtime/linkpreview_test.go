package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/linkpreview"
)

func ogPNG(t *testing.T, w, h int) string {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// waitLinkPreview waits for the background check of a deploy that just
// went live.
func (h *harness) waitLinkPreview(app, id string) *linkpreview.LinkReport {
	h.t.Helper()
	for range 100 {
		if rep, _ := h.r.st.getLinkPreview(context.Background(), "shop", app, id); rep != nil {
			return rep
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatalf("no link preview for %s", id)
	return nil
}

// Every web deploy is checked once live: kept with the deploy, compared
// with the version before, shown in brief on the deploy, and gone with it.
func TestLinkPreview(t *testing.T) {
	h := newHarness(t)
	page := `<title>Shop</title><meta name="description" content="Bread."><meta property="og:title" content="Shop – fresh bread daily">` +
		`<meta property="og:description" content="Bread."><meta property="og:image" content="https://shop.tiffin.localhost:8443/og.png">` +
		`<meta name="twitter:card" content="summary_large_image"><link rel="icon" href="/icon.png">`
	d1 := h.deploy("site", "", map[string]string{"public/index.html": page, "public/og.png": ogPNG(t, 1200, 630), "public/icon.png": ogPNG(t, 32, 32)})
	rep := h.waitLinkPreview("site", d1.ID)
	if rep.Status != linkpreview.StatusOK || rep.URL != "https://shop.tiffin.localhost:8443/" || rep.Image == nil || !rep.Image.Thumb || rep.Compared != nil {
		t.Fatalf("first report: %+v %+v", rep, rep.Checks)
	}

	d2 := h.deploy("site", "", map[string]string{"public/index.html": "<title>Shop</title>"})
	rep2 := h.waitLinkPreview("site", d2.ID)
	if rep2.Status != linkpreview.StatusBad || rep2.Compared == nil || rep2.Compared.Deploy != d1.ID || rep2.Compared.Version != d1.Version {
		t.Fatalf("second report: %+v", rep2)
	}
	if s := rep2.Summary(); !s.Worse || len(s.Changes) == 0 {
		t.Fatalf("summary: %+v", s)
	}
	// Workers aren't checked.
	w := h.deploy("jobs", "", map[string]string{"index.ts": "x"})
	time.Sleep(50 * time.Millisecond)
	if rep, _ := h.r.st.getLinkPreview(context.Background(), "shop", "jobs", w.ID); rep != nil {
		t.Fatalf("a worker was checked: %+v", rep)
	}

	owner, _, err := h.p.Tokens.Bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a := api.New(api.Deps{DB: h.p.DB, Engine: h.p.Engine, Tokens: h.p.Tokens, Platform: h.p})
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	call := func(method, path string) (int, []byte, http.Header) {
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+owner)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, b, res.Header
	}
	base := "/v1/projects/shop/apps/site/deploys/"

	// deploys get carries it in brief.
	code, body, _ := call("GET", base+d2.ID)
	var got Deploy
	if code != 200 || json.Unmarshal(body, &got) != nil || got.LinkPreview == nil || got.LinkPreview.Status != "bad" || !got.LinkPreview.Worse {
		t.Fatalf("deploy get: %d %s", code, body)
	}
	// The whole report, with the thumbnail's address; only the live deploy can be checked again.
	code, body, _ = call("GET", base+d1.ID+"/link-preview")
	var lp LinkPreview
	if code != 200 || json.Unmarshal(body, &lp) != nil || lp.ThumbURL == "" || lp.CanCheck || len(lp.Tags) == 0 {
		t.Fatalf("link-preview d1: %d %s", code, body)
	}
	if code, b, hdr := call("GET", lp.ThumbURL); code != 200 || hdr.Get("Content-Type") != "image/jpeg" || len(b) == 0 ||
		!strings.Contains(hdr.Get("Cache-Control"), "immutable") {
		t.Fatalf("thumb: %d %v", code, hdr)
	}
	if code, body, _ := call("POST", base+d1.ID+"/link-preview"); code != 409 {
		t.Fatalf("checking a replaced deploy: %d %s", code, body)
	}
	code, body, _ = call("POST", base+d2.ID+"/link-preview")
	if code != 200 || !strings.Contains(string(body), `"canCheck":true`) {
		t.Fatalf("check again: %d %s", code, body)
	}
	// The project card's image follows the live version: d2 has none.
	if code, _, _ := call("GET", "/v1/projects/shop/preview"); code != 404 {
		t.Fatalf("project preview without an image: %d", code)
	}
	if _, err := h.r.rollback(context.Background(), "shop", "site", d1.ID); err != nil {
		t.Fatal(err)
	}
	if code, b, _ := call("HEAD", "/v1/projects/shop/preview"); code != 200 || len(b) != 0 {
		t.Fatalf("project preview: %d", code)
	}

	// A live deploy that was never checked is checked when asked.
	h.r.st.deleteLinkPreview(context.Background(), "shop", "site", d1.ID)
	if code, body, _ := call("GET", base+d1.ID+"/link-preview"); code != 200 || !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("unchecked live deploy: %d %s", code, body)
	}

	// It goes with its deploy.
	if err := h.r.st.deleteDeploy(context.Background(), d2); err != nil {
		t.Fatal(err)
	}
	if rep, _ := h.r.st.getLinkPreview(context.Background(), "shop", "site", d2.ID); rep != nil {
		t.Fatal("report outlived its deploy")
	}
}
