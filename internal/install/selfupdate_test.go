package install

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/state"
)

// fakeService stands in for systemd: "restarting" reads the build the
// symlink points at and serves /v1/health for it — unless the file says
// BROKEN, in which case nothing answers.
type fakeService struct {
	mu   sync.Mutex
	link string
	srv  *http.Server
	addr string
	// broken runs when a BROKEN build starts, before it crashes.
	broken func()
}

func (f *fakeService) stop(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.srv != nil {
		f.srv.Close()
		f.srv = nil
	}
	return nil
}

func (f *fakeService) restart(ctx context.Context) error {
	_ = f.stop(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	target, err := os.Readlink(f.link)
	if err != nil {
		return err
	}
	body, _ := os.ReadFile(target)
	if strings.HasPrefix(string(body), "BROKEN") {
		if f.broken != nil {
			f.broken()
		}
		return nil // the process crashes on start; restart itself "succeeds"
	}
	sum, _ := FileSHA(target)
	ln, err := net.Listen("tcp", f.addr)
	if err != nil {
		return err
	}
	f.srv = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"status":"ok","build":%q}`, sum)
	})}
	go f.srv.Serve(ln)
	return nil
}

func setup(t *testing.T) (*Updater, *fakeService, string) {
	t.Helper()
	dir := t.TempDir()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	f := &fakeService{link: filepath.Join(dir, "bin", "tiffin"), addr: addr}
	_ = os.MkdirAll(filepath.Dir(f.link), 0o755)
	t.Cleanup(func() {
		if f.srv != nil {
			f.srv.Close()
		}
	})
	u := &Updater{
		VersionsDir: filepath.Join(dir, "versions"),
		BinLink:     f.link,
		HealthURL:   "http://" + addr + "/v1/health",
		Timeout:     2 * time.Second,
		Restart:     f.restart,
		Stop:        f.stop,
		Logs:        func(context.Context) string { return "(logs)" },
		Progress:    func(string) {},
	}
	return u, f, dir
}

func build(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func current(t *testing.T, u *Updater) string {
	t.Helper()
	target, err := os.Readlink(u.BinLink)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(target)
	return string(b)
}

func TestUpdateAndRollback(t *testing.T) {
	u, _, dir := setup(t)
	ctx := context.Background()

	if err := u.Update(ctx, build(t, dir, "v1", "build one")); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if err := u.Update(ctx, build(t, dir, "v2", "build two")); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := current(t, u); got != "build two" {
		t.Fatalf("current = %q", got)
	}
	// A broken build is rolled back and v2 serves again.
	err := u.Update(ctx, build(t, dir, "bad", "BROKEN build"))
	if !errors.Is(err, ErrRolledBack) {
		t.Fatalf("want ErrRolledBack, got %v", err)
	}
	if got := current(t, u); got != "build two" {
		t.Fatalf("after rollback current = %q", got)
	}
	// Re-applying the current build is a no-op that still checks health.
	if err := u.Update(ctx, build(t, dir, "v2again", "build two")); err != nil {
		t.Fatalf("idempotent update: %v", err)
	}
	// Old builds are pruned to KeepBuilds (+ the broken one may remain as one of them).
	for i := range 5 {
		if err := u.Update(ctx, build(t, dir, fmt.Sprintf("n%d", i), fmt.Sprintf("build n%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(u.VersionsDir)
	if len(entries) > KeepBuilds {
		t.Fatalf("%d builds kept, want <= %d", len(entries), KeepBuilds)
	}
}

func TestFirstInstallBrokenHasNothingToRollBackTo(t *testing.T) {
	u, _, dir := setup(t)
	err := u.Update(context.Background(), build(t, dir, "bad", "BROKEN"))
	if err == nil || errors.Is(err, ErrRolledBack) || !strings.Contains(err.Error(), "no previous build") {
		t.Fatalf("got %v", err)
	}
}

func TestUnitAndURL(t *testing.T) {
	o := Options{Domain: "tiffin.localhost", HTTPSPort: 8443, HTTPPort: 8080}
	if o.PublicURL() != "https://dashboard.tiffin.localhost:8443" {
		t.Fatal(o.PublicURL())
	}
	if (Options{Domain: "example.com", HTTPSPort: 443}).PublicURL() != "https://dashboard.example.com" {
		t.Fatal("443 must be implicit")
	}
	unit := Unit(o)
	for _, want := range []string{"User=root", "--box", "--edge-external", "--https-port 8443", "NoNewPrivileges=yes", "ProtectHome=yes", "Wants=network-online.target tiffin-edge.service"} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit lacks %q", want)
		}
	}
	// Restarting tiffin must not restart the edge: no Requires/BindsTo/PartOf on it.
	for _, bad := range []string{"Requires=tiffin-edge", "BindsTo=", "PartOf="} {
		if strings.Contains(unit, bad) {
			t.Errorf("unit has %q", bad)
		}
	}
	if s := EdgeSocketUnit(o); !strings.Contains(s, "ListenStream=8443\nListenStream=8080\nListenDatagram=8443") {
		t.Errorf("socket unit: %s", s)
	}
	if s := EdgeUnit(); !strings.Contains(s, "ExecStart=/usr/local/bin/tiffin edge --home /var/lib/tiffin/platform") || !strings.Contains(s, "Requires=tiffin-edge.socket") {
		t.Errorf("edge unit: %s", s)
	}
}

// systemd can refuse the rollback's restart (a crash-looping build trips its
// start limit) while still bringing the previous build back on its own:
// health, not the restart's exit code, decides that the rollback worked.
func TestRollbackSurvivesARefusedRestart(t *testing.T) {
	u, f, dir := setup(t)
	ctx := context.Background()
	if err := u.Update(ctx, build(t, dir, "v1", "build one")); err != nil {
		t.Fatal(err)
	}
	calls := 0
	u.Restart = func(ctx context.Context) error {
		calls++
		err := f.restart(ctx) // the service does come back...
		if calls == 2 {
			return errors.New("Job for tiffin.service failed") // ...but the call reports failure
		}
		return err
	}
	err := u.Update(ctx, build(t, dir, "bad", "BROKEN build"))
	if !errors.Is(err, ErrRolledBack) {
		t.Fatalf("want ErrRolledBack despite the refused restart, got %v", err)
	}
	if got := current(t, u); got != "build one" {
		t.Fatalf("after rollback current = %q", got)
	}
}

// A new build that migrates the state database and then fails to start is
// rolled back together with the database, so the previous build opens it.
func TestRollbackRestoresTheStateDatabase(t *testing.T) {
	u, f, dir := setup(t)
	ctx := context.Background()
	u.StateDB = filepath.Join(dir, "platform", "state.db")
	db, err := state.Open(u.StateDB)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.KVPut(ctx, "t", "before", []byte("1")); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := u.Update(ctx, build(t, dir, "v1", "build one")); err != nil {
		t.Fatal(err)
	}
	if err := u.Update(ctx, build(t, dir, "v2", "build two")); err != nil {
		t.Fatal(err)
	}
	if m, _ := filepath.Glob(filepath.Join(u.VersionsDir, "*", "state-before.db")); len(m) != 0 {
		t.Fatalf("a successful update keeps no snapshot: %v", m)
	}
	f.broken = func() {
		db, err := state.Open(u.StateDB)
		if err != nil {
			t.Error(err)
			return
		}
		defer db.Close()
		_ = db.KVPut(ctx, "t", "after", []byte("1"))
		if _, err := db.SQL().Exec(fmt.Sprintf(`PRAGMA user_version = %d`, state.SchemaVersion()+1)); err != nil {
			t.Error(err)
		}
	}
	if err := u.Update(ctx, build(t, dir, "bad", "BROKEN migrates")); !errors.Is(err, ErrRolledBack) {
		t.Fatalf("want ErrRolledBack, got %v", err)
	}
	db, err = state.Open(u.StateDB)
	if err != nil {
		t.Fatalf("the previous build cannot open the state: %v", err)
	}
	defer db.Close()
	if _, ok, _ := db.KVGet(ctx, "t", "before"); !ok {
		t.Fatal("state from before the update is gone")
	}
	if _, ok, _ := db.KVGet(ctx, "t", "after"); ok {
		t.Fatal("the failed build's write survived the rollback")
	}
}
