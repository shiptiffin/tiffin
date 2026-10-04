//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestStorage is the storage acceptance test, through the CLI on a fresh box:
// apply storage with a private and a public bucket → upload through the S3
// API from inside the box with the app env credentials (curl SigV4) → the
// public object is served at files.<domain> over HTTPS with caching; the
// private one is 403 without a signature and 200 with a presigned URL →
// quotas refuse uploads → checksum audit → deleting a bucket moves it to the
// trash and undo brings it back with its files.
func TestStorage(t *testing.T) {
	start := time.Now()
	phase := phaseLogger(t)
	p := time.Now()
	b := newCLIBox(t, "storage", "shop")
	phase("up", p)

	p = time.Now()
	full := `{"project":"shop","services":{"storage":{"buckets":{"media":{},"assets":{"public":true}}}}}`
	b.apply("storage", full)
	b.waitReady("service/storage", "bucket/media", "bucket/assets")
	phase("apply", p)

	// ---- upload through S3 from inside the box with the app's env ----
	p = time.Now()
	env := b.ok("storage", "credentials", "shop")
	for _, k := range []string{"S3_ENDPOINT", "S3_REGION", "S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY", "S3_BUCKET_MEDIA", "S3_BUCKET_ASSETS", "AWS_ACCESS_KEY_ID", "S3_PUBLIC_ENDPOINT", "TIFFIN_FILES_URL"} {
		if env[k] == nil || env[k] == "" {
			t.Fatalf("credentials: %s missing: %v", k, env)
		}
	}
	if env["S3_BUCKET_MEDIA"] != "shop-media" || env["S3_BUCKET_ASSETS"] != "shop-assets" {
		t.Fatalf("bucket env: %v", env)
	}
	curl := fmt.Sprintf("curl -s --aws-sigv4 aws:amz:%s:s3 --user '%s:%s'", env["S3_REGION"], env["S3_ACCESS_KEY_ID"], env["S3_SECRET_ACCESS_KEY"])
	ep := env["S3_ENDPOINT"].(string)
	out := b.inBox(`printf 'hello public world' > /tmp/pub.txt; printf 'top secret' > /tmp/priv.txt
` + curl + ` -o /dev/null -w '%{http_code} ' -H 'Content-Type: text/plain' -T /tmp/pub.txt ` + ep + `/shop-assets/docs/hello.txt
` + curl + ` -o /dev/null -w '%{http_code} ' -T /tmp/priv.txt ` + ep + `/shop-media/secret.txt
` + curl + ` -o /dev/null -w '%{http_code}' -X PUT ` + ep + `/not-mine`)
	if out != "200 200 403" {
		t.Fatalf("uploads (public, private, create bucket with the project key): %q", out)
	}
	if l := b.ok("storage", "objects", "list", "shop", "media"); len(l["objects"].([]any)) != 1 {
		t.Fatalf("objects list: %v", l)
	}
	phase("s3 upload", p)

	// ---- HTTPS from outside: public, private, presigned ----
	p = time.Now()
	c := b.https()
	code, hdr, body := b.get(c, "GET", b.url("files")+"/shop/assets/docs/hello.txt", nil)
	if code != 200 || body != "hello public world" || !strings.HasPrefix(hdr.Get("Cache-Control"), "public") || hdr.Get("Content-Type") != "text/plain" {
		t.Fatalf("public file: %d %q %v", code, body, hdr)
	}
	if code, _, _ := b.get(c, "GET", b.url("files")+"/shop/media/secret.txt", nil); code != 403 {
		t.Fatalf("private via files: %d", code)
	}
	if code, _, _ := b.get(c, "GET", b.url("s3")+"/shop-media/secret.txt", nil); code != 403 {
		t.Fatalf("private anonymous S3: %d", code)
	}
	pre := b.ok("storage", "presign", "shop", "media", "--key", "secret.txt", "--expires-in", "300")
	signed, _ := pre["url"].(string)
	if !strings.HasPrefix(signed, b.url("s3")+"/shop-media/secret.txt?") {
		t.Fatalf("presign: %v", pre)
	}
	if code, _, body := b.get(c, "GET", signed, nil); code != 200 || body != "top secret" {
		t.Fatalf("presigned GET: %d %q", code, body)
	}
	if code, _, _ := b.get(c, "GET", strings.Replace(signed, "secret.txt", "secret2.txt", 1), nil); code != 403 {
		t.Fatalf("tampered presigned URL: %d", code)
	}
	put := b.ok("storage", "presign", "shop", "assets", "--key", "browser/upload.txt", "--method", "PUT")
	if code, _, _ := b.get(c, "PUT", put["url"].(string), strings.NewReader("from a browser")); code != 200 {
		t.Fatalf("presigned PUT: %d", code)
	}
	if code, _, body := b.get(c, "GET", b.url("files")+"/shop/assets/browser/upload.txt", nil); code != 200 || body != "from a browser" {
		t.Fatalf("presigned upload served: %d %q", code, body)
	}
	phase("https", p)

	// ---- quota, API upload, audit ----
	p = time.Now()
	b.ok("storage", "quota", "set", "shop", "--max-bytes", "20")
	out = b.inBox(curl + ` -w ' %{http_code}' -T /tmp/pub.txt ` + ep + `/shop-media/more.txt`)
	if !strings.Contains(out, "QuotaExceeded") || !strings.HasSuffix(out, "403") {
		t.Fatalf("over quota: %q", out)
	}
	b.ok("storage", "quota", "set", "shop", "--max-bytes", "0")
	b.ok("storage", "objects", "put", "shop", "media", "--key", "notes/a.txt", "--text", "written by the API")
	if g := b.ok("storage", "objects", "get", "shop", "media", "--key", "notes/a.txt"); g["text"] != "written by the API" {
		t.Fatalf("objects get: %v", g)
	}
	audit := b.ok("storage", "audit", "shop")
	if audit["ok"] != true || audit["verified"].(float64) != 4 {
		t.Fatalf("audit: %v", audit)
	}
	phase("quota+audit", p)

	// ---- delete a bucket → trash → undo restores it ----
	p = time.Now()
	del := b.apply("storage-drop-media", `{"project":"shop","services":{"storage":{"buckets":{"assets":{"public":true}}}}}`)
	b.waitReady("bucket/assets")
	trash := b.list("storage", "trash", "list")
	if len(trash) != 1 || trash[0]["bucket"] != "media" || trash[0]["objects"].(float64) != 2 {
		t.Fatalf("trash: %v", trash)
	}
	if code, _, _ := b.get(c, "GET", signed, nil); code != 404 {
		t.Fatalf("deleted bucket still serves: %d", code)
	}
	_, undo := b.run("undo", del)
	var u struct {
		Plan struct {
			Hash string `json:"hash"`
		} `json:"plan"`
	}
	_ = json.Unmarshal([]byte(undo), &u)
	if len(u.Plan.Hash) < 12 {
		t.Fatalf("undo plan: %s", undo)
	}
	b.ok("undo", del, "--confirm", u.Plan.Hash[:12], "-m", "bring media back")
	b.waitReady("bucket/media")
	if code, _, body := b.get(c, "GET", signed, nil); code != 200 || body != "top secret" {
		t.Fatalf("after undo: %d %q", code, body)
	}
	if l := b.list("storage", "trash", "list"); len(l) != 0 {
		t.Fatalf("trash after undo: %v", l)
	}
	phase("trash+undo", p)

	st := b.ok("storage", "show", "shop")
	t.Logf("storage show: used %v bytes, quota %v", st["usedBytes"], st["quotaBytes"])
	t.Logf("TOTAL storage acceptance: %s", time.Since(start).Round(time.Second))
}
