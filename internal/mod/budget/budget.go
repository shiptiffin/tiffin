// Package budget divides the box between projects. Every project's app
// containers (production and previews) run in one systemd slice,
// tiffin-p-<project>.slice, under tiffin-p.slice whose MemoryMax keeps the
// platform's memory out of the apps' reach.
//
// By default a project is automatic (elastic): it grows into whatever the
// box has free, is protected up to a fair share when memory gets tight, and
// shares the CPU equally with other projects under contention. A project
// can instead set `resources` in tiffin.config.ts (memoryMB, cpus,
// maxSharePercent), and the box owner can set a default share for every
// project that sets none. Limits apply live; nothing restarts.
package budget

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	goruntime "runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/manifest"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/state"
)

var mod = &Module{}

func init() { platform.Register(mod) }

// Module is the budget module.
type Module struct {
	syncMu   sync.Mutex
	mu       sync.Mutex
	p        *platform.Platform
	root     string  // "" in production; tests point it at a fake tree
	sd       systemd // nil: limits are computed but not applied (not a Linux box)
	specs    map[string]*manifest.Resources
	settings Settings
	applied  map[string]string // unit → properties last set
	limits   map[string]Limits
	box      Box
	boxOK    bool
	history  map[string][]eventMark // project → memory events over the last hour
	kills    map[string]uint64      // project → oom_kill count at the last sync
	wake     chan struct{}
	gen      uint64 // bumped whenever any project's limits change
	synced   bool   // limits were resolved at least once
}

// Synced reports whether the limits were resolved since the box started,
// so modules that follow them don't act on an empty answer.
func Synced() bool {
	mod.mu.Lock()
	defer mod.mu.Unlock()
	return mod.synced
}

// Shared is a project's limit for what it uses of the services every
// project shares: its database's CPU and connections, its cache and its
// builds. Percent 0 means no limit (and the rest is zero).
type Shared struct {
	Percent  int     // share of the box, 1-99
	CPUs     float64 // that share of the box's CPUs, in cores
	MemoryMB int     // the memory its apps may use (their cap)
	Source   string  // SourceProject or SourceBoxDefault
}

// SharedLimit returns a project's shared-services limit as last resolved.
func SharedLimit(project string) Shared {
	mod.mu.Lock()
	defer mod.mu.Unlock()
	l, ok := mod.limits[project]
	if !ok || l.SharePercent == 0 || !mod.boxOK {
		return Shared{}
	}
	src := SourceBoxDefault
	if r := mod.specs[project]; r != nil {
		src = SourceProject
	}
	return Shared{Percent: l.SharePercent, CPUs: max(0.01, float64(mod.box.CPUs*l.SharePercent)/100),
		MemoryMB: l.MemoryMaxMB, Source: src}
}

// Generation changes whenever any project's resolved limits change, so
// readers that cache answers know to measure again.
func Generation() uint64 {
	mod.mu.Lock()
	defer mod.mu.Unlock()
	return mod.gen
}

func limitsEqual(a, b map[string]Limits) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

func (*Module) Name() string { return "budget" }

// Order: before the runtime (40), so a project's slice exists before its
// containers start.
func (*Module) Order() int { return 35 }

const (
	kvNS        = "budget"
	kvSettings  = "settings"
	syncEvery   = 15 * time.Second
	historyKeep = time.Hour
)

// Start loads every project's budget and the box settings, applies them and
// keeps them current: automatic limits depend on what the other projects
// run, and shares follow the box if it is resized.
func (m *Module) Start(ctx context.Context, p *platform.Platform) error {
	var sd systemd
	if goruntime.GOOS == "linux" {
		if _, err := exec.LookPath("systemctl"); err == nil {
			sd = systemctl{}
		}
	}
	return m.start(ctx, p, "", sd)
}

