package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// A running job or workflow run reports how it is going: progress is one JSON
// value (the latest wins, stored on the job or run row) and output is an
// append-only list of JSON chunks (tq_output). Browsers watch both live
// (live.go).

const (
	maxProgress    = 16 << 10 // bytes of one progress value
	maxChunk       = 64 << 10 // bytes of one output chunk
	maxChunks      = 10000    // chunks per job or run
	maxOutputBytes = 1 << 20  // bytes of output per job or run
)

func checkJSON(what string, v json.RawMessage, limit int) (json.RawMessage, error) {
	if len(v) == 0 {
		v = json.RawMessage("null")
	}
	if !json.Valid(v) {
		return nil, invalid(what+" must be JSON", "")
	}
	if len(v) > limit {
		return nil, invalid(fmt.Sprintf("%s is %d bytes; the limit is %d KB", what, len(v), limit>>10), "keep it small; store big results elsewhere and send their key")
	}
	return v, nil
}

func outputFull() error {
	return &Error{Status: 413, Code: "validation", Msg: fmt.Sprintf("output is full: at most %d chunks and %d MB per job or run", maxChunks, maxOutputBytes>>20),
		Hint: "send fewer, larger chunks, or store big results and send their key"}
}

// JobProgress sets a running job's progress. attempt is the attemptId the
// delivery carried (0: whichever attempt is running).
func (e *Engine) JobProgress(ctx context.Context, project string, id int64, attempt int, v json.RawMessage) error {
	v, err := checkJSON("progress", v, maxProgress)
	if err != nil {
		return err
	}
	tag, err := e.pool.Exec(ctx, `UPDATE tq_jobs SET progress = $4 WHERE id = $1 AND project = $2 AND state = 'running'
		AND ($3 = 0 OR total_attempts = $3)`, id, project, attempt, []byte(v))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return conflict(jobID(id)+" has no running attempt to report progress for", "the attempt finished, timed out or was cancelled; stop working on it")
	}
	return nil
}

// JobOutput appends a chunk to a running job's output and returns its ID.
func (e *Engine) JobOutput(ctx context.Context, project string, id int64, attempt int, data json.RawMessage) (int64, error) {
	data, err := checkJSON("an output chunk", data, maxChunk)
	if err != nil {
		return 0, err
	}
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var n, size int
	err = tx.QueryRow(ctx, `UPDATE tq_jobs SET out_n = out_n + 1, out_bytes = out_bytes + $4 WHERE id = $1 AND project = $2
		AND state = 'running' AND ($3 = 0 OR total_attempts = $3) RETURNING out_n, out_bytes`, id, project, attempt, len(data)).Scan(&n, &size)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, conflict(jobID(id)+" has no running attempt to add output to", "the attempt finished, timed out or was cancelled; stop working on it")
	}
	if err != nil {
		return 0, err
	}
	if n > maxChunks || size > maxOutputBytes {
		return 0, outputFull()
	}
	var chunk int64
	err = tx.QueryRow(ctx, `INSERT INTO tq_output (job_id, data) VALUES ($1, $2) RETURNING id`, id, []byte(data)).Scan(&chunk)
	if err != nil {
		return 0, err
	}
	return chunk, tx.Commit(ctx)
}

// RunProgress sets an unfinished workflow run's progress.
func (e *Engine) RunProgress(ctx context.Context, project, runID string, v json.RawMessage) error {
	v, err := checkJSON("progress", v, maxProgress)
	if err != nil {
		return err
	}
	tag, err := e.pool.Exec(ctx, `UPDATE wf_runs SET progress = $3 WHERE id = $1 AND project = $2 AND state IN ('running', 'waiting')`,
		runID, project, []byte(v))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return e.runNotLive(ctx, project, runID)
	}
	return nil
}

// RunOutput appends a chunk to an unfinished workflow run's output.
func (e *Engine) RunOutput(ctx context.Context, project, runID string, data json.RawMessage) (int64, error) {
	data, err := checkJSON("an output chunk", data, maxChunk)
	if err != nil {
		return 0, err
	}
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var n, size int
	err = tx.QueryRow(ctx, `UPDATE wf_runs SET out_n = out_n + 1, out_bytes = out_bytes + $3 WHERE id = $1 AND project = $2
		AND state IN ('running', 'waiting') RETURNING out_n, out_bytes`, runID, project, len(data)).Scan(&n, &size)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, e.runNotLive(ctx, project, runID)
	}
	if err != nil {
		return 0, err
	}
	if n > maxChunks || size > maxOutputBytes {
		return 0, outputFull()
	}
	var chunk int64
	if err := tx.QueryRow(ctx, `INSERT INTO tq_output (run_id, data) VALUES ($1, $2) RETURNING id`, runID, []byte(data)).Scan(&chunk); err != nil {
		return 0, err
	}
	return chunk, tx.Commit(ctx)
}

func (e *Engine) runNotLive(ctx context.Context, project, runID string) error {
	run, err := e.loadRun(ctx, e.pool, project, runID, false)
	if err != nil {
		return err
	}
	return conflict("run "+runID+" is "+run.State, "stop: the run will not continue")
}
