package platform

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/edge"
	"github.com/shiptiffin/tiffin/internal/edge/switchboard"
)

// routeSpy gives routes named by a version that grows with each call; the
// first call of an armed spy waits until released, as a slow read would.
type routeSpy struct {
	mu      sync.Mutex
	armed   bool
	n       int
	entered chan struct{}
	release chan struct{}
	loaded  int
}

var routes = &routeSpy{}

func init() { Register(routes) }

func (*routeSpy) Name() string { return "spy-routes" }

func (s *routeSpy) Routes(context.Context, *Platform) ([]edge.Route, error) {
	s.mu.Lock()
	if !s.armed {
		s.mu.Unlock()
		return nil, nil
	}
	s.n++
	n := s.n
	s.mu.Unlock()
	if n == 1 {
		close(s.entered)
		<-s.release
	}
	return []edge.Route{{Host: "v" + string(rune('0'+n)) + ".test", Upstream: "127.0.0.1:1"}}, nil
}

func (s *routeSpy) RoutesLoaded() {
	s.mu.Lock()
	s.loaded++
	s.mu.Unlock()
}

type routeEdge struct {
	mu   sync.Mutex
	last []edge.Route
}

func (e *routeEdge) SetRoutes(rs []edge.Route) error {
	e.mu.Lock()
	e.last = rs
	e.mu.Unlock()
	return nil
}
func (*routeEdge) Switchboard() string                  { return "" }
func (*routeEdge) TableSource(func() switchboard.Table) {}
func (*routeEdge) SyncTable() error                     { return nil }
func (*routeEdge) Busy([]string) (int64, error)         { return 0, nil }
func (*routeEdge) Activity() (map[string]time.Time, error) {
	return nil, nil
}

// Two refreshes at once: the one that gathered later loads last, so the
// edge never ends up with the older routes.
func TestConcurrentRefreshesLoadTheNewerRoutesLast(t *testing.T) {
	routes.mu.Lock()
	routes.armed, routes.n, routes.loaded = true, 0, 0
	routes.entered, routes.release = make(chan struct{}), make(chan struct{})
	routes.mu.Unlock()
	t.Cleanup(func() {
		routes.mu.Lock()
		routes.armed = false
		routes.mu.Unlock()
	})
	e := &routeEdge{}
	p := &Platform{Edge: e}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = p.RefreshRoutes(context.Background()) }()
	<-routes.entered // the first refresh is gathering
	go func() { defer wg.Done(); _ = p.RefreshRoutes(context.Background()) }()
	time.Sleep(100 * time.Millisecond) // the second would gather and load meanwhile
	close(routes.release)
	wg.Wait()
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.last) != 1 || e.last[0].Host != "v2.test" {
		t.Fatalf("the edge serves %+v, want the newer routes (v2)", e.last)
	}
	if routes.loaded != 2 {
		t.Fatalf("RoutesLoaded called %d times, want 2", routes.loaded)
	}
}
