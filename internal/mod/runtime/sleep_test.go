package runtime

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/manifest"
)

// sleepy turns on sleepAfter for the harness project.
func (h *harness) sleepy(after string) {
	h.t.Helper()
	h.mf.SleepAfter = after
	h.apply()
}

// idle makes production of app look unused for d.
func (h *harness) idle(app string, d time.Duration) {
	h.r.mu.Lock()
	h.r.lastSeen[envKey("shop", app, "")] = time.Now().Add(-d)
	h.r.mu.Unlock()
}

func (h *harness) runningOf(app string) int {
	n := 0
	for _, c := range h.eng.running() {
		if strings.HasPrefix(c.spec.Name, "tf.shop."+app+".prod.") {
			n++
		}
	}
	return n
}

// keptOf counts app's production containers, running or stopped.
func (h *harness) keptOf(app string) int {
	h.eng.mu.Lock()
	defer h.eng.mu.Unlock()
	n := 0
	for name := range h.eng.ctrs {
		if strings.HasPrefix(name, "tf.shop."+app+".prod.") {
			n++
		}
	}
	return n
}

// asleep puts app's production to sleep now.
func (h *harness) asleep(app string) *AppState {
	h.t.Helper()
	h.r.sleep(context.Background(), "shop", app, "", 0)
	st := h.state(app, "")
	if !st.Sleeping || len(st.Parked) == 0 {
		h.t.Fatalf("not asleep with kept containers: %+v", st)
	}
	return st
}

// A wake after a config change creates new containers and removes the
// kept ones; so do a deploy, a rollback and a stop while asleep.
func TestKeptContainersGoWhenTheyNoLongerFit(t *testing.T) {
	h := newHarness(t)
	v1 := h.deploy("api", "", map[string]string{"index.ts": "v1"})
	h.sleepy("1h")
	ctx := context.Background()

	// A changed env: the wake starts new containers with it.
	h.asleep("api")
	app := h.mf.Apps["api"]
	app.Env = map[string]string{"GREETING": "hello again"}
	h.mf.Apps["api"] = app
	h.apply()
	runs := h.eng.runs
	if _, body := h.get("shop.tiffin.localhost", "/api/x"); !strings.Contains(body, "greeting=hello again") {
		t.Fatalf("woke with the old env: %s", body)
	}
	if h.eng.runs != runs+2 {
		t.Fatalf("runs %d → %d: want new containers", runs, h.eng.runs)
	}
	waitFor(t, func() bool { return h.keptOf("api") == 2 })

	// Unchanged config: the next wake reuses them.
	h.asleep("api")
	runs = h.eng.runs
	if code, _ := h.get("shop.tiffin.localhost", "/api/x"); code != 200 || h.eng.runs != runs {
		t.Fatalf("wake made new containers (%d → %d)", runs, h.eng.runs)
	}

	// A deploy while asleep.
	h.asleep("api")
	v2 := h.deploy("api", "", map[string]string{"index.ts": "v2"})
	if st := h.state("api", ""); v2.Status != StatusLive || st.Sleeping || len(st.Parked) != 0 {
		t.Fatalf("deploy while asleep: %s %+v", v2.Status, st)
	}
	waitFor(t, func() bool { return h.keptOf("api") == 2 })

	// A rollback while asleep.
	h.asleep("api")
	if _, err := h.r.rollback(ctx, "shop", "api", v1.ID); err != nil {
		t.Fatal(err)
	}
	if st := h.state("api", ""); st.Live != v1.ID || st.Sleeping || len(st.Parked) != 0 {
		t.Fatalf("rollback while asleep: %+v", st)
	}
	waitFor(t, func() bool { return h.keptOf("api") == 2 })

	// A stop while asleep removes them and frees their ports.
	st := h.asleep("api")
	if err := h.r.stopEnv(ctx, st); err != nil {
		t.Fatal(err)
	}
	if n := h.keptOf("api"); n != 0 {
		t.Fatalf("%d container(s) left after a stop", n)
	}
	for _, in := range st.Parked {
		if h.r.ownedNames()[in.Name] != 0 {
			t.Fatalf("port of %s still held", in.Name)
		}
	}
}

