package base

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSwapSize(t *testing.T) {
	const gib = 1 << 30
	for ram, want := range map[uint64]uint64{1 * gib: 1 * gib, 4 * gib: 2 * gib, 8 * gib: 4 * gib, 64 * gib: 4 * gib} {
		if got := swapSize(ram); got != want {
			t.Errorf("RAM %d GiB: swap %d GiB, want %d", ram/gib, got/gib, want/gib)
		}
	}
}

func TestUnattendedConfig(t *testing.T) {
	if c := unattendedConfig(""); !strings.Contains(c, `Automatic-Reboot "false"`) || strings.Contains(c, "Reboot-Time") {
		t.Fatalf("off by default:\n%s", c)
	}
	if c := unattendedConfig("04:30"); !strings.Contains(c, `Automatic-Reboot "true"`) || !strings.Contains(c, `Automatic-Reboot-Time "04:30"`) {
		t.Fatalf("window:\n%s", c)
	}
	if !strings.Contains(sshdConfig, "PasswordAuthentication no") || !strings.Contains(sshdConfig, "PermitRootLogin prohibit-password") {
		t.Fatal("sshd config must turn off passwords")
	}
}

// The drop-in names the dashboard's key file, keys only, and reads back as
// such; an older drop-in (before Server access) reads back as out of date.
func TestSSHDConfigReadsBack(t *testing.T) {
	for _, want := range []string{"AuthenticationMethods publickey\n", "AuthorizedKeysFile .ssh/authorized_keys /etc/ssh/authorized_keys.d/%u\n",
		"MaxAuthTries 3\n", "LogLevel VERBOSE\n"} {
		if !strings.Contains(sshdConfig, want) {
			t.Errorf("sshd config lacks %q", want)
		}
	}
	if sshdRSASize != "RequiredRSASize 3072\n" {
		t.Errorf("RSA size line: %q", sshdRSASize)
	}
	if ok, d := sshdCheck(sshdConfig + sshdRSASize); !ok || d != "SSH keys only" {
		t.Errorf("current config: %v %q", ok, d)
	}
	if ok, d := sshdCheck(sshdConfig); !ok {
		t.Errorf("without RequiredRSASize: %v %q", ok, d)
	}
	old := "PasswordAuthentication no\nPermitRootLogin prohibit-password\nMaxAuthTries 4\n"
	if ok, d := sshdCheck(old); ok || !strings.Contains(d, "Server access") {
		t.Errorf("old drop-in: %v %q", ok, d)
	}
	if ok, d := sshdCheck(""); ok || !strings.Contains(d, "not turned off") {
		t.Errorf("no drop-in: %v %q", ok, d)
	}
	if ok, _ := sshdCheck("#PasswordAuthentication no\n"); ok {
		t.Error("a commented-out line counts")
	}
}

// writeSSHDConfig keeps the strictest config sshd accepts and never leaves
// one it refuses.
func TestWriteSSHDConfig(t *testing.T) {
	write := func(p string, b []byte, m os.FileMode) (bool, error) {
		cur, err := os.ReadFile(p)
		if err == nil && string(cur) == string(b) {
			return false, nil
		}
		return true, os.WriteFile(p, b, m)
	}
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }
	accept := func() error { return nil }
	noRSASize := func(p string) func() error {
		return func() error {
			if strings.Contains(read(p), "RequiredRSASize") {
				return errors.New(`/etc/ssh/sshd_config.d/10-tiffin.conf: line 14: Bad configuration option: RequiredRSASize`)
			}
			return nil
		}
	}
	refuse := func() error { return errors.New("sshd -t: exit status 255") }

	p := filepath.Join(t.TempDir(), "10-tiffin.conf")
	if changed, err := writeSSHDConfig(p, write, accept); err != nil || !changed || read(p) != sshdConfig+sshdRSASize {
		t.Fatalf("fresh: %v %v\n%s", changed, err, read(p))
	}
	if changed, err := writeSSHDConfig(p, write, accept); err != nil || changed {
		t.Fatalf("again: %v %v", changed, err)
	}

	// An OpenSSH without RequiredRSASize gets the rest.
	p = filepath.Join(t.TempDir(), "10-tiffin.conf")
	if changed, err := writeSSHDConfig(p, write, noRSASize(p)); err != nil || !changed || read(p) != sshdConfig {
		t.Fatalf("old OpenSSH: %v %v\n%s", changed, err, read(p))
	}
	if changed, err := writeSSHDConfig(p, write, noRSASize(p)); err != nil || changed {
		t.Fatalf("old OpenSSH again: %v %v", changed, err)
	}

	// Refused outright: the previous drop-in stays, or none is left.
	if err := os.WriteFile(p, []byte("PasswordAuthentication no\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, err := writeSSHDConfig(p, write, refuse); err == nil || changed || read(p) != "PasswordAuthentication no\n" || !strings.Contains(err.Error(), "kept the previous") {
		t.Fatalf("refused with a previous drop-in: %v %v\n%s", changed, err, read(p))
	}
	p = filepath.Join(t.TempDir(), "10-tiffin.conf")
	if _, err := writeSSHDConfig(p, write, refuse); err == nil || !strings.Contains(err.Error(), "removed it") {
		t.Fatalf("refused, none before: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("a refused drop-in was left: %v", err)
	}
}

func TestKeysDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "authorized_keys.d")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := keysDir(dir); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := keysDir(link); err == nil {
		t.Fatal("followed a symlink")
	}
}
