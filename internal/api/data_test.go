package api_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// Delete all data and its restore go through the change engine: the empty
// asks for a confirm (it is irreversible), History records both, the
// project's state says what is restorable until when, and past 7 days a
// restore is refused. Leaving a part out of the config never removes it.
func TestDataEmptyAndRestore(t *testing.T) {
	e := newEnv(t)
	e.applyManifest(shop)
	path := "/v1/projects/shop/data/postgres/empty"
	code, prob, _ := e.call(e.owner, "POST", path, map[string]any{})
	plan, _ := prob["plan"].(map[string]any)
	if code != 428 || plan["risk"] != "irreversible" {
		t.Fatalf("empty without confirm: %d %v", code, prob)
	}
	ops := plan["ops"].([]any)
	if op := ops[0].(map[string]any); len(ops) != 1 || op["address"] != "emptied/postgres" || op["action"] != "create" {
		t.Fatalf("ops %v", ops)
	}
	code, out, _ := e.call(e.owner, "POST", path, map[string]any{"confirm": plan["hash"]})
	if code != 200 || out["applied"] != true {
		t.Fatalf("empty: %d %v", code, out)
	}
	emptyID := out["change"].(map[string]any)["id"].(string)
	_, st, _ := e.call(e.owner, "GET", "/v1/projects/shop", nil)
	rs, _ := st["restorable"].([]any)
	if len(rs) != 1 || rs[0].(map[string]any)["part"] != "postgres" {
		t.Fatalf("restorable: %v", st["restorable"])
	}
	// A config without the database plans nothing.
	_, p2, _ := e.call(e.owner, "POST", "/v1/plan", map[string]any{"manifest": map[string]any{"project": "shop", "apps": shop["apps"]}})
	if ops, _ := p2["ops"].([]any); len(ops) != 1 || ops[0].(map[string]any)["address"] != "service/postgres" || ops[0].(map[string]any)["action"] != "update" {
		t.Fatalf("leaving postgres out only drops its extension: %v", p2)
	}

	restore := "/v1/projects/shop/data/postgres/restore"
	code, prob, _ = e.call(e.owner, "POST", restore, map[string]any{})
	plan, _ = prob["plan"].(map[string]any)
	if code != 428 || plan["ops"].([]any)[0].(map[string]any)["action"] != "delete" {
		t.Fatalf("restore without confirm: %d %v", code, prob)
	}
	if code, out, _ = e.call(e.owner, "POST", restore, map[string]any{"confirm": plan["hash"]}); code != 200 {
		t.Fatalf("restore: %d %v", code, out)
	}
	if _, st, _ = e.call(e.owner, "GET", "/v1/projects/shop", nil); st["restorable"] != nil {
		t.Fatalf("nothing restorable after the restore: %v", st["restorable"])
	}
	if code, prob, _ = e.call(e.owner, "POST", restore, map[string]any{}); code != 409 {
		t.Fatalf("restore twice: %d %v", code, prob)
	}

	// Emptied again, then 8 days pass: nothing to restore, by either way.
	code, prob, _ = e.call(e.owner, "POST", "/v1/projects/shop/data/valkey/empty", map[string]any{})
	if code != 428 {
		t.Fatalf("empty kv: %d %v", code, prob)
	}
	code, out, _ = e.call(e.owner, "POST", "/v1/projects/shop/data/valkey/empty", map[string]any{"confirm": prob["plan"].(map[string]any)["hash"]})
	if code != 200 {
		t.Fatalf("empty kv: %d %v", code, out)
	}
	kvID := out["change"].(map[string]any)["id"].(string)
	ctx := context.Background()
	raw, ok, _ := e.db.KVGet(ctx, "api.emptied", "shop/valkey")
	var rec map[string]any
	if !ok || json.Unmarshal(raw, &rec) != nil {
		t.Fatalf("no record of the delete: %s", raw)
	}
	rec["at"] = time.Now().Add(-8 * 24 * time.Hour)
	raw, _ = json.Marshal(rec)
	_ = e.db.KVPut(ctx, "api.emptied", "shop/valkey", raw)
	if _, st, _ = e.call(e.owner, "GET", "/v1/projects/shop", nil); st["restorable"] != nil {
		t.Fatalf("past 7 days nothing is restorable: %v", st["restorable"])
	}
	if code, prob, _ = e.call(e.owner, "POST", "/v1/projects/shop/data/valkey/restore", map[string]any{}); code != 409 || prob["code"] != "precondition" {
		t.Fatalf("restore past 7 days: %d %v", code, prob)
	}
	if code, prob, _ = e.call(e.owner, "POST", "/v1/changes/"+kvID+"/undo", map[string]any{}); code != 409 {
		t.Fatalf("undo past 7 days: %d %v", code, prob)
	}
	// The first empty was restored already: its undo no longer applies.
	if code, _, _ = e.call(e.owner, "POST", "/v1/changes/"+emptyID+"/undo", map[string]any{}); code != 409 {
		t.Fatalf("undo of a restored empty: %d", code)
	}
	if code, _, _ = e.call(e.owner, "POST", "/v1/projects/shop/data/nope/empty", map[string]any{}); code != 422 {
		t.Fatalf("unknown part: %d", code)
	}
}
