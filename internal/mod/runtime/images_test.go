package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeStored overrides when the fake engine says an image name was stored
// (default: a day ago, older than imageMinAge).
var fakeStored = struct {
	sync.Mutex
	at map[string]time.Time
}{at: map[string]time.Time{}}

func (e *fakeEngine) Images(ctx context.Context) ([]Image, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	fakeStored.Lock()
	defer fakeStored.Unlock()
	var out []Image
	for name, c := range e.images {
		if c == "" {
			continue
		}
		at, ok := fakeStored.at[name]
		if !ok {
			at = time.Now().Add(-24 * time.Hour)
		}
		sum := sha256.Sum256([]byte(c))
		out = append(out, Image{Name: name, Digest: "sha256:" + hex.EncodeToString(sum[:]), Created: at, Size: 1 << 20})
	}
	return out, nil
}

func (e *fakeEngine) UsedImages(ctx context.Context) (map[string]bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	used := map[string]bool{}
	for _, c := range e.ctrs {
		used[c.spec.Image] = true
	}
	return used, nil
}

func (e *fakeEngine) hasImage(name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.image(name)
	return ok
}

func (e *fakeEngine) putImage(name string) {
	e.mu.Lock()
	e.images[name] = "content:" + name
	e.mu.Unlock()
}

