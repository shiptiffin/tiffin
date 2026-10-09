//go:build e2e

// Command fakegithub serves a stand-in GitHub (internal/mod/runtime/ghapp/ghfake)
// over HTTPS for the GitHub e2e test: the REST API at /api, the website
// (manifest flow, install page, OAuth), git smart HTTP for its repositories
// and signed webhook deliveries to -hook. The test drives it through
// /_fake/* (add a repository, commit, push, open or close a pull request,
// read what the box did).
//
//	fakegithub -addr 127.0.0.1:9443 -dir /var/lib/fakegithub -hook http://127.0.0.1:7070/v1/github/webhook -ca /tmp/fakegithub-ca.pem
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/shiptiffin/tiffin/internal/mod/runtime/ghapp/ghfake"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9443", "listen address")
	dir := flag.String("dir", "/var/lib/fakegithub", "where repositories live")
	hook := flag.String("hook", "", "deliver webhooks here instead of the app's hook URL")
	caOut := flag.String("ca", "/tmp/fakegithub-ca.pem", "write the server certificate (its own CA) here")
	flag.Parse()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	host, _, _ := net.SplitHostPort(*addr)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "fake GitHub"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(7 * 24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP(host)}, DNSNames: []string{"github.test"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*caOut, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		log.Fatal(err)
	}
	f, err := ghfake.New(*dir)
	if err != nil {
		log.Fatal(err)
	}
	f.URL = "https://" + *addr
	f.HookURL = *hook
	srv := &http.Server{Addr: *addr, Handler: f, TLSConfig: &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}}
	log.Printf("fake GitHub at %s (hooks to %s)", f.URL, *hook)
	log.Fatal(srv.ListenAndServeTLS("", ""))
}
