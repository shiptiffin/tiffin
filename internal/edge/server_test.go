package edge

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

	"github.com/libdns/libdns"

	"github.com/btahir/tiffin/internal/edge/switchboard"
)

// shortDir is a temp dir short enough for unix socket paths (macOS: 104 bytes).
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "tfe")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

// startServer runs an edge process's Server in dir until the test ends (or stop).
func startServer(t *testing.T, dir, sb string) (*Server, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{Socket: filepath.Join(dir, EdgeSocket), Control: filepath.Join(dir, ControlSocket), State: filepath.Join(dir, SnapshotFile), Switchboard: sb, Build: "test"}
	if err := s.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	stop := func() { cancel(); s.Wait() }
	t.Cleanup(stop)
	return s, stop
}

// fakeControl is the runtime's side: it wakes "shop/nap" by giving it an
// instance and sending the table, as the runtime does.
type fakeControl struct {
	mu      sync.Mutex
	table   switchboard.Table
	cl      *Client
	port    int
	firstMs float64
}

func (f *fakeControl) get() switchboard.Table {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := json.Marshal(f.table)
	var t switchboard.Table
	_ = json.Unmarshal(raw, &t)
	return t
}

func (f *fakeControl) Wake(ctx context.Context, env string) (bool, error) {
	f.mu.Lock()
	e := f.table.Envs[env]
	e.Sleeping, e.Instances = false, []switchboard.Instance{{Name: "nap-1", Port: f.port}}
	f.mu.Unlock()
	return true, f.cl.SyncTable()
}

func (f *fakeControl) WokeFirstByte(env string, secs float64) {
	f.mu.Lock()
	f.firstMs = secs * 1000
	f.mu.Unlock()
}

func (f *fakeControl) Forward(w http.ResponseWriter, r *http.Request, env, prefix string) {
	io.WriteString(w, "forwarded "+env+" "+r.URL.Path)
}

func appServer(t *testing.T, body string, hold <-chan struct{}) (*httptest.Server, int) {
	t.Helper()
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" && hold != nil {
			<-hold
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(app.Close)
	port, _ := strconv.Atoi(app.URL[strings.LastIndex(app.URL, ":")+1:])
	return app, port
}

