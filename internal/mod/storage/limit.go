package storage

import (
	"context"
	"fmt"
	"sync"

	"github.com/btahir/tiffin/internal/platform"
)

// A project's storage limit (its quota) counts its database and its files
// together. The box's disk guard measures the database and keeps this
// package told (SetDatabaseBytes), and holds a project read-only when it is
// over its limit or the data disk is nearly full (SetReadOnly). Uploads are
// refused with QuotaExceeded in both cases.

var limits = struct {
	sync.Mutex
	held    map[string][2]string // project → reason ("disk", "limit") and why its uploads are held
	db      map[string]int64     // project → database bytes, last measured
	changed chan struct{}
}{held: map[string][2]string{}, db: map[string]int64{}, changed: make(chan struct{}, 1)}

// SetReadOnly holds a project's uploads for reason ("disk" or "limit"; why
// says which limit and how to fix it) or, with why "", lifts the hold.
func SetReadOnly(project, reason, why string) {
	limits.Lock()
	defer limits.Unlock()
	if why == "" {
		delete(limits.held, project)
	} else {
		limits.held[project] = [2]string{reason, why}
	}
}

// ReadOnly returns why a project's uploads are held, or "".
func ReadOnly(project string) string {
	limits.Lock()
	defer limits.Unlock()
	return limits.held[project][1]
}

// SetDatabaseBytes records the size of a project's databases, which counts
// toward its storage limit with its files.
func SetDatabaseBytes(project string, n int64) {
	limits.Lock()
	defer limits.Unlock()
	limits.db[project] = n
}

func databaseBytes(project string) int64 {
	limits.Lock()
	defer limits.Unlock()
	return limits.db[project]
}

// LimitsChanged fires when an owner sets a storage limit, so the guard looks
// again now rather than at its next round.
func LimitsChanged() <-chan struct{} { return limits.changed }

func limitsChanged() {
	select {
	case limits.changed <- struct{}{}:
	default:
	}
}

// Limit returns a project's storage limit in bytes (0: none) and whether it
// was set for the project rather than taken from the box default.
func Limit(ctx context.Context, p *platform.Platform, project string) (int64, bool, error) {
	return quotaFor(ctx, p, project)
}

// FilesBytes returns every project's bucket bytes: the last scan plus
// uploads since.
func FilesBytes(ctx context.Context, p *platform.Platform) (map[string]int64, error) {
	meta, err := allMeta(ctx, p)
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	t := mod.tracker()
	for project := range projectsOf(meta) {
		out[project] = t.project(meta, project)
	}
	return out, nil
}

func projectsOf(meta map[string]*bucketMeta) map[string]bool {
	out := map[string]bool{}
	for _, b := range meta {
		out[b.Project] = true
	}
	return out
}

// refusal says why writing n more bytes to a project's buckets is refused
// (what happened, and how to fix it), or "" when it is allowed.
func (m *Module) refusal(ctx context.Context, p *platform.Platform, meta map[string]*bucketMeta, project string, n int64) (string, string) {
	limits.Lock()
	h := limits.held[project]
	limits.Unlock()
	if h[0] == "disk" {
		return h[1], ""
	}
	// A project held for its limit goes by the limit itself: the moment it
	// is raised (or files are deleted) uploads fit again, before the guard's
	// next round lifts the hold.
	limit, _, err := quotaFor(ctx, p, project)
	if err != nil || limit <= 0 {
		return "", ""
	}
	files, db := m.tracker().project(meta, project), databaseBytes(project)
	if used := files + db; used+max(n, 0) > limit {
		return fmt.Sprintf("project %s is over its storage limit: %s used of %s (database %s, files %s).", project, HumanBytes(used), HumanBytes(limit), HumanBytes(db), HumanBytes(files)),
			fmt.Sprintf("Delete files, or ask the box owner to raise the limit (tiffin storage quota set %s --max-bytes N).", project)
	}
	return "", ""
}
