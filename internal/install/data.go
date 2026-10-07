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
hidden() { # a new data disk must never hide the box's data already on the root disk
  first="$(sudo find "$root" -mindepth 1 -maxdepth 1 ! -name lost+found -print -quit)"
  [ -z "$first" ] && return 0
  echo "$root already holds this box's data on the root disk (e.g. $first); mounting $1 there would hide it. Run up without the data option to keep it there, or stop Tiffin (sudo systemctl stop tiffin tiffin-edge.socket tiffin-edge.service) and move the data onto $1 first" >&2
  exit 3
}
`
	switch {
	case d.Device != "":
		return head + fmt.Sprintf(`dev=%q
grow() { # the disk grew (a resized volume): grow XFS to fill it, online
  [ "$(findmnt -n -o FSTYPE "$root" 2>/dev/null || true)" = xfs ] && command -v xfs_growfs >/dev/null || return 0
  name="$(basename "$(readlink -f "$dev")")"
  if [ -e "/sys/class/block/$name/device/rescan" ]; then echo 1 | sudo tee "/sys/class/block/$name/device/rescan" >/dev/null || true; fi
  before="$(df -B1 --output=size "$root" | tail -n1 | tr -d ' ')"
  sudo xfs_growfs -d "$root" >/dev/null
  after="$(df -B1 --output=size "$root" | tail -n1 | tr -d ' ')"
  if [ "$after" -gt "$before" ]; then echo "grew the data disk from $(( (before + (1<<29)) >> 30 )) to $(( (after + (1<<29)) >> 30 )) GB"; fi
}
quota() { # project quotas hold apps' disk folders to their sizes; XFS turns them on only when it mounts
  [ "$(findmnt -n -o FSTYPE "$root" 2>/dev/null || true)" = xfs ] || return 0
  findmnt -n -o OPTIONS "$root" | grep -q prjquota && return 0
  if ! grep -qE "^[^#]+[[:space:]]$root[[:space:]]+xfs[[:space:]]+[^[:space:]]*prjquota" /etc/fstab; then
    sudo sed -i -E "s|^([^#[:space:]]+[[:space:]]+$root[[:space:]]+xfs[[:space:]]+)([^[:space:]]+)|\1\2,prjquota|" /etc/fstab
    sudo systemctl daemon-reload
  fi
  echo "warning: the data disk was mounted before project quotas, so apps' disk folder sizes are not enforced yet (folders can grow past them, as before). They are on from the server's next restart: sudo reboot (about a minute offline)"
}
if mountpoint -q "$root"; then grow; quota; fsok; exit 0; fi
for i in $(seq 1 90); do [ -b "$dev" ] && break; sleep 1; done
[ -b "$dev" ] || { echo "the data disk $dev does not exist on this server" >&2; exit 3; }
real="$(readlink -f "$dev")"
# A disk Tiffin labelled holds the box's data already: mounting it again is fine.
if [ "$(sudo blkid -o value -s LABEL "$real" 2>/dev/null || true)" != %[2]s ]; then hidden "$dev"; fi
moved=0
cur="$(findmnt -n -o TARGET -S "$real" 2>/dev/null | head -n1 || true)"
if [ -n "$cur" ]; then
  # Mounted elsewhere (Hetzner automounts volumes at /mnt/HC_Volume_<id>).
  # An empty disk moves to /var/lib/tiffin; one holding files is left alone.
  extra="$(sudo find "$cur" -mindepth 1 -maxdepth 1 ! -name lost+found -print -quit)"
  if [ -n "$extra" ]; then
    echo "$dev is mounted at $cur and holds files (e.g. $extra); Tiffin never moves or formats a disk with data. Empty it, or pass --data-dir $cur" >&2; exit 3
  fi
  echo "moving $dev from $cur to $root (it is empty)"
  sudo umount "$cur"
  sudo sed -i "\|[[:space:]]$cur[[:space:]]|d" /etc/fstab
  sudo rmdir "$cur" 2>/dev/null || true
  sudo systemctl daemon-reload
  moved=1
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
if [ "$label" != %[2]s ] && { [ -z "$label" ] || [ "$moved" = 1 ]; }; then
  # Label it as the box expects (the filesystem is unmounted here).
  case "$fstype" in
    xfs) sudo xfs_admin -L %[2]s "$real" >/dev/null && label=%[2]s ;;
    ext2|ext3|ext4) sudo e2label "$real" %[2]s && label=%[2]s ;;
  esac
fi
if [ "$label" = %[2]s ]; then src="LABEL=%[2]s"; else src="UUID=$(sudo blkid -o value -s UUID "$real")"; fi
sudo sed -i '\| /var/lib/tiffin |d' /etc/fstab
opts=defaults
if [ "$fstype" = xfs ]; then opts=defaults,prjquota; fi
echo "$src $root $fstype $opts,nofail,x-systemd.device-timeout=90s 0 2" | sudo tee -a /etc/fstab >/dev/null
sudo systemctl daemon-reload
sudo mount "$root"
grow
fsok
`, d.Device, DataLabel), nil
	case d.Dir != "":
		return head + fmt.Sprintf(`dir=%q
if mountpoint -q "$root"; then fsok; exit 0; fi
# A folder that holds files already is the box's data moved there.
if [ -z "$(sudo find "$dir" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null)" ]; then hidden "$dir"; fi
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
// first install, and grows its XFS filesystem (online) when the disk grew.
// It returns warnings for people (e.g. not XFS).
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
