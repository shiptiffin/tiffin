package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/manifest"
	"github.com/shiptiffin/tiffin/internal/mod/domains"
	"github.com/shiptiffin/tiffin/internal/mod/observe/edgelog"
)

// stripeNS is the box KV namespace of the Stripe connections, one per project.
const stripeNS = "analytics-stripe"

// stripeConn is a project's Stripe connection as stored: the restricted key
// and the webhook's signing secret sealed with the box's key.
type stripeConn struct {
	Project         string        `json:"project"`
	Key             []byte        `json:"key"`
	KeyHint         string        `json:"keyHint"`
	Live            bool          `json:"live"`
	EndpointID      string        `json:"endpointId"`
	Secret          []byte        `json:"secret"`
	URL             string        `json:"url"`
	AccountCurrency string        `json:"accountCurrency"`
	ConnectedAt     time.Time     `json:"connectedAt"`
	ConnectedBy     string        `json:"connectedBy"`
	Backfill        BackfillState `json:"backfill"`
}

// BackfillState is how importing the last 12 months went.
type BackfillState struct {
	State         string     `json:"state" enum:"running,done,failed" doc:"running: importing; done; failed: see error (stripe repair runs it again)"`
	Payments      int        `json:"payments" doc:"Payments imported so far"`
	Refunds       int        `json:"refunds"`
	Subscriptions int        `json:"subscriptions" doc:"Active and past-due subscriptions"`
	Error         string     `json:"error,omitempty"`
	Started       time.Time  `json:"started"`
	Finished      *time.Time `json:"finished,omitempty"`
}

func (m *Module) stripeConn(ctx context.Context, project string) (*stripeConn, bool) {
	if m.p == nil || m.p.DB == nil {
		return nil, false
	}
	b, ok, err := m.p.DB.KVGet(ctx, stripeNS, project)
	if err != nil || !ok {
		return nil, false
	}
	var c stripeConn
	if json.Unmarshal(b, &c) != nil {
		return nil, false
	}
	return &c, true
}

func (m *Module) putStripeConn(ctx context.Context, c *stripeConn) error {
	b, _ := json.Marshal(c)
	return m.p.DB.KVPut(ctx, stripeNS, c.Project, b)
}

func (m *Module) stripeClient(c *stripeConn) (*stripeAPI, error) {
	if m.p.Secrets == nil {
		return nil, errors.New("the box's secrets are not available")
	}
	key, err := m.p.Secrets.Unseal(c.Key)
	if err != nil {
		return nil, fmt.Errorf("read the stored Stripe key: %w", err)
	}
	return newStripe(string(key)), nil
}

// Tests replace these.
var (
	liveDomain     = domains.Live
	redirectTarget = domains.RedirectTarget
)

