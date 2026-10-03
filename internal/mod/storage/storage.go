// Package storage is the Tiffin storage module. Storage: S3-compatible buckets on the data disk via versitygw (M5).
package storage

import "github.com/btahir/tiffin/internal/platform"

func init() { platform.Register(&Module{}) }

// Module implements the storage module. See internal/platform for the optional interfaces.
type Module struct{}

func (*Module) Name() string { return "storage" }
func (*Module) Order() int   { return 20 }
