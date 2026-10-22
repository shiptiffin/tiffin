//go:build !linux

package runtime

// xattrs is Linux-only: images are only mounted on a box.
func xattrs(string) (string, error) { return "", nil }
