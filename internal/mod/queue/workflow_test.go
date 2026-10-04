package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSDK speaks the app side of the workflow protocol the way tiffin-sdk
// does, through the app-facing endpoints: replay recorded steps, run new
// ones, record each, suspend at waits.
type fakeSDK struct {
	t    testing.TB
	box  *httptest.Server
	key  string
	defs map[string]func(c *wctx) (any, error)
}

type wctx struct {
	sdk     *fakeSDK
	run     *turnRun
	seq     int
	byName  map[string]Step
	release string
}

type suspend struct{}

var errNonRetryable = errors.New("non-retryable")

func newFakeSDK(t testing.TB, e *Engine) *fakeSDK {
	box := httptest.NewServer(e.InternalHandler())
	t.Cleanup(box.Close)
	key, _, _ := e.cfg.Keys.Get(context.Background(), proj)
	return &fakeSDK{t: t, box: box, key: key, defs: map[string]func(*wctx) (any, error){}}
}

func (s *fakeSDK) call(method, path string, body any, out any) int {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, s.box.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+s.key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if out != nil {
		_ = json.Unmarshal(raw, out)
	}
	if res.StatusCode >= 300 {
		s.t.Logf("%s %s → %d %s", method, path, res.StatusCode, raw)
	}
	return res.StatusCode
}

// handler serves turns for this fake app (release label for pinning tests).
func (s *fakeSDK) handler(release string, hits *atomic.Int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		var b deliveryBody
		_ = json.NewDecoder(r.Body).Decode(&b)
		c := &wctx{sdk: s, run: b.Run, byName: map[string]Step{}, release: release}
		for _, st := range b.Run.Steps {
			c.byName[st.Name] = st
		}
		def := s.defs[b.Run.Workflow]
		var out any
		var err error
		suspended := false
		func() {
			defer func() {
				if p := recover(); p != nil {
					if _, ok := p.(suspend); ok {
						suspended = true
						return
					}
					panic(p)
				}
			}()
			out, err = def(c)
		}()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case suspended:
			fmt.Fprint(w, `{"status":"suspended"}`)
		case errors.Is(err, errNonRetryable):
			w.WriteHeader(StatusNonRetrying)
			fmt.Fprintf(w, `{"error":%q}`, err.Error())
		case err != nil:
			w.WriteHeader(500)
			fmt.Fprintf(w, `{"error":%q}`, err.Error())
		default:
			o, _ := json.Marshal(out)
			fmt.Fprintf(w, `{"status":"completed","output":%s}`, o)
		}
	}
}

func (c *wctx) next() int { c.seq++; return c.seq - 1 }

func (c *wctx) step(name string, fn func() (any, error)) (json.RawMessage, error) {
	seq := c.next()
	if st, ok := c.byName[name]; ok && st.State == stepCompleted {
		return st.Output, nil
	}
	v, err := fn()
	rec := StepRecord{Name: name, Seq: seq, OK: err == nil, StartedAt: time.Now()}
	if err != nil {
		rec.Error = err.Error()
	} else {
		rec.Output, _ = json.Marshal(v)
	}
	var st Step
	if code := c.sdk.call("POST", "/v1/queue-internal/workflows/runs/"+c.run.ID+"/steps", rec, &st); code != 200 {
		return nil, fmt.Errorf("record step: %d", code)
	}
	if err != nil {
		return nil, err
	}
	return st.Output, nil
}

func (c *wctx) wait(w WaitRequest) Step {
	w.Seq = c.next()
	if st, ok := c.byName[w.Name]; ok && st.State != stepWaiting {
		return st
	}
	var st Step
	if code := c.sdk.call("POST", "/v1/queue-internal/workflows/runs/"+c.run.ID+"/waits", w, &st); code != 200 {
		panic(fmt.Sprintf("wait %s: HTTP %d", w.Name, code))
	}
	if st.State == stepWaiting {
		panic(suspend{})
	}
	return st
}

func (c *wctx) patched(id string) bool {
	if _, ok := c.byName["patch:"+id]; ok {
		return true
	}
	// Replaying recorded history that never saw this patch: old code path.
	for _, st := range c.byName {
		if st.Seq >= c.seq && st.Kind != stepPatch {
			return false
		}
	}
	c.sdk.call("POST", "/v1/queue-internal/workflows/runs/"+c.run.ID+"/waits", WaitRequest{Name: "patch:" + id, Kind: stepPatch, Seq: c.seq}, nil)
	return true
}

