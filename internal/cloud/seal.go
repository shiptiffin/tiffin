// Package cloud is ShipTiffin's control plane worker: it creates managed
// boxes in a customer's own Hetzner Cloud project, keeps their
// <name>.shiptiffin.app records, resizes them and, when asked, deletes
// them. The website (site/) is the other half: accounts, billing, the
// pages, the box check-in and the monitor. They share one Postgres
// database; this package owns its schema.
package cloud

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Envelope encryption for customers' Hetzner tokens (and a new box's owner
// token, until the customer signs in). Each value gets its own random data
// key; the data key is wrapped with the KEK, which lives in the project's
// secrets (CLOUD_KEK), never in the database. The additional data binds a
// sealed value to its row ("hetzner:<box id>"), so a value copied to another
// row does not open.
//
// Format: "v1." + base64url(nonce(12) | AES-GCM(KEK, data key)) + "." +
// base64url(nonce(12) | AES-GCM(data key, value, aad)).
// The website seals the same way (site/lib/cloud/seal.ts).

const sealPrefix = "v1."

var b64 = base64.RawURLEncoding

// ParseKEK reads the KEK: the base64 of 32 random bytes.
func ParseKEK(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		raw, err = base64.RawURLEncoding.DecodeString(s)
	}
	if err != nil || len(raw) != 32 {
		return nil, errors.New("CLOUD_KEK: want the base64 of 32 random bytes (openssl rand -base64 32)")
	}
	return raw, nil
}

func gcmSeal(key, plain, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return g.Seal(nonce, nonce, plain, aad), nil
}

func gcmOpen(key, sealed, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(sealed) < g.NonceSize()+g.Overhead() {
		return nil, errors.New("too short")
	}
	return g.Open(nil, sealed[:g.NonceSize()], sealed[g.NonceSize():], aad)
}

// Seal encrypts value for the row aad names.
func Seal(kek []byte, value []byte, aad string) (string, error) {
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		return "", err
	}
	wrapped, err := gcmSeal(kek, dek, []byte("dek"))
	if err != nil {
		return "", err
	}
	body, err := gcmSeal(dek, value, []byte(aad))
	if err != nil {
		return "", err
	}
	clear(dek)
	return sealPrefix + b64.EncodeToString(wrapped) + "." + b64.EncodeToString(body), nil
}

// ErrSealed means a sealed value could not be opened: another KEK, another
// row, or changed.
var ErrSealed = errors.New("sealed value does not open")

// Open decrypts a Seal'ed value.
func Open(kek []byte, sealed, aad string) ([]byte, error) {
	rest, ok := strings.CutPrefix(sealed, sealPrefix)
	if !ok {
		return nil, ErrSealed
	}
	w, b, ok := strings.Cut(rest, ".")
	if !ok {
		return nil, ErrSealed
	}
	wrapped, err1 := b64.DecodeString(w)
	body, err2 := b64.DecodeString(b)
	if err1 != nil || err2 != nil {
		return nil, ErrSealed
	}
	dek, err := gcmOpen(kek, wrapped, []byte("dek"))
	if err != nil {
		return nil, fmt.Errorf("%w (data key)", ErrSealed)
	}
	defer clear(dek)
	v, err := gcmOpen(dek, body, []byte(aad))
	if err != nil {
		return nil, ErrSealed
	}
	return v, nil
}

// TokenAAD binds a sealed Hetzner token to its box.
func TokenAAD(boxID string) string { return "hetzner:" + boxID }

// OwnerAAD binds a sealed box owner token to its box.
func OwnerAAD(boxID string) string { return "owner:" + boxID }

// Fingerprint is what stays of a token once it is forgotten: enough to
// recognise it ("is this the key I made on Tuesday?"), useless to call with.
func Fingerprint(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])[:12]
}
