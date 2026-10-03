//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestDrill is the restore-drill acceptance test on a fresh box: a project
// with Postgres rows → full backup → a drill passes with the exact row
// counts and leaves nothing behind → the backup's table list gains a table
// the backup never had (a missing-table simulation) → the drill fails and
// names it → a table file in the pgBackRest repository is corrupted → the
// drill fails at restore → the restore-drill check reports the failure.
func TestDrill(t *testing.T) {
	start := time.Now()
	phase := phaseLogger(t)
	p := time.Now()
	b := newCLIBox(t, "drill", "shop")
	phase("up", p)

	p = time.Now()
	b.apply("shop", `{"project":"shop","services":{"postgres":{}}}`)
	b.waitReady("service/postgres")
	sql := func(body map[string]any) [][]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		res := b.ok("sql", "shop", "--body", string(raw))
		r, _ := res["results"].([]any)
		if len(r) == 0 {
			return nil
		}
		var rows [][]any
		enc, _ := json.Marshal(r[len(r)-1].(map[string]any)["rows"])
		_ = json.Unmarshal(enc, &rows)
		return rows
	}
	sql(map[string]any{"write": true, "sql": `create table orders(id serial primary key, total int);
insert into orders(total) select g from generate_series(1, 5000) g;
create table customers(id int primary key, name text);
insert into customers select g, 'c' || g from generate_series(1, 1234) g;
create schema billing;
create table billing.invoices(id int);
insert into billing.invoices select generate_series(1, 77);`})
	want := map[string]float64{"public.orders": 5000, "public.customers": 1234, "billing.invoices": 77}
	phase("data", p)

	p = time.Now()
	bk := b.ok("backup", "--kind", "full")
	if bk["status"] != "ok" {
		t.Fatalf("backup: %v", bk)
	}
	bkID := bk["id"].(string)
	label := bk["postgres"].(map[string]any)["label"].(string)
	phase("backup", p)

	// drill runs one and waits for it to end.
	drill := func(args ...string) map[string]any {
		t.Helper()
		d := b.ok(append(args, "--wait")...)
		id, _ := d["id"].(string)
		if !strings.HasPrefix(id, "dr_") {
			t.Fatalf("%v: %v", args, d)
		}
		deadline := time.Now().Add(10 * time.Minute)
		for d["status"] == "running" {
			if time.Now().After(deadline) {
				t.Fatalf("drill %s still running: %v", id, d)
			}
			time.Sleep(2 * time.Second)
			d = b.ok("backups", "drills", "get", id)
		}
		secs, _ := d["seconds"].(map[string]any)
		t.Logf("DRILL %s %s: restore %vs, start %vs, verify %vs, total %vs, %v bytes restored: %v",
			id, d["status"], secs["restore"], secs["start"], secs["verify"], secs["total"], d["restoredBytes"], d["message"])
		return d
	}
	leftovers := func() {
		t.Helper()
		if got := b.inBox("sudo find /var/lib/tiffin/drill -mindepth 1 -maxdepth 1 | wc -l"); got != "0" {
			t.Fatalf("scratch directories left behind: %s", got)
		}
		if got := b.inBox("pgrep -fc '[t]iffin/drill/' || true"); got != "0" {
			t.Fatalf("scratch Postgres still running: %s", b.inBox("pgrep -fa '[t]iffin/drill/' || true"))
		}
	}
	check := func() map[string]any {
		t.Helper()
		for _, c := range b.ok("status")["checks"].([]any) {
			if c := c.(map[string]any); c["name"] == "restore-drill" {
				return c
			}
		}
		t.Fatal("no restore-drill check")
		return nil
	}
	if c := check(); c["ok"] != true || !strings.Contains(fmt.Sprint(c["detail"]), "no restore drill yet") {
		t.Fatalf("restore-drill check on a fresh box: %v", c)
	}

	// ---- a drill of the newest backup passes with matching counts ----
	p = time.Now()
	d := drill("backups", "drill")
	if d["status"] != "passed" || d["backup"] != bkID || d["comparedWith"] != "backup" {
		t.Fatalf("drill: %v", d)
	}
	var shop map[string]any
	for _, x := range d["databases"].([]any) {
		if x := x.(map[string]any); x["name"] == "p_shop" {
			shop = x
		}
	}
	if shop == nil || shop["ok"] != true || shop["tables"].(float64) != 3 || shop["rows"].(float64) != 6311 || shop["liveTables"].(float64) != 3 {
		t.Fatalf("p_shop: %v", shop)
	}
	for _, c := range shop["counts"].([]any) {
		c := c.(map[string]any)
		if c["rows"] != want[c["table"].(string)] || c["exact"] != true || c["liveRows"] != c["rows"] {
			t.Fatalf("count of %v: %v", c["table"], c)
		}
	}
	if secs := d["seconds"].(map[string]any); secs["restore"].(float64) <= 0 || secs["total"].(float64) <= 0 {
		t.Fatalf("timings: %v", secs)
	}
	leftovers()
	if c := check(); c["ok"] != true || !strings.Contains(fmt.Sprint(c["detail"]), "restore drill passed") {
		t.Fatalf("restore-drill check after a pass: %v", c)
	}
	if last := b.ok("backups", "list")["lastDrill"].(map[string]any); last["id"] != d["id"] {
		t.Fatalf("lastDrill: %v", last)
	}
	// The live cluster and its archive are untouched.
	b.inBox("sudo -u postgres pgbackrest --stanza=tiffin --log-level-console=warn check")
	if rows := sql(map[string]any{"sql": "select count(*) from orders"}); fmt.Sprint(rows) != "[[5000]]" {
		t.Fatalf("live rows: %v", rows)
	}
	phase("drill passes", p)

	// ---- a missing table: the backup's table list names a table it never had ----
	p = time.Now()
	b.inBox(`sudo python3 - <<'EOF'
import json
p = "/var/lib/tiffin/backups/sets/` + bkID + `/catalog.json"
c = json.load(open(p))
for d in c["databases"]:
    if d["name"] == "p_shop":
        d["tables"].append({"name": "public.ghost", "rows": 10})
json.dump(c, open(p, "w"))
EOF`)
	d = drill("backups", "drills", "start", bkID)
	if d["status"] != "failed" || !strings.Contains(fmt.Sprint(d["message"]), "p_shop (1 missing table: public.ghost)") {
		t.Fatalf("missing table must fail the drill: %v", d)
	}
	leftovers()
	if c := check(); c["ok"] != false || !strings.Contains(fmt.Sprint(c["detail"]), "public.ghost") {
		t.Fatalf("restore-drill check after a failure: %v", c)
	}
	phase("missing table", p)

	// ---- a corrupted backup: the orders table's file in the repository ----
	p = time.Now()
	rel := sql(map[string]any{"sql": "select pg_relation_filepath('orders')"})[0][0].(string)
	file := b.inBox("sudo find /var/lib/tiffin/backups/pgbackrest/backup/tiffin/" + label + " -path '*pg_data/" + rel + "*' | head -1")
	if file == "" {
		t.Fatalf("no repository file for %s in %s: %s", rel, label, b.inBox("sudo ls -R /var/lib/tiffin/backups/pgbackrest/backup/tiffin/"+label+" | head -40"))
	}
	b.inBox("sudo dd if=/dev/urandom of=" + file + " bs=1k count=8 conv=notrunc status=none")
	d = drill("backups", "drills", "start", bkID)
	if msg := fmt.Sprint(d["message"]); d["status"] != "failed" || !strings.Contains(msg, "into the scratch directory failed") || strings.Contains(msg, "WARN") {
		t.Fatalf("a corrupted backup must fail the drill: %v", d)
	}
	leftovers()
	phase("corrupted", p)

	// ---- the schedule setting ----
	s := b.ok("backups", "schedule", "--drill-every-days", "3")
	if s["drillEveryDays"] != float64(3) || s["drillEnabled"] != true {
		t.Fatalf("schedule: %v", s)
	}
	if l := b.list("backups", "drills"); len(l) != 3 {
		t.Fatalf("drills: %d", len(l))
	}
	t.Logf("TOTAL %s", time.Since(start).Round(time.Second))
}
