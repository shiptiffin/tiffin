package box

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/mod/budget"
	"github.com/btahir/tiffin/internal/platform"
)

// Usage is what one project uses of the box, against its limits.
type Usage struct {
	Project     string        `json:"project"`
	SampledAt   time.Time     `json:"sampledAt"`
	Budget      Budget        `json:"budget"`
	LimitSource string        `json:"limitSource" enum:"project,box default,automatic" doc:"Where the project's limits come from: its own resources in tiffin.config.ts, the box-wide default share, or automatic (elastic)"`
	Memory      UsageMemory   `json:"memory"`
	CPU         UsageCPU      `json:"cpu"`
	Disk        UsageDisk     `json:"disk"`
	Storage     *UsageStorage `json:"storage,omitempty" doc:"Its storage limit (databases and files together; none by default) and whether its writes are held read-only (absent until the disk guard's first round)"`
	Apps        []UsageApp    `json:"apps" doc:"Every app of the project, and every preview with running copies"`
	Services    UsageServices `json:"services"`
}

// Budget is what the project's tiffin.config.ts asks for.
type Budget struct {
	Auto            bool     `json:"auto" doc:"True when the project sets no resources of its own (automatic, or the box default share if one is set)"`
	MemoryMB        *int     `json:"memoryMB,omitempty"`
	CPUs            *float64 `json:"cpus,omitempty"`
	MaxSharePercent *int     `json:"maxSharePercent,omitempty"`
}

// UsageMemory is the project's memory: all its app copies together.
type UsageMemory struct {
	UsedBytes      uint64 `json:"usedBytes" doc:"Memory the project's app copies use, file cache included"`
	CacheBytes     uint64 `json:"cacheBytes" doc:"Of usedBytes, file cache the kernel takes back first when memory is tight"`
	SwapBytes      uint64 `json:"swapBytes" doc:"Memory swapped out to disk because the box was tight (automatic projects over their fair share only; fixed caps never swap)"`
	LimitBytes     uint64 `json:"limitBytes" doc:"The most the project may use now (hard cap). Automatic projects: the box's memory for apps minus 128 MB for each copy other projects run"`
	ProtectedBytes uint64 `json:"protectedBytes" doc:"Memory the kernel keeps for the project when the box runs short (its fair share, or its whole memoryMB budget)"`
	HeadroomBytes  uint64 `json:"headroomBytes" doc:"How much more the project could take right now: the smaller of what is left under its limit and what the box has available"`
	LimitSource    string `json:"limitSource" enum:"project,box default,automatic"`
	Pressure       string `json:"pressure" enum:"none,some,oom" doc:"In the last hour: none; some (the project was held back: it reached a limit, waited for memory or has memory swapped out); oom (an app copy was killed for memory and restarted: the project is using all the memory it was given)"`
}

// UsageCPU is the project's CPU use.
type UsageCPU struct {
	Percent     float64  `json:"percent" doc:"CPU use over the last few seconds; 100 = one full core"`
	LimitCpus   *float64 `json:"limitCpus,omitempty" doc:"CPU cap in cores; absent: no cap (the project shares the CPUs equally with others under contention)"`
	Weight      int      `json:"weight" doc:"CPU weight under contention; every project has the same"`
	LimitSource string   `json:"limitSource" enum:"project,box default,automatic"`
}

// UsageDisk is the project's data on disk.
type UsageDisk struct {
	DatabaseBytes int64      `json:"databaseBytes" doc:"Postgres databases, branches included"`
	FilesBytes    int64      `json:"filesBytes" doc:"Bucket files"`
	KVBytes       int64      `json:"kvBytes" doc:"Valkey keys (held in memory, snapshotted to disk)"`
	TotalBytes    int64      `json:"totalBytes"`
	MeasuredAt    *time.Time `json:"measuredAt,omitempty" doc:"When disk use was measured (refreshed every 30 seconds while asked for); absent until the first measurement finishes"`
}

