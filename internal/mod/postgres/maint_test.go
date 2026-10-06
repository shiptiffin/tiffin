package postgres

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeBox is apt, systemd, Postgres and the pooler for update tests: it
// records every step and serves versions from its own package table.
type fakeBox struct {
	states  map[string]aptState
	flagged []string
	running string // Postgres version the server runs
	pooler  string // PgBouncer version the pooler runs
	calls   []string
	fail    map[string]error // step → error, once
	paused  bool
}

func newFakeBox() *fakeBox {
	return &fakeBox{running: "18.4", pooler: "1.26.0", fail: map[string]error{}, states: map[string]aptState{
		"postgresql-18":          {"18.4-1.pgdg26.04+1", "18.6-1.pgdg26.04+2"},
		"postgresql-client-18":   {"18.4-1.pgdg26.04+1", "18.6-1.pgdg26.04+2"},
		"libpq5":                 {"18.4-1.pgdg26.04+1", "18.6-1.pgdg26.04+2"},
		"postgresql-18-pgvector": {"0.8.7-1.pgdg26.04+1", "0.8.7-1.pgdg26.04+1"},
		"postgresql-18-cron":     {"1.6.8-1.pgdg26.04+2", "1.6.8-1.pgdg26.04+2"},
		"pgbouncer":              {"1.26.0-4.pgdg26.04+1", "1.26.0-4.pgdg26.04+1"},
		"postgresql-common":      {"293.pgdg26.04+1", "293.pgdg26.04+1"},
	}}
}

func (f *fakeBox) step(name string) error {
	f.calls = append(f.calls, name)
	if err, ok := f.fail[name]; ok {
		delete(f.fail, name)
		return err
	}
	return nil
}

func (f *fakeBox) Refresh(context.Context) error { return f.step("refresh") }
func (f *fakeBox) Policy(_ context.Context, pkgs []string) (map[string]aptState, error) {
	out := map[string]aptState{}
	for _, p := range pkgs {
		if st, ok := f.states[p]; ok {
			out[p] = st
		}
	}
	return out, nil
}
func (f *fakeBox) NeedsRestart(context.Context) []string { return f.flagged }
func (f *fakeBox) Download(_ context.Context, pkgs []string) error {
	return f.step("download " + strings.Join(pkgs, " "))
}
func (f *fakeBox) Install(_ context.Context, pkgs []string) error {
	if err := f.step("install " + strings.Join(pkgs, " ")); err != nil {
		return err
	}
	for _, p := range pkgs {
		st := f.states[p]
		st.Installed = st.Candidate
		f.states[p] = st
	}
	return nil
}
func (f *fakeBox) Rollback(_ context.Context, pkgs []PGPackage) error {
	var names []string
	for _, p := range pkgs {
		names = append(names, p.Name+"="+p.From)
		st := f.states[p.Name]
		st.Installed = p.From
		f.states[p.Name] = st
	}
	return f.step("rollback " + strings.Join(names, " "))
}
func (f *fakeBox) ServerVersion(context.Context) string { return f.running }
func (f *fakeBox) PoolerVersion(context.Context) string { return f.pooler }
func (f *fakeBox) Checkpoint(context.Context) error     { return f.step("checkpoint") }
func (f *fakeBox) Pause(context.Context) (func() error, error) {
	if err := f.step("pause"); err != nil {
		return func() error { return nil }, err
	}
	f.paused = true
	return func() error {
		f.paused = false
		return f.step("resume")
	}, nil
}
func (f *fakeBox) Restart(_ context.Context, unit string) error {
	if err := f.step("restart " + unit); err != nil {
		return err
	}
	switch unit {
	case UnitName:
		f.running = upstream(f.states["postgresql-18"].Installed)
	case PoolerUnit:
		f.pooler = upstream(f.states["pgbouncer"].Installed)
		f.paused = false
	}
	return nil
}
func (f *fakeBox) WaitReady(_ context.Context, unit string) error { return f.step("ready " + unit) }

func useTempRecord(t *testing.T) {
	t.Helper()
	old := maintPath
	maintPath = filepath.Join(t.TempDir(), "maintenance.json")
	t.Cleanup(func() { maintPath = old })
}

