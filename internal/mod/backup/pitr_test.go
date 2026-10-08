package backup

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestScheduleDefaults(t *testing.T) {
	if d := DefaultSchedule; d.FullEveryHours != 24 || d.IncrementalEveryHours != 6 || d.RetainFull != 7 {
		t.Fatalf("default: %+v", d)
	}
	if got := scheduleFrom(nil, false); got != DefaultSchedule {
		t.Fatalf("nothing saved: %+v", got)
	}
	// The old default, saved by the dashboard or the API, becomes the new one.
	old, _ := json.Marshal(oldDefaultSchedule)
	if got := scheduleFrom(old, true); got != DefaultSchedule {
		t.Fatalf("old default saved: %+v", got)
	}
	// Saved before drills existed: the drill fields fill in from the default, and it is still the old default.
	if got := scheduleFrom([]byte(`{"enabled":true,"fullEveryHours":24,"incrementalEveryHours":1,"retainFull":7}`), true); got != DefaultSchedule {
		t.Fatalf("old default without drill fields: %+v", got)
	}
	// Anything the owner chose stays, hourly included.
	mine := oldDefaultSchedule
	mine.RetainFull = 14
	raw, _ := json.Marshal(mine)
	if got := scheduleFrom(raw, true); got != mine {
		t.Fatalf("own schedule changed: %+v", got)
	}
	p := testPlatform(t)
	ctx := context.Background()
	_ = p.DB.KVPut(ctx, nsMeta, "schedule", old)
	if got := getSchedule(ctx, p); got.IncrementalEveryHours != 6 {
		t.Fatalf("stored old default: %+v", got)
	}
}

// sets builds a list, newest first, of successful sets finishing at the
// given times (oldest first in the arguments), each taking a minute.
func sets(ends ...time.Time) []Backup {
	out := make([]Backup, len(ends))
	for i, e := range ends {
		b := Backup{ID: "bk_" + e.Format("150405"), Status: "ok", Trigger: "schedule", StartedAt: e.Add(-time.Minute), FinishedAt: e}
		b.Postgres.Label = "L" + e.Format("150405")
		b.Postgres.MarkAt = e.Add(-time.Minute + time.Second)
		out[len(ends)-1-i] = b
	}
	return out
}

func TestRestorableRange(t *testing.T) {
	now := time.Date(2026, 10, 7, 18, 0, 0, 500, time.UTC)
	if restorableRange(nil, now) != nil {
		t.Fatal("no sets: no range")
	}
	t0 := time.Date(2026, 10, 1, 0, 30, 0, 0, time.UTC)
	list := sets(t0, t0.Add(6*time.Hour), t0.Add(24*time.Hour))
	failed := Backup{ID: "bk_old", Status: "failed", FinishedAt: t0.Add(-time.Hour)}
	list = append(list, failed)
	r := restorableRange(list, now)
	if !r.Earliest.Equal(t0) || !r.Latest.Equal(now.Truncate(time.Second)) {
		t.Fatalf("range: %+v", r)
	}
}

func TestPickForTime(t *testing.T) {
	t0 := time.Date(2026, 10, 7, 0, 1, 0, 0, time.UTC)
	now := t0.Add(13 * time.Hour)
	list := sets(t0, t0.Add(6*time.Hour), t0.Add(12*time.Hour))
	cases := []struct {
		at   time.Time
		want string // set ID, or part of the error
	}{
		{t0, list[2].ID}, // the moment the oldest finished
		{t0.Add(3 * time.Hour), list[2].ID},
		{t0.Add(6 * time.Hour), list[1].ID},
		{t0.Add(12*time.Hour + 30*time.Minute), list[0].ID},
		{now, list[0].ID},
		{t0.Add(-time.Second), "before the oldest moment"},
		{now.Add(time.Minute), "in the future"},
		// Between the next set's marker commit and its end: no commit is sure to follow.
		{t0.Add(6*time.Hour - 30*time.Second), "a backup was running"},
	}
	for _, c := range cases {
		b, err := pickForTime(list, c.at, now)
		got := ""
		if err != nil {
			got = err.Error()
		} else {
			got = b.ID
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("at %s: got %q, want %q", c.at, got, c.want)
		}
	}
	// A restore ran right after a safety set: the moments until the next set are refused.
	list[1].Trigger = "pre-restore"
	if _, err := pickForTime(list, t0.Add(7*time.Hour), now); err == nil || !strings.Contains(err.Error(), "restored just after") {
		t.Fatalf("after a restore: %v", err)
	}
	if b, err := pickForTime(list, t0.Add(5*time.Hour), now); err != nil || b.ID != list[2].ID {
		t.Fatalf("before the restore's safety set: %v %v", b, err)
	}
	if b, err := pickForTime(list, t0.Add(12*time.Hour+time.Minute), now); err != nil || b.ID != list[0].ID {
		t.Fatalf("after the next set: %v %v", b, err)
	}
}

