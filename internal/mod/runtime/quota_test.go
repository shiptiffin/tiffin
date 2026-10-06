package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
)

// fakeQuota is quotaFS in memory: a project's usage is what its folders
// hold, its limit what was last set.
type fakeQuota struct {
	mu     sync.Mutex
	dirs   map[uint32][]string
	limits map[uint32]int64
	calls  int // Limit calls
}

func newFakeQuota() *fakeQuota {
	return &fakeQuota{dirs: map[uint32][]string{}, limits: map[uint32]int64{}}
}

func (f *fakeQuota) Assign(dir string, id uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dirs[id] = append(f.dirs[id], dir)
	return nil
}

func (f *fakeQuota) Limit(id uint32, n int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.limits[id] = n
	f.calls++
	return nil
}

func (f *fakeQuota) Usage() (map[uint32]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[uint32]int64{}
	for id, dirs := range f.dirs {
		for _, d := range dirs {
			out[id] += dirSize(d)
		}
	}
	return out, nil
}

// limitOf is the limit of the project holding dir.
func (f *fakeQuota) limitOf(t *testing.T, dir string) int64 {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, dirs := range f.dirs {
		for _, d := range dirs {
			if d == dir {
				return f.limits[id]
			}
		}
	}
	t.Fatalf("%s has no project", dir)
	return 0
}

func TestParseQuotaReport(t *testing.T) {
	out := "#0                   0          0          0     00 [--------]\n#1001             3072          0      10240     00 [--------]\n\n"
	got := parseQuotaReport(out)
	if len(got) != 2 || got[1001] != 3072<<10 || got[0] != 0 {
		t.Fatalf("%v", got)
	}
}

func TestMountOf(t *testing.T) {
	mounts := `/dev/vda1 / ext4 rw,relatime 0 0
/dev/vdb1 /var/lib/tiffin xfs rw,relatime,inode64,prjquota 0 0
/dev/vdb1 /mnt/lima-qq xfs rw,relatime,prjquota 0 0
`
	if m, fs, opts := mountOf(mounts, "/var/lib/tiffin/runtime/disks"); m != "/var/lib/tiffin" || fs != "xfs" || !strings.Contains(opts, "prjquota") {
		t.Fatalf("%q %q %q", m, fs, opts)
	}
	if m, fs, _ := mountOf(mounts, "/var/lib/tiffinx"); m != "/" || fs != "ext4" {
		t.Fatalf("a sibling path is not under the mount: %q %q", m, fs)
	}
}

