package budget

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/btahir/tiffin/internal/manifest"
)

// Limit sources, as /usage reports them.
const (
	SourceProject    = "project"     // the project's own resources in tiffin.config.ts
	SourceBoxDefault = "box default" // the box-wide default share (box settings)
	SourceAutomatic  = "automatic"   // elastic: whatever the box has free
)

const (
	// FloorPerCopyMB is the memory an automatic project leaves for each app
	// copy other projects run, so a runaway cannot squeeze its neighbours
	// down to nothing.
	FloorPerCopyMB = 128
	// MinAutoMB is the least an automatic project can be capped to, however
	// many copies its neighbours run.
	MinAutoMB = 256
	// CPUWeight is every project's CPU weight: equal shares under
	// contention, full speed when the box is idle.
	CPUWeight = 100
)

// Box is what the machine has.
type Box struct {
	MemoryMB int `json:"memoryMB"`
	CPUs     int `json:"cpus"`
}

// Reserve is the memory the platform keeps for itself, by part.
type Reserve struct {
	// BaseMB covers the Tiffin service (API, edge, queues, analytics), the
	// auth engine, object storage, metrics and logs, the container runtime
	// and the kernel.
	BaseMB int `json:"baseMB"`
	// PostgresMB is Postgres's shared buffers (an eighth of memory, 128 MB-4 GB).
	PostgresMB int `json:"postgresMB"`
	// ValkeyMB is Valkey's cache limit (an eighth of memory, 128 MB-4 GB).
	ValkeyMB int `json:"valkeyMB"`
	// TotalMB is what the platform keeps, at most three quarters of memory.
	TotalMB int `json:"totalMB"`
}

// baseReserveMB is what Tiffin's own processes need besides Postgres's and
// Valkey's caches. A fresh, idle 3 GB box has about 650 MB in use in all
// (tiffin ~65 MB, auth ~40, crowdsec ~70, metrics and logs ~40,
// containerd/storage/buildkit ~25, the rest kernel and page tables); the
// base leaves room for them to grow under load.
const baseReserveMB = 640

// ReserveFor computes the platform reserve for a box with memMB of RAM.
// Postgres and Valkey size themselves the same way (see their Config).
func ReserveFor(memMB int) Reserve {
	r := Reserve{BaseMB: baseReserveMB, PostgresMB: clamp(memMB/8, 128, 4096), ValkeyMB: clamp(memMB/8, 128, 4096)}
	r.TotalMB = min(r.BaseMB+r.PostgresMB+r.ValkeyMB, memMB*3/4)
	return r
}

// PoolMB is the memory the box keeps for apps: total minus the reserve.
func (b Box) PoolMB() int { return max(0, b.MemoryMB-ReserveFor(b.MemoryMB).TotalMB) }

// Explain says in plain words how the pool is computed.
func (b Box) Explain() string {
	r := ReserveFor(b.MemoryMB)
	return fmt.Sprintf("This box has %d MB of memory. Tiffin keeps %d MB for itself: about %d MB for its own services "+
		"(the API and edge, auth, storage, metrics and logs, the container runtime and the kernel), %d MB for Postgres's "+
		"shared buffers and %d MB for Valkey's cache (an eighth of memory each). That leaves %d MB for apps, shared by "+
		"every project.", b.MemoryMB, r.TotalMB, r.BaseMB, r.PostgresMB, r.ValkeyMB, b.PoolMB())
}

// ReadBox measures the machine under root ("" in production): memory from
// /proc/meminfo and CPUs from /proc/stat. ok is false when they cannot be
// read (not Linux).
func ReadBox(root string) (Box, bool) {
	var b Box
	f, err := os.Open(filepath.Join(root, "/proc/meminfo"))
	if err != nil {
		return b, false
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if fs := strings.Fields(sc.Text()); len(fs) >= 2 && fs[0] == "MemTotal:" {
			kb, _ := strconv.Atoi(fs[1])
			b.MemoryMB = kb / 1024
		}
	}
	f.Close()
	if raw, err := os.ReadFile(filepath.Join(root, "/proc/stat")); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "cpu") && !strings.HasPrefix(line, "cpu ") {
				b.CPUs++
			}
		}
	}
	return b, b.MemoryMB > 0 && b.CPUs > 0
}