func viaSwitchboard(t *testing.T, s *Server, host, path string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", "http://"+s.SwitchboardAddr()+path, nil)
	req.Host = host
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

// TestSnapshotVersionsAndAck: SyncTable returns once the edge serves the
// table; an older snapshot is refused; a client behind the edge's version
// (a clock step back) goes past it; busy and activity come from the edge.
func TestSnapshotVersionsAndAck(t *testing.T) {
	dir := shortDir(t)
	s, _ := startServer(t, dir, "127.0.0.1:0")
	hold := make(chan struct{})
	_, port := appServer(t, "v1", hold)
	ctl := &fakeControl{table: switchboard.Table{
		Hosts: map[string][]switchboard.Route{"shop.box.test": {{Env: "shop/web"}}},
		Envs:  map[string]*switchboard.Env{"shop/web": {Project: "shop", App: "web", Live: "d1", Instances: []switchboard.Instance{{Name: "web-1", Port: port}}}},
	}}
	cl := NewClient(ClientOptions{Socket: s.Socket, Local: true})
	cl.TableSource(ctl.get)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cl.Start(ctx, 5*time.Second)
	if code, _ := viaSwitchboard(t, s, "shop.box.test", "/"); code != http.StatusNotFound {
		t.Fatalf("before the first table: %d", code)
	}
	if err := cl.SyncTable(); err != nil {
		t.Fatal(err)
	}
	if code, body := viaSwitchboard(t, s, "shop.box.test", "/"); code != 200 || body != "v1" {
		t.Fatalf("right after the ack: %d %q", code, body)
	}

	// An older snapshot is refused and changes nothing.
	ack, err := cl.put(ctx, Snapshot{Version: 1, Table: &switchboard.Table{}})
	if _, stale := err.(*staleError); !stale || ack.Version <= 1 {
		t.Fatalf("stale snapshot: %v %+v", err, ack)
	}
	if code, _ := viaSwitchboard(t, s, "shop.box.test", "/"); code != 200 {
		t.Fatal("a refused snapshot changed the table")
	}
	// The edge is ahead of the client (it served a later one): the client goes past it.
	far := uint64(time.Now().Add(time.Hour).UnixNano())
	t1 := ctl.get()
	if _, err := cl.put(ctx, Snapshot{Version: far, Table: &t1}); err != nil {
		t.Fatal(err)
	}
	if err := cl.SyncTable(); err != nil {
		t.Fatalf("after the edge went ahead: %v", err)
	}
	if h, _ := cl.Status(); h.Version <= far {
		t.Fatalf("version %d, want past %d", h.Version, far)
	}

	// A request in flight counts until it ends; activity is the edge's.
	done := make(chan struct{})
	go func() { viaSwitchboard(t, s, "shop.box.test", "/slow"); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for n, _ := cl.Busy([]string{"web-1"}); n != 1; n, _ = cl.Busy([]string{"web-1"}) {
		if time.Now().After(deadline) {
			t.Fatal("the request in flight is not counted")
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(hold)
	<-done
	if n, err := cl.Busy([]string{"web-1"}); err != nil || n != 0 {
		t.Fatalf("after the request: %d %v", n, err)
	}
	if seen, err := cl.Activity(); err != nil || time.Since(seen["shop/web"]) > time.Minute {
		t.Fatalf("activity: %v %v", seen, err)
	}
}

// TestEdgeServesAloneFromItsSavedSnapshot: a restarted edge serves its saved
// snapshot through Caddy while the control plane is down; a sleeping app
// and live progress get a 503 with Retry-After; activity waits in the edge;
// once the control plane is back a request wakes the app.
func TestEdgeServesAloneFromItsSavedSnapshot(t *testing.T) {
	dir := shortDir(t)
	_, port := appServer(t, "hello from the app", nil)
	s1, stop1 := startServer(t, dir, "127.0.0.1:0")
	sb := s1.SwitchboardAddr()
	ctl := &fakeControl{port: port, table: switchboard.Table{
		Hosts: map[string][]switchboard.Route{"shop.box.test": {{Env: "shop/web"}}, "nap.box.test": {{Env: "shop/nap"}}},
		Envs: map[string]*switchboard.Env{
			"shop/web": {Project: "shop", App: "web", Live: "d1", Instances: []switchboard.Instance{{Name: "web-1", Port: port}}},
			"shop/nap": {Project: "shop", App: "nap", Live: "d1", Sleeping: true},
		},
	}}
	cfg := Config{Domain: "box.test", Upstream: "127.0.0.1:9", DataDir: filepath.Join(dir, "caddy"), Internal: true, HTTPPort: 18080, HTTPSPort: 18443}
	routes := []Route{{Host: "shop.box.test", Upstream: sb}, {Host: "nap.box.test", Upstream: sb}}

	cpCtx, cpStop := context.WithCancel(context.Background())
	if err := ServeControl(cpCtx, s1.Control, ctl); err != nil {
		t.Fatal(err)
	}
	cl := NewClient(ClientOptions{Socket: s1.Socket, Base: cfg, Local: true})
	ctl.cl = cl
	cl.TableSource(ctl.get)
	cl.Start(cpCtx, 5*time.Second)
	if err := cl.SetRoutes(routes); err != nil {
		t.Fatal(err)
	}
	ca, err := RootCAPEM(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	c := client(t, ca, cfg.HTTPSPort)
	if _, body := get(t, c, "https://shop.box.test:18443/"); body != "hello from the app" {
		t.Fatalf("through the edge: %q", body)
	}

	// The control plane goes away, then the edge restarts.
	cpStop()
	stop1()
	s2, _ := startServer(t, dir, sb)
	if res, body := get(t, c, "https://shop.box.test:18443/"); res.StatusCode != 200 || body != "hello from the app" {
		t.Fatalf("a restarted edge with no control plane: %d %q", res.StatusCode, body)
	}
	res, body := get(t, c, "https://nap.box.test:18443/")
	if res.StatusCode != http.StatusServiceUnavailable || res.Header.Get("Retry-After") == "" || !strings.Contains(body, "restarting") {
		t.Fatalf("a sleeping app without the control plane: %d %q %v", res.StatusCode, body, res.Header)
	}
	if res, _ := get(t, c, "https://shop.box.test:18443"+switchboard.LivePrefix+"/x"); res.StatusCode != http.StatusServiceUnavailable || res.Header.Get("Retry-After") == "" {
		t.Fatalf("live progress without the control plane: %d", res.StatusCode)
	}
	cl2 := NewClient(ClientOptions{Socket: s2.Socket, Base: cfg, Local: true})
	if seen, err := cl2.Activity(); err != nil || time.Since(seen["shop/web"]) > time.Minute {
		t.Fatalf("activity kept by the edge: %v %v", seen, err)
	}

	// The control plane is back: it catches the edge up, and a request wakes the app.
	cpCtx, cpStop = context.WithCancel(context.Background())
	defer cpStop()
	if err := ServeControl(cpCtx, s2.Control, ctl); err != nil {
		t.Fatal(err)
	}
	ctl.cl = cl2
	cl2.TableSource(ctl.get)
	cl2.Start(cpCtx, 5*time.Second)
	if err := cl2.SetRoutes(routes); err != nil {
		t.Fatal(err)
	}
	if res, body := get(t, c, "https://nap.box.test:18443/"); res.StatusCode != 200 || body != "hello from the app" {
		t.Fatalf("waking: %d %q", res.StatusCode, body)
	}
	if res, body := get(t, c, "https://shop.box.test:18443"+switchboard.LivePrefix+"/x"); res.StatusCode != 200 || body != "forwarded shop/web "+switchboard.LivePrefix+"/x" {
		t.Fatalf("live progress: %d %q", res.StatusCode, body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		ctl.mu.Lock()
		ms := ctl.firstMs
		ctl.mu.Unlock()
		if ms > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the woken app's first byte was not reported")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestListenersFromSystemd(t *testing.T) {
	s := &Server{fds: map[string]int{"tcp/443": 3, "tcp/80": 4, "udp/443": 5}}
	in := `{"apps":{"http":{"servers":{"https":{"listen":[":443"],"protocols":["h1","h2","h3"]},"http":{"listen":[":80"]},"other":{"listen":[":9000"]}}}}}`
	out, err := s.listeners(json.RawMessage(in))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Listen          []string   `json:"listen"`
					ListenProtocols [][]string `json:"listen_protocols"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatal(err)
	}
	srv := cfg.Apps.HTTP.Servers
	if got := srv["https"]; strings.Join(got.Listen, ",") != "fd/3,fdgram/5" || len(got.ListenProtocols) != 2 ||
		strings.Join(got.ListenProtocols[0], ",") != "h1,h2" || strings.Join(got.ListenProtocols[1], ",") != "h3" {
		t.Errorf("https: %+v", got)
	}
	if got := srv["http"]; strings.Join(got.Listen, ",") != "fd/4" || strings.Join(got.ListenProtocols[0], ",") != "h1,h2" {
		t.Errorf("http: %+v", got)
	}
	if got := srv["other"]; strings.Join(got.Listen, ",") != ":9000" {
		t.Errorf("a port systemd did not pass: %+v", got)
	}
	if same, _ := (&Server{}).listeners(json.RawMessage(in)); string(same) != in {
		t.Error("without systemd's sockets the config must stay as it is")
	}
}

type fakeDNS struct {
	mu   sync.Mutex
	recs []libdns.Record
}

func (f *fakeDNS) AppendRecords(_ context.Context, _ string, recs []libdns.Record) ([]libdns.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recs = append(f.recs, recs...)
	return recs, nil
}

func (f *fakeDNS) DeleteRecords(_ context.Context, _ string, recs []libdns.Record) ([]libdns.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recs = nil
	return recs, nil
}

// The edge solves DNS-01 challenges through the provider the control plane holds.
func TestDNSChallengeThroughTheControlPlane(t *testing.T) {
	dir := shortDir(t)
	f := &fakeDNS{}
	providersMu.Lock()
	providers["test-key"] = f
	providersMu.Unlock()
	if err := ServeControl(t.Context(), filepath.Join(dir, ControlSocket), nil); err != nil {
		t.Fatal(err)
	}
	d := &controlDNS{c: newControlClient(filepath.Join(dir, ControlSocket)), key: "test-key"}
	got, err := d.AppendRecords(t.Context(), "box.test.", []libdns.Record{libdns.TXT{Name: "_acme-challenge", Text: "token", TTL: time.Minute}})
	if err != nil || len(got) != 1 {
		t.Fatalf("append: %v %v", got, err)
	}
	if txt, ok := f.recs[0].(libdns.TXT); !ok || txt.Text != "token" || txt.Name != "_acme-challenge" {
		t.Fatalf("the provider got %#v", f.recs)
	}
	if _, err := d.DeleteRecords(t.Context(), "box.test.", got); err != nil || len(f.recs) != 0 {
		t.Fatalf("delete: %v %v", f.recs, err)
	}
	// Without the control plane the challenge fails (certmagic retries later).
	d = &controlDNS{c: newControlClient(filepath.Join(dir, "gone.sock")), key: "test-key"}
	if _, err := d.AppendRecords(t.Context(), "box.test.", got); err == nil {
		t.Fatal("no control plane, yet the record was set")
	}
}