func (h *harness) imageNames() []string {
	h.eng.mu.Lock()
	defer h.eng.mu.Unlock()
	var out []string
	for n, c := range h.eng.images {
		if c != "" {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func (h *harness) image(app, id string) string {
	d, err := h.r.st.getDeploy(context.Background(), "shop", app, id)
	if err != nil {
		return "(no record)"
	}
	return d.Image
}

// A deploy that built but failed to start does not keep its image.
func TestFailedDeployDropsItsImage(t *testing.T) {
	h := newHarness(t)
	v1 := h.deploy("api", "", map[string]string{"index.ts": "v1"})
	crash := h.deploy("api", "", map[string]string{"index.ts": "v2", "CRASH": ""})
	if crash.Status != StatusFailed {
		t.Fatalf("crash: %+v", crash)
	}
	if h.eng.hasImage(imageRef("shop", "api", crash.ID)) || h.image("api", crash.ID) != "" {
		t.Fatalf("a failed deploy kept its image: %v", h.imageNames())
	}
	if !h.eng.hasImage(h.image("api", v1.ID)) {
		t.Fatal("the live build lost its image")
	}
}

// Destroying a project removes its images, deploy records, states and
// folders, so nothing is left for a new project of the same name.
func TestDestroyForgetsEverything(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for i := range 3 {
		h.deploy("api", "", map[string]string{"index.ts": "v" + string(rune('1'+i))})
	}
	h.deploy("api", "pr-1", map[string]string{"index.ts": "p1"})
	h.deploy("site", "", map[string]string{"index.html": "<h1>hi</h1>"})
	if len(h.imageNames()) == 0 {
		t.Fatal("no images to start with")
	}
	// A static build's caches (staticCaches) are per app, outside the deploy.
	buildCache := filepath.Join(h.r.opt.DataDir, buildCacheDir, "shop")
	if err := os.MkdirAll(filepath.Join(buildCache, "site", "bun"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, app := range []string{"api", "site", "jobs"} {
		if err := h.m.Reconcile(ctx, h.p, "shop", "app/"+app, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.m.ProjectDeleted(ctx, h.p, "shop"); err != nil {
		t.Fatal(err)
	}
	if n := h.imageNames(); len(n) != 0 {
		t.Fatalf("images survived the destroy: %v", n)
	}
	if ds, _ := h.r.st.listProjectDeploys(ctx, "shop"); len(ds) != 0 {
		t.Fatalf("deploy records survived: %d", len(ds))
	}
	states, _ := h.r.st.allStates(ctx)
	for _, s := range states {
		if s.Project == "shop" {
			t.Fatalf("state survived: %+v", s)
		}
	}
	for _, dir := range []string{filepath.Join(h.r.opt.DataDir, "deploys", "shop"), filepath.Join(h.r.opt.DataDir, "static", "shop"),
		filepath.Join(h.r.opt.LogDir, "shop"), buildCache} {
		if exists(dir) {
			t.Errorf("%s survived the destroy", dir)
		}
	}
}

// Deleting an app drops its rollback targets at once and keeps the live
// build for an undo; past deletedAppKeep the sweep forgets the app.
func TestDeletedAppKeepsItsLiveBuildForUndo(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var ds []*Deploy
	for i := range 3 {
		ds = append(ds, h.deploy("api", "", map[string]string{"index.ts": "v" + string(rune('1'+i))}))
	}
	a := h.mf.Apps["api"]
	delete(h.mf.Apps, "api")
	h.apply()
	live := ds[2]
	if h.image("api", live.ID) == "" || h.image("api", ds[1].ID) != "" || h.image("api", ds[0].ID) != "" {
		t.Fatalf("after the delete: live %q, earlier %q %q", h.image("api", live.ID), h.image("api", ds[1].ID), h.image("api", ds[0].ID))
	}
	// Within the window the sweep keeps it.
	res, err := h.r.sweepImages(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 0 || !h.eng.hasImage(h.image("api", live.ID)) {
		t.Fatalf("the sweep took a deleted app's live build within its window: %+v", res)
	}
	// Undo brings it back from that build.
	h.mf.Apps["api"] = a
	h.apply()
	if code, body := h.get("shop.tiffin.localhost", "/api/"); code != 200 || !strings.Contains(body, strings.ToLower(live.ID)) {
		t.Fatalf("undo: %d %s", code, body)
	}
	// Delete again, and let the window pass.
	delete(h.mf.Apps, "api")
	h.apply()
	old := time.Now().Add(-deletedAppKeep - time.Hour)
	all, _ := h.r.st.listDeploys(ctx, "shop", "api", "*")
	for _, d := range all {
		d.CreatedAt = old
		if err := h.r.st.putDeploy(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	st := h.state("api", "")
	st.UpdatedAt = old
	b, _ := json.Marshal(st)
	if err := h.p.DB.KVPut(ctx, nsState, envKey("shop", "api", ""), b); err != nil {
		t.Fatal(err)
	}
	res, err = h.r.sweepImages(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ExpiredApps) != 1 || res.ExpiredApps[0] != "shop/api" {
		t.Fatalf("expired apps: %v", res.ExpiredApps)
	}
	for _, n := range h.imageNames() {
		if strings.Contains(n, "shop-api") {
			t.Fatalf("the expired app kept image %s", n)
		}
	}
	if left, _ := h.r.st.listDeploys(ctx, "shop", "api", "*"); len(left) != 0 {
		t.Fatalf("the expired app kept %d records", len(left))
	}
	if exists(filepath.Join(h.r.opt.DataDir, "deploys", "shop", "api")) {
		t.Fatal("the expired app kept its work folders")
	}
}

// The sweep removes images nothing needs (destroyed projects', records
// past the rollback targets, a load's digest names) and keeps the live
// builds, rollback targets, base images, fresh names and images in use.
// A dry run only reports.
func TestSweepImages(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var ds []*Deploy
	for i := range 4 {
		ds = append(ds, h.deploy("api", "", map[string]string{"index.ts": "v" + string(rune('1'+i))}))
	}
	// The replaced instances are gone: no container uses an old build.
	h.r.drains.Wait()
	// KeepImages is 2: ds[3] live, ds[2] and ds[1] rollback targets.
	// An older build whose removal failed once (the record still names it).
	stale := imageRef("shop", "api", ds[0].ID)
	h.eng.putImage(stale)
	d0, _ := h.r.st.getDeploy(ctx, "shop", "api", ds[0].ID)
	d0.Image = stale
	_ = h.r.st.putDeploy(ctx, d0)
	// A destroyed project's leftovers, from before destroys cleaned up.
	goneImg := imageRef("gone", "web", "dep_01gone")
	h.eng.putImage(goneImg)
	_ = h.r.st.putDeploy(ctx, &Deploy{ID: "dep_01GONE", Project: "gone", App: "web", Status: StatusLive, Image: goneImg, CreatedAt: time.Now()})
	for _, dir := range []string{filepath.Join(h.r.opt.DataDir, "deploys", "gone", "web", "dep_01GONE"), filepath.Join(h.r.opt.DataDir, "static", "gone2"), filepath.Join(h.r.opt.LogDir, "gone", "web", "prod")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	digestName := loadDigestPrefix + strings.Repeat("c", 64)
	h.eng.putImage(digestName)
	orphan := imageRef("shop", "api", "dep_01orphan") // no record
	h.eng.putImage(orphan)
	base := "docker.io/oven/bun:1.4.2-slim"
	h.eng.putImage(base)
	fresh := imageRef("shop", "api", "dep_01fresh")
	h.eng.putImage(fresh)
	fakeStored.Lock()
	fakeStored.at[fresh] = time.Now()
	fakeStored.Unlock()
	t.Cleanup(func() { fakeStored.Lock(); delete(fakeStored.at, fresh); fakeStored.Unlock() })
	inUse := imageRef("shop", "api", "dep_01inuse")
	h.eng.putImage(inUse)
	h.eng.mu.Lock()
	h.eng.ctrs["leftover"] = &fakeCtr{spec: RunSpec{Name: "leftover", Image: inUse}}
	h.eng.mu.Unlock()

	want := []string{digestName, goneImg, orphan, stale}
	sort.Strings(want)
	names := func(res *SweepResult) []string {
		var out []string
		for _, s := range res.Removed {
			out = append(out, s.Name)
		}
		sort.Strings(out)
		return out
	}
	before := h.imageNames()
	dry, err := h.r.sweepImages(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(dry); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("dry run would remove %v, want %v (kept: %v)", got, want, dry.Kept)
	}
	if strings.Join(dry.GoneProjects, " ") != "gone gone2" {
		t.Fatalf("gone projects: %v", dry.GoneProjects)
	}
	if after := h.imageNames(); strings.Join(after, " ") != strings.Join(before, " ") || !exists(filepath.Join(h.r.opt.DataDir, "deploys", "gone")) {
		t.Fatal("a dry run changed something")
	}
	for _, s := range dry.Removed {
		if s.Name == goneImg && !strings.Contains(s.Why, "project gone was destroyed") {
			t.Errorf("why %s: %s", s.Name, s.Why)
		}
	}

	if n := h.bld.prunes.Load(); n != 0 {
		t.Fatalf("a dry run pruned the build cache %d times", n)
	}
	res, err := h.r.sweepImages(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(res); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("removed %v, want %v", got, want)
	}
	if n, kept := h.bld.prunes.Load(), h.bld.pruneTo.Load(); n != 1 || kept != buildCacheCap(diskBytes(h.r.opt.DataDir)) {
		t.Fatalf("build cache pruned %d times, to %d", n, kept)
	}
	for _, n := range want {
		if h.eng.hasImage(n) {
			t.Errorf("%s survived", n)
		}
	}
	for _, n := range []string{base, fresh, inUse, h.image("api", ds[3].ID), h.image("api", ds[2].ID), h.image("api", ds[1].ID)} {
		if n == "" || !h.eng.hasImage(n) {
			t.Errorf("%q was removed", n)
		}
	}
	if h.image("api", ds[0].ID) != "" {
		t.Error("the record still names its removed image")
	}
	if ds, _ := h.r.st.listProjectDeploys(ctx, "gone"); len(ds) != 0 {
		t.Error("the destroyed project's records survived")
	}
	for _, dir := range []string{filepath.Join(h.r.opt.DataDir, "deploys", "gone"), filepath.Join(h.r.opt.DataDir, "static", "gone2"), filepath.Join(h.r.opt.LogDir, "gone")} {
		if exists(dir) {
			t.Errorf("%s survived", dir)
		}
	}
	if code, _ := h.get("shop.tiffin.localhost", "/api/"); code != 200 {
		t.Fatalf("the live app stopped serving: %d", code)
	}
	// Nothing left to do.
	again, err := h.r.sweepImages(ctx, false)
	if err != nil || len(again.Removed)+len(again.Failed) != 0 {
		t.Fatalf("second sweep: %+v %v", again, err)
	}
}

func (b *fakeBuilder) PruneCache(ctx context.Context, maxBytes int64) error {
	b.prunes.Add(1)
	b.pruneTo.Store(maxBytes)
	return nil
}

// The build cache may hold 15% of the data disk, between 4 and 20 GiB, and
// buildkitd.toml says so.
func TestBuildCacheCap(t *testing.T) {
	const gib = int64(1) << 30
	for _, c := range []struct{ disk, want int64 }{{0, 4 * gib}, {20 * gib, 4 * gib}, {40 * gib, 6 * gib}, {80 * gib, 12 * gib}, {160 * gib, 20 * gib}, {2000 * gib, 20 * gib}} {
		if got := buildCacheCap(c.disk); got != c.want {
			t.Errorf("disk %d GiB: cap %d, want %d", c.disk/gib, got, c.want)
		}
	}
	conf := buildkitConfig(6 * gib)
	if !strings.Contains(conf, `maxUsedSpace = "3072MB"`) || !strings.Contains(conf, `maxUsedSpace = "6144MB"`) || strings.Count(conf, "maxUsedSpace") != 2 {
		t.Fatalf("config:\n%s", conf)
	}
}

func TestParseImageList(t *testing.T) {
	out := `{"CreatedAt":"2026-10-07 04:57:44 +0000 UTC","Digest":"sha256:def8","Name":"docker.io/tiffin/mailcheck-web:dep_01m4","Size":"544.5MB"}
{"CreatedAt":"2026-10-05 17:01:13 +0000 UTC","Digest":"sha256:3392","Name":"import@sha256:33922d","Size":"1.551GB"}
not json
`
	imgs := parseImageList(out)
	if len(imgs) != 2 || imgs[0].Name != "docker.io/tiffin/mailcheck-web:dep_01m4" || imgs[0].Size != 544_500_000 ||
		!imgs[0].Created.Equal(time.Date(2026, 10, 7, 4, 57, 44, 0, time.UTC)) || imgs[1].Size != 1_551_000_000 {
		t.Fatalf("%+v", imgs)
	}
	for in, want := range map[string]int64{"0B": 0, "118B": 118, "12.29kB": 12290, "175.3MiB": 183815372, "x": 0, "": 0} {
		if got := parseSize(in); got != want {
			t.Errorf("parseSize(%q) = %d, want %d", in, got, want)
		}
	}
	if managedImage("docker.io/oven/bun:1.4.2-slim") || !managedImage("docker.io/tiffin/a-b:dep_1") || !managedImage(loadDigestPrefix+strings.Repeat("a", 64)) {
		t.Fatal("managedImage")
	}
}

// DiskUse reports each project's images, build files and logs, and the
// images the sweep would remove.
func TestDiskUse(t *testing.T) {
	h := newHarness(t)
	h.deploy("api", "", map[string]string{"index.ts": "v1"})
	h.deploy("site", "", map[string]string{"index.html": "<h1>hi</h1>"})
	h.eng.putImage(imageRef("gone", "web", "dep_01x"))
	// A static build cache counts toward its project's build files.
	cache := filepath.Join(h.r.opt.DataDir, buildCacheDir, "cached", "web", "bun")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "pkg.tgz"), make([]byte, 5000), 0o644); err != nil {
		t.Fatal(err)
	}
	rd, err := h.m.DiskUse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	u := rd.Projects["shop"]
	if u == nil || u.Images != 1 || u.ImageBytes != 1<<20 || u.BuildBytes == 0 {
		t.Fatalf("shop: %+v", u)
	}
	if c := rd.Projects["cached"]; c == nil || c.BuildBytes < 5000 {
		t.Fatalf("build cache not counted: %+v", c)
	}
	if rd.UnusedImages != 1 || rd.UnusedImageBytes != 1<<20 {
		t.Fatalf("unused: %d %d", rd.UnusedImages, rd.UnusedImageBytes)
	}
	if rd.BuildCacheCapBytes != buildCacheCap(diskBytes(h.r.opt.DataDir)) || rd.BuildCacheCapBytes < 4<<30 {
		t.Fatalf("build cache cap: %d", rd.BuildCacheCapBytes)
	}
	if a, b := parseSystemDF(`{"Type":"Images","Size":"4.768GB"}` + "\n" + `{"Type":"Build Cache","Size":"12.03GB"}`); a != 4_768_000_000 || b != 12_030_000_000 {
		t.Fatalf("system df: %d %d", a, b)
	}
}
