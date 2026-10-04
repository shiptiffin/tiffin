package storage

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
)

// The storage limit is off by default and counts the database with the
// files; a read-only hold refuses every upload with its own words.
func TestRefusal(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := &platform.Platform{DB: db}
	m := &Module{}
	meta := map[string]*bucketMeta{"shop-media": {Project: "shop"}, "blog-media": {Project: "blog"}}
	m.tracker().buckets = map[string]Usage{"shop-media": {Bytes: 60}, "blog-media": {Bytes: 500}}
	SetDatabaseBytes("shop", 30)
	defer SetDatabaseBytes("shop", 0)

	if what, _ := m.refusal(ctx, p, meta, "shop", 1<<40); what != "" {
		t.Fatalf("no limit by default: %q", what)
	}
	if err := db.KVPut(ctx, kvNS, "quota/shop", []byte("100")); err != nil {
		t.Fatal(err)
	}
	if what, _ := m.refusal(ctx, p, meta, "shop", 10); what != "" {
		t.Fatalf("60 files + 30 database + 10 fits in 100: %q", what)
	}
	what, fix := m.refusal(ctx, p, meta, "shop", 11)
	if !strings.Contains(what, "over its storage limit") || !strings.Contains(what, "database 30 B, files 60 B") || !strings.Contains(fix, "tiffin storage quota set shop") ||
		!strings.HasPrefix(fix, "Delete files (60 B) or data from its database (30 B)") {
		t.Fatalf("over: %q %q", what, fix)
	}
	// The project's "storagelimit" resource sets the limit (undo removes it).
	if err := m.Reconcile(ctx, p, "shop", change.KindStorageLimit, json.RawMessage(`{"maxBytes":200}`)); err != nil {
		t.Fatal(err)
	}
	if n, own, _ := Limit(ctx, p, "shop"); n != 200 || !own {
		t.Fatalf("limit from the resource: %d %v", n, own)
	}
	if err := m.Reconcile(ctx, p, "shop", change.KindStorageLimit, nil); err != nil {
		t.Fatal(err)
	}
	if n, own, _ := Limit(ctx, p, "shop"); n != 0 || own {
		t.Fatalf("limit after deleting the resource: %d %v", n, own)
	}
	_ = db.KVPut(ctx, kvNS, "quota/shop", []byte("100"))
	if what, _ := m.refusal(ctx, p, meta, "blog", 1); what != "" {
		t.Fatalf("blog has no limit: %q", what)
	}

	// Held for its limit, shop goes by the limit itself: raising it lets
	// uploads in before the guard's next round lifts the hold.
	SetReadOnly("shop", "limit", "shop uses 90 B of its 100 B storage limit, so it is read-only.")
	defer SetReadOnly("shop", "", "")
	if what, _ := m.refusal(ctx, p, meta, "shop", 11); !strings.Contains(what, "over its storage limit") {
		t.Fatalf("held for its limit: %q", what)
	}
	_ = db.KVPut(ctx, kvNS, "quota/shop", []byte("1000"))
	if what, _ := m.refusal(ctx, p, meta, "shop", 11); what != "" || ReadOnly("shop") == "" {
		t.Fatalf("limit raised: %q", what)
	}

	SetReadOnly("blog", "disk", "The box's data disk is 96% full and blog grew the most, so it is read-only.")
	defer SetReadOnly("blog", "", "")
	if what, _ := m.refusal(ctx, p, meta, "blog", 0); !strings.Contains(what, "96% full") {
		t.Fatalf("held: %q", what)
	}
}
