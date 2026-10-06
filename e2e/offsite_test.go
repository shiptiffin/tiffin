//go:build e2e

package e2e

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// localS3 is a versitygw on this machine, over TLS with a private CA, that
// the boxes reach at host.lima.internal.
type localS3 struct {
	endpoint, ca, access, secret, data string
	stop                               func()
}

// startS3 runs versitygw (TIFFIN_TEST_VERSITYGW: a build for this OS) on
// 127.0.0.1 and creates the bucket "offsite".
func startS3(t *testing.T, dir string) *localS3 {
	t.Helper()
	bin := os.Getenv("TIFFIN_TEST_VERSITYGW")
	if bin == "" {
		t.Skip("TIFFIN_TEST_VERSITYGW not set (a versitygw build for this machine)")
	}
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "tiffin e2e S3 CA"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(48 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	caCert, _ := x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	srv := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "host.lima.internal"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(48 * time.Hour), DNSNames: []string{"host.lima.internal", "localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("192.168.5.2")},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	srvDER, _ := x509.CreateCertificate(rand.Reader, srv, caCert, &key.PublicKey, caKey)
	keyDER, _ := x509.MarshalECPrivateKey(key)
	certPath, keyPath, caPath := filepath.Join(dir, "s3.crt"), filepath.Join(dir, "s3.key"), filepath.Join(dir, "s3-ca.pem")
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	_ = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srvDER}), 0o600)
	_ = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
	_ = os.WriteFile(caPath, ca, 0o600)

	s := &localS3{ca: string(ca), access: "TFNE2EROOT", data: filepath.Join(dir, "s3data")}
	sec := make([]byte, 16)
	_, _ = rand.Read(sec)
	s.secret = hex.EncodeToString(sec)
	_ = os.MkdirAll(s.data, 0o755)
	port := freePort(t)
	cmd := exec.Command(bin, "--port", fmt.Sprintf("127.0.0.1:%d", port), "--cert", certPath, "--key", keyPath, "--region", "us-east-1", "--quiet", "posix", s.data)
	cmd.Env = append(os.Environ(), "ROOT_ACCESS_KEY="+s.access, "ROOT_SECRET_KEY="+s.secret)
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	s.stop = func() {
		once.Do(func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
	}
	t.Cleanup(func() {
		s.stop()
		if t.Failed() {
			t.Logf("versitygw:\n%s", tail(logs.String(), 4000))
		}
	})
	for i := 0; ; i++ {
		out, err := exec.Command("curl", "-sS", "--fail-with-body", "--cacert", caPath, "--aws-sigv4", "aws:amz:us-east-1:s3",
			"--user", s.access+":"+s.secret, "-X", "PUT", fmt.Sprintf("https://127.0.0.1:%d/offsite", port)).CombinedOutput()
		if err == nil {
			break
		}
		if i > 50 {
			t.Fatalf("create the bucket: %v %s", err, out)
		}
		time.Sleep(200 * time.Millisecond)
	}
	s.endpoint = fmt.Sprintf("https://host.lima.internal:%d", port)
	return s
}

// plaintext reports which markers appear in any file of the bucket.
func (s *localS3) plaintext(t *testing.T, markers ...string) []string {
	t.Helper()
	var found []string
	_ = filepath.WalkDir(s.data, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, m := range markers {
			if bytes.Contains(raw, []byte(m)) {
				found = append(found, m+" in "+strings.TrimPrefix(p, s.data))
			}
		}
		return nil
	})
	return found
}

