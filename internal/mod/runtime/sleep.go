package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
)

// Sleeping apps. Previews sleep after PreviewIdle without requests. A
// project's production apps sleep only when the project opts in
// (sleepAfter in its manifest): after that long with no requests and no
// job, cron or workflow deliveries. A sleeping app's containers are
// stopped; its routes stay with the switchboard, which holds the next
// request, starts the app and forwards it. A delivery wakes it the same
// way before it is sent, and a deploy starts it as usual.
//
// Activity is kept in memory (lastSeen) and written to the database at
// most once a minute and on shutdown, so a restart neither resets the
// clock nor skips it.

const nsActivity = "runtime/activity"

// What woke an app environment.
const (
	wakeRequest  = "request"
	wakeDelivery = "delivery"
	wakeNow      = "wake"
	wakeSetting  = "setting"
)

// touch records activity on an app environment now.
func (r *rt) touch(key string) { r.touchAt(key, time.Now()) }

func (r *rt) touchAt(key string, at time.Time) {
	r.mu.Lock()
	if at.After(r.lastSeen[key]) {
		r.lastSeen[key] = at
		r.seenDirty[key] = true
	}
	r.mu.Unlock()
}

// NoteActivity counts a request the edge served on an app's production
// host at at, whoever answered it (the observe module reads the access log).
func (m *Module) NoteActivity(project, app string, at time.Time) {
	if r, err := m.rt(); err == nil {
		if now := time.Now(); at.After(now) {
			at = now
		}
		r.touchAt(envKey(project, app, ""), at)
	}
}

// lastActive is when s last had a request or delivery. Unknown since the
// box started: a preview counts from its last change; production from now,
// so turning sleep on never puts an app to sleep at once.
func (r *rt) lastActive(s *AppState) time.Time {
	key := envKey(s.Project, s.App, s.Preview)
	r.mu.Lock()
	defer r.mu.Unlock()
	seen, ok := r.lastSeen[key]
	if !ok {
		seen = time.Now()
		if s.Preview != "" {
			seen = s.UpdatedAt
		}
		r.lastSeen[key], r.seenDirty[key] = seen, true
	}
	return seen
}

// loadActivity reads the activity a previous run saved.
func (r *rt) loadActivity(ctx context.Context) error {
	kv, err := r.p.DB.KVList(ctx, nsActivity)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, b := range kv {
		var t time.Time
		if json.Unmarshal(b, &t) == nil && t.After(r.lastSeen[k]) {
			r.lastSeen[k] = t
		}
	}
	return nil
}

// saveActivity writes the activity that changed since the last save, and
// forgets environments that are gone.
func (r *rt) saveActivity(ctx context.Context) {
	r.mu.Lock()
	changed := make(map[string]time.Time, len(r.seenDirty))
	for k := range r.seenDirty {
		changed[k] = r.lastSeen[k]
	}
	r.seenDirty = map[string]bool{}
	r.mu.Unlock()
	for k, t := range changed {
		b, _ := json.Marshal(t.UTC())
		if err := r.p.DB.KVPut(ctx, nsActivity, k, b); err != nil {
			r.mu.Lock()
			r.seenDirty[k] = true // next time
			r.mu.Unlock()
		}
	}
	states, err := r.st.allStates(ctx)
	if err != nil {
		return
	}
	live := map[string]bool{}
	for _, s := range states {
		live[envKey(s.Project, s.App, s.Preview)] = true
	}
	saved, err := r.p.DB.KVList(ctx, nsActivity)
	if err != nil {
		return
	}
	for k := range saved {
		if !live[k] {
			_ = r.p.DB.KVDelete(ctx, nsActivity, k)
			r.mu.Lock()
			delete(r.lastSeen, k)
			r.mu.Unlock()
		}
	}
}

// sleepAfter is how long the project's production apps may go unused
// before they sleep (0: never). Options.SleepAfter replaces any opted-in
// project's time (tests).
func (r *rt) sleepAfter(ctx context.Context, project string) time.Duration {
	_, res, err := r.p.DB.Load(ctx, project)
	if err != nil {
		return 0
	}
	var ps change.ProjectSpec
	if rs, ok := res[change.KindProject]; !ok || json.Unmarshal(rs.Spec, &ps) != nil {
		return 0
	}
	d, err := manifest.ParseSleepAfter(ps.SleepAfter)
	if err != nil || d == 0 {
		return 0
	}
	if r.opt.SleepAfter > 0 {
		return r.opt.SleepAfter
	}
	return d
}

