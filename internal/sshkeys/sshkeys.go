// Package sshkeys is the owner's SSH keys on a box: which public keys are
// accepted, the one line each becomes, and the file sshd reads them from.
//
// Keys Tiffin manages live in File, not root's own authorized_keys: the
// box's service can't see /root (its unit has ProtectHome=yes), and keys the
// owner adds there by hand stay theirs. The sshd drop-in written by tiffin
// provision names both files (AuthorizedKeysFile).
//
// Every line Tiffin writes is built here from a parsed key: the key itself,
// re-encoded, and a comment made only of checked fields. Nothing a person
// typed (options, comments, extra lines) reaches the file.
package sshkeys

import (
	"bytes"
	"crypto/rsa"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	// Dir and File are where sshd finds the keys Tiffin manages for root
	// (AuthorizedKeysFile ... /etc/ssh/authorized_keys.d/%u).
	Dir  = "/etc/ssh/authorized_keys.d"
	File = Dir + "/root"

	// MaxKeys and MaxBytes bound what one request (or the whole file) may hold.
	MaxKeys  = 10
	MaxBytes = 16 << 10

	// MinRSABits matches the sshd drop-in's RequiredRSASize.
	MinRSABits = 3072
)

// Source says where a key came from. Its text is part of the key's line.
type Source string

const (
	SourcePasted    Source = "pasted"
	SourceHetzner   Source = "hetzner"
	SourceDashboard Source = "dashboard"
	sourceGitHub           = "github:"
)

var githubUser = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)

// GitHub is the source of keys imported from github.com/<user>.
func GitHub(user string) Source { return Source(sourceGitHub + user) }

// Valid reports whether s is one of the sources a line may name.
func (s Source) Valid() bool {
	switch s {
	case SourcePasted, SourceHetzner, SourceDashboard:
		return true
	}
	u, ok := strings.CutPrefix(string(s), sourceGitHub)
	return ok && githubUser.MatchString(u)
}

// allowed are the key types accepted, by their wire name.
var allowed = map[string]bool{
	ssh.KeyAlgoED25519:    true,
	ssh.KeyAlgoSKED25519:  true,
	ssh.KeyAlgoECDSA256:   true,
	ssh.KeyAlgoECDSA384:   true,
	ssh.KeyAlgoECDSA521:   true,
	ssh.KeyAlgoSKECDSA256: true,
	ssh.KeyAlgoRSA:        true,
}

// Key is one accepted public key.
type Key struct {
	Type        string    `json:"type"`        // wire name, e.g. ssh-ed25519
	Fingerprint string    `json:"fingerprint"` // SHA256:...
	Bits        int       `json:"bits,omitempty"`
	Source      Source    `json:"source"`
	Added       time.Time `json:"added"`
	pub         ssh.PublicKey
}

// Line is the authorized_keys line for k: the key re-encoded and a comment
// of its source and the day it was added.
func (k Key) Line() string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k.pub))) + " tiffin " + string(k.Source) + " " + k.Added.UTC().Format("2006-01-02")
}

// Public is the key itself as an authorized_keys line without a comment.
func (k Key) Public() string { return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k.pub))) }

// Parse accepts public keys as a person pastes them: one per non-blank line,
// "type base64 [comment]". It refuses the whole input when any line is not
// an accepted key: options (command=, from=...), certificates, short RSA,
// DSA, anything left over after a key. Duplicates are dropped.
func Parse(text string, src Source, added time.Time) ([]Key, error) {
	if !src.Valid() {
		return nil, fmt.Errorf("unknown key source %q", src)
	}
	if len(text) > MaxBytes {
		return nil, fmt.Errorf("that's more than %d KB of keys", MaxBytes>>10)
	}
	var keys []Key
	seen := map[string]bool{}
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" {
			continue
		}
		k, err := parseLine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		if seen[k.Fingerprint] {
			continue
		}
		seen[k.Fingerprint] = true
		k.Source, k.Added = src, added.UTC()
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return nil, errors.New("no SSH public key found")
	}
	if len(keys) > MaxKeys {
		return nil, fmt.Errorf("at most %d keys", MaxKeys)
	}
	return keys, nil
}

