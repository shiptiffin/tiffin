// Package cloud is ShipTiffin's control plane worker: it creates managed
// boxes in a customer's own Hetzner Cloud project, keeps their
// <name>.shiptiffin.app records, resizes them and, when asked, deletes
// them. The website (site/) is the other half: accounts, billing, the
// pages, the box check-in and the monitor. The worker runs in a project of
// its own (cmd/tiffin-provisioner/tiffin.config.ts) and reaches the website's
// database, whose cloud_* tables this package owns.
package cloud

import (
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"github.com/shiptiffin/tiffin/internal/sealbox"
)

// Customers' Hetzner tokens are sealed to the worker's X25519 public key
// (sealbox): the website (which holds only CLOUD_SEAL_PUBLIC) can seal but
// never open; only the worker, which holds CLOUD_SEAL_KEY, opens. The
// additional data binds a value to its row ("hetzner:<box id>").

// ParseSealKey reads CLOUD_SEAL_KEY: the base64 of a 32-byte X25519 private key.
func ParseSealKey(s string) (*ecdh.PrivateKey, error) {
	raw, err := sealbox.DecodeKey(s)
	if err != nil {
		return nil, errors.New("CLOUD_SEAL_KEY: want the base64 of a 32-byte X25519 private key (tiffin-provisioner keygen makes one)")
	}
	return ecdh.X25519().NewPrivateKey(raw)
}

// ParseSealPublic reads CLOUD_SEAL_PUBLIC (the website's half).
func ParseSealPublic(s string) (*ecdh.PublicKey, error) {
	pub, err := sealbox.ParsePublic(s)
	if err != nil {
		return nil, errors.New("CLOUD_SEAL_PUBLIC: want the base64 of a 32-byte X25519 public key")
	}
	return pub, nil
}

// KeyText is a key as the secrets hold it.
func KeyText(b []byte) string { return sealbox.KeyText(b) }

// Seal encrypts value to pub for the row aad names.
func Seal(pub *ecdh.PublicKey, value []byte, aad string) (string, error) {
	return sealbox.Seal(pub, value, aad)
}

// ErrSealed means a sealed value could not be opened: another key, another
// row, or changed.
var ErrSealed = sealbox.ErrSealed

// Open decrypts a Seal'ed value with the worker's private key.
func Open(priv *ecdh.PrivateKey, sealed, aad string) ([]byte, error) {
	return sealbox.Open(priv, sealed, aad)
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