func (s *fakeSDK) start(workflow string, input any, url, idem string) *Run {
	var res struct {
		Run     *Run `json:"run"`
		Created bool `json:"created"`
	}
	in, _ := json.Marshal(input)
	body := map[string]any{"workflow": workflow, "input": json.RawMessage(in), "id": idem, "fromApp": "web"}
	if code := s.call("POST", "/v1/queue-internal/workflows/start", body, &res); code != 200 {
		s.t.Fatalf("start: %d", code)
	}
	return res.Run
}

func waitRun(t testing.TB, e *Engine, id, state string, d time.Duration) *Run {
	t.Helper()
	var r *Run
	eventually(t, d, "run "+id+" to be "+state, func() bool {
		var err error
		r, err = e.GetRun(context.Background(), proj, id, true)
		if err != nil {
			t.Fatal(err)
		}
		return r.State == state
	})
	return r
}

func TestWorkflowEndToEnd(t *testing.T) {
	var cur atomic.Value
	cur.Store("r1")
	endpoints := map[string]string{}
	var emu sync.Mutex
	e := newEngine(t, func(c *Config) {
		c.CurrentRelease = func(ctx context.Context, project, app string) (string, error) { return cur.Load().(string), nil }
		c.Endpoint = func(ctx context.Context, project, app, release string) (string, error) {
			emu.Lock()
			defer emu.Unlock()
			if release == "" {
				release = cur.Load().(string)
			}
			u, ok := endpoints[release]
			if !ok {
				return "", fmt.Errorf("release %s: %w", release, ErrReleaseGone)
			}
			return u, nil
		}
	})
	sdk := newFakeSDK(t, e.Engine)
	var charges atomic.Int64
	sdk.defs["order"] = func(c *wctx) (any, error) {
		var in struct {
			ID     int `json:"id"`
			Amount int `json:"amount"`
		}
		_ = json.Unmarshal(c.run.Input, &in)
		charged, err := c.step("charge", func() (any, error) {
			if charges.Add(1) == 1 {
				return nil, errors.New("card network timeout")
			}
			return map[string]int{"charged": in.Amount}, nil
		})
		if err != nil {
			return nil, err
		}
		c.wait(WaitRequest{Name: "cool-off", Kind: stepSleep, Until: c.run.CreatedAt.Add(time.Second)})
		paid := c.wait(WaitRequest{Name: "paid", Kind: stepEvent, Event: fmt.Sprintf("order-%d-paid", in.ID), TimeoutS: 60})
		ship := c.wait(WaitRequest{Name: "ship", Kind: stepApproval, Title: "Ship order", HumanOnly: true})
		extra := json.RawMessage("null")
		if c.patched("gift-note") {
			extra, _ = c.step("gift-note", func() (any, error) { return "added", nil })
		}
		return map[string]any{"charged": charged, "paid": paid.Output, "ship": ship.Output, "extra": extra, "release": c.release}, nil
	}
	var hitsR1, hitsR2 atomic.Int64
	r1 := httptest.NewServer(sdk.handler("r1", &hitsR1))
	defer r1.Close()
	r2 := httptest.NewServer(sdk.handler("r2", &hitsR2))
	defer r2.Close()
	emu.Lock()
	endpoints["r1"], endpoints["r2"] = r1.URL, r2.URL
	emu.Unlock()
	ctx := context.Background()

	run := sdk.start("order", map[string]int{"id": 7, "amount": 1200}, "", "order-7")
	if run.Release != "r1" || run.App != "web" {
		t.Fatalf("run %+v", run)
	}
	again := sdk.start("order", map[string]int{"id": 7}, "", "order-7")
	if again.ID != run.ID {
		t.Fatal("idempotent start made a second run")
	}
	// The run charges (after one failed attempt), sleeps 1s, then waits for payment.
	r := waitRun(t, e.Engine, run.ID, runWaiting, 15*time.Second)
	eventually(t, 10*time.Second, "waiting for the payment event", func() bool {
		r, _ = e.GetRun(ctx, proj, run.ID, true)
		return strings.Contains(r.WaitingFor, `event "order-7-paid"`)
	})
	if pins, _ := e.PinnedReleases(ctx, proj, "web"); len(pins) != 1 || pins[0] != "r1" {
		t.Errorf("pinned %v", pins)
	}
	// Deploy release r2: the run stays on r1.
	cur.Store("r2")
	res, err := e.Emit(ctx, proj, "order-7-paid", json.RawMessage(`{"txn":"t_1"}`), "test")
	if err != nil || !res.Accepted || len(res.Woke) != 1 {
		t.Fatalf("emit %+v %v", res, err)
	}
	if res, _ := e.Emit(ctx, proj, "order-7-paid", json.RawMessage(`{"txn":"t_2"}`), "test"); res.Accepted {
		t.Error("second emit accepted: first emit must win")
	}
	if _, err := e.Emit(ctx, proj, "approval:x", nil, "test"); err == nil {
		t.Error("reserved event name accepted")
	}
	eventually(t, 10*time.Second, "approval to be requested", func() bool {
		as, _ := e.Approvals(ctx, proj, "waiting")
		return len(as) == 1
	})
	as, _ := e.Approvals(ctx, proj, "waiting")
	if as[0].Title != "Ship order" || !as[0].HumanOnly || as[0].RunID != run.ID {
		t.Errorf("approval %+v", as[0])
	}
	if _, err := e.Decide(ctx, proj, as[0].ID, true, "", Decider{Name: "claude (agent)", Human: false}); err == nil || err.(*Error).Status != 403 {
		t.Fatalf("agent decided a humanOnly approval: %v", err)
	}
	if _, err := e.Decide(ctx, proj, as[0].ID, true, "looks good", Decider{Name: "owner (human)", Human: true}); err != nil {
		t.Fatal(err)
	}
	r = waitRun(t, e.Engine, run.ID, runCompleted, 15*time.Second)
	var out struct {
		Charged map[string]int    `json:"charged"`
		Paid    map[string]string `json:"paid"`
		Ship    map[string]any    `json:"ship"`
		Extra   string            `json:"extra"`
		Release string            `json:"release"`
	}
	if err := json.Unmarshal(r.Output, &out); err != nil {
		t.Fatal(err)
	}
	if out.Charged["charged"] != 1200 || out.Paid["txn"] != "t_1" || out.Ship["approved"] != true || out.Release != "r1" || out.Extra != "added" {
		t.Errorf("output %s", r.Output)
	}
	if charges.Load() != 2 {
		t.Errorf("charge ran %d times; completed steps must not re-run", charges.Load())
	}
	if hitsR2.Load() != 0 {
		t.Errorf("pinned run was delivered to the new release %d times", hitsR2.Load())
	}
	names := []string{}
	for _, s := range r.Steps {
		names = append(names, s.Name+":"+s.State)
	}
	if strings.Join(names, ",") != "charge:completed,cool-off:completed,paid:completed,ship:completed,patch:gift-note:completed,gift-note:completed" {
		t.Errorf("steps %v", names)
	}
	var kinds []string
	for _, tl := range r.Timeline {
		kinds = append(kinds, tl.Kind)
	}
	tl := strings.Join(kinds, ",")
	for _, k := range []string{"started", "turn", "wait", "event", "approval", "completed"} {
		if !strings.Contains(tl, k) {
			t.Errorf("timeline lacks %s: %s", k, tl)
		}
	}
	if pins, _ := e.PinnedReleases(ctx, proj, "web"); len(pins) != 0 {
		t.Errorf("finished run still pins %v", pins)
	}
	t.Logf("run %s: %d turns, %d steps, timeline %s", r.ID, r.Turns, len(r.Steps), tl)

	// A run whose release is gone moves to the current release.
	run2 := sdk.start("order", map[string]int{"id": 8, "amount": 5}, "", "")
	if run2.Release != "r2" {
		t.Fatalf("new run on %s", run2.Release)
	}
	eventually(t, 15*time.Second, "run2 waiting for payment", func() bool {
		r, _ = e.GetRun(ctx, proj, run2.ID, false)
		return strings.Contains(r.WaitingFor, "order-8-paid")
	})
	emu.Lock()
	delete(endpoints, "r2")
	endpoints["r3"] = r1.URL
	emu.Unlock()
	cur.Store("r3")
	_, _ = e.Emit(ctx, proj, "order-8-paid", nil, "test")
	eventually(t, 15*time.Second, "run2 moved to r3", func() bool {
		r, _ = e.GetRun(ctx, proj, run2.ID, true)
		return r.Release == "r3"
	})
	if _, err := e.CancelRun(ctx, proj, run2.ID, "test"); err != nil {
		t.Fatal(err)
	}
	r = waitRun(t, e.Engine, run2.ID, runCancelled, 5*time.Second)
	moved := false
	for _, x := range r.Timeline {
		moved = moved || (x.Kind == "release" && strings.Contains(x.Message, "moved to release r3"))
	}
	if !moved {
		t.Errorf("timeline: %+v", r.Timeline)
	}
}

