//go:build e2e

package e2e

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
		args := []string{"sql", "shop"}
		if strings.Contains(body, `"write":true,`) {
			body = strings.Replace(body, `"write":true,`, "", 1)
			args = append(args, "--write")
		}
		res := b.ok(append(args, "--body", body)...)
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
	agent := a.ok("tokens", "create", "--name", "e2e-agent", "--projects", "shop", "--access", "full")
	agentSecret, _ := agent["secret"].(string)
	if agentSecret == "" || person["person"] == nil {
		t.Fatalf("person %v / token created: %v", person, agentSecret != "")
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

	// ---- one project: export → import beside it, duplicate, stop/start ----
	p = time.Now()
	projectCopies(t, b, dir)
	phase("project copies", p)

	// ---- prebuilt images: tarballs with index.json and manifest.json ----
	p = time.Now()
	prebuiltImages(t, b, dir)
	phase("prebuilt images", p)
	t.Logf("PORTABLE total %s: export %.1fs, archive %d bytes, import %.1fs", time.Since(start).Round(time.Second), exportSecs, size, importSecs)
}

// projectCopies works on box b's project shop (rows in notes, notes/a.txt
// in bucket media, p_shop:greeting, the static app web at shop, secret
// GREETING): its export is imported on the same box as shop-2, it is
// duplicated as shop-copy, and every copy has the data, its own addresses,
// and stays independent of the original.
func projectCopies(t *testing.T, b *cliBox, dir string) {
	t.Helper()
	sqlIn := func(project, body string) string {
		t.Helper()
		args := []string{"sql", project}
		if strings.Contains(body, `"write":true,`) {
			body = strings.Replace(body, `"write":true,`, "", 1)
			args = append(args, "--write")
		}
		res := b.ok(append(args, "--body", body)...)
		r, _ := res["results"].([]any)
		if len(r) == 0 {
			return ""
		}
		raw, _ := json.Marshal(r[len(r)-1].(map[string]any)["rows"])
		return string(raw)
	}
	serves := func(project, want string) {
		t.Helper()
		var code int
		var body string
		for i := 0; i < 90; i++ {
			code, _, body = b.get(b.https(), "GET", b.url(project)+"/", nil)
			if code == 200 && strings.Contains(body, want) {
				return
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("%s does not serve %q: %d %s", project, want, code, body)
	}
	check := func(project string) {
		t.Helper()
		if got := sqlIn(project, `{"sql":"select id, body from notes order by id"}`); got != `[[1,"moved with the box"],[2,"still here"]]` {
			t.Fatalf("%s rows: %s", project, got)
		}
		if g := b.ok("storage", "objects", "get", project, "media", "--key", "notes/a.txt"); g["text"] != "written on box A" {
			t.Fatalf("%s object: %v", project, g)
		}
		kv := b.ok("kv", "connection", project)["redisUrl"].(string)
		prefix := "p_" + strings.ReplaceAll(project, "-", "_") + ":"
		if got := b.inBox(`valkey-cli -u '` + kv + `' --no-auth-warning get ` + prefix + `greeting`); got != "hello" {
			t.Fatalf("%s cache key: %q", project, got)
		}
		if l := b.list("secrets", "list", project); !strings.Contains(fmt.Sprint(l), "GREETING") {
			t.Fatalf("%s secrets: %v", project, l)
		}
		serves(project, "Shop from box A")
	}

	// Export, then import beside the original under another name.
	archive := filepath.Join(dir, "shop-project.tiffin")
	ex := b.ok("projects", "export", "shop", "-o", archive)
	if ex["sha256"] == "" || ex["sizeBytes"].(float64) == 0 {
		t.Fatalf("project export: %v", ex)
	}
	if code, out := b.run("projects", "import", archive); code != 3 || !strings.Contains(out, "--name") {
		t.Fatalf("import over the original: exit %d %s", code, out)
	}
	im := b.ok("projects", "import", archive, "--name", "shop-2")
	if im["status"] != "done" || im["healthy"] != true || im["project"] != "shop-2" {
		t.Fatalf("project import: %v", im)
	}
	check("shop-2")

	// Duplicate, then change the original: the copy keeps its own data.
	dup := b.ok("projects", "duplicate", "shop", "shop-copy")
	if dup["status"] != "done" || dup["healthy"] != true {
		t.Fatalf("duplicate: %v", dup)
	}
	check("shop-copy")
	hist := b.list("changes", "list", "--project", "shop-copy")
	if len(hist) != 1 || hist[0]["intent"] != "Duplicated from shop" {
		t.Fatalf("the copy's History: %v", hist)
	}
	sqlIn("shop", `{"write":true,"sql":"update notes set body = 'changed' where id = 1"}`)
	if got := sqlIn("shop-copy", `{"sql":"select body from notes where id = 1"}`); got != `[["moved with the box"]]` {
		t.Fatalf("the copy follows the original: %s", got)
	}

	// Stop keeps the copy's app down until it starts again.
	b.ok("projects", "stop", "shop-copy")
	for i := 0; ; i++ {
		code, _, _ := b.get(b.https(), "GET", b.url("shop-copy")+"/", nil)
		if code != 200 {
			break
		}
		if i > 30 {
			t.Fatal("a stopped project still serves")
		}
		time.Sleep(time.Second)
	}
	b.ok("projects", "start", "shop-copy")
	serves("shop-copy", "Shop from box A")

	// A name destroyed less than 7 days ago is refused before the archive is sent.
	_, out := b.run("projects", "destroy", "shop-copy")
	var pr struct {
		Plan struct{ Hash string } `json:"plan"`
	}
	if _ = json.Unmarshal([]byte(out), &pr); len(pr.Plan.Hash) < 12 {
		t.Fatalf("destroy plan: %s", out)
	}
	b.ok("projects", "destroy", "shop-copy", "--confirm", pr.Plan.Hash[:12])
	if code, out := b.run("projects", "import", archive, "--name", "shop-copy"); code != 3 || !strings.Contains(out, "destroyed less than 7 days ago") {
		t.Fatalf("import under a name destroyed just now: exit %d %s", code, out)
	}
}

// prebuiltImages loads images saved by nerdctl (an OCI index.json and a
// docker manifest.json in one tarball, each naming the image) on box b:
// tiffin deploy --prebuilt puts one live under the deploy's own name; a
// tarball naming that deploy's image is loaded beside it without replacing
// it; and the project's export (whose image.tar nerdctl saves the same way)
// imports as a new project whose app goes live.
func prebuiltImages(t *testing.T, b *cliBox, dir string) {
	t.Helper()
	srv := filepath.Join(dir, "hellosrv")
	build := exec.Command("go", "build", "-tags", "e2e", "-o", srv, "./e2e/hellosrv")
	build.Dir = RepoRoot()
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+HostArch())
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build hellosrv: %v\n%s", err, out)
	}
	bin, err := os.ReadFile(srv)
	if err != nil {
		t.Fatal(err)
	}
	const nerdctl = "sudo /usr/local/bin/nerdctl"
	// saved builds an image of the server named name, loads it into a
	// namespace of its own and saves it again with nerdctl: what it writes
	// has both index.json and manifest.json.
	saved := func(label, name string, extra map[string]string) string {
		t.Helper()
		local := filepath.Join(dir, label+".docker.tar")
		if err := os.WriteFile(local, dockerArchiveOf(t, bin, extra, name), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("limactl", "copy", local, b.instance+":/tmp/"+label+".docker.tar").CombinedOutput(); err != nil {
			t.Fatalf("copy %s: %v\n%s", label, err, out)
		}
		files := b.inBox(nerdctl + " -n e2e load -i /tmp/" + label + ".docker.tar >/dev/null && " + nerdctl + " -n e2e save -o /tmp/" + label + ".tar " + name +
			" && sudo chmod 644 /tmp/" + label + ".tar && tar tf /tmp/" + label + ".tar")
		if !strings.Contains(files, "index.json") || !strings.Contains(files, "manifest.json") {
			t.Fatalf("nerdctl save wrote no index.json and manifest.json: %s", files)
		}
		out := filepath.Join(dir, label+".tar")
		if o, err := exec.Command("limactl", "copy", b.instance+":/tmp/"+label+".tar", out).CombinedOutput(); err != nil {
			t.Fatalf("copy back %s: %v\n%s", label, err, o)
		}
		return out
	}
	images := func() string {
		return b.inBox("sudo ctr -n tiffin images ls -q 2>/dev/null || " + nerdctl + " -n tiffin images --format '{{.Repository}}:{{.Tag}}'")
	}
	imageID := func(ref string) string {
		return b.inBox(nerdctl + " -n tiffin image inspect --format '{{.ID}}' " + ref)
	}
	buildLog := func(project, app, id string) string {
		text, _ := b.ok("deploys", "build-log", project, app, id)["text"].(string)
		for _, line := range strings.Split(text, "\n") {
			if strings.Contains(line, "Loaded image") || strings.Contains(line, "loaded the image") || strings.Contains(line, "==> live") {
				t.Logf("%s/%s %s build log: %s", project, app, id, line)
			}
		}
		return text
	}
	prebuilt := func(file string) map[string]any {
		t.Helper()
		code, out := b.run("deploy", "--project", "pre", "--app", "api", "--prebuilt", file)
		var res struct {
			Deploys []map[string]any `json:"deploys"`
		}
		if err := json.Unmarshal([]byte(out), &res); err != nil || code != 0 || len(res.Deploys) != 1 || res.Deploys[0]["status"] != "live" {
			t.Fatalf("deploy --prebuilt %s: exit %d\n%s", filepath.Base(file), code, out)
		}
		return res.Deploys[0]
	}
	serves := func(project string) {
		t.Helper()
		var code int
		var body string
		for i := 0; i < 60; i++ {
			if code, _, body = b.get(b.https(), "GET", b.url(project)+"/", nil); code == 200 && strings.Contains(body, "hello from a prebuilt image") {
				return
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("%s does not serve the prebuilt app: %d %s", project, code, body)
	}

	b.apply("pre", `{"project":"pre","apps":{"api":{"framework":"bun","routes":["pre"]}}}`)
	b.project = "pre" // waitReady reads b.project
	b.waitReady("app/api")
	b.project = "shop"

	// The tarball names its image e2e/hello:v1; it goes live as the deploy's.
	d1 := prebuilt(saved("hello", "docker.io/e2e/hello:v1", nil))
	id1, _ := d1["id"].(string)
	ref1 := "docker.io/tiffin/pre-api:" + strings.ToLower(id1)
	if log := buildLog("pre", "api", id1); !strings.Contains(log, "loaded the image as "+ref1) {
		t.Fatalf("the image was not loaded as %s:\n%s", ref1, log)
	}
	serves("pre")
	if imgs := images(); strings.Contains(imgs, "e2e/hello") || strings.Contains(imgs, "import") || !strings.Contains(imgs, ref1) {
		t.Fatalf("the box's images after the load:\n%s", imgs)
	}

	// A tarball naming that live image (other content) is loaded beside it.
	victimID := imageID(ref1)
	d2 := prebuilt(saved("victim", ref1, map[string]string{"victim.txt": "not the live image"}))
	id2, _ := d2["id"].(string)
	ref2 := "docker.io/tiffin/pre-api:" + strings.ToLower(id2)
	if log := buildLog("pre", "api", id2); !strings.Contains(log, "loaded the image as "+ref2) {
		t.Fatalf("the image was not loaded as %s:\n%s", ref2, log)
	}
	if got := imageID(ref1); got != victimID || imageID(ref2) == victimID {
		t.Fatalf("a tarball named %s replaced it: %s, was %s", ref1, got, victimID)
	}
	if imgs := images(); strings.Contains(imgs, "import") {
		t.Fatalf("a load left a digest name behind:\n%s", imgs)
	}
	serves("pre")

	// Export → import: the archive's image.tar is nerdctl's save of the live image.
	archive := filepath.Join(dir, "pre.tiffin")
	b.ok("projects", "export", "pre", "-o", archive)
	im := b.ok("projects", "import", archive, "--name", "pre-2")
	apps, _ := im["apps"].([]any)
	if im["status"] != "done" || im["healthy"] != true || len(apps) != 1 {
		t.Fatalf("import of pre: %v", im)
	}
	a, _ := apps[0].(map[string]any)
	id3, _ := a["deploy"].(string)
	ref3 := "docker.io/tiffin/pre-2-api:" + strings.ToLower(id3)
	if log := buildLog("pre-2", "api", id3); a["status"] != "live" || !strings.Contains(log, "loaded the image as "+ref3) {
		t.Fatalf("pre-2's app: %v\n%s", a, log)
	}
	serves("pre-2")
	t.Logf("prebuilt images live as %s, %s and %s", ref1, ref2, ref3)
}

// dockerArchiveOf is a docker archive (docker save's format) of an image
// whose one layer holds bin as /server and the extra files, named name.
func dockerArchiveOf(t *testing.T, bin []byte, extra map[string]string, name string) []byte {
	t.Helper()
	tarOf := func(files map[string][]byte, mode map[string]int64) []byte {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		for _, n := range slices.Sorted(maps.Keys(files)) {
			if err := tw.WriteHeader(&tar.Header{Name: n, Mode: mode[n], Size: int64(len(files[n])), Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			_, _ = tw.Write(files[n])
		}
		_ = tw.Close()
		return buf.Bytes()
	}
	hexsum := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	files, mode := map[string][]byte{"server": bin}, map[string]int64{"server": 0o755}
	for n, body := range extra {
		files[n], mode[n] = []byte(body), 0o644
	}
	layer := tarOf(files, mode)
	cfg, _ := json.Marshal(map[string]any{"architecture": HostArch(), "os": "linux",
		"config": map[string]any{"Entrypoint": []string{"/server"}},
		"rootfs": map[string]any{"type": "layers", "diff_ids": []string{"sha256:" + hexsum(layer)}}})
	cfgName, layerName := hexsum(cfg)+".json", hexsum(layer)+"/layer.tar"
	man, _ := json.Marshal([]map[string]any{{"Config": cfgName, "RepoTags": []string{name}, "Layers": []string{layerName}}})
	return tarOf(map[string][]byte{cfgName: cfg, layerName: layer, "manifest.json": man},
		map[string]int64{cfgName: 0o644, layerName: 0o644, "manifest.json": 0o644})
}
