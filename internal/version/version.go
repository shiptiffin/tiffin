// Package version holds build metadata, stamped at link time by the Makefile:
//
//	-X github.com/btahir/tiffin/internal/version.Version=...
//	-X github.com/btahir/tiffin/internal/version.Commit=...
//	-X github.com/btahir/tiffin/internal/version.Date=...
package version

import "fmt"

// These are overridden with -ldflags -X at build time.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String returns a single-line human description, e.g. "tiffin 0.1.0 (abc1234, 2026-10-02T12:00:00Z)".
func String() string {
	return fmt.Sprintf("tiffin %s (%s, %s)", Version, Commit, Date)
}
