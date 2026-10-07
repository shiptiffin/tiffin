package storage

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Test vectors from the AWS S3 SigV4 documentation ("Authenticating
// Requests: Using Query Parameters" and "Examples: Signature Calculations").
var (
	awsCreds = Creds{AccessKey: "AKIAIOSFODNN7EXAMPLE", Secret: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}
	awsTime  = time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
)

func TestPresignAWSVector(t *testing.T) {
	// Virtual-hosted request for /test.txt == path-style with an empty bucket segment
	// stripped: build it by hand through the same canonicalisation.
	u, err := Presign("GET", "https://examplebucket.s3.amazonaws.com", "test.txt", "", awsCreds, "us-east-1", 86400*time.Second, awsTime)
	if err != nil {
		t.Fatal(err)
	}
	want := "aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404"
	if !strings.Contains(u, "X-Amz-Signature="+want) {
		t.Fatalf("presigned URL %s\nwant signature %s", u, want)
	}
	if !strings.HasPrefix(u, "https://examplebucket.s3.amazonaws.com/test.txt?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request") {
		t.Fatalf("url shape: %s", u)
	}
}

func TestSignRequestAWSVector(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	req.Header.Set("Range", "bytes=0-9")
	signRequest(req, awsCreds, "us-east-1", emptySHA256, awsTime)
	want := "Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if a := req.Header.Get("Authorization"); !strings.HasSuffix(a, want) || !strings.Contains(a, "SignedHeaders=host;range;x-amz-content-sha256;x-amz-date") {
		t.Fatalf("authorization %q", a)
	}
}

func TestEscapeAndAccessKey(t *testing.T) {
	if got := objectPath("b", "a b/ü+x.png"); got != "/b/a%20b/%C3%BC%2Bx.png" {
		t.Fatalf("objectPath: %s", got)
	}
	req, _ := http.NewRequest("GET", "http://x/b/k?X-Amz-Credential=AK1%2F20250101%2Fus-east-1%2Fs3%2Faws4_request", nil)
	if k := accessKeyOf(req); k != "AK1" {
		t.Fatalf("query key %q", k)
	}
	req, _ = http.NewRequest("GET", "http://x/b/k", nil)
	signRequest(req, Creds{"AK2", "s"}, "us-east-1", emptySHA256, time.Now())
	if k := accessKeyOf(req); k != "AK2" {
		t.Fatalf("header key %q", k)
	}
	req, _ = http.NewRequest("GET", "http://x/b/k", nil)
	if k := accessKeyOf(req); k != "" {
		t.Fatalf("anonymous key %q", k)
	}
}

// The front checks signatures before it acts as root (checkComplete): the
// header form signRequest makes, presigned URLs from PresignWith and from
// @shiptiffin/sdk (the same URL its test pins), and nothing else.
func TestVerifySigV4(t *testing.T) {
	c := Creds{"TFNTESTKEY", "secret"}
	at := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	incoming := func(method, rawURL string) *http.Request {
		req := httptest.NewRequest(method, rawURL, nil)
		return req
	}
	// From packages/sdk/test/storage.test.ts.
	sdk := "https://s3.tiffin.localhost:18443/shop-media/up/%C3%BC%20a%2Bb.png?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=TFNTESTKEY%2F20130524%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Date=20130524T000000Z&X-Amz-Expires=600&X-Amz-Signature=f63432929d2c2cd09bd366b63c556dd7b4c6025626c553f5b0e55c31e91f595d&X-Amz-SignedHeaders=content-length%3Bcontent-type%3Bhost&partNumber=2&uploadId=abc-123&x-tiffin-max-size=1000"
	req := httptest.NewRequest("PUT", sdk, strings.NewReader(strings.Repeat("x", 500)))
	req.Header.Set("Content-Type", "image/png")
	if !verifySigV4(req, c, at.Add(time.Minute)) {
		t.Fatal("the SDK's presigned URL")
	}
	if verifySigV4(req, c, at.Add(11*time.Minute)) {
		t.Fatal("an expired presigned URL")
	}
	if verifySigV4(req, Creds{"TFNTESTKEY", "other"}, at.Add(time.Minute)) {
		t.Fatal("another secret")
	}
	tampered := httptest.NewRequest("PUT", strings.Replace(sdk, "x-tiffin-max-size=1000", "x-tiffin-max-size=1", 1), strings.NewReader(strings.Repeat("x", 500)))
	tampered.Header.Set("Content-Type", "image/png")
	if verifySigV4(tampered, c, at.Add(time.Minute)) {
		t.Fatal("a changed size cap")
	}
	// PresignWith's own URLs, with a subresource.
	u, err := PresignWith("POST", "http://s3.tiffin.localhost", "shop--media", "a b/ü.png", url.Values{"uploadId": {"U1"}}, nil, c, Region, time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !verifySigV4(incoming("POST", u), c, time.Now()) {
		t.Fatalf("PresignWith URL %s", u)
	}
	// Header signatures, body hash and all.
	now := time.Now()
	body := []byte("<CompleteMultipartUpload/>")
	hreq := httptest.NewRequest("POST", "http://s3.tiffin.localhost/shop--media/v%20x.png?uploadId=U1", strings.NewReader(string(body)))
	signRequest(hreq, c, Region, sha256Hex(body), now)
	if !verifySigV4(hreq, c, now) {
		t.Fatal("a header-signed request")
	}
	if verifySigV4(hreq, c, now.Add(20*time.Minute)) {
		t.Fatal("a header signature 20 minutes old")
	}
	if verifySigV4(hreq, Creds{"TFNOTHER", "secret"}, now) {
		t.Fatal("another access key")
	}
	hreq.Host = "s3.elsewhere"
	if verifySigV4(hreq, c, now) {
		t.Fatal("a different host")
	}
	if verifySigV4(incoming("POST", "http://s3.tiffin.localhost/shop--media/v.png?uploadId=U1"), c, now) {
		t.Fatal("an unsigned request")
	}
}
