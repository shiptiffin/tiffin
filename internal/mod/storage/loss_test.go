package storage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/platform"
)

func TestEstimateLoss(t *testing.T) {
	root := t.TempDir()
	p := &platform.Platform{DataRoot: root}
	dir := filepath.Join(dataDir(root), S3Name("shop", "uploads"))
	if err := os.MkdirAll(filepath.Join(dir, "receipts"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, size := range map[string]int{"receipts/1042.pdf": 3000, "avatar.png": 1500} {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := &Module{}
	del := change.Op{Action: change.Delete, Address: "bucket/uploads", Before: json.RawMessage(`{}`)}
	l, err := m.EstimateLoss(context.Background(), p, "shop", del)
	if err != nil || l == nil {
		t.Fatalf("loss %v, err %v", l, err)
	}
	if l.Bytes != 4500 || len(l.Counts) != 1 || l.Counts[0].N != 2 || l.Counts[0].Unit != "file" {
		t.Fatalf("got %+v", l)
	}
	if s := l.Summarize().Summary; s != "2 files · 4.4 KB" {
		t.Fatalf("summary %q", s)
	}
	// Not a bucket delete, or a bucket that never existed: no claim either way.
	for _, op := range []change.Op{
		{Action: change.Update, Address: "bucket/uploads"},
		{Action: change.Delete, Address: "service/storage"},
		{Action: change.Delete, Address: "bucket/missing"},
	} {
		if l, err := m.EstimateLoss(context.Background(), p, "shop", op); l != nil || err != nil {
			t.Fatalf("%s %s: %+v %v", op.Action, op.Address, l, err)
		}
	}
	// Counted now, not from a stale scan: a file uploaded since still counts.
	m.tracker().refresh(context.Background(), p)
	if err := os.WriteFile(filepath.Join(dir, "late.png"), make([]byte, 500), 0o644); err != nil {
		t.Fatal(err)
	}
	if l, _ := m.EstimateLoss(context.Background(), p, "shop", del); l == nil || l.Bytes != 5000 || l.Counts[0].N != 3 {
		t.Fatalf("after an upload: %+v", l)
	}
	// Out of time: the last scan stands in.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if l, _ := m.EstimateLoss(ctx, p, "shop", del); l == nil || l.Bytes != 4500 || l.Counts[0].N != 2 {
		t.Fatalf("from the scan: %+v", l)
	}
}