func TestParseMoment(t *testing.T) {
	want := time.Date(2026, 10, 7, 14, 32, 0, 0, time.UTC)
	for _, s := range []string{"2026-10-07T14:32:00Z", "2026-10-07 14:32", "2026-10-07T14:32", "2026-10-07 14:32:00", "2026-10-07T16:32:00+02:00", " 2026-10-07 14:32:00Z "} {
		got, err := parseMoment(s)
		if err != nil || !got.Equal(want) || got.Location() != time.UTC {
			t.Errorf("%q: %v %v", s, got, err)
		}
	}
	if _, err := parseMoment("yesterday"); err == nil {
		t.Error("not a time must fail")
	}
}

func TestRestoreArgs(t *testing.T) {
	src := restoreFrom{repo: 1, label: "20261007-000100F"}
	if got := strings.Join(restoreArgs(src), " "); got != "--repo=1 --delta --set=20261007-000100F --type=immediate --target-action=promote --log-level-console=warn" {
		t.Fatalf("set restore: %s", got)
	}
	at := time.Date(2026, 10, 7, 14, 32, 5, 123456789, time.FixedZone("x", 2*3600))
	src.at = &at
	got := strings.Join(restoreArgs(src), " ")
	if got != "--repo=1 --delta --set=20261007-000100F --type=time --target=2026-10-07 12:32:05.123456+00 --target-timeline=current --target-action=promote --log-level-console=warn" {
		t.Fatalf("time restore: %s", got)
	}
}

func TestConfirmKeyCoversTime(t *testing.T) {
	a := &BackupRestorePreview{Backup: "bk_1", From: SourceLocal, Targets: []string{"postgres"}}
	at := time.Date(2026, 10, 7, 14, 32, 0, 0, time.UTC)
	b := *a
	b.Time = &at
	ka, _ := json.Marshal(a.Key())
	kb, _ := json.Marshal(b.Key())
	if string(ka) == string(kb) {
		t.Fatal("the confirm value must change with the moment")
	}
}

func TestPITRDrill(t *testing.T) {
	t0 := time.Date(2026, 10, 7, 0, 1, 0, 0, time.UTC)
	list := sets(t0, t0.Add(6*time.Hour))
	info := []repoBackup{{Label: list[1].Postgres.Label}, {Label: list[0].Postgres.Label}}
	info[0].Archive.Start = "000000010000000000000004"
	info[1].Archive.Start = "000000010000000000000009"
	base, next, at, ok := pitrDrill(list, info)
	if !ok || base.ID != list[1].ID || next.ID != list[0].ID {
		t.Fatalf("pick: %v %v %v", base, next, ok)
	}
	if !at.After(base.FinishedAt) || !at.Before(next.Postgres.MarkAt) {
		t.Fatalf("target %s not between %s and %s", at, base.FinishedAt, next.Postgres.MarkAt)
	}
	// Another timeline (a restore in between), an old set without a marker, one set: no PITR drill.
	info[1].Archive.Start = "000000020000000000000009"
	if _, _, _, ok := pitrDrill(list, info); ok {
		t.Error("different timelines")
	}
	info[1].Archive.Start = "000000010000000000000009"
	list[0].Postgres.MarkAt = time.Time{}
	if _, _, _, ok := pitrDrill(list, info); ok {
		t.Error("no marker")
	}
	if _, _, _, ok := pitrDrill(list[:1], info); ok {
		t.Error("one set")
	}
}

func TestWantPITRDrill(t *testing.T) {
	at := time.Now()
	d := func(source, status string, pitr bool) BackupDrill {
		x := BackupDrill{Source: source, Status: status}
		if pitr {
			x.TargetTime = &at
		}
		return x
	}
	cases := []struct {
		drills []BackupDrill
		want   bool
	}{
		{nil, false},
		{[]BackupDrill{d("", DrillPassed, false)}, true},
		{[]BackupDrill{d("local", DrillPassed, true)}, false},
		{[]BackupDrill{d("offsite", DrillPassed, false), d("local", DrillPassed, false)}, true},
		{[]BackupDrill{d("local", DrillFailed, true)}, true},
		{[]BackupDrill{d("local", DrillFailed, false)}, false},
	}
	for i, c := range cases {
		if got := wantPITRDrill(c.drills); got != c.want {
			t.Errorf("case %d: %v, want %v", i, got, c.want)
		}
	}
}

func TestIntersectCatalogs(t *testing.T) {
	a := &clusterCatalog{Databases: []clusterDB{{Name: "p_a", Tables: []clusterTable{{Name: "public.x"}, {Name: "public.gone"}}}, {Name: "p_dropped"}}}
	b := &clusterCatalog{Databases: []clusterDB{{Name: "p_a", Tables: []clusterTable{{Name: "public.x"}, {Name: "public.new"}}}, {Name: "p_new"}}}
	got := intersectCatalogs(a, b)
	if len(got.Databases) != 1 || got.Databases[0].Name != "p_a" || len(got.Databases[0].Tables) != 1 || got.Databases[0].Tables[0].Name != "public.x" {
		t.Fatalf("intersection: %+v", got)
	}
	if intersectCatalogs(a, nil) != a || intersectCatalogs(nil, b) != b {
		t.Fatal("a missing side keeps the other")
	}
}
