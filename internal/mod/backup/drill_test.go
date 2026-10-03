package backup

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
)

func testPlatform(t *testing.T) *platform.Platform {
	t.Helper()
	home := t.TempDir()
	db, err := state.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &platform.Platform{DB: db, Home: home, Log: slog.Default()}
}

func TestDrillDue(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	s := DefaultSchedule
	dr := func(status string, ago time.Duration) *BackupDrill {
		return &BackupDrill{Status: status, StartedAt: now.Add(-ago)}
	}
	day := 24 * time.Hour
	cases := []struct {
		name  string
		last  *BackupDrill
		since time.Duration
		want  bool
	}{
		{"new box", nil, time.Hour, false},
		{"box a day old", nil, 25 * time.Hour, true},
		{"passed yesterday", dr(DrillPassed, day), 30 * day, false},
		{"passed a week ago", dr(DrillPassed, 7*day), 30 * day, true},
		{"failed an hour ago", dr(DrillFailed, time.Hour), 30 * day, false},
		{"failed a day ago", dr(DrillFailed, 25*time.Hour), 30 * day, true},
		{"running for ages", dr(DrillRunning, 30*day), 30 * day, false},
	}
	for _, c := range cases {
		if got := drillDue(s, c.last, now.Add(-c.since), now); got != c.want {
			t.Errorf("%s: due = %v, want %v", c.name, got, c.want)
		}
	}
	off := s
	off.DrillEnabled = false
	if drillDue(off, nil, now.Add(-30*day), now) {
		t.Error("drills off must never be due")
	}
}

func TestDrillCheck(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	s := DefaultSchedule
	day := 24 * time.Hour
	passed := BackupDrill{ID: "dr_2", Status: DrillPassed, StartedAt: now.Add(-2*day - time.Minute), FinishedAt: now.Add(-2 * day), Seconds: BackupDrillSeconds{Restore: 14.2}}
	failed := BackupDrill{ID: "dr_3", Status: DrillFailed, StartedAt: now.Add(-time.Hour), FinishedAt: now.Add(-time.Hour), Message: "p_shop is missing table public.orders"}

	if c := drillCheck(nil, s, now.Add(-time.Hour), now); !c.OK || !strings.Contains(c.Detail, "no restore drill yet") {
		t.Fatalf("fresh box: %+v", c)
	}
	if c := drillCheck(nil, s, now.Add(-15*day), now); c.OK {
		t.Fatalf("no drill in 14 days must warn: %+v", c)
	}
	c := drillCheck([]BackupDrill{passed}, s, now.Add(-30*day), now)
	if !c.OK || c.Detail != "restore drill passed 2 days ago (restored in 14 s)" {
		t.Fatalf("passed: %+v", c)
	}
	if c := drillCheck([]BackupDrill{failed, passed}, s, now.Add(-30*day), now); c.OK || !strings.Contains(c.Detail, "public.orders") || !strings.Contains(c.Detail, "dr_3") {
		t.Fatalf("failed must warn and say why: %+v", c)
	}
	old := passed
	old.StartedAt, old.FinishedAt = now.Add(-15*day), now.Add(-15*day)
	if c := drillCheck([]BackupDrill{old}, s, now.Add(-30*day), now); c.OK || !strings.Contains(c.Detail, "14 days") {
		t.Fatalf("stale pass must warn: %+v", c)
	}
	running := BackupDrill{ID: "dr_4", Status: DrillRunning, Phase: "restoring", StartedAt: now}
	if c := drillCheck([]BackupDrill{running, passed}, s, now.Add(-30*day), now); !c.OK || !strings.Contains(c.Detail, "running now (restoring)") {
		t.Fatalf("running uses the last finished drill: %+v", c)
	}
	off := s
	off.DrillEnabled = false
	if c := drillCheck([]BackupDrill{passed}, off, now.Add(-30*day), now); !strings.Contains(c.Detail, "--drill-enabled") {
		t.Fatalf("off: %+v", c)
	}
}

func cat(dbs ...clusterDB) *clusterCatalog { return &clusterCatalog{Databases: dbs} }

