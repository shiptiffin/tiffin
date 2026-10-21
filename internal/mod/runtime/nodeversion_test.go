package runtime

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/manifest"
)

// nodeIndex is a slice of nodejs.org/dist/index.json, newest first.
const nodeIndex = `[
{"version":"v25.0.0-rc.1"},
{"version":"v24.22.1"},{"version":"v24.22.0"},{"version":"v24.21.0"},
{"version":"v22.23.4"},{"version":"v22.23.3"},{"version":"v20.19.9"}]`

func nodeIndexServer(t *testing.T) (*releaseList, *atomic.Int32, *atomic.Bool) {
	t.Helper()
	var hits atomic.Int32
	var down atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		if down.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		w.Write([]byte(nodeIndex))
	}))
	t.Cleanup(srv.Close)
	return &releaseList{url: srv.URL, ttl: time.Hour, client: srv.Client()}, &hits, &down
}

func TestNewestMatch(t *testing.T) {
	l, _, _ := nodeIndexServer(t)
	vs, err := l.get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for spec, want := range map[string]string{
		"24":       "24.22.1",
		"24.x":     "24.22.1",
		"v24":      "24.22.1",
		" 24.x\n":  "24.22.1",
		"^22.1":    "22.23.4",
		">=20 <23": "22.23.4",
		"~24.21":   "24.21.0",
		"24.22.1":  "", // already one release
		"v24.21.0": "",
		"lts/*":    "", // an alias: mise knows it
		"node":     "",
		"30":       "", // nothing matches
		"":         "",
		">=25":     "", // only a release candidate
	} {
		if got := newestMatch(spec, vs); got != want {
			t.Errorf("newestMatch(%q) = %q, want %q", spec, got, want)
		}
	}
}

func TestReleaseListCachesAndKeepsLastList(t *testing.T) {
	l, hits, down := nodeIndexServer(t)
	ctx := context.Background()
	for range 3 {
		if _, err := l.get(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("fetched %d times within the ttl, want 1", hits.Load())
	}
	l.at = time.Now().Add(-2 * time.Hour)
	down.Store(true)
	if vs, err := l.get(ctx); err != nil || len(vs) != 6 {
		t.Fatalf("a failed refetch should keep the last list: %d releases, %v", len(vs), err)
	}
	empty := &releaseList{url: l.url, ttl: time.Hour, client: l.client}
	if _, err := empty.get(ctx); err == nil {
		t.Fatal("no list and a failed fetch should be an error")
	}
}

// An app that names a Node.js range builds with the newest release in it:
// its version files say so in the build's copy of the source, so a rebuild
// picks up new releases instead of the one the build cache found first.
func TestRailpackPinsNodeRange(t *testing.T) {
	l, _, down := nodeIndexServer(t)
	pkg := "{\n  \"name\": \"shop\",\n  \"engines\": { \"node\": \"24.x\", \"bun\": \">=1.4\" },\n  \"scripts\": {\"start\": \"node index.js\"}\n}\n"
	files := map[string]string{"package.json": pkg, ".nvmrc": "24\n", ".node-version": "22.23.3\n"}

	bin, log := fakeTools(t)
	b := &boxBuilder{eng: newFakeEngine(), binDir: bin, memoryMB: 2048, nodeReleases: l}
	req := buildReq(t, manifest.App{Framework: manifest.FrameworkBun}, files)
	if _, err := b.Build(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		raw, _ := os.ReadFile(filepath.Join(req.SrcDir, name))
		return string(raw)
	}
	if got, want := read("package.json"), strings.Replace(pkg, `"node": "24.x"`, `"node": "24.22.1"`, 1); got != want {
		t.Errorf("package.json:\n%s\nwant only engines.node changed:\n%s", got, want)
	}
	if got := read(".nvmrc"); got != "24.22.1\n" {
		t.Errorf(".nvmrc = %q", got)
	}
	if got := read(".node-version"); got != "22.23.3\n" {
		t.Errorf(".node-version named one release and should stay: %q", got)
	}
	if strings.Contains(readCalls(t, log), "RAILPACK_NODE_VERSION") {
		t.Error("the app chose its Node.js: the box's own pin should stay out")
	}
	if out := req.Log.(*bytes.Buffer).String(); !strings.Contains(out, `==> Node.js: package.json asks for "24.x"; building with 24.22.1`) {
		t.Errorf("the build log does not say which release:\n%s", out)
	}

	// Without the release list the source stays as the app wrote it.
	down.Store(true)
	b.nodeReleases = &releaseList{url: l.url, ttl: time.Hour, client: l.client}
	req = buildReq(t, manifest.App{Framework: manifest.FrameworkBun}, files)
	if _, err := b.Build(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if read("package.json") != pkg || read(".nvmrc") != "24\n" {
		t.Error("the source changed although no release list could be read")
	}
	if out := req.Log.(*bytes.Buffer).String(); !strings.Contains(out, "could not read the release list") {
		t.Errorf("the build log does not say the range was left to the build:\n%s", out)
	}
}
