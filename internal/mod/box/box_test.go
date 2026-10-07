package box

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
	"github.com/btahir/tiffin/internal/tokens"
)

const (
	idWeb   = "1111111111111111111111111111111111111111111111111111111111111111"
	idOther = "2222222222222222222222222222222222222222222222222222222222222222"
)

// fakeBox writes a /proc, /etc/systemd and /sys/fs/cgroup tree.
type fakeBox struct {
	t    *testing.T
	root string
}

func (f *fakeBox) write(rel, body string) {
	f.t.Helper()
	p := filepath.Join(f.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeBox) tick(busy, idle, webCPUusec string) {
	f.write("proc/stat", "cpu  "+busy+" 0 0 "+idle+" 0 0 0 0 0 0\ncpu0 1 0 0 1 0 0 0 0\ncpu1 1 0 0 1 0 0 0 0\nintr 1\n")
	f.write("sys/fs/cgroup/system.slice/nerdctl-"+idWeb+".scope/cpu.stat", "usage_usec "+webCPUusec+"\nuser_usec 1\n")
}

func newFake(t *testing.T) (*sampler, *fakeBox, *time.Time, *int) {
	f := &fakeBox{t: t, root: t.TempDir()}
	f.write("proc/meminfo", "MemTotal:        4000000 kB\nMemFree:          100000 kB\nMemAvailable:    3000000 kB\nSwapTotal:       1000000 kB\nSwapFree:         750000 kB\n")
	f.write("proc/loadavg", "0.52 0.40 0.31 2/300 1234\n")
	f.write("proc/uptime", "3600.5 7000.1\n")
	for _, u := range []string{"tiffin.service", "tiffin-postgres.service", "tiffin-valkey.service", "buildkit.service", "tiffin-firewall.service"} {
		f.write("etc/systemd/system/"+u, "[Unit]\n")
	}
	f.write("usr/lib/systemd/system/crowdsec.service", "[Unit]\n")
	f.write("sys/fs/cgroup/system.slice/nerdctl-"+idWeb+".scope/memory.current", "52428800\n")
	f.write("sys/fs/cgroup/system.slice/nerdctl-"+idWeb+".scope/memory.max", "536870912\n")
	f.write("sys/fs/cgroup/system.slice/nerdctl-"+idWeb+".scope/memory.stat", "anon 41943040\nfile 10485760\nkernel 0\n")
	f.write("sys/fs/cgroup/system.slice/tiffin-postgres.service/memory.stat", "anon 20971520\nfile 83886080\n")
	f.write("sys/fs/cgroup/system.slice/nerdctl-"+idOther+".scope/memory.current", "1000\n")
	f.write("sys/fs/cgroup/system.slice/nerdctl-"+idOther+".scope/memory.max", "max\n")
	f.tick("1000", "9000", "2000000")

	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	lists := 0
	s := newSampler(f.root, t.TempDir())
	s.now = func() time.Time { return now }
	cpuNs := 5_000_000_000
	s.systemctl = func(_ context.Context, units []string) ([]byte, error) {
		if strings.Join(units, " ") != "buildkit.service crowdsec.service tiffin-firewall.service tiffin-postgres.service tiffin-valkey.service tiffin.service" {
			t.Errorf("units: %v", units)
		}
		cpuNs += 2_000_000_000
		return []byte("Id=tiffin-postgres.service\nLoadState=loaded\nActiveState=active\nSubState=running\nNRestarts=0\nMemoryCurrent=104857600\nCPUUsageNSec=" + itoa(cpuNs) + "\n\n" +
			"Id=tiffin.service\nLoadState=loaded\nActiveState=active\nSubState=running\nNRestarts=1\nMemoryCurrent=209715200\nCPUUsageNSec=9000000000\n\n" +
			"Id=tiffin-firewall.service\nLoadState=loaded\nActiveState=active\nSubState=exited\nNRestarts=0\nMemoryCurrent=[not set]\nCPUUsageNSec=[not set]\n\n" +
			"Id=crowdsec.service\nLoadState=not-found\nActiveState=inactive\nSubState=dead\n"), nil
	}
	s.list = func(context.Context) ([]container, error) {
		lists++
		return parseNerdctlPS([]byte(`{"ID":"` + idWeb + `","Names":"tf.shop.web.prod.3","Labels":"tiffin.app=web,tiffin.deploy=dep_1,tiffin.port=20001,tiffin.preview=,tiffin.project=shop","Status":"Up 5 minutes"}
{"ID":"3333333333333333333333333333333333333333333333333333333333333333","Names":"tf.shop.api.prod.1","Labels":"tiffin.app=api,tiffin.project=shop","Status":"Exited (137) 2 minutes ago"}
{"ID":"4444","Names":"short"}
`)), nil
	}
	return s, f, &now, &lists
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestSample(t *testing.T) {
	s, f, now, lists := newFake(t)
	ctx := context.Background()
	s.sample(ctx) // the priming sample get() takes on its own
	*now = now.Add(2 * time.Second)
	f.tick("1500", "9500", "3000000") // 500 busy of 1000 jiffies; web used 1 CPU-second in 2s
	r := s.get(ctx)

	if r.CPU.Count != 2 || r.CPU.UsedPercent != 50 || r.CPU.Load1 != 0.52 || r.WindowSeconds != 2 || r.UptimeSeconds != 3600.5 {
		t.Fatalf("cpu: %+v window %v uptime %v", r.CPU, r.WindowSeconds, r.UptimeSeconds)
	}
	m := r.Memory
	if m.TotalBytes != 4000000*1024 || m.AvailableBytes != 3000000*1024 || m.UsedBytes != 1000000*1024 || m.UsedPercent != 25 || m.SwapUsedBytes != 250000*1024 {
		t.Fatalf("memory: %+v", m)
	}
	if r.Disks.Data.TotalBytes == 0 || r.Disks.System.Mount != "/" || r.Disks.System.TotalBytes == 0 {
		t.Fatalf("disks: %+v", r.Disks)
	}
	if len(r.Services) != 3 {
		t.Fatalf("services (crowdsec is not installed, so it is left out): %+v", r.Services)
	}
	tf, pg := r.Services[0], r.Services[1]
	if tf.Name != "tiffin" || tf.Restarts != 1 || tf.MemoryBytes != 200<<20 || tf.CacheBytes != 0 || tf.Description == "" {
		t.Fatalf("tiffin: %+v", tf)
	}
	// Postgres used 2 CPU-seconds in the 2 seconds since the last sample.
	if pg.Name != "postgres" || pg.Unit != "tiffin-postgres.service" || pg.MemoryBytes != 100<<20 || pg.CacheBytes != 80<<20 || pg.CPUPercent != 100 || pg.State != "active" {
		t.Fatalf("postgres: %+v", pg)
	}
	if fw := r.Services[2]; fw.Name != "firewall" || fw.SubState != "exited" || fw.MemoryBytes != 0 {
		t.Fatalf("firewall: %+v", fw)
	}
	if len(r.Apps) != 2 {
		t.Fatalf("apps: %+v", r.Apps)
	}
	web, api := r.Apps[0], r.Apps[1]
	if web.Project != "shop" || web.App != "web" || web.Deploy != "dep_1" || web.State != "running" || web.MemoryBytes != 50<<20 || web.CacheBytes != 10<<20 ||
		web.MemoryLimitBytes != 512<<20 || web.CPUSeconds != 3 || web.CPUPercent != 50 || web.Container != "tf.shop.web.prod.3" {
		t.Fatalf("web: %+v", web)
	}
	if api.App != "api" || api.State != "exited" || api.MemoryBytes != 0 {
		t.Fatalf("exited api: %+v", api)
	}

	// Cached for a few seconds, then sampled again.
	if again := s.get(ctx); again != r {
		t.Fatal("a second call within the cache window must not sample again")
	}
	*now = now.Add(4 * time.Second)
	if again := s.get(ctx); again == r || again.WindowSeconds != 4 {
		t.Fatalf("after the cache window: %+v", again)
	}
	// The container list is reused: idOther (not Tiffin's) does not force a refresh.
	if *lists != 1 {
		t.Fatalf("nerdctl listed %d times, want 1", *lists)
	}
}

func TestPSState(t *testing.T) {
	for in, want := range map[string]string{"Up 5 minutes": "running", "Exited (0) 2 minutes ago": "exited", "Created": "created", "": "unknown", "Paused": "paused"} {
		if got := psState(in); got != want {
			t.Errorf("%q: %s", in, got)
		}
	}
}

// Off a box (tiffin serve without --box) the endpoint says so; on a box it
// answers with a sample.
func TestAPI(t *testing.T) {
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tm := tokens.NewManager(db)
	owner, _, _ := tm.Bootstrap(context.Background())
	srv := httptest.NewServer(api.New(api.Deps{DB: db, Engine: change.NewEngine(db), Tokens: tm}).Handler())
	defer srv.Close()
	getAs := func(token, path string) (int, map[string]any) {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		var out map[string]any
		_ = json.Unmarshal(b, &out)
		return res.StatusCode, out
	}
	get := func() (int, map[string]any) { return getAs(owner, "/v1/box/resources") }
	var m *Module
	for _, mod := range platform.Modules() {
		if bm, ok := mod.(*Module); ok {
			m = bm
		}
	}
	if code, prob := get(); code != 503 || !strings.Contains(prob["detail"].(string), "--box") || prob["hint"] == "" {
		t.Fatalf("off box: %d %v", code, prob)
	}
	s, _, _, _ := newFake(t)
	m.mu.Lock()
	m.s = s
	m.mu.Unlock()
	t.Cleanup(func() { m.mu.Lock(); m.s = nil; m.mu.Unlock() })
	code, out := get()
	if code != 200 || out["cpu"].(map[string]any)["count"].(float64) != 2 || len(out["services"].([]any)) != 3 || len(out["apps"].([]any)) != 2 {
		t.Fatalf("on box: %d %v", code, out)
	}
	// Box-wide reports name every project: a key for some projects gets none.
	op, _ := tm.Authenticate(context.Background(), owner)
	shop, _, err := tm.CreateKey(context.Background(), op, tokens.KeyRequest{Name: "shop", Projects: tokens.Projects{"shop"}, Access: tokens.LevelRead})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/box/resources", "/v1/box/disk"} {
		if code, out := getAs(shop, path); code != 403 {
			t.Fatalf("%s for a project key: %d %v", path, code, out)
		}
	}
}
