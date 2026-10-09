//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/mod/queue"
)

// TestJobsURL is a project with no apps whose cron and queue call a web
// address outside the box: a small HTTP server on this machine, which the
// VM reaches as host.lima.internal. The box is told that one address may be
// called although it is private (on a real server it would be public).
//
//   - the cron's ticks and the queue's jobs arrive as signed POSTs
//   - a plan with a private address is refused before anything is stored
//   - a host name that resolves inside the box, and a redirect there, are
//     refused when the call is made, and the job says why
//   - pausing a cron holds its ticks; Run now still runs it
func TestJobsURL(t *testing.T) {
	b := newCLIBox(t, "jobsurl", "hooks")

	type call struct {
		path, cron, id string
		signed         bool
	}
	var mu sync.Mutex
	var calls []call
	var secret string
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/to-inside" {
			http.Redirect(w, r, "http://inside.test:7070/v1/health", http.StatusTemporaryRedirect)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body struct{ ID, Cron string }
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		ok := secret != "" && queue.Verify(secret, r.Header.Get(queue.HeaderSignature), raw, time.Now(), 5*time.Minute)
		calls = append(calls, call{path: r.URL.Path, cron: body.Cron, id: body.ID, signed: ok})
		mu.Unlock()
		if !ok {
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	got := func(match func(call) bool) int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, c := range calls {
			if match(c) {
				n++
			}
		}
		return n
	}

	// The VM reaches this machine as host.lima.internal (192.168.5.2): let calls go there,
	// and give the box a name that points at itself.
	hostIP := b.inBox(`getent hosts host.lima.internal | awk '{print $1}'`)
	if hostIP == "" {
		t.Fatal("the VM cannot resolve host.lima.internal")
	}
	b.inBox(fmt.Sprintf(`sudo install -d /etc/systemd/system/tiffin.service.d && printf '[Service]\nEnvironment=TIFFIN_QUEUE_ALLOW_NETS=%s/32\n' | sudo tee /etc/systemd/system/tiffin.service.d/e2e-jobs.conf >/dev/null && echo '127.0.0.1 inside.test' | sudo tee -a /etc/hosts >/dev/null && sudo systemctl daemon-reload && sudo systemctl restart tiffin && for i in $(seq 60); do curl -sf http://127.0.0.1:7070/v1/health >/dev/null && break; sleep 0.5; done`, hostIP))

	base := fmt.Sprintf("http://host.lima.internal:%d", port)
	// A private address is refused at plan time, in words.
	// A folder of its own: the CLI refuses a manifest beside its settings (dir/config).
	_ = os.MkdirAll(filepath.Join(b.dir, "project"), 0o755)
	bad := filepath.Join(b.dir, "project", "bad.json")
	if err := os.WriteFile(bad, []byte(`{"project":"hooks","queues":{"inside":{"url":"http://10.0.0.5/admin"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out := b.run("plan", bad); code == 0 || !strings.Contains(out, "a private address") {
		t.Fatalf("plan with a private address: exit %d\n%s", code, out)
	}

	b.apply("hooks", fmt.Sprintf(`{"project":"hooks",
		"crons":{"ping":{"schedule":"* * * * *","url":"%[1]s/ping","timeoutSeconds":20}},
		"queues":{"out":{"url":"%[1]s/out","maxAttempts":3},"inside":{"url":"http://inside.test:7070/v1/health","maxAttempts":3},"hop":{"url":"%[1]s/to-inside","maxAttempts":3}}}`, base))
	b.waitReady("cron/ping", "queue/out", "queue/inside", "queue/hop")
	s := b.ok("queue", "signing-secret", "hooks")
	mu.Lock()
	secret, _ = s["secret"].(string)
	mu.Unlock()
	if !strings.HasPrefix(secret, "tqs_") {
		t.Fatalf("signing secret: %v", s)
	}

	send := func(name string) string {
		t.Helper()
		res := b.ok("queue", "send", "hooks", "--body", fmt.Sprintf(`{"name":%q,"payload":{"n":1}}`, name))
		jobs, _ := res["jobs"].([]any)
		if len(jobs) != 1 {
			t.Fatalf("send %s: %v", name, res)
		}
		return jobs[0].(string)
	}
	waitJob := func(id, state string) map[string]any {
		t.Helper()
		var j map[string]any
		for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline); time.Sleep(time.Second) {
			if j = b.ok("queue", "jobs", "get", "hooks", id); j["state"] == state {
				return j
			}
		}
		t.Fatalf("%s: wanted %s, is %v (%v)", id, state, j["state"], j["lastError"])
		return nil
	}

	// A queue job and the cron's ticks arrive signed.
	out := send("out")
	waitJob(out, "completed")
	if got(func(c call) bool { return c.path == "/out" && c.id == out && c.signed }) != 1 {
		t.Errorf("the queue's job did not arrive signed: %+v", calls)
	}
	deadline := time.Now().Add(150 * time.Second)
	for got(func(c call) bool { return c.cron == "ping" && c.signed }) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("no signed tick from the cron: %+v", calls)
		}
		time.Sleep(2 * time.Second)
	}
	t.Logf("signed calls from outside the box: %d", got(func(c call) bool { return c.signed }))

	// Inside the box: refused, at once, with the reason.
	for name, want := range map[string]string{"inside": "inside.test: it resolves to 127.0.0.1, the box itself", "hop": "refused to call inside.test"} {
		j := waitJob(send(name), "dead")
		atts, _ := j["attempts"].([]any)
		if !strings.Contains(fmt.Sprint(j["lastError"]), want) || len(atts) != 1 {
			t.Errorf("%s: %d attempts, %v", name, len(atts), j["lastError"])
		}
	}

	// Pause holds the ticks; Run now still runs it.
	b.ok("queue", "crons", "pause", "hooks", "ping")
	time.Sleep(3 * time.Second) // a tick already on its way may land
	before := got(func(c call) bool { return c.cron == "ping" })
	time.Sleep(75 * time.Second)
	if n := got(func(c call) bool { return c.cron == "ping" }); n != before {
		t.Errorf("a paused cron ticked %d times", n-before)
	}
	trig := b.ok("queue", "crons", "trigger", "hooks", "ping")
	waitJob(fmt.Sprint(trig["job"]), "completed")
	cs := b.list("queue", "crons", "list", "hooks")
	if len(cs) != 1 || cs[0]["paused"] != true {
		t.Errorf("crons: %v", cs)
	}
	b.ok("queue", "crons", "resume", "hooks", "ping")
}
