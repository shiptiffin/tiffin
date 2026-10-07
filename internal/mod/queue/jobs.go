package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Job states.
const (
	stateScheduled = "scheduled" // waiting for its run time (delay)
	stateQueued    = "queued"    // due; waiting for a worker or a limit
	stateRunning   = "running"   // pushed to the app, lease held
	stateRetrying  = "retrying"  // failed; next attempt scheduled
	stateCompleted = "completed"
	stateDead      = "dead" // dead-letter queue: retries exhausted or non-retryable
	stateCancelled = "cancelled"
)

// Job kinds.
const (
	kindJob      = "job"
	kindCron     = "cron"
	kindWorkflow = "workflow"
)

// System queues.
const (
	queueWorkflows = "_workflows"
	queueCron      = "_cron"
)

const (
	defaultMaxAttempts = 10
	defaultLeaseS      = 60
	maxPayload         = 1 << 20
)

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Error is a domain error the API maps to a problem with a stable code.
type Error struct {
	Status int
	Code   string
	Msg    string
	Hint   string
}

func (e *Error) Error() string { return e.Msg }

func invalid(msg, hint string) error {
	return &Error{Status: 422, Code: "validation", Msg: msg, Hint: hint}
}
func notFound(msg string) error { return &Error{Status: 404, Code: "not_found", Msg: msg} }
func conflict(msg, hint string) error {
	return &Error{Status: 409, Code: "conflict", Msg: msg, Hint: hint}
}

type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type jobRow struct {
	ID            int64           `db:"id"`
	Project       string          `db:"project"`
	Queue         string          `db:"queue"`
	Kind          string          `db:"kind"`
	Topic         *string         `db:"topic"`
	Subscription  *string         `db:"subscription"`
	Cron          *string         `db:"cron"`
	RunID         *string         `db:"run_id"`
	App           string          `db:"app"`
	Path          string          `db:"path"`
	URL           string          `db:"url"`
	Release       string          `db:"release"`
	Payload       json.RawMessage `db:"payload"`
	Key           *string         `db:"key"`
	GroupKey      *string         `db:"group_key"`
	Dedupe        *string         `db:"dedupe"`
	Priority      int             `db:"priority"`
	State         string          `db:"state"`
	Attempt       int             `db:"attempt"`
	TotalAttempts int             `db:"total_attempts"`
	MaxAttempts   int             `db:"max_attempts"`
	LeaseS        int             `db:"lease_s"`
	Seq           int             `db:"seq"`
	RiverID       *int64          `db:"river_id"`
	BlockedOn     *string         `db:"blocked_on"`
	RunAt         time.Time       `db:"run_at"`
	EnqueuedAt    time.Time       `db:"enqueued_at"`
	StartedAt     *time.Time      `db:"started_at"`
	LeaseUntil    *time.Time      `db:"lease_until"`
	FinishedAt    *time.Time      `db:"finished_at"`
	LastError     *string         `db:"last_error"`
	LastStatus    *int            `db:"last_status"`
	Output        json.RawMessage `db:"output"`
	EnqueuedBy    string          `db:"enqueued_by"`
	Progress      json.RawMessage `db:"progress"`
}

const jobCols = `id, project, queue, kind, topic, subscription, cron, run_id, app, path, url, release, payload, key, group_key, dedupe,
	priority, state, attempt, total_attempts, max_attempts, lease_s, seq, river_id, blocked_on, run_at, enqueued_at, started_at,
	lease_until, finished_at, last_error, last_status, output, enqueued_by, progress`

func (e *Engine) loadJob(ctx context.Context, q querier, id int64, suffix ...string) (*jobRow, error) {
	rows, err := q.Query(ctx, `SELECT `+jobCols+` FROM tq_jobs WHERE id = $1 `+strings.Join(suffix, " "), id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByName[jobRow])
}

func jobID(id int64) string { return "job_" + strconv.FormatInt(id, 10) }

// ParseJobID accepts "job_42" or "42".
func ParseJobID(s string) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimPrefix(s, "job_"), 10, 64)
	if err != nil || n <= 0 {
		return 0, invalid("job IDs look like job_42", "list jobs with `tiffin queue jobs list <project>`")
	}
	return n, nil
}

func prioName(p int) string {
	switch p {
	case prioHigh:
		return "high"
	case prioLow:
		return "low"
	default:
		return "normal"
	}
}

func prioValue(s string) (int, error) {
	switch s {
	case "", "normal":
		return prioNormal, nil
	case "high":
		return prioHigh, nil
	case "low":
		return prioLow, nil
	}
	return 0, invalid("priority must be high, normal or low", "")
}

// QueueConfig is a queue's delivery settings. Zero limits mean "no limit".
type QueueConfig struct {
	Name           string `json:"name" doc:"Queue (or topic) name"`
	App            string `json:"app,omitempty" doc:"App that receives the jobs (default: the app that sends them)"`
	Path           string `json:"path,omitempty" doc:"Path jobs are POSTed to on the app (default /queues/<name>)"`
	URL            string `json:"url,omitempty" doc:"Address outside the box jobs are POSTed to instead of an app"`
	Concurrency    int    `json:"concurrency" doc:"Most jobs of this queue running at once (0 = no limit)"`
	KeyConcurrency int    `json:"keyConcurrency" doc:"Most jobs running at once per key (the send option key; 0 = no limit)"`
	RateLimit      int    `json:"rateLimit" doc:"Most jobs started per rate period, per key (0 = no limit)"`
	RatePeriodS    int    `json:"ratePeriodSeconds" doc:"Rate limit window in seconds"`
	MaxAttempts    int    `json:"maxAttempts" doc:"Attempts before a job goes to the dead-letter queue"`
	LeaseS         int    `json:"leaseSeconds" doc:"How long an attempt may run without a response or heartbeat"`
	Paused         bool   `json:"paused" doc:"Paused queues accept jobs but deliver none"`
	Configured     bool   `json:"configured" doc:"False when the queue only uses defaults"`
}