func (m *Module) start(ctx context.Context, p *platform.Platform, root string, sd systemd) error {
	specs := map[string]*manifest.Resources{}
	projects, err := p.DB.ListProjects(ctx)
	if err != nil {
		return err
	}
	for _, pr := range projects {
		_, res, err := p.DB.Load(ctx, pr)
		if err != nil {
			return err
		}
		if rs, ok := res[change.KindProject]; ok {
			specs[pr] = decodeSpec(rs.Spec)
		}
	}
	set := DefaultSettings
	if raw, ok, _ := p.DB.KVGet(ctx, kvNS, kvSettings); ok {
		_ = json.Unmarshal(raw, &set) // settings saved before the disk guard keep its defaults
		if set.check() != "" {
			set = DefaultSettings
		}
	}
	m.mu.Lock()
	m.p, m.root, m.sd, m.specs, m.settings = p, root, sd, specs, set
	m.applied, m.limits, m.history, m.kills = map[string]string{}, map[string]Limits{}, map[string][]eventMark{}, map[string]uint64{}
	m.wake = make(chan struct{}, 1)
	m.mu.Unlock()
	p.DB.OnCommit(m.checkCommit)
	if err := m.sync(ctx); err != nil {
		p.Log.Error("budget sync", "err", err)
	}
	go m.loop(ctx)
	return nil
}

func (m *Module) loop(ctx context.Context) {
	t := time.NewTicker(syncEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-m.wake:
		}
		if err := m.sync(ctx); err != nil {
			m.p.Log.Error("budget sync", "err", err)
		}
	}
}

func decodeSpec(raw json.RawMessage) *manifest.Resources {
	var ps change.ProjectSpec
	_ = json.Unmarshal(raw, &ps)
	return ps.Resources
}

// Kinds: the project resource carries the budget.
func (*Module) Kinds() []string { return []string{change.KindProject} }

// Reconcile records a project's budget and applies every slice's limits
// (one project's budget changes the others' fair shares). A deleted
// project's slice is removed.
func (m *Module) Reconcile(ctx context.Context, p *platform.Platform, project, address string, spec json.RawMessage) error {
	m.mu.Lock()
	if m.specs == nil {
		m.mu.Unlock()
		return nil // not serving
	}
	if spec == nil {
		delete(m.specs, project)
	} else {
		m.specs[project] = decodeSpec(spec)
	}
	sd := m.sd
	m.mu.Unlock()
	if spec == nil && sd != nil {
		unit := Slice(project)
		if err := sd.Remove(ctx, unit); err != nil {
			return err
		}
		m.mu.Lock()
		delete(m.applied, unit)
		delete(m.history, project)
		delete(m.kills, project)
		m.mu.Unlock()
	}
	if spec == nil {
		forgetEvents(ctx, project)
	}
	return m.sync(ctx)
}

// sync resolves every project's limits and applies the ones that changed.
// Syncs are serialized; systemctl runs outside m.mu so readers never wait.
func (m *Module) sync(ctx context.Context) error {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	box, ok := ReadBox(m.root)
	m.mu.Lock()
	m.box, m.boxOK = box, ok
	if !ok {
		m.mu.Unlock()
		return nil
	}
	names := make([]string, 0, len(m.specs))
	for n := range m.specs {
		names = append(names, n)
	}
	specs := make(map[string]*manifest.Resources, len(m.specs))
	for k, v := range m.specs {
		specs[k] = v
	}
	settings, sd := m.settings, m.sd
	m.mu.Unlock()

	sort.Strings(names)
	now := time.Now()
	in := make([]Project, 0, len(names))
	stats := map[string]Stats{}
	for _, n := range names {
		st := ReadStats(SliceDir(m.root, n))
		stats[n] = st
		in = append(in, Project{Name: n, Resources: specs[n], Copies: st.Copies})
	}
	limits := Resolve(box, settings, in)

	type want struct {
		unit  string
		props []string
	}
	wants := []want{{ParentSlice, []string{fmt.Sprintf("MemoryMax=%dM", box.PoolMB()), "TasksMax=" + AppsTasksMax}}}
	for _, n := range names {
		wants = append(wants, want{Slice(n), props(limits[n])})
	}
	m.mu.Lock()
	if !limitsEqual(m.limits, limits) {
		m.gen++
	}
	m.limits, m.synced = limits, true
	var killed []string
	for n, st := range stats {
		if st.Exists {
			m.recordLocked(n, st, now)
			if prev, ok := m.kills[n]; ok && st.OOMKill > prev {
				killed = append(killed, n)
			}
			m.kills[n] = st.OOMKill
		}
	}
	var todo []want
	for _, w := range wants {
		if m.applied[w.unit] != strings.Join(w.props, " ") {
			todo = append(todo, w)
		}
	}
	m.mu.Unlock()
	sort.Strings(killed)
	for _, n := range killed {
		RecordEvent(ctx, n, EventMemory, killWords(n, limits[n]))
	}
	if sd == nil {
		return nil
	}
	var errs []error
	for _, w := range todo {
		if err := sd.Set(ctx, w.unit, w.props); err != nil {
			errs = append(errs, err)
			continue
		}
		m.mu.Lock()
		m.applied[w.unit] = strings.Join(w.props, " ")
		m.mu.Unlock()
	}
	return errors.Join(errs...)
}

