package observe

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/mod/budget"
)

// An app killed for memory again and again fires app-out-of-memory for its
// project, once, and other projects stay quiet. (On a box where every check
// passed, one project was killed 16 times in two days and no alert fired:
// the machine had memory to spare, the project's limit did not.)
func TestMemoryKillsAlert(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	now := time.Now()
	kills := map[string][]budget.Event{}
	old := limitEvents
	limitEvents = func(_ context.Context, project string) []budget.Event { return kills[project] }
	t.Cleanup(func() { limitEvents = old })

	firing := func() string {
		_ = h.m.alerter.Evaluate(ctx)
		as, err := h.m.store.Firing(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, a := range as {
			if a.Rule == "app-out-of-memory" {
				out = append(out, a.Project+": "+a.Summary)
			}
		}
		return strings.Join(out, " | ")
	}
	ev := func(kind string, ago time.Duration) budget.Event {
		return budget.Event{At: now.Add(-ago), Kind: kind, Message: "shop used all of the 256 MB of memory it may use, so an app was stopped and restarted. Give it a bigger limit if it needs more."}
	}

	// One kill is a blip (a deploy, a spike). Other limit events and kills
	// older than a day do not count.
	kills["shop"] = []budget.Event{ev(budget.EventMemory, time.Hour), ev(budget.EventCacheFull, 2*time.Hour), ev(budget.EventMemory, 30*time.Hour)}
	if got := firing(); got != "" {
		t.Fatalf("one kill must not fire: %s", got)
	}
	// A second kill within the day fires, for that project only, with what to do.
	kills["shop"] = append([]budget.Event{ev(budget.EventMemory, time.Minute)}, kills["shop"]...)
	got := firing()
	if !strings.HasPrefix(got, "shop: project shop was stopped for memory 2 times in the last 24 hours") || !strings.Contains(got, "Give it a bigger limit") || strings.Contains(got, "other") {
		t.Fatalf("firing: %q", got)
	}
	hist, _ := h.m.store.History(ctx, 10, 0)
	if len(hist) != 1 || hist[0].Rule != "app-out-of-memory" || hist[0].Subject != "project:shop" || hist[0].State != "firing" {
		t.Fatalf("history: %+v", hist)
	}
	// Still at its limit: no second notification.
	_ = firing()
	if hist, _ = h.m.store.History(ctx, 10, 0); len(hist) != 1 {
		t.Fatalf("notified again: %+v", hist)
	}
	// A day without kills resolves it.
	kills["shop"] = nil
	if got := firing(); got != "" {
		t.Fatalf("must resolve: %s", got)
	}
	if hist, _ = h.m.store.History(ctx, 10, 0); len(hist) != 2 || hist[0].State != "resolved" {
		t.Fatalf("resolve: %+v", hist)
	}
}
