package analytics

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// stripeTolerance is how old a signed event may be (Stripe's libraries
// default to 5 minutes): an old signature replayed is refused.
const stripeTolerance = 5 * time.Minute

var (
	errNoSignature  = errors.New("the Stripe-Signature header has no t= timestamp or v1= signature")
	errBadSignature = errors.New("no v1 signature matches: wrong signing secret, or the body was changed")
	errStale        = errors.New("the signature's timestamp is more than 5 minutes from now (a replay?)")
)

// verifyStripe checks a Stripe-Signature header: t=<unix>,v1=<hex>[,v1=...]
// where v1 is HMAC-SHA256(secret, "<t>.<body>"). Any v1 may match (Stripe
// signs with each secret while a rolled one is still valid).
func verifyStripe(body []byte, header, secret string, now time.Time) error {
	var ts string
	var sigs []string
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			ts = v
		case "v1":
			sigs = append(sigs, v)
		}
	}
	t, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || len(sigs) == 0 {
		return errNoSignature
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte{'.'})
	mac.Write(body)
	want := []byte(hex.EncodeToString(mac.Sum(nil)))
	match := false
	for _, s := range sigs {
		if hmac.Equal([]byte(s), want) {
			match = true
		}
	}
	if !match {
		return errBadSignature
	}
	if d := now.Sub(time.Unix(t, 0)); d > stripeTolerance || d < -stripeTolerance {
		return errStale
	}
	return nil
}

// stripeInline processes events and back-fills in the request (tests).
var stripeInline = false

const maxStripeBody = 1 << 20

