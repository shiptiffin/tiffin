// Package approvals is the Tiffin approvals module. Approvals and undo safety: passkey-bound approvals, pre-destroy snapshots (M9).
package approvals

import "github.com/btahir/tiffin/internal/platform"

func init() { platform.Register(&Module{}) }

// Module implements the approvals module. See internal/platform for the optional interfaces.
type Module struct{}

func (*Module) Name() string { return "approvals" }
func (*Module) Order() int   { return 50 }
