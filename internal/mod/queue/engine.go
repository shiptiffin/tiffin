package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
)

// River queues. Deliveries are I/O bound (an HTTP request held open while the
// app works), so the pool is wide; per-queue and per-key limits are ours.
const (
	riverDeliver = "deliver"
	riverSystem  = "system"
)

// Keys hands out per-project credentials: the app key apps use to call the
// box (TIFFIN_QUEUE_KEY) and the secret the box signs deliveries with
// (TIFFIN_QUEUE_SIGNING_SECRET).
type Keys interface {
	Get(ctx context.Context, project string) (appKey, signingSecret string, err error)
}

// Config wires an Engine to the box. Only DSN is required.
type Config struct {
	DSN string
	// Keys stores per-project credentials. Default: in memory (tests).
	Keys Keys
	// Endpoint resolves an app's base URL for a release ("" = current).
	Endpoint func(ctx context.Context, project, app, release string) (string, error)
	// CurrentRelease reports an app's current release ("" = unknown).
	CurrentRelease func(ctx context.Context, project, app string) (string, error)
	// PublicURL is the box's public base (webhook URLs), e.g. https://dashboard.tiffin.localhost.
	PublicURL string
	Log       *slog.Logger
	// Workers is River's delivery pool size (default 200).
	Workers int
	// RetryBase is the first retry delay; it doubles per attempt (default 2s).
	RetryBase time.Duration
	// Outbox resolves a project's app DATABASE_URL for sendTx ("" = none).
	Outbox func(ctx context.Context) (map[string]string, error)
	// AppEnv is an app's environment (env and secrets): GET crons send its
	// CRON_SECRET. Nil: none.
	AppEnv func(ctx context.Context, project, app string) (map[string]string, error)
	// AllowNets are non-public ranges URL targets may call anyway (tests, a
	// receiver on the LAN). Default none (outside.go).
	AllowNets []netip.Prefix
	// SelfIPs are the box's own public addresses, which URL targets may not
	// call. Nil: none known.
	SelfIPs func() []netip.Addr
	// URLRatePerMinute caps the calls one project makes to URLs outside the
	// box per minute, across its queues and crons (0 = no cap).
	URLRatePerMinute int
	// Resolve looks up the hosts of URL targets (tests; nil: the system resolver).
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
}

// Engine is the queue and workflow engine on one Postgres database.
type Engine struct {
	cfg   Config
	pool  *pgxpool.Pool
	river *river.Client[pgx.Tx]
	log   *slog.Logger
	http  *http.Client
	guard *guard
	// outside calls URL targets (outside.go).
	outside *http.Client
	now     func() time.Time

	lockConn *pgx.Conn
	active   sync.Map // job id → *delivery
	retryAt  sync.Map // river job id → time.Time
	cancel   context.CancelFunc
	done     chan struct{}
	ready    chan struct{}

	outboxKick chan struct{}

	hub       *hub // live streams to browsers (live.go)
	hubCancel context.CancelFunc
}

// Open connects and migrates; it does not start working jobs (Start does).
func Open(ctx context.Context, cfg Config) (*Engine, error) {
	if cfg.DSN == "" {
		return nil, errors.New("queue: no database")
	}
	pcfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("queue database url: %w", err)
	}
	if pcfg.MaxConns < 20 {
		pcfg.MaxConns = 20
	}
	pcfg.AfterConnect = func(_ context.Context, c *pgx.Conn) error {
		// Times come back in UTC, whatever the box's time zone.
		c.TypeMap().RegisterType(&pgtype.Type{Name: "timestamptz", OID: pgtype.TimestamptzOID, Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC}})
		return nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Keys == nil {
		cfg.Keys = newMemKeys()
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 200
	}
	e := &Engine{cfg: cfg, pool: pool, log: cfg.Log, now: time.Now,
		http:       &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 64, IdleConnTimeout: 90 * time.Second}},
		guard:      &guard{allow: cfg.AllowNets, self: cfg.SelfIPs, lookup: cfg.Resolve},
		ready:      make(chan struct{}),
		outboxKick: make(chan struct{}, 1)}
	e.outside = e.guard.client()
	var hctx context.Context
	hctx, e.hubCancel = context.WithCancel(context.Background())
	e.hub = newHub(hctx, e)
	workers := river.NewWorkers()
	river.AddWorker(workers, river.WorkFunc(e.workDeliver))
	river.AddWorker(workers, river.WorkFunc(e.workWake))
	rc, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues: map[string]river.QueueConfig{
			riverDeliver: {MaxWorkers: cfg.Workers},
			riverSystem:  {MaxWorkers: 20},
		},
		Workers:     workers,
		JobTimeout:  -1, // leases (ours) bound deliveries
		RetryPolicy: e,
		// We own crash recovery (see recover); River's rescuer is a backstop
		// that must never fire on a delivery that is still running.
		RescueStuckJobsAfter:        25 * time.Hour,
		MaxAttempts:                 25,
		CompletedJobRetentionPeriod: time.Hour,
		Logger:                      slog.New(slog.DiscardHandler),
		FetchCooldown:               20 * time.Millisecond,
		FetchPollInterval:           200 * time.Millisecond,
		Middleware:                  []rivertype.Middleware{&promptSchedule{}},
	})
	if err != nil {
		pool.Close()
		return nil, err
	}
	e.river = rc
	return e, nil
}

// NextRetry is the River retry policy for internal failures (a database
// error while finalizing): quick, capped. App failures never reach it: they
// are scheduled as new attempts by the delivery worker.
func (e *Engine) NextRetry(job *riverRow) time.Time {
	if t, ok := e.retryAt.LoadAndDelete(job.ID); ok {
		return t.(time.Time)
	}
	n := len(job.Errors)
	return time.Now().Add(min(time.Duration(1<<min(n, 8))*time.Second, 5*time.Minute))
}