// UsageApp is one app environment's copies.
type UsageApp struct {
	App         string  `json:"app"`
	Preview     string  `json:"preview,omitempty"`
	Instances   int     `json:"instances" doc:"Copies running now"`
	MemoryBytes uint64  `json:"memoryBytes"`
	CPUPercent  float64 `json:"cpuPercent"`
	State       string  `json:"state" doc:"running, starting, or stopped (no copies running)"`
}

// UsageServices are the project's box services.
type UsageServices struct {
	Postgres *PGUsage      `json:"postgres,omitempty"`
	Valkey   *KVUsage      `json:"valkey,omitempty"`
	Storage  *StorageUsage `json:"storage,omitempty"`
}

// PGUsage is the project's Postgres.
type PGUsage struct {
	Connections   int64 `json:"connections" doc:"Open connections to its databases"`
	DatabaseBytes int64 `json:"databaseBytes"`
}

// KVUsage is the project's Valkey namespace.
type KVUsage struct {
	Keys        int64 `json:"keys"`
	MemoryBytes int64 `json:"memoryBytes"`
}

// StorageUsage is the project's buckets.
type StorageUsage struct {
	Buckets int64 `json:"buckets"`
	Objects int64 `json:"objects"`
	Bytes   int64 `json:"bytes"`
}

// ProjectTotal is one project's line in /v1/box/resources.
type ProjectTotal struct {
	Project     string   `json:"project"`
	MemoryBytes uint64   `json:"memoryBytes" doc:"All the project's app copies"`
	CacheBytes  uint64   `json:"cacheBytes"`
	SwapBytes   uint64   `json:"swapBytes"`
	LimitBytes  uint64   `json:"limitBytes"`
	CPUPercent  float64  `json:"cpuPercent" doc:"100 = one full core"`
	LimitCpus   *float64 `json:"limitCpus,omitempty"`
	DiskBytes   int64    `json:"diskBytes" doc:"Database, files and KV (measured in the background; 0 until the first measurement)"`
	Budget      Budget   `json:"budget"`
	LimitSource string   `json:"limitSource" enum:"project,box default,automatic"`
	Pressure    string   `json:"pressure" enum:"none,some,oom"`
}

// ---- background measurements ----

// cpuMark is a CPU-seconds reading.
type cpuMark struct {
	cpu float64
	at  time.Time
}

// tracker keeps CPU marks of project slices and containers (taken every
// few seconds) and disk measurements (refreshed in the background).
type tracker struct {
	root string
	p    *platform.Platform

	mu       sync.Mutex
	marks    map[string][2]cpuMark // key → older, newer
	disk     map[string]*diskEntry
	cache    map[string]*Usage // project → last answer
	cacheGen map[string]uint64 // project → budget generation of that answer
}

type diskEntry struct {
	at       time.Time
	running  bool
	done     chan struct{}
	services []platform.ServiceUsage
}

const (
	markEvery  = 5 * time.Second
	diskFresh  = 30 * time.Second
	usageCache = 2 * time.Second
)

func newTracker(root string, p *platform.Platform) *tracker {
	return &tracker{root: root, p: p, marks: map[string][2]cpuMark{}, disk: map[string]*diskEntry{}, cache: map[string]*Usage{}, cacheGen: map[string]uint64{}}
}

func (t *tracker) loop(ctx context.Context) {
	tk := time.NewTicker(markEvery)
	defer tk.Stop()
	for {
		t.markAll(time.Now())
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
	}
}

