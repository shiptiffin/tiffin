package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/change"
)

// Release puts an app live from an existing image or a site's files with no
// build (project duplicate and import); a stop keeps every app down until
// it is removed.
func TestReleaseAndStop(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	api := h.deploy("api", "", map[string]string{"index.ts": "v1"})
	d, err := h.m.Release(ctx, "shop", "api", ReleaseSource{Image: api.Image, Note: "copied"}, "tok_test")
	if err != nil || d.Status != StatusLive || d.Image == api.Image || d.Image == "" || d.Source != SourcePrebuilt {
		t.Fatalf("release from an image: %v %+v", err, d)
	}
	if _, err := h.eng.ImageDigest(ctx, api.Image); err != nil {
		t.Fatal("the source image is gone")
	}
	site := t.TempDir()
	if err := os.WriteFile(filepath.Join(site, "index.html"), []byte("<h1>copied</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := h.m.Release(ctx, "shop", "site", ReleaseSource{StaticDir: site, Framework: "static"}, "tok_test")
	if err != nil || s.Status != StatusLive || s.StaticRoot == "" {
		t.Fatalf("release a site: %v %+v", err, s)
	}
	if _, body := h.get("shop.tiffin.localhost", "/"); !strings.Contains(body, "copied") {
		t.Fatalf("released site: %s", body)
	}
	if live, err := h.m.LiveRelease(ctx, "shop", "site"); err != nil || live == nil || live.ID != s.ID {
		t.Fatalf("live release: %v %+v", err, live)
	}
	if bad, _ := h.m.Release(ctx, "shop", "api", ReleaseSource{Image: "docker.io/tiffin/none:x"}, "tok_test"); bad == nil || bad.Status != StatusFailed {
		t.Fatalf("release of a missing image: %+v", bad)
	}

	// A stop takes everything down and keeps it down.
	stop := func(on bool) {
		t.Helper()
		plan, err := h.p.Engine.PlanEdit(ctx, "shop", func(cur map[string]change.Resource) (map[string]change.Resource, error) {
			if on {
				cur[change.KindStopped] = change.Resource{Address: change.KindStopped, Spec: json.RawMessage(`{}`)}
			} else {
				delete(cur, change.KindStopped)
			}
			return cur, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.p.Engine.Apply(ctx, change.ApplyRequest{Plan: plan, Confirm: plan.Hash}); err != nil {
			t.Fatal(err)
		}
		var spec json.RawMessage
		if on {
			spec = json.RawMessage(`{}`)
		}
		h.apply() // apps first, as the platform's converge does
		if err := h.m.Reconcile(ctx, h.p, "shop", change.KindStopped, spec); err != nil {
			t.Fatal(err)
		}
	}
	stop(true)
	time.Sleep(300 * time.Millisecond)
	if n := len(h.eng.running()); n != 0 {
		t.Fatalf("%d containers still run in a stopped project", n)
	}
	if code, _ := h.get("shop.tiffin.localhost", "/"); code != 404 {
		t.Fatalf("a stopped project's site is still served: %d", code)
	}
	if dd := h.deploy("api", "", map[string]string{"index.ts": "v2"}); dd.Status != StatusFailed || !strings.Contains(dd.Error, "stopped") {
		t.Fatalf("deploy into a stopped project: %+v", dd)
	}
	stop(false)
	if code, body := h.get("shop.tiffin.localhost", "/api/"); code != 200 || !strings.Contains(body, strings.ToLower(d.ID)) {
		t.Fatalf("started again: %d %s", code, body)
	}
	if _, body := h.get("shop.tiffin.localhost", "/"); !strings.Contains(body, "copied") {
		t.Fatalf("site after start: %s", body)
	}
}
