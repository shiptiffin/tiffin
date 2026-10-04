package box

// The disk guard. Every project's database and files share the data disk,
// so one project filling it would stop Postgres and with it every project.
// Every 30 seconds the guard reads the disk and each project's database and
// files. Past the warning level it names the project growing fastest; past
// the stop level it makes that project read-only (a "readonly" hold: a change
// by the system, so it shows in the project's history and undoing it lifts
// it); below the resume level it lifts the hold again. A project over its own
// storage limit is held the same way until it is under it.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/mod/budget"
	"github.com/btahir/tiffin/internal/mod/postgres"
	"github.com/btahir/tiffin/internal/mod/storage"
	"github.com/btahir/tiffin/internal/platform"
)

// Guard is the disk guard's view of the data disk.
type Guard struct {
	Level         string        `json:"level" enum:"ok,warn,stop" doc:"ok; warn: the data disk is past warnPercent; stop: past stopPercent, and the project growing fastest is read-only"`
	UsedPercent   float64       `json:"usedPercent"`
	WarnPercent   int           `json:"warnPercent"`
	StopPercent   int           `json:"stopPercent" doc:"100: the guard only warns"`
	ResumePercent int           `json:"resumePercent"`
	Top           *GuardProject `json:"top,omitempty" doc:"The project growing fastest over the last hour (the biggest when none grew): the one the guard stops first"`
	ReadOnly      []Hold        `json:"readOnly" doc:"Projects whose writes are held now"`
	Message       string        `json:"message,omitempty" doc:"What is happening and how to fix it, in plain words (absent when all is well)"`
}

// GuardProject is one project's data on the data disk.
type GuardProject struct {
	Project       string `json:"project"`
	Bytes         int64  `json:"bytes" doc:"Its databases (branches included) and files"`
	DatabaseBytes int64  `json:"databaseBytes"`
	FilesBytes    int64  `json:"filesBytes"`
	GrowthBytes   int64  `json:"growthBytes" doc:"How much it grew over the last hour (since the guard started, when sooner)"`
}

// Hold is a project held read-only: its database refuses writes and its
// buckets refuse uploads.
type Hold struct {
	Project string    `json:"project"`
	Reason  string    `json:"reason" enum:"disk,limit" doc:"disk: the data disk is nearly full and it grew the most; limit: it reached its storage limit"`
	Message string    `json:"message" doc:"Which limit, and how to fix it"`
	Since   time.Time `json:"since"`
}

// holdSpec is the spec of a project's "readonly" resource.
type holdSpec struct {
	Reason  string    `json:"reason"`
	Message string    `json:"message"`
	Since   time.Time `json:"since"`
}

const (
	guardEvery   = 30 * time.Second
	growthWindow = time.Hour
)

type sizeMark struct {
	at    time.Time
	bytes int64
}

type guard struct {
	p     *platform.Platform
	mount string
	now   func() time.Time
	disk  func(mount string) Disk
	// measure returns each project's database and files bytes; a nil map
	// means that part could not be measured this round.
	measure func(ctx context.Context, projects []string) (db, files map[string]int64)
	limit   func(ctx context.Context, project string) int64

	mu     sync.Mutex
	db     map[string]int64
	files  map[string]int64
	hist   map[string][]sizeMark
	seen   map[string]string // holds after the last round: project → reason
	waived map[string]string // holds the owner lifted while still over: project → reason
	holds  map[string]Hold
	view   *Guard
}

func newGuard(p *platform.Platform, mount string) *guard {
	return &guard{p: p, mount: mount, now: time.Now, disk: disk,
		measure: func(ctx context.Context, projects []string) (map[string]int64, map[string]int64) {
			db, err := postgres.DatabaseSizes(ctx, projects)
			if err != nil {
				p.Log.Debug("disk guard: database sizes", "err", err)
			}
			files, err := storage.FilesBytes(ctx, p)
			if err != nil {
				p.Log.Debug("disk guard: files", "err", err)
			}
			return db, files
		},
		limit: func(ctx context.Context, project string) int64 {
			n, _, _ := storage.Limit(ctx, p, project)
			return n
		},
		db: map[string]int64{}, files: map[string]int64{}, hist: map[string][]sizeMark{}, seen: map[string]string{}, waived: map[string]string{}, holds: map[string]Hold{}}
}

