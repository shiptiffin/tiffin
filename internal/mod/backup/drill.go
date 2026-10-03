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
	"github.com/btahir/tiffin/internal/platform"
	"github.com/jackc/pgx/v5"
)

// A restore drill proves a backup can be restored: it restores the backup's
// Postgres part into a scratch data directory on the data disk, starts a
// temporary Postgres on it (unix socket only, archiving off, its own port),
// counts every table of every database, compares the result with the table
// list recorded when the backup was taken and with the live cluster, then
// stops the temporary server and deletes the scratch directory. Nothing live
// changes: the live cluster, its WAL archive and the backup repository are
// only read.
const (
	// DrillRoot holds one scratch directory per running drill.
	DrillRoot = "/var/lib/tiffin/drill"
	// DrillStale is how long the box may go without a passing drill before
	// the restore-drill check warns.
	DrillStale = 14 * 24 * time.Hour
	// drillPort names the scratch server's socket (it listens on no TCP port).
	drillPort = 5499
	// diskHeadroom: a drill needs this much free space per restored byte.
	diskHeadroom = 1.2
	nsDrills     = "backup.drills"
	keepDrills   = 30
	// Limits on the slow parts, so a drill always ends.
	drillLockWait    = 30 * time.Minute
	drillStartWait   = 30 * time.Minute
	countTimeout     = time.Minute
	verifyBudget     = 10 * time.Minute
	liveExactBelow   = 8 << 20 // live tables smaller than this are counted exactly
	liveCountTimeout = 5 * time.Second
	maxTableDetail   = 100
	drillWaitMax     = 50 * time.Second
)

// drillRoot is DrillRoot; tests point it elsewhere.
var drillRoot = DrillRoot

// Drill statuses.
const (
	DrillRunning = "running"
	DrillPassed  = "passed"
	DrillFailed  = "failed"
)

// BackupDrillSeconds are a drill's timings.
type BackupDrillSeconds struct {
	Restore float64 `json:"restore" doc:"pgBackRest restoring the backup into the scratch directory"`
	Start   float64 `json:"start" doc:"The scratch Postgres replaying WAL to the backup's end and opening"`
	Verify  float64 `json:"verify" doc:"Counting every table and comparing"`
	Total   float64 `json:"total" doc:"Whole drill, including waiting and clean-up"`
}

// BackupDrillTable is one table's row count in the restored copy.
type BackupDrillTable struct {
	Table    string `json:"table" doc:"schema.table"`
	Rows     int64  `json:"rows" doc:"Rows in the restored copy (-1 when it could not be counted)"`
	Exact    bool   `json:"exact" doc:"false when counting took too long and Rows is the planner's estimate"`
	LiveRows int64  `json:"liveRows" doc:"Rows in the live table now: exact for small tables, otherwise the planner's estimate (-1: unknown or not live)"`
	Error    string `json:"error,omitempty" doc:"Why the restored table could not be read"`
}

// BackupDrillDatabase is one database's verification.
type BackupDrillDatabase struct {
	Name       string             `json:"name"`
	OK         bool               `json:"ok"`
	Tables     int                `json:"tables" doc:"User tables in the restored copy"`
	Rows       int64              `json:"rows" doc:"Rows in the restored copy (sum over its tables)"`
	LiveTables int                `json:"liveTables" doc:"User tables in the live database now (0 when it no longer exists)"`
	LiveRows   int64              `json:"liveRows" doc:"Approximate rows in the live database now"`
	Missing    []string           `json:"missing" doc:"Tables the database had when the backup was taken that the restored copy lacks"`
	Problems   []string           `json:"problems,omitempty" doc:"Everything else that went wrong, in plain words"`
	Counts     []BackupDrillTable `json:"counts,omitempty" doc:"Per-table counts (at most 100 per database, largest first)"`
}