// webhookURL is where Stripe sends a project's events: /_tiffin/stripe on
// its main production address. Like an app's address: the live primary
// domain, else the first live domain of its own, else its first box name,
// of the hosts its web apps serve whole (the app named web first).
func (m *Module) webhookURL(ctx context.Context, project string) (string, error) {
	_, res, err := m.p.DB.Load(ctx, project)
	if err != nil {
		return "", err
	}
	var apps []string
	for addr := range res {
		if change.Kind(addr) == change.KindApp {
			apps = append(apps, change.Name(addr))
		}
	}
	sort.Slice(apps, func(i, j int) bool {
		if (apps[i] == "web") != (apps[j] == "web") {
			return apps[i] == "web"
		}
		return apps[i] < apps[j]
	})
	var hosts []string
	for _, name := range apps {
		var a manifest.App
		if json.Unmarshal(res[change.KindApp+"/"+name].Spec, &a) != nil || a.Role == manifest.RoleWorker {
			continue
		}
		for _, rt := range a.Routes {
			if h, prefix := edgelog.RouteHost(rt, m.p.AppsDomain()); prefix == "" {
				hosts = append(hosts, h)
			}
		}
	}
	if len(hosts) == 0 {
		return "", api.NewProblem(409, "precondition", "project "+project+" has no web app serving a whole host for Stripe to send events to")
	}
	pick := ""
	for _, h := range hosts {
		if t := redirectTarget(h); t != "" && contains(hosts, t) && liveDomain(t) {
			pick = t
			break
		}
	}
	for _, h := range hosts {
		if pick == "" && !m.p.IsBoxHost(h) && liveDomain(h) {
			pick = h
		}
	}
	if pick == "" {
		for _, h := range hosts {
			if m.p.IsBoxHost(h) {
				pick = h
				break
			}
		}
	}
	if pick == "" {
		return "", api.NewProblem(409, "precondition", "none of project "+project+"'s domains is live yet: Stripe could not reach it")
	}
	return m.p.URL(pick) + stripePath, nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

var keyRe = []string{"rk_live_", "rk_test_", "sk_live_", "sk_test_"}

func keyHint(key string) string {
	i := strings.LastIndexByte(key, '_')
	if len(key) < 12 || i < 0 {
		return "…"
	}
	return key[:i+1] + "…" + key[len(key)-4:]
}

func permissionHint() string {
	return "create a restricted key at https://dashboard.stripe.com/apikeys (Create restricted key) with " + strings.Join(stripePermissions, ", ") + ", and None for everything else"
}

func stripeProblem(err error, what string) error {
	var se *stripeError
	if errors.As(err, &se) && (se.Status == 401 || se.Status == 403 || se.Type == "invalid_request_error") {
		p := api.NewProblem(422, "validation", "Stripe refused the key "+what+": "+se.Message)
		p.Hint = permissionHint()
		return p
	}
	return api.NewProblem(502, "internal", what+": "+err.Error())
}

// connectStripe checks key, creates the webhook endpoint, stores both
// sealed and starts importing the last 12 months.
func (m *Module) connectStripe(ctx context.Context, project, key, by string) (*stripeConn, error) {
	key = strings.TrimSpace(key)
	okPrefix := false
	for _, p := range keyRe {
		okPrefix = okPrefix || strings.HasPrefix(key, p)
	}
	if !okPrefix || len(key) > 255 {
		p := api.NewProblem(422, "validation", "that is not a Stripe secret or restricted key (rk_live_..., rk_test_...)")
		p.Hint = permissionHint()
		return nil, p
	}
	target, err := m.webhookURL(ctx, project)
	if err != nil {
		return nil, err
	}
	sa := newStripe(key)
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := sa.do(cctx, http.MethodGet, "/v1/charges", url.Values{"limit": {"1"}}, nil); err != nil {
		return nil, stripeProblem(err, "reading charges")
	}
	if old, ok := m.stripeConn(ctx, project); ok { // reconnecting: the old endpoint goes
		if oa, err := m.stripeClient(old); err == nil {
			_ = oa.deleteWebhook(cctx, old.EndpointID)
		}
	}
	we, err := sa.createWebhook(cctx, project, target)
	if err != nil {
		return nil, stripeProblem(err, "creating the webhook endpoint")
	}
	sealedKey, err := m.p.Secrets.Seal([]byte(key))
	if err != nil {
		return nil, err
	}
	sealedSecret, err := m.p.Secrets.Seal([]byte(we.Secret))
	if err != nil {
		return nil, err
	}
	c := &stripeConn{Project: project, Key: sealedKey, KeyHint: keyHint(key), Live: strings.Contains(key, "_live_"), EndpointID: we.ID, Secret: sealedSecret,
		URL: target, ConnectedAt: time.Now().UTC(), ConnectedBy: by, Backfill: BackfillState{State: "running", Started: time.Now().UTC()}}
	if err := m.putStripeConn(ctx, c); err != nil {
		_ = sa.deleteWebhook(context.WithoutCancel(ctx), we.ID)
		return nil, err
	}
	m.startBackfill(project, sa)
	return c, nil
}

// disconnectStripe deletes the webhook endpoint (best effort), the stored
// key and the project's Stripe revenue. warn says what could not be done.
func (m *Module) disconnectStripe(ctx context.Context, project string) (warn string, err error) {
	c, ok := m.stripeConn(ctx, project)
	if !ok {
		return "", api.NewProblem(404, "not_found", "Stripe is not connected to project "+project)
	}
	if sa, err := m.stripeClient(c); err != nil {
		warn = err.Error()
	} else {
		dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		if err := sa.deleteWebhook(dctx, c.EndpointID); err != nil {
			warn = "the webhook endpoint " + c.EndpointID + " could not be deleted in Stripe (" + err.Error() + "): delete it at https://dashboard.stripe.com/webhooks"
		}
		cancel()
	}
	if err := m.p.DB.KVDelete(ctx, stripeNS, project); err != nil {
		return warn, err
	}
	return warn, m.attr.deleteRevenue(ctx, project, providerStripe)
}

// repairStripe points the webhook endpoint at the project's address now
// (creating it again if it was deleted in Stripe) and runs a failed import
// again.
func (m *Module) repairStripe(ctx context.Context, project string) (*stripeConn, error) {
	c, ok := m.stripeConn(ctx, project)
	if !ok {
		return nil, api.NewProblem(404, "not_found", "Stripe is not connected to project "+project)
	}
	target, err := m.webhookURL(ctx, project)
	if err != nil {
		return nil, err
	}
	sa, err := m.stripeClient(c)
	if err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err = sa.webhookURL(rctx, c.EndpointID, target)
	var se *stripeError
	if errors.As(err, &se) && se.missing() {
		we, err := sa.createWebhook(rctx, project, target)
		if err != nil {
			return nil, stripeProblem(err, "creating the webhook endpoint")
		}
		if c.Secret, err = m.p.Secrets.Seal([]byte(we.Secret)); err != nil {
			return nil, err
		}
		c.EndpointID = we.ID
	} else if err != nil {
		return nil, stripeProblem(err, "updating the webhook endpoint")
	}
	rerun := c.Backfill.State == "failed" || m.interrupted(c)
	err = m.updateStripeConn(ctx, project, func(s *stripeConn) bool {
		s.EndpointID, s.Secret, s.URL = c.EndpointID, c.Secret, target
		if rerun {
			s.Backfill = BackfillState{State: "running", Started: time.Now().UTC()}
		}
		*c = *s
		return true
	})
	if err == nil && rerun {
		m.startBackfill(project, sa)
	}
	return c, err
}

// forgetStripe drops a connection whose project is gone or has attribution
// off: the endpoint in Stripe (best effort), the key, the revenue rows.
func (m *Module) forgetStripe(ctx context.Context, c *stripeConn) {
	if sa, err := m.stripeClient(c); err == nil {
		dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		_ = sa.deleteWebhook(dctx, c.EndpointID)
		cancel()
	}
	_ = m.p.DB.KVDelete(ctx, stripeNS, c.Project)
	_ = m.attr.deleteRevenue(ctx, c.Project, providerStripe)
}

// sweepStripe checks every connection: a project that is gone or turned
// attribution off is disconnected, and a webhook whose project moved to
// another address follows it.
func (m *Module) sweepStripe(ctx context.Context) {
	all, err := m.p.DB.KVList(ctx, stripeNS)
	if err != nil || len(all) == 0 {
		return
	}
	projects, err := m.p.DB.ListProjects(ctx)
	if err != nil {
		return
	}
	for project, raw := range all {
		var c stripeConn
		if json.Unmarshal(raw, &c) != nil {
			continue
		}
		if !contains(projects, project) || !m.Settings(ctx, project).Attribution {
			m.forgetStripe(ctx, &c)
			continue
		}
		if target, err := m.webhookURL(ctx, project); err == nil && target != c.URL {
			if _, err := m.repairStripe(ctx, project); err != nil && m.p.Log != nil {
				m.p.Log.Error("analytics: move the Stripe webhook", "project", project, "err", err)
			}
		}
	}
}

// sweepSoon sweeps in the background now, after a short wait for the
// change that prompted it to settle (Reconcile, a project deleted, a change
// committed: attribution off, a project gone or moved to another address).
func (m *Module) sweepSoon() {
	if m.p == nil || m.p.DB == nil {
		return
	}
	go func() {
		time.Sleep(sweepSettle)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		m.sweepStripe(ctx)
	}()
}

// sweepSettle is how long sweepSoon waits (tests shorten it).
var sweepSettle = 5 * time.Second

// maybeSweep sweeps in the background at most every few minutes (the
// receiver calls it on events it cannot place, the API on reads).
func (m *Module) maybeSweep() {
	st := m.rev()
	st.mu.Lock()
	if time.Since(st.lastSweep) < 5*time.Minute {
		st.mu.Unlock()
		return
	}
	st.lastSweep = time.Now()
	st.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		m.sweepStripe(ctx)
	}()
}

