package budget

import (
	"slices"
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/manifest"
)

// A 3 GB dev box (MemTotal 2899 MB, 2 CPUs).
var box3 = Box{MemoryMB: 2899, CPUs: 2}

func TestReserve(t *testing.T) {
	r := ReserveFor(2899)
	if r.PostgresMB != 362 || r.ValkeyMB != 362 || r.TotalMB != 640+362+362 {
		t.Fatalf("reserve on 3 GB: %+v", r)
	}
	if box3.PoolMB() != 2899-1364 {
		t.Fatalf("pool = %d", box3.PoolMB())
	}
	// Big boxes: the caches cap at 4 GB each.
	if r := ReserveFor(64 * 1024); r.TotalMB != 640+4096+4096 {
		t.Fatalf("64 GB: %+v", r)
	}
	// Tiny boxes keep at least a quarter for apps.
	if r := ReserveFor(1024); r.TotalMB != 768 {
		t.Fatalf("1 GB: %+v", r)
	}
	if !strings.Contains(box3.Explain(), "1535 MB for apps") {
		t.Fatalf("explain: %s", box3.Explain())
	}
}

// Two automatic projects: each may burst to nearly the whole pool while the
// other idles (only the floor for the other's copies is held back), and
// each is protected up to half the pool when both want memory. CPU: equal
// weights, no quota, so each gets every core when alone and half under
// contention.
func TestAutomaticProjectsAreElasticAndFair(t *testing.T) {
	pool := box3.PoolMB()
	l := Resolve(box3, DefaultSettings, []Project{{Name: "a", Copies: 1}, {Name: "b", Copies: 2}})
	a, b := l["a"], l["b"]
	if a.MemoryMaxMB != pool-2*FloorPerCopyMB || b.MemoryMaxMB != pool-FloorPerCopyMB {
		t.Fatalf("burst caps: a %d b %d (pool %d)", a.MemoryMaxMB, b.MemoryMaxMB, pool)
	}
	if a.MemoryLowMB != pool/2 || b.MemoryLowMB != pool/2 {
		t.Fatalf("fair shares: a %d b %d", a.MemoryLowMB, b.MemoryLowMB)
	}
	// Under contention the one over its share is swapped out first, not killed.
	if a.SwapMaxMB != a.MemoryMaxMB/2 || b.SwapMaxMB != b.MemoryMaxMB/2 {
		t.Fatalf("swap: %+v %+v", a, b)
	}
	if a.CPUs != 0 || b.CPUs != 0 || a.CPUWeight != b.CPUWeight {
		t.Fatalf("cpu: %+v %+v", a, b)
	}
	if a.Source() != SourceAutomatic || a.CPUSource != SourceAutomatic {
		t.Fatalf("source: %+v", a)
	}
	// Many neighbours never squeeze a project below MinAutoMB.
	l = Resolve(box3, DefaultSettings, []Project{{Name: "a"}, {Name: "b", Copies: 40}})
	if l["a"].MemoryMaxMB != MinAutoMB {
		t.Fatalf("floor: %+v", l["a"])
	}
}

func TestBudgetsAndShares(t *testing.T) {
	pool := box3.PoolMB() // 1535
	l := Resolve(box3, DefaultSettings, []Project{
		{Name: "fixed", Resources: &manifest.Resources{MemoryMB: 256, CPUs: 0.5}, Copies: 1},
		{Name: "both", Resources: &manifest.Resources{MemoryMB: 900, MaxSharePercent: 25}, Copies: 1},
		{Name: "share", Resources: &manifest.Resources{MaxSharePercent: 50}, Copies: 1},
		{Name: "cpuonly", Resources: &manifest.Resources{CPUs: 1.5}, Copies: 1},
		{Name: "auto", Copies: 1},
	})
	if f := l["fixed"]; f.MemoryMaxMB != 256 || f.SwapMaxMB != 0 || f.MemoryLowMB != 256 || f.CPUs != 0.5 || f.Source() != SourceProject {
		t.Fatalf("fixed: %+v", f)
	}
	// memoryMB 900 vs 25% of 1535 = 383: the lower wins; CPUs 25% of 2 = 0.5.
	if b := l["both"]; b.MemoryMaxMB != pool/4 || b.CPUs != 0.5 || b.MemoryLowMB != pool/4 {
		t.Fatalf("both: %+v", b)
	}
	if s := l["share"]; s.MemoryMaxMB != pool/2 || s.CPUs != 1 || s.MemorySource != SourceProject {
		t.Fatalf("share: %+v", s)
	}
	// cpus alone: memory stays automatic, CPU is the project's.
	if c := l["cpuonly"]; c.CPUs != 1.5 || c.MemorySource != SourceAutomatic || c.CPUSource != SourceProject || c.Source() != SourceProject {
		t.Fatalf("cpuonly: %+v", c)
	}
	// The fair share splits what the memoryMB budgets leave among the
	// three elastic projects with copies.
	fair := (pool - 256 - pool/4) / 3
	if a := l["auto"]; a.MemoryLowMB != fair || a.MemoryMaxMB != pool-4*FloorPerCopyMB {
		t.Fatalf("auto: %+v (fair %d)", a, fair)
	}
}

