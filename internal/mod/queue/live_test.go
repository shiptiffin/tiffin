package queue

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSubscribeTokenScopeAndExpiry(t *testing.T) {
	e := newEngine(t, nil)
	ctx := context.Background()
	_, secret, _ := e.cfg.Keys.Get(ctx, proj)
	now := time.Now()
	good := SubscribeToken(secret, proj, "run_A", now.Add(time.Hour))
	if p, err := e.checkToken(ctx, good, "run_A", now); err != nil || p != proj {
		t.Fatalf("valid token: %q %v", p, err)
	}
	// The same vector tiffin-sdk's test checks (packages/sdk/test/queue.test.ts).
	if v := SubscribeToken("tqs_test", "shop", "run_01ABC", time.Unix(1_900_000_000, 0)); v != "live1.shop.run_01ABC.1900000000.d5035e46a7cbe4b6929e0b3f051cbce30924bb8b9d52226f5261eb3edf358fa2" {
		t.Fatalf("token format %q", v)
	}
	_, other, _ := e.cfg.Keys.Get(ctx, "other")
	for name, c := range map[string]struct {
		token, id string
		status    int
	}{
		"another run":     {good, "run_B", 403},
		"expired":         {SubscribeToken(secret, proj, "run_A", now.Add(-time.Second)), "run_A", 401},
		"too long":        {SubscribeToken(secret, proj, "run_A", now.Add(30*24*time.Hour)), "run_A", 401},
		"tampered":        {strings.Replace(good, "run_A", "run_B", 1), "run_B", 401},
		"other project":   {SubscribeToken(other, proj, "run_A", now.Add(time.Hour)), "run_A", 401},
		"project swapped": {strings.Replace(SubscribeToken(other, "other", "run_A", now.Add(time.Hour)), "other", proj, 1), "run_A", 401},
		"garbage":         {"Bearer x", "run_A", 401},
		"empty":           {"", "run_A", 401},
	} {
		_, err := e.checkToken(ctx, c.token, c.id, now)
		qe, ok := err.(*Error)
		if !ok || qe.Status != c.status {
			t.Errorf("%s: %v, want %d", name, err, c.status)
		}
	}
}

// sseEvent is one server-sent event.
type sseEvent struct {
	ID, Event, Data string
	At              time.Time
}

// sseStream reads events from GET /_tiffin/runs/{id}/events.
type sseStream struct {
	Status int
	Body   string // error responses
	Events chan sseEvent
	cancel context.CancelFunc
}

func openSSE(t testing.TB, base, id, token, lastID string) *sseStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", base+LivePath+id+"/events", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	s := &sseStream{Status: res.StatusCode, Events: make(chan sseEvent, 1000), cancel: cancel}
	t.Cleanup(s.Close)
	if res.StatusCode != 200 {
		var b strings.Builder
		_, _ = bufio.NewReader(res.Body).WriteTo(&b)
		res.Body.Close()
		s.Body = b.String()
		close(s.Events)
		return s
	}
	go func() {
		defer res.Body.Close()
		defer close(s.Events)
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 1<<20), 4<<20)
		var ev sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if ev.Event != "" || ev.Data != "" {
					ev.At = time.Now()
					s.Events <- ev
				}
				ev = sseEvent{}
			case strings.HasPrefix(line, "id: "):
				ev.ID = line[4:]
			case strings.HasPrefix(line, "event: "):
				ev.Event = line[7:]
			case strings.HasPrefix(line, "data: "):
				ev.Data = line[6:]
			}
		}
	}()
	return s
}

func (s *sseStream) Close() { s.cancel() }

// next waits for the next event of a kind ("" any), skipping others.
func (s *sseStream) next(t testing.TB, kind string) sseEvent {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-s.Events:
			if !ok {
				t.Fatalf("stream ended waiting for %q", kind)
			}
			if kind == "" || ev.Event == kind {
				return ev
			}
		case <-timeout:
			t.Fatalf("no %q event in 10s", kind)
		}
	}
}

