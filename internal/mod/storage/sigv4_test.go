package storage

import (
	"net/http"
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
