package observe

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBuildLogLabels(t *testing.T) {
	l, ok := appLogLabels("/l/apps", "/l/apps/shop/web/build/dep_01.log")
	if !ok || l != (AppLog{Project: "shop", App: "web", Deploy: "dep_01", Build: true}) {
		t.Fatalf("%+v %v", l, ok)
	}
	// Environment folders are prod and pr-<preview>, so a preview named
	// build is pr-build: still an app log.
	if l, ok := appLogLabels("/l/apps", "/l/apps/shop/web/pr-build/dep_01.1.log"); !ok || l.Build || l.Env != "pr-build" || l.Instance != "1" {
		t.Fatalf("%+v", l)
	}
}

func TestBuildLogRecord(t *testing.T) {
	l := AppLog{Project: "shop", App: "web", Deploy: "dep_01", Build: true}
	r := buildLogRecord([]byte(`{"time":"2026-10-06T10:00:01.25+02:00","env":"pr-7","log":"#12 ERROR: process \"/bin/sh -c npm run build\" did not complete successfully"}`), l)
	want := map[string]any{"source": "build", "app": "web", "project": "shop", "deploy": "dep_01", "env": "pr-7",
		"_time": "2026-10-06T08:00:01.25Z", "level": "error", "_msg": `#12 ERROR: process "/bin/sh -c npm run build" did not complete successfully`}
	if len(r) != len(want) {
		t.Fatalf("got %v", r)
	}
	for k, v := range want {
		if r[k] != v {
			t.Errorf("%s = %v, want %v", k, r[k], v)
		}
	}
	r = buildLogRecord([]byte(`{"time":"2026-10-06T10:00:02Z","env":"prod","log":"\u001b[32m==> built in 12.0s\u001b[0m"}`), l)
	if r["_msg"] != "==> built in 12.0s" || r["env"] != "prod" || r["level"] != nil {
		t.Fatalf("got %v", r)
	}
	if r := buildLogRecord([]byte(`npm warn deprecated glob@7`), l); r["_msg"] != "npm warn deprecated glob@7" || r["level"] != "warning" || r["_time"] != nil {
		t.Fatalf("plain line: %v", r)
	}
	// "0 errors" in build output is not an error.
	if r := buildLogRecord([]byte(`{"time":"2026-10-06T10:00:02Z","env":"prod","log":"#9 4.1 Found 0 errors."}`), l); r["level"] != nil {
		t.Fatalf("got %v", r)
	}
}

// fakeVL records what the shipper sends to /insert/jsonline.
type fakeVL struct {
	mu   sync.Mutex
	sent []pushed
}

type pushed struct {
	account, stream string
	rec             map[string]any
}

func (f *fakeVL) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/insert/jsonline" {
		http.NotFound(w, r)
		return
	}
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil {
			f.sent = append(f.sent, pushed{r.Header.Get("AccountID"), r.URL.Query().Get("_stream_fields"), rec})
		}
	}
}

func (f *fakeVL) build() []pushed {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []pushed
	for _, p := range f.sent {
		if p.rec["source"] == "build" {
			out = append(out, p)
		}
	}
	return out
}

// TestBuildLinesShipped follows a build file the way the runtime writes it
// and checks every line reaches the store in the project's tenant,
// labelled, timed and levelled; a pruned file's tail position is forgotten.
func TestBuildLinesShipped(t *testing.T) {
	fake := &fakeVL{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	h := newHarness(t, &Victoria{VM: srv.URL, VL: srv.URL})
	root := filepath.Join(t.TempDir(), "apps")
	dir := filepath.Join(root, "shop", "web", "build")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "dep_01.log")
	lines := []string{
		`{"time":"2026-10-06T10:00:00Z","env":"prod","log":"==> deploy dep_01 of shop/web, queued 2026-10-06T09:59:59Z"}`,
		`{"time":"2026-10-06T10:00:01Z","env":"prod","log":"#8 2.1 npm warn deprecated inflight@1.0.6"}`,
		`{"time":"2026-10-06T10:00:02Z","env":"prod","log":"#8 3.4 Error: ENOENT: no such file or directory"}`,
		`{"time":"2026-10-06T10:00:03Z","env":"prod","log":"==> FAILED: build: exit status 1"}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.m.batch.Run(ctx)
	go h.m.runAppLogs(ctx, root)

	var got []pushed
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if got = fake.build(); len(got) >= len(lines) {
			break
		}
	}
	if len(got) != len(lines) {
		t.Fatalf("shipped %d build lines, want %d: %v", len(got), len(lines), got)
	}
	tenant, err := h.m.store.TenantFor(ctx, "shop")
	if err != nil || tenant == 0 {
		t.Fatal(tenant, err)
	}
	wantLevel := []any{nil, "warning", "error", "error"}
	for i, p := range got {
		if p.account != strconv.FormatUint(uint64(tenant), 10) || p.stream != "source,app,env" {
			t.Errorf("line %d: tenant %s stream %s", i, p.account, p.stream)
		}
		r := p.rec
		if r["app"] != "web" || r["project"] != "shop" || r["deploy"] != "dep_01" || r["env"] != "prod" || r["level"] != wantLevel[i] {
			t.Errorf("line %d: %v", i, r)
		}
		if want := "2026-10-06T10:00:0" + string(rune('0'+i)) + "Z"; r["_time"] != want {
			t.Errorf("line %d: _time %v, want %s", i, r["_time"], want)
		}
	}
	if _, ok := h.m.store.loadPos(path); !ok {
		t.Fatal("no tail position saved")
	}
	// Pruned: the next discovery pass (every 5s) forgets the position.
	os.Remove(path)
	for deadline := time.Now().Add(12 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if _, ok := h.m.store.loadPos(path); !ok {
			return
		}
	}
	t.Fatal("tail position of a removed file kept")
}
