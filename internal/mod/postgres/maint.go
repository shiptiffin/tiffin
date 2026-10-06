package postgres

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/btahir/tiffin/internal/ids"
)

// Minor updates. The PGDG packages are outside unattended-upgrades'
// security origins and nothing restarts Tiffin's cluster after an upgrade,
// so the box does it: check for newer packages, download and install them
// while the old server keeps running (as Debian always does; minor releases
// keep the server's ABI), CHECKPOINT, PAUSE the pooler (queries wait, none
// fail), restart Postgres, wait until it answers, RESUME. Installing
// before the pause keeps it short: dpkg takes seconds, the restart a
// fraction of one, and some clients give up waiting after two seconds
// (Prisma's transaction maxWait). If the new version does not start, the
// old packages go back. In the maintenance window when one is set (tiffin
// up --reboot-window), or now with `tiffin maintenance postgres-update
// --now`; `tiffin up` does the same when the box runs a version older than
// the minimum below.

// The packages an update covers.
var (
	serverPackages = []string{"postgresql-" + Major, "postgresql-" + Major + "-pgvector", "postgresql-" + Major + "-cron"}
	clientPackages = []string{"postgresql-client-" + Major, "libpq5", "postgresql-common", "postgresql-client-common"}
	poolerPackages = []string{"pgbouncer"}
	updatePackages = slices.Concat(serverPackages, clientPackages, poolerPackages)
)

// minimums are the oldest upstream versions a box may run. `tiffin up`
// upgrades a box below one (the same pause and restart), so raising one
// ships a fix to every box. PgBouncer needs 1.21 for prepared statements
// in transaction mode.
var minimums = map[string]string{
	"postgresql-" + Major: Major + ".6",
	"pgbouncer":           "1.21",
}

// restartOrder is the order services restart in after their libraries
// were replaced (needrestart flags them; it never restarts them itself).
// Postgres comes first, with the pooler paused; the pooler's own restart
// closes client connections (apps' pools reconnect). tiffin.service is
// left out: it is a static binary (needrestart flags it for helpers it
// runs), its restart would drop every request at the edge, and `tiffin up`
// or a reboot renews it.
var restartOrder = []string{UnitName, PoolerUnit, "tiffin-valkey.service", "tiffin-auth.service", "tiffin-storage.service",
	"tiffin-metrics.service", "tiffin-logs.service", "containerd.service", "buildkit.service"}

// PGPackage is one package an update changes.
type PGPackage struct {
	Name string `json:"name"`
	From string `json:"from" doc:"Installed version"`
	To   string `json:"to" doc:"Version it updates to"`
}

// PGUpdate is one minor update the box ran.
type PGUpdate struct {
	ID           string      `json:"id"`
	At           time.Time   `json:"at"`
	Trigger      string      `json:"trigger" enum:"schedule,now,tiffin up" doc:"What started it: the maintenance window, someone (--now), or tiffin up raising a minimum version"`
	Status       string      `json:"status" enum:"ok,failed"`
	From         string      `json:"from,omitempty" doc:"Postgres version before"`
	To           string      `json:"to,omitempty" doc:"Postgres version after"`
	Packages     []PGPackage `json:"packages"`
	Restarted    []string    `json:"restarted" doc:"Services restarted, in order"`
	PauseSeconds float64     `json:"pauseSeconds" doc:"How long the pooler held queries (they waited; none failed)"`
	Summary      string      `json:"summary"`
	Error        string      `json:"error,omitempty"`
	Seconds      float64     `json:"seconds" doc:"The whole update, downloads included"`
}

// aptState is a package's installed and candidate versions ("" none).
type aptState struct{ Installed, Candidate string }

// updatePlan is what an update does.
type updatePlan struct {
	Packages        []PGPackage
	RestartPostgres bool
	RestartPooler   bool
	Restart         []string // other services, in restartOrder
	Flagged         []string // every service needrestart flagged, in restartOrder
}

func (u updatePlan) empty() bool {
	return len(u.Packages) == 0 && !u.RestartPostgres && !u.RestartPooler && len(u.Restart) == 0
}

