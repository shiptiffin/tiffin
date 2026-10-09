package update

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/shiptiffin/tiffin/internal/install"
	"github.com/shiptiffin/tiffin/internal/mod/backup"
	"github.com/shiptiffin/tiffin/internal/mod/postgres"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/version"
)

func init() { platform.Register(mod) }

// Module is the automatic Tiffin update.
type Module struct {
	mu sync.Mutex
	u  *updater // nil off-box
}

var mod = &Module{}

func (*Module) Name() string { return "update" }
func (*Module) Order() int   { return 90 }

func (m *Module) updater() *updater {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.u
}

// Start runs the loop: once every module started (so the audit log and
// alerts work), then every minute.
func (m *Module) Start(ctx context.Context, p *platform.Platform) error {
	if p.DB == nil || p.Restart == nil {
		return nil // not a box
	}
	u := newUpdater(sysBox{p}, version.Version, p.Notify, func(msg string, args ...any) { p.Log.Info(msg, args...) })
	m.mu.Lock()
	m.u = u
	m.mu.Unlock()
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for !p.Started() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
		for {
			u.tick(ctx, p, time.Now())
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return nil
}

// sysBox is the real box.
type sysBox struct{ p *platform.Platform }

func (sysBox) HoldPostgres() (func(), error) { return postgres.HoldUpdates() }

// backupWait is how long an update waits for a backup already running.
var backupWait = 30 * time.Minute

func (b sysBox) Backup(ctx context.Context) (string, error) {
	deadline := time.Now().Add(backupWait)
	for {
		set, err := backup.Take(ctx, b.p, "incremental", "pre-update")
		if errors.Is(err, backup.ErrBusy) && time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(15 * time.Second):
			}
			continue
		}
		if err != nil {
			return "", err
		}
		return set.ID, nil
	}
}

// Launch starts the update in a unit of its own: it restarts tiffin, which
// would stop anything this service started.
func (sysBox) Launch(ctx context.Context, id, bin string, restartEdge bool) error {
	args := []string{"--unit", "tiffin-update-" + strings.TrimPrefix(id, "upd_"), "--collect", "--quiet",
		install.BinLink, "self-update", "--staged", id}
	if restartEdge {
		args = append(args, "--restart-edge")
	}
	if out, err := exec.CommandContext(ctx, "systemd-run", append(args, bin)...).CombinedOutput(); err != nil {
		return fmt.Errorf("systemd-run: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
