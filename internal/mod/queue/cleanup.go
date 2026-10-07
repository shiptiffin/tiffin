package queue

import (
	"context"
	"errors"

	"github.com/btahir/tiffin/internal/platform"
)

var _ platform.ProjectCleaner = (*Module)(nil)

// ProjectDeleted runs once a destroyed project has no resources left. Its
// queue key and signing secret are forgotten first, so a key an old app
// (or anyone who copied it) still holds stops working at once, and a new
// project of the same name gets fresh ones. Then its jobs, runs, events,
// webhooks, crons and limits go.
func (m *Module) ProjectDeleted(ctx context.Context, p *platform.Platform, project string) error {
	m.mu.RLock()
	k := m.keys
	m.mu.RUnlock()
	if k == nil {
		k = &kvKeys{db: p.DB}
	}
	if err := k.Delete(ctx, project); err != nil {
		return err
	}
	e := m.engine()
	if e == nil {
		return errors.New("the queue is not running yet; the project's jobs and runs are removed when it is")
	}
	return e.DeleteProject(ctx, project)
}

// DeleteProject removes everything the engine keeps about a project and
// cuts off its deliveries in flight.
func (e *Engine) DeleteProject(ctx context.Context, project string) error {
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id FROM tq_jobs WHERE project = $1 AND state = 'running'`, project)
	if err != nil {
		return err
	}
	var running []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		running = append(running, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, q := range []string{
		`DELETE FROM tq_slots WHERE job_id IN (SELECT id FROM tq_jobs WHERE project = $1) OR slot LIKE '_:' || $1 || '/%'`,
		`DELETE FROM tq_rates WHERE bucket LIKE '_:' || $1 || '/%'`,
		`DELETE FROM tq_jobs WHERE project = $1`, // attempts and output cascade
		`DELETE FROM wf_runs WHERE project = $1`, // steps, timeline and output cascade
		`DELETE FROM wf_events WHERE project = $1`,
		`DELETE FROM wf_hooks WHERE project = $1`,
		`DELETE FROM tq_dedupe WHERE project = $1`,
		`DELETE FROM tq_crons WHERE project = $1`,
		`DELETE FROM tq_subscriptions WHERE project = $1`,
		`DELETE FROM tq_topics WHERE project = $1`,
		`DELETE FROM tq_queues WHERE project = $1`,
		`DELETE FROM tq_stopped WHERE project = $1`,
	} {
		if _, err := tx.Exec(ctx, q, project); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	for _, id := range running {
		if d, ok := e.active.Load(id); ok {
			d.(*delivery).cancel(errCancelled)
		}
	}
	return nil
}