// TestDiskSizes: each folder of each environment gets its own project,
// limited to its size; growing applies without a restart; shrinking below
// what a folder holds and sizes over the project's storage limit are
// refused; the disk guard's hold freezes the folders; a preview's go with
// it; folders nearly full show in health.
func TestDiskSizes(t *testing.T) {
	fq := newFakeQuota()
	h := newHarnessQuota(t, fq)
	ctx := context.Background()
	web := h.mf.Apps["api"]
	web.Disk = manifest.Disk{{Path: "data", Size: "10MB"}, {Path: "cache"}}
	h.mf.Apps["api"] = web
	h.apply()
	if d := h.deploy("api", "", map[string]string{"index.ts": "v1", "data/app.db": "seed"}); d.Status != StatusLive {
		t.Fatalf("deploy: %s %s", d.Status, d.Error)
	}
	prod := h.r.diskDir("shop", "api", "")
	if n := fq.limitOf(t, filepath.Join(prod, "data")); n != 10<<20 {
		t.Fatalf("data's limit %d, want 10 MB", n)
	}
	if n := fq.limitOf(t, filepath.Join(prod, "cache")); n != 1<<30 {
		t.Fatalf("cache's limit %d, want the default 1 GB", n)
	}
	os.WriteFile(filepath.Join(prod, "data", "big"), make([]byte, 3<<20), 0o644)
	forgetDiskBytes("shop")
	fs := DiskFolders(ctx, "shop")
	if len(fs) != 2 || fs[1].Path != "data" || fs[1].SizeBytes != 10<<20 || fs[1].UsedBytes != 3<<20+4 || !fs[1].Enforced {
		t.Fatalf("folders %+v", fs)
	}
	if n := DiskBytes("shop"); n != 3<<20+4 {
		t.Fatalf("DiskBytes %d", n)
	}

	// Growing: the limit changes, the instances stay.
	before := h.state("api", "").Instances
	web.Disk[0].Size = "20MB"
	h.mf.Apps["api"] = web
	h.apply()
	if n := fq.limitOf(t, filepath.Join(prod, "data")); n != 20<<20 {
		t.Fatalf("grown limit %d", n)
	}
	if after := h.state("api", "").Instances; after[0].Name != before[0].Name {
		t.Fatal("a new size must not restart the app")
	}

	// Shrinking below what it holds, or past the storage limit: refused.
	check := func(mutate func(*manifest.Manifest), desired map[string]change.Resource) error {
		mf := *h.mf
		mf.Apps = map[string]manifest.App{}
		for k, v := range h.mf.Apps {
			v.Disk = append(manifest.Disk(nil), v.Disk...)
			mf.Apps[k] = v
		}
		mutate(&mf)
		res, err := change.Resources(&mf)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range desired {
			res[k] = v
		}
		return h.m.CheckPlan(ctx, h.p, "shop", res)
	}
	var prob *api.Problem
	err := check(func(mf *manifest.Manifest) { a := mf.Apps["api"]; a.Disk[0].Size = "2MB"; mf.Apps["api"] = a }, nil)
	if !errors.As(err, &prob) || !strings.Contains(prob.Detail, "cannot shrink below what it holds") {
		t.Fatalf("shrink below usage: %v", err)
	}
	if err := check(func(mf *manifest.Manifest) { a := mf.Apps["api"]; a.Disk[0].Size = "5MB"; mf.Apps["api"] = a }, nil); err != nil {
		t.Fatalf("shrinking to above usage is fine: %v", err)
	}
	limit := map[string]change.Resource{change.KindStorageLimit: {Address: change.KindStorageLimit, Spec: []byte(`{"maxBytes":104857600}`)}}
	if err := check(func(*manifest.Manifest) {}, limit); !errors.As(err, &prob) || !strings.Contains(prob.Detail, "more than project shop's storage limit") {
		t.Fatalf("20 MB + 1 GB of folders under a 100 MB limit: %v", err)
	}
	listed := func(mf *manifest.Manifest) {
		a := mf.Apps["api"]
		a.Disk = manifest.Disk{{Path: "data"}, {Path: "cache"}}
		mf.Apps["api"] = a
	}
	if err := check(listed, limit); err != nil {
		t.Fatalf("folders listed without sizes (from before sizes) are not counted: %v", err)
	}
	if err := check(func(mf *manifest.Manifest) { a := mf.Apps["api"]; a.Disk[1].Size = "50MB"; mf.Apps["api"] = a }, limit); err != nil {
		t.Fatalf("70 MB of folders under a 100 MB limit: %v", err)
	}

	// The disk guard's hold: what it holds is the limit; lifted, its size again.
	if err := HoldDisks(ctx, "shop", true); err != nil {
		t.Fatal(err)
	}
	if n := fq.limitOf(t, filepath.Join(prod, "data")); n != 3<<20+4 {
		t.Fatalf("held limit %d", n)
	}
	if err := HoldDisks(ctx, "shop", false); err != nil {
		t.Fatal(err)
	}
	if n := fq.limitOf(t, filepath.Join(prod, "data")); n != 20<<20 {
		t.Fatalf("lifted limit %d", n)
	}

	// Nearly full: health says which folder.
	os.WriteFile(filepath.Join(prod, "data", "big"), make([]byte, 19<<20), 0o644)
	forgetDiskBytes("shop")
	if c := h.r.diskCheck(ctx); c == nil || c.OK || !strings.Contains(c.Detail, "shop/api/prod/data (19.0 MB of 20.0 MB)") {
		t.Fatalf("health %+v", c)
	}

	// A preview's folders are its own, of the same size, and go with it.
	if d := h.deploy("api", "pr-1", map[string]string{"index.ts": "pv"}); d.Status != StatusLive {
		t.Fatalf("preview: %s %s", d.Status, d.Error)
	}
	pv := h.r.diskDir("shop", "api", "pr-1")
	if n := fq.limitOf(t, filepath.Join(pv, "data")); n != 20<<20 {
		t.Fatalf("preview's limit %d", n)
	}
	if err := h.r.deletePreview(ctx, "shop", "api", "pr-1"); err != nil {
		t.Fatal(err)
	}
	for _, f := range DiskFolders(ctx, "shop") {
		if f.Preview != "" {
			t.Fatalf("a deleted preview's folder is still counted: %+v", f)
		}
	}
}

// TestDiskSizeFloor: a folder that held more than its size when sizes came
// in (an upgrade) keeps room for what it held plus 1 GB.
func TestDiskSizeFloor(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t)
	q := newQuotas(newFakeQuota(), "", h.p.DB, dir)
	data := filepath.Join(dir, "shop", "api", "prod", "data")
	os.MkdirAll(data, 0o755)
	os.WriteFile(filepath.Join(data, "db"), make([]byte, 2<<20), 0o644)
	var log strings.Builder
	if err := q.apply(context.Background(), "shop", "api", "", manifest.Disk{{Path: "data", Size: "1MB"}}, nil, &log); err != nil {
		t.Fatal(err)
	}
	fs := q.folders(context.Background(), "shop")
	if len(fs) != 1 || fs[0].SizeBytes != 2<<30 || !strings.Contains(log.String(), "until you give it a size") {
		t.Fatalf("%+v %s", fs, log.String())
	}
	// Given a size it fits in, that is its size.
	if err := q.apply(context.Background(), "shop", "api", "", manifest.Disk{{Path: "data", Size: "5MB"}}, nil, &log); err != nil {
		t.Fatal(err)
	}
	if fs := q.folders(context.Background(), "shop"); fs[0].SizeBytes != 5<<20 {
		t.Fatalf("%+v", fs)
	}
	// Without project quotas there is nothing to report or hold.
	off := newQuotas(nil, "no quotas here", h.p.DB, dir)
	if off.folders(context.Background(), "shop") != nil || off.apply(context.Background(), "shop", "api", "", manifest.Disk{{Path: "data"}}, nil, &log) != nil {
		t.Fatal("no quotas: nothing to do")
	}
	if _, ok := off.projectBytes(context.Background(), "shop"); ok {
		t.Fatal("no quotas: usage is measured by walking")
	}
}
