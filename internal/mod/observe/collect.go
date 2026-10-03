package observe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
)

// Snapshot is one sample of the box's vital signs. It is kept in memory (for
// alerts and the overview, so both work when the metrics store is down) and
// pushed to VictoriaMetrics.
type Snapshot struct {
	At         time.Time   `json:"at"`
	CPUs       int         `json:"cpus"`
	CPU        float64     `json:"cpuUsedRatio" doc:"Share of all CPU time used since the previous sample, 0-1"`
	Load1      float64     `json:"load1"`
	Load5      float64     `json:"load5"`
	Load15     float64     `json:"load15"`
	MemTotal   uint64      `json:"memoryTotalBytes"`
	MemAvail   uint64      `json:"memoryAvailableBytes"`
	MemUsed    float64     `json:"memoryUsedRatio"`
	SwapTotal  uint64      `json:"swapTotalBytes"`
	SwapFree   uint64      `json:"swapFreeBytes"`
	Disks      []Disk      `json:"disks"`
	Net        []NetDev    `json:"network"`
	Units      []Unit      `json:"units"`
	Containers []Container `json:"containers"`
	UptimeSec  float64     `json:"uptimeSeconds"`
}

// Disk is one mounted filesystem.
type Disk struct {
	Mount     string  `json:"mount"`
	Total     uint64  `json:"totalBytes"`
	Free      uint64  `json:"freeBytes"`
	UsedRatio float64 `json:"usedRatio"`
}

// NetDev is one network interface's counters.
type NetDev struct {
	Name string `json:"name"`
	Rx   uint64 `json:"rxBytes"`
	Tx   uint64 `json:"txBytes"`
}

// Unit is one systemd service the box runs.
type Unit struct {
	Name       string  `json:"name"`
	Active     bool    `json:"active"`
	State      string  `json:"state"`
	Restarts   int     `json:"restarts"`
	MemBytes   uint64  `json:"memoryBytes"`
	CPUSeconds float64 `json:"cpuSeconds"`
}

// Container is one container cgroup (containerd).
type Container struct {
	ID         string  `json:"id"`
	Name       string  `json:"name,omitempty"`
	Project    string  `json:"project,omitempty"`
	App        string  `json:"app,omitempty"`
	Cgroup     string  `json:"cgroup"`
	MemBytes   uint64  `json:"memoryBytes"`
	CPUSeconds float64 `json:"cpuSeconds"`
}

// Collector samples /proc, statfs, systemd and cgroup v2 files. No agent or
// exporter is needed.
type Collector struct {
	Root    string   // "" in production; tests point it at a fake tree
	Mounts  []string // filesystems to report
	mu      sync.Mutex
	last    *Snapshot
	prevCPU [2]uint64 // busy, total jiffies
}

// Latest returns the most recent snapshot (nil before the first sample).
func (c *Collector) Latest() *Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

func (c *Collector) path(p string) string { return filepath.Join(c.Root, p) }

// Sample takes a snapshot.
func (c *Collector) Sample(ctx context.Context) *Snapshot {
	s := &Snapshot{At: time.Now().UTC()}
	c.cpu(s)
	c.mem(s)
	c.load(s)
	for _, m := range c.Mounts {
		var st syscall.Statfs_t
		if err := syscall.Statfs(m, &st); err != nil {
			continue
		}
		total := uint64(st.Blocks) * uint64(st.Bsize)
		free := uint64(st.Bavail) * uint64(st.Bsize)
		d := Disk{Mount: m, Total: total, Free: free}
		if total > 0 {
			// df's definition: used / (used + available to users).
			used := total - uint64(st.Bfree)*uint64(st.Bsize)
			d.UsedRatio = float64(used) / float64(used+free)
		}
		s.Disks = append(s.Disks, d)
	}
	c.net(s)
	s.Units = units(ctx)
	s.Containers = c.containers()
	if b, err := os.ReadFile(c.path("/proc/uptime")); err == nil {
		f := strings.Fields(string(b))
		if len(f) > 0 {
			s.UptimeSec, _ = strconv.ParseFloat(f[0], 64)
		}
	}
	c.mu.Lock()
	c.last = s
	c.mu.Unlock()
	return s
}

