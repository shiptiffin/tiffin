package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/install"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/release"
	"github.com/shiptiffin/tiffin/internal/state"
)

// fakeBox records what an update did, in order.
type fakeBox struct {
	mu        sync.Mutex
	calls     []string
	backupErr error
	pgBusy    bool
	held      int
	launched  string // the build it was given
}

func (b *fakeBox) note(s string) { b.mu.Lock(); b.calls = append(b.calls, s); b.mu.Unlock() }

func (b *fakeBox) HoldPostgres() (func(), error) {
	if b.pgBusy {
		return nil, errors.New("busy")
	}
	b.note("hold postgres")
	b.held++
	return func() { b.held-- }, nil
}

func (b *fakeBox) Backup(context.Context) (string, error) {
	b.note("backup")
	if b.backupErr != nil {
		return "", b.backupErr
	}
	return "bk_1", nil
}

func (b *fakeBox) Launch(_ context.Context, id, bin string, edge bool) error {
	raw, _ := os.ReadFile(bin)
	b.launched = string(raw)
	if edge {
		b.note("launch with edge")
	} else {
		b.note("launch")
	}
	return nil
}

// releaseServer serves a signed manifest on the stable channel.
type releaseServer struct {
	key      release.SecretKey
	m        release.Manifest
	artifact string
	srv      *httptest.Server
}

func (s *releaseServer) source() string { return s.srv.URL + "/{channel}/manifest.json" }

