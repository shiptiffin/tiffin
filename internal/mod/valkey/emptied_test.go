package valkey

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/state"
)

// Delete all data on a real valkey-server: the project's keys are saved to
// a file and deleted, other projects' keys stay; deleting the emptied
// resource puts them back, with their expiry, in place of what is there.
func TestEmptyAndRestore(t *testing.T) {
	s := startValkey(t)
	ctx := context.Background()
	old := adminConn
	adminConn = s.admin
	t.Cleanup(func() { adminConn = old })
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := &platform.Platform{DB: db, Engine: change.NewEngine(db), DataRoot: t.TempDir(), Log: slog.New(slog.DiscardHandler)}
	addr := change.EmptyAddress("valkey")
	set := func(withEmptied bool) {
		t.Helper()
		plan, err := p.Engine.PlanEdit(ctx, "shop", func(map[string]change.Resource) (map[string]change.Resource, error) {
			res := map[string]change.Resource{
				change.KindProject:             {Address: change.KindProject, Spec: json.RawMessage(`{}`)},
				change.KindService + "/valkey": {Address: change.KindService + "/valkey", Spec: json.RawMessage(`{"maxMemoryMB":64}`)},
			}
			if withEmptied {
				res[addr] = change.Resource{Address: addr, Spec: json.RawMessage(`{"version":2}`)}
			}
			return res, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Engine.Apply(ctx, change.ApplyRequest{Plan: plan, Confirm: plan.Hash, Actor: change.Actor{Kind: "human", ID: "t"}}); err != nil {
			t.Fatal(err)
		}
	}
	set(false)
	a, err := s.admin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	shop, other := Prefix("shop"), Prefix("other")
	for _, cmd := range [][]string{
		{"SET", shop + "plain", "v\x00binary"},
		{"SET", shop + "session", "s", "PX", "600000"},
		{"HSET", shop + "user:1", "name", "Ada", "plan", "pro"},
		{"RPUSH", shop + "queue", "a", "b", "c"},
		{"SET", other + "keep", "x"},
	} {
		if _, err := a.Do(ctx, cmd...); err != nil {
			t.Fatal(err)
		}
	}
	count := func(prefix string) int {
		n := 0
		cursor := "0"
		for {
			v, err := a.Do(ctx, "SCAN", cursor, "MATCH", prefix+"*", "COUNT", "100")
			if err != nil {
				t.Fatal(err)
			}
			next, keys := scanReply(v)
			n += len(keys)
			if cursor = next; cursor == "0" {
				return n
			}
		}
	}

	set(true)
	var m Module
	spec := json.RawMessage(`{"version":2}`)
	if err := m.Reconcile(ctx, p, "shop", addr, spec); err != nil {
		t.Fatal(err)
	}
	if n := count(shop); n != 0 {
		t.Fatalf("%d of the project's keys left after Delete all data", n)
	}
	if count(other) != 1 {
		t.Fatal("another project's key went too")
	}
	raw, _, _ := db.KVGet(ctx, nsEmptied, "shop")
	var rec emptiedRecord
	_ = json.Unmarshal(raw, &rec)
	if rec.Keys != 4 || !rec.Done {
		t.Fatalf("record %+v", rec)
	}
	if _, err := os.Stat(rec.File); err != nil {
		t.Fatal(err)
	}
	// Written after the delete; the restore replaces it. A second pass changes nothing.
	if _, err := a.Do(ctx, "SET", shop+"new", "1"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(ctx, p, "shop", addr, spec); err != nil || count(shop) != 1 {
		t.Fatalf("a second pass: %v", err)
	}

	set(false)
	if err := m.Reconcile(ctx, p, "shop", addr, nil); err != nil {
		t.Fatal(err)
	}
	if n := count(shop); n != 4 {
		t.Fatalf("%d keys after restore, want 4", n)
	}
	if v, _ := a.String(ctx, "GET", shop+"plain"); v != "v\x00binary" {
		t.Fatalf("plain = %q", v)
	}
	if v, _ := a.String(ctx, "HGET", shop+"user:1", "plan"); v != "pro" {
		t.Fatalf("hash field = %q", v)
	}
	if ttl, _ := a.Int(ctx, "PTTL", shop+"session"); ttl <= 0 || ttl > int64(10*time.Minute/time.Millisecond) {
		t.Fatalf("session ttl %d", ttl)
	}
	if ttl, _ := a.Int(ctx, "PTTL", shop+"plain"); ttl != -1 {
		t.Fatalf("plain ttl %d", ttl)
	}
	if _, ok, _ := db.KVGet(ctx, nsEmptied, "shop"); ok {
		t.Fatal("the record must go once the keys are back")
	}
	if _, err := os.Stat(rec.File); !os.IsNotExist(err) {
		t.Fatal("the saved keys must go once they are back")
	}
}
