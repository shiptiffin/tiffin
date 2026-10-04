// Package backup is the Tiffin backup module. A backup is a set taken
// together: a pgBackRest backup of the whole Postgres cluster (full or
// incremental, with WAL archiving, to a local repository on the data
// disk), a copy of Valkey's RDB snapshot, an online copy of the platform
// state database (plus the box key that decrypts its secrets) and any files
// other modules register with Include. Sets are taken on a schedule (daily
// full, hourly incremental by default) and on demand, and restored with a
// confirm step. Off-box copies are not supported yet.
package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/btahir/tiffin/internal/ids"
	"github.com/btahir/tiffin/internal/mod/datakit"
	"github.com/btahir/tiffin/internal/mod/postgres"
	"github.com/btahir/tiffin/internal/mod/valkey"
	"github.com/btahir/tiffin/internal/platform"
)

// Layout on the data disk.
const (
	Root     = "/var/lib/tiffin/backups"
	RepoPath = Root + "/pgbackrest"
	SetsPath = Root + "/sets"
	LogPath  = "/var/lib/tiffin/logs/pgbackrest"
	ConfPath = "/etc/pgbackrest/pgbackrest.conf"
	Stanza   = "tiffin"
	// MaxAge is how old the newest good backup may be before the box is unhealthy.
	MaxAge = 26 * time.Hour
)

const (
	nsSets = "backup.sets" // id → Backup
	nsMeta = "backup"      // "schedule", "since"
)

func init() { platform.Register(&Module{}) }

// Module implements the backup module.
type Module struct{}

func (*Module) Name() string { return "backup" }
func (*Module) Order() int   { return 60 }

var (
	incMu    sync.Mutex
	includes = map[string]include{}
	// run serialises backups and restores.
	run sync.Mutex
)

type include struct {
	path  string
	units []string
}

// Include adds a file or directory to every backup set under name (for
// example Include("storage", "/var/lib/tiffin/storage")). Call it from init().
// Directories are copied with reflinks, so large ones cost little; SQLite
// databases in them are snapshotted with VACUUM INTO. The "files" restore
// target puts them back while the tiffin service and units (the systemd
// units that write them) are stopped.
func Include(name, path string, units ...string) {
	incMu.Lock()
	defer incMu.Unlock()
	includes[name] = include{path, units}
}

func included() map[string]include {
	incMu.Lock()
	defer incMu.Unlock()
	out := make(map[string]include, len(includes))
	for k, v := range includes {
		out[k] = v
	}
	return out
}

// Provision installs pgBackRest and creates the stanza for the cluster.
func (*Module) Provision(ctx context.Context, s *platform.System) error {
	if err := s.Apt(ctx, "pgbackrest"); err != nil {
		return err
	}
	if _, err := s.Sh(ctx, `install -d -m 0755 /etc/pgbackrest
install -d -m 0755 `+Root+`
install -d -o postgres -g postgres -m 0750 `+RepoPath+` `+LogPath+`
install -d -m 0700 `+SetsPath); err != nil {
		return err
	}
	if _, err := s.WriteFile(ConfPath, []byte(pgbackrestConf), 0o644); err != nil {
		return err
	}
	// Idempotent: creates the stanza or checks it matches the cluster.
	_, err := s.Run(ctx, "runuser", "-u", "postgres", "--", "pgbackrest", "--stanza="+Stanza, "--log-level-console=warn", "stanza-create")
	return err
}

const pgbackrestConf = `# Managed by Tiffin (tiffin provision). Edits are overwritten.
[global]
repo1-path=` + RepoPath + `
repo1-retention-full-type=count
repo1-retention-full=7
start-fast=y
delta=y
compress-type=zst
compress-level=3
process-max=2
log-level-console=warn
log-level-file=info
log-path=` + LogPath + `

[` + Stanza + `]
pg1-path=` + postgres.DataDir + `
pg1-socket-path=` + postgres.SocketDir + `
pg1-port=5432
`