// BackupDrill is one restore drill.
type BackupDrill struct {
	ID               string                `json:"id" doc:"Drill ID (dr_...)"`
	Backup           string                `json:"backup" doc:"The backup set restored"`
	BackupLabel      string                `json:"backupLabel" doc:"Its pgBackRest label"`
	BackupTakenAt    time.Time             `json:"backupTakenAt"`
	BackupAgeSeconds int64                 `json:"backupAgeSeconds" doc:"How old the backup was when the drill started"`
	Trigger          string                `json:"trigger" enum:"manual,schedule"`
	Status           string                `json:"status" enum:"running,passed,failed"`
	Phase            string                `json:"phase" doc:"What it is doing now (while running)"`
	StartedAt        time.Time             `json:"startedAt"`
	FinishedAt       time.Time             `json:"finishedAt,omitzero"`
	Seconds          BackupDrillSeconds    `json:"seconds"`
	BackupBytes      int64                 `json:"backupBytes" doc:"Size of the Postgres cluster in the backup"`
	RestoredBytes    int64                 `json:"restoredBytes" doc:"Bytes restored into the scratch directory (grows while restoring)"`
	Percent          int                   `json:"percent" doc:"Rough progress of the restore phase, 0-100"`
	ComparedWith     string                `json:"comparedWith" enum:"backup,live" doc:"backup: the table list recorded when the backup was taken; live: the live cluster (older backups have no list)"`
	Databases        []BackupDrillDatabase `json:"databases"`
	Message          string                `json:"message" doc:"The outcome in plain words"`
	Hint             string                `json:"hint,omitempty" doc:"What to do next"`
	Scratch          string                `json:"scratch" doc:"Scratch directory (deleted when the drill ends)"`
}

// ErrDrillRunning is returned when a drill is already running.
var ErrDrillRunning = errors.New("a restore drill is already running; wait for it (tiffin backups drills) or cancel it")

// DiskError is returned when the data disk is too full for a drill.
type DiskError struct{ Need, Free int64 }

func (e *DiskError) Error() string {
	return fmt.Sprintf("not enough free space on the data disk for a restore drill: it needs %s (the backup's %s plus 20%%) and %s is free",
		human(e.Need), human(int64(float64(e.Need)/diskHeadroom)), human(e.Free))
}

// drillState is the one running drill and the box's context.
var drillState struct {
	mu      sync.Mutex
	ctx     context.Context
	running *drillRun
}

// freeBytes reports the space available on the filesystem holding path
// (the nearest existing parent).
var freeBytes = func(path string) (int64, error) {
	for {
		var st syscall.Statfs_t
		err := syscall.Statfs(path, &st)
		if err == nil {
			return int64(uint64(st.Bavail) * uint64(st.Bsize)), nil
		}
		parent := filepath.Dir(path)
		if parent == path {
			return 0, err
		}
		path = parent
	}
}

// diskCheck refuses a drill that could fill the data disk.
func diskCheck(b *Backup) error {
	need := int64(float64(b.Postgres.SizeBytes) * diskHeadroom)
	free, err := freeBytes(drillRoot)
	if err != nil {
		return fmt.Errorf("checking free space: %w", err)
	}
	if free < need {
		return &DiskError{Need: need, Free: free}
	}
	return nil
}

// drillable explains why a backup cannot be drilled ("" when it can).
func drillable(b *Backup) string {
	switch {
	case b.Status != "ok":
		return "backup " + b.ID + " did not succeed (" + b.Status + "); pick another from `tiffin backups list`"
	case b.Postgres.Label == "":
		return "backup " + b.ID + " has no Postgres part to restore"
	}
	return ""
}

func saveDrill(ctx context.Context, p *platform.Platform, d *BackupDrill) error {
	raw, _ := json.Marshal(d)
	return p.DB.KVPut(ctx, nsDrills, d.ID, raw)
}

