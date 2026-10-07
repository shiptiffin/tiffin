//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/mod/backup"
)

// offsiteDest is a bucket the boxes copy to, and the test's own view of it.
type offsiteDest struct {
	endpoint, bucket, prefix, access, secret, ca string
	store                                        *backup.Bucket
	stop                                         func()   // takes the bucket away; nil when the test cannot
	private                                      []string // values the test's output must not show
}

// hide masks d's private values in s.
func (d *offsiteDest) hide(s string) string {
	for _, v := range d.private {
		if v != "" {
			s = strings.ReplaceAll(s, v, "<hidden>")
		}
	}
	return s
}

// set runs `tiffin backups offsite set` for d with extra flags, with the
// secret key or (withSecret false) without it. The destination goes in on
// stdin, so it never shows in the arguments or a failure message. It
// waits out a copy that is running.
func (d *offsiteDest) set(b *cliBox, withSecret bool, extra ...string) (int, string) {
	t := b.t
	t.Helper()
	in := map[string]string{"endpoint": d.endpoint, "bucket": d.bucket, "prefix": d.prefix, "accessKeyId": d.access, "caCert": d.ca}
	if withSecret {
		in["secretAccessKey"] = d.secret
	}
	body, _ := json.Marshal(in)
	for deadline := time.Now().Add(15 * time.Minute); ; time.Sleep(5 * time.Second) {
		code, out := b.runWith(string(body), append([]string{"backups", "offsite", "set", "--body-file", "-"}, extra...)...)
		if !strings.Contains(out, "copy or restore is running") || time.Now().After(deadline) {
			return code, out
		}
	}
}

// census counts the objects under the prefix and their bytes, in all and
// by folder (the first two levels under the prefix).
func (d *offsiteDest) census(t *testing.T) string {
	t.Helper()
	type tally struct{ n, size int64 }
	var all tally
	by := map[string]*tally{}
	err := d.store.List(context.Background(), d.prefix+"/", func(key string, size int64) error {
		f := strings.SplitN(strings.TrimPrefix(key, d.prefix+"/"), "/", 3)
		dir := f[0]
		if len(f) > 2 {
			dir += "/" + f[1]
		}
		if by[dir] == nil {
			by[dir] = &tally{}
		}
		by[dir].n++
		by[dir].size += size
		all.n++
		all.size += size
		return nil
	})
	if err != nil {
		t.Fatal(d.hide("listing the bucket: " + err.Error()))
	}
	dirs := make([]string, 0, len(by))
	for k := range by {
		dirs = append(dirs, k)
	}
	sort.Strings(dirs)
	s := fmt.Sprintf("%d objects, %d bytes:", all.n, all.size)
	for _, k := range dirs {
		s += fmt.Sprintf(" %s %d/%dB;", k, by[k].n, by[k].size)
	}
	return s
}

// plaintext reports which markers appear in any object under the prefix.
func (d *offsiteDest) plaintext(t *testing.T, markers ...string) []string {
	t.Helper()
	ctx := context.Background()
	var keys, found []string
	if err := d.store.List(ctx, d.prefix+"/", func(k string, _ int64) error { keys = append(keys, k); return nil }); err != nil {
		t.Fatal(d.hide("listing the bucket: " + err.Error()))
	}
	for _, k := range keys {
		for _, m := range markers {
			if strings.Contains(k, m) {
				found = append(found, m+" in the name "+k)
			}
		}
		raw, err := d.store.Get(ctx, k, 1<<30)
		if err != nil {
			t.Fatal(d.hide(fmt.Sprintf("reading %s: %v", k, err)))
		}
		for _, m := range markers {
			if bytes.Contains(raw, []byte(m)) {
				found = append(found, m+" in "+k)
			}
		}
	}
	return found
}

