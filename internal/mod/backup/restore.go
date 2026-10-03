package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/mod/datakit"
	"github.com/btahir/tiffin/internal/mod/postgres"
	"github.com/btahir/tiffin/internal/mod/valkey"
	"github.com/btahir/tiffin/internal/platform"
)

// Restore targets.
const (
	TargetPostgres = "postgres"
	TargetValkey   = "valkey"
	TargetFiles    = "files"
)

// Overwrite is one thing a restore replaces.
type BackupOverwrite struct {
	Target string   `json:"target"`
	What   string   `json:"what"`
	Items  []string `json:"items,omitempty"`
}

// RestorePreview is what a restore would do. Its hash is the confirm value.
type BackupRestorePreview struct {
	Backup     string            `json:"backup"`
	TakenAt    time.Time         `json:"takenAt"`
	Targets    []string          `json:"targets"`
	Overwrites []BackupOverwrite `json:"overwrites"`
	Safety     string            `json:"safety"`
	Downtime   string            `json:"downtime"`
}

// Restored is the outcome of a restore.
type BackupRestored struct {
	Backup     string   `json:"backup"`
	Targets    []string `json:"targets"`
	SafetyID   string   `json:"safetyBackup" doc:"Backup of the state just before the restore; restore it to go back"`
	DurationMs int64    `json:"durationMs"`
}

func normalizeTargets(t []string) ([]string, error) {
	if len(t) == 0 {
		t = []string{TargetPostgres, TargetValkey}
	}
	out := []string{}
	for _, x := range t {
		switch x {
		case TargetPostgres, TargetValkey, TargetFiles:
			if !slices.Contains(out, x) {
				out = append(out, x)
			}
		default:
			return nil, fmt.Errorf("unknown restore target %q (postgres, valkey, files)", x)
		}
	}
	slices.Sort(out)
	return out, nil
}

// Key is what the confirm value covers: the backup, the targets and which
// databases would be replaced. Live counts in the preview may drift.
func (pv *BackupRestorePreview) Key() any {
	k := []string{pv.Backup}
	k = append(k, pv.Targets...)
	for _, o := range pv.Overwrites {
		k = append(k, o.Items...)
	}
	return k
}

// Preview describes what restoring b would overwrite.
func Preview(ctx context.Context, p *platform.Platform, b *Backup, targets []string) (*BackupRestorePreview, error) {
	pv := &BackupRestorePreview{Backup: b.ID, TakenAt: b.StartedAt, Targets: targets,
		Safety:   "a backup of the current state is taken first; its ID is returned so you can restore back",
		Downtime: "Postgres and Valkey are stopped while their data is replaced (usually seconds); apps lose their connections and reconnect"}
	for _, t := range targets {
		switch t {
		case TargetPostgres:
			admin, err := postgres.Admin(ctx, "postgres")
			if err != nil {
				return nil, err
			}
			rows, err := admin.Query(ctx, `SELECT datname FROM pg_database WHERE datname NOT IN ('template0','template1') ORDER BY datname`)
			var dbs []string
			if err == nil {
				for rows.Next() {
					var n string
					if rows.Scan(&n) == nil {
						dbs = append(dbs, n)
					}
				}
				rows.Close()
			}
			admin.Close(ctx)
			pv.Overwrites = append(pv.Overwrites, BackupOverwrite{Target: t, Items: dbs,
				What: fmt.Sprintf("the whole Postgres cluster: every database (%d now) goes back to its state at %s; changes since then are lost and databases created since are removed", len(dbs), b.StartedAt.Format(time.RFC3339))})
		case TargetValkey:
			c, err := valkey.Admin(ctx)
			if err != nil {
				return nil, err
			}
			n, _ := c.Int(ctx, "DBSIZE")
			c.Close()
			pv.Overwrites = append(pv.Overwrites, BackupOverwrite{Target: t,
				What: fmt.Sprintf("all Valkey keys of every project (%d now) are replaced by the %s snapshot", n, b.StartedAt.Format(time.RFC3339))})
		case TargetFiles:
			var items []string
			for name := range b.Files {
				items = append(items, name+" ("+b.Files[name].Detail+")")
			}
			slices.Sort(items)
			pv.Overwrites = append(pv.Overwrites, BackupOverwrite{Target: t, Items: items, What: "registered files and directories are replaced by their copies in the backup"})
		}
	}
	return pv, nil
}

// Restore puts a backup back. It takes a safety backup first.
func Restore(ctx context.Context, p *platform.Platform, b *Backup, targets []string) (*BackupRestored, error) {
	if !run.TryLock() {
		return nil, ErrBusy
	}
	defer run.Unlock()
	start := time.Now()
	if b.Status != "ok" {
		return nil, errors.New("only successful backups can be restored")
	}
	safety, err := take(ctx, p, "incremental", "pre-restore")
	if err != nil {
		return nil, fmt.Errorf("safety backup before restoring failed, nothing was changed: %w", err)
	}
	out := &BackupRestored{Backup: b.ID, Targets: targets, SafetyID: safety.ID}
	for _, t := range targets {
		var err error
		switch t {
		case TargetPostgres:
			err = restorePostgres(ctx, b)
		case TargetValkey:
			err = restoreValkey(ctx, b)
		case TargetFiles:
			err = restoreFiles(ctx, b)
		}
		if err != nil {
			return out, fmt.Errorf("restore %s: %w (safety backup %s holds the state from before)", t, err, safety.ID)
		}
		p.Log.Info("backup: restored", "backup", b.ID, "target", t)
	}
	// Re-converge every project: roles, ACL users and extensions created
	// after the backup come back; passwords match what apps have.
	if projects, err := p.DB.ListProjects(ctx); err == nil {
		for _, pr := range projects {
			p.ReconcileProject(pr)
		}
	}
	out.DurationMs = time.Since(start).Milliseconds()
	return out, nil
}