// decide plans an update from the packages' versions and the services
// needrestart flags: upgrade every installed package with a newer
// candidate, restart Postgres when a server package changed or its
// libraries did, the pooler likewise, and other flagged box services.
func decide(states map[string]aptState, flagged []string) updatePlan {
	var p updatePlan
	for _, name := range updatePackages {
		st := states[name]
		if st.Installed == "" || st.Candidate == "" || st.Candidate == st.Installed {
			continue
		}
		p.Packages = append(p.Packages, PGPackage{Name: name, From: st.Installed, To: st.Candidate})
		p.RestartPostgres = p.RestartPostgres || slices.Contains(serverPackages, name)
		p.RestartPooler = p.RestartPooler || slices.Contains(poolerPackages, name)
	}
	for _, u := range restartOrder {
		if !slices.Contains(flagged, u) {
			continue
		}
		p.Flagged = append(p.Flagged, u)
		switch u {
		case UnitName:
			p.RestartPostgres = true
		case PoolerUnit:
			p.RestartPooler = true
		default:
			p.Restart = append(p.Restart, u)
		}
	}
	return p
}

// upstream is a Debian version's upstream part: "1:18.4-1.pgdg24.04+1" → "18.4".
func upstream(v string) string {
	if _, rest, ok := strings.Cut(v, ":"); ok {
		v = rest
	}
	if i := strings.LastIndex(v, "-"); i > 0 {
		v = v[:i]
	}
	return v
}

