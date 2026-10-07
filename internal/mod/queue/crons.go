package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/btahir/tiffin/internal/manifest"
	"github.com/jackc/pgx/v5"
	"github.com/robfig/cron/v3"
)

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// schedule is a cron expression read on the wall clock of a time zone.
type schedule struct {
	spec cron.Schedule // matched against wall-clock times written as UTC
	loc  *time.Location
}

// parseSchedule reads expr in the IANA time zone tz ("" is UTC).
func parseSchedule(expr, tz string) (*schedule, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "Local" {
		return nil, fmt.Errorf("unknown time zone %q", tz)
	}
	spec, err := cronParser.Parse("CRON_TZ=UTC " + expr)
	if err != nil {
		return nil, err
	}
	s := &schedule{spec, loc}
	if s.Next(time.Now()).IsZero() {
		// The parser accepts dates that never come, such as 31 February;
		// a zero next tick would make the cron due for ever.
		return nil, errors.New("the schedule never matches a date (such as 31 February)")
	}
	return s, nil
}

// Next is the first tick after t. Ticks follow the zone's wall clock, so a
// clock change neither repeats nor drops one: a time that happens twice
// runs once (ticks in the repeated hour are passed over) and a time the
// clocks skip runs at the change.
func (s *schedule) Next(t time.Time) time.Time {
	w := wallClock(t.In(s.loc))
	for range 1000 {
		if w = s.spec.Next(w); w.IsZero() {
			return w
		}
		at := time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), w.Second(), 0, s.loc)
		// w does not exist in the zone (clocks skipped it): take the change itself.
		if got := wallClock(at); got.Before(w) {
			_, at = at.ZoneBounds()
		} else if got.After(w) {
			at, _ = at.ZoneBounds()
		}
		if at.After(t) {
			return at.UTC()
		}
	}
	return time.Time{}
}

func wallClock(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
}

// NextRuns returns the next n ticks of a schedule in a time zone after from,
// or a plain error saying what is wrong with it.
func NextRuns(expr, tz string, n int, from time.Time) ([]time.Time, error) {
	if tz == "UTC" {
		tz = ""
	}
	if _, err := time.LoadLocation(tz); err != nil || tz == "Local" {
		return nil, invalid(fmt.Sprintf("%q is not a time zone name", tz), "use an IANA name such as Europe/London, America/New_York or UTC")
	}
	s, err := parseSchedule(expr, tz)
	if err != nil {
		return nil, invalid(fmt.Sprintf("%q is not a schedule: %s", expr, err.Error()),
			"use 5 fields, minute hour day-of-month month day-of-week, such as \"0 9 * * 1-5\" (weekdays at 9:00), or @hourly, @daily, @weekly, @monthly")
	}
	out := make([]time.Time, 0, n)
	for t := from; len(out) < n; {
		if t = s.Next(t); t.IsZero() {
			break
		}
		out = append(out, t)
	}
	return out, nil
}

// cronRow is a stored cron as the loop and triggers need it.
type cronRow struct {
	project, name, schedule, tz, app, path, url string
	timeoutS                                    int
}