func TestDecide(t *testing.T) {
	f := newFakeBox()
	p := decide(f.states, nil)
	if len(p.Packages) != 3 || !p.RestartPostgres || p.RestartPooler || len(p.Restart) != 0 ||
		p.Packages[0] != (PGPackage{"postgresql-18", "18.4-1.pgdg26.04+1", "18.6-1.pgdg26.04+2"}) {
		t.Fatalf("server update: %+v", p)
	}
	// Client libraries alone restart nothing.
	f.states["postgresql-18"] = aptState{"18.6-1.pgdg26.04+2", "18.6-1.pgdg26.04+2"}
	if p := decide(f.states, nil); len(p.Packages) != 2 || p.RestartPostgres {
		t.Fatalf("client update: %+v", p)
	}
	// Not installed is not upgraded; a new pooler wants the pooler's restart;
	// flagged services restart in order, never tiffin itself.
	f.states = map[string]aptState{"postgresql-18-pgvector": {"", "0.9.0-1"}, "pgbouncer": {"1.26.0-4", "1.26.1-1"}}
	p = decide(f.states, []string{"containerd.service", "tiffin.service", "tiffin-valkey.service", "ssh.service"})
	if len(p.Packages) != 1 || p.RestartPostgres || !p.RestartPooler ||
		!slices.Equal(p.Restart, []string{"tiffin-valkey.service", "containerd.service"}) || !slices.Equal(p.Flagged, p.Restart) {
		t.Fatalf("pooler and flagged: %+v", p)
	}
	if p := decide(nil, []string{UnitName}); !p.RestartPostgres || len(p.Packages) != 0 {
		t.Fatalf("libraries replaced under Postgres: %+v", p)
	}
}

func TestVersions(t *testing.T) {
	for v, want := range map[string]string{"18.4-1.pgdg26.04+1": "18.4", "1:2.43-2ubuntu2": "2.43", "293.pgdg26.04+1": "293.pgdg26.04+1", "1.26.0": "1.26.0"} {
		if got := upstream(v); got != want {
			t.Errorf("upstream(%s) = %s, want %s", v, got, want)
		}
	}
	for _, c := range []struct {
		v, min string
		older  bool
	}{{"18.4-1", "18.6", true}, {"18.6-1.pgdg", "18.6", false}, {"18.10-1", "18.9", false}, {"1.20.1-1", "1.21", true}, {"1.26.0-4", "1.21", false}} {
		if got := olderThan(c.v, c.min); got != c.older {
			t.Errorf("olderThan(%s, %s) = %v", c.v, c.min, got)
		}
	}
	low := belowMinimum(map[string]aptState{"postgresql-18": {Installed: "18.4-1"}, "pgbouncer": {Installed: "1.26.0-4"}})
	if !slices.Equal(low, []string{"postgresql-18"}) {
		t.Fatalf("below minimum: %v", low)
	}
	if checkMinimum("pgbouncer", "1.26.0-4.pgdg26.04+1") != nil || checkMinimum("pgbouncer", "") != nil || checkMinimum("pgbouncer", "1.20.1-1") == nil {
		t.Fatal("checkMinimum")
	}
}

// apt-cache fails while an apt-get update rewrites the index: it is tried
// again, a bounded number of times.
func TestRetryApt(t *testing.T) {
	defer func(n int, w time.Duration) { aptTries, aptWait = n, w }(aptTries, aptWait)
	aptTries, aptWait = 4, time.Millisecond
	calls := 0
	flaky := func() (string, error) {
		calls++
		if calls < 3 {
			return "", errors.New("apt-cache policy pgbouncer: exit status 100: E: Cache is out of sync")
		}
		return "pgbouncer:", nil
	}
	if out, err := retryApt(context.Background(), flaky); err != nil || out != "pgbouncer:" || calls != 3 {
		t.Fatalf("out %q, err %v, calls %d", out, err, calls)
	}
	calls = 0
	if _, err := retryApt(context.Background(), func() (string, error) { calls++; return "", errors.New("broken") }); err == nil || calls != 4 {
		t.Fatalf("err %v after %d calls", err, calls)
	}
}

