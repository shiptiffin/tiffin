//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// The app for TestLive: a route starts a workflow (as a server action would)
// and returns { id, token }; the workflow reports progress and output in
// three steps with a sleep before the last. A queue job counts with 20
// progress updates.
const liveApp = `import { defineHandler, queue, workflow } from "./tiffin-sdk.gen.js";

async function part(ctx: any, n: number) {
  await ctx.step("part " + n, async () => {
    await Bun.sleep(400);
    await ctx.progress({ step: n, at: Date.now() });
    await ctx.stream({ line: n, at: Date.now() });
    return n;
  });
}
workflow.define("report", async (ctx) => {
  await part(ctx, 1);
  await part(ctx, 2);
  await ctx.sleep("pause", "5s");
  await part(ctx, 3);
  return { rows: 3 };
});
const count = defineHandler<{ n: number; gap: number }>(async (job) => {
  for (let i = 1; i <= job.payload.n; i++) {
    await Bun.sleep(job.payload.gap);
    await job.progress({ i, at: Date.now() });
  }
  return { counted: job.payload.n };
});
const turns = workflow.handler();
Bun.serve({
  port: Number(process.env.PORT ?? 3000),
  async fetch(req) {
    const { pathname } = new URL(req.url);
    if (pathname === workflow.DEFAULT_PATH) return turns(req);
    if (pathname === "/queues/count") return count(req);
    if (pathname === "/start" && req.method === "POST") return Response.json(await workflow.startWithToken("report", {}));
    if (pathname === "/count" && req.method === "POST") return Response.json(await queue.sendWithToken("count", { n: 20, gap: 150 }));
    if (pathname === "/now") return Response.json({ now: Date.now() });
    if (pathname.startsWith("/_tiffin/")) return new Response("the app saw it", { status: 418 });
    return new Response("ok");
  },
});
`

type liveEvent struct {
	id, event, data string
	at              time.Time
}

// openLive opens a stream; events arrive on the channel until it ends.
func openLive(t *testing.T, c *http.Client, url, token, lastID string) (int, string, <-chan liveEvent, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	res, err := c.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("GET %s: %v", url, err)
	}
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		cancel()
		return res.StatusCode, string(b), nil, cancel
	}
	ch := make(chan liveEvent, 1000)
	go func() {
		defer res.Body.Close()
		defer close(ch)
		sc := bufio.NewScanner(res.Body)
		var ev liveEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if ev.event != "" {
					ev.at = time.Now()
					ch <- ev
				}
				ev = liveEvent{}
			case strings.HasPrefix(line, "id: "):
				ev.id = line[4:]
			case strings.HasPrefix(line, "event: "):
				ev.event = line[7:]
			case strings.HasPrefix(line, "data: "):
				ev.data = line[6:]
			}
		}
	}()
	return 200, "", ch, cancel
}

func nextLive(t *testing.T, ch <-chan liveEvent, d time.Duration) (liveEvent, bool) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		return ev, ok
	case <-time.After(d):
		t.Fatalf("no event in %s", d)
	}
	return liveEvent{}, false
}

