package backup

// Point-in-time restore. Postgres archives every WAL segment to the
// pgBackRest repository, so it can go back to any moment since the oldest
// backup set: pgBackRest restores the newest set taken before that moment
// and Postgres replays the archived WAL up to it (--type=time). Valkey, the
// platform state and registered files have only their set's snapshot, so
// they come back from that same set: the newest one at or before the moment.
//
// Two things keep a time restore from failing halfway:
//   - Recovery to a time stops at the first commit after it; one that never
//     finds such a commit fails, and Postgres does not start. Every backup
//     writes a marker commit first (markCommit), and a time restore takes its
//     safety backup before stopping Postgres, so every moment in the range
//     has a commit after it in the archive.
//   - A restore starts a new timeline. The restore follows the set's own
//     timeline (--target-timeline=current), and moments between a restore's
//     safety backup and the next set (when the timeline changed) are refused.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/mod/postgres"
)

// BackupRestorable is the range a point-in-time restore can reach.
type BackupRestorable struct {
	Earliest time.Time `json:"earliest" doc:"The oldest moment Postgres can be restored to: when the oldest backup set finished"`
	Latest   time.Time `json:"latest" doc:"The newest: now (a time restore first takes a safety backup, which archives everything up to it)"`
}

// restorableRange is the range over list (newest first); nil without a
// successful set.
func restorableRange(list []Backup, now time.Time) *BackupRestorable {
	var oldest *Backup
	for i := range list {
		if list[i].Status == "ok" && list[i].Postgres.Label != "" {
			oldest = &list[i]
		}
	}
	if oldest == nil {
		return nil
	}
	return &BackupRestorable{Earliest: oldest.FinishedAt.UTC(), Latest: now.UTC().Truncate(time.Second)}
}

// timeLayouts are what the API accepts for a moment; those without an
// offset are UTC.
var timeLayouts = []string{time.RFC3339Nano, "2006-01-02 15:04:05Z07:00", "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02 15:04"}

// parseMoment reads a restore time.
func parseMoment(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, l := range timeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("time %q is not a time: use RFC 3339 (2026-10-07T14:32:00Z, or with an offset) or 2026-10-07 14:32 (UTC)", s)
}

// pickForTime chooses the set a restore to t starts from: the newest
// successful one that finished at or before t. list is newest first.
func pickForTime(list []Backup, t, now time.Time) (*Backup, error) {
	r := restorableRange(list, now)
	if r == nil {
		return nil, errors.New("this box has no successful backup to start from")
	}
	if t.Before(r.Earliest) {
		return nil, fmt.Errorf("%s is before the oldest moment this box can restore to, %s (when its oldest backup finished)", stamp(t), stamp(r.Earliest))
	}
	if t.After(now) {
		return nil, fmt.Errorf("%s is in the future; the newest moment is now (%s)", stamp(t), stamp(now))
	}
	var next *Backup // the oldest successful set after the chosen one
	for i := range list {
		b := &list[i]
		if b.Status != "ok" || b.Postgres.Label == "" {
			continue
		}
		if b.FinishedAt.After(t) {
			next = b
			continue
		}
		after := "after the next backup"
		if next != nil {
			after = "after " + stamp(next.FinishedAt)
		}
		if b.Trigger == "pre-restore" {
			// The box was restored right after this set: the database went on
			// along another timeline, and no set records where it was in between.
			return nil, fmt.Errorf("the box was restored just after %s, so the moments from then until the next backup can't be reached; pick a moment before %s or %s, or restore backup %s itself",
				stamp(b.FinishedAt), stamp(b.StartedAt), after, b.ID)
		}
		if next != nil && !next.Postgres.MarkAt.IsZero() && !t.Before(next.Postgres.MarkAt) {
			return nil, fmt.Errorf("a backup was running at %s; pick a moment before %s or %s", stamp(t), stamp(next.Postgres.MarkAt), after)
		}
		return b, nil
	}
	return nil, fmt.Errorf("no backup finished at or before %s", stamp(t))
}

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05 UTC") }

