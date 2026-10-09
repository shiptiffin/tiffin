// Package sealbox seals a value to an X25519 public key: anyone with the
// public key can seal, only the private key's holder can open. Each value
// gets a fresh ephemeral key; the additional data binds a value to where it
// belongs ("hetzner:<box id>"), so a value copied elsewhere does not open.
//
// Format: "v2." + base64url(ephemeral public key, 32 bytes) + "." +
// base64url(nonce(12) | AES-256-GCM(key, value, aad)), where
// key = HKDF-SHA256(X25519(ephemeral, recipient), salt = ephemeral public |
// recipient public, info = "shiptiffin seal v2"). The website seals the same
// way (site/lib/cloud/seal.ts).
//
// The control plane seals customers' Hetzner tokens to the provisioner
// (internal/cloud), and a managed box's off-site credentials to the box
// (internal/mod/managed).
package sealbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

const (
	prefix = "v2."
	info   = "shiptiffin seal v2"
)

var b64 = base64.RawURLEncoding

// ErrSealed means a sealed value could not be opened: another key, another
// row, or changed.
var ErrSealed = errors.New("sealed value does not open")

func derive(shared, ephPub, recipPub []byte) ([]byte, error) {
	salt := append(append([]byte{}, ephPub...), recipPub...)
	return hkdf.Key(sha256.New, shared, salt, info, 32)
}

func gcm(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal encrypts value to pub for aad.
func Seal(pub *ecdh.PublicKey, value []byte, aad string) (string, error) {
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	shared, err := eph.ECDH(pub)
	if err != nil {
		return "", err
	}
	key, err := derive(shared, eph.PublicKey().Bytes(), pub.Bytes())
	clear(shared)
	if err != nil {
		return "", err
	}
	defer clear(key)
	g, err := gcm(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	body := g.Seal(nonce, nonce, value, []byte(aad))
	return prefix + b64.EncodeToString(eph.PublicKey().Bytes()) + "." + b64.EncodeToString(body), nil
}

// Open decrypts a Seal'ed value with the recipient's private key.
func Open(priv *ecdh.PrivateKey, sealed, aad string) ([]byte, error) {
	rest, ok := strings.CutPrefix(sealed, prefix)
	if !ok {
		return nil, ErrSealed
	}
	e, b, ok := strings.Cut(rest, ".")
	if !ok {
		return nil, ErrSealed
	}
	ephRaw, err1 := b64.DecodeString(e)
	body, err2 := b64.DecodeString(b)
	if err1 != nil || err2 != nil || len(ephRaw) != 32 {
		return nil, ErrSealed
	}
	eph, err := ecdh.X25519().NewPublicKey(ephRaw)
	if err != nil {
		return nil, ErrSealed
	}
	shared, err := priv.ECDH(eph)
	if err != nil {
		return nil, ErrSealed
	}
	key, err := derive(shared, ephRaw, priv.PublicKey().Bytes())
	clear(shared)
	if err != nil {
		return nil, ErrSealed
	}
	defer clear(key)
	g, err := gcm(key)
	if err != nil || len(body) < g.NonceSize()+g.Overhead() {
		return nil, ErrSealed
	}
	v, err := g.Open(nil, body[:g.NonceSize()], body[g.NonceSize():], []byte(aad))
	if err != nil {
		return nil, ErrSealed
	}
	return v, nil
}

// DecodeKey reads a 32-byte key as the secrets and check-ins hold it
// (standard base64, or base64url).
func DecodeKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		raw, err = base64.RawURLEncoding.DecodeString(s)
	}
	if err == nil && len(raw) != 32 {
		err = errors.New("want 32 bytes")
	}
	return raw, err
}

// ParsePublic reads the base64 of a 32-byte X25519 public key.
func ParsePublic(s string) (*ecdh.PublicKey, error) {
	raw, err := DecodeKey(s)
	if err != nil {
		return nil, errors.New("want the base64 of a 32-byte X25519 public key")
	}
	return ecdh.X25519().NewPublicKey(raw)
}

// KeyText is a key as the secrets and check-ins hold it (standard base64).
func KeyText(b []byte) string { return base64.StdEncoding.EncodeToString(b) }
