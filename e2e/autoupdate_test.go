//go:build e2e

package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/release"
)

// releaseHost serves signed release manifests and builds to the box, as
// the channel releases on GitHub do, from this machine
// (host.lima.internal in the VM).
type releaseHost struct {
	t        *testing.T
	key      release.SecretKey
	mu       sync.Mutex
	files    map[string][]byte // path → body
	port     int
	platform string
}

func newReleaseHost(t *testing.T, key release.SecretKey) *releaseHost {
	h := &releaseHost{t: t, key: key, files: map[string][]byte{}, platform: "linux/" + HostArch()}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.port = ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		body, ok := h.files[r.URL.Path]
		h.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return h
}

// source is the update source the box is pointed at.
func (h *releaseHost) source() string {
	return fmt.Sprintf("http://host.lima.internal:%d/{channel}/manifest.json", h.port)
}

// publish signs a stable manifest for version with build's sha256 and
// serves served as the build (a tampered one when they differ).
func (h *releaseHost) publish(version string, build, served []byte, edgeRestart bool) {
	sum := sha256.Sum256(build)
	name := "tiffin-" + strings.ReplaceAll(h.platform, "/", "-") + "-" + version
	m := release.Manifest{Version: version, Channel: "stable", Date: time.Now().UTC(), Rollout: 100, EdgeRestart: edgeRestart,
		Artifacts: map[string]release.Artifact{h.platform: {Name: name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(build))}}}
	raw, _ := json.MarshalIndent(m, "", "  ")
	sig, err := release.Sign(h.key, raw, m.TrustedComment())
	if err != nil {
		h.t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.files["/stable/manifest.json"], h.files["/stable/manifest.json.minisig"], h.files["/stable/"+name] = raw, sig, served
}

