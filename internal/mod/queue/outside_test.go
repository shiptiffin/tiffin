package queue

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/platform"
)

// hosts is a fake DNS for URL targets.
func hosts(m map[string]string) func(context.Context, string) ([]netip.Addr, error) {
	return func(_ context.Context, host string) ([]netip.Addr, error) {
		if a, ok := m[host]; ok {
			return []netip.Addr{netip.MustParseAddr(a)}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
}

func port(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Port()
}

func TestGuard(t *testing.T) {
	g := &guard{self: func() []netip.Addr { return []netip.Addr{netip.MustParseAddr("203.0.114.9")} }}
	for _, c := range []struct {
		ip, why string
	}{
		{"127.0.0.1", "the box itself"},
		{"::1", "the box itself"},
		{"10.1.2.3", "a private address"},
		{"172.16.0.1", "a private address"},
		{"192.168.5.2", "a private address"},
		{"fd00::1", "a private address"},
		{"169.254.169.254", "a link-local address"},
		{"fe80::1", "a link-local address"},
		{"100.64.0.1", "not a public address"},
		{"0.1.2.3", "not a public address"},
		{"::ffff:10.0.0.1", "a private address"},
		{"64:ff9b::a00:1", "not a public address"},
		{"203.0.114.9", "the box's own address"},
		{"1.1.1.1", ""},
		{"2606:4700:4700::1111", ""},
	} {
		err := g.check("h", netip.MustParseAddr(c.ip))
		if c.why == "" {
			if err != nil {
				t.Errorf("%s refused: %v", c.ip, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.why) {
			t.Errorf("%s: %v, want %q", c.ip, err, c.why)
		}
	}
	for raw, want := range map[string]string{
		"https://hooks.example.com/x": "",
		"http://127.0.0.1:9/x":        "the box itself",
		"http://localhost/x":          "the box itself",
		"http://app.localhost/x":      "the box itself",
		"http://[::1]/x":              "the box itself",
		"http://10.0.0.5/x":           "a private address",
		"ftp://example.com/x":         "not a web address",
		"https://u:p@example.com/":    "user name or password",
	} {
		err := g.checkURL(raw)
		if (want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: %v, want %q", raw, err, want)
		}
	}
	allowed := &guard{allow: []netip.Prefix{netip.MustParsePrefix("192.168.5.0/24")}}
	if err := allowed.check("host.lima.internal", netip.MustParseAddr("192.168.5.2")); err != nil {
		t.Errorf("allowed range refused: %v", err)
	}
}

// A queue and a cron of a project with no apps call a URL: the calls are
// signed POSTs, recorded like any job, and the answer is kept as output.
func TestURLTargets(t *testing.T) {
	var secret string
	var got atomic.Int64
	var lastSig, lastBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		lastSig.Store(r.Header.Get(HeaderSignature))
		lastBody.Store(string(raw))
		if r.Method != http.MethodPost || !Verify(secret, r.Header.Get(HeaderSignature), raw, time.Now(), time.Minute) {
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		got.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "path": r.URL.Path})
	}))
	defer srv.Close()
	e := newEngine(t, func(c *Config) {
		c.AllowNets = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
		c.Resolve = hosts(map[string]string{"hooks.test": "127.0.0.1"})
	})
	ctx := context.Background()
	_, secret, _ = e.cfg.Keys.Get(ctx, "hooks")
	base := "http://hooks.test:" + port(t, srv)

	q, _ := json.Marshal(map[string]any{"url": base + "/orders", "leaseSeconds": 5})
	if err := e.ReconcileQueue(ctx, "hooks", "orders", q); err != nil {
		t.Fatal(err)
	}
	id := e.send("hooks", SendRequest{Name: "orders", Payload: json.RawMessage(`{"n":1}`), By: "test"}).Jobs[0]
	j := e.waitState("hooks", id, stateCompleted, 10*time.Second)
	if j.Target != base+"/orders" || !strings.Contains(string(j.Output), `"/orders"`) {
		t.Errorf("job %+v output %s", j, j.Output)
	}
	var body deliveryBody
	_ = json.Unmarshal([]byte(lastBody.Load().(string)), &body)
	if body.ID != id || string(body.Payload) != `{"n":1}` || !strings.HasPrefix(lastSig.Load().(string), "t=") {
		t.Errorf("delivery %+v", body)
	}

	c, _ := json.Marshal(map[string]any{"schedule": "@hourly", "url": base + "/digest", "timeoutSeconds": 7})
	if err := e.ReconcileCron(ctx, "hooks", "digest", c); err != nil {
		t.Fatal(err)
	}
	cid, err := e.TriggerCron(ctx, "hooks", "digest", "test")
	if err != nil {
		t.Fatal(err)
	}
	j = e.waitState("hooks", cid, stateCompleted, 10*time.Second)
	if j.Target != base+"/digest" || j.Kind != kindCron {
		t.Errorf("cron job %+v", j)
	}
	crons, err := e.Crons(ctx, "hooks")
	if err != nil || len(crons) != 1 || crons[0].URL != base+"/digest" || crons[0].TimeoutSeconds != 7 || len(crons[0].Recent) != 1 || crons[0].Recent[0].State != stateCompleted {
		t.Errorf("crons %+v %v", crons, err)
	}
	if got.Load() != 2 {
		t.Errorf("receiver got %d signed calls, want 2", got.Load())
	}
}