// pgTarget is t as pgBackRest's --target (recovery_target_time).
func pgTarget(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05.000000") + "+00" }

// restoreArgs are pgBackRest's restore options for src: the set itself
// (consistent at its end), or, with src.at, the set and the WAL after it up
// to that moment, along the set's own timeline.
func restoreArgs(src restoreFrom) []string {
	args := []string{"--repo=" + strconv.Itoa(src.repo), "--delta", "--set=" + src.label}
	if src.at != nil {
		args = append(args, "--type=time", "--target="+pgTarget(*src.at), "--target-timeline=current")
	} else {
		args = append(args, "--type=immediate")
	}
	return append(args, "--target-action=promote", "--log-level-console=warn")
}

// markCommit writes a commit to the live cluster and returns its time: a
// transaction that only takes a transaction ID still writes a commit record
// to the WAL, which a point-in-time recovery can stop at.
func markCommit(ctx context.Context) (time.Time, error) {
	c, err := postgres.Admin(ctx, "postgres")
	if err != nil {
		return time.Time{}, err
	}
	defer c.Close(context.WithoutCancel(ctx))
	if _, err := c.Exec(ctx, `SELECT pg_current_xact_id()`); err != nil {
		return time.Time{}, fmt.Errorf("marker commit: %w", err)
	}
	return time.Now().UTC(), nil
}

// timeline is the timeline of a pgBackRest backup (the first 8 hex digits
// of its first WAL segment), "" when unknown.
func timeline(info []repoBackup, label string) string {
	for _, r := range info {
		if r.Label == label && len(r.Archive.Start) >= 8 {
			return r.Archive.Start[:8]
		}
	}
	return ""
}

// pitrDrill picks a point-in-time drill from list (newest first): the
// second-newest set, replayed to halfway between it and the newest one. It
// needs both on one timeline (no restore in between) and the newest one's
// marker commit, so recovery has a commit after the target to stop at.
func pitrDrill(list []Backup, info []repoBackup) (base, next *Backup, at time.Time, ok bool) {
	var okSets []*Backup
	for i := range list {
		if list[i].Status == "ok" && list[i].Postgres.Label != "" {
			okSets = append(okSets, &list[i])
			if len(okSets) == 2 {
				break
			}
		}
	}
	if len(okSets) < 2 {
		return nil, nil, time.Time{}, false
	}
	next, base = okSets[0], okSets[1]
	if base.Trigger == "pre-restore" || next.Postgres.MarkAt.IsZero() || next.Postgres.MarkAt.Sub(base.FinishedAt) < 2*time.Second {
		return nil, nil, time.Time{}, false
	}
	tl := timeline(info, base.Postgres.Label)
	if tl == "" || tl != timeline(info, next.Postgres.Label) {
		return nil, nil, time.Time{}, false
	}
	at = base.FinishedAt.Add(next.Postgres.MarkAt.Sub(base.FinishedAt) / 2).UTC().Truncate(time.Second)
	return base, next, at, true
}

// intersectCatalogs keeps the databases and tables both catalogs have: what
// a copy restored to a moment between them must hold. Either may be nil.
func intersectCatalogs(a, b *clusterCatalog) *clusterCatalog {
	if a == nil || b == nil {
		if a == nil {
			return b
		}
		return a
	}
	out := &clusterCatalog{TakenAt: a.TakenAt, Databases: []clusterDB{}}
	for _, da := range a.Databases {
		db := b.db(da.Name)
		if db == nil {
			continue
		}
		keep := clusterDB{Name: da.Name, Tables: []clusterTable{}, Err: da.Err}
		have := map[string]bool{}
		for _, t := range db.Tables {
			have[t.Name] = true
		}
		for _, t := range da.Tables {
			if have[t.Name] {
				keep.Tables = append(keep.Tables, t)
			}
		}
		out.Databases = append(out.Databases, keep)
	}
	return out
}
