package backup

import (
	"context"
	"encoding/json"
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
	// TargetPlatform is the platform state (projects, settings, secrets,
	// tokens, people, deploy records) and the box key: for recovering onto
	// a box with no projects.
	TargetPlatform = "platform"
)

// Restore sources.
const (
	SourceLocal   = "local"
	SourceOffsite = "offsite"
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
	From       string            `json:"from" enum:"local,offsite" doc:"local: this box's copy; offsite: the copy in the bucket"`
	TakenAt    time.Time         `json:"takenAt"`
	Targets    []string          `json:"targets"`
	Overwrites []BackupOverwrite `json:"overwrites"`
	Safety     string            `json:"safety"`
	Downtime   string            `json:"downtime"`
}

// Restored is the outcome of a restore.
type BackupRestored struct {
	Backup     string   `json:"backup"`
	From       string   `json:"from" enum:"local,offsite"`
	Targets    []string `json:"targets"`
	SafetyID   string   `json:"safetyBackup" doc:"Backup of the state just before the restore; restore it to go back (empty on a box with no projects)"`
	DurationMs int64    `json:"durationMs"`
	Restarting bool     `json:"restarting,omitempty" doc:"The box's service restarts now to swap in the restored files or state (seconds)"`
	Notes      []string `json:"notes,omitempty" doc:"What else happened, in plain words"`
}

func normalizeTargets(t, def []string) ([]string, error) {
	if len(t) == 0 {
		t = def
	}
	out := []string{}
	for _, x := range t {
		if x == "all" {
			out = []string{TargetFiles, TargetPlatform, TargetPostgres, TargetValkey}
			continue
		}
		switch x {
		case TargetPostgres, TargetValkey, TargetFiles, TargetPlatform:
			if !slices.Contains(out, x) {
				out = append(out, x)
			}
		default:
			return nil, fmt.Errorf("unknown restore target %q (postgres, valkey, files, platform, all)", x)
		}
	}
	slices.Sort(out)
	return out, nil
}

// defaultTargets: Postgres and Valkey; everything on a box with no
// projects (recovering a lost box onto a new one).
func defaultTargets(ctx context.Context, p *platform.Platform) []string {
	if projects, err := p.DB.ListProjects(ctx); err == nil && len(projects) == 0 {
		return []string{TargetFiles, TargetPlatform, TargetPostgres, TargetValkey}
	}
	return []string{TargetPostgres, TargetValkey}
}

// Key is what the confirm value covers: the backup, the targets and which
// databases would be replaced. Live counts in the preview may drift.
func (pv *BackupRestorePreview) Key() any {
	k := []string{pv.Backup, pv.From}
	k = append(k, pv.Targets...)
	for _, o := range pv.Overwrites {
		k = append(k, o.Items...)
	}
	return k
}

// Preview describes what restoring b would overwrite.
func Preview(ctx context.Context, p *platform.Platform, b *Backup, from string, targets []string) (*BackupRestorePreview, error) {
	pv := &BackupRestorePreview{Backup: b.ID, From: from, TakenAt: b.StartedAt, Targets: targets,
		Safety:   "a backup of the current state is taken first; its ID is returned so you can restore back",
		Downtime: "Postgres and Valkey are stopped while their data is replaced (usually seconds); apps lose their connections and reconnect"}
	projects, _ := p.DB.ListProjects(ctx)
	if from == SourceOffsite && len(projects) == 0 {
		pv.Safety = "none: this box has no projects to lose"
	}
	restart := false
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
			what := fmt.Sprintf("the whole Postgres cluster: every database (%d now) goes back to its state at %s; changes since then are lost and databases created since are removed", len(dbs), b.StartedAt.Format(time.RFC3339))
			if from == SourceOffsite {
				what += "; it is restored from the off-box copy (pgBackRest repo2)"
			}
			pv.Overwrites = append(pv.Overwrites, BackupOverwrite{Target: t, Items: dbs, What: what})
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
			restart = true
		case TargetPlatform:
			pv.Overwrites = append(pv.Overwrites, BackupOverwrite{Target: t, Items: projects,
				What: "the platform state becomes the backup's: projects, their settings, secrets and deploy records, tokens, people and passkeys, and the box key; " +
					"this box's owner token, domain, backup settings and off-box destination are kept. Apps are not in backups: deploy them again afterwards"})
			restart = true
		}
	}
	if restart {
		pv.Downtime += "; the box's service restarts once to swap the files and state in (seconds)"
	}
	return pv, nil
}

