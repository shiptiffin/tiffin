package platform

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

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
