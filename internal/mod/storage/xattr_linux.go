package storage

import "golang.org/x/sys/unix"

// hasXattrs reports whether path carries extended attributes: a directory
// with them is a folder object the gateway made, not just a parent.
func hasXattrs(path string) bool {
	n, err := unix.Llistxattr(path, nil)
	return err == nil && n > 0
}
