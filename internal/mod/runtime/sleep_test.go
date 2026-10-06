package runtime

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/manifest"
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
	// Routes stay: the switchboard holds the request and starts the app.
	runs := h.eng.runs
	code, body := h.get("shop.tiffin.localhost", "/api/hello")
	if code != 200 || !strings.Contains(body, strings.ToLower(d.ID)) {
		t.Fatalf("wake on request: %d %s", code, body)
	}
	st = h.state("api", "")
	if st.Sleeping || len(st.Instances) != 2 || h.eng.runs != runs+2 || st.SleptAt != nil {
		t.Fatalf("woke wrong: %+v runs %d → %d", st, runs, h.eng.runs)
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
