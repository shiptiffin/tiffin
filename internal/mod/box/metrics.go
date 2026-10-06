package box

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"time"

	"github.com/btahir/tiffin/internal/mod/budget"
	"github.com/btahir/tiffin/internal/mod/postgres"
	"github.com/btahir/tiffin/internal/platform"
)

// dataEvery is how often Metrics has a project's data (database, files,
// KV) measured again; the Usage page's 30 seconds is for the page in view.
const dataEvery = 5 * time.Minute

// Metrics writes every project's usage as gauges for the metrics store, so
// its Usage page can draw it over time: memory and CPU against their limits
// (only when it has a limit of its own or the box's default), its data and
// its database connections. All of it was measured already.
//
//	tiffin_project_memory_bytes{project}         memory its apps hold (no file cache)
//	tiffin_project_memory_limit_bytes{project}   its memory limit
//	tiffin_project_cpu_percent{project}          100 = one core
//	tiffin_project_cpu_limit_cores{project}
//	tiffin_project_data_bytes{project,part}      part: database, files or kv
//	tiffin_project_db_connections{project}
func (m *Module) Metrics(ctx context.Context, w io.Writer) {
	s := m.sampler()
	if s == nil || s.track == nil {
		return
	}
	t := s.track
	now := time.Now()
	names := map[string]bool{}
	for n := range budget.Projects() {
		names[n] = true
	}
	if t.p != nil && t.p.DB != nil {
		if ps, err := t.p.DB.ListProjects(ctx); err == nil {
			for _, n := range ps {
				names[n] = true
			}
		}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	s.metrics(w, t, sorted, now)
}

func (s *sampler) metrics(w io.Writer, t *tracker, projects []string, now time.Time) {
	for _, n := range projects {
		g := func(name string, v float64, part string) {
			if part != "" {
				fmt.Fprintf(w, "%s{project=%q,part=%q} %g\n", name, n, part, v)
				return
			}
			fmt.Fprintf(w, "%s{project=%q} %g\n", name, n, v)
		}
		dir := budget.SliceDir(s.root, n)
		st := budget.ReadStats(dir)
		v := budget.Lookup(n, st)
		mem, _, cpus := limits(st, v)
		g("tiffin_project_memory_bytes", float64(st.MemoryBytes-min(st.CacheBytes, st.MemoryBytes)), "")
		if orAutomatic(v.Limits.MemorySource) != "automatic" && mem > 0 {
			g("tiffin_project_memory_limit_bytes", float64(mem), "")
		}
		g("tiffin_project_cpu_percent", t.rate("slice:"+filepath.Base(dir), st.CPUSeconds, now), "")
		if cpus != nil && orAutomatic(v.Limits.CPUSource) != "automatic" {
			g("tiffin_project_cpu_limit_cores", *cpus, "")
		}
		parts := map[string]int64{}
		svcs := t.latest(n, dataEvery)
		for _, sv := range svcs {
			if sv.Disk != "" {
				parts[sv.Disk] += sv.Bytes
			}
		}
		for _, part := range []string{"database", "files", "kv"} {
			if b, ok := parts[part]; ok {
				g("tiffin_project_data_bytes", float64(b), part)
			}
		}
		for _, sv := range svcs {
			if sv.Service == "postgres" {
				c := sv.Counts["connections"]
				if l := postgres.LimitUsage(n); l.Tracked {
					c = int64(l.Connections)
				}
				g("tiffin_project_db_connections", float64(c), "")
			}
		}
	}
}

// latest returns the project's last data measurement, starting a new one
// in the background when it is older than maxAge.
func (t *tracker) latest(project string, maxAge time.Duration) []platform.ServiceUsage {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.disk[project]
	if e == nil {
		e = &diskEntry{}
		t.disk[project] = e
	}
	if !e.running && time.Since(e.at) > maxAge && t.p != nil {
		e.running, e.done = true, make(chan struct{})
		go t.measure(project, e)
	}
	return e.services
}
