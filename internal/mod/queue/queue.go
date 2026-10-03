// Package queue is the Tiffin queue module. Queues and workflows on River (M7).
package queue

import "github.com/btahir/tiffin/internal/platform"

func init() { platform.Register(&Module{}) }

// Module implements the queue module. See internal/platform for the optional interfaces.
type Module struct{}

func (*Module) Name() string { return "queue" }
func (*Module) Order() int   { return 30 }
