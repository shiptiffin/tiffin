package queue

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/ids"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// Workflows are durable functions that run inside the app (tiffin-sdk).
// Each "turn" is a queue job that POSTs the run and its completed steps to
// the app; the SDK replays the function, skipping finished steps, until it
// completes or reaches something to wait for (a sleep, an event, an
// approval). The SDK records each step the moment it finishes, so a crash
// never loses one. Turns of a run share a FIFO group, so one runs at a time.

// Run states.
const (
	runRunning   = "running" // a turn is queued or running
	runWaiting   = "waiting" // suspended on a sleep, event or approval
	runCompleted = "completed"
	runFailed    = "failed"
	runCancelled = "cancelled"
)

// Step kinds and states.
const (
	stepStep     = "step"
	stepSleep    = "sleep"
	stepEvent    = "event"
	stepApproval = "approval"
	stepWebhook  = "webhook"
	stepPatch    = "patch"

	stepCompleted = "completed"
	stepFailed    = "failed"
	stepWaiting   = "waiting"
	stepTimedOut  = "timed_out"
	stepCancelled = "cancelled"
)

// DefaultWorkflowPath is where tiffin-sdk's workflow handler is mounted.
const DefaultWorkflowPath = "/_tiffin/workflows"

var errRunFinished = errors.New("run already finished")

type wakeArgs struct {
	RunID  string `json:"run_id"`
	Step   string `json:"step"`
	Reason string `json:"reason"` // sleep | timeout
}

func (wakeArgs) Kind() string { return "wf_wake" }
func (wakeArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: riverSystem, MaxAttempts: 25}
}