// markAll records CPU seconds of every project slice and its containers.
func (t *tracker) markAll(now time.Time) {
	seen := map[string]bool{}
	ents, _ := os.ReadDir(budget.ParentDir(t.root))
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "tiffin-p-") {
			continue
		}
		dir := filepath.Join(budget.ParentDir(t.root), e.Name())
		st := budget.ReadStats(dir)
		t.mark("slice:"+e.Name(), st.CPUSeconds, now, seen)
		scopes, _ := os.ReadDir(dir)
		for _, sc := range scopes {
			if m := containerDir.FindStringSubmatch(sc.Name()); m != nil {
				t.mark("ctr:"+m[1], cgroupCPU(filepath.Join(dir, sc.Name())), now, seen)
			}
		}
	}
	t.mu.Lock()
	for k := range t.marks {
		if !seen[k] {
			delete(t.marks, k)
		}
	}
	t.mu.Unlock()
}

func (t *tracker) mark(key string, cpu float64, now time.Time, seen map[string]bool) {
	seen[key] = true
	t.mu.Lock()
	m := t.marks[key]
	t.marks[key] = [2]cpuMark{m[1], {cpu, now}}
	t.mu.Unlock()
}

// rate is CPU percent between the newest mark at least a second old and now.
func (t *tracker) rate(key string, cpu float64, now time.Time) float64 {
	t.mu.Lock()
	m := t.marks[key]
	t.mu.Unlock()
	base := m[1]
	if now.Sub(base.at) < time.Second {
		base = m[0]
	}
	dt := now.Sub(base.at).Seconds()
	if base.at.IsZero() || dt <= 0 || cpu < base.cpu {
		return 0
	}
	return round((cpu-base.cpu)/dt*100, 1)
}

// services returns the project's last disk measurement and starts a new
// one in the background when it is stale. When there is none yet it waits
// up to wait for the first.
func (t *tracker) services(ctx context.Context, project string, wait time.Duration) ([]platform.ServiceUsage, *time.Time) {
	t.mu.Lock()
	e := t.disk[project]
	if e == nil {
		e = &diskEntry{}
		t.disk[project] = e
	}
	if !e.running && time.Since(e.at) > diskFresh && t.p != nil {
		e.running, e.done = true, make(chan struct{})
		go t.measure(project, e)
	}
	done, at := e.done, e.at
	t.mu.Unlock()
	if at.IsZero() && done != nil && wait > 0 {
		select {
		case <-done:
		case <-time.After(wait):
		case <-ctx.Done():
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if e.at.IsZero() {
		return nil, nil
	}
	when := e.at
	return e.services, &when
}

func (t *tracker) measure(project string, e *diskEntry) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var out []platform.ServiceUsage
	for _, m := range platform.Modules() {
		ur, ok := m.(platform.UsageReporter)
		if !ok {
			continue
		}
		u, err := ur.ProjectUsage(ctx, t.p, project)
		if err != nil {
			if t.p.Log != nil {
				t.p.Log.Warn("project usage", "module", m.Name(), "project", project, "err", err)
			}
			continue
		}
		if u != nil {
			out = append(out, *u)
		}
	}
	t.mu.Lock()
	e.services, e.at, e.running = out, time.Now().UTC(), false
	close(e.done)
	t.mu.Unlock()
}

// memAvailable is MemAvailable from /proc/meminfo, in bytes.
func memAvailable(root string) uint64 {
	b, err := os.ReadFile(filepath.Join(root, "/proc/meminfo"))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "MemAvailable:" {
			var kb uint64
			_ = json.Unmarshal([]byte(f[1]), &kb)
			return kb * 1024
		}
	}
	return 0
}

func cgroupCPU(dir string) float64 {
	b, err := os.ReadFile(filepath.Join(dir, "cpu.stat"))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "usage_usec "); ok {
			var us float64
			_ = json.Unmarshal([]byte(strings.TrimSpace(v)), &us)
			return us / 1e6
		}
	}
	return 0
}

// ---- assembling answers ----

func budgetOf(r *manifest.Resources) Budget {
	b := Budget{Auto: r == nil}
	if r == nil {
		return b
	}
	if r.MemoryMB > 0 {
		v := r.MemoryMB
		b.MemoryMB = &v
	}
	if r.CPUs > 0 {
		v := r.CPUs
		b.CPUs = &v
	}
	if r.MaxSharePercent > 0 {
		v := r.MaxSharePercent
		b.MaxSharePercent = &v
	}
	return b
}

