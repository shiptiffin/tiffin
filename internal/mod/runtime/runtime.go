// Package runtime is the Tiffin runtime module. App runtime: builds (Railpack + BuildKit), containers (containerd), deploys, routes, logs (M2/M3).
package runtime

import "github.com/btahir/tiffin/internal/platform"

func init() { platform.Register(&Module{}) }

// Module implements the runtime module. See internal/platform for the optional interfaces.
type Module struct{}

func (*Module) Name() string { return "runtime" }
func (*Module) Order() int   { return 40 }
