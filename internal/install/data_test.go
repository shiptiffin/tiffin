package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/platform"
)

func TestDataScript(t *testing.T) {
	for _, d := range []DataSpec{{Device: "/dev/disk/by-id/scsi-0HC_Volume_123"}, {Dir: "/srv/tiffin"}, {}} {
		s, err := dataScript(d)
		if err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("bash", "-n", "-c", s).CombinedOutput(); err != nil {
			t.Fatalf("%+v: script does not parse: %v\n%s", d, err, out)
		}
		if d.Device != "" && (!strings.Contains(s, "mkfs.xfs -q -L tiffin-data") || !strings.Contains(s, `if [ -z "$fstype" ]`)) {
			t.Fatal("a blank device must be formatted XFS with the label, and only when blank")
		}
		if grows := strings.Count(s, "\ngrow\n") + strings.Count(s, "then grow;"); (d.Device != "") != (grows == 2) || d.Device != "" && !strings.Contains(s, `sudo xfs_growfs -d "$root"`) {
			t.Fatalf("%+v: a data disk grows its XFS filesystem whether it was mounted already or not (%d calls)", d, grows)
		}
	}
	for _, bad := range []DataSpec{{Device: "/dev/sdb", Dir: "/srv"}, {Device: "sdb"}, {Dir: "/srv/$(reboot)"}, {Dir: "/srv/../etc"}} {
		if _, err := dataScript(bad); err == nil {
			t.Errorf("%+v must be refused", bad)
		}
	}
}

func TestUnitCarriesPublicIPs(t *testing.T) {
	u := Unit(Options{Domain: "203-0-113-5.sslip.io", HTTPSPort: 443, HTTPPort: 80, PublicIP: "203.0.113.5", PublicIPv6: "2001:db8::1"})
	if !strings.Contains(u, "--public-url https://dashboard.203-0-113-5.sslip.io --public-ip 203.0.113.5 --public-ipv6 2001:db8::1\n") {
		t.Fatalf("unit:\n%s", u)
	}
	if strings.Contains(Unit(Options{Domain: "tiffin.localhost", HTTPSPort: 8443, HTTPPort: 8080}), "--public-ip") {
		t.Fatal("a local box has no public IP")
	}
	bad := &platform.ServerConfig{Provider: "hetzner", RebootWindow: "4am"}
	if err := bad.Validate(); err == nil {
		t.Fatal("a bad reboot window must be refused")
	}
}

// A box with public certificates writes no CA: the installer must not wait
// for one (a fresh Hetzner box failed every install), and reads it for the
// box's own CA.
func TestCAScript(t *testing.T) {
	read := func(mode, ca string) (string, error) {
		home := t.TempDir()
		if mode != "" {
			if err := os.WriteFile(filepath.Join(home, TLSModeFile), []byte(mode+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if ca != "" {
			if err := os.WriteFile(filepath.Join(home, "ca.crt"), []byte(ca), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		out, err := exec.Command("bash", "-c", "sudo() { \"$@\"; }\n"+caScript(home)).Output()
		return string(out), err
	}
	start := time.Now()
	if out, err := read("acme", ""); err != nil || out != "" || time.Since(start) > 5*time.Second {
		t.Fatalf("public certificates: %q %v after %s", out, err, time.Since(start))
	}
	if out, err := read("internal", "PEM"); err != nil || out != "PEM" {
		t.Fatalf("internal CA: %q %v", out, err)
	}
	if out, err := read("acme", "STALE"); err != nil || out != "" {
		t.Fatalf("a stale CA on a box with public certificates was read: %q %v", out, err)
	}
}

// Adding a data disk or folder to a box whose data is on the root disk
// would hide that data under an empty mount: refused, before any change.
func TestDataNeverHidesExistingData(t *testing.T) {
	run := func(populate, dirHasFiles bool) (string, bool) {
		tmp := t.TempDir()
		root, dir := filepath.Join(tmp, "root"), filepath.Join(tmp, "new")
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if populate {
			if err := os.WriteFile(filepath.Join(root, "state.db"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if dirHasFiles {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "state.db"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		s, err := dataScript(DataSpec{Dir: "/srv/tiffin"})
		if err != nil {
			t.Fatal(err)
		}
		s = strings.ReplaceAll(s, "root="+DataRoot, "root="+root)
		s = strings.ReplaceAll(s, `dir="/srv/tiffin"`, `dir="`+dir+`"`)
		s = strings.ReplaceAll(s, "/etc/fstab", filepath.Join(tmp, "fstab"))
		mounted := filepath.Join(tmp, "mounted")
		shims := "sudo() { \"$@\"; }\nmountpoint() { return 1; }\nfindmnt() { :; }\nsystemctl() { :; }\nsed() { :; }\nmount() { touch " + mounted + "; }\n"
		out, _ := exec.Command("bash", "-c", shims+s).CombinedOutput()
		_, err = os.Stat(mounted)
		return string(out), err == nil
	}
	if out, mounted := run(true, false); mounted || !strings.Contains(out, "would hide it") {
		t.Fatalf("an empty folder was mounted over the box's data: %s", out)
	}
	if out, mounted := run(false, false); !mounted {
		t.Fatalf("a new box must get its data folder: %s", out)
	}
	if out, mounted := run(true, true); !mounted {
		t.Fatalf("a folder holding the moved data must be mounted: %s", out)
	}
	s, _ := dataScript(DataSpec{Device: "/dev/sdb"})
	if i, j := strings.Index(s, `then hidden "$dev"`), strings.Index(s, "mkfs.xfs"); i < 0 || i > j || !strings.Contains(s, `-s LABEL "$real" 2>/dev/null || true)" != tiffin-data ]; then hidden`) {
		t.Fatal("a disk Tiffin did not label must be checked before anything is formatted or mounted")
	}
}
