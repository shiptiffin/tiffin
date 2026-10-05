package box

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/btahir/tiffin/internal/platform"
)

// Resources is the box view: the machine, its services and its apps.
type Resources struct {
	SampledAt     time.Time      `json:"sampledAt"`
	WindowSeconds float64        `json:"windowSeconds" doc:"CPU percentages are averages over this many seconds before sampledAt"`
	Hostname      string         `json:"hostname"`
	UptimeSeconds float64        `json:"uptimeSeconds"`
	CPU           CPU            `json:"cpu"`
	Memory        Memory         `json:"memory"`
	Disks         Disks          `json:"disks"`
	Services      []Service      `json:"services" doc:"Every systemd service the box runs, biggest memory first"`
	Apps          []App          `json:"apps" doc:"Every app container (production and previews), biggest memory first"`
	Projects      []ProjectTotal `json:"projects" doc:"Every project: its app copies' memory and CPU together (its slice), its data on disk and its limits, biggest memory first. Per-project detail: projects usage."`
	Guard         *Guard         `json:"guard,omitempty" doc:"The disk guard: the data disk against its warning, stop and resume levels, the project growing fastest, and the projects held read-only (absent until its first round, 30 seconds after the box starts)"`
	Server        *Server        `json:"server,omitempty" doc:"How the box was set up on a real server (absent on a local box): its provider and name, and on Hetzner its server type, volume and the bigger types it can change to"`
}

// Server is a box on a real server, as `tiffin up` recorded it.
type Server struct {
	Provider string                  `json:"provider" enum:"hetzner,ssh"`
	Name     string                  `json:"name" doc:"The box's name on the owner's computer (tiffin up --name)"`
	Machine  *platform.ServerMachine `json:"machine,omitempty" doc:"Hetzner only: the server type, the data volume and the next bigger types with monthly prices, as of the last tiffin up. Resize with: tiffin up --name <name> --type <type> (restarts the box for about 2 minutes) or --volume-size <GB> (no downtime)"`
}

// CPU is the machine's processors.
type CPU struct {
	Count       int     `json:"count"`
	UsedPercent float64 `json:"usedPercent" doc:"Share of all cores busy over the window, 0-100"`
	Load1       float64 `json:"load1"`
	Load5       float64 `json:"load5"`
	Load15      float64 `json:"load15"`
}

// Memory is the machine's RAM and swap.
type Memory struct {
	TotalBytes     uint64  `json:"totalBytes"`
	UsedBytes      uint64  `json:"usedBytes" doc:"Total minus available"`
	AvailableBytes uint64  `json:"availableBytes" doc:"What new work can use without swapping (includes reclaimable cache)"`
	UsedPercent    float64 `json:"usedPercent"`
	SwapTotalBytes uint64  `json:"swapTotalBytes"`
	SwapUsedBytes  uint64  `json:"swapUsedBytes"`
}

// Disks are the two filesystems that matter.
type Disks struct {
	Data   Disk `json:"data" doc:"The data disk (/var/lib/tiffin): databases, buckets, builds, images, backups"`
	System Disk `json:"system" doc:"The system disk (/): the OS and packages"`
}

// Disk is one filesystem.
type Disk struct {
	Mount       string  `json:"mount"`
	TotalBytes  uint64  `json:"totalBytes"`
	UsedBytes   uint64  `json:"usedBytes"`
	FreeBytes   uint64  `json:"freeBytes" doc:"Available to Tiffin (excludes blocks reserved for root)"`
	UsedPercent float64 `json:"usedPercent" doc:"As df reports it: used / (used + free)"`
}

// Service is one systemd unit the box runs.
type Service struct {
	Name        string  `json:"name" example:"postgres"`
	Unit        string  `json:"unit" example:"tiffin-postgres.service"`
	Description string  `json:"description"`
	State       string  `json:"state" example:"active" doc:"systemd ActiveState: active, inactive, failed, activating, deactivating"`
	SubState    string  `json:"subState" example:"running" doc:"systemd SubState: running, exited (one-shot units), dead..."`
	MemoryBytes uint64  `json:"memoryBytes"`
	CacheBytes  uint64  `json:"cacheBytes" doc:"Of memoryBytes, file cache the kernel hands back under pressure (cgroup memory.stat file)"`
	CPUSeconds  float64 `json:"cpuSeconds" doc:"CPU time used since the service started"`
	CPUPercent  float64 `json:"cpuPercent" doc:"CPU use over the window; 100 = one full core"`
	Restarts    int     `json:"restarts"`
}