func orAutomatic(s string) string {
	if s == "" {
		return budget.SourceAutomatic
	}
	return s
}

// limits merges the slice's live cgroup limits with what the budget module
// resolved (the cgroup wins: it is what the kernel enforces).
func limits(st budget.Stats, v budget.View) (mem, low uint64, cpus *float64) {
	mem, low = st.MaxBytes, st.LowBytes
	if mem == 0 {
		mem = uint64(v.Limits.MemoryMaxMB) << 20
	}
	if low == 0 {
		low = uint64(v.Limits.MemoryLowMB) << 20
	}
	c := st.QuotaCPUs
	if c == 0 {
		c = v.Limits.CPUs
	}
	if c > 0 {
		c = round(c, 2)
		cpus = &c
	}
	return mem, low, cpus
}

// usage builds a project's usage answer. available is the box's
// MemAvailable in bytes.
func (s *sampler) usage(ctx context.Context, t *tracker, project string, apps []string, available uint64) *Usage {
	now := s.now().UTC()
	t.mu.Lock()
	gen := budget.Generation()
	if c := t.cache[project]; c != nil && now.Sub(c.SampledAt) < usageCache && t.cacheGen[project] == gen {
		t.mu.Unlock()
		return c
	}
	t.mu.Unlock()
	dir := budget.SliceDir(s.root, project)
	st := budget.ReadStats(dir)
	v := budget.Lookup(project, st)
	u := &Usage{Project: project, SampledAt: now, Budget: budgetOf(v.Resources), LimitSource: orAutomatic(v.Limits.Source()),
		Apps: []UsageApp{}}
	mem, low, cpus := limits(st, v)
	held := st.MemoryBytes - st.CacheBytes
	u.Memory = UsageMemory{UsedBytes: st.MemoryBytes, CacheBytes: st.CacheBytes, SwapBytes: st.SwapBytes, LimitBytes: mem, ProtectedBytes: low,
		LimitSource: orAutomatic(v.Limits.MemorySource), Pressure: v.Pressure}
	if mem > held {
		u.Memory.HeadroomBytes = min(mem-held, available)
	}
	u.CPU = UsageCPU{Percent: t.rate("slice:"+filepath.Base(dir), st.CPUSeconds, now), LimitCpus: cpus, Weight: budget.CPUWeight,
		LimitSource: orAutomatic(v.Limits.CPUSource)}

	// Apps: every app the project declares, then the copies its slice holds.
	byEnv := map[[2]string]*UsageApp{}
	for _, a := range apps {
		byEnv[[2]string{a, ""}] = &UsageApp{App: a, State: "stopped"}
	}
	ids := map[string]cgroupStats{}
	scopes, _ := os.ReadDir(dir)
	for _, sc := range scopes {
		if m := containerDir.FindStringSubmatch(sc.Name()); m != nil {
			p := filepath.Join(dir, sc.Name())
			cs := cgroupStats{cpu: cgroupCPU(p)}
			if b, err := os.ReadFile(filepath.Join(p, "memory.current")); err == nil {
				_ = json.Unmarshal([]byte(strings.TrimSpace(string(b))), &cs.mem)
			}
			ids[m[1]] = cs
		}
	}
	if len(ids) > 0 {
		s.mu.Lock()
		ctrs := s.containers(ctx, ids)
		s.mu.Unlock()
		for id, cs := range ids {
			c, ok := ctrs[id]
			if !ok || c.Labels["tiffin.app"] == "" {
				continue
			}
			key := [2]string{c.Labels["tiffin.app"], c.Labels["tiffin.preview"]}
			a := byEnv[key]
			if a == nil {
				a = &UsageApp{App: key[0], Preview: key[1], State: "stopped"}
				byEnv[key] = a
			}
			a.Instances++
			a.MemoryBytes += cs.mem
			a.CPUPercent = round(a.CPUPercent+t.rate("ctr:"+id, cs.cpu, now), 1)
			a.State = "running"
		}
	}
	for _, a := range byEnv {
		u.Apps = append(u.Apps, *a)
	}
	sort.Slice(u.Apps, func(i, j int) bool {
		if u.Apps[i].App != u.Apps[j].App {
			return u.Apps[i].App < u.Apps[j].App
		}
		return u.Apps[i].Preview < u.Apps[j].Preview
	})

	svcs, at := t.services(ctx, project, 60*time.Millisecond)
	u.Disk.MeasuredAt = at
	for _, sv := range svcs {
		switch sv.Disk {
		case "database":
			u.Disk.DatabaseBytes += sv.Bytes
		case "files":
			u.Disk.FilesBytes += sv.Bytes
		case "kv":
			u.Disk.KVBytes += sv.Bytes
		}
		switch sv.Service {
		case "postgres":
			u.Services.Postgres = &PGUsage{Connections: sv.Counts["connections"], DatabaseBytes: sv.Bytes}
		case "valkey":
			u.Services.Valkey = &KVUsage{Keys: sv.Counts["keys"], MemoryBytes: sv.Bytes}
		case "storage":
			u.Services.Storage = &StorageUsage{Buckets: sv.Counts["buckets"], Objects: sv.Counts["objects"], Bytes: sv.Bytes}
		}
	}
	u.Disk.TotalBytes = u.Disk.DatabaseBytes + u.Disk.FilesBytes + u.Disk.KVBytes
	if s.guard.state() != nil {
		u.Storage = s.guard.projectStorage(ctx, project)
	}
	t.mu.Lock()
	t.cache[project] = u
	t.cacheGen[project] = gen
	t.mu.Unlock()
	return u
}