// ListDrills returns drills, newest first.
func ListDrills(ctx context.Context, p *platform.Platform) ([]BackupDrill, error) {
	all, err := p.DB.KVList(ctx, nsDrills)
	if err != nil {
		return nil, err
	}
	out := make([]BackupDrill, 0, len(all))
	for _, raw := range all {
		var d BackupDrill
		if json.Unmarshal(raw, &d) == nil {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

func getDrill(ctx context.Context, p *platform.Platform, id string) (*BackupDrill, error) {
	raw, ok, err := p.DB.KVGet(ctx, nsDrills, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, os.ErrNotExist
	}
	var d BackupDrill
	return &d, json.Unmarshal(raw, &d)
}

func pruneDrills(ctx context.Context, p *platform.Platform) {
	list, err := ListDrills(ctx, p)
	if err != nil {
		return
	}
	for i := keepDrills; i < len(list); i++ {
		if list[i].Status != DrillRunning {
			_ = p.DB.KVDelete(ctx, nsDrills, list[i].ID)
		}
	}
}

// drillRun is one drill in progress.
type drillRun struct {
	p      *platform.Platform
	b      *Backup
	cancel context.CancelFunc
	done   chan struct{}
	start  time.Time

	mu   sync.Mutex
	rec  *BackupDrill
	last time.Time
}

func (r *drillRun) set(f func(d *BackupDrill), force bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f(r.rec)
	if force || time.Since(r.last) > time.Second {
		r.last = time.Now()
		cp := *r.rec
		_ = saveDrill(context.Background(), r.p, &cp)
	}
}

func (r *drillRun) phase(s string) { r.set(func(d *BackupDrill) { d.Phase = s }, true) }

func (r *drillRun) snapshot() *BackupDrill {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *r.rec
	return &cp
}

// StartDrill starts a restore drill of b in the background and returns its
// record (status running) and a channel closed when it ends. It refuses at
// once (no record) when b cannot be drilled, a drill is running or the
// data disk is too full.
func StartDrill(ctx context.Context, p *platform.Platform, b *Backup, trigger string) (*BackupDrill, <-chan struct{}, error) {
	if why := drillable(b); why != "" {
		return nil, nil, errors.New(why)
	}
	drillState.mu.Lock()
	if drillState.running != nil {
		drillState.mu.Unlock()
		return nil, nil, ErrDrillRunning
	}
	if err := diskCheck(b); err != nil {
		drillState.mu.Unlock()
		return nil, nil, err
	}
	now := time.Now().UTC()
	id := ids.New("dr")
	rec := &BackupDrill{ID: id, Backup: b.ID, BackupLabel: b.Postgres.Label, BackupTakenAt: b.StartedAt,
		BackupAgeSeconds: int64(now.Sub(b.StartedAt).Seconds()), Trigger: trigger, Status: DrillRunning, Phase: "starting",
		StartedAt: now, BackupBytes: b.Postgres.SizeBytes, Databases: []BackupDrillDatabase{}, Scratch: filepath.Join(drillRoot, id)}
	if err := saveDrill(ctx, p, rec); err != nil {
		drillState.mu.Unlock()
		return nil, nil, err
	}
	base := drillState.ctx
	if base == nil {
		base = context.Background()
	}
	rctx, cancel := context.WithCancel(base)
	r := &drillRun{p: p, b: b, cancel: cancel, done: make(chan struct{}), start: time.Now(), rec: rec}
	drillState.running = r
	drillState.mu.Unlock()
	go r.run(rctx)
	return r.snapshot(), r.done, nil
}

// CancelDrill stops the running drill with this ID; it cleans up and ends
// as failed. It reports whether such a drill was running.
func CancelDrill(id string) bool {
	drillState.mu.Lock()
	defer drillState.mu.Unlock()
	if r := drillState.running; r != nil && r.rec.ID == id {
		r.cancel()
		return true
	}
	return false
}

// runningDrill returns the running drill's live record, if id is running.
func runningDrill(id string) *BackupDrill {
	drillState.mu.Lock()
	r := drillState.running
	drillState.mu.Unlock()
	if r == nil || r.rec.ID != id {
		return nil
	}
	return r.snapshot()
}

func secs(d time.Duration) float64 { return float64(d.Milliseconds()/100) / 10 }

func (r *drillRun) run(ctx context.Context) {
	defer func() {
		drillState.mu.Lock()
		drillState.running = nil
		drillState.mu.Unlock()
		r.cancel()
		close(r.done)
	}()
	dir := r.rec.Scratch
	var srv *scratchServer
	err := r.steps(ctx, dir, &srv)

	// Clean up whatever happened, even when cancelled.
	r.phase("cleaning up")
	var notes []string
	if srv != nil {
		if serr := srv.stop(); serr != nil {
			notes = append(notes, "stopping the scratch Postgres: "+serr.Error())
		}
	}
	if rerr := os.RemoveAll(dir); rerr != nil {
		notes = append(notes, "the scratch directory "+dir+" could not be deleted ("+rerr.Error()+"); it is removed when the box restarts")
	}

	r.set(func(d *BackupDrill) {
		d.Phase, d.FinishedAt = "", time.Now().UTC()
		d.Seconds.Total = secs(time.Since(r.start))
		switch {
		case errors.Is(err, context.Canceled):
			d.Status, d.Message = DrillFailed, "cancelled before it finished; nothing was verified (the scratch copy was deleted)"
			d.Hint = "start a new drill with `tiffin backups drill`"
		case err != nil:
			d.Status, d.Message = DrillFailed, err.Error()
			if d.Hint == "" {
				d.Hint = "the backup may not be restorable: take a fresh full backup (`tiffin backup --kind full`) and drill it (`tiffin backups drill`); if that fails too, check `journalctl -u tiffin` on the box"
			}
		default:
			d.Status, d.Message, d.Hint = verdict(d)
		}
		if len(notes) > 0 {
			d.Message += " Note: " + strings.Join(notes, "; ") + "."
		}
	}, true)
	final := r.snapshot()
	if final.Status == DrillPassed {
		r.p.Log.Info("backup: restore drill passed", "drill", final.ID, "backup", final.Backup, "seconds", final.Seconds.Total)
	} else {
		r.p.Log.Error("backup: restore drill failed", "drill", final.ID, "backup", final.Backup, "message", final.Message)
	}
	pruneDrills(context.WithoutCancel(ctx), r.p)
}

// steps runs the drill up to verification. srv is set once the scratch
// server runs, so the caller can stop it.
func (r *drillRun) steps(ctx context.Context, dir string, srv **scratchServer) error {
	// Backups, restores, exports and imports run one at a time; the drill
	// holds the same lock while it reads the repository (restore and WAL
	// replay), so pgBackRest never expires what it is reading.
	release, err := r.lock(ctx)
	if err != nil {
		return err
	}
	locked := true
	defer func() {
		if locked {
			release()
		}
	}()
	if err := diskCheck(r.b); err != nil {
		r.set(func(d *BackupDrill) {
			d.Hint = "free space on the data disk (`tiffin box` shows what uses it), then drill again"
		}, false)
		return err
	}

	// 1. Restore into the scratch directory.
	r.phase("restoring the backup into a scratch directory")
	t := time.Now()
	data := filepath.Join(dir, "data")
	if err := scratchDirs(dir); err != nil {
		return fmt.Errorf("preparing the scratch directory: %w", err)
	}
	stop := make(chan struct{})
	go func() {
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				n := datakit.DirSize(data)
				r.set(func(d *BackupDrill) {
					d.RestoredBytes = n
					if d.BackupBytes > 0 {
						d.Percent = int(min(99, n*100/d.BackupBytes))
					}
				}, false)
			}
		}
	}()
	_, rerr := pgbackrest(ctx, "--pg1-path="+data, "--set="+r.b.Postgres.Label, "--type=immediate", "--target-action=promote",
		"--archive-mode=off", "--log-level-console=warn", "--log-level-file=off", "restore")
	close(stop)
	n := datakit.DirSize(data)
	r.set(func(d *BackupDrill) { d.Seconds.Restore, d.RestoredBytes = secs(time.Since(t)), n }, true)
	if rerr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("restoring backup %s (pgBackRest set %s) into the scratch directory failed: %s", r.b.ID, r.b.Postgres.Label, clean(rerr))
	}
	r.set(func(d *BackupDrill) { d.Percent = 100 }, false)

	// 2. Start a private Postgres on it; it replays WAL to the end of the
	// backup and opens (archiving off: nothing reaches the repository).
	r.phase("starting a scratch Postgres on the restored copy")
	t = time.Now()
	s, err := startScratch(dir)
	if err != nil {
		return fmt.Errorf("starting the scratch Postgres: %w", err)
	}
	*srv = s
	if err := s.waitReady(ctx, drillStartWait); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("the restored copy of backup %s did not start: %w", r.b.ID, err)
	}
	r.set(func(d *BackupDrill) { d.Seconds.Start = secs(time.Since(t)) }, true)
	release()
	locked = false

	// 3. Verify.
	r.phase("counting every table and comparing")
	t = time.Now()
	dbs, from, err := verify(ctx, r.b, s)
	r.set(func(d *BackupDrill) { d.Seconds.Verify, d.Databases, d.ComparedWith = secs(time.Since(t)), dbs, from }, true)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("verifying the restored copy: %w", err)
	}
	return nil
}