// restoreFrom is where a restore reads a set from.
type restoreFrom struct {
	b     *Backup
	from  string
	dir   string // the set's files: its local directory, or a download of the off-box copy
	repo  int    // pgBackRest repository: 1 local, 2 off-box
	label string // its Postgres backup in that repository
}

// checkTargets refuses what the box cannot do.
func checkTargets(ctx context.Context, p *platform.Platform, targets []string) error {
	if slices.Contains(targets, TargetPlatform) {
		if projects, err := p.DB.ListProjects(ctx); err != nil || len(projects) > 0 {
			return fmt.Errorf("the platform target replaces every project's settings, so it is only for a box with no projects (recovering a lost box onto a new one); this box has %d", len(projects))
		}
	}
	if (slices.Contains(targets, TargetPlatform) || slices.Contains(targets, TargetFiles)) && p.Restart == nil {
		return errors.New("files and the platform state are swapped in by a restart of the box's service, and this process is not it")
	}
	return nil
}

// Restore puts a backup back from this box. It takes a safety backup first.
func Restore(ctx context.Context, p *platform.Platform, b *Backup, targets []string) (*BackupRestored, error) {
	if !run.TryLock() {
		return nil, ErrBusy
	}
	defer run.Unlock()
	if b.Status != "ok" {
		return nil, errors.New("only successful backups can be restored")
	}
	return restore(ctx, p, restoreFrom{b: b, from: SourceLocal, dir: b.dir(), repo: 1, label: b.Postgres.Label}, targets, true)
}