func parseLine(line string) (Key, error) {
	if strings.HasPrefix(line, "-----") || strings.Contains(line, "PRIVATE KEY") {
		return Key{}, errors.New("that's a private key; paste the public one (the .pub file)")
	}
	pub, comment, opts, rest, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return Key{}, errors.New("not an SSH public key")
	}
	for _, f := range strings.Fields(comment) {
		if allowed[f] || strings.HasPrefix(f, "ssh-") || strings.HasPrefix(f, "ecdsa-") || strings.HasPrefix(f, "sk-") {
			return Key{}, errors.New("one key per line")
		}
	}
	if len(opts) > 0 {
		return Key{}, errors.New("options before the key (like command= or from=) aren't allowed")
	}
	if len(bytes.TrimSpace(rest)) > 0 {
		return Key{}, errors.New("one key per line")
	}
	if _, ok := pub.(*ssh.Certificate); ok {
		return Key{}, errors.New("certificates aren't supported; use the plain public key")
	}
	t := pub.Type()
	if !allowed[t] {
		return Key{}, fmt.Errorf("%s keys aren't accepted; use ed25519 (ssh-keygen -t ed25519)", t)
	}
	k := Key{Type: t, Fingerprint: ssh.FingerprintSHA256(pub), pub: pub}
	if t == ssh.KeyAlgoRSA {
		ck, ok := pub.(ssh.CryptoPublicKey)
		if !ok {
			return Key{}, errors.New("not an SSH public key")
		}
		rk, ok := ck.CryptoPublicKey().(*rsa.PublicKey)
		if !ok {
			return Key{}, errors.New("not an SSH public key")
		}
		k.Bits = rk.N.BitLen()
		if k.Bits < MinRSABits {
			return Key{}, fmt.Errorf("RSA keys need at least %d bits (this one has %d); ed25519 is better: ssh-keygen -t ed25519", MinRSABits, k.Bits)
		}
	}
	return k, nil
}

// ParseFile reads the lines Tiffin wrote back into keys. Lines it can't
// read as its own (hand edits) are skipped, so a stray line never hides the
// rest.
func ParseFile(raw []byte) []Key {
	var keys []Key
	seen := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 5 || f[2] != "tiffin" {
			continue
		}
		k, err := parseLine(f[0] + " " + f[1])
		if err != nil || seen[k.Fingerprint] {
			continue
		}
		src := Source(f[3])
		day, err := time.Parse("2006-01-02", f[4])
		if !src.Valid() || err != nil {
			continue
		}
		seen[k.Fingerprint] = true
		k.Source, k.Added = src, day
		keys = append(keys, k)
	}
	return keys
}

// Render is the whole file for keys, sorted by when they were added.
func Render(keys []Key) []byte {
	ks := append([]Key(nil), keys...)
	sort.SliceStable(ks, func(i, j int) bool { return ks[i].Added.Before(ks[j].Added) })
	var b strings.Builder
	b.WriteString("# Managed by Tiffin: your SSH keys for root. Change them in the dashboard\n# (Settings › Server access); keys you add yourself go in /root/.ssh/authorized_keys.\n")
	for _, k := range ks {
		b.WriteString(k.Line())
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// Merge adds keys to have, skipping ones already there, up to MaxKeys.
func Merge(have, add []Key) ([]Key, error) {
	out := append([]Key(nil), have...)
	seen := map[string]bool{}
	for _, k := range have {
		seen[k.Fingerprint] = true
	}
	for _, k := range add {
		if !seen[k.Fingerprint] {
			seen[k.Fingerprint] = true
			out = append(out, k)
		}
	}
	if len(out) > MaxKeys {
		return nil, fmt.Errorf("at most %d keys; remove one first", MaxKeys)
	}
	return out, nil
}

// Store reads and writes the key file at Path (File on a box; tests use a
// temp dir). Writes are atomic and serialised with a lock file.
type Store struct{ Path string }

// Read returns the keys in the file; none when it doesn't exist.
func (s Store) Read() ([]Key, error) {
	raw, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return ParseFile(raw), nil
}

// Update applies fn to the current keys under the lock and writes the
// result atomically (root-owned 0644 in a 0755 directory, as sshd's
// StrictModes wants).
func (s Store) Update(fn func([]Key) ([]Key, error)) ([]Key, error) {
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".tiffin.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	if fi, err := os.Lstat(s.Path); err == nil && !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", s.Path)
	}
	have, err := s.Read()
	if err != nil {
		return nil, err
	}
	next, err := fn(have)
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dir, ".root-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(Render(next)); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp.Name(), s.Path); err != nil {
		return nil, err
	}
	return next, nil
}