// Settings are the box-wide budget settings.
type Settings struct {
	// DefaultMaxSharePercent caps every project that sets no limit of its
	// own at this share of the box (memory for apps and CPUs). 100 means no
	// cap: automatic projects are elastic.
	DefaultMaxSharePercent int `json:"defaultMaxSharePercent" minimum:"5" maximum:"100" doc:"Projects that set no limit of their own may use at most this percentage of the box: of the memory it keeps for apps and of its CPUs. 100 (the default) means elastic: a project grows into whatever the box has free."`
}

// DefaultSettings is a fresh box: elastic.
var DefaultSettings = Settings{DefaultMaxSharePercent: 100}

// Project is one project's input to Resolve.
type Project struct {
	Name      string
	Resources *manifest.Resources // nil: automatic
	Copies    int                 // app containers it runs now (production and previews)
}

// Limits are a project's effective limits.
type Limits struct {
	MemoryMaxMB int `json:"memoryMaxMB"`
	MemoryLowMB int `json:"memoryLowMB"`
	// SwapMaxMB is how much of the project may be swapped out when the box
	// is tight: half its cap for automatic projects (so a project over its
	// fair share slows down instead of anyone being killed), 0 for fixed caps
	// (a capped project fails fast and restarts).
	SwapMaxMB    int     `json:"swapMaxMB"`
	CPUs         float64 `json:"cpus,omitempty"` // CPU quota in cores; 0: none
	CPUWeight    int     `json:"cpuWeight"`
	MemorySource string  `json:"memorySource"`
	CPUSource    string  `json:"cpuSource"`
}

// Source is the overall source: the project's own when it set either
// limit, else the memory source.
func (l Limits) Source() string {
	if l.MemorySource == SourceProject || l.CPUSource == SourceProject {
		return SourceProject
	}
	return l.MemorySource
}

// Resolve computes every project's effective limits on box b.
//
//   - memoryMB caps the project and guarantees it (MemoryLow = the cap).
//   - maxSharePercent caps it at a share of the pool (memory for apps) and
//     of the CPUs; with memoryMB as well, the lower memory limit wins.
//   - a project with no memory limit of its own takes the box default share
//     when it is below 100, else it is automatic: it may grow to the pool
//     minus FloorPerCopyMB for every copy other projects run (at least
//     MinAutoMB).
//   - shares and the box default never take the floor from neighbours either.
//   - every project without a memoryMB budget is protected (MemoryLow) up
//     to a fair share: what the memoryMB budgets leave of the pool, split
//     evenly between the projects that run copies. Automatic projects may
//     swap up to half their cap, so when apps together reach the pool the
//     kernel swaps out the project over its fair share first and nobody is
//     killed (measured: shop at 1 GB and blog asking for 700 MB on a 1.5 GB
//     pool ended at 691 MB + 371 MB swapped and 756 MB, both serving);
//     without swap the kernel could only kill the biggest app process.
//   - fixed caps (memoryMB, shares, the box default) never swap: a project
//     at its cap has its file cache reclaimed, then its biggest process is
//     killed and restarted.
//
// There is no MemoryHigh: app memory cannot swap, so a project held above
// MemoryHigh is throttled to a standstill instead of failing fast (measured:
// a 256 MB project with MemoryHigh at 230 MB froze for minutes). At
// MemoryMax the kernel reclaims the project's file cache first, then kills
// the biggest process, and the container restarts.
func Resolve(b Box, s Settings, projects []Project) map[string]Limits {
	pool := b.PoolMB()
	totalCopies := 0
	for _, p := range projects {
		totalCopies += p.Copies
	}
	share := func(pct int) int { return pool * pct / 100 }
	cpuShare := func(pct int) float64 { return math.Floor(float64(b.CPUs)*float64(pct)) / 100 }
	out := make(map[string]Limits, len(projects))
	reserved, elastic := 0, 0
	for _, p := range projects {
		r := p.Resources
		if r == nil {
			r = &manifest.Resources{}
		}
		auto := max(MinAutoMB, pool-FloorPerCopyMB*(totalCopies-p.Copies))
		l := Limits{CPUWeight: CPUWeight}
		switch {
		case r.MemoryMB > 0:
			l.MemoryMaxMB, l.MemorySource = r.MemoryMB, SourceProject
			if r.MaxSharePercent > 0 {
				l.MemoryMaxMB = min(l.MemoryMaxMB, max(share(r.MaxSharePercent), 1))
			}
		case r.MaxSharePercent > 0:
			l.MemoryMaxMB, l.MemorySource = min(max(share(r.MaxSharePercent), 1), auto), SourceProject
		case s.DefaultMaxSharePercent > 0 && s.DefaultMaxSharePercent < 100:
			l.MemoryMaxMB, l.MemorySource = min(max(share(s.DefaultMaxSharePercent), 1), auto), SourceBoxDefault
		default:
			l.MemoryMaxMB, l.MemorySource = auto, SourceAutomatic
		}
		if l.MemorySource == SourceAutomatic {
			l.SwapMaxMB = l.MemoryMaxMB / 2
		}
		switch {
		case r.CPUs > 0 || r.MaxSharePercent > 0:
			l.CPUSource = SourceProject
			l.CPUs = r.CPUs
			if r.MaxSharePercent > 0 {
				c := max(cpuShare(r.MaxSharePercent), 0.01)
				if l.CPUs == 0 || c < l.CPUs {
					l.CPUs = c
				}
			}
		case s.DefaultMaxSharePercent > 0 && s.DefaultMaxSharePercent < 100:
			l.CPUs, l.CPUSource = max(cpuShare(s.DefaultMaxSharePercent), 0.01), SourceBoxDefault
		default:
			l.CPUSource = SourceAutomatic
		}
		if r.MemoryMB > 0 {
			reserved += l.MemoryMaxMB
		} else if p.Copies > 0 {
			elastic++
		}
		out[p.Name] = l
	}
	fair := max(0, pool-reserved) / max(1, elastic)
	for _, p := range projects {
		l := out[p.Name]
		if p.Resources != nil && p.Resources.MemoryMB > 0 {
			l.MemoryLowMB = l.MemoryMaxMB
		} else {
			l.MemoryLowMB = min(fair, l.MemoryMaxMB)
		}
		out[p.Name] = l
	}
	return out
}

