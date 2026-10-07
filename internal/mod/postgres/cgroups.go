package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/mod/budget"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/jackc/pgx/v5"
)

// The database's share of a project's limit. Postgres serves every
// connection with its own backend process, so a project's queries are its
// backends' work. The unit delegates its cgroup (Delegate=cpu io,
// DelegateSubgroup=shared): the server and every backend start in
// tiffin-postgres.service/shared. Every second the watcher reads
// pg_stat_activity and moves the backends of each project with a limit into
// tiffin-postgres.service/p-<project>, whose cpu.max is the project's share
// of the box's CPUs and io.weight its share; everything else (projects
// without a limit, autovacuum, the checkpointer, the WAL writer) stays in
// shared. A busy project's queries slow down; nobody's are stopped.

const (
	serviceCgroup = "/sys/fs/cgroup/system.slice/" + UnitName
	sharedGroup   = "shared"
	groupPrefix   = "p-"
	// cpuPeriod is the cpu.max period in microseconds: short, so a throttled
	// backend that holds a lock everyone needs (a buffer, the WAL) waits at
	// most a few milliseconds before it runs again.
	cpuPeriod = 20000
)

// GroupDir is the cgroup a project's backends run in while it has a limit
// (root is "" in production; tests point it at a temp dir).
func GroupDir(root, project string) string {
	return filepath.Join(root, serviceCgroup, groupPrefix+project)
}

// cgroups is the Postgres service's delegated cgroup.
type cgroups struct {
	dir     string
	enabled bool
}

// ready says the unit runs with delegation: the server sits in "shared".
func (c *cgroups) ready() bool {
	_, err := os.Stat(filepath.Join(c.dir, sharedGroup))
	return err == nil
}

// enable turns on the cpu (and, where the kernel has it, io) controller for
// the groups below the service.
func (c *cgroups) enable() error {
	if c.enabled {
		return nil
	}
	cur, _ := os.ReadFile(filepath.Join(c.dir, "cgroup.subtree_control"))
	if !slices.Contains(strings.Fields(string(cur)), "cpu") {
		f := filepath.Join(c.dir, "cgroup.subtree_control")
		if err := os.WriteFile(f, []byte("+cpu +io"), 0o644); err != nil {
			if err := os.WriteFile(f, []byte("+cpu"), 0o644); err != nil {
				return fmt.Errorf("enable the cpu controller under %s: %w", c.dir, err)
			}
		}
	}
	c.enabled = true
	return nil
}

// cpuMax renders cpu.max for a cap in cores.
func cpuMax(cpus float64) string {
	return fmt.Sprintf("%d %d", max(1000, int(cpus*cpuPeriod+0.5)), cpuPeriod)
}

// ensure creates a project's group with its CPU cap and IO weight.
func (c *cgroups) ensure(project string, cpus float64, ioWeight int) error {
	dir := filepath.Join(c.dir, groupPrefix+project)
	if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "cpu.max"), []byte(cpuMax(cpus)), 0o644); err != nil {
		return err
	}
	// io.weight exists only with the io controller (and counts only with an
	// IO scheduler that has weights): best effort.
	_ = os.WriteFile(filepath.Join(dir, "io.weight"), []byte("default "+strconv.Itoa(max(1, ioWeight))), 0o644)
	return nil
}

