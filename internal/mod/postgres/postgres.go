// Package postgres is the Tiffin postgres module. Postgres 18: one database per project, preview branches as reflink clones, backups (M4).
package postgres

import "github.com/btahir/tiffin/internal/platform"

func init() { platform.Register(&Module{}) }

// Module implements the postgres module. See internal/platform for the optional interfaces.
type Module struct{}

func (*Module) Name() string { return "postgres" }
func (*Module) Order() int   { return 10 }
