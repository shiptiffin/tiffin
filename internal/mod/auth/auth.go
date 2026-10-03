// Package auth is the Tiffin auth module: user accounts, sessions and
// organizations for every project's apps (M6).
//
// One engine process (packages/auth-engine: Better Auth on Bun) serves every
// auth-enabled project on the box. Each project keeps its users in its own
// Postgres database, in the `auth` schema. The edge sends /api/auth/* on each
// web app host to the engine, which picks the project by Host. This module
// installs the engine (Provision), writes its config and migrates each
// project's schema (Reconcile), adds the routes and TIFFIN_AUTH_URL, and
// serves the dashboard's Auth pages through the platform API.
//
// Why one process: Bun plus Better Auth costs ~60-90 MB resident; one per
// project would eat a 4 GB box after a handful of projects. Instances are
// per project inside the process (own secret, own database pool that idles to
// zero connections), so projects stay isolated without the memory bill.
package auth

import (
	"path/filepath"

	"github.com/btahir/tiffin/internal/platform"
)

func init() { platform.Register(&Module{}) }

// Module implements the auth module.
type Module struct{}

func (*Module) Name() string { return "auth" }
func (*Module) Order() int   { return 30 }

// Engine locations on the box.
const (
	// EngineAddr is where the engine listens for the edge and for apps on the box.
	EngineAddr = "127.0.0.1:7393"
	// AdminSocket is the engine's admin API (root only).
	AdminSocket = "/run/tiffin-auth/admin.sock"
	// Unit is the engine's systemd unit.
	Unit = "tiffin-auth.service"
	// EngineUser runs the engine.
	EngineUser = "tiffin-auth"
	// PathPrefix is where the endpoint lives on every app host.
	PathPrefix = "/api/auth"
)

// Dir is the module's directory on the data disk (DataRoot/auth).
func Dir(p *platform.Platform) string {
	root := "/var/lib/tiffin"
	if p != nil && p.DataRoot != "" {
		root = p.DataRoot
	}
	return filepath.Join(root, "auth")
}

// ConfigPath is the engine config the module writes.
func ConfigPath(p *platform.Platform) string { return filepath.Join(Dir(p), "engine.json") }

// KV namespaces.
const (
	nsSecret = "auth/secret" // key: project → base64 secret
)
