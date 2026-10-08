// Package cloud is ShipTiffin's control plane worker: it creates managed
// boxes in a customer's own Hetzner Cloud project, keeps their
// <name>.shiptiffin.app records, resizes them and, when asked, deletes
// them. The website (site/) is the other half: accounts, billing, the
// pages, the box check-in and the monitor. The worker runs in a project of
// its own (cmd/tiffin-cloud/tiffin.config.ts) and reaches the website's
// database, whose cloud_* tables this package owns.
package cloud

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
)

// Customers' Hetzner tokens are sealed to the worker's X25519 public key: the
// website (which holds only CLOUD_SEAL_PUBLIC) can seal but never open; only
// the worker, which holds CLOUD_SEAL_KEY, opens. Each value gets a fresh
// ephemeral key. The additional data binds a value to its row
// ("hetzner:<box id>"), so a value copied to another row does not open.
//
// Format: "v2." + base64url(ephemeral public key, 32 bytes) + "." +
// base64url(nonce(12) | AES-256-GCM(key, value, aad)), where
// key = HKDF-SHA256(X25519(ephemeral, recipient), salt = ephemeral public |
// recipient public, info = "shiptiffin seal v2"). The website seals the same
// way (site/lib/cloud/seal.ts).

const (
	sealPrefix = "v2."
	sealInfo   = "shiptiffin seal v2"
)

var b64 = base64.RawURLEncoding

func decodeKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		raw, err = base64.RawURLEncoding.DecodeString(s)
	}
	return raw, err
}

// ParseSealKey reads CLOUD_SEAL_KEY: the base64 of a 32-byte X25519 private key.
func ParseSealKey(s string) (*ecdh.PrivateKey, error) {
	raw, err := decodeKey(s)
	if err != nil || len(raw) != 32 {
		return nil, errors.New("CLOUD_SEAL_KEY: want the base64 of a 32-byte X25519 private key (tiffin-cloud keygen makes one)")
	}
	return ecdh.X25519().NewPrivateKey(raw)
}

// ParseSealPublic reads CLOUD_SEAL_PUBLIC (the website's half).
func ParseSealPublic(s string) (*ecdh.PublicKey, error) {
	raw, err := decodeKey(s)
	if err != nil || len(raw) != 32 {
		return nil, errors.New("CLOUD_SEAL_PUBLIC: want the base64 of a 32-byte X25519 public key")
	}
	return ecdh.X25519().NewPublicKey(raw)
}

// KeyText is a key as the secrets hold it.
func KeyText(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func sealKey(shared, ephPub, recipPub []byte) ([]byte, error) {
	salt := append(append([]byte{}, ephPub...), recipPub...)
	return hkdf.Key(sha256.New, shared, salt, sealInfo, 32)
}

func gcm(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal encrypts value to pub for the row aad names.
func Seal(pub *ecdh.PublicKey, value []byte, aad string) (string, error) {
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	shared, err := eph.ECDH(pub)
	if err != nil {
		return "", err
	}
	key, err := sealKey(shared, eph.PublicKey().Bytes(), pub.Bytes())
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
	return sealPrefix + b64.EncodeToString(eph.PublicKey().Bytes()) + "." + b64.EncodeToString(body), nil
}

// ErrSealed means a sealed value could not be opened: another key, another
// row, or changed.
var ErrSealed = errors.New("sealed value does not open")

// Open decrypts a Seal'ed value with the worker's private key.
func Open(priv *ecdh.PrivateKey, sealed, aad string) ([]byte, error) {
	rest, ok := strings.CutPrefix(sealed, sealPrefix)
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
	key, err := sealKey(shared, ephRaw, priv.PublicKey().Bytes())
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

// TokenAAD binds a sealed Hetzner token to its box.
func TokenAAD(boxID string) string { return "hetzner:" + boxID }

// Fingerprint is what stays of a token once it is forgotten: enough to
// recognise it ("is this the key I made on Tuesday?"), useless to call with.
func Fingerprint(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])[:12]
}

// AddrMAC authenticates the addresses the worker itself recorded for a box,
// so DNS only ever points a name at an address the worker learned from
// Hetzner for that box and installation, never at one written to the
// database by anything else (the website can write the database; it can't
// forge this). The key is derived from the worker's private key.
func AddrMAC(priv *ecdh.PrivateKey, boxID, name, ipv4, ipv6 string, gen int64) string {
	k, _ := hkdf.Key(sha256.New, priv.Bytes(), nil, "shiptiffin dns addresses", 32)
	m := hmac.New(sha256.New, k)
	clear(k)
	m.Write([]byte(strings.Join([]string{boxID, name, ipv4, ipv6, strconv.FormatInt(gen, 10)}, "\n")))
	return hex.EncodeToString(m.Sum(nil))
}

// AddrOK checks AddrMAC in constant time.
func AddrOK(priv *ecdh.PrivateKey, mac, boxID, name, ipv4, ipv6 string, gen int64) bool {
	return mac != "" && hmac.Equal([]byte(mac), []byte(AddrMAC(priv, boxID, name, ipv4, ipv6, gen)))
}
