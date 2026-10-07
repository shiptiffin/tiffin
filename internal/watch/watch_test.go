package watch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const beat = "0123456789abcdef0123"

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	for body, want := range map[string]string{
		`{"webhook":"https://h","boxes":[{"name":"a","url":"https://a"}]}`:                           "",
		`{"webhook":"https://h","boxes":[{"name":"a","heartbeat":"` + beat + `"}]}`:                  "listen",
		`{"webhook":"https://h","listen":":1","boxes":[{"name":"a","heartbeat":"short"}]}`:           "16+",
		`{"boxes":[{"name":"a","url":"https://a"}]}`:                                                 "webhook or command",
		`{"command":"true","boxes":[{"name":"a","url":"https://a"},{"name":"a","url":"https://b"}]}`: "own name",
		`{"command":"true","downAfter":"1s","boxes":[{"name":"a","url":"https://a"}]}`:               "10s",
		`{"command":"true","boxes":[{"name":"a","url":"https://a","extra":1}]}`:                      "unknown field",
	} {
		path := filepath.Join(dir, "w.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if (want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: %v, want %q", body, err, want)
		}
	}
}

// A box is down once health fails or heartbeats stop for downAfter, or as
// soon as it pings /fail; each change alerts once.
func TestWatch(t *testing.T) {
	var healthy atomic.Bool
	healthy.Store(true)
	box := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/health" || !healthy.Load() {
			http.Error(w, "down", http.StatusBadGateway)
		}
	}))
	defer box.Close()
	cfg := &Config{Command: "true", Listen: ":0", Boxes: []Box{{Name: "shop", URL: box.URL}, {Name: "blog", Heartbeat: beat}}}
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	w := New(cfg, io.Discard)
	now := time.Now()
	w.now = func() time.Time { return now }
	var mu sync.Mutex
	var got []string
	w.notify = func(_ context.Context, a Alert) error {
		mu.Lock()
		got = append(got, a.Box+" "+a.State+": "+a.Text)
		mu.Unlock()
		return nil
	}
	alerts := func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(got, "|")
	}
	collector := httptest.NewServer(w.Handler())
	defer collector.Close()
	ping := func(path string, body string) int {
		res, err := http.Post(collector.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	step := func(d time.Duration, want ...string) {
		t.Helper()
		now = now.Add(d)
		mu.Lock()
		got = nil
		mu.Unlock()
		w.Check(context.Background())
		if a := alerts(); a != strings.Join(want, "|") {
			t.Fatalf("alerts %q, want %q", a, want)
		}
	}

	step(time.Minute)
	if code := ping("/ping/"+beat, `{}`); code != 200 {
		t.Fatalf("ping: %d", code)
	}
	if ping("/ping/wrong-token-0000000000", `{}`) != 404 || ping("/ping/"+beat+"/other", `{}`) != 404 {
		t.Fatal("unknown pings must 404")
	}
	healthy.Store(false)
	step(4 * time.Minute) // minute 5: shop has failed since minute 1, not long enough
	ping("/ping/"+beat, `{}`)
	step(time.Minute, "shop down: Tiffin box shop is down: /v1/health has failed for 5m0s (/v1/health answered 502 Bad Gateway).")
	step(3 * time.Minute) // no repeats
	step(time.Minute, "blog down: Tiffin box blog is down: no heartbeat for 5m0s.")
	healthy.Store(true)
	ping("/ping/"+beat+"/start", ``)
	step(time.Minute, "shop up: Tiffin box shop is back up after 5m0s.", "blog up: Tiffin box blog is back up after 1m0s.")

	// A /fail ping alerts at once, with the failing checks; a good ping clears it.
	mu.Lock()
	got = nil
	mu.Unlock()
	ping("/ping/"+beat+"/fail", `{"status":"failing","failing":["postgres","disk"]}`)
	for i := 0; i < 100 && alerts() == ""; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if a := alerts(); a != "blog down: Tiffin box blog is down: it reports failing checks: disk, postgres." {
		t.Fatalf("fail ping: %q", a)
	}
	ping("/ping/"+beat, `{}`)
	step(0, "blog up: Tiffin box blog is back up after 0s.")
}

// A down alert that could not be delivered goes out at the next check,
// once; a box back before its alert got through sends nothing.
func TestUndeliveredAlertsAreRetried(t *testing.T) {
	var healthy atomic.Bool
	box := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !healthy.Load() {
			http.Error(w, "down", http.StatusBadGateway)
		}
	}))
	defer box.Close()
	cfg := &Config{Command: "true", Boxes: []Box{{Name: "shop", URL: box.URL}}}
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	w := New(cfg, io.Discard)
	now := time.Now()
	w.now = func() time.Time { return now }
	var delivered []string
	failing := true
	attempts := 0
	w.notify = func(_ context.Context, a Alert) error {
		attempts++
		if failing {
			return errors.New("webhook answered 503")
		}
		delivered = append(delivered, a.State)
		return nil
	}
	step := func(d time.Duration) {
		now = now.Add(d)
		w.Check(context.Background())
	}
	step(6 * time.Minute) // down; delivery fails
	if attempts != 1 || len(delivered) != 0 {
		t.Fatalf("attempts %d, delivered %v", attempts, delivered)
	}
	step(time.Minute) // still failing: tried again
	failing = false
	step(time.Minute) // delivered
	step(time.Minute) // not again
	if attempts != 3 || strings.Join(delivered, ",") != "down" {
		t.Fatalf("attempts %d, delivered %v", attempts, delivered)
	}
	healthy.Store(true)
	failing = true
	step(time.Minute) // up; delivery fails
	failing = false
	step(time.Minute)
	if strings.Join(delivered, ",") != "down,up" {
		t.Fatalf("delivered %v", delivered)
	}
	// Down and back before the down alert went out: nothing at all.
	healthy.Store(false)
	failing = true
	step(6 * time.Minute)
	healthy.Store(true)
	failing = false
	step(time.Minute)
	step(time.Minute)
	if strings.Join(delivered, ",") != "down,up" {
		t.Fatalf("delivered %v", delivered)
	}
}

func TestDeliver(t *testing.T) {
	var body string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
	}))
	defer hook.Close()
	out := filepath.Join(t.TempDir(), "out")
	w := New(&Config{Webhook: hook.URL, Command: `cat > ` + out + `; echo "$TIFFIN_WATCH_BOX $TIFFIN_WATCH_STATE" >> ` + out}, io.Discard)
	if err := w.deliver(context.Background(), Alert{Box: "shop", State: "down", Text: "Tiffin box shop is down."}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	if !strings.Contains(body, `"text":"Tiffin box shop is down."`) || string(b) != "Tiffin box shop is down.\nshop down\n" {
		t.Fatalf("webhook %s, command %q", body, b)
	}
}
