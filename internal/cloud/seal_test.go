package cloud

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// kekVector and sealedVector come from the website's seal.ts (see
// site/lib/cloud/seal.test.ts): the worker must open what the website seals.
const (
	kekVector    = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	sealedVector = "v1.dr7uacXjrv-vq84KX8StUWK2w64X2qnN8YeCQcplZQ_Qd1UyQRK0QPYw_PeXTuBBAjqHiTABZaJMNt9E.J-UYlxU4l6euVV7XZUWFuGY38yArcZTXjcDtZuxI5nXtbcgcr_Z3uL6X79rjwlo"
)

func TestSealOpen(t *testing.T) {
	kek, err := ParseKEK(kekVector)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Seal(kek, []byte("hcloud-token-123"), TokenAAD("box_a"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s, "hcloud-token-123") || !strings.HasPrefix(s, "v1.") {
		t.Fatalf("sealed: %s", s)
	}
	if got, err := Open(kek, s, TokenAAD("box_a")); err != nil || string(got) != "hcloud-token-123" {
		t.Fatalf("open: %q %v", got, err)
	}
	s2, _ := Seal(kek, []byte("hcloud-token-123"), TokenAAD("box_a"))
	if s2 == s {
		t.Fatal("two seals of the same value must differ (fresh data key and nonce)")
	}
	other := bytes.Repeat([]byte{7}, 32)
	for name, try := range map[string]func() error{
		"another row":   func() error { _, err := Open(kek, s, TokenAAD("box_b")); return err },
		"owner vs key":  func() error { _, err := Open(kek, s, OwnerAAD("box_a")); return err },
		"another KEK":   func() error { _, err := Open(other, s, TokenAAD("box_a")); return err },
		"changed":       func() error { _, err := Open(kek, s[:len(s)-4]+"AAAA", TokenAAD("box_a")); return err },
		"not sealed":    func() error { _, err := Open(kek, "hcloud-token-123", TokenAAD("box_a")); return err },
		"empty":         func() error { _, err := Open(kek, "", TokenAAD("box_a")); return err },
		"missing parts": func() error { _, err := Open(kek, "v1.abc", TokenAAD("box_a")); return err },
	} {
		if err := try(); !errors.Is(err, ErrSealed) {
			t.Errorf("%s: want ErrSealed, got %v", name, err)
		}
	}
	if _, err := ParseKEK("c2hvcnQ="); err == nil {
		t.Fatal("a short KEK must be refused")
	}
	if fp := Fingerprint(" hcloud-token-123\n"); len(fp) != 12 || fp != Fingerprint("hcloud-token-123") {
		t.Fatalf("fingerprint %q", fp)
	}
}

func TestOpenWhatTheWebsiteSealed(t *testing.T) {
	kek, _ := ParseKEK(kekVector)
	got, err := Open(kek, sealedVector, TokenAAD("box_vector"))
	if err != nil || string(got) != "hcloud-vector-token" {
		t.Fatalf("open the website's seal: %q %v", got, err)
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
	// A failed call is recorded too.
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