// Private addresses are refused when configured (literals) and when a call
// is made (names resolving inside, and redirects there), and the job goes to
// the dead letters saying why.
func TestURLGuard(t *testing.T) {
	var reached atomic.Int64
	inside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1) }))
	defer inside.Close()
	var redirected atomic.Value
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Store(r.Method + " sig=" + r.Header.Get(HeaderSignature))
	}))
	defer other.Close()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/to-inside":
			http.Redirect(w, r, "http://inside.test:"+port(t, inside)+"/x", http.StatusTemporaryRedirect)
		case "/to-other":
			http.Redirect(w, r, "http://other.test:"+port(t, other)+"/y", http.StatusTemporaryRedirect)
		}
	}))
	defer front.Close()
	e := newEngine(t, func(c *Config) {
		// 127.0.0.1 plays the outside world; 127.0.0.2 is "inside the box".
		c.AllowNets = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
		c.Resolve = hosts(map[string]string{"front.test": "127.0.0.1", "other.test": "127.0.0.1", "inside.test": "127.0.0.2"})
	})
	ctx := context.Background()

	if _, err := e.ConfigureQueue(ctx, "p", QueueConfig{Name: "lit", URL: "http://10.0.0.5/x"}); err == nil || !strings.Contains(err.Error(), "a private address") {
		t.Errorf("private literal: %v", err)
	}
	if _, err := e.ConfigureQueue(ctx, "p", QueueConfig{Name: "lit", URL: "http://169.254.169.254/latest/meta-data"}); err == nil || !strings.Contains(err.Error(), "link-local") {
		t.Errorf("metadata address: %v", err)
	}
	c, _ := json.Marshal(map[string]any{"schedule": "@daily", "url": "http://127.0.0.2:1/x"})
	if err := e.ReconcileCron(ctx, "p", "lit", c); err == nil || !strings.Contains(err.Error(), "the box itself") {
		t.Errorf("cron with a box address: %v", err)
	}

	dead := func(name, u string) *Job {
		t.Helper()
		e.configure("p", QueueConfig{Name: name, URL: u, MaxAttempts: 3})
		return e.waitState("p", e.send("p", SendRequest{Name: name}).Jobs[0], stateDead, 10*time.Second)
	}
	j := dead("named", "http://inside.test:"+port(t, inside)+"/x")
	if len(j.Attempts) != 1 || !strings.Contains(j.LastError, "inside.test: it resolves to 127.0.0.2, the box itself") {
		t.Errorf("name resolving inside: %d attempts, %q", len(j.Attempts), j.LastError)
	}
	j = dead("hop", "http://front.test:"+port(t, front)+"/to-inside")
	if !strings.Contains(j.LastError, "refused to call inside.test") {
		t.Errorf("redirect inside: %q", j.LastError)
	}
	if reached.Load() != 0 {
		t.Errorf("the inside server was reached %d times", reached.Load())
	}

	// A redirect to another public host is followed, without the signature.
	e.configure("p", QueueConfig{Name: "moved", URL: "http://front.test:" + port(t, front) + "/to-other"})
	e.waitState("p", e.send("p", SendRequest{Name: "moved"}).Jobs[0], stateCompleted, 10*time.Second)
	if v, _ := redirected.Load().(string); v != "POST sig=" {
		t.Errorf("redirected call: %q", v)
	}
}