// TestOffsite proves off-box copies end to end against a local S3: box A
// (Postgres rows, a bucket object, a Valkey key, a secret, an agent token)
// sets a destination (the passphrase is shown once) → test → a full backup
// is copied → a second copy sends only what changed → a drill of the
// off-box copy passes → the bucket holds no plaintext → A is destroyed → a
// fresh box B: setting the destination without the passphrase is refused,
// with it the destination is another cluster's ("foreign") → restore latest
// --from offsite brings back Postgres, Valkey, the files and the platform
// state (A's token works on B) → B's own copies resume into the bucket.
func TestOffsite(t *testing.T) {
	RequireLima(t)
	start := time.Now()
	phase := phaseLogger(t)
	dir := t.TempDir()
	if os.Getenv("TIFFIN_E2E_KEEP") == "1" {
		// Keep the CLI configs too, to drive the boxes left behind.
		dir, _ = os.MkdirTemp("", "tiffin-offsite-e2e-")
		t.Logf("TIFFIN_E2E_KEEP=1: CLI configs in %s", dir)
	}
	s3 := startS3(t, dir)
	cli := buildTiffin(t, dir, "", "")
	bin := buildTiffin(t, dir, "linux", "0.0.1-offsite")

	// ---- box A ----
	p := time.Now()
	a := portBox(t, "a", dir, cli, os.Getenv("TIFFIN_E2E_BOX_A"))
	a.ok("up", "--binary", bin)
	phase("A up", p)
	if code := a.inBox(fmt.Sprintf("curl -sk -o /dev/null -w '%%{http_code}' %s/ || true", s3.endpoint)); code == "000" || code == "" {
		t.Fatalf("the box cannot reach the S3 on this machine at %s (got %q)", s3.endpoint, code)
	}

	p = time.Now()
	a.apply("shop", `{"project":"shop","services":{"postgres":{},"valkey":{"maxMemoryMB":32},"storage":{"buckets":{"media":{}}}}}`)
	a.waitReady("service/postgres", "service/valkey", "service/storage", "bucket/media")
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
	sql(a, `{"write":true,"sql":"create table notes(id int primary key, body text); insert into notes values (1,'offbox-row-7f3a'),(2,'still here'); insert into notes select g, repeat('filler ', 40) from generate_series(3, 20000) g"}`)
	a.ok("storage", "objects", "put", "shop", "media", "--key", "notes/a.txt", "--text", "offbox-object-91c2")
	kvURL := a.ok("kv", "connection", "shop")["redisUrl"].(string)
	if got := a.inBox(`valkey-cli -u '` + kvURL + `' --no-auth-warning set p_shop:greeting offbox-valkey-55d1`); got != "OK" {
		t.Fatalf("valkey set: %s", got)
	}
	a.ok("secrets", "set", "shop", "GREETING", "--value", "offbox-secret-c0de")
	agent := a.ok("tokens", "create", "--name", "e2e-agent", "--projects", "shop", "--access", "full")
	agentSecret, _ := agent["secret"].(string)
	phase("A data", p)

	// ---- the destination ----
	p = time.Now()
	set := a.ok("backups", "offsite", "set", "--endpoint", s3.endpoint, "--bucket", "offsite", "--prefix", "boxes/shop",
		"--access-key-id", s3.access, "--secret-access-key", s3.secret, "--ca-cert", s3.ca)
	pass, _ := set["passphrase"].(string)
	if set["state"] != "active" || len(pass) < 20 || !strings.Contains(fmt.Sprint(set["passphraseNote"]), "shown only now") {
		t.Fatalf("offsite set: %v", set)
	}
	_, shown := a.run("backups", "offsite", "show")
	if strings.Contains(shown, pass) || strings.Contains(shown, s3.secret) || !strings.Contains(shown, `"state": "active"`) {
		t.Fatalf("show reveals a secret or is not active: %s", shown)
	}
	if again := a.ok("backups", "offsite", "set", "--endpoint", s3.endpoint, "--bucket", "offsite", "--prefix", "boxes/shop",
		"--access-key-id", s3.access, "--retention-days", "14", "--ca-cert", s3.ca); again["passphrase"] != nil || again["retentionDays"] != float64(14) {
		t.Fatalf("changing the retention must keep the secret and not show the passphrase again: %v", again)
	}
	tst := a.ok("backups", "offsite", "test")
	if tst["ok"] != true || len(tst["steps"].([]any)) != 4 {
		t.Fatalf("offsite test: %v", tst)
	}
	if got := a.inBox("sudo stat -c '%a %U:%G' /etc/pgbackrest/tiffin-offsite.conf"); got != "640 root:postgres" {
		t.Fatalf("repo2 config file: %s", got)
	}
	// Postgres archives to the local repository only: the bucket never holds it up.
	if got := a.inBox("sudo cat /etc/pgbackrest/pgbackrest.conf; sudo ls /etc/pgbackrest/conf.d 2>/dev/null || true"); strings.Contains(got, "repo2") {
		t.Fatalf("the archive_command's config mentions repo2: %s", got)
	}
	phase("offsite set", p)

	// ---- copies ----
	p = time.Now()
	backup := func(b *cliBox, kind string) map[string]any {
		t.Helper()
		for i := 0; ; i++ {
			code, out := b.run("backup", "--kind", kind)
			if code == 0 {
				var bk map[string]any
				_ = json.Unmarshal([]byte(out), &bk)
				return bk
			}
			if i == 60 || !strings.Contains(out, "another backup") {
				t.Fatalf("tiffin backup: exit %d\n%s", code, out)
			}
			time.Sleep(5 * time.Second)
		}
	}
	copyNow := func(b *cliBox, id string) map[string]any {
		t.Helper()
		cp := b.ok("backups", "offsite", "copy", "--backup", id, "--timeout-seconds", "900")
		if cp["status"] != "ok" {
			t.Fatalf("copy of %s: %v", id, cp)
		}
		return cp
	}
	bk1 := backup(a, "full")
	cp1 := copyNow(a, bk1["id"].(string))
	f1 := cp1["files"].(map[string]any)
	t.Logf("COPY 1 %s: postgres %v (%v bytes), files %v (%v bytes), %v new chunks, %v bytes sent in %vms",
		bk1["id"], cp1["postgresLabel"], cp1["postgresBytes"], f1["files"], f1["bytes"], f1["newChunks"], cp1["sentBytes"], cp1["durationMs"])
	if cp1["postgresLabel"] == "" || f1["files"].(float64) == 0 || f1["chunks"].(float64) == 0 {
		t.Fatalf("first copy: %v", cp1)
	}
	sql(a, `{"write":true,"sql":"insert into notes values (20001, 'after the first copy')"}`)
	bk2 := backup(a, "incremental")
	cp2 := copyNow(a, bk2["id"].(string))
	f2 := cp2["files"].(map[string]any)
	t.Logf("COPY 2 %s: postgres %v %v (%v bytes), files %v, %v unchanged, %v new chunks, %v bytes sent in %vms",
		bk2["id"], cp2["postgresType"], cp2["postgresLabel"], cp2["postgresBytes"], f2["files"], f2["reusedFiles"], f2["newChunks"], cp2["sentBytes"], cp2["durationMs"])
	if cp2["postgresType"] != "incr" || f2["reusedFiles"].(float64) == 0 || f2["newChunks"].(float64) >= f2["chunks"].(float64) ||
		f2["sentBytes"].(float64) >= f2["bytes"].(float64)/2 {
		t.Fatalf("the second copy should send only what changed: %v", cp2)
	}
	// The box's first scheduled backup was copied too, when the destination was set.
	sets := a.list("backups", "offsite", "list")
	if len(sets) < 2 || sets[0]["id"] != bk2["id"] || sets[1]["id"] != bk1["id"] || sets[0]["restorable"] != true {
		t.Fatalf("sets in the bucket: %v", sets)
	}
	if l := a.ok("backups", "list"); l["offsite"].(map[string]any)["lastOk"].(map[string]any)["backup"] != bk2["id"] {
		t.Fatalf("overview: %v", l["offsite"])
	}
	phase("copies", p)

	// ---- a drill of the off-box copy ----
	p = time.Now()
	d := a.ok("backups", "drill", "--from", "offsite", "--wait")
	for deadline := time.Now().Add(10 * time.Minute); d["status"] == "running"; time.Sleep(2 * time.Second) {
		if time.Now().After(deadline) {
			t.Fatalf("drill still running: %v", d)
		}
		d = a.ok("backups", "drills", "get", d["id"].(string))
	}
	off, _ := d["offsite"].(map[string]any)
	t.Logf("OFFSITE DRILL %s: %v", d["status"], d["message"])
	if d["status"] != "passed" || d["source"] != "offsite" || off == nil || off["files"].(float64) == 0 || len(off["checks"].([]any)) < 4 {
		t.Fatalf("off-box drill: %v", d)
	}
	for _, c := range a.ok("status")["checks"].([]any) {
		if c := c.(map[string]any); c["name"] == "offsite-backups" && (c["ok"] != true || !strings.Contains(fmt.Sprint(c["detail"]), "copied to")) {
			t.Fatalf("offsite check: %v", c)
		}
	}
	if leaks := s3.plaintext(t, "offbox-row-7f3a", "offbox-object-91c2", "offbox-valkey-55d1", "offbox-secret-c0de", "notes/a.txt", "p_shop"); len(leaks) > 0 {
		t.Fatalf("plaintext in the bucket: %v", leaks)
	}
	phase("drill", p)

	p = time.Now()
	a.ok("down", "--confirm", "local")
	phase("A down", p)

	// ---- a fresh box B restores everything from the bucket ----
	p = time.Now()
	b := portBox(t, "b", dir, cli, os.Getenv("TIFFIN_E2E_BOX_B"))
	b.ok("up", "--binary", bin)
	phase("B up", p)

	p = time.Now()
	dest := []string{"backups", "offsite", "set", "--endpoint", s3.endpoint, "--bucket", "offsite", "--prefix", "boxes/shop",
		"--access-key-id", s3.access, "--secret-access-key", s3.secret, "--ca-cert", s3.ca}
	if code, out := b.run(dest...); code == 0 || !strings.Contains(out, "passphrase") {
		t.Fatalf("a destination holding copies needs the passphrase: exit %d %s", code, out)
	}
	if code, out := b.run(append(dest, "--passphrase", "not-the-right-one")...); code == 0 || !strings.Contains(out, "does not match") {
		t.Fatalf("a wrong passphrase: exit %d %s", code, out)
	}
	bset := b.ok(append(dest, "--passphrase", pass)...)
	if bset["state"] != "foreign" || bset["passphrase"] != nil {
		t.Fatalf("B's destination: %v", bset)
	}
	if l := b.list("backups", "offsite", "list"); len(l) < 2 || l[0]["id"] != bk2["id"] {
		t.Fatalf("B sees A's sets: %v", l)
	}
	code, out := b.run("restore", "latest", "--from", "offsite")
	var pv struct {
		Confirm string `json:"confirm"`
		Preview struct {
			Backup  string   `json:"backup"`
			Targets []string `json:"targets"`
			Safety  string   `json:"safety"`
		} `json:"preview"`
	}
	_ = json.Unmarshal([]byte(out), &pv)
	if code != 4 || pv.Preview.Backup != bk2["id"] || strings.Join(pv.Preview.Targets, ",") != "files,platform,postgres,valkey" || !strings.HasPrefix(pv.Preview.Safety, "none") {
		t.Fatalf("restore preview on a fresh box: exit %d %s", code, out)
	}
	rs := b.ok("restore", "latest", "--from", "offsite", "--confirm", pv.Confirm, "--timeout-seconds", "900")
	t.Logf("RESTORE from the bucket: %v", rs)
	if rs["restarting"] != true || rs["from"] != "offsite" {
		t.Fatalf("restore: %v", rs)
	}
	// The service restarts to swap the state and files in.
	time.Sleep(5 * time.Second)
	for deadline := time.Now().Add(5 * time.Minute); ; time.Sleep(2 * time.Second) {
		if code, _ := b.run("whoami"); code == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("B did not come back: %s", b.inBox("sudo journalctl -u tiffin -n 80 --no-pager"))
		}
	}
	phase("restore", p)

	// ---- everything is back ----
	p = time.Now()
	var rows string
	for i := 0; i < 60; i++ { // the project converges after the restart
		if code, out := b.run("sql", "shop", "--body", `{"sql":"select id, body from notes where id in (1, 2, 20001) order by id"}`); code == 0 {
			var res map[string]any
			_ = json.Unmarshal([]byte(out), &res)
			if r, _ := res["results"].([]any); len(r) > 0 {
				raw, _ := json.Marshal(r[len(r)-1].(map[string]any)["rows"])
				rows = string(raw)
				break
			}
		}
		time.Sleep(2 * time.Second)
	}
	if rows != `[[1,"offbox-row-7f3a"],[2,"still here"],[20001,"after the first copy"]]` {
		t.Fatalf("postgres rows on B: %s", rows)
	}
	if n := sql(b, `{"sql":"select count(*) from notes"}`); n != "[[20001]]" {
		t.Fatalf("row count on B: %s", n)
	}
	if g := b.ok("storage", "objects", "get", "shop", "media", "--key", "notes/a.txt"); g["text"] != "offbox-object-91c2" {
		t.Fatalf("object on B: %v", g)
	}
	kvB := b.ok("kv", "connection", "shop")["redisUrl"].(string)
	if got := b.inBox(`valkey-cli -u '` + kvB + `' --no-auth-warning get p_shop:greeting`); got != "offbox-valkey-55d1" {
		t.Fatalf("valkey on B: %s", got)
	}
	if l := b.list("secrets", "list", "shop"); !strings.Contains(fmt.Sprint(l), "GREETING") {
		t.Fatalf("secrets on B: %v", l)
	}
	agentEnv := append(append([]string(nil), b.env...), "TIFFIN_TOKEN="+agentSecret)
	cmd := exec.Command(cli, "whoami")
	cmd.Env, cmd.Dir = agentEnv, b.dir
	if out, err := cmd.Output(); err != nil || !strings.Contains(string(out), "e2e-agent") {
		t.Fatalf("A's agent token on B: %v %s", err, out)
	}
	if w := b.ok("whoami"); w["kind"] != "owner" {
		t.Fatalf("B's owner token: %v", w)
	}
	phase("verify", p)

	// ---- B's own copies resume into the same bucket ----
	p = time.Now()
	for deadline := time.Now().Add(8 * time.Minute); ; time.Sleep(5 * time.Second) {
		if s := b.ok("backups", "offsite", "show"); s["state"] == "active" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("B's copies did not resume: %v", b.ok("backups", "offsite", "show"))
		}
	}
	sql(b, `{"write":true,"sql":"insert into notes values (30001, 'written on box B')"}`)
	bk3 := backup(b, "incremental")
	cp3 := copyNow(b, bk3["id"].(string))
	t.Logf("COPY 3 (box B) %s: postgres %v %v, %v bytes sent", bk3["id"], cp3["postgresType"], cp3["postgresLabel"], cp3["sentBytes"])
	if l := b.list("backups", "offsite", "list"); len(l) < 3 || l[0]["id"] != bk3["id"] {
		t.Fatalf("sets after B's copy: %v", l)
	}
	phase("B copies", p)

	// ---- the bucket goes away: local backups carry on, the copy fails and says so ----
	p = time.Now()
	s3.stop()
	sql(b, `{"write":true,"sql":"insert into notes select g, 'while the bucket is down' from generate_series(40001, 41000) g"}`)
	bk4 := backup(b, "incremental")
	if bk4["status"] != "ok" {
		t.Fatalf("a local backup with the bucket down: %v", bk4)
	}
	code, out = b.run("backups", "offsite", "copy", "--backup", bk4["id"].(string), "--timeout-seconds", "600")
	if !strings.Contains(out, `"status": "failed"`) {
		t.Fatalf("a copy with the bucket down: exit %d %s", code, out)
	}
	for _, c := range b.ok("status")["checks"].([]any) {
		if c := c.(map[string]any); c["name"] == "backups" && c["ok"] != true {
			t.Fatalf("local backups check with the bucket down: %v", c)
		}
	}
	if got := sql(b, `{"sql":"select count(*) from notes"}`); got != "[[21002]]" {
		t.Fatalf("Postgres keeps working with the bucket down: %s", got)
	}
	t.Logf("BUCKET DOWN: local backup %s ok in %vms; copy: %s", bk4["id"], bk4["durationMs"], tail(out, 400))
	phase("bucket down", p)
	t.Logf("OFFSITE total %s", time.Since(start).Round(time.Second))
}
