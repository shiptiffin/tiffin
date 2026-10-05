package queue

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// A stopped project (tiffin projects stop) has no app to deliver to. Its
// jobs wait, queued, instead of spending attempts, and its crons do not
// fire. Starting it again delivers the waiting jobs, and its crons continue
// from their next tick (ticks that fell inside the stop are skipped).

func stopKey(project string) string { return "s:" + project + "/" }

func projectStopped(ctx context.Context, q querier, project string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tq_stopped WHERE project = $1)`, project).Scan(&ok)
	return ok, err
}

// SetProjectStopped holds a project's deliveries and crons (stopped) or lets
// them go again. It is idempotent.
func (e *Engine) SetProjectStopped(ctx context.Context, project string, stopped bool) error {
	if stopped {
		_, err := e.pool.Exec(ctx, `INSERT INTO tq_stopped (project) VALUES ($1) ON CONFLICT DO NOTHING`, project)
		return err
	}
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM tq_stopped WHERE project = $1`, project); err != nil {
		return err
	}
	// Same lock as admit's: either admit sees the stop lifted, or we see
	// the job it parked.
	if err := e.wake(ctx, tx, stopKey(project), 0); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT name, schedule FROM tq_crons WHERE project = $1 AND next_at <= now() FOR UPDATE`, project)
	if err != nil {
		return err
	}
	type due struct{ name, schedule string }
	ds, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (due, error) {
		var d due
		return d, r.Scan(&d.name, &d.schedule)
	})
	if err != nil {
		return err
	}
	for _, d := range ds {
		sched, err := cronParser.Parse(d.schedule)
		if err != nil {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE tq_crons SET next_at = $3 WHERE project = $1 AND name = $2`,
			project, d.name, sched.Next(e.now().UTC())); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// heldByStop reports whether admit must park j because its project is
// stopped, re-checking under the stop's lock so a concurrent start cannot
// miss the parked job.
func heldByStop(ctx context.Context, tx pgx.Tx, project string) (string, time.Duration, error) {
	stopped, err := projectStopped(ctx, tx, project)
	if err != nil || !stopped {
		return "", 0, err
	}
	key := stopKey(project)
	if err := lockKey(ctx, tx, key); err != nil {
		return "", 0, err
	}
	if stopped, err = projectStopped(ctx, tx, project); err != nil || !stopped {
		return "", 0, err
	}
	return key, time.Minute, nil
}