func defaultConfig(project, name string) QueueConfig {
	c := QueueConfig{Name: name, MaxAttempts: defaultMaxAttempts, LeaseS: defaultLeaseS}
	if name == queueWorkflows {
		c.MaxAttempts = 20
	}
	return c
}

func (e *Engine) queueConfig(ctx context.Context, q querier, project, name string) (QueueConfig, error) {
	c := defaultConfig(project, name)
	var maxA, lease int
	err := q.QueryRow(ctx, `SELECT app, path, url, concurrency, key_concurrency, rate_limit, rate_period_s, max_attempts, lease_s, paused
		FROM tq_queues WHERE project = $1 AND name = $2`, project, name).Scan(&c.App, &c.Path, &c.URL, &c.Concurrency, &c.KeyConcurrency,
		&c.RateLimit, &c.RatePeriodS, &maxA, &lease, &c.Paused)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	c.Configured = true
	if maxA > 0 {
		c.MaxAttempts = maxA
	}
	if lease > 0 {
		c.LeaseS = lease
	}
	return c, nil
}

// ConfigureQueue stores a queue's settings and wakes jobs a looser limit lets run.
func (e *Engine) ConfigureQueue(ctx context.Context, project string, c QueueConfig) (QueueConfig, error) {
	if !nameRE.MatchString(c.Name) && c.Name != queueWorkflows && c.Name != queueCron {
		return c, invalid("queue names are 1-64 lowercase letters, digits, dots, dashes or underscores", "")
	}
	if err := e.validTarget(c.App, c.Path, c.URL); err != nil {
		return c, err
	}
	switch {
	case c.Concurrency < 0, c.KeyConcurrency < 0, c.RateLimit < 0, c.RatePeriodS < 0:
		return c, invalid("limits cannot be negative", "use 0 for no limit")
	case c.RateLimit > 10000:
		return c, invalid("rateLimit is at most 10000 per period", "use a longer period for bigger numbers")
	case c.RateLimit > 0 && c.RatePeriodS == 0:
		return c, invalid("a rate limit needs ratePeriodSeconds", "e.g. rateLimit 10 with ratePeriodSeconds 60 is 10 jobs a minute")
	case c.MaxAttempts < 0 || c.MaxAttempts > 100:
		return c, invalid("maxAttempts must be 1-100", "")
	case c.LeaseS < 0 || c.LeaseS > 3600:
		return c, invalid("leaseSeconds must be 1-3600", "long jobs should heartbeat to extend their lease")
	}
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return c, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO tq_queues (project, name, app, path, url, concurrency, key_concurrency, rate_limit, rate_period_s, max_attempts, lease_s, paused, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, now())
		ON CONFLICT (project, name) DO UPDATE SET app = $3, path = $4, url = $5, concurrency = $6, key_concurrency = $7, rate_limit = $8,
		rate_period_s = $9, max_attempts = $10, lease_s = $11, paused = $12, updated_at = now()`,
		project, c.Name, c.App, c.Path, c.URL, c.Concurrency, c.KeyConcurrency, c.RateLimit, c.RatePeriodS, c.MaxAttempts, c.LeaseS, c.Paused); err != nil {
		return c, err
	}
	// Limits may have loosened: let every parked job of this queue try again.
	if err := e.wakeQueue(ctx, tx, project, c.Name); err != nil {
		return c, err
	}
	if err := tx.Commit(ctx); err != nil {
		return c, err
	}
	return e.queueConfig(ctx, e.pool, project, c.Name)
}

func (e *Engine) wakeQueue(ctx context.Context, tx pgx.Tx, project, queue string) error {
	rows, err := tx.Query(ctx, `SELECT DISTINCT blocked_on FROM tq_jobs WHERE project = $1 AND queue = $2 AND blocked_on IS NOT NULL`, project, queue)
	if err != nil {
		return err
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, k := range keys {
		if err := e.wake(ctx, tx, k, 0); err != nil {
			return err
		}
	}
	return nil
}

// SetPaused pauses or resumes a queue.
func (e *Engine) SetPaused(ctx context.Context, project, name string, paused bool) (QueueConfig, error) {
	c, err := e.queueConfig(ctx, e.pool, project, name)
	if err != nil {
		return c, err
	}
	c.Paused = paused
	if !c.Configured {
		c.MaxAttempts, c.LeaseS = 0, 0
	}
	return e.ConfigureQueue(ctx, project, c)
}

var appRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

// validTarget checks an app and path, or a URL outside the box.
func (e *Engine) validTarget(app, path, rawURL string) error {
	if app != "" && !appRE.MatchString(app) {
		return invalid("app must be an app name from the manifest", "")
	}
	if path != "" && !strings.HasPrefix(path, "/") {
		return invalid("path must start with /", "")
	}
	if rawURL != "" {
		if app != "" {
			return invalid("set an app or a url, not both", "")
		}
		if len(rawURL) > 2048 {
			return invalid("the url is longer than 2048 characters", "")
		}
		return e.guard.checkURL(rawURL)
	}
	return nil
}

// SendRequest enqueues a job on a queue, or one job per subscriber of a topic.
type SendRequest struct {
	Name        string
	Payload     json.RawMessage
	Delay       time.Duration
	RunAt       time.Time
	Key         string
	GroupKey    string
	Dedupe      string
	Priority    string
	App, Path   string
	MaxAttempts int
	By          string
	FromApp     string // the sending app: the default target
}

// SendResult says what was enqueued.
type SendResult struct {
	Jobs         []string  `json:"jobs" doc:"IDs of the jobs created (one per subscriber for a topic)"`
	Topic        bool      `json:"topic" doc:"True when the name is a topic and the job fanned out to its subscribers"`
	Deduplicated bool      `json:"deduplicated" doc:"True when the dedupe key was already used in the last 24 hours; jobs are the earlier ones"`
	RunAt        time.Time `json:"runAt" doc:"When the job becomes due"`
	Message      string    `json:"message" doc:"What happened, in plain words"`
}

type target struct {
	sub, app, path, url string
}

// Send enqueues (inside tx when given, so callers can enqueue atomically).
func (e *Engine) Send(ctx context.Context, project string, r SendRequest) (*SendResult, error) {
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	res, err := e.sendTx(ctx, tx, project, r)
	if err != nil {
		return nil, err
	}
	return res, tx.Commit(ctx)
}

func (e *Engine) sendTx(ctx context.Context, tx pgx.Tx, project string, r SendRequest) (*SendResult, error) {
	if !nameRE.MatchString(r.Name) {
		return nil, invalid(fmt.Sprintf("%q is not a valid queue or topic name: use 1-64 lowercase letters, digits, dots, dashes or underscores", r.Name), "")
	}
	if len(r.Payload) == 0 {
		r.Payload = json.RawMessage("null")
	}
	if !json.Valid(r.Payload) {
		return nil, invalid("payload must be JSON", "")
	}
	if len(r.Payload) > maxPayload {
		return nil, invalid(fmt.Sprintf("payload is %d bytes; the limit is 1 MB", len(r.Payload)), "put large data in storage and send its key")
	}
	for name, v := range map[string]string{"key": r.Key, "groupKey": r.GroupKey, "dedupe": r.Dedupe} {
		if len(v) > 200 {
			return nil, invalid(name+" is longer than 200 characters", "")
		}
	}
	prio, err := prioValue(r.Priority)
	if err != nil {
		return nil, err
	}
	if r.Delay < 0 || r.Delay > 366*24*time.Hour {
		return nil, invalid("delay must be between 0 and 366 days", "")
	}
	if r.MaxAttempts < 0 || r.MaxAttempts > 100 {
		return nil, invalid("maxAttempts must be 1-100", "")
	}
	if err := e.validTarget(r.App, r.Path, ""); err != nil {
		return nil, err
	}
	runAt := e.now()
	if !r.RunAt.IsZero() {
		runAt = r.RunAt
	}
	runAt = runAt.Add(r.Delay)
	res := &SendResult{Jobs: []string{}, RunAt: runAt}
	if r.Dedupe != "" {
		if err := lockKey(ctx, tx, "d:"+project+"/"+r.Dedupe); err != nil {
			return nil, err
		}
		var ids []int64
		err := tx.QueryRow(ctx, `SELECT job_ids FROM tq_dedupe WHERE project = $1 AND key = $2 AND expires_at > now()`, project, r.Dedupe).Scan(&ids)
		if err == nil {
			for _, id := range ids {
				res.Jobs = append(res.Jobs, jobID(id))
			}
			res.Deduplicated = true
			res.Message = "dedupe key " + strconv.Quote(r.Dedupe) + " was already used in the last 24 hours; nothing new was enqueued"
			return res, nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	var targets []target
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tq_topics WHERE project = $1 AND name = $2)`, project, r.Name).Scan(&res.Topic); err != nil {
		return nil, err
	}
	cfg, err := e.queueConfig(ctx, tx, project, r.Name)
	if err != nil {
		return nil, err
	}
	if res.Topic {
		rows, err := tx.Query(ctx, `SELECT name, app, path, url FROM tq_subscriptions WHERE project = $1 AND topic = $2 ORDER BY name`, project, r.Name)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var t target
			if err := rows.Scan(&t.sub, &t.app, &t.path, &t.url); err != nil {
				rows.Close()
				return nil, err
			}
			targets = append(targets, t)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	} else {
		t := target{app: cfg.App, path: cfg.Path, url: cfg.URL}
		if r.App != "" {
			t.app, t.url = r.App, ""
		}
		if r.Path != "" {
			t.path = r.Path
		}
		if t.app == "" && t.url == "" {
			t.app = r.FromApp
		}
		if t.app == "" && t.url == "" {
			return nil, invalid(fmt.Sprintf("queue %q has no target app", r.Name),
				"pass app (the app whose route handles the job), or set one with `tiffin queue configure <project> "+r.Name+" --app <app>`")
		}
		if t.path == "" {
			t.path = "/queues/" + r.Name
		}
		targets = append(targets, t)
	}
	maxA := cfg.MaxAttempts
	if r.MaxAttempts > 0 {
		maxA = r.MaxAttempts
	}
	var ids []int64
	for _, t := range targets {
		nj := newJob{project: project, queue: r.Name, kind: kindJob, app: t.app, path: t.path, url: t.url,
			payload: r.Payload, key: r.Key, groupKey: r.GroupKey, dedupe: r.Dedupe, priority: prio,
			maxAttempts: maxA, leaseS: cfg.LeaseS, runAt: runAt, by: r.By}
		if res.Topic {
			nj.topic, nj.subscription = r.Name, t.sub
			if nj.path == "" {
				nj.path = "/topics/" + r.Name
			}
			if nj.groupKey != "" {
				nj.groupKey = t.sub + ":" + nj.groupKey // each subscriber keeps its own order
			}
		}
		id, err := e.insertJob(ctx, tx, nj)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
		res.Jobs = append(res.Jobs, jobID(id))
	}
	if r.Dedupe != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO tq_dedupe (project, key, job_ids, expires_at) VALUES ($1, $2, $3, now() + interval '24 hours')
			ON CONFLICT (project, key) DO UPDATE SET job_ids = excluded.job_ids, expires_at = excluded.expires_at`, project, r.Dedupe, ids); err != nil {
			return nil, err
		}
	}
	when := "now"
	if runAt.After(e.now().Add(time.Second)) {
		when = "at " + runAt.UTC().Format(time.RFC3339)
	}
	switch {
	case res.Topic && len(ids) == 0:
		res.Message = fmt.Sprintf("topic %q has no subscribers; nothing was enqueued", r.Name)
	case res.Topic:
		res.Message = fmt.Sprintf("published to topic %q: %d subscriber job(s), due %s", r.Name, len(ids), when)
	default:
		res.Message = fmt.Sprintf("enqueued %s on queue %q, due %s", res.Jobs[0], r.Name, when)
	}
	return res, nil
}

type newJob struct {
	project, queue, kind             string
	topic, subscription, cron, runID string
	app, path, url, release          string
	payload                          json.RawMessage
	key, groupKey, dedupe            string
	priority, maxAttempts, leaseS    int
	runAt                            time.Time
	by                               string
}

func (e *Engine) insertJob(ctx context.Context, tx pgx.Tx, nj newJob) (int64, error) {
	state := stateQueued
	if nj.runAt.After(e.now().Add(500 * time.Millisecond)) {
		state = stateScheduled
	}
	if len(nj.payload) == 0 {
		nj.payload = json.RawMessage("null")
	}
	var id int64
	err := tx.QueryRow(ctx, `INSERT INTO tq_jobs (project, queue, kind, topic, subscription, cron, run_id, app, path, url, release,
		payload, key, group_key, dedupe, priority, state, max_attempts, lease_s, seq, run_at, enqueued_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, 1, $20, $21) RETURNING id`,
		nj.project, nj.queue, nj.kind, nullStr(nj.topic), nullStr(nj.subscription), nullStr(nj.cron), nullStr(nj.runID),
		nj.app, nj.path, nj.url, nj.release, []byte(nj.payload), nullStr(nj.key), nullStr(nj.groupKey), nullStr(nj.dedupe),
		nj.priority, state, nj.maxAttempts, nj.leaseS, nj.runAt, nj.by).Scan(&id)
	if err != nil {
		return 0, err
	}
	rid, err := e.insertRiver(ctx, tx, id, 1, nj.priority, nj.runAt)
	if err != nil {
		return 0, err
	}
	_, err = tx.Exec(ctx, `UPDATE tq_jobs SET river_id = $2 WHERE id = $1`, id, rid)
	return id, err
}

// Job is a job as people and agents see it.
type Job struct {
	ID           string          `json:"id" doc:"Job ID, e.g. job_42"`
	Queue        string          `json:"queue" doc:"Queue or topic name (_workflows and _cron are the box's own)"`
	Kind         string          `json:"kind" enum:"job,cron,workflow" doc:"job, a cron tick, or a workflow turn"`
	State        string          `json:"state" enum:"scheduled,queued,running,retrying,completed,dead,cancelled"`
	Target       string          `json:"target" doc:"Where it is pushed: app:path, or a URL"`
	Topic        string          `json:"topic,omitempty"`
	Subscription string          `json:"subscription,omitempty"`
	Cron         string          `json:"cron,omitempty"`
	RunID        string          `json:"runId,omitempty" doc:"Workflow run this turn belongs to"`
	Release      string          `json:"release,omitempty" doc:"Pinned app release (workflow turns)"`
	Priority     string          `json:"priority" enum:"high,normal,low"`
	Key          string          `json:"key,omitempty" doc:"Limit key (per-key concurrency and rate limits)"`
	GroupKey     string          `json:"groupKey,omitempty" doc:"FIFO group: jobs with the same group run one at a time, in order"`
	Dedupe       string          `json:"dedupe,omitempty"`
	Attempt      int             `json:"attempt" doc:"Attempts so far (reset by a replay)"`
	MaxAttempts  int             `json:"maxAttempts"`
	WaitingFor   string          `json:"waitingFor,omitempty" doc:"Why a queued job is not running yet"`
	RunAt        time.Time       `json:"runAt" doc:"When it is (or was) due"`
	EnqueuedAt   time.Time       `json:"enqueuedAt"`
	StartedAt    *time.Time      `json:"startedAt,omitempty" doc:"Start of the latest attempt"`
	LeaseUntil   *time.Time      `json:"leaseUntil,omitempty" doc:"Running jobs: when the attempt fails unless the app answers or heartbeats"`
	FinishedAt   *time.Time      `json:"finishedAt,omitempty"`
	LastError    string          `json:"lastError,omitempty"`
	LastStatus   int             `json:"lastStatus,omitempty" doc:"HTTP status of the latest attempt"`
	EnqueuedBy   string          `json:"enqueuedBy,omitempty"`
	Progress     json.RawMessage `json:"progress,omitempty" doc:"The latest progress the app reported (job.progress in @shiptiffin/sdk)"`
	Payload      json.RawMessage `json:"payload,omitempty" doc:"Job payload (get only)"`
	Output       json.RawMessage `json:"output,omitempty" doc:"The app's JSON response to the successful attempt (get only)"`
	Attempts     []Attempt       `json:"attempts,omitempty" doc:"Every attempt, oldest first (get only)"`
}

// Attempt is one delivery.
type Attempt struct {
	Attempt    int       `json:"attempt"`
	StartedAt  time.Time `json:"startedAt"`
	DurationMS int       `json:"durationMs"`
	Outcome    string    `json:"outcome" enum:"ok,retry,dead,interrupted,cancelled" doc:"ok, retry (failed, retried later), dead, interrupted (box shut down or project stopped; not counted) or cancelled"`
	Status     int       `json:"status,omitempty" doc:"HTTP status, 0 when there was no response"`
	Error      string    `json:"error,omitempty"`
	Release    string    `json:"release,omitempty"`
}

func waitingFor(key string) string {
	kind, rest, _ := strings.Cut(key, ":")
	_, rest, _ = strings.Cut(rest, "/")
	switch kind {
	case "s":
		return "the project is stopped"
	case "a":
		return "the project's limit on deliveries at once"
	case "p":
		return "the queue is paused"
	case "q":
		return "the queue's concurrency limit"
	case "k":
		return "the concurrency limit for its key"
	case "g":
		return "earlier jobs in FIFO group " + strconv.Quote(rest)
	case "r":
		return "the rate limit"
	case "x":
		return "the project's limit on calls outside the box"
	}
	return key
}

func (j *jobRow) public(full bool) Job {
	out := Job{ID: jobID(j.ID), Queue: j.Queue, Kind: j.Kind, State: j.State, Topic: deref(j.Topic), Subscription: deref(j.Subscription),
		Cron: deref(j.Cron), RunID: deref(j.RunID), Release: j.Release, Priority: prioName(j.Priority), Key: deref(j.Key),
		GroupKey: deref(j.GroupKey), Dedupe: deref(j.Dedupe), Attempt: j.Attempt, MaxAttempts: j.MaxAttempts, RunAt: j.RunAt,
		EnqueuedAt: j.EnqueuedAt, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt, LastError: deref(j.LastError), EnqueuedBy: j.EnqueuedBy,
		Progress: j.Progress}
	if j.State == stateRunning {
		out.LeaseUntil = j.LeaseUntil
	}
	if j.LastStatus != nil {
		out.LastStatus = *j.LastStatus
	}
	if j.BlockedOn != nil {
		out.WaitingFor = waitingFor(*j.BlockedOn)
	}
	if j.URL != "" {
		out.Target = j.URL
	} else {
		out.Target = j.App + ":" + j.Path
	}
	if full {
		out.Payload, out.Output = j.Payload, j.Output
	}
	return out
}

// ListFilter narrows a job listing.
type ListFilter struct {
	Queue  string
	State  string
	RunID  string
	Before int64
	Limit  int
}

// jobListCols are jobCols without the payload and output a listing drops
// (up to 1 MB and 64 KB a job).
var jobListCols = strings.NewReplacer(" payload,", " NULL::jsonb AS payload,", " output,", " NULL::jsonb AS output,").Replace(jobCols)

// ListJobs lists a project's jobs, newest first.
func (e *Engine) ListJobs(ctx context.Context, project string, f ListFilter) ([]Job, error) {
	q := `SELECT ` + jobListCols + ` FROM tq_jobs WHERE project = $1`
	args := []any{project}
	add := func(cond string, v any) {
		args = append(args, v)
		q += fmt.Sprintf(" AND "+cond, len(args))
	}
	if f.Queue != "" {
		add("queue = $%d", f.Queue)
	}
	if f.State != "" {
		add("state = $%d", f.State)
	}
	if f.RunID != "" {
		add("run_id = $%d", f.RunID)
	}
	if f.Before > 0 {
		add("id < $%d", f.Before)
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}
	args = append(args, f.Limit)
	q += fmt.Sprintf(" ORDER BY id DESC LIMIT $%d", len(args))
	rows, err := e.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	js, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByName[jobRow])
	if err != nil {
		return nil, err
	}
	out := make([]Job, 0, len(js))
	for _, j := range js {
		out = append(out, j.public(false))
	}
	return out, nil
}

// GetJob returns one job with its payload, output and attempts.
func (e *Engine) GetJob(ctx context.Context, project string, id int64) (*Job, error) {
	j, err := e.loadJob(ctx, e.pool, id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && j.Project != project) {
		return nil, notFound(fmt.Sprintf("no job %s in project %s", jobID(id), project))
	}
	if err != nil {
		return nil, err
	}
	out := j.public(true)
	rows, err := e.pool.Query(ctx, `SELECT attempt, started_at, duration_ms, outcome, coalesce(status, 0), coalesce(error, ''), release
		FROM tq_attempts WHERE job_id = $1 ORDER BY attempt`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a Attempt
		if err := rows.Scan(&a.Attempt, &a.StartedAt, &a.DurationMS, &a.Outcome, &a.Status, &a.Error, &a.Release); err != nil {
			return nil, err
		}
		out.Attempts = append(out.Attempts, a)
	}
	return &out, rows.Err()
}

// CancelJob stops a job: pending jobs never run, a running attempt is cut off.
// A dead job is discarded: it leaves the dead-letter queue as cancelled, with
// its attempts kept, and can still be replayed with RetryJob.
func (e *Engine) CancelJob(ctx context.Context, project string, id int64) (*Job, error) {
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	j, err := e.loadJob(ctx, tx, id, "FOR UPDATE")
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && j.Project != project) {
		return nil, notFound(fmt.Sprintf("no job %s in project %s", jobID(id), project))
	}
	if err != nil {
		return nil, err
	}
	switch j.State {
	case stateCompleted, stateCancelled:
		return nil, conflict(fmt.Sprintf("%s is already %s", jobID(id), j.State), "only scheduled, queued, retrying, running or dead jobs can be cancelled")
	case stateDead:
		// Nothing holds a slot or waits on it: only the state changes (finished_at stays when it died).
		if _, err := tx.Exec(ctx, `UPDATE tq_jobs SET state = 'cancelled', seq = seq + 1 WHERE id = $1`, id); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return e.GetJob(ctx, project, id)
	}
	wasRunning := j.State == stateRunning
	if _, err := tx.Exec(ctx, `UPDATE tq_jobs SET state = 'cancelled', finished_at = now(), blocked_on = NULL, lease_until = NULL,
		seq = seq + 1 WHERE id = $1`, id); err != nil {
		return nil, err
	}
	j.State = stateCancelled
	if !wasRunning {
		if err := e.releaseTx(ctx, tx, j); err != nil {
			return nil, err
		}
	}
	if j.Kind == kindWorkflow && j.RunID != nil {
		// Cancelling a turn does not cancel the run; it can be retried.
		if err := e.timeline(ctx, tx, *j.RunID, "turn", "a turn ("+jobID(id)+") was cancelled", "", nil); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	if wasRunning {
		if d, ok := e.active.Load(id); ok {
			d.(*delivery).cancel(errCancelled)
		}
	}
	return e.GetJob(ctx, project, id)
}

// RetryJob runs a job again now: a dead, cancelled or completed job is
// replayed (its attempt count starts over); a scheduled or retrying one stops
// waiting.
func (e *Engine) RetryJob(ctx context.Context, project string, id int64) (*Job, error) {
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err := e.retryTx(ctx, tx, project, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return e.GetJob(ctx, project, id)
}

func (e *Engine) retryTx(ctx context.Context, tx pgx.Tx, project string, id int64) error {
	j, err := e.loadJob(ctx, tx, id, "FOR UPDATE")
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && j.Project != project) {
		return notFound(fmt.Sprintf("no job %s in project %s", jobID(id), project))
	}
	if err != nil {
		return err
	}
	switch j.State {
	case stateRunning:
		return conflict(jobID(id)+" is running now", "wait for the attempt to finish, or cancel it first")
	case stateQueued:
		if j.BlockedOn != nil {
			hint := "raise the limit with `tiffin queue configure`, or resume the queue"
			if *j.BlockedOn == stopKey(project) {
				hint = "it runs once the project starts: tiffin projects start " + project
			}
			return conflict(jobID(id)+" is waiting for "+waitingFor(*j.BlockedOn), hint)
		}
		return conflict(jobID(id)+" is already queued", "")
	}
	if _, busy := e.active.Load(id); busy {
		// A cancelled attempt that is still being cut off: replaying now
		// would run two attempts of the job at once.
		return conflict(jobID(id)+"'s cancelled attempt is still stopping", "retry in a moment")
	}
	replay := j.State == stateDead || j.State == stateCancelled || j.State == stateCompleted
	prio := j.Priority
	rid, err := e.insertRiver(ctx, tx, id, j.Seq+1, prio, e.now())
	if err != nil {
		return err
	}
	if replay {
		_, err = tx.Exec(ctx, `UPDATE tq_jobs SET state = 'queued', attempt = 0, seq = seq + 1, river_id = $2, run_at = now(),
			finished_at = NULL, blocked_on = NULL WHERE id = $1`, id, rid)
	} else {
		_, err = tx.Exec(ctx, `UPDATE tq_jobs SET state = 'queued', seq = seq + 1, river_id = $2, run_at = now(), blocked_on = NULL WHERE id = $1`, id, rid)
	}
	if err != nil {
		return err
	}
	if replay && j.Kind == kindWorkflow && j.RunID != nil {
		return e.reviveRun(ctx, tx, *j.RunID)
	}
	return nil
}

// ReplayResult reports a dead-letter replay.
type ReplayResult struct {
	DryRun   bool     `json:"dryRun"`
	Count    int      `json:"count" doc:"Dead jobs matched"`
	Replayed []string `json:"replayed" doc:"Jobs put back on their queue (empty on a dry run)"`
	Message  string   `json:"message"`
}

// ReplayDead puts dead jobs back on their queues (oldest first, up to limit).
func (e *Engine) ReplayDead(ctx context.Context, project, queue string, limit int, dryRun bool) (*ReplayResult, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	q := `SELECT id FROM tq_jobs WHERE project = $1 AND state = 'dead'`
	args := []any{project}
	if queue != "" {
		q += ` AND queue = $2`
		args = append(args, queue)
	}
	args = append(args, limit)
	q += fmt.Sprintf(` ORDER BY id LIMIT $%d`, len(args))
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	res := &ReplayResult{DryRun: dryRun, Count: len(ids), Replayed: []string{}}
	where := "in project " + project
	if queue != "" {
		where = "on queue " + strconv.Quote(queue)
	}
	if dryRun {
		res.Message = fmt.Sprintf("dry run: %d dead job(s) %s would be replayed; send dryRun=false to replay them", len(ids), where)
		return res, nil
	}
	for _, id := range ids {
		if err := e.retryTx(ctx, tx, project, id); err != nil {
			return nil, err
		}
		res.Replayed = append(res.Replayed, jobID(id))
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	res.Message = fmt.Sprintf("replayed %d dead job(s) %s", len(ids), where)
	return res, nil
}

// PurgeResult reports a purge.
type PurgeResult struct {
	Count   int    `json:"count" doc:"Jobs matched"`
	Purged  bool   `json:"purged"`
	Confirm string `json:"confirm,omitempty" doc:"Send this back as confirm to purge"`
	Message string `json:"message"`
}

// Purge deletes a queue's waiting jobs (and dead ones when dead is set).
// Without the right confirm token it only counts them and returns the token.
func (e *Engine) Purge(ctx context.Context, project, queue string, dead bool, confirm string) (*PurgeResult, error) {
	states := []string{stateScheduled, stateQueued, stateRetrying}
	if dead {
		states = append(states, stateDead)
	}
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var n int
	var maxID int64
	if err := tx.QueryRow(ctx, `SELECT count(*), coalesce(max(id), 0) FROM tq_jobs WHERE project = $1 AND queue = $2 AND state = ANY($3)`,
		project, queue, states).Scan(&n, &maxID); err != nil {
		return nil, err
	}
	token := fmt.Sprintf("purge-%d-%d", n, maxID)
	res := &PurgeResult{Count: n}
	if confirm != token {
		res.Confirm = token
		res.Message = fmt.Sprintf("%d job(s) on %q would be deleted for good; call again with confirm=%s", n, queue, token)
		if confirm != "" {
			res.Message = "the queue changed since you counted; " + res.Message
		}
		return res, nil
	}
	rows, err := tx.Query(ctx, `DELETE FROM tq_jobs WHERE project = $1 AND queue = $2 AND state = ANY($3) AND id <= $4
		RETURNING id, project, group_key`, project, queue, states, maxID)
	if err != nil {
		return nil, err
	}
	groups := map[string]bool{}
	for rows.Next() {
		var id int64
		var p string
		var g *string
		if err := rows.Scan(&id, &p, &g); err != nil {
			rows.Close()
			return nil, err
		}
		if g != nil {
			groups[*g] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for g := range groups {
		if err := e.wakeGroupHead(ctx, tx, project, g); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	res.Purged = true
	res.Message = fmt.Sprintf("deleted %d job(s) from %q", n, queue)
	return res, nil
}

// QueueStats is a queue's health.
type QueueStats struct {
	QueueConfig
	Topic         bool       `json:"topic" doc:"True for topics (fan-out to subscribers)"`
	Scheduled     int        `json:"scheduled"`
	Queued        int        `json:"queued" doc:"Due and waiting (the depth)"`
	Running       int        `json:"running"`
	Retrying      int        `json:"retrying"`
	Dead          int        `json:"dead" doc:"In the dead-letter queue"`
	OldestQueuedS int        `json:"oldestQueuedSeconds" doc:"Age of the oldest due job"`
	Completed1h   int        `json:"completedLastHour"`
	Failed1h      int        `json:"failedAttemptsLastHour"`
	Throughput1m  int        `json:"completedLastMinute"`
	FailureRate   float64    `json:"failureRate" doc:"Failed attempts / all attempts, last hour"`
	P50MS         int        `json:"p50Ms" doc:"Median attempt duration, last hour"`
	P95MS         int        `json:"p95Ms"`
	LastRunAt     *time.Time `json:"lastRunAt,omitempty" doc:"When its latest attempt finished"`
	DoneSeries    []int      `json:"doneByFiveMinutes" doc:"Attempts that succeeded in each of the last twelve 5-minute windows, oldest first (the last hour)"`
	FailedSeries  []int      `json:"failedByFiveMinutes" doc:"Attempts that failed in each of the last twelve 5-minute windows, oldest first"`
}

// seriesBuckets is how many 5-minute windows the stats series covers (an hour).
const seriesBuckets = 12

// Stats returns per-queue depth, throughput, failures and durations.
func (e *Engine) Stats(ctx context.Context, project, only string) ([]QueueStats, error) {
	by := map[string]*QueueStats{}
	get := func(name string) *QueueStats {
		if s, ok := by[name]; ok {
			return s
		}
		s := &QueueStats{QueueConfig: defaultConfig(project, name), DoneSeries: make([]int, seriesBuckets), FailedSeries: make([]int, seriesBuckets)}
		by[name] = s
		return s
	}
	rows, err := e.pool.Query(ctx, `SELECT queue, state, count(*), coalesce(extract(epoch FROM now() - min(run_at) FILTER (WHERE state = 'queued')), 0)::int
		FROM tq_jobs WHERE project = $1 AND state NOT IN ('completed', 'cancelled') GROUP BY queue, state`, project)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var q, st string
		var n, age int
		if err := rows.Scan(&q, &st, &n, &age); err != nil {
			rows.Close()
			return nil, err
		}
		s := get(q)
		switch st {
		case stateScheduled:
			s.Scheduled = n
		case stateQueued:
			s.Queued, s.OldestQueuedS = n, max(age, 0)
		case stateRunning:
			s.Running = n
		case stateRetrying:
			s.Retrying = n
		case stateDead:
			s.Dead = n
		}
	}
	rows.Close()
	rows, err = e.pool.Query(ctx, `SELECT queue,
		count(*) FILTER (WHERE outcome = 'ok'),
		count(*) FILTER (WHERE outcome IN ('retry', 'dead')),
		count(*) FILTER (WHERE outcome = 'ok' AND finished_at > now() - interval '1 minute'),
		coalesce(percentile_cont(0.5) WITHIN GROUP (ORDER BY duration_ms), 0)::int,
		coalesce(percentile_cont(0.95) WITHIN GROUP (ORDER BY duration_ms), 0)::int
		FROM tq_attempts WHERE project = $1 AND finished_at > now() - interval '1 hour' GROUP BY queue`, project)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var q string
		var ok, failed, m1, p50, p95 int
		if err := rows.Scan(&q, &ok, &failed, &m1, &p50, &p95); err != nil {
			rows.Close()
			return nil, err
		}
		s := get(q)
		s.Completed1h, s.Failed1h, s.Throughput1m, s.P50MS, s.P95MS = ok, failed, m1, p50, p95
		if ok+failed > 0 {
			s.FailureRate = float64(failed) / float64(ok+failed)
		}
	}
	rows.Close()
	// The last hour in 5-minute windows: bucket 0 is the newest (now-5m..now).
	rows, err = e.pool.Query(ctx, `SELECT queue, least(floor(extract(epoch FROM now() - finished_at) / 300), $2 - 1)::int,
		count(*) FILTER (WHERE outcome = 'ok'), count(*) FILTER (WHERE outcome IN ('retry', 'dead'))
		FROM tq_attempts WHERE project = $1 AND finished_at > now() - interval '1 hour' GROUP BY 1, 2`, project, seriesBuckets)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var q string
		var b, ok, failed int
		if err := rows.Scan(&q, &b, &ok, &failed); err != nil {
			rows.Close()
			return nil, err
		}
		if b < 0 || b >= seriesBuckets {
			continue
		}
		s := get(q)
		s.DoneSeries[seriesBuckets-1-b] += ok
		s.FailedSeries[seriesBuckets-1-b] += failed
	}
	rows.Close()
	rows, err = e.pool.Query(ctx, `SELECT queue, max(finished_at) FROM tq_attempts WHERE project = $1 GROUP BY queue`, project)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var q string
		var at time.Time
		if err := rows.Scan(&q, &at); err != nil {
			rows.Close()
			return nil, err
		}
		get(q).LastRunAt = &at
	}
	rows.Close()
	rows, err = e.pool.Query(ctx, `SELECT name FROM tq_queues WHERE project = $1 UNION SELECT name FROM tq_topics WHERE project = $1`, project)
	if err != nil {
		return nil, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		get(n)
	}
	out := []QueueStats{}
	for name, s := range by {
		if only != "" && name != only {
			continue
		}
		cfg, err := e.queueConfig(ctx, e.pool, project, name)
		if err != nil {
			return nil, err
		}
		s.QueueConfig = cfg
		if err := e.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tq_topics WHERE project = $1 AND name = $2)`, project, name).Scan(&s.Topic); err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	sortStats(out)
	return out, nil
}

func sortStats(s []QueueStats) {
	for i := 1; i < len(s); i++ {
		for k := i; k > 0 && s[k].Name < s[k-1].Name; k-- {
			s[k], s[k-1] = s[k-1], s[k]
		}
	}
}

// Heartbeat extends a running attempt's lease. attempt is the attemptId the
// delivery carried; a mismatch means that attempt is already over.
func (e *Engine) Heartbeat(ctx context.Context, project string, id int64, attempt int) (time.Time, error) {
	var until time.Time
	err := e.pool.QueryRow(ctx, `UPDATE tq_jobs SET lease_until = now() + make_interval(secs => lease_s)
		WHERE id = $1 AND project = $2 AND state = 'running' AND ($3 = 0 OR total_attempts = $3) RETURNING lease_until`, id, project, attempt).Scan(&until)
	if errors.Is(err, pgx.ErrNoRows) {
		return until, conflict(jobID(id)+" has no running attempt to extend", "the attempt timed out or was cancelled; stop working on it")
	}
	if err != nil {
		return until, err
	}
	if d, ok := e.active.Load(id); ok {
		select {
		case d.(*delivery).extend <- until:
		default:
		}
	}
	return until, nil
}