func liveState(t testing.TB, ev sseEvent) LiveState {
	t.Helper()
	var st LiveState
	if err := json.Unmarshal([]byte(ev.Data), &st); err != nil {
		t.Fatalf("state %q: %v", ev.Data, err)
	}
	return st
}

// A job reports progress and output while it runs; two browsers watch it
// live (one LISTEN connection serves both), a third joins mid-way with
// Last-Event-ID and gets only what it missed, and every stream ends when the
// job completes.
func TestLiveJobReplayResumeAndFanOut(t *testing.T) {
	e := newEngine(t, nil)
	ctx := context.Background()
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	t.Cleanup(finish)
	a := newApp(t, e.Engine, proj)
	a.handle("/q/report", func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"rows":3}`))
	})
	e.configure(proj, QueueConfig{Name: "report", URL: a.url("/q/report"), LeaseS: 60})
	id := e.send(proj, SendRequest{Name: "report", By: "test"}).Jobs[0]
	n, _ := ParseJobID(id)
	e.waitState(proj, id, stateRunning, 10*time.Second)
	srv := httptest.NewServer(http.HandlerFunc(e.ServeLive))
	t.Cleanup(srv.Close)
	_, secret, _ := e.cfg.Keys.Get(ctx, proj)
	token := SubscribeToken(secret, proj, id, time.Now().Add(time.Minute))

	if err := e.JobProgress(ctx, proj, n, 0, json.RawMessage(`{"pct":10}`)); err != nil {
		t.Fatal(err)
	}
	c1, err := e.JobOutput(ctx, proj, n, 0, json.RawMessage(`"line 1"`))
	if err != nil {
		t.Fatal(err)
	}
	// Replay: the chunk so far, then the state with the latest progress.
	s1 := openSSE(t, srv.URL, id, token, "")
	s2 := openSSE(t, srv.URL, id, "", "")
	if s2.Status != 401 || !strings.Contains(s2.Body, "unauthenticated") {
		t.Fatalf("no token: %d %s", s2.Status, s2.Body)
	}
	s2 = openSSE(t, srv.URL, id, token, "")
	for _, s := range []*sseStream{s1, s2} {
		if ev := s.next(t, ""); ev.Event != "output" || ev.Data != `"line 1"` || ev.ID != strconv.FormatInt(c1, 10) {
			t.Fatalf("replay chunk: %+v", ev)
		}
		ev := s.next(t, "")
		if st := liveState(t, ev); ev.Event != "state" || st.Status != stateRunning || string(st.Progress) != `{"pct":10}` || st.Done || st.Type != "job" || st.Name != "report" {
			t.Fatalf("replay state: %+v", ev)
		}
	}
	listeners := func() (n int) {
		_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND query LIKE 'LISTEN%'`).Scan(&n)
		return n
	}
	eventually(t, 5*time.Second, "the listener", func() bool { return listeners() == 1 })

	// Live: both streams get each change, in order.
	sent := time.Now()
	if err := e.JobProgress(ctx, proj, n, 0, json.RawMessage(`{"pct":50}`)); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*sseStream{s1, s2} {
		ev := s.next(t, "state")
		if st := liveState(t, ev); string(st.Progress) != `{"pct":50}` {
			t.Fatalf("live progress: %s", ev.Data)
		}
		t.Logf("progress update reached a stream in %s", ev.At.Sub(sent).Round(100*time.Microsecond))
	}
	c2, _ := e.JobOutput(ctx, proj, n, 0, json.RawMessage(`"line 2"`))
	for _, s := range []*sseStream{s1, s2} {
		if ev := s.next(t, "output"); ev.Data != `"line 2"` || ev.ID != strconv.FormatInt(c2, 10) {
			t.Fatalf("live chunk: %+v", ev)
		}
	}
	// Resume: a stream that saw chunk 1 gets chunk 2 and the current state, not chunk 1 again.
	s3 := openSSE(t, srv.URL, id, token, strconv.FormatInt(c1, 10))
	if ev := s3.next(t, ""); ev.Event != "output" || ev.Data != `"line 2"` {
		t.Fatalf("resume: %+v", ev)
	}
	if ev := s3.next(t, ""); ev.Event != "state" || ev.ID != strconv.FormatInt(c2, 10) || !strings.Contains(ev.Data, `"pct":50`) {
		t.Fatalf("resume state: %+v", ev)
	}

	finish()
	for _, s := range []*sseStream{s1, s2, s3} {
		ev := s.next(t, "state")
		st := liveState(t, ev)
		if st.Status != stateCompleted || !st.Done || !strings.Contains(string(st.Output), `"rows"`) || string(st.Progress) != `{"pct":50}` {
			t.Fatalf("final state: %s", ev.Data)
		}
		if ev := s.next(t, ""); ev.Event != "end" || !strings.Contains(ev.Data, "completed") {
			t.Fatalf("end: %+v", ev)
		}
		if _, open := <-s.Events; open {
			t.Fatal("stream stayed open after end")
		}
	}
	// A finished job: progress and output are refused; a late stream replays and ends.
	if err := e.JobProgress(ctx, proj, n, 0, json.RawMessage(`1`)); err == nil {
		t.Fatal("progress after the job finished was accepted")
	}
	s4 := openSSE(t, srv.URL, id, token, "")
	var kinds []string
	for ev := range s4.Events {
		kinds = append(kinds, ev.Event)
	}
	if strings.Join(kinds, ",") != "output,output,state,end" {
		t.Fatalf("late stream: %v", kinds)
	}
	// The wrong job's token, an unknown job, a bad path.
	if s := openSSE(t, srv.URL, "job_999999", token, ""); s.Status != 403 {
		t.Fatalf("wrong job token: %d %s", s.Status, s.Body)
	}
	other := SubscribeToken(secret, proj, "job_999999", time.Now().Add(time.Minute))
	if s := openSSE(t, srv.URL, "job_999999", other, ""); s.Status != 404 {
		t.Fatalf("unknown job: %d %s", s.Status, s.Body)
	}
	if s := openSSE(t, srv.URL, "../x", token, ""); s.Status != 404 {
		t.Fatalf("bad path: %d", s.Status)
	}
}

