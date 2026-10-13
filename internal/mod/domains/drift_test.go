package domains

import (
	"strings"
	"testing"
)

// A domain added with `tiffin domains add`, then a plan from the config
// file written before it: the plan marks what it would undo, apply needs
// discardDrift, and tiffin pull clears it.
func TestPlanDriftAfterDomainsAdd(t *testing.T) {
	h := newHarness(t)
	old := map[string]any{"project": "shop", "apps": map[string]any{"web": map[string]any{}, "api": map[string]any{}, "jobs": map[string]any{"role": "worker"}}}
	h.confirmed("POST", "/v1/apply", map[string]any{"manifest": old})
	added := h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "example.test", "app": "web", "www": true})
	addedID := added["change"].(map[string]any)["id"].(string)
	if via := added["change"].(map[string]any)["via"]; via != "tiffin domains add" {
		t.Errorf("via: %v", via)
	}

	code, plan := h.call("POST", "/v1/plan", map[string]any{"manifest": old})
	if code != 200 {
		t.Fatalf("plan: %d %v", code, plan)
	}
	drifted := map[string]map[string]any{}
	for _, o := range plan["ops"].([]any) {
		op := o.(map[string]any)
		if d, ok := op["drift"].(map[string]any); ok {
			drifted[op["action"].(string)+" "+op["address"].(string)] = d
		}
	}
	if d := drifted["delete domain/example.test"]; d == nil || d["change"] != addedID || d["via"] != "tiffin domains add" {
		t.Errorf("domain op: %v", drifted)
	}
	if d := drifted["update app/web"]; d == nil || len(d["fields"].([]any)) != 1 || d["fields"].([]any)[0] != "routes" {
		t.Errorf("app op: %v", drifted)
	}
	w := plan["warnings"].([]any)[0].(string)
	if !strings.HasPrefix(w, "Your tiffin.config.ts doesn't have changes made outside it: ") || !strings.Contains(w, "domain/example.test (added ") ||
		!strings.Contains(w, "with `tiffin domains add`") || !strings.Contains(w, "tiffin pull") {
		t.Errorf("warning: %q", w)
	}

	// The same manifest as an edit of the current one (the dashboard's way)
	// is a plain plan.
	if _, p := h.call("POST", "/v1/plan", map[string]any{"manifest": old, "source": "edit"}); strings.Contains(string(mustJSON(p)), `"drift"`) {
		t.Errorf("source edit: %v", p)
	}

	// Apply: the plan first, then the hash alone is refused.
	code, out := h.call("POST", "/v1/apply", map[string]any{"manifest": old})
	if code != 428 || out["code"] != "confirm_required" {
		t.Fatalf("no confirm: %d %v", code, out)
	}
	hash := out["plan"].(map[string]any)["hash"]
	code, out = h.call("POST", "/v1/apply", map[string]any{"manifest": old, "confirm": hash})
	if code != 428 || out["code"] != "drift" || !strings.Contains(out["hint"].(string), "tiffin pull") || out["plan"] == nil {
		t.Fatalf("confirm without discardDrift: %d %v", code, out)
	}
	if d := h.domain("shop", "example.test"); d == nil {
		t.Fatal("the domain went away without discardDrift")
	}

	// tiffin pull: the current manifest plans nothing and nothing drifts.
	_, pulled := h.call("GET", "/v1/projects/shop/manifest", nil)
	code, p := h.call("POST", "/v1/plan", map[string]any{"manifest": pulled["manifest"]})
	if code != 200 || len(p["ops"].([]any)) != 0 || p["warnings"] != nil {
		t.Fatalf("after pull: %d %v", code, p)
	}

	// Discarding on purpose removes the domain, recorded as a config apply.
	code, out = h.call("POST", "/v1/apply", map[string]any{"manifest": old, "confirm": hash, "discardDrift": true})
	if code != 200 || out["applied"] != true || out["change"].(map[string]any)["via"] != "config" {
		t.Fatalf("discardDrift: %d %v", code, out)
	}
	if d := h.domain("shop", "example.test"); d != nil {
		t.Errorf("still there: %v", d)
	}
	// Now the config file is the latest word: no drift.
	if _, p := h.call("POST", "/v1/plan", map[string]any{"manifest": old}); len(p["ops"].([]any)) != 0 {
		t.Errorf("after discard: %v", p)
	}
}
