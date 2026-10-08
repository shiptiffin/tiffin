package main

import (
	"bytes"
	"crypto/ed25519"
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/cloud"
	"github.com/btahir/tiffin/internal/licence"
)

// keygen's halves belong together: what the website seals the worker opens,
// and what the worker signs the website verifies.
func TestKeygen(t *testing.T) {
	var b bytes.Buffer
	keygen(&b)
	v := map[string]string{}
	for _, line := range strings.Split(b.String(), "\n") {
		if k, val, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(k, "#") {
			v[k] = val
		}
	}
	priv, err := cloud.ParseSealKey(v["CLOUD_SEAL_KEY"])
	if err != nil {
		t.Fatal(err)
	}
	pub, err := cloud.ParseSealPublic(v["CLOUD_SEAL_PUBLIC"])
	if err != nil {
		t.Fatal(err)
	}
	s, _ := cloud.Seal(pub, []byte("x"), "a")
	if got, err := cloud.Open(priv, s, "a"); err != nil || string(got) != "x" {
		t.Fatal("seal pair does not match")
	}
	key, err := licence.KeyFromSeed(v["CLOUD_LICENCE_KEY"])
	if err != nil {
		t.Fatal(err)
	}
	lpub, err := licence.ParsePublicKey(v["CLOUD_LICENCE_PUBLIC"])
	if err != nil || !lpub.Equal(key.Public().(ed25519.PublicKey)) {
		t.Fatal("licence pair does not match")
	}
}
