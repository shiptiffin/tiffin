package analytics

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/state"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// ---- a fake Stripe ----

// fakeStripe answers the Stripe API calls revenue makes from objects in
// the current documented shapes (API version stripeVersion).
type fakeStripe struct {
	mu        sync.Mutex
	t         *testing.T
	key       string
	objects   map[string]map[string]any // id → object
	endpoints map[string]map[string]any
	calls     []string
	deny      map[string]bool // paths refused with a permission error
}

func newFakeStripe(t *testing.T, key string) *fakeStripe {
	f := &fakeStripe{t: t, key: key, objects: map[string]map[string]any{}, endpoints: map[string]map[string]any{}, deny: map[string]bool{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	old := stripeAPIBase
	stripeAPIBase = srv.URL
	t.Cleanup(func() { stripeAPIBase = old })
	return f
}

func (f *fakeStripe) put(objs ...map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, o := range objs {
		f.objects[o["id"].(string)] = o
	}
}

func (f *fakeStripe) ofType(object string) []map[string]any {
	var out []map[string]any
	for _, o := range f.objects {
		if o["object"] == object {
			out = append(out, o)
		}
	}
	// Newest first, like Stripe.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && created(out[j]) > created(out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func created(o map[string]any) int64 { n, _ := o["created"].(int64); return n }

// expand returns o with the expandable fields named in expand embedded.
func (f *fakeStripe) expand(o map[string]any, expand []string) map[string]any {
	out := map[string]any{}
	for k, v := range o {
		out[k] = v
	}
	for _, e := range expand {
		e = strings.TrimPrefix(e, "data.")
		if id, ok := out[e].(string); ok && f.objects[id] != nil {
			out[e] = f.objects[id]
		}
	}
	return out
}

func (f *fakeStripe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	if r.Header.Get("Stripe-Version") != stripeVersion {
		f.t.Errorf("%s %s without Stripe-Version", r.Method, r.URL.Path)
	}
	fail := func(status int, typ, msg string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": typ, "message": msg}})
	}
	if r.Header.Get("Authorization") != "Bearer "+f.key {
		fail(401, "invalid_request_error", "Invalid API Key provided")
		return
	}
	if f.deny[r.URL.Path] {
		fail(403, "invalid_request_error", "The provided key does not have the required permissions for this endpoint")
		return
	}
	_ = r.ParseForm()
	q := r.Form
	send := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	list := func(object string, keep func(map[string]any) bool) {
		data := []any{}
		for _, o := range f.ofType(object) {
			if keep == nil || keep(o) {
				data = append(data, f.expand(o, q["expand[]"]))
			}
		}
		send(map[string]any{"object": "list", "data": data, "has_more": false})
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/"), "/")
	switch {
	case r.URL.Path == "/v1/webhook_endpoints" && r.Method == http.MethodPost:
		id := fmt.Sprintf("we_%d", len(f.endpoints)+1)
		we := map[string]any{"id": id, "object": "webhook_endpoint", "url": q.Get("url"), "enabled_events": q["enabled_events[]"],
			"api_version": q.Get("api_version"), "secret": "whsec_test_" + id, "status": "enabled"}
		f.endpoints[id] = we
		send(we)
	case parts[0] == "webhook_endpoints" && len(parts) == 2:
		we := f.endpoints[parts[1]]
		if we == nil {
			w.WriteHeader(404)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": "invalid_request_error", "code": "resource_missing", "message": "No such webhook endpoint"}})
			return
		}
		if r.Method == http.MethodDelete {
			delete(f.endpoints, parts[1])
			send(map[string]any{"id": parts[1], "deleted": true})
			return
		}
		we["url"] = q.Get("url")
		send(we)
	case r.URL.Path == "/v1/charges":
		list("charge", func(o map[string]any) bool {
			return q.Get("payment_intent") == "" || o["payment_intent"] == q.Get("payment_intent")
		})
	case r.URL.Path == "/v1/refunds":
		list("refund", func(o map[string]any) bool { return q.Get("charge") == "" || o["charge"] == q.Get("charge") })
	case r.URL.Path == "/v1/invoices":
		list("invoice", func(o map[string]any) bool { return o["status"] == q.Get("status") })
	case r.URL.Path == "/v1/invoice_payments":
		list("invoice_payment", func(o map[string]any) bool { return q.Get("invoice") == "" || o["invoice"] == q.Get("invoice") })
	case r.URL.Path == "/v1/checkout/sessions":
		list("checkout.session", func(o map[string]any) bool { return o["status"] == q.Get("status") })
	case r.URL.Path == "/v1/products":
		list("product", nil)
	case len(parts) == 4 && parts[0] == "checkout" && parts[3] == "line_items":
		items, _ := f.objects[parts[2]]["line_items_data"].([]any)
		if items == nil {
			items = []any{}
		}
		send(map[string]any{"object": "list", "data": items, "has_more": false})
	case r.URL.Path == "/v1/subscriptions":
		list("subscription", func(o map[string]any) bool { return o["status"] == q.Get("status") })
	case len(parts) == 2:
		o := f.objects[parts[1]]
		if o == nil {
			fail(404, "invalid_request_error", "No such object: "+parts[1])
			return
		}
		send(f.expand(o, q["expand[]"]))
	default:
		fail(404, "invalid_request_error", "Unrecognized request URL")
	}
}

func (f *fakeStripe) endpoint() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, we := range f.endpoints {
		return we
	}
	return nil
}

// Objects in Stripe's shapes.

func stCh(id string, amount int64, cur, customer, pi string, at time.Time, bt string, meta map[string]any) map[string]any {
	return map[string]any{"id": id, "object": "charge", "amount": amount, "amount_captured": amount, "amount_refunded": int64(0), "currency": cur,
		"customer": nilIfEmpty(customer), "payment_intent": nilIfEmpty(pi), "paid": true, "captured": true, "status": "succeeded", "created": at.Unix(),
		"balance_transaction": bt, "metadata": orEmpty(meta), "billing_details": map[string]any{"email": nil}, "receipt_email": nil, "livemode": false}
}

func stBT(id string, amount int64, cur string) map[string]any {
	return map[string]any{"id": id, "object": "balance_transaction", "amount": amount, "currency": cur, "fee": int64(0), "net": amount, "type": "charge"}
}

func stCustomerObj(id, email string) map[string]any {
	return map[string]any{"id": id, "object": "customer", "email": nilIfEmpty(email)}
}

func stSessionObj(id, mode, pi, invoice, sub, ref string, at time.Time) map[string]any {
	return map[string]any{"id": id, "object": "checkout.session", "mode": mode, "status": "complete", "payment_status": "paid",
		"client_reference_id": nilIfEmpty(ref), "metadata": map[string]any{}, "payment_intent": nilIfEmpty(pi), "invoice": nilIfEmpty(invoice),
		"subscription": nilIfEmpty(sub), "customer_details": map[string]any{"email": nil}, "created": at.Unix()}
}

func stInvoiceObj(id, reason, sub string, at time.Time) map[string]any {
	var parent any
	if sub != "" {
		parent = map[string]any{"type": "subscription_details", "subscription_details": map[string]any{"subscription": sub, "metadata": map[string]any{}}}
	}
	return map[string]any{"id": id, "object": "invoice", "status": "paid", "billing_reason": reason, "parent": parent, "metadata": map[string]any{},
		"customer_email": nil, "created": at.Unix()}
}

func stInvoicePay(id, invoice, pi string, at time.Time) map[string]any {
	return map[string]any{"id": id, "object": "invoice_payment", "invoice": invoice, "status": "paid", "created": at.Unix(),
		"payment": map[string]any{"type": "payment_intent", "payment_intent": pi}}
}

func stSub(id, customer, status, cur string, unit int64, interval string) map[string]any {
	return map[string]any{"id": id, "object": "subscription", "customer": customer, "status": status, "currency": cur, "metadata": map[string]any{},
		"start_date": int64(1700000000), "ended_at": nil, "items": map[string]any{"object": "list", "data": []any{map[string]any{
			"quantity": 1, "price": map[string]any{"unit_amount": unit, "currency": cur, "recurring": map[string]any{"interval": interval, "interval_count": 1}}}}}}
}

func stRefundObj(id, charge string, amount int64, cur, bt string, at time.Time) map[string]any {
	return map[string]any{"id": id, "object": "refund", "charge": charge, "amount": amount, "currency": cur, "status": "succeeded", "created": at.Unix(),
		"balance_transaction": bt}
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// ---- harness ----

type revHarness struct {
	t     *testing.T
	m     *Module
	p     *platform.Platform
	db    *state.DB
	owner string
	coll  *httptest.Server
	api   *httptest.Server
}

func newRevHarness(t *testing.T) *revHarness {
	t.Helper()
	root := t.TempDir()
	db, err := state.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tm := tokens.NewManager(db)
	owner, _, _ := tm.Bootstrap(context.Background())
	sec, err := platform.OpenSecrets(db, root)
	if err != nil {
		t.Fatal(err)
	}
	p := &platform.Platform{DB: db, Tokens: tm, Engine: change.NewEngine(db), Secrets: sec, Home: root, DataRoot: root, Domain: "box.test",
		PublicURL: "https://dashboard.box.test", Log: slog.New(slog.DiscardHandler)}
	var m *Module
	for _, x := range platform.Modules() {
		if am, ok := x.(*Module); ok {
			m = am
		}
	}
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if m.attr, err = openAttrStore(":memory:"); err != nil {
		t.Fatal(err)
	}
	m.setup(p, st, nil)
	revStates.Delete(m)
	m.rev().lastSweep = time.Now().Add(time.Hour) // no background sweeps: tests sweep themselves
	stripeInline = true
	oldUser, oldExists, oldLive, oldRedirect := userByEmail, userExists, liveDomain, redirectTarget
	t.Cleanup(func() {
		stripeInline, userByEmail, userExists, liveDomain, redirectTarget = false, oldUser, oldExists, oldLive, oldRedirect
	})
	userExists = func(_ context.Context, _, id string) (bool, error) { return strings.HasPrefix(id, "u_"), nil }
	liveDomain = func(string) bool { return false }
	redirectTarget = func(string) string { return "" }
	userByEmail = func(_ context.Context, project, email string) (string, error) {
		return map[string]string{"alice@example.com": "u_alice"}[email], nil
	}
	h := &revHarness{t: t, m: m, p: p, db: db, owner: owner}
	h.coll = httptest.NewServer(m.collectorHandler())
	t.Cleanup(h.coll.Close)
	h.api = httptest.NewServer(api.New(api.Deps{DB: db, Engine: p.Engine, Tokens: tm, Platform: p}).Handler())
	t.Cleanup(h.api.Close)
	return h
}

func (h *revHarness) call(method, path string, body any, into any) int {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, h.api.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+h.owner)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if into != nil && json.Unmarshal(b, into) != nil {
		h.t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, b)
	}
	return res.StatusCode
}

func signStripe(body []byte, secret string, at time.Time) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.%s", at.Unix(), body)
	return fmt.Sprintf("t=%d,v1=%s", at.Unix(), hex.EncodeToString(mac.Sum(nil)))
}

// webhook posts a Stripe event to /_tiffin/stripe on host.
func (h *revHarness) webhook(host string, body []byte, sig string) (int, string) {
	req, _ := http.NewRequest("POST", h.coll.URL+stripePath, strings.NewReader(string(body)))
	req.Host = host
	req.Header.Set("Stripe-Signature", sig)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func event(id, typ string, obj map[string]any, at time.Time) []byte {
	b, _ := json.Marshal(map[string]any{"id": id, "object": "event", "api_version": stripeVersion, "type": typ, "created": at.Unix(),
		"livemode": false, "pending_webhooks": 1, "data": map[string]any{"object": obj}})
	return b
}

func days(n int) time.Time { return time.Now().UTC().Add(-time.Duration(n) * 24 * time.Hour) }

// ---- tests ----

func TestVerifyStripe(t *testing.T) {
	body := []byte(`{"id":"evt_1"}`)
	now := time.Unix(1_800_000_000, 0)
	good := signStripe(body, "whsec_a", now)
	if err := verifyStripe(body, good, "whsec_a", now); err != nil {
		t.Fatal(err)
	}
	if err := verifyStripe(body, good, "whsec_b", now); err != errBadSignature {
		t.Fatalf("wrong secret: %v", err)
	}
	if err := verifyStripe([]byte(`{"id":"evt_2"}`), good, "whsec_a", now); err != errBadSignature {
		t.Fatalf("changed body: %v", err)
	}
	if err := verifyStripe(body, good, "whsec_a", now.Add(6*time.Minute)); err != errStale {
		t.Fatalf("stale: %v", err)
	}
	if err := verifyStripe(body, good, "whsec_a", now.Add(-6*time.Minute)); err != errStale {
		t.Fatalf("from the future: %v", err)
	}
	// While a rolled secret is valid Stripe signs with each: any v1 may match.
	_, v1, _ := strings.Cut(good, ",")
	multi := fmt.Sprintf("t=%d,v1=%s,%s,v0=deadbeef", now.Unix(), strings.Repeat("0", 64), v1)
	if err := verifyStripe(body, multi, "whsec_a", now); err != nil {
		t.Fatalf("multiple signatures: %v", err)
	}
	for _, h := range []string{"", "v1=abc", fmt.Sprintf("t=%d", now.Unix()), "t=x,v1=abc"} {
		if err := verifyStripe(body, h, "whsec_a", now); err != errNoSignature {
			t.Fatalf("%q: %v", h, err)
		}
	}
}

func TestMonthly(t *testing.T) {
	var s stSubscription
	raw, _ := json.Marshal(stSub("sub", "cus", "active", "usd", 12000, "year"))
	_ = json.Unmarshal(raw, &s)
	if got := s.monthly(); got != 1000 {
		t.Fatalf("yearly 120.00 = %d a month", got)
	}
	s.Items.Data[0].Quantity = 3
	s.Items.Data[0].Price.Recurring.Interval, s.Items.Data[0].Price.Recurring.IntervalCount = "week", 2
	if got := s.monthly(); got != 78000 { // 3 × 120.00 every 2 weeks = 52/12/2 × 360.00
		t.Fatalf("weekly: %d", got)
	}
	s.Items.Data[0].Price.UnitAmount = nil // metered or tiered
	if got := s.monthly(); got != 0 {
		t.Fatalf("no unit price: %d", got)
	}
}

// The whole Stripe flow: connect refuses without attribution, checks the
// key, creates the webhook, imports 12 months; payments match by
// reference, email, checkout and earlier payments; the report sums in the
// account's currency; webhooks are verified and deduplicated; refunds go
// negative with their payment's match; MRR follows subscriptions; the
// webhook follows the address; disconnecting cleans up.
func TestStripeRevenue(t *testing.T) {
	h := newRevHarness(t)
	ctx := context.Background()
	applyManifest(t, h.db, `{"project":"shop","apps":{"web":{"routes":["shop"]}},"services":{"analytics":{"attribution":{}}}}`)
	applyManifest(t, h.db, `{"project":"plain","apps":{"web":{"routes":["plain"]}},"services":{"analytics":{}}}`)
	key := "rk_" + "test_" + "fake0000000000000000000001234"
	f := newFakeStripe(t, key)

	// Where accounts came from.
	_ = h.m.attr.putSignup(ctx, "shop", Signup{UserID: "u_alice", At: days(20), Matched: "visit",
		Visit: Touch{Source: "Hacker News", RefHost: "news.ycombinator.com", Landing: "/pricing", Country: "US"},
		First: &Touch{At: days(25), Source: "Google", Landing: "/", Country: "US"}})
	_ = h.m.attr.putSignup(ctx, "shop", Signup{UserID: "u_bob", At: days(6), Matched: "visit",
		Visit: Touch{Source: "newsletter", UTMSource: "newsletter", UTMMedium: "email", UTMCampaign: "oct", Landing: "/", Country: "DE"}})
	_ = h.m.attr.putCheckout(ctx, "shop", "cs_1", Touch{Source: "Twitter", RefHost: "x.com", Landing: "/buy", Country: "US"}, days(10))
	_ = h.m.attr.putCheckout(ctx, "shop", "cs_3", Touch{Source: "Reddit", RefHost: "www.reddit.com", Landing: "/", Country: "GB"}, days(40))

	// The Stripe account (USD): a year of payments.
	f.put(
		stCustomerObj("cus_A", "alice@example.com"), stCustomerObj("cus_B", "bob@elsewhere.test"), stCustomerObj("cus_C", "carol@example.org"),
		stCustomerObj("cus_D", ""),
		// alice: checkout without a reference: matched by email, last touch = the checkout return.
		stCh("ch_1", 2000, "usd", "cus_A", "pi_1", days(10), "txn_1", nil), stBT("txn_1", 2000, "usd"),
		stSessionObj("cs_1", "payment", "pi_1", "", "", "", days(10)),
		// bob pays in EUR, the account gets USD: sums use the balance transaction.
		stCh("ch_2", 1000, "eur", "cus_B", "pi_2", days(5), "txn_2", map[string]any{"tiffin_user": "u_bob"}), stBT("txn_2", 1100, "usd"),
		// carol subscribes through Checkout (reference), then renews.
		stSub("sub_1", "cus_C", "active", "usd", 1500, "month"),
		stSessionObj("cs_3", "subscription", "", "in_1", "sub_1", "u_carol", days(40)),
		stInvoiceObj("in_1", "subscription_create", "sub_1", days(40)), stInvoicePay("inpay_1", "in_1", "pi_3", days(40)),
		stCh("ch_3", 1500, "usd", "cus_C", "pi_3", days(40), "txn_3", nil), stBT("txn_3", 1500, "usd"),
		stInvoiceObj("in_2", "subscription_cycle", "sub_1", days(10)), stInvoicePay("inpay_2", "in_2", "pi_4", days(10)),
		stCh("ch_4", 1500, "usd", "cus_C", "pi_4", days(10), "txn_4", nil), stBT("txn_4", 1500, "usd"),
		// A yearly EUR subscription (MRR converts at its payment's rate), unmatched.
		stSub("sub_2", "cus_D", "active", "eur", 12000, "year"),
		stInvoiceObj("in_5", "subscription_create", "sub_2", days(3)), stInvoicePay("inpay_5", "in_5", "pi_5", days(3)),
		stCh("ch_5", 12000, "eur", "cus_D", "pi_5", days(3), "txn_5", nil), stBT("txn_5", 13200, "usd"),
		// A guest payment with nothing to match.
		stCh("ch_6", 500, "usd", "", "pi_6", days(1), "txn_6", nil), stBT("txn_6", 500, "usd"),
		// A partial refund of alice's payment.
		stRefundObj("re_1", "ch_1", 500, "usd", "txn_r1", days(2)), stBT("txn_r1", -500, "usd"),
	)

	// Attribution off: refused. A bad key: refused with the permissions.
	var prob api.Problem
	if c := h.call("POST", "/v1/analytics/stripe", map[string]string{"project": "plain", "key": key}, &prob); c != 409 || !strings.Contains(prob.Hint, "Attribution") {
		t.Fatalf("connect without attribution: %d %+v", c, prob)
	}
	if c := h.call("POST", "/v1/analytics/stripe", map[string]string{"project": "shop", "key": "rk_test_wrongwrongwrong"}, &prob); c != 422 || !strings.Contains(prob.Hint, "Webhook Endpoints, Event Destinations: Write") {
		t.Fatalf("bad key: %d %+v", c, prob)
	}
	if c := h.call("POST", "/v1/analytics/stripe", map[string]string{"project": "shop", "key": "pk_test_123456789012"}, &prob); c != 422 {
		t.Fatalf("publishable key: %d", c)
	}
	f.deny["/v1/webhook_endpoints"] = true
	if c := h.call("POST", "/v1/analytics/stripe", map[string]string{"project": "shop", "key": key}, &prob); c != 422 || !strings.Contains(prob.Detail, "webhook") {
		t.Fatalf("key without webhook write: %d %+v", c, prob)
	}
	delete(f.deny, "/v1/webhook_endpoints")

	var st StripeStatus
	if c := h.call("POST", "/v1/analytics/stripe", map[string]string{"project": "shop", "key": key}, &st); c != 200 {
		t.Fatalf("connect: %d %+v", c, st)
	}
	we := f.endpoint()
	if we == nil || we["url"] != "https://shop.box.test"+stripePath || we["api_version"] != stripeVersion || len(we["enabled_events"].([]string)) != len(stripeEvents) {
		t.Fatalf("webhook endpoint %+v", we)
	}
	if !st.Connected || st.Mode != "test" || st.Key != "rk_test_…1234" || st.Backfill.State != "done" || st.Backfill.Payments != 6 || st.Backfill.Refunds != 1 ||
		st.Backfill.Subscriptions != 2 || st.AccountCurrency != "usd" || st.URLMismatch {
		t.Fatalf("status %+v", st)
	}
	conn, _ := h.m.stripeConn(ctx, "shop")
	if strings.Contains(string(conn.Key), key) || strings.Contains(string(conn.Secret), "whsec_") {
		t.Fatal("the key or signing secret is stored in the clear")
	}
	if !revenueConnected(ctx, h.m, "shop") || !h.m.privacy(ctx, "shop").Revenue {
		t.Fatal("privacy text does not cover payments")
	}

	pay := func(id string) *payment {
		t.Helper()
		p, err := h.m.attr.getPayment(ctx, "shop", id)
		if err != nil || p == nil {
			t.Fatalf("payment %s: %v %v", id, p, err)
		}
		return p
	}
	if p := pay("ch_1"); p.MatchedBy != "email" || p.UserID != "u_alice" || p.Kind != kindPayment || p.First.Source != "Google" || p.FirstFrom != "first-visit" ||
		p.Last.Source != "Twitter" || p.LastFrom != "checkout" || p.Checkout != "cs_1" {
		t.Fatalf("ch_1 %+v", p)
	}
	if p := pay("ch_2"); p.MatchedBy != "reference" || p.UserID != "u_bob" || p.Amount != 1000 || p.Currency != "eur" || p.AccountAmount != 1100 ||
		p.AccountCurrency != "usd" || p.First.UTMCampaign != "oct" || p.Last.UTMCampaign != "oct" {
		t.Fatalf("ch_2 %+v", p)
	}
	if p := pay("ch_3"); p.MatchedBy != "reference" || p.UserID != "u_carol" || p.Kind != kindPayment || p.Last.Source != "Reddit" || p.Subscription != "sub_1" {
		t.Fatalf("ch_3 %+v", p)
	}
	if p := pay("ch_4"); p.MatchedBy != "reference" || p.UserID != "u_carol" || p.Kind != kindRenewal || p.First.Source != "Reddit" || p.BillingReason != "subscription_cycle" {
		t.Fatalf("ch_4 renewal %+v", p)
	}
	if p := pay("ch_6"); p.MatchedBy != "" || !strings.Contains(p.Unmatched, "did not come through Checkout") || !strings.Contains(p.Unmatched, "has no email") {
		t.Fatalf("ch_6 %+v", p)
	}
	if p := pay("re_1"); p.Kind != kindRefund || p.AccountAmount != -500 || p.Parent != "ch_1" || p.UserID != "u_alice" || p.Last.Source != "Twitter" {
		t.Fatalf("refund %+v", p)
	}

	// Visitors for the conversion rate: 8 today.
	for i := 0; i < 8; i++ {
		hit := browserHit(time.Now(), "web", "https://shop.box.test/", "")
		hit.UA, hit.IP = human(i), fmt.Sprintf("203.0.113.%d", i+1)
		h.m.pipe.Add(ctx, hit)
	}
	var rep RevenueReport
	if c := h.call("GET", "/v1/analytics/revenue?project=shop&period=30d", nil, &rep); c != 200 {
		t.Fatalf("report %d %+v", c, rep)
	}
	tot := rep.Totals
	// 2000 + 1100 + 1500 + 13200 + 500 - 500 in USD.
	if rep.Currency != "usd" || rep.Provider != "stripe" || tot.Revenue != 17800 || tot.Gross != 18300 || tot.Refunded != 500 || tot.Payments != 5 ||
		tot.Renewals != 1 || tot.Refunds != 1 || tot.Customers != 5 || tot.NewCustomers != 4 || tot.Visitors != 8 || tot.ConversionRate != 0.5 ||
		tot.RevenuePerVisitor != 2225 || tot.MRR != 1500+1100 {
		t.Fatalf("totals %+v", tot)
	}
	if rep.Previous.Revenue != 1500 || rep.Previous.NewCustomers != 1 {
		t.Fatalf("previous %+v", rep.Previous)
	}
	want := map[string]int64{"Twitter": 1500, "newsletter": 1100, "Reddit": 1500}
	if len(rep.Sources) != 3 {
		t.Fatalf("sources %+v", rep.Sources)
	}
	for _, s := range rep.Sources {
		if want[s.Value] != s.Revenue {
			t.Fatalf("sources %+v", rep.Sources)
		}
	}
	if rep.Unattributed.Revenue != 13700 || rep.Unattributed.Payments != 2 || len(rep.Unmatched) != 2 || rep.Unmatched[0].ID != "ch_6" {
		t.Fatalf("unattributed %+v unmatched %+v", rep.Unattributed, rep.Unmatched)
	}
	if rep.MatchedBy["email"] != 1 || rep.MatchedBy["reference"] != 2 || rep.MatchedBy["none"] != 2 {
		t.Fatalf("matched by %+v", rep.MatchedBy)
	}
	var first RevenueReport
	h.call("GET", "/v1/analytics/revenue?project=shop&period=30d&touch=first", nil, &first)
	google := int64(0)
	for _, s := range first.Sources {
		if s.Value == "Google" {
			google = s.Revenue
		}
	}
	if google != 1500 { // alice's first visit, less her refund
		t.Fatalf("first-touch sources %+v", first.Sources)
	}
	var sum int64
	for _, pt := range rep.Timeseries {
		sum += pt.Revenue
	}
	if sum != 17800 || rep.Granularity != "day" {
		t.Fatalf("timeseries sums to %d", sum)
	}

	// Webhooks: a new Checkout payment by alice, with her id.
	f.put(stCh("ch_7", 3000, "usd", "cus_A", "pi_7", time.Now(), "txn_7", nil), stBT("txn_7", 3000, "usd"))
	secret := we["secret"].(string)
	body := event("evt_1", "checkout.session.completed", stSessionObj("cs_7", "payment", "pi_7", "", "", "u_alice", time.Now()), time.Now())
	if c, b := h.webhook("shop.box.test", body, signStripe(body, "whsec_wrong", time.Now())); c != 400 || !strings.Contains(b, "no v1 signature") {
		t.Fatalf("bad signature: %d %s", c, b)
	}
	if c, _ := h.webhook("shop.box.test", body, signStripe(body, secret, time.Now().Add(-10*time.Minute))); c != 400 {
		t.Fatalf("stale signature: %d", c)
	}
	if c, _ := h.webhook("plain.box.test", body, signStripe(body, secret, time.Now())); c != 404 {
		t.Fatalf("another project's host: %d", c)
	}
	if _, err := h.m.attr.getPayment(ctx, "shop", "ch_7"); err != nil {
		t.Fatal(err)
	} else if p, _ := h.m.attr.getPayment(ctx, "shop", "ch_7"); p != nil {
		t.Fatal("an unverified event was applied")
	}
	if c, b := h.webhook("shop.box.test:8443", body, signStripe(body, secret, time.Now())); c != 200 || !strings.Contains(b, "received") {
		t.Fatalf("webhook: %d %s", c, b)
	}
	if c, b := h.webhook("shop.box.test", body, signStripe(body, secret, time.Now())); c != 200 || !strings.Contains(b, "duplicate") {
		t.Fatalf("replayed event: %d %s", c, b)
	}
	if p := pay("ch_7"); p.MatchedBy != "reference" || p.Kind != kindRenewal || p.Checkout != "cs_7" || p.AccountAmount != 3000 {
		t.Fatalf("ch_7 %+v", p)
	}
	// charge.succeeded for the same charge later changes nothing.
	body = event("evt_2", "charge.succeeded", map[string]any{"id": "ch_7", "object": "charge"}, time.Now())
	h.webhook("shop.box.test", body, signStripe(body, secret, time.Now()))
	if p := pay("ch_7"); p.MatchedBy != "reference" || p.Checkout != "cs_7" {
		t.Fatalf("ch_7 after charge.succeeded %+v", p)
	}

	// bob is refunded in full: in EUR, back out of the account in USD.
	f.put(stRefundObj("re_2", "ch_2", 1000, "eur", "txn_r2", time.Now()), stBT("txn_r2", -1100, "usd"))
	f.objects["ch_2"]["amount_refunded"], f.objects["ch_2"]["refunded"] = int64(1000), true
	body = event("evt_3", "charge.refunded", f.objects["ch_2"], time.Now())
	h.webhook("shop.box.test", body, signStripe(body, secret, time.Now()))
	if p := pay("re_2"); p.AccountAmount != -1100 || p.AccountCurrency != "usd" || p.Amount != -1000 || p.UserID != "u_bob" || p.Last.UTMCampaign != "oct" {
		t.Fatalf("re_2 %+v", p)
	}

	// carol renews through an invoice (no Checkout): matched by her subscription's reference.
	f.put(stInvoiceObj("in_8", "subscription_cycle", "sub_1", time.Now()), stInvoicePay("inpay_8", "in_8", "pi_8", time.Now()),
		stCh("ch_8", 1500, "usd", "cus_C", "pi_8", time.Now(), "txn_8", nil), stBT("txn_8", 1500, "usd"))
	body = event("evt_4", "invoice.paid", f.objects["in_8"], time.Now())
	h.webhook("shop.box.test", body, signStripe(body, secret, time.Now()))
	if p := pay("ch_8"); p.Kind != kindRenewal || p.UserID != "u_carol" || p.Invoice != "in_8" {
		t.Fatalf("ch_8 %+v", p)
	}

	// carol cancels: MRR drops.
	sub := stSub("sub_1", "cus_C", "canceled", "usd", 1500, "month")
	body = event("evt_5", "customer.subscription.deleted", sub, time.Now().Add(2*time.Second))
	h.webhook("shop.box.test", body, signStripe(body, secret, time.Now()))
	h.call("GET", "/v1/analytics/revenue?project=shop&period=30d", nil, &rep)
	if rep.Totals.MRR != 1100 || rep.Totals.Revenue != 17800+3000-1100+1500 {
		t.Fatalf("after webhooks %+v", rep.Totals)
	}

	// revenue() is ignored while Stripe is connected.
	env, _ := h.m.Env(ctx, h.p, "shop", "web")
	if c, b := h.revenue(env["TIFFIN_ANALYTICS_KEY"], `{"id":"order-1","amount":999,"currency":"usd"}`); c != 202 || !strings.Contains(b, `"ignored":"stripe connected"`) {
		t.Fatalf("revenue() while connected: %d %s", c, b)
	}

	// Matching again: the guest's charge now carries a sign-in user's email.
	f.objects["ch_6"]["billing_details"] = map[string]any{"email": "alice@example.com"}
	var mr MatchResult
	if c := h.call("POST", "/v1/analytics/revenue/match", map[string]string{"project": "shop"}, &mr); c != 200 || mr.Checked != 2 || mr.Matched != 1 {
		t.Fatalf("match again: %d %+v", c, mr)
	}
	if p := pay("ch_6"); p.MatchedBy != "email" || p.UserID != "u_alice" || p.Kind != kindRenewal {
		t.Fatalf("ch_6 after match again %+v", p)
	}

	// The project moves to another address: status says so, repair follows.
	applyManifest(t, h.db, `{"project":"shop","apps":{"web":{"routes":["store"]}},"services":{"analytics":{"attribution":{}}}}`)
	h.m.sites.Invalidate()
	h.call("GET", "/v1/analytics/stripe?project=shop", nil, &st)
	if !st.URLMismatch || st.WantURL != "https://store.box.test"+stripePath {
		t.Fatalf("status after a move %+v", st)
	}
	if c := h.call("POST", "/v1/analytics/stripe/repair", map[string]string{"project": "shop"}, &st); c != 200 || st.URLMismatch || f.endpoint()["url"] != "https://store.box.test"+stripePath {
		t.Fatalf("repair: %d %+v %v", c, st, f.endpoint())
	}

	// Disconnect: the endpoint, the key and the Stripe revenue go.
	var dc struct{ Warning string }
	if c := h.call("DELETE", "/v1/analytics/stripe?project=shop", nil, &dc); c != 200 || dc.Warning != "" {
		t.Fatalf("disconnect: %d %+v", c, dc)
	}
	if f.endpoint() != nil {
		t.Fatal("the webhook endpoint was not deleted")
	}
	if _, ok := h.m.stripeConn(ctx, "shop"); ok {
		t.Fatal("the connection is still stored")
	}
	if rows, _ := h.m.attr.projectRows(ctx, "shop"); rows["payments"] != 0 || rows["subscriptions"] != 0 || rows["stripe_events"] != 0 {
		t.Fatalf("left after disconnect: %v", rows)
	}
	if c, _ := h.webhook("store.box.test", body, signStripe(body, secret, time.Now())); c != 404 {
		t.Fatalf("webhook after disconnect: %d", c)
	}
}

func (h *revHarness) revenue(key, body string) (int, string) {
	req, _ := http.NewRequest("POST", h.coll.URL+"/revenue", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

// Turning attribution off deletes the revenue with the rest, and the next
// sweep removes the webhook endpoint and the key.
func TestStripeAttributionOff(t *testing.T) {
	h := newRevHarness(t)
	ctx := context.Background()
	applyManifest(t, h.db, `{"project":"shop","apps":{"web":{"routes":["shop"]}},"services":{"analytics":{"attribution":{}}}}`)
	key := "rk_" + "live_" + "fake0000000000000000000009876" // split: a made-up key, not one secret scanners should match
	f := newFakeStripe(t, key)
	f.put(stCh("ch_1", 2000, "usd", "", "", days(3), "txn_1", nil), stBT("txn_1", 2000, "usd"))
	var st StripeStatus
	if c := h.call("POST", "/v1/analytics/stripe", map[string]string{"project": "shop", "key": key}, &st); c != 200 || st.Mode != "live" || st.Backfill.Payments != 1 {
		t.Fatalf("connect %d %+v", c, st)
	}
	applyManifest(t, h.db, `{"project":"shop","apps":{"web":{"routes":["shop"]}},"services":{"analytics":{}}}`)
	_, res, _ := h.db.Load(ctx, "shop")
	if err := h.m.Reconcile(ctx, h.p, "shop", "service/analytics", res["service/analytics"].Spec); err != nil {
		t.Fatal(err)
	}
	if rows, _ := h.m.attr.projectRows(ctx, "shop"); rows["payments"] != 0 {
		t.Fatalf("payments kept with attribution off: %v", rows)
	}
	if revenueConnected(ctx, h.m, "shop") && h.m.privacy(ctx, "shop").Revenue {
		t.Fatal("privacy text still covers payments")
	}
	h.m.sweepStripe(ctx)
	if _, ok := h.m.stripeConn(ctx, "shop"); ok || f.endpoint() != nil {
		t.Fatal("the connection outlived attribution")
	}
}

// revenue() from server code, for other payment providers.
func TestServerRevenue(t *testing.T) {
	h := newRevHarness(t)
	ctx := context.Background()
	applyManifest(t, h.db, `{"project":"shop","apps":{"web":{"routes":["shop"]}},"services":{"analytics":{"attribution":{}}}}`)
	applyManifest(t, h.db, `{"project":"plain","apps":{"web":{"routes":["plain"]}},"services":{"analytics":{}}}`)
	env, _ := h.m.Env(ctx, h.p, "shop", "web")
	key := env["TIFFIN_ANALYTICS_KEY"]
	_ = h.m.attr.putSignup(ctx, "shop", Signup{UserID: "u_1", At: days(2), Matched: "visit", Visit: Touch{Source: "Google", Landing: "/", Country: "FR"}})

	// A buyer browsing from Hacker News pays during the visit.
	at := time.Now().UTC()
	h.m.pipe.Add(ctx, browserHit(at, "web", "https://shop.box.test/pricing?utm_campaign=launch", "https://news.ycombinator.com/"))
	ua := strconv.Quote(testUA)

	for _, bad := range []string{`{"id":"x","amount":19.99,"currency":"usd"}`, `{"id":"x","amount":1999,"currency":"dollars"}`, `{"amount":1999,"currency":"usd"}`} {
		if c, b := h.revenue(key, bad); c != 400 {
			t.Fatalf("%s: %d %s", bad, c, b)
		}
	}
	if c, _ := h.revenue("tak_wrong", `{"id":"x","amount":1,"currency":"usd"}`); c != 401 {
		t.Fatalf("bad key: %d", c)
	}
	plainEnv, _ := h.m.Env(ctx, h.p, "plain", "web")
	if c, b := h.revenue(plainEnv["TIFFIN_ANALYTICS_KEY"], `{"id":"x","amount":1,"currency":"usd"}`); c != 409 || !strings.Contains(b, "attribution") {
		t.Fatalf("without attribution: %d %s", c, b)
	}
	if c, b := h.revenue(key, `{"id":"o1","amount":4900,"currency":"USD","ip":"203.0.113.7","ua":`+ua+`}`); c != 202 || !strings.Contains(b, `"matched":"visit"`) {
		t.Fatalf("visit match: %d %s", c, b)
	}
	if c, b := h.revenue(key, `{"id":"o1","amount":4900,"currency":"usd"}`); c != 202 || !strings.Contains(b, "duplicate") {
		t.Fatalf("retry: %d %s", c, b)
	}
	if c, b := h.revenue(key, `{"id":"o2","amount":1000,"currency":"usd","user":"u_1"}`); c != 202 || !strings.Contains(b, `"matched":"reference"`) {
		t.Fatalf("user match: %d %s", c, b)
	}
	if c, b := h.revenue(key, `{"id":"o3","amount":1000,"currency":"usd","user":"u_1","renewal":true}`); c != 202 {
		t.Fatalf("renewal: %d %s", c, b)
	}
	if c, b := h.revenue(key, `{"id":"r1","amount":1000,"currency":"usd","refund":"o2"}`); c != 202 || !strings.Contains(b, `"matched":"reference"`) {
		t.Fatalf("refund: %d %s", c, b)
	}
	if c, b := h.revenue(key, `{"id":"o4","amount":700,"currency":"eur"}`); c != 202 || !strings.Contains(b, `"matched":""`) {
		t.Fatalf("unmatched: %d %s", c, b)
	}
	p, _ := h.m.attr.getPayment(ctx, "shop", "app:o1")
	if p.Last.Source != "Hacker News" || p.Last.UTMCampaign != "launch" || p.FirstFrom != "visit" || p.Currency != "usd" {
		t.Fatalf("o1 %+v", p)
	}
	if p, _ := h.m.attr.getPayment(ctx, "shop", "app:o3"); p.Kind != kindRenewal {
		t.Fatalf("o3 %+v", p)
	}
	if p, _ := h.m.attr.getPayment(ctx, "shop", "app:r1"); p.AccountAmount != -1000 || p.UserID != "u_1" || p.Last.Source != "Google" {
		t.Fatalf("r1 %+v", p)
	}
	var rep RevenueReport
	if c := h.call("GET", "/v1/analytics/revenue?project=shop&period=7d", nil, &rep); c != 200 {
		t.Fatalf("report %d", c)
	}
	if rep.Provider != "app" || rep.Currency != "usd" || rep.Totals.Revenue != 4900+1000+1000-1000 || rep.Totals.NewCustomers != 2 || rep.Totals.Renewals != 1 ||
		len(rep.Other) != 1 || rep.Other[0].Amount != 700 || rep.Totals.MRR != 0 {
		t.Fatalf("report %+v other %+v", rep.Totals, rep.Other)
	}
	if !revenueConnected(ctx, h.m, "shop") {
		t.Fatal("revenue() payments should put payments in the privacy text")
	}
	var prob api.Problem
	if c := h.call("GET", "/v1/analytics/revenue?project=plain", nil, &prob); c != 409 {
		t.Fatalf("report without attribution: %d", c)
	}
}

// An app's own id in client_reference_id (a box, an order) is not taken for
// an account: the payment falls through to the email. metadata.tiffin_user
// names the account and wins over client_reference_id.
func TestReferenceMustBeAnAccount(t *testing.T) {
	h := newRevHarness(t)
	ctx := context.Background()
	applyManifest(t, h.db, `{"project":"shop","apps":{"web":{"routes":["shop"]}},"services":{"analytics":{"attribution":{}}}}`)
	email := func(context.Context) (string, error) { return "alice@example.com", nil }
	pay := func(id, ref string) *payment {
		p := &payment{ID: id, Provider: providerStripe, Kind: kindPayment, At: time.Now(), Amount: 1900, Currency: "usd",
			AccountAmount: 1900, AccountCurrency: "usd", Status: "succeeded", Customer: "cus_" + id, RefUser: ref}
		saved, err := h.m.attr.putPayment(ctx, "shop", *p)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.m.match(ctx, "shop", saved, email); err != nil {
			t.Fatal(err)
		}
		got, _ := h.m.attr.getPayment(ctx, "shop", id)
		return got
	}
	if p := pay("ch_box", "box_7"); p.UserID != "u_alice" || p.MatchedBy != "email" {
		t.Fatalf("a box id in client_reference_id: %+v", p)
	}
	if p := pay("ch_named", "u_carol"); p.UserID != "u_carol" || p.MatchedBy != "reference" {
		t.Fatalf("an account reference: %+v", p)
	}
	email = func(context.Context) (string, error) { return "", nil }
	if p := pay("ch_none", "box_8"); p.UserID != "" || !strings.Contains(p.Unmatched, "is not an account") {
		t.Fatalf("unmatched reason: %+v", p)
	}
	s := sessionHints(&stSession{ClientReferenceID: "box_9", Metadata: map[string]string{"tiffin_user": "u_dan"}})
	if s.RefUser != "u_dan" {
		t.Fatalf("metadata.tiffin_user must win: %+v", s)
	}
}


// lineItem is a Checkout line item for product (description = its name).
func lineItem(product, name string, amount int64) map[string]any {
	return map[string]any{"object": "item", "description": name, "amount_total": amount, "price": map[string]any{"product": product}}
}

// One Stripe account sells two apps' products: with no choice every payment
// counts (exactly as before); a choice counts only its products' payments
// (through subscription, invoice or Checkout), refunds following their
// payment, and MRR its subscriptions; changing it needs no import.
func TestStripeProductChoice(t *testing.T) {
	h := newRevHarness(t)
	applyManifest(t, h.db, `{"project":"shop","apps":{"web":{"routes":["shop"]}},"services":{"analytics":{"attribution":{}}}}`)
	key := "rk_" + "test_" + "fake0000000000000000000005678"
	f := newFakeStripe(t, key)
	sub := func(id string, unit int64, product string) map[string]any {
		o := stSub(id, "cus_"+id, "active", "usd", unit, "month")
		it := o["items"].(map[string]any)["data"].([]any)[0].(map[string]any)
		it["price"].(map[string]any)["product"] = product
		return o
	}
	inv := func(id, sub, product string) map[string]any {
		o := stInvoiceObj(id, "subscription_create", sub, days(8))
		o["lines"] = map[string]any{"object": "list", "data": []any{map[string]any{"description": "1 × it",
			"pricing": map[string]any{"type": "price_details", "price_details": map[string]any{"product": product, "price": "price_x"}}}}}
		return o
	}
	csReel := stSessionObj("cs_reel", "payment", "pi_r1", "", "", "", days(10))
	csReel["line_items_data"] = []any{lineItem("prod_reel", "Reelbird Pro", 500)}
	csDibs := stSessionObj("cs_dibs", "payment", "pi_d1", "", "", "", days(9))
	csDibs["line_items_data"] = []any{lineItem("prod_dibs", "Dibs Plus", 800)}
	f.put(
		map[string]any{"id": "prod_reel", "object": "product", "name": "Reelbird Pro", "active": true},
		map[string]any{"id": "prod_dibs", "object": "product", "name": "Dibs Plus", "active": true},
		map[string]any{"id": "prod_oss", "object": "product", "name": "Open Source Sponsor", "active": true},
		// Reelbird: a Checkout purchase and a subscription paid by invoice.
		csReel, stCh("ch_r1", 500, "usd", "cus_r1", "pi_r1", days(10), "txn_r1", nil), stBT("txn_r1", 500, "usd"),
		sub("sub_reel", 500, "prod_reel"), inv("in_reel", "sub_reel", "prod_reel"), stInvoicePay("ip_reel", "in_reel", "pi_r2", days(8)),
		stCh("ch_r2", 500, "usd", "cus_sub_reel", "pi_r2", days(8), "txn_r2", nil), stBT("txn_r2", 500, "usd"),
		// Dibs: a Checkout purchase, partly refunded, and a subscription.
		csDibs, stCh("ch_d1", 800, "usd", "cus_d1", "pi_d1", days(9), "txn_d1", nil), stBT("txn_d1", 800, "usd"),
		stRefundObj("re_d1", "ch_d1", 200, "usd", "txn_rd", days(4)), stBT("txn_rd", -200, "usd"),
		sub("sub_dibs", 900, "prod_dibs"),
		// A charge with no product (outside Checkout and invoices).
		stCh("ch_x", 300, "usd", "", "pi_x", days(2), "txn_x", nil), stBT("txn_x", 300, "usd"),
	)
	var st StripeStatus
	if c := h.call("POST", "/v1/analytics/stripe", map[string]string{"project": "shop", "key": key}, &st); c != 200 {
		t.Fatalf("connect %d %+v", c, st)
	}
	report := func() RevenueReport {
		t.Helper()
		var r RevenueReport
		if c := h.call("GET", "/v1/analytics/revenue?project=shop&period=30d", nil, &r); c != 200 {
			t.Fatalf("revenue %d", c)
		}
		return r
	}
	set := func(body map[string]any) (int, StripeProducts) {
		t.Helper()
		body["project"] = "shop"
		var out StripeProducts
		return h.call("POST", "/v1/analytics/stripe/products", body, &out), out
	}
	all := report()
	if all.Totals.Gross != 2100 || all.Totals.Refunded != 200 || all.Totals.Revenue != 1900 || all.Totals.MRR != 1400 || all.Totals.Payments != 4 {
		t.Fatalf("every payment: %+v", all.Totals)
	}

	var list StripeProducts
	if c := h.call("GET", "/v1/analytics/stripe/products?project=shop", nil, &list); c != 200 || !list.All || !list.Catalog || list.NoneCount != 1 {
		t.Fatalf("products %d %+v", c, list)
	}
	byID := map[string]StripeProduct{}
	for _, p := range list.Products {
		byID[p.ID] = p
	}
	if byID["prod_reel"].Name != "Reelbird Pro" || byID["prod_reel"].Payments != 2 || byID["prod_dibs"].Payments != 1 || byID["prod_oss"].Payments != 0 || !byID["prod_dibs"].Counted {
		t.Fatalf("products %+v", list.Products)
	}

	if c, out := set(map[string]any{"products": []string{"prod_reel"}}); c != 200 || out.All || out.NoProduct {
		t.Fatalf("choose reelbird %d %+v", c, out)
	}
	reel := report()
	if reel.Totals.Gross != 1000 || reel.Totals.Refunded != 0 || reel.Totals.Payments != 2 || reel.Totals.MRR != 500 || reel.Totals.NewCustomers != 2 {
		t.Fatalf("reelbird only: %+v", reel.Totals)
	}

	if c, _ := set(map[string]any{"products": []string{"prod_dibs"}, "noProduct": true}); c != 200 {
		t.Fatalf("choose dibs %d", c)
	}
	dibs := report()
	if dibs.Totals.Gross != 1100 || dibs.Totals.Refunded != 200 || dibs.Totals.Revenue != 900 || dibs.Totals.MRR != 900 {
		t.Fatalf("dibs and no product: %+v", dibs.Totals)
	}
	for _, u := range dibs.Unmatched {
		if u.ID == "ch_r1" || u.ID == "ch_r2" {
			t.Fatalf("another product's payment in unmatched: %+v", u)
		}
	}

	if c, _ := set(map[string]any{}); c != 422 {
		t.Fatalf("an empty choice: %d", c)
	}
	if c, _ := set(map[string]any{"products": []string{"price_123"}}); c != 422 {
		t.Fatalf("not a product id: %d", c)
	}
	if c, out := set(map[string]any{"all": true}); c != 200 || !out.All {
		t.Fatalf("back to every payment %d %+v", c, out)
	}
	if again := report(); again.Totals != all.Totals {
		t.Fatalf("every payment again:\n got %+v\nwant %+v", again.Totals, all.Totals)
	}

	// Disconnecting forgets the products with the payments.
	if c := h.call("DELETE", "/v1/analytics/stripe?project=shop", nil, nil); c != 200 && c != 204 {
		t.Fatalf("disconnect %d", c)
	}
	if rows, _ := h.m.attr.projectRows(context.Background(), "shop"); rows["stripe_items"] != 0 || rows["stripe_products"] != 0 {
		t.Fatalf("after disconnect %v", rows)
	}
}

// MRR takes subscriptions' discounts off while they last: a free plan counts
// nothing, half off counts half, an ended repeating discount and no discount
// count the list price; a discount removed (by webhook) stops counting.
func TestMRRDiscounts(t *testing.T) {
	h := newRevHarness(t)
	applyManifest(t, h.db, `{"project":"shop","apps":{"web":{"routes":["shop"]}},"services":{"analytics":{"attribution":{}}}}`)
	key := "rk_" + "test_" + "fake0000000000000000000009999"
	f := newFakeStripe(t, key)
	disc := func(id string, end *time.Time) map[string]any {
		var e any
		if end != nil {
			e = end.Unix()
		}
		return map[string]any{"id": id, "object": "discount", "end": e, "source": map[string]any{"type": "coupon", "coupon": "co_x"}}
	}
	inv := func(id string, subtotal, total int64) map[string]any {
		return map[string]any{"id": id, "object": "invoice_totals", "subtotal": subtotal, "total": total, "total_excluding_tax": total}
	}
	withDisc := func(s map[string]any, latest string, ds ...map[string]any) map[string]any {
		list := []any{}
		for _, d := range ds {
			list = append(list, d)
		}
		s["discounts"], s["latest_invoice"] = list, latest
		return s
	}
	past := time.Now().AddDate(0, -1, 0)
	f.put(
		inv("in_free", 500, 0), inv("in_half", 1000, 500), inv("in_rep", 800, 400),
		withDisc(stSub("sub_free", "cus_1", "active", "usd", 500, "month"), "in_free", disc("di_free", nil)),
		withDisc(stSub("sub_half", "cus_2", "active", "usd", 1000, "month"), "in_half", disc("di_half", nil)),
		withDisc(stSub("sub_rep", "cus_3", "active", "usd", 800, "month"), "in_rep", disc("di_rep", &past)),
		stSub("sub_plain", "cus_4", "active", "usd", 300, "month"),
	)
	var st StripeStatus
	if c := h.call("POST", "/v1/analytics/stripe", map[string]string{"project": "shop", "key": key}, &st); c != 200 {
		t.Fatalf("connect %d", c)
	}
	mrr := func() int64 {
		t.Helper()
		var r RevenueReport
		if c := h.call("GET", "/v1/analytics/revenue?project=shop&period=30d", nil, &r); c != 200 {
			t.Fatalf("revenue %d", c)
		}
		return r.Totals.MRR
	}
	if got := mrr(); got != 0+500+800+300 {
		t.Fatalf("MRR %d, want 1600", got)
	}

	// The half-off discount is removed: the subscription pays its list price again.
	we := f.endpoint()
	secret, _ := we["secret"].(string)
	plain := withDisc(stSub("sub_half", "cus_2", "active", "usd", 1000, "month"), "in_half")
	body := event("evt_d1", "customer.subscription.updated", plain, time.Now())
	if c, b := h.webhook("shop.box.test", body, signStripe(body, secret, time.Now())); c != 200 {
		t.Fatalf("webhook %d %s", c, b)
	}
	h.m.drainStripe(context.Background())
	if got := mrr(); got != 0+1000+800+300 {
		t.Fatalf("MRR after the discount went %d, want 2100", got)
	}
}
