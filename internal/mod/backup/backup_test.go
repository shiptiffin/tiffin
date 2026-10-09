package backup

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/state"
)

func TestDue(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	s := DefaultSchedule
	bk := func(kind, status string, ago time.Duration) Backup {
		return Backup{Kind: kind, Status: status, StartedAt: now.Add(-ago)}
	}
	cases := []struct {
		name string
		list []Backup
		want string
	}{
		{"nothing yet", nil, "full"},
		{"only failed", []Backup{bk("full", "failed", time.Minute)}, "full"},
		{"fresh full", []Backup{bk("full", "ok", 10*time.Minute)}, ""},
		{"6 hours since full", []Backup{bk("full", "ok", 6*time.Hour+time.Minute)}, "incremental"},
		{"hour since full", []Backup{bk("full", "ok", 61*time.Minute)}, ""},
		{"recent incr", []Backup{bk("incremental", "ok", 5*time.Minute), bk("full", "ok", 5*time.Hour)}, ""},
		{"day since full", []Backup{bk("incremental", "ok", 5*time.Minute), bk("full", "ok", 25*time.Hour)}, "full"},
	}
	for _, c := range cases {
		if got := due(s, c.list, now); got != c.want {
			t.Errorf("%s: due = %q, want %q", c.name, got, c.want)
		}
	}
	off := s
	off.Enabled = false
	if due(off, nil, now) != "" {
		t.Error("disabled schedule must not back up")
	}
	noIncr := s
	noIncr.IncrementalEveryHours = 0
	if due(noIncr, []Backup{bk("full", "ok", 5*time.Hour)}, now) != "" {
		t.Error("incrementals off")
	}
}

func TestTargetsAndConfirmKey(t *testing.T) {
	def := []string{"postgres", "valkey"}
	got, err := normalizeTargets(nil, def)
	if err != nil || strings.Join(got, ",") != "postgres,valkey" {
		t.Fatalf("default targets: %v %v", got, err)
	}
	if got, _ := normalizeTargets([]string{"valkey", "postgres", "valkey"}, def); strings.Join(got, ",") != "postgres,valkey" {
		t.Fatalf("dedupe/sort: %v", got)
	}
	if got, _ := normalizeTargets([]string{"all"}, def); strings.Join(got, ",") != "files,platform,postgres,valkey" {
		t.Fatalf("all: %v", got)
	}
	if _, err := normalizeTargets([]string{"bogus"}, def); err == nil {
		t.Fatal("unknown target accepted")
	}
	a := &BackupRestorePreview{Backup: "bk_1", Targets: []string{"postgres"}, Overwrites: []BackupOverwrite{{Target: "postgres", What: "3 databases now", Items: []string{"p_a"}}}}
	b := *a
	b.Overwrites = []BackupOverwrite{{Target: "postgres", What: "4 databases now", Items: []string{"p_a"}}}
	ka, _ := json.Marshal(a.Key())
	kb, _ := json.Marshal(b.Key())
	if string(ka) != string(kb) {
		t.Fatal("drifting counts must not change the confirm key")
	}
	b.Overwrites[0].Items = []string{"p_a", "p_new"}
	kb, _ = json.Marshal(b.Key())
	if string(ka) == string(kb) {
		t.Fatal("a new database must change the confirm key")
	}
}

func TestChecks(t *testing.T) {
	home := t.TempDir()
	db, err := state.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := &platform.Platform{DB: db, Home: home, Log: slog.Default()}
	ctx := context.Background()
	m := &Module{}
	if c := m.Checks(ctx, p); !c[0].OK || !strings.Contains(c[0].Detail, "no backup yet") {
		t.Fatalf("fresh box: %+v", c)
	}
	_ = db.KVPut(ctx, nsMeta, "since", []byte(time.Now().Add(-30*time.Hour).Format(time.RFC3339)))
	if c := m.Checks(ctx, p); c[0].OK {
		t.Fatalf("no backup after 26h must fail: %+v", c)
	}
	old := &Backup{ID: "bk_01J00000000000000000000001", Kind: "full", Status: "ok", StartedAt: time.Now().Add(-27 * time.Hour)}
	raw, _ := json.Marshal(old)
	_ = db.KVPut(ctx, nsSets, old.ID, raw)
	if c := m.Checks(ctx, p); c[0].OK {
		t.Fatalf("27h old backup must fail: %+v", c)
	}
	fresh := &Backup{ID: "bk_01J00000000000000000000002", Kind: "incremental", Status: "ok", StartedAt: time.Now().Add(-time.Hour)}
	raw, _ = json.Marshal(fresh)
	_ = db.KVPut(ctx, nsSets, fresh.ID, raw)
	if c := m.Checks(ctx, p); !c[0].OK || !strings.Contains(c[0].Detail, fresh.ID) {
		t.Fatalf("fresh backup: %+v", c)
	}
	list, _ := List(ctx, p)
	if len(list) != 2 || list[0].ID != fresh.ID {
		t.Fatalf("newest first: %+v", list)
	}
	if s := getSchedule(ctx, p); s != DefaultSchedule {
		t.Fatalf("default schedule: %+v", s)
	}
}

func TestInclude(t *testing.T) {
	Include("storage", "/var/lib/tiffin/storage", "tiffin-storage.service")
	defer func() { incMu.Lock(); delete(includes, "storage"); incMu.Unlock() }()
	if inc := included()["storage"]; inc.path != "/var/lib/tiffin/storage" || len(inc.units) != 1 {
		t.Fatal("include not registered")
	}
	if !strings.Contains(pgbackrestConf, "repo1-path="+RepoPath) || !strings.Contains(pgbackrestConf, "pg1-path=/var/lib/tiffin/postgres/18/data") {
		t.Fatal("pgbackrest config")
	}
}