// projectTotals is the projects section of /v1/box/resources.
func (s *sampler) projectTotals(ctx context.Context, t *tracker, now time.Time, seen map[string]bool) []ProjectTotal {
	names := map[string]bool{}
	for n := range budget.Projects() {
		names[n] = true
	}
	if t != nil && t.p != nil {
		if ps, err := t.p.DB.ListProjects(ctx); err == nil {
			for _, n := range ps {
				names[n] = true
			}
		}
	}
	out := []ProjectTotal{}
	for n := range names {
		st := budget.ReadStats(budget.SliceDir(s.root, n))
		v := budget.Lookup(n, st)
		mem, _, cpus := limits(st, v)
		pt := ProjectTotal{Project: n, MemoryBytes: st.MemoryBytes, CacheBytes: st.CacheBytes, SwapBytes: st.SwapBytes, LimitBytes: mem, LimitCpus: cpus,
			Budget: budgetOf(v.Resources), LimitSource: orAutomatic(v.Limits.Source()), Pressure: v.Pressure}
		pt.CPUPercent = s.rate("slice:"+n, st.CPUSeconds, now, seen)
		if t != nil {
			svcs, _ := t.services(ctx, n, 0)
			for _, sv := range svcs {
				pt.DiskBytes += sv.Bytes
			}
		}
		out = append(out, pt)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MemoryBytes != out[j].MemoryBytes {
			return out[i].MemoryBytes > out[j].MemoryBytes
		}
		return out[i].Project < out[j].Project
	})
	return out
}

// appNames lists a project's apps from its applied resources; ok is false
// when the project does not exist.
func appNames(ctx context.Context, p *platform.Platform, project string) ([]string, bool, error) {
	if p == nil {
		return nil, true, nil
	}
	v, res, err := p.DB.Load(ctx, project)
	if err != nil || v == 0 {
		return nil, false, err
	}
	var apps []string
	for addr := range res {
		if change.Kind(addr) == change.KindApp {
			apps = append(apps, change.Name(addr))
		}
	}
	sort.Strings(apps)
	return apps, true, nil
}