func (c *Collector) cpu(s *Snapshot) {
	f, err := os.Open(c.path("/proc/stat"))
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "cpu ") {
			var total, idle uint64
			for i, x := range strings.Fields(line)[1:] {
				v, _ := strconv.ParseUint(x, 10, 64)
				if i >= 8 { // guest time is already in user/nice
					break
				}
				total += v
				if i == 3 || i == 4 { // idle, iowait
					idle += v
				}
			}
			busy := total - idle
			c.mu.Lock()
			if pt := c.prevCPU[1]; pt > 0 && total > pt {
				s.CPU = float64(busy-c.prevCPU[0]) / float64(total-pt)
			}
			c.prevCPU = [2]uint64{busy, total}
			c.mu.Unlock()
		} else if strings.HasPrefix(line, "cpu") {
			s.CPUs++
		}
	}
}

func (c *Collector) mem(s *Snapshot) {
	b, err := os.ReadFile(c.path("/proc/meminfo"))
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(f[1], 10, 64)
		v *= 1024
		switch f[0] {
		case "MemTotal:":
			s.MemTotal = v
		case "MemAvailable:":
			s.MemAvail = v
		case "SwapTotal:":
			s.SwapTotal = v
		case "SwapFree:":
			s.SwapFree = v
		}
	}
	if s.MemTotal > 0 {
		s.MemUsed = 1 - float64(s.MemAvail)/float64(s.MemTotal)
	}
}

func (c *Collector) load(s *Snapshot) {
	b, err := os.ReadFile(c.path("/proc/loadavg"))
	if err != nil {
		return
	}
	f := strings.Fields(string(b))
	if len(f) >= 3 {
		s.Load1, _ = strconv.ParseFloat(f[0], 64)
		s.Load5, _ = strconv.ParseFloat(f[1], 64)
		s.Load15, _ = strconv.ParseFloat(f[2], 64)
	}
}

func (c *Collector) net(s *Snapshot) {
	b, err := os.ReadFile(c.path("/proc/net/dev"))
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "lo" || strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "cni") || strings.HasPrefix(name, "docker") {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		rx, _ := strconv.ParseUint(f[0], 10, 64)
		tx, _ := strconv.ParseUint(f[8], 10, 64)
		s.Net = append(s.Net, NetDev{Name: name, Rx: rx, Tx: tx})
	}
}

// boxUnits are the services modules install (unit files in
// /etc/systemd/system) plus containerd when present.
func boxUnits() []string {
	var out []string
	files, _ := filepath.Glob("/etc/systemd/system/*.service")
	for _, f := range files {
		if fi, err := os.Lstat(f); err == nil && fi.Mode().IsRegular() {
			out = append(out, filepath.Base(f))
		}
	}
	if _, err := os.Stat("/usr/lib/systemd/system/containerd.service"); err == nil {
		out = append(out, "containerd.service")
	}
	sort.Strings(out)
	return out
}

func units(ctx context.Context) []Unit {
	names := boxUnits()
	if len(names) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	args := append([]string{"show", "--no-pager", "-p", "Id,ActiveState,SubState,NRestarts,MemoryCurrent,CPUUsageNSec"}, names...)
	out, err := exec.CommandContext(ctx, "systemctl", args...).Output()
	if err != nil && len(out) == 0 {
		return nil
	}
	return parseSystemctlShow(out)
}

func parseSystemctlShow(out []byte) []Unit {
	var res []Unit
	for _, block := range bytes.Split(out, []byte("\n\n")) {
		u := Unit{}
		for _, line := range strings.Split(string(block), "\n") {
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			switch k {
			case "Id":
				u.Name = v
			case "ActiveState":
				u.State = v
				u.Active = v == "active"
			case "NRestarts":
				u.Restarts, _ = strconv.Atoi(v)
			case "MemoryCurrent":
				u.MemBytes, _ = strconv.ParseUint(v, 10, 64) // "[not set]" parses as 0
			case "CPUUsageNSec":
				ns, _ := strconv.ParseUint(v, 10, 64)
				u.CPUSeconds = float64(ns) / 1e9
			}
		}
		if u.Name != "" {
			res = append(res, u)
		}
	}
	return res
}

var containerDir = regexp.MustCompile(`^(?:cri-containerd-|nerdctl-|docker-)?([0-9a-f]{64})(?:\.scope)?$`)

// containers finds container cgroups (named by a 64-hex container ID, as
// containerd, nerdctl and the CRI plugin create them) up to four levels deep.
func (c *Collector) containers() []Container {
	root := c.path("/sys/fs/cgroup")
	var out []Container
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
			if m := containerDir.FindStringSubmatch(e.Name()); m != nil {
				ct := Container{ID: m[1][:12], Cgroup: strings.TrimPrefix(p, root)}
				if b, err := os.ReadFile(filepath.Join(p, "memory.current")); err == nil {
					ct.MemBytes, _ = strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
				}
				if b, err := os.ReadFile(filepath.Join(p, "cpu.stat")); err == nil {
					for _, line := range strings.Split(string(b), "\n") {
						if v, ok := strings.CutPrefix(line, "usage_usec "); ok {
							us, _ := strconv.ParseUint(v, 10, 64)
							ct.CPUSeconds = float64(us) / 1e6
						}
					}
				}
				out = append(out, ct)
				continue
			}
			walk(p, depth+1)
		}
	}
	walk(root, 0)
	return out
}

