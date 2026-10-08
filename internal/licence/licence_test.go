package licence

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// The vector the website's TypeScript check (site/lib/cloud/licence.test.ts)
// verifies too: a fixed seed and payload give one exact token.
const (
	vectorSeed  = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
	vectorToken = "tl1.eyJ2IjoxLCJib3giOiJib3hfdGVzdDEiLCJuYW1lIjoic2hvcCIsImRvbWFpbiI6InNob3Auc2hpcHRpZmZpbi5hcHAiLCJpYXQiOjE3OTE0MjQwMDB9.2mTyCQpuWP66FZq723Ql6u5Nar4pWzJCCUGgf80JW2ZMDXw288-RGYaRLVqcFbAO72mOeh1pJPOpaL-QPz26CQ"
)

func TestSignVerify(t *testing.T) {
	key, err := KeyFromSeed(vectorSeed)
	if err != nil {
		t.Fatal(err)
	}
	pub := key.Public().(ed25519.PublicKey)
	tok, err := Sign(key, Licence{BoxID: "box_test1", Name: "shop", Domain: "shop.shiptiffin.app", Issued: 1791424000})
	if err != nil {
		t.Fatal(err)
	}
	if tok != vectorToken {
		t.Fatalf("token changed (update site/lib/cloud/licence.test.ts too):\n%s", tok)
	}
	l, err := Verify(pub, tok)
	if err != nil || l.BoxID != "box_test1" || l.Name != "shop" || l.Domain != "shop.shiptiffin.app" || l.Issued != 1791424000 {
		t.Fatalf("verify: %+v %v", l, err)
	}
	pk, err := ParsePublicKey(PublicKeyText(pub))
	if err != nil || !pk.Equal(pub) {
		t.Fatalf("public key round trip: %v", err)
	}
}

func TestVerifyRefuses(t *testing.T) {
	key, _ := KeyFromSeed(vectorSeed)
	pub := key.Public().(ed25519.PublicKey)
	other := ed25519.NewKeyFromSeed(make([]byte, 32))
	forged, _ := Sign(other, Licence{BoxID: "box_test1", Name: "shop"})

	// A payload swapped under a valid signature.
	parts := strings.Split(vectorToken, ".")
	swapped := parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"box":"box_other","name":"x"}`)) + "." + parts[2]

	for name, tok := range map[string]string{
		"empty":       "",
		"no prefix":   strings.TrimPrefix(vectorToken, "tl1."),
		"other key":   forged,
		"swapped":     swapped,
		"cut":         vectorToken[:len(vectorToken)-3],
		"no sig":      parts[0] + "." + parts[1],
		"garbage sig": parts[0] + "." + parts[1] + ".!!!",
	} {
		if _, err := Verify(pub, tok); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
	if _, err := Sign(key, Licence{Name: "x"}); err == nil {
		t.Fatal("a licence without a box id must not be signed")
	}
	if _, err := KeyFromSeed("c2hvcnQ="); err == nil {
		t.Fatal("a short seed must be refused")
	}
}