func tbl(name string, rows int64) clusterTable {
	return clusterTable{Name: name, Rows: rows, Exact: true}
}

func TestCompare(t *testing.T) {
	expected := cat(
		clusterDB{Name: "p_shop", Tables: []clusterTable{tbl("public.orders", -1), tbl("public.users", -1)}},
		clusterDB{Name: "p_blog", Tables: []clusterTable{tbl("public.posts", -1)}},
		clusterDB{Name: "postgres"},
	)
	live := cat(
		clusterDB{Name: "p_shop", Tables: []clusterTable{tbl("public.orders", 12), tbl("public.users", 3), tbl("public.new_since", 1)}},
		clusterDB{Name: "p_blog", Tables: []clusterTable{tbl("public.posts", 7)}},
		clusterDB{Name: "postgres"},
	)
	good := cat(
		clusterDB{Name: "p_blog", Tables: []clusterTable{tbl("public.posts", 7)}},
		clusterDB{Name: "p_shop", Tables: []clusterTable{tbl("public.orders", 10), tbl("public.users", 3)}},
		clusterDB{Name: "postgres"},
	)
	got := compare(expected, true, good, live)
	if len(got) != 3 {
		t.Fatalf("databases: %+v", got)
	}
	for _, d := range got {
		if !d.OK || len(d.Missing) != 0 {
			t.Fatalf("good restore: %+v", d)
		}
	}
	shop := got[1]
	if shop.Name != "p_shop" || shop.Tables != 2 || shop.Rows != 13 || shop.LiveTables != 3 || shop.LiveRows != 16 {
		t.Fatalf("p_shop counts: %+v", shop)
	}
	if shop.Counts[0].Table != "public.orders" || shop.Counts[0].Rows != 10 || shop.Counts[0].LiveRows != 12 {
		t.Fatalf("per-table counts, largest first: %+v", shop.Counts)
	}

	// A table and a database missing, and a table that cannot be read.
	bad := cat(
		clusterDB{Name: "p_shop", Tables: []clusterTable{tbl("public.users", 3), {Name: "public.orders", Rows: -1, Err: "could not read block 0"}}},
		clusterDB{Name: "postgres"},
	)
	bad.Databases[0].Tables = bad.Databases[0].Tables[:1]
	got = compare(expected, true, bad, live)
	var blog, shopBad BackupDrillDatabase
	for _, d := range got {
		switch d.Name {
		case "p_blog":
			blog = d
		case "p_shop":
			shopBad = d
		}
	}
	if blog.OK || !strings.Contains(strings.Join(blog.Problems, " "), "not in the restored copy") {
		t.Fatalf("missing database: %+v", blog)
	}
	if shopBad.OK || len(shopBad.Missing) != 1 || shopBad.Missing[0] != "public.orders" {
		t.Fatalf("missing table: %+v", shopBad)
	}
	unreadable := cat(clusterDB{Name: "p_shop", Tables: []clusterTable{tbl("public.users", 3), {Name: "public.orders", Rows: -1, Err: "could not read block 0"}}})
	if d := compare(cat(expected.Databases[0]), true, unreadable, nil)[0]; d.OK || !strings.Contains(d.Problems[0], "public.orders cannot be read") || d.Counts[len(d.Counts)-1].LiveRows != -1 {
		t.Fatalf("unreadable table: %+v", d)
	}

	// Without a recorded list the live cluster is the reference: newer
	// tables are reported, not failed.
	got = compare(live, false, good, live)
	for _, d := range got {
		if !d.OK {
			t.Fatalf("live comparison must not fail on new tables: %+v", d)
		}
	}
	if !strings.Contains(strings.Join(got[1].Problems, " "), "public.new_since") {
		t.Fatalf("new live table not reported: %+v", got[1])
	}
}