// olderThan reports whether upstream version v is older than min, comparing
// dotted numbers ("18.10" is newer than "18.9").
func olderThan(v, min string) bool {
	a, b := strings.Split(upstream(v), "."), strings.Split(min, ".")
	for i := 0; i < max(len(a), len(b)); i++ {
		var x, y int
		if i < len(a) {
			x = leadingInt(a[i])
		}
		if i < len(b) {
			y = leadingInt(b[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}

func leadingInt(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// belowMinimum lists installed packages older than their minimum.
func belowMinimum(states map[string]aptState) []string {
	var out []string
	for name, min := range minimums {
		if st := states[name]; st.Installed != "" && olderThan(st.Installed, min) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// ---- running an update ----

// updateBox is what an update acts on: apt, systemd, Postgres and the
// pooler on a box, fakes in tests.
type updateBox interface {
	Refresh(ctx context.Context) error
	Policy(ctx context.Context, pkgs []string) (map[string]aptState, error)
	NeedsRestart(ctx context.Context) []string
	Download(ctx context.Context, pkgs []string) error
	Install(ctx context.Context, pkgs []string) error
	Rollback(ctx context.Context, pkgs []PGPackage) error
	ServerVersion(ctx context.Context) string
	PoolerVersion(ctx context.Context) string
	Checkpoint(ctx context.Context) error
	Pause(ctx context.Context) (resume func() error, err error)
	Restart(ctx context.Context, unit string) error
	WaitReady(ctx context.Context, unit string) error
}

// pauseWait is how long PAUSE may wait for transactions in flight.
var pauseWait = 30 * time.Second

// check refreshes the package index and plans an update.
func check(ctx context.Context, b updateBox) (updatePlan, map[string]aptState, error) {
	if err := b.Refresh(ctx); err != nil {
		return updatePlan{}, nil, fmt.Errorf("refresh the package index: %w", err)
	}
	states, err := b.Policy(ctx, updatePackages)
	if err != nil {
		return updatePlan{}, nil, err
	}
	flagged := b.NeedsRestart(ctx)
	// A server older than its installed package (an install whose restart
	// did not happen) needs the restart, needrestart or not; so does the
	// pooler.
	for unit, v := range map[string][2]string{
		UnitName:   {b.ServerVersion(ctx), states["postgresql-"+Major].Installed},
		PoolerUnit: {b.PoolerVersion(ctx), states["pgbouncer"].Installed},
	} {
		if v[0] != "" && v[1] != "" && v[0] != upstream(v[1]) && !slices.Contains(flagged, unit) {
			flagged = append(flagged, unit)
		}
	}
	return decide(states, flagged), states, nil
}

// apply runs a planned update. Nothing in it is destructive: on any failure
// the pooler resumes, a Postgres that does not come back on the new
// packages gets the old ones again, and the record says what happened.
func apply(ctx context.Context, b updateBox, plan updatePlan, trigger string) *PGUpdate {
	start := time.Now()
	u := &PGUpdate{ID: ids.New("pgu"), At: start.UTC(), Trigger: trigger, Status: "ok", Packages: plan.Packages, Restarted: []string{}}
	if u.Packages == nil {
		u.Packages = []PGPackage{}
	}
	u.From = b.ServerVersion(ctx)
	fail := func(err error) *PGUpdate {
		u.Status, u.Error = "failed", err.Error()
		u.Seconds = round1(time.Since(start).Seconds())
		u.To = b.ServerVersion(ctx)
		u.Summary = u.summary()
		return u
	}
	names := make([]string, 0, len(plan.Packages))
	for _, p := range plan.Packages {
		names = append(names, p.Name)
	}
	if len(names) > 0 {
		if err := b.Download(ctx, names); err != nil {
			return fail(fmt.Errorf("download: %w", err))
		}
		// The running server keeps its old binaries until the restart.
		if err := b.Install(ctx, names); err != nil {
			return fail(fmt.Errorf("install: %w", err))
		}
	}
	if plan.RestartPostgres {
		// Flush dirty buffers now, so the shutdown checkpoint is quick.
		if err := b.Checkpoint(ctx); err != nil {
			return fail(fmt.Errorf("checkpoint: %w", err))
		}
		// The pooler holds new queries while Postgres restarts; transactions
		// in flight finish first.
		at := time.Now()
		resume, err := b.Pause(ctx)
		if err != nil {
			return fail(fmt.Errorf("pause the pooler: %w", err))
		}
		err = b.Restart(ctx, UnitName)
		if err == nil {
			err = b.WaitReady(ctx, UnitName)
		}
		if err != nil {
			err = fmt.Errorf("restart Postgres: %w", err)
			if rerr := rollback(ctx, b, plan.Packages); rerr != nil {
				err = fmt.Errorf("%w; %v", err, rerr)
			}
		}
		if rerr := resume(); rerr != nil {
			err = errors.Join(err, fmt.Errorf("resume the pooler: %w", rerr))
		}
		u.PauseSeconds = float64(time.Since(at).Milliseconds()) / 1000
		if err != nil {
			return fail(err)
		}
		u.Restarted = append(u.Restarted, UnitName)
	}

	if plan.RestartPooler {
		// Its stop is a safe shutdown (SIGINT): transactions in flight finish,
		// then client connections close and apps' pools open new ones.
		err := b.Restart(ctx, PoolerUnit)
		if err == nil {
			err = b.WaitReady(ctx, PoolerUnit)
		}
		if err != nil {
			return fail(fmt.Errorf("restart the pooler: %w", err))
		}
		u.Restarted = append(u.Restarted, PoolerUnit)
	}
	var errs []string
	for _, unit := range plan.Restart {
		if err := b.Restart(ctx, unit); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		u.Restarted = append(u.Restarted, unit)
	}
	if len(errs) > 0 {
		return fail(errors.New("restart: " + strings.Join(errs, "; ")))
	}
	u.To = b.ServerVersion(ctx)
	u.Seconds = round1(time.Since(start).Seconds())
	u.Summary = u.summary()
	return u
}

// rollback puts the replaced Postgres packages back and starts the old
// version, when the new one did not come up.
func rollback(ctx context.Context, b updateBox, pkgs []PGPackage) error {
	var back []PGPackage
	for _, p := range pkgs {
		if !slices.Contains(poolerPackages, p.Name) {
			back = append(back, p)
		}
	}
	if len(back) > 0 {
		if err := b.Rollback(ctx, back); err != nil {
			return fmt.Errorf("putting the old packages back failed: %w", err)
		}
	}
	if err := b.Restart(ctx, UnitName); err != nil {
		return fmt.Errorf("starting Postgres again failed: %w", err)
	}
	if err := b.WaitReady(ctx, UnitName); err != nil {
		return fmt.Errorf("starting Postgres again failed: %w", err)
	}
	if len(back) > 0 {
		return errors.New("the old version runs again")
	}
	return nil
}

// summary says what an update did in one line:
// "Postgres 18.4 → 18.5, paused 3.1 s".
func (u *PGUpdate) summary() string {
	var parts []string
	if u.From != "" && u.To != "" && u.From != u.To {
		parts = append(parts, fmt.Sprintf("Postgres %s → %s", u.From, u.To))
	}
	for _, p := range u.Packages {
		if strings.HasPrefix(p.Name, "postgresql-"+Major) && p.Name != "postgresql-"+Major && !strings.HasPrefix(p.Name, "postgresql-client") {
			parts = append(parts, fmt.Sprintf("%s %s → %s", strings.TrimPrefix(p.Name, "postgresql-"+Major+"-"), upstream(p.From), upstream(p.To)))
		}
		if p.Name == "pgbouncer" {
			parts = append(parts, fmt.Sprintf("PgBouncer %s → %s", upstream(p.From), upstream(p.To)))
		}
	}
	if len(parts) == 0 {
		switch {
		case len(u.Packages) > 0:
			parts = append(parts, fmt.Sprintf("%d client package(s) updated", len(u.Packages)))
		case len(u.Restarted) > 0:
			parts = append(parts, "restarted "+strings.Join(trimUnits(u.Restarted), ", ")+" (updated libraries)")
		}
	}
	s := strings.Join(parts, ", ")
	if slices.Contains(u.Restarted, UnitName) {
		if u.PauseSeconds < 1 {
			s += fmt.Sprintf(", paused %.2f s", u.PauseSeconds)
		} else {
			s += fmt.Sprintf(", paused %.1f s", u.PauseSeconds)
		}
	}
	if u.Status == "failed" {
		s = "failed: " + u.Error
		if u.From != "" {
			s += fmt.Sprintf(" (Postgres %s runs)", u.To)
		}
	}
	return s
}

func trimUnits(units []string) []string {
	out := make([]string, len(units))
	for i, u := range units {
		out[i] = strings.TrimSuffix(u, ".service")
	}
	return out
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }

// ---- the record ----

// maintPath keeps the last checks and updates; the provisioner writes it
// too, so it is a file rather than platform state.
var maintPath = "/var/lib/tiffin/postgres/maintenance.json"

type maintRecord struct {
	CheckedAt     time.Time   `json:"checkedAt"`
	Available     []PGPackage `json:"available"`
	Restart       []string    `json:"restart"`
	LastScheduled time.Time   `json:"lastScheduled"`
	Updates       []PGUpdate  `json:"updates"` // newest first
	Audited       []string    `json:"audited"` // update IDs already in the audit log
}

var maintMu sync.Mutex // guards the file

func loadMaint() maintRecord {
	var r maintRecord
	if raw, err := os.ReadFile(maintPath); err == nil {
		_ = json.Unmarshal(raw, &r)
	}
	return r
}

// editMaint changes the record under the lock.
func editMaint(f func(*maintRecord)) error {
	maintMu.Lock()
	defer maintMu.Unlock()
	r := loadMaint()
	f(&r)
	if len(r.Updates) > 30 {
		r.Updates = r.Updates[:30]
	}
	if len(r.Audited) > 60 {
		r.Audited = r.Audited[len(r.Audited)-60:]
	}
	raw, _ := json.MarshalIndent(r, "", "  ")
	if err := os.MkdirAll(filepath.Dir(maintPath), 0o755); err != nil {
		return err
	}
	tmp := maintPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, maintPath)
}

func noteChecked(plan updatePlan) error {
	return editMaint(func(r *maintRecord) {
		r.CheckedAt = time.Now().UTC()
		r.Available, r.Restart = plan.Packages, plan.Flagged
	})
}

func noteUpdate(u *PGUpdate, plan updatePlan) error {
	return editMaint(func(r *maintRecord) {
		r.Updates = append([]PGUpdate{*u}, r.Updates...)
		r.CheckedAt = u.At
		if u.Status == "ok" {
			r.Available, r.Restart = nil, nil
			for _, unit := range plan.Flagged {
				if !slices.Contains(u.Restarted, unit) {
					r.Restart = append(r.Restart, unit) // the pooler, unless asked
				}
			}
		}
		if u.Trigger == "schedule" {
			r.LastScheduled = u.At
		}
	})
}

// LatestUpdate is the box's most recent Postgres update, or nil.
func LatestUpdate() *PGUpdate {
	r := loadMaint()
	if len(r.Updates) == 0 {
		return nil
	}
	return &r.Updates[0]
}

// ---- the real box ----

// sysBox drives apt, systemd, Postgres and the pooler on the machine.
type sysBox struct{ log func(string) }

// aptEnv keeps needrestart out of our own installs: the box restarts what
// it updated itself, in order.
var aptEnv = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive", "NEEDRESTART_SUSPEND=1", "NEEDRESTART_MODE=l")

func (b sysBox) run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = aptEnv
	out, err := cmd.CombinedOutput()
	if err != nil {
		s := strings.TrimSpace(string(out))
		if len(s) > 1500 {
			s = "…" + s[len(s)-1500:]
		}
		return string(out), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, s)
	}
	return string(out), nil
}

var aptOpts = []string{"-o", "DPkg::Lock::Timeout=600", "-o", "Acquire::Retries=3"}

// Refresh reads the PGDG repository's index only (the rest of the system
// refreshes daily on its own).
func (b sysBox) Refresh(ctx context.Context) error {
	list := "/etc/apt/sources.list.d/pgdg.list"
	if _, err := os.Stat(list); err != nil {
		return err
	}
	_, err := b.run(ctx, "apt-get", append(aptOpts, "-o", "Dir::Etc::sourcelist="+list, "-o", "Dir::Etc::sourceparts=-",
		"-o", "APT::Get::List-Cleanup=0", "update", "-q")...)
	return err
}

func (b sysBox) Policy(ctx context.Context, pkgs []string) (map[string]aptState, error) {
	out, err := b.run(ctx, "apt-cache", append([]string{"policy"}, pkgs...)...)
	if err != nil {
		return nil, err
	}
	return parsePolicy(out), nil
}

// parsePolicy reads `apt-cache policy` output.
func parsePolicy(out string) map[string]aptState {
	states := map[string]aptState{}
	var name string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if line != "" && line[0] != ' ' && strings.HasSuffix(line, ":") {
			name = strings.TrimSuffix(line, ":")
			continue
		}
		f := strings.Fields(line)
		if name == "" || len(f) != 2 {
			continue
		}
		v := f[1]
		if v == "(none)" {
			v = ""
		}
		st := states[name]
		switch f[0] {
		case "Installed:":
			st.Installed = v
		case "Candidate:":
			st.Candidate = v
		default:
			continue
		}
		states[name] = st
	}
	return states
}

// NeedsRestart lists the services needrestart finds running on replaced
// binaries or libraries (none without needrestart).
func (b sysBox) NeedsRestart(ctx context.Context) []string {
	if _, err := exec.LookPath("needrestart"); err != nil {
		return nil
	}
	out, err := exec.CommandContext(ctx, "needrestart", "-b", "-r", "l").Output()
	if err != nil {
		return nil
	}
	return parseNeedrestart(string(out))
}

// parseNeedrestart reads `needrestart -b` (batch) output.
func parseNeedrestart(out string) []string {
	var units []string
	for _, line := range strings.Split(out, "\n") {
		if u, ok := strings.CutPrefix(line, "NEEDRESTART-SVC: "); ok {
			units = append(units, strings.TrimSpace(u))
		}
	}
	return units
}

func (b sysBox) Download(ctx context.Context, pkgs []string) error {
	_, err := b.run(ctx, "apt-get", slices.Concat(aptOpts, []string{"install", "-y", "-q", "--only-upgrade", "--download-only"}, pkgs)...)
	return err
}

func (b sysBox) Install(ctx context.Context, pkgs []string) error {
	_, err := b.run(ctx, "apt-get", slices.Concat(aptOpts, []string{"install", "-y", "-q", "--only-upgrade", "--no-install-recommends",
		"-o", "Dpkg::Options::=--force-confold"}, pkgs)...)
	return err
}

// Rollback installs the versions an update replaced (PGDG keeps them).
func (b sysBox) Rollback(ctx context.Context, pkgs []PGPackage) error {
	args := slices.Concat(aptOpts, []string{"install", "-y", "-q", "--allow-downgrades", "--no-install-recommends", "-o", "Dpkg::Options::=--force-confold"})
	for _, p := range pkgs {
		args = append(args, p.Name+"="+p.From)
	}
	_, err := b.run(ctx, "apt-get", args...)
	return err
}

func (b sysBox) ServerVersion(ctx context.Context) string {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, err := Admin(cctx, "postgres")
	if err != nil {
		return ""
	}
	defer c.Close(cctx)
	var v string
	if c.QueryRow(cctx, `SHOW server_version`).Scan(&v) != nil {
		return ""
	}
	return strings.Fields(v + " ")[0]
}

func (b sysBox) PoolerVersion(ctx context.Context) string {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if st, err := poolerStats(cctx); err == nil {
		return st.Version
	}
	return ""
}

func (b sysBox) Checkpoint(ctx context.Context) error {
	c, err := Admin(ctx, "postgres")
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	_, err = c.Exec(ctx, `CHECKPOINT`)
	return err
}

func (b sysBox) Pause(ctx context.Context) (func() error, error) {
	return pausePooler(ctx, "", pauseWait)
}

func (b sysBox) Restart(ctx context.Context, unit string) error {
	if unit != UnitName && unit != PoolerUnit && exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", unit).Run() != nil {
		return nil // not running here: nothing to restart
	}
	_, err := b.run(ctx, "systemctl", "restart", unit)
	return err
}

func (b sysBox) WaitReady(ctx context.Context, unit string) error {
	if unit == PoolerUnit {
		deadline := time.Now().Add(30 * time.Second)
		for !poolerRunning(ctx) {
			if time.Now().After(deadline) {
				return errors.New("the pooler did not answer within 30 s")
			}
			time.Sleep(100 * time.Millisecond)
		}
		return nil
	}
	return waitReady(ctx, 90*time.Second)
}

// updateOnce runs one update at a time on this box (the API, the schedule).
var updateOnce sync.Mutex

// errBusy: an update is already running.
var errBusy = errors.New("an update is already running")

// runOpts say how runUpdate runs.
type runOpts struct {
	Trigger   string
	CheckOnly bool
	// RestartPooler lets it restart PgBouncer for a new version or replaced
	// libraries. That closes every client connection (in-flight waits fail,
	// and node-postgres apps without an error listener exit), so it happens
	// only when asked; otherwise the new version runs from the next reboot.
	RestartPooler bool
}

// runUpdate checks and, unless CheckOnly, applies an update now. It returns
// nil when there was nothing to do.
func runUpdate(ctx context.Context, b updateBox, o runOpts) (*PGUpdate, updatePlan, error) {
	if !updateOnce.TryLock() {
		return nil, updatePlan{}, errBusy
	}
	defer updateOnce.Unlock()
	// `tiffin provision` (tiffin up) is another process: a file lock keeps
	// it and the running box from updating at once.
	if err := os.MkdirAll(filepath.Dir(maintPath), 0o755); err != nil {
		return nil, updatePlan{}, err
	}
	lock, err := os.OpenFile(maintPath+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, updatePlan{}, err
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return nil, updatePlan{}, errBusy
	}
	plan, _, err := check(ctx, b)
	if err != nil {
		return nil, plan, err
	}
	if err := noteChecked(plan); err != nil {
		return nil, plan, err
	}
	if plan.RestartPooler && !slices.Contains(plan.Flagged, PoolerUnit) {
		plan.Flagged = append(plan.Flagged, PoolerUnit) // a new version of it
	}
	plan.RestartPooler = plan.RestartPooler && o.RestartPooler
	if o.CheckOnly || plan.empty() {
		return nil, plan, nil
	}
	// Branch clones and restores hold mu; the restart waits for them.
	if plan.RestartPostgres {
		mu.Lock()
		defer mu.Unlock()
	}
	u := apply(ctx, b, plan, o.Trigger)
	return u, plan, noteUpdate(u, plan)
}

// checkMinimum fails when an installed package is older than its minimum
// even after an update (for the pooler, whose features the box relies on).
func checkMinimum(ctx context.Context, pkg string) error {
	states, err := sysBox{}.Policy(ctx, []string{pkg})
	if err != nil {
		return err
	}
	if st := states[pkg]; st.Installed != "" && olderThan(st.Installed, minimums[pkg]) {
		return fmt.Errorf("%s %s is older than %s, which the box needs; the PGDG repository should have a newer one", pkg, upstream(st.Installed), minimums[pkg])
	}
	return nil
}

// raiseMinimums updates the packages when one is older than its minimum:
// `tiffin up` after a Tiffin release raised one.
func raiseMinimums(ctx context.Context, log func(string)) error {
	b := sysBox{log: log}
	states, err := b.Policy(ctx, updatePackages)
	if err != nil {
		return err
	}
	low := belowMinimum(states)
	if len(low) == 0 {
		return nil
	}
	log("updating " + strings.Join(low, ", ") + " to the minimum this Tiffin needs (queries wait at the pooler while Postgres restarts)")
	u, _, err := runUpdate(ctx, b, runOpts{Trigger: "tiffin up"})
	if err != nil {
		return err
	}
	if u != nil {
		log(u.Summary)
		if u.Status == "failed" {
			return errors.New(u.Summary)
		}
	}
	if states, err = b.Policy(ctx, updatePackages); err == nil {
		if low := belowMinimum(states); len(low) > 0 {
			return fmt.Errorf("%s still older than the minimum this Tiffin needs: the PGDG repository has no newer version yet", strings.Join(low, ", "))
		}
	}
	return nil
}
