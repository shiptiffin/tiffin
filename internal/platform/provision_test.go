package platform

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// A failed provisioning runs again from the box, backing off, until the
// report is clean.
func TestRetryProvision(t *testing.T) {
	defer func(path string, first time.Duration, launch func(context.Context) error) {
		ProvisionReportPath, provisionRetryFirst, launchProvision = path, first, launch
	}(ProvisionReportPath, provisionRetryFirst, launchProvision)
	ProvisionReportPath = filepath.Join(t.TempDir(), "provision.json")
	provisionRetryFirst = time.Millisecond
	failing := ProvisionReport{Results: []ProvisionResult{{Module: "base"}, {Module: "pgbouncer", Error: "apt-cache policy pgbouncer: exit status 100"}}}
	if err := SaveProvisionReport(failing); err != nil {
		t.Fatal(err)
	}
	if c := provisionChecks(context.Background()); len(c) != 1 || c[0].OK {
		t.Fatalf("checks: %+v", c)
	}
	runs := 0
	launchProvision = func(context.Context) error {
		runs++
		if runs < 3 {
			return SaveProvisionReport(failing)
		}
		return SaveProvisionReport(ProvisionReport{Results: []ProvisionResult{{Module: "base"}, {Module: "pgbouncer"}}})
	}
	p := &Platform{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	done := make(chan struct{})
	go func() { p.retryProvision(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("retryProvision did not return once the report was clean")
	}
	if runs != 3 {
		t.Fatalf("runs: %d", runs)
	}
	if c := provisionChecks(context.Background()); len(c) != 1 || !c[0].OK {
		t.Fatalf("checks after the retry: %+v", c)
	}
}

type fakeProv struct {
	name  string
	needs []string
	err   error
	mu    *sync.Mutex
	log   *[]string
}

func (f *fakeProv) Name() string    { return f.name }
func (f *fakeProv) Needs() []string { return f.needs }
func (f *fakeProv) Provision(context.Context, *System) error {
	time.Sleep(20 * time.Millisecond)
	f.mu.Lock()
	*f.log = append(*f.log, f.name)
	f.mu.Unlock()
	return f.err
}

func TestProvisionAllOrderAndIsolation(t *testing.T) {
	saved := modules
	defer func() { modules = saved }()
	var mu sync.Mutex
	var log []string
	mk := func(n string, err error, needs ...string) Module {
		return &fakeProv{name: n, needs: needs, err: err, mu: &mu, log: &log}
	}
	modules = []Module{mk("base", nil), mk("pg", nil), mk("backup", nil, "pg"), mk("broken", errors.New("boom")), mk("web", nil)}
	start := time.Now()
	r := ProvisionAll(context.Background(), NewSystem(nil), func(string) {})
	if f := r.Failed(); len(f) != 1 || f[0] != "broken" {
		t.Fatalf("failed = %v", f)
	}
	idx := map[string]int{}
	for i, n := range log {
		idx[n] = i
	}
	if idx["base"] != 0 || idx["backup"] < idx["pg"] {
		t.Fatalf("order wrong: %v", log)
	}
	// base, then {pg, broken, web} together, then backup: ~3 steps, not 5.
	if d := time.Since(start); d > 90*time.Millisecond {
		t.Fatalf("not concurrent: %s", d)
	}
}

// Downloads go once every module installed; a failure keeps them for the
// retry. Other files in the folder stay.
func TestProvisionAllClearsDownloads(t *testing.T) {
	saved := modules
	defer func() { modules = saved }()
	var mu sync.Mutex
	var log []string
	sys := NewSystem(nil)
	sys.CacheDir = t.TempDir()
	files := []string{"0123456789abcdef-nerdctl-full.tar.gz", "fedcba9876543210-bun.zip.part", "notes.txt"}
	put := func() {
		for _, f := range files {
			if err := os.WriteFile(filepath.Join(sys.CacheDir, f), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	left := func() (n []string) {
		es, _ := os.ReadDir(sys.CacheDir)
		for _, e := range es {
			n = append(n, e.Name())
		}
		return n
	}
	put()
	modules = []Module{&fakeProv{name: "base", mu: &mu, log: &log}, &fakeProv{name: "broken", err: errors.New("boom"), mu: &mu, log: &log}}
	ProvisionAll(context.Background(), sys, func(string) {})
	if got := left(); len(got) != 3 {
		t.Fatalf("a failed provision removed downloads: %v", got)
	}
	modules = []Module{&fakeProv{name: "base", mu: &mu, log: &log}}
	ProvisionAll(context.Background(), sys, func(string) {})
	if got := left(); len(got) != 1 || got[0] != "notes.txt" {
		t.Fatalf("after a good provision: %v", got)
	}
}
