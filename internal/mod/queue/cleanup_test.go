package queue

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A wrong key or subscribe token never creates credentials: authentication
// only reads, whatever project the caller claims.
func TestAuthenticationCreatesNothing(t *testing.T) {
	e := newEngine(t, nil)
	box := httptest.NewServer(e.InternalHandler())
	defer box.Close()
	ctx := context.Background()
	for _, key := range []string{"tqk_ghost_bad", "tqk_ghost_web_" + strings.Repeat("0", 32), "tqk_NOT-A-SLUG_x"} {
		req, _ := http.NewRequest("POST", box.URL+"/v1/queue-internal/send", strings.NewReader(`{"name":"q"}`))
		req.Header.Set("Authorization", "Bearer "+key)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 401 {
			t.Errorf("%s: %d", key, res.StatusCode)
		}
	}
	if _, _, err := e.checkToken(ctx, "live1.ghost2.job_1."+strings.Repeat("9", 10)+".abc", "job_1", time.Now()); err == nil {
		t.Error("token for an unknown project accepted")
	}
	for _, p := range []string{"ghost", "ghost2", "NOT-A-SLUG"} {
		if _, _, ok, _ := e.cfg.Keys.Lookup(ctx, p); ok {
			t.Errorf("a failed authentication created keys for %q", p)
		}
	}
}

// Deleting a project revokes its key (a new project of the same name gets a
// fresh one) and removes its jobs, runs and events.
func TestDeleteProject(t *testing.T) {
	e := newEngine(t, nil)
	ctx := context.Background()
	oldKey, _, _ := e.cfg.Keys.Get(ctx, proj)
	if _, _, ok := CheckKey(ctx, e.cfg.Keys, oldKey); !ok {
		t.Fatal("key refused before the delete")
	}
	res := e.send(proj, SendRequest{Name: "emails", Payload: json.RawMessage(`{}`), Delay: time.Hour, App: "web"})
	run, _, err := e.StartRun(ctx, proj, StartRequest{Workflow: "w", URL: "https://example.com/wf"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Emit(ctx, proj, "paid", nil, "test"); err != nil {
		t.Fatal(err)
	}
	other := e.send("other", SendRequest{Name: "emails", Delay: time.Hour, App: "web"})

	if err := e.cfg.Keys.Delete(ctx, proj); err != nil {
		t.Fatal(err)
	}
	if err := e.DeleteProject(ctx, proj); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := CheckKey(ctx, e.cfg.Keys, oldKey); ok {
		t.Error("the deleted project's key still works")
	}
	if newKey, _, _ := e.cfg.Keys.Get(ctx, proj); newKey == oldKey {
		t.Error("a new project of the same name got the old key")
	}
	if _, err := e.GetJob(ctx, proj, mustJobID(t, res.Jobs[0])); err == nil {
		t.Error("job kept")
	}
	if _, err := e.GetRun(ctx, proj, run.ID, false); err == nil {
		t.Error("run kept")
	}
	if r, err := e.Emit(ctx, proj, "paid", nil, "test"); err != nil || !r.Accepted {
		t.Errorf("event kept: %+v %v", r, err)
	}
	if _, err := e.GetJob(ctx, "other", mustJobID(t, other.Jobs[0])); err != nil {
		t.Errorf("another project's job went too: %v", err)
	}
}

func mustJobID(t testing.TB, id string) int64 {
	t.Helper()
	n, err := ParseJobID(id)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