// Problem is one reason a budget does not fit the box.
type Problem struct {
	Path    string // JSON pointer into the manifest
	Message string
}

// Check says whether project's resources fit box b next to the other
// projects' memoryMB budgets (others: project → resources).
func Check(b Box, project string, r *manifest.Resources, others map[string]*manifest.Resources) []Problem {
	if r == nil {
		return nil
	}
	var probs []Problem
	if r.CPUs > float64(b.CPUs) {
		probs = append(probs, Problem{"/resources/cpus", fmt.Sprintf("cpus %s is more than this box has (%d CPUs); use at most %d, or maxSharePercent for a share of the box",
			fmtCPUs(r.CPUs), b.CPUs, b.CPUs)})
	}
	if r.MemoryMB <= 0 {
		return probs
	}
	pool := b.PoolMB()
	if r.MemoryMB > pool {
		probs = append(probs, Problem{"/resources/memoryMB", fmt.Sprintf("memoryMB %d is more than this box keeps for apps (%d MB); use at most %d, or maxSharePercent for a share of the box",
			r.MemoryMB, pool, pool)})
		return probs
	}
	used, names := 0, []string{}
	for name, o := range others {
		if name == project || o == nil || o.MemoryMB <= 0 {
			continue
		}
		used += o.MemoryMB
		names = append(names, fmt.Sprintf("%s %d MB", name, o.MemoryMB))
	}
	if used+r.MemoryMB > pool {
		sort.Strings(names)
		probs = append(probs, Problem{"/resources/memoryMB", fmt.Sprintf("memoryMB %d does not fit: this box keeps %d MB for apps and other projects' budgets already take %d MB (%s), so at most %d MB is left",
			r.MemoryMB, pool, used, strings.Join(names, ", "), max(0, pool-used))})
	}
	return probs
}

func fmtCPUs(c float64) string { return strconv.FormatFloat(c, 'f', -1, 64) }

func clamp(v, lo, hi int) int { return max(lo, min(v, hi)) }
