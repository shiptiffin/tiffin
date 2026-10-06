package runtime

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
)

func TestAptPackages(t *testing.T) {
	for _, c := range []struct{ cur, want string }{
		{"", "... ffmpeg chromium"},
		{"... libvips", "... libvips ffmpeg chromium"},
		{"ffmpeg", "ffmpeg chromium"}, // the app's own list replaces Railpack's; keep that
	} {
		if got := aptPackages(c.cur, []string{"ffmpeg", "chromium"}); got != c.want {
			t.Errorf("aptPackages(%q) = %q, want %q", c.cur, got, c.want)
		}
	}
}

func readText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestDiskFolders: an app's disk folders start with what its image has,
// are shared by its instances, keep what the app writes across deploys and
// rollbacks, are a preview's own, count toward the project's usage, and go
// to the trash with the app.
func TestDiskFolders(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	api := h.mf.Apps["api"]
	api.Disk = manifest.Disk{{Path: "data"}, {Path: "cache/renders"}}
	h.mf.Apps["api"] = api
	h.apply()
	d1 := h.deploy("api", "", map[string]string{"index.ts": "v1", "data/app.db": "seed v1"})
	if d1.Status != StatusLive {
		t.Fatalf("deploy: %s %s", d1.Status, d1.Error)
	}
	prod := h.r.diskDir("shop", "api", "")
	if got := readText(t, filepath.Join(prod, "data", "app.db")); got != "seed v1" {
		t.Fatalf("seeded from the image: %q", got)
	}
	if !exists(filepath.Join(prod, "cache", "renders")) {
		t.Fatal("a folder the image does not have starts empty")
	}
	mounts := func(preview string) []string {
		var out []string
		h.eng.mu.Lock()
		defer h.eng.mu.Unlock()
		for _, in := range h.state("api", preview).Instances {
			out = append(out, strings.Join(h.eng.ctrs[in.Name].spec.Mounts, " "))
		}
		return out
	}
	want := prod + "/data:/app/data " + prod + "/cache/renders:/app/cache/renders"
	if ms := mounts(""); len(ms) != 2 || ms[0] != want || ms[1] != want {
		t.Fatalf("both instances must mount the folders: %v, want %q", ms, want)
	}
	if !strings.Contains(h.buildLog("api", d1.ID), "disk folder data: made, with 1 files") {
		t.Fatalf("build log: %s", h.buildLog("api", d1.ID))
	}

	// What the app writes outlives deploys and rollbacks; the image's copy is used once.
	os.WriteFile(filepath.Join(prod, "data", "app.db"), []byte("written by the app"), 0o644)
	os.WriteFile(filepath.Join(prod, "cache", "renders", "a.mp4"), []byte("frames"), 0o644)
	d2 := h.deploy("api", "", map[string]string{"index.ts": "v2", "data/app.db": "seed v2"})
	if d2.Status != StatusLive || readText(t, filepath.Join(prod, "data", "app.db")) != "written by the app" {
		t.Fatalf("after a deploy: %s %q", d2.Status, readText(t, filepath.Join(prod, "data", "app.db")))
	}
	if _, err := h.r.rollback(ctx, "shop", "api", d1.ID); err != nil {
		t.Fatal(err)
	}
	if readText(t, filepath.Join(prod, "cache", "renders", "a.mp4")) != "frames" {
		t.Fatal("a rollback must keep the folders")
	}

	// A preview has its own, from its own image.
	pv := h.deploy("api", "pr-1", map[string]string{"index.ts": "pv", "data/app.db": "seed pv"})
	pvDir := h.r.diskDir("shop", "api", "pr-1")
	if pv.Status != StatusLive || readText(t, filepath.Join(pvDir, "data", "app.db")) != "seed pv" {
		t.Fatalf("preview: %s %s", pv.Status, pv.Error)
	}
	if ms := mounts("pr-1"); len(ms) != 1 || !strings.Contains(ms[0], pvDir+"/data:/app/data") {
		t.Fatalf("preview mounts %v", ms)
	}
	forgetDiskBytes("shop")
	if got, want := DiskBytes("shop"), int64(len("written by the app")+len("frames")+len("seed pv")); got != want {
		t.Fatalf("DiskBytes = %d, want %d", got, want)
	}
	if u, _ := h.m.ProjectUsage(ctx, h.p, "shop"); u == nil || u.Disk != "files" || u.Bytes == 0 {
		t.Fatalf("usage %+v", u)
	}
	if err := h.r.deletePreview(ctx, "shop", "api", "pr-1"); err != nil {
		t.Fatal(err)
	}
	if exists(pvDir) || !exists(prod) {
		t.Fatal("deleting a preview drops its folders only")
	}

	// Adding a folder restarts the app with it mounted.
	api.Disk = append(api.Disk, manifest.DiskFolder{Path: "uploads"})
	h.mf.Apps["api"] = api
	h.apply()
	if ms := mounts(""); len(ms) != 2 || !strings.Contains(ms[0], prod+"/uploads:/app/uploads") {
		t.Fatalf("after adding a folder: %v", ms)
	}

	// Deleting the app says what goes, then moves the folders to the trash.
	loss, err := h.m.EstimateLoss(ctx, h.p, "shop", change.Op{Address: "app/api", Action: change.Delete})
	if err != nil || loss == nil || loss.Bytes != int64(len("written by the app")+len("frames")) || loss.Counts[0].N != 2 {
		t.Fatalf("loss %+v %v", loss, err)
	}
	delete(h.mf.Apps, "api")
	h.apply()
	if exists(filepath.Dir(prod)) {
		t.Fatal("the app's folders are still in place")
	}
	es, _ := os.ReadDir(filepath.Join(h.r.opt.DataDir, "disks-trash"))
	if len(es) != 1 || readText(t, filepath.Join(h.r.opt.DataDir, "disks-trash", es[0].Name(), "prod", "data", "app.db")) != "written by the app" {
		t.Fatalf("trash: %v", es)
	}
}