func (g *guard) loop(ctx context.Context) {
	t := time.NewTicker(guardEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-budget.SettingsChanged():
		case <-storage.LimitsChanged():
		}
		if err := g.round(ctx); err != nil {
			g.p.Log.Error("disk guard", "err", err)
		}
	}
}

// guardIn is what one decision looks at.
type guardIn struct {
	used                         float64 // percent of the data disk in use
	set                          budget.Settings
	sizes, growth, recent, limit map[string]int64
	held, waived                 map[string]string // project → reason
}

// decide returns which projects should be read-only and why ("disk" or
// "limit"), and the owner's waivers still in force.
func decide(in guardIn) (want, waived map[string]string) {
	want, waived = map[string]string{}, map[string]string{}
	for p, r := range in.waived {
		if (r == "disk" && in.used >= float64(in.set.DiskResumePercent)) || (r == "limit" && in.limit[p] > 0 && in.sizes[p] >= in.limit[p]) {
			waived[p] = r // still over: the owner's word stands until it is not
		}
	}
	for p, lim := range in.limit {
		if lim > 0 && in.sizes[p] >= lim && waived[p] != "limit" {
			want[p] = "limit"
		}
	}
	if in.set.DiskStopPercent >= 100 {
		return want, waived
	}
	if in.used >= float64(in.set.DiskResumePercent) {
		for p, r := range in.held {
			if r == "disk" && want[p] == "" {
				want[p] = "disk"
			}
		}
	}
	if in.used >= float64(in.set.DiskStopPercent) {
		stopped := len(want) > 0
		for _, r := range waived {
			stopped = stopped || r == "disk" // the owner let the top consumer write: don't stop a bystander for it
		}
		top := topProject(in.sizes, in.growth, func(p string) bool { return want[p] != "" || waived[p] == "disk" })
		// The first hold goes to the top consumer; while the disk stays full
		// another goes to whoever is still writing, one a round.
		if top != "" && (!stopped || in.recent[top] > 0) {
			want[top] = "disk"
		}
	}
	return want, waived
}

// topProject is the project growing fastest, then the biggest (skip leaves
// projects out).
func topProject(sizes, growth map[string]int64, skip func(string) bool) string {
	names := make([]string, 0, len(sizes))
	for p := range sizes {
		if skip == nil || !skip(p) {
			names = append(names, p)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := names[i], names[j]
		if ga, gb := max(growth[a], 0), max(growth[b], 0); ga != gb {
			return ga > gb
		}
		if sizes[a] != sizes[b] {
			return sizes[a] > sizes[b]
		}
		return a < b
	})
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

// round measures, decides and applies.
func (g *guard) round(ctx context.Context) error {
	set := budget.CurrentSettings()
	d := g.disk(g.mount)
	if d.TotalBytes == 0 {
		return nil // nothing to guard (not measurable here)
	}
	projects, err := g.p.DB.ListProjects(ctx)
	if err != nil {
		return err
	}
	db, files := g.measure(ctx, projects)
	held := map[string]string{}
	for _, pr := range projects {
		_, res, err := g.p.DB.Load(ctx, pr)
		if err != nil {
			return err
		}
		if r, ok := res[change.KindReadOnly]; ok {
			var h holdSpec
			_ = json.Unmarshal(r.Spec, &h)
			held[pr] = h.Reason
		}
	}
	now := g.now()

	g.mu.Lock()
	in := guardIn{used: d.UsedPercent, set: set, sizes: map[string]int64{}, growth: map[string]int64{}, recent: map[string]int64{},
		limit: map[string]int64{}, held: held, waived: map[string]string{}}
	live := map[string]bool{}
	for _, pr := range projects {
		live[pr] = true
		if db != nil { // keep the last reading when Postgres does not answer
			g.db[pr] = db[pr]
		}
		if files != nil {
			g.files[pr] = files[pr]
		}
		size := g.db[pr] + g.files[pr]
		in.sizes[pr] = size
		h := g.hist[pr]
		if len(h) > 0 {
			in.recent[pr] = size - h[len(h)-1].bytes
		}
		for len(h) > 1 && now.Sub(h[1].at) >= growthWindow {
			h = h[1:]
		}
		if len(h) > 0 {
			in.growth[pr] = size - h[0].bytes
		}
		g.hist[pr] = append(h, sizeMark{now, size})
		if r, was := g.seen[pr]; was && held[pr] == "" {
			g.waived[pr] = r // lifted by someone other than the guard
		}
	}
	for pr := range g.hist {
		if !live[pr] {
			delete(g.hist, pr)
			delete(g.db, pr)
			delete(g.files, pr)
			delete(g.waived, pr)
		}
	}
	for pr, r := range g.waived {
		in.waived[pr] = r
	}
	g.mu.Unlock()
	for _, pr := range projects {
		in.limit[pr] = g.limit(ctx, pr)
		storage.SetDatabaseBytes(pr, g.dbBytes(pr))
	}

	want, waived := decide(in)
	var firstErr error
	for _, pr := range projects {
		if want[pr] == held[pr] {
			continue
		}
		var h *holdSpec
		reason := held[pr]
		intent := g.liftIntent(pr, reason, d.UsedPercent, in.limit[pr])
		if want[pr] != "" {
			reason = want[pr]
			h = &holdSpec{Reason: reason, Since: now.UTC()}
			h.Message, intent = g.holdWords(pr, h.Reason, in, set)
		}
		if err := g.setHold(ctx, pr, h, reason, intent); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if h == nil {
			delete(held, pr)
		} else {
			held[pr] = h.Reason
		}
	}

	g.mu.Lock()
	g.seen, g.waived = held, waived
	g.view = g.describe(in)
	g.mu.Unlock()
	return firstErr
}

func (g *guard) dbBytes(project string) int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.db[project]
}

