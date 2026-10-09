package queue

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/state"
	"github.com/shiptiffin/tiffin/internal/tokens"
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
	if code, body, _ := call(owner, "PUT", "/v1/projects/shop/queue/queues/emails", map[string]any{"url": "http://10.0.0.5/x"}); code != 422 || !strings.Contains(fmt.Sprint(body["detail"]), "a private address") {
		t.Errorf("private url: %d %v", code, body)
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
	_, list, _ := call(owner, "GET", "/v1/projects/shop/queue/jobs?queue=emails&state=completed", nil)
	if items, _ := list["items"].([]any); len(items) != 1 || list["nextCursor"] != nil {
		t.Errorf("list %v", list)
	}
	if code, prob, _ := call(owner, "GET", "/v1/projects/shop/queue/jobs?cursor=nonsense", nil); code != 422 {
		t.Errorf("bad cursor: %d %v", code, prob)
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
	// A read-only token sees a waiting webhook but not its token: the URL
	// resumes the run with whatever is posted to it.
	viewer, _, err := tm.Create(t.Context(), &tokens.Principal{TokenID: "x", Name: "owner", Kind: tokens.KindOwner, Scopes: []tokens.Scope{tokens.ScopeAll}, Projects: []string{"*"}},
		tokens.CreateRequest{Name: "viewer", Kind: tokens.KindAgent, Scopes: []tokens.Scope{tokens.ScopeRead}, Projects: []string{"shop"}})
	if err != nil {
		t.Fatal(err)
	}
	sdk.defs["hooked"] = func(c *wctx) (any, error) {
		h := c.wait(WaitRequest{Name: "confirm", Kind: stepWebhook})
		var hk struct{ Event string }
		_ = json.Unmarshal(h.Output, &hk)
		return c.wait(WaitRequest{Name: "confirm:wait", Kind: stepEvent, Event: hk.Event}).Output, nil
	}
	_, hrun, _ := call(owner, "POST", "/v1/projects/shop/workflows/runs", map[string]any{"workflow": "hooked", "url": wf.URL + "/wf"})
	hookRun := hrun["id"].(string)
	eventually(t, 10*time.Second, "webhook run waiting", func() bool {
		_, r, _ := call(owner, "GET", "/v1/projects/shop/workflows/runs/"+hookRun, nil)
		return r["state"] == "waiting"
	})
	_, full, _ := call(owner, "GET", "/v1/projects/shop/workflows/runs/"+hookRun, nil)
	if raw, _ := json.Marshal(full); !hookToken.Match(raw) {
		t.Errorf("a writer should see the webhook URL: %s", raw)
	}
	code, seen, _ := call(viewer, "GET", "/v1/projects/shop/workflows/runs/"+hookRun, nil)
	if raw, _ := json.Marshal(seen); code != 200 || hookToken.Match(raw) || !strings.Contains(string(raw), "wh_(hidden)") {
		t.Errorf("read-only view %d: %s", code, raw)
	}
	// Mutations are audited.
	ev, _ := db.AuditLog(t.Context(), 50, 0)
	found := false
	for _, x := range ev {
		found = found || x.Action == "workflow.approval.approve"
	}
	if !found {
		t.Error("approval not audited")
	}
	// The dashboard watches a job over the box API: state, progress and
	// output as server-sent events until it ends.
	release := make(chan struct{})
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	defer free()
	ap.handle("/watched", func(w http.ResponseWriter, r *http.Request) {
		var b deliveryBody
		_ = json.NewDecoder(r.Body).Decode(&b)
		n, _ := ParseJobID(b.ID)
		_ = e.JobProgress(r.Context(), "shop", n, b.AttemptID, json.RawMessage(`{"pct":40}`))
		_, _ = e.JobOutput(r.Context(), "shop", n, b.AttemptID, json.RawMessage(`"rendering page 2"`))
		<-release
		_, _ = w.Write([]byte(`{"pages":3}`))
	})
	e.configure("shop", QueueConfig{Name: "watched", URL: ap.url("/watched")})
	wid := e.send("shop", SendRequest{Name: "watched"}).Jobs[0]
	if code, st, _ := call(owner, "GET", "/v1/projects/shop/queue/live/"+wid, nil); code != 200 || st["type"] != "job" {
		t.Fatalf("live state %d %v", code, st)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/v1/projects/shop/queue/live/"+wid, nil)
	req.Header.Set("Authorization", "Bearer "+owner)
	req.Header.Set("Accept", "text/event-stream")
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream: %v %v", err, res)
	}
	stream := make(chan string, 1)
	go func() { raw, _ := io.ReadAll(res.Body); stream <- string(raw) }()
	eventually(t, 10*time.Second, "progress reported", func() bool { return strings.ReplaceAll(string(e.job("shop", wid).Progress), " ", "") == `{"pct":40}` })
	free()
	select {
	case s := <-stream:
		for _, want := range []string{`event: output` + "\n" + `data: "rendering page 2"`, `"progress":{"pct":40}`, `"status":"completed"`, "event: end"} {
			if !strings.Contains(s, want) {
				t.Errorf("stream lacks %q:\n%s", want, s)
			}
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the stream did not end with the job")
	}
	if code, _, _ := call(owner, "GET", "/v1/projects/other/queue/live/"+wid, nil); code != 404 {
		t.Errorf("another project's job: %d", code)
	}

	// Crons: preview a schedule, pause and resume.
	if code, pv, _ := call(owner, "GET", "/v1/projects/shop/queue/schedule-preview?schedule=0+9+*+*+1-5&timezone=Europe/London", nil); code != 200 || len(pv["next"].([]any)) != 5 {
		t.Errorf("preview %d %v", code, pv)
	}
	if code, pv, _ := call(owner, "GET", "/v1/projects/shop/queue/schedule-preview?schedule=61+*+*+*+*", nil); code != 422 || pv["hint"] == nil {
		t.Errorf("bad preview %d %v", code, pv)
	}
	if err := e.ReconcileCron(t.Context(), "shop", "nightly", json.RawMessage(`{"schedule":"0 2 * * *","url":"`+ap.url("/n")+`"}`)); err != nil {
		t.Fatal(err)
	}
	if code, c, _ := call(owner, "POST", "/v1/projects/shop/queue/crons/nightly/pause", nil); code != 200 || c["paused"] != true {
		t.Errorf("pause %d %v", code, c)
	}
	if code, c, _ := call(owner, "POST", "/v1/projects/shop/queue/crons/nightly/resume", nil); code != 200 || c["paused"] != false {
		t.Errorf("resume %d %v", code, c)
	}
	if code, s, _ := call(owner, "GET", "/v1/projects/shop/queue/signing-secret", nil); code != 200 || !strings.HasPrefix(fmt.Sprint(s["secret"]), "tqs_") {
		t.Errorf("signing secret %d %v", code, s)
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
	if n != 29 {
		t.Errorf("%d queue/workflow operations", n)
	}
}
