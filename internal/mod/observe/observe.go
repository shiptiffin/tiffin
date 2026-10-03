// Package observe is the Tiffin observe module. Observability: OTel into VictoriaMetrics and VictoriaLogs, error ingest, alerts (M8).
package observe

import "github.com/btahir/tiffin/internal/platform"

func init() { platform.Register(&Module{}) }

// Module implements the observe module. See internal/platform for the optional interfaces.
type Module struct{}

func (*Module) Name() string { return "observe" }
func (*Module) Order() int   { return 15 }