// Step is one checkpoint of a run.
type Step struct {
	Name        string          `json:"name"`
	Seq         int             `json:"seq" doc:"Position in the workflow's call order"`
	Kind        string          `json:"kind" enum:"step,sleep,event,approval,webhook,patch"`
	State       string          `json:"state" enum:"completed,failed,waiting,timed_out,cancelled"`
	Output      json.RawMessage `json:"output,omitempty"`
	Error       string          `json:"error,omitempty" doc:"Latest failure (steps retry with the turn)"`
	Attempts    int             `json:"attempts" doc:"Failed attempts so far"`
	StartedAt   time.Time       `json:"startedAt"`
	FinishedAt  *time.Time      `json:"finishedAt,omitempty"`
	DurationMS  int             `json:"durationMs,omitempty"`
	WaitUntil   *time.Time      `json:"waitUntil,omitempty" doc:"Sleeps: wake time; event waits: timeout"`
	Event       string          `json:"event,omitempty" doc:"Event this step waits for"`
	ApprovalID  string          `json:"approvalId,omitempty"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	HumanOnly   bool            `json:"humanOnly,omitempty"`
	DecidedBy   string          `json:"decidedBy,omitempty"`
	Release     string          `json:"release,omitempty"`
}

// TimelineEntry is one thing that happened to a run.
type TimelineEntry struct {
	At      time.Time       `json:"at"`
	Kind    string          `json:"kind" doc:"started, turn, step, wait, event, approval, completed, failed, cancelled, retried, release"`
	Message string          `json:"message"`
	Actor   string          `json:"actor,omitempty" doc:"Who did it (token name and kind), for operator actions"`
	Detail  json.RawMessage `json:"detail,omitempty"`
}

// Run is a workflow run.
type Run struct {
	ID         string          `json:"id"`
	Workflow   string          `json:"workflow"`
	App        string          `json:"app"`
	Release    string          `json:"release,omitempty" doc:"The app release this run is pinned to"`
	State      string          `json:"state" enum:"running,waiting,completed,failed,cancelled"`
	WaitingFor string          `json:"waitingFor,omitempty" doc:"What a waiting run waits for"`
	Input      json.RawMessage `json:"input,omitempty"`
	Output     json.RawMessage `json:"output,omitempty"`
	Progress   json.RawMessage `json:"progress,omitempty" doc:"The latest progress the run reported (ctx.progress in tiffin-sdk)"`
	Error      string          `json:"error,omitempty"`
	Turns      int             `json:"turns" doc:"Times the app has run the function"`
	IdemKey    string          `json:"idempotencyKey,omitempty"`
	StartedBy  string          `json:"startedBy,omitempty"`
	CreatedAt  time.Time       `json:"createdAt"`
	UpdatedAt  time.Time       `json:"updatedAt"`
	FinishedAt *time.Time      `json:"finishedAt,omitempty"`
	Steps      []Step          `json:"steps,omitempty" doc:"Checkpoints in call order (get only)"`
	Timeline   []TimelineEntry `json:"timeline,omitempty" doc:"Everything that happened, oldest first (get only)"`
	TurnJobs   []Job           `json:"turnJobs,omitempty" doc:"The queue jobs that ran each turn (get only)"`
}

type runRow struct {
	ID, Project, Workflow, App, Path, URL, Release, State string
	Input, Output, Progress                               json.RawMessage
	Error, Idem                                           *string
	Turns                                                 int
	StartedBy                                             string
	CreatedAt, UpdatedAt                                  time.Time
	FinishedAt                                            *time.Time
}

const runCols = `id, project, workflow, app, path, url, release, state, input, output, error, idem, turns, started_by, created_at, updated_at, finished_at, progress`

func scanRun(r pgx.Row) (*runRow, error) {
	var x runRow
	err := r.Scan(&x.ID, &x.Project, &x.Workflow, &x.App, &x.Path, &x.URL, &x.Release, &x.State, &x.Input, &x.Output, &x.Error, &x.Idem,
		&x.Turns, &x.StartedBy, &x.CreatedAt, &x.UpdatedAt, &x.FinishedAt, &x.Progress)
	return &x, err
}

func (e *Engine) loadRun(ctx context.Context, q querier, project, id string, lock bool) (*runRow, error) {
	sql := `SELECT ` + runCols + ` FROM wf_runs WHERE id = $1`
	if lock {
		sql += ` FOR UPDATE`
	}
	r, err := scanRun(q.QueryRow(ctx, sql, id))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && project != "" && r.Project != project) {
		return nil, notFound(fmt.Sprintf("no workflow run %s in project %s", id, project))
	}
	return r, err
}

func finalRun(state string) bool {
	return state == runCompleted || state == runFailed || state == runCancelled
}

// StartRequest starts a workflow run.
type StartRequest struct {
	Workflow string
	Input    json.RawMessage
	ID       string // idempotency key: the same key returns the same run
	App      string
	Path     string
	URL      string
	By       string
	FromApp  string
}

// StartRun starts a run (or returns the existing one for the same idempotency key).
func (e *Engine) StartRun(ctx context.Context, project string, r StartRequest) (*Run, bool, error) {
	if !nameRE.MatchString(r.Workflow) {
		return nil, false, invalid("workflow names are 1-64 lowercase letters, digits, dots, dashes or underscores", "")
	}
	if len(r.Input) == 0 {
		r.Input = json.RawMessage("null")
	}
	if !json.Valid(r.Input) || len(r.Input) > maxPayload {
		return nil, false, invalid("input must be JSON, at most 1 MB", "")
	}
	if len(r.ID) > 200 {
		return nil, false, invalid("the idempotency key is longer than 200 characters", "")
	}
	if r.App == "" {
		r.App = r.FromApp
	}
	if r.Path == "" {
		r.Path = DefaultWorkflowPath
	}
	if r.App == "" && r.URL == "" {
		return nil, false, invalid("which app runs this workflow?", "pass app: the app whose code defines the workflow with tiffin-sdk")
	}
	if err := e.validTarget(r.App, r.Path, r.URL); err != nil {
		return nil, false, err
	}
	release := ""
	if r.App != "" && e.cfg.CurrentRelease != nil {
		rel, err := e.cfg.CurrentRelease(ctx, project, r.App)
		if err == nil {
			release = rel
		}
	}
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	if r.ID != "" {
		if err := lockKey(ctx, tx, "wf:"+project+"/"+r.Workflow+"/"+r.ID); err != nil {
			return nil, false, err
		}
		var existing string
		err := tx.QueryRow(ctx, `SELECT id FROM wf_runs WHERE project = $1 AND workflow = $2 AND idem = $3`, project, r.Workflow, r.ID).Scan(&existing)
		if err == nil {
			run, err := e.GetRun(ctx, project, existing, false)
			return run, false, err
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, false, err
		}
	}
	id := ids.New("run")
	if _, err := tx.Exec(ctx, `INSERT INTO wf_runs (id, project, workflow, app, path, url, release, state, input, idem, started_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'running', $8, $9, $10)`, id, project, r.Workflow, r.App, r.Path, r.URL, release, []byte(r.Input), nullStr(r.ID), r.By); err != nil {
		return nil, false, err
	}
	msg := "started"
	if release != "" {
		msg += " on release " + release
	}
	if err := e.timeline(ctx, tx, id, "started", msg, r.By, nil); err != nil {
		return nil, false, err
	}
	if err := e.enqueueTurn(ctx, tx, id); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	run, err := e.GetRun(ctx, project, id, false)
	return run, true, err
}

// enqueueTurn queues the next turn of a run unless one is already waiting
// to start (a running turn may have read old history, so it doesn't count).
func (e *Engine) enqueueTurn(ctx context.Context, tx pgx.Tx, runID string) error {
	var pending bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tq_jobs WHERE run_id = $1 AND state IN ('scheduled', 'queued', 'retrying'))`, runID).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return nil
	}
	run, err := e.loadRun(ctx, tx, "", runID, false)
	if err != nil {
		return err
	}
	cfg, err := e.queueConfig(ctx, tx, run.Project, queueWorkflows)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"runId": runID, "workflow": run.Workflow})
	_, err = e.insertJob(ctx, tx, newJob{project: run.Project, queue: queueWorkflows, kind: kindWorkflow, runID: runID,
		app: run.App, path: run.Path, url: run.URL, release: run.Release, payload: payload, groupKey: "run:" + runID,
		priority: prioNormal, maxAttempts: cfg.MaxAttempts, leaseS: cfg.LeaseS, runAt: e.now(), by: "workflow"})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE wf_runs SET state = 'running', updated_at = now() WHERE id = $1 AND state = 'waiting'`, runID)
	return err
}

func (e *Engine) timeline(ctx context.Context, q querier, runID, kind, msg, actor string, detail any) error {
	var d []byte
	if detail != nil {
		d, _ = json.Marshal(detail)
	}
	_, err := q.Exec(ctx, `INSERT INTO wf_timeline (run_id, kind, message, actor, detail) VALUES ($1, $2, $3, $4, $5)`, runID, kind, msg, actor, d)
	return err
}

// deliveryBody is what every push carries. Apps verify the signature, then
// read it (tiffin-sdk does both).
type deliveryBody struct {
	Type         string          `json:"type"` // job | cron | workflow
	ID           string          `json:"id"`
	Queue        string          `json:"queue"`
	Topic        string          `json:"topic,omitempty"`
	Subscription string          `json:"subscription,omitempty"`
	Cron         string          `json:"cron,omitempty"`
	Key          string          `json:"key,omitempty"`
	GroupKey     string          `json:"groupKey,omitempty"`
	Attempt      int             `json:"attempt"`
	MaxAttempts  int             `json:"maxAttempts"`
	AttemptID    int             `json:"attemptId"`
	LeaseSeconds int             `json:"leaseSeconds"`
	EnqueuedAt   time.Time       `json:"enqueuedAt"`
	Payload      json.RawMessage `json:"payload"`
	Run          *turnRun        `json:"run,omitempty"`
}

type turnRun struct {
	ID        string          `json:"id"`
	Workflow  string          `json:"workflow"`
	Input     json.RawMessage `json:"input"`
	Release   string          `json:"release,omitempty"`
	Turn      int             `json:"turn"`
	CreatedAt time.Time       `json:"createdAt"`
	Steps     []Step          `json:"steps"`
}

func (e *Engine) buildBody(ctx context.Context, j *jobRow) ([]byte, error) {
	b := deliveryBody{Type: j.Kind, ID: jobID(j.ID), Queue: j.Queue, Topic: deref(j.Topic), Subscription: deref(j.Subscription),
		Cron: deref(j.Cron), Key: deref(j.Key), GroupKey: deref(j.GroupKey), Attempt: j.Attempt, MaxAttempts: j.MaxAttempts,
		AttemptID: j.TotalAttempts, LeaseSeconds: j.LeaseS, EnqueuedAt: j.EnqueuedAt, Payload: j.Payload}
	if j.Kind == kindWorkflow && j.RunID != nil {
		run, err := e.loadRun(ctx, e.pool, j.Project, *j.RunID, false)
		if err != nil {
			return nil, err
		}
		if finalRun(run.State) {
			return nil, errRunFinished
		}
		steps, err := e.steps(ctx, *j.RunID)
		if err != nil {
			return nil, err
		}
		b.Payload = nil
		b.Run = &turnRun{ID: run.ID, Workflow: run.Workflow, Input: run.Input, Release: run.Release, Turn: run.Turns + 1,
			CreatedAt: run.CreatedAt, Steps: steps}
	}
	return json.Marshal(b)
}

// target resolves where a job goes. Workflow turns go to the run's pinned
// release; if the runtime says that release is gone, the run moves to the
// current release (patched() keeps such moves safe).
func (e *Engine) target(ctx context.Context, j *jobRow) (string, string, error) {
	if j.URL != "" {
		return j.URL, j.Release, nil
	}
	if e.cfg.Endpoint == nil {
		return "", "", fmt.Errorf("no app runtime on this box yet to push to app %q; for development, set a url with `tiffin queue configure`", j.App)
	}
	release := j.Release
	if j.Kind == kindWorkflow && j.RunID != nil {
		if run, err := e.loadRun(ctx, e.pool, j.Project, *j.RunID, false); err == nil {
			release = run.Release
		}
	}
	base, err := e.cfg.Endpoint(ctx, j.Project, j.App, release)
	if err != nil && release != "" && (errors.Is(err, ErrReleaseGone) || strings.Contains(err.Error(), "release gone")) {
		cur := ""
		if e.cfg.CurrentRelease != nil {
			cur, _ = e.cfg.CurrentRelease(ctx, j.Project, j.App)
		}
		if j.RunID != nil {
			if _, uerr := e.pool.Exec(ctx, `UPDATE wf_runs SET release = $2, updated_at = now() WHERE id = $1`, *j.RunID, cur); uerr == nil {
				_ = e.timeline(ctx, e.pool, *j.RunID, "release", fmt.Sprintf("release %s no longer runs; the run moved to release %s", release, orDash(cur)), "", nil)
			}
		}
		release = cur
		base, err = e.cfg.Endpoint(ctx, j.Project, j.App, "")
	}
	if err != nil {
		return "", release, fmt.Errorf("app %q: %w", j.App, err)
	}
	return strings.TrimRight(base, "/") + j.Path, release, nil
}

func orDash(s string) string {
	if s == "" {
		return "(current)"
	}
	return s
}

func (e *Engine) steps(ctx context.Context, runID string) ([]Step, error) {
	rows, err := e.pool.Query(ctx, `SELECT id, name, seq, kind, state, output, coalesce(error, ''), attempts, started_at, finished_at, wait_until,
		coalesce(event, ''), coalesce(title, ''), coalesce(description, ''), human_only, coalesce(decided_by, ''), release
		FROM wf_steps WHERE run_id = $1 ORDER BY seq, id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Step{}
	for rows.Next() {
		var s Step
		var id int64
		if err := rows.Scan(&id, &s.Name, &s.Seq, &s.Kind, &s.State, &s.Output, &s.Error, &s.Attempts, &s.StartedAt, &s.FinishedAt,
			&s.WaitUntil, &s.Event, &s.Title, &s.Description, &s.HumanOnly, &s.DecidedBy, &s.Release); err != nil {
			return nil, err
		}
		if s.FinishedAt != nil {
			s.DurationMS = int(s.FinishedAt.Sub(s.StartedAt).Milliseconds())
		}
		if s.Kind == stepApproval {
			s.ApprovalID = approvalID(id)
		}
		if strings.HasPrefix(s.Event, "approval:") {
			s.Event = ""
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func approvalID(id int64) string { return "apr_" + strconv.FormatInt(id, 10) }

// turnResult is what tiffin-sdk's workflow handler answers.
type turnResult struct {
	Status string          `json:"status"` // completed | suspended
	Output json.RawMessage `json:"output"`
}

// turnFinished applies a turn's outcome to its run (inside finish's tx).
func (e *Engine) turnFinished(ctx context.Context, tx pgx.Tx, j *jobRow, oc outcome, attemptOutcome string) error {
	if j.RunID == nil {
		return nil
	}
	run, err := e.loadRun(ctx, tx, j.Project, *j.RunID, true)
	if err != nil {
		var qe *Error
		if errors.As(err, &qe) {
			return nil
		}
		return err
	}
	if finalRun(run.State) {
		return nil
	}
	switch attemptOutcome {
	case outcomeOK:
		var tr turnResult
		if err := json.Unmarshal(oc.output, &tr); err != nil || (tr.Status != "completed" && tr.Status != "suspended") {
			msg := fmt.Sprintf("the app answered HTTP %d but not with a workflow result; is tiffin-sdk's workflow handler mounted at %s?", oc.status, run.Path)
			return e.failRun(ctx, tx, run.ID, msg)
		}
		if _, err := tx.Exec(ctx, `UPDATE wf_runs SET turns = turns + 1, updated_at = now() WHERE id = $1`, run.ID); err != nil {
			return err
		}
		if tr.Status == "completed" {
			if len(tr.Output) == 0 {
				tr.Output = json.RawMessage("null")
			}
			if _, err := tx.Exec(ctx, `UPDATE wf_runs SET state = 'completed', output = $2, finished_at = now(), updated_at = now() WHERE id = $1`,
				run.ID, []byte(tr.Output)); err != nil {
				return err
			}
			return e.timeline(ctx, tx, run.ID, "completed", fmt.Sprintf("completed in %d turn(s)", run.Turns+1), "", nil)
		}
		var pending bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tq_jobs WHERE run_id = $1 AND id <> $2 AND state IN ('scheduled', 'queued', 'retrying'))`,
			run.ID, j.ID).Scan(&pending); err != nil {
			return err
		}
		state := runWaiting
		if pending {
			state = runRunning
		}
		_, err := tx.Exec(ctx, `UPDATE wf_runs SET state = $2, updated_at = now() WHERE id = $1`, run.ID, state)
		return err
	case outcomeDead:
		return e.failRun(ctx, tx, run.ID, oc.err)
	case outcomeRetry:
		return e.timeline(ctx, tx, run.ID, "turn", "turn failed, will retry: "+oc.err, "", nil)
	}
	return nil
}

func (e *Engine) failRun(ctx context.Context, tx pgx.Tx, runID, msg string) error {
	if _, err := tx.Exec(ctx, `UPDATE wf_runs SET state = 'failed', error = $2, finished_at = now(), updated_at = now() WHERE id = $1`, runID, msg); err != nil {
		return err
	}
	return e.timeline(ctx, tx, runID, "failed", msg, "", nil)
}

// StepRecord is a step result the SDK reports as soon as the step finishes.
type StepRecord struct {
	Name       string          `json:"name"`
	Seq        int             `json:"seq"`
	OK         bool            `json:"ok"`
	Output     json.RawMessage `json:"output,omitempty"`
	Error      string          `json:"error,omitempty"`
	StartedAt  time.Time       `json:"startedAt"`
	DurationMS int             `json:"durationMs"`
}

// RecordStep checkpoints a step. It returns the step as stored: a step that
// was already completed keeps its first result.
func (e *Engine) RecordStep(ctx context.Context, project, runID string, s StepRecord) (*Step, error) {
	if s.Name == "" || len(s.Name) > 200 {
		return nil, invalid("step names are 1-200 characters", "")
	}
	if len(s.Output) == 0 {
		s.Output = json.RawMessage("null")
	}
	if !json.Valid(s.Output) || len(s.Output) > maxPayload {
		return nil, invalid("a step's result must be JSON, at most 1 MB", "return less, or store it and return a key")
	}
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	run, err := e.loadRun(ctx, tx, project, runID, true)
	if err != nil {
		return nil, err
	}
	if finalRun(run.State) {
		return nil, conflict("run "+runID+" is "+run.State, "stop: the run will not continue")
	}
	if s.StartedAt.IsZero() {
		s.StartedAt = e.now().Add(-time.Duration(s.DurationMS) * time.Millisecond)
	}
	finished := s.StartedAt.Add(time.Duration(s.DurationMS) * time.Millisecond)
	if s.OK {
		_, err = tx.Exec(ctx, `INSERT INTO wf_steps (run_id, project, name, seq, kind, state, output, started_at, finished_at, release)
			VALUES ($1, $2, $3, $4, 'step', 'completed', $5, $6, $7, $8)
			ON CONFLICT (run_id, name) DO UPDATE SET state = 'completed', output = $5, error = NULL, started_at = $6, finished_at = $7, release = $8
			WHERE wf_steps.state <> 'completed'`, runID, project, s.Name, s.Seq, []byte(s.Output), s.StartedAt, finished, run.Release)
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO wf_steps (run_id, project, name, seq, kind, state, error, attempts, started_at, finished_at, release)
			VALUES ($1, $2, $3, $4, 'step', 'failed', $5, 1, $6, $7, $8)
			ON CONFLICT (run_id, name) DO UPDATE SET state = 'failed', error = $5, attempts = wf_steps.attempts + 1, started_at = $6, finished_at = $7
			WHERE wf_steps.state <> 'completed'`, runID, project, s.Name, s.Seq, clip(s.Error, 4000), s.StartedAt, finished, run.Release)
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return e.step(ctx, runID, s.Name)
}

func (e *Engine) step(ctx context.Context, runID, name string) (*Step, error) {
	steps, err := e.steps(ctx, runID)
	if err != nil {
		return nil, err
	}
	for _, s := range steps {
		if s.Name == name {
			return &s, nil
		}
	}
	return nil, notFound("no step " + strconv.Quote(name))
}

// WaitRequest creates a suspension point (idempotent by step name).
type WaitRequest struct {
	Name        string    `json:"name"`
	Seq         int       `json:"seq"`
	Kind        string    `json:"kind"` // sleep | event | approval | webhook | patch
	Until       time.Time `json:"until,omitempty"`
	Event       string    `json:"event,omitempty"`
	TimeoutS    int       `json:"timeoutSeconds,omitempty"`
	Title       string    `json:"title,omitempty"`
	Description string    `json:"description,omitempty"`
	HumanOnly   bool      `json:"humanOnly,omitempty"`
}

// Wait records a sleep, event wait, approval, webhook or patch marker and
// returns its state now: completed (with output), waiting or timed_out.
func (e *Engine) Wait(ctx context.Context, project, runID string, w WaitRequest) (*Step, error) {
	if w.Name == "" || len(w.Name) > 200 {
		return nil, invalid("step names are 1-200 characters", "")
	}
	switch w.Kind {
	case stepSleep, stepEvent, stepApproval, stepWebhook, stepPatch:
	default:
		return nil, invalid("kind must be sleep, event, approval, webhook or patch", "")
	}
	if w.Kind == stepEvent && (w.Event == "" || len(w.Event) > 300) {
		return nil, invalid("an event wait needs an event name (1-300 characters)", "")
	}
	if w.TimeoutS < 0 || w.TimeoutS > 366*24*3600 {
		return nil, invalid("timeoutSeconds must be between 0 and a year", "")
	}
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	run, err := e.loadRun(ctx, tx, project, runID, true)
	if err != nil {
		return nil, err
	}
	var kind string
	err = tx.QueryRow(ctx, `SELECT kind FROM wf_steps WHERE run_id = $1 AND name = $2`, runID, w.Name).Scan(&kind)
	if err == nil {
		if kind != w.Kind {
			return nil, conflict(fmt.Sprintf("step %q was a %s in this run's history but the code now makes it a %s", w.Name, kind, w.Kind),
				"workflow code changed under a running run; guard the change with ctx.patched()")
		}
		tx.Rollback(ctx)
		return e.step(ctx, runID, w.Name)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if finalRun(run.State) {
		return nil, conflict("run "+runID+" is "+run.State, "stop: the run will not continue")
	}
	now := e.now()
	insert := func(state string, output any, until *time.Time, event, title, desc string, human bool) error {
		var out []byte
		if output != nil {
			out, _ = json.Marshal(output)
		}
		var fin *time.Time
		if state != stepWaiting {
			fin = &now
		}
		_, err := tx.Exec(ctx, `INSERT INTO wf_steps (run_id, project, name, seq, kind, state, output, started_at, finished_at, wait_until, event, title, description, human_only, release)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
			runID, project, w.Name, w.Seq, w.Kind, state, out, now, fin, until, nullStr(event), nullStr(title), nullStr(desc), human, run.Release)
		return err
	}
	schedule := func(at time.Time, reason string) error {
		_, err := e.river.InsertTx(ctx, tx, wakeArgs{RunID: runID, Step: w.Name, Reason: reason}, &river.InsertOpts{ScheduledAt: at})
		return err
	}
	var timeout *time.Time
	if w.TimeoutS > 0 {
		t := now.Add(time.Duration(w.TimeoutS) * time.Second)
		timeout = &t
	}
	switch w.Kind {
	case stepPatch:
		err = insert(stepCompleted, true, nil, "", "", "", false)
	case stepSleep:
		if !w.Until.After(now) {
			err = insert(stepCompleted, nil, &w.Until, "", "", "", false)
			break
		}
		if err = insert(stepWaiting, nil, &w.Until, "", "", "", false); err == nil {
			err = schedule(w.Until, "sleep")
		}
		if err == nil {
			err = e.timeline(ctx, tx, runID, "wait", fmt.Sprintf("sleeping until %s (%s)", w.Until.UTC().Format(time.RFC3339), w.Name), "", nil)
		}
	case stepWebhook:
		token := "wh_" + randomHex(20)
		if _, err = tx.Exec(ctx, `INSERT INTO wf_hooks (token, project, run_id, step) VALUES ($1, $2, $3, $4)`, token, project, runID, w.Name); err == nil {
			url := strings.TrimRight(e.cfg.PublicURL, "/") + "/v1/hooks/" + token
			err = insert(stepCompleted, map[string]string{"url": url, "event": "hook:" + token}, nil, "", "", "", false)
		}
	case stepEvent, stepApproval:
		event := w.Event
		if w.Kind == stepApproval {
			event = "approval:" + runID + ":" + w.Name
			if w.Title == "" {
				w.Title = w.Name
			}
		}
		var payload json.RawMessage
		perr := tx.QueryRow(ctx, `SELECT payload FROM wf_events WHERE project = $1 AND name = $2`, project, event).Scan(&payload)
		if perr == nil {
			err = insert(stepCompleted, payload, nil, event, w.Title, w.Description, w.HumanOnly)
			break
		} else if !errors.Is(perr, pgx.ErrNoRows) {
			return nil, perr
		}
		if err = insert(stepWaiting, nil, timeout, event, w.Title, w.Description, w.HumanOnly); err == nil && timeout != nil {
			err = schedule(*timeout, "timeout")
		}
		if err == nil {
			msg := "waiting for event " + strconv.Quote(event)
			if w.Kind == stepApproval {
				who := "anyone with write access"
				if w.HumanOnly {
					who = "a human"
				}
				msg = fmt.Sprintf("waiting for approval %q from %s", w.Title, who)
			}
			err = e.timeline(ctx, tx, runID, "wait", msg, "", nil)
		}
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return e.step(ctx, runID, w.Name)
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// workWake resolves a sleep or an event timeout and queues the next turn.
func (e *Engine) workWake(ctx context.Context, rj *river.Job[wakeArgs]) error {
	a := rj.Args
	newState := stepCompleted
	msg := "woke from sleep " + strconv.Quote(a.Step)
	if a.Reason == "timeout" {
		newState, msg = stepTimedOut, "timed out waiting: "+strconv.Quote(a.Step)
	}
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE wf_steps SET state = $3, finished_at = now() WHERE run_id = $1 AND name = $2 AND state = 'waiting'`, a.RunID, a.Step, newState)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	if err := e.timeline(ctx, tx, a.RunID, "wait", msg, "", nil); err != nil {
		return err
	}
	if err := e.enqueueTurn(ctx, tx, a.RunID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// EmitResult reports an event.
type EmitResult struct {
	Accepted bool     `json:"accepted" doc:"False when the event was already emitted: the first emit wins and later ones are ignored"`
	Woke     []string `json:"woke" doc:"Runs this event resumed"`
	Message  string   `json:"message"`
}

// reservedEvent reports names only the box may emit.
func reservedEvent(name string) bool {
	return strings.HasPrefix(name, "approval:") || strings.HasPrefix(name, "hook:")
}

// Emit records an event (first emit wins) and resumes runs waiting for it.
// Events are kept 30 days, so a run that starts waiting later still gets it.
func (e *Engine) Emit(ctx context.Context, project, name string, payload json.RawMessage, by string) (*EmitResult, error) {
	if name == "" || len(name) > 300 {
		return nil, invalid("event names are 1-300 characters", "")
	}
	if reservedEvent(name) {
		return nil, invalid("event names starting with approval: or hook: are reserved", "decide approvals with `tiffin workflows approvals decide`")
	}
	return e.emit(ctx, project, name, payload, by)
}

func (e *Engine) emit(ctx context.Context, project, name string, payload json.RawMessage, by string) (*EmitResult, error) {
	if len(payload) == 0 {
		payload = json.RawMessage("null")
	}
	if !json.Valid(payload) || len(payload) > maxPayload {
		return nil, invalid("an event payload must be JSON, at most 1 MB", "")
	}
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	res, err := e.emitTx(ctx, tx, project, name, payload, by)
	if err != nil {
		return nil, err
	}
	return res, tx.Commit(ctx)
}

func (e *Engine) emitTx(ctx context.Context, tx pgx.Tx, project, name string, payload json.RawMessage, by string) (*EmitResult, error) {
	tag, err := tx.Exec(ctx, `INSERT INTO wf_events (project, name, payload, emitted_by) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`,
		project, name, []byte(payload), by)
	if err != nil {
		return nil, err
	}
	res := &EmitResult{Accepted: tag.RowsAffected() == 1, Woke: []string{}}
	if !res.Accepted {
		res.Message = fmt.Sprintf("event %q was already emitted; the first payload stands", name)
		return res, nil
	}
	rows, err := tx.Query(ctx, `UPDATE wf_steps SET state = 'completed', output = $3, finished_at = now()
		WHERE project = $1 AND event = $2 AND state = 'waiting' RETURNING run_id, name`, project, name, []byte(payload))
	if err != nil {
		return nil, err
	}
	type w struct{ run, step string }
	var ws []w
	for rows.Next() {
		var x w
		if err := rows.Scan(&x.run, &x.step); err != nil {
			rows.Close()
			return nil, err
		}
		ws = append(ws, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, x := range ws {
		label := "event " + strconv.Quote(name)
		if strings.HasPrefix(name, "hook:") {
			label = "webhook call"
		}
		if err := e.timeline(ctx, tx, x.run, "event", label+" resumed step "+strconv.Quote(x.step), by, nil); err != nil {
			return nil, err
		}
		if err := e.enqueueTurn(ctx, tx, x.run); err != nil {
			return nil, err
		}
		res.Woke = append(res.Woke, x.run)
	}
	res.Message = fmt.Sprintf("event %q recorded; resumed %d run(s)", name, len(ws))
	return res, nil
}

// Hook emits a webhook's event. The token is the secret.
func (e *Engine) Hook(ctx context.Context, token string, payload json.RawMessage) (*EmitResult, error) {
	var project string
	err := e.pool.QueryRow(ctx, `SELECT project FROM wf_hooks WHERE token = $1`, token).Scan(&project)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFound("unknown webhook")
	}
	if err != nil {
		return nil, err
	}
	return e.emit(ctx, project, "hook:"+token, payload, "webhook")
}

// Approval is a pending or decided approval step.
type Approval struct {
	ID          string          `json:"id" doc:"Approval ID, e.g. apr_12"`
	RunID       string          `json:"runId"`
	Workflow    string          `json:"workflow"`
	Step        string          `json:"step"`
	Title       string          `json:"title"`
	Description string          `json:"description,omitempty"`
	HumanOnly   bool            `json:"humanOnly" doc:"Only a human (not an agent token) may decide"`
	State       string          `json:"state" enum:"waiting,completed,timed_out,cancelled"`
	Decision    json.RawMessage `json:"decision,omitempty" doc:"{approved, by, comment, decidedAt}"`
	TimeoutAt   *time.Time      `json:"timeoutAt,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
}

// Approvals lists approvals (state "" = all; default waiting).
func (e *Engine) Approvals(ctx context.Context, project, state string) ([]Approval, error) {
	q := `SELECT s.id, s.run_id, r.workflow, s.name, coalesce(s.title, ''), coalesce(s.description, ''), s.human_only, s.state, s.output, s.wait_until, s.started_at
		FROM wf_steps s JOIN wf_runs r ON r.id = s.run_id WHERE s.project = $1 AND s.kind = 'approval'`
	args := []any{project}
	if state != "" && state != "all" {
		q += ` AND s.state = $2`
		args = append(args, state)
	}
	q += ` ORDER BY s.id DESC LIMIT 200`
	rows, err := e.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Approval{}
	for rows.Next() {
		var a Approval
		var id int64
		if err := rows.Scan(&id, &a.RunID, &a.Workflow, &a.Step, &a.Title, &a.Description, &a.HumanOnly, &a.State, &a.Decision, &a.TimeoutAt, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.ID = approvalID(id)
		out = append(out, a)
	}
	return out, rows.Err()
}

// Decider is who decides an approval.
type Decider struct {
	Name  string
	Human bool
}

// Decide approves or rejects a waiting approval and resumes its run.
func (e *Engine) Decide(ctx context.Context, project, id string, approve bool, comment string, by Decider) (*Approval, error) {
	n, err := strconv.ParseInt(strings.TrimPrefix(id, "apr_"), 10, 64)
	if err != nil {
		return nil, invalid("approval IDs look like apr_12", "list them with `tiffin workflows approvals list <project>`")
	}
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var runID, name, state, title string
	var human bool
	err = tx.QueryRow(ctx, `SELECT run_id, name, state, coalesce(title, ''), human_only FROM wf_steps WHERE id = $1 AND project = $2 AND kind = 'approval' FOR UPDATE`,
		n, project).Scan(&runID, &name, &state, &title, &human)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFound(fmt.Sprintf("no approval %s in project %s", id, project))
	}
	if err != nil {
		return nil, err
	}
	if state != stepWaiting {
		return nil, conflict(fmt.Sprintf("approval %s is already %s", id, state), "")
	}
	if human && !by.Human {
		return nil, &Error{Status: 403, Code: "forbidden", Msg: fmt.Sprintf("approval %q needs a human decision; agent tokens cannot decide it", title),
			Hint: "ask a person to approve it in the dashboard or with their own token"}
	}
	decision, _ := json.Marshal(map[string]any{"approved": approve, "by": by.Name, "comment": comment, "decidedAt": e.now().UTC()})
	if _, err := tx.Exec(ctx, `UPDATE wf_steps SET state = 'completed', output = $2, decided_by = $3, finished_at = now() WHERE id = $1`, n, decision, by.Name); err != nil {
		return nil, err
	}
	verb := "rejected"
	if approve {
		verb = "approved"
	}
	msg := fmt.Sprintf("%s %q", verb, title)
	if comment != "" {
		msg += ": " + comment
	}
	if err := e.timeline(ctx, tx, runID, "approval", msg, by.Name, nil); err != nil {
		return nil, err
	}
	if err := e.enqueueTurn(ctx, tx, runID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	as, err := e.Approvals(ctx, project, "all")
	if err != nil {
		return nil, err
	}
	for _, a := range as {
		if a.ID == id {
			return &a, nil
		}
	}
	return nil, notFound(id)
}

// CancelRun stops a run: waits are cancelled and no further turn runs.
func (e *Engine) CancelRun(ctx context.Context, project, id, by string) (*Run, error) {
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := e.loadRun(ctx, tx, project, id, false); err != nil {
		return nil, err
	}
	// Lock in the order everyone else does: the run's jobs (a finishing turn
	// holds its job, then locks the run), then its waiting steps (an event or
	// approval holds the step, then locks the run), then the run. Taking the
	// run first deadlocks with either.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM tq_jobs WHERE run_id = $1 AND state IN ('scheduled', 'queued', 'retrying', 'running') ORDER BY id FOR UPDATE`, id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM wf_steps WHERE run_id = $1 AND state = 'waiting' ORDER BY seq FOR UPDATE`, id); err != nil {
		return nil, err
	}
	run, err := e.loadRun(ctx, tx, project, id, true)
	if err != nil {
		return nil, err
	}
	if finalRun(run.State) {
		return nil, conflict("run "+id+" is already "+run.State, "")
	}
	if _, err := tx.Exec(ctx, `UPDATE wf_runs SET state = 'cancelled', finished_at = now(), updated_at = now() WHERE id = $1`, id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE wf_steps SET state = 'cancelled', finished_at = now() WHERE run_id = $1 AND state = 'waiting'`, id); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `UPDATE tq_jobs SET state = 'cancelled', finished_at = now(), blocked_on = NULL, seq = seq + 1
		WHERE run_id = $1 AND state IN ('scheduled', 'queued', 'retrying', 'running') RETURNING id, state`, id)
	if err != nil {
		return nil, err
	}
	var running []int64
	for rows.Next() {
		var jid int64
		var st string
		if err := rows.Scan(&jid, &st); err != nil {
			rows.Close()
			return nil, err
		}
		running = append(running, jid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := e.timeline(ctx, tx, id, "cancelled", "cancelled", by, nil); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	for _, jid := range running {
		if d, ok := e.active.Load(jid); ok {
			d.(*delivery).cancel(errCancelled)
		}
	}
	return e.GetRun(ctx, project, id, true)
}

// RetryRun resumes a failed run from where it stopped: completed steps are
// kept, failed ones run again with fresh attempts.
func (e *Engine) RetryRun(ctx context.Context, project, id, by string) (*Run, error) {
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	run, err := e.loadRun(ctx, tx, project, id, true)
	if err != nil {
		return nil, err
	}
	if run.State != runFailed {
		return nil, conflict("run "+id+" is "+run.State+", not failed", "only failed runs can be retried")
	}
	if err := e.reviveRun(ctx, tx, id); err != nil {
		return nil, err
	}
	if err := e.timeline(ctx, tx, id, "retried", "retried from the last checkpoint", by, nil); err != nil {
		return nil, err
	}
	if err := e.enqueueTurn(ctx, tx, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return e.GetRun(ctx, project, id, true)
}

func (e *Engine) reviveRun(ctx context.Context, tx pgx.Tx, id string) error {
	if _, err := tx.Exec(ctx, `UPDATE wf_runs SET state = 'running', error = NULL, finished_at = NULL, updated_at = now() WHERE id = $1 AND state = 'failed'`, id); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE wf_steps SET attempts = 0 WHERE run_id = $1 AND state = 'failed'`, id)
	return err
}

// RunFilter narrows a run listing.
type RunFilter struct {
	Workflow string
	State    string
	Limit    int
}

// ListRuns lists runs, newest first.
func (e *Engine) ListRuns(ctx context.Context, project string, f RunFilter) ([]Run, error) {
	q := `SELECT ` + runCols + ` FROM wf_runs WHERE project = $1`
	args := []any{project}
	if f.Workflow != "" {
		args = append(args, f.Workflow)
		q += fmt.Sprintf(" AND workflow = $%d", len(args))
	}
	if f.State != "" {
		args = append(args, f.State)
		q += fmt.Sprintf(" AND state = $%d", len(args))
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}
	args = append(args, f.Limit)
	q += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))
	rows, err := e.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		run := r.public()
		run.Input = nil
		out = append(out, run)
	}
	rows.Close()
	for i := range out {
		if out[i].State == runWaiting {
			out[i].WaitingFor = e.waitingSummary(ctx, out[i].ID)
		}
	}
	return out, rows.Err()
}

func (r *runRow) public() Run {
	return Run{ID: r.ID, Workflow: r.Workflow, App: r.App, Release: r.Release, State: r.State, Input: r.Input, Output: r.Output, Progress: r.Progress,
		Error: deref(r.Error), Turns: r.Turns, IdemKey: deref(r.Idem), StartedBy: r.StartedBy, CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt, FinishedAt: r.FinishedAt}
}

func (e *Engine) waitingSummary(ctx context.Context, runID string) string {
	rows, err := e.pool.Query(ctx, `SELECT kind, name, coalesce(event, ''), wait_until, coalesce(title, '') FROM wf_steps WHERE run_id = $1 AND state = 'waiting' ORDER BY seq`, runID)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var kind, name, event, title string
		var until *time.Time
		if rows.Scan(&kind, &name, &event, &until, &title) != nil {
			continue
		}
		switch kind {
		case stepSleep:
			parts = append(parts, "sleep until "+until.UTC().Format(time.RFC3339))
		case stepApproval:
			parts = append(parts, "approval "+strconv.Quote(title))
		default:
			if strings.HasPrefix(event, "hook:") {
				parts = append(parts, "webhook call ("+name+")")
			} else {
				parts = append(parts, "event "+strconv.Quote(event))
			}
		}
	}
	return strings.Join(parts, ", ")
}

// GetRun returns a run; full adds steps, timeline and turn jobs.
func (e *Engine) GetRun(ctx context.Context, project, id string, full bool) (*Run, error) {
	r, err := e.loadRun(ctx, e.pool, project, id, false)
	if err != nil {
		return nil, err
	}
	run := r.public()
	if run.State == runWaiting {
		run.WaitingFor = e.waitingSummary(ctx, id)
	}
	if run.Steps, err = e.steps(ctx, id); err != nil {
		return nil, err
	}
	if !full {
		return &run, nil
	}
	rows, err := e.pool.Query(ctx, `SELECT at, kind, message, actor, detail FROM wf_timeline WHERE run_id = $1 ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t TimelineEntry
		if err := rows.Scan(&t.At, &t.Kind, &t.Message, &t.Actor, &t.Detail); err != nil {
			rows.Close()
			return nil, err
		}
		run.Timeline = append(run.Timeline, t)
	}
	rows.Close()
	if run.TurnJobs, err = e.ListJobs(ctx, project, ListFilter{RunID: id, Limit: 200}); err != nil {
		return nil, err
	}
	return &run, nil
}

// PinnedReleases lists releases of an app that unfinished runs are pinned to.
func (e *Engine) PinnedReleases(ctx context.Context, project, app string) ([]string, error) {
	rows, err := e.pool.Query(ctx, `SELECT DISTINCT release FROM wf_runs WHERE project = $1 AND app = $2 AND release <> ''
		AND state IN ('running', 'waiting') ORDER BY release`, project, app)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
