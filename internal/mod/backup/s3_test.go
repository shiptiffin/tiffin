package backup

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/ids"
)

// These tests drive a real versitygw over TLS with a private CA. Point
// TIFFIN_TEST_VERSITYGW at a versitygw binary for this OS (the release
// tarballs have darwin builds); without it they are skipped.

// testCA writes a CA and a server certificate for 127.0.0.1 into dir and
// returns the CA's PEM and the cert and key paths.
func testCA(t *testing.T, dir string) (caPEM, certPath, keyPath string) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "tiffin test CA"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	srvKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	srv := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "127.0.0.1"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(24 * time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	caCert, _ := x509.ParseCertificate(caDER)
	srvDER, err := x509.CreateCertificate(rand.Reader, srv, caCert, &srvKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(srvKey)
	certPath, keyPath = filepath.Join(dir, "srv.crt"), filepath.Join(dir, "srv.key")
	_ = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srvDER}), 0o600)
	_ = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})), certPath, keyPath
}

// versity starts versitygw over TLS and returns a destination for a fresh bucket.
func versity(t *testing.T) (*OffsiteConfig, string) {
	t.Helper()
	bin := os.Getenv("TIFFIN_TEST_VERSITYGW")
	if bin == "" {
		t.Skip("TIFFIN_TEST_VERSITYGW not set")
	}
	dir := t.TempDir()
	caPEM, cert, key := testCA(t, dir)
	data := filepath.Join(dir, "data")
	_ = os.MkdirAll(data, 0o755)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	access, secret := "TFNTESTROOT", "test-secret-"+ids.New("x")
	cmd := exec.Command(bin, "--port", addr, "--cert", cert, "--key", key, "--region", "us-east-1", "--quiet", "posix", data)
	cmd.Env = append(os.Environ(), "ROOT_ACCESS_KEY="+access, "ROOT_SECRET_KEY="+secret)
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("versitygw:\n%s", logs.String())
		}
	})
	c := &OffsiteConfig{Endpoint: "https://" + addr, Region: "us-east-1", Bucket: "offsite", Prefix: "boxes/a", AccessKeyID: access, URIStyle: "path", CACert: caPEM}
	cl, err := newS3(c, secret)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; ; i++ {
		err := cl.CreateBucket(ctx)
		if err == nil {
			break
		}
		if i > 50 {
			t.Fatalf("create bucket: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return c, secret
}

// storeRoundTrip drives a real store under prefix: the probe, odd keys,
// missing objects, listing past 1000 keys, and sets with dedup.
func storeRoundTrip(t *testing.T, cl *s3Client, prefix string) {
	t.Helper()
	ctx := context.Background()
	steps, err := probe(ctx, cl, prefix)
	if err != nil || len(steps) != 3 {
		fatalR2(t, "probe: %v %+v", err, steps)
	}
	for _, key := range []string{prefix + "/plain", prefix + "/odd name+=&é.txt"} {
		if err := cl.Put(ctx, key, []byte("v:"+key)); err != nil {
			fatalR2(t, "put %q: %v", key, err)
		}
		if got, err := cl.Get(ctx, key, 1<<20); err != nil || string(got) != "v:"+key {
			fatalR2(t, "get %q: %q %v", key, got, err)
		}
	}
	if _, err := cl.Get(ctx, prefix+"/nope", 1<<20); !errors.Is(err, errNoObject) {
		fatalR2(t, "missing object: %v", err)
	}
	if err := cl.Delete(ctx, prefix+"/nope"); err != nil {
		fatalR2(t, "deleting a missing object: %v", err)
	}
	// Listing pages through more than 1000 keys.
	many := make(chan int)
	errs := make(chan error, 16)
	for range 16 {
		go func() {
			var first error
			for i := range many {
				if err := cl.Put(ctx, fmt.Sprintf("%s/many/%04d", prefix, i), []byte{byte(i)}); err != nil && first == nil {
					first = err
				}
			}
			errs <- first
		}()
	}
	for i := range 1010 {
		many <- i
	}
	close(many)
	for range 16 {
		if err := <-errs; err != nil {
			fatalR2(t, "%v", err)
		}
	}
	n := 0
	if err := cl.List(ctx, prefix+"/many/", func(k string, size int64) error {
		if size != 1 || k != fmt.Sprintf("%s/many/%04d", prefix, n) {
			return fmt.Errorf("listed %s (%d) at %d", k, size, n)
		}
		n++
		return nil
	}); err != nil || n != 1010 {
		fatalR2(t, "list: %d %v", n, err)
	}

	// Sets through the real store: round trip and dedup.
	k, _ := newKeys("box-a")
	v, err := newVault(cl, prefix, k)
	if err != nil {
		fatalR2(t, "%v", err)
	}
	src := t.TempDir()
	writeTree(t, src)
	known := map[string]bool{}
	rec := &offsiteSet{Backup: Backup{ID: ids.New("bk"), Status: "ok"}}
	entries, err := v.putSet(ctx, rec, src, known)
	if err != nil {
		fatalR2(t, "%v", err)
	}
	rec2 := &offsiteSet{Backup: Backup{ID: ids.New("bk"), Status: "ok"}}
	if _, err := v.putSet(ctx, rec2, src, known); err != nil || rec2.Upload.NewChunks != 0 {
		fatalR2(t, "second upload: %+v %v", rec2.Upload, err)
	}
	listed, err := v.chunkIDs(ctx)
	if err != nil || len(listed) != rec.Upload.NewChunks {
		fatalR2(t, "chunks listed %d, uploaded %d: %v", len(listed), rec.Upload.NewChunks, err)
	}
	dst := filepath.Join(t.TempDir(), "restore")
	if _, err := v.getTree(ctx, entries, dst); err != nil {
		fatalR2(t, "%v", err)
	}
	sameTree(t, src, dst)
	ids2, _ := v.setIDs(ctx)
	if len(ids2) != 2 {
		fatalR2(t, "sets: %v", ids2)
	}
}

func TestS3Versitygw(t *testing.T) {
	c, secret := versity(t)
	ctx := context.Background()
	cl, _ := newS3(c, secret)
	storeRoundTrip(t, cl, c.Prefix)

	// Wrong secret, and no CA: refused.
	bad, _ := newS3(c, "wrong")
	if err := bad.Put(ctx, c.Prefix+"/x", []byte("x")); err == nil || !strings.Contains(err.Error(), "403") {
		fatalR2(t, "wrong secret: %v", err)
	}
	noCA := *c
	noCA.CACert = ""
	plain, _ := newS3(&noCA, secret)
	if _, err := plain.Get(ctx, c.Prefix+"/plain", 1<<20); err == nil || !strings.Contains(err.Error(), "certificate") {
		fatalR2(t, "without the CA: %v", err)
	}
	emptied(t, cl, c.Prefix)
}

// emptied deletes everything under prefix and checks nothing is left.
func emptied(t *testing.T, cl *s3Client, prefix string) {
	t.Helper()
	ctx := context.Background()
	if n, err := cl.DeletePrefix(ctx, prefix+"/"); err != nil || n == 0 {
		errorR2(t, "emptying %s: %d deleted, %v", prefix, n, err)
	}
	left := 0
	_ = cl.List(ctx, prefix+"/", func(string, int64) error { left++; return nil })
	if left > 0 {
		errorR2(t, "%d objects left under %s", left, prefix)
	}
}

// r2 is a fresh prefix in a real Cloudflare R2 bucket (TIFFIN_TEST_R2=1 with
// R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY, R2_BUCKET, CLOUDFLARE_ACCOUNT_ID),
// emptied when the test ends.
func r2(t *testing.T) (*OffsiteConfig, string) {
	t.Helper()
	if os.Getenv("TIFFIN_TEST_R2") != "1" {
		t.Skip("TIFFIN_TEST_R2 not set")
	}
	c, err := normalize(OffsiteInput{Endpoint: os.Getenv("CLOUDFLARE_ACCOUNT_ID") + ".r2.cloudflarestorage.com", Bucket: os.Getenv("R2_BUCKET"),
		Prefix: "test/" + strings.ToLower(ids.New("s3")), AccessKeyID: os.Getenv("R2_ACCESS_KEY_ID")}, nil)
	if err != nil {
		fatalR2(t, "%v", err)
	}
	secret := os.Getenv("R2_SECRET_ACCESS_KEY")
	cl, _ := newS3(c, secret)
	t.Cleanup(func() { emptied(t, cl, c.Prefix) })
	return c, secret
}

func TestS3R2(t *testing.T) {
	c, secret := r2(t)
	if c.Region != "auto" {
		fatalR2(t, "an R2 endpoint signs for region auto, got %q", c.Region)
	}
	cl, _ := newS3(c, secret)
	storeRoundTrip(t, cl, c.Prefix)
	bad, _ := newS3(c, "wrong")
	if err := bad.Put(context.Background(), c.Prefix+"/x", []byte("x")); err == nil || !strings.Contains(err.Error(), "403") {
		fatalR2(t, "wrong secret: %v", err)
	}
}

// fatalR2 and errorR2 fail the test with the R2 account, keys and bucket
// masked: errors may hold the endpoint's URL.
func fatalR2(t *testing.T, format string, a ...any) {
	t.Helper()
	t.Fatal(hideR2(fmt.Sprintf(format, a...)))
}

func errorR2(t *testing.T, format string, a ...any) {
	t.Helper()
	t.Error(hideR2(fmt.Sprintf(format, a...)))
}

func hideR2(s string) string {
	for _, k := range []string{"R2_SECRET_ACCESS_KEY", "R2_ACCESS_KEY_ID", "CLOUDFLARE_ACCOUNT_ID", "R2_BUCKET"} {
		if v := os.Getenv(k); v != "" {
			s = strings.ReplaceAll(s, v, "<"+k+">")
		}
	}
	return s
}
