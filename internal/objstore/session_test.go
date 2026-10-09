package objstore_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/objstore"
	"github.com/btahir/tiffin/internal/objstore/objstoretest"
)

// Temporary credentials (R2's, limited to a folder): the session token is
// sent and signed with every request; without it, past the expiry or
// outside the folder the bucket refuses.
func TestTemporaryCredentials(t *testing.T) {
	f := objstoretest.New("backups")
	defer f.Close()
	now := time.Now()
	f.Allow(objstoretest.Cred{AccessKeyID: "tmp", SecretAccessKey: "tmp-secret", SessionToken: "tok-123", Expires: now.Add(time.Hour), Prefixes: []string{"box_a/"}})
	cfg := objstore.Config{Endpoint: f.URL, Region: "auto", Bucket: "backups", AccessKeyID: "tmp", SecretAccessKey: "tmp-secret", SessionToken: "tok-123", CACert: f.CAPEM()}
	cl, err := objstore.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := range 3 {
		if err := cl.Put(ctx, fmt.Sprintf("box_a/odd key %d+~", i), []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := cl.Get(ctx, "box_a/odd key 0+~", 10); err != nil || string(got) != "x" {
		t.Fatalf("get: %q %v", got, err)
	}
	n := 0
	if err := cl.List(ctx, "box_a/", func(string, int64) error { n++; return nil }); err != nil || n != 3 {
		t.Fatalf("list: %d %v", n, err)
	}
	if _, err := cl.Get(ctx, "box_a/none", 10); !errors.Is(err, objstore.ErrNoObject) {
		t.Fatalf("missing object: %v", err)
	}

	refused := func(name string, err error, code string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), code) {
			t.Errorf("%s: want %s, got %v", name, code, err)
		}
	}
	refused("another box's folder", cl.Put(ctx, "box_b/x", []byte("x")), "AccessDenied")
	refused("listing the bucket", cl.List(ctx, "", func(string, int64) error { return nil }), "AccessDenied")
	noTok := cfg
	noTok.SessionToken = ""
	bare, _ := objstore.New(noTok)
	refused("no session token", bare.Put(ctx, "box_a/y", []byte("y")), "InvalidToken")
	wrong := cfg
	wrong.SecretAccessKey = "nope"
	bad, _ := objstore.New(wrong)
	refused("wrong secret", bad.Put(ctx, "box_a/y", []byte("y")), "SignatureDoesNotMatch")
	f.SetNow(func() time.Time { return now.Add(2 * time.Hour) })
	refused("expired", cl.Put(ctx, "box_a/y", []byte("y")), "ExpiredToken")
	f.SetNow(time.Now)

	if n, err := cl.DeletePrefix(ctx, "box_a/"); err != nil || n != 3 || len(f.Keys("")) != 0 {
		t.Fatalf("delete prefix: %d %v, left %v", n, err, f.Keys(""))
	}
}