// App is one app container.
type App struct {
	Project          string  `json:"project"`
	App              string  `json:"app"`
	Preview          string  `json:"preview,omitempty"`
	Deploy           string  `json:"deploy,omitempty"`
	Container        string  `json:"container"`
	State            string  `json:"state" example:"running" doc:"running, exited, created, paused, restarting"`
	MemoryBytes      uint64  `json:"memoryBytes"`
	CacheBytes       uint64  `json:"cacheBytes" doc:"Of memoryBytes, file cache the kernel hands back under pressure (cgroup memory.stat file)"`
	MemoryLimitBytes uint64  `json:"memoryLimitBytes,omitempty" doc:"The memoryMB cap from the manifest (0: none)"`
	CPUSeconds       float64 `json:"cpuSeconds"`
	CPUPercent       float64 `json:"cpuPercent" doc:"CPU use over the window; 100 = one full core"`
}

// container is what the container lister knows.
type container struct {
	ID     string // full ID
	Name   string
	State  string
	Labels map[string]string
}

type mark struct {
	cpu float64
	at  time.Time
}

// sampler takes and caches samples.
type sampler struct {
	root      string // "" in production; tests point it at a fake tree
	dataMount string
	now       func() time.Time
	systemctl func(ctx context.Context, units []string) ([]byte, error)
	list      func(ctx context.Context) ([]container, error)

	mu      sync.Mutex
	last    *Resources
	prev    map[string]mark // CPU seconds per service/container
	prevCPU [2]uint64       // busy, total jiffies
	prevAt  time.Time
	ctrs    map[string]container // by full ID
	ctrsAt  time.Time
	foreign map[string]bool // container cgroups the list does not know
	track   *tracker        // per-project CPU marks and disk measurements (nil in some tests)
	guard   *guard          // the disk guard (nil in tests)
}

func newSampler(root, dataMount string) *sampler {
	return &sampler{root: root, dataMount: dataMount, now: time.Now, systemctl: systemctlShow, list: nerdctlList, prev: map[string]mark{}}
}

func (s *sampler) path(p string) string { return filepath.Join(s.root, p) }

// get returns a sample at most cacheFor old. The very first one is taken
// twice, a quarter second apart, so CPU percentages have a window.
func (s *sampler) get(ctx context.Context) *Resources {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last != nil && s.now().Sub(s.last.SampledAt) < cacheFor {
		return s.last
	}
	if s.prevAt.IsZero() {
		s.sample(ctx)
		time.Sleep(250 * time.Millisecond)
	}
	s.last = s.sample(ctx)
	return s.last
}

func (s *sampler) sample(ctx context.Context) *Resources {
	now := s.now().UTC()
	r := &Resources{SampledAt: now, Services: []Service{}, Apps: []App{}}
	if !s.prevAt.IsZero() {
		r.WindowSeconds = round(now.Sub(s.prevAt).Seconds(), 2)
	}
	r.Hostname, _ = os.Hostname()
	if c, _ := platform.LoadServerConfig(); c != nil {
		r.Server = &Server{Provider: c.Provider, Name: c.Name, Machine: c.Machine}
	}
	s.cpu(r)
	s.memory(r)
	r.Disks.Data = disk(s.dataMount)
	r.Disks.System = disk("/")
	if b, err := os.ReadFile(s.path("/proc/uptime")); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			r.UptimeSeconds, _ = strconv.ParseFloat(f[0], 64)
		}
	}
	seen := map[string]bool{}
	s.services(ctx, r, now, seen)
	s.apps(ctx, r, now, seen)
	r.Projects = s.projectTotals(ctx, s.track, now, seen)
	r.Guard = s.guard.state()
	for k := range s.prev {
		if !seen[k] {
			delete(s.prev, k)
		}
	}
	s.prevAt = now
	return r
}

// rate turns a cumulative CPU-seconds counter into percent of one core
// since the previous sample.
func (s *sampler) rate(key string, cpu float64, now time.Time, seen map[string]bool) float64 {
	seen[key] = true
	p, ok := s.prev[key]
	s.prev[key] = mark{cpu, now}
	if !ok || cpu < p.cpu {
		return 0
	}
	dt := now.Sub(p.at).Seconds()
	if dt <= 0 {
		return 0
	}
	return round((cpu-p.cpu)/dt*100, 1)
}