func TestWorkflowFailuresAndEarlyEvents(t *testing.T) {
	e := newEngine(t, nil)
	sdk := newFakeSDK(t, e.Engine)
	app := httptest.NewServer(sdk.handler("", nil))
	defer app.Close()
	ctx := context.Background()
	var broken atomic.Bool
	broken.Store(true)
	sdk.defs["flaky"] = func(c *wctx) (any, error) {
		if _, err := c.step("one", func() (any, error) { return 1, nil }); err != nil {
			return nil, err
		}
		_, err := c.step("two", func() (any, error) {
			if broken.Load() {
				return nil, fmt.Errorf("bad input: %w", errNonRetryable)
			}
			return 2, nil
		})
		if err != nil {
			return nil, err
		}
		ev := c.wait(WaitRequest{Name: "go", Kind: stepEvent, Event: "go-signal"})
		late := c.wait(WaitRequest{Name: "late", Kind: stepEvent, Event: "never", TimeoutS: 1})
		return map[string]any{"go": ev.Output, "lateState": late.State}, nil
	}
	start := func() *Run {
		run, _, err := e.StartRun(ctx, proj, StartRequest{Workflow: "flaky", URL: app.URL + "/wf", By: "test"})
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	run := start()
	r := waitRun(t, e.Engine, run.ID, runFailed, 10*time.Second)
	if !strings.Contains(r.Error, "bad input") {
		t.Errorf("error %q", r.Error)
	}
	// The event arrives before anyone waits: it is kept, not lost.
	if res, _ := e.Emit(ctx, proj, "go-signal", json.RawMessage(`"early"`), "test"); !res.Accepted || len(res.Woke) != 0 {
		t.Errorf("early emit %+v", res)
	}
	broken.Store(false)
	if _, err := e.RetryRun(ctx, proj, run.ID, "test"); err != nil {
		t.Fatal(err)
	}
	r = waitRun(t, e.Engine, run.ID, runCompleted, 15*time.Second)
	if string(r.Output) != `{"go": "early", "lateState": "timed_out"}` {
		t.Errorf("output %s", r.Output)
	}
	if _, err := e.RetryRun(ctx, proj, run.ID, "test"); err == nil {
		t.Error("retried a completed run")
	}
	// Changing a step's kind under a running run is refused.
	run2 := start()
	waitRun(t, e.Engine, run2.ID, runCompleted, 15*time.Second)
	if _, err := e.Wait(ctx, proj, run2.ID, WaitRequest{Name: "one", Kind: stepSleep}); err == nil {
		t.Error("kind change accepted")
	}
	// A 2xx that isn't a workflow result fails the run with a hint.
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer plain.Close()
	run3, _, _ := e.StartRun(ctx, proj, StartRequest{Workflow: "x", URL: plain.URL + "/wf"})
	r = waitRun(t, e.Engine, run3.ID, runFailed, 10*time.Second)
	if !strings.Contains(r.Error, "workflow handler mounted") {
		t.Errorf("error %q", r.Error)
	}
	runs, _ := e.ListRuns(ctx, proj, RunFilter{State: runFailed})
	if len(runs) != 1 || runs[0].ID != run3.ID {
		t.Errorf("failed runs %+v", runs)
	}
}

func TestWebhookAndInternalAuth(t *testing.T) {
	e := newEngine(t, nil)
	sdk := newFakeSDK(t, e.Engine)
	app := httptest.NewServer(sdk.handler("", nil))
	defer app.Close()
	ctx := context.Background()
	hookURL := make(chan string, 1)
	sdk.defs["hooked"] = func(c *wctx) (any, error) {
		h := c.wait(WaitRequest{Name: "confirm", Kind: stepWebhook})
		var hk struct{ URL, Event string }
		_ = json.Unmarshal(h.Output, &hk)
		select {
		case hookURL <- hk.URL:
		default:
		}
		got := c.wait(WaitRequest{Name: "confirm:wait", Kind: stepEvent, Event: hk.Event})
		return got.Output, nil
	}
	run, _, _ := e.StartRun(ctx, proj, StartRequest{Workflow: "hooked", URL: app.URL + "/wf"})
	u := <-hookURL
	if !strings.HasPrefix(u, "https://dashboard.tiffin.localhost/v1/hooks/wh_") {
		t.Fatalf("hook url %s", u)
	}
	waitRun(t, e.Engine, run.ID, runWaiting, 10*time.Second)
	// The public endpoint needs no key: the token is the secret.
	path := strings.TrimPrefix(u, "https://dashboard.tiffin.localhost")
	res, err := http.Post(sdk.box.URL+path, "text/plain", strings.NewReader("confirmed=yes"))
	if err != nil || res.StatusCode != 202 {
		t.Fatalf("hook %v %v", res, err)
	}
	r := waitRun(t, e.Engine, run.ID, runCompleted, 10*time.Second)
	if !strings.Contains(string(r.Output), "confirmed=yes") {
		t.Errorf("output %s", r.Output)
	}
	if res, _ := http.Post(sdk.box.URL+"/v1/hooks/wh_nope", "application/json", nil); res.StatusCode != 404 {
		t.Errorf("unknown hook: %d", res.StatusCode)
	}

	// App key auth.
	req, _ := http.NewRequest("POST", sdk.box.URL+"/v1/queue-internal/send", strings.NewReader(`{"name":"q"}`))
	req.Header.Set("Authorization", "Bearer tqk_shop_wrong")
	if res, _ := http.DefaultClient.Do(req); res.StatusCode != 401 {
		t.Errorf("wrong key: %d", res.StatusCode)
	}
	var sent SendResult
	if code := sdk.call("POST", "/v1/queue-internal/send", map[string]any{"name": "emails", "payload": map[string]int{"n": 1}, "fromApp": "web", "delaySeconds": 3600}, &sent); code != 200 || len(sent.Jobs) != 1 {
		t.Fatalf("internal send %d %+v", code, sent)
	}
	j := (&testEngine{Engine: e.Engine, t: t}).job(proj, sent.Jobs[0])
	if j.Target != "web:/queues/emails" || j.State != stateScheduled || j.EnqueuedBy != "app:"+proj+"/web" {
		t.Errorf("job %+v", j)
	}
	// An app's own key names it, whatever the request claims; a key for an
	// app that was not derived from the project's key is refused.
	projectKey := sdk.key
	sdk.key = AppKey(projectKey, proj, "worker")
	if code := sdk.call("POST", "/v1/queue-internal/send", map[string]any{"name": "emails", "fromApp": "web", "delaySeconds": 3600}, &sent); code != 200 {
		t.Fatalf("send with an app key: %d", code)
	}
	if j := (&testEngine{Engine: e.Engine, t: t}).job(proj, sent.Jobs[0]); j.EnqueuedBy != "app:"+proj+"/worker" {
		t.Errorf("sent with worker's key: enqueuedBy %q", j.EnqueuedBy)
	}
	sdk.key = "tqk_" + proj + "_worker_" + strings.Repeat("0", 32)
	if code := sdk.call("POST", "/v1/queue-internal/send", map[string]any{"name": "emails"}, &sent); code != 401 {
		t.Errorf("forged app key: %d", code)
	}
	sdk.key = projectKey
	var prob map[string]any
	if code := sdk.call("POST", "/v1/queue-internal/jobs/"+sent.Jobs[0]+"/heartbeat", map[string]int{"attemptId": 1}, &prob); code != 409 || prob["code"] != "conflict" {
		t.Errorf("heartbeat on a waiting job: %d %v", code, prob)
	}
	if code := sdk.call("POST", "/v1/queue-internal/send", map[string]any{"name": "Bad Name"}, &prob); code != 422 {
		t.Errorf("bad name: %d", code)
	}
}
