package api_test

import (
	"strings"
	"testing"
)

// A project's title is a label: set, shown beside the name in the list and
// the project, written into its config, undone like any change, cleared by
// an empty title. Its name stays the key everywhere.
func TestProjectTitle(t *testing.T) {
	e := newEnv(t)
	_, plan, _ := e.call(e.owner, "POST", "/v1/plan", map[string]any{"manifest": shop})
	e.call(e.owner, "POST", "/v1/apply", map[string]any{"manifest": shop, "confirm": plan["hash"]})

	code, out, _ := e.call(e.owner, "POST", "/v1/projects/shop/title", map[string]any{"title": "  Shop storefront  "})
	if code != 200 || out["applied"] != true {
		t.Fatalf("set title: %d %v", code, out)
	}
	c := out["change"].(map[string]any)
	ops := c["plan"].(map[string]any)["ops"].([]any)
	op := ops[0].(map[string]any)
	if len(ops) != 1 || op["address"] != "project" || op["risk"] != "reversible" || !strings.Contains(op["reason"].(string), "title") {
		t.Fatalf("title ops: %v", ops)
	}
	if c["intent"] != `Title shop "Shop storefront"` || c["via"] != "tiffin projects title" {
		t.Fatalf("title change: intent %v via %v", c["intent"], c["via"])
	}
	_, _, list := e.call(e.owner, "GET", "/v1/projects", nil)
	if p := list[0].(map[string]any); p["name"] != "shop" || p["title"] != "Shop storefront" {
		t.Fatalf("list: %v", list)
	}
	if _, got, _ := e.call(e.owner, "GET", "/v1/projects/shop", nil); got["name"] != "shop" || got["title"] != "Shop storefront" {
		t.Fatalf("project: %v", got)
	}
	_, man, _ := e.call(e.owner, "GET", "/v1/projects/shop/manifest", nil)
	if man["manifest"].(map[string]any)["title"] != "Shop storefront" || !strings.Contains(man["config"].(string), `title: "Shop storefront"`) {
		t.Fatalf("manifest: %v", man)
	}

	// The same title again changes nothing.
	if code, again, _ := e.call(e.owner, "POST", "/v1/projects/shop/title", map[string]any{"title": "Shop storefront"}); code != 200 || again["applied"] != false {
		t.Fatalf("same title: %d %v", code, again)
	}
	// Undo takes it away.
	if code, undo, _ := e.call(e.owner, "POST", "/v1/changes/"+c["id"].(string)+"/undo", map[string]any{"confirm": "x"}); code != 428 {
		t.Fatalf("undo plan: %d %v", code, undo)
	} else {
		hash := undo["plan"].(map[string]any)["hash"]
		if code, u, _ := e.call(e.owner, "POST", "/v1/changes/"+c["id"].(string)+"/undo", map[string]any{"confirm": hash}); code != 200 || u["applied"] != true {
			t.Fatalf("undo: %d %v", code, u)
		}
	}
	if _, _, list := e.call(e.owner, "GET", "/v1/projects", nil); list[0].(map[string]any)["title"] != nil {
		t.Fatalf("title after undo: %v", list)
	}

	// An empty title clears it.
	e.call(e.owner, "POST", "/v1/projects/shop/title", map[string]any{"title": "Shop"})
	if code, out, _ := e.call(e.owner, "POST", "/v1/projects/shop/title", map[string]any{"title": " "}); code != 200 || out["applied"] != true ||
		out["change"].(map[string]any)["intent"] != "Clear the title of shop" {
		t.Fatalf("clear: %d %v", code, out)
	}
	if _, got, _ := e.call(e.owner, "GET", "/v1/projects/shop", nil); got["title"] != nil {
		t.Fatalf("title after clear: %v", got)
	}

	// The config sets it the same way.
	titled := map[string]any{"project": "shop", "title": "From the config", "apps": shop["apps"], "services": shop["services"]}
	_, plan, _ = e.call(e.owner, "POST", "/v1/plan", map[string]any{"manifest": titled})
	if code, out, _ := e.call(e.owner, "POST", "/v1/apply", map[string]any{"manifest": titled, "confirm": plan["hash"]}); code != 200 || out["applied"] != true {
		t.Fatalf("config title: %d %v", code, out)
	}
	if _, got, _ := e.call(e.owner, "GET", "/v1/projects/shop", nil); got["title"] != "From the config" {
		t.Fatalf("config title: %v", got)
	}

	// Refused: too long, control characters, a project that isn't there, a read-only key.
	for _, bad := range []string{strings.Repeat("x", 61), "two\nlines", "tab\there", "evil\u202Eeman"} {
		if code, prob, _ := e.call(e.owner, "POST", "/v1/projects/shop/title", map[string]any{"title": bad}); code != 422 {
			t.Fatalf("title %q: %d %v", bad, code, prob)
		}
	}
	if code, _, _ := e.call(e.owner, "POST", "/v1/projects/shop/title", map[string]any{"title": strings.Repeat("é", 60)}); code != 200 {
		t.Fatalf("60 characters refused: %d", code)
	}
	if code, _, _ := e.call(e.owner, "POST", "/v1/projects/nope/title", map[string]any{"title": "x"}); code != 404 {
		t.Fatalf("missing project: %d", code)
	}
	if code, _, _ := e.call(e.key("all", "read"), "POST", "/v1/projects/shop/title", map[string]any{"title": "x"}); code != 403 {
		t.Fatalf("read key: %d", code)
	}
	// The name is untouched throughout.
	if _, _, list := e.call(e.owner, "GET", "/v1/projects", nil); len(list) != 1 || list[0].(map[string]any)["name"] != "shop" {
		t.Fatalf("projects: %v", list)
	}
}