func TestLiveRunStepsProgressAndLimits(t *testing.T) {
	var appURL string
	e := newEngine(t, func(c *Config) {
		c.Endpoint = func(context.Context, string, string, string) (string, error) { return appURL, nil }
	})
	ctx := context.Background()
	sdk := newFakeSDK(t, e.Engine)
	gate := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(gate) }) }
	sdk.defs["report"] = func(c *wctx) (any, error) {
		if _, err := c.step("load", func() (any, error) { return 1, nil }); err != nil {
			return nil, err
		}
		<-gate
		return map[string]int{"rows": 3}, nil
	}
	app := httptest.NewServer(sdk.handler("r1", nil))
	t.Cleanup(app.Close)
	t.Cleanup(open) // before app.Close: a turn waits on the gate
	appURL = app.URL
	run := sdk.start("report", nil, "", "")
	srv := httptest.NewServer(http.HandlerFunc(e.ServeLive))
	t.Cleanup(srv.Close)
	_, secret, _ := e.cfg.Keys.Get(ctx, proj)
	token := SubscribeToken(secret, proj, run.ID, time.Now().Add(time.Minute))

	eventually(t, 10*time.Second, "the step", func() bool {
		r, _ := e.GetRun(ctx, proj, run.ID, false)
		return len(r.Steps) == 1
	})
	if code := sdk.call("POST", "/v1/queue-internal/workflows/runs/"+run.ID+"/progress", map[string]any{"progress": map[string]int{"done": 1}}, nil); code != 200 {
		t.Fatalf("progress: %d", code)
	}
	s := openSSE(t, srv.URL, run.ID, token, "")
	st := liveState(t, s.next(t, "state"))
	if st.Type != "run" || st.Name != "report" || st.Status != runRunning || string(st.Progress) != `{"done":1}` || len(st.Steps) != 1 || st.Steps[0].Name != "load" {
		t.Fatalf("run state: %+v", st)
	}
	var out struct{ ID int64 }
	if code := sdk.call("POST", "/v1/queue-internal/workflows/runs/"+run.ID+"/output", map[string]any{"data": "half way"}, &out); code != 200 || out.ID == 0 {
		t.Fatalf("output: %d", code)
	}
	if ev := s.next(t, "output"); ev.Data != `"half way"` {
		t.Fatalf("chunk: %+v", ev)
	}
	// Limits: a progress value over 16 KB, a chunk over 64 KB.
	if code := sdk.call("POST", "/v1/queue-internal/workflows/runs/"+run.ID+"/progress", map[string]any{"progress": strings.Repeat("x", maxProgress)}, nil); code != 422 {
		t.Fatalf("big progress: %d", code)
	}
	if code := sdk.call("POST", "/v1/queue-internal/workflows/runs/"+run.ID+"/output", map[string]any{"data": strings.Repeat("x", maxChunk)}, nil); code != 422 {
		t.Fatalf("big chunk: %d", code)
	}
	open()
	final := liveState(t, s.next(t, "state"))
	for !final.Done {
		final = liveState(t, s.next(t, "state"))
	}
	if final.Status != runCompleted || !strings.Contains(string(final.Output), "rows") {
		t.Fatalf("final: %+v", final)
	}
	s.next(t, "end")
	// The run's progress is on the API too.
	r, _ := e.GetRun(ctx, proj, run.ID, false)
	if string(r.Progress) != `{"done": 1}` && string(r.Progress) != `{"done":1}` {
		t.Fatalf("api progress %s", r.Progress)
	}
	// Streams per project are bounded.
	old := maxLivePerProject
	maxLivePerProject = 1
	t.Cleanup(func() { maxLivePerProject = old })
	run2 := sdk.start("report", nil, "", "")
	tok2 := SubscribeToken(secret, proj, run2.ID, time.Now().Add(time.Minute))
	held := openSSE(t, srv.URL, run2.ID, tok2, "")
	held.next(t, "state")
	if s := openSSE(t, srv.URL, run2.ID, tok2, ""); s.Status != 429 {
		t.Fatalf("over the limit: %d %s", s.Status, s.Body)
	}
	held.Close()
	eventually(t, 5*time.Second, "the stream slot to free", func() bool {
		e.hub.mu.Lock()
		defer e.hub.mu.Unlock()
		return e.hub.perProject[proj] == 0
	})
}