func TestURLRateLimit(t *testing.T) {
	a := newApp(t, nil, "")
	e := newEngine(t, func(c *Config) { c.URLRatePerMinute = 2 })
	a.secret = func() string { _, s, _ := e.cfg.Keys.Get(context.Background(), "p"); return s }
	e.configure("p", QueueConfig{Name: "out", URL: a.url("/out")})
	var ids []string
	for range 4 {
		ids = append(ids, e.send("p", SendRequest{Name: "out"}).Jobs[0])
	}
	eventually(t, 10*time.Second, "two calls", func() bool { return len(a.deliveries()) == 2 })
	time.Sleep(300 * time.Millisecond)
	if n := len(a.deliveries()); n != 2 {
		t.Fatalf("%d calls in the first minute, want 2", n)
	}
	waiting := 0
	for _, id := range ids {
		if j := e.job("p", id); j.State == stateQueued && j.WaitingFor == "the project's limit on calls outside the box" {
			waiting++
		}
	}
	if waiting != 2 {
		t.Errorf("%d jobs waiting for the project's limit, want 2", waiting)
	}
}

func TestCronPauseAndNextRuns(t *testing.T) {
	a := newApp(t, nil, "")
	e := newEngine(t, nil)
	a.secret = func() string { _, s, _ := e.cfg.Keys.Get(context.Background(), "p"); return s }
	ctx := context.Background()
	c, _ := json.Marshal(map[string]any{"schedule": "* * * * *", "url": a.url("/tick")})
	if err := e.ReconcileCron(ctx, "p", "tick", c); err != nil {
		t.Fatal(err)
	}
	info, err := e.SetCronPaused(ctx, "p", "tick", true, "sam (human)")
	if err != nil || !info.Paused || info.PausedBy != "sam (human)" || info.PausedAt == nil {
		t.Fatalf("pause: %+v %v", info, err)
	}
	// Due now, but paused: it does not fire, and an apply keeps it paused.
	if _, err := e.pool.Exec(ctx, `UPDATE tq_crons SET next_at = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	if err := e.ReconcileCron(ctx, "p", "tick", c); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2500 * time.Millisecond)
	if n := len(a.deliveries()); n != 0 {
		t.Fatalf("a paused cron fired %d times", n)
	}
	// Run once now works while paused.
	id, err := e.TriggerCron(ctx, "p", "tick", "test")
	if err != nil {
		t.Fatal(err)
	}
	e.waitState("p", id, stateCompleted, 10*time.Second)
	info, err = e.SetCronPaused(ctx, "p", "tick", false, "sam (human)")
	if err != nil || info.Paused || !info.NextAt.After(time.Now()) {
		t.Fatalf("resume: %+v %v", info, err)
	}
	if _, err := e.SetCronPaused(ctx, "p", "nope", true, ""); err == nil {
		t.Error("pausing a missing cron worked")
	}

	from := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC) // a Friday; London is on BST until the 25th
	next, err := NextRuns("0 9 * * 1-5", "Europe/London", 3, from)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-10-26T09:00:00Z", "2026-10-27T09:00:00Z", "2026-10-28T09:00:00Z"} // after the clocks go back: GMT
	for i, w := range want {
		if next[i].Format(time.RFC3339) != w {
			t.Errorf("next[%d] = %s, want %s", i, next[i].Format(time.RFC3339), w)
		}
	}
	if _, err := NextRuns("0 9 * *", "", 5, from); err == nil || !strings.Contains(err.Error(), "is not a schedule") {
		t.Errorf("short schedule: %v", err)
	}
	if _, err := NextRuns("@daily", "Mars/Base", 5, from); err == nil || !strings.Contains(err.Error(), "time zone") {
		t.Errorf("bad zone: %v", err)
	}
	var qe *Error
	if _, err := NextRuns("nope", "", 1, from); !errors.As(err, &qe) || qe.Status != 422 {
		t.Errorf("bad schedule is not a 422: %v", err)
	}
}

func TestCheckPlanRefusesInsideURLs(t *testing.T) {
	m := &Module{}
	p := &platform.Platform{}
	ok := map[string]change.Resource{"cron/a": {Address: "cron/a", Spec: json.RawMessage(`{"schedule":"@daily","url":"https://hooks.example.com/a"}`)}}
	if err := m.CheckPlan(context.Background(), p, "x", ok); err != nil {
		t.Errorf("public url refused: %v", err)
	}
	never := map[string]change.Resource{"cron/n": {Address: "cron/n", Spec: json.RawMessage(`{"schedule":"0 0 31 2 *","app":"web"}`)}}
	if err := m.CheckPlan(context.Background(), p, "x", never); err == nil || !strings.Contains(err.Error(), "never matches") {
		t.Errorf("31 February: %v", err)
	}
	bad := map[string]change.Resource{"queue/q": {Address: "queue/q", Spec: json.RawMessage(`{"url":"http://192.168.1.10/hook"}`)}}
	if err := m.CheckPlan(context.Background(), p, "x", bad); err == nil || !strings.Contains(err.Error(), "a private address") {
		t.Errorf("private url: %v", err)
	}
}
