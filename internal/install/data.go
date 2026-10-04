package install

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/btahir/tiffin/internal/provider"
)

// DataRoot is where every box keeps its data. On a server it is a separate
// disk when there is one, so the server can be rebuilt without losing it.
const DataRoot = "/var/lib/tiffin"

// DataLabel is the filesystem label of a data disk Tiffin formatted. XFS
// labels are at most 12 characters.
const DataLabel = "tiffin-data"

// DataSpec says what holds /var/lib/tiffin on a server.
type DataSpec struct {
	// Device is a block device (a Hetzner Volume, /dev/sdb, a partition).
	// A blank device is formatted XFS; a device with a filesystem is used
	// as it is (never reformatted).
	Device string
	// Dir is a directory on an existing filesystem, bind-mounted at
	// /var/lib/tiffin. Empty Device and Dir: /var/lib/tiffin on the root disk.
	Dir string
}

var safePath = regexp.MustCompile(`^/[A-Za-z0-9_./:+-]*$`)

// dataScript renders the shell script that mounts the data disk. It is
// idempotent: a mounted /var/lib/tiffin is left alone. Pure, for tests.
func dataScript(d DataSpec) (string, error) {
	if d.Device != "" && d.Dir != "" {
		return "", fmt.Errorf("give a data disk or a data directory, not both")
	}
	for _, p := range []string{d.Device, d.Dir} {
		if p != "" && (!safePath.MatchString(p) || strings.Contains(p, "..")) {
			return "", fmt.Errorf("%q is not an absolute path", p)
		}
	}
	if d.Dir == DataRoot {
		d.Dir = ""
	}
	head := `set -euo pipefail
root=` + DataRoot + `
fsok() { # warn when reflink branches will not work
  t="$(findmnt -n -o FSTYPE --target "$root" 2>/dev/null || true)"
  if [ "$t" != xfs ]; then echo "warning: $root is on ${t:-an unknown filesystem}, not XFS: database branches and box exports copy files instead of sharing them (slower, uses more disk)"; fi
  echo "data: $(findmnt -n -o SOURCE,FSTYPE --target "$root" | tr -s ' ')"
}
sudo mkdir -p "$root"
`
	switch {
	case d.Device != "":
		return head + fmt.Sprintf(`dev=%q
if mountpoint -q "$root"; then fsok; exit 0; fi
for i in $(seq 1 90); do [ -b "$dev" ] && break; sleep 1; done
[ -b "$dev" ] || { echo "the data disk $dev does not exist on this server" >&2; exit 3; }
real="$(readlink -f "$dev")"
if [ -n "$(findmnt -n -o TARGET -S "$real" 2>/dev/null || true)" ]; then
  echo "$dev is already mounted at $(findmnt -n -o TARGET -S "$real" | head -n1); unmount it (and remove it from /etc/fstab) first, or pass that directory instead" >&2; exit 3
fi
fstype="$(sudo blkid -o value -s TYPE "$real" 2>/dev/null || true)"
if [ -z "$fstype" ]; then
  if [ "$(lsblk -nro NAME "$real" | wc -l)" -gt 1 ]; then echo "$dev has partitions but no filesystem; pass the partition instead" >&2; exit 3; fi
  command -v mkfs.xfs >/dev/null || { sudo apt-get update -y >/dev/null && sudo DEBIAN_FRONTEND=noninteractive apt-get install -y xfsprogs >/dev/null; }
  echo "formatting $dev as XFS (it was blank)"
  sudo mkfs.xfs -q -L %[2]s "$real"
  fstype=xfs
fi
label="$(sudo blkid -o value -s LABEL "$real" 2>/dev/null || true)"
if [ "$label" = %[2]s ]; then src="LABEL=%[2]s"; else src="UUID=$(sudo blkid -o value -s UUID "$real")"; fi
sudo sed -i '\| /var/lib/tiffin |d' /etc/fstab
echo "$src $root $fstype defaults,nofail,x-systemd.device-timeout=90s 0 2" | sudo tee -a /etc/fstab >/dev/null
sudo systemctl daemon-reload
sudo mount "$root"
fsok
`, d.Device, DataLabel), nil
	case d.Dir != "":
		return head + fmt.Sprintf(`dir=%q
if mountpoint -q "$root"; then fsok; exit 0; fi
sudo mkdir -p "$dir"
sudo sed -i '\| /var/lib/tiffin |d' /etc/fstab
echo "$dir $root none bind,nofail 0 0" | sudo tee -a /etc/fstab >/dev/null
sudo systemctl daemon-reload
sudo mount "$root"
fsok
`, d.Dir), nil
	default:
		return head + "fsok\n", nil
	}
}

// PrepareData mounts the server's data disk at /var/lib/tiffin before the
// first install. It returns warnings for people (e.g. not XFS).
func PrepareData(ctx context.Context, m provider.Machine, d DataSpec, progress func(string)) ([]string, error) {
	script, err := dataScript(d)
	if err != nil {
		return nil, err
	}
	progress("preparing the data disk at " + DataRoot)
	out, stderr, err := m.Exec(ctx, script)
	if err != nil {
		return nil, fmt.Errorf("data disk: %s%s", strings.TrimSpace(stderr), errSuffix(err))
	}
	var warnings []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "warning: "):
			warnings = append(warnings, strings.TrimPrefix(line, "warning: "))
			progress(line)
		case line != "":
			progress(line)
		}
	}
	return warnings, nil
}

func errSuffix(err error) string {
	if err == nil {
		return ""
	}
	return " (" + err.Error() + ")"
}