// setHold records a hold (nil lifts it) as a change by the system and
// converges the project, which applies it. History names the project's
// storage limit as the actor for a hold of its own limit ("limit"), the disk
// guard otherwise.
func (g *guard) setHold(ctx context.Context, project string, h *holdSpec, reason, intent string) error {
	plan, err := g.p.Engine.PlanEdit(ctx, project, func(cur map[string]change.Resource) (map[string]change.Resource, error) {
		if h == nil {
			delete(cur, change.KindReadOnly)
			return cur, nil
		}
		raw, err := json.Marshal(h)
		if err != nil {
			return nil, err
		}
		cur[change.KindReadOnly] = change.Resource{Address: change.KindReadOnly, Spec: raw}
		return cur, nil
	})
	if err != nil {
		return err
	}
	actor := "disk guard"
	if reason == "limit" {
		actor = "storage limit"
	}
	c, err := g.p.Engine.Apply(ctx, change.ApplyRequest{Plan: plan, Confirm: plan.Hash, Intent: intent,
		Actor: change.Actor{Kind: "system", ID: "system", Name: actor}})
	if err != nil {
		return err
	}
	g.p.AfterApply(c)
	return nil
}

// holdWords is the message a held project's writes are refused with, and
// the change's intent.
func (g *guard) holdWords(project, reason string, in guardIn, set budget.Settings) (string, string) {
	size := storage.HumanBytes(in.sizes[project])
	if reason == "limit" {
		lim := storage.HumanBytes(in.limit[project])
		db := g.dbBytes(project)
		return fmt.Sprintf("%s uses %s of its %s storage limit (database and files), so it is read-only: its database refuses writes and its buckets refuse uploads. "+
				"%s, or raise the limit (tiffin storage quota set %s --max-bytes N). It can write again once it is under the limit.",
				project, size, lim, storage.FreeUp(db, in.sizes[project]-db), project),
			fmt.Sprintf("%s reached its %s storage limit: read-only until it is under it", project, lim)
	}
	why := fmt.Sprintf("uses the most space (%s)", size)
	if in.growth[project] > 0 {
		why = fmt.Sprintf("grew the most (%s in the last hour)", storage.HumanBytes(in.growth[project]))
	}
	return fmt.Sprintf("The box's data disk is %.0f%% full and %s %s, so it is read-only: its database refuses writes and its buckets refuse uploads, "+
			"so every other project keeps running. Free space on the box, raise the disk guard's stop level (tiffin box settings set --disk-stop-percent N), "+
			"or move to a bigger box. It can write again once the disk is below %d%%.", in.used, project, why, set.DiskResumePercent),
		fmt.Sprintf("Data disk %.0f%% full: %s is read-only until it is below %d%%", in.used, project, set.DiskResumePercent)
}

func (g *guard) liftIntent(project, reason string, used float64, limit int64) string {
	if reason == "limit" {
		if limit <= 0 {
			return project + " has no storage limit now: it can write again"
		}
		return fmt.Sprintf("%s is under its %s storage limit: it can write again", project, storage.HumanBytes(limit))
	}
	return fmt.Sprintf("Data disk down to %.0f%%: %s can write again", used, project)
}

