package install

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/platform"
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
