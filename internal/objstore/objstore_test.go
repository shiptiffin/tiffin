package objstore

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestS3DeleteMany checks the DeleteObjects requests: at most 1000 keys
// each, XML-escaped, with the Content-MD5 that S3 and R2 require, and a
// 200 answer that lists keys it could not delete is a failure.
func TestS3DeleteMany(t *testing.T) {
	var batches []int
	var seen []string
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sum := md5.Sum(body)
		if r.Method != http.MethodPost || r.URL.RawQuery != "delete=" || r.Header.Get("Content-MD5") != base64.StdEncoding.EncodeToString(sum[:]) {
			http.Error(w, "<Error><Code>InvalidRequest</Code></Error>", http.StatusBadRequest)
			return
		}
		var in struct {
			Objects []struct{ Key string } `xml:"Object"`
		}
		if err := xml.Unmarshal(body, &in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		batches = append(batches, len(in.Objects))
		for _, o := range in.Objects {
			seen = append(seen, o.Key)
		}
		if fail {
			fmt.Fprint(w, `<DeleteResult><Error><Key>p/1</Key><Code>AccessDenied</Code><Message>Access Denied</Message></Error></DeleteResult>`)
			return
		}
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></DeleteResult>`)
	}))
	defer srv.Close()
	cl, _ := New(Config{Endpoint: srv.URL, Region: "auto", Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s", HTTPClient: srv.Client()})
	keys := []string{"p/a&b<c> d"}
	for i := range 2000 {
		keys = append(keys, fmt.Sprintf("p/%d", i))
	}
	ctx := context.Background()
	if err := cl.DeleteMany(ctx, keys); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(batches) != "[1000 1000 1]" || len(seen) != len(keys) || seen[0] != keys[0] {
		t.Fatalf("batches %v, %d keys, first %q", batches, len(seen), seen[0])
	}
	if err := cl.DeleteMany(ctx, nil); err != nil || len(batches) != 3 {
		t.Fatalf("no keys: %v, %d requests", err, len(batches))
	}
	fail = true
	if err := cl.DeleteMany(ctx, keys[:5]); err == nil || !strings.Contains(err.Error(), "AccessDenied") {
		t.Fatalf("a key that was not deleted: %v", err)
	}
}

// A store's answers are bounded: an object bigger than its kind can be is
// refused (by its Content-Length, or as it streams in), and a body that
// stops arriving fails after BodyStall instead of holding the caller.
func TestS3BoundedBodies(t *testing.T) {
	old := BodyStall
	BodyStall = 300 * time.Millisecond
	t.Cleanup(func() { BodyStall = old })
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/b/fits":
			w.Write(bytes.Repeat([]byte("x"), 1000))
		case "/b/declared":
			w.Header().Set("Content-Length", strconv.Itoa(10<<20))
			w.Write(bytes.Repeat([]byte("x"), 10<<20))
		case "/b/streamed": // no Content-Length
			for range 100 {
				w.Write(bytes.Repeat([]byte("x"), 100<<10))
				w.(http.Flusher).Flush()
			}
		case "/b/stalls":
			w.Write([]byte("partial"))
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-r.Context().Done():
			}
		case "/b":
			// A listing that never ends.
			w.Write([]byte("<ListBucketResult>"))
			for range 2000 {
				w.Write([]byte("<Contents><Key>" + strings.Repeat("k", 10<<10) + "</Key></Contents>"))
			}
		}
	}))
	defer srv.Close()
	defer close(release)
	cl, _ := New(Config{Endpoint: srv.URL, Region: "auto", Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s", HTTPClient: srv.Client()})
	ctx := context.Background()
	if got, err := cl.Get(ctx, "fits", 1000); err != nil || len(got) != 1000 {
		t.Fatalf("an object of exactly the limit: %d %v", len(got), err)
	}
	if _, err := cl.Get(ctx, "fits", 999); err == nil {
		t.Fatal("an object one byte over the limit was read")
	}
	if _, err := cl.Get(ctx, "declared", 1<<20); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("a declared oversize object: %v", err)
	}
	if _, err := cl.Get(ctx, "streamed", 1<<20); !errors.Is(err, ErrBodyTooBig) {
		t.Fatalf("a streamed oversize object: %v", err)
	}
	start := time.Now()
	if _, err := cl.Get(ctx, "stalls", 1<<20); err == nil || !strings.Contains(err.Error(), "sent nothing") {
		t.Fatalf("a stalled body: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("a stalled body held the call for %s", d)
	}
	if err := cl.List(ctx, "", func(string, int64) error { return nil }); err == nil {
		t.Fatal("an endless listing was read")
	}
}
