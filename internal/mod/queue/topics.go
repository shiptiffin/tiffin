package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/jackc/pgx/v5"
	"github.com/robfig/cron/v3"
)

// Subscription is one subscriber of a topic: every message published to the
// topic becomes its own job for each subscriber, retried independently.
type Subscription struct {
	Name      string    `json:"name" doc:"Subscription name, unique within the topic"`
	App       string    `json:"app,omitempty" doc:"App that receives the messages"`
	Path      string    `json:"path" doc:"Path messages are POSTed to"`
	URL       string    `json:"url,omitempty" doc:"Explicit loopback URL instead of an app (development)"`
	CreatedAt time.Time `json:"createdAt"`
}

// Topic is a fan-out name with its subscribers.
type Topic struct {
	Name          string         `json:"name"`
	Subscriptions []Subscription `json:"subscriptions"`
}

// Subscribe adds (or updates) a subscriber, creating the topic.
func (e *Engine) Subscribe(ctx context.Context, project, topic string, s Subscription) (*Topic, error) {
	if !nameRE.MatchString(topic) || !nameRE.MatchString(s.Name) {
		return nil, invalid("topic and subscription names are 1-64 lowercase letters, digits, dots, dashes or underscores", "")
	}
	if s.App == "" && s.URL == "" {
		return nil, invalid("a subscription needs an app (or a loopback url)", "")
	}
	if s.Path == "" {
		s.Path = "/topics/" + topic
	}
	if err := validTarget(s.App, s.Path, s.URL); err != nil {
		return nil, err
	}
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO tq_topics (project, name) VALUES ($1, $2) ON CONFLICT DO NOTHING`, project, topic); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO tq_subscriptions (project, topic, name, app, path, url) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (project, topic, name) DO UPDATE SET app = $4, path = $5, url = $6`, project, topic, s.Name, s.App, s.Path, s.URL); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return e.topic(ctx, project, topic)
}

// Unsubscribe removes a subscriber. The topic stays (publishing to it with
// no subscribers enqueues nothing) so the name never silently turns back
// into a plain queue.
func (e *Engine) Unsubscribe(ctx context.Context, project, topic, name string) error {
	tag, err := e.pool.Exec(ctx, `DELETE FROM tq_subscriptions WHERE project = $1 AND topic = $2 AND name = $3`, project, topic, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return notFound(fmt.Sprintf("topic %q has no subscription %q", topic, name))
	}
	return nil
}

func (e *Engine) topic(ctx context.Context, project, name string) (*Topic, error) {
	ts, err := e.Topics(ctx, project)
	if err != nil {
		return nil, err
	}
	for _, t := range ts {
		if t.Name == name {
			return &t, nil
		}
	}
	return nil, notFound("no topic " + strconv.Quote(name))
}

// Topics lists a project's topics and subscribers.
func (e *Engine) Topics(ctx context.Context, project string) ([]Topic, error) {
	rows, err := e.pool.Query(ctx, `SELECT t.name, s.name, s.app, s.path, s.url, s.created_at FROM tq_topics t
		LEFT JOIN tq_subscriptions s ON s.project = t.project AND s.topic = t.name WHERE t.project = $1 ORDER BY t.name, s.name`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Topic{}
	for rows.Next() {
		var t string
		var name, app, path, url *string
		var at *time.Time
		if err := rows.Scan(&t, &name, &app, &path, &url, &at); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].Name != t {
			out = append(out, Topic{Name: t, Subscriptions: []Subscription{}})
		}
		if name != nil {
			cur := &out[len(out)-1]
			cur.Subscriptions = append(cur.Subscriptions, Subscription{Name: *name, App: *app, Path: *path, URL: *url, CreatedAt: *at})
		}
	}
	return out, rows.Err()
}

// ---- cron ----

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
	return &schedule{spec, loc}, nil
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

// ReconcileCron makes the stored cron match the manifest (spec nil = deleted).
func (e *Engine) ReconcileCron(ctx context.Context, project, name string, spec json.RawMessage) error {
	if spec == nil {
		_, err := e.pool.Exec(ctx, `DELETE FROM tq_crons WHERE project = $1 AND name = $2`, project, name)
		return err
	}
	var c manifest.Cron
	if err := json.Unmarshal(spec, &c); err != nil {
		return err
	}
	if c.Path == "" {
		c.Path = "/cron/" + name
	}
	sched, err := parseSchedule(c.Schedule, c.Timezone)
	if err != nil {
		return fmt.Errorf("cron %s: schedule %q: %w", name, c.Schedule, err)
	}
	next := sched.Next(e.now())
	// A changed schedule or time zone restarts from now; an unchanged one keeps its next tick.
	_, err = e.pool.Exec(ctx, `INSERT INTO tq_crons (project, name, schedule, timezone, overlap, app, path, next_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (project, name) DO UPDATE SET app = $6, path = $7, overlap = $5,
		next_at = CASE WHEN tq_crons.schedule = $3 AND tq_crons.timezone = $4 THEN tq_crons.next_at ELSE $8 END, schedule = $3, timezone = $4`,
		project, name, c.Schedule, c.Timezone, c.Overlap, c.App, c.Path, next)
	return err
}

