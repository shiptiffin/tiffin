package runtime

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/manifest"
)

func TestHealthPollSchedule(t *testing.T) {
	for _, tc := range []struct{ since, want time.Duration }{
		{0, 10 * time.Millisecond},
		{1900 * time.Millisecond, 10 * time.Millisecond},
		{2 * time.Second, 200 * time.Millisecond},
		{time.Minute, 200 * time.Millisecond},
	} {
		if got := healthPoll(tc.since); got != tc.want {
			t.Errorf("healthPoll(%s) = %s, want %s", tc.since, got, tc.want)
		}
	}
}

// runningEngine reports every container running (Inspect is all
// waitHealthy asks of the engine); inspect stands in for the CLI call's cost.
type runningEngine struct {
	Engine
	inspect func()
}

func (e runningEngine) Inspect(ctx context.Context, name string) (*Container, error) {
	if e.inspect != nil {
		e.inspect()
	}
	return &Container{Name: name, Running: true, Status: "running"}, nil
}

func freePort(t testing.TB) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// An app that starts answering is seen healthy within a poll or two, not up
// to 200ms later.
func TestWaitHealthySeesTheAppAtOnce(t *testing.T) {
	r := &rt{opt: Options{HealthTimeout: 5 * time.Second}, eng: runningEngine{}, ports: map[int]string{}}
	spec := &manifest.App{Role: manifest.RoleWeb, Healthcheck: "/"}
	for range 3 {
		port := freePort(t)
		r.ports[port] = "x"
		upc := make(chan time.Time, 1)
		go func() {
			time.Sleep(150 * time.Millisecond)
			ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
			if err != nil {
				return
			}
			upc <- time.Now()
			srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") })}
			t.Cleanup(func() { srv.Close() })
			srv.Serve(ln)
		}()
		if err := r.waitHealthy(context.Background(), Instance{Name: "x", Port: port}, spec, ""); err != nil {
			t.Fatal(err)
		}
		if late := time.Since(<-upc); late > 60*time.Millisecond {
			t.Errorf("healthy %s after the app answered, want within a few polls of 10ms", late)
		}
	}
}

// A slow inspect (nerdctl on a busy box) runs beside the checks: an app
// that answers while one is under way is seen healthy at once, not when
// the inspect returns.
func TestWaitHealthyDoesNotWaitForInspect(t *testing.T) {
	r := &rt{opt: Options{HealthTimeout: 5 * time.Second}, eng: runningEngine{inspect: func() { time.Sleep(1500 * time.Millisecond) }}, ports: map[int]string{}}
	spec := &manifest.App{Role: manifest.RoleWeb, Healthcheck: "/"}
	port := freePort(t)
	r.ports[port] = "x"
	upc := make(chan time.Time, 1)
	go func() {
		time.Sleep(1100 * time.Millisecond) // just after the first inspect started
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil {
			return
		}
		upc <- time.Now()
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") })}
		t.Cleanup(func() { srv.Close() })
		srv.Serve(ln)
	}()
	if err := r.waitHealthy(context.Background(), Instance{Name: "x", Port: port}, spec, ""); err != nil {
		t.Fatal(err)
	}
	if late := time.Since(<-upc); late > 100*time.Millisecond {
		t.Errorf("healthy %s after the app answered: the check waited for the inspect", late)
	}
}

// TestWakeTimeBun measures a wake's start on a small Bun app: from spawning
// the process to the health check passing, with the 200ms poll the box used
// before (and its inspect first) and with waitHealthy as it is. It is a
// measurement, not a check: TIFFIN_WAKE_BENCH=1 go test -run TestWakeTimeBun -v
func TestWakeTimeBun(t *testing.T) {
	if os.Getenv("TIFFIN_WAKE_BENCH") == "" {
		t.Skip("set TIFFIN_WAKE_BENCH=1 to measure")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun not found")
	}
	app := filepath.Join(t.TempDir(), "server.ts")
	os.WriteFile(app, []byte(`Bun.serve({ port: Number(process.env.PORT), hostname: "127.0.0.1", fetch: () => new Response("hello") });`), 0o644)
	// One `ps` per inspect: a CLI call, as nerdctl inspect is (nerdctl costs more).
	inspect := func() { _ = exec.Command("ps", "-p", "1").Run() }
	r := &rt{opt: Options{HealthTimeout: 10 * time.Second}, eng: runningEngine{inspect: inspect}, ports: map[int]string{}}
	spec := &manifest.App{Role: manifest.RoleWeb, Healthcheck: "/"}

	// before is waitHealthy's loop as it was: inspect at once, then every
	// second; a check every 200ms.
	before := func(port int) {
		client := &http.Client{Timeout: 3 * time.Second}
		lastInspect := time.Time{}
		for {
			if time.Since(lastInspect) > time.Second {
				lastInspect = time.Now()
				inspect()
			}
			if res, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", port)); err == nil {
				res.Body.Close()
				if res.StatusCode < 500 {
					return
				}
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	after := func(port int) {
		r.mu.Lock()
		r.ports[port] = "x"
		r.mu.Unlock()
		if err := r.waitHealthy(context.Background(), Instance{Name: "x", Port: port}, spec, ""); err != nil {
			t.Fatal(err)
		}
	}
	run := func(wait func(int)) time.Duration {
		port := freePort(t)
		cmd := exec.Command(bun, app)
		cmd.Env = append(os.Environ(), "PORT="+strconv.Itoa(port))
		began := time.Now()
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		wait(port)
		took := time.Since(began)
		cmd.Process.Kill()
		cmd.Wait()
		return took
	}
	const n = 21
	stats := func(name string, wait func(int)) {
		var ds []time.Duration
		for range n {
			ds = append(ds, run(wait))
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		t.Logf("%-28s median %4dms  p90 %4dms  min %4dms  max %4dms", name, ds[n/2].Milliseconds(), ds[n*9/10].Milliseconds(), ds[0].Milliseconds(), ds[n-1].Milliseconds())
	}
	run(after) // warm the file cache
	stats("before (200ms poll):", before)
	stats("after (10ms poll):", after)
}
