//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A project's title, on a real box through the CLI: it shows beside the
// name, moves nothing (the database and its rows stay as they are), a
// config without it plans to clear it as drift, undo takes it away, and
// a config that carries it sets it.
func TestProjectTitle(t *testing.T) {
	b := newCLIBox(t, "title", "shop")
	b.apply("shop", `{"project":"shop","services":{"postgres":{}}}`)
	b.ok("sql", "shop", "--write", "create table orders (id int); insert into orders values (1), (2)")

	set := b.ok("projects", "title", "shop", "Shop storefront")
	ch, _ := set["change"].(map[string]any)
	id, _ := ch["id"].(string)
	if set["applied"] != true || id == "" {
		b.fatalf("projects title: %v", set)
	}
	list := b.list("projects", "list")
	if len(list) != 1 || list[0]["name"] != "shop" || list[0]["title"] != "Shop storefront" {
		b.fatalf("projects list: %v", list)
	}
	if got := b.ok("projects", "get", "shop"); got["name"] != "shop" || got["title"] != "Shop storefront" {
		b.fatalf("projects get: %v", got)
	}
	rows := b.ok("sql", "shop", "select count(*) as n from orders")
	if !strings.Contains(fmt.Sprint(rows["results"]), "2") {
		b.fatalf("rows after the title: %v", rows)
	}

	// The config without a title would clear it: the plan says it was set elsewhere.
	untitled := filepath.Join(b.dir, "project", "untitled.json")
	if err := os.WriteFile(untitled, []byte(`{"project":"shop","services":{"postgres":{}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := b.ok("plan", untitled)
	if ops, _ := plan["ops"].([]any); len(ops) != 1 || ops[0].(map[string]any)["address"] != "project" || ops[0].(map[string]any)["drift"] == nil {
		b.fatalf("plan from a config without the title: %v", plan)
	}

	// Undo takes it away; the config's title puts it back.
	_, undo := b.run("undo", id)
	var u struct {
		Plan struct {
			Hash string `json:"hash"`
		} `json:"plan"`
	}
	if _ = json.Unmarshal([]byte(undo), &u); len(u.Plan.Hash) < 12 {
		b.fatalf("undo plan: %s", undo)
	}
	b.ok("undo", id, "--confirm", u.Plan.Hash[:12], "-m", "no title")
	if l := b.list("projects", "list"); l[0]["title"] != nil {
		b.fatalf("title after undo: %v", l)
	}
	b.apply("shop", `{"project":"shop","title":"From the config","services":{"postgres":{}}}`)
	if l := b.list("projects", "list"); l[0]["title"] != "From the config" || l[0]["name"] != "shop" {
		b.fatalf("title from the config: %v", l)
	}
	if code, out := b.run("projects", "title", "shop", "two\nlines"); code == 0 {
		b.fatalf("a two-line title was taken: %s", out)
	}
}
