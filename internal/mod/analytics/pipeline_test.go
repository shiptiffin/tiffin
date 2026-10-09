package analytics

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/mod/analytics/enrich"
)

// fakeStore blocks or fails inserts and rollups on demand.
type fakeStore struct {
	Store
	mu         sync.Mutex
	block      chan struct{} // inserts wait on it while non-nil
	insertErr  error
	rollupErr  error
	inserted   int
	rolledUp   [][3]string
	rollupFail int // fail this many rollups
}

func (f *fakeStore) Insert(_ context.Context, evs []Event) error {
	f.mu.Lock()
	block, err := f.block, f.insertErr
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.inserted += len(evs)
	f.mu.Unlock()
	return nil
}

func (f *fakeStore) Rollup(_ context.Context, project, app, day string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rollupFail > 0 {
		f.rollupFail--
		return errors.New("rollup failed")
	}
	f.rolledUp = append(f.rolledUp, [3]string{project, app, day})
	return nil
}

func testPipeline(st Store) *Pipeline {
	geo, _ := enrich.OpenGeo(filepath.Join("no", "such.mmdb"))
	return &Pipeline{Store: st, Bots: enrich.NewBots(), Agents: enrich.NewAgents(), Geo: geo, RT: &Realtime{}}
}

func serverHit(project string, at time.Time, path string) Hit {
	return Hit{At: at, Project: project, App: "web", Kind: "pageview", Name: "pageview", URL: "https://" + project + ".box.test" + path, Src: "server"}
}

// A flood while the store is stuck starts at most one extra flush and
// stops buffering at the cap.
func TestPipelineFloodIsBounded(t *testing.T) {
	ctx := context.Background()
	st := &fakeStore{block: make(chan struct{})}
	pl := testPipeline(st)
	before := runtime.NumGoroutine()
	now := time.Now()
	accepted := 0
	for i := 0; i < maxBufferedEvents+5000; i++ {
		if ok, _ := pl.Add(ctx, serverHit("shop", now, "/")); ok {
			accepted++
		}
	}
	if n := runtime.NumGoroutine() - before; n > 2 {
		t.Fatalf("%d goroutines started by the flood", n)
	}
	// The one early flush holds a batch, stuck in Insert; the buffer is full.
	if accepted > 2*maxBufferedEvents {
		t.Fatalf("accepted %d events with the store stuck", accepted)
	}
	if s := pl.Stats(); s.Failed == 0 || s.Buffered > maxBufferedEvents {
		t.Fatalf("stats %+v", s)
	}
	close(st.block)
	st.mu.Lock()
	st.block = nil
	st.mu.Unlock()
	for pl.flushing.Load() {
		time.Sleep(time.Millisecond)
	}
	if err := pl.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if st.inserted != accepted {
		t.Fatalf("inserted %d of %d", st.inserted, accepted)
	}
}

// A day is due for a rollup once its events are stored, and a failed
// rollup keeps every day it did not reach.
func TestRollupDaysFollowStoredEvents(t *testing.T) {
	ctx := context.Background()
	st := &fakeStore{insertErr: errors.New("store down")}
	pl := testPipeline(st)
	now := time.Now().UTC()
	pl.Add(ctx, serverHit("shop", now, "/"))
	if err := pl.RollupDirty(ctx); err == nil || len(pl.dirty) != 0 {
		t.Fatalf("a day was marked before its event was stored: %v %v", err, pl.dirty)
	}
	st.insertErr = nil
	for d := 0; d < 3; d++ {
		pl.Add(ctx, serverHit("shop", now.AddDate(0, 0, -d), "/"))
	}
	if err := pl.Flush(ctx); err != nil || len(pl.dirty) != 3 {
		t.Fatalf("after storing: %v %v", err, pl.dirty)
	}
	st.rollupFail = 1
	if err := pl.RollupDirty(ctx); err == nil || len(pl.dirty) != 3 {
		t.Fatalf("a failed rollup lost days: %v %v", err, pl.dirty)
	}
	if err := pl.RollupDirty(ctx); err != nil || len(pl.dirty) != 0 || len(st.rolledUp) != 3 {
		t.Fatalf("retry: %v %v %v", err, pl.dirty, st.rolledUp)
	}
}