// ---- back-fill ----

func (m *Module) startBackfill(project string, sa *stripeAPI) {
	st := m.rev()
	prog := &BackfillState{State: "running", Started: time.Now().UTC()}
	st.mu.Lock()
	st.backfills[project] = prog
	st.mu.Unlock()
	run := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		err := m.backfill(ctx, project, sa, prog)
		st.mu.Lock()
		now := time.Now().UTC()
		prog.Finished = &now
		prog.State = "done"
		if err != nil {
			prog.State, prog.Error = "failed", err.Error()
		}
		final := *prog
		delete(st.backfills, project)
		st.mu.Unlock()
		_ = m.updateStripeConn(ctx, project, func(c *stripeConn) bool {
			c.Backfill = final
			return true
		})
	}
	if stripeInline {
		run()
		return
	}
	go run()
}

// interrupted: the stored back-fill says running, but none runs (the box
// restarted during it).
func (m *Module) interrupted(c *stripeConn) bool {
	if c.Backfill.State != "running" {
		return false
	}
	_, running := m.backfillProgress(c.Project)
	return !running
}

// backfillProgress is a running back-fill's state.
func (m *Module) backfillProgress(project string) (BackfillState, bool) {
	st := m.rev()
	st.mu.Lock()
	defer st.mu.Unlock()
	if p := st.backfills[project]; p != nil {
		return *p, true
	}
	return BackfillState{}, false
}