// killWords says why an app of a project was stopped for memory.
func killWords(project string, l Limits) string {
	if l.MemorySource == SourceAutomatic {
		return fmt.Sprintf("The box ran out of memory for apps, so an app of %s was stopped and restarted. Give another project a limit, or move to a bigger box.", project)
	}
	return fmt.Sprintf("%s used all of the %d MB of memory it may use, so an app was stopped and restarted. Give it a bigger limit if it needs more.", project, l.MemoryMaxMB)
}

// View is what the box knows about one project's budget.
type View struct {
	Box       Box
	BoxKnown  bool
	Settings  Settings
	Resources *manifest.Resources // nil: no limit of its own
	Limits    Limits
	Known     bool // the project exists here
	Pressure  string
}

// Lookup returns a project's budget and limits as last resolved, and the
// memory pressure from the given fresh reading of its slice.
func Lookup(project string, st Stats) View {
	m := mod
	m.mu.Lock()
	defer m.mu.Unlock()
	v := View{Box: m.box, BoxKnown: m.boxOK, Settings: m.settings, Pressure: PressureNone}
	if m.specs == nil {
		v.Settings = DefaultSettings
		return v
	}
	r, ok := m.specs[project]
	v.Resources, v.Known = r, ok
	v.Limits = m.limits[project]
	if st.Exists {
		v.Pressure = m.pressureLocked(project, st, time.Now())
	}
	return v
}

// Projects returns every known project's budget (nil: none of its own).
func Projects() map[string]*manifest.Resources {
	m := mod
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]*manifest.Resources, len(m.specs))
	for k, v := range m.specs {
		out[k] = v
	}
	return out
}

// CheckPlan refuses a plan whose project budget cannot fit this box: more
// CPUs than it has, or memoryMB budgets that add up to more than it keeps
// for apps. Unchanged budgets are not re-checked, so a box that shrank does
// not block unrelated changes. It answers at plan time; checkCommit holds
// the line when the change commits.
func (m *Module) CheckPlan(ctx context.Context, p *platform.Platform, project string, desired map[string]change.Resource) error {
	rs, ok := desired[change.KindProject]
	if !ok {
		return nil
	}
	specs, err := p.DB.SpecsAt(ctx, change.KindProject)
	if err != nil {
		return err
	}
	return m.fits(project, specs[project], rs.Spec, specs)
}

// checkCommit is CheckPlan inside the commit (see state.CommitCheck): an
// undo, which plans nothing, and two applies racing for the same free
// memory are refused there.
func (m *Module) checkCommit(ctx context.Context, v state.CommitView, project string, ops []change.Op) error {
	for _, o := range ops {
		if o.Address != change.KindProject || o.Action == change.Delete {
			continue
		}
		if decodeSpec(o.After) == nil {
			return nil
		}
		specs, err := v.SpecsAt(ctx, change.KindProject)
		if err != nil {
			return err
		}
		return m.fits(project, o.Before, o.After, specs)
	}
	return nil
}

// fits refuses project's budget after (it was before) unless it fits the
// box beside the other projects' budgets in specs.
func (m *Module) fits(project string, before, after json.RawMessage, specs map[string]json.RawMessage) error {
	want := decodeSpec(after)
	if want == nil {
		return nil
	}
	if h := decodeSpec(before); h != nil && *h == *want {
		return nil
	}
	m.mu.Lock()
	root := m.root
	m.mu.Unlock()
	box, ok := ReadBox(root)
	if !ok {
		return nil // not a box: nothing to measure against
	}
	others := map[string]*manifest.Resources{}
	for n, spec := range specs {
		if n != project {
			others[n] = decodeSpec(spec)
		}
	}
	probs := Check(box, project, want, others)
	if len(probs) == 0 {
		return nil
	}
	out := api.NewProblem(422, "validation", "project "+project+"'s resources do not fit this box: "+probs[0].Message)
	for _, pr := range probs {
		out.Errors = append(out.Errors, api.FieldError{Path: pr.Path, Message: pr.Message})
	}
	out.Hint = box.Explain() + " Lower the budget, use maxSharePercent (a ceiling that may overlap with other projects), or leave resources out to share the box automatically."
	return out
}
