//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/watch"
)

// TestOutsideCheck is the box's heartbeat end to end: a `tiffin watch`
// collector on this machine (the VM reaches it as host.lima.internal).
//
//   - monitor set pings first and keeps the URL; the ping carries the
//     version and check counts, no project names
//   - restarting Tiffin sends /start; monitor test pings at once
//   - with Tiffin stopped the pings stop, and the watcher says the box is down
func TestOutsideCheck(t *testing.T) {
	b := newCLIBox(t, "monitor", "secretproj")
	b.apply("secretproj", `{"project":"secretproj","services":{"postgres":{}}}`)

	const beat = "e2e0123456789abcdef0123456789ab"
	cfg := fmt.Sprintf(`{"downAfter":"80s","listen":":0","command":"true","boxes":[{"name":"vm","heartbeat":%q}]}`, beat)
	path := b.dir + "/watch.json"
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	wc, err := watch.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var pings []string
	var bodies []string
	var alerts []string
	w := watch.New(wc, testLog{t})
	w.Notify(func(_ context.Context, a watch.Alert) error {
		mu.Lock()
		alerts = append(alerts, a.State+": "+a.Text)
		mu.Unlock()
		return nil
	})
	inner := w.Handler()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		pings = append(pings, r.URL.Path)
		bodies = append(bodies, string(body))
		mu.Unlock()
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		inner.ServeHTTP(rw, r)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	waitPing := func(path string, d time.Duration) string {
		t.Helper()
		for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
			mu.Lock()
			for i, p := range pings {
				if p == path {
					body := bodies[i]
					pings, bodies = append(pings[:i:i], pings[i+1:]...), append(bodies[:i:i], bodies[i+1:]...)
					mu.Unlock()
					return body
				}
			}
			mu.Unlock()
		}
		t.Fatalf("no ping to %s within %s (got %v)", path, d, pings)
		return ""
	}

	url := fmt.Sprintf("http://host.lima.internal:%d/ping/%s", ln.Addr().(*net.TCPAddr).Port, beat)
	if code, out := b.run("monitor", "set", url+"-wrong"); code == 0 || !strings.Contains(out, "404") {
		t.Fatalf("a URL that 404s was accepted: exit %d\n%s", code, out)
	}
	set := b.ok("monitor", "set", url)
	if set["on"] != true || set["receiver"] != "healthchecks" {
		t.Fatalf("monitor set: %v", set)
	}
	body := waitPing("/ping/"+beat, 10*time.Second)
	var pl struct {
		Status        string `json:"status"`
		Version       string `json:"version"`
		Checks        int    `json:"checks"`
		UptimeSeconds int64  `json:"uptimeSeconds"`
	}
	if err := json.Unmarshal([]byte(body), &pl); err != nil || pl.Version == "" || pl.Checks < 5 || strings.Contains(body, "secretproj") {
		t.Fatalf("ping body: %s (%v)", body, err)
	}

	b.inBox(`sudo systemctl restart tiffin`)
	waitPing("/ping/"+beat+"/start", 2*time.Minute)
	b.ok("monitor", "test")
	waitPing("/ping/"+beat, 30*time.Second)

	b.inBox(`sudo systemctl stop tiffin`)
	t.Cleanup(func() {
		_, _, _ = runCmd(time.Minute, "limactl", "shell", b.instance, "--", "sudo", "systemctl", "start", "tiffin")
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for deadline := time.Now().Add(4 * time.Minute); ; time.Sleep(5 * time.Second) {
		w.Check(ctx)
		mu.Lock()
		got := strings.Join(alerts, "|")
		mu.Unlock()
		if strings.Contains(got, "down: Tiffin box vm is down: no heartbeat") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the watcher never said the box is down: %q", got)
		}
	}
}

type testLog struct{ t *testing.T }

func (l testLog) Write(p []byte) (int, error) {
	l.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}