// TestAutoUpdate is the acceptance test of automatic updates: a box built
// to trust a (throwaway) release key reads signed releases from a server on
// this machine.
//
//	a build that does not match its signed manifest is refused, nothing changes
//	→ in the maintenance window the box installs a release by itself, after a
//	  backup, under steady load: no request fails
//	→ a release that fails its health check rolls back and alerts the owner
//	→ a release that changed the edge restarts the edge too (its socket holds
//	  the ports), and the edge runs the new build
func TestAutoUpdate(t *testing.T) {
	start := time.Now()
	phase := phaseLogger(t)
	key, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	trust := "-X github.com/shiptiffin/tiffin/internal/release.extraKeys=" + key.Public().String()
	p := time.Now()
	v1 := buildTiffinFrom(t, RepoRoot(), dir, "linux", "0.1.0", trust)
	v2 := buildTiffinFrom(t, RepoRoot(), dir, "linux", "0.2.0", trust)
	v4 := buildTiffinFrom(t, RepoRoot(), dir, "linux", "0.4.0", trust)
	read := func(p string) []byte {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	broken := []byte("#!/bin/sh\ncase \"$1\" in provision|install-units) exit 0 ;; esac\necho 'broken build' >&2\nexit 1\n")
	phase("builds", p)

	p = time.Now()
	b := newCLIBoxFrom(t, dir, "steady", buildTiffin(t, dir, "", ""), v1)
	b.inBox(`sudo cp /var/lib/tiffin/platform/ca.crt /tmp/ca.crt && sudo chmod 644 /tmp/ca.crt`)
	phase("up", p)
	host := newReleaseHost(t, key)
	// The auth engine runs on the Bun app builds use.
	if got := b.inBox(`systemctl is-active tiffin-auth; grep -o 'bun-[0-9.]*' /etc/systemd/system/tiffin-auth.service | head -1`); got != "active\nbun-1.4.2" {
		t.Errorf("auth engine: %q", got)
	}

	// ---- the app under load ----
	p = time.Now()
	app := filepath.Join(b.dir, "steady")
	_ = os.MkdirAll(app, 0o755)
	for f, body := range map[string]string{
		"index.ts":         strings.ReplaceAll(steadyApp, "VERSION", "v1"),
		"package.json":     `{"name":"steady","private":true,"type":"module","scripts":{"start":"bun index.ts"}}`,
		"tiffin.config.ts": `export default { project: "steady", apps: { web: { instances: 2, routes: ["steady"] } } };` + "\n",
	} {
		if err := os.WriteFile(filepath.Join(app, f), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	plan := b.ok("plan", app)
	b.ok("apply", app, "--confirm", plan["hash"].(string), "-m", "e2e auto update")
	deploy(t, b, app)
	installLoadgen(t, b)
	loads := &steadyLoads{t: t, b: b, phase: phase}
	phase("app", p)

	st := b.ok("update", "settings", "--source", host.source())
	if st["version"] != "0.1.0" || st["release"] != true || st["auto"] != true {
		t.Fatalf("update status: %v", st)
	}
	status := func() map[string]any {
		code, out := b.run("update", "status")
		var m map[string]any
		if code != 0 || json.Unmarshal([]byte(out), &m) != nil {
			return nil // tiffin is restarting
		}
		return m
	}
	newest := func() string {
		if ups, _ := b.ok("update", "status")["updates"].([]any); len(ups) > 0 {
			return fmt.Sprint(ups[0].(map[string]any)["id"])
		}
		return ""
	}
	// finished waits for an update to want after the one with ID prev to end.
	finished := func(want, prev string) map[string]any {
		t.Helper()
		deadline := time.Now().Add(10 * time.Minute)
		for time.Now().Before(deadline) {
			if s := status(); s != nil {
				if ups, _ := s["updates"].([]any); len(ups) > 0 {
					u := ups[0].(map[string]any)
					if u["id"] != prev && u["to"] == want && u["status"] != "running" {
						return u
					}
				}
			}
			time.Sleep(3 * time.Second)
		}
		t.Fatalf("the update to %s did not finish: %v", want, status())
		return nil
	}
	version := func() any { return b.ok("health")["version"] }
	edgePID := func() string { return b.inBox(`systemctl show -p MainPID --value tiffin-edge`) }

	// ---- (1) a tampered build is refused: nothing changes ----
	p = time.Now()
	host.publish("0.2.0", read(v2), read(v1), false)
	if code, out := b.run("update", "apply"); code == 0 || !strings.Contains(out, "refused") || !strings.Contains(out, "sha256") {
		t.Fatalf("a tampered build must be refused: exit %d\n%s", code, out)
	}
	if v := version(); v != "0.1.0" {
		t.Fatalf("after the refusal: version %v", v)
	}
	phase("tampered", p)

	// ---- (2) the maintenance window: installs by itself, under load ----
	host.publish("0.2.0", read(v2), read(v2), false)
	pid, prev := edgePID(), newest()
	var up map[string]any
	r := loads.run("auto update in window", func() {
		// The window opens so that its update time (30 min in) is the next minute.
		win := b.inBox(`date -d '-29 minutes' +%H:%M`)
		b.ok("update", "settings", "--window", win)
		up = finished("0.2.0", prev)
	})
	if up["status"] != "ok" || up["trigger"] != "schedule" || up["backup"] == "" {
		t.Fatalf("the window's update: %v", up)
	}
	if r.Failed > 0 {
		t.Errorf("auto update: %d of %d requests failed", r.Failed, r.Requests)
	}
	if v := version(); v != "0.2.0" {
		t.Fatalf("after the window's update: version %v", v)
	}
	if after := edgePID(); after != pid {
		t.Errorf("a release without edge changes restarted the edge (pid %s → %s)", pid, after)
	}
	backups, _ := b.ok("backups", "list")["backups"].([]any)
	pre := ""
	for _, x := range backups {
		if bk := x.(map[string]any); bk["trigger"] == "pre-update" && bk["status"] == "ok" {
			pre, _ = bk["id"].(string)
		}
	}
	if pre == "" || pre != up["backup"] {
		t.Fatalf("no backup before the update (%v): %v", up["backup"], backups)
	}
	t.Logf("UPDATE window: %s", up["summary"])

	// ---- (3) a build that fails its health check rolls back and alerts ----
	host.publish("0.3.0", broken, broken, false)
	_ = b.ok("update", "settings", "--window", "box") // no window: only update apply
	prev = newest()
	r = loads.run("unhealthy release", func() {
		b.ok("update", "apply")
		up = finished("0.3.0", prev)
	})
	if up["status"] != "rolled-back" {
		t.Fatalf("the broken release: %v", up)
	}
	if r.Failed > 0 {
		t.Errorf("unhealthy release: %d of %d requests failed", r.Failed, r.Requests)
	}
	if v := version(); v != "0.2.0" {
		t.Fatalf("after the rollback: version %v", v)
	}
	alerted := false
	deadline := time.Now().Add(2 * time.Minute)
	for !alerted && time.Now().Before(deadline) {
		hist, _ := b.ok("alerts", "list")["history"].([]any)
		for _, x := range hist {
			if e := x.(map[string]any); e["rule"] == "tiffin-update" && strings.Contains(fmt.Sprint(e["summary"]), "0.3.0") {
				alerted = true
			}
		}
		time.Sleep(3 * time.Second)
	}
	if !alerted {
		t.Fatal("the rollback did not alert the owner")
	}
	t.Logf("UPDATE rollback: %s", up["summary"])

	// ---- (4) a release that changed the edge restarts it ----
	host.publish("0.4.0", read(v4), read(v4), true)
	pid, prev = edgePID(), newest()
	r = loads.run("release with edge restart", func() {
		b.ok("update", "apply")
		up = finished("0.4.0", prev)
	})
	if up["status"] != "ok" || up["edgeRestart"] != true {
		t.Fatalf("the edge release: %v", up)
	}
	if after := edgePID(); after == pid {
		t.Errorf("the edge did not restart (pid %s)", pid)
	}
	sum := sha256.Sum256(read(v4))
	if exe := b.inBox(`sudo readlink /proc/$(systemctl show -p MainPID --value tiffin-edge)/exe`); !strings.Contains(exe, hex.EncodeToString(sum[:])[:12]) {
		t.Errorf("the edge runs %s, not the new build", exe)
	}
	if r.Failed > r.Requests/100 {
		t.Errorf("edge restart: %d of %d requests failed", r.Failed, r.Requests)
	}
	t.Logf("UPDATE edge: %s (%d of %d requests failed, max gap %.0f ms)", up["summary"], r.Failed, r.Requests, r.MaxGapMs)

	for _, l := range loads.report {
		t.Logf("SUMMARY %s", l)
	}
	t.Logf("TOTAL %s", time.Since(start).Round(time.Second))
}