func newServer(t *testing.T, version string, rollout int, edge bool) *releaseServer {
	t.Helper()
	k, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	s := &releaseServer{key: k, artifact: "build " + version}
	sum := sha256.Sum256([]byte(s.artifact))
	s.m = release.Manifest{Version: version, Channel: "stable", Rollout: rollout, EdgeRestart: edge,
		Artifacts: map[string]release.Artifact{"linux/amd64": {Name: "tiffin-linux-amd64", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(s.artifact))}}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := json.Marshal(s.m)
		switch r.URL.Path {
		case "/stable/manifest.json":
			w.Write(raw)
		case "/stable/manifest.json.minisig":
			sig, _ := release.Sign(s.key, raw, s.m.TrustedComment())
			w.Write(sig)
		case "/stable/tiffin-linux-amd64":
			w.Write([]byte(s.artifact))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

type notice struct{ subject, summary string }

func setup(t *testing.T, s *releaseServer, running string) (*updater, *fakeBox, *[]notice) {
	t.Helper()
	dir := t.TempDir()
	statePath, dlDir = filepath.Join(dir, "update.json"), filepath.Join(dir, "updates")
	platform.ServerConfigPath = filepath.Join(dir, "server.json")
	b := &fakeBox{}
	var notices []notice
	u := newUpdater(b, running, func(_ context.Context, sub, sum string) { notices = append(notices, notice{sub, sum}) }, func(string, ...any) {})
	u.keys, u.platform = []release.PublicKey{s.key.Public()}, "linux/amd64"
	if _, err := edit(func(r *record) { r.Source = s.source() }); err != nil {
		t.Fatal(err)
	}
	return u, b, &notices
}

// The backup comes first and must finish, with Postgres updates held,
// before the switch starts; the outcome comes back from the staged update.
func TestApplyBacksUpFirst(t *testing.T) {
	s := newServer(t, "1.5.0", 100, true)
	u, b, _ := setup(t, s, "1.4.0")
	up, err := u.apply(context.Background(), "now")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(b.calls, ", "); got != "hold postgres, backup, launch with edge" {
		t.Fatalf("order: %s", got)
	}
	if b.launched != "build 1.5.0" {
		t.Fatalf("launched %q", b.launched)
	}
	if up.Status != "running" || up.Backup != "bk_1" || up.From != "1.4.0" || up.To != "1.5.0" || !up.EdgeRestart {
		t.Fatalf("update: %+v", up)
	}
	// One at a time.
	if _, err := u.apply(context.Background(), "now"); !errors.Is(err, errBusy) {
		t.Fatalf("a second update while one runs: %v", err)
	}
	// The outcome arrives: recorded, the hold and the download go.
	if err := install.WriteResult(dlDir, install.StagedResult{ID: up.ID, Status: "ok", Seconds: 21, EdgeRestartMs: 118}); err != nil {
		t.Fatal(err)
	}
	u.collect(time.Now())
	r := load()
	if r.Updates[0].Status != "ok" || !strings.Contains(r.Updates[0].Summary, "edge restarted in 118 ms") {
		t.Fatalf("recorded: %+v", r.Updates[0])
	}
	if b.held != 0 {
		t.Fatal("Postgres updates are still held")
	}
	if _, err := os.Stat(filepath.Join(dlDir, up.ID)); err == nil {
		t.Fatal("the download is still there")
	}
}

func TestBackupFailureInstallsNothing(t *testing.T) {
	s := newServer(t, "1.5.0", 100, false)
	u, b, _ := setup(t, s, "1.4.0")
	b.backupErr = errors.New("pgbackrest: disk full")
	up, err := u.apply(context.Background(), "schedule")
	if err != nil {
		t.Fatal(err)
	}
	if up.Status != "failed" || !strings.Contains(up.Error, "backup") || strings.Contains(strings.Join(b.calls, ","), "launch") {
		t.Fatalf("update %+v after %v", up, b.calls)
	}
	if b.held != 0 {
		t.Fatal("Postgres updates are still held")
	}
}

// A build that does not match the signed manifest is refused before
// anything else happens; so is a downgrade or the same version.
func TestRefusals(t *testing.T) {
	s := newServer(t, "1.5.0", 100, false)
	u, b, notices := setup(t, s, "1.4.0")
	s.artifact = "build 1.5.X" // same size, other bytes
	up, err := u.apply(context.Background(), "now")
	if err != nil || up.Status != "failed" || !strings.Contains(up.Error, "sha256") || len(b.calls) != 0 {
		t.Fatalf("tampered build: %+v %v %v", up, err, b.calls)
	}
	for _, running := range []string{"1.5.0", "1.6.0"} {
		u.version = running
		var n nothing
		if _, err := u.apply(context.Background(), "now"); !errors.As(err, &n) || len(b.calls) != 0 {
			t.Fatalf("from %s: %v %v", running, err, b.calls)
		}
	}
	// A manifest signed by another key: refused, and the owner hears once.
	u.version = "1.4.0"
	s.key, _ = release.GenerateKey()
	for range 2 {
		if _, _, err := u.checkNow(context.Background()); !errors.Is(err, release.ErrRefused) {
			t.Fatalf("untrusted signature: %v", err)
		}
	}
	if len(*notices) != 1 || !strings.Contains((*notices)[0].summary, "refused") {
		t.Fatalf("notices: %+v", *notices)
	}
	if r := load(); r.CheckError == "" || r.Available != nil {
		t.Fatalf("record after a refusal: %+v", r)
	}
}

func TestScheduling(t *testing.T) {
	loc := time.UTC
	at := func(h, m int) time.Time { return time.Date(2026, 10, 6, h, m, 0, 0, loc) }
	if n := nextRun(at(3, 0), "04:00"); !n.Equal(at(4, 30)) {
		t.Fatalf("next run %v", n)
	}
	if n := nextRun(at(5, 0), "04:00"); !n.Equal(at(4, 30).AddDate(0, 0, 1)) {
		t.Fatalf("next run after the window %v", n)
	}
	for _, c := range []struct {
		now  time.Time
		last time.Time
		want bool
	}{
		{at(4, 29), time.Time{}, false},
		{at(4, 30), time.Time{}, true},
		{at(5, 29), time.Time{}, true},
		{at(5, 31), time.Time{}, false},                // the hour passed
		{at(4, 45), at(4, 31), false},                  // ran today
		{at(4, 45), at(4, 31).AddDate(0, 0, -1), true}, // ran yesterday
	} {
		if got := due(c.now, "04:00", c.last); got != c.want {
			t.Errorf("due at %s (last %s) = %v", c.now.Format("15:04"), c.last.Format("01-02 15:04"), got)
		}
	}
	if due(at(4, 45), "", time.Time{}) {
		t.Fatal("no window: never due")
	}
}

// In the window the box applies a release only when automatic updates are
// on and it falls in the release's rollout; update apply ignores both.
func TestScheduleRolloutAndAutoOff(t *testing.T) {
	s := newServer(t, "1.5.0", 0, false)
	u, b, _ := setup(t, s, "1.4.0")
	now := time.Now()
	win := now.Add(-windowDelay - time.Minute).Format("15:04")
	_, _ = edit(func(r *record) { r.Window = win })

	u.tick(context.Background(), nil, now) // rollout 0: not this box
	if len(b.calls) != 0 || load().LastScheduled.IsZero() || load().Available == nil || load().Available.InRollout {
		t.Fatalf("rollout 0: %v %+v", b.calls, load())
	}
	s.m.Rollout = 100
	_, _ = edit(func(r *record) { r.LastScheduled, r.Manual = time.Time{}, true })
	u.tick(context.Background(), nil, now) // automatic updates off
	if len(b.calls) != 0 {
		t.Fatalf("auto off: %v", b.calls)
	}
	_, _ = edit(func(r *record) { r.Manual = false })
	u.tick(context.Background(), nil, now)
	if strings.Join(b.calls, ", ") != "hold postgres, backup, launch" {
		t.Fatalf("auto on, in the rollout: %v", b.calls)
	}

	// update apply ignores the rollout.
	s2 := newServer(t, "1.6.0", 0, false)
	u2, b2, _ := setup(t, s2, "1.4.0")
	if up, err := u2.apply(context.Background(), "now"); err != nil || up.Status != "running" || len(b2.calls) != 3 {
		t.Fatalf("apply now at rollout 0: %v %v", err, b2.calls)
	}
}

// A Postgres update in progress makes the window's update wait a minute.
func TestWaitsForPostgres(t *testing.T) {
	s := newServer(t, "1.5.0", 100, false)
	u, b, _ := setup(t, s, "1.4.0")
	b.pgBusy = true
	now := time.Now()
	_, _ = edit(func(r *record) { r.Window = now.Add(-windowDelay - time.Minute).Format("15:04") })
	u.tick(context.Background(), nil, now)
	if len(b.calls) != 0 || !load().LastScheduled.IsZero() {
		t.Fatalf("ran during a Postgres update: %v", b.calls)
	}
	if _, err := os.ReadDir(dlDir); err == nil {
		if e, _ := os.ReadDir(dlDir); len(e) != 0 {
			t.Fatal("the download was left behind")
		}
	}
	b.pgBusy = false
	u.tick(context.Background(), nil, now.Add(time.Minute))
	if len(b.calls) != 3 {
		t.Fatalf("after the Postgres update: %v", b.calls)
	}
}

type fakeNotifier struct{ got []string }

func (*fakeNotifier) Name() string { return "fake-notifier" }
func (f *fakeNotifier) Notify(_ context.Context, subject, summary string) {
	f.got = append(f.got, subject+": "+summary)
}

var notifier = &fakeNotifier{}

func init() { platform.Register(notifier) }

// A rolled-back update goes in the audit log once and alerts the owner.
func TestRollbackIsAuditedAndAlerted(t *testing.T) {
	s := newServer(t, "1.5.0", 100, false)
	u, _, _ := setup(t, s, "1.4.0")
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := &platform.Platform{DB: db}
	up, err := u.apply(context.Background(), "schedule")
	if err != nil {
		t.Fatal(err)
	}
	_ = install.WriteResult(dlDir, install.StagedResult{ID: up.ID, Status: "rolled-back", Error: "new build was unhealthy; rolled back"})
	notifier.got = nil
	for range 2 {
		u.tick(context.Background(), p, time.Now())
	}
	events, _ := db.AuditLog(context.Background(), 10, 0)
	n := 0
	for _, e := range events {
		if e.Action == "box.update" {
			n++
		}
	}
	if n != 1 || len(notifier.got) != 1 || !strings.Contains(notifier.got[0], "rolled back") {
		t.Fatalf("audit events %d, notices %v", n, notifier.got)
	}
	if st := status(load(), time.Now()); st.Updates[0].Status != "rolled-back" {
		t.Fatalf("status: %+v", st.Updates[0])
	}
}

// A managed box whose control plane said "no updates" installs nothing, by
// schedule or on request; every other box is unaffected.
func TestManagedPauseInstallsNothing(t *testing.T) {
	s := newServer(t, "1.5.0", 100, false)
	u, b, _ := setup(t, s, "1.4.0")
	u.paused = func() (bool, string) { return true, "Automatic updates are paused" }
	for _, trigger := range []string{"schedule", "now"} {
		up, err := u.apply(context.Background(), trigger)
		var n nothing
		if up != nil || !errors.As(err, &n) || !strings.Contains(n.why, "paused") {
			t.Fatalf("%s: %+v %v", trigger, up, err)
		}
	}
	if len(b.calls) != 0 {
		t.Fatalf("nothing may run while paused: %v", b.calls)
	}
	u.paused = func() (bool, string) { return false, "" }
	if up, err := u.apply(context.Background(), "now"); err != nil || up.Status != "running" {
		t.Fatalf("unpaused: %+v %v", up, err)
	}
}

// releaseStatus is the status a release build would show (tests run a
// development build).
func releaseStatus(now time.Time) *Status {
	s := status(load(), now)
	s.Release = true
	s.Summary = s.words()
	return s
}

// With no update window, the hourly check installs a new release at once
// when the box is in its rollout, and checks again 50 to 70 minutes later.
func TestInstallsOnHourlyCheck(t *testing.T) {
	s := newServer(t, "1.5.0", 0, false)
	u, b, _ := setup(t, s, "1.4.0")
	now := time.Now()
	u.tick(context.Background(), nil, now) // rollout 0: checked only
	r := load()
	if len(b.calls) != 0 || r.Available == nil || r.Available.InRollout {
		t.Fatalf("rollout 0: %v %+v", b.calls, r)
	}
	if d := r.NextCheck.Sub(now); d < 50*time.Minute || d > 70*time.Minute {
		t.Fatalf("next check in %v", d)
	}
	s.m.Rollout = 100
	u.tick(context.Background(), nil, now.Add(30*time.Minute)) // not time to check yet
	if len(b.calls) != 0 {
		t.Fatalf("before the next check: %v", b.calls)
	}
	u.tick(context.Background(), nil, r.NextCheck)
	if strings.Join(b.calls, ", ") != "hold postgres, backup, launch" {
		t.Fatalf("at the next check: %v", b.calls)
	}
	if up := load().Updates[0]; up.Trigger != "schedule" || up.To != "1.5.0" {
		t.Fatalf("update: %+v", up)
	}
}

// With an update window set, the hourly check only checks; the release
// installs in the window.
func TestWindowSetInstallsOnlyInWindow(t *testing.T) {
	s := newServer(t, "1.5.0", 100, false)
	u, b, _ := setup(t, s, "1.4.0")
	now := time.Now()
	_, _ = edit(func(r *record) { r.Window = now.Add(2 * time.Hour).Format("15:04") })
	u.tick(context.Background(), nil, now)
	if r := load(); len(b.calls) != 0 || r.Available == nil || r.NextCheck.IsZero() {
		t.Fatalf("outside the window: %v %+v", b.calls, r)
	}
	if st := releaseStatus(now); st.NextRun == nil || st.NextCheck == nil || !strings.Contains(st.Summary, "update window") {
		t.Fatalf("status: %+v", st)
	}
	u.tick(context.Background(), nil, now.Add(2*time.Hour+windowDelay+time.Minute))
	if len(b.calls) != 3 {
		t.Fatalf("in the window: %v", b.calls)
	}
}

// A release that failed or rolled back here is not installed again by
// itself; update apply still can, and a newer release installs as usual.
func TestBrokenReleaseNotRetried(t *testing.T) {
	s := newServer(t, "1.5.0", 100, false)
	u, b, _ := setup(t, s, "1.4.0")
	ctx := context.Background()
	u.tick(ctx, nil, time.Now())
	up := load().Updates[0]
	_ = install.WriteResult(dlDir, install.StagedResult{ID: up.ID, Status: "rolled-back", Error: "unhealthy"})
	u.collect(time.Now())
	_, _ = edit(func(r *record) { r.NextCheck = time.Time{} })
	u.tick(ctx, nil, time.Now())
	if len(b.calls) != 3 {
		t.Fatalf("retried a rolled-back release: %v", b.calls)
	}
	if st := releaseStatus(time.Now()); !strings.Contains(st.Summary, "failed here") {
		t.Fatalf("summary: %s", st.Summary)
	}
	again, err := u.apply(ctx, "now")
	if err != nil || again.Status != "running" || len(b.calls) != 6 {
		t.Fatalf("update apply: %+v %v %v", again, err, b.calls)
	}
	_ = install.WriteResult(dlDir, install.StagedResult{ID: again.ID, Status: "failed", Error: "provision"})
	u.collect(time.Now())

	s2 := newServer(t, "1.6.0", 100, false)
	u.keys = []release.PublicKey{s2.key.Public()}
	_, _ = edit(func(r *record) { r.Source, r.NextCheck = s2.source(), time.Time{} })
	u.tick(ctx, nil, time.Now())
	if len(b.calls) != 9 || load().Updates[0].To != "1.6.0" {
		t.Fatalf("newer release: %v %+v", b.calls, load().Updates[0])
	}
}
