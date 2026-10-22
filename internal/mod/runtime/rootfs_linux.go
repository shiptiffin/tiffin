package runtime

import (
	"bytes"
	"encoding/hex"
	"errors"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

// xattrs lists a path's extended attributes (not following a link) as
// name=hex(value), sorted, comma-separated.
func xattrs(p string) (string, error) {
	buf := make([]byte, 4096)
	n, err := unix.Llistxattr(p, buf)
	if errors.Is(err, unix.ERANGE) {
		if n, err = unix.Llistxattr(p, nil); err == nil {
			buf = make([]byte, n)
			n, err = unix.Llistxattr(p, buf)
		}
	}
	if errors.Is(err, unix.ENOTSUP) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var names []string
	for _, name := range bytes.Split(buf[:n], []byte{0}) {
		if len(name) > 0 {
			names = append(names, string(name))
		}
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, name := range names {
		sz, err := unix.Lgetxattr(p, name, nil)
		if err != nil {
			return "", err
		}
		v := make([]byte, sz)
		if sz, err = unix.Lgetxattr(p, name, v); err != nil {
			return "", err
		}
		out = append(out, name+"="+hex.EncodeToString(v[:sz]))
	}
	return strings.Join(out, ","), nil
}
