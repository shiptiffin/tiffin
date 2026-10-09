package storage

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/manifest"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/state"
)

// Delete all data moves every bucket of the project to the trash, tagged so
// that making a bucket again does not bring it back; the restore puts the
// tagged ones back in place of what they hold by then (which goes to the
// trash, untagged). The gateway's part (owners, empty buckets) needs a
// real versitygw: see integration_test.go.
func TestEmptyAndTakeBack(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	p := &platform.Platform{DB: db, DataRoot: root, Log: slog.New(slog.DiscardHandler)}
	m := &Module{}
	bucket := func(project, name string, files map[string]string) {
		t.Helper()
		dir := filepath.Join(dataDir(root), S3Name(project, name))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for f, body := range files {
			if err := os.WriteFile(filepath.Join(dir, f), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := putMeta(ctx, p, S3Name(project, name), &bucketMeta{Project: project, Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	read := func(project, name, f string) string {
		b, _ := os.ReadFile(filepath.Join(dataDir(root), S3Name(project, name), f))
		return string(b)
	}
	bucket("shop", "files", map[string]string{"a.txt": "first", "b.txt": "second"})
	bucket("shop", "media", map[string]string{"cat.png": "meow"})
	bucket("other", "files", map[string]string{"keep.txt": "keep"})

	l, err := m.EstimateLoss(ctx, p, "shop", change.Op{Action: change.Create, Address: change.EmptyAddress("storage")})
	if err != nil || l == nil || l.Counts[0].N != 3 || l.Bytes != 15 {
		t.Fatalf("loss %+v %v", l, err)
	}
	n, err := m.trashAll(ctx, p, "shop", 2)
	if err != nil || n != 2 {
		t.Fatalf("trashAll: %d %v", n, err)
	}
	if read("shop", "files", "a.txt") != "" || read("other", "files", "keep.txt") != "keep" {
		t.Fatal("only the project's buckets go")
	}
	// Making the bucket again starts it empty: the tagged copy stays in the trash.
	if back, err := m.restoreBucket(ctx, p, "shop", "files"); err != nil || back {
		t.Fatalf("a bucket made again must not bring back the deleted data: %v %v", back, err)
	}
	bucket("shop", "files", map[string]string{"new.txt": "after"})

	back, err := m.takeBack(ctx, p, "shop", 2, map[string]manifest.Bucket{"files": {}, "media": {}})
	if err != nil || len(back) != 2 {
		t.Fatalf("takeBack: %v %v", back, err)
	}
	if read("shop", "files", "a.txt") != "first" || read("shop", "media", "cat.png") != "meow" || read("shop", "files", "new.txt") != "" {
		t.Fatal("the deleted files are not back in place of the new ones")
	}
	entries, _ := trashEntries(ctx, p)
	if len(entries) != 1 || entries[0].Bucket != "files" || entries[0].Emptied != 0 {
		t.Fatalf("what the bucket held by then goes to the trash, untagged: %+v", entries)
	}

	// A bucket the project no longer declares stays in the trash, untagged.
	if _, err := m.trashAll(ctx, p, "shop", 3); err != nil {
		t.Fatal(err)
	}
	if back, err := m.takeBack(ctx, p, "shop", 3, map[string]manifest.Bucket{"files": {}}); err != nil || len(back) != 1 {
		t.Fatalf("takeBack: %v %v", back, err)
	}
	entries, _ = trashEntries(ctx, p)
	for _, e := range entries {
		if e.Emptied != 0 {
			t.Fatalf("left tagged: %+v", e)
		}
	}
	// Deleting again replaces the earlier delete: its buckets go for good.
	if _, err := m.trashAll(ctx, p, "shop", 4); err != nil {
		t.Fatal(err)
	}
	if err := m.untagTrash(ctx, p, "shop", 4, true); err != nil {
		t.Fatal(err)
	}
	entries, _ = trashEntries(ctx, p)
	for _, e := range entries {
		if e.Emptied == 4 {
			t.Fatalf("not purged: %+v", e)
		}
	}
}