func (s *sampler) cpu(r *Resources) {
	f, err := os.Open(s.path("/proc/stat"))
	if err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "cpu ") {
				var total, idle uint64
				for i, x := range strings.Fields(line)[1:] {
					if i >= 8 { // guest time is already counted in user/nice
						break
					}
					v, _ := strconv.ParseUint(x, 10, 64)
					total += v
					if i == 3 || i == 4 { // idle, iowait
						idle += v
					}
				}
				busy := total - idle
				if pt := s.prevCPU[1]; pt > 0 && total > pt && busy >= s.prevCPU[0] {
					r.CPU.UsedPercent = round(float64(busy-s.prevCPU[0])/float64(total-pt)*100, 1)
				}
				s.prevCPU = [2]uint64{busy, total}
			} else if strings.HasPrefix(line, "cpu") {
				r.CPU.Count++
			}
		}
		f.Close()
	}
	if b, err := os.ReadFile(s.path("/proc/loadavg")); err == nil {
		if f := strings.Fields(string(b)); len(f) >= 3 {
			r.CPU.Load1, _ = strconv.ParseFloat(f[0], 64)
			r.CPU.Load5, _ = strconv.ParseFloat(f[1], 64)
			r.CPU.Load15, _ = strconv.ParseFloat(f[2], 64)
		}
	}
}

func (s *sampler) memory(r *Resources) {
	b, err := os.ReadFile(s.path("/proc/meminfo"))
	if err != nil {
		return
	}
	var swapFree uint64
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(f[1], 10, 64)
		v *= 1024
		switch f[0] {
		case "MemTotal:":
			r.Memory.TotalBytes = v
		case "MemAvailable:":
			r.Memory.AvailableBytes = v
		case "SwapTotal:":
			r.Memory.SwapTotalBytes = v
		case "SwapFree:":
			swapFree = v
		}
	}
	m := &r.Memory
	if m.TotalBytes >= m.AvailableBytes {
		m.UsedBytes = m.TotalBytes - m.AvailableBytes
	}
	if m.TotalBytes > 0 {
		m.UsedPercent = round(float64(m.UsedBytes)/float64(m.TotalBytes)*100, 1)
	}
	if m.SwapTotalBytes >= swapFree {
		m.SwapUsedBytes = m.SwapTotalBytes - swapFree
	}
}

func disk(mount string) Disk {
	d := Disk{Mount: mount}
	var st syscall.Statfs_t
	if err := syscall.Statfs(mount, &st); err != nil {
		return d
	}
	bs := uint64(st.Bsize)
	d.TotalBytes = uint64(st.Blocks) * bs
	d.FreeBytes = uint64(st.Bavail) * bs
	if free := uint64(st.Bfree) * bs; d.TotalBytes >= free {
		d.UsedBytes = d.TotalBytes - free
	}
	if d.UsedBytes+d.FreeBytes > 0 {
		d.UsedPercent = round(float64(d.UsedBytes)/float64(d.UsedBytes+d.FreeBytes)*100, 1)
	}
	return d
}

// known names and describes the units modules install.
var known = map[string][2]string{
	"tiffin.service":                  {"tiffin", "Tiffin itself: the API, dashboard, HTTPS edge, email, queues, workflows and analytics"},
	"tiffin-postgres.service":         {"postgres", "PostgreSQL: every project's database"},
	"tiffin-valkey.service":           {"valkey", "Valkey: caches, sessions and KV for every project"},
	"tiffin-storage.service":          {"storage", "S3-compatible object storage for buckets"},
	"tiffin-auth.service":             {"auth", "The auth engine: users, sessions and organizations"},
	"tiffin-metrics.service":          {"victoria-metrics", "VictoriaMetrics: the metrics store"},
	"tiffin-logs.service":             {"victoria-logs", "VictoriaLogs: the logs store"},
	"tiffin-firewall.service":         {"firewall", "Host firewall rules (applied once at boot)"},
	"tiffin-runtime-firewall.service": {"app-firewall", "Keeps app ports private (applied once at boot)"},
	"containerd.service":              {"containerd", "Runs the app containers"},
	"buildkit.service":                {"buildkit", "Builds app images"},
	"crowdsec.service":                {"crowdsec", "CrowdSec: detects and bans abusive IPs"},
}