// startS3 runs versitygw (TIFFIN_TEST_VERSITYGW: a build for this OS) on
// 127.0.0.1, over TLS with a private CA, with the bucket "offsite"; the
// boxes reach it at host.lima.internal.
func startS3(t *testing.T, dir string) *offsiteDest {
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
	certPath, keyPath := filepath.Join(dir, "s3.crt"), filepath.Join(dir, "s3.key")
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	_ = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srvDER}), 0o600)
	_ = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)

	d := &offsiteDest{bucket: "offsite", prefix: "boxes/shop", ca: string(ca), access: "TFNE2EROOT"}
	sec := make([]byte, 16)
	_, _ = rand.Read(sec)
	d.secret = hex.EncodeToString(sec)
	d.private = []string{d.secret}
	data := filepath.Join(dir, "s3data")
	_ = os.MkdirAll(data, 0o755)
	port := freePort(t)
	cmd := exec.Command(bin, "--port", fmt.Sprintf("127.0.0.1:%d", port), "--cert", certPath, "--key", keyPath, "--region", "us-east-1", "--quiet", "posix", data)
	cmd.Env = append(os.Environ(), "ROOT_ACCESS_KEY="+d.access, "ROOT_SECRET_KEY="+d.secret)
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	d.stop = func() {
		once.Do(func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
	}
	t.Cleanup(func() {
		d.stop()
		if t.Failed() {
			t.Logf("versitygw:\n%s", tail(logs.String(), 4000))
		}
	})
	var err error
	d.store, err = backup.NewBucket(backup.OffsiteInput{Endpoint: fmt.Sprintf("https://127.0.0.1:%d", port), Bucket: d.bucket,
		AccessKeyID: d.access, SecretAccessKey: d.secret, CACert: d.ca})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; ; i++ {
		if err = d.store.CreateBucket(context.Background()); err == nil {
			break
		}
		if i > 50 {
			t.Fatalf("create the bucket: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	d.endpoint = fmt.Sprintf("https://host.lima.internal:%d", port)
	return d
}

// r2Dest is a fresh prefix e2e/<time>-<random> in the owner's Cloudflare R2
// bucket (TIFFIN_TEST_R2=1 with R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY,
// R2_BUCKET and CLOUDFLARE_ACCOUNT_ID). Everything under it is deleted when
// the test ends, failed or not, and the prefix is checked to be empty.
func r2Dest(t *testing.T) *offsiteDest {
	t.Helper()
	if os.Getenv("TIFFIN_TEST_R2") != "1" {
		t.Skip("TIFFIN_TEST_R2 not set (runs against a real Cloudflare R2 bucket)")
	}
	for _, k := range []string{"R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY", "R2_BUCKET", "CLOUDFLARE_ACCOUNT_ID"} {
		if os.Getenv(k) == "" {
			t.Fatalf("TIFFIN_TEST_R2=1 needs %s", k)
		}
	}
	rnd := make([]byte, 4)
	_, _ = rand.Read(rnd)
	d := &offsiteDest{endpoint: "https://" + os.Getenv("CLOUDFLARE_ACCOUNT_ID") + ".r2.cloudflarestorage.com", bucket: os.Getenv("R2_BUCKET"),
		prefix: "e2e/" + time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(rnd),
		access: os.Getenv("R2_ACCESS_KEY_ID"), secret: os.Getenv("R2_SECRET_ACCESS_KEY"),
		private: []string{os.Getenv("R2_SECRET_ACCESS_KEY"), os.Getenv("R2_ACCESS_KEY_ID"), os.Getenv("CLOUDFLARE_ACCOUNT_ID"), os.Getenv("R2_BUCKET")}}
	var err error
	if d.store, err = backup.NewBucket(backup.OffsiteInput{Endpoint: d.endpoint, Bucket: d.bucket, AccessKeyID: d.access, SecretAccessKey: d.secret}); err != nil {
		t.Fatal(d.hide(err.Error()))
	}
	t.Logf("R2 prefix %s", d.prefix)
	// Registered first, so it runs after the boxes (which copy into the prefix) are gone.
	t.Cleanup(func() {
		ctx := context.Background()
		n, err := d.store.DeletePrefix(ctx, d.prefix+"/")
		if err != nil {
			t.Error(d.hide(fmt.Sprintf("emptying %s: %v", d.prefix, err)))
		}
		left := 0
		if err := d.store.List(ctx, d.prefix+"/", func(string, int64) error { left++; return nil }); err != nil || left > 0 {
			t.Error(d.hide(fmt.Sprintf("after emptying %s: %d objects left (%v)", d.prefix, left, err)))
		}
		t.Logf("R2 CLEANUP: deleted %d objects under %s; %d left", n, d.prefix, left)
	})
	return d
}

// TestOffsite proves off-box copies end to end against a local S3
// (versitygw); see offsiteRoundTrip.
func TestOffsite(t *testing.T) {
	RequireLima(t)
	dir := t.TempDir()
	if os.Getenv("TIFFIN_E2E_KEEP") == "1" {
		// Keep the CLI configs too, to drive the boxes left behind.
		dir, _ = os.MkdirTemp("", "tiffin-offsite-e2e-")
		t.Logf("TIFFIN_E2E_KEEP=1: CLI configs in %s", dir)
	}
	offsiteRoundTrip(t, dir, startS3(t, dir))
}

// TestOffsiteR2 is the same round trip against a real Cloudflare R2 bucket
// (TIFFIN_TEST_R2=1), under a prefix of its own that it empties at the end.
func TestOffsiteR2(t *testing.T) {
	d := r2Dest(t)
	RequireLima(t)
	offsiteRoundTrip(t, t.TempDir(), d)
}

// offsiteRoundTrip: box A (Postgres rows, a bucket object, a Valkey key, a
// secret, an agent token) sets the destination (the passphrase is shown
// once) → test → a full backup is copied → a second copy sends only what
// changed → WAL reaches the bucket on its own → a drill of the off-box copy
// passes → the bucket holds no plaintext → A is destroyed → a fresh box B:
// setting the destination without the passphrase, or with a wrong one, is
// refused; with it the destination is another cluster's ("foreign") →
// restore latest --from offsite brings back Postgres, Valkey, the files and
// the platform state (A's token works on B) → B's own copies resume into
// the bucket → (when the test can take the bucket away) local backups carry
// on while copies fail.
func offsiteRoundTrip(t *testing.T, dir string, d *offsiteDest) {
	// The boxes' answers name the endpoint: the output masks d's private values.
	logf := func(format string, a ...any) { t.Helper(); t.Log(d.hide(fmt.Sprintf(format, a...))) }
	fatalf := func(format string, a ...any) { t.Helper(); t.Fatal(d.hide(fmt.Sprintf(format, a...))) }
	start := time.Now()
	phase := phaseLogger(t)
	cli := buildTiffin(t, dir, "", "")
	bin := buildTiffin(t, dir, "linux", "0.0.1-offsite")

	// ---- box A ----
	p := time.Now()
	a := portBox(t, "a", dir, cli, os.Getenv("TIFFIN_E2E_BOX_A"))
	a.hide = d.hide
	a.ok("up", "--binary", bin)
	phase("A up", p)
	if code := a.inBox(fmt.Sprintf("curl -sk -o /dev/null -w '%%{http_code}' %s/ || true", d.endpoint)); code == "000" || code == "" {
		fatalf("the box cannot reach the S3 endpoint %s (got %q)", d.endpoint, code)
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
	kvURL := a.ok("kv", "connection", "shop", "--reveal")["redisUrl"].(string)
	if got := a.inBox(`valkey-cli -u '` + kvURL + `' --no-auth-warning set p_shop:greeting offbox-valkey-55d1`); got != "OK" {
		fatalf("valkey set: %s", got)
	}
	a.ok("secrets", "set", "shop", "GREETING", "--value", "offbox-secret-c0de")
	agent := a.ok("tokens", "create", "--name", "e2e-agent", "--projects", "shop", "--access", "full")
	agentSecret, _ := agent["secret"].(string)
	phase("A data", p)

	// ---- the destination ----
	p = time.Now()
	code, out := d.set(a, true)
	var set map[string]any
	_ = json.Unmarshal([]byte(out), &set)
	pass, _ := set["passphrase"].(string)
	if code != 0 || set["state"] != "active" || len(pass) < 20 || !strings.Contains(fmt.Sprint(set["passphraseNote"]), "shown only now") {
		fatalf("offsite set: exit %d %s", code, strings.ReplaceAll(out, pass, "<passphrase>"))
	}
	_, shown := a.run("backups", "offsite", "show")
	if strings.Contains(shown, pass) || strings.Contains(shown, d.secret) || !strings.Contains(shown, `"state": "active"`) {
		t.Fatal("show reveals a secret or is not active")
	}
	if code, out := d.set(a, false, "--retention-days", "14"); code != 0 || strings.Contains(out, "passphrase\"") || !strings.Contains(out, `"retentionDays": 14`) {
		fatalf("changing the retention must keep the secret and not show the passphrase again: exit %d %s", code, out)
	}
	tst := a.ok("backups", "offsite", "test")
	if tst["ok"] != true || len(tst["steps"].([]any)) != 4 {
		fatalf("offsite test: %v", tst)
	}
	logf("PROBE: %v", tst["steps"])
	if got := a.inBox("sudo stat -c '%a %U:%G' /etc/pgbackrest/tiffin-offsite.conf"); got != "640 root:postgres" {
		fatalf("repo2 config file: %s", got)
	}
	// Postgres archives to the local repository only: the bucket never holds it up.
	if got := a.inBox("sudo cat /etc/pgbackrest/pgbackrest.conf; sudo ls /etc/pgbackrest/conf.d 2>/dev/null || true"); strings.Contains(got, "repo2") {
		fatalf("the archive_command's config mentions repo2: %s", got)
	}
	phase("offsite set", p)

	// ---- copies ----
	p = time.Now()
	takeBackup := func(b *cliBox, kind string) map[string]any {
		t.Helper()
		for i := 0; ; i++ {
			code, out := b.run("backup", "--kind", kind)
			if code == 0 {
				var bk map[string]any
				_ = json.Unmarshal([]byte(out), &bk)
				return bk
			}
			if i == 60 || !strings.Contains(out, "another backup") {
				fatalf("tiffin backup: exit %d\n%s", code, out)
			}
			time.Sleep(5 * time.Second)
		}
	}
	copyNow := func(b *cliBox, id string) map[string]any {
		t.Helper()
		cp := b.ok("backups", "offsite", "copy", "--backup", id, "--timeout-seconds", "900")
		if cp["status"] != "ok" {
			fatalf("copy of %s: %v", id, cp)
		}
		return cp
	}
	rate := func(cp map[string]any) string {
		ms, _ := cp["durationMs"].(float64)
		sent, _ := cp["sentBytes"].(float64)
		if ms == 0 {
			return "-"
		}
		return fmt.Sprintf("%.2f MB/s", sent/1e6/(ms/1000))
	}
	bk1 := takeBackup(a, "full")
	cp1 := copyNow(a, bk1["id"].(string))
	f1 := cp1["files"].(map[string]any)
	logf("COPY 1 %s: postgres %v %v (%v bytes), files %v (%v bytes), %v new chunks, %v bytes sent in %vms (%s)",
		bk1["id"], cp1["postgresType"], cp1["postgresLabel"], cp1["postgresBytes"], f1["files"], f1["bytes"], f1["newChunks"], cp1["sentBytes"], cp1["durationMs"], rate(cp1))
	if cp1["postgresLabel"] == "" || f1["files"].(float64) == 0 || f1["chunks"].(float64) == 0 {
		fatalf("first copy: %v", cp1)
	}
	logf("BUCKET after copy 1: %s", d.census(t))
	sql(a, `{"write":true,"sql":"insert into notes values (20001, 'after the first copy')"}`)
	bk2 := takeBackup(a, "incremental")
	cp2 := copyNow(a, bk2["id"].(string))
	f2 := cp2["files"].(map[string]any)
	logf("COPY 2 %s: postgres %v %v (%v bytes), files %v, %v new chunks, %v bytes sent in %vms (%s)",
		bk2["id"], cp2["postgresType"], cp2["postgresLabel"], cp2["postgresBytes"], f2["files"], f2["newChunks"], cp2["sentBytes"], cp2["durationMs"], rate(cp2))
	if cp2["postgresType"] != "incr" || f2["newChunks"].(float64) >= f2["chunks"].(float64) ||
		f2["sentBytes"].(float64) >= f2["bytes"].(float64)/2 {
		fatalf("the second copy should send only what changed: %v", cp2)
	}
	// The box's first scheduled backup was copied too, when the destination was set.
	sets := a.list("backups", "offsite", "list")
	if len(sets) < 2 || sets[0]["id"] != bk2["id"] || sets[1]["id"] != bk1["id"] || sets[0]["restorable"] != true {
		fatalf("sets in the bucket: %v", sets)
	}
	if l := a.ok("backups", "list"); l["offsite"].(map[string]any)["lastOk"].(map[string]any)["backup"] != bk2["id"] {
		fatalf("overview: %v", l["offsite"])
	}
	repo2Times(t, a)
	logf("BUCKET after copy 2: %s", d.census(t))
	phase("copies", p)

	// ---- WAL archived after a copy reaches the bucket on its own ----
	p = time.Now()
	sql(a, `{"write":true,"sql":"insert into notes values (20002, 'shipped with the WAL')"}`)
	seg := a.inBox(`sudo -u postgres psql -h /var/run/postgresql -XAtc "select pg_walfile_name(pg_switch_wal())"`)
	switched := time.Now()
	var local, remote time.Duration
	for deadline := switched.Add(9 * time.Minute); local == 0 || remote == 0; time.Sleep(2 * time.Second) {
		if time.Now().After(deadline) {
			fatalf("WAL %s: in the local archive after %s, in the bucket after %s (0: not yet)", seg, local, remote)
		}
		if local == 0 && a.inBox("sudo find "+backup.RepoPath+"/archive -name '"+seg+"*' | head -1") != "" {
			local = time.Since(switched)
		}
		if remote == 0 {
			found := false
			_ = d.store.List(context.Background(), d.prefix+"/pgbackrest/archive/", func(k string, _ int64) error {
				found = found || strings.Contains(k, seg)
				return nil
			})
			if found {
				remote = time.Since(switched)
			}
		}
	}
	logf("WAL %s: local archive after %s, bucket after %s (the box ships every 5 minutes)", seg, local.Round(time.Second), remote.Round(time.Second))
	phase("WAL shipping", p)

	// ---- a drill of the off-box copy ----
	p = time.Now()
	dr := a.ok("backups", "drill", "--from", "offsite", "--wait")
	for deadline := time.Now().Add(10 * time.Minute); dr["status"] == "running"; time.Sleep(2 * time.Second) {
		if time.Now().After(deadline) {
			fatalf("drill still running: %v", dr)
		}
		dr = a.ok("backups", "drills", "get", dr["id"].(string))
	}
	off, _ := dr["offsite"].(map[string]any)
	logf("OFFSITE DRILL %s: %v", dr["status"], dr["message"])
	if dr["status"] != "passed" || dr["source"] != "offsite" || off == nil || off["files"].(float64) == 0 || len(off["checks"].([]any)) < 4 {
		fatalf("off-box drill: %v", dr)
	}
	for _, c := range a.ok("status")["checks"].([]any) {
		if c := c.(map[string]any); c["name"] == "offsite-backups" && (c["ok"] != true || !strings.Contains(fmt.Sprint(c["detail"]), "copied to")) {
			fatalf("offsite check: %v", c)
		}
	}
	if leaks := d.plaintext(t, "offbox-row-7f3a", "offbox-object-91c2", "offbox-valkey-55d1", "offbox-secret-c0de", "notes/a.txt", "p_shop", "shipped with the WAL"); len(leaks) > 0 {
		fatalf("plaintext in the bucket: %v", leaks)
	}
	phase("drill", p)

	p = time.Now()
	a.ok("down", "--confirm", "local")
	phase("A down", p)

	// ---- a fresh box B restores everything from the bucket ----
	p = time.Now()
	b := portBox(t, "b", dir, cli, os.Getenv("TIFFIN_E2E_BOX_B"))
	b.hide = d.hide
	b.ok("up", "--binary", bin)
	phase("B up", p)

	p = time.Now()
	if code, out := d.set(b, true); code == 0 || !strings.Contains(out, "passphrase") {
		fatalf("a destination holding copies needs the passphrase: exit %d %s", code, out)
	}
	if code, out := d.set(b, true, "--passphrase", "not-the-right-one"); code == 0 || !strings.Contains(out, "does not match") {
		fatalf("a wrong passphrase: exit %d %s", code, out)
	}
	code, out = d.set(b, true, "--passphrase", pass)
	var bset map[string]any
	_ = json.Unmarshal([]byte(out), &bset)
	if code != 0 || bset["state"] != "foreign" || bset["passphrase"] != nil {
		fatalf("B's destination: exit %d %s", code, strings.ReplaceAll(out, pass, "<passphrase>"))
	}
	if l := b.list("backups", "offsite", "list"); len(l) < 2 || l[0]["id"] != bk2["id"] {
		fatalf("B sees A's sets: %v", l)
	}
	code, out = b.run("restore", "latest", "--from", "offsite")
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
		fatalf("restore preview on a fresh box: exit %d %s", code, out)
	}
	restoreStart := time.Now()
	rs := b.ok("restore", "latest", "--from", "offsite", "--confirm", pv.Confirm, "--timeout-seconds", "900")
	restoreCall := time.Since(restoreStart)
	logf("RESTORE from the bucket: %v", rs)
	if rs["restarting"] != true || rs["from"] != "offsite" {
		fatalf("restore: %v", rs)
	}
	// The service restarts to swap the state and files in.
	time.Sleep(5 * time.Second)
	for deadline := time.Now().Add(5 * time.Minute); ; time.Sleep(2 * time.Second) {
		if code, _ := b.run("whoami"); code == 0 {
			break
		}
		if time.Now().After(deadline) {
			fatalf("B did not come back: %s", b.inBox("sudo journalctl -u tiffin -n 80 --no-pager"))
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
	logf("RESTORE timing: the restore call %s, until Postgres answers %s", restoreCall.Round(time.Second), time.Since(restoreStart).Round(time.Second))
	if rows != `[[1,"offbox-row-7f3a"],[2,"still here"],[20001,"after the first copy"]]` {
		fatalf("postgres rows on B: %s", rows)
	}
	// A set restores to its own point: 20002, written after copy 2, is not in it.
	if n := sql(b, `{"sql":"select count(*) from notes"}`); n != "[[20001]]" {
		fatalf("row count on B: %s", n)
	}
	if g := b.ok("storage", "objects", "get", "shop", "media", "--key", "notes/a.txt"); g["text"] != "offbox-object-91c2" {
		fatalf("object on B: %v", g)
	}
	kvB := b.ok("kv", "connection", "shop", "--reveal")["redisUrl"].(string)
	if got := b.inBox(`valkey-cli -u '` + kvB + `' --no-auth-warning get p_shop:greeting`); got != "offbox-valkey-55d1" {
		fatalf("valkey on B: %s", got)
	}
	if l := b.list("secrets", "list", "shop"); !strings.Contains(fmt.Sprint(l), "GREETING") {
		fatalf("secrets on B: %v", l)
	}
	agentEnv := append(append([]string(nil), b.env...), "TIFFIN_TOKEN="+agentSecret)
	cmd := exec.Command(cli, "whoami")
	cmd.Env, cmd.Dir = agentEnv, b.dir
	if out, err := cmd.Output(); err != nil || !strings.Contains(string(out), "e2e-agent") {
		fatalf("A's agent token on B: %v %s", err, out)
	}
	if w := b.ok("whoami"); w["kind"] != "owner" {
		fatalf("B's owner token: %v", w)
	}
	phase("verify", p)

	// ---- B's own copies resume into the same bucket ----
	p = time.Now()
	for deadline := time.Now().Add(8 * time.Minute); ; time.Sleep(5 * time.Second) {
		if s := b.ok("backups", "offsite", "show"); s["state"] == "active" {
			break
		}
		if time.Now().After(deadline) {
			fatalf("B's copies did not resume: %v", b.ok("backups", "offsite", "show"))
		}
	}
	sql(b, `{"write":true,"sql":"insert into notes values (30001, 'written on box B')"}`)
	bk3 := takeBackup(b, "incremental")
	cp3 := copyNow(b, bk3["id"].(string))
	logf("COPY 3 (box B) %s: postgres %v %v, %v bytes sent in %vms", bk3["id"], cp3["postgresType"], cp3["postgresLabel"], cp3["sentBytes"], cp3["durationMs"])
	if l := b.list("backups", "offsite", "list"); len(l) < 3 || l[0]["id"] != bk3["id"] {
		fatalf("sets after B's copy: %v", l)
	}
	repo2Times(t, b)
	logf("BUCKET at the end: %s", d.census(t))
	phase("B copies", p)

	if d.stop != nil {
		// ---- the bucket goes away: local backups carry on, the copy fails and says so ----
		p = time.Now()
		d.stop()
		sql(b, `{"write":true,"sql":"insert into notes select g, 'while the bucket is down' from generate_series(40001, 41000) g"}`)
		bk4 := takeBackup(b, "incremental")
		if bk4["status"] != "ok" {
			fatalf("a local backup with the bucket down: %v", bk4)
		}
		code, out = b.run("backups", "offsite", "copy", "--backup", bk4["id"].(string), "--timeout-seconds", "600")
		if !strings.Contains(out, `"status": "failed"`) {
			fatalf("a copy with the bucket down: exit %d %s", code, out)
		}
		for _, c := range b.ok("status")["checks"].([]any) {
			if c := c.(map[string]any); c["name"] == "backups" && c["ok"] != true {
				fatalf("local backups check with the bucket down: %v", c)
			}
		}
		if got := sql(b, `{"sql":"select count(*) from notes"}`); got != "[[21002]]" {
			fatalf("Postgres keeps working with the bucket down: %s", got)
		}
		logf("BUCKET DOWN: local backup %s ok in %vms; copy: %s", bk4["id"], bk4["durationMs"], tail(out, 400))
		phase("bucket down", p)
	}
	logf("OFFSITE total %s", time.Since(start).Round(time.Second))
}

// repo2Times logs every Postgres backup in the bucket's pgBackRest
// repository: type, how long it took, and what it added there.
func repo2Times(t *testing.T, b *cliBox) {
	t.Helper()
	out := b.inBox("sudo -u postgres pgbackrest --config=/etc/pgbackrest/tiffin-offsite.conf --stanza=" + backup.Stanza + " --repo=2 --output=json info")
	var st []struct {
		Backup []struct {
			Label     string `json:"label"`
			Type      string `json:"type"`
			Timestamp struct{ Start, Stop int64 }
			Info      struct {
				Size       int64 `json:"size"`
				Repository struct{ Size, Delta int64 }
			} `json:"info"`
		} `json:"backup"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil || len(st) == 0 {
		b.fatalf("pgbackrest info for repo2: %v %s", err, out)
	}
	for _, x := range st[0].Backup {
		t.Logf("REPO2 %s %s: %ds, database %d bytes, %d bytes added to the bucket", x.Label, x.Type, x.Timestamp.Stop-x.Timestamp.Start, x.Info.Size, x.Info.Repository.Delta)
	}
}
