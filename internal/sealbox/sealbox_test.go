package sealbox

import (
	"crypto/ecdh"
	"crypto/rand"
	"errors"
	"testing"
)

func TestSealOpen(t *testing.T) {
	priv, _ := ecdh.X25519().GenerateKey(rand.Reader)
	pub, err := ParsePublic(KeyText(priv.PublicKey().Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	s, err := Seal(pub, []byte("value"), "offsite:box_a")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Open(priv, s, "offsite:box_a"); err != nil || string(got) != "value" {
		t.Fatalf("open: %q %v", got, err)
	}
	if _, err := Open(priv, s, "offsite:box_b"); !errors.Is(err, ErrSealed) {
		t.Fatalf("another row: %v", err)
	}
	if _, err := ParsePublic("c2hvcnQ="); err == nil {
		t.Fatal("a short key was accepted")
	}
}