// An update installs while the old server runs, then holds queries only for
// the restart.
func TestApplyOrder(t *testing.T) {
	useTempRecord(t)
	f := newFakeBox()
	u, plan, err := runUpdate(context.Background(), f, runOpts{Trigger: "now"})
	if err != nil || u == nil {
		t.Fatalf("%v %v", u, err)
	}
	want := []string{"refresh", "download postgresql-18 postgresql-client-18 libpq5", "install postgresql-18 postgresql-client-18 libpq5",
		"checkpoint", "pause", "restart tiffin-postgres.service", "ready tiffin-postgres.service", "resume"}
	if !slices.Equal(f.calls, want) {
		t.Fatalf("steps:\n%s\nwant:\n%s", strings.Join(f.calls, "\n"), strings.Join(want, "\n"))
	}
	if u.Status != "ok" || u.From != "18.4" || u.To != "18.6" || !slices.Equal(u.Restarted, []string{UnitName}) || !plan.RestartPostgres ||
		!strings.HasPrefix(u.Summary, "Postgres 18.4 → 18.6, paused ") || f.paused {
		t.Fatalf("update: %+v", u)
	}
	r := loadMaint()
	if len(r.Updates) != 1 || r.Updates[0].ID != u.ID || len(r.Available) != 0 || r.CheckedAt.IsZero() {
		t.Fatalf("record: %+v", r)
	}
	// Up to date now: nothing runs, nothing is recorded.
	f.calls = nil
	u, _, err = runUpdate(context.Background(), f, runOpts{Trigger: "schedule"})
	if err != nil || u != nil || !slices.Equal(f.calls, []string{"refresh"}) || len(loadMaint().Updates) != 1 {
		t.Fatalf("second run: %v %v %v", u, err, f.calls)
	}
}

// A new version that does not start gets the old packages back; the pooler
// resumes either way.
func TestApplyRollsBack(t *testing.T) {
	useTempRecord(t)
	f := newFakeBox()
	f.fail["ready tiffin-postgres.service"] = errors.New("did not answer in 90s")
	u, _, err := runUpdate(context.Background(), f, runOpts{Trigger: "schedule"})
	if err != nil {
		t.Fatal(err)
	}
	if u.Status != "failed" || !strings.Contains(u.Error, "did not answer") || !strings.Contains(u.Error, "the old version runs again") || f.paused {
		t.Fatalf("update: %+v (paused %v)", u, f.paused)
	}
	tail := f.calls[len(f.calls)-5:]
	want := []string{"rollback postgresql-18=18.4-1.pgdg26.04+1 postgresql-client-18=18.4-1.pgdg26.04+1 libpq5=18.4-1.pgdg26.04+1",
		"restart tiffin-postgres.service", "ready tiffin-postgres.service", "resume"}
	if !slices.Equal(tail[1:], want) || f.running != "18.4" {
		t.Fatalf("steps: %s", strings.Join(f.calls, "\n"))
	}
	if c := updateCheck(loadMaint(), "", time.Now()); c.OK || !strings.Contains(c.Detail, "failed") {
		t.Fatalf("check: %+v", c)
	}
}

func TestApplyFailuresBeforeTheRestart(t *testing.T) {
	useTempRecord(t)
	// A failed install restarts nothing and never pauses.
	f := newFakeBox()
	f.fail["install postgresql-18 postgresql-client-18 libpq5"] = errors.New("dpkg: error")
	u, _, _ := runUpdate(context.Background(), f, runOpts{Trigger: "now"})
	if u.Status != "failed" || slices.Contains(f.calls, "pause") || slices.ContainsFunc(f.calls, func(s string) bool { return strings.HasPrefix(s, "restart") }) {
		t.Fatalf("install failure: %+v %v", u, f.calls)
	}
	// Transactions that outlast the pause: no restart; a later run sees the
	// server older than its package and restarts it.
	f = newFakeBox()
	f.fail["pause"] = errors.New("transactions still running after 30s")
	u, _, _ = runUpdate(context.Background(), f, runOpts{Trigger: "now"})
	if u.Status != "failed" || slices.Contains(f.calls, "restart tiffin-postgres.service") || f.running != "18.4" {
		t.Fatalf("pause failure: %+v %v", u, f.calls)
	}
	f.calls = nil
	u, plan, _ := runUpdate(context.Background(), f, runOpts{Trigger: "now"})
	if u == nil || u.Status != "ok" || len(plan.Packages) != 0 || !plan.RestartPostgres || f.running != "18.6" || slices.ContainsFunc(f.calls, func(s string) bool { return strings.HasPrefix(s, "install") }) {
		t.Fatalf("restart only: %+v %+v %v", u, plan, f.calls)
	}
}

