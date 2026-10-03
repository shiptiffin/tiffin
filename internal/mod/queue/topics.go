package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/btahir/tiffin/internal/manifest"
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
	sched, err := cronParser.Parse(c.Schedule)
	if err != nil {
		return fmt.Errorf("cron %s: schedule %q: %w", name, c.Schedule, err)
	}
	next := sched.Next(e.now().UTC())
	// A changed schedule restarts from now; an unchanged one keeps its next tick.
	_, err = e.pool.Exec(ctx, `INSERT INTO tq_crons (project, name, schedule, app, path, next_at) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (project, name) DO UPDATE SET app = $4, path = $5,
		next_at = CASE WHEN tq_crons.schedule = $3 THEN tq_crons.next_at ELSE $6 END, schedule = $3`,
		project, name, c.Schedule, c.App, c.Path, next)
	return err
}

// cronLoop fires due crons. Ticks missed while the box was down fire once
// when it comes back, then the schedule continues from now.
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
	rows, err := tx.Query(ctx, `SELECT project, name, schedule, app, path, next_at FROM tq_crons WHERE next_at <= now() FOR UPDATE SKIP LOCKED LIMIT 100`)
	if err != nil {
		return err
	}
	type due struct {
		project, name, schedule, app, path string
		at                                 time.Time
	}
	var ds []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.project, &d.name, &d.schedule, &d.app, &d.path, &d.at); err != nil {
			rows.Close()
			return err
		}
		ds = append(ds, d)
	}
	rows.Close()
	for _, d := range ds {
		sched, err := cronParser.Parse(d.schedule)
		if err != nil {
			continue
		}
		id, err := e.enqueueCron(ctx, tx, d.project, d.name, d.app, d.path, d.schedule, d.at, "cron")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE tq_crons SET next_at = $3, last_at = $4, last_job = $5 WHERE project = $1 AND name = $2`,
			d.project, d.name, sched.Next(e.now().UTC()), d.at, id); err != nil {
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
	Name      string     `json:"name"`
	Schedule  string     `json:"schedule" doc:"Cron expression, in UTC"`
	Target    string     `json:"target" doc:"app:path it calls"`
	NextAt    time.Time  `json:"nextAt"`
	LastAt    *time.Time `json:"lastAt,omitempty"`
	LastJob   string     `json:"lastJob,omitempty" doc:"Job of the latest tick (see queue jobs get)"`
	LastState string     `json:"lastState,omitempty"`
}

// Crons lists a project's crons.
func (e *Engine) Crons(ctx context.Context, project string) ([]CronInfo, error) {
	rows, err := e.pool.Query(ctx, `SELECT c.name, c.schedule, c.app, c.path, c.next_at, c.last_at, c.last_job, j.state
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
		if err := rows.Scan(&c.Name, &c.Schedule, &app, &path, &c.NextAt, &c.LastAt, &last, &st); err != nil {
			return nil, err
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
