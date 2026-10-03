// Package protect is the Tiffin protect module. Protection: rate limits, CrowdSec, the proof-of-work challenge and the under-attack switch (M11).
package protect

import "github.com/btahir/tiffin/internal/platform"

func init() { platform.Register(&Module{}) }

// Module implements the protect module. See internal/platform for the optional interfaces.
type Module struct{}

func (*Module) Name() string { return "protect" }
func (*Module) Order() int   { return 5 }
