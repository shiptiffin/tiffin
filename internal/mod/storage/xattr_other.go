//go:build !linux

package storage

// hasXattrs: only the box (Linux) has folder objects to keep.
func hasXattrs(string) bool { return false }
