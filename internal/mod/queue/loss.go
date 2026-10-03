package queue

import (
	"context"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/platform"
)

// Waiting counts the jobs deleting a queue would destroy (scheduled, queued,
// retrying or dead; running ones finish) and the size of their payloads.
func (e *Engine) Waiting(ctx context.Context, project, queue string) (jobs, bytes int64, err error) {
	err = e.pool.QueryRow(ctx, `SELECT count(*), coalesce(sum(pg_column_size(payload)), 0)::bigint FROM tq_jobs
		WHERE project = $1 AND queue = $2 AND state = ANY($3)`,
		project, queue, []string{stateScheduled, stateQueued, stateRetrying, stateDead}).Scan(&jobs, &bytes)
	return jobs, bytes, err
}

// EstimateLoss says how many jobs deleting a queue throws away.
func (m *Module) EstimateLoss(ctx context.Context, _ *platform.Platform, project string, op change.Op) (*change.Loss, error) {
	if change.Kind(op.Address) != change.KindQueue || op.Action != change.Delete {
		return nil, nil
	}
	e := m.engine()
	if e == nil {
		return nil, nil
	}
	n, b, err := e.Waiting(ctx, project, change.Name(op.Address))
	if err != nil {
		return nil, err
	}
	return &change.Loss{Bytes: b, Counts: []change.LossCount{{N: n, Unit: "job"}}}, nil
}