// A kept container that will not start is replaced on the wake.
func TestWakeReplacesAKeptContainerThatWillNotStart(t *testing.T) {
	h := newHarness(t)
	h.deploy("api", "", map[string]string{"index.ts": "v1"})
	h.sleepy("1h")
	st := h.asleep("api")
	// Removed behind the runtime's back.
	if err := h.eng.Remove(context.Background(), st.Parked[0].Name, time.Second); err != nil {
		t.Fatal(err)
	}
	runs := h.eng.runs
	if code, body := h.get("shop.tiffin.localhost", "/api/x"); code != 200 {
		t.Fatalf("wake: %d %s", code, body)
	}
	if st := h.state("api", ""); st.Sleeping || len(st.Instances) != 2 || h.eng.runs != runs+2 {
		t.Fatalf("not replaced: %+v runs %d → %d", st, runs, h.eng.runs)
	}
	waitFor(t, func() bool { return h.keptOf("api") == 2 })
}

// Kept containers survive a restart of the runtime: their ports stay held
// and the leftover sweep leaves them alone.
func TestKeptContainersSurviveRestart(t *testing.T) {
	h := newHarness(t)
	h.deploy("api", "", map[string]string{"index.ts": "v1"})
	h.sleepy("1h")
	st := h.asleep("api")
	// A restart: the port table is rebuilt from the stored states.
	h.r.mu.Lock()
	h.r.ports = map[int]string{}
	h.r.mu.Unlock()
	if err := h.r.recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.r.removeOrphans(context.Background())
	if n := h.keptOf("api"); n != 2 {
		t.Fatalf("leftover sweep removed kept containers: %d left", n)
	}
	owned := h.r.ownedNames()
	for _, in := range st.Parked {
		if owned[in.Name] != in.Port {
			t.Fatalf("port of %s not held after a restart", in.Name)
		}
	}
	runs := h.eng.runs
	if code, _ := h.get("shop.tiffin.localhost", "/api/x"); code != 200 || h.eng.runs != runs {
		t.Fatalf("wake after restart: %d, runs %d → %d", code, runs, h.eng.runs)
	}
}

func TestProductionNeverSleepsByDefault(t *testing.T) {
	h := newHarness(t)
	h.deploy("api", "", map[string]string{"index.ts": "v1"})
	h.idle("api", 365*24*time.Hour)
	h.r.sleepIdle(context.Background())
	if st := h.state("api", ""); st.Sleeping || len(st.Instances) != 2 {
		t.Fatalf("production slept without sleepAfter: %+v", st)
	}
}

func TestProductionSleepsAndWakesOnRequest(t *testing.T) {
	h := newHarness(t)
	d := h.deploy("api", "", map[string]string{"index.ts": "v1"})
	h.sleepy("24h")
	ctx := context.Background()

	// Used within the time: stays up.
	h.idle("api", 23*time.Hour)
	h.r.sleepIdle(ctx)
	if h.state("api", "").Sleeping {
		t.Fatal("slept before its time")
	}
	h.idle("api", 25*time.Hour)
	h.r.sleepIdle(ctx)
	st := h.state("api", "")
	if !st.Sleeping || len(st.Instances) != 0 || st.SleptAt == nil || h.runningOf("api") != 0 {
		t.Fatalf("not asleep: %+v (running %d)", st, h.runningOf("api"))
	}
	// Stopped, not removed: kept for the wake.
	if len(st.Parked) != 2 || h.keptOf("api") != 2 {
		t.Fatalf("containers not kept: %+v (kept %d)", st.Parked, h.keptOf("api"))
	}
	// Routes stay: the switchboard holds the request and starts the app.
	runs, starts := h.eng.runs, h.eng.starts
	code, body := h.get("shop.tiffin.localhost", "/api/hello")
	if code != 200 || !strings.Contains(body, strings.ToLower(d.ID)) {
		t.Fatalf("wake on request: %d %s", code, body)
	}
	st = h.state("api", "")
	if st.Sleeping || len(st.Instances) != 2 || h.eng.runs != runs || h.eng.starts != starts+2 || st.SleptAt != nil || len(st.Parked) != 0 {
		t.Fatalf("woke wrong: %+v runs %d → %d, starts %d → %d", st, runs, h.eng.runs, starts, h.eng.starts)
	}
	// The first byte is noted once the handler returns, which may be after the client read the response.
	for deadline := time.Now().Add(2 * time.Second); st.LastWake != nil && st.LastWake.FirstByteSeconds == 0 && time.Now().Before(deadline); st = h.state("api", "") {
		time.Sleep(10 * time.Millisecond)
	}
	if w := st.LastWake; w == nil || w.Trigger != wakeRequest || w.FirstByteSeconds < w.StartSeconds {
		t.Fatalf("last wake %+v", st.LastWake)
	}
	rt, _ := h.r.appRuntime(ctx, "shop", "api")
	if rt.Prod.LastWake == nil || rt.Prod.LastActive == nil || time.Since(*rt.Prod.LastActive) > time.Minute {
		t.Fatalf("status %+v", rt.Prod)
	}
	// The request reset the clock.
	h.r.sleepIdle(ctx)
	if h.state("api", "").Sleeping {
		t.Fatal("slept right after a request")
	}
}