// Start takes the single-owner lock (one process works this database at a
// time; a second waits), recovers deliveries orphaned by a crash and starts
// working. It returns at once; work begins when the lock is held.
func (e *Engine) Start(ctx context.Context) {
	ctx, e.cancel = context.WithCancel(ctx)
	e.done = make(chan struct{})
	go func() {
		defer close(e.done)
		if err := e.acquireOwner(ctx); err != nil {
			if ctx.Err() == nil {
				e.log.Error("queue: owner lock", "err", err)
			}
			return
		}
		if err := e.recover(ctx); err != nil {
			e.log.Error("queue: recover", "err", err)
		}
		if err := e.river.Start(ctx); err != nil {
			e.log.Error("queue: start river", "err", err)
			return
		}
		close(e.ready)
		var wg sync.WaitGroup
		for _, loop := range []func(context.Context){e.reaperLoop, e.cronLoop, e.pruneLoop, e.outboxLoop} {
			wg.Add(1)
			go func() { defer wg.Done(); loop(ctx) }()
		}
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = e.river.Stop(sctx)
		cancel()
		wg.Wait()
	}()
}

// Ready is closed once the engine works jobs.
func (e *Engine) Ready() <-chan struct{} { return e.ready }

// Close stops work and releases the database.
func (e *Engine) Close() {
	e.hubCancel()
	if e.cancel != nil {
		e.cancel()
		<-e.done
	}
	if e.lockConn != nil {
		_ = e.lockConn.Close(context.Background())
	}
	e.pool.Close()
}

// acquireOwner holds a session advisory lock on a dedicated connection. If
// tiffin is killed the connection drops and Postgres releases the lock.
func (e *Engine) acquireOwner(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, e.cfg.DSN)
	if err != nil {
		return err
	}
	for {
		var ok bool
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(7357001)`).Scan(&ok); err != nil {
			conn.Close(context.Background())
			return err
		}
		if ok {
			e.lockConn = conn
			return nil
		}
		select {
		case <-ctx.Done():
			conn.Close(context.Background())
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// recover runs once, as owner, before work starts: nothing is delivering, so
// every job still marked running was cut off by a crash or kill. Each one
// gets a failed attempt ("the box restarted") and is retried; limit slots
// held by the dead process are freed.
func (e *Engine) recover(ctx context.Context) error {
	if _, err := e.pool.Exec(ctx, `DELETE FROM tq_slots`); err != nil {
		return err
	}
	rows, err := e.pool.Query(ctx, `SELECT id FROM tq_jobs WHERE state = 'running'`)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := e.failLost(ctx, id, "the box restarted while this attempt was running"); err != nil {
			return err
		}
	}
	if len(ids) > 0 {
		e.log.Info("queue: recovered interrupted deliveries", "count", len(ids))
	}
	return nil
}

// reaperLoop fails attempts whose lease ran out without a heartbeat and that
// no goroutine here is delivering (normally the delivery itself notices).
func (e *Engine) reaperLoop(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		rows, err := e.pool.Query(ctx, `SELECT id FROM tq_jobs WHERE state = 'running' AND lease_until < now() - interval '15 seconds'`)
		if err != nil {
			continue
		}
		ids, _ := pgx.CollectRows(rows, pgx.RowTo[int64])
		for _, id := range ids {
			if _, live := e.active.Load(id); live {
				continue
			}
			if err := e.failLost(ctx, id, "the lease expired and no delivery was in progress"); err != nil {
				e.log.Error("queue: reap", "job", id, "err", err)
			}
		}
	}
}

// pruneLoop drops finished jobs (7 days; dead ones 30), expired dedupe keys
// and old workflow events.
func (e *Engine) pruneLoop(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		_, _ = e.pool.Exec(ctx, `DELETE FROM tq_jobs WHERE id IN (SELECT id FROM tq_jobs WHERE finished_at IS NOT NULL AND
			((state IN ('completed', 'cancelled') AND finished_at < now() - interval '7 days') OR (state = 'dead' AND finished_at < now() - interval '30 days'))
			AND (run_id IS NULL OR NOT EXISTS (SELECT 1 FROM wf_runs r WHERE r.id = tq_jobs.run_id)) LIMIT 5000)`)
		_, _ = e.pool.Exec(ctx, `DELETE FROM tq_dedupe WHERE expires_at < now()`)
		_, _ = e.pool.Exec(ctx, `DELETE FROM wf_runs WHERE state IN ('completed', 'failed', 'cancelled') AND finished_at < now() - interval '30 days'`)
		_, _ = e.pool.Exec(ctx, `DELETE FROM wf_events WHERE created_at < now() - interval '30 days'`)
		_, _ = e.pool.Exec(ctx, `DELETE FROM tq_rates WHERE tat < now() - interval '1 day'`)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// promptSchedule inserts jobs due within a minute as "available" with a
// future scheduled_at. River's fetch already skips rows not yet due, so they
// start on time; River's scheduler, which promotes "scheduled" rows only
// every 5 seconds, would make short retry backoffs and delays late.
type promptSchedule struct{ river.MiddlewareDefaults }

func (*promptSchedule) InsertMany(ctx context.Context, many []*rivertype.JobInsertParams, inner func(context.Context) ([]*rivertype.JobInsertResult, error)) ([]*rivertype.JobInsertResult, error) {
	for _, p := range many {
		if p.State == rivertype.JobStateScheduled && p.ScheduledAt != nil && time.Until(*p.ScheduledAt) < time.Minute {
			p.State = rivertype.JobStateAvailable
		}
	}
	return inner(ctx)
}
