package runtime

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sync/atomic"
	"time"

	"github.com/shiptiffin/tiffin/internal/platform"
)

// The queue module pushes jobs, cron ticks and workflow turns to apps over
// localhost and pins each workflow run to the release (deploy) that started
// it. This file is the runtime's side of that contract (see
// internal/mod/queue/contract.go): where a release's instances listen, which
// release is current, and keeping old releases running while runs are
// pinned to them.

// errReleaseGone matches the queue's ErrReleaseGone by its text.
type errReleaseGone struct{ release string }

func (e errReleaseGone) Error() string { return "release " + e.release + ": release gone" }

var rrCounter atomic.Uint64

// AppEndpoint returns "http://127.0.0.1:PORT" for one instance of the app's
// release ("" = current). Old releases still draining for pinned runs are
// reachable too; anything else is "release gone".
func (m *Module) AppEndpoint(ctx context.Context, p *platform.Platform, project, app, release string) (string, error) {
	r, err := m.rt()
	if err != nil {
		return "", err
	}
	st, err := r.st.getState(ctx, project, app, "")
	if err != nil {
		return "", err
	}
	if st.Live == "" || st.Stopped {
		if release != "" {
			return "", errReleaseGone{release}
		}
		return "", fmt.Errorf("app %s/%s is not deployed", project, app)
	}
	// A delivery is activity, and a sleeping app wakes before it is sent.
	r.touch(envKey(project, app, ""))
	if st.Sleeping && (release == "" || release == st.Live) {
		if _, _, err := r.wake(ctx, project, app, "", wakeDelivery); err != nil {
			return "", fmt.Errorf("app %s/%s was asleep and could not start: %w", project, app, err)
		}
		if st, err = r.st.getState(ctx, project, app, ""); err != nil {
			return "", err
		}
	}
	pick := func(ins []Instance) (string, error) {
		if len(ins) == 0 {
			return "", fmt.Errorf("app %s/%s has no running instances", project, app)
		}
		in := ins[int(rrCounter.Add(1)%uint64(len(ins)))]
		return fmt.Sprintf("http://127.0.0.1:%d", in.Port), nil
	}
	if release == "" || release == st.Live {
		return pick(st.Instances)
	}
	for _, ds := range st.Draining {
		if ds.Release == release {
			return pick(ds.Instances)
		}
	}
	return "", errReleaseGone{release}
}

// CurrentRelease is the live deploy of the app ("" when not deployed).
func (m *Module) CurrentRelease(ctx context.Context, p *platform.Platform, project, app string) (string, error) {
	r, err := m.rt()
	if err != nil {
		return "", err
	}
	st, err := r.st.getState(ctx, project, app, "")
	if err != nil || st.Stopped {
		return "", err
	}
	return st.Live, nil
}

type pinnedReleaser interface {
	PinnedReleases(ctx context.Context, p *platform.Platform, project, app string) ([]string, error)
}

// pinnedReleases asks the queue module (if any) which releases have
// unfinished workflow runs. ok is false when the queue gave no answer
// (without a queue module nothing is pinned).
func (r *rt) pinnedReleases(ctx context.Context, project, app string) ([]string, bool) {
	for _, mod := range platform.Modules() {
		if pr, ok := mod.(pinnedReleaser); ok {
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			rels, err := pr.PinnedReleases(ctx, r.p, project, app)
			if err != nil {
				r.p.Log.Warn("pinned releases", "project", project, "app", app, "err", err)
				return nil, false
			}
			return rels, true
		}
	}
	return nil, true
}

// pinned reports whether workflow runs may be pinned to release: they are,
// or the queue gave no answer (the reaper asks again).
func (r *rt) pinned(ctx context.Context, project, app, release string) bool {
	rels, ok := r.pinnedReleases(ctx, project, app)
	return !ok || slices.Contains(rels, release)
}

// drainMax caps how long an old release may run for pinned workflows.
func drainMax() time.Duration {
	if v, err := time.ParseDuration(os.Getenv("TIFFIN_DRAIN_MAX")); err == nil && v > 0 {
		return v
	}
	return 24 * time.Hour
}

// reapDrained stops old releases once no workflow run is pinned to them
// (or they hit drainMax; the queue then moves those runs to the current
// release).
func (r *rt) reapDrained(ctx context.Context) {
	states, err := r.st.allStates(ctx)
	if err != nil {
		return
	}
	for _, s := range states {
		if len(s.Draining) == 0 {
			continue
		}
		func() {
			unlock := r.lock(envKey(s.Project, s.App, s.Preview))
			defer unlock()
			st, err := r.st.getState(ctx, s.Project, s.App, s.Preview)
			if err != nil || len(st.Draining) == 0 {
				return
			}
			// Asked under the lock: a deploy that adds a release to
			// Draining (it had pins then) cannot slip in between.
			rels, ok := r.pinnedReleases(ctx, s.Project, s.App)
			var keep []DrainSet
			var stop []Instance
			for _, ds := range st.Draining {
				expired := time.Since(ds.Since) > drainMax()
				if (ok && !slices.Contains(rels, ds.Release)) || expired {
					stop = append(stop, ds.Instances...)
					r.p.Log.Info("release drained", "project", s.Project, "app", s.App, "release", ds.Release, "expired", expired)
					continue
				}
				keep = append(keep, ds)
			}
			if len(stop) == 0 {
				return
			}
			st.Draining = keep
			if r.st.putState(ctx, st) == nil {
				r.removeInstances(ctx, stop)
			}
		}()
	}
}