func TestDeliveryWakesSleepingApp(t *testing.T) {
	h := newHarness(t)
	h.deploy("jobs", "", map[string]string{"index.ts": "worker"})
	h.sleepy("1h")
	h.idle("jobs", 2*time.Hour)
	ctx := context.Background()
	h.r.sleepIdle(ctx)
	if !h.state("jobs", "").Sleeping {
		t.Fatal("worker did not sleep")
	}
	// The queue asks for an endpoint before each delivery: the app wakes
	// first, so the delivery is not spent on a stopped app.
	url, err := h.m.AppEndpoint(ctx, h.p, "shop", "jobs", "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.Get(url + "/job")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	st := h.state("jobs", "")
	if st.Sleeping || len(st.Instances) != 1 || st.LastWake == nil || st.LastWake.Trigger != wakeDelivery {
		t.Fatalf("delivery did not wake it: %+v", st)
	}
	// A delivery is activity.
	h.r.sleepIdle(ctx)
	if h.state("jobs", "").Sleeping {
		t.Fatal("slept right after a delivery")
	}
}

func TestWakeNowAndSettingOff(t *testing.T) {
	h := newHarness(t)
	h.deploy("api", "", map[string]string{"index.ts": "v1"})
	h.deploy("jobs", "", map[string]string{"index.ts": "worker"})
	h.sleepy("7d")
	h.idle("api", 8*24*time.Hour)
	h.idle("jobs", 8*24*time.Hour)
	ctx := context.Background()
	h.r.sleepIdle(ctx)
	if !h.state("api", "").Sleeping || !h.state("jobs", "").Sleeping {
		t.Fatal("not asleep")
	}
	out, err := h.r.wakeProject(ctx, "shop", "api")
	if err != nil || len(out) != 1 || !out[0].Woke || out[0].Wake == nil || out[0].Wake.Trigger != wakeNow {
		t.Fatalf("wake now: %+v %v", out, err)
	}
	if h.state("api", "").Sleeping || !h.state("jobs", "").Sleeping {
		t.Fatal("wake now woke the wrong apps")
	}
	// Turning sleep off wakes what sleeps.
	h.sleepy("")
	h.r.sleepIdle(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for h.state("jobs", "").Sleeping && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if st := h.state("jobs", ""); st.Sleeping || st.LastWake == nil || st.LastWake.Trigger != wakeSetting {
		t.Fatalf("not woken when sleep was turned off: %+v", st)
	}
}

func TestDeployWakesAndCountsAsActivity(t *testing.T) {
	h := newHarness(t)
	h.deploy("api", "", map[string]string{"index.ts": "v1"})
	h.sleepy("1h")
	h.idle("api", 2*time.Hour)
	h.r.sleepIdle(context.Background())
	if !h.state("api", "").Sleeping {
		t.Fatal("not asleep")
	}
	d := h.deploy("api", "", map[string]string{"index.ts": "v2"})
	st := h.state("api", "")
	if d.Status != StatusLive || st.Sleeping || len(st.Instances) != 2 {
		t.Fatalf("deploy to a sleeping app: %s %+v", d.Status, st)
	}
	h.r.sleepIdle(context.Background())
	if h.state("api", "").Sleeping {
		t.Fatal("slept right after a deploy")
	}
}

func TestActivitySurvivesRestart(t *testing.T) {
	h := newHarness(t)
	h.deploy("api", "", map[string]string{"index.ts": "v1"})
	h.sleepy("1h")
	ctx := context.Background()
	old := time.Now().Add(-50 * time.Minute).UTC().Truncate(time.Second)
	h.r.mu.Lock()
	h.r.lastSeen[envKey("shop", "api", "")] = old
	h.r.seenDirty[envKey("shop", "api", "")] = true
	h.r.mu.Unlock()
	h.r.saveActivity(ctx)

	// A restart: memory is gone, the saved clock comes back.
	h.r.mu.Lock()
	h.r.lastSeen = map[string]time.Time{}
	h.r.mu.Unlock()
	if err := h.r.loadActivity(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.r.lastActive(h.state("api", "")); !got.Equal(old) {
		t.Fatalf("clock after restart %v, want %v", got, old)
	}
	// It neither resets (still 10 minutes to go) nor skips.
	h.r.sleepIdle(ctx)
	if h.state("api", "").Sleeping {
		t.Fatal("slept early")
	}
	h.idle("api", 61*time.Minute)
	h.r.sleepIdle(ctx)
	if !h.state("api", "").Sleeping {
		t.Fatal("did not sleep on time")
	}
	// Activity of environments that are gone is forgotten.
	h.r.touch(envKey("shop", "gone", ""))
	h.r.saveActivity(ctx)
	if kv, _ := h.p.DB.KVList(ctx, nsActivity); kv[envKey("shop", "gone", "")] != nil || kv[envKey("shop", "api", "")] == nil {
		t.Fatalf("saved activity %v", kv)
	}
}

func TestParseSleepAfterBounds(t *testing.T) {
	for s, want := range map[string]time.Duration{"": 0, "1h": time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour} {
		if got, err := manifest.ParseSleepAfter(s); err != nil || got != want {
			t.Errorf("%q: %v %v", s, got, err)
		}
	}
	for _, s := range []string{"0h", "31d", "721h", "30m", "1w", "7"} {
		if _, err := manifest.ParseSleepAfter(s); err == nil {
			t.Errorf("%q parsed", s)
		}
	}
}

// A static preview counts its requests (the edge's access log) and
// expires like any other preview once nobody uses it.
func TestStaticPreviewsExpire(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	pv := h.deploy("site", "pr-1", map[string]string{"index.html": "<h1>preview</h1>"})
	if pv.Status != StatusLive || pv.StaticRoot == "" {
		t.Fatalf("preview: %s %s", pv.Status, pv.Error)
	}
	spec, _ := h.r.appSpec(ctx, "shop", "site")
	host := previewHost("pr-1", "shop", "site", spec, h.p.AppsDomain())
	h.r.opt.PreviewExpire = 50 * time.Millisecond
	time.Sleep(60 * time.Millisecond)
	h.m.NoteHostActivity(host, time.Now()) // someone looked at it
	h.r.expirePreviews(ctx)
	if h.state("site", "pr-1").Live != pv.ID {
		t.Fatal("a static preview used within PreviewExpire was deleted")
	}
	time.Sleep(60 * time.Millisecond)
	h.r.expirePreviews(ctx)
	if st := h.state("site", "pr-1"); st.Live != "" {
		t.Fatalf("unused static preview kept: %+v", st)
	}
	if code, _ := h.get(host, "/"); code != 404 {
		t.Fatalf("expired static preview still served: %d", code)
	}
}

// A request that slipped in just before the app fell asleep runs to its
// end (up to its time limit, not 5 seconds): the sleep is called off.
func TestSleepNeverCutsARequestUnderWay(t *testing.T) {
	h := newHarness(t)
	h.deploy("api", "", map[string]string{"index.ts": "v1"})
	h.sleepy("1h")
	done := make(chan string, 1)
	go func() {
		code, body := h.get("shop.tiffin.localhost", "/api/slow?sleep=6s")
		done <- fmt.Sprint(code, " ", body)
	}()
	waitFor(t, func() bool { return h.r.busy(h.state("api", "").Instances) > 0 })
	h.r.sleep(context.Background(), "shop", "api", "", 0)
	if got := <-done; !strings.HasPrefix(got, "200 ") {
		t.Fatalf("the request under way was cut: %s", got)
	}
	if st := h.state("api", ""); st.Sleeping || len(st.Instances) != 2 {
		t.Fatalf("an app busy with a request went to sleep: %+v", st)
	}
}