// units are the services modules installed (regular unit files in
// /etc/systemd/system) plus the packaged ones Tiffin relies on.
func (s *sampler) units() []string {
	set := map[string]bool{}
	files, _ := filepath.Glob(s.path("/etc/systemd/system/*.service"))
	for _, f := range files {
		if fi, err := os.Lstat(f); err == nil && fi.Mode().IsRegular() {
			set[filepath.Base(f)] = true
		}
	}
	for _, u := range []string{"containerd.service", "crowdsec.service"} {
		for _, dir := range []string{"/usr/lib/systemd/system", "/lib/systemd/system"} {
			if _, err := os.Stat(s.path(filepath.Join(dir, u))); err == nil {
				set[u] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for u := range set {
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}

func systemctlShow(ctx context.Context, units []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	args := append([]string{"show", "--no-pager", "-p", "Id,LoadState,ActiveState,SubState,NRestarts,MemoryCurrent,CPUUsageNSec"}, units...)
	return exec.CommandContext(ctx, "systemctl", args...).Output()
}

func (s *sampler) services(ctx context.Context, r *Resources, now time.Time, seen map[string]bool) {
	units := s.units()
	if len(units) == 0 {
		return
	}
	out, err := s.systemctl(ctx, units)
	if err != nil && len(out) == 0 {
		return
	}
	for _, block := range bytes.Split(out, []byte("\n\n")) {
		var sv Service
		load := ""
		for _, line := range strings.Split(string(block), "\n") {
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			switch k {
			case "Id":
				sv.Unit = v
			case "LoadState":
				load = v
			case "ActiveState":
				sv.State = v
			case "SubState":
				sv.SubState = v
			case "NRestarts":
				sv.Restarts, _ = strconv.Atoi(v)
			case "MemoryCurrent":
				sv.MemoryBytes, _ = strconv.ParseUint(v, 10, 64) // "[not set]" stays 0
			case "CPUUsageNSec":
				ns, _ := strconv.ParseUint(v, 10, 64)
				sv.CPUSeconds = round(float64(ns)/1e9, 2)
			}
		}
		if sv.Unit == "" || load == "not-found" {
			continue
		}
		if k, ok := known[sv.Unit]; ok {
			sv.Name, sv.Description = k[0], k[1]
		} else {
			sv.Name = strings.TrimSuffix(strings.TrimPrefix(sv.Unit, "tiffin-"), ".service")
		}
		sv.CacheBytes = min(fileCache(s.path(filepath.Join("/sys/fs/cgroup/system.slice", sv.Unit))), sv.MemoryBytes)
		sv.CPUPercent = s.rate("unit:"+sv.Unit, sv.CPUSeconds, now, seen)
		r.Services = append(r.Services, sv)
	}
	sort.SliceStable(r.Services, func(i, j int) bool {
		if r.Services[i].MemoryBytes != r.Services[j].MemoryBytes {
			return r.Services[i].MemoryBytes > r.Services[j].MemoryBytes
		}
		return r.Services[i].Name < r.Services[j].Name
	})
}

var containerDir = regexp.MustCompile(`^(?:cri-containerd-|nerdctl-|docker-)?([0-9a-f]{64})(?:\.scope)?$`)

type cgroupStats struct {
	mem, cache, limit uint64
	cpu               float64
}

// fileCache reads the reclaimable file cache from a cgroup's memory.stat.
func fileCache(dir string) uint64 {
	b, err := os.ReadFile(filepath.Join(dir, "memory.stat"))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "file "); ok {
			n, _ := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
			return n
		}
	}
	return 0
}

// cgroups finds container cgroups (named by their 64-hex ID, as containerd
// and nerdctl create them) up to four levels deep.
func (s *sampler) cgroups() map[string]cgroupStats {
	root := s.path("/sys/fs/cgroup")
	out := map[string]cgroupStats{}
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > 4 {
			return
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			p := filepath.Join(dir, e.Name())
			m := containerDir.FindStringSubmatch(e.Name())
			if m == nil {
				walk(p, depth+1)
				continue
			}
			var st cgroupStats
			if b, err := os.ReadFile(filepath.Join(p, "memory.current")); err == nil {
				st.mem, _ = strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
			}
			st.cache = fileCache(p)
			if b, err := os.ReadFile(filepath.Join(p, "memory.max")); err == nil {
				st.limit, _ = strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64) // "max" stays 0
			}
			if b, err := os.ReadFile(filepath.Join(p, "cpu.stat")); err == nil {
				for _, line := range strings.Split(string(b), "\n") {
					if v, ok := strings.CutPrefix(line, "usage_usec "); ok {
						us, _ := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
						st.cpu = float64(us) / 1e6
					}
				}
			}
			out[m[1]] = st
		}
	}
	walk(root, 0)
	return out
}

