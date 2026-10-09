package monitor

import (
	"context"
	"encoding/json"
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

func TestNextDelay(t *testing.T) {
	if lo, hi := nextDelay(0), nextDelay(2*Jitter-1); lo != 50*time.Second || hi >= 70*time.Second || hi < 69*time.Second {
		t.Fatalf("delays %s..%s, want 50s..70s", lo, hi)
	}
}

// Checks must fail for FailAfter before a ping says so, and one passing
// round clears it.
func TestFailThreshold(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	ok := true
	p := &platform.Platform{Version: "v1.2.3", BoxChecks: func(context.Context) []platform.Check {
		return []platform.Check{{Name: "state", OK: true}, {Name: "disk", OK: ok, Detail: "95% used"}}
	}}
	m := &Module{now: func() time.Time { return now }, started: now}
	at := func(min int, wantStatus string) {
		t.Helper()
		now = time.Date(2026, 10, 6, 12, min, 0, 0, time.UTC)
		if pl := m.payload(context.Background(), p, false); pl.Status != wantStatus {
			t.Fatalf("minute %d: status %s, want %s (%+v)", min, pl.Status, wantStatus, pl)
		}
	}
	at(0, "ok")
	ok = false
	at(1, "ok") // failing since minute 1
	at(10, "ok")
	at(11, "failing")
	at(30, "failing")
	ok = true
	at(31, "ok")
	ok = false
	at(32, "ok") // a new failure starts its own ten minutes
	if k := kindFor(now.Add(FailAfter), m.badSince); k != KindFail {
		t.Fatalf("kind %s", k)
	}
}

// A ping carries counts and check names only: never what a check says
// (paths, project names, connection strings) unless details is on, and
// never the ping URL.
func TestPayloadHasNoSecrets(t *testing.T) {
	p := &platform.Platform{Version: "v1", BoxChecks: func(context.Context) []platform.Check {
		return []platform.Check{
			{Name: "postgres", OK: false, Detail: "dial postgres://app:hunter2@127.0.0.1:5432/acme failed"},
			{Name: "resources", OK: false, Detail: "1 failed: acme-secret-project app/web (boom)"},
			{Name: "edge", OK: true, Detail: "fine"},
		}
	}}
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
	}))
	defer srv.Close()
	m := &Module{}
	pingURL := srv.URL + "/0f1e2d3c-secret-check-uuid"
	if b := m.ping(context.Background(), p, &Config{URL: pingURL}, ""); !b.OK || b.Kind != KindOK {
		t.Fatalf("beat %+v", b)
	}
	for _, leak := range []string{"hunter2", "acme", "127.0.0.1", "secret-check-uuid", "fine"} {
		if strings.Contains(body, leak) {
			t.Errorf("payload leaks %q: %s", leak, body)
		}
	}
	var pl Payload
	if err := json.Unmarshal([]byte(body), &pl); err != nil || pl.Checks != 3 || pl.FailedChecks != 2 || strings.Join(pl.Failing, ",") != "postgres,resources" || pl.Version != "v1" {
		t.Fatalf("payload %+v (%v)", pl, err)
	}
	// Opted in: what failing checks say goes too (cut short).
	m.ping(context.Background(), p, &Config{URL: pingURL, Details: true}, "")
	if !strings.Contains(body, "acme-secret-project") || strings.Contains(body, "fine") {
		t.Fatalf("details payload: %s", body)
	}
}

