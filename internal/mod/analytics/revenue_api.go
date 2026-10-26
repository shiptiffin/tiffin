package analytics

import (
	"context"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// StripeStatus is a project's Stripe connection.
type StripeStatus struct {
	Project         string        `json:"project"`
	Connected       bool          `json:"connected"`
	Mode            string        `json:"mode,omitempty" enum:"live,test" doc:"Whether the key is a live or a test (sandbox) key"`
	Key             string        `json:"key,omitempty" doc:"The key's kind and last four characters (rk_live_…a1b2)"`
	WebhookID       string        `json:"webhookId,omitempty" doc:"The webhook endpoint the box created in the Stripe account"`
	WebhookURL      string        `json:"webhookUrl,omitempty" doc:"Where Stripe sends events now"`
	WantURL         string        `json:"wantUrl,omitempty" doc:"Where it should send them: /_tiffin/stripe on the project's main production address"`
	URLMismatch     bool          `json:"urlMismatch" doc:"The project's address changed since: stripe repair points the webhook at wantUrl (the box also does within minutes)"`
	AccountCurrency string        `json:"accountCurrency,omitempty" doc:"The Stripe account's currency (of its balance transactions): every sum is in it"`
	ConnectedAt     *time.Time    `json:"connectedAt,omitempty"`
	ConnectedBy     string        `json:"connectedBy,omitempty"`
	Backfill        BackfillState `json:"backfill" doc:"Importing the last 12 months of payments, refunds and subscriptions"`
	EventsReceived  int64         `json:"eventsReceived"`
	EventsWaiting   int64         `json:"eventsWaiting" doc:"Received, not processed yet (failures are tried again every minute)"`
	EventsFailed    int64         `json:"eventsFailed" doc:"Events given up on (Stripe refused a read: check the key's permissions)"`
	LastEventAt     *time.Time    `json:"lastEventAt,omitempty"`
	Permissions     []string      `json:"permissions" doc:"The restricted key's permissions, as Stripe's Create restricted key form names them"`
	Warnings        []string      `json:"warnings" doc:"Things that keep revenue from being complete"`
	Hint            string        `json:"hint,omitempty"`
}

func (m *Module) stripeStatus(ctx context.Context, project string) StripeStatus {
	out := StripeStatus{Project: project, Permissions: stripePermissions, Warnings: []string{}}
	c, ok := m.stripeConn(ctx, project)
	if !ok || !m.Settings(ctx, project).Attribution {
		if ok {
			m.maybeSweep() // attribution was turned off: it is disconnected
		}
		out.Hint = "connect with POST /v1/analytics/stripe {project, key}: " + permissionHint()
		return out
	}
	out.Connected, out.Key, out.WebhookID, out.WebhookURL, out.AccountCurrency = true, c.KeyHint, c.EndpointID, c.URL, c.AccountCurrency
	out.Mode = "test"
	if c.Live {
		out.Mode = "live"
	}
	at := c.ConnectedAt
	out.ConnectedAt, out.ConnectedBy = &at, c.ConnectedBy
	out.Backfill = c.Backfill
	if p, running := m.backfillProgress(project); running {
		out.Backfill = p
	} else if m.interrupted(c) {
		out.Backfill.State, out.Backfill.Error = "failed", "interrupted: the box restarted while importing"
	}
	if want, err := m.webhookURL(ctx, project); err != nil {
		out.Warnings = append(out.Warnings, err.Error())
	} else if out.WantURL = want; want != c.URL {
		out.URLMismatch = true
		out.Warnings = append(out.Warnings, "the webhook points at "+c.URL+", but the project is now at "+want+": run stripe repair")
		m.maybeSweep()
	}
	if len(m.p.Reach.PublicIPs) == 0 {
		out.Warnings = append(out.Warnings, "this box is not reachable from the internet, so Stripe cannot deliver events: only the import of past payments works")
	}
	if out.Backfill.State == "failed" {
		out.Warnings = append(out.Warnings, "importing past payments failed ("+out.Backfill.Error+"): check the key's permissions, then run stripe repair")
	}
	var last time.Time
	out.EventsReceived, out.EventsWaiting, out.EventsFailed, last = m.attr.eventStats(ctx, project)
	if !last.IsZero() {
		out.LastEventAt = &last
	}
	if out.EventsWaiting > 0 {
		m.kickStripe() // after a restart, the worker starts with the first look
	}
	return out
}

// ---- the report ----

// RevenueTotals are the headline revenue numbers. Amounts are in the
// report's currency, in its smallest unit (cents).
type RevenueTotals struct {
	Revenue           int64   `json:"revenue" doc:"Payments less refunds"`
	Gross             int64   `json:"gross" doc:"Payments before refunds"`
	Refunded          int64   `json:"refunded" doc:"Refunds, as a positive amount"`
	Payments          int64   `json:"payments" doc:"Payments, renewals included"`
	Renewals          int64   `json:"renewals" doc:"Payments by customers who had paid before (subscription renewals, repeat purchases)"`
	Refunds           int64   `json:"refunds"`
	Customers         int64   `json:"customers" doc:"Customers who paid"`
	NewCustomers      int64   `json:"newCustomers" doc:"Customers whose first payment was in the range"`
	MRR               int64   `json:"mrr" doc:"Monthly recurring revenue now: active and past-due subscriptions at their list price per month (trials, discounts, metered and tiered prices left out)"`
	Visitors          int64   `json:"visitors" doc:"Unique visitors in the range (counted per day)"`
	ConversionRate    float64 `json:"conversionRate" doc:"New customers / visitors, 0-1"`
	RevenuePerVisitor float64 `json:"revenuePerVisitor" doc:"Revenue / visitors, in the smallest unit"`
}

// RevenuePoint is one step of the revenue timeseries.
type RevenuePoint struct {
	T            time.Time `json:"t"`
	Revenue      int64     `json:"revenue"`
	Payments     int64     `json:"payments"`
	Refunds      int64     `json:"refunds"`
	NewCustomers int64     `json:"newCustomers"`
}

// RevenueCount is one row of a revenue breakdown.
type RevenueCount struct {
	Value     string `json:"value"`
	Revenue   int64  `json:"revenue"`
	Payments  int64  `json:"payments"`
	Customers int64  `json:"customers"`
}

// CurrencyAmount is an amount in another currency.
type CurrencyAmount struct {
	Currency string `json:"currency"`
	Amount   int64  `json:"amount"`
	Payments int64  `json:"payments"`
}

// UnmatchedPayment is a payment no account or visit was found for.
type UnmatchedPayment struct {
	ID              string    `json:"id" doc:"Stripe charge id, or app:<id> for revenue()"`
	Provider        string    `json:"provider" enum:"stripe,app"`
	Kind            string    `json:"kind" enum:"payment,renewal"`
	At              time.Time `json:"at"`
	Amount          int64     `json:"amount"`
	Currency        string    `json:"currency"`
	AccountAmount   int64     `json:"accountAmount"`
	AccountCurrency string    `json:"accountCurrency"`
	Customer        string    `json:"customer,omitempty" doc:"Stripe customer id"`
	Reason          string    `json:"reason"`
}

// RevenueReport is revenue for a range and where it came from.
type RevenueReport struct {
	Project      string             `json:"project"`
	Period       string             `json:"period"`
	From         time.Time          `json:"from"`
	To           time.Time          `json:"to"`
	Touch        string             `json:"touch" enum:"first,last" doc:"The attribution model. first: credit to where the account first came from; last: to the visit the payment was made in (else the sign-up visit)"`
	Provider     string             `json:"provider" enum:"stripe,app,none" doc:"stripe while Stripe is connected (revenue() calls are then ignored and not counted), else app"`
	Currency     string             `json:"currency" doc:"The currency of every amount: the Stripe account's, or the one most revenue() calls used"`
	Totals       RevenueTotals      `json:"totals"`
	Previous     RevenueTotals      `json:"previous" doc:"The same-length range just before"`
	Timeseries   []RevenuePoint     `json:"timeseries"`
	Granularity  string             `json:"granularity" enum:"hour,day"`
	Sources      []RevenueCount     `json:"sources" doc:"By source (Google, news.ycombinator.com, a utm_source); value \"\" is direct (no referrer, no tags)"`
	UTMSources   []RevenueCount     `json:"utmSources"`
	UTMMediums   []RevenueCount     `json:"utmMediums"`
	UTMCampaigns []RevenueCount     `json:"utmCampaigns"`
	Landing      []RevenueCount     `json:"landing" doc:"By the first page of the credited visit"`
	Countries    []RevenueCount     `json:"countries"`
	Unattributed RevenueCount       `json:"unattributed" doc:"Revenue with no visit to credit for the touch model (not matched, or matched to an account without a sign-up source); left out of the breakdowns"`
	MatchedBy    map[string]int64   `json:"matchedBy" doc:"Payments by how they were matched: reference, email, checkout, visit, customer, and none"`
	Other        []CurrencyAmount   `json:"otherCurrencies" doc:"Revenue that reached the account in another currency, left out of every sum"`
	MRROther     []CurrencyAmount   `json:"mrrOtherCurrencies" doc:"MRR of subscriptions in another currency with no payment yet to convert at, left out of mrr"`
	Unmatched    []UnmatchedPayment `json:"unmatched" doc:"The newest payments not matched, with why (revenue match tries them again)"`
	Stripe       *StripeStatus      `json:"stripe,omitempty"`
}

type revenueQuery struct {
	Project string `query:"project" required:"true" doc:"Project"`
	Period  string `query:"period" enum:"today,yesterday,24h,7d,30d,90d,12mo" doc:"Default 30d. Days are UTC. Ignored when from is set."`
	From    string `query:"from" pattern:"^\\d{4}-\\d{2}-\\d{2}$" doc:"First day, YYYY-MM-DD (UTC)"`
	To      string `query:"to" pattern:"^\\d{4}-\\d{2}-\\d{2}$" doc:"Last day, YYYY-MM-DD (UTC, inclusive; default today)"`
	Touch   string `query:"touch" enum:"first,last" doc:"Attribution model: first touch (where the account first came from) or last touch (the visit the payment was made in); default last"`
	Limit   int    `query:"limit" minimum:"1" maximum:"100" default:"10" doc:"Rows per breakdown and unmatched payments"`
}

// Revenue computes the report.
func (m *Module) Revenue(ctx context.Context, in revenueQuery, now time.Time) (*RevenueReport, error) {
	if in.Period == "" && in.From == "" {
		in.Period = "30d"
	}
	from, to, label, aligned, err := resolve(in.Period, in.From, in.To, now)
	if err != nil {
		return nil, err
	}
	if in.Touch == "" {
		in.Touch = "last"
	}
	limit := in.Limit
	if limit == 0 {
		limit = 10
	}
	r := &RevenueReport{Project: in.Project, Period: label, From: from, To: to, Touch: in.Touch, Provider: "none", MatchedBy: map[string]int64{},
		Other: []CurrencyAmount{}, MRROther: []CurrencyAmount{}, Unmatched: []UnmatchedPayment{}}
	conn, connected := m.stripeConn(ctx, in.Project)
	switch {
	case connected:
		r.Provider = providerStripe
		st := m.stripeStatus(ctx, in.Project)
		r.Stripe = &st
	case m.attr.hasPayments(ctx, in.Project, providerApp):
		r.Provider = providerApp
	}
	span := to.Sub(from)
	rows, err := m.attr.paymentsWhere(ctx, `project = ? AND provider = ? AND at >= ? AND at < ? AND status NOT IN ('failed', 'canceled') ORDER BY at`,
		in.Project, r.Provider, from.Add(-span).UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	r.Currency = reportCurrency(conn, rows)
	firsts, err := m.attr.firstPayments(ctx, in.Project, r.Provider, r.Currency)
	if err != nil {
		return nil, err
	}
	if err := m.pipe.RollupDirty(ctx); err != nil {
		return nil, err
	}
	for i, rng := range [][2]time.Time{{from, to}, {from.Add(-span), from}} {
		t := &r.Totals
		if i == 1 {
			t = &r.Previous
		}
		v, err := m.totals(ctx, Query{Project: in.Project, From: rng[0], To: rng[1]}, aligned)
		if err != nil {
			return nil, err
		}
		t.Visitors = v.Visitors
	}
	step := 24 * time.Hour
	r.Granularity = "day"
	if span <= 48*time.Hour {
		step, r.Granularity = time.Hour, "hour"
	}
	points := map[int64]*RevenuePoint{}
	for t := from.Truncate(step); t.Before(to) && !t.After(now); t = t.Add(step) {
		p := &RevenuePoint{T: t}
		points[t.Unix()] = p
		r.Timeseries = append(r.Timeseries, *p)
	}
	type agg struct {
		RevenueCount
		who map[string]bool
	}
	dims := map[string]map[string]*agg{}
	add := func(dim, value string, p *payment) {
		if dims[dim] == nil {
			dims[dim] = map[string]*agg{}
		}
		a := dims[dim][value]
		if a == nil {
			a = &agg{RevenueCount: RevenueCount{Value: value}, who: map[string]bool{}}
			dims[dim][value] = a
		}
		a.Revenue += p.AccountAmount
		if p.Kind != kindRefund {
			a.Payments++
			a.who[p.customerKey()] = true
		}
	}
	other := map[string]*CurrencyAmount{}
	who := [2]map[string]bool{{}, {}}
	counted := map[string]bool{} // customers counted new (two payments can share a first instant)
	for _, p := range rows {
		cur := p.At.Before(from)
		t := &r.Totals
		idx := 0
		if cur {
			t, idx = &r.Previous, 1
		}
		if p.AccountCurrency != r.Currency {
			if !cur {
				o := other[p.AccountCurrency]
				if o == nil {
					o = &CurrencyAmount{Currency: p.AccountCurrency}
					other[p.AccountCurrency] = o
				}
				o.Amount += p.AccountAmount
				if p.Kind != kindRefund {
					o.Payments++
				}
			}
			continue
		}
		t.Revenue += p.AccountAmount
		isNew := false
		if p.Kind == kindRefund {
			t.Refunds++
			t.Refunded -= p.AccountAmount
		} else {
			t.Payments++
			t.Gross += p.AccountAmount
			if p.Kind == kindRenewal {
				t.Renewals++
			}
			key := p.customerKey()
			who[idx][key] = true
			if f, ok := firsts[key]; ok && f.Equal(p.At) && !counted[key] {
				counted[key], isNew = true, true
				t.NewCustomers++
			}
		}
		if cur {
			continue
		}
		if pt := points[p.At.Truncate(step).Unix()]; pt != nil {
			pt.Revenue += p.AccountAmount
			if p.Kind == kindRefund {
				pt.Refunds++
			} else {
				pt.Payments++
			}
			if isNew {
				pt.NewCustomers++
			}
		}
		if p.Kind != kindRefund {
			by := p.MatchedBy
			if by == "" {
				by = "none"
			}
			r.MatchedBy[by]++
		}
		touch, src := p.Last, p.LastFrom
		if in.Touch == "first" {
			touch, src = p.First, p.FirstFrom
		}
		if src == "" {
			r.Unattributed.Revenue += p.AccountAmount
			if p.Kind != kindRefund {
				r.Unattributed.Payments++
			}
			continue
		}
		add("source", touch.Source, p)
		for dim, v := range map[string]string{"utm_source": touch.UTMSource, "utm_medium": touch.UTMMedium, "utm_campaign": touch.UTMCampaign,
			"landing": touch.Landing, "country": touch.Country} {
			if v != "" {
				add(dim, v, p)
			}
		}
	}
	for i := range r.Timeseries {
		r.Timeseries[i] = *points[r.Timeseries[i].T.Unix()]
	}
	if r.Timeseries == nil {
		r.Timeseries = []RevenuePoint{}
	}
	r.Totals.Customers, r.Previous.Customers = int64(len(who[0])), int64(len(who[1]))
	for _, t := range []*RevenueTotals{&r.Totals, &r.Previous} {
		t.ConversionRate = ratio(t.NewCustomers, t.Visitors)
		if t.Visitors > 0 {
			t.RevenuePerVisitor = math.Round(float64(t.Revenue)/float64(t.Visitors)*100) / 100
		}
	}
	for _, o := range other {
		r.Other = append(r.Other, *o)
	}
	sort.Slice(r.Other, func(i, j int) bool { return r.Other[i].Currency < r.Other[j].Currency })
	for dim, dst := range map[string]*[]RevenueCount{"source": &r.Sources, "utm_source": &r.UTMSources, "utm_medium": &r.UTMMediums,
		"utm_campaign": &r.UTMCampaigns, "landing": &r.Landing, "country": &r.Countries} {
		out := []RevenueCount{}
		for _, a := range dims[dim] {
			a.Customers = int64(len(a.who))
			out = append(out, a.RevenueCount)
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Revenue != out[j].Revenue {
				return out[i].Revenue > out[j].Revenue
			}
			return out[i].Value < out[j].Value
		})
		if len(out) > limit {
			out = out[:limit]
		}
		*dst = out
	}
	if r.Provider == providerStripe {
		if r.Totals.MRR, r.MRROther, err = m.mrr(ctx, in.Project, r.Currency); err != nil {
			return nil, err
		}
	}
	un, err := m.attr.paymentsWhere(ctx, `project = ? AND provider = ? AND kind != 'refund' AND matched_by = '' ORDER BY at DESC LIMIT ?`, in.Project, r.Provider, limit)
	if err != nil {
		return nil, err
	}
	for _, p := range un {
		r.Unmatched = append(r.Unmatched, UnmatchedPayment{ID: p.ID, Provider: p.Provider, Kind: p.Kind, At: p.At, Amount: p.Amount, Currency: p.Currency,
			AccountAmount: p.AccountAmount, AccountCurrency: p.AccountCurrency, Customer: p.Customer, Reason: p.Unmatched})
	}
	return r, nil
}

// reportCurrency is the Stripe account's currency, else the one most of
// the rows reached the account in.
func reportCurrency(conn *stripeConn, rows []*payment) string {
	if conn != nil && conn.AccountCurrency != "" {
		return conn.AccountCurrency
	}
	n := map[string]int{}
	best := ""
	for _, p := range rows {
		n[p.AccountCurrency]++
		if n[p.AccountCurrency] > n[best] || (n[p.AccountCurrency] == n[best] && p.AccountCurrency < best) {
			best = p.AccountCurrency
		}
	}
	return best
}

// mrr sums the subscriptions in force per month in currency: a
// subscription in another currency converts at its latest payment's rate.
func (m *Module) mrr(ctx context.Context, project, currency string) (int64, []CurrencyAmount, error) {
	subs, rates, err := m.attr.activeSubscriptions(ctx, project)
	if err != nil {
		return 0, nil, err
	}
	var total float64
	other := map[string]*CurrencyAmount{}
	for _, s := range subs {
		if s.Currency == currency || currency == "" {
			total += float64(s.Monthly)
			continue
		}
		if rate, ok := rates[s.ID+"/"+currency]; ok {
			total += float64(s.Monthly) * rate
			continue
		}
		o := other[s.Currency]
		if o == nil {
			o = &CurrencyAmount{Currency: s.Currency}
			other[s.Currency] = o
		}
		o.Amount += s.Monthly
		o.Payments++
	}
	out := []CurrencyAmount{}
	for _, o := range other {
		out = append(out, *o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency < out[j].Currency })
	return int64(math.Round(total)), out, nil
}

// MatchResult says what matching again did.
type MatchResult struct {
	Checked int `json:"checked" doc:"Unmatched payments tried"`
	Matched int `json:"matched" doc:"Of those, now matched"`
}

// matchAgain runs the matching chain again for unmatched payments (newest
// 500), as sign-ups and sign-in users may have come since.
func (m *Module) matchAgain(ctx context.Context, project string) (MatchResult, error) {
	var res MatchResult
	rows, err := m.attr.paymentsWhere(ctx, `project = ? AND kind != 'refund' AND matched_by = '' ORDER BY at DESC LIMIT 500`, project)
	if err != nil {
		return res, err
	}
	var sa *stripeAPI
	if c, ok := m.stripeConn(ctx, project); ok {
		if sa, err = m.stripeClient(c); err != nil {
			return res, err
		}
	}
	emails := map[string]string{}
	for _, p := range rows {
		res.Checked++
		var emailOf func(context.Context) (string, error)
		if p.Provider == providerStripe && sa != nil {
			id := p.ID
			emailOf = func(ctx context.Context) (string, error) {
				ch, err := sa.charge(ctx, id)
				if err != nil {
					return "", err
				}
				if cu := ch.Customer.ID; cu != "" {
					e, ok := emails[cu]
					if !ok {
						c, err := sa.customer(ctx, cu)
						if err != nil {
							return "", err
						}
						e, emails[cu] = c.Email, c.Email
					}
					if e != "" {
						return e, nil
					}
				}
				for _, e := range []string{ch.BillingDetails.Email, ch.ReceiptEmail} {
					if e != "" {
						return e, nil
					}
				}
				return "", nil
			}
		}
		if err := m.match(ctx, project, p, emailOf); err != nil {
			return res, err
		}
		if p.MatchedBy != "" {
			res.Matched++
		}
	}
	return res, nil
}

// ---- operations ----

func (m *Module) attributionOn(ctx context.Context, project string) error {
	if !m.Settings(ctx, project).Attribution {
		p := api.NewProblem(409, "precondition", "revenue needs marketing attribution, which is off for project "+project)
		p.Hint = "a person turns it on in the dashboard (the project's Analytics page, Attribution); then connect Stripe or call revenue()"
		return p
	}
	return nil
}

func (m *Module) registerRevenueAPI(a huma.API) {
	type projectIn struct {
		Project string `query:"project" required:"true" doc:"Project"`
	}
	stripeTag := "Stripe: a restricted key with " + strings.Join(stripePermissions, "; ") + " (None for everything else)."

	huma.Register(a, api.Op("analytics-stripe-status", http.MethodGet, "/v1/analytics/stripe", "analytics stripe status", api.RiskRead,
		"Show a project's Stripe connection",
		"Whether Stripe is connected for revenue attribution, the key's mode, the webhook endpoint and where it should point, how importing the last 12 months went, "+
			"events received and anything that keeps revenue from being complete. "+stripeTag, "analytics"),
		api.Wrap(func(ctx context.Context, in *projectIn) (*struct{ Body StripeStatus }, error) {
			if err := m.ready(ctx, in.Project); err != nil {
				return nil, err
			}
			return &struct{ Body StripeStatus }{m.stripeStatus(ctx, in.Project)}, nil
		}))

	cn := api.Outbound(api.Op("analytics-stripe-connect", http.MethodPost, "/v1/analytics/stripe", "analytics stripe connect", api.RiskWrite,
		"Connect Stripe for revenue attribution",
		"Checks the key, creates a webhook endpoint in the Stripe account pointing at /_tiffin/stripe on the project's main production address "+
			"(events: "+strings.Join(stripeEvents, ", ")+"), stores the key and the endpoint's signing secret encrypted with the box's key, "+
			"and imports the last 12 months of payments, refunds and subscriptions in the background (stripe status shows progress). "+
			"Payments are then matched to accounts and to where they came from: pass metadata.tiffin_user (the user id) on the Checkout Session and subscription for exact matches; client_reference_id counts too when it is a user id. "+
			"Needs marketing attribution on. Reconnecting replaces the key and the endpoint. "+stripeTag, "analytics"))
	cn.Errors = append(cn.Errors, 409, 422, 502)
	huma.Register(a, cn, api.Wrap(func(ctx context.Context, in *struct {
		Body struct {
			Project string `json:"project" doc:"Project"`
			Key     string `json:"key" maxLength:"255" doc:"A Stripe restricted key (rk_live_... or rk_test_...) with the permissions above"`
		}
	}) (*struct{ Body StripeStatus }, error) {
		project := in.Body.Project
		if err := m.ready(ctx, project); err != nil {
			return nil, err
		}
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeApplyReversible, project); err != nil {
			return nil, err
		}
		if err := m.attributionOn(ctx, project); err != nil {
			return nil, err
		}
		by := "unknown"
		if pr := api.PrincipalFrom(ctx); pr != nil {
			by = pr.Name
			_ = m.p.DB.Audit(ctx, pr.TokenID, "analytics.stripe_connect", project, map[string]any{"key": keyHint(in.Body.Key)})
		}
		if _, err := m.connectStripe(ctx, project, in.Body.Key, by); err != nil {
			return nil, err
		}
		return &struct{ Body StripeStatus }{m.stripeStatus(ctx, project)}, nil
	}))

	rp := api.Outbound(api.Op("analytics-stripe-repair", http.MethodPost, "/v1/analytics/stripe/repair", "analytics stripe repair", api.RiskWrite,
		"Point the Stripe webhook at the project's address again",
		"Updates the webhook endpoint to /_tiffin/stripe on the project's main production address now (after a domain change; the box also does it within minutes), "+
			"creates it again if it was deleted in Stripe, and runs a failed import of past payments again.", "analytics"))
	rp.Errors = append(rp.Errors, 404, 409, 422, 502)
	huma.Register(a, rp, api.Wrap(func(ctx context.Context, in *struct {
		Body struct {
			Project string `json:"project" doc:"Project"`
		}
	}) (*struct{ Body StripeStatus }, error) {
		project := in.Body.Project
		if err := m.ready(ctx, project); err != nil {
			return nil, err
		}
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeApplyReversible, project); err != nil {
			return nil, err
		}
		if err := m.attributionOn(ctx, project); err != nil {
			return nil, err
		}
		if _, err := m.repairStripe(ctx, project); err != nil {
			return nil, err
		}
		return &struct{ Body StripeStatus }{m.stripeStatus(ctx, project)}, nil
	}))

	dc := api.Op("analytics-stripe-disconnect", http.MethodDelete, "/v1/analytics/stripe", "analytics stripe disconnect", api.RiskDestructive,
		"Disconnect Stripe and delete its revenue",
		"Deletes the webhook endpoint in the Stripe account (best effort: a warning says if it could not), the stored key, and every payment, refund and subscription "+
			"imported from Stripe for the project. revenue() calls count again afterwards. Connecting again imports the last 12 months anew.", "analytics")
	dc.Errors = append(dc.Errors, 404)
	huma.Register(a, dc, api.Wrap(func(ctx context.Context, in *projectIn) (*struct {
		Body struct {
			Project string `json:"project"`
			Warning string `json:"warning,omitempty"`
		}
	}, error) {
		if err := m.ready(ctx, in.Project); err != nil {
			return nil, err
		}
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeApplyIrreversible, in.Project); err != nil {
			return nil, err
		}
		warn, err := m.disconnectStripe(ctx, in.Project)
		if err != nil {
			return nil, err
		}
		if pr := api.PrincipalFrom(ctx); pr != nil {
			_ = m.p.DB.Audit(ctx, pr.TokenID, "analytics.stripe_disconnect", in.Project, map[string]any{"warning": warn})
		}
		out := &struct {
			Body struct {
				Project string `json:"project"`
				Warning string `json:"warning,omitempty"`
			}
		}{}
		out.Body.Project, out.Body.Warning = in.Project, warn
		return out, nil
	}))

	huma.Register(a, api.Untrusted(api.Op("analytics-revenue", http.MethodGet, "/v1/analytics/revenue", "analytics revenue", api.RiskRead,
		"Show revenue and where it came from",
		"Revenue (payments less refunds, in the Stripe account's currency), payments, renewals, refunds, customers, new customers, MRR, conversion rate (new customers / visitors) "+
			"and revenue per visitor over a period, with the period before, a timeseries, and revenue by source, UTM source, medium and campaign, landing page and country "+
			"under the first- or last-touch model. Plus the payments not matched to an account or visit, with why. Needs marketing attribution on; "+
			"payments come from Stripe (stripe connect) or from revenue() in @shiptiffin/sdk/analytics."+untrusted, "analytics")),
		api.Wrap(func(ctx context.Context, in *revenueQuery) (*struct{ Body *RevenueReport }, error) {
			if err := m.ready(ctx, in.Project); err != nil {
				return nil, err
			}
			if err := m.enabledHint(ctx, in.Project); err != nil {
				return nil, err
			}
			if err := m.attributionOn(ctx, in.Project); err != nil {
				return nil, err
			}
			r, err := m.Revenue(ctx, *in, time.Now())
			if err != nil {
				return nil, err
			}
			return &struct{ Body *RevenueReport }{r}, nil
		}))

	mt := api.Outbound(api.Op("analytics-revenue-match", http.MethodPost, "/v1/analytics/revenue/match", "analytics revenue match", api.RiskWrite,
		"Match unmatched payments again",
		"Runs the matching chain again (the account the payment names, the customer's email among sign-in users, the checkout return visit, an earlier payment by the same customer) "+
			"for the newest 500 payments not matched yet: accounts may have signed up since.", "analytics"))
	huma.Register(a, mt, api.Wrap(func(ctx context.Context, in *struct {
		Body struct {
			Project string `json:"project" doc:"Project"`
		}
	}) (*struct{ Body MatchResult }, error) {
		project := in.Body.Project
		if err := m.ready(ctx, project); err != nil {
			return nil, err
		}
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeApplyReversible, project); err != nil {
			return nil, err
		}
		if err := m.attributionOn(ctx, project); err != nil {
			return nil, err
		}
		res, err := m.matchAgain(ctx, project)
		if err != nil {
			return nil, err
		}
		return &struct{ Body MatchResult }{res}, nil
	}))
}