// Deleting a project's analytics drops what is pending for it, so the
// next flush cannot write it back, and its hits until it is turned on again.
func TestDeletionDropsPendingEvents(t *testing.T) {
	ctx := t.Context()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "analytics.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m := &Module{store: st, pipe: testPipeline(st), vit: &Vitals{Store: st}}
	m.rt = m.pipe.RT
	now := time.Now()
	for _, p := range []string{"shop", "blog"} {
		if ok, why := m.pipe.Add(ctx, serverHit(p, now, "/")); !ok {
			t.Fatal(why)
		}
	}
	m.vit.add("shop", "web", "/", now, map[string]float64{"LCP": 1000})
	if err := m.deleteProject(ctx, "shop"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := m.pipe.Add(ctx, serverHit("shop", now, "/late")); ok {
		t.Fatal("a hit for a deleted project was accepted")
	}
	if err := m.pipe.RollupDirty(ctx); err != nil {
		t.Fatal(err)
	}
	_ = m.vit.Flush(ctx)
	q := func(p string) Query { return Query{Project: p, From: now.Add(-time.Hour), To: now.Add(time.Hour)} }
	if _, pv, _, _ := st.Counts(ctx, q("shop")); pv != 0 {
		t.Fatalf("deleted project's pending event was written back: %d", pv)
	}
	if vs, _ := st.Vitals(ctx, q("shop")); len(vs) != 0 {
		t.Fatalf("deleted project's vitals were written back: %v", vs)
	}
	if _, pv, _, _ := st.Counts(ctx, q("blog")); pv != 1 {
		t.Fatal("another project's event was dropped")
	}
	if v := m.rt.View("shop", "", now); v.Pageviews30m != 0 {
		t.Fatalf("realtime kept the deleted project: %+v", v)
	}
	m.pipe.Revive("shop")
	if ok, why := m.pipe.Add(ctx, serverHit("shop", now, "/")); !ok {
		t.Fatalf("turned back on: %s", why)
	}
}

// Realtime holds a bounded number of keys per minute and lets go of
// minutes (and apps) that left the window, with or without new traffic.
func TestRealtimeIsBoundedAndSwept(t *testing.T) {
	r := &Realtime{}
	now := time.Now()
	for i := 0; i < 3*maxRTKeys; i++ {
		r.Add(Event{TS: now, Project: "shop", App: "web", Kind: "pageview", Path: fmt.Sprintf("/p/%d", i), RefSource: fmt.Sprint(i), Visitor: int64(i + 1)})
	}
	for i := 0; i < 2*maxRTVisitors; i++ {
		r.Add(Event{TS: now, Project: "shop", App: "web", Kind: "pageview", Path: "/", Visitor: int64(i + 1)})
	}
	b := &r.apps[[2]string{"shop", "web"}][now.Unix()/60%rtMinutes]
	if len(b.pages) > maxRTKeys+1 || len(b.refs) > maxRTKeys+1 || len(b.visitors) > maxRTVisitors || b.pages["(other)"] == 0 {
		t.Fatalf("pages %d, refs %d, visitors %d", len(b.pages), len(b.refs), len(b.visitors))
	}
	r.Add(Event{TS: now, Project: "blog", App: "web", Kind: "pageview", Path: "/"})
	r.Sweep(now.Add(30 * time.Minute))
	if len(r.apps) != 2 {
		t.Fatalf("swept live minutes: %d apps", len(r.apps))
	}
	r.Forget("blog")
	r.Sweep(now.Add(2 * time.Hour))
	if len(r.apps) != 0 {
		t.Fatalf("%d apps left after the window passed", len(r.apps))
	}
}

// An explicit range is at most the longest retention: a year-0 range
// would fill ~740,000 daily points (twice, with the previous period).
func TestRangeIsBounded(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	if _, _, _, _, err := resolve("", "0000-01-01", "2026-10-07", now); err == nil {
		t.Fatal("a 2,000-year range was accepted")
	}
	if _, _, _, _, err := resolve("", "2016-10-08", "", now); err != nil {
		t.Fatalf("ten years: %v", err)
	}
	if _, _, _, _, err := resolve("", "2016-10-01", "", now); err == nil {
		t.Fatal("more than ten years was accepted")
	}
}
