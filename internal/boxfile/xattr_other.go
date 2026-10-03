//go:build !linux

package boxfile

import "os"

// Extended attributes only matter on the box (Linux); elsewhere (tests on
// a Mac) they are not read or written.
func listXattrs(string) (map[string]string, error) { return nil, nil }

func setXattrs(*os.File, map[string]string) error { return nil }
