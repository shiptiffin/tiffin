package remote

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseTarget(t *testing.T) {
	for in, want := range map[string]Target{
		"root@203.0.113.5":            {User: "root", Host: "203.0.113.5", Port: 22},
		"ubuntu@box.example.com:2222": {User: "ubuntu", Host: "box.example.com", Port: 2222},
		"me@[2001:db8::1]:22":         {User: "me", Host: "2001:db8::1", Port: 22},
	} {
		got, err := ParseTarget(in)
		if err != nil || got != want {
			t.Errorf("%s: got %+v %v", in, got, err)
		}
	}
	for _, bad := range []string{"203.0.113.5", "root@", "root@-oProxyCommand=x", "Root User@host", "root@host:99999"} {
		if _, err := ParseTarget(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
	if s := (Target{User: "a", Host: "2001:db8::1", Port: 2222}).String(); s != "a@[2001:db8::1]:2222" {
		t.Errorf("String: %s", s)
	}
}

func TestOwnerRangeAndArch(t *testing.T) {
	if r := OwnerRange("198.51.100.4"); r != "198.51.100.4" {
		t.Error(r)
	}
	if r := OwnerRange("2001:db8:1:2:3:4:5:6"); r != "2001:db8:1:2::/64" {
		t.Error(r)
	}
	if GoArch("aarch64") != "arm64" || GoArch("x86_64") != "amd64" {
		t.Error("GoArch")
	}
}

func TestKeysAndKnownHosts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "box", "id_ed25519")
	pub, err := GenerateKey(path, "tiffin-shop")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pub, "ssh-ed25519 ") || !strings.HasSuffix(pub, " tiffin-shop") {
		t.Fatalf("public key %q", pub)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode %v %v", fi.Mode(), err)
	}
	again, _ := GenerateKey(path, "tiffin-shop")
	if again != pub {
		t.Fatal("GenerateKey must reuse an existing key")
	}
	fp, err := Fingerprint(pub)
	if err != nil || len(strings.Split(fp, ":")) != 16 {
		t.Fatalf("fingerprint %q %v", fp, err)
	}

	kh := filepath.Join(dir, "known_hosts")
	_ = os.WriteFile(kh, []byte("203.0.113.5 ssh-ed25519 AAAA\n[203.0.113.5]:2222 ssh-ed25519 BBBB\n198.51.100.1 ssh-ed25519 CCCC\n"), 0o600)
	if err := ForgetHost(kh, "203.0.113.5"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(kh)
	if string(raw) != "198.51.100.1 ssh-ed25519 CCCC\n" {
		t.Fatalf("known_hosts: %q", raw)
	}
}

func TestSSHArgs(t *testing.T) {
	m := New(Target{User: "root", Host: "203.0.113.5", Identity: "/k/id", KnownHosts: "/k/known_hosts"})
	a := strings.Join(m.args("true"), " ")
	for _, want := range []string{"BatchMode=yes", "StrictHostKeyChecking=accept-new", "UserKnownHostsFile=/k/known_hosts", "-i /k/id", "-p 22", "-- root@203.0.113.5 true"} {
		if !strings.Contains(a, want) {
			t.Errorf("ssh args %q lack %q", a, want)
		}
	}
}
