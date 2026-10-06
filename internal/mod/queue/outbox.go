package queue

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// sendTx: an app enqueues inside its own Postgres transaction by inserting a
// row into tiffin.outbox in its own database (@shiptiffin/sdk's queue.sendTx). The
// row commits or rolls back with the app's data; the box drains committed
// rows into the queue. Each row carries a uuid used as the dedupe key, so a
// crash between enqueue and delete never enqueues twice.
//
// Why an outbox and not River's insert in the app database: River would need
// its schema and a client in every project database, and the SDK would have
// to speak River's row format. One small table works with any Postgres client
// (Bun.sql, postgres.js, pg) and keeps every job in one place.

// OutboxDDL creates the outbox (the box runs it; apps only insert).
const OutboxDDL = `CREATE SCHEMA IF NOT EXISTS tiffin;
CREATE TABLE IF NOT EXISTS tiffin.outbox (
	id         bigserial PRIMARY KEY,
	uid        uuid NOT NULL DEFAULT gen_random_uuid(),
	name       text NOT NULL,
	payload    jsonb NOT NULL DEFAULT 'null',
	options    jsonb NOT NULL DEFAULT '{}',
	app        text NOT NULL DEFAULT '',
	created_at timestamptz NOT NULL DEFAULT now()
);`

type outboxOptions struct {
	DelaySeconds int       `json:"delaySeconds"`
	RunAt        time.Time `json:"runAt"`
	Key          string    `json:"key"`
	GroupKey     string    `json:"groupKey"`
	Dedupe       string    `json:"dedupe"`
	Priority     string    `json:"priority"`
	App          string    `json:"app"`
	Path         string    `json:"path"`
	MaxAttempts  int       `json:"maxAttempts"`
}

type outboxPools struct {
	mu    sync.Mutex
	pools map[string]*pgxpool.Pool // project → pool
	dsns  map[string]string
}

// KickOutbox drains outboxes now instead of at the next poll.
func (e *Engine) KickOutbox() {
	select {
	case e.outboxKick <- struct{}{}:
	default:
	}
}

func (e *Engine) outboxLoop(ctx context.Context) {
	if e.cfg.Outbox == nil {
		return
	}
	ps := &outboxPools{pools: map[string]*pgxpool.Pool{}, dsns: map[string]string{}}
	defer func() {
		for _, p := range ps.pools {
			p.Close()
		}
	}()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	refresh := time.Time{}
	var dsns map[string]string
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-e.outboxKick:
		}
		if time.Since(refresh) > 15*time.Second {
			if d, err := e.cfg.Outbox(ctx); err == nil {
				dsns = d
			}
			refresh = time.Now()
		}
		for project, dsn := range dsns {
			pool, err := ps.get(ctx, project, dsn)
			if err != nil {
				continue
			}
			if _, err := e.DrainOutbox(ctx, project, pool); err != nil && ctx.Err() == nil {
				e.log.Warn("queue: outbox", "project", project, "err", err)
			}
		}
	}
}

func (ps *outboxPools) get(ctx context.Context, project, dsn string) (*pgxpool.Pool, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if p, ok := ps.pools[project]; ok && ps.dsns[project] == dsn {
		return p, nil
	} else if ok {
		p.Close()
		delete(ps.pools, project)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 2
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if _, err := p.Exec(ctx, OutboxDDL); err != nil {
		p.Close()
		return nil, err
	}
	ps.pools[project], ps.dsns[project] = p, dsn
	return p, nil
}

// DrainOutbox moves committed outbox rows of one project database into the
// queue and returns how many it moved.
func (e *Engine) DrainOutbox(ctx context.Context, project string, app *pgxpool.Pool) (int, error) {
	total := 0
	for {
		rows, err := app.Query(ctx, `SELECT id, uid::text, name, payload, options, app FROM tiffin.outbox ORDER BY id LIMIT 200`)
		if err != nil {
			return total, err
		}
		type row struct {
			id                  int64
			uid, name, fromApp  string
			payload, optionsRaw []byte
		}
		var rs []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.uid, &r.name, &r.payload, &r.optionsRaw, &r.fromApp); err != nil {
				rows.Close()
				return total, err
			}
			rs = append(rs, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return total, err
		}
		if len(rs) == 0 {
			return total, nil
		}
		ids := make([]int64, 0, len(rs))
		for _, r := range rs {
			var o outboxOptions
			_ = json.Unmarshal(r.optionsRaw, &o)
			req := SendRequest{Name: r.name, Payload: r.payload, Delay: time.Duration(o.DelaySeconds) * time.Second, RunAt: o.RunAt,
				Key: o.Key, GroupKey: o.GroupKey, Dedupe: o.Dedupe, Priority: o.Priority, App: o.App, Path: o.Path,
				MaxAttempts: o.MaxAttempts, By: "outbox", FromApp: r.fromApp}
			if req.Dedupe == "" {
				req.Dedupe = "outbox:" + r.uid
			}
			if _, err := e.Send(ctx, project, req); err != nil {
				if qe, ok := err.(*Error); ok {
					// A row the queue can never accept must not block the rest.
					if derr := e.deadLetter(ctx, project, r.name, r.payload, "sendTx rejected: "+qe.Msg); derr != nil {
						return total, derr
					}
				} else {
					return total, err
				}
			}
			ids = append(ids, r.id)
		}
		if _, err := app.Exec(ctx, `DELETE FROM tiffin.outbox WHERE id = ANY($1)`, ids); err != nil {
			return total, err
		}
		total += len(ids)
	}
}

// deadLetter records a message that could not be enqueued at all.
func (e *Engine) deadLetter(ctx context.Context, project, name string, payload []byte, reason string) error {
	if !json.Valid(payload) {
		payload = []byte("null")
	}
	_, err := e.pool.Exec(ctx, `INSERT INTO tq_jobs (project, queue, kind, payload, state, max_attempts, lease_s, last_error, finished_at, enqueued_by)
		VALUES ($1, $2, 'job', $3, 'dead', 0, 0, $4, now(), 'outbox')`, project, clip(name, 64), payload, reason)
	return err
}

// outboxInsert is the statement @shiptiffin/sdk runs (documented for other clients).
const outboxInsert = `INSERT INTO tiffin.outbox (name, payload, options, app) VALUES ($1, $2, $3, $4)`