// Prometheus renders a snapshot in the text exposition format.
func (s *Snapshot) Prometheus(w *bytes.Buffer) {
	g := func(name string, v float64, labels ...string) {
		w.WriteString(name)
		if len(labels) > 0 {
			w.WriteByte('{')
			for i := 0; i+1 < len(labels); i += 2 {
				if i > 0 {
					w.WriteByte(',')
				}
				fmt.Fprintf(w, "%s=%q", labels[i], labels[i+1])
			}
			w.WriteByte('}')
		}
		fmt.Fprintf(w, " %s\n", strconv.FormatFloat(v, 'g', -1, 64))
	}
	g("tiffin_cpu_count", float64(s.CPUs))
	g("tiffin_cpu_used_ratio", s.CPU)
	g("tiffin_load1", s.Load1)
	g("tiffin_load5", s.Load5)
	g("tiffin_load15", s.Load15)
	g("tiffin_memory_total_bytes", float64(s.MemTotal))
	g("tiffin_memory_available_bytes", float64(s.MemAvail))
	g("tiffin_memory_used_ratio", s.MemUsed)
	g("tiffin_swap_total_bytes", float64(s.SwapTotal))
	g("tiffin_swap_free_bytes", float64(s.SwapFree))
	g("tiffin_uptime_seconds", s.UptimeSec)
	for _, d := range s.Disks {
		g("tiffin_disk_total_bytes", float64(d.Total), "mount", d.Mount)
		g("tiffin_disk_free_bytes", float64(d.Free), "mount", d.Mount)
		g("tiffin_disk_used_ratio", d.UsedRatio, "mount", d.Mount)
	}
	for _, n := range s.Net {
		g("tiffin_network_receive_bytes_total", float64(n.Rx), "device", n.Name)
		g("tiffin_network_transmit_bytes_total", float64(n.Tx), "device", n.Name)
	}
	for _, u := range s.Units {
		up := 0.0
		if u.Active {
			up = 1
		}
		g("tiffin_unit_up", up, "unit", u.Name)
		g("tiffin_unit_restarts_total", float64(u.Restarts), "unit", u.Name)
		g("tiffin_unit_memory_bytes", float64(u.MemBytes), "unit", u.Name)
		g("tiffin_unit_cpu_seconds_total", u.CPUSeconds, "unit", u.Name)
	}
	for _, c := range s.Containers {
		g("tiffin_container_memory_bytes", float64(c.MemBytes), "container", c.ID, "name", c.Name, "project", c.Project, "app", c.App)
		g("tiffin_container_cpu_seconds_total", c.CPUSeconds, "container", c.ID, "name", c.Name, "project", c.Project, "app", c.App)
	}
}

// nerdctlBin is the runtime's container CLI; when present, containers get
// their project and app from the runtime's labels.
const nerdctlBin = "/usr/local/bin/nerdctl"

// labelContainers fills Project/App/Name from container labels.
func (m *Module) labelContainers(ctx context.Context, s *Snapshot) {
	if len(s.Containers) == 0 {
		return
	}
	if _, err := os.Stat(nerdctlBin); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, nerdctlBin, "--namespace", "tiffin", "ps", "--all", "--no-trunc", "--format", "{{json .}}").Output()
	if err != nil {
		return
	}
	type row struct {
		ID     string `json:"ID"`
		Names  string `json:"Names"`
		Labels string `json:"Labels"`
	}
	byID := map[string]row{}
	for _, line := range bytes.Split(out, []byte("\n")) {
		var r row
		if json.Unmarshal(line, &r) == nil && len(r.ID) >= 12 {
			byID[r.ID[:12]] = r
		}
	}
	for i := range s.Containers {
		r, ok := byID[s.Containers[i].ID]
		if !ok {
			continue
		}
		s.Containers[i].Name = r.Names
		for _, kv := range strings.Split(r.Labels, ",") {
			k, v, _ := strings.Cut(kv, "=")
			switch k {
			case "tiffin.project":
				s.Containers[i].Project = v
			case "tiffin.app":
				s.Containers[i].App = v
			}
		}
	}
}
