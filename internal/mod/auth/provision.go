package auth

import (
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/btahir/tiffin/internal/platform"
)

// The engine, bundled by `make auth-engine` (bun build of packages/auth-engine,
// gzipped). It is JavaScript run by a pinned Bun: ~0.6 MB in the tiffin
// binary instead of ~40 MB per architecture for a compiled Bun executable.
//
//go:embed engine/tiffin-auth.js.gz
var engineBundle []byte

// BunVersion is the Bun release that runs the engine.
const BunVersion = "1.3.13"

// bunRelease is the pinned Bun download for each architecture.
var bunRelease = map[string]struct{ asset, sha256 string }{
	"arm64": {"bun-linux-aarch64", "70bae41b3908b0a120e1e58c5c8af30e74afae3b8d11b0d3fdd8e787ddfb4b22"},
	"amd64": {"bun-linux-x64", "79c0771fa8b92c33aae41e15a0e0d307ea99d0e2f00317c71c6c53237a78e25a"},
}

// EngineBundle returns the engine JavaScript.
func EngineBundle() ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(engineBundle))
	if err != nil {
		return nil, fmt.Errorf("auth engine bundle: %w (run `make auth-engine`)", err)
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

// --no-install: never let Bun fetch packages at runtime (Better Auth imports
// @opentelemetry/api optionally; without it, Bun would try to install it).
func unitFile(bun, script, config string) string {
	return `[Unit]
Description=Tiffin auth engine (Better Auth for every project's apps)
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
Type=simple
User=` + EngineUser + `
Group=` + EngineUser + `
ExecStart=` + bun + ` --smol --no-install ` + script + ` serve --config ` + config + ` --listen ` + EngineAddr + ` --admin-socket ` + AdminSocket + `
Environment=NODE_ENV=production
Environment=BUN_RUNTIME_TRANSPILER_CACHE_PATH=0
RuntimeDirectory=tiffin-auth
RuntimeDirectoryMode=0700
Restart=always
RestartSec=2
MemoryMax=384M
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true

[Install]
WantedBy=multi-user.target
`
}

// Provision installs Bun (pinned, checksummed), writes the engine bundle and
// runs it as the tiffin-auth systemd unit. Idempotent.
func (*Module) Provision(ctx context.Context, s *platform.System) error {
	rel, ok := bunRelease[runtime.GOARCH]
	if !ok {
		return fmt.Errorf("auth engine: no Bun build pinned for %s", runtime.GOARCH)
	}
	js, err := EngineBundle()
	if err != nil {
		return err
	}
	dir := Dir(nil)
	if err := os.MkdirAll(filepath.Join(dir, "engine"), 0o755); err != nil {
		return err
	}
	bunDir := filepath.Join(dir, "bun-"+BunVersion)
	bun := filepath.Join(bunDir, "bun")
	if _, err := os.Stat(bun); err != nil {
		zip, err := s.Fetch(ctx, "https://github.com/oven-sh/bun/releases/download/bun-v"+BunVersion+"/"+rel.asset+".zip", rel.sha256)
		if err != nil {
			return err
		}
		tmp := bunDir + ".tmp"
		_ = os.RemoveAll(tmp)
		if _, err := s.Run(ctx, "unzip", "-q", "-o", zip, "-d", tmp); err != nil {
			return err
		}
		_ = os.RemoveAll(bunDir) // a partial earlier attempt
		if err := os.MkdirAll(bunDir, 0o755); err != nil {
			return err
		}
		if err := os.Rename(filepath.Join(tmp, rel.asset, "bun"), bun); err != nil {
			return err
		}
		_ = os.RemoveAll(tmp)
		if err := os.Chmod(bun, 0o755); err != nil {
			return err
		}
	}
	if err := s.User(ctx, EngineUser, dir); err != nil {
		return err
	}
	script := filepath.Join(dir, "engine", "tiffin-auth.js")
	changed, err := s.WriteFile(script, js, 0o644)
	if err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		return err
	}
	// The config holds database passwords and secrets: engine-readable only.
	if _, err := s.Run(ctx, "chown", EngineUser+":"+EngineUser, dir); err != nil {
		return err
	}
	if _, err := os.Stat(ConfigPath(nil)); err == nil {
		if _, err := s.Run(ctx, "chown", EngineUser+":"+EngineUser, ConfigPath(nil)); err != nil {
			return err
		}
	}
	if err := s.Unit(ctx, Unit, unitFile(bun, script, ConfigPath(nil))); err != nil {
		return err
	}
	if _, err := os.Stat(AdminSocket); err != nil && !changed {
		changed = true // running but its admin socket is gone: restart it
	}
	if changed {
		s.Log("restarting the auth engine")
		if _, err := s.Run(ctx, "systemctl", "restart", Unit); err != nil {
			return err
		}
	}
	return s.WaitTCP(ctx, EngineAddr, 30*time.Second)
}

// Needs: base installs unzip; the engine unit starts after Postgres, and the
// email module's SMTP server should be up before auth sends mail.
func (*Module) Needs() []string { return []string{"base", "postgres", "email"} }
