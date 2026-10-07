package queue

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
)

type riverRow = rivertype.JobRow

// deliverArgs is the River job that delivers one attempt of a tq_jobs row.
// Seq guards against stale River rows: only the row whose seq matches the
// job's current seq may act.
type deliverArgs struct {
	JobID int64 `json:"job_id"`
	Seq   int   `json:"seq"`
}

func (deliverArgs) Kind() string { return "tq_deliver" }
func (deliverArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: riverDeliver, MaxAttempts: 25}
}

// Priorities (River: 1 is first). Retries rank below every new job.
const (
	prioHigh   = 1
	prioNormal = 2
	prioLow    = 3
	prioRetry  = 4
)

// Delivery protocol, shared with @shiptiffin/sdk.
const (
	HeaderSignature   = "Tiffin-Signature"
	HeaderJobID       = "Tiffin-Job-Id"
	HeaderAttempt     = "Tiffin-Attempt"
	HeaderNonRetry    = "Tiffin-Non-Retryable"
	HeaderRetryAfter  = "Tiffin-Retry-After"
	StatusNonRetrying = 489 // the app's "do not retry" status, as in QStash
	maxOutput         = 64 << 10
	maxAttemptTime    = 24 * time.Hour
)

const (
	outcomeOK          = "ok"
	outcomeRetry       = "retry"
	outcomeDead        = "dead"
	outcomeInterrupted = "interrupted"
)

type outcome struct {
	kind       string
	status     int
	err        string
	retryAfter time.Duration
	output     json.RawMessage
	started    time.Time
	finished   time.Time
	release    string
}

// delivery is one attempt in flight here. Heartbeats move its deadline.
type delivery struct {
	extend chan time.Time
	cancel context.CancelCauseFunc
}

var errCancelled = errors.New("cancelled by an operator")

func ready(state string) bool {
	return state == stateScheduled || state == stateQueued || state == stateRetrying
}

func (e *Engine) workDeliver(ctx context.Context, rj *river.Job[deliverArgs]) error {
	j, err := e.loadJob(ctx, e.pool, rj.Args.JobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // purged
	}
	if err != nil {
		return err
	}
	if j.Seq != rj.Args.Seq || !ready(j.State) {
		return nil // stale: a newer River row owns this job, or it finished
	}
	cfg, err := e.queueConfig(ctx, e.pool, j.Project, j.Queue)
	if err != nil {
		return err
	}
	ok, err := e.admit(ctx, rj, j, cfg)
	if err != nil || !ok {
		return err
	}
	j, err = e.loadJob(ctx, e.pool, j.ID)
	if err != nil {
		return err
	}
	rctx, cancel := context.WithCancelCause(ctx)
	d := &delivery{extend: make(chan time.Time, 4), cancel: cancel}
	e.active.Store(j.ID, d)
	oc := e.deliver(rctx, j, d)
	e.active.Delete(j.ID)
	cancel(nil)
	if ctx.Err() != nil && oc.kind != outcomeOK {
		oc.kind, oc.err = outcomeInterrupted, "the box is shutting down; the attempt will run again"
	}
	fctx, fcancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer fcancel()
	return e.finish(fctx, rj, j, oc)
}

type slotReq struct {
	key   string
	limit int
	group bool
}

func (e *Engine) slotsFor(j *jobRow, cfg QueueConfig) []slotReq {
	s := []slotReq{{key: projectSlot(j.Project), limit: e.cfg.ProjectConcurrency}}
	if cfg.Concurrency > 0 {
		s = append(s, slotReq{key: "q:" + j.Project + "/" + j.Queue, limit: cfg.Concurrency})
	}
	if j.Key != nil && cfg.KeyConcurrency > 0 {
		s = append(s, slotReq{key: "k:" + j.Project + "/" + j.Queue + "/" + *j.Key, limit: cfg.KeyConcurrency})
	}
	if j.GroupKey != nil {
		s = append(s, slotReq{key: groupSlot(j.Project, *j.GroupKey), limit: 1, group: true})
	}
	sort.Slice(s, func(a, b int) bool { return s[a].key < s[b].key })
	return s
}

func groupSlot(project, group string) string { return "g:" + project + "/" + group }

// projectSlot is the project's share of the delivery workers.
func projectSlot(project string) string { return "a:" + project + "/" }

func lockKey(ctx context.Context, tx pgx.Tx, key string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key)
	return err
}

