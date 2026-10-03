// Package analytics is the Tiffin analytics module. First-party, cookieless analytics (M8).
package analytics

import "github.com/btahir/tiffin/internal/platform"

func init() { platform.Register(&Module{}) }

// Module implements the analytics module. See internal/platform for the optional interfaces.
type Module struct{}

func (*Module) Name() string { return "analytics" }
func (*Module) Order() int   { return 35 }