// sleepIdle puts to sleep the previews nobody requested for PreviewIdle
// and the production apps of opted-in projects nobody used for their
// project's sleepAfter. It wakes production apps whose project stopped
// letting them sleep.
func (r *rt) sleepIdle(ctx context.Context) {
	r.pullActivity()
	states, err := r.st.allStates(ctx)
	if err != nil {
		return
	}
	after := map[string]time.Duration{}
	for _, s := range states {
		if s.Stopped || s.Live == "" {
			continue
		}
		idle := r.opt.PreviewIdle
		if s.Preview == "" {
			d, ok := after[s.Project]
			if !ok {
				d = r.sleepAfter(ctx, s.Project)
				after[s.Project] = d
			}
			if s.Sleeping && d == 0 {
				r.wakeUnwanted(s)
				continue
			}
			// Old releases still running for pinned workflow runs are work under way.
			if d == 0 || len(s.Draining) > 0 {
				continue
			}
			idle = d
		}
		if s.Sleeping || len(s.Instances) == 0 {
			continue
		}
		if time.Since(r.lastActive(s)) < idle || r.busy(s.Instances) > 0 {
			continue // a request still under way keeps the app awake, however long it runs
		}
		if s.Preview == "" && r.delivering(ctx, s.Project, s.App) {
			continue // so does a job under way
		}
		r.sleep(ctx, s.Project, s.App, s.Preview, idle)
	}
}

type deliveryReporter interface {
	Delivering(ctx context.Context, p *platform.Platform, project, app string) (bool, error)
}

// delivering asks the queue module whether a delivery to the app is under
// way (false when it cannot say: no delivery reaches a broken queue's apps).
func (r *rt) delivering(ctx context.Context, project, app string) bool {
	for _, mod := range platform.Modules() {
		if d, ok := mod.(deliveryReporter); ok {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			busy, err := d.Delivering(ctx, r.p, project, app)
			return err == nil && busy
		}
	}
	return false
}

// wakeUnwanted starts a sleeping production app whose project no longer
// lets it sleep, in the background, trying at most every 5 minutes.
func (r *rt) wakeUnwanted(s *AppState) {
	key := envKey(s.Project, s.App, "")
	r.mu.Lock()
	if time.Since(r.wakeTried[key]) < 5*time.Minute {
		r.mu.Unlock()
		return
	}
	r.wakeTried[key] = time.Now()
	r.mu.Unlock()
	go func() {
		if _, _, err := r.wake(r.ctx, s.Project, s.App, "", wakeSetting); err != nil {
			r.p.Log.Warn("runtime: wake an app that may no longer sleep", "project", s.Project, "app", s.App, "err", err)
		}
	}()
}

// sleep stops an app environment's instances, keeping its routes, unless it
// had activity within idle (0: sleep now, asked for). The stopped containers
// are kept (Parked): their memory is freed, and a wake only starts them
// again instead of creating new ones.
func (r *rt) sleep(ctx context.Context, project, app, preview string, idle time.Duration) {
	unlock := r.lock(envKey(project, app, preview))
	defer unlock()
	st, err := r.st.getState(ctx, project, app, preview)
	if err != nil || st.Sleeping || len(st.Instances) == 0 || (idle > 0 && time.Since(r.lastActive(st)) < idle) {
		return
	}
	// Only while the edge answers: one out of reach may still send requests to the instances.
	if r.syncEdge() != nil {
		return
	}
	ins := st.Instances
	now := time.Now().UTC()
	st.Instances, st.Sleeping, st.SleptAt = nil, true, &now
	st.Parked = append(st.Parked[:0:0], ins...)
	if r.st.putState(ctx, st) != nil {
		return
	}
	// From here new requests wait for a wake; one that got in just before
	// finishes first.
	for i := 0; i < 200 && r.busy(ins) > 0; i++ {
		time.Sleep(25 * time.Millisecond)
	}
	if failed := r.park(ctx, ins); len(failed) > 0 {
		// Not kept: removed instead, and the wake creates new ones.
		st.Parked = nil
		_ = r.st.putState(ctx, st)
		r.removeInstances(ctx, ins)
	}
	r.p.Log.Info("app asleep", "project", project, "app", app, "preview", preview, "idle", idle)
}