// procs lists the processes in a group ("shared" or a project's).
func (c *cgroups) procs(group string) []int {
	raw, err := os.ReadFile(filepath.Join(c.dir, group, "cgroup.procs"))
	if err != nil {
		return nil
	}
	var out []int
	for _, f := range strings.Fields(string(raw)) {
		if n, err := strconv.Atoi(f); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// move puts a process into a group. A process that already exited is not
// an error.
func (c *cgroups) move(group string, pid int) error {
	f, err := os.OpenFile(filepath.Join(c.dir, group, "cgroup.procs"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	_, err = f.WriteString(strconv.Itoa(pid))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// projects lists the projects that have a group.
func (c *cgroups) projects() []string {
	ents, _ := os.ReadDir(c.dir)
	var out []string
	for _, e := range ents {
		if e.IsDir() && strings.HasPrefix(e.Name(), groupPrefix) {
			out = append(out, strings.TrimPrefix(e.Name(), groupPrefix))
		}
	}
	return out
}

// remove deletes an empty project group.
func (c *cgroups) remove(project string) error {
	return os.Remove(filepath.Join(c.dir, groupPrefix+project))
}

// move is one process to put into a group.
type move struct {
	pid   int
	group string // sharedGroup or "p-<project>"
}

// place decides where backends go. limited: projects with a limit;
// backends: pid → project of every backend of a project with a limit;
// members: project → pids in its group now. Backends of limited projects
// go to their group; anything else in a project's group (a project whose
// limit was lifted, a pid reused by another backend) goes back to shared.
// gone lists the groups to remove: no limit, and nothing left in them.
func place(limited map[string]bool, backends map[int]string, members map[string][]int) (moves []move, gone []string) {
	in := map[int]string{}
	for pr, pids := range members {
		for _, pid := range pids {
			in[pid] = pr
		}
	}
	for pid, pr := range backends {
		if limited[pr] && in[pid] != pr {
			moves = append(moves, move{pid, groupPrefix + pr})
		}
	}
	for pr, pids := range members {
		left := 0
		for _, pid := range pids {
			if limited[pr] && backends[pid] == pr {
				left++
				continue
			}
			if !limited[backends[pid]] {
				moves = append(moves, move{pid, sharedGroup})
			} // else: the move above takes it to its own project's group
		}
		if !limited[pr] && left == 0 {
			gone = append(gone, pr)
		}
	}
	sort.Slice(moves, func(i, j int) bool { return moves[i].pid < moves[j].pid })
	sort.Strings(gone)
	return moves, gone
}

// ---- the watcher ----

// Usage is a project's database against its limits.
type Usage struct {
	Tracked                 bool    // the watcher follows it (it has a database, on a box)
	SharePercent            int     // 0: no limit
	CPUs                    float64 // its backends' CPU cap in cores; 0: none (no limit, or the box can't hold it)
	Connections             int     // open now
	ConnectionLimit         int
	StatementTimeoutSeconds int
	TimeoutsToday           int // queries stopped by the time limit since midnight UTC
}

type watcher struct {
	mu          sync.Mutex
	cg          *cgroups
	conn        *pgx.Conn
	retryAt     time.Time
	gen         uint64
	refreshed   time.Time
	scanned     time.Time
	roles       map[string]string     // role → project, for projects with postgres (its read role too)
	timeout     map[string]int        // project → statementTimeoutSeconds in its config
	applied     map[string]RoleLimits // project → settings its role has
	readApplied map[string]RoleLimits // project → settings its read role has
	groups      map[string]string     // project → its group's cpu.max and io weight as written
	counts      map[string]int        // project → connections open now
	timeouts    map[string]int        // project → queries stopped today
	full        map[string]time.Time  // project → when its connections were last all in use
	warned      bool
}

var watch = &watcher{cg: &cgroups{dir: serviceCgroup}, roles: map[string]string{}, timeout: map[string]int{}, applied: map[string]RoleLimits{}, readApplied: map[string]RoleLimits{},
	groups: map[string]string{}, counts: map[string]int{}, timeouts: map[string]int{}, full: map[string]time.Time{}}

const (
	watchEvery   = time.Second
	refreshEvery = 30 * time.Second
	scanEvery    = time.Minute
)

func (w *watcher) run(ctx context.Context, p *platform.Platform) {
	t := time.NewTicker(watchEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			w.mu.Lock()
			if w.conn != nil {
				w.conn.Close(context.Background())
				w.conn = nil
			}
			w.mu.Unlock()
			return
		case <-t.C:
		}
		if err := w.tick(ctx, p, time.Now()); err != nil && ctx.Err() == nil {
			p.Log.Warn("postgres limits", "err", err)
		}
	}
}

// tick is one round: role settings when limits changed, connections, the
// backends' groups, and once a minute the queries stopped today.
func (w *watcher) tick(ctx context.Context, p *platform.Platform, now time.Time) error {
	if !budget.Synced() {
		return nil
	}
	w.mu.Lock()
	err := w.round(ctx, p, now)
	scan := w.conn != nil && now.Sub(w.scanned) >= scanEvery
	if scan {
		w.scanned = now
	}
	w.mu.Unlock()
	if scan {
		// Outside the lock: the journal can take a moment to read.
		if byRole, err := timeoutsSince(ctx, now.UTC().Truncate(24*time.Hour)); err == nil {
			w.mu.Lock()
			w.timeouts = map[string]int{}
			for role, n := range byRole {
				if pr, ok := w.roles[role]; ok {
					w.timeouts[pr] += n
				}
			}
			w.mu.Unlock()
		}
	}
	return err
}

func (w *watcher) round(ctx context.Context, p *platform.Platform, now time.Time) error {
	if w.conn == nil {
		if now.Before(w.retryAt) {
			return nil
		}
		c, err := Admin(ctx, "postgres")
		if err != nil {
			w.retryAt = now.Add(10 * time.Second)
			return nil // not up yet (or not a box): try again later
		}
		w.conn = c
	}
	if gen := budget.Generation(); gen != w.gen || now.Sub(w.refreshed) >= refreshEvery {
		if err := w.refresh(ctx, p); err != nil {
			w.drop()
			return err
		}
		w.gen, w.refreshed = gen, now
	}
	roles := make([]string, 0, len(w.roles))
	for r := range w.roles {
		roles = append(roles, r)
	}
	rows, err := w.conn.Query(ctx, `SELECT pid, usename FROM pg_stat_activity WHERE usename = ANY($1)`, roles)
	if err != nil {
		w.drop()
		return err
	}
	backends := map[int]string{}
	counts, own := map[string]int{}, map[string]int{}
	for rows.Next() {
		var pid int32
		var user string
		if err := rows.Scan(&pid, &user); err != nil {
			rows.Close()
			w.drop()
			return err
		}
		// A build's read-role sessions are the project's too: they count
		// and run in its CPU group.
		pr := w.roles[user]
		backends[int(pid)] = pr
		counts[pr]++
		if user == Role(pr) {
			own[pr]++
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		w.drop()
		return err
	}
	w.counts = counts
	for pr, n := range own {
		if lim := w.applied[pr].Connections; lim > 0 && n >= lim && now.Sub(w.full[pr]) >= time.Hour {
			w.full[pr] = now
			go budget.RecordEvent(context.WithoutCancel(ctx), pr, budget.EventConnections, fmt.Sprintf(
				"%s's database had all %d connections it may open in use, so new ones were refused. Use a smaller connection pool, or give it a bigger limit.", pr, lim))
		}
	}
	w.placeBackends(p, backends)
	return nil
}

func (w *watcher) drop() {
	if w.conn != nil {
		w.conn.Close(context.Background())
		w.conn = nil
	}
}

// refresh learns which projects have a database and gives each role its
// settings where they changed.
func (w *watcher) refresh(ctx context.Context, p *platform.Platform) error {
	projects, err := p.DB.ListProjects(ctx)
	if err != nil {
		return err
	}
	roles, timeouts := map[string]string{}, map[string]int{}
	for _, pr := range projects {
		_, res, err := p.DB.Load(ctx, pr)
		if err != nil {
			return err
		}
		r, ok := res[change.KindService+"/postgres"]
		if !ok {
			continue
		}
		var s manifest.Postgres
		_ = json.Unmarshal(r.Spec, &s)
		roles[Role(pr)], roles[ReadRole(pr)], timeouts[pr] = pr, pr, s.StatementTimeoutSeconds
	}
	w.roles, w.timeout = roles, timeouts
	disk := dataDiskBytes()
	for pr := range w.applied {
		if _, ok := timeouts[pr]; !ok {
			delete(w.applied, pr)
			delete(w.readApplied, pr)
		}
	}
	readRoles, err := existingRoles(ctx, w.conn, `%\_\_read`)
	if err != nil {
		return err
	}
	for pr, t := range timeouts {
		want := roleLimits(budget.SharedLimit(pr).Percent, t, MaxConnections(memTotalMB()), disk)
		if w.applied[pr] != want {
			if err := applyRoleLimits(ctx, w.conn, Role(pr), want); err != nil {
				p.Log.Debug("postgres: role limits", "project", pr, "err", err) // the role may not exist yet: reconcile makes it
				continue
			}
			w.applied[pr] = want
		}
		if rl := readLimits(want); readRoles[ReadRole(pr)] && w.readApplied[pr] != rl {
			if err := applyRoleLimits(ctx, w.conn, ReadRole(pr), rl); err != nil {
				p.Log.Debug("postgres: read role limits", "project", pr, "err", err)
				continue
			}
			w.readApplied[pr] = rl
		}
	}
	return nil
}

// existingRoles returns the roles whose names are LIKE pattern.
func existingRoles(ctx context.Context, c *pgx.Conn, pattern string) (map[string]bool, error) {
	rows, err := c.Query(ctx, `SELECT rolname FROM pg_roles WHERE rolname LIKE $1`, pattern)
	if err != nil {
		return nil, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	out := map[string]bool{}
	for _, n := range names {
		out[n] = true
	}
	return out, err
}

// placeBackends moves backends between groups (no-op without delegation).
func (w *watcher) placeBackends(p *platform.Platform, backends map[int]string) {
	if !w.cg.ready() {
		return
	}
	if err := w.cg.enable(); err != nil {
		if !w.warned {
			p.Log.Warn("postgres: the database's share of project limits is off", "err", err)
			w.warned = true
		}
		return
	}
	limited := map[string]bool{}
	for pr := range w.timeout {
		sh := budget.SharedLimit(pr)
		if sh.Percent == 0 {
			continue
		}
		want := cpuMax(sh.CPUs) + " " + strconv.Itoa(sh.Percent)
		if w.groups[pr] != want {
			if err := w.cg.ensure(pr, sh.CPUs, sh.Percent); err != nil {
				p.Log.Warn("postgres: project group", "project", pr, "err", err)
				continue
			}
			w.groups[pr] = want
		}
		limited[pr] = true
	}
	members := map[string][]int{}
	for _, pr := range w.cg.projects() {
		members[pr] = w.cg.procs(groupPrefix + pr)
	}
	moves, gone := place(limited, backends, members)
	for _, m := range moves {
		if err := w.cg.move(m.group, m.pid); err != nil {
			p.Log.Debug("postgres: move backend", "pid", m.pid, "group", m.group, "err", err)
		}
	}
	for _, pr := range gone {
		if w.cg.remove(pr) == nil {
			delete(w.groups, pr)
		}
	}
}

// LimitUsage returns a project's database against its limits. Before the
// watcher has given its role its settings (Tracked false) the limits are
// what it will give and the connection count is unknown.
func LimitUsage(project string) Usage {
	w := watch
	w.mu.Lock()
	defer w.mu.Unlock()
	l, ok := w.applied[project]
	if !ok {
		l = roleLimits(budget.SharedLimit(project).Percent, w.timeout[project], MaxConnections(memTotalMB()), 0)
	}
	u := Usage{Tracked: ok, Connections: w.counts[project], ConnectionLimit: l.Connections, StatementTimeoutSeconds: l.StatementTimeoutSeconds,
		TimeoutsToday: w.timeouts[project]}
	if sh := budget.SharedLimit(project); sh.Percent > 0 {
		u.SharePercent = sh.Percent
		// The cap holds whenever the service runs with delegation, even before
		// the project's first connection makes its group.
		if _, grouped := w.groups[project]; grouped || (w.cg != nil && w.cg.ready()) {
			u.CPUs = sh.CPUs
		}
	}
	return u
}

// noteApplied records settings ensure gave a role, so the watcher doesn't
// apply them again.
func noteApplied(project string, timeout int, l RoleLimits) {
	watch.mu.Lock()
	watch.applied[project], watch.timeout[project] = l, timeout
	watch.roles[Role(project)] = project
	watch.mu.Unlock()
}

func forgetApplied(project string) {
	watch.mu.Lock()
	delete(watch.applied, project)
	delete(watch.timeout, project)
	delete(watch.roles, Role(project))
	watch.mu.Unlock()
}
