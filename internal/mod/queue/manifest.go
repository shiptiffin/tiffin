package queue

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/shiptiffin/tiffin/internal/manifest"
)

// This file reconciles the manifest's queues and topics onto the engine. The
// manifest owns a declared queue's target and limits: every converge writes
// them back, so a change made with `queue configure` is undone by the next
// apply (pause state is the exception and is kept).

// ReconcileQueue makes a queue's stored configuration match the manifest
// (spec nil = deleted: its waiting and dead jobs are purged and the
// configuration removed). It is idempotent.
func (e *Engine) ReconcileQueue(ctx context.Context, project, name string, spec json.RawMessage) error {
	if spec == nil {
		return e.removeQueue(ctx, project, name)
	}
	var q manifest.Queue
	if err := json.Unmarshal(spec, &q); err != nil {
		return fmt.Errorf("queue %s: %w", name, err)
	}
	if q.App == "" && q.URL == "" {
		return fmt.Errorf("queue %s: no app or url", name)
	}
	if q.Path == "" && q.App != "" {
		q.Path = manifest.DefaultQueuePathPrefix + name
	}
	if q.RateLimit > 0 && q.RatePeriodSeconds == 0 {
		q.RatePeriodSeconds = manifest.DefaultRatePeriodSecs
	}
	if q.MaxAttempts == 0 {
		q.MaxAttempts = manifest.DefaultMaxAttempts
	}
	if q.LeaseSeconds == 0 {
		q.LeaseSeconds = manifest.DefaultLeaseSeconds
	}
	have, err := e.queueConfig(ctx, e.pool, project, name)
	if err != nil {
		return err
	}
	want := QueueConfig{Name: name, App: q.App, Path: q.Path, URL: q.URL, Concurrency: q.Concurrency, KeyConcurrency: q.KeyConcurrency,
		RateLimit: q.RateLimit, RatePeriodS: q.RatePeriodSeconds, MaxAttempts: q.MaxAttempts, LeaseS: q.LeaseSeconds,
		Paused: have.Paused, Configured: true}
	if have == want {
		return nil
	}
	_, err = e.ConfigureQueue(ctx, project, want)
	return err
}

// removeQueue deletes a queue's configuration and every job still waiting
// (scheduled, queued, retrying) or dead in it. Running jobs finish.
func (e *Engine) removeQueue(ctx context.Context, project, name string) error {
	// Purge only deletes what it counted, so retry if jobs arrived in between.
	for range 5 {
		res, err := e.Purge(ctx, project, name, true, "")
		if err != nil {
			return err
		}
		if res, err = e.Purge(ctx, project, name, true, res.Confirm); err != nil {
			return err
		}
		if res.Purged {
			break
		}
	}
	_, err := e.pool.Exec(ctx, `DELETE FROM tq_queues WHERE project = $1 AND name = $2`, project, name)
	return err
}

// ReconcileTopic makes a topic's subscribers match the manifest (spec nil =
// deleted: every subscriber is removed). A subscriber is a queue of the same
// project: messages go to that queue's app and path. Subscribers not listed
// are removed, so the manifest is the whole truth about a declared topic. The
// subscribed queues must already be configured (queues reconcile first).
func (e *Engine) ReconcileTopic(ctx context.Context, project, name string, spec json.RawMessage) error {
	if spec == nil {
		_, err := e.pool.Exec(ctx, `DELETE FROM tq_subscriptions WHERE project = $1 AND topic = $2`, project, name)
		return err
	}
	var t manifest.Topic
	if err := json.Unmarshal(spec, &t); err != nil {
		return fmt.Errorf("topic %s: %w", name, err)
	}
	if !nameRE.MatchString(name) {
		return invalid(fmt.Sprintf("topic %q: names are 1-64 lowercase letters, digits, dots, dashes or underscores", name), "")
	}
	// The topic exists even with no subscribers, so sends to it are not
	// mistaken for a queue.
	if _, err := e.pool.Exec(ctx, `INSERT INTO tq_topics (project, name) VALUES ($1, $2) ON CONFLICT DO NOTHING`, project, name); err != nil {
		return err
	}
	keep := make([]string, 0, len(t.Subscribers))
	for _, sub := range t.Subscribers {
		cfg, err := e.queueConfig(ctx, e.pool, project, sub)
		if err != nil {
			return err
		}
		if !cfg.Configured || (cfg.App == "" && cfg.URL == "") {
			return fmt.Errorf("topic %s: subscriber queue %q is not configured yet; it is set up by the same apply, so this retries", name, sub)
		}
		path := cfg.Path
		if path == "" && cfg.App != "" {
			path = manifest.DefaultQueuePathPrefix + sub
		}
		if _, err := e.Subscribe(ctx, project, name, Subscription{Name: sub, App: cfg.App, Path: path, URL: cfg.URL}); err != nil {
			return err
		}
		keep = append(keep, sub)
	}
	_, err := e.pool.Exec(ctx, `DELETE FROM tq_subscriptions WHERE project = $1 AND topic = $2 AND NOT (name = ANY($3))`, project, name, keep)
	return err
}
