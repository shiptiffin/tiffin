package analytics

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Revenue's tables in attribution.db: they go with the project's other
// attribution data (attribution off, project deleted) and on disconnect.
func init() {
	registerAttrSchema([]string{"payments", "subscriptions", "stripe_events"},
		// One row per payment (a Stripe charge, or a revenue() call) and per
		// refund (negative, with parent the payment). amount is as charged;
		// account_amount is what reached the Stripe account (its balance
		// transaction), so sums never mix currencies. The touches are the
		// credit the match gave: first (where the account first came from)
		// and last (the visit the payment was made in, else the sign-up
		// visit); *_from says where each came from ("" = none).
		`CREATE TABLE IF NOT EXISTS payments (
			project TEXT NOT NULL, id TEXT NOT NULL, provider TEXT NOT NULL, kind TEXT NOT NULL, at INTEGER NOT NULL,
			amount INTEGER NOT NULL, currency TEXT NOT NULL, account_amount INTEGER NOT NULL, account_currency TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'succeeded', parent TEXT NOT NULL DEFAULT '', customer TEXT NOT NULL DEFAULT '',
			payment_intent TEXT NOT NULL DEFAULT '', invoice TEXT NOT NULL DEFAULT '', subscription TEXT NOT NULL DEFAULT '',
			checkout TEXT NOT NULL DEFAULT '', ref_user TEXT NOT NULL DEFAULT '', billing_reason TEXT NOT NULL DEFAULT '', renewal INTEGER NOT NULL DEFAULT 0,
			user_id TEXT NOT NULL DEFAULT '', matched_by TEXT NOT NULL DEFAULT '', unmatched TEXT NOT NULL DEFAULT '',
			first_from TEXT NOT NULL DEFAULT '', `+prefixedTouchCols("first_")+`,
			last_from TEXT NOT NULL DEFAULT '', `+prefixedTouchCols("last_")+`,
			PRIMARY KEY(project, id)) WITHOUT ROWID`,
		`CREATE INDEX IF NOT EXISTS payments_at ON payments(project, at)`,
		`CREATE INDEX IF NOT EXISTS payments_customer ON payments(project, customer, at)`,
		`CREATE INDEX IF NOT EXISTS payments_parent ON payments(project, parent)`,
		`CREATE INDEX IF NOT EXISTS payments_pi ON payments(project, payment_intent)`,
		// Stripe subscriptions, for MRR: monthly is the list price per month in
		// currency's smallest unit. updated is the newest event applied.
		`CREATE TABLE IF NOT EXISTS subscriptions (
			project TEXT NOT NULL, id TEXT NOT NULL, customer TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, currency TEXT NOT NULL,
			monthly INTEGER NOT NULL, ref_user TEXT NOT NULL DEFAULT '', started INTEGER NOT NULL DEFAULT 0, ended INTEGER NOT NULL DEFAULT 0,
			updated INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(project, id)) WITHOUT ROWID`,
		// Stripe events received: dedupe by id, and the queue the worker
		// processes (done = 1). Kept 30 days.
		`CREATE TABLE IF NOT EXISTS stripe_events (
			project TEXT NOT NULL, id TEXT NOT NULL, type TEXT NOT NULL, created INTEGER NOT NULL, received INTEGER NOT NULL,
			object BLOB NOT NULL, done INTEGER NOT NULL DEFAULT 0, attempts INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '',
			UNIQUE(project, id))`,
		`CREATE INDEX IF NOT EXISTS stripe_events_todo ON stripe_events(done)`,
	)
}

var touchFields = []string{"at", "source", "ref_host", "utm_source", "utm_medium", "utm_campaign", "landing", "country"}

func prefixedTouchCols(prefix string) string {
	cols := make([]string, len(touchFields))
	for i, f := range touchFields {
		if f == "at" {
			cols[i] = prefix + f + " INTEGER NOT NULL DEFAULT 0"
		} else {
			cols[i] = prefix + f + " TEXT NOT NULL DEFAULT ''"
		}
	}
	return strings.Join(cols, ", ")
}

// Payment kinds.
const (
	kindPayment = "payment"
	kindRenewal = "renewal"
	kindRefund  = "refund"
)

// payment is one row of payments.
type payment struct {
	ID, Provider, Kind              string
	At                              time.Time
	Amount                          int64
	Currency                        string
	AccountAmount                   int64
	AccountCurrency                 string
	Status                          string
	Parent, Customer, PaymentIntent string
	Invoice, Subscription, Checkout string
	RefUser, BillingReason          string
	Renewal                         bool // the provider says it is a repeat payment
	UserID, MatchedBy, Unmatched    string
	FirstFrom, LastFrom             string
	First, Last                     Touch
}