// ReconcileCron makes the stored cron match the manifest (spec nil =
// deleted). A paused cron stays paused.
func (e *Engine) ReconcileCron(ctx context.Context, project, name string, spec json.RawMessage) error {
	if spec == nil {
		_, err := e.pool.Exec(ctx, `DELETE FROM tq_crons WHERE project = $1 AND name = $2`, project, name)
		return err
	}
	var c manifest.Cron
	if err := json.Unmarshal(spec, &c); err != nil {
		return err
	}
	if c.URL != "" {
		if err := e.guard.checkURL(c.URL); err != nil {
			return fmt.Errorf("cron %s: %w", name, err)
		}
		c.App, c.Path = "", ""
	} else if c.Path == "" {
		c.Path = "/cron/" + name
	}
	sched, err := parseSchedule(c.Schedule, c.Timezone)
	if err != nil {
		return fmt.Errorf("cron %s: schedule %q: %w", name, c.Schedule, err)
	}
	next := sched.Next(e.now())
	// A changed schedule or time zone restarts from now; an unchanged one
	// keeps its next tick. The manifest takes over a cron of the same name
	// an app's files declared.
	_, err = e.pool.Exec(ctx, `INSERT INTO tq_crons (project, name, schedule, timezone, overlap, app, path, url, timeout_s, next_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (project, name) DO UPDATE SET app = $6, path = $7, url = $8, timeout_s = $9, overlap = $5, origin = '', method = 'POST',
		next_at = CASE WHEN tq_crons.schedule = $3 AND tq_crons.timezone = $4 THEN tq_crons.next_at ELSE $10 END, schedule = $3, timezone = $4`,
		project, name, c.Schedule, c.Timezone, c.Overlap, c.App, c.Path, c.URL, c.TimeoutSeconds, next)
	return err
}

