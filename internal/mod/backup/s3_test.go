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
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/ids"
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
		resp, err := cl.do(ctx, http.MethodPut, "", nil, nil) // CreateBucket
		if err == nil {
			resp.Body.Close()
			break
		}
		if i > 50 {
			t.Fatalf("create bucket: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return c, secret
}

func TestS3Versitygw(t *testing.T) {
	c, secret := versity(t)
	ctx := context.Background()
	cl, _ := newS3(c, secret)

	steps, err := probe(ctx, cl, c.Prefix)
	if err != nil || len(steps) != 3 {
		t.Fatalf("probe: %v %+v", err, steps)
	}
	for _, key := range []string{"boxes/a/plain", "boxes/a/odd name+=&é.txt"} {
		if err := cl.Put(ctx, key, []byte("v:"+key)); err != nil {
			t.Fatalf("put %q: %v", key, err)
		}
		if got, err := cl.Get(ctx, key); err != nil || string(got) != "v:"+key {
			t.Fatalf("get %q: %q %v", key, got, err)
		}
	}
	if _, err := cl.Get(ctx, "boxes/a/nope"); !errors.Is(err, errNoObject) {
		t.Fatalf("missing object: %v", err)
	}
	if err := cl.Delete(ctx, "boxes/a/nope"); err != nil {
		t.Fatalf("deleting a missing object: %v", err)
	}
	// Listing pages through more than 1000 keys.
	for i := range 1010 {
		if err := cl.Put(ctx, fmt.Sprintf("boxes/a/many/%04d", i), []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	if err := cl.List(ctx, "boxes/a/many/", func(k string, size int64) error {
		if size != 1 || k != fmt.Sprintf("boxes/a/many/%04d", n) {
			return fmt.Errorf("listed %s (%d) at %d", k, size, n)
		}
		n++
		return nil
	}); err != nil || n != 1010 {
		t.Fatalf("list: %d %v", n, err)
	}

	// Sets through the real store: round trip and dedup.
	k, _ := newKeys("box-a")
	v, err := newVault(cl, c.Prefix, k)
	if err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	writeTree(t, src)
	known, memo := map[string]bool{}, map[string]fileMemo{}
	rec := &offsiteSet{Backup: Backup{ID: ids.New("bk"), Status: "ok"}}
	entries, err := v.putSet(ctx, rec, src, known, memo)
	if err != nil {
		t.Fatal(err)
	}
	rec2 := &offsiteSet{Backup: Backup{ID: ids.New("bk"), Status: "ok"}}
	if _, err := v.putSet(ctx, rec2, src, known, memo); err != nil || rec2.Upload.NewChunks != 0 {
		t.Fatalf("second upload: %+v %v", rec2.Upload, err)
	}
	listed, err := v.chunkIDs(ctx)
	if err != nil || len(listed) != rec.Upload.NewChunks {
		t.Fatalf("chunks listed %d, uploaded %d: %v", len(listed), rec.Upload.NewChunks, err)
	}
	dst := filepath.Join(t.TempDir(), "restore")
	if _, err := v.getTree(ctx, entries, dst); err != nil {
		t.Fatal(err)
	}
	sameTree(t, src, dst)
	ids2, _ := v.setIDs(ctx)
	if len(ids2) != 2 {
		t.Fatalf("sets: %v", ids2)
	}

	// Wrong secret, and no CA: refused.
	bad, _ := newS3(c, "wrong")
	if err := bad.Put(ctx, "boxes/a/x", []byte("x")); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("wrong secret: %v", err)
	}
	noCA := *c
	noCA.CACert = ""
	plain, _ := newS3(&noCA, secret)
	if _, err := plain.Get(ctx, "boxes/a/plain"); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("without the CA: %v", err)
	}
}