const paymentCols = `id, provider, kind, at, amount, currency, account_amount, account_currency, status, parent, customer, payment_intent, invoice,
	subscription, checkout, ref_user, billing_reason, renewal, user_id, matched_by, unmatched,
	first_from, first_at, first_source, first_ref_host, first_utm_source, first_utm_medium, first_utm_campaign, first_landing, first_country,
	last_from, last_at, last_source, last_ref_host, last_utm_source, last_utm_medium, last_utm_campaign, last_landing, last_country`

func touchArgs(t Touch) []any {
	return []any{ms(t.At), t.Source, t.RefHost, t.UTMSource, t.UTMMedium, t.UTMCampaign, t.Landing, t.Country}
}

func (p *payment) args(project string) []any {
	a := []any{project, p.ID, p.Provider, p.Kind, p.At.UnixMilli(), p.Amount, p.Currency, p.AccountAmount, p.AccountCurrency, p.Status, p.Parent,
		p.Customer, p.PaymentIntent, p.Invoice, p.Subscription, p.Checkout, p.RefUser, p.BillingReason, p.Renewal, p.UserID, p.MatchedBy, p.Unmatched, p.FirstFrom}
	a = append(a, touchArgs(p.First)...)
	a = append(a, p.LastFrom)
	return append(a, touchArgs(p.Last)...)
}

type scanner interface{ Scan(...any) error }

func scanPayment(r scanner) (*payment, error) {
	var p payment
	var at, fat, lat int64
	var f, l Touch
	err := r.Scan(&p.ID, &p.Provider, &p.Kind, &at, &p.Amount, &p.Currency, &p.AccountAmount, &p.AccountCurrency, &p.Status, &p.Parent, &p.Customer,
		&p.PaymentIntent, &p.Invoice, &p.Subscription, &p.Checkout, &p.RefUser, &p.BillingReason, &p.Renewal, &p.UserID, &p.MatchedBy, &p.Unmatched,
		&p.FirstFrom, &fat, &f.Source, &f.RefHost, &f.UTMSource, &f.UTMMedium, &f.UTMCampaign, &f.Landing, &f.Country,
		&p.LastFrom, &lat, &l.Source, &l.RefHost, &l.UTMSource, &l.UTMMedium, &l.UTMCampaign, &l.Landing, &l.Country)
	if err != nil {
		return nil, err
	}
	p.At, f.At, l.At = fromMS(at), fromMS(fat), fromMS(lat)
	p.First, p.Last = f, l
	return &p, nil
}

var placeholders = strings.TrimSuffix(strings.Repeat("?,", 40), ",")

