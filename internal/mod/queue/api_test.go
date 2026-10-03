package queue

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
	"github.com/btahir/tiffin/internal/tokens"
)

func TestAPI(t *testing.T) {
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tm := tokens.NewManager(db)
	owner, _, err := tm.Bootstrap(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var mod *Module
	for _, m := range platform.Modules() {
		if q, ok := m.(*Module); ok {
			mod = q
		}
	}
	plat := &platform.Platform{DB: db}
	a := api.New(api.Deps{DB: db, Engine: change.NewEngine(db), Tokens: tm, Version: "test", Platform: plat})
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	call := func(token, method, path string, body any) (int, map[string]any, []any) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rd)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		var m map[string]any
		var l []any
		if json.Unmarshal(raw, &m) != nil {
			_ = json.Unmarshal(raw, &l)
		}
		return res.StatusCode, m, l
	}

	// Before the engine runs, operations answer 503 with a reason.
	if code, body, _ := call(owner, "GET", "/v1/projects/shop/queue/stats", nil); code != 503 || !strings.Contains(body["detail"].(string), "not running") {
		t.Fatalf("not ready: %d %v", code, body)
	}
	e := newEngine(t, nil)
	mod.mu.Lock()
	mod.eng, mod.keys = e.Engine, e.cfg.Keys
	mod.mu.Unlock()
	defer func() { mod.mu.Lock(); mod.eng = nil; mod.mu.Unlock() }()

	ap := newApp(t, e.Engine, "shop")
	sdk := newFakeSDK(t, e.Engine)
	sdk.defs["approve"] = func(c *wctx) (any, error) {
		return c.wait(WaitRequest{Name: "go", Kind: stepApproval, Title: "Deploy to prod", HumanOnly: true}).Output, nil
	}
	wf := httptest.NewServer(sdk.handler("", nil))
	defer wf.Close()

	code, cfg, _ := call(owner, "PUT", "/v1/projects/shop/queue/queues/emails", map[string]any{"url": ap.url("/e"), "keyConcurrency": 2})
	if code != 200 || cfg["keyConcurrency"].(float64) != 2 || cfg["maxAttempts"].(float64) != 10 {
		t.Fatalf("configure %d %v", code, cfg)
	}
	if code, body, _ := call(owner, "PUT", "/v1/projects/shop/queue/queues/emails", map[string]any{"url": "http://example.com/x"}); code != 422 || body["hint"] == nil {
		t.Errorf("non-loopback url: %d %v", code, body)
	}
	code, sent, _ := call(owner, "POST", "/v1/projects/shop/queue/send", map[string]any{"name": "emails", "payload": map[string]string{"to": "x"}, "key": "c1"})
	if code != 200 {
		t.Fatalf("send %d %v", code, sent)
	}
	id := sent["jobs"].([]any)[0].(string)
	eventually(t, 10*time.Second, "job completed via API", func() bool {
		_, j, _ := call(owner, "GET", "/v1/projects/shop/queue/jobs/"+id, nil)
		return j["state"] == "completed"
	})
	_, _, list := call(owner, "GET", "/v1/projects/shop/queue/jobs?queue=emails&state=completed", nil)
	if len(list) != 1 {
		t.Errorf("list %v", list)
	}
	_, _, stats := call(owner, "GET", "/v1/projects/shop/queue/stats", nil)
	if len(stats) != 1 || stats[0].(map[string]any)["completedLastHour"].(float64) != 1 {
		t.Errorf("stats %v", stats)
	}

	// A scoped agent token: can send and read, cannot purge, cannot decide humanOnly approvals.
	agent, _, err := tm.Create(t.Context(), &tokens.Principal{TokenID: "x", Name: "owner", Kind: tokens.KindOwner, Scopes: []tokens.Scope{tokens.ScopeAll}, Projects: []string{"*"}},
		tokens.CreateRequest{Name: "claude", Kind: tokens.KindAgent, Projects: []string{"shop"}})
	if err != nil {
		t.Fatal(err)
	}
	if code, _, _ := call(agent, "GET", "/v1/projects/other/queue/stats", nil); code != 403 {
		t.Errorf("agent on another project: %d", code)
	}
	if code, body, _ := call(agent, "POST", "/v1/projects/shop/queue/queues/emails/purge", map[string]any{}); code != 403 {
		t.Errorf("agent purge: %d %v", code, body)
	}
	code, run, _ := call(agent, "POST", "/v1/projects/shop/workflows/runs", map[string]any{"workflow": "approve", "url": wf.URL + "/wf"})
	if code != 200 {
		t.Fatalf("start %d %v", code, run)
	}
	var apr string
	eventually(t, 10*time.Second, "approval listed", func() bool {
		_, _, as := call(agent, "GET", "/v1/projects/shop/workflows/approvals", nil)
		if len(as) == 1 {
			apr = as[0].(map[string]any)["id"].(string)
		}
		return apr != ""
	})
	code, body, _ := call(agent, "POST", "/v1/projects/shop/workflows/approvals/"+apr, map[string]any{"decision": "approve"})
	if code != 403 || !strings.Contains(body["detail"].(string), "needs a human decision") {
		t.Errorf("agent approval: %d %v", code, body)
	}
	if code, body, _ := call(owner, "POST", "/v1/projects/shop/workflows/approvals/"+apr, map[string]any{"decision": "approve", "comment": "ship it"}); code != 200 {
		t.Fatalf("owner approval %d %v", code, body)
	}
	runID := run["id"].(string)
	eventually(t, 10*time.Second, "run completed", func() bool {
		_, r, _ := call(owner, "GET", "/v1/projects/shop/workflows/runs/"+runID, nil)
		return r["state"] == "completed"
	})
	_, r, _ := call(owner, "GET", "/v1/projects/shop/workflows/runs/"+runID, nil)
	tl, _ := json.Marshal(r["timeline"])
	if !strings.Contains(string(tl), "approved") || !strings.Contains(string(tl), "owner (owner)") {
		t.Errorf("timeline %s", tl)
	}
	// Mutations are audited.
	ev, _ := db.AuditLog(t.Context(), 50)
	found := false
	for _, x := range ev {
		found = found || x.Action == "workflow.approval.approve"
	}
	if !found {
		t.Error("approval not audited")
	}
	// Spec: every operation has a CLI path under queue or workflows.
	n := 0
	for _, o := range a.Operations() {
		if p := api.CLIPath(o); p[0] == "queue" || p[0] == "workflows" {
			n++
			if o.Description == "" {
				t.Errorf("%s has no description", o.OperationID)
			}
		}
		if strings.Contains(o.Path, "queue-internal") || strings.Contains(o.Path, "/hooks/") {
			t.Errorf("internal endpoint %s leaked into the spec", o.Path)
		}
	}
	if n != 24 {
		t.Errorf("%d queue/workflow operations", n)
	}
}
