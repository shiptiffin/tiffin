package sshkeys

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

var day = time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)

func line(t *testing.T, pub any, comment string) string {
	t.Helper()
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp))) + " " + comment
}

func ed(t *testing.T) string {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	return line(t, pub, "me@laptop")
}

func TestParseAcceptsAndCanonicalises(t *testing.T) {
	a, b := ed(t), ed(t)
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	keys, err := Parse("\n"+a+"\r\n  "+b+"  \n"+a+"\n"+line(t, &ec.PublicKey, ""), GitHub("sam-o"), day)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 {
		t.Fatalf("want 3 keys (duplicate dropped), got %d", len(keys))
	}
	l := keys[0].Line()
	if !strings.HasSuffix(l, " tiffin github:sam-o 2026-10-11") || strings.Contains(l, "laptop") {
		t.Fatalf("line keeps the person's comment or lacks ours: %q", l)
	}
	if !strings.HasPrefix(keys[0].Fingerprint, "SHA256:") {
		t.Fatal(keys[0].Fingerprint)
	}
}

func TestParseRefuses(t *testing.T) {
	small, _ := rsa.GenerateKey(rand.Reader, 2048)
	cases := map[string]string{
		"options":     `command="sh" ` + ed(t),
		"from":        `from="1.2.3.4" ` + ed(t),
		"two on one":  ed(t) + " " + ed(t),
		"garbage":     "ssh-ed25519 AAAAnotbase64!!",
		"private":     "-----BEGIN OPENSSH PRIVATE KEY-----",
		"short rsa":   line(t, &small.PublicKey, "x"),
		"empty":       "\n \n",
		"bad line 2":  ed(t) + "\nhello",
		"newline cmt": ed(t) + "\ncommand=\"x\" " + ed(t),
	}
	for name, in := range cases {
		if _, err := Parse(in, SourcePasted, day); err == nil {
			t.Errorf("%s: accepted %q", name, in)
		}
	}
	many := ""
	for range MaxKeys + 1 {
		many += ed(t) + "\n"
	}
	if _, err := Parse(many, SourcePasted, day); err == nil {
		t.Error("accepted more than MaxKeys")
	}
	if _, err := Parse(ed(t), Source("github:bad user\nx"), day); err == nil {
		t.Error("accepted a bad source")
	}
}

func TestRSA3072Accepted(t *testing.T) {
	k, _ := rsa.GenerateKey(rand.Reader, 3072)
	keys, err := Parse(line(t, &k.PublicKey, "x"), SourceHetzner, day)
	if err != nil || keys[0].Bits != 3072 {
		t.Fatal(keys, err)
	}
}

func TestSources(t *testing.T) {
	for _, s := range []Source{SourcePasted, SourceHetzner, SourceDashboard, GitHub("a"), GitHub("a-b-1")} {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []Source{"", "github:", "github:-a", "github:a b", "github:a\n", "other", GitHub(strings.Repeat("a", 40))} {
		if s.Valid() {
			t.Errorf("%q should be invalid", s)
		}
	}
}

func TestStoreRoundTripAndHandEdits(t *testing.T) {
	s := Store{Path: filepath.Join(t.TempDir(), "authorized_keys.d", "root")}
	if keys, err := s.Read(); err != nil || keys != nil {
		t.Fatal(keys, err)
	}
	add, _ := Parse(ed(t)+"\n"+ed(t), SourcePasted, day)
	got, err := s.Update(func(have []Key) ([]Key, error) { return Merge(have, add) })
	if err != nil || len(got) != 2 {
		t.Fatal(got, err)
	}
	again, err := s.Update(func(have []Key) ([]Key, error) { return Merge(have, add) })
	if err != nil || len(again) != 2 {
		t.Fatal("merge is not idempotent", again, err)
	}
	read, _ := s.Read()
	if len(read) != 2 || read[0].Source != SourcePasted || !read[0].Added.Equal(day.Truncate(24*time.Hour)) {
		t.Fatal(read)
	}
	// A hand-added line is ignored on read (and dropped on the next write).
	raw := string(Render(read)) + ed(t) + "\n"
	if ks := ParseFile([]byte(raw)); len(ks) != 2 {
		t.Fatal(len(ks))
	}
	left, err := s.Update(func(have []Key) ([]Key, error) { return have[1:], nil })
	if err != nil || len(left) != 1 || left[0].Fingerprint != read[1].Fingerprint {
		t.Fatal(left, err)
	}
}
