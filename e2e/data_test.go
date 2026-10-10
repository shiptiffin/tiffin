//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestData is the data acceptance test, driven through the CLI on a
// fresh box: apply postgres(vector)+valkey → ready → SQL create/insert →
// a ~1 GB table branched by reflink clone (timed) → Valkey through
// REDIS_URL inside the box (and its ACL) → backup → destroy data → restore
// (preview, confirm) → data back → Delete all data in the database
// (irreversible, a snapshot) → restore it.
func TestData(t *testing.T) {
	RequireLima(t)
	start := time.Now()
	phase := func(name string, since time.Time) {
		t.Logf("PHASE %-16s %s", name, time.Since(since).Round(100*time.Millisecond))
	}
	dir := t.TempDir()
	cli := buildTiffin(t, dir, "", "")
	bin := buildTiffin(t, dir, "linux", "0.0.1-data")

	instance, disk, port := newName(), newDiskName(), freePort(t)
	env := append(os.Environ(),
		"TIFFIN_CONFIG_DIR="+filepath.Join(dir, "config"),
		"TIFFIN_LIMA_INSTANCE="+instance, "TIFFIN_LIMA_DISK="+disk, fmt.Sprintf("TIFFIN_LIMA_PORT=%d", port),
		"TIFFIN_LIMA_MEMORY=3GiB", "TIFFIN_HOME=", "TIFFIN_URL=", "TIFFIN_TOKEN=")
	t.Cleanup(func() {
		if os.Getenv("TIFFIN_E2E_KEEP") != "1" {
			_ = exec.Command("limactl", "delete", "-f", instance).Run()
			_ = exec.Command("limactl", "disk", "delete", "-f", disk).Run()
		}
	})
	run := func(args ...string) (int, string) {
		t.Helper()
		cmd := exec.Command(cli, args...)
		cmd.Env, cmd.Dir = env, dir
		out, err := cmd.Output()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("tiffin %v: %v", args, err)
		}
		return code, string(out)
	}
	ok := func(args ...string) map[string]any {
		t.Helper()
		code, out := run(args...)
		if code != 0 {
			t.Fatalf("tiffin %s: exit %d\n%s", strings.Join(args, " "), code, out)
		}
		var m map[string]any
		_ = json.Unmarshal([]byte(out), &m)
		return m
	}
	sql := func(body map[string]any) map[string]any {
		t.Helper()
		args := []string{"sql", "data"}
		if body["write"] == true {
			delete(body, "write")
			args = append(args, "--write")
		}
		raw, _ := json.Marshal(body)
		return ok(append(args, "--body", string(raw))...)
	}
	rows := func(res map[string]any) string {
		r, _ := res["results"].([]any)
		if len(r) == 0 {
			return ""
		}
		b, _ := json.Marshal(r[len(r)-1].(map[string]any)["rows"])
		return string(b)
	}
	inBox := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("limactl", append([]string{"shell", "--workdir", "/", instance, "--"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("in box %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	writeConfig := func(name, services string) string {
		// A folder of its own: the CLI refuses a config beside its settings (dir/config).
		proj := filepath.Join(dir, "project")
		_ = os.MkdirAll(proj, 0o755)
		path := filepath.Join(proj, name)
		cfg := `import { defineConfig } from "@shiptiffin/sdk";
export default defineConfig({ project: "data", services: {` + services + `} });
`
		if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	apply := func(path string) map[string]any {
		t.Helper()
		plan := ok("plan", path)
		hash, _ := plan["hash"].(string)
		if len(hash) < 12 {
			t.Fatalf("plan: %v", plan)
		}
		res := ok("apply", path, "--confirm", hash[:12], "-m", "e2e data")
		if res["applied"] != true {
			t.Fatalf("apply: %v", res)
		}
		return plan
	}
	waitStatus := func(want map[string]string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Minute)
		for {
			_, out := run("projects", "get", "data")
			var st struct {
				Status map[string]struct {
					State   string `json:"state"`
					Message string `json:"message"`
				} `json:"status"`
			}
			_ = json.Unmarshal([]byte(out), &st)
			done := true
			for addr, state := range want {
				got := st.Status[addr].State
				if got == "failed" {
					t.Fatalf("%s failed: %s", addr, st.Status[addr].Message)
				}
				if got != state {
					done = false
				}
			}
			if done {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %v; last:\n%s", want, out)
			}
			time.Sleep(time.Second)
		}
	}

	// ---- up: provisions Postgres 18, Valkey and pgBackRest ----
	p := time.Now()
	if code, out := run("up", "--provider", "local", "--binary", bin); code != 0 {
		t.Fatalf("up: exit %d\n%s", code, out)
	}
	st := ok("status")
	for _, want := range []string{"postgres", "valkey", "backups"} {
		found := false
		for _, c := range st["checks"].([]any) {
			c := c.(map[string]any)
			if c["name"] == want {
				found = c["ok"] == true
			}
		}
		if !found {
			t.Fatalf("status check %s not ok: %v", want, st)
		}
	}
	phase("up", p)

	// ---- apply postgres(vector) + valkey ----
	p = time.Now()
	full := writeConfig("tiffin.config.ts", `postgres: { extensions: ["vector"] }, valkey: { maxMemoryMB: 32 }`)
	apply(full)
	waitStatus(map[string]string{"service/postgres": "ready", "service/valkey": "ready"})
	info := ok("db", "info", "data")
	if !strings.Contains(fmt.Sprint(info["extensions"]), "vector@") {
		t.Fatalf("vector not installed: %v", info)
	}
	phase("apply", p)

	// ---- SQL: create, insert, read; read-only is enforced ----
	p = time.Now()
	w := sql(map[string]any{"write": true, "sql": "create table notes(id serial primary key, body text, embedding vector(3)); insert into notes(body, embedding) values ('keep me', '[1,2,3]')"})
	if w["snapshot"] == nil {
		t.Fatalf("a write must snapshot first: %v", w)
	}
	if got := rows(sql(map[string]any{"sql": "select body, embedding from notes"})); got != `[["keep me","[1,2,3]"]]` {
		t.Fatalf("select: %s", got)
	}
	if code, out := run("sql", "data", "--sql", "delete from notes"); code == 0 || !strings.Contains(out, "25006") {
		t.Fatalf("read-only mode must refuse writes: %d %s", code, out)
	}
	if _, tables := run("db", "tables", "data"); !strings.Contains(tables, `"vector(3)"`) {
		t.Fatalf("tables: %s", tables)
	}
	phase("sql", p)

	// ---- branch a ~1 GB database by reflink clone ----
	p = time.Now()
	sql(map[string]any{"write": true, "timeoutSeconds": 600,
		"sql": "create table big as select g as id, md5(g::text) || repeat('x', 900) as payload from generate_series(1, 1100000) g"})
	size := ok("db", "info", "data")["sizeBytes"].(float64)
	if size < 1e9 {
		t.Fatalf("expected a ~1 GB database, got %.0f bytes", size)
	}
	br := ok("branches", "create", "data", "--name", "pr-1")
	t.Logf("BRANCH clone of %.2f GB: clone %vms, source blocked %vms, total %vms", size/1e9, br["cloneMs"], br["blockedMs"], br["totalMs"])
	if br["totalMs"].(float64) > 2000 {
		t.Errorf("branch clone took %vms, budget 2000ms", br["totalMs"])
	}
	if got := rows(sql(map[string]any{"sql": "select count(*) from big", "branch": "pr-1"})); got != "[[1100000]]" {
		t.Fatalf("branch contents: %s", got)
	}
	sql(map[string]any{"write": true, "branch": "pr-1", "sql": "delete from notes"})
	if got := rows(sql(map[string]any{"sql": "select count(*) from notes"})); got != "[[1]]" {
		t.Fatalf("writes to a branch must not touch main: %s", got)
	}
	ok("branches", "delete", "data", "pr-1")
	sql(map[string]any{"write": true, "sql": "drop table big"}) // keep the backup and restore small
	phase("branch", p)

	// ---- Valkey through REDIS_URL inside the box ----
	p = time.Now()
	url := ok("kv", "connection", "data", "--reveal")["redisUrl"].(string)
	if got := inBox("valkey-cli", "-u", url, "--no-auth-warning", "set", "p_data:greeting", "hello"); got != "OK" {
		t.Fatalf("set: %s", got)
	}
	if got := inBox("valkey-cli", "-u", url, "--no-auth-warning", "get", "p_data:greeting"); got != "hello" {
		t.Fatalf("get: %s", got)
	}
	if got := inBox("valkey-cli", "-u", url, "--no-auth-warning", "set", "other:key", "x"); !strings.Contains(got, "NOPERM") {
		t.Fatalf("keys outside the project's prefix must be refused: %s", got)
	}
	if k := ok("kv", "stats", "data"); k["keys"].(float64) != 1 {
		t.Fatalf("kv stats: %v", k)
	}
	phase("valkey", p)

	// ---- backup → destroy → restore → data back ----
	p = time.Now()
	// The box may be taking its own first backup: wait for it, as a person would.
	var bk map[string]any
	for i := 0; ; i++ {
		code, out := run("backup")
		if code == 0 {
			_ = json.Unmarshal([]byte(out), &bk)
			break
		}
		if i == 60 || !strings.Contains(out, "another backup") {
			t.Fatalf("tiffin backup: exit %d\n%s", code, out)
		}
		time.Sleep(5 * time.Second)
	}
	if bk["status"] != "ok" {
		t.Fatalf("backup: %v", bk)
	}
	id := bk["id"].(string)
	t.Logf("BACKUP %s %s in %vms", id, bk["kind"], bk["durationMs"])
	sql(map[string]any{"write": true, "sql": "drop table notes"})
	inBox("valkey-cli", "-u", url, "--no-auth-warning", "del", "p_data:greeting")
	code, out := run("restore", id)
	if code != 4 || !strings.Contains(out, "confirm_required") {
		t.Fatalf("restore without confirm must exit 4 with a preview: %d %s", code, out)
	}
	var preview struct {
		Confirm string `json:"confirm"`
	}
	_ = json.Unmarshal([]byte(out), &preview)
	rs := ok("restore", id, "--confirm", preview.Confirm)
	t.Logf("RESTORE in %vms (safety backup %v)", rs["durationMs"], rs["safetyBackup"])
	if got := rows(sql(map[string]any{"sql": "select body from notes"})); got != `[["keep me"]]` {
		t.Fatalf("row not back after restore: %s", got)
	}
	if got := inBox("valkey-cli", "-u", url, "--no-auth-warning", "get", "p_data:greeting"); got != "hello" {
		t.Fatalf("key not back after restore: %q", got)
	}
	phase("restore", p)

	// ---- point-in-time restore: Postgres to the second, Valkey to the set before ----
	p = time.Now()
	boxNow := func() string { return inBox("date", "-u", "+%Y-%m-%dT%H:%M:%S.%NZ") }
	// Just after a restore, until the next backup, moments can't be reached (the timeline changed).
	if code, out := run("restore", "latest", "--time", boxNow()); code == 0 || !strings.Contains(out, "restored just after") {
		t.Fatalf("a moment right after a restore must be refused: %d %s", code, out)
	}
	if bk := ok("backup"); bk["status"] != "ok" {
		t.Fatalf("backup after the restore: %v", bk)
	}
	inBox("valkey-cli", "-u", url, "--no-auth-warning", "set", "p_data:after-set", "x")
	sql(map[string]any{"write": true, "sql": "insert into notes (body) values ('before')"})
	time.Sleep(1500 * time.Millisecond)
	moment := boxNow()
	time.Sleep(1500 * time.Millisecond)
	sql(map[string]any{"write": true, "sql": "insert into notes (body) values ('after')"})
	sql(map[string]any{"write": true, "sql": "delete from notes where body = 'keep me'"})
	var over struct {
		Restorable struct{ Earliest, Latest time.Time } `json:"restorable"`
	}
	_, out = run("backups", "list")
	if err := json.Unmarshal([]byte(out), &over); err != nil || over.Restorable.Earliest.IsZero() || !over.Restorable.Earliest.Before(over.Restorable.Latest) {
		t.Fatalf("restorable range: %v %+v", err, over.Restorable)
	}
	code, out = run("restore", "latest", "--time", moment)
	if code != 4 || !strings.Contains(out, "replayed up to that moment") {
		t.Fatalf("time restore without confirm must exit 4 with a preview: %d %s", code, out)
	}
	_ = json.Unmarshal([]byte(out), &preview)
	rs = ok("restore", "latest", "--time", moment, "--confirm", preview.Confirm)
	t.Logf("TIME RESTORE to %s in %vms (from %v, safety backup %v)", moment, rs["durationMs"], rs["backup"], rs["safetyBackup"])
	if got := rows(sql(map[string]any{"sql": "select body from notes order by body"})); got != `[["before"],["keep me"]]` {
		t.Fatalf("rows after the time restore: %s", got)
	}
	if got := inBox("valkey-cli", "-u", url, "--no-auth-warning", "exists", "p_data:after-set"); got != "0" {
		t.Fatalf("Valkey must go back to the set before the moment: %q", got)
	}
	phase("time restore", p)

	// ---- Delete all data in the database: irreversible, kept 7 days, restored ----
	p = time.Now()
	confirmed := func(args ...string) {
		t.Helper()
		_, out := run(args...)
		var pr struct {
			Plan struct {
				Hash string `json:"hash"`
				Risk string `json:"risk"`
			} `json:"plan"`
		}
		if _ = json.Unmarshal([]byte(out), &pr); len(pr.Plan.Hash) < 12 || pr.Plan.Risk != "irreversible" {
			t.Fatalf("tiffin %s must ask to confirm an irreversible plan: %s", strings.Join(args, " "), out)
		}
		ok(append(args, "--confirm", pr.Plan.Hash[:12])...)
	}
	// A config without postgres keeps it: Database, KV and Files are always there.
	plan := ok("plan", writeConfig("nopg.config.ts", `valkey: { maxMemoryMB: 32 }`))
	for _, o := range plan["ops"].([]any) {
		if o := o.(map[string]any); o["address"] == "service/postgres" && o["action"] == "delete" {
			t.Fatalf("leaving postgres out of the config must not delete it: %v", plan)
		}
	}
	notes := func() (string, bool) {
		code, out := run("sql", "data", "--body", `{"sql":"select body from notes"}`)
		if code != 0 {
			return "", false
		}
		var m map[string]any
		_ = json.Unmarshal([]byte(out), &m)
		return rows(m), true
	}
	waitFor := func(what string, done func() bool) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Minute)
		for !done() {
			if time.Now().After(deadline) {
				t.Fatalf("still waiting: %s", what)
			}
			time.Sleep(time.Second)
		}
	}
	confirmed("data", "empty", "data", "postgres")
	waitFor("the notes table to go", func() bool { _, there := notes(); return !there })
	if st := ok("projects", "get", "data"); st["restorable"] == nil {
		t.Fatalf("nothing restorable after Delete all data: %v", st)
	}
	_, snaps := run("snapshots", "list", "data")
	if !strings.Contains(snaps, `"Delete all data"`) {
		t.Fatalf("no snapshot of the deleted data: %s", snaps)
	}
	confirmed("data", "restore", "data", "postgres")
	waitFor("the notes to come back", func() bool { got, _ := notes(); return got == `[["keep me"]]` })
	phase("empty+restore", p)

	if st := ok("status"); st["ok"] != true {
		t.Fatalf("status after the drill: %v", st)
	}
	if code, out := run("down", "--confirm", "local"); code != 0 {
		t.Fatalf("down: %d %s", code, out)
	}
	t.Logf("TOTAL %s", time.Since(start).Round(time.Second))
}