// TestLongRequests: the switchboard passes a big upload whole, and a request
// still under way keeps its preview awake and its old release running
// through a deploy.
func TestLongRequests(t *testing.T) {
	h := newHarness(t)
	h.deploy("api", "", map[string]string{"index.ts": "v1"})
	body := bytes.Repeat([]byte("x"), 64<<20)
	req, _ := http.NewRequest("POST", h.srv.URL+"/api/upload", bytes.NewReader(body))
	req.Host = "shop.tiffin.localhost"
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.HasPrefix(string(got), fmt.Sprintf("read %d ", len(body))) {
		t.Fatalf("upload: %d %.80s", res.StatusCode, got)
	}

	slow := func(host, path string) (wait func() (int, string)) {
		var wg sync.WaitGroup
		var code int
		var out string
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, out = h.get(host, path)
		}()
		time.Sleep(200 * time.Millisecond) // under way
		return func() (int, string) { wg.Wait(); return code, out }
	}
	// A deploy lets the old release finish the request, however long the drain allows.
	h.r.opt.Drain = time.Minute
	done := slow("shop.tiffin.localhost", "/api/render?sleep=2500ms")
	if d := h.deploy("api", "", map[string]string{"index.ts": "v2"}); d.Status != StatusLive {
		t.Fatal(d.Error)
	}
	if code, out := done(); code != 200 || !strings.Contains(out, "greeting=hello") {
		t.Fatalf("a request under way through a deploy: %d %s", code, out)
	}

	pv := h.deploy("api", "feat-x", map[string]string{"index.ts": "preview"})
	if pv.Status != StatusLive {
		t.Fatal(pv.Error)
	}
	host := "feat-x--shop-api.tiffin.localhost"
	done = slow(host, "/render?sleep=1s")
	h.r.opt.PreviewIdle = time.Millisecond
	time.Sleep(5 * time.Millisecond)
	h.r.mu.Lock()
	h.r.lastSeen[envKey("shop", "api", "feat-x")] = time.Now().Add(-time.Hour) // as if it began long ago
	h.r.mu.Unlock()
	h.r.sleepIdle(context.Background())
	if h.state("api", "feat-x").Sleeping {
		t.Fatal("a preview fell asleep with a request under way")
	}
	if code, out := done(); code != 200 || !strings.Contains(out, "preview=feat-x") {
		t.Fatalf("long preview request: %d %s", code, out)
	}
	time.Sleep(5 * time.Millisecond)
	h.r.sleepIdle(context.Background())
	if !h.state("api", "feat-x").Sleeping {
		t.Fatal("an idle preview must still fall asleep")
	}
}