// backfill imports the last 12 months: subscriptions in force, then the
// links Checkout Sessions and invoices give charges, then the charges
// oldest first (so each customer's first payment is known before its
// renewals), then the refunds.
func (m *Module) backfill(ctx context.Context, project string, sa *stripeAPI, prog *BackfillState) error {
	from := time.Now().AddDate(-1, 0, 0)
	st := m.rev()
	count := func(f func()) {
		st.mu.Lock()
		f()
		st.mu.Unlock()
	}
	for _, status := range []string{"active", "past_due"} {
		err := sa.list(ctx, "/v1/subscriptions", url.Values{"status": {status}}, func(raw json.RawMessage) error {
			var s stSubscription
			if err := json.Unmarshal(raw, &s); err != nil {
				return err
			}
			count(func() { prog.Subscriptions++ })
			return m.recordSubscription(ctx, project, &s, time.Now(), false)
		})
		if err != nil {
			return fmt.Errorf("subscriptions: %w", err)
		}
	}
	byPI := map[string]hints{}      // payment intent → checkout and invoice links
	byInvoice := map[string]hints{} // invoice → its subscription, reason, checkout
	err := sa.list(ctx, "/v1/checkout/sessions", mergeQ(since(from), url.Values{"status": {"complete"}}), func(raw json.RawMessage) error {
		var s stSession
		if err := json.Unmarshal(raw, &s); err != nil {
			return err
		}
		h := sessionHints(&s)
		if s.PaymentIntent.ID != "" {
			byPI[s.PaymentIntent.ID] = h
		}
		if s.Invoice.ID != "" {
			byInvoice[s.Invoice.ID] = h
		}
		if s.Subscription.ID != "" && h.RefUser != "" {
			return m.attr.setSubscriptionRef(ctx, project, s.Subscription.ID, h.RefUser)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("checkout sessions: %w", err)
	}
	err = sa.list(ctx, "/v1/invoices", mergeQ(since(from), url.Values{"status": {"paid"}}), func(raw json.RawMessage) error {
		var in stInvoice
		if err := json.Unmarshal(raw, &in); err != nil {
			return err
		}
		byInvoice[in.ID] = m.invoiceHints(ctx, project, &in, byInvoice[in.ID])
		return nil
	})
	if err != nil {
		return fmt.Errorf("invoices: %w", err)
	}
	byCharge := map[string]hints{}
	err = sa.list(ctx, "/v1/invoice_payments", mergeQ(since(from), url.Values{"status": {"paid"}}), func(raw json.RawMessage) error {
		var ip stInvoicePayment
		if err := json.Unmarshal(raw, &ip); err != nil {
			return err
		}
		h, ok := byInvoice[ip.Invoice.ID]
		if !ok {
			h = hints{Invoice: ip.Invoice.ID}
		}
		if pi := ip.Payment.PaymentIntent.ID; pi != "" {
			byPI[pi] = mergeHints(h, byPI[pi])
		}
		if ch := ip.Payment.Charge.ID; ch != "" {
			byCharge[ch] = h
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("invoice payments: %w", err)
	}
	var charges []stCharge
	err = sa.list(ctx, "/v1/charges", mergeQ(since(from), url.Values{"expand[]": {"data.balance_transaction"}}), func(raw json.RawMessage) error {
		var c stCharge
		if err := json.Unmarshal(raw, &c); err != nil {
			return err
		}
		charges = append(charges, c)
		return nil
	})
	if err != nil {
		return fmt.Errorf("charges: %w", err)
	}
	sort.SliceStable(charges, func(i, j int) bool { return charges[i].Created < charges[j].Created })
	emails := map[string]string{}
	for i := range charges {
		c := &charges[i]
		h := mergeHints(byPI[c.PaymentIntent.ID], byCharge[c.ID])
		saved, err := m.recordCharge(ctx, project, sa, c, h, emails)
		if err != nil {
			return fmt.Errorf("charge %s: %w", c.ID, err)
		}
		if saved != nil {
			count(func() { prog.Payments++ })
		}
	}
	err = sa.list(ctx, "/v1/refunds", mergeQ(since(from), url.Values{"expand[]": {"data.balance_transaction"}}), func(raw json.RawMessage) error {
		var r stRefund
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		ok, err := m.recordRefund(ctx, project, sa, &r, false)
		if ok {
			count(func() { prog.Refunds++ })
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("refunds: %w", err)
	}
	return nil
}

func mergeQ(qs ...url.Values) url.Values {
	out := url.Values{}
	for _, q := range qs {
		for k, v := range q {
			out[k] = append(out[k], v...)
		}
	}
	return out
}

// mergeHints fills a's empty fields from b.
func mergeHints(a, b hints) hints {
	fill := func(dst *string, v string) {
		if *dst == "" {
			*dst = v
		}
	}
	fill(&a.Invoice, b.Invoice)
	fill(&a.Subscription, b.Subscription)
	fill(&a.Checkout, b.Checkout)
	fill(&a.RefUser, b.RefUser)
	fill(&a.Email, b.Email)
	fill(&a.BillingReason, b.BillingReason)
	return a
}