func (s *attrStore) getPayment(ctx context.Context, project, id string) (*payment, error) {
	p, err := scanPayment(s.db.QueryRowContext(ctx, `SELECT `+paymentCols+` FROM payments WHERE project = ? AND id = ?`, project, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

// putPayment saves a payment as it now is. The links a payment gathers
// from several events (invoice, subscription, checkout, reference) are
// kept when the new row lacks them, and so is its match: the caller
// matches again with what the row now holds.
func (s *attrStore) putPayment(ctx context.Context, project string, p payment) (*payment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, err := s.getPayment(ctx, project, p.ID)
	if err != nil {
		return nil, err
	}
	if old != nil {
		keep := func(dst *string, v string) {
			if *dst == "" {
				*dst = v
			}
		}
		keep(&p.Parent, old.Parent)
		keep(&p.Customer, old.Customer)
		keep(&p.PaymentIntent, old.PaymentIntent)
		keep(&p.Invoice, old.Invoice)
		keep(&p.Subscription, old.Subscription)
		keep(&p.Checkout, old.Checkout)
		keep(&p.RefUser, old.RefUser)
		keep(&p.BillingReason, old.BillingReason)
		p.Renewal = p.Renewal || old.Renewal
		if p.AccountCurrency == "" {
			p.AccountAmount, p.AccountCurrency = old.AccountAmount, old.AccountCurrency
		}
		p.UserID, p.MatchedBy, p.Unmatched, p.FirstFrom, p.First, p.LastFrom, p.Last = old.UserID, old.MatchedBy, old.Unmatched, old.FirstFrom, old.First, old.LastFrom, old.Last
		if p.Kind != kindRefund {
			p.Kind = old.Kind
		}
	}
	if _, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO payments (project, `+paymentCols+`) VALUES (`+placeholders+`)`, p.args(project)...); err != nil {
		return nil, err
	}
	return &p, nil
}

// setMatch saves a payment's match and kind; its refunds inherit the match.
func (s *attrStore) setMatch(ctx context.Context, project string, p *payment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := `user_id = ?, matched_by = ?, unmatched = ?, first_from = ?, first_at = ?, first_source = ?, first_ref_host = ?, first_utm_source = ?, first_utm_medium = ?,
		first_utm_campaign = ?, first_landing = ?, first_country = ?, last_from = ?, last_at = ?, last_source = ?, last_ref_host = ?, last_utm_source = ?,
		last_utm_medium = ?, last_utm_campaign = ?, last_landing = ?, last_country = ?`
	args := []any{p.UserID, p.MatchedBy, p.Unmatched, p.FirstFrom}
	args = append(args, touchArgs(p.First)...)
	args = append(args, p.LastFrom)
	args = append(args, touchArgs(p.Last)...)
	if _, err := s.db.ExecContext(ctx, `UPDATE payments SET kind = ?, `+set+` WHERE project = ? AND id = ?`, append(append([]any{p.Kind}, args...), project, p.ID)...); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE payments SET `+set+` WHERE project = ? AND parent = ?`, append(args, project, p.ID)...)
	return err
}

// firstPayment is the earliest payment (not refund) before at by the same
// customer or the same account, other than id.
func (s *attrStore) firstPayment(ctx context.Context, project, id, customer, user string, at time.Time) (*payment, error) {
	if customer == "" && user == "" {
		return nil, nil
	}
	q := `SELECT ` + paymentCols + ` FROM payments WHERE project = ? AND id != ? AND kind != 'refund' AND at <= ? AND (`
	args := []any{project, id, at.UnixMilli()}
	var or []string
	if customer != "" {
		or = append(or, "customer = ?")
		args = append(args, customer)
	}
	if user != "" {
		or = append(or, "user_id = ?")
		args = append(args, user)
	}
	q += strings.Join(or, " OR ") + `) ORDER BY at, id LIMIT 1`
	p, err := scanPayment(s.db.QueryRowContext(ctx, q, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

func (s *attrStore) paymentsWhere(ctx context.Context, where string, args ...any) ([]*payment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+paymentCols+` FROM payments WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*payment
	for rows.Next() {
		p, err := scanPayment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// customerKey says who paid, to count customers: the account when the
// payment matched one, else the Stripe customer, else the payment itself.
func (p *payment) customerKey() string {
	switch {
	case p.UserID != "":
		return "u:" + p.UserID
	case p.Customer != "":
		return "c:" + p.Customer
	}
	return "p:" + p.ID
}

// firstPayments is when each customer (customerKey) first paid, in
// currency, with provider.
func (s *attrStore) firstPayments(ctx context.Context, project, provider, currency string) (map[string]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT CASE WHEN user_id != '' THEN 'u:' || user_id WHEN customer != '' THEN 'c:' || customer ELSE 'p:' || id END AS who, min(at)
		FROM payments WHERE project = ? AND provider = ? AND account_currency = ? AND kind != 'refund' GROUP BY who`, project, provider, currency)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var who string
		var at int64
		if err := rows.Scan(&who, &at); err != nil {
			return nil, err
		}
		out[who] = fromMS(at)
	}
	return out, rows.Err()
}

// hasPayments reports whether the project has any payment rows of provider.
func (s *attrStore) hasPayments(ctx context.Context, project, provider string) bool {
	var n int
	_ = s.db.QueryRowContext(ctx, `SELECT 1 FROM payments WHERE project = ? AND provider = ? LIMIT 1`, project, provider).Scan(&n)
	return n == 1
}

// deleteRevenue deletes a provider's payments (and, for Stripe, the
// subscriptions and events) of a project.
func (s *attrStore) deleteRevenue(ctx context.Context, project, provider string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM payments WHERE project = ? AND provider = ?`, project, provider); err != nil {
		return err
	}
	if provider != providerStripe {
		return nil
	}
	for _, t := range []string{"subscriptions", "stripe_items", "stripe_products"} {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM `+t+` WHERE project = ?`, project); err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM stripe_events WHERE project = ?`, project)
	return err
}

// ---- subscriptions ----

type subscription struct {
	ID, Customer, Status, Currency string
	Monthly                        int64
	RefUser                        string
	Started, Ended, Updated        time.Time
}

// putSubscription saves a subscription unless a newer event already did.
func (s *attrStore) putSubscription(ctx context.Context, project string, sub subscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO subscriptions (project, id, customer, status, currency, monthly, ref_user, started, ended, updated)
		VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT(project, id) DO UPDATE SET customer = excluded.customer, status = excluded.status,
		currency = excluded.currency, monthly = excluded.monthly, ref_user = CASE WHEN excluded.ref_user != '' THEN excluded.ref_user ELSE ref_user END,
		started = excluded.started, ended = excluded.ended, updated = excluded.updated WHERE excluded.updated >= subscriptions.updated`,
		project, sub.ID, sub.Customer, sub.Status, sub.Currency, sub.Monthly, sub.RefUser, ms(sub.Started), ms(sub.Ended), ms(sub.Updated))
	return err
}

// setSubscriptionRef notes the account a subscription's Checkout named
// (before or after the subscription itself arrives).
func (s *attrStore) setSubscriptionRef(ctx context.Context, project, id, user string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO subscriptions (project, id, status, currency, monthly, ref_user) VALUES (?,?,'incomplete','',0,?)
		ON CONFLICT(project, id) DO UPDATE SET ref_user = excluded.ref_user`, project, id, user)
	return err
}

func (s *attrStore) subscriptionRef(ctx context.Context, project, id string) string {
	var ref string
	_ = s.db.QueryRowContext(ctx, `SELECT ref_user FROM subscriptions WHERE project = ? AND id = ?`, project, id).Scan(&ref)
	return ref
}

// activeSubscriptions are those counted in MRR (active and past due), with
// the rate their latest payment converted at (0 when none is known).
func (s *attrStore) activeSubscriptions(ctx context.Context, project string) ([]subscription, map[string]float64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, customer, status, currency, monthly FROM subscriptions
		WHERE project = ? AND status IN ('active', 'past_due') AND monthly > 0`, project)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var subs []subscription
	for rows.Next() {
		var sub subscription
		if err := rows.Scan(&sub.ID, &sub.Customer, &sub.Status, &sub.Currency, &sub.Monthly); err != nil {
			return nil, nil, err
		}
		subs = append(subs, sub)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	rates := map[string]float64{}
	for _, sub := range subs {
		var amt, acct int64
		var cur string
		err := s.db.QueryRowContext(ctx, `SELECT amount, account_amount, account_currency FROM payments WHERE project = ? AND subscription = ? AND kind != 'refund' AND amount > 0
			ORDER BY at DESC LIMIT 1`, project, sub.ID).Scan(&amt, &acct, &cur)
		if err == nil && amt > 0 {
			rates[sub.ID+"/"+cur] = float64(acct) / float64(amt)
		}
	}
	return subs, rates, nil
}

// ---- Stripe events ----

// addEvent queues a Stripe event; false when it was received before.
func (s *attrStore) addEvent(ctx context.Context, project string, ev *stEvent, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx, `INSERT INTO stripe_events (project, id, type, created, received, object) VALUES (?,?,?,?,?,?) ON CONFLICT(project, id) DO NOTHING`,
		project, ev.ID, ev.Type, ev.Created, now.UnixMilli(), []byte(ev.Data.Object))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

type queuedEvent struct {
	rowid    int64
	project  string
	ev       stEvent
	attempts int
}

// pendingEvents are queued events not done yet, oldest first.
func (s *attrStore) pendingEvents(ctx context.Context, limit int) ([]queuedEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT rowid, project, id, type, created, object, attempts FROM stripe_events WHERE done = 0 ORDER BY rowid LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []queuedEvent
	for rows.Next() {
		var q queuedEvent
		var obj []byte
		if err := rows.Scan(&q.rowid, &q.project, &q.ev.ID, &q.ev.Type, &q.ev.Created, &obj, &q.attempts); err != nil {
			return nil, err
		}
		q.ev.Data.Object = obj
		out = append(out, q)
	}
	return out, rows.Err()
}

// eventDone counts an attempt at an event: done (errText says why it
// failed for good, "" when it worked), or to try again later.
func (s *attrStore) eventDone(ctx context.Context, rowid int64, errText string, retry bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	done := 1
	if retry {
		done = 0
	}
	// A done event keeps its id (dedupe), not its payload.
	_, err := s.db.ExecContext(ctx, `UPDATE stripe_events SET done = ?, attempts = attempts + 1, error = ?, object = CASE WHEN ? = 1 THEN x'' ELSE object END WHERE rowid = ?`,
		done, errText, done, rowid)
	return err
}

// eventStats counts a project's events: received in all, still waiting,
// failed for good, and when the last one came.
func (s *attrStore) eventStats(ctx context.Context, project string) (received, waiting, failed int64, last time.Time) {
	var lastMS sql.NullInt64
	_ = s.db.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(done = 0), 0), coalesce(sum(done = 1 AND error != ''), 0), max(received) FROM stripe_events WHERE project = ?`,
		project).Scan(&received, &waiting, &failed, &lastMS)
	if lastMS.Valid {
		last = fromMS(lastMS.Int64)
	}
	return
}

// purgeEvents forgets processed events after 30 days (Stripe retries for 3).
func (s *attrStore) purgeEvents(ctx context.Context, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM stripe_events WHERE done = 1 AND received < ?`, now.Add(-30*24*time.Hour).UnixMilli())
	return err
}
