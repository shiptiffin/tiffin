package box

import (
	"context"
	"github.com/shiptiffin/tiffin/internal/mod/postgres"
	"testing"
	"time"
)

// A project's usage comes from its slice: memory against its limit,
// headroom, CPU over the last marks, and its apps' copies by environment.
func TestUsage(t *testing.T) {
	s, f, _, _ := newFake(t)
	slice := "sys/fs/cgroup/tiffin.slice/tiffin-p.slice/tiffin-p-shop.slice/"
	f.write(slice+"memory.current", "209715200\n") // 200 MB, 50 MB of it cache
	f.write(slice+"memory.stat", "anon 1\nfile 52428800\n")
	f.write(slice+"memory.max", "268435456\n")
	f.write(slice+"memory.low", "134217728\n")
	f.write(slice+"memory.swap.current", "0\n")
	f.write(slice+"cpu.max", "50000 100000\n")
	f.write(slice+"cpu.stat", "usage_usec 1000000\n")
	f.write(slice+"nerdctl-"+idWeb+".scope/memory.current", "104857600\n")
	f.write(slice+"nerdctl-"+idWeb+".scope/cpu.stat", "usage_usec 1000000\n")
	tr := newTracker(f.root, nil)
	now := time.Now() // one clock: the CPU window is exactly 2 s however slow the test runs
	tr.markAll(now.Add(-2 * time.Second))
	f.write(slice+"cpu.stat", "usage_usec 2000000\n") // 1 CPU-second in 2 s
	f.write(slice+"nerdctl-"+idWeb+".scope/cpu.stat", "usage_usec 1500000\n")
	s.now = func() time.Time { return now }

	u := s.usage(context.Background(), tr, "shop", []string{"api", "web"}, 1<<30)
	// limit 256 - (200 used - 50 cache) = 106 MB of headroom.
	if u.Memory.UsedBytes != 200<<20 || u.Memory.CacheBytes != 50<<20 || u.Memory.LimitBytes != 256<<20 || u.Memory.ProtectedBytes != 128<<20 ||
		u.Memory.HeadroomBytes != 106<<20 || u.Memory.Pressure != "none" || u.Memory.LimitSource != "automatic" {
		t.Fatalf("memory: %+v", u.Memory)
	}
	if u.CPU.Percent < 49 || u.CPU.Percent > 51 || u.CPU.LimitCpus == nil || *u.CPU.LimitCpus != 0.5 || u.CPU.Weight != 100 {
		t.Fatalf("cpu: %+v", u.CPU)
	}
	if !u.Budget.Auto || u.LimitSource != "automatic" || u.Disk.TotalBytes != 0 || u.Disk.MeasuredAt != nil {
		t.Fatalf("budget/disk: %+v %+v", u.Budget, u.Disk)
	}
	if len(u.Apps) != 2 || u.Apps[0].App != "api" || u.Apps[0].State != "stopped" || u.Apps[1].App != "web" ||
		u.Apps[1].Instances != 1 || u.Apps[1].MemoryBytes != 100<<20 || u.Apps[1].CPUPercent < 24 || u.Apps[1].CPUPercent > 26 || u.Apps[1].State != "running" {
		t.Fatalf("apps: %+v", u.Apps)
	}
	if again := s.usage(context.Background(), tr, "shop", nil, 0); again != u {
		t.Fatal("a second call within 2 seconds must be served from the cache")
	}

	// The box view adds a line per project.
	r := s.sample(context.Background())
	var shop *ProjectTotal
	for i := range r.Projects {
		if r.Projects[i].Project == "shop" {
			shop = &r.Projects[i]
		}
	}
	if shop != nil {
		t.Fatalf("projects come from the box's projects, and this box has none: %+v", r.Projects)
	}
	tr.p = nil
	if got := s.projectTotals(context.Background(), nil, s.now(), map[string]bool{}); len(got) != 0 {
		t.Fatalf("no known projects: %+v", got)
	}
}

// Without a limit: the database's safety settings, the cache's limit only
// reported, no build cap; queries of limited projects are marked for CPU.
func TestSharedUsage(t *testing.T) {
	s, f, _, _ := newFake(t)
	f.write("sys/fs/cgroup/system.slice/tiffin-postgres.service/p-shop/cpu.stat", "usage_usec 1000000\n")
	tr := newTracker(f.root, nil)
	tr.markAll(time.Now())
	if _, ok := tr.marks["pg:shop"]; !ok {
		t.Fatalf("the database group's CPU is not marked: %v", tr.marks)
	}
	u := &Usage{Project: "blog", Services: UsageServices{Postgres: &PGUsage{Connections: 3}}, Cache: &UsageCache{UsedBytes: 1 << 20, LimitBytes: 64 << 20}}
	s.sharedUsage(context.Background(), tr, u, time.Now())
	if d := u.Database; d == nil || d.Connections != 3 || d.ConnectionLimit != postgres.LimitUsage("blog").ConnectionLimit || d.ConnectionLimit < 80 || d.QueryTimeLimitSeconds != 300 || d.LimitCpus != nil {
		t.Fatalf("database: %+v", u.Database)
	}
	if u.SharePercent != 0 || u.Builds.LimitCpus != nil || u.Cache.Enforced || u.LimitEvents == nil {
		t.Fatalf("no limit: %+v", u)
	}
}

func TestHeadroomNeverNegative(t *testing.T) {
	s, f, _, _ := newFake(t)
	slice := "sys/fs/cgroup/tiffin.slice/tiffin-p.slice/tiffin-p-full.slice/"
	f.write(slice+"memory.current", "300000000\n")
	f.write(slice+"memory.max", "268435456\n")
	u := s.usage(context.Background(), newTracker(f.root, nil), "full", nil, 1<<30)
	if u.Memory.HeadroomBytes != 0 || u.Memory.LimitBytes != 256<<20 {
		t.Fatalf("over its limit: %+v", u.Memory)
	}
	// A slice that does not exist yet (no copies started) reads as zero use.
	u = s.usage(context.Background(), newTracker(f.root, nil), "nothing", []string{"web"}, 1<<30)
	if u.Memory.UsedBytes != 0 || len(u.Apps) != 1 || u.Apps[0].State != "stopped" {
		t.Fatalf("no slice: %+v", u)
	}
}