func TestVerdict(t *testing.T) {
	d := &BackupDrill{Backup: "bk_1", BackupAgeSeconds: 7200, RestoredBytes: 50 << 20, ComparedWith: "backup",
		Seconds:   BackupDrillSeconds{Restore: 14.2, Start: 2.5, Verify: 0.4},
		Databases: []BackupDrillDatabase{{Name: "p_shop", OK: true, Tables: 2, Rows: 1234567}, {Name: "postgres", OK: true}}}
	st, msg, _ := verdict(d)
	if st != DrillPassed || !strings.Contains(msg, "2 databases, 2 tables and 1,234,567 rows") || !strings.Contains(msg, "in 14 s") || !strings.Contains(msg, "2 hours") {
		t.Fatalf("passed: %s %s", st, msg)
	}
	d.Databases[0].OK, d.Databases[0].Missing = false, []string{"public.orders"}
	st, msg, hint := verdict(d)
	if st != DrillFailed || !strings.Contains(msg, "p_shop (1 missing table: public.orders)") || hint == "" {
		t.Fatalf("failed: %s %s", st, msg)
	}
	if st, _, _ := verdict(&BackupDrill{}); st != DrillFailed {
		t.Fatal("no databases must fail")
	}
}

func TestStartDrillRefusals(t *testing.T) {
	p := testPlatform(t)
	ctx := context.Background()
	drillRoot = t.TempDir()
	defer func() { drillRoot = DrillRoot }()
	b := &Backup{ID: "bk_01J00000000000000000000001", Status: "failed"}
	if _, _, err := StartDrill(ctx, p, b, "manual"); err == nil || !strings.Contains(err.Error(), "did not succeed") {
		t.Fatalf("failed backup: %v", err)
	}
	b.Status = "ok"
	if _, _, err := StartDrill(ctx, p, b, "manual"); err == nil || !strings.Contains(err.Error(), "no Postgres part") {
		t.Fatalf("no label: %v", err)
	}
	b.Postgres.Label, b.Postgres.SizeBytes = "20261003-120000F", 10<<30
	orig := freeBytes
	defer func() { freeBytes = orig }()
	freeBytes = func(string) (int64, error) { return 11 << 30, nil }
	_, _, err := StartDrill(ctx, p, b, "manual")
	var de *DiskError
	if !errors.As(err, &de) || de.Need != int64(float64(10<<30)*1.2) {
		t.Fatalf("10 GiB backup with 11 GiB free must be refused: %v", err)
	}
	if !strings.Contains(err.Error(), "12.0 GB") || !strings.Contains(err.Error(), "11.0 GB is free") {
		t.Fatalf("disk message: %v", err)
	}
	drillState.mu.Lock()
	drillState.running = &drillRun{rec: &BackupDrill{ID: "dr_x"}}
	drillState.mu.Unlock()
	defer func() { drillState.mu.Lock(); drillState.running = nil; drillState.mu.Unlock() }()
	freeBytes = func(string) (int64, error) { return 100 << 30, nil }
	if _, _, err := StartDrill(ctx, p, b, "manual"); !errors.Is(err, ErrDrillRunning) {
		t.Fatalf("second drill: %v", err)
	}
	if list, _ := ListDrills(ctx, p); len(list) != 0 {
		t.Fatalf("refusals must not record drills: %+v", list)
	}
}

func TestCleanupScratch(t *testing.T) {
	p := testPlatform(t)
	ctx := context.Background()
	drillRoot = t.TempDir()
	defer func() { drillRoot = DrillRoot }()
	left := filepath.Join(drillRoot, "dr_01J00000000000000000000009", "data")
	if err := os.MkdirAll(left, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(left, "postmaster.pid"), []byte("999999\n"+left+"\n"), 0o600)
	d := &BackupDrill{ID: "dr_01J00000000000000000000009", Status: DrillRunning, Phase: "restoring", StartedAt: time.Now()}
	_ = saveDrill(ctx, p, d)
	done := &BackupDrill{ID: "dr_01J00000000000000000000008", Status: DrillPassed, StartedAt: time.Now()}
	_ = saveDrill(ctx, p, done)
	cleanupScratch(ctx, p)
	if entries, _ := os.ReadDir(drillRoot); len(entries) != 0 {
		t.Fatalf("leftover scratch not removed: %v", entries)
	}
	got, _ := getDrill(ctx, p, d.ID)
	if got.Status != DrillFailed || !strings.Contains(got.Message, "interrupted") || got.FinishedAt.IsZero() {
		t.Fatalf("interrupted drill: %+v", got)
	}
	if got, _ := getDrill(ctx, p, done.ID); got.Status != DrillPassed {
		t.Fatalf("finished drills stay as they were: %+v", got)
	}
}

