//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPooler covers the connection pooler and Postgres minor updates on a
// fresh box:
//
//	every common client (Bun.SQL, postgres.js, node-postgres, Drizzle on
//	both drivers, Prisma 7 and Prisma 6) works through the pooler with
//	prepared statements, and the release command migrates straight to
//	Postgres → Postgres is put back one minor version → with every client
//	querying non-stop, `tiffin maintenance postgres-update --now` updates it:
//	no query fails, the stall is measured → LISTEN/NOTIFY (direct) and the
//	queue engine still work → back again, and `tiffin up` raises it to the
//	minimum the same way → a library replaced under the box's services:
//	needrestart leaves them alone and a maintenance run restarts Postgres
//	with the pause, again with no failed query.
func TestPooler(t *testing.T) {
	start := time.Now()
	phase := phaseLogger(t)
	b := newCLIBox(t, "pooler", "poolx")
	phase("up", start)

	root := filepath.Join(b.dir, "poolx")
	src := filepath.Join(RepoRoot(), "e2e", "poolerapp")
	if out, err := exec.Command("cp", "-R", src, root).CombinedOutput(); err != nil {
		t.Fatalf("copy app: %v %s", err, out)
	}
	if out, err := exec.Command("cp", filepath.Join(root, "api", "lib.ts"), filepath.Join(root, "p6", "lib.ts")).CombinedOutput(); err != nil {
		t.Fatalf("copy lib: %v %s", err, out)
	}
	p := time.Now()
	plan := b.ok("plan", root)
	hash, _ := plan["hash"].(string)
	b.ok("apply", root, "--confirm", hash, "-m", "e2e: pooler")
	b.waitReady("service/postgres", "app/api", "app/p6")
	api := deployArgs(t, b, root, "--app", "api")
	deployArgs(t, b, root, "--app", "p6")
	phase("deploy", p)

	c := b.https()
	call := func(host, path string, into any) {
		t.Helper()
		code, _, body := b.get(c, "GET", b.url(host)+path, nil)
		if code != 200 {
			t.Fatalf("%s%s: %d %s", host, path, code, body)
		}
		if err := json.Unmarshal([]byte(body), into); err != nil {
			t.Fatalf("%s%s: %v %s", host, path, err, body)
		}
	}

	// ---- every client through the pooler; the release straight to Postgres ----
	log := fmt.Sprint(b.ok("deploys", "build-log", "poolx", "api", api.ID)["text"])
	if !strings.Contains(log, "migrated through port 5432") {
		t.Fatalf("the release command must reach Postgres directly:\n%s", grepLines(log, "release", "migrated"))
	}
	var info struct {
		PooledPort       string `json:"pooledPort"`
		DirectServerPort int    `json:"directServerPort"`
		PoolMax          int    `json:"poolMax"`
	}
	call("poolx", "/info", &info)
	if info.PooledPort != "6432" || info.DirectServerPort != 5432 || info.PoolMax != 20 {
		t.Fatalf("env: %+v", info)
	}
	type result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	var clients []string
	for _, host := range []string{"poolx", "poolx-p6"} {
		var res map[string]result
		call(host, "/clients", &res)
		for name, r := range res {
			if !r.OK {
				t.Errorf("%s through the pooler: %s", name, r.Error)
			}
			clients = append(clients, name)
		}
	}
	if len(clients) != 8 {
		t.Fatalf("clients: %v", clients)
	}
	t.Logf("through the pooler, prepared statements included: %s", strings.Join(clients, ", "))
	if pools := b.inBox(`sudo cat /etc/tiffin/pgbouncer/pools.ini`); !strings.Contains(pools, "p_poolx = host=/var/run/postgresql port=5432 pool_size=60") ||
		!strings.Contains(pools, "p_poolx = max_user_connections=60") {
		t.Fatalf("pools.ini:\n%s", pools)
	}
	// PgBouncer itself runs with them (the box reloads it).
	if live := b.inBox(`sudo psql -h /var/run/postgresql -p 6432 -U postgres -XA pgbouncer -c 'SHOW DATABASES' | awk -F'|' 'NR == 1 {for (i = 1; i <= NF; i++) if ($i == "pool_size") c = i} $1 == "p_poolx" {print $1, $c}'`); live != "p_poolx 60" {
		t.Fatalf("SHOW DATABASES: %q", live)
	}
	if st := b.ok("status"); !strings.Contains(fmt.Sprint(st["checks"]), "PgBouncer 1.") {
		t.Fatalf("status: %v", st["checks"])
	}

	// ---- a minor update under load ----
	p = time.Now()
	older := b.inBox(`apt-cache madison postgresql-18 | awk '$3 ~ /pgdg/ {print $3}' | sed -n 2p`)
	server := func() string {
		return b.inBox(`sudo -u postgres psql -h /var/run/postgresql -XAt -c 'show server_version' | cut -d' ' -f1`)
	}
	downgrade := func() {
		t.Helper()
		b.inBox(fmt.Sprintf(`export DEBIAN_FRONTEND=noninteractive NEEDRESTART_SUSPEND=1
sudo -E apt-get install -y -q --allow-downgrades postgresql-18=%[1]s postgresql-client-18=%[1]s libpq5=%[1]s >/tmp/downgrade.log 2>&1 || { tail -20 /tmp/downgrade.log; exit 1; }
sudo systemctl restart tiffin-postgres`, older))
	}
	type stats struct {
		Queries    int    `json:"queries"`
		Errors     int    `json:"errors"`
		MaxMs      int    `json:"maxMs"`
		FirstError string `json:"firstError"`
	}
	load := func(what string, during func()) (stall int) {
		t.Helper()
		var started map[string]any
		call("poolx", "/load/start", &started)
		call("poolx-p6", "/load/start", &started)
		time.Sleep(3 * time.Second)
		during()
		time.Sleep(3 * time.Second)
		var parts []string
		for _, host := range []string{"poolx", "poolx-p6"} {
			var res map[string]stats
			call(host, "/load/stop", &res)
			if len(res) == 0 {
				t.Fatalf("%s: the load stopped (did the app restart?)", host)
			}
			for name, s := range res {
				if s.Errors > 0 || s.Queries < 50 {
					t.Errorf("%s: %s ran %d queries, %d failed: %s", what, name, s.Queries, s.Errors, s.FirstError)
				}
				stall = max(stall, s.MaxMs)
				parts = append(parts, fmt.Sprintf("%s %d queries, slowest %d ms", name, s.Queries, s.MaxMs))
			}
		}
		t.Logf("%s: %s", what, strings.Join(parts, "; "))
		return stall
	}
	var upd map[string]any
	if older == "" || !strings.HasPrefix(older, "18.") {
		t.Logf("PGDG has no older 18.x for this release: the update below is a restart for replaced libraries only")
	} else {
		downgrade()
		if v := server(); !strings.HasPrefix(older, v+"-") {
			t.Fatalf("downgrade: server runs %s, want %s", v, older)
		}
		checked := b.ok("maintenance", "postgres-update")
		if s := fmt.Sprint(checked["summary"]); !strings.Contains(s, "postgresql-18 18.") || !strings.Contains(s, "--now") {
			t.Fatalf("check: %v", checked)
		}
		before := server()
		stall := load("minor update", func() { upd = b.ok("maintenance", "postgres-update", "--now") })
		u, _ := upd["update"].(map[string]any)
		pause, _ := u["pauseSeconds"].(float64)
		if u["status"] != "ok" || u["from"] != before || u["to"] == before || pause <= 0 || pause >= 10 {
			t.Fatalf("update: %v", upd)
		}
		t.Logf("MINOR UPDATE %s; queries stalled at most %d ms", upd["summary"], stall)
	}
	phase("minor update", p)

	// ---- LISTEN/NOTIFY over the direct URL, and the queue engine ----
	var heard struct {
		Received bool `json:"received"`
		Ms       int  `json:"ms"`
	}
	call("poolx", "/listen", &heard)
	if !heard.Received {
		t.Fatal("LISTEN over DIRECT_DATABASE_URL did not hear the NOTIFY after the restart")
	}
	res := b.ok("queue", "send", "poolx", "--body", `{"name":"ping","payload":{"n":1}}`)
	jobs, _ := res["jobs"].([]any)
	if len(jobs) != 1 {
		t.Fatalf("send: %v", res)
	}
	for i := 0; ; i++ {
		j := b.ok("queue", "jobs", "get", "poolx", jobs[0].(string))
		if j["state"] == "completed" {
			break
		}
		if i == 60 {
			t.Fatalf("job after the update: %v", j)
		}
		time.Sleep(time.Second)
	}
	// A preview branch clone pauses the source database at the pooler: its
	// queries wait instead of failing.
	stall := load("branch clone", func() { b.ok("branches", "create", "poolx", "--name", "pr-9") })
	b.ok("branches", "delete", "poolx", "pr-9")
	t.Logf("BRANCH CLONE queries stalled at most %d ms", stall)
	audit := b.list("audit", "list")
	found := false
	for _, e := range audit {
		found = found || e["action"] == "postgres.update"
	}
	if !found && upd != nil {
		t.Fatalf("the update is not in the audit log: %v", audit)
	}

	// ---- tiffin up raises a box below the minimum, the same way ----
	if older != "" && strings.HasPrefix(older, "18.") {
		p = time.Now()
		downgrade()
		bin := filepath.Join(b.dir, "tiffin-linux-0.0.1-pooler")
		stall := load("tiffin up", func() { b.ok("up", "--provider", "local", "--binary", bin) })
		show := b.ok("maintenance", "show")
		ups, _ := show["updates"].([]any)
		last, _ := ups[0].(map[string]any)
		if v := server(); strings.HasPrefix(older, v+"-") || last["trigger"] != "tiffin up" || last["status"] != "ok" {
			t.Fatalf("tiffin up left Postgres %s: %v", v, last)
		}
		t.Logf("TIFFIN UP %s; queries stalled at most %d ms", last["summary"], stall)
		phase("tiffin up", p)
	}

	// ---- replaced libraries: needrestart leaves the box's services alone ----
	p = time.Now()
	pid := func(unit string) string { return b.inBox(`systemctl show -p MainPID --value ` + unit) }
	pg, pool := pid("tiffin-postgres"), pid("tiffin-pgbouncer")
	b.inBox(`sudo DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a apt-get install -y -q --reinstall libzstd1 libssl3t64 >/tmp/reinstall.log 2>&1 || { tail -20 /tmp/reinstall.log; exit 1; }`)
	if pid("tiffin-postgres") != pg || pid("tiffin-pgbouncer") != pool {
		t.Fatalf("needrestart restarted a box service:\n%s", b.inBox(`grep -i restart /tmp/reinstall.log || true`))
	}
	checked := b.ok("maintenance", "postgres-update")
	if s := fmt.Sprint(checked["summary"]); !strings.Contains(s, "restart of tiffin-postgres") {
		t.Fatalf("check after a library update: %v", checked)
	}
	stall = load("restart for replaced libraries", func() { upd = b.ok("maintenance", "postgres-update", "--now") })
	u, _ := upd["update"].(map[string]any)
	if u["status"] != "ok" || !strings.Contains(fmt.Sprint(u["restarted"]), "tiffin-postgres.service") || pid("tiffin-pgbouncer") != pool {
		t.Fatalf("restart: %v", upd)
	}
	t.Logf("LIBRARIES %s; queries stalled at most %d ms; %v", upd["summary"], stall, upd["maintenance"].(map[string]any)["restart"])
	phase("libraries", p)
	phase("total", start)
}
