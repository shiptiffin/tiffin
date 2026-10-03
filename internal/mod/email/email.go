// Package email is the Tiffin email module. Email: SMTP relay, the dev inbox, templates and suppression (M6).
package email

import "github.com/btahir/tiffin/internal/platform"

func init() { platform.Register(&Module{}) }

// Module implements the email module. See internal/platform for the optional interfaces.
type Module struct{}

func (*Module) Name() string { return "email" }
func (*Module) Order() int   { return 20 }
