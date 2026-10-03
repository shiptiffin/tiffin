// Package valkey is the Tiffin valkey module. Valkey: per-project KV/cache namespaces and REDIS_URL (M3).
package valkey

import "github.com/btahir/tiffin/internal/platform"

func init() { platform.Register(&Module{}) }

// Module implements the valkey module. See internal/platform for the optional interfaces.
type Module struct{}

func (*Module) Name() string { return "valkey" }
func (*Module) Order() int   { return 10 }