func TestJobOutputCap(t *testing.T) {
	e := newEngine(t, nil)
	ctx := context.Background()
	release := make(chan struct{})
	a := newApp(t, e.Engine, proj)
	a.handle("/q/big", func(w http.ResponseWriter, r *http.Request) { <-release })
	t.Cleanup(func() { close(release) })
	e.configure(proj, QueueConfig{Name: "big", URL: a.url("/q/big")})
	id := e.send(proj, SendRequest{Name: "big", By: "test"}).Jobs[0]
	n, _ := ParseJobID(id)
	e.waitState(proj, id, stateRunning, 10*time.Second)
	chunk := json.RawMessage(`"` + strings.Repeat("x", 60<<10) + `"`)
	var err error
	sent := 0
	for ; sent < 100 && err == nil; sent++ {
		_, err = e.JobOutput(ctx, proj, n, 0, chunk)
	}
	if qe, ok := err.(*Error); !ok || qe.Status != 413 || sent != 18 {
		t.Fatalf("after %d chunks: %v", sent, err)
	}
	var count int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM tq_output WHERE job_id = $1`, n).Scan(&count)
	if count != 17 {
		t.Fatalf("%d chunks stored, want 17 (1 MB)", count)
	}
	if err := e.JobProgress(ctx, proj, n, 99, json.RawMessage(`1`)); err == nil {
		t.Fatal("progress for another attempt was accepted")
	}
}
