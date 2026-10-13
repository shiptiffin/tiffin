package domains

import (
	"slices"
	"strings"
	"testing"

	"github.com/shiptiffin/tiffin/internal/edge"
)

// redirects are the edge's redirect routes, as "host>target".
func (h *harness) redirects() []string {
	h.edge.mu.Lock()
	defer h.edge.mu.Unlock()
	var out []string
	for _, r := range h.edge.routes {
		if r.RedirectTo != "" {
			out = append(out, r.Host+">"+r.RedirectTo)
		}
	}
	slices.Sort(out)
	return out
}

// goLive marks a custom domain live, as followCerts does once the edge
// holds its certificate, and runs the part of a tick that reloads the edge.
func (h *harness) goLive(host string, live bool) {
	h.t.Helper()
	h.m.mu.Lock()
	r := h.m.recs[host]
	if r == nil {
		h.t.Fatalf("%s is not tracked", host)
	}
	r.Ready = live
	r.State = map[bool]string{true: StateLive, false: StateWaiting}[live]
	h.m.mu.Unlock()
	if h.m.primaryTick(h.t.Context(), h.p) {
		if err := h.p.RefreshRoutes(h.t.Context()); err != nil {
			h.t.Fatal(err)
		}
	}
}

func TestPrimaryDomain(t *testing.T) {
	h := newHarness(t)
	h.project("shop") // web serves shop.box.test, api shop-api.box.test

	// The app's domains: the primary (with www), an old one (with www), and
	// one shared with another app's path.
	h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "old.test", "app": "web", "www": true})
	h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "shared.test", "app": "web"})
	h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "shared.test", "app": "api", "path": "/api"})
	out := h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "example.test", "app": "web", "www": true, "primary": true})
	d := out["domain"].(map[string]any)
	if d["primary"] != true || d["redirecting"] == true || !strings.Contains(d["summary"].(string), "once it is live, shop.box.test, old.test, www.old.test redirect here; until then they keep serving") {
		t.Fatalf("new primary: %v", d)
	}

	// tiffin pull sees it.
	_, man := h.call("GET", "/v1/projects/shop/manifest", nil)
	if cfg := man["config"].(string); !strings.Contains(cfg, `"example.test": { www: "redirect", primary: true }`) {
		t.Errorf("manifest config:\n%s", cfg)
	}

	// Not live yet: every address keeps serving, only www redirects.
	h.m.tick(t.Context())
	want := []string{"www.example.test>example.test", "www.old.test>old.test"}
	if got := h.redirects(); !slices.Equal(got, want) {
		t.Errorf("before the primary is live: %v, want %v", got, want)
	}
	if o := h.domain("shop", "old.test"); o["primaryDomain"] != "example.test" || o["redirecting"] == true ||
		!strings.Contains(o["summary"].(string), "once that is live; until then it keeps serving") {
		t.Errorf("old.test before: %v", o)
	}

	// Live: the app's other whole addresses redirect; the shared host, the
	// other app and previews (not routes at all here) do not.
	h.goLive("example.test", true)
	want = []string{"old.test>example.test", "shop.box.test>example.test", "www.example.test>example.test", "www.old.test>old.test"}
	if got := h.redirects(); !slices.Equal(got, want) {
		t.Errorf("live: %v, want %v", got, want)
	}
	d = h.domain("shop", "example.test")
	if d["redirecting"] != true || !strings.Contains(d["summary"].(string), "Primary domain of app web: shop.box.test, old.test, www.old.test redirect here (308).") {
		t.Errorf("live primary: %v", d)
	}
	if rf := d["redirectFrom"].([]any); len(rf) != 3 {
		t.Errorf("redirectFrom: %v", rf)
	}
	if o := h.domain("shop", "old.test"); o["primaryDomain"] != "example.test" || o["redirecting"] != true {
		t.Errorf("old.test live: %v", o)
	}
	if s := h.domain("shop", "shared.test"); s["primaryDomain"] != nil {
		t.Errorf("a host shared with another app must not redirect: %v", s)
	}
	// The redirects reach the edge as whole-host routes, which win over the
	// runtime's own routes for those hosts (internal/edge).
	if !slices.ContainsFunc(h.edge.routes, func(r edge.Route) bool {
		return r.Host == "shop.box.test" && r.PathPrefix == "" && r.RedirectTo == "example.test"
	}) {
		t.Errorf("edge routes: %+v", h.edge.routes)
	}

	// The primary stops being live (DNS moved away): the old addresses serve again.
	h.goLive("example.test", false)
	if got := h.redirects(); slices.Contains(got, "shop.box.test>example.test") {
		t.Errorf("not live any more: %v", got)
	}

	// domains edit: old.test becomes the primary; example.test stops being one.
	h.goLive("old.test", true)
	h.confirmed("POST", "/v1/projects/shop/domains/old.test/edit", map[string]any{"primary": true})
	_, man = h.call("GET", "/v1/projects/shop/manifest", nil)
	if cfg := man["config"].(string); !strings.Contains(cfg, `"old.test": { www: "redirect", primary: true }`) || strings.Contains(cfg, `"example.test": { www: "redirect", primary: true }`) {
		t.Errorf("after edit:\n%s", cfg)
	}
	h.goLive("old.test", true)
	if got := h.redirects(); !slices.Contains(got, "shop.box.test>old.test") || !slices.Contains(got, "example.test>old.test") {
		t.Errorf("old.test primary: %v", got)
	}
	// ... and back off: nothing redirects but www.
	h.confirmed("POST", "/v1/projects/shop/domains/old.test/edit", map[string]any{"primary": false})
	h.m.tick(t.Context())
	want = []string{"www.example.test>example.test", "www.old.test>old.test"}
	if got := h.redirects(); !slices.Equal(got, want) {
		t.Errorf("no primary: %v, want %v", got, want)
	}

	// Refusals, with field errors.
	for _, tc := range []struct {
		path string
		body map[string]any
		code int
	}{
		{"/v1/projects/shop/domains", map[string]any{"domain": "example.test", "app": "api", "path": "/x", "primary": true}, 422}, // a path
		{"/v1/projects/shop/domains", map[string]any{"domain": "blog.box.test", "app": "web", "primary": true}, 422},              // a box name
		{"/v1/projects/shop/domains/nope.test/edit", map[string]any{"primary": true}, 404},
		{"/v1/projects/shop/domains/example.test/edit", map[string]any{}, 422},
	} {
		if code, out := h.call("POST", tc.path, tc.body); code != tc.code {
			t.Errorf("%s %v: %d %v", tc.path, tc.body, code, out)
		}
	}
}