func TestBoxDefaultShare(t *testing.T) {
	set := Settings{DefaultMaxSharePercent: 25}
	l := Resolve(box3, set, []Project{{Name: "a"}, {Name: "own", Resources: &manifest.Resources{MaxSharePercent: 80}}})
	if a := l["a"]; a.MemoryMaxMB != box3.PoolMB()/4 || a.CPUs != 0.5 || a.Source() != SourceBoxDefault {
		t.Fatalf("box default: %+v", a)
	}
	// A project's own share overrides the default, even upward.
	if o := l["own"]; o.MemoryMaxMB != box3.PoolMB()*80/100 || o.CPUs != 1.6 || o.Source() != SourceProject {
		t.Fatalf("own: %+v", o)
	}
	// Shares follow the box when it is resized.
	big := Box{MemoryMB: 16384, CPUs: 8}
	if a := Resolve(big, set, []Project{{Name: "a"}})["a"]; a.MemoryMaxMB != big.PoolMB()/4 || a.CPUs != 2 {
		t.Fatalf("resized: %+v", a)
	}
}

// The share a project is limited to, for the services the box shares
// (database, cache, builds) and its disk weight.
func TestSharePercent(t *testing.T) {
	pool := box3.PoolMB() // 1535
	l := Resolve(box3, Settings{DefaultMaxSharePercent: 20}, []Project{
		{Name: "share", Resources: &manifest.Resources{MaxSharePercent: 25, MemoryMB: 128}},
		{Name: "mem", Resources: &manifest.Resources{MemoryMB: pool / 10}},
		{Name: "cpu", Resources: &manifest.Resources{MemoryMB: 128, CPUs: 0.5}},
		{Name: "all", Resources: &manifest.Resources{MaxSharePercent: 100}},
		{Name: "default"},
	})
	for name, want := range map[string]int{"share": 25, "mem": 10, "cpu": 25, "all": 0, "default": 20} {
		if got := l[name].SharePercent; got != want {
			t.Errorf("%s: share %d, want %d", name, got, want)
		}
	}
	if l["share"].IOWeight != 25 || l["all"].IOWeight != 100 {
		t.Fatalf("io weights: %+v %+v", l["share"], l["all"])
	}
	// A box default of 100 is no limit.
	if a := Resolve(box3, DefaultSettings, []Project{{Name: "a"}})["a"]; a.SharePercent != 0 || a.IOWeight != 100 {
		t.Fatalf("elastic: %+v", a)
	}
}

func TestProps(t *testing.T) {
	p := props(Limits{MemoryMaxMB: 256, MemoryLowMB: 256, CPUs: 1.5, CPUWeight: 100, IOWeight: 25})
	want := []string{"MemoryMax=256M", "MemorySwapMax=0M", "MemoryLow=256M", "CPUWeight=100", "MemoryHigh=infinity", "CPUQuota=150%", "IOWeight=25"}
	if !slices.Equal(p, want) {
		t.Fatalf("props = %v", p)
	}
	p = props(Limits{MemoryMaxMB: 1279, MemoryLowMB: 767, CPUWeight: 100, IOWeight: 100})
	if !slices.Contains(p, "MemoryHigh=infinity") || !slices.Contains(p, "CPUQuota=") || !slices.Contains(p, "IOWeight=100") {
		t.Fatalf("automatic props = %v", p)
	}
}

func TestCheck(t *testing.T) {
	others := map[string]*manifest.Resources{"shop": {MemoryMB: 1000}, "blog": nil, "x": {MaxSharePercent: 90}}
	if p := Check(box3, "new", &manifest.Resources{MemoryMB: 600}, others); len(p) != 1 ||
		!strings.Contains(p[0].Message, "other projects' budgets already take 1000 MB (shop 1000 MB), so at most 535 MB is left") {
		t.Fatalf("sum: %+v", p)
	}
	if p := Check(box3, "new", &manifest.Resources{MemoryMB: 4000, CPUs: 3}, nil); len(p) != 2 || p[0].Path != "/resources/cpus" ||
		!strings.Contains(p[1].Message, "more than this box keeps for apps (1535 MB)") {
		t.Fatalf("too big: %+v", p)
	}
	// Re-checking the same project ignores its own old budget; shares never add up.
	if p := Check(box3, "shop", &manifest.Resources{MemoryMB: 1500, MaxSharePercent: 100}, others); p != nil {
		t.Fatalf("own budget counted: %+v", p)
	}
	if Check(box3, "a", nil, others) != nil {
		t.Fatal("automatic projects always fit")
	}
}

func TestSliceNames(t *testing.T) {
	if Slice("my-shop") != `tiffin-p-my\x2dshop.slice` || Slice("blog") != "tiffin-p-blog.slice" {
		t.Fatal(Slice("my-shop"))
	}
	if d := SliceDir("/r", "blog"); d != "/r/sys/fs/cgroup/tiffin.slice/tiffin-p.slice/tiffin-p-blog.slice" {
		t.Fatal(d)
	}
}