// TestLive: live progress from the box on the app's own host, end to end.
//
//   - a route starts a workflow and returns { id, token }
//   - an SSE client gets the replay and every update in order
//   - it drops mid-run and resumes with Last-Event-ID: nothing repeated or lost
//   - it sees the completion; a token for another run is refused
//   - a static site's host answers the same path; the path never reaches the app
//   - update latency (the app's progress call → the browser), measured
func TestLive(t *testing.T) {
	b := newCLIBox(t, "live", "live")
	dir := filepath.Join(b.dir, "live")
	cfg := `{"project":"live","apps":{"web":{"path":"web"},"site":{"path":"site","framework":"static","routes":["live-site"]}}}`
	sdk, err := os.ReadFile(filepath.Join(RepoRoot(), "templates", "queues-worker", "tiffin-sdk.gen.js"))
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"tiffin.config.json":    cfg,
		"web/package.json":      `{"name":"live","private":true,"type":"module","scripts":{"start":"bun index.ts"}}`,
		"web/index.ts":          liveApp,
		"web/tiffin-sdk.gen.js": string(sdk),
		"site/index.html":       "<h1>site</h1>",
	} {
		p := filepath.Join(dir, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b.apply("live", cfg)
	b.waitReady("app/web", "app/site")
	cmd := exec.Command(b.cli, "deploy", dir)
	cmd.Env, cmd.Dir = b.env, dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	c := b.https()
	c.Timeout = 0
	web := b.url("live")
	post := func(path string) (string, string) {
		t.Helper()
		code, _, body := b.get(c, "POST", web+path, nil)
		var r struct{ ID, Token string }
		if code != 200 || json.Unmarshal([]byte(body), &r) != nil || r.ID == "" || r.Token == "" {
			t.Fatalf("POST %s: %d %s", path, code, body)
		}
		return r.ID, r.Token
	}
	// The VM's clock against ours, from the round trip with the smallest delay.
	var offset, best time.Duration = 0, time.Hour
	for i := 0; i < 10; i++ {
		t0 := time.Now()
		_, _, body := b.get(c, "GET", web+"/now", nil)
		t1 := time.Now()
		var r struct{ Now int64 }
		_ = json.Unmarshal([]byte(body), &r)
		if rtt := t1.Sub(t0); rtt < best && r.Now > 0 {
			best = rtt
			offset = time.UnixMilli(r.Now).Sub(t0.Add(rtt / 2))
		}
	}
	t.Logf("clock offset to the box %s (round trip %s)", offset.Round(time.Millisecond), best.Round(time.Millisecond))
	var lat []time.Duration
	// Only live updates count, not the replay a connection starts with.
	latency := func(ev liveEvent) {
		var st struct{ Progress map[string]float64 }
		_ = json.Unmarshal([]byte(ev.data), &st)
		if at := st.Progress["at"]; at > 0 {
			lat = append(lat, ev.at.Sub(time.UnixMilli(int64(at)).Add(-offset)))
		}
	}

	runID, token := post("/start")
	_, otherToken := post("/start")
	events := func(id string) string { return web + "/_tiffin/runs/" + id + "/events" }

	// Refused: another run's token, a forged one. The app (418) never sees the path.
	if code, body, _, _ := openLive(t, c, events(runID), otherToken, ""); code != 403 || !strings.Contains(body, "forbidden") {
		t.Fatalf("another run's token: %d %s", code, body)
	}
	if code, body, _, _ := openLive(t, c, events(runID), token+"0", ""); code != 401 || strings.Contains(body, "the app saw it") {
		t.Fatalf("forged token: %d %s", code, body)
	}

	// Connection 1: replay, then live updates; drop after line 2.
	code, body, ch, stop := openLive(t, c, events(runID), token, "")
	if code != 200 {
		t.Fatalf("stream: %d %s", code, body)
	}
	var lines []float64
	var steps []float64
	lastID := ""
	replay := true // until a connection's first state event
	seen := func(ev liveEvent) {
		switch ev.event {
		case "output":
			var o struct{ Line float64 }
			_ = json.Unmarshal([]byte(ev.data), &o)
			lines = append(lines, o.Line)
			lastID = ev.id
		case "state":
			var st struct {
				Progress struct{ Step float64 }
			}
			_ = json.Unmarshal([]byte(ev.data), &st)
			if n := len(steps); st.Progress.Step > 0 && (n == 0 || steps[n-1] != st.Progress.Step) {
				steps = append(steps, st.Progress.Step)
				if !replay {
					latency(ev)
				}
			}
			replay = false
		}
	}
	first := true
	for len(lines) < 2 {
		ev, ok := nextLive(t, ch, 30*time.Second)
		if !ok {
			t.Fatal("stream ended early")
		}
		if first && ev.event != "state" {
			t.Fatalf("the replay starts with the state: %+v", ev)
		}
		first = false
		seen(ev)
	}
	stop()
	t.Logf("connection 1: lines %v, progress steps %v, dropped at Last-Event-ID %s", lines, steps, lastID)

	// Connection 2 resumes mid-run: no line again, the rest, then the end.
	time.Sleep(time.Second)
	code, body, ch, stop = openLive(t, c, events(runID), token, lastID)
	defer stop()
	replay = true
	if code != 200 {
		t.Fatalf("resume: %d %s", code, body)
	}
	var final struct {
		Status string
		Done   bool
		Output map[string]float64
		Steps  []struct{ Name, State string }
	}
	ended := false
	resumeFirst := ""
	for !ended {
		ev, ok := nextLive(t, ch, 60*time.Second)
		if !ok {
			t.Fatal("stream ended without an end event")
		}
		if resumeFirst == "" {
			resumeFirst = ev.event
			var st struct{ Status string }
			_ = json.Unmarshal([]byte(ev.data), &st)
			t.Logf("resumed: first event %s (status %s)", ev.event, st.Status)
		}
		seen(ev)
		if ev.event == "state" {
			_ = json.Unmarshal([]byte(ev.data), &final)
		}
		ended = ev.event == "end"
	}
	if resumeFirst != "state" || fmt.Sprint(lines) != "[1 2 3]" || fmt.Sprint(steps) != "[1 2 3]" {
		t.Fatalf("resume: first %s, lines %v, steps %v", resumeFirst, lines, steps)
	}
	if final.Status != "completed" || !final.Done || final.Output["rows"] != 3 || len(final.Steps) != 4 {
		t.Fatalf("final state: %+v", final)
	}
	if _, open := <-ch; open {
		t.Fatal("the stream stayed open after end")
	}
	t.Logf("connection 2: lines %v, progress steps %v, final %s with %d steps", lines, steps, final.Status, len(final.Steps))

	// A job: 20 progress updates 150 ms apart, watched from a static site's host.
	jobID, jobToken := post("/count")
	code, body, ch, stop = openLive(t, c, b.url("live-site")+"/_tiffin/runs/"+jobID+"/events", jobToken, "")
	defer stop()
	if code != 200 {
		t.Fatalf("job stream on the static host: %d %s", code, body)
	}
	var counts []float64
	var jobDone string
	jobReplay := true
	for {
		ev, ok := nextLive(t, ch, 60*time.Second)
		if !ok || ev.event == "end" {
			break
		}
		var st struct {
			Status   string
			Progress struct{ I float64 }
		}
		_ = json.Unmarshal([]byte(ev.data), &st)
		jobDone = st.Status
		if n := len(counts); st.Progress.I > 0 && (n == 0 || counts[n-1] != st.Progress.I) {
			if n > 0 && st.Progress.I < counts[n-1] {
				t.Fatalf("progress went back: %v then %v", counts, st.Progress.I)
			}
			counts = append(counts, st.Progress.I)
			if !jobReplay {
				latency(ev)
			}
		}
		if ev.event == "state" {
			jobReplay = false
		}
	}
	if jobDone != "completed" || len(counts) < 18 || counts[len(counts)-1] != 20 {
		t.Fatalf("job: %s, progress seen %v", jobDone, counts)
	}
	t.Logf("job %s: %d of 20 progress updates seen in order, then completed", jobID, len(counts))

	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	if len(lat) < 15 {
		t.Fatalf("latency samples: %v", lat)
	}
	pct := func(p float64) time.Duration { return lat[int(p*float64(len(lat)-1))].Round(100 * time.Microsecond) }
	t.Logf("update latency (app's progress call → event at the client, via the edge), %d samples: min %s, p50 %s, p90 %s, max %s",
		len(lat), lat[0].Round(100*time.Microsecond), pct(0.5), pct(0.9), lat[len(lat)-1].Round(100*time.Microsecond))
	if pct(0.5) > time.Second {
		t.Errorf("median update latency %s is over a second", pct(0.5))
	}
}
