package email

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/ids"
	"github.com/btahir/tiffin/internal/platform"
)

// localQueue is the default in-process queue: queued rows live in the state
// DB and the worker picks up due ones.
type localQueue struct{ m *Module }

func (q *localQueue) Enqueue(_ context.Context, _ string, at time.Time) {
	if d := time.Until(at); d > 0 {
		time.AfterFunc(d, q.m.kick)
		return
	}
	q.m.kick()
}

func (m *Module) kick() {
	m.state()
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Module) enqueue(ctx context.Context, id string, at time.Time) {
	m.mu.Lock()
	q := m.queue
	m.mu.Unlock()
	if q == nil {
		q = &localQueue{m: m}
	}
	q.Enqueue(ctx, id, at)
}

// backoffFor is the wait before attempt n+1: 30s, 1m, 2m, ... capped at 1h.
func (m *Module) backoffFor(attempt int) time.Duration {
	if m.backoff != nil {
		return m.backoff(attempt)
	}
	d := 30 * time.Second << max(attempt-1, 0)
	if d > time.Hour || d <= 0 {
		d = time.Hour
	}
	return d
}

func (m *Module) worker(ctx context.Context, p *platform.Platform) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		m.mu.Lock()
		_, local := m.queue.(*localQueue)
		m.mu.Unlock()
		if local {
			for _, id := range dueIDs(ctx, p.DB.SQL(), time.Now()) {
				if ctx.Err() != nil {
					return
				}
				if _, err := m.deliverOne(ctx, p, id); err != nil {
					p.Log.Warn("email: delivery attempt failed", "id", id, "err", err)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-m.wake:
		}
	}
}

func dueIDs(ctx context.Context, db *sql.DB, now time.Time) []string {
	rows, err := db.QueryContext(ctx, `SELECT id FROM email_messages WHERE status = ? AND next_at <= ? ORDER BY next_at LIMIT 50`, StatusQueued, ts(now))
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out
}

// Deliver makes one relay attempt for a queued message and records the
// outcome. It returns when the next attempt is due (zero when the message
// is done: sent, failed for good, or not queued). Queue implementations
// call it for each job.
func Deliver(ctx context.Context, p *platform.Platform, id string) (time.Time, error) {
	return mod.deliverOne(ctx, p, id)
}

func (m *Module) deliverOne(ctx context.Context, p *platform.Platform, id string) (time.Time, error) {
	db := p.DB.SQL()
	rec, err := scanRecord(db.QueryRowContext(ctx, `SELECT `+cols+` FROM email_messages WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) || (err == nil && rec.Status != StatusQueued) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	attempt := rec.Attempts + 1
	fail := func(cause error, permanent bool) (time.Time, error) {
		next := time.Now().Add(m.backoffFor(attempt))
		status := StatusQueued
		if permanent || attempt >= MaxAttempts {
			status, next = StatusFailed, time.Time{}
		}
		_, err := db.ExecContext(ctx, `UPDATE email_messages SET attempts = ?, status = ?, next_at = ?, last_error = ? WHERE id = ?`,
			attempt, status, ts(next), cause.Error(), id)
		if err != nil {
			return time.Time{}, err
		}
		if s, ok := m.summary(ctx, p, rec.Project, id); ok {
			_, h := m.state()
			h.publish(rec.Project, s)
		}
		if status == StatusQueued {
			m.enqueue(ctx, id, next)
		}
		return next, cause
	}
	relay, err := getRelay(ctx, p)
	if err != nil {
		return time.Time{}, err
	}
	if relay == nil {
		return fail(errors.New("no SMTP relay is configured any more; configure one (PUT /v1/email/relay) and it will be retried"), false)
	}
	raw, err := readRaw(p.DataRoot, rec.Project, id)
	if err != nil {
		return fail(fmt.Errorf("message file is gone: %w", err), true)
	}
	pw, err := relayPassword(ctx, p)
	if err != nil {
		return time.Time{}, err
	}
	actx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	res, err := deliver(actx, relay, pw, p.Domain, rec.MailFrom, rec.Rcpt, raw)
	cancel()
	if err != nil {
		return fail(err, !isTemporary(err))
	}
	// Permanent per-recipient refusals are hard bounces: suppress them.
	var bounced []string
	for to, why := range res.Rejected {
		bounced = append(bounced, to)
		_ = addSuppression(ctx, db, rec.Project, Suppression{Address: to, Reason: "bounce", Detail: why})
	}
	sort.Strings(bounced)
	if len(res.Accepted) == 0 {
		return fail(fmt.Errorf("the relay refused every recipient (now suppressed): %s", strings.Join(bounced, ", ")), true)
	}
	note := ""
	if len(bounced) > 0 {
		note = "refused by the relay and suppressed: " + strings.Join(bounced, ", ")
	}
	if _, err := db.ExecContext(ctx, `UPDATE email_messages SET attempts = ?, status = ?, next_at = '', last_error = ?, sent_at = ?, rcpt = ? WHERE id = ?`,
		attempt, StatusSent, note, ts(time.Now()), jsonList(res.Accepted), id); err != nil {
		return time.Time{}, err
	}
	if s, ok := m.summary(ctx, p, rec.Project, id); ok {
		_, h := m.state()
		h.publish(rec.Project, s)
	}
	return time.Time{}, nil
}

func (m *Module) summary(ctx context.Context, p *platform.Platform, project, id string) (Summary, bool) {
	r, err := getRecord(ctx, p.DB.SQL(), project, id)
	if err != nil {
		return Summary{}, false
	}
	return r.Summary, true
}

// testRelay sends one test message through the configured relay now.
func testRelay(ctx context.Context, p *platform.Platform, to, from string) (*sendResult, error) {
	r, err := getRelay(ctx, p)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.New("no SMTP relay is configured")
	}
	pw, err := relayPassword(ctx, p)
	if err != nil {
		return nil, err
	}
	if from == "" {
		from = "tiffin@" + p.Domain
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	return deliver(ctx, r, pw, p.Domain, from, []string{to}, testMessage(from, to, p.Domain, ids.New("msg")))
}
