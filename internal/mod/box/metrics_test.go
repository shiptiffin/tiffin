package box

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/platform"
)

// Every project's usage goes to the metrics store as gauges: memory without
// file cache, CPU over the last marks, data by part. Limits only when the
// project has one (an automatic project's "limit" is just the box).
func TestMetrics(t *testing.T) {
	s, f, _, _ := newFake(t)
	slice := "sys/fs/cgroup/tiffin.slice/tiffin-p.slice/tiffin-p-shop.slice/"
	f.write(slice+"memory.current", "209715200\n") // 200 MB, 50 MB of it cache
	f.write(slice+"memory.stat", "anon 1\nfile 52428800\n")
	f.write(slice+"memory.max", "268435456\n")
	f.write(slice+"cpu.stat", "usage_usec 1000000\n")
	tr := newTracker(f.root, nil)
	tr.markAll(time.Now().Add(-2 * time.Second))
	f.write(slice+"cpu.stat", "usage_usec 2000000\n") // 1 CPU-second in 2 s
	tr.disk["shop"] = &diskEntry{at: time.Now(), services: []platform.ServiceUsage{
		{Service: "postgres", Disk: "database", Bytes: 4096, Counts: map[string]int64{"connections": 3}},
		{Service: "valkey", Disk: "kv", Bytes: 1024},
	}}

	var b bytes.Buffer
	s.metrics(&b, tr, []string{"shop", "empty"}, time.Now())
	out := b.String()
	for _, want := range []string{
		`tiffin_project_memory_bytes{project="shop"} 1.572864e+08`,
		`tiffin_project_data_bytes{project="shop",part="database"} 4096`,
		`tiffin_project_data_bytes{project="shop",part="kv"} 1024`,
		`tiffin_project_db_connections{project="shop"} 3`,
		`tiffin_project_memory_bytes{project="empty"} 0`,
	} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("missing %s in\n%s", want, out)
		}
	}
	_, after, _ := strings.Cut(out, `tiffin_project_cpu_percent{project="shop"} `)
	line, _, _ := strings.Cut(after, "\n")
	if cpu, err := strconv.ParseFloat(line, 64); err != nil || cpu < 45 || cpu > 55 {
		t.Errorf("cpu %q, want about 50:\n%s", line, out)
	}
	if strings.Contains(out, "limit") {
		t.Errorf("an automatic project has no limit series:\n%s", out)
	}
}
