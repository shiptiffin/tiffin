package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/state"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// projects-list reads every project's version, resource count and failing
// resources in a fixed number of queries; what it reports is unchanged.
func TestProjectsListSummaries(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tm := tokens.NewManager(db)
	owner, _, _ := tm.Bootstrap(ctx)
	ownerP, _ := tm.Authenticate(ctx, owner)
	e := change.NewEngine(db)
	a := New(Deps{DB: db, Engine: e, Tokens: tm, Version: "test"})
	const n = 200
	for i := range n {
		project := fmt.Sprintf("p%03d", i)
		want := map[string]change.Resource{}
		for _, addr := range []string{change.KindProject, "app/web", "app/api"} {
			want[addr] = change.Resource{Address: addr, Spec: json.RawMessage(`{}`)}
		}
		p, err := e.Plan(ctx, project, want)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.Apply(ctx, change.ApplyRequest{Plan: p, Confirm: p.Hash}); err != nil {
			t.Fatal(err)
		}
		_ = db.SetResourceStatus(ctx, project, "app/web", "ready", "")
	}
	_ = db.SetResourceStatus(ctx, "p007", "app/api", "failed", "health check failed\nlog line")
	key, _, _ := tm.CreateKey(ctx, ownerP, tokens.KeyRequest{Name: "two", Projects: tokens.Projects{"p007", "p100"}, Access: tokens.LevelRead})
	list := func(token string) []ProjectSummary {
		req := httptest.NewRequest("GET", "/v1/projects", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, req)
		var out []ProjectSummary
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		return out
	}
	start := time.Now()
	all := list(owner)
	t.Logf("%d projects listed in %s", n, time.Since(start))
	if len(all) != n || all[7].Name != "p007" || all[7].Version != 1 || all[7].Resources != 3 ||
		len(all[7].Failing) != 1 || all[7].Failing[0] != "app/api: health check failed" || len(all[8].Failing) != 0 {
		t.Fatalf("list: %d projects, p007 %+v", len(all), all[7])
	}
	if two := list(key); len(two) != 2 || two[0].Name != "p007" || two[1].Name != "p100" {
		t.Fatalf("a key's projects: %+v", two)
	}
}
