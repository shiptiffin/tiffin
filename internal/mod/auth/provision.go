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
	"strconv"
	"time"

	"github.com/shiptiffin/tiffin/internal/platform"
)

// The engine, bundled by `make auth-engine` (bun build of packages/auth-engine,
// gzipped). It is JavaScript run by a pinned Bun: ~0.6 MB in the tiffin
// binary instead of ~40 MB per architecture for a compiled Bun executable.
//
//go:embed engine/tiffin-auth.js.gz
var engineBundle []byte

// BunVersion is the Bun release that runs the engine: the one app builds
// use (runtime.BunVersion), so one Bun is tested and patched.
const BunVersion = "1.4.4"

// bunRelease is the pinned Bun download for each architecture.
var bunRelease = map[string]struct{ asset, sha256 string }{
	"arm64": {"bun-linux-aarch64", "baa6bb36f51d6e58561e02bbea825038a9df454f673d6d776e8bbf86f8c7850e"},
	"amd64": {"bun-linux-x64", "bf8ccbfb2b0b80159e0f9a6d15c55317add8077534dbc6130a3edb52fccb395c"},
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
// engineMemoryMB is the auth engine's memory cap on a box with memMB of
// RAM: 384 MB up to 6 GB, a sixteenth of RAM above that, at most 1 GB.
func engineMemoryMB(memMB int) int { return max(384, min(memMB/16, 1024)) }

func unitFile(bun, script, config string, memMB int) string {
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
MemoryMax=` + strconv.Itoa(engineMemoryMB(memMB)) + `M
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
	if err := s.Unit(ctx, Unit, unitFile(bun, script, ConfigPath(nil), platform.MemoryMB())); err != nil {
		return err
	}
	// The unit runs this Bun now: drop the ones earlier versions pinned.
	old, _ := filepath.Glob(filepath.Join(dir, "bun-*"))
	for _, d := range old {
		if d != bunDir {
			_ = os.RemoveAll(d)
		}
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