func TestPingRequests(t *testing.T) {
	ctx := context.Background()
	pl := Payload{Status: "failing", Version: "v1", UptimeSeconds: 3700, Checks: 5, FailedChecks: 1, Failing: []string{"disk"}}
	for _, c := range []struct{ url, kind, method, want string }{
		{"https://hc-ping.com/abc", KindOK, "POST", "https://hc-ping.com/abc"},
		{"https://hc-ping.com/abc/", KindFail, "POST", "https://hc-ping.com/abc/fail"},
		{"https://hc-ping.com/abc", KindStart, "POST", "https://hc-ping.com/abc/start"},
		{"https://watch.example.com/ping/tok", KindFail, "POST", "https://watch.example.com/ping/tok/fail"},
		{"https://kuma.example.com/api/push/tok?status=up&msg=OK&ping=", KindFail, "GET",
			"https://kuma.example.com/api/push/tok?msg=failing%3A+tiffin+v1%2C+up+1h1m40s%2C+1+of+5+checks+failing+%28disk%29&ping=&status=down"},
		{"https://kuma.example.com/api/push/tok", KindStart, "GET", "https://kuma.example.com/api/push/tok?msg=failing%3A+tiffin+v1%2C+up+1h1m40s%2C+1+of+5+checks+failing+%28disk%29&status=up"},
	} {
		req, err := pingRequest(ctx, c.url, c.kind, pl)
		if err != nil || req.Method != c.method || req.URL.String() != c.want {
			t.Errorf("%s %s: %v %s %s", c.url, c.kind, err, req.Method, req.URL)
		}
	}
}

// The API: owner only, a URL is kept only once it answered, test pings,
// off forgets it.
func TestAPI(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tm := tokens.NewManager(db)
	owner, _, _ := tm.Bootstrap(ctx)
	ownerP, _ := tm.Authenticate(ctx, owner)
	reader, _, err := tm.Create(ctx, ownerP, tokens.CreateRequest{Name: "ci", Scopes: []tokens.Scope{tokens.ScopeRead}, Projects: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	p := &platform.Platform{DB: db, Version: "v9", BoxChecks: func(context.Context) []platform.Check { return []platform.Check{{Name: "disk", OK: true}} }}
	srv := httptest.NewServer(api.New(api.Deps{DB: db, Engine: change.NewEngine(db), Tokens: tm, Platform: p}).Handler())
	defer srv.Close()

	var mu sync.Mutex
	var pings []string
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		pings = append(pings, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/missing") {
			http.NotFound(w, r)
		}
	}))
	defer recv.Close()

	call := func(token, method, path, body string) (int, Monitor) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var v Monitor
		_ = json.NewDecoder(res.Body).Decode(&v)
		return res.StatusCode, v
	}

	if code, v := call(owner, "GET", "/v1/monitor", ""); code != 200 || v.On || v.Payload.Checks != 1 || v.EverySeconds != 60 || v.FailAfterMinutes != 10 {
		t.Fatalf("show off: %d %+v", code, v)
	}
	if code, _ := call(reader, "GET", "/v1/monitor", ""); code != 403 {
		t.Fatalf("reader show: %d", code)
	}
	if code, _ := call(owner, "POST", "/v1/monitor/test", "{}"); code != 409 {
		t.Fatalf("test while off: %d", code)
	}
	if code, _ := call(owner, "PUT", "/v1/monitor", `{"url":"ftp://x"}`); code != 422 {
		t.Fatalf("ftp url: %d", code)
	}
	if code, _ := call(owner, "PUT", "/v1/monitor", `{"url":"`+recv.URL+`/missing"}`); code != 422 {
		t.Fatalf("404 url: %d", code)
	}
	if code, v := call(owner, "GET", "/v1/monitor", ""); code != 200 || v.On {
		t.Fatalf("a URL that failed its ping was kept: %+v", v)
	}
	code, v := call(owner, "PUT", "/v1/monitor", `{"url":"`+recv.URL+`/abc","details":true}`)
	if code != 200 || !v.On || v.Receiver != "healthchecks" || !v.Details || v.Last == nil || !v.Last.OK || v.Last.Kind != KindOK {
		t.Fatalf("set: %d %+v", code, v)
	}
	if code, v := call(owner, "POST", "/v1/monitor/test", "{}"); code != 200 || v.Last == nil || !v.Last.OK {
		t.Fatalf("test: %d %+v", code, v)
	}
	if code, v := call(owner, "DELETE", "/v1/monitor", ""); code != 200 || v.On || v.Last != nil {
		t.Fatalf("off: %d %+v", code, v)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(pings, ","); got != "POST /missing,POST /abc,POST /abc" {
		t.Fatalf("pings: %s", got)
	}
}
