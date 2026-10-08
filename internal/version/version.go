// Package version holds build metadata, stamped at link time by the Makefile:
//
//	-X github.com/btahir/tiffin/internal/version.Version=...
//	-X github.com/btahir/tiffin/internal/version.Commit=...
//	-X github.com/btahir/tiffin/internal/version.Date=...
package version

import (
	"fmt"
	"runtime/debug"
)

// These are overridden with -ldflags -X at build time.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// Source is where Tiffin's source code is (AGPL-3.0 §13: people who use a
// box over the network are offered the source of the version it runs).
const Source = "https://github.com/shiptiffin/tiffin"

func init() {
	// A plain `go build` in a checkout has no -X flags; Go still records the commit.
	if Commit != "none" {
		return
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 12 {
				Commit = s.Value[:12]
			}
		}
	}
}

// String returns a single-line human description, e.g. "tiffin 0.1.0 (abc1234, 2026-10-02T12:00:00Z)".
func String() string {
	return fmt.Sprintf("tiffin %s (%s, %s)", Version, Commit, Date)
}

// SourceURL is the source code of this build: the repository at its commit,
// or the repository itself when the commit is unknown.
func SourceURL() string {
	if Commit == "" || Commit == "none" {
		return Source
	}
	return Source + "/tree/" + Commit
}
