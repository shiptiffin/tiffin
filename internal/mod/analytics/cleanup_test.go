package analytics

import (
	"path/filepath"
	"testing"
	"time"
)

// Destroying a project forgets its events, rollups and collector keys, and
// nothing of other projects.
func TestProjectDeletedForgetsEverything(t *testing.T) {
	ctx := t.Context()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "analytics.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now()
	var evs []Event
	for _, p := range []string{"shop", "blog"} {
		evs = append(evs, Event{TS: now, Project: p, App: "web", Kind: "pageview", Name: "pageview", Host: p + ".box.test", Path: "/", Visitor: 1, Session: 1})
	}
	if err := st.Insert(ctx, evs); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"shop", "blog"} {
		if err := st.Rollup(ctx, p, "web", dayOf(now)); err != nil {
			t.Fatal(err)
		}
	}
	shopKey, _ := st.Key(ctx, "shop", "web")
	blogKey, _ := st.Key(ctx, "blog", "web")

	m := &Module{store: st}
	if err := m.ProjectDeleted(ctx, nil, "shop"); err != nil {
		t.Fatal(err)
	}
	q := func(p string) Query { return Query{Project: p, From: now.Add(-time.Hour), To: now.Add(time.Hour)} }
	if v, pv, e, _ := st.Counts(ctx, q("shop")); v+pv+e != 0 {
		t.Fatal("events left after the project was destroyed")
	}
	if rows, _ := st.Daily(ctx, q("shop")); len(rows) != 0 {
		t.Fatalf("rollups left: %v", rows)
	}
	if _, _, ok := st.LookupKey(ctx, shopKey); ok {
		t.Fatal("the destroyed project's collector key still works")
	}
	if _, pv, _, _ := st.Counts(ctx, q("blog")); pv != 1 {
		t.Fatal("another project's events went too")
	}
	if p, _, ok := st.LookupKey(ctx, blogKey); !ok || p != "blog" {
		t.Fatal("another project's key went too")
	}
	if err := (&Module{}).ProjectDeleted(ctx, nil, "shop"); err != nil {
		t.Fatal("analytics not running: nothing to do")
	}
}