// cronLoop fires due crons. Ticks missed while the box was down fire once
// when it comes back, then the schedule continues from now. A tick whose
// cron's previous run is still queued or running is skipped (unless the
// cron allows overlap). A stopped project's crons wait (see stop.go).
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
	rows, err := tx.Query(ctx, `SELECT c.project, c.name, c.schedule, c.timezone, c.app, c.path, c.next_at,
		NOT c.overlap AND coalesce(j.state IN ('scheduled', 'queued', 'running', 'retrying'), false)
		FROM tq_crons c LEFT JOIN tq_jobs j ON j.id = c.last_job WHERE c.next_at <= now()
		AND c.project NOT IN (SELECT project FROM tq_stopped) FOR UPDATE OF c SKIP LOCKED LIMIT 100`)
	if err != nil {
		return err
	}
	type due struct {
		project, name, schedule, tz, app, path string
		at                                     time.Time
		busy                                   bool // the previous run is still going
	}
	var ds []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.project, &d.name, &d.schedule, &d.tz, &d.app, &d.path, &d.at, &d.busy); err != nil {
			rows.Close()
			return err
		}
		ds = append(ds, d)
	}
	rows.Close()
	for _, d := range ds {
		sched, err := parseSchedule(d.schedule, d.tz)
		if err != nil {
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
		id, err := e.enqueueCron(ctx, tx, d.project, d.name, d.app, d.path, d.schedule, d.at, "cron")
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

func (e *Engine) enqueueCron(ctx context.Context, tx pgx.Tx, project, name, app, path, schedule string, at time.Time, by string) (int64, error) {
	cfg, err := e.queueConfig(ctx, tx, project, queueCron)
	if err != nil {
		return 0, err
	}
	payload, _ := json.Marshal(map[string]any{"cron": name, "schedule": schedule, "scheduledAt": at.UTC()})
	return e.insertJob(ctx, tx, newJob{project: project, queue: queueCron, kind: kindCron, cron: name, app: app, path: path,
		payload: payload, priority: prioNormal, maxAttempts: cfg.MaxAttempts, leaseS: cfg.LeaseS, runAt: e.now(), by: by})
}

// CronInfo is a cron with its next and last run.
type CronInfo struct {
	Name          string     `json:"name"`
	Schedule      string     `json:"schedule" doc:"Cron expression, read in timezone"`
	Timezone      string     `json:"timezone" doc:"IANA time zone of the schedule"`
	Overlap       bool       `json:"overlap,omitempty" doc:"Ticks run even while the previous run is still going"`
	Target        string     `json:"target" doc:"app:path it calls"`
	NextAt        time.Time  `json:"nextAt"`
	LastAt        *time.Time `json:"lastAt,omitempty"`
	LastJob       string     `json:"lastJob,omitempty" doc:"Job of the latest tick (see queue jobs get)"`
	LastState     string     `json:"lastState,omitempty"`
	LastSkippedAt *time.Time `json:"lastSkippedAt,omitempty" doc:"Latest tick skipped because the previous run was still queued or running"`
}

// Crons lists a project's crons.
func (e *Engine) Crons(ctx context.Context, project string) ([]CronInfo, error) {
	rows, err := e.pool.Query(ctx, `SELECT c.name, c.schedule, c.timezone, c.overlap, c.app, c.path, c.next_at, c.last_at, c.last_job, j.state, c.skipped_at
		FROM tq_crons c LEFT JOIN tq_jobs j ON j.id = c.last_job WHERE c.project = $1 ORDER BY c.name`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CronInfo{}
	for rows.Next() {
		var c CronInfo
		var app, path string
		var last *int64
		var st *string
		if err := rows.Scan(&c.Name, &c.Schedule, &c.Timezone, &c.Overlap, &app, &path, &c.NextAt, &c.LastAt, &last, &st, &c.LastSkippedAt); err != nil {
			return nil, err
		}
		if c.Timezone == "" {
			c.Timezone = "UTC"
		}
		c.Target = app + ":" + path
		if last != nil {
			c.LastJob = jobID(*last)
		}
		c.LastState = deref(st)
		out = append(out, c)
	}
	return out, rows.Err()
}

// TriggerCron runs a cron now, outside its schedule.
func (e *Engine) TriggerCron(ctx context.Context, project, name, by string) (string, error) {
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var schedule, app, path string
	err = tx.QueryRow(ctx, `SELECT schedule, app, path FROM tq_crons WHERE project = $1 AND name = $2`, project, name).Scan(&schedule, &app, &path)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", notFound(fmt.Sprintf("no cron %q in project %s", name, project))
	}
	if err != nil {
		return "", err
	}
	id, err := e.enqueueCron(ctx, tx, project, name, app, path, schedule, e.now(), by)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE tq_crons SET last_at = now(), last_job = $3 WHERE project = $1 AND name = $2`, project, name, id); err != nil {
		return "", err
	}
	return jobID(id), tx.Commit(ctx)
}

// PublishEvent sends a box event (such as storage.object.created) to the
// project's topic of that name. A project that has not declared the topic
// gets nothing: sent reports whether it was published.
func (m *Module) PublishEvent(ctx context.Context, _ *platform.Platform, project, topic string, payload json.RawMessage, dedupe string) (sent bool, err error) {
	e := m.engine()
	if e == nil {
		return false, errors.New("the queue is not running yet")
	}
	if err := e.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tq_topics WHERE project = $1 AND name = $2)`, project, topic).Scan(&sent); err != nil || !sent {
		return false, err
	}
	_, err = e.Send(ctx, project, SendRequest{Name: topic, Payload: payload, Dedupe: dedupe, By: "box"})
	return err == nil, err
}
