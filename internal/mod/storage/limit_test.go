package storage

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
	"github.com/btahir/tiffin/internal/tokens"
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

// Setting a project's storage limit is a change in History (by whoever set
// it), kept by manifest plans; a project that does not exist has none.
func TestStorageLimitIsAChange(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tm := tokens.NewManager(db)
	owner, _, err := tm.Bootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p := &platform.Platform{DB: db, Engine: change.NewEngine(db), Tokens: tm, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	pg := map[string]change.Resource{"service/postgres": {Address: "service/postgres", Spec: json.RawMessage(`{}`)}}
	plan, _ := p.Engine.Plan(ctx, "shop", pg)
	if _, err := p.Engine.Apply(ctx, change.ApplyRequest{Plan: plan, Confirm: plan.Hash, Actor: change.Actor{Kind: "human", ID: "t"}}); err != nil {
		t.Fatal(err)
	}
	h := api.New(api.Deps{DB: db, Engine: p.Engine, Tokens: tm, Platform: p}).Handler()
	put := func(project, body string) (int, map[string]any) {
		req := httptest.NewRequest("PUT", "/v1/projects/"+project+"/storage/quota", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+owner)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var m map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		return w.Code, m
	}
	if code, out := put("shop", `{"maxBytes":1073741824}`); code != 200 || out["quotaBytes"] != float64(1<<30) || out["quotaSource"] != "project" {
		t.Fatalf("set: %d %v", code, out)
	}
	cs, _ := db.ListChanges(ctx, change.ListFilter{Project: "shop", Limit: 1})
	if c := cs[0]; c.Intent != "Set shop's storage limit to 1.0 GiB" || c.Actor.Kind != "human" || len(c.Plan.Ops) != 1 || c.Plan.Ops[0].Address != change.KindStorageLimit {
		t.Fatalf("history: %+v", c)
	}
	// A manifest plan keeps it, as it keeps secrets.
	if plan, _ := p.Engine.Plan(ctx, "shop", pg); !plan.Empty() {
		t.Fatalf("manifest plan touches the limit: %+v", plan.Ops)
	}
	if code, _ := put("shop", `{"maxBytes":0}`); code != 200 {
		t.Fatal("clear")
	}
	if cs, _ = db.ListChanges(ctx, change.ListFilter{Project: "shop", Limit: 1}); cs[0].Intent != "shop's storage limit follows the box default again" {
		t.Fatalf("cleared: %+v", cs[0])
	}
	if n, own, _ := Limit(ctx, p, "shop"); n != 0 || own {
		t.Fatalf("limit after clearing: %d %v", n, own)
	}
	if code, _ := put("nope", `{"maxBytes":5}`); code != 404 {
		t.Fatalf("unknown project: %d", code)
	}
}
