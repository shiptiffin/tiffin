package budget

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
	"github.com/btahir/tiffin/internal/tokens"
)

type fakeSD struct {
	mu      sync.Mutex
	set     map[string][]string
	removed []string
}

func (f *fakeSD) Set(_ context.Context, unit string, props []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.set[unit] = props
	return nil
}

func (f *fakeSD) Remove(_ context.Context, unit string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.set, unit)
	f.removed = append(f.removed, unit)
	return nil
}

func (f *fakeSD) props(unit string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.set[unit]
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

type harness struct {
	t     *testing.T
	srv   *httptest.Server
	owner string
	p     *platform.Platform
	sd    *fakeSD
	root  string
}

func (h *harness) call(method, path string, body any) (int, map[string]any) {
	h.t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, h.srv.URL+path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+h.owner)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return res.StatusCode, out
}

// apply plans and applies a manifest, then reconciles its project resource
// the way the platform's reconciler would.
func (h *harness) apply(manifest string) {
	h.t.Helper()
	m := json.RawMessage(manifest)
	code, plan := h.call("POST", "/v1/plan", map[string]any{"manifest": m})
	if code != 200 {
		h.t.Fatalf("plan: %d %v", code, plan)
	}
	if code, out := h.call("POST", "/v1/apply", map[string]any{"manifest": m, "confirm": plan["hash"]}); code != 200 {
		h.t.Fatalf("apply: %d %v", code, out)
	}
	var probe struct {
		Project string `json:"project"`
	}
	_ = json.Unmarshal(m, &probe)
	_, res, _ := h.p.DB.Load(context.Background(), probe.Project)
	if err := mod.Reconcile(context.Background(), h.p, probe.Project, change.KindProject, res[change.KindProject].Spec); err != nil {
		h.t.Fatal(err)
	}
}

func newHarness(t *testing.T) *harness {
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tm := tokens.NewManager(db)
	owner, _, _ := tm.Bootstrap(context.Background())
	p := &platform.Platform{DB: db, Engine: change.NewEngine(db), Tokens: tm, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv := httptest.NewServer(api.New(api.Deps{DB: db, Engine: p.Engine, Tokens: tm, Platform: p}).Handler())
	t.Cleanup(srv.Close)
	root := t.TempDir()
	write(t, root, "proc/meminfo", "MemTotal:        2968576 kB\nMemAvailable:    2000000 kB\n") // 2899 MB
	write(t, root, "proc/stat", "cpu  1 0 0 1 0 0 0 0\ncpu0 1 0 0 1\ncpu1 1 0 0 1\n")
	sd := &fakeSD{set: map[string][]string{}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		mod.mu.Lock()
		mod.specs, mod.p = nil, nil
		mod.mu.Unlock()
	})
	if err := mod.start(ctx, p, root, sd); err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, srv: srv, owner: owner, p: p, sd: sd, root: root}
}

