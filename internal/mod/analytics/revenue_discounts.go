package analytics

import (
	"context"
	"encoding/json"
	"net/url"
	"time"
)

// Discounts in MRR. A subscription's list price overstates what it brings
// in when a coupon takes some or all of it off (an owner's free plan, a
// launch discount). Stripe's discount object doesn't say how much (that is
// on the coupon, which needs another permission), so the box takes the
// ratio its latest invoice billed against its list price (total before tax
// over subtotal) and applies it while the subscription still has a discount
// and that discount hasn't ended. A "once" coupon leaves the subscription
// after its invoice, so the list price counts again; a repeating one ends
// on its end date. A subscription without discounts counts at list price,
// as before.

func init() {
	registerAttrSchema([]string{"subscription_discounts"},
		// factor: the share of the list price the subscription pays while
		// discounted (0 = free); ends: when the last of its discounts ends
		// (0 = no end).
		`CREATE TABLE IF NOT EXISTS subscription_discounts (
			project TEXT NOT NULL, id TEXT NOT NULL, factor REAL NOT NULL, ends INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY(project, id)) WITHOUT ROWID`,
	)
}

// stDiscountRef is a subscription's discount: an id, or the object when
// expanded (only its end matters here).
type stDiscountRef struct {
	ID  string
	End *int64
	obj bool
}

func (d *stDiscountRef) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &d.ID)
	}
	var o struct {
		ID  string `json:"id"`
		End *int64 `json:"end"`
	}
	if err := json.Unmarshal(b, &o); err != nil {
		return err
	}
	d.ID, d.End, d.obj = o.ID, o.End, true
	return nil
}

// stInvoiceTotals is what an invoice billed.
type stInvoiceTotals struct {
	Subtotal          int64  `json:"subtotal"`
	Total             int64  `json:"total"`
	TotalExcludingTax *int64 `json:"total_excluding_tax"`
}

// discount works out a subscription's discount for MRR: ok is false when
// it has none (list price). It fetches what the object doesn't carry (the
// discounts' end dates, the latest invoice).
func (m *Module) discount(ctx context.Context, sa *stripeAPI, s *stSubscription) (factor float64, ends time.Time, ok bool, err error) {
	if len(s.Discounts) == 0 {
		return 1, time.Time{}, false, nil
	}
	expanded := s.LatestInvoice.Obj != nil
	for _, d := range s.Discounts {
		expanded = expanded && d.obj
	}
	if !expanded && sa != nil {
		var full stSubscription
		if err := sa.do(ctx, "GET", "/v1/subscriptions/"+url.PathEscape(s.ID), url.Values{"expand[]": {"discounts", "latest_invoice"}}, &full); err != nil {
			return 1, time.Time{}, false, err
		}
		s = &full
	}
	var last int64
	for _, d := range s.Discounts {
		if d.End == nil { // once or forever: no end date
			last = 0
			break
		}
		last = max(last, *d.End)
	}
	if last > 0 {
		ends = time.Unix(last, 0).UTC()
	}
	factor = 1
	if in := s.LatestInvoice.Obj; in != nil && in.Subtotal > 0 {
		paid := in.Total
		if in.TotalExcludingTax != nil {
			paid = *in.TotalExcludingTax
		}
		factor = min(max(float64(paid)/float64(in.Subtotal), 0), 1)
	}
	return factor, ends, true, nil
}

func (s *attrStore) putDiscount(ctx context.Context, project, id string, factor float64, ends time.Time, ok bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ok {
		_, err := s.db.ExecContext(ctx, `DELETE FROM subscription_discounts WHERE project = ? AND id = ?`, project, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO subscription_discounts (project, id, factor, ends) VALUES (?,?,?,?)
		ON CONFLICT(project, id) DO UPDATE SET factor = excluded.factor, ends = excluded.ends`, project, id, factor, ms(ends))
	return err
}

// discounts are a project's subscriptions' discounts in force at now: the
// share of the list price each pays.
func (s *attrStore) discounts(ctx context.Context, project string, now time.Time) (map[string]float64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, factor FROM subscription_discounts WHERE project = ? AND (ends = 0 OR ends > ?)`, project, now.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var id string
		var f float64
		if err := rows.Scan(&id, &f); err != nil {
			return nil, err
		}
		out[id] = f
	}
	return out, rows.Err()
}

// refreshSubscriptions records the account's active and past-due
// subscriptions again.
func (m *Module) refreshSubscriptions(ctx context.Context, project string, sa *stripeAPI) error {
	for _, status := range []string{"active", "past_due"} {
		err := sa.list(ctx, "/v1/subscriptions", url.Values{"status": {status}, "expand[]": {"data.discounts", "data.latest_invoice"}}, func(raw json.RawMessage) error {
			var s stSubscription
			if err := json.Unmarshal(raw, &s); err != nil {
				return err
			}
			return m.recordSubscription(ctx, project, sa, &s, time.Now(), false)
		})
		if err != nil {
			return err
		}
	}
	return nil
}