// lock waits for a running backup, restore, export or import.
func (r *drillRun) lock(ctx context.Context) (func(), error) {
	deadline := time.Now().Add(drillLockWait)
	told := false
	for {
		if run.TryLock() {
			return run.Unlock, nil
		}
		if time.Now().After(deadline) {
			return nil, errors.New("a backup, restore, export or import kept running for 30 minutes; the drill gave up waiting")
		}
		if !told {
			r.phase("waiting for a running backup or restore to finish")
			told = true
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// verdict decides a verified drill's status and message.
func verdict(d *BackupDrill) (status, msg, hint string) {
	var bad []string
	tables, rows, estimated := 0, int64(0), 0
	for _, db := range d.Databases {
		tables += db.Tables
		rows += db.Rows
		for _, c := range db.Counts {
			if !c.Exact && c.Error == "" {
				estimated++
			}
		}
		if db.OK {
			continue
		}
		var why []string
		if len(db.Missing) > 0 {
			why = append(why, plural(len(db.Missing), "missing table")+": "+strings.Join(clip(db.Missing, 5), ", "))
		}
		why = append(why, clip(db.Problems, 3)...)
		bad = append(bad, db.Name+" ("+strings.Join(why, "; ")+")")
	}
	if len(d.Databases) == 0 {
		return DrillFailed, "the restored copy has no databases", "check `tiffin backups list` and take a fresh backup"
	}
	if len(bad) > 0 {
		return DrillFailed, fmt.Sprintf("Restored backup %s but %s did not check out: %s.", d.Backup, plural(len(bad), "database"), strings.Join(bad, "; ")),
			"the backup is incomplete or damaged: take a fresh full backup (`tiffin backup --kind full`) and drill it (`tiffin backups drill`)"
	}
	msg = fmt.Sprintf("Restored backup %s (taken %s before the drill, %s) in %s; the scratch Postgres started in %s; %s, %s and %s verified in %s.",
		d.Backup, ago(time.Duration(d.BackupAgeSeconds)*time.Second), human(d.RestoredBytes), fmtSecs(d.Seconds.Restore), fmtSecs(d.Seconds.Start),
		plural(len(d.Databases), "database"), plural(tables, "table"), plural64(rows, "row"), fmtSecs(d.Seconds.Verify))
	if estimated > 0 {
		msg += fmt.Sprintf(" %s took too long to count, so their rows are estimates.", plural(estimated, "table"))
	}
	if d.ComparedWith == "live" {
		msg += " This backup predates table lists in backups, so the restore was compared with the live cluster only."
	}
	return DrillPassed, msg, ""
}

// ---- the scratch directory and server ----

func pgIDs() (int, int, error) {
	u, err := user.Lookup("postgres")
	if err != nil {
		return 0, 0, err
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return uid, gid, nil
}

// scratchDirs creates dir with data/ and sock/ owned by postgres (0700) and
// conf/ for the server's configuration.
func scratchDirs(dir string) error {
	uid, gid, err := pgIDs()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(drillRoot, 0o755); err != nil {
		return err
	}
	for _, d := range []string{dir, filepath.Join(dir, "data"), filepath.Join(dir, "sock"), filepath.Join(dir, "conf")} {
		if err := os.Mkdir(d, 0o700); err != nil && !os.IsExist(err) {
			return err
		}
	}
	// The postgres user must reach data/ and sock/ through dir.
	if err := os.Chmod(dir, 0o711); err != nil {
		return err
	}
	for _, d := range []string{"data", "sock"} {
		if err := os.Chown(filepath.Join(dir, d), uid, gid); err != nil {
			return err
		}
	}
	return os.Chmod(filepath.Join(dir, "conf"), 0o755)
}

// scratchConf is the scratch server's configuration: no TCP, a private
// socket, archiving off, no background extensions, small memory, no
// durability (it is thrown away).
func scratchConf(dir string) string {
	return fmt.Sprintf(`# Tiffin restore drill: a throwaway copy of a backup. Never archived.
data_directory = '%[1]s/data'
hba_file = '%[1]s/conf/pg_hba.conf'
ident_file = '%[1]s/conf/pg_ident.conf'
listen_addresses = ''
port = %[2]d
unix_socket_directories = '%[1]s/sock'
unix_socket_permissions = 0700
max_connections = 20
shared_buffers = 32MB
work_mem = 4MB
maintenance_work_mem = 16MB
huge_pages = off
hot_standby = off
archive_mode = off
shared_preload_libraries = ''
fsync = off
synchronous_commit = off
full_page_writes = off
autovacuum = off
jit = off
max_wal_size = 1GB
logging_collector = off
log_destination = 'stderr'
log_timezone = 'UTC'
timezone = 'UTC'
datestyle = 'iso, mdy'
`, dir, drillPort)
}

// scratchArgs override anything the restored postgresql.auto.conf says
// (command-line settings win over every file).
func scratchArgs(dir string) []string {
	return []string{"-c", "config_file=" + filepath.Join(dir, "conf", "postgresql.conf"),
		"-c", "archive_mode=off", "-c", "hot_standby=off", "-c", "shared_preload_libraries=",
		"-c", "listen_addresses=", "-c", "port=" + strconv.Itoa(drillPort),
		"-c", "unix_socket_directories=" + filepath.Join(dir, "sock")}
}

type scratchServer struct {
	dir    string
	cmd    *exec.Cmd
	exited chan struct{}
	err    error
	logf   *os.File
}

func startScratch(dir string) (*scratchServer, error) {
	files := map[string]string{
		"postgresql.conf": scratchConf(dir),
		"pg_hba.conf":     "# Tiffin restore drill: only root and postgres can reach the socket directory.\nlocal all all trust\n",
		"pg_ident.conf":   "",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, "conf", name), []byte(body), 0o644); err != nil {
			return nil, err
		}
	}
	uid, gid, err := pgIDs()
	if err != nil {
		return nil, err
	}
	logf, err := os.OpenFile(filepath.Join(dir, "postgres.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	// Not CommandContext: the server is stopped gracefully by stop().
	cmd := exec.Command(postgres.BinDir+"/postgres", scratchArgs(dir)...)
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}}
	if err := cmd.Start(); err != nil {
		logf.Close()
		return nil, err
	}
	s := &scratchServer{dir: dir, cmd: cmd, exited: make(chan struct{}), logf: logf}
	go func() {
		s.err = cmd.Wait()
		logf.Close()
		close(s.exited)
	}()
	return s, nil
}

// logTail returns the end of the scratch server's log.
func (s *scratchServer) logTail() string {
	raw, _ := os.ReadFile(filepath.Join(s.dir, "postgres.log"))
	out := strings.TrimSpace(string(raw))
	if len(out) > 1200 {
		out = "…" + out[len(out)-1200:]
	}
	return out
}

func (s *scratchServer) connect(ctx context.Context, db string) (*pgx.Conn, error) {
	cfg, err := pgx.ParseConfig(fmt.Sprintf("host=%s port=%d user=postgres dbname=%s sslmode=disable connect_timeout=5", filepath.Join(s.dir, "sock"), drillPort, db))
	if err != nil {
		return nil, err
	}
	cfg.RuntimeParams["application_name"] = "tiffin-drill"
	return pgx.ConnectConfig(ctx, cfg)
}

// waitReady waits until recovery has finished and the server accepts connections.
func (s *scratchServer) waitReady(ctx context.Context, d time.Duration) error {
	deadline := time.Now().Add(d)
	var last error
	for {
		select {
		case <-s.exited:
			return fmt.Errorf("the scratch Postgres exited (%v): %s", s.err, s.logTail())
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		c, err := s.connect(ctx, "postgres")
		if err == nil {
			var rec bool
			err = c.QueryRow(ctx, "SELECT pg_is_in_recovery()").Scan(&rec)
			c.Close(ctx)
			if err == nil && !rec {
				return nil
			}
			if err == nil {
				err = errors.New("still recovering")
			}
		}
		last = err
		if time.Now().After(deadline) {
			return fmt.Errorf("not ready after %s (%v): %s", d, last, s.logTail())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// stop shuts the server down fast (SIGINT), then kills it.
func (s *scratchServer) stop() error {
	select {
	case <-s.exited:
		return nil
	default:
	}
	_ = s.cmd.Process.Signal(syscall.SIGINT)
	select {
	case <-s.exited:
		return nil
	case <-time.After(time.Minute):
	}
	_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
	select {
	case <-s.exited:
		return nil
	case <-time.After(10 * time.Second):
		return errors.New("the scratch Postgres did not stop")
	}
}

// cleanupScratch removes scratch directories a crash or restart left
// behind, killing a scratch server still running in one, and marks drills
// that were running as failed. It runs when the box starts.
func cleanupScratch(ctx context.Context, p *platform.Platform) {
	entries, _ := os.ReadDir(drillRoot)
	for _, e := range entries {
		dir := filepath.Join(drillRoot, e.Name())
		killScratch(dir)
		if err := os.RemoveAll(dir); err != nil {
			p.Log.Error("backup: removing a leftover drill directory", "dir", dir, "err", err)
		} else {
			p.Log.Info("backup: removed a leftover drill directory", "dir", dir)
		}
	}
	list, _ := ListDrills(ctx, p)
	for _, d := range list {
		if d.Status != DrillRunning {
			continue
		}
		d.Status, d.Phase, d.FinishedAt = DrillFailed, "", time.Now().UTC()
		d.Message = "interrupted: the box restarted during the drill (its scratch copy was deleted)"
		d.Hint = "start a new drill with `tiffin backups drill`"
		_ = saveDrill(ctx, p, &d)
	}
}

// killScratch kills the postmaster of a leftover scratch directory, if it
// is still running (systemd normally stops it with the service).
func killScratch(dir string) {
	raw, err := os.ReadFile(filepath.Join(dir, "data", "postmaster.pid"))
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(strings.SplitN(string(raw), "\n", 2)[0]))
	if err != nil || pid <= 1 {
		return
	}
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || !strings.Contains(string(cmdline), dir) {
		return // gone, or the pid now belongs to something else
	}
	_ = syscall.Kill(pid, syscall.SIGQUIT)
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// ---- schedule and health ----

// drillDue reports whether the schedule wants a drill now: drills are on,
// and the last one is older than the interval (or failed a day ago, or
// none ran and the box has been up for a day).
func drillDue(s BackupSchedule, last *BackupDrill, since, now time.Time) bool {
	if !s.DrillEnabled || s.DrillEveryDays < 1 {
		return false
	}
	if last == nil {
		return now.Sub(since) >= 24*time.Hour
	}
	if last.Status == DrillRunning {
		return false
	}
	age := now.Sub(last.StartedAt)
	return age >= time.Duration(s.DrillEveryDays)*24*time.Hour || (last.Status == DrillFailed && age >= 24*time.Hour)
}

// drillCheck is the restore-drill health check.
func drillCheck(list []BackupDrill, s BackupSchedule, since, now time.Time) platform.Check {
	c := platform.Check{Name: "restore-drill", OK: true}
	var last, lastDone *BackupDrill
	for i := range list {
		if last == nil {
			last = &list[i]
		}
		if lastDone == nil && list[i].Status != DrillRunning {
			lastDone = &list[i]
		}
	}
	off := ""
	if !s.DrillEnabled {
		off = "; scheduled drills are off (turn them on with `tiffin backups schedule --drill-enabled`)"
	}
	running := ""
	if last != nil && last.Status == DrillRunning {
		running = "; a drill is running now (" + last.Phase + ")"
	}
	if lastDone == nil {
		if now.Sub(since) < DrillStale {
			c.Detail = "no restore drill yet; the first runs a day after the box starts (or run `tiffin backups drill`)" + running + off
			return c
		}
		c.OK = false
		c.Detail = "no restore drill has run; run `tiffin backups drill` to prove the backups restore" + running + off
		return c
	}
	when := ago(now.Sub(lastDone.FinishedAt)) + " ago"
	if lastDone.Status == DrillFailed {
		c.OK = false
		c.Detail = "restore drill failed " + when + ": " + clipText(lastDone.Message, 300) + "; see `tiffin backups drills get " + lastDone.ID + "`" + running + off
		return c
	}
	c.Detail = fmt.Sprintf("restore drill passed %s (restored in %s)", when, fmtSecs(lastDone.Seconds.Restore)) + running + off
	if now.Sub(lastDone.StartedAt) > DrillStale {
		c.OK = false
		c.Detail += "; that is more than 14 days ago: run `tiffin backups drill`"
	}
	return c
}

// ---- words ----

func plural(n int, word string) string { return plural64(int64(n), word) }

func plural64(n int64, word string) string {
	s := commas(n) + " " + word
	if n != 1 {
		s += "s"
	}
	return s
}

func commas(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func human(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func fmtSecs(s float64) string {
	switch {
	case s < 10:
		return strconv.FormatFloat(s, 'f', 1, 64) + " s"
	case s < 120:
		return strconv.FormatFloat(s, 'f', 0, 64) + " s"
	default:
		return fmt.Sprintf("%d min %d s", int(s)/60, int(s)%60)
	}
}

// ago renders a duration as "2 days", "5 hours", "3 minutes", "40 seconds".
func ago(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return plural(int(d/(24*time.Hour)), "day")
	case d >= 2*time.Hour:
		return plural(int(d/time.Hour), "hour")
	case d >= 2*time.Minute:
		return plural(int(d/time.Minute), "minute")
	default:
		return plural(int(d/time.Second), "second")
	}
}

func clip(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return append(append([]string{}, s[:n]...), fmt.Sprintf("and %d more", len(s)-n))
}

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// clean shortens a command error for a message.
func clean(err error) string {
	s := strings.Join(strings.Fields(err.Error()), " ")
	return clipText(s, 600)
}
