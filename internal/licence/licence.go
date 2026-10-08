// Package licence signs and checks the licence of a managed box: a small
// ed25519-signed token the control plane (shiptiffin.com) issues when it
// installs a box in a customer's own cloud account. The box sends it with
// its daily check-in; it proves which box is talking, nothing more. It holds
// no secret and grants no access to the box.
//
// Format: "tl1." + base64url(JSON payload) + "." + base64url(signature),
// where the signature covers "tl1." + the encoded payload.
package licence

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const prefix = "tl1."

// Licence is what a token says.
type Licence struct {
	V      int    `json:"v"`
	BoxID  string `json:"box"`
	Name   string `json:"name"`
	Domain string `json:"domain"`
	// Issued is when the control plane made it (Unix seconds).
	Issued int64 `json:"iat"`
	// Gen is the installation generation: each setup of the box gets the
	// next one, and the control plane only believes the current one, so a
	// licence from an earlier install (a restored backup, a copy) is revoked.
	Gen int64 `json:"gen,omitempty"`
}

var b64 = base64.RawURLEncoding

// Sign makes a token for l.
func Sign(key ed25519.PrivateKey, l Licence) (string, error) {
	if len(key) != ed25519.PrivateKeySize {
		return "", errors.New("licence: bad signing key")
	}
	if l.BoxID == "" || l.Name == "" {
		return "", errors.New("licence: box id and name are required")
	}
	l.V = 1
	if l.Issued == 0 {
		l.Issued = time.Now().Unix()
	}
	raw, err := json.Marshal(l)
	if err != nil {
		return "", err
	}
	body := prefix + b64.EncodeToString(raw)
	return body + "." + b64.EncodeToString(ed25519.Sign(key, []byte(body))), nil
}

// ErrInvalid is any token that does not check out.
var ErrInvalid = errors.New("licence is not valid")

// Verify checks a token against pub and returns what it says.
func Verify(pub ed25519.PublicKey, token string) (Licence, error) {
	var l Licence
	if len(pub) != ed25519.PublicKeySize {
		return l, fmt.Errorf("%w: bad public key", ErrInvalid)
	}
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, prefix) {
		return l, ErrInvalid
	}
	i := strings.LastIndexByte(token, '.')
	if i <= len(prefix) {
		return l, ErrInvalid
	}
	body, sigText := token[:i], token[i+1:]
	sig, err := b64.DecodeString(sigText)
	if err != nil || !ed25519.Verify(pub, []byte(body), sig) {
		return l, ErrInvalid
	}
	raw, err := b64.DecodeString(body[len(prefix):])
	if err != nil || json.Unmarshal(raw, &l) != nil || l.V != 1 || l.BoxID == "" {
		return l, ErrInvalid
	}
	return l, nil
}

// KeyFromSeed reads a signing key from its base64 32-byte seed (the
// CLOUD_LICENCE_KEY secret).
func KeyFromSeed(seed string) (ed25519.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(seed))
	if err != nil {
		raw, err = base64.RawURLEncoding.DecodeString(strings.TrimSpace(seed))
	}
	if err != nil || len(raw) != ed25519.SeedSize {
		return nil, errors.New("licence key: want the base64 of a 32-byte ed25519 seed")
	}
	return ed25519.NewKeyFromSeed(raw), nil
}

// PublicKeyText is a public key as base64 (what a box keeps to check its licence).
func PublicKeyText(pub ed25519.PublicKey) string { return base64.StdEncoding.EncodeToString(pub) }

// ParsePublicKey reads PublicKeyText.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("licence public key: want the base64 of a 32-byte ed25519 key")
	}
	return ed25519.PublicKey(raw), nil
}
