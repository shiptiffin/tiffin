package queue

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/manifest"
)

// Crons from an app's vercel.json: called with GET and CRON_SECRET as
// Vercel calls them, replaced as a whole on each deploy, and the manifest
// wins on a clash.
func TestFileCrons(t *testing.T) {
	var a *app
	e := newEngine(t, func(c *Config) {
		c.Endpoint = func(ctx context.Context, project, name, release string) (string, error) { return a.srv.URL, nil }
		c.AppEnv = func(ctx context.Context, project, app string) (map[string]string, error) {
			return map[string]string{"CRON_SECRET": "s3cret"}, nil
		}
	})
	a = newApp(t, e.Engine, proj)
	type call struct{ method, auth, ua, ctype string }
	var mu sync.Mutex
	var calls []call
	record := func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, call{r.Method, r.Header.Get("Authorization"), r.Header.Get("User-Agent"), r.Header.Get("Content-Type")})
		mu.Unlock()
	}
	a.handle("/api/cron/digest", record)
	a.handle("/cron/nightly", record)
	ctx := context.Background()
	if err := e.ReconcileCron(ctx, proj, "nightly", json.RawMessage(`{"schedule":"0 3 * * *","app":"web","path":"/cron/nightly"}`)); err != nil {
		t.Fatal(err)
	}
	set := func(crons map[string]manifest.Cron) {
		t.Helper()
		if err := e.SetFileCrons(ctx, proj, "web", "vercel.json", crons); err != nil {
			t.Fatal(err)
		}
	}
	list := func() map[string]CronInfo {
		cs, err := e.Crons(ctx, proj)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]CronInfo{}
		for _, c := range cs {
			out[c.Name] = c
		}
		return out
	}
	set(map[string]manifest.Cron{
		"api-cron-digest": {Schedule: "0 5 * * *", App: "web", Path: "/api/cron/digest"},
		"nightly":         {Schedule: "0 1 * * *", App: "web", Path: "/elsewhere"}, // the manifest's name
		"cron-nightly":    {Schedule: "0 1 * * *", App: "web", Path: "/cron/nightly"},
	})
	cs := list()
	if len(cs) != 2 || cs["api-cron-digest"].Origin != "vercel.json" || cs["api-cron-digest"].Method != "GET" ||
		cs["nightly"].Origin != "tiffin.config.ts" || cs["nightly"].Method != "POST" || cs["nightly"].Target != "web:/cron/nightly" {
		t.Fatalf("crons %+v", cs)
	}

	for _, name := range []string{"api-cron-digest", "nightly"} {
		id, err := e.TriggerCron(ctx, proj, name, "test")
		if err != nil {
			t.Fatal(err)
		}
		e.waitState(proj, id, stateCompleted, 10*time.Second)
	}
	mu.Lock()
	got := append([]call(nil), calls...)
	mu.Unlock()
	want := []call{{"GET", "Bearer s3cret", "vercel-cron/1.0", ""}, {"POST", "", "tiffin-queue/1", "application/json"}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("calls %+v, want %+v", got, want)
	}

	// The next deploy's vercel.json replaces the set; an unchanged schedule keeps its tick.
	before := cs["api-cron-digest"].NextAt
	set(map[string]manifest.Cron{"api-cron-digest": {Schedule: "0 5 * * *", App: "web", Path: "/api/cron/digest"}})
	if c := list()["api-cron-digest"]; !c.NextAt.Equal(before) {
		t.Errorf("redeploy moved the next tick: %v → %v", before, c.NextAt)
	}
	set(nil)
	if cs := list(); len(cs) != 1 || cs["nightly"].Origin != "tiffin.config.ts" {
		t.Fatalf("after removing vercel.json crons: %+v", cs)
	}

	// The manifest takes over a name a file declared.
	set(map[string]manifest.Cron{"weekly": {Schedule: "0 5 * * 1", App: "web", Path: "/api/cron/digest"}})
	if err := e.ReconcileCron(ctx, proj, "weekly", json.RawMessage(`{"schedule":"0 6 * * 1","app":"web","path":"/api/cron/digest"}`)); err != nil {
		t.Fatal(err)
	}
	if c := list()["weekly"]; c.Origin != "tiffin.config.ts" || c.Method != "POST" || c.Schedule != "0 6 * * 1" {
		t.Fatalf("manifest over a file cron: %+v", c)
	}
	set(map[string]manifest.Cron{"weekly": {Schedule: "0 5 * * 1", App: "web", Path: "/api/cron/digest"}})
	if c := list()["weekly"]; c.Origin != "tiffin.config.ts" || c.Schedule != "0 6 * * 1" {
		t.Fatalf("a file cron replaced the manifest's: %+v", c)
	}
}