// admit moves a ready job to running if its project is not stopped, its
// queue is not paused and every limit has room: queue concurrency, per-key
// concurrency, FIFO group order and the rate limit. Otherwise it parks the
// job (blocked_on) with a fallback wake-up; whoever frees the limit wakes it
// sooner.
func (e *Engine) admit(ctx context.Context, rj *river.Job[deliverArgs], j *jobRow, cfg QueueConfig) (bool, error) {
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var state string
	var seq int
	if err := tx.QueryRow(ctx, `SELECT state, seq FROM tq_jobs WHERE id = $1 FOR UPDATE`, j.ID).Scan(&state, &seq); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if seq != rj.Args.Seq || !ready(state) {
		return false, nil
	}
	blocked, wait, err := heldByStop(ctx, tx, j.Project)
	if err != nil {
		return false, err
	}
	if blocked == "" && cfg.Paused {
		blocked, wait = pauseKey(j.Project, j.Queue), time.Minute
		if err := lockKey(ctx, tx, blocked); err != nil {
			return false, err
		}
	}
	slots := e.slotsFor(j, cfg)
	for _, s := range slots {
		if blocked != "" {
			break
		}
		if err := lockKey(ctx, tx, s.key); err != nil {
			return false, err
		}
		if s.group {
			var head int64
			if err := tx.QueryRow(ctx, `SELECT id FROM tq_jobs WHERE project = $1 AND group_key = $2
				AND state IN ('scheduled', 'queued', 'running', 'retrying') ORDER BY id LIMIT 1`, j.Project, *j.GroupKey).Scan(&head); err != nil {
				return false, err
			}
			if head != j.ID {
				blocked, wait = s.key, 30*time.Second
				break
			}
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tq_slots WHERE slot = $1`, s.key).Scan(&n); err != nil {
			return false, err
		}
		if n >= s.limit {
			blocked, wait = s.key, 30*time.Second
		}
	}
	if blocked == "" && cfg.RateLimit > 0 && cfg.RatePeriodS > 0 {
		bucket := "r:" + j.Project + "/" + j.Queue + "/" + deref(j.Key)
		w, err := takeToken(ctx, tx, bucket, cfg.RateLimit, time.Duration(cfg.RatePeriodS)*time.Second)
		if err != nil {
			return false, err
		}
		if w > 0 {
			blocked = bucket
			// Spread a crowd waiting on one bucket over the next intervals.
			wait = w + time.Duration(rand.Int64N(int64(time.Duration(cfg.RatePeriodS)*time.Second/time.Duration(cfg.RateLimit))+1))
		}
	}
	if blocked == "" && j.URL != "" && e.cfg.URLRatePerMinute > 0 {
		// The project's cap on calls outside the box, across its queues and crons.
		bucket := outsideKey(j.Project)
		w, err := takeToken(ctx, tx, bucket, e.cfg.URLRatePerMinute, time.Minute)
		if err != nil {
			return false, err
		}
		if w > 0 {
			blocked, wait = bucket, w+time.Duration(rand.Int64N(int64(time.Minute/time.Duration(e.cfg.URLRatePerMinute))+1))
		}
	}
	if blocked != "" {
		rid, err := e.insertRiver(ctx, tx, j.ID, seq, j.Priority, e.now().Add(wait))
		if err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `UPDATE tq_jobs SET blocked_on = $2, river_id = $3,
			state = CASE WHEN state IN ('scheduled', 'retrying') THEN 'queued' ELSE state END WHERE id = $1`, j.ID, blocked, rid); err != nil {
			return false, err
		}
		if _, err := river.JobCompleteTx[*riverpgxv5.Driver](ctx, tx, rj); err != nil {
			return false, err
		}
		return false, tx.Commit(ctx)
	}
	for _, s := range slots {
		if _, err := tx.Exec(ctx, `INSERT INTO tq_slots (slot, job_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, s.key, j.ID); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE tq_jobs SET state = 'running', attempt = attempt + 1, total_attempts = total_attempts + 1,
		started_at = now(), lease_until = now() + make_interval(secs => lease_s), blocked_on = NULL WHERE id = $1`, j.ID); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func pauseKey(project, queue string) string { return "p:" + project + "/" + queue }

func outsideKey(project string) string { return "x:" + project + "/" }

// takeToken is a sliding-window rate limiter: at most limit starts in any
// window of length period. It returns how long to wait, or 0 when it took a
// slot.
func takeToken(ctx context.Context, tx pgx.Tx, bucket string, limit int, period time.Duration) (time.Duration, error) {
	if err := lockKey(ctx, tx, bucket); err != nil {
		return 0, err
	}
	var starts []time.Time
	var now time.Time
	err := tx.QueryRow(ctx, `SELECT coalesce((SELECT starts FROM tq_rates WHERE bucket = $1), '{}'), clock_timestamp()`, bucket).Scan(&starts, &now)
	if err != nil {
		return 0, err
	}
	live := starts[:0]
	for _, t := range starts {
		if t.After(now.Add(-period)) {
			live = append(live, t)
		}
	}
	if len(live) >= limit {
		return live[len(live)-limit].Add(period).Sub(now), nil
	}
	live = append(live, now)
	_, err = tx.Exec(ctx, `INSERT INTO tq_rates (bucket, starts, tat) VALUES ($1, $2, $3) ON CONFLICT (bucket) DO UPDATE SET starts = excluded.starts, tat = excluded.tat`,
		bucket, live, now)
	return 0, err
}

func (e *Engine) insertRiver(ctx context.Context, tx pgx.Tx, jobID int64, seq, priority int, at time.Time) (int64, error) {
	opts := &river.InsertOpts{Priority: priority}
	if at.After(e.now()) {
		opts.ScheduledAt = at
	}
	res, err := e.river.InsertTx(ctx, tx, deliverArgs{JobID: jobID, Seq: seq}, opts)
	if err != nil {
		return 0, err
	}
	return res.Job.ID, nil
}

// deliver pushes one attempt to the app and classifies the response.
func (e *Engine) deliver(ctx context.Context, j *jobRow, d *delivery) outcome {
	oc := outcome{started: e.now(), release: j.Release}
	defer func() { oc.finished = e.now() }()
	target, release, err := e.target(ctx, j)
	if err != nil {
		oc.kind, oc.err = outcomeRetry, err.Error()
		return oc
	}
	oc.release = release
	body, err := e.buildBody(ctx, j)
	if errors.Is(err, errRunFinished) {
		oc.kind = outcomeOK // nothing to do: the run finished or was cancelled
		return oc
	}
	if err != nil {
		oc.kind, oc.err = outcomeRetry, "could not build the delivery: "+err.Error()
		return oc
	}
	_, secret, err := e.cfg.Keys.Get(ctx, j.Project)
	if err != nil {
		oc.kind, oc.err = outcomeRetry, "signing key: "+err.Error()
		return oc
	}
	rctx, cancel := ctx, d.cancel
	lease := time.Duration(j.LeaseS) * time.Second
	hard := time.NewTimer(maxAttemptTime)
	defer hard.Stop()
	go func() {
		t := time.NewTimer(lease)
		defer t.Stop()
		for {
			select {
			case <-rctx.Done():
				return
			case <-hard.C:
				cancel(fmt.Errorf("the attempt ran longer than %s", maxAttemptTime))
				return
			case until := <-d.extend:
				if !t.Stop() {
					select {
					case <-t.C:
					default:
					}
				}
				t.Reset(time.Until(until))
			case <-t.C:
				if j.URL != "" {
					cancel(fmt.Errorf("no response within the %ds timeout", j.LeaseS))
				} else {
					cancel(fmt.Errorf("no response or heartbeat within the %ds lease", j.LeaseS))
				}
				return
			}
		}
	}()
	method := http.MethodPost
	vercel := j.Kind == kindCron && e.cronMethod(ctx, j) == http.MethodGet
	if vercel {
		method, body = http.MethodGet, nil // as Vercel calls cron paths
	}
	req, err := http.NewRequestWithContext(rctx, method, target, bytes.NewReader(body))
	if err != nil {
		oc.kind, oc.err = outcomeDead, "bad target URL: "+err.Error()
		return oc
	}
	if vercel {
		req.Header.Set("User-Agent", "vercel-cron/1.0")
		if auth := e.cronSecret(ctx, j); auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
	} else {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "tiffin-queue/1")
	}
	req.Header.Set(HeaderSignature, Sign(secret, e.now(), body))
	req.Header.Set(HeaderJobID, jobID(j.ID))
	req.Header.Set(HeaderAttempt, strconv.Itoa(j.TotalAttempts))
	client := e.http
	if j.URL != "" {
		client = e.outside
	}
	res, err := client.Do(req)
	if err != nil {
		if c := context.Cause(rctx); c != nil && rctx.Err() != nil {
			if errors.Is(c, errCancelled) {
				oc.kind, oc.err = outcomeDead, "cancelled by an operator while running"
				return oc
			}
			oc.kind, oc.err = outcomeRetry, c.Error()
			return oc
		}
		if r, ok := refusal(err); ok {
			oc.kind, oc.err = outcomeDead, r.Error()
			return oc
		}
		oc.kind, oc.err = outcomeRetry, describeTransport(err, target, j.URL != "")
		return oc
	}
	defer res.Body.Close()
	raw, rerr := io.ReadAll(io.LimitReader(res.Body, 1<<20+1))
	oc.status = res.StatusCode
	if rerr != nil && res.StatusCode >= 200 && res.StatusCode < 300 {
		// A success whose body never arrived whole (the app stalled or hung
		// up mid-answer) is not one: delivery is at least once, so try again.
		if c := context.Cause(rctx); c != nil && rctx.Err() != nil {
			if errors.Is(c, errCancelled) {
				oc.kind, oc.err = outcomeDead, "cancelled by an operator while running"
				return oc
			}
			rerr = c
		}
		oc.kind, oc.err = outcomeRetry, fmt.Sprintf("the response (HTTP %d) was cut off: %v", res.StatusCode, rerr)
		return oc
	}
	switch {
	case res.StatusCode >= 200 && res.StatusCode < 300:
		oc.kind = outcomeOK
		if len(raw) > 0 && len(raw) <= maxOutput && json.Valid(raw) {
			oc.output = raw
		}
		if j.Kind == kindWorkflow {
			oc.output = raw // the turn result is read by finish, whatever its size
		}
	case res.StatusCode == StatusNonRetrying || truthy(res.Header.Get(HeaderNonRetry)):
		oc.kind, oc.err = outcomeDead, responseError(res.StatusCode, raw, "the app said not to retry")
	default:
		oc.kind, oc.err = outcomeRetry, responseError(res.StatusCode, raw, "")
		oc.retryAfter = retryAfter(res)
	}
	return oc
}

func truthy(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "1" || s == "true" || s == "yes"
}

func retryAfter(res *http.Response) time.Duration {
	for _, h := range []string{HeaderRetryAfter, "Retry-After"} {
		v := strings.TrimSpace(res.Header.Get(h))
		if v == "" {
			continue
		}
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
		if t, err := http.ParseTime(v); err == nil {
			return time.Until(t)
		}
	}
	return 0
}

func responseError(status int, raw []byte, fallback string) string {
	msg := strings.TrimSpace(string(raw))
	var pj struct {
		Error   any    `json:"error"`
		Message string `json:"message"`
		Detail  string `json:"detail"`
	}
	if json.Unmarshal(raw, &pj) == nil {
		switch v := pj.Error.(type) {
		case string:
			msg = v
		case map[string]any:
			if m, ok := v["message"].(string); ok {
				msg = m
			}
		}
		if pj.Message != "" {
			msg = pj.Message
		} else if pj.Detail != "" && msg == strings.TrimSpace(string(raw)) {
			msg = pj.Detail
		}
	}
	if msg == "" {
		msg = fallback
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	return fmt.Sprintf("HTTP %d: %s", status, clip(msg, 2000))
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func describeTransport(err error, target string, outside bool) string {
	msg := err.Error()
	switch {
	case outside && strings.Contains(msg, "no such host"):
		return "the address " + target + " does not resolve (check the host name)"
	case outside && strings.Contains(msg, "connection refused"):
		return "connection refused at " + target + ": nothing is listening there"
	case outside && (strings.Contains(msg, "EOF") || strings.Contains(msg, "connection reset")):
		return target + " closed the connection without a response"
	case strings.Contains(msg, "connection refused"):
		return "connection refused at " + target + ": the app is not listening (stopped, crashed or still starting)"
	case strings.Contains(msg, "EOF"), strings.Contains(msg, "connection reset"):
		return "the app closed the connection without a response (it may have crashed mid-job)"
	}
	return clip(msg, 500)
}

// Sign returns the Tiffin-Signature header value: t=<unix>,v1=<hex HMAC-SHA256 of "<t>.<body>">.
func Sign(secret string, at time.Time, body []byte) string {
	ts := strconv.FormatInt(at.Unix(), 10)
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(ts))
	m.Write([]byte("."))
	m.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(m.Sum(nil))
}

// Verify checks a Tiffin-Signature header (used by tests; apps use @shiptiffin/sdk).
func Verify(secret, header string, body []byte, now time.Time, tolerance time.Duration) bool {
	var ts, sig string
	for _, part := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sig = v
		}
	}
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || sig == "" {
		return false
	}
	if d := now.Sub(time.Unix(n, 0)); d > tolerance || d < -tolerance {
		return false
	}
	want := Sign(secret, time.Unix(n, 0), body)
	return hmac.Equal([]byte(want[strings.Index(want, "v1=")+3:]), []byte(sig))
}

func (e *Engine) backoff(attempt int, workflow bool) time.Duration {
	limit := time.Hour
	if workflow {
		limit = 5 * time.Minute
	}
	base := e.cfg.RetryBase
	if base <= 0 {
		base = 2 * time.Second
	}
	d := base << min(attempt-1, 20)
	if d > limit || d <= 0 {
		d = limit
	}
	// ±25% jitter so a burst of failures doesn't retry in lockstep.
	return time.Duration(float64(d) * (0.75 + rand.Float64()/2))
}

// finish records an attempt's outcome, frees its limits, wakes parked jobs
// and completes the River row, all in one transaction.
func (e *Engine) finish(ctx context.Context, rj *river.Job[deliverArgs], j *jobRow, oc outcome) error {
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := e.finishTx(ctx, tx, j.ID, j.TotalAttempts, oc); err != nil {
		return err
	}
	if rj != nil {
		if _, err := river.JobCompleteTx[*riverpgxv5.Driver](ctx, tx, rj); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// failLost finishes a running attempt nobody is delivering any more.
func (e *Engine) failLost(ctx context.Context, id int64, reason string) error {
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var total int
	var started time.Time
	err = tx.QueryRow(ctx, `SELECT total_attempts, coalesce(started_at, now()) FROM tq_jobs WHERE id = $1 AND state = 'running' FOR UPDATE`, id).Scan(&total, &started)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := e.finishTx(ctx, tx, id, total, outcome{kind: outcomeRetry, err: reason, started: started, finished: e.now()}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (e *Engine) finishTx(ctx context.Context, tx pgx.Tx, id int64, attemptID int, oc outcome) error {
	j, err := e.loadJob(ctx, tx, id, "FOR UPDATE")
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if j.TotalAttempts != attemptID {
		return nil // an older attempt; the job has moved on
	}
	running := j.State == stateRunning
	if running && oc.kind == outcomeRetry {
		// The app went away because its project was stopped: not the job's
		// fault, so the attempt does not count and the job waits.
		stopped, err := projectStopped(ctx, tx, j.Project)
		if err != nil {
			return err
		}
		if stopped {
			oc.kind, oc.err = outcomeInterrupted, "the project was stopped during this attempt; it runs again once the project starts ("+oc.err+")"
		}
	}
	if oc.started.IsZero() {
		oc.started = e.now()
	}
	if oc.finished.IsZero() {
		oc.finished = e.now()
	}
	attemptOutcome := oc.kind
	if running {
		var status *int
		if oc.status != 0 {
			status = &oc.status
		}
		var errText *string
		if oc.err != "" {
			errText = &oc.err
		}
		switch oc.kind {
		case outcomeOK:
			out := oc.output
			if len(out) > maxOutput || !json.Valid(out) {
				out = nil
			}
			_, err = tx.Exec(ctx, `UPDATE tq_jobs SET state = 'completed', finished_at = now(), lease_until = NULL,
				output = $2, last_status = $3, last_error = NULL WHERE id = $1`, id, nullJSON(out), status)
		case outcomeInterrupted:
			var rid int64
			if rid, err = e.insertRiver(ctx, tx, id, j.Seq+1, j.Priority, e.now()); err == nil {
				_, err = tx.Exec(ctx, `UPDATE tq_jobs SET state = 'queued', attempt = attempt - 1, seq = seq + 1, river_id = $2,
					lease_until = NULL, last_error = $3 WHERE id = $1`, id, rid, errText)
			}
		case outcomeRetry:
			if j.Attempt >= j.MaxAttempts {
				attemptOutcome = outcomeDead
				msg := fmt.Sprintf("gave up after %d attempts; last error: %s", j.Attempt, oc.err)
				_, err = tx.Exec(ctx, `UPDATE tq_jobs SET state = 'dead', finished_at = now(), lease_until = NULL,
					last_status = $2, last_error = $3 WHERE id = $1`, id, status, msg)
				break
			}
			wait := e.backoff(j.Attempt, j.Kind == kindWorkflow)
			if oc.retryAfter > 0 {
				wait = min(max(oc.retryAfter, time.Second), 24*time.Hour)
			}
			at := e.now().Add(wait)
			var rid int64
			if rid, err = e.insertRiver(ctx, tx, id, j.Seq+1, prioRetry, at); err == nil {
				_, err = tx.Exec(ctx, `UPDATE tq_jobs SET state = 'retrying', run_at = $2, seq = seq + 1, river_id = $3,
					lease_until = NULL, last_status = $4, last_error = $5 WHERE id = $1`, id, at, rid, status, errText)
			}
		default: // dead
			_, err = tx.Exec(ctx, `UPDATE tq_jobs SET state = 'dead', finished_at = now(), lease_until = NULL,
				last_status = $2, last_error = $3 WHERE id = $1`, id, status, errText)
		}
		if err != nil {
			return err
		}
	} else if j.State == stateCancelled {
		attemptOutcome = "cancelled"
	}
	if attemptID > 0 {
		dur := oc.finished.Sub(oc.started).Milliseconds()
		var status *int
		if oc.status != 0 {
			status = &oc.status
		}
		if _, err := tx.Exec(ctx, `INSERT INTO tq_attempts (job_id, attempt, project, queue, started_at, finished_at, duration_ms, outcome, status, error, release)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) ON CONFLICT DO NOTHING`,
			id, attemptID, j.Project, j.Queue, oc.started, oc.finished, dur, attemptOutcome, status, nullStr(oc.err), oc.release); err != nil {
			return err
		}
	}
	if err := e.releaseTx(ctx, tx, j); err != nil {
		return err
	}
	if j.Kind == kindWorkflow && running {
		return e.turnFinished(ctx, tx, j, oc, attemptOutcome)
	}
	return nil
}

// releaseTx frees a job's limit slots and wakes jobs parked on them. When
// the job is finished it also wakes the next job of its FIFO group.
func (e *Engine) releaseTx(ctx context.Context, tx pgx.Tx, j *jobRow) error {
	rows, err := tx.Query(ctx, `DELETE FROM tq_slots WHERE job_id = $1 RETURNING slot`, j.ID)
	if err != nil {
		return err
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.HasPrefix(k, "g:") {
			continue
		}
		if err := e.wake(ctx, tx, k, 1); err != nil {
			return err
		}
	}
	if j.GroupKey != nil {
		return e.wakeGroupHead(ctx, tx, j.Project, *j.GroupKey)
	}
	return nil
}

// wake makes up to n jobs parked on key runnable now.
func (e *Engine) wake(ctx context.Context, tx pgx.Tx, key string, n int) error {
	if err := lockKey(ctx, tx, key); err != nil {
		return err
	}
	q := `SELECT id, river_id FROM tq_jobs WHERE blocked_on = $1 ORDER BY priority, id FOR UPDATE SKIP LOCKED`
	args := []any{key}
	if n > 0 {
		q = `SELECT id, river_id FROM tq_jobs WHERE blocked_on = $1 ORDER BY priority, id LIMIT $2 FOR UPDATE SKIP LOCKED`
		args = append(args, n)
	}
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return err
	}
	type parked struct {
		id  int64
		rid *int64
	}
	var ps []parked
	for rows.Next() {
		var p parked
		if err := rows.Scan(&p.id, &p.rid); err != nil {
			rows.Close()
			return err
		}
		ps = append(ps, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range ps {
		if err := e.unpark(ctx, tx, p.id, p.rid); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) unpark(ctx context.Context, tx pgx.Tx, id int64, rid *int64) error {
	if _, err := tx.Exec(ctx, `UPDATE tq_jobs SET blocked_on = NULL WHERE id = $1`, id); err != nil {
		return err
	}
	if rid != nil {
		if _, err := e.river.JobRetryTx(ctx, tx, *rid); err != nil && !errors.Is(err, river.ErrNotFound) {
			return err
		}
	}
	return nil
}

func (e *Engine) wakeGroupHead(ctx context.Context, tx pgx.Tx, project, group string) error {
	// Same lock as admit's head check: either admit sees this job finished,
	// or we see the job admit parked.
	if err := lockKey(ctx, tx, groupSlot(project, group)); err != nil {
		return err
	}
	var id int64
	var rid *int64
	var blocked *string
	err := tx.QueryRow(ctx, `SELECT id, river_id, blocked_on FROM tq_jobs WHERE project = $1 AND group_key = $2
		AND state IN ('scheduled', 'queued', 'running', 'retrying') ORDER BY id LIMIT 1`, project, group).Scan(&id, &rid, &blocked)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && blocked == nil) {
		return nil
	}
	if err != nil {
		return err
	}
	return e.unpark(ctx, tx, id, rid)
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullJSON(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return []byte(b)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
