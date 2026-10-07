package srcpack

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// MaxMeta is the most the box reads of one file of a source tree that it
// parses itself (package.json, configs, lock, ignore and workspace files).
const MaxMeta = 16 << 20

// ErrNotPlain is returned for a path that is not a regular file.
var ErrNotPlain = errors.New("not a plain file")

// ReadFile reads a regular file of an untrusted source tree, at most
// MaxMeta bytes, into memory. It refuses anything else (a FIFO, a device,
// a bigger file) without blocking on it. With links false a symlink is
// refused too: use that on a tree whose links are not yet checked (a
// fresh clone), where one could lead to /dev/zero or any file of the box.
func ReadFile(p string, links bool) ([]byte, error) {
	flags := os.O_RDONLY | syscall.O_NONBLOCK
	if !links {
		flags |= syscall.O_NOFOLLOW
	}
	f, err := os.OpenFile(p, flags, 0)
	if err != nil {
		if !links {
			if fi, lerr := os.Lstat(p); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("%s: %w", filepath.Base(p), ErrNotPlain)
			}
		}
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: %w", filepath.Base(p), ErrNotPlain)
	}
	if fi.Size() > MaxMeta {
		return nil, fmt.Errorf("%s is over %d MiB", filepath.Base(p), MaxMeta>>20)
	}
	raw, err := io.ReadAll(io.LimitReader(f, MaxMeta+1))
	if err == nil && len(raw) > MaxMeta {
		err = fmt.Errorf("%s is over %d MiB", filepath.Base(p), MaxMeta>>20)
	}
	return raw, err
}