// restore puts a set back; the caller holds run.
func restore(ctx context.Context, p *platform.Platform, src restoreFrom, targets []string, safety bool) (*BackupRestored, error) {
	start := time.Now()
	if err := checkTargets(ctx, p, targets); err != nil {
		return nil, err
	}
	out := &BackupRestored{Backup: src.b.ID, From: src.from, Targets: targets}
	// Sets taken before now no longer match the database: they are not
	// copied off the box any more (a copy pairs a set with the database of
	// the moment; the safety backup below is never copied either).
	if err := p.DB.KVPut(ctx, nsOffsite, "notBefore", []byte(time.Now().UTC().Format(time.RFC3339Nano))); err != nil {
		return nil, err
	}
	if safety {
		s, err := take(ctx, p, "incremental", "pre-restore")
		if err != nil {
			return nil, fmt.Errorf("safety backup before restoring failed, nothing was changed: %w", err)
		}
		out.SafetyID = s.ID
	}
	failed := func(t string, err error) error {
		if out.SafetyID != "" {
			return fmt.Errorf("restore %s: %w (safety backup %s holds the state from before)", t, err, out.SafetyID)
		}
		return fmt.Errorf("restore %s: %w", t, err)
	}
	// Files and the state are staged first (nothing live changes), and
	// swapped in by the restart at the end.
	plan := platform.PendingImport{Import: "backup " + src.b.ID, Aside: restoreAside}
	staged := false
	if slices.Contains(targets, TargetFiles) || slices.Contains(targets, TargetPlatform) {
		if _, err := os.Stat(platform.PendingImportPath(p.DataRoot)); err == nil {
			return out, errors.New("a box import is waiting for the service to restart; restore after it")
		}
		_ = os.RemoveAll(restoreStage)
		_ = os.RemoveAll(restoreAside)
		staged = true
	}
	for _, t := range targets {
		var err error
		switch t {
		case TargetFiles:
			err = stageFiles(ctx, src.dir, src.b, &plan)
		case TargetPlatform:
			err = stagePlatform(ctx, p, src.dir, &plan)
		}
		if err != nil {
			return out, failed(t, err)
		}
	}
	for _, t := range targets {
		var err error
		switch t {
		case TargetPostgres:
			var note string
			note, err = restorePostgres(ctx, p, src)
			if note != "" {
				out.Notes = append(out.Notes, note)
			}
		case TargetValkey:
			err = restoreValkey(ctx, src.dir)
		default:
			continue
		}
		if err != nil {
			return out, failed(t, err)
		}
		p.Log.Info("backup: restored", "backup", src.b.ID, "from", src.from, "target", t)
	}
	if staged {
		if err := writePlan(p.DataRoot, plan); err != nil {
			return out, err
		}
		// Swapped in by the next start, before any module opens them
		// (platform.ApplyPendingImport); every project reconciles then.
		out.Restarting = true
		out.DurationMs = time.Since(start).Milliseconds()
		p.Restart("swap in the files and state of backup " + src.b.ID)
		return out, nil
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

// restorePostgres restores the cluster from the set's Postgres backup. From
// the off-box repository, WAL comes from there too, and a cluster that is
// not this box's (a lost box's, restored onto a new one) takes over the
// local repository and the off-box copies.
func restorePostgres(ctx context.Context, p *platform.Platform, src restoreFrom) (string, error) {
	if src.label == "" {
		return "", errors.New("this backup has no Postgres part")
	}
	if err := systemctl(ctx, "stop", postgres.UnitName); err != nil {
		return "", err
	}
	args := []string{"--repo=" + strconv.Itoa(src.repo), "--delta", "--set=" + src.label, "--type=immediate", "--target-action=promote", "--log-level-console=warn"}
	if src.repo == 2 {
		args = append(args, `--recovery-option=restore_command=pgbackrest --stanza=`+Stanza+` --repo=2 archive-get %f "%p"`)
	}
	_, rerr := pgbackrest(ctx, append(args, "restore")...)
	note := ""
	if rerr == nil && src.repo == 2 {
		note, rerr = adoptCluster(ctx, p)
	}
	// Start again whatever happened: after a failed restore the old data is still there (--delta only rewrites what differs).
	if err := systemctl(ctx, "start", postgres.UnitName); err != nil && rerr == nil {
		rerr = err
	}
	if err := postgres.WaitReady(ctx, 5*time.Minute); err != nil && rerr == nil {
		rerr = err
	}
	return note, rerr
}

// adoptCluster runs after a restore from the bucket, Postgres stopped: when
// the local repository belongs to another cluster (this box's own, before
// it took on a lost box's), it is started again for the restored one, and
// the off-box copies resume (the bucket's backups are the restored
// cluster's own).
func adoptCluster(ctx context.Context, p *platform.Platform) (string, error) {
	note := ""
	out, err := datakit.Run(ctx, postgres.BinDir+"/pg_controldata", "-D", postgres.DataDir)
	if err != nil {
		return "", err
	}
	var sysID string
	for _, l := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(l, ":"); ok && strings.TrimSpace(k) == "Database system identifier" {
			sysID = strings.TrimSpace(v)
		}
	}
	ids, err := stanzaSystemIDs(ctx, 1)
	if err != nil {
		return "", err
	}
	if len(ids) > 0 && !slices.Contains(ids, sysID) {
		for _, step := range [][]string{{"stop"}, {"--repo=1", "--force", "stanza-delete"}, {"start"}, {"--no-online", "stanza-create"}} {
			if _, err := pgbackrest(ctx, append([]string{"--log-level-console=warn"}, step...)...); err != nil {
				_, _ = pgbackrest(ctx, "start")
				return "", fmt.Errorf("starting the local backup repository again for the restored cluster: %s", clean(err))
			}
		}
		note = "the local backup repository held this box's earlier cluster; it was emptied and started again for the restored one (its old sets are gone)"
		p.Log.Info("backup: local repository started again for the restored cluster")
	}
	c, s := current()
	if c != nil && c.State != OffsiteActive {
		c.State = OffsiteActive
		if err := writeRepo2Conf(c, s); err != nil {
			return note, err
		}
		if err := saveOffsite(ctx, p, c, s); err != nil {
			return note, err
		}
	} else if c != nil {
		if err := writeRepo2Conf(c, s); err != nil {
			return note, err
		}
	}
	return note, nil
}

// restoreValkey replaces the dataset with the set's RDB.
func restoreValkey(ctx context.Context, dir string) error {
	src := filepath.Join(dir, "valkey.rdb")
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

// Where a files restore stages its copies and moves the replaced files
// (the data disk, so the swap is a rename).
var (
	restoreStage = Root + "/restore-staged"
	restoreAside = Root + "/pre-restore"
)

// stageFiles copies the backup's files next to the live ones and adds them
// to the plan the next start of the service swaps in, before any module
// opens them: observe and analytics hold their SQLite databases open in
// this process, so replacing them under it would keep it reading (and
// writing) the old ones. The units that write a set are stopped meanwhile.
func stageFiles(ctx context.Context, setDir string, b *Backup, plan *platform.PendingImport) error {
	inc := included()
	for name, part := range b.Files {
		src := filepath.Join(setDir, "files", name)
		dst := part.Detail
		if dst == "" || !filepath.IsAbs(dst) {
			continue
		}
		staged := filepath.Join(restoreStage, name)
		_ = os.MkdirAll(restoreStage, 0o700)
		if _, err := datakit.Run(ctx, "cp", "-a", "--reflink=auto", src, staged); err != nil {
			return err
		}
		plan.Swaps = append(plan.Swaps, platform.PendingSwap{From: staged, To: dst})
		if fi, err := os.Stat(staged); err == nil && !fi.IsDir() {
			// A single database: its old WAL must not be replayed onto the restored one.
			for _, s := range []string{"-wal", "-shm", "-journal"} {
				plan.Swaps = append(plan.Swaps, platform.PendingSwap{To: dst + s})
			}
		}
		for _, u := range inc[name].units {
			if !slices.Contains(plan.StopUnits, u) {
				plan.StopUnits, plan.StartUnits = append(plan.StopUnits, u), append(plan.StartUnits, u)
			}
		}
	}
	return nil
}

// writePlan leaves the plan for the next start of the service; it refuses
// when another (a box import's) is waiting.
func writePlan(dataRoot string, plan platform.PendingImport) error {
	path := platform.PendingImportPath(dataRoot)
	if _, err := os.Stat(path); err == nil {
		return errors.New("a box import is waiting for the service to restart; restore after it")
	}
	raw, _ := json.MarshalIndent(plan, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

// stagePlatform prepares the set's platform state and box key for the swap
// (adoptState keeps what belongs to this box) and adds them to the plan.
func stagePlatform(ctx context.Context, p *platform.Platform, setDir string, plan *platform.PendingImport) error {
	src := filepath.Join(setDir, "platform")
	for _, f := range []string{"state.db", "secrets.key"} {
		if _, err := os.Stat(filepath.Join(src, f)); err != nil {
			return fmt.Errorf("this backup has no platform state: %w", err)
		}
	}
	dir := filepath.Join(restoreStage, "platform")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, f := range []string{"state.db", "secrets.key"} {
		if _, err := datakit.Run(ctx, "cp", "--reflink=auto", filepath.Join(src, f), filepath.Join(dir, f)); err != nil {
			return err
		}
	}
	if err := adoptState(ctx, p, filepath.Join(dir, "state.db"), filepath.Join(dir, "secrets.key")); err != nil {
		return fmt.Errorf("preparing the state: %w", err)
	}
	plan.Swaps = append(plan.Swaps,
		platform.PendingSwap{From: filepath.Join(dir, "state.db"), To: filepath.Join(p.Home, "state.db")},
		platform.PendingSwap{To: filepath.Join(p.Home, "state.db-wal")},
		platform.PendingSwap{To: filepath.Join(p.Home, "state.db-shm")},
		platform.PendingSwap{From: filepath.Join(dir, "secrets.key"), To: filepath.Join(p.Home, "secrets.key")})
	return nil
}

// finishFilesRestore reports how the swap of a files restore went and
// removes what it staged and replaced (the safety backup has the latter).
func finishFilesRestore(p *platform.Platform) {
	raw, err := os.ReadFile(platform.PendingResultPath(p.DataRoot))
	var res platform.PendingResult
	if err == nil && json.Unmarshal(raw, &res) == nil && strings.HasPrefix(res.Import, "backup ") {
		if res.OK {
			p.Log.Info("backup: restored files swapped in", "backup", strings.TrimPrefix(res.Import, "backup "), "items", res.Swapped)
		} else {
			p.Log.Error("backup: swapping in restored files failed; the box kept its files", "backup", strings.TrimPrefix(res.Import, "backup "), "err", res.Error)
		}
		_ = os.Remove(platform.PendingResultPath(p.DataRoot))
	}
	_ = os.RemoveAll(restoreStage)
	_ = os.RemoveAll(restoreAside)
	_ = os.RemoveAll(offsiteStage)
}