// SetFileCrons replaces the crons app's own files declare (origin is the
// file, such as vercel.json) with crons, read in UTC and called with GET. A
// manifest cron wins over one of the same name, or the same app and path.
func (e *Engine) SetFileCrons(ctx context.Context, project, app, origin string, crons map[string]manifest.Cron) error {
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT name, app, path, origin FROM tq_crons WHERE project = $1 FOR UPDATE`, project)
	if err != nil {
		return err
	}
	type row struct{ app, path, origin string }
	have := map[string]row{}
	manifestPaths := map[string]bool{}
	for rows.Next() {
		var name string
		var r row
		if err := rows.Scan(&name, &r.app, &r.path, &r.origin); err != nil {
			rows.Close()
			return err
		}
		have[name] = r
		if r.origin == "" {
			manifestPaths[r.app+" "+r.path] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	names := make([]string, 0, len(crons))
	for n := range crons {
		names = append(names, n)
	}
	sort.Strings(names)
	keep := []string{}
	for _, name := range names {
		c := crons[name]
		if h, ok := have[name]; (ok && (h.origin == "" || h.app != app)) || manifestPaths[app+" "+c.Path] {
			continue // the manifest's (or another app's) cron
		}
		sched, err := parseSchedule(c.Schedule, "")
		if err != nil {
			e.log.Warn("queue: cron from "+origin+" skipped", "project", project, "app", app, "cron", name, "err", err)
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO tq_crons (project, name, schedule, app, path, next_at, origin, method) VALUES ($1, $2, $3, $4, $5, $6, $7, 'GET')
			ON CONFLICT (project, name) DO UPDATE SET path = $5, origin = $7, method = 'GET',
			next_at = CASE WHEN tq_crons.schedule = $3 THEN tq_crons.next_at ELSE $6 END, schedule = $3`,
			project, name, c.Schedule, app, c.Path, sched.Next(e.now()), origin); err != nil {
			return err
		}
		keep = append(keep, name)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM tq_crons WHERE project = $1 AND app = $2 AND origin = $3 AND NOT (name = ANY($4))`, project, app, origin, keep); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// cronMethod is how a cron job's app is called: POST, or GET for a cron an
// app's vercel.json declares.
func (e *Engine) cronMethod(ctx context.Context, j *jobRow) string {
	var m string
	if j.Cron == nil || j.URL != "" || e.pool.QueryRow(ctx, `SELECT method FROM tq_crons WHERE project = $1 AND name = $2`, j.Project, *j.Cron).Scan(&m) != nil {
		return http.MethodPost
	}
	return m
}

// cronSecret is the app's CRON_SECRET (env or secret), sent to GET crons as
// a bearer token the way Vercel sends it ("" when the app has none).
func (e *Engine) cronSecret(ctx context.Context, j *jobRow) string {
	if e.cfg.AppEnv == nil {
		return ""
	}
	env, err := e.cfg.AppEnv(ctx, j.Project, j.App)
	if err != nil {
		return ""
	}
	return env["CRON_SECRET"]
}

// cronLoop fires due crons. Ticks missed while the box was down fire once
// when it comes back, then the schedule continues from now. A tick whose
// cron's previous run is still queued or running is skipped (unless the
// cron allows overlap). A stopped project's crons wait (see stop.go); a
// paused cron does not tick until it is resumed.
func (e *Engine) cronLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := e.fireDueCrons(ctx); err != nil && ctx.Err() == nil {
			e.log.Error("queue: cron", "err", err)
		}
	}
}

func (e *Engine) fireDueCrons(ctx context.Context) error {
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT c.project, c.name, c.schedule, c.timezone, c.app, c.path, c.url, c.timeout_s, c.next_at,
		NOT c.overlap AND coalesce(j.state IN ('scheduled', 'queued', 'running', 'retrying'), false)
		FROM tq_crons c LEFT JOIN tq_jobs j ON j.id = c.last_job WHERE c.next_at <= now() AND NOT c.paused
		AND c.project NOT IN (SELECT project FROM tq_stopped) FOR UPDATE OF c SKIP LOCKED LIMIT 100`)
	if err != nil {
		return err
	}
	type due struct {
		cronRow
		at   time.Time
		busy bool // the previous run is still going
	}
	var ds []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.project, &d.name, &d.schedule, &d.tz, &d.app, &d.path, &d.url, &d.timeoutS, &d.at, &d.busy); err != nil {
			rows.Close()
			return err
		}
		ds = append(ds, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range ds {
		sched, err := parseSchedule(d.schedule, d.tz)
		if err != nil {
			// Stored before it was refused: look again tomorrow, not every second.
			if _, err := tx.Exec(ctx, `UPDATE tq_crons SET next_at = now() + interval '1 day' WHERE project = $1 AND name = $2`, d.project, d.name); err != nil {
				return err
			}
			continue
		}
		if d.busy {
			e.log.Info("queue: cron tick skipped: the previous run is still queued or running", "project", d.project, "cron", d.name, "tick", d.at)
			if _, err := tx.Exec(ctx, `UPDATE tq_crons SET next_at = $3, skipped_at = $4 WHERE project = $1 AND name = $2`,
				d.project, d.name, sched.Next(e.now()), d.at); err != nil {
				return err
			}
			continue
		}
		id, err := e.enqueueCron(ctx, tx, d.cronRow, d.at, "cron")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE tq_crons SET next_at = $3, last_at = $4, last_job = $5 WHERE project = $1 AND name = $2`,
			d.project, d.name, sched.Next(e.now()), d.at, id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (e *Engine) enqueueCron(ctx context.Context, tx pgx.Tx, c cronRow, at time.Time, by string) (int64, error) {
	cfg, err := e.queueConfig(ctx, tx, c.project, queueCron)
	if err != nil {
		return 0, err
	}
	lease := cfg.LeaseS
	if c.timeoutS > 0 {
		lease = c.timeoutS
	}
	payload, _ := json.Marshal(map[string]any{"cron": c.name, "schedule": c.schedule, "scheduledAt": at.UTC()})
	return e.insertJob(ctx, tx, newJob{project: c.project, queue: queueCron, kind: kindCron, cron: c.name, app: c.app, path: c.path, url: c.url,
		payload: payload, priority: prioNormal, maxAttempts: cfg.MaxAttempts, leaseS: lease, runAt: e.now(), by: by})
}

// CronRun is one recent run of a cron.
type CronRun struct {
	Job        string     `json:"job"`
	State      string     `json:"state"`
	At         time.Time  `json:"at" doc:"When it was queued"`
	DurationMS *int       `json:"durationMs,omitempty" doc:"Latest attempt, start to finish"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// CronInfo is a cron with its next and last run and its recent health.
type CronInfo struct {
	Name           string     `json:"name"`
	Schedule       string     `json:"schedule" doc:"Cron expression, read in timezone"`
	Timezone       string     `json:"timezone" doc:"IANA time zone of the schedule"`
	Overlap        bool       `json:"overlap,omitempty" doc:"Ticks run even while the previous run is still going"`
	Target         string     `json:"target" doc:"What it calls: app:path, or a URL"`
	App            string     `json:"app,omitempty" doc:"App it calls"`
	Path           string     `json:"path,omitempty" doc:"Path on the app"`
	URL            string     `json:"url,omitempty" doc:"Address outside the box it calls instead of an app"`
	TimeoutSeconds int        `json:"timeoutSeconds,omitempty" doc:"Each call's timeout (0: the default, 60 s)"`
	Origin         string     `json:"origin" enum:"tiffin.config.ts,vercel.json" doc:"Where it is declared: tiffin.config.ts, or the app's vercel.json (it comes and goes with the app's live production deploy)"`
	Method         string     `json:"method" enum:"POST,GET" doc:"POST: a signed job delivery; GET: as Vercel calls cron paths (user-agent vercel-cron/1.0, Authorization: Bearer $CRON_SECRET when the app has CRON_SECRET)"`
	Paused         bool       `json:"paused" doc:"A paused cron does not tick until it is resumed (Run now still works)"`
	PausedAt       *time.Time `json:"pausedAt,omitempty"`
	PausedBy       string     `json:"pausedBy,omitempty"`
	NextAt         time.Time  `json:"nextAt" doc:"Next tick (UTC); while paused, the tick it would run next"`
	LastAt         *time.Time `json:"lastAt,omitempty"`
	LastJob        string     `json:"lastJob,omitempty" doc:"Job of the latest tick (see queue jobs get)"`
	LastState      string     `json:"lastState,omitempty"`
	LastSkippedAt  *time.Time `json:"lastSkippedAt,omitempty" doc:"Latest tick skipped because the previous run was still queued or running"`
	FailureRate    float64    `json:"failureRate" doc:"Share of the recent finished runs that gave up (dead), 0-1"`
	Recent         []CronRun  `json:"recent" doc:"The latest runs (up to 20), newest first"`
}

// Crons lists a project's crons with their recent runs.
func (e *Engine) Crons(ctx context.Context, project string) ([]CronInfo, error) {
	rows, err := e.pool.Query(ctx, `SELECT c.name, c.schedule, c.timezone, c.overlap, c.app, c.path, c.url, c.timeout_s, c.next_at, c.last_at,
		c.last_job, j.state, c.skipped_at, c.origin, c.method, c.paused, c.paused_at, c.paused_by
		FROM tq_crons c LEFT JOIN tq_jobs j ON j.id = c.last_job WHERE c.project = $1 ORDER BY c.name`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CronInfo{}
	for rows.Next() {
		var c CronInfo
		var last *int64
		var st *string
		if err := rows.Scan(&c.Name, &c.Schedule, &c.Timezone, &c.Overlap, &c.App, &c.Path, &c.URL, &c.TimeoutSeconds, &c.NextAt, &c.LastAt,
			&last, &st, &c.LastSkippedAt, &c.Origin, &c.Method, &c.Paused, &c.PausedAt, &c.PausedBy); err != nil {
			return nil, err
		}
		if c.Origin == "" {
			c.Origin = "tiffin.config.ts"
		}
		if c.Timezone == "" {
			c.Timezone = "UTC"
		}
		c.Target = c.App + ":" + c.Path
		if c.URL != "" {
			c.Target = c.URL
		}
		if last != nil {
			c.LastJob = jobID(*last)
		}
		c.LastState = deref(st)
		c.Recent = []CronRun{}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	recent, err := e.pool.Query(ctx, `SELECT c.name, r.id, r.state, r.enqueued_at, r.started_at, r.finished_at FROM tq_crons c
		CROSS JOIN LATERAL (SELECT id, state, enqueued_at, started_at, finished_at FROM tq_jobs j
			WHERE j.project = c.project AND j.cron = c.name ORDER BY id DESC LIMIT 20) r
		WHERE c.project = $1 ORDER BY c.name, r.id DESC`, project)
	if err != nil {
		return nil, err
	}
	defer recent.Close()
	at := map[string]*CronInfo{}
	for i := range out {
		at[out[i].Name] = &out[i]
	}
	for recent.Next() {
		var name string
		var id int64
		var r CronRun
		var started *time.Time
		if err := recent.Scan(&name, &id, &r.State, &r.At, &started, &r.FinishedAt); err != nil {
			return nil, err
		}
		r.Job = jobID(id)
		if started != nil && r.FinishedAt != nil {
			ms := int(r.FinishedAt.Sub(*started).Milliseconds())
			r.DurationMS = &ms
		}
		if c := at[name]; c != nil {
			c.Recent = append(c.Recent, r)
		}
	}
	for i := range out {
		var done, dead int
		for _, r := range out[i].Recent {
			switch r.State {
			case stateCompleted:
				done++
			case stateDead:
				dead++
			}
		}
		if done+dead > 0 {
			out[i].FailureRate = float64(dead) / float64(done+dead)
		}
	}
	return out, recent.Err()
}

func (e *Engine) loadCron(ctx context.Context, q querier, project, name, suffix string) (cronRow, error) {
	c := cronRow{project: project, name: name}
	err := q.QueryRow(ctx, `SELECT schedule, timezone, app, path, url, timeout_s FROM tq_crons WHERE project = $1 AND name = $2 `+suffix, project, name).
		Scan(&c.schedule, &c.tz, &c.app, &c.path, &c.url, &c.timeoutS)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, notFound(fmt.Sprintf("no cron %q in project %s", name, project))
	}
	return c, err
}

// TriggerCron runs a cron now, outside its schedule (a paused one too).
func (e *Engine) TriggerCron(ctx context.Context, project, name, by string) (string, error) {
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	c, err := e.loadCron(ctx, tx, project, name, "")
	if err != nil {
		return "", err
	}
	id, err := e.enqueueCron(ctx, tx, c, e.now(), by)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE tq_crons SET last_at = now(), last_job = $3 WHERE project = $1 AND name = $2`, project, name, id); err != nil {
		return "", err
	}
	return jobID(id), tx.Commit(ctx)
}

// SetCronPaused pauses a cron (no ticks until it is resumed; the pause
// outlasts applies and restarts) or resumes it from the next tick after now:
// ticks missed while paused do not run.
func (e *Engine) SetCronPaused(ctx context.Context, project, name string, paused bool, by string) (*CronInfo, error) {
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	c, err := e.loadCron(ctx, tx, project, name, "FOR UPDATE")
	if err != nil {
		return nil, err
	}
	if paused {
		_, err = tx.Exec(ctx, `UPDATE tq_crons SET paused = true, paused_at = coalesce(paused_at, now()),
			paused_by = CASE WHEN paused THEN paused_by ELSE $3 END WHERE project = $1 AND name = $2`, project, name, by)
	} else {
		sched, perr := parseSchedule(c.schedule, c.tz)
		if perr != nil {
			return nil, perr
		}
		_, err = tx.Exec(ctx, `UPDATE tq_crons SET paused = false, paused_at = NULL, paused_by = '',
			next_at = CASE WHEN paused THEN $3 ELSE next_at END WHERE project = $1 AND name = $2`, project, name, sched.Next(e.now()))
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	all, err := e.Crons(ctx, project)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Name == name {
			return &all[i], nil
		}
	}
	return nil, notFound(fmt.Sprintf("no cron %q in project %s", name, project))
}
