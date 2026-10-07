package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// sendTx: an app enqueues inside its own Postgres transaction by inserting a
// row into tiffin_queue.outbox in its own database (@shiptiffin/sdk's queue.sendTx). The
// row commits or rolls back with the app's data; the box drains committed
// rows into the queue. Each row carries a uuid used as the dedupe key, so a
// crash between enqueue and delete never enqueues twice.
//
// Why an outbox and not River's insert in the app database: River would need
// its schema and a client in every project database, and the SDK would have
// to speak River's row format. One small table works with any Postgres client
// (Bun.sql, postgres.js, pg) and keeps every job in one place.

// OutboxDDL creates the outbox (the box runs it; apps only insert).
const OutboxDDL = `CREATE SCHEMA IF NOT EXISTS tiffin_queue;
CREATE TABLE IF NOT EXISTS tiffin_queue.outbox (
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

// Draining is fair: each pass moves at most one batch per project, starting
// from a different project each time, under a deadline, so an app that
// fills its outbox faster than the box drains it cannot hold up the others.
const (
	outboxBatch    = 100              // rows per project per pass
	outboxBytes    = 8 << 20          // payload bytes per batch (one row always goes)
	outboxOptBytes = 16 << 10         // a row's options; more is rejected
	outboxTimeout  = 10 * time.Second // per project per pass
)

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
	defer ps.close(nil)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	refresh := time.Time{}
	var dsns map[string]string
	busy := false
	for pass := 0; ; pass++ {
		if busy { // a project has more: go again at once, still fairly
			select {
			case <-ctx.Done():
				return
			default:
			}
		} else {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			case <-e.outboxKick:
			}
		}
		if time.Since(refresh) > 15*time.Second {
			if d, err := e.cfg.Outbox(ctx); err == nil {
				dsns = d
				ps.close(dsns) // projects that are gone, or lost Postgres
			}
			refresh = time.Now()
		}
		busy = e.drainPass(ctx, ps, dsns, pass)
	}
}

// drainPass moves one batch from each project's outbox, starting with the
// pass-th project, and reports whether any had more.
func (e *Engine) drainPass(ctx context.Context, ps *outboxPools, dsns map[string]string, pass int) (more bool) {
	projects := make([]string, 0, len(dsns))
	for p := range dsns {
		projects = append(projects, p)
	}
	sort.Strings(projects)
	for i := range projects {
		project := projects[(pass+i)%len(projects)]
		pctx, cancel := context.WithTimeout(ctx, outboxTimeout)
		pool, err := ps.get(pctx, project, dsns[project])
		if err == nil {
			var full bool
			_, full, err = e.drainBatch(pctx, project, pool)
			more = more || full
		}
		cancel()
		if err != nil && ctx.Err() == nil {
			e.log.Warn("queue: outbox", "project", project, "err", err)
		}
	}
	return more
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

// close closes the pools of projects not in keep (nil: all).
func (ps *outboxPools) close(keep map[string]string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for project, p := range ps.pools {
		if dsn, ok := keep[project]; !ok || dsn != ps.dsns[project] {
			p.Close()
			delete(ps.pools, project)
			delete(ps.dsns, project)
		}
	}
}

// DrainOutbox moves every committed outbox row of one project database into
// the queue and returns how many it moved (tests; the loop drains a batch
// per project per pass).
func (e *Engine) DrainOutbox(ctx context.Context, project string, app *pgxpool.Pool) (int, error) {
	total := 0
	for {
		n, more, err := e.drainBatch(ctx, project, app)
		total += n
		if err != nil || !more {
			return total, err
		}
	}
}

// outboxSelect reads one batch, oldest first. Sizes are measured in the
// app's database: a payload over the limit ($1) or options over theirs ($2)
// come back NULL, and a batch stops once $3 bytes of payload are in it (its
// first row always comes), so an app can't make the box hold much more than
// that in memory, whatever it writes.
const outboxSelect = `WITH b AS (
	SELECT id, uid, name, payload, options, app, octet_length(payload::text) AS ps, octet_length(options::text) AS os
	FROM tiffin_queue.outbox ORDER BY id LIMIT $4
), c AS (
	SELECT *, sum(least(ps, $1)) OVER (ORDER BY id) - least(ps, $1) AS before FROM b
)
SELECT id, uid::text, left(name, 200), CASE WHEN ps <= $1 THEN payload END, CASE WHEN os <= $2 THEN options END, left(app, 100), ps, os
FROM c WHERE before < $3 ORDER BY id`

// drainBatch moves one batch; full reports that it moved some, so the
// outbox may hold more.
func (e *Engine) drainBatch(ctx context.Context, project string, app *pgxpool.Pool) (moved int, full bool, err error) {
	rows, err := app.Query(ctx, outboxSelect, maxPayload, outboxOptBytes, outboxBytes, outboxBatch)
	if err != nil {
		return 0, false, err
	}
	type row struct {
		id                  int64
		uid, name, fromApp  string
		payload, optionsRaw []byte
		psize, osize        int64
	}
	var rs []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.uid, &r.name, &r.payload, &r.optionsRaw, &r.fromApp, &r.psize, &r.osize); err != nil {
			rows.Close()
			return 0, false, err
		}
		rs = append(rs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, false, err
	}
	if len(rs) == 0 {
		return 0, false, nil
	}
	ids := make([]int64, 0, len(rs))
	for _, r := range rs {
		reject := ""
		switch {
		case r.payload == nil:
			reject = fmt.Sprintf("the payload is %d bytes; the limit is %d", r.psize, maxPayload)
		case r.optionsRaw == nil:
			reject = fmt.Sprintf("the options are %d bytes; the limit is %d", r.osize, outboxOptBytes)
		}
		if reject == "" {
			var o outboxOptions
			_ = json.Unmarshal(r.optionsRaw, &o)
			req := SendRequest{Name: r.name, Payload: r.payload, Delay: time.Duration(o.DelaySeconds) * time.Second, RunAt: o.RunAt,
				Key: o.Key, GroupKey: o.GroupKey, Dedupe: o.Dedupe, Priority: o.Priority, App: o.App, Path: o.Path,
				MaxAttempts: o.MaxAttempts, By: "outbox", FromApp: r.fromApp}
			if req.Dedupe == "" {
				req.Dedupe = "outbox:" + r.uid
			}
			if _, err := e.Send(ctx, project, req); err != nil {
				qe, ok := err.(*Error)
				if !ok {
					return len(ids), false, err
				}
				reject = qe.Msg
			}
		}
		if reject != "" {
			// A row the queue can never accept must not block the rest.
			if err := e.deadLetter(ctx, project, r.name, r.payload, "sendTx rejected: "+reject); err != nil {
				return len(ids), false, err
			}
		}
		ids = append(ids, r.id)
	}
	if _, err := app.Exec(ctx, `DELETE FROM tiffin_queue.outbox WHERE id = ANY($1)`, ids); err != nil {
		return 0, false, err
	}
	return len(ids), true, nil // more may have come: look again
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
const outboxInsert = `INSERT INTO tiffin_queue.outbox (name, payload, options, app) VALUES ($1, $2, $3, $4)`
