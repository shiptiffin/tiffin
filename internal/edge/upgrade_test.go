package edge

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// savedSnapshot writes the snapshot an edge would have saved for cfg, its
// Caddy storage module switched to storage (an earlier build's).
func savedSnapshot(t *testing.T, path string, cfg Config, storage string) {
	t.Helper()
	r, err := render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var c map[string]any
	if err := json.Unmarshal(r.Config, &c); err != nil {
		t.Fatal(err)
	}
	c["storage"].(map[string]any)["module"] = storage
	if r.Config, err = json.Marshal(c); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, Snapshot{Version: 1, Caddy: &r.Rendered}); err != nil {
		t.Fatal(err)
	}
}

// waitServes polls url until it answers 200 with body, for up to max.
func waitServes(t *testing.T, c *http.Client, url, body string, max time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	var last string
	for time.Since(start) < max {
		res, err := c.Get(url)
		if err == nil {
			b := make([]byte, 256)
			n, _ := res.Body.Read(b)
			res.Body.Close()
			if res.StatusCode == 200 && string(b[:n]) == body {
				return time.Since(start)
			}
			last = res.Status + " " + string(b[:n])
		} else {
			last = err.Error()
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s not served within %s: %s", url, max, last)
	return 0
}

// TestSavedSnapshotOfAnEarlierBuildLoads: snapshots saved before the edge
// had its own storage module name Caddy's file_system storage; an upgraded
// edge still serves them alone.
func TestSavedSnapshotOfAnEarlierBuildLoads(t *testing.T) {
	dir := shortDir(t)
	_, port := appServer(t, "from the saved snapshot", nil)
	cfg := Config{Domain: "box.test", Upstream: "127.0.0.1:9", DataDir: filepath.Join(dir, "caddy"), Internal: true, HTTPPort: 18086, HTTPSPort: 18449,
		Routes: []Route{{Host: "shop.box.test", Upstream: "127.0.0.1:" + strconv.Itoa(port)}}}
	savedSnapshot(t, filepath.Join(dir, SnapshotFile), cfg, "file_system")
	s, _ := startServer(t, dir, "127.0.0.1:0")
	h := hello(t, s)
	if !h.Caddy || h.Version != 1 {
		t.Fatalf("the saved snapshot is not served: %+v", h)
	}
	ca, err := RootCAPEM(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	waitServes(t, client(t, ca, cfg.HTTPSPort), "https://shop.box.test:18449/", "from the saved snapshot", 5*time.Second)
}

// TestUpgradedEdgeGetsTheConfigTheOldOneRefused: an upgrade starts the
// control plane on the new build first. Its config uses a module the old
// edge lacks, so the old edge refuses it; then the edge restarts on the new
// build and its saved snapshot (the old build's) names a module the new
// build lacks. The control plane offers the restarted edge the refused
// config at once rather than leaving it with no config until the next
// route change.
func TestUpgradedEdgeGetsTheConfigTheOldOneRefused(t *testing.T) {
	dir := shortDir(t)
	_, port := appServer(t, "after the upgrade", nil)
	cfg := Config{Domain: "box.test", Upstream: "127.0.0.1:9", DataDir: filepath.Join(dir, "caddy"), Internal: true, HTTPPort: 18086, HTTPSPort: 18449}
	routes := []Route{{Host: "shop.box.test", Upstream: "127.0.0.1:" + strconv.Itoa(port)}}
	old := cfg
	old.Routes = routes
	savedSnapshot(t, filepath.Join(dir, SnapshotFile), old, "gone_in_this_build")

	// The old edge: it refuses every Caddy config (a module it predates).
	ln, err := listenUnix(filepath.Join(dir, EdgeSocket))
	if err != nil {
		t.Fatal(err)
	}
	oldStarted := time.Now().Add(-time.Hour)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/hello", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, Hello{Protocol: Protocol, Build: "old", PID: os.Getpid(), Version: 1, Caddy: true, Started: oldStarted})
	})
	mux.HandleFunc("PUT /v1/snapshot", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "loading module 'tiffin': unknown module: caddy.storage.tiffin", http.StatusUnprocessableEntity)
	})
	oldEdge := &http.Server{Handler: mux}
	go func() { _ = oldEdge.Serve(ln) }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cl := NewClient(ClientOptions{Socket: filepath.Join(dir, EdgeSocket), Base: cfg, Local: true})
	cl.Start(ctx, 5*time.Second)
	if err := cl.SetRoutes(routes); err == nil || !strings.Contains(err.Error(), "unknown module") {
		t.Fatalf("the old edge took the new config: %v", err)
	}
	oldEdge.Close()

	// The edge restarts on the new build; its saved snapshot fails to load.
	s, _ := startServer(t, dir, "127.0.0.1:0")
	if h := hello(t, s); h.Caddy {
		t.Fatalf("the old build's snapshot loaded: %+v", h)
	}
	ca, err := RootCAPEM(cfg.DataDir)
	for i := 0; err != nil && i < 100; i++ { // the CA appears with the first config
		time.Sleep(50 * time.Millisecond)
		ca, err = RootCAPEM(cfg.DataDir)
	}
	if err != nil {
		t.Fatal(err)
	}
	took := waitServes(t, client(t, ca, cfg.HTTPSPort), "https://shop.box.test:18449/", "after the upgrade", 8*time.Second)
	t.Logf("the restarted edge served the new config after %s", took)
}

func hello(t *testing.T, s *Server) Hello {
	t.Helper()
	var h Hello
	c := NewClient(ClientOptions{Socket: s.Socket, Local: true})
	if err := c.get(context.Background(), c.hc, "/v1/hello", &h); err != nil {
		t.Fatal(err)
	}
	return h
}