// Schedule controls automatic backups.
type BackupSchedule struct {
	Enabled               bool `json:"enabled" doc:"Take backups automatically"`
	FullEveryHours        int  `json:"fullEveryHours" minimum:"1" maximum:"720" doc:"Hours between full backups (default 24)"`
	IncrementalEveryHours int  `json:"incrementalEveryHours" minimum:"0" maximum:"168" doc:"Hours between incremental backups; 0 turns them off (default 1)"`
	RetainFull            int  `json:"retainFull" minimum:"1" maximum:"60" doc:"Full backups to keep, with their incrementals (default 7)"`
	DrillEnabled          bool `json:"drillEnabled" doc:"Run restore drills automatically: restore the newest backup into a scratch copy and verify it (default on)"`
	DrillEveryDays        int  `json:"drillEveryDays" minimum:"1" maximum:"90" doc:"Days between scheduled restore drills (default 7)"`
}

// DefaultSchedule is daily full, hourly incremental, a week kept, and a
// weekly restore drill.
var DefaultSchedule = BackupSchedule{Enabled: true, FullEveryHours: 24, IncrementalEveryHours: 1, RetainFull: 7, DrillEnabled: true, DrillEveryDays: 7}

func getSchedule(ctx context.Context, p *platform.Platform) BackupSchedule {
	s := DefaultSchedule
	if raw, ok, _ := p.DB.KVGet(ctx, nsMeta, "schedule"); ok {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

// Part is one component of a backup set.
type BackupPart struct {
	SizeBytes int64  `json:"sizeBytes"`
	Detail    string `json:"detail,omitempty"`
}

// Backup is one backup set.
type Backup struct {
	ID         string    `json:"id" doc:"Backup ID (bk_...)"`
	Kind       string    `json:"kind" enum:"full,incremental" doc:"pgBackRest backup type"`
	Trigger    string    `json:"trigger" enum:"schedule,manual,pre-restore" doc:"What started it"`
	Status     string    `json:"status" enum:"running,ok,failed"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt,omitzero"`
	DurationMs int64     `json:"durationMs"`
	// Postgres is the pgBackRest backup (label, logical size of the cluster).
	Postgres struct {
		Label     string `json:"label"`
		Type      string `json:"type"`
		SizeBytes int64  `json:"sizeBytes" doc:"Size of the cluster"`
		RepoBytes int64  `json:"repoBytes" doc:"What this backup added to the repository (compressed)"`
	} `json:"postgres"`
	Valkey   BackupPart            `json:"valkey"`
	Platform BackupPart            `json:"platform"`
	Files    map[string]BackupPart `json:"files,omitempty" doc:"Paths registered by other modules"`
}

func (b *Backup) dir() string { return filepath.Join(SetsPath, b.ID) }

func save(ctx context.Context, p *platform.Platform, b *Backup) error {
	raw, _ := json.Marshal(b)
	if err := p.DB.KVPut(ctx, nsSets, b.ID, raw); err != nil {
		return err
	}
	if err := os.MkdirAll(b.dir(), 0o700); err == nil {
		pretty, _ := json.MarshalIndent(b, "", "  ")
		_ = os.WriteFile(filepath.Join(b.dir(), "backup.json"), pretty, 0o600)
	}
	return nil
}

// List returns backups, newest first.
func List(ctx context.Context, p *platform.Platform) ([]Backup, error) {
	all, err := p.DB.KVList(ctx, nsSets)
	if err != nil {
		return nil, err
	}
	out := make([]Backup, 0, len(all))
	for _, raw := range all {
		var b Backup
		if json.Unmarshal(raw, &b) == nil {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

func get(ctx context.Context, p *platform.Platform, id string) (*Backup, error) {
	raw, ok, err := p.DB.KVGet(ctx, nsSets, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, os.ErrNotExist
	}
	var b Backup
	return &b, json.Unmarshal(raw, &b)
}

// ErrBusy is returned when another backup or restore is running.
var ErrBusy = errors.New("another backup, restore or restore drill is running; try again when it finishes")

// Exclusive holds the lock backups and restores take, so none runs until
// release is called (box exports and imports use it). It returns ErrBusy
// when one is running now.
func Exclusive() (release func(), err error) {
	if !run.TryLock() {
		return nil, ErrBusy
	}
	return run.Unlock, nil
}

// Take runs a backup now. kind is "full" or "incremental" (pgBackRest takes
// a full one anyway when none exists yet).
func Take(ctx context.Context, p *platform.Platform, kind, trigger string) (*Backup, error) {
	if !run.TryLock() {
		return nil, ErrBusy
	}
	defer run.Unlock()
	return take(ctx, p, kind, trigger)
}

func take(ctx context.Context, p *platform.Platform, kind, trigger string) (*Backup, error) {
	b := &Backup{ID: ids.New("bk"), Kind: kind, Trigger: trigger, Status: "running", StartedAt: time.Now().UTC()}
	if err := save(ctx, p, b); err != nil {
		return nil, err
	}
	err := takeParts(ctx, p, b)
	b.FinishedAt = time.Now().UTC()
	b.DurationMs = b.FinishedAt.Sub(b.StartedAt).Milliseconds()
	b.Status = "ok"
	if err != nil {
		b.Status, b.Error = "failed", err.Error()
		p.Log.Error("backup failed", "id", b.ID, "err", err)
	}
	if serr := save(context.WithoutCancel(ctx), p, b); serr != nil && err == nil {
		err = serr
	}
	if err == nil {
		prune(context.WithoutCancel(ctx), p)
	}
	return b, err
}

func takeParts(ctx context.Context, p *platform.Platform, b *Backup) error {
	dir := b.dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	sched := getSchedule(ctx, p)
	// 1. Postgres: pgBackRest, whole cluster, with retention applied after.
	typ := "incr"
	if b.Kind == "full" {
		typ = "full"
	}
	if _, err := pgbackrest(ctx, "--type="+typ, "--repo1-retention-full="+strconv.Itoa(sched.RetainFull), "backup"); err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	info, err := repoInfo(ctx)
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	if len(info) == 0 {
		return errors.New("postgres: pgBackRest reported no backups after backing up")
	}
	last := info[len(info)-1]
	b.Postgres.Label, b.Postgres.Type = last.Label, last.Type
	b.Postgres.SizeBytes, b.Postgres.RepoBytes = last.Info.Size, last.Info.Repository.Delta
	if last.Type == "full" {
		b.Kind = "full"
	} else {
		b.Kind = "incremental"
	}
	// The table list a restore drill checks the restored copy against.
	recordCatalog(ctx, b)

	// 2. Valkey: a fresh RDB snapshot, copied with a reflink.
	n, err := valkeySnapshot(ctx, filepath.Join(dir, "valkey.rdb"))
	if err != nil {
		return fmt.Errorf("valkey: %w", err)
	}
	b.Valkey = BackupPart{SizeBytes: n}

	// 3. Platform state: an online, consistent copy of the SQLite database,
	// and the box key that decrypts the secrets in it.
	pdir := filepath.Join(dir, "platform")
	if err := os.MkdirAll(pdir, 0o700); err != nil {
		return err
	}
	dst := filepath.Join(pdir, "state.db")
	os.Remove(dst)
	if _, err := p.DB.SQL().ExecContext(ctx, `VACUUM INTO ?`, dst); err != nil {
		return fmt.Errorf("platform state: %w", err)
	}
	if _, err := datakit.Run(ctx, "cp", "-a", filepath.Join(p.Home, "secrets.key"), filepath.Join(pdir, "secrets.key")); err != nil {
		return fmt.Errorf("platform key: %w", err)
	}
	b.Platform = BackupPart{SizeBytes: datakit.DirSize(pdir), Detail: "state database and box key (manual recovery)"}

	// 4. Registered files.
	for name, inc := range included() {
		path := inc.path
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if b.Files == nil {
			b.Files = map[string]BackupPart{}
		}
		fdir := filepath.Join(dir, "files")
		_ = os.MkdirAll(fdir, 0o700)
		if _, err := datakit.Run(ctx, "cp", "-a", "--reflink=auto", path, filepath.Join(fdir, name)); err != nil {
			return fmt.Errorf("files %s: %w", name, err)
		}
		if err := snapshotSQLite(ctx, path, filepath.Join(fdir, name)); err != nil {
			return fmt.Errorf("files %s: %w", name, err)
		}
		b.Files[name] = BackupPart{SizeBytes: datakit.DirSize(filepath.Join(fdir, name)), Detail: path}
	}
	return nil
}

// snapshotSQLite replaces the file copies of the SQLite databases in live
// (a database file or a directory) under dst with VACUUM INTO snapshots:
// a copied database misses commits still in its WAL, or is torn.
func snapshotSQLite(ctx context.Context, live, dst string) error {
	for _, rel := range datakit.FindSQLite(live, nil) {
		to := filepath.Join(dst, filepath.FromSlash(rel))
		for _, s := range []string{"", "-wal", "-shm", "-journal"} {
			if err := os.Remove(to + s); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if err := datakit.SQLiteSnapshot(ctx, filepath.Join(live, filepath.FromSlash(rel)), to); err != nil {
			return fmt.Errorf("snapshot %s: %w", rel, err)
		}
	}
	return nil
}

// valkeySnapshot runs BGSAVE, waits for it and copies dump.rdb to dst.
func valkeySnapshot(ctx context.Context, dst string) (int64, error) {
	c, err := valkey.Admin(ctx)
	if err != nil {
		return 0, err
	}
	defer c.Close()
	before, err := c.Int(ctx, "LASTSAVE")
	if err != nil {
		return 0, err
	}
	if _, err := c.Do(ctx, "BGSAVE"); err != nil && !strings.Contains(err.Error(), "in progress") {
		return 0, err
	}
	deadline := time.Now().Add(5 * time.Minute)
	for {
		raw, err := c.String(ctx, "INFO", "persistence")
		if err != nil {
			return 0, err
		}
		info := map[string]string{}
		for _, l := range strings.Split(raw, "\n") {
			if k, v, ok := strings.Cut(strings.TrimSpace(l), ":"); ok {
				info[k] = v
			}
		}
		last, _ := strconv.ParseInt(info["rdb_last_save_time"], 10, 64)
		if info["rdb_bgsave_in_progress"] == "0" && last >= before && (last > before || info["rdb_last_bgsave_status"] == "ok") {
			if info["rdb_last_bgsave_status"] != "ok" {
				return 0, errors.New("BGSAVE failed (see journalctl -u " + valkey.UnitName + ")")
			}
			break
		}
		if time.Now().After(deadline) {
			return 0, errors.New("BGSAVE did not finish in 5 minutes")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := datakit.Run(ctx, "cp", "--reflink=auto", filepath.Join(valkey.DataDir, "dump.rdb"), dst); err != nil {
		return 0, err
	}
	fi, err := os.Stat(dst)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// pgbackrest runs pgBackRest as the postgres user.
func pgbackrest(ctx context.Context, args ...string) (string, error) {
	args = append([]string{"--stanza=" + Stanza}, args...)
	return asUser(ctx, "postgres", "pgbackrest", args...)
}

func asUser(ctx context.Context, username, name string, args ...string) (string, error) {
	u, err := user.Lookup(username)
	if err != nil {
		return "", err
	}
	uid, _ := strconv.ParseUint(u.Uid, 10, 32)
	gid, _ := strconv.ParseUint(u.Gid, 10, 32)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=" + u.HomeDir, "USER=" + username}
	// Its own process group, so cancelling kills pgBackRest's worker
	// processes too (they would otherwise linger, orphaned).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil && cmd.Process != nil {
		// Make sure the whole group is gone before the caller cleans up.
		pg := -cmd.Process.Pid
		_ = syscall.Kill(pg, syscall.SIGKILL)
		for i := 0; i < 50 && syscall.Kill(pg, 0) == nil; i++ {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if err != nil {
		s := strings.TrimSpace(string(out))
		if len(s) > 1500 {
			s = "…" + s[len(s)-1500:]
		}
		return string(out), fmt.Errorf("%s: %w: %s", name, err, s)
	}
	return string(out), nil
}

// repoBackup is one entry of `pgbackrest info --output=json`.
type repoBackup struct {
	Label string `json:"label"`
	Type  string `json:"type"`
	Info  struct {
		Size       int64 `json:"size"`
		Repository struct {
			Delta int64 `json:"delta"`
			Size  int64 `json:"size"`
		} `json:"repository"`
	} `json:"info"`
	Timestamp struct {
		Start int64 `json:"start"`
		Stop  int64 `json:"stop"`
	} `json:"timestamp"`
}

func repoInfo(ctx context.Context) ([]repoBackup, error) {
	out, err := pgbackrest(ctx, "--output=json", "info")
	if err != nil {
		return nil, err
	}
	var stanzas []struct {
		Name   string       `json:"name"`
		Backup []repoBackup `json:"backup"`
	}
	if err := json.Unmarshal([]byte(out), &stanzas); err != nil {
		return nil, fmt.Errorf("parse pgbackrest info: %w", err)
	}
	for _, s := range stanzas {
		if s.Name == Stanza {
			return s.Backup, nil
		}
	}
	return nil, nil
}

// prune drops sets whose pgBackRest backup has expired, and failed sets
// older than a week.
func prune(ctx context.Context, p *platform.Platform) {
	info, err := repoInfo(ctx)
	if err != nil {
		return
	}
	labels := map[string]bool{}
	for _, b := range info {
		labels[b.Label] = true
	}
	list, _ := List(ctx, p)
	for _, b := range list {
		expired := b.Status == "ok" && !labels[b.Postgres.Label]
		staleFail := b.Status != "ok" && time.Since(b.StartedAt) > 7*24*time.Hour
		if expired || staleFail {
			os.RemoveAll(b.dir())
			_ = p.DB.KVDelete(ctx, nsSets, b.ID)
			p.Log.Info("backup: removed expired set", "id", b.ID)
		}
	}
}

func lastOK(list []Backup, kind string) *Backup {
	for i := range list {
		if list[i].Status == "ok" && (kind == "" || list[i].Kind == kind) {
			return &list[i]
		}
	}
	return nil
}

// due decides which backup the schedule wants now ("" for none).
func due(s BackupSchedule, list []Backup, now time.Time) string {
	if !s.Enabled {
		return ""
	}
	full := lastOK(list, "full")
	if full == nil || now.Sub(full.StartedAt) >= time.Duration(s.FullEveryHours)*time.Hour {
		return "full"
	}
	if s.IncrementalEveryHours > 0 {
		last := lastOK(list, "")
		if now.Sub(last.StartedAt) >= time.Duration(s.IncrementalEveryHours)*time.Hour {
			return "incremental"
		}
	}
	return ""
}

// since is when the box first ran the backup module.
func since(ctx context.Context, p *platform.Platform) time.Time {
	if raw, ok, _ := p.DB.KVGet(ctx, nsMeta, "since"); ok {
		if t, err := time.Parse(time.RFC3339, string(raw)); err == nil {
			return t
		}
	}
	return time.Now()
}

// Start removes what an interrupted restore drill left behind and runs the
// schedule: it checks every minute what is due. A failed scheduled backup
// is retried after 15 minutes.
func (*Module) Start(ctx context.Context, p *platform.Platform) error {
	if _, ok, _ := p.DB.KVGet(ctx, nsMeta, "since"); !ok {
		_ = p.DB.KVPut(ctx, nsMeta, "since", []byte(time.Now().UTC().Format(time.RFC3339)))
	}
	drillState.mu.Lock()
	drillState.ctx = ctx
	drillState.mu.Unlock()
	cleanupScratch(ctx, p)
	finishFilesRestore(p)
	go func() {
		var lastFail time.Time
		timer := time.NewTimer(20 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			timer.Reset(time.Minute)
			if time.Since(lastFail) < 15*time.Minute {
				continue
			}
			list, err := List(ctx, p)
			if err != nil {
				continue
			}
			sched := getSchedule(ctx, p)
			if kind := due(sched, list, time.Now()); kind != "" {
				if _, err := Take(ctx, p, kind, "schedule"); err != nil && !errors.Is(err, ErrBusy) {
					lastFail = time.Now()
				}
				continue
			}
			scheduledDrill(ctx, p, sched, list)
		}
	}()
	return nil
}

// scheduledDrill starts a drill of the newest good backup when one is due.
// A drill the box refuses (a full disk) is recorded as failed, so the
// restore-drill check says why.
func scheduledDrill(ctx context.Context, p *platform.Platform, sched BackupSchedule, list []Backup) {
	b := lastOK(list, "")
	if b == nil {
		return
	}
	drills, err := ListDrills(ctx, p)
	if err != nil {
		return
	}
	var last *BackupDrill
	if len(drills) > 0 {
		last = &drills[0]
	}
	if !drillDue(sched, last, since(ctx, p), time.Now()) {
		return
	}
	_, _, err = StartDrill(ctx, p, b, "schedule")
	var de *DiskError
	if errors.As(err, &de) {
		now := time.Now().UTC()
		d := &BackupDrill{ID: ids.New("dr"), Backup: b.ID, BackupLabel: b.Postgres.Label, BackupTakenAt: b.StartedAt,
			BackupAgeSeconds: int64(now.Sub(b.StartedAt).Seconds()), Trigger: "schedule", Status: DrillFailed, StartedAt: now, FinishedAt: now,
			BackupBytes: b.Postgres.SizeBytes, Databases: []BackupDrillDatabase{}, Message: "not started: " + err.Error(),
			Hint: "free space on the data disk (`tiffin box` shows what uses it), then run `tiffin backups drill`"}
		_ = saveDrill(ctx, p, d)
	}
}

// Checks reports the age of the newest good backup and how the last
// restore drill went.
func (*Module) Checks(ctx context.Context, p *platform.Platform) []platform.Check {
	out := backupChecks(ctx, p)
	drills, err := ListDrills(ctx, p)
	if err != nil {
		return append(out, platform.Check{Name: "restore-drill", OK: false, Detail: err.Error()})
	}
	return append(out, drillCheck(drills, getSchedule(ctx, p), since(ctx, p), time.Now()))
}

func backupChecks(ctx context.Context, p *platform.Platform) []platform.Check {
	list, err := List(ctx, p)
	if err != nil {
		return []platform.Check{{Name: "backups", OK: false, Detail: err.Error()}}
	}
	last := lastOK(list, "")
	if last == nil {
		since := time.Now()
		if raw, ok, _ := p.DB.KVGet(ctx, nsMeta, "since"); ok {
			since, _ = time.Parse(time.RFC3339, string(raw))
		}
		if time.Since(since) < MaxAge {
			return []platform.Check{{Name: "backups", OK: true, Detail: "no backup yet; the first one runs shortly after the box starts"}}
		}
		return []platform.Check{{Name: "backups", OK: false, Detail: "no successful backup yet; run `tiffin backup` and check `tiffin backups list` for errors"}}
	}
	age := time.Since(last.StartedAt)
	detail := fmt.Sprintf("last backup %s ago (%s, %s); local repository only (off-box copies come later)", age.Round(time.Minute), last.Kind, last.ID)
	if age > MaxAge {
		return []platform.Check{{Name: "backups", OK: false, Detail: detail + "; older than 26h: run `tiffin backup` and check `tiffin backups list` for errors"}}
	}
	return []platform.Check{{Name: "backups", OK: true, Detail: detail}}
}