func TestModule(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if got := h.sd.props(ParentSlice); !slices.Equal(got, []string{"MemoryMax=1535M"}) {
		t.Fatalf("parent slice: %v", got)
	}

	// A budget bigger than the box is refused at plan time, with the reserve explained.
	code, prob := h.call("POST", "/v1/plan", map[string]any{"manifest": json.RawMessage(`{"project":"shop","resources":{"memoryMB":4000,"cpus":4}}`)})
	if code != 422 || !strings.Contains(prob["hint"].(string), "Tiffin keeps 1364 MB for itself") || len(prob["errors"].([]any)) != 2 {
		t.Fatalf("too big: %d %v", code, prob)
	}

	// A fixed budget: hard cap, guarantee, CPU quota.
	h.apply(`{"project":"shop","resources":{"memoryMB":256,"cpus":0.5},"apps":{"web":{}}}`)
	want := []string{"MemoryMax=256M", "MemorySwapMax=0M", "MemoryLow=256M", "CPUWeight=100", "MemoryHigh=infinity", "CPUQuota=50%"}
	if got := h.sd.props(`tiffin-p-shop.slice`); !slices.Equal(got, want) {
		t.Fatalf("shop slice: %v", got)
	}
	// The budget round-trips through the pulled manifest.
	if code, out := h.call("GET", "/v1/projects/shop/manifest", nil); code != 200 || !strings.Contains(out["config"].(string), "resources: { memoryMB: 256, cpus: 0.5 }") {
		t.Fatalf("manifest: %d %v", code, out["config"])
	}

	// Budgets must add up: 256 + 1300 > 1535.
	code, prob = h.call("POST", "/v1/plan", map[string]any{"manifest": json.RawMessage(`{"project":"my-blog","resources":{"memoryMB":1300}}`)})
	if code != 422 || !strings.Contains(prob["detail"].(string), "shop 256 MB") {
		t.Fatalf("sum: %d %v", code, prob)
	}

	// An automatic project with two running copies: elastic up to the pool.
	h.apply(`{"project":"my-blog","apps":{"web":{}}}`)
	blogDir := SliceDir(h.root, "my-blog")
	write(t, h.root, strings.TrimPrefix(blogDir, h.root)+"/nerdctl-"+strings.Repeat("a", 64)+".scope/cpu.stat", "usage_usec 1\n")
	write(t, h.root, strings.TrimPrefix(blogDir, h.root)+"/nerdctl-"+strings.Repeat("b", 64)+".scope/cpu.stat", "usage_usec 1\n")
	if err := mod.sync(ctx); err != nil {
		t.Fatal(err)
	}
	blog := h.sd.props(`tiffin-p-my\x2dblog.slice`)
	// fair share: (1535 - 256) / 1 elastic project with copies = 1279.
	if !slices.Contains(blog, "MemoryMax=1535M") || !slices.Contains(blog, "MemoryLow=1279M") || !slices.Contains(blog, "CPUQuota=") || !slices.Contains(blog, "MemoryHigh=infinity") {
		t.Fatalf("blog slice: %v", blog)
	}
	if v := Lookup("my-blog", Stats{}); v.Limits.Source() != SourceAutomatic || v.Resources != nil || !v.Known {
		t.Fatalf("lookup: %+v", v)
	}

	// The box default share caps projects that set nothing, live (and
	// readers caching answers see the generation move).
	gen := Generation()
	if code, out := h.call("PUT", "/v1/box/settings", map[string]any{"defaultMaxSharePercent": 25}); code != 200 ||
		out["defaultMaxSharePercent"].(float64) != 25 || out["appMemoryMB"].(float64) != 1535 || out["defaultMemoryMB"].(float64) != 383 || out["budgetedMB"].(float64) != 256 {
		t.Fatalf("settings: %d %v", code, out)
	}
	blog = h.sd.props(`tiffin-p-my\x2dblog.slice`)
	if !slices.Contains(blog, "MemoryMax=383M") || !slices.Contains(blog, "CPUQuota=50%") {
		t.Fatalf("blog under the default: %v", blog)
	}
	if Generation() == gen {
		t.Fatal("generation did not move when limits changed")
	}
	if v := Lookup("my-blog", Stats{}); v.Limits.Source() != SourceBoxDefault {
		t.Fatalf("source: %+v", v.Limits)
	}
	if code, _ := h.call("PUT", "/v1/box/settings", map[string]any{"defaultMaxSharePercent": 2}); code != 422 {
		t.Fatalf("2%%: %d", code)
	}
	if code, out := h.call("GET", "/v1/box/settings", nil); code != 200 || out["defaultMaxSharePercent"].(float64) != 25 || out["cpus"].(float64) != 2 {
		t.Fatalf("get settings: %d %v", code, out)
	}

	// Pressure: an OOM kill after Tiffin started watching.
	shopRel := strings.TrimPrefix(SliceDir(h.root, "shop"), h.root)
	write(t, h.root, shopRel+"/memory.events", "low 0\nhigh 3\nmax 0\noom 0\noom_kill 0\n")
	mod.mu.Lock()
	delete(mod.history, "shop")
	mod.mu.Unlock()
	if err := mod.sync(ctx); err != nil {
		t.Fatal(err)
	}
	if v := Lookup("shop", ReadStats(SliceDir(h.root, "shop"))); v.Pressure != PressureNone {
		t.Fatalf("baseline pressure: %s", v.Pressure)
	}
	// Memory swapped out under contention is pressure too.
	write(t, h.root, shopRel+"/memory.swap.current", "1048576\n")
	if v := Lookup("shop", ReadStats(SliceDir(h.root, "shop"))); v.Pressure != PressureSome {
		t.Fatalf("swap pressure: %s", v.Pressure)
	}
	write(t, h.root, shopRel+"/memory.swap.current", "0\n")
	write(t, h.root, shopRel+"/memory.events", "low 0\nhigh 9\nmax 2\noom 0\noom_kill 0\n")
	if v := Lookup("shop", ReadStats(SliceDir(h.root, "shop"))); v.Pressure != PressureSome {
		t.Fatalf("some pressure: %s", v.Pressure)
	}
	write(t, h.root, shopRel+"/memory.events", "low 0\nhigh 9\nmax 5\noom 1\noom_kill 1\n")
	if v := Lookup("shop", ReadStats(SliceDir(h.root, "shop"))); v.Pressure != PressureOOM {
		t.Fatalf("oom pressure: %s", v.Pressure)
	}

	// Deleting a project removes its slice.
	if err := mod.Reconcile(ctx, h.p, "shop", change.KindProject, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(h.sd.removed, "tiffin-p-shop.slice") {
		t.Fatalf("removed: %v", h.sd.removed)
	}
	if _, ok := Projects()["shop"]; ok {
		t.Fatal("shop still known")
	}
}

func TestReadStats(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "s")
	write(t, root, "s/memory.current", "104857600\n")
	write(t, root, "s/memory.max", "max\n")
	write(t, root, "s/memory.low", "52428800\n")
	write(t, root, "s/memory.stat", "anon 1\nfile 209715200\n")
	write(t, root, "s/cpu.stat", "usage_usec 2500000\n")
	write(t, root, "s/cpu.max", "150000 100000\n")
	write(t, root, "s/memory.events", "low 0\nhigh 1\nmax 2\noom 3\noom_kill 4\noom_group_kill 0\n")
	write(t, root, "s/nerdctl-x.scope/cgroup.procs", "")
	write(t, root, "s/memory.swap.current", "4096\n")
	write(t, root, "s/memory.pressure", "some avg10=0.00 avg60=0.00 avg300=0.00 total=10560\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=9000\n")
	st := ReadStats(dir)
	if !st.Exists || st.MemoryBytes != 100<<20 || st.CacheBytes != 100<<20 || st.MaxBytes != 0 || st.LowBytes != 50<<20 ||
		st.CPUSeconds != 2.5 || st.QuotaCPUs != 1.5 || st.High != 1 || st.Max != 2 || st.OOM != 3 || st.OOMKill != 4 || st.Copies != 1 ||
		st.SwapBytes != 4096 || st.StallMicros != 10560 {
		t.Fatalf("stats: %+v", st)
	}
	if ReadStats(filepath.Join(root, "missing")).Exists {
		t.Fatal("missing dir exists")
	}
}
