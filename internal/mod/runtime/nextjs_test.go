package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/manifest"
)

func TestPrepareNextWritesTheAdapter(t *testing.T) {
	src := t.TempDir()
	env := map[string]string{}
	req := BuildRequest{Deploy: &Deploy{ID: "dep_01TEST"}, SrcDir: src, NextCache: true, Log: io.Discard, Env: map[string]string{}}
	img := prepareNext(req, env)
	if env[nextAdapterEnv] != nextAdapterPath || img[nextAdapterEnv] != nextAdapterPath {
		t.Fatalf("env %v, image env %v", env, img)
	}
	for _, f := range []string{"adapter.js", "package.json", "cache-handler.js", "use-cache.js", "store.js", "resp.js"} {
		if !exists(filepath.Join(src, nextDir, f)) {
			t.Errorf("%s not written", f)
		}
	}
	// The app's own adapter wins.
	src2 := t.TempDir()
	req2 := BuildRequest{Deploy: &Deploy{ID: "dep_2"}, SrcDir: src2, Log: io.Discard, Env: map[string]string{nextAdapterEnv: "./mine.js"}}
	if img := prepareNext(req2, map[string]string{}); img != nil || exists(filepath.Join(src2, nextDir)) {
		t.Fatal("the box's adapter must stay out when the app sets its own")
	}

	// The adapter itself, run the way Next.js runs it.
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found: adapter logic not exercised")
	}
	script := `const a = (await import(process.argv[1])).default;
const out = [
  a.modifyConfig({ compress: true, images: { maximumDiskCacheSize: undefined }, cacheHandlers: {} }),
  a.modifyConfig({ deploymentId: "mine", cacheHandler: "/my.js", cacheMaxMemorySize: 5, cacheHandlers: { default: "/d.js" }, images: { maximumDiskCacheSize: 7 } }),
];
console.log(JSON.stringify({ name: a.name, out }));`
	cmd := exec.Command(node, "--input-type=module", "-e", script, filepath.Join(src, nextDir, "adapter.js"))
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, raw)
	}
	var res struct {
		Name string           `json:"name"`
		Out  []map[string]any `json:"out"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	real, _ := filepath.EvalSymlinks(src)
	here := filepath.Join(real, nextDir)
	plain, mine := res.Out[0], res.Out[1]
	hs, _ := plain["cacheHandlers"].(map[string]any)
	if res.Name != "tiffin" || plain["compress"] != false || plain["deploymentId"] != "dep_01TEST" ||
		plain["cacheHandler"] != filepath.Join(here, "cache-handler.js") || plain["cacheMaxMemorySize"] != 0.0 ||
		hs["default"] != filepath.Join(here, "use-cache.js") || hs["remote"] != filepath.Join(here, "use-cache.js") ||
		plain["images"].(map[string]any)["maximumDiskCacheSize"] != float64(nextImageCacheBytes) {
		t.Errorf("defaults: %v", plain)
	}
	hs, _ = mine["cacheHandlers"].(map[string]any)
	if mine["deploymentId"] != "mine" || mine["cacheHandler"] != "/my.js" || mine["cacheMaxMemorySize"] != 5.0 ||
		hs["default"] != "/d.js" || mine["images"].(map[string]any)["maximumDiskCacheSize"] != 7.0 || mine["compress"] != false {
		t.Errorf("the app's own config must win: %v", mine)
	}
}

func TestSetDeployEnvKeepsThePlan(t *testing.T) {
	p := filepath.Join(t.TempDir(), "plan.json")
	os.WriteFile(p, []byte(`{"steps":[{"name":"build"}],"deploy":{"startCommand":"bun --bun next start","variables":{"A":"1"}}}`), 0o644)
	if err := setDeployEnv(p, map[string]string{nextAdapterEnv: nextAdapterPath}); err != nil {
		t.Fatal(err)
	}
	var plan struct {
		Steps  []map[string]any `json:"steps"`
		Deploy struct {
			Start string            `json:"startCommand"`
			Vars  map[string]string `json:"variables"`
		} `json:"deploy"`
	}
	raw, _ := os.ReadFile(p)
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 1 || plan.Deploy.Start != "bun --bun next start" || plan.Deploy.Vars["A"] != "1" || plan.Deploy.Vars[nextAdapterEnv] != nextAdapterPath {
		t.Fatalf("plan: %s", raw)
	}
}

func TestNextAppsGetAStableKeyAndAnImageCache(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	api := h.mf.Apps["api"]
	api.Framework = manifest.FrameworkNext
	h.mf.Apps["api"] = api
	h.apply()
	d1 := h.deploy("api", "", map[string]string{"package.json": `{}`})
	if d1.Status != StatusLive {
		t.Fatalf("deploy: %s %s", d1.Status, d1.Error)
	}
	key := func() string {
		for _, c := range h.eng.running() {
			if c.spec.Labels["tiffin.app"] == "api" {
				return c.spec.Env[nextKeyEnv]
			}
		}
		return ""
	}
	k1 := key()
	if b, err := base64.StdEncoding.DecodeString(k1); err != nil || len(b) != 32 {
		t.Fatalf("key %q is not 32 bytes of base64", k1)
	}
	h.deploy("api", "", map[string]string{"package.json": `{}`})
	pv := h.deploy("api", "pr-1", map[string]string{"package.json": `{}`})
	if pv.Status != StatusLive {
		t.Fatal(pv.Error)
	}
	var mounts []string
	for _, c := range h.eng.running() {
		if c.spec.Labels["tiffin.app"] != "api" {
			continue
		}
		if c.spec.Env[nextKeyEnv] != k1 {
			t.Fatal("the key changed across deploys or previews")
		}
		mounts = append(mounts, c.spec.Mounts...)
	}
	want := []string{h.r.nextCacheDir("shop", "api", "") + ":" + nextImageCache, h.r.nextCacheDir("shop", "api", "pr-1") + ":" + nextImageCache}
	if !strings.Contains(strings.Join(mounts, " "), want[0]) || !strings.Contains(strings.Join(mounts, " "), want[1]) {
		t.Fatalf("mounts %v, want %v", mounts, want)
	}
	// The app's own key wins, at run time and build time.
	got, err := h.r.nextActionsKey(ctx, "shop", "api", map[string]string{nextKeyEnv: "mine"})
	if err != nil || got != "mine" {
		t.Fatalf("own key: %q %v", got, err)
	}
	// Deleting the preview drops its image cache; destroying the project drops the key.
	if err := h.r.deletePreview(ctx, "shop", "api", "pr-1"); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Dir(h.r.nextCacheDir("shop", "api", "pr-1"))) || !exists(h.r.nextCacheDir("shop", "api", "")) {
		t.Fatal("deleting a preview must drop its image cache only")
	}
	if err := h.m.ProjectDeleted(ctx, h.p, "shop"); err != nil {
		t.Fatal(err)
	}
	if kv, _ := h.p.DB.KVList(ctx, nsNextKeys); len(kv) != 0 {
		t.Fatalf("keys left: %v", kv)
	}
}
