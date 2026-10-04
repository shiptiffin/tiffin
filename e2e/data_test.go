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
// (preview, confirm) → data back → delete the postgres service (irreversible,
// owner token) leaves a snapshot.
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
		path := filepath.Join(dir, name)
		cfg := `import { defineConfig } from "tiffin-sdk";
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
	if code, out := run("up", "--binary", bin); code != 0 {
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
	url := ok("kv", "connection", "data")["redisUrl"].(string)
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
	bk := ok("backup")
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

	// ---- deleting the postgres service is irreversible and leaves a snapshot ----
	p = time.Now()
	plan := apply(writeConfig("nopg.config.ts", `valkey: { maxMemoryMB: 32 }`))
	if plan["risk"] != "irreversible" {
		t.Fatalf("deleting postgres must plan as irreversible: %v", plan)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		_, out := run("projects", "get", "data")
		if !strings.Contains(out, "service/postgres") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("service/postgres still present:\n%s", out)
		}
		time.Sleep(time.Second)
	}
	_, snaps := run("snapshots", "list", "data")
	if !strings.Contains(snaps, `"service deleted"`) {
		t.Fatalf("no delete snapshot: %s", snaps)
	}
	if got := inBox("sudo", "-u", "postgres", "psql", "-h", "/var/run/postgresql", "-Atc", "select count(*) from pg_database where datname like 'p_data%'"); got != "0" {
		t.Fatalf("database still there: %s", got)
	}
	phase("delete", p)

	if st := ok("status"); st["ok"] != true {
		t.Fatalf("status after the drill: %v", st)
	}
	if code, out := run("down", "--confirm", "local"); code != 0 {
		t.Fatalf("down: %d %s", code, out)
	}
	t.Logf("TOTAL %s", time.Since(start).Round(time.Second))
}
