// Package auth is the Tiffin auth module. Auth: the Better Auth engine for apps, organizations and roles (M6).
package auth

import "github.com/btahir/tiffin/internal/platform"

func init() { platform.Register(&Module{}) }

// Module implements the auth module. See internal/platform for the optional interfaces.
type Module struct{}

func (*Module) Name() string { return "auth" }
func (*Module) Order() int   { return 30 }