// containers returns the app containers by ID. Listing them costs a
// nerdctl call, so the list is cached and refreshed every 30 seconds or
// when a container cgroup appears that the list does not know.
func (s *sampler) containers(ctx context.Context, ids map[string]cgroupStats) map[string]container {
	stale := s.ctrs == nil || s.now().Sub(s.ctrsAt) > 30*time.Second
	for id := range ids {
		if _, ok := s.ctrs[id]; !ok && !s.foreign[id] {
			stale = true
		}
	}
	if stale {
		cs, err := s.list(ctx)
		if err == nil {
			s.ctrs = map[string]container{}
			for _, c := range cs {
				s.ctrs[c.ID] = c
			}
			s.ctrsAt = s.now()
			// Cgroups of containers that are not Tiffin's (another
			// namespace) would otherwise trigger a refresh every sample.
			s.foreign = map[string]bool{}
			for id := range ids {
				if _, ok := s.ctrs[id]; !ok {
					s.foreign[id] = true
				}
			}
		}
	}
	return s.ctrs
}

func (s *sampler) apps(ctx context.Context, r *Resources, now time.Time, seen map[string]bool) {
	cg := s.cgroups()
	for id, c := range s.containers(ctx, cg) {
		if c.Labels["tiffin.app"] == "" {
			continue
		}
		a := App{Project: c.Labels["tiffin.project"], App: c.Labels["tiffin.app"], Preview: c.Labels["tiffin.preview"],
			Deploy: c.Labels["tiffin.deploy"], Container: c.Name, State: c.State}
		if st, ok := cg[id]; ok {
			a.MemoryBytes, a.CacheBytes, a.MemoryLimitBytes, a.CPUSeconds = st.mem, min(st.cache, st.mem), st.limit, round(st.cpu, 2)
			a.CPUPercent = s.rate("ctr:"+id, st.cpu, now, seen)
			if a.State == "" || a.State == "unknown" {
				a.State = "running"
			}
		} else if a.State == "running" {
			a.State = "starting" // listed as up, but its cgroup is not there (yet)
		}
		r.Apps = append(r.Apps, a)
	}
	sort.SliceStable(r.Apps, func(i, j int) bool {
		x, y := r.Apps[i], r.Apps[j]
		if x.MemoryBytes != y.MemoryBytes {
			return x.MemoryBytes > y.MemoryBytes
		}
		return x.Container < y.Container
	})
}

// nerdctlList lists Tiffin's containers (namespace tiffin) with labels.
func nerdctlList(ctx context.Context) ([]container, error) {
	const bin = "/usr/local/bin/nerdctl"
	if _, err := os.Stat(bin); err != nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--namespace", "tiffin", "ps", "--all", "--no-trunc", "--format", "{{json .}}").Output()
	if err != nil {
		return nil, err
	}
	return parseNerdctlPS(out), nil
}

func parseNerdctlPS(out []byte) []container {
	var cs []container
	for _, line := range bytes.Split(out, []byte("\n")) {
		var row struct {
			ID     string `json:"ID"`
			Names  string `json:"Names"`
			Labels string `json:"Labels"`
			Status string `json:"Status"`
		}
		if json.Unmarshal(line, &row) != nil || len(row.ID) < 12 {
			continue
		}
		c := container{ID: row.ID, Name: row.Names, Labels: map[string]string{}, State: psState(row.Status)}
		for _, kv := range strings.Split(row.Labels, ",") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				c.Labels[strings.TrimSpace(k)] = v
			}
		}
		cs = append(cs, c)
	}
	return cs
}

// psState maps nerdctl's Status column ("Up 5 minutes", "Exited (0) 2
// minutes ago", "Created") to a state word.
func psState(status string) string {
	f := strings.Fields(strings.ToLower(status))
	if len(f) == 0 {
		return "unknown"
	}
	switch f[0] {
	case "up":
		return "running"
	case "exited":
		return "exited"
	}
	return f[0]
}

func round(f float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(f*p) / p
}
