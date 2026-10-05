package queue

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

// migrations are this module's own tables, applied in order once each. River's
// tables (river_job, ...) are migrated by rivermigrate, untouched.
var migrations = []string{
	// 1: jobs, attempts, limits, queues, topics, crons.
	`
CREATE TABLE tq_jobs (
	id             bigserial PRIMARY KEY,
	project        text NOT NULL,
	queue          text NOT NULL,
	kind           text NOT NULL DEFAULT 'job',
	topic          text,
	subscription   text,
	cron           text,
	run_id         text,
	app            text NOT NULL DEFAULT '',
	path           text NOT NULL DEFAULT '',
	url            text NOT NULL DEFAULT '',
	release        text NOT NULL DEFAULT '',
	payload        jsonb NOT NULL DEFAULT 'null',
	key            text,
	group_key      text,
	dedupe         text,
	priority       smallint NOT NULL DEFAULT 2,
	state          text NOT NULL,
	attempt        int NOT NULL DEFAULT 0,
	total_attempts int NOT NULL DEFAULT 0,
	max_attempts   int NOT NULL,
	lease_s        int NOT NULL,
	seq            int NOT NULL DEFAULT 0,
	river_id       bigint,
	blocked_on     text,
	run_at         timestamptz NOT NULL DEFAULT now(),
	enqueued_at    timestamptz NOT NULL DEFAULT now(),
	started_at     timestamptz,
	lease_until    timestamptz,
	finished_at    timestamptz,
	last_error     text,
	last_status    int,
	output         jsonb,
	enqueued_by    text NOT NULL DEFAULT ''
);
CREATE INDEX tq_jobs_list ON tq_jobs (project, queue, state, id DESC);
CREATE INDEX tq_jobs_project ON tq_jobs (project, id DESC);
CREATE INDEX tq_jobs_state ON tq_jobs (project, state, id DESC);
CREATE INDEX tq_jobs_group ON tq_jobs (project, group_key, id)
	WHERE group_key IS NOT NULL AND state IN ('scheduled', 'queued', 'running', 'retrying');
CREATE INDEX tq_jobs_blocked ON tq_jobs (blocked_on, priority, id) WHERE blocked_on IS NOT NULL;
CREATE INDEX tq_jobs_running ON tq_jobs (lease_until) WHERE state = 'running';
CREATE INDEX tq_jobs_run ON tq_jobs (run_id, id) WHERE run_id IS NOT NULL;
CREATE INDEX tq_jobs_finished ON tq_jobs (finished_at) WHERE finished_at IS NOT NULL;

CREATE TABLE tq_attempts (
	job_id      bigint NOT NULL REFERENCES tq_jobs (id) ON DELETE CASCADE,
	attempt     int NOT NULL,
	project     text NOT NULL,
	queue       text NOT NULL,
	started_at  timestamptz NOT NULL,
	finished_at timestamptz NOT NULL,
	duration_ms int NOT NULL,
	outcome     text NOT NULL,
	status      int,
	error       text,
	release     text NOT NULL DEFAULT '',
	PRIMARY KEY (job_id, attempt)
);
CREATE INDEX tq_attempts_stats ON tq_attempts (project, queue, finished_at);

CREATE TABLE tq_slots (
	slot   text NOT NULL,
	job_id bigint NOT NULL,
	PRIMARY KEY (slot, job_id)
);
CREATE INDEX tq_slots_job ON tq_slots (job_id);

CREATE TABLE tq_rates (
	bucket text PRIMARY KEY,
	starts timestamptz[] NOT NULL DEFAULT '{}',
	tat    timestamptz NOT NULL
);

CREATE TABLE tq_dedupe (
	project    text NOT NULL,
	key        text NOT NULL,
	job_ids    bigint[] NOT NULL,
	expires_at timestamptz NOT NULL,
	PRIMARY KEY (project, key)
);

CREATE TABLE tq_queues (
	project         text NOT NULL,
	name            text NOT NULL,
	app             text NOT NULL DEFAULT '',
	path            text NOT NULL DEFAULT '',
	url             text NOT NULL DEFAULT '',
	concurrency     int NOT NULL DEFAULT 0,
	key_concurrency int NOT NULL DEFAULT 0,
	rate_limit      int NOT NULL DEFAULT 0,
	rate_period_s   int NOT NULL DEFAULT 0,
	max_attempts    int NOT NULL DEFAULT 0,
	lease_s         int NOT NULL DEFAULT 0,
	paused          bool NOT NULL DEFAULT false,
	updated_at      timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (project, name)
);

CREATE TABLE tq_topics (
	project    text NOT NULL,
	name       text NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (project, name)
);

CREATE TABLE tq_subscriptions (
	project    text NOT NULL,
	topic      text NOT NULL,
	name       text NOT NULL,
	app        text NOT NULL DEFAULT '',
	path       text NOT NULL DEFAULT '',
	url        text NOT NULL DEFAULT '',
	created_at timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (project, topic, name)
);

CREATE TABLE tq_crons (
	project  text NOT NULL,
	name     text NOT NULL,
	schedule text NOT NULL,
	app      text NOT NULL,
	path     text NOT NULL,
	next_at  timestamptz NOT NULL,
	last_at  timestamptz,
	last_job bigint,
	PRIMARY KEY (project, name)
);
`,
	// 2: workflows.
	`
CREATE TABLE wf_runs (
	id          text PRIMARY KEY,
	project     text NOT NULL,
	workflow    text NOT NULL,
	app         text NOT NULL DEFAULT '',
	path        text NOT NULL DEFAULT '',
	url         text NOT NULL DEFAULT '',
	release     text NOT NULL DEFAULT '',
	state       text NOT NULL,
	input       jsonb NOT NULL DEFAULT 'null',
	output      jsonb,
	error       text,
	idem        text,
	turns       int NOT NULL DEFAULT 0,
	started_by  text NOT NULL DEFAULT '',
	created_at  timestamptz NOT NULL DEFAULT now(),
	updated_at  timestamptz NOT NULL DEFAULT now(),
	finished_at timestamptz
);
CREATE UNIQUE INDEX wf_runs_idem ON wf_runs (project, workflow, idem) WHERE idem IS NOT NULL;
CREATE INDEX wf_runs_list ON wf_runs (project, created_at DESC);
CREATE INDEX wf_runs_live ON wf_runs (project, app, release) WHERE state IN ('running', 'waiting');

CREATE TABLE wf_steps (
	id          bigserial UNIQUE,
	run_id      text NOT NULL REFERENCES wf_runs (id) ON DELETE CASCADE,
	project     text NOT NULL,
	name        text NOT NULL,
	seq         int NOT NULL,
	kind        text NOT NULL,
	state       text NOT NULL,
	output      jsonb,
	error       text,
	attempts    int NOT NULL DEFAULT 0,
	started_at  timestamptz NOT NULL DEFAULT now(),
	finished_at timestamptz,
	wait_until  timestamptz,
	event       text,
	title       text,
	description text,
	human_only  bool NOT NULL DEFAULT false,
	decided_by  text,
	release     text NOT NULL DEFAULT '',
	PRIMARY KEY (run_id, name)
);
CREATE INDEX wf_steps_event ON wf_steps (project, event) WHERE state = 'waiting' AND event IS NOT NULL;
CREATE INDEX wf_steps_approvals ON wf_steps (project, state) WHERE kind = 'approval';

CREATE TABLE wf_events (
	project    text NOT NULL,
	name       text NOT NULL,
	payload    jsonb NOT NULL DEFAULT 'null',
	emitted_by text NOT NULL DEFAULT '',
	created_at timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (project, name)
);

CREATE TABLE wf_hooks (
	token   text PRIMARY KEY,
	project text NOT NULL,
	run_id  text NOT NULL,
	step    text NOT NULL
);

CREATE TABLE wf_timeline (
	id      bigserial PRIMARY KEY,
	run_id  text NOT NULL REFERENCES wf_runs (id) ON DELETE CASCADE,
	at      timestamptz NOT NULL DEFAULT now(),
	kind    text NOT NULL,
	message text NOT NULL,
	actor   text NOT NULL DEFAULT '',
	detail  jsonb
);
CREATE INDEX wf_timeline_run ON wf_timeline (run_id, id);
`,
	// 3: stopped projects: their jobs wait and their crons do not fire.
	`
CREATE TABLE tq_stopped (
	project text PRIMARY KEY,
	since   timestamptz NOT NULL DEFAULT now()
);
`,
}

// migrate brings River's schema and ours up to date. Concurrent callers are
// serialised by an advisory lock.
func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	m, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return err
	}
	if _, err := m.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("river migrations: %w", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7357002)`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS tq_schema (version int NOT NULL)`); err != nil {
		return err
	}
	var v int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(version), 0) FROM tq_schema`).Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		if _, err := tx.Exec(ctx, migrations[i]); err != nil {
			return fmt.Errorf("queue migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO tq_schema (version) VALUES ($1)`, i+1); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