// describe builds the view box resources shows (g.mu held).
func (g *guard) describe(in guardIn) *Guard {
	set := in.set
	v := &Guard{Level: "ok", UsedPercent: in.used, WarnPercent: set.DiskWarnPercent, StopPercent: set.DiskStopPercent, ResumePercent: set.DiskResumePercent}
	if top := topProject(in.sizes, in.growth, nil); top != "" {
		v.Top = &GuardProject{Project: top, Bytes: in.sizes[top], DatabaseBytes: g.db[top], FilesBytes: g.files[top], GrowthBytes: max(in.growth[top], 0)}
	}
	switch {
	case set.DiskStopPercent < 100 && in.used >= float64(set.DiskStopPercent):
		v.Level = "stop"
		v.Message = fmt.Sprintf("The data disk is %.0f%% full. The project growing fastest is read-only so the others keep running. Free space, give projects storage limits, or move to a bigger box.", in.used)
	case in.used >= float64(set.DiskWarnPercent):
		v.Level = "warn"
		v.Message = fmt.Sprintf("The data disk is %.0f%% full.", in.used)
		if v.Top != nil {
			v.Message += fmt.Sprintf(" %s uses the most (%s).", v.Top.Project, storage.HumanBytes(v.Top.Bytes))
			if v.Top.GrowthBytes > 0 {
				v.Message = fmt.Sprintf("The data disk is %.0f%% full. %s is growing fastest (%s in the last hour).", in.used, v.Top.Project, storage.HumanBytes(v.Top.GrowthBytes))
			}
		}
		if set.DiskStopPercent < 100 {
			v.Message += fmt.Sprintf(" At %d%% it becomes read-only so the other projects keep running.", set.DiskStopPercent)
		}
		v.Message += " Free space, give it a storage limit, or move to a bigger box."
	}
	return v
}

// state returns the last view with the holds in force (nil before the
// first round).
func (g *guard) state() *Guard {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.view == nil {
		return nil
	}
	v := *g.view
	v.ReadOnly = []Hold{}
	for _, pr := range sortedKeys(g.holds) {
		v.ReadOnly = append(v.ReadOnly, g.holds[pr])
	}
	return &v
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// hold applies a project's "readonly" resource (nil spec lifts it) to its
// database and buckets.
func (g *guard) hold(ctx context.Context, project string, spec json.RawMessage) error {
	var h holdSpec
	if spec != nil {
		if err := json.Unmarshal(spec, &h); err != nil {
			return err
		}
	}
	g.mu.Lock()
	if spec == nil {
		delete(g.holds, project)
	} else {
		g.holds[project] = Hold{Project: project, Reason: h.Reason, Message: h.Message, Since: h.Since}
	}
	g.mu.Unlock()
	storage.SetReadOnly(project, h.Reason, h.Message)
	return postgres.SetReadOnly(ctx, g.p, project, h.Message)
}

// UsageStorage is a project's storage limit and read-only state.
type UsageStorage struct {
	UsedBytes     int64  `json:"usedBytes" doc:"Its databases (branches included) and files: what its storage limit counts (measured every 30 seconds)"`
	DatabaseBytes int64  `json:"databaseBytes"`
	FilesBytes    int64  `json:"filesBytes"`
	LimitBytes    int64  `json:"limitBytes" doc:"Its storage limit; 0: none (the default)"`
	LimitSource   string `json:"limitSource" enum:"project,box-default"`
	ReadOnly      *Hold  `json:"readOnly,omitempty" doc:"Set while its writes are held: which limit and how to fix it"`
	DiskWarning   string `json:"diskWarning,omitempty" doc:"Set while the data disk is past its warning level and this project is the one growing fastest"`
}

func (g *guard) projectStorage(ctx context.Context, project string) *UsageStorage {
	g.mu.Lock()
	u := &UsageStorage{UsedBytes: g.db[project] + g.files[project], DatabaseBytes: g.db[project], FilesBytes: g.files[project], LimitSource: "box-default"}
	if h, ok := g.holds[project]; ok {
		u.ReadOnly = &h
	}
	if v := g.view; v != nil && v.Level != "ok" && v.Top != nil && v.Top.Project == project {
		u.DiskWarning = v.Message
	}
	g.mu.Unlock()
	n, own, err := storage.Limit(ctx, g.p, project)
	if err == nil {
		u.LimitBytes = n
		if own {
			u.LimitSource = "project"
		}
	}
	return u
}
