// Package release builds, signs and checks Tiffin releases: the manifest a
// release publishes for each channel, its minisign signature, and the
// rules a box applies before it installs one (signed by a trusted key, the
// channel it follows, never older, never from below minVersion, the
// artifact's sha256).
//
// Signatures use minisign's format (Ed25519 over the BLAKE2b-512 hash of
// the file, with a signed "trusted comment"), so anyone can check a release
// with the stock tool:
//
//	minisign -Vm manifest.json -P <public key>
//
// The format carries a key ID, so a box trusts a list of keys and a new one
// can sign releases while boxes still trust the old one (rotation).
package release

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// PublicKey is a minisign public key.
type PublicKey struct {
	ID  [8]byte
	Key ed25519.PublicKey
}

// SecretKey is an unencrypted minisign secret key (minisign -G -W makes
// one; so does GenerateKey).
type SecretKey struct {
	ID  [8]byte
	Key ed25519.PrivateKey
}

// KeyID is the key's ID as minisign prints it.
func (k PublicKey) KeyID() string { return keyID(k.ID) }

func keyID(id [8]byte) string {
	b := id
	for i, j := 0, 7; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return strings.ToUpper(hex.EncodeToString(b[:]))
}

// String is the key's one-line base64 form (what -P takes).
func (k PublicKey) String() string {
	return base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), k.ID[:]...), k.Key...))
}

// File is the key in minisign's .pub file format.
func (k PublicKey) File() string {
	return "untrusted comment: minisign public key " + k.KeyID() + "\n" + k.String() + "\n"
}

// Public returns the secret key's public half.
func (k SecretKey) Public() PublicKey {
	return PublicKey{ID: k.ID, Key: k.Key.Public().(ed25519.PublicKey)}
}

// GenerateKey makes a new key pair with a random key ID.
func GenerateKey() (SecretKey, error) {
	var sk SecretKey
	if _, err := rand.Read(sk.ID[:]); err != nil {
		return sk, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	sk.Key = priv
	return sk, err
}

// File is the key in minisign's unencrypted secret key format.
func (k SecretKey) File() string {
	var b bytes.Buffer
	b.WriteString("Ed")
	b.Write([]byte{0, 0}) // no key derivation: not encrypted
	b.WriteString("B2")
	b.Write(make([]byte, 32+8+8)) // salt, opslimit, memlimit (unused)
	b.Write(k.ID[:])
	b.Write(k.Key)
	sum := secretChecksum(k)
	b.Write(sum[:])
	return "untrusted comment: minisign secret key (unencrypted)\n" + base64.StdEncoding.EncodeToString(b.Bytes()) + "\n"
}

func secretChecksum(k SecretKey) [32]byte {
	return blake2b.Sum256(append(append([]byte("Ed"), k.ID[:]...), k.Key...))
}

// lastLine returns the base64 line of a key file, or s itself when it is
// one line.
func lastLine(s string) string {
	var out string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "untrusted comment:") {
			out = l
		}
	}
	return out
}

// ParsePublicKey reads a public key: the base64 line or a whole .pub file.
func ParsePublicKey(s string) (PublicKey, error) {
	var k PublicKey
	raw, err := base64.StdEncoding.DecodeString(lastLine(s))
	if err != nil || len(raw) != 42 || string(raw[:2]) != "Ed" {
		return k, errors.New("not a minisign public key")
	}
	copy(k.ID[:], raw[2:10])
	k.Key = ed25519.PublicKey(raw[10:])
	return k, nil
}

// ParseSecretKey reads an unencrypted minisign secret key file. An
// encrypted one (minisign -G without -W) is refused: decrypt it for CI by
// generating the key with -W, and keep it in the repository's secrets.
func ParseSecretKey(s string) (SecretKey, error) {
	var k SecretKey
	raw, err := base64.StdEncoding.DecodeString(lastLine(s))
	if err != nil || len(raw) != 158 || string(raw[:2]) != "Ed" || string(raw[4:6]) != "B2" {
		return k, errors.New("not a minisign secret key")
	}
	if raw[2] != 0 || raw[3] != 0 {
		return k, errors.New("the minisign secret key is password-protected; make one without a password (minisign -G -W, or tiffin-release keygen)")
	}
	sk := raw[54:]
	copy(k.ID[:], sk[:8])
	k.Key = ed25519.PrivateKey(append([]byte(nil), sk[8:72]...))
	// minisign writes the checksum; aead.dev/minisign leaves it zero.
	if sum := secretChecksum(k); !bytes.Equal(sum[:], sk[72:104]) && !bytes.Equal(sk[72:104], make([]byte, 32)) {
		return k, errors.New("the minisign secret key's checksum does not match")
	}
	return k, nil
}

// Sign signs msg (prehashed, minisign's default) with trusted, a comment
// the signature covers too. It returns the .minisig file.
func Sign(k SecretKey, msg []byte, trusted string) ([]byte, error) {
	if strings.ContainsAny(trusted, "\r\n") {
		return nil, errors.New("the trusted comment must be one line")
	}
	h := blake2b.Sum512(msg)
	sig := ed25519.Sign(k.Key, h[:])
	global := ed25519.Sign(k.Key, append(append([]byte(nil), sig...), trusted...))
	line := append(append([]byte("ED"), k.ID[:]...), sig...)
	return []byte("untrusted comment: signature from tiffin-release\n" +
		base64.StdEncoding.EncodeToString(line) + "\n" +
		"trusted comment: " + trusted + "\n" +
		base64.StdEncoding.EncodeToString(global) + "\n"), nil
}

// Verify checks a .minisig file for msg against the trusted keys and
// returns the signed trusted comment and the key that signed.
func Verify(keys []PublicKey, msg, sigFile []byte) (trusted string, key PublicKey, err error) {
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(string(sigFile), "\r\n", "\n"), "\n"), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "untrusted comment:") {
		return "", key, errors.New("not a minisign signature")
	}
	sig, err := base64.StdEncoding.DecodeString(lines[1])
	if err != nil || len(sig) != 74 {
		return "", key, errors.New("not a minisign signature")
	}
	trusted, ok := strings.CutPrefix(lines[2], "trusted comment: ")
	if !ok {
		return "", key, errors.New("the signature has no trusted comment")
	}
	global, err := base64.StdEncoding.DecodeString(lines[3])
	if err != nil || len(global) != ed25519.SignatureSize {
		return "", key, errors.New("not a minisign signature")
	}
	var id [8]byte
	copy(id[:], sig[2:10])
	found := false
	for _, k := range keys {
		if k.ID == id {
			key, found = k, true
			break
		}
	}
	if !found {
		return "", key, fmt.Errorf("signed by key %s, which this build does not trust", keyID(id))
	}
	data := msg
	switch string(sig[:2]) {
	case "ED":
		h := blake2b.Sum512(msg)
		data = h[:]
	case "Ed": // legacy, unhashed
	default:
		return "", key, errors.New("unknown signature algorithm")
	}
	if !ed25519.Verify(key.Key, data, sig[10:]) {
		return "", key, errors.New("the signature does not match: the file was changed after it was signed")
	}
	if !ed25519.Verify(key.Key, append(append([]byte(nil), sig[10:]...), trusted...), global) {
		return "", key, errors.New("the signature's trusted comment was changed")
	}
	return trusted, key, nil
}
