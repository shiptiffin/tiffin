//go:build e2e

package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/boxfile"
)

// portBox is a box for the portable test. spec ("instance:disk:port", from
// TIFFIN_E2E_BOX_A / _B) pins its names; otherwise they are fresh.
func portBox(t *testing.T, label, root, cli, spec string) *cliBox {
	t.Helper()
	// Each box has its own directory: cliBox helpers find its CLI config in dir/config.
	dir := filepath.Join(root, label)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	instance, disk, port := newName(), newDiskName(), 0
	if f := strings.Split(spec, ":"); len(f) == 3 {
		instance, disk = f[0], f[1]
		port, _ = strconv.Atoi(f[2])
	}
	if port == 0 {
		port = freePort(t)
	}
	b := &cliBox{t: t, dir: dir, cli: cli, instance: instance, port: port, project: "shop"}
	b.env = append(os.Environ(),
		"TIFFIN_CONFIG_DIR="+filepath.Join(dir, "config"),
		"TIFFIN_LIMA_INSTANCE="+instance, "TIFFIN_LIMA_DISK="+disk, fmt.Sprintf("TIFFIN_LIMA_PORT=%d", port),
		"TIFFIN_LIMA_MEMORY=3GiB", "TIFFIN_HOME=", "TIFFIN_URL=", "TIFFIN_TOKEN=")
	t.Cleanup(func() {
		if os.Getenv("TIFFIN_E2E_KEEP") != "1" {
			_ = exec.Command("limactl", "delete", "-f", instance).Run()
			_ = exec.Command("limactl", "disk", "delete", "-f", disk).Run()
		}
	})
	return b
}

