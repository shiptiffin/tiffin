package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/state"
	"github.com/btahir/tiffin/internal/tokens"
)

// Pages of the change log follow commit order for a key that reaches some
// projects: a change created earlier but committed later (its clock read
// first) was skipped when the page was sorted by time and the cursor was a
// commit sequence.
func TestChangesPagingFollowsCommitOrder(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tm := tokens.NewManager(db)
	owner, _, _ := tm.Bootstrap(ctx)
	ownerP, _ := tm.Authenticate(ctx, owner)
	key, _, err := tm.CreateKey(ctx, ownerP, tokens.KeyRequest{Name: "two", Projects: tokens.Projects{"alpha", "beta"}, Access: tokens.LevelFull})
	if err != nil {
		t.Fatal(err)
	}
	e := change.NewEngine(db)
	a := New(Deps{DB: db, Engine: e, Tokens: tm, Version: "test"})
	t0 := time.Now()
	commit := func(project string, at time.Time) string {
		e.Now = func() time.Time { return at }
		p, err := e.Plan(ctx, project, map[string]change.Resource{change.KindProject: {Address: change.KindProject, Spec: json.RawMessage(`{}`)}})
		if err != nil {
			t.Fatal(err)
		}
		c, err := e.Apply(ctx, change.ApplyRequest{Plan: p, Confirm: p.Hash})
		if err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	b := commit("beta", t0.Add(time.Second)) // committed first, later clock
	al := commit("alpha", t0)                // committed second, earlier clock
	var seen []string
	before := ""
	for range 3 {
		req := httptest.NewRequest("GET", "/v1/changes?limit=1"+before, nil)
		req.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, req)
		var page []change.Change
		_ = json.Unmarshal(rec.Body.Bytes(), &page)
		if len(page) == 0 {
			break
		}
		seen = append(seen, page[0].ID)
		before = "&before=" + page[0].ID
	}
	if len(seen) != 2 || seen[0] != al || seen[1] != b {
		t.Fatalf("pages %v, want [%s %s]", seen, al, b)
	}
}
