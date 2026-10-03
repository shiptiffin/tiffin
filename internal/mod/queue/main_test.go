package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The tests run against a real Postgres 18 (embedded-postgres downloads a
// pinned build once into ~/.embedded-postgres-go). Each test gets its own
// database.

var (
	pgBase string // postgres://...@127.0.0.1:port/
	dbSeq  atomic.Int64
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	if v := os.Getenv("TIFFIN_QUEUE_TEST_PG"); v != "" {
		pgBase = v
		return m.Run()
	}
	dir, err := os.MkdirTemp("", "tiffin-queue-pg-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	port := freePort()
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V18).Port(uint32(port)).
		RuntimePath(filepath.Join(dir, "run")).DataPath(filepath.Join(dir, "data")).
		Username("tiffin").Password("tiffin").Database("postgres").
		StartParameters(map[string]string{"max_connections": "400", "fsync": "off"}).
		StartTimeout(60 * time.Second).Logger(io.Discard))
	if err := pg.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "embedded postgres:", err)
		return 1
	}
	defer pg.Stop()
	pgBase = fmt.Sprintf("postgres://tiffin:tiffin@127.0.0.1:%d/", port)
	return m.Run()
}

func freePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// newDB creates a fresh database and returns its DSN.
func newDB(t testing.TB) string {
	t.Helper()
	name := fmt.Sprintf("t%d_%d", os.Getpid(), dbSeq.Add(1))
	ctx := context.Background()
	c, err := pgx.Connect(ctx, pgBase+"postgres")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	if _, err := c.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	return pgBase + name
}

type testEngine struct {
	*Engine
	t   testing.TB
	dsn string
}

func newEngine(t testing.TB, mod func(*Config)) *testEngine {
	t.Helper()
	dsn := newDB(t)
	return startEngine(t, dsn, mod)
}

func startEngine(t testing.TB, dsn string, mod func(*Config)) *testEngine {
	t.Helper()
	cfg := Config{DSN: dsn, RetryBase: 50 * time.Millisecond, PublicURL: "https://dashboard.tiffin.localhost",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if testing.Verbose() {
		cfg.Log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	if mod != nil {
		mod(&cfg)
	}
	e, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	e.Start(context.Background())
	select {
	case <-e.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("engine did not start")
	}
	te := &testEngine{Engine: e, t: t, dsn: dsn}
	t.Cleanup(func() { e.Close() })
	return te
}

// app is a fake app: handlers by path, every delivery verified and recorded.
type app struct {
	t        testing.TB
	srv      *httptest.Server
	mu       sync.Mutex
	handlers map[string]http.HandlerFunc
	got      []deliveryBody
	badSig   atomic.Int64
	secret   func() string
}

func newApp(t testing.TB, e *Engine, project string) *app {
	a := &app{t: t, handlers: map[string]http.HandlerFunc{}}
	a.secret = func() string {
		_, s, _ := e.cfg.Keys.Get(context.Background(), project)
		return s
	}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if !Verify(a.secret(), r.Header.Get(HeaderSignature), raw, time.Now(), 5*time.Minute) {
			a.badSig.Add(1)
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		var b deliveryBody
		_ = json.Unmarshal(raw, &b)
		a.mu.Lock()
		a.got = append(a.got, b)
		h := a.handlers[r.URL.Path]
		a.mu.Unlock()
		if h == nil {
			w.WriteHeader(200)
			return
		}
		r.Body = io.NopCloser(bytesReader(raw))
		h(w, r)
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *app) handle(path string, h http.HandlerFunc) {
	a.mu.Lock()
	a.handlers[path] = h
	a.mu.Unlock()
}

func (a *app) url(path string) string { return a.srv.URL + path }

func (a *app) deliveries() []deliveryBody {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]deliveryBody(nil), a.got...)
}

func eventually(t testing.TB, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", d, what)
}

func (te *testEngine) job(project, id string) *Job {
	te.t.Helper()
	n, err := ParseJobID(id)
	if err != nil {
		te.t.Fatal(err)
	}
	j, err := te.GetJob(context.Background(), project, n)
	if err != nil {
		te.t.Fatal(err)
	}
	return j
}

func (te *testEngine) waitState(project, id, state string, d time.Duration) *Job {
	te.t.Helper()
	var j *Job
	eventually(te.t, d, id+" to be "+state, func() bool {
		j = te.job(project, id)
		return j.State == state
	})
	return j
}

func (te *testEngine) configure(project string, c QueueConfig) {
	te.t.Helper()
	if _, err := te.ConfigureQueue(context.Background(), project, c); err != nil {
		te.t.Fatal(err)
	}
}

func (te *testEngine) send(project string, r SendRequest) *SendResult {
	te.t.Helper()
	res, err := te.Send(context.Background(), project, r)
	if err != nil {
		te.t.Fatal(err)
	}
	return res
}

func pgxpoolNew(ctx context.Context, dsn string) (*pgxpool.Pool, error) { return pgxpool.New(ctx, dsn) }
