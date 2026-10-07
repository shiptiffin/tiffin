package budget

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ParentSlice holds every project's slice. Its MemoryMax is the pool, so
// all apps together can never take the platform's memory; its TasksMax
// (AppsTasksMax) leaves the platform tasks to run with.
const ParentSlice = "tiffin-p.slice"

// AppsTasksMax and ProjectTasksMax cap the tasks (processes and threads)
// of all apps together and of one project's, as systemd reads a
// percentage: of the box's own task limit.
const (
	AppsTasksMax    = "50%"
	ProjectTasksMax = "25%"
)

// Slice is the systemd slice a project's app containers run in, e.g.
// "tiffin-p-shop.slice". Dashes in the project name are escaped (\x2d) as
// systemd requires, so "my-shop" is not nested under "my".
func Slice(project string) string {
	return "tiffin-p-" + strings.ReplaceAll(project, "-", `\x2d`) + ".slice"
}

// SliceDir is the slice's cgroup v2 directory.
func SliceDir(root, project string) string {
	return filepath.Join(root, "/sys/fs/cgroup/tiffin.slice", ParentSlice, Slice(project))
}

// ParentDir is the parent slice's cgroup directory.
func ParentDir(root string) string {
	return filepath.Join(root, "/sys/fs/cgroup/tiffin.slice", ParentSlice)
}

// Stats are a slice's live cgroup readings.
type Stats struct {
	MemoryBytes uint64 // memory.current
	CacheBytes  uint64 // file cache in memory.stat
	MaxBytes    uint64 // memory.max (0: max)
	HighBytes   uint64 // memory.high (0: max)
	LowBytes    uint64 // memory.low
	SwapBytes   uint64 // memory.swap.current
	// StallMicros is memory.pressure's "some" total: how long tasks of the
	// slice waited for memory.
	StallMicros uint64
	CPUSeconds  float64
	QuotaCPUs   float64 // cpu.max quota/period (0: max)
	Copies      int     // container scopes inside
	// Events from memory.events (hierarchical): high = throttled at
	// MemoryHigh, max = hit MemoryMax, oom = allocation failed at a limit,
	// oomKill = processes killed for memory.
	High, Max, OOM, OOMKill uint64
	Exists                  bool
}

// ReadStats reads a cgroup directory. A missing directory gives zero
// Stats with Exists false.
func ReadStats(dir string) Stats {
	var s Stats
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	if _, err := os.Stat(dir); err != nil {
		return s
	}
	s.Exists = true
	s.MemoryBytes, _ = strconv.ParseUint(read("memory.current"), 10, 64)
	s.MaxBytes, _ = strconv.ParseUint(read("memory.max"), 10, 64)
	s.HighBytes, _ = strconv.ParseUint(read("memory.high"), 10, 64)
	s.LowBytes, _ = strconv.ParseUint(read("memory.low"), 10, 64)
	s.SwapBytes, _ = strconv.ParseUint(read("memory.swap.current"), 10, 64)
	for _, line := range strings.Split(read("memory.pressure"), "\n") {
		if rest, ok := strings.CutPrefix(line, "some "); ok {
			for _, f := range strings.Fields(rest) {
				if v, ok := strings.CutPrefix(f, "total="); ok {
					s.StallMicros, _ = strconv.ParseUint(v, 10, 64)
				}
			}
		}
	}
	for _, line := range strings.Split(read("memory.stat"), "\n") {
		if v, ok := strings.CutPrefix(line, "file "); ok {
			s.CacheBytes, _ = strconv.ParseUint(v, 10, 64)
		}
	}
	s.CacheBytes = min(s.CacheBytes, s.MemoryBytes)
	for _, line := range strings.Split(read("cpu.stat"), "\n") {
		if v, ok := strings.CutPrefix(line, "usage_usec "); ok {
			us, _ := strconv.ParseUint(v, 10, 64)
			s.CPUSeconds = float64(us) / 1e6
		}
	}
	if f := strings.Fields(read("cpu.max")); len(f) == 2 && f[0] != "max" {
		q, _ := strconv.ParseFloat(f[0], 64)
		p, _ := strconv.ParseFloat(f[1], 64)
		if p > 0 {
			s.QuotaCPUs = q / p
		}
	}
	for _, line := range strings.Split(read("memory.events"), "\n") {
		k, v, _ := strings.Cut(line, " ")
		n, _ := strconv.ParseUint(v, 10, 64)
		switch k {
		case "high":
			s.High = n
		case "max":
			s.Max = n
		case "oom":
			s.OOM = n
		case "oom_kill":
			s.OOMKill = n
		}
	}
	if ents, err := os.ReadDir(dir); err == nil {
		for _, e := range ents {
			if e.IsDir() && strings.HasSuffix(e.Name(), ".scope") {
				s.Copies++
			}
		}
	}
	return s
}

// props renders a slice's systemd properties for limits l.
func props(l Limits) []string {
	p := []string{
		"MemoryMax=" + strconv.Itoa(l.MemoryMaxMB) + "M",
		"MemorySwapMax=" + strconv.Itoa(l.SwapMaxMB) + "M",
		"MemoryLow=" + strconv.Itoa(l.MemoryLowMB) + "M",
		"CPUWeight=" + strconv.Itoa(l.CPUWeight),
		"MemoryHigh=infinity", // see Resolve: no throttling short of the cap
		// Processes and threads: a project that forks without end stops at
		// its share of the box's tasks, not the box's own limit.
		"TasksMax=" + ProjectTasksMax,
	}
	if l.CPUs > 0 {
		// systemd takes whole percents: 1.5 cores = 150%.
		p = append(p, "CPUQuota="+strconv.Itoa(max(1, int(l.CPUs*100+0.5)))+"%")
	} else {
		p = append(p, "CPUQuota=")
	}
	// Disk reads and writes: a limited project gets its share's weight
	// against everyone else's 100 when the disk is busy. The kernel honours
	// weights only with an IO scheduler that has them (BFQ, or io.cost).
	return append(p, "IOWeight="+strconv.Itoa(max(1, l.IOWeight)))
}

// systemd applies slice properties. Tests use a fake.
type systemd interface {
	// Set applies properties to a unit live and keeps them across reboots.
	Set(ctx context.Context, unit string, props []string) error
	// Remove forgets a unit's properties and stops it.
	Remove(ctx context.Context, unit string) error
}

type systemctl struct{}

func (systemctl) Set(ctx context.Context, unit string, props []string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// Without --runtime the properties persist (/etc/systemd/system.control),
	// so a rebooted box restarts containers inside their limits.
	var out bytes.Buffer
	c := exec.CommandContext(ctx, "systemctl", append([]string{"set-property", unit}, props...)...)
	c.Stdout, c.Stderr = &out, &out
	if err := c.Run(); err != nil {
		return fmt.Errorf("systemctl set-property %s: %w: %s", unit, err, strings.TrimSpace(out.String()))
	}
	return nil
}

func (systemctl) Remove(ctx context.Context, unit string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	changed := false
	for _, d := range []string{"/etc/systemd/system.control", "/run/systemd/system.control"} {
		p := filepath.Join(d, unit+".d")
		if _, err := os.Stat(p); err == nil {
			if err := os.RemoveAll(p); err != nil {
				return err
			}
			changed = true
		}
	}
	if changed {
		_ = exec.CommandContext(ctx, "systemctl", "daemon-reload").Run()
	}
	_ = exec.CommandContext(ctx, "systemctl", "stop", unit).Run()
	return nil
}