// The pooler restarts only when asked; until then the status says it waits.
func TestPoolerRestartsOnlyWhenAsked(t *testing.T) {
	useTempRecord(t)
	f := newFakeBox()
	f.running = "18.6"
	for _, p := range []string{"postgresql-18", "postgresql-client-18", "libpq5"} {
		f.states[p] = aptState{"18.6-1.pgdg26.04+2", "18.6-1.pgdg26.04+2"}
	}
	f.states["pgbouncer"] = aptState{"1.26.0-4.pgdg26.04+1", "1.26.1-1.pgdg26.04+1"}
	u, _, _ := runUpdate(context.Background(), f, runOpts{Trigger: "schedule"})
	if u == nil || u.Status != "ok" || slices.Contains(f.calls, "restart "+PoolerUnit) || f.pooler != "1.26.0" {
		t.Fatalf("scheduled: %+v %v", u, f.calls)
	}
	// The pooler runs 1.26.0 with 1.26.1 installed: it waits for a reboot.
	c := updateCheck(loadMaint(), "", time.Now())
	if !slices.Contains(loadMaint().Restart, PoolerUnit) || !strings.Contains(c.Detail, "PgBouncer runs on replaced files") || !c.OK {
		t.Fatalf("check: %+v %+v", c, loadMaint())
	}
	f.calls = nil
	u, _, _ = runUpdate(context.Background(), f, runOpts{Trigger: "now", RestartPooler: true})
	if u == nil || !slices.Equal(u.Restarted, []string{PoolerUnit}) || f.pooler != "1.26.1" || len(loadMaint().Restart) != 0 {
		t.Fatalf("asked: %+v %v", u, f.calls)
	}
}

func TestParsers(t *testing.T) {
	out := `postgresql-18:
  Installed: 18.4-1.pgdg26.04+1
  Candidate: 18.6-1.pgdg26.04+2
  Version table:
     18.6-1.pgdg26.04+2 600
        600 https://apt.postgresql.org/pub/repos/apt resolute-pgdg/main arm64 Packages
 *** 18.4-1.pgdg26.04+1 600
        100 /var/lib/dpkg/status
pgbouncer:
  Installed: (none)
  Candidate: 1.26.0-4.pgdg26.04+1
  Version table:
     1.26.0-4.pgdg26.04+1 600
`
	got := parsePolicy(out)
	if got["postgresql-18"] != (aptState{"18.4-1.pgdg26.04+1", "18.6-1.pgdg26.04+2"}) || got["pgbouncer"] != (aptState{"", "1.26.0-4.pgdg26.04+1"}) || len(got) != 2 {
		t.Fatalf("policy: %+v", got)
	}
	nr := "NEEDRESTART-VER: 3.11\nNEEDRESTART-KSTA: 1\nNEEDRESTART-SVC: tiffin-postgres.service\nNEEDRESTART-SVC: ssh.service\n"
	if got := parseNeedrestart(nr); !slices.Equal(got, []string{"tiffin-postgres.service", "ssh.service"}) {
		t.Fatalf("needrestart: %v", got)
	}
}

func TestWindow(t *testing.T) {
	at := func(s string) time.Time {
		v, _ := time.ParseInLocation("2006-01-02 15:04", s, time.UTC)
		return v
	}
	if got := nextRun(at("2026-10-05 03:00"), "04:00"); !got.Equal(at("2026-10-05 04:15")) {
		t.Fatalf("next run %s", got)
	}
	if got := nextRun(at("2026-10-05 04:20"), "04:00"); !got.Equal(at("2026-10-06 04:15")) {
		t.Fatalf("next run after %s", got)
	}
	never := time.Time{}
	for _, c := range []struct {
		now, last string
		due       bool
	}{
		{"2026-10-05 04:10", "", false},                 // the window opened, the delay has not passed
		{"2026-10-05 04:15", "", true},                  // a quarter hour in
		{"2026-10-05 05:14", "", true},                  // still within the hour
		{"2026-10-05 05:15", "", false},                 // too late today
		{"2026-10-05 04:30", "2026-10-05 04:16", false}, // already ran
		{"2026-10-05 04:30", "2026-10-04 04:16", true},  // ran yesterday
	} {
		last := never
		if c.last != "" {
			last = at(c.last)
		}
		if got := due(at(c.now), "04:00", last); got != c.due {
			t.Errorf("due(%s, last %s) = %v", c.now, c.last, got)
		}
	}
	if due(at("2026-10-05 04:30"), "", never) || !nextRun(at("2026-10-05 04:30"), "").IsZero() {
		t.Fatal("no window, no schedule")
	}
	r := maintRecord{CheckedAt: at("2026-10-05 01:00"), Available: []PGPackage{{"postgresql-18", "18.4-1", "18.6-1"}}}
	if c := updateCheck(r, "04:00", at("2026-10-05 03:00")); !c.OK || !strings.Contains(c.Detail, "postgresql-18 18.6 waiting: installs in the maintenance window at 04:15") {
		t.Fatalf("check: %+v", c)
	}
	if c := updateCheck(r, "", at("2026-10-05 03:00")); !strings.Contains(c.Detail, "--now") {
		t.Fatalf("check without window: %+v", c)
	}
}
