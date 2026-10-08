package cloud

import (
	"crypto/ecdh"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The worker's test key (X25519 private key bytes 0..31) and a value the
// website sealed to its public half with site/lib/cloud/seal.ts
// (PRINT_VECTOR=1 bun test lib/cloud/crypto.test.ts): the worker must open
// what the website seals.
const (
	keyVector = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
	pubVector = "j0DFrbaPJWJK5bIU6nZ6bslNgp09e14a0bpvPiE4KF8="
	tsSealed  = "v2.5OoEEq6FrbMf_7JEYwy4buyPcz7sWni-cWjjHrSDlkU.1bacuFHzvBsPso2lvH9wBoODwxgNKdBDyPU0wOxGM29slNKI6pT2w8-qxmaEbGw"
)

func TestSealOpen(t *testing.T) {
	priv, err := ParseSealKey(keyVector)
	if err != nil {
		t.Fatal(err)
	}
	if KeyText(priv.PublicKey().Bytes()) != pubVector {
		t.Fatal("the public half changed (update site/lib/cloud/crypto.test.ts)")
	}
	pub, err := ParseSealPublic(pubVector)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Seal(pub, []byte("hcloud-token-123"), TokenAAD("box_a"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s, "hcloud-token-123") || !strings.HasPrefix(s, "v2.") {
		t.Fatalf("sealed: %s", s)
	}
	if got, err := Open(priv, s, TokenAAD("box_a")); err != nil || string(got) != "hcloud-token-123" {
		t.Fatalf("open: %q %v", got, err)
	}
	s2, _ := Seal(pub, []byte("hcloud-token-123"), TokenAAD("box_a"))
	if s2 == s {
		t.Fatal("two seals of the same value must differ (fresh ephemeral key and nonce)")
	}
	other, _ := ecdh.X25519().GenerateKey(rand.Reader)
	for name, try := range map[string]func() error{
		"another row":   func() error { _, err := Open(priv, s, TokenAAD("box_b")); return err },
		"another key":   func() error { _, err := Open(other, s, TokenAAD("box_a")); return err },
		"changed":       func() error { _, err := Open(priv, s[:len(s)-4]+"AAAA", TokenAAD("box_a")); return err },
		"not sealed":    func() error { _, err := Open(priv, "hcloud-token-123", TokenAAD("box_a")); return err },
		"empty":         func() error { _, err := Open(priv, "", TokenAAD("box_a")); return err },
		"missing parts": func() error { _, err := Open(priv, "v2.abc", TokenAAD("box_a")); return err },
		"old format":    func() error { _, err := Open(priv, "v1.abc.def", TokenAAD("box_a")); return err },
	} {
		if err := try(); !errors.Is(err, ErrSealed) {
			t.Errorf("%s: want ErrSealed, got %v", name, err)
		}
	}
	if _, err := ParseSealKey("c2hvcnQ="); err == nil {
		t.Fatal("a short key must be refused")
	}
	if fp := Fingerprint(" hcloud-token-123\n"); len(fp) != 12 || fp != Fingerprint("hcloud-token-123") {
		t.Fatalf("fingerprint %q", fp)
	}
}

func TestOpenWhatTheWebsiteSealed(t *testing.T) {
	priv, _ := ParseSealKey(keyVector)
	got, err := Open(priv, tsSealed, TokenAAD("box_vector"))
	if err != nil || string(got) != "hcloud-vector-token" {
		t.Fatalf("open the website's seal: %q %v", got, err)
	}
}

func TestAddrMAC(t *testing.T) {
	priv, _ := ParseSealKey(keyVector)
	other, _ := ecdh.X25519().GenerateKey(rand.Reader)
	m := AddrMAC(priv, "box_1", "shop", "203.0.113.5", "2001:db8::1", 2)
	if !AddrOK(priv, m, "box_1", "shop", "203.0.113.5", "2001:db8::1", 2) {
		t.Fatal("own MAC refused")
	}
	for name, ok := range map[string]bool{
		"another IP":         AddrOK(priv, m, "box_1", "shop", "198.51.100.9", "2001:db8::1", 2),
		"another name":       AddrOK(priv, m, "box_1", "cafe", "203.0.113.5", "2001:db8::1", 2),
		"another box":        AddrOK(priv, m, "box_2", "shop", "203.0.113.5", "2001:db8::1", 2),
		"another generation": AddrOK(priv, m, "box_1", "shop", "203.0.113.5", "2001:db8::1", 3),
		"another key":        AddrOK(other, m, "box_1", "shop", "203.0.113.5", "2001:db8::1", 2),
		"empty":              AddrOK(priv, "", "box_1", "shop", "203.0.113.5", "2001:db8::1", 2),
	} {
		if ok {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestValidName(t *testing.T) {
	for _, ok := range []string{"shop", "my-shop", "a1b", "acme-2026", "pineapple"} {
		if err := ValidName(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "ab", "Shop", "1shop", "shop-", "-shop", "my--shop", "xn--shop", "sh_op", "shop.app",
		"www", "admin", "dashboard", "shiptiffin", "paypal-login", "my-shiptiffin", "google", strings.Repeat("a", 31)} {
		if err := ValidName(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestRecorder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201) }))
	defer srv.Close()
	var calls []Call
	c := &http.Client{Transport: &Recorder{Record: func(c Call) { calls = append(calls, c) }}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/servers?label_selector=tiffin%3Dbox", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if len(calls) != 1 || calls[0].Method != "POST" || calls[0].Path != "/v1/servers?label_selector=tiffin%3Dbox" || calls[0].Status != 201 {
		t.Fatalf("calls: %+v", calls)
	}
	srv.Close()
	if _, err := c.Get(srv.URL + "/v1/pricing"); err == nil {
		t.Fatal("want an error")
	}
	if len(calls) != 2 || calls[1].Status != 0 || calls[1].Error == "" {
		t.Fatalf("failed call: %+v", calls[1])
	}
	for _, c := range calls {
		if strings.Contains(c.Path+c.Error, "secret-token") {
			t.Fatal("the token must never be recorded")
		}
	}
}
