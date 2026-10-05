package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/btahir/tiffin/internal/boxfile"
)

// projects import checks the archive on this computer before uploading.
func TestProjectImportChecksLocally(t *testing.T) {
	env := newEnv(t)
	box := writeTestArchive(t, &boxfile.Manifest{Kind: boxfile.ManifestKind, Format: boxfile.FormatVersion})
	if code, out, _ := run(t, env, "projects", "import", box); code != ExitInvalid || !strings.Contains(string(out), "tiffin box import") {
		t.Errorf("a box export: exit %d %s", code, out)
	}
	proj := writeTestArchive(t, &boxfile.Manifest{Kind: boxfile.ProjectKind, Project: "shop", Format: boxfile.FormatVersion})
	if code, out, _ := run(t, env, "box", "import", proj); code != ExitInvalid || !strings.Contains(string(out), "tiffin projects import") {
		t.Errorf("a project export into box import: exit %d %s", code, out)
	}
	newer := writeTestArchive(t, &boxfile.Manifest{Kind: boxfile.ProjectKind, Project: "shop", Format: boxfile.FormatVersion + 1})
	if code, out, _ := run(t, env, "projects", "import", newer); code != ExitInvalid || !strings.Contains(string(out), "newer Tiffin") {
		t.Errorf("newer: exit %d %s", code, out)
	}
	if code, out, _ := run(t, env, "projects", "import", proj); code != ExitInvalid || !strings.Contains(string(out), "works with a box") {
		t.Errorf("local home: exit %d %s", code, out)
	}
	if code, out, _ := run(t, env, "projects", "move", "shop"); code != ExitInvalid || !strings.Contains(string(out), "--to") {
		t.Errorf("move without --to: exit %d %s", code, out)
	}
}

// stubBoxes are two boxes' APIs, as far as a move uses them.
type stubBoxes struct {
	mu        sync.Mutex
	archive   []byte
	received  []byte
	failApply bool
	stopped   string
	dnsSet    bool
	applied   map[string]any
}

func (s *stubBoxes) src() http.Handler {
	sum := sha256.Sum256(s.archive)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/projects/shop/exports", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["includeSecrets"] != true || body["withHistory"] != true {
			http.Error(w, "a move carries secrets and History", 400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "px_1", "project": "shop", "status": "pending", "download": "/v1/projects/shop/exports/px_1/download"})
	})
	mux.HandleFunc("GET /v1/projects/shop/exports/px_1/download", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(s.archive) })
	mux.HandleFunc("GET /v1/projects/shop/exports/px_1", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "px_1", "status": "done", "sha256": hex.EncodeToString(sum[:]), "sizeBytes": len(s.archive),
			"apps": []map[string]any{{"app": "web", "status": "live", "url": "https://shop.a.example"}}})
	})
	mux.HandleFunc("POST /v1/projects/shop/stop", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		s.stopped = body["intent"]
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"applied": true})
	})
	return mux
}

func (s *stubBoxes) dst() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/projects", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`[{"name":"blog"}]`)) })
	mux.HandleFunc("POST /v1/project-imports", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sum := sha256.Sum256(b)
		s.mu.Lock()
		s.received = b
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "pj_1", "status": "uploaded", "sha256": hex.EncodeToString(sum[:])})
	})
	mux.HandleFunc("POST /v1/project-imports/pj_1/apply", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		_ = json.NewDecoder(r.Body).Decode(&s.applied)
		s.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "pj_1", "status": "running"})
	})
	mux.HandleFunc("GET /v1/project-jobs/pj_1", func(w http.ResponseWriter, r *http.Request) {
		if s.failApply {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "pj_1", "status": "failed", "error": "postgres: boom"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "pj_1", "status": "done", "project": "shop", "healthy": true,
			"apps": []map[string]any{{"app": "web", "status": "live", "url": "https://shop.b.example"}}})
	})
	mux.HandleFunc("GET /v1/projects/shop/domains", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"domain":"example.com","managedBy":"cloudflare","records":[{"name":"example.com","type":"A","value":"203.0.113.9"}]}]`))
	})
	mux.HandleFunc("PUT /v1/dns/records", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.dnsSet = true
		s.mu.Unlock()
		_, _ = w.Write([]byte(`{"set":[]}`))
	})
	return mux
}

// TestMoveProject: the export streams from one box into the other, the
// project stops on the old box only once the new one has it, and custom
// domains are re-pointed when asked.
func TestMoveProject(t *testing.T) {
	ctx := context.Background()
	s := &stubBoxes{archive: []byte(strings.Repeat("archive bytes ", 100000))}
	a, b := httptest.NewServer(s.src()), httptest.NewServer(s.dst())
	defer a.Close()
	defer b.Close()
	src, dst := &client{base: a.URL, close: func() error { return nil }}, &client{base: b.URL, close: func() error { return nil }}
	res, err := moveProject(ctx, src, dst, moveOptions{project: "shop", fromName: "old", toName: "new", updateDNS: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(s.received) != string(s.archive) || s.applied["intent"] != "Moved from old" || s.stopped != "Moved to new" || !s.dnsSet {
		t.Fatalf("move: received %d bytes, applied %v, stopped %q, dns %v", len(s.received), s.applied, s.stopped, s.dnsSet)
	}
	if len(res.Addresses) != 1 || res.Addresses[0].Before != "https://shop.a.example" || res.Addresses[0].After != "https://shop.b.example" ||
		!res.Stopped || len(res.Domains) != 1 || !res.Domains[0].Updated || !strings.Contains(res.Next, "tiffin projects destroy shop") {
		t.Fatalf("result: %+v", res)
	}

	// A failed import leaves the project running on the old box.
	s.stopped, s.failApply = "", true
	if _, err := moveProject(ctx, src, dst, moveOptions{project: "shop", fromName: "old", toName: "new"}); err == nil || !strings.Contains(err.Error(), "still runs on old") {
		t.Fatalf("failed import: %v", err)
	}
	if s.stopped != "" {
		t.Fatal("stopped on the old box after a failed import")
	}
	// A name the new box already has is refused before anything moves.
	if _, err := moveProject(ctx, src, dst, moveOptions{project: "blog", fromName: "old", toName: "new"}); err == nil || !strings.Contains(err.Error(), "already has") {
		t.Fatalf("name taken: %v", err)
	}
	if _, err := moveProject(ctx, src, src, moveOptions{project: "shop", fromName: "old", toName: "old"}); err == nil {
		t.Fatal("a move onto the same box")
	}
}
