package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
)

// These tests drive a real versitygw. Point TIFFIN_TEST_VERSITYGW at a
// versitygw binary for this OS (the release tarballs have darwin builds);
// without it they are skipped.

type rig struct {
	t     *testing.T
	m     *Module
	p     *platform.Platform
	front string
	ctx   context.Context
}

func freeAddr(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func newRig(t *testing.T) *rig {
	bin := os.Getenv("TIFFIN_TEST_VERSITYGW")
	if bin == "" {
		t.Skip("TIFFIN_TEST_VERSITYGW not set")
	}
	root := t.TempDir()
	for _, d := range []string{dataDir(root), iamDir(root)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rc := newCreds("TFNROOT")
	if err := os.WriteFile(rootEnv(root), []byte("ROOT_ACCESS_KEY="+rc.AccessKey+"\nROOT_SECRET_KEY="+rc.Secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gwAddr := freeAddr(t)
	cmd := exec.Command(bin, "--port", gwAddr, "--region", Region, "--iam-dir", iamDir(root), "--health", healthPath, "--quiet", "posix", dataDir(root))
	cmd.Env = append(os.Environ(), "ROOT_ACCESS_KEY="+rc.AccessKey, "ROOT_SECRET_KEY="+rc.Secret)
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("versitygw output:\n%s", logs.String())
		}
	})
	db, err := state.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	sec, err := platform.OpenSecrets(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := &platform.Platform{DB: db, Engine: change.NewEngine(db), Secrets: sec, DataRoot: root, Domain: "tiffin.localhost",
		PublicURL: "https://dashboard.tiffin.localhost:8443", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	front := freeAddr(t)
	m := &Module{bin: bin, gwAddr: gwAddr, frontAddr: front}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	gw, err := m.gateway(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := gw.waitHealthy(ctx, 10*time.Second); err != nil {
		t.Fatalf("gateway: %v\n%s", err, logs.String())
	}
	if err := m.Start(ctx, p); err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, m: m, p: p, front: front, ctx: ctx}
}

// apply commits a manifest and reconciles what changed, like the platform does.
func (r *rig) apply(raw string) {
	r.t.Helper()
	mf, err := manifest.Parse([]byte(raw))
	if err != nil {
		r.t.Fatal(err)
	}
	desired, err := change.Resources(mf)
	if err != nil {
		r.t.Fatal(err)
	}
	plan, err := r.p.Engine.Plan(r.ctx, mf.Project, desired)
	if err != nil {
		r.t.Fatal(err)
	}
	if _, err := r.p.Engine.Apply(r.ctx, change.ApplyRequest{Plan: plan, Confirm: plan.Hash}); err != nil {
		r.t.Fatal(err)
	}
	for _, op := range plan.Ops {
		if !r.m.handles(op.Address) {
			continue
		}
		var spec json.RawMessage
		if op.Action != change.Delete {
			spec = op.After
		}
		if err := r.m.Reconcile(r.ctx, r.p, mf.Project, op.Address, spec); err != nil {
			r.t.Fatalf("reconcile %s: %v", op.Address, err)
		}
	}
	r.m.tracker().refresh(r.ctx, r.p)
}

func (m *Module) handles(addr string) bool {
	for _, k := range m.Kinds() {
		if k == addr || k == change.Kind(addr) {
			return true
		}
	}
	return false
}

// s3 sends a request to the front server as host, signed with c (nil: anonymous).
func (r *rig) s3(method, host, path string, body []byte, c *Creds, hdr map[string]string) (*http.Response, string) {
	r.t.Helper()
	req, _ := http.NewRequest(method, "http://"+r.front+path, bytes.NewReader(body))
	req.Host = host
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if c != nil {
		signRequest(req, *c, Region, sha256Hex(body), time.Now())
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

const shopManifest = `{"project":"shop","services":{"storage":{"buckets":{"media":{},"assets":{"public":true}}}}}`

func TestStorageEndToEnd(t *testing.T) {
	r := newRig(t)
	r.apply(shopManifest)

	env, err := r.m.Env(r.ctx, r.p, "shop", "web")
	if err != nil {
		t.Fatal(err)
	}
	if env["S3_BUCKET_MEDIA"] != "shop-media" || env["S3_BUCKET_ASSETS"] != "shop-assets" || env["S3_REGION"] != Region ||
		env["S3_PUBLIC_ENDPOINT"] != "https://s3.tiffin.localhost:8443" || env["TIFFIN_FILES_URL"] != "https://files.tiffin.localhost:8443/shop" ||
		env["AWS_ACCESS_KEY_ID"] != env["S3_ACCESS_KEY_ID"] || env["S3_BUCKET"] != "" || env["TIFFIN_PUBLIC_BUCKETS"] != "assets" {
		t.Fatalf("env: %v", env)
	}
	c := Creds{env["S3_ACCESS_KEY_ID"], env["S3_SECRET_ACCESS_KEY"]}
	// Upload with the app's credentials through the front (as an app would).
	if res, body := r.s3("PUT", r.front, "/shop-assets/img/logo.3f9a2c1d.png", []byte("PNGDATA"), &c, map[string]string{"Content-Type": "image/png"}); res.StatusCode != 200 {
		t.Fatalf("put public: %d %s", res.StatusCode, body)
	}
	if res, body := r.s3("PUT", r.front, "/shop-media/private/doc.txt", []byte("secret doc"), &c, nil); res.StatusCode != 200 {
		t.Fatalf("put private: %d %s", res.StatusCode, body)
	}
	// The key cannot touch another project's buckets or create buckets.
	if res, _ := r.s3("PUT", r.front, "/other-bucket", nil, &c, nil); res.StatusCode != 403 {
		t.Fatalf("create bucket with project key: %d", res.StatusCode)
	}

	// Public file through files.<domain>, with caching headers.
	res, body := r.s3("GET", "files.tiffin.localhost:8443", "/shop/assets/img/logo.3f9a2c1d.png", nil, nil, nil)
	if res.StatusCode != 200 || body != "PNGDATA" || res.Header.Get("Content-Type") != "image/png" ||
		!strings.Contains(res.Header.Get("Cache-Control"), "immutable") || !strings.Contains(res.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("public file: %d %q %v", res.StatusCode, body, res.Header)
	}
	if res, _ := r.s3("GET", "files.tiffin.localhost", "/shop/assets/img/logo.3f9a2c1d.png", nil, nil, map[string]string{"Range": "bytes=0-2"}); res.StatusCode != 206 {
		t.Fatalf("range: %d", res.StatusCode)
	}
	if res, _ := r.s3("GET", "files.tiffin.localhost", "/shop/assets/nope.png", nil, nil, nil); res.StatusCode != 404 {
		t.Fatalf("missing public file: %d", res.StatusCode)
	}
	// Private bucket: not on files.<domain>, not anonymously on S3.
	if res, _ := r.s3("GET", "files.tiffin.localhost", "/shop/media/private/doc.txt", nil, nil, nil); res.StatusCode != 403 {
		t.Fatalf("private via files: %d", res.StatusCode)
	}
	if res, _ := r.s3("GET", "s3.tiffin.localhost:8443", "/shop-media/private/doc.txt", nil, nil, nil); res.StatusCode != 403 {
		t.Fatalf("private anonymous: %d", res.StatusCode)
	}
	// Presigned GET on the public endpoint verifies through the front (Host kept).
	pre, err := r.m.presign(r.ctx, r.p, "shop", "media", "private/doc.txt", "GET", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(pre.URL)
	if u.Host != "s3.tiffin.localhost:8443" {
		t.Fatalf("presign host: %s", pre.URL)
	}
	if res, body := r.s3("GET", u.Host, u.RequestURI(), nil, nil, nil); res.StatusCode != 200 || body != "secret doc" {
		t.Fatalf("presigned get: %d %s", res.StatusCode, body)
	}
	tampered := strings.Replace(u.RequestURI(), "doc.txt", "doc2.txt", 1)
	if res, _ := r.s3("GET", u.Host, tampered, nil, nil, nil); res.StatusCode != 403 {
		t.Fatalf("tampered presign: %d", res.StatusCode)
	}
	// Presigned PUT.
	pre, err = r.m.presign(r.ctx, r.p, "shop", "media", "uploads/a.bin", "PUT", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, _ = url.Parse(pre.URL)
	if res, body := r.s3("PUT", u.Host, u.RequestURI(), []byte("uploaded"), nil, nil); res.StatusCode != 200 {
		t.Fatalf("presigned put: %d %s", res.StatusCode, body)
	}

	// API helpers: info, list, upload, audit.
	if _, err := r.m.upload(r.ctx, r.p, "shop", "assets", "hello.txt", "", []byte("hi there")); err != nil {
		t.Fatal(err)
	}
	r.m.tracker().refresh(r.ctx, r.p)
	info, err := r.m.info(r.ctx, r.p, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Buckets) != 2 || info.Buckets[0].Name != "assets" || info.Buckets[0].Objects != 2 || info.Buckets[1].Objects != 2 ||
		info.UsedBytes != int64(len("PNGDATA")+len("hi there")+len("secret doc")+len("uploaded")) || info.QuotaBytes != DefaultQuotaBytes {
		b, _ := json.MarshalIndent(info, "", " ")
		t.Fatalf("info: %s", b)
	}
	gw, _ := r.m.gateway(r.p)
	lst, err := gw.list(r.ctx, "shop-media", "", "/", "", 10)
	if err != nil || len(lst.Prefixes) != 2 || len(lst.Objects) != 0 {
		t.Fatalf("list: %+v %v", lst, err)
	}
	rep, err := r.m.audit(r.ctx, r.p, "shop")
	if err != nil || !rep.OK || rep.Verified != 4 {
		t.Fatalf("audit: %+v %v", rep, err)
	}
	if err := os.WriteFile(filepath.Join(dataDir(r.p.DataRoot), "shop-media", "private", "doc.txt"), []byte("bitrot doc"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rep, _ := r.m.audit(r.ctx, r.p, "shop"); rep.OK || len(rep.Problems) != 1 || rep.Problems[0].Issue != "checksum_mismatch" {
		t.Fatalf("audit after corruption: %+v", rep)
	}

	// Quota: uploads past it are refused at the front and the API.
	if err := r.p.DB.KVPut(r.ctx, kvNS, "quota/shop", []byte("40")); err != nil {
		t.Fatal(err)
	}
	if res, body := r.s3("PUT", r.front, "/shop-media/big.bin", bytes.Repeat([]byte("x"), 100), &c, nil); res.StatusCode != 403 || !strings.Contains(body, "QuotaExceeded") {
		t.Fatalf("quota at front: %d %s", res.StatusCode, body)
	}
	if _, err := r.m.upload(r.ctx, r.p, "shop", "media", "big.bin", "", bytes.Repeat([]byte("x"), 100)); err == nil {
		t.Fatal("quota at API: upload accepted")
	}
	_ = r.p.DB.KVDelete(r.ctx, kvNS, "quota/shop")

	// Delete the private bucket: it goes to the trash; adding it back restores it.
	r.apply(`{"project":"shop","services":{"storage":{"buckets":{"assets":{"public":true}}}}}`)
	if _, err := os.Stat(filepath.Join(dataDir(r.p.DataRoot), "shop-media")); !os.IsNotExist(err) {
		t.Fatalf("bucket dir still there: %v", err)
	}
	tr, _ := trashEntries(r.ctx, r.p)
	if len(tr) != 1 || tr[0].Bucket != "media" || tr[0].Objects != 2 {
		t.Fatalf("trash: %+v", tr)
	}
	if res, _ := r.s3("GET", r.front, "/shop-media/uploads/a.bin", nil, &c, nil); res.StatusCode != 404 {
		t.Fatalf("deleted bucket still serves: %d", res.StatusCode)
	}
	r.apply(shopManifest)
	if res, body := r.s3("GET", r.front, "/shop-media/uploads/a.bin", nil, &c, nil); res.StatusCode != 200 || body != "uploaded" {
		t.Fatalf("restored object: %d %s", res.StatusCode, body)
	}
	if tr, _ := trashEntries(r.ctx, r.p); len(tr) != 0 {
		t.Fatalf("trash after restore: %+v", tr)
	}

	// Purge: entries past retention are deleted for good.
	r.apply(`{"project":"shop","services":{"storage":{"buckets":{"assets":{"public":true}}}}}`)
	if n, err := r.m.purgeTrash(r.ctx, r.p, time.Now().Add(time.Second)); err != nil || n != 1 {
		t.Fatalf("purge: %d %v", n, err)
	}

	// Making a bucket private removes anonymous access.
	r.apply(`{"project":"shop","services":{"storage":{"buckets":{"assets":{}}}}}`)
	if res, _ := r.s3("GET", "files.tiffin.localhost", "/shop/assets/hello.txt", nil, nil, nil); res.StatusCode != 403 {
		t.Fatalf("now-private via files: %d", res.StatusCode)
	}
	if res, _ := r.s3("GET", "s3.tiffin.localhost", "/shop-assets/hello.txt", nil, nil, nil); res.StatusCode != 403 {
		t.Fatalf("now-private anonymous: %d", res.StatusCode)
	}
	if res, body := r.s3("GET", r.front, "/shop-assets/hello.txt", nil, &c, nil); res.StatusCode != 200 || body != "hi there" {
		t.Fatalf("owner after private: %d %s", res.StatusCode, body)
	}

	// Removing storage revokes the key.
	r.apply(`{"project":"shop"}`)
	if res, _ := r.s3("GET", r.front, "/shop-assets/hello.txt", nil, &c, nil); res.StatusCode != 403 {
		t.Fatalf("key after storage removed: %d", res.StatusCode)
	}
	if env, _ := r.m.Env(r.ctx, r.p, "shop", "web"); env != nil {
		t.Fatalf("env without storage: %v", env)
	}
}

func TestBucketNameCollision(t *testing.T) {
	r := newRig(t)
	r.apply(`{"project":"shop","services":{"storage":{"buckets":{"a-b":{}}}}}`)
	mf, _ := manifest.Parse([]byte(`{"project":"shop-a","services":{"storage":{"buckets":{"b":{}}}}}`))
	desired, _ := change.Resources(mf)
	plan, _ := r.p.Engine.Plan(r.ctx, "shop-a", desired)
	if _, err := r.p.Engine.Apply(r.ctx, change.ApplyRequest{Plan: plan, Confirm: plan.Hash}); err != nil {
		t.Fatal(err)
	}
	if err := r.m.Reconcile(r.ctx, r.p, "shop-a", "service/storage", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	err := r.m.Reconcile(r.ctx, r.p, "shop-a", "bucket/b", json.RawMessage(`{"public":false}`))
	if err == nil || !strings.Contains(err.Error(), "already belongs") {
		t.Fatalf("collision: %v", err)
	}
	long := strings.Repeat("x", 40)
	if err := r.m.Reconcile(r.ctx, r.p, strings.Repeat("p", 30), "bucket/"+long, json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "63") {
		t.Fatalf("long name: %v", err)
	}
}
