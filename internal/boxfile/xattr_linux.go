//go:build linux

package boxfile

import (
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// listXattrs returns a file's user.* extended attributes (versitygw keeps
// object metadata, ETags and bucket ACLs there).
func listXattrs(path string) (map[string]string, error) {
	sz, err := unix.Llistxattr(path, nil)
	if err != nil || sz == 0 {
		if errors.Is(err, unix.ENOTSUP) {
			return nil, nil
		}
		return nil, err
	}
	buf := make([]byte, sz)
	sz, err = unix.Llistxattr(path, buf)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, name := range strings.Split(string(buf[:sz]), "\x00") {
		if !strings.HasPrefix(name, "user.") {
			continue
		}
		vsz, err := unix.Lgetxattr(path, name, nil)
		if err != nil {
			continue
		}
		v := make([]byte, vsz)
		if vsz > 0 {
			if vsz, err = unix.Lgetxattr(path, name, v); err != nil {
				continue
			}
		}
		out[name] = string(v[:vsz])
	}
	return out, nil
}

// setXattrs restores the user.* attributes recorded in PAX records.
func setXattrs(f *os.File, pax map[string]string) error {
	for k, v := range pax {
		name, ok := strings.CutPrefix(k, "SCHILY.xattr.")
		if !ok || !strings.HasPrefix(name, "user.") {
			continue
		}
		if err := unix.Fsetxattr(int(f.Fd()), name, []byte(v), 0); err != nil {
			return err
		}
	}
	return nil
}