// TestPortable moves a box: box A gets a project with Postgres rows, a
// bucket object, a Valkey key, a deployed static app, a secret, a person
// and an agent token → tiffin box export → A is destroyed → a fresh box B
// → tiffin box import → every item is back, the app serves over HTTPS and
// A's tokens work on B.
func TestPortable(t *testing.T) {
	RequireLima(t)
	start := time.Now()
	phase := phaseLogger(t)
	dir := t.TempDir()
	cli := buildTiffin(t, dir, "", "")
	bin := buildTiffin(t, dir, "linux", "0.0.1-portable")

	// ---- box A ----
	p := time.Now()
	a := portBox(t, "a", dir, cli, os.Getenv("TIFFIN_E2E_BOX_A"))
	a.ok("up", "--binary", bin)
	phase("A up", p)

	p = time.Now()
	a.apply("shop", `{"project":"shop","apps":{"web":{"framework":"static","routes":["shop"]}},
		"services":{"postgres":{},"valkey":{"maxMemoryMB":32},"storage":{"buckets":{"media":{}}}}}`)
	a.waitReady("service/postgres", "service/valkey", "service/storage", "bucket/media", "app/web")
	sql := func(b *cliBox, body string) string {
		t.Helper()
		res := b.ok("sql", "shop", "--body", body)
		r, _ := res["results"].([]any)
		if len(r) == 0 {
			return ""
		}
		raw, _ := json.Marshal(r[len(r)-1].(map[string]any)["rows"])
		return string(raw)
	}
	sql(a, `{"write":true,"sql":"create table notes(id int primary key, body text); insert into notes values (1,'moved with the box'),(2,'still here')"}`)
	a.ok("storage", "objects", "put", "shop", "media", "--key", "notes/a.txt", "--text", "written on box A")
	kvURL := a.ok("kv", "connection", "shop")["redisUrl"].(string)
	if got := a.inBox(`valkey-cli -u '` + kvURL + `' --no-auth-warning set p_shop:greeting hello`); got != "OK" {
		t.Fatalf("valkey set: %s", got)
	}
	files, _ := json.Marshal(map[string]any{"files": map[string]string{"index.html": "<!doctype html><title>Shop</title><h1>Shop from box A</h1>"}})
	dep := a.ok("deploys", "create", "shop", "web", "--body", string(files))
	depID, _ := dep["id"].(string)
	for i := 0; ; i++ {
		d := a.ok("deploys", "get", "shop", "web", depID)
		if d["status"] == "live" {
			break
		}
		if d["status"] == "failed" || i > 300 {
			t.Fatalf("static deploy: %v", d)
		}
		time.Sleep(time.Second)
	}
	if code, _, body := a.get(a.https(), "GET", a.url("shop")+"/", nil); code != 200 || !strings.Contains(body, "Shop from box A") {
		t.Fatalf("app on A: %d %s", code, body)
	}
	a.ok("secrets", "set", "shop", "GREETING", "--value", "hi from a secret")
	person := a.ok("people", "add", "--name", "Ada Lovelace", "--email", "ada@example.com", "--role", "member")
	agent := a.ok("tokens", "create", "--name", "e2e-agent")
	agentSecret, _ := agent["secret"].(string)
	if agentSecret == "" || person["id"] == nil {
		t.Fatalf("person %v / token %v", person, agent)
	}
	phase("A data", p)

	// ---- export ----
	p = time.Now()
	archive := filepath.Join(dir, "shop.tiffin")
	keyFile := filepath.Join(dir, "shop.key")
	ex := a.ok("box", "export", archive, "--key-out", keyFile)
	exportSecs := time.Since(p).Seconds()
	size := int64(ex["sizeBytes"].(float64))
	raw, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != ex["sha256"] || int64(len(raw)) != size {
		t.Fatalf("archive on disk differs from the reported size/sha256: %v", ex)
	}
	if fi, err := os.Stat(keyFile); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", fi, err)
	}
	// The archive verifies, names the project, and does not carry the key.
	f, _ := os.Open(archive)
	ar, err := boxfile.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string]bool{}
	for {
		e, err := ar.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("archive: %v", err)
		}
		entries[e.Name] = true
	}
	ar.Close()
	f.Close()
	if entries["platform/secrets.key"] || !entries["platform/state.db"] || !entries["postgres/db/p_shop.sql"] || !entries["valkey/dump.rdb"] ||
		!entries["files/storage/data/shop-media/notes/a.txt"] || strings.Join(ar.Manifest.Projects, ",") != "shop" {
		t.Fatalf("archive contents: projects %v, entries %d", ar.Manifest.Projects, len(entries))
	}
	t.Logf("EXPORT %d bytes (%.1f MB) in %.1fs, writes paused %vms, sha256 %s", size, float64(size)/(1<<20), exportSecs, ex["writesPausedMs"], ex["sha256"])
	phase("export", p)

	p = time.Now()
	a.ok("down", "--confirm", "local")
	phase("A down", p)

	// ---- box B ----
	p = time.Now()
	b := portBox(t, "b", dir, cli, os.Getenv("TIFFIN_E2E_BOX_B"))
	b.ok("up", "--binary", bin)
	phase("B up", p)

	// Without the key the import is refused before anything is uploaded.
	if code, out := b.run("box", "import", archive); code != 3 || !strings.Contains(out, "key") {
		t.Fatalf("import without the key: exit %d %s", code, out)
	}
	p = time.Now()
	im := b.ok("box", "import", archive, "--key-file", keyFile)
	importSecs := time.Since(p).Seconds()
	if im["healthy"] != true {
		t.Fatalf("import not healthy: %v\nstatus: %s\njournal: %s", im, b.inBox("curl -s -o /dev/null -w %{http_code} http://127.0.0.1:7070/v1/health"),
			b.inBox("sudo journalctl -u tiffin -n 80 --no-pager"))
	}
	t.Logf("IMPORT %.1fs (%v)", importSecs, im["seconds"])
	phase("import", p)

	// ---- everything is back ----
	p = time.Now()
	if got := sql(b, `{"sql":"select id, body from notes order by id"}`); got != `[[1,"moved with the box"],[2,"still here"]]` {
		t.Fatalf("postgres rows: %s", got)
	}
	if g := b.ok("storage", "objects", "get", "shop", "media", "--key", "notes/a.txt"); g["text"] != "written on box A" {
		t.Fatalf("object: %v", g)
	}
	kvB := b.ok("kv", "connection", "shop")["redisUrl"].(string)
	if got := b.inBox(`valkey-cli -u '` + kvB + `' --no-auth-warning get p_shop:greeting`); got != "hello" {
		t.Fatalf("valkey get: %s", got)
	}
	// The app serves over HTTPS, with the source box's CA (the CLI updated its copy).
	var code int
	var body string
	for i := 0; i < 60; i++ {
		code, _, body = b.get(b.https(), "GET", b.url("shop")+"/", nil)
		if code == 200 {
			break
		}
		time.Sleep(time.Second)
	}
	if code != 200 || !strings.Contains(body, "Shop from box A") {
		t.Fatalf("app on B: %d %s", code, body)
	}
	if l := b.list("secrets", "list", "shop"); !strings.Contains(fmt.Sprint(l), "GREETING") {
		t.Fatalf("secrets: %v", l)
	}
	if l := b.list("people", "list"); !strings.Contains(fmt.Sprint(l), "ada@example.com") {
		t.Fatalf("people: %v", l)
	}
	// A's agent token works on B; B's own owner token (the CLI's) still works.
	agentEnv := append(append([]string(nil), b.env...), "TIFFIN_TOKEN="+agentSecret)
	cmd := exec.Command(cli, "whoami")
	cmd.Env, cmd.Dir = agentEnv, dir
	out, err := cmd.Output()
	if err != nil || !strings.Contains(string(out), "e2e-agent") {
		t.Fatalf("A's agent token on B: %v %s", err, out)
	}
	if w := b.ok("whoami"); w["kind"] != "owner" {
		t.Fatalf("B's owner token: %v", w)
	}
	st := b.ok("status")
	if checks, _ := st["checks"].([]any); len(checks) > 0 {
		for _, c := range checks {
			if m, _ := c.(map[string]any); m["ok"] != true {
				t.Errorf("status check not green after import: %v", m)
			}
		}
	}
	// A box with projects refuses another import unless replaced.
	if code, out := b.run("box", "import", archive, "--key-file", keyFile); code != 4 || !strings.Contains(out, "replace") {
		t.Fatalf("import onto a box with projects: exit %d %s", code, out)
	}
	phase("verify", p)
	t.Logf("PORTABLE total %s: export %.1fs, archive %d bytes, import %.1fs", time.Since(start).Round(time.Second), exportSecs, size, importSecs)
}