func systemctl(ctx context.Context, args ...string) error {
	_, err := datakit.Run(ctx, "systemctl", args...)
	return err
}

func restorePostgres(ctx context.Context, b *Backup) error {
	if b.Postgres.Label == "" {
		return errors.New("this backup has no Postgres part")
	}
	if err := systemctl(ctx, "stop", postgres.UnitName); err != nil {
		return err
	}
	_, rerr := pgbackrest(ctx, "--delta", "--set="+b.Postgres.Label, "--type=immediate", "--target-action=promote", "--log-level-console=warn", "restore")
	// Start again whatever happened: after a failed restore the old data is still there (--delta only rewrites what differs).
	if err := systemctl(ctx, "start", postgres.UnitName); err != nil && rerr == nil {
		rerr = err
	}
	if err := postgres.WaitReady(ctx, 5*time.Minute); err != nil && rerr == nil {
		rerr = err
	}
	return rerr
}

// restoreValkey replaces the dataset with the backup's RDB.
func restoreValkey(ctx context.Context, b *Backup) error {
	src := filepath.Join(b.dir(), "valkey.rdb")
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("this backup has no Valkey snapshot: %w", err)
	}
	return RestoreValkeyRDB(ctx, src)
}

// RestoreValkeyRDB replaces Valkey's whole dataset with the RDB snapshot at
// src (box imports use it too). With AOF on, Valkey loads only the AOF at
// startup, so the RDB is loaded by a temporary server with AOF off, which
// then rewrites a fresh AOF from it. Valkey is down meanwhile.
func RestoreValkeyRDB(ctx context.Context, src string) error {
	if err := systemctl(ctx, "stop", valkey.UnitName); err != nil {
		return err
	}
	defer func() { _ = systemctl(context.WithoutCancel(ctx), "start", valkey.UnitName) }()
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	aof := filepath.Join(valkey.DataDir, "appendonlydir")
	aside := aof + ".pre-restore-" + stamp
	if _, err := os.Stat(aof); err == nil {
		if err := os.Rename(aof, aside); err != nil {
			return err
		}
	}
	rdb := filepath.Join(valkey.DataDir, "dump.rdb")
	if _, err := datakit.Run(ctx, "cp", "--reflink=auto", src, rdb); err != nil {
		return err
	}
	if _, err := datakit.Run(ctx, "chown", "valkey:valkey", rdb); err != nil {
		return err
	}
	if err := rewriteAOF(ctx); err != nil {
		// Put the old AOF back so the server comes up with the old data.
		os.RemoveAll(aof)
		_ = os.Rename(aside, aof)
		return err
	}
	os.RemoveAll(aside)
	return nil
}

func rewriteAOF(ctx context.Context) error {
	sock := filepath.Join(valkey.SocketDir, "restore.sock")
	cmdCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := asUser(cmdCtx, "valkey", valkey.InstallDir+"/bin/valkey-server", valkey.ConfDir+"/valkey.conf",
			"--appendonly", "no", "--port", "0", "--unixsocket", sock, "--save", "")
		done <- err
	}()
	pw, err := os.ReadFile(valkey.ConfDir + "/admin.pass")
	if err != nil {
		return err
	}
	var c *valkey.Client
	for i := 0; i < 600; i++ {
		select {
		case err := <-done:
			return fmt.Errorf("temporary server exited: %v", err)
		default:
		}
		if c, err = valkey.Dial(ctx, "unix", sock, "tiffin", strings.TrimSpace(string(pw))); err == nil {
			if _, err = c.Do(ctx, "PING"); err == nil {
				break
			}
			c.Close()
			c = nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if c == nil {
		return fmt.Errorf("temporary server did not answer: %v", err)
	}
	defer c.Close()
	if _, err := c.Do(ctx, "CONFIG", "SET", "appendonly", "yes"); err != nil {
		return err
	}
	for {
		raw, err := c.String(ctx, "INFO", "persistence")
		if err != nil {
			return err
		}
		if strings.Contains(raw, "aof_rewrite_in_progress:0") && strings.Contains(raw, "aof_rewrite_scheduled:0") && strings.Contains(raw, "aof_enabled:1") {
			if !strings.Contains(raw, "aof_last_bgrewrite_status:ok") {
				return errors.New("rewriting the AOF from the restored snapshot failed")
			}
			break
		}
		select {
		case <-cmdCtx.Done():
			return cmdCtx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	_, _ = c.Do(ctx, "SHUTDOWN", "NOSAVE")
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		cancel()
		<-done
	}
	return nil
}

func restoreFiles(ctx context.Context, b *Backup) error {
	for name, part := range b.Files {
		src := filepath.Join(b.dir(), "files", name)
		dst := part.Detail
		if dst == "" || !filepath.IsAbs(dst) {
			continue
		}
		aside := dst + ".pre-restore"
		os.RemoveAll(aside)
		if _, err := os.Stat(dst); err == nil {
			if err := os.Rename(dst, aside); err != nil {
				return err
			}
		}
		if _, err := datakit.Run(ctx, "cp", "-a", "--reflink=auto", src, dst); err != nil {
			_ = os.Rename(aside, dst)
			return err
		}
		os.RemoveAll(aside)
	}
	return nil
}
