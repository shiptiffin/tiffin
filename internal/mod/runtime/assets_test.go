package runtime

import (
	"context"
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
		{`{"devDependencies":{"@sveltejs/kit":"3","@sveltejs/adapter-bun":"1"}}`, "build/client"},
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

// A file of the app's own public/assets/ lands among Vite's hashed
// /assets/ files under the same name in every release: it is revalidated,
// not cached for a year.
func TestClientAssetsLeavesPublicFilesMutable(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"package.json":            `{"devDependencies":{"@react-router/dev":"7"}}`,
		"public/assets/logo.svg":  "<svg/>",
		"public/assets/img/a.png": "png",
		"public/favicon.ico":      "ico",
	})
	got := clientAssets(dir, &manifest.App{Framework: manifest.FrameworkBun})
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	if want := []string{"/assets/", "!/assets/img/", "!/assets/logo.svg"}; !reflect.DeepEqual(got[0].Immutable, want) {
		t.Errorf("immutable %q, want %q", got[0].Immutable, want)
	}
	// The kinds' own lists stay as they are.
	if again := clientAssets(t.TempDir(), &manifest.App{Framework: manifest.FrameworkBun}); again != nil {
		t.Fatalf("no package.json: %+v", again)
	}
	for _, k := range knownAssets {
		for _, a := range k.dirs {
			for _, pre := range a.Immutable {
				if strings.Contains(pre, "logo") {
					t.Fatalf("knownAssets changed: %+v", a)
				}
			}
		}
	}
}