func TestPruneDrills(t *testing.T) {
	p := testPlatform(t)
	ctx := context.Background()
	for i := 0; i < keepDrills+5; i++ {
		id := "dr_01J000000000000000000" + strings.Repeat("0", 3-len(itoa(i))) + itoa(i)
		_ = saveDrill(ctx, p, &BackupDrill{ID: id, Status: DrillPassed})
	}
	pruneDrills(ctx, p)
	list, _ := ListDrills(ctx, p)
	if len(list) != keepDrills || list[0].ID != "dr_01J000000000000000000034" {
		t.Fatalf("kept %d, newest %s", len(list), list[0].ID)
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func TestScratchConfig(t *testing.T) {
	dir := "/var/lib/tiffin/drill/dr_X"
	conf := scratchConf(dir)
	for _, want := range []string{"archive_mode = off", "listen_addresses = ''", "unix_socket_directories = '/var/lib/tiffin/drill/dr_X/sock'",
		"data_directory = '/var/lib/tiffin/drill/dr_X/data'", "hot_standby = off", "shared_preload_libraries = ''"} {
		if !strings.Contains(conf, want) {
			t.Errorf("scratch config lacks %q", want)
		}
	}
	args := strings.Join(scratchArgs(dir), " ")
	for _, want := range []string{"archive_mode=off", "listen_addresses= ", "shared_preload_libraries= ", "config_file=/var/lib/tiffin/drill/dr_X/conf/postgresql.conf"} {
		if !strings.Contains(args, want) {
			t.Errorf("command line lacks %q: %s", want, args)
		}
	}
	// The socket path must fit the 107-byte unix socket limit.
	if n := len(filepath.Join(DrillRoot, "dr_01J00000000000000000000000", "sock", ".s.PGSQL.5499")); n > 100 {
		t.Errorf("socket path is %d bytes", n)
	}
}

func TestScheduleKeepsDrillDefaults(t *testing.T) {
	p := testPlatform(t)
	ctx := context.Background()
	// A schedule saved before drills existed.
	_ = p.DB.KVPut(ctx, nsMeta, "schedule", []byte(`{"enabled":true,"fullEveryHours":12,"incrementalEveryHours":2,"retainFull":3}`))
	s := getSchedule(ctx, p)
	if !s.DrillEnabled || s.DrillEveryDays != 7 || s.FullEveryHours != 12 {
		t.Fatalf("schedule: %+v", s)
	}
}

func TestWords(t *testing.T) {
	for in, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", -4500: "-4,500"} {
		if got := commas(in); got != want {
			t.Errorf("commas(%d) = %s", in, got)
		}
	}
	if fmtSecs(0.44) != "0.4 s" || fmtSecs(14.2) != "14 s" || fmtSecs(125) != "2 min 5 s" {
		t.Errorf("fmtSecs: %s %s %s", fmtSecs(0.44), fmtSecs(14.2), fmtSecs(125))
	}
	if ago(49*time.Hour) != "2 days" || ago(90*time.Minute) != "90 minutes" || ago(5*time.Second) != "5 seconds" {
		t.Errorf("ago: %s %s %s", ago(49*time.Hour), ago(90*time.Minute), ago(5*time.Second))
	}
	pgErr := errors.New("pgbackrest: exit status 29: 2026-10-03 13:34:24.091 P00 WARN: something minor\n2026-10-03 13:34:39.242 P00 ERROR: [029]: raised from local-1 protocol: zst error: [-10] Unknown frame descriptor")
	if got := clean(pgErr); got != "[029]: raised from local-1 protocol: zst error: [-10] Unknown frame descriptor" {
		t.Errorf("clean: %s", got)
	}
	if secs(14249*time.Millisecond) != 14.2 {
		t.Errorf("secs: %v", secs(14249*time.Millisecond))
	}
}