// stripeWebhook receives a project's Stripe events at /_tiffin/stripe on its
// app hosts: it checks the signature with that project's secret, queues the
// event once (by id) and answers at once; the worker does the rest.
func (m *Module) stripeWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		reply(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only: Stripe sends events here"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxStripeBody+1))
	if err != nil || len(body) > maxStripeBody {
		reply(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "body too large"})
		return
	}
	ctx := r.Context()
	site, ok := m.sites.Lookup(ctx, r.Host, stripePath)
	if !ok {
		m.maybeSweep()
		reply(w, http.StatusNotFound, map[string]string{"error": "no project serves " + r.Host})
		return
	}
	c, ok := m.stripeConn(ctx, site.Project)
	if !ok {
		m.maybeSweep()
		reply(w, http.StatusNotFound, map[string]string{"error": "Stripe is not connected to this project"})
		return
	}
	secret, err := m.p.Secrets.Unseal(c.Secret)
	if err != nil {
		reply(w, http.StatusInternalServerError, map[string]string{"error": "the signing secret could not be read"})
		return
	}
	if err := verifyStripe(body, r.Header.Get("Stripe-Signature"), string(secret), time.Now()); err != nil {
		reply(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if !m.Settings(ctx, site.Project).Attribution {
		m.maybeSweep()
		reply(w, http.StatusOK, map[string]string{"ignored": "attribution is off for this project"})
		return
	}
	var ev stEvent
	if err := json.Unmarshal(body, &ev); err != nil || ev.ID == "" || ev.Type == "" {
		reply(w, http.StatusBadRequest, map[string]string{"error": "not a Stripe event"})
		return
	}
	if !contains(stripeEvents, ev.Type) {
		reply(w, http.StatusOK, map[string]string{"ignored": ev.Type})
		return
	}
	fresh, err := m.attr.addEvent(ctx, site.Project, &ev, time.Now())
	if err != nil {
		reply(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !fresh {
		reply(w, http.StatusOK, map[string]bool{"duplicate": true})
		return
	}
	m.kickStripe()
	reply(w, http.StatusOK, map[string]bool{"received": true})
}

// kickStripe has the worker look at the queue (in tests: works it now).
func (m *Module) kickStripe() {
	if stripeInline {
		m.drainStripe(context.Background())
		return
	}
	st := m.rev()
	st.worker.Do(func() {
		go func() {
			for {
				m.drainStripe(context.Background())
				select {
				case <-st.wake:
				case <-time.After(time.Minute):
				}
			}
		}()
	})
	select {
	case st.wake <- struct{}{}:
	default:
	}
}

const maxEventAttempts = 6

// drainStripe processes the queued events, oldest first. A failure is tried
// again on the next pass (a minute later), up to maxEventAttempts times.
func (m *Module) drainStripe(ctx context.Context) {
	st := m.rev()
	if time.Since(st.purged) > time.Hour {
		st.purged = time.Now()
		_ = m.attr.purgeEvents(ctx, time.Now())
	}
	seen := map[int64]bool{}
	for {
		evs, err := m.attr.pendingEvents(ctx, 50)
		if err != nil || len(evs) == 0 {
			return
		}
		progress := false
		for _, q := range evs {
			if seen[q.rowid] {
				continue
			}
			seen[q.rowid], progress = true, true
			ectx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			err := m.processEvent(ectx, q.project, &q.ev)
			cancel()
			switch {
			case err == nil:
				_ = m.attr.eventDone(ctx, q.rowid, "", false)
			case q.attempts+1 >= maxEventAttempts || permanent(err):
				_ = m.attr.eventDone(ctx, q.rowid, err.Error(), false)
				if m.p != nil && m.p.Log != nil {
					m.p.Log.Error("analytics: Stripe event", "project", q.project, "event", q.ev.ID, "err", err)
				}
			default:
				_ = m.attr.eventDone(ctx, q.rowid, err.Error(), true)
			}
		}
		if !progress {
			return
		}
	}
}

// permanent: Stripe refused the request for good (a missing permission, an
// object that does not exist); trying again would not help.
func permanent(err error) bool {
	var se *stripeError
	return errors.As(err, &se) && se.Status >= 400 && se.Status < 500 && se.Status != http.StatusTooManyRequests
}

// processEvent applies one event.
func (m *Module) processEvent(ctx context.Context, project string, ev *stEvent) error {
	c, ok := m.stripeConn(ctx, project)
	if !ok || !m.Settings(ctx, project).Attribution {
		return nil
	}
	sa, err := m.stripeClient(c)
	if err != nil {
		return err
	}
	obj := ev.Data.Object
	switch {
	case strings.HasPrefix(ev.Type, "charge."):
		var o struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(obj, &o); err != nil {
			return err
		}
		ch, err := sa.charge(ctx, o.ID)
		if err != nil {
			return err
		}
		_, err = m.recordCharge(ctx, project, sa, ch, hints{}, nil)
		return err
	case strings.HasPrefix(ev.Type, "checkout.session."):
		var s stSession
		if err := json.Unmarshal(obj, &s); err != nil {
			return err
		}
		return m.recordSession(ctx, project, sa, &s)
	case ev.Type == "invoice.paid":
		var in stInvoice
		if err := json.Unmarshal(obj, &in); err != nil {
			return err
		}
		return m.recordInvoice(ctx, project, sa, &in, hints{})
	case strings.HasPrefix(ev.Type, "customer.subscription."):
		var s stSubscription
		if err := json.Unmarshal(obj, &s); err != nil {
			return err
		}
		return m.recordSubscription(ctx, project, &s, time.Unix(ev.Created, 0), ev.Type == "customer.subscription.deleted")
	}
	return nil
}

func sessionHints(s *stSession) hints {
	// metadata.tiffin_user names the account for analytics alone; an app's
	// client_reference_id may be its own id (an order, a box), so it counts
	// only when it is an account (match checks).
	h := hints{Checkout: s.ID, RefUser: s.Metadata["tiffin_user"], Invoice: s.Invoice.ID, Subscription: s.Subscription.ID}
	if h.RefUser == "" {
		h.RefUser = s.ClientReferenceID
	}
	if s.CustomerDetails != nil {
		h.Email = s.CustomerDetails.Email
	}
	return h
}

// invoiceHints adds what an invoice says (its subscription and why it was
// made, the account its subscription names) to h.
func (m *Module) invoiceHints(ctx context.Context, project string, in *stInvoice, h hints) hints {
	sub, meta := in.subscription()
	ref := meta["tiffin_user"]
	if ref == "" {
		ref = in.Metadata["tiffin_user"]
	}
	if ref == "" && sub != "" {
		ref = m.attr.subscriptionRef(ctx, project, sub)
	}
	return mergeHints(h, hints{Invoice: in.ID, Subscription: sub, BillingReason: in.BillingReason, RefUser: ref, Email: in.CustomerEmail})
}

// recordSession: a completed Checkout Session links its payment to the
// buyer's return visit and to the account it names.
func (m *Module) recordSession(ctx context.Context, project string, sa *stripeAPI, s *stSession) error {
	h := sessionHints(s)
	if s.Subscription.ID != "" && h.RefUser != "" {
		if err := m.attr.setSubscriptionRef(ctx, project, s.Subscription.ID, h.RefUser); err != nil {
			return err
		}
	}
	if s.PaymentStatus != "paid" { // a trial, or a delayed method not paid yet
		return nil
	}
	if s.PaymentIntent.ID != "" {
		charges, err := sa.chargesOf(ctx, s.PaymentIntent.ID)
		if err != nil {
			return err
		}
		for i := range charges {
			if _, err := m.recordCharge(ctx, project, sa, &charges[i], h, nil); err != nil {
				return err
			}
		}
	}
	if s.Invoice.ID != "" {
		in, err := sa.invoice(ctx, s.Invoice.ID)
		if err != nil {
			return err
		}
		return m.recordInvoice(ctx, project, sa, in, h)
	}
	return nil
}

// recordInvoice records a paid invoice's charges (through its payments).
func (m *Module) recordInvoice(ctx context.Context, project string, sa *stripeAPI, in *stInvoice, h hints) error {
	h = m.invoiceHints(ctx, project, in, h)
	pays, err := sa.invoicePayments(ctx, in.ID)
	if err != nil {
		return err
	}
	for _, ip := range pays {
		var charges []stCharge
		switch {
		case ip.Payment.PaymentIntent.ID != "":
			if charges, err = sa.chargesOf(ctx, ip.Payment.PaymentIntent.ID); err != nil {
				return err
			}
		case ip.Payment.Charge.ID != "":
			ch, err := sa.charge(ctx, ip.Payment.Charge.ID)
			if err != nil {
				return err
			}
			charges = []stCharge{*ch}
		}
		for i := range charges {
			if _, err := m.recordCharge(ctx, project, sa, &charges[i], h, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// recordCharge saves a succeeded charge as a payment, matches it, and
// records its refunds. emails caches customers' emails (nil: no cache).
func (m *Module) recordCharge(ctx context.Context, project string, sa *stripeAPI, c *stCharge, h hints, emails map[string]string) (*payment, error) {
	if !c.Paid || c.Status != "succeeded" || !c.Captured {
		return nil, nil
	}
	amount := c.AmountCaptured
	if amount == 0 {
		amount = c.Amount
	}
	p := payment{ID: c.ID, Provider: providerStripe, Kind: kindPayment, At: time.Unix(c.Created, 0).UTC(), Amount: amount, Currency: c.Currency,
		AccountAmount: amount, AccountCurrency: c.Currency, Status: "succeeded", Customer: c.Customer.ID, PaymentIntent: c.PaymentIntent.ID,
		Invoice: h.Invoice, Subscription: h.Subscription, Checkout: h.Checkout, RefUser: h.RefUser, BillingReason: h.BillingReason}
	if p.RefUser == "" {
		p.RefUser = c.Metadata["tiffin_user"]
	}
	bt := c.BalanceTransaction.Obj
	if bt == nil && c.BalanceTransaction.ID != "" {
		var err error
		if bt, err = sa.balanceTxn(ctx, c.BalanceTransaction.ID); err != nil {
			return nil, err
		}
	}
	if bt != nil {
		// What reached the account, in its currency (made at capture, for
		// the amount captured).
		p.AccountAmount, p.AccountCurrency = bt.Amount, bt.Currency
		m.noteAccountCurrency(ctx, project, bt.Currency)
	}
	saved, err := m.attr.putPayment(ctx, project, p)
	if err != nil {
		return nil, err
	}
	emailOf := func(ctx context.Context) (string, error) {
		if id := c.Customer.ID; id != "" {
			e, ok := emails[id]
			if !ok {
				cu := c.Customer.Obj
				if cu == nil {
					var err error
					if cu, err = sa.customer(ctx, id); err != nil {
						return "", err
					}
				}
				e = cu.Email
				if emails != nil {
					emails[id] = e
				}
			}
			if e != "" {
				return e, nil
			}
		}
		for _, e := range []string{h.Email, c.BillingDetails.Email, c.ReceiptEmail} {
			if e != "" {
				return e, nil
			}
		}
		return "", nil
	}
	if err := m.match(ctx, project, saved, emailOf); err != nil {
		return nil, err
	}
	if c.AmountRefunded > 0 && emails == nil { // the back-fill lists refunds itself
		refunds, err := sa.refundsOf(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		for i := range refunds {
			if _, err := m.recordRefund(ctx, project, sa, &refunds[i], true); err != nil {
				return nil, err
			}
		}
	}
	return saved, nil
}

// recordRefund saves a refund as a negative payment with its charge's
// match. fetch: record the charge first when it is not known yet.
func (m *Module) recordRefund(ctx context.Context, project string, sa *stripeAPI, r *stRefund, fetch bool) (bool, error) {
	parent, err := m.attr.getPayment(ctx, project, r.Charge.ID)
	if err != nil {
		return false, err
	}
	if parent == nil && fetch && r.Charge.ID != "" {
		ch, err := sa.charge(ctx, r.Charge.ID)
		if err != nil {
			return false, err
		}
		if parent, err = m.recordCharge(ctx, project, sa, ch, hints{}, map[string]string{}); err != nil {
			return false, err
		}
	}
	if parent == nil { // a refund of a payment from before the import
		return false, nil
	}
	p := payment{ID: r.ID, Provider: providerStripe, Kind: kindRefund, At: time.Unix(r.Created, 0).UTC(), Amount: -r.Amount, Currency: r.Currency,
		AccountAmount: -r.Amount, AccountCurrency: r.Currency, Status: r.Status, Parent: parent.ID, Customer: parent.Customer, Subscription: parent.Subscription}
	bt := r.BalanceTransaction.Obj
	if bt == nil && r.BalanceTransaction.ID != "" {
		if bt, err = sa.balanceTxn(ctx, r.BalanceTransaction.ID); err != nil {
			return false, err
		}
	}
	if bt != nil {
		p.AccountAmount, p.AccountCurrency = bt.Amount, bt.Currency
	} else if r.Currency == parent.Currency && parent.Amount != 0 { // pending: at the payment's rate
		p.AccountAmount, p.AccountCurrency = -r.Amount*parent.AccountAmount/parent.Amount, parent.AccountCurrency
	}
	saved, err := m.attr.putPayment(ctx, project, p)
	if err != nil {
		return false, err
	}
	return true, m.inherit(ctx, project, saved)
}

// recordSubscription keeps a subscription's state for MRR.
func (m *Module) recordSubscription(ctx context.Context, project string, s *stSubscription, at time.Time, deleted bool) error {
	sub := subscription{ID: s.ID, Customer: s.Customer.ID, Status: s.Status, Currency: s.Currency, Monthly: s.monthly(),
		RefUser: s.Metadata["tiffin_user"], Updated: at}
	if deleted && sub.Status == "" {
		sub.Status = "canceled"
	}
	if s.StartDate > 0 {
		sub.Started = time.Unix(s.StartDate, 0)
	}
	if s.EndedAt > 0 {
		sub.Ended = time.Unix(s.EndedAt, 0)
	}
	return m.attr.putSubscription(ctx, project, sub)
}

// noteAccountCurrency remembers the Stripe account's currency (that of its
// balance transactions) the first time one is seen.
func (m *Module) noteAccountCurrency(ctx context.Context, project, cur string) {
	_ = m.updateStripeConn(ctx, project, func(c *stripeConn) bool {
		if c.AccountCurrency != "" {
			return false
		}
		c.AccountCurrency = cur
		return true
	})
}

// updateStripeConn changes a stored connection, one change at a time;
// change reports whether it changed anything.
func (m *Module) updateStripeConn(ctx context.Context, project string, change func(*stripeConn) bool) error {
	st := m.rev()
	st.connMu.Lock()
	defer st.connMu.Unlock()
	c, ok := m.stripeConn(ctx, project)
	if !ok || !change(c) {
		return nil
	}
	return m.putStripeConn(ctx, c)
}