// park stops instances and keeps them, ports included. It returns the ones
// it could not stop.
func (r *rt) park(ctx context.Context, ins []Instance) []Instance {
	ctx, cancel := cleanupContext(ctx)
	defer cancel()
	var (
		mu     sync.Mutex
		failed []Instance
		wg     sync.WaitGroup
	)
	for _, in := range ins {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.eng.Stop(ctx, in.Name, r.opt.StopGrace); err != nil {
				r.p.Log.Warn("runtime: stop a sleeping app's container (removing it instead)", "name", in.Name, "err", err)
				mu.Lock()
				failed = append(failed, in)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return failed
}

// unpark starts parked instances again and waits for their health checks.
func (r *rt) unpark(ctx context.Context, d *Deploy, spec *manifest.App, ins []Instance) error {
	errs := make([]error, len(ins))
	var wg sync.WaitGroup
	for i, in := range ins {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if errs[i] = r.eng.Start(ctx, in.Name); errs[i] == nil {
				errs[i] = r.waitHealthy(ctx, in, spec, r.logFile(d.Project, d.App, d.Preview, d.ID, serialOf(in.Name)))
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// wake returns a running instance's port for an app environment, starting
// it first if it sleeps. woke reports whether this call started it.
func (r *rt) wake(ctx context.Context, project, app, preview, trigger string) (port int, woke bool, err error) {
	unlock := r.lock(envKey(project, app, preview))
	defer unlock()
	st, err := r.st.getState(ctx, project, app, preview)
	if err != nil {
		return 0, false, err
	}
	if len(st.Instances) > 0 && !st.Sleeping {
		return st.Instances[0].Port, false, nil
	}
	if st.Live == "" {
		return 0, false, errNotFound
	}
	d, err := r.st.getDeploy(ctx, project, app, st.Live)
	if err != nil {
		return 0, false, err
	}
	spec, err := r.appSpec(ctx, project, app)
	if err != nil {
		return 0, false, err
	}
	began := time.Now()
	// Detach from the caller: a client giving up must not abort the start.
	if err := r.promoteLocked(r.ctx, d, spec, modeWake, io.Discard); err != nil {
		r.p.Log.Warn("app could not wake", "project", project, "app", app, "preview", preview, "trigger", trigger, "err", err)
		return 0, false, err
	}
	secs := roundMs(time.Since(began).Seconds())
	st, err = r.st.getState(ctx, project, app, preview)
	if err != nil {
		return 0, false, err
	}
	st.LastWake = &Wake{At: began.UTC(), Trigger: trigger, StartSeconds: secs}
	_ = r.st.putState(ctx, st)
	r.p.Log.Info("app woke", "project", project, "app", app, "preview", preview, "trigger", trigger, "start_seconds", secs)
	if len(st.Instances) == 0 {
		return 0, true, fmt.Errorf("no instance")
	}
	return st.Instances[0].Port, true, nil
}

// wokeFirstByte records how long a request that woke its app waited for
// the first byte of its response.
func (r *rt) wokeFirstByte(project, app, preview string, secs float64) {
	r.p.Log.Info("app woke: first byte", "project", project, "app", app, "preview", preview, "first_byte_seconds", secs)
	unlock := r.lock(envKey(project, app, preview))
	defer unlock()
	st, err := r.st.getState(r.ctx, project, app, preview)
	if err != nil || st.LastWake == nil || st.LastWake.Trigger != wakeRequest {
		return
	}
	st.LastWake.FirstByteSeconds = secs
	_ = r.st.putState(r.ctx, st)
}

// WakeResult is one app Wake now started (or found awake).
type WakeResult struct {
	App   string `json:"app"`
	Woke  bool   `json:"woke" doc:"It was asleep and started now"`
	Error string `json:"error,omitempty" doc:"Why it could not start"`
	Wake  *Wake  `json:"wake,omitempty" doc:"The wake, with its timing"`
}

// wakeProject starts the project's sleeping production apps (or one app).
func (r *rt) wakeProject(ctx context.Context, project, app string) ([]WakeResult, error) {
	states, err := r.st.allStates(ctx)
	if err != nil {
		return nil, err
	}
	out := []WakeResult{}
	for _, s := range states {
		if s.Project != project || s.Preview != "" || s.Live == "" || s.Stopped || (app != "" && s.App != app) {
			continue
		}
		res := WakeResult{App: s.App}
		if s.Sleeping {
			r.touch(envKey(project, s.App, ""))
			_, woke, err := r.wake(ctx, project, s.App, "", wakeNow)
			res.Woke = woke
			if err != nil {
				res.Error = err.Error()
			} else if st, err := r.st.getState(ctx, project, s.App, ""); err == nil && woke {
				res.Wake = st.LastWake
			}
		}
		out = append(out, res)
	}
	if app != "" && len(out) == 0 {
		return nil, errNotFound
	}
	return out, nil
}

// SleepingApps lists a project's sleeping app environments, as "app" or
// "app@preview", read from the stored states (for usage views).
func SleepingApps(ctx context.Context, db *state.DB, project string) map[string]bool {
	out := map[string]bool{}
	kv, err := db.KVList(ctx, nsState)
	if err != nil {
		return out
	}
	for _, b := range kv {
		var st AppState
		if json.Unmarshal(b, &st) == nil && st.Project == project && st.Sleeping && !st.Stopped {
			k := st.App
			if st.Preview != "" {
				k += "@" + st.Preview
			}
			out[k] = true
		}
	}
	return out
}

func roundMs(f float64) float64 { return math.Round(f*1000) / 1000 }
