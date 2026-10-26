package analytics

// A small client for the parts of the Stripe API revenue attribution reads
// (charges, refunds, balance transactions, invoices and their payments,
// Checkout Sessions, subscriptions, customers) and the one thing it writes:
// its own webhook endpoint. Requests and the webhook's events are pinned to
// stripeVersion, so the shapes below are the ones parsed.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// stripeVersion is the API version requests and webhook events use
// (docs.stripe.com/api/versioning, current as of 2026-10).
const stripeVersion = "2026-09-30.endive"

// stripeAPIBase is Stripe's API; tests point it at a fake.
var stripeAPIBase = "https://api.stripe.com"

// stripeEvents are the events the webhook endpoint subscribes to.
var stripeEvents = []string{
	"charge.succeeded", "charge.captured", "charge.refunded",
	"checkout.session.completed", "checkout.session.async_payment_succeeded",
	"invoice.paid",
	"customer.subscription.created", "customer.subscription.updated", "customer.subscription.deleted",
}

// stripePermissions are the restricted key's permissions, named as the rows
// of Stripe's restricted-key form (Balance covers balance transactions;
// Charges and Refunds is one row).
var stripePermissions = []string{
	"Balance: Read", "Charges and Refunds: Read", "Customers: Read", "Invoices: Read", "Subscriptions: Read",
	"Checkout Sessions: Read", "Webhook Endpoints, Event Destinations: Write",
}

type stripeAPI struct {
	key string
	hc  *http.Client
}

func newStripe(key string) *stripeAPI {
	return &stripeAPI{key: key, hc: &http.Client{Timeout: 30 * time.Second}}
}

// stripeError is an error Stripe answered.
type stripeError struct {
	Status  int
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *stripeError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("Stripe answered %d", e.Status)
	}
	return "Stripe: " + e.Message
}

// missing reports whether Stripe said the object does not exist.
func (e *stripeError) missing() bool {
	return e.Status == http.StatusNotFound || e.Code == "resource_missing"
}

func (c *stripeAPI) do(ctx context.Context, method, path string, form url.Values, out any) error {
	var body io.Reader
	u := stripeAPIBase + path
	if method == http.MethodGet {
		if len(form) > 0 {
			u += "?" + form.Encode()
		}
	} else if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Stripe-Version", stripeVersion)
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	res, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("reach Stripe: %w", err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("read Stripe's answer: %w", err)
	}
	if res.StatusCode/100 != 2 {
		var e struct {
			Error stripeError `json:"error"`
		}
		_ = json.Unmarshal(b, &e)
		e.Error.Status = res.StatusCode
		return &e.Error
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(bytes.TrimSpace(b), out)
}

// list pages through a list endpoint, oldest page last (Stripe lists newest
// first), calling each for every object.
func (c *stripeAPI) list(ctx context.Context, path string, q url.Values, each func(json.RawMessage) error) error {
	q2 := url.Values{}
	for k, v := range q {
		q2[k] = v
	}
	q2.Set("limit", "100")
	for {
		var page struct {
			Data    []json.RawMessage `json:"data"`
			HasMore bool              `json:"has_more"`
		}
		if err := c.do(ctx, http.MethodGet, path, q2, &page); err != nil {
			return err
		}
		last := ""
		for _, raw := range page.Data {
			if err := each(raw); err != nil {
				return err
			}
			var o struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(raw, &o)
			last = o.ID
		}
		if !page.HasMore || last == "" {
			return nil
		}
		q2.Set("starting_after", last)
	}
}

// ---- objects (only the fields used) ----

// ref is an expandable field: an id, or the object with expand[].
type ref[T any] struct {
	ID  string
	Obj *T
}

func (r *ref[T]) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &r.ID)
	}
	if string(b) == "null" {
		return nil
	}
	var o T
	if err := json.Unmarshal(b, &o); err != nil {
		return err
	}
	var id struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(b, &id)
	r.ID, r.Obj = id.ID, &o
	return nil
}

type stBalanceTxn struct {
	ID       string `json:"id"`
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

type stCharge struct {
	ID                 string                 `json:"id"`
	Amount             int64                  `json:"amount"`
	AmountCaptured     int64                  `json:"amount_captured"`
	AmountRefunded     int64                  `json:"amount_refunded"`
	Currency           string                 `json:"currency"`
	Created            int64                  `json:"created"`
	Customer           ref[stCustomer]        `json:"customer"`
	PaymentIntent      ref[struct{}]          `json:"payment_intent"`
	Paid               bool                   `json:"paid"`
	Captured           bool                   `json:"captured"`
	Status             string                 `json:"status"`
	Metadata           map[string]string      `json:"metadata"`
	ReceiptEmail       string                 `json:"receipt_email"`
	BalanceTransaction ref[stBalanceTxn]      `json:"balance_transaction"`
	BillingDetails     struct{ Email string } `json:"billing_details"`
}

type stRefund struct {
	ID                 string            `json:"id"`
	Amount             int64             `json:"amount"`
	Currency           string            `json:"currency"`
	Charge             ref[struct{}]     `json:"charge"`
	Status             string            `json:"status"`
	Created            int64             `json:"created"`
	BalanceTransaction ref[stBalanceTxn] `json:"balance_transaction"`
}

type stCustomer struct {
	ID      string `json:"id"`
	Email   string `json:"email"`
	Deleted bool   `json:"deleted"`
}

type stSession struct {
	ID                string            `json:"id"`
	Mode              string            `json:"mode"`
	Status            string            `json:"status"`
	PaymentStatus     string            `json:"payment_status"`
	ClientReferenceID string            `json:"client_reference_id"`
	Metadata          map[string]string `json:"metadata"`
	Customer          ref[stCustomer]   `json:"customer"`
	CustomerDetails   *struct {
		Email string `json:"email"`
	} `json:"customer_details"`
	PaymentIntent ref[struct{}] `json:"payment_intent"`
	Invoice       ref[struct{}] `json:"invoice"`
	Subscription  ref[struct{}] `json:"subscription"`
	Created       int64         `json:"created"`
}

type stInvoice struct {
	ID            string            `json:"id"`
	BillingReason string            `json:"billing_reason"`
	CustomerEmail string            `json:"customer_email"`
	Metadata      map[string]string `json:"metadata"`
	Parent        *struct {
		Type                string `json:"type"`
		SubscriptionDetails *struct {
			Subscription ref[struct{}]     `json:"subscription"`
			Metadata     map[string]string `json:"metadata"`
		} `json:"subscription_details"`
	} `json:"parent"`
}

func (in *stInvoice) subscription() (id string, meta map[string]string) {
	if in.Parent != nil && in.Parent.SubscriptionDetails != nil {
		return in.Parent.SubscriptionDetails.Subscription.ID, in.Parent.SubscriptionDetails.Metadata
	}
	return "", nil
}

type stInvoicePayment struct {
	Invoice ref[struct{}] `json:"invoice"`
	Status  string        `json:"status"`
	Payment struct {
		Type          string        `json:"type"`
		PaymentIntent ref[struct{}] `json:"payment_intent"`
		Charge        ref[struct{}] `json:"charge"`
	} `json:"payment"`
}

type stSubscription struct {
	ID        string            `json:"id"`
	Status    string            `json:"status"`
	Customer  ref[stCustomer]   `json:"customer"`
	Currency  string            `json:"currency"`
	Metadata  map[string]string `json:"metadata"`
	StartDate int64             `json:"start_date"`
	EndedAt   int64             `json:"ended_at"`
	Items     struct {
		Data []struct {
			Quantity int64 `json:"quantity"`
			Price    struct {
				UnitAmount *int64 `json:"unit_amount"`
				Currency   string `json:"currency"`
				Recurring  *struct {
					Interval      string `json:"interval"`
					IntervalCount int64  `json:"interval_count"`
				} `json:"recurring"`
			} `json:"price"`
		} `json:"data"`
	} `json:"items"`
}

// monthly is the subscription's list price per month in its currency's
// smallest unit (items without a fixed unit price, like tiered or metered
// prices, are left out).
func (s *stSubscription) monthly() int64 {
	var total float64
	for _, it := range s.Items.Data {
		p := it.Price
		if p.UnitAmount == nil || p.Recurring == nil {
			continue
		}
		n := p.Recurring.IntervalCount
		if n <= 0 {
			n = 1
		}
		per := map[string]float64{"day": 365.0 / 12, "week": 52.0 / 12, "month": 1, "year": 1.0 / 12}[p.Recurring.Interval]
		q := it.Quantity
		if q == 0 {
			q = 1
		}
		total += float64(*p.UnitAmount*q) * per / float64(n)
	}
	return int64(total + 0.5)
}

type stEvent struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Created int64  `json:"created"`
	Data    struct {
		Object json.RawMessage `json:"object"`
	} `json:"data"`
}

type stWebhookEndpoint struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	Secret string `json:"secret"`
	Status string `json:"status"`
}

// ---- calls ----

func (c *stripeAPI) charge(ctx context.Context, id string) (*stCharge, error) {
	var ch stCharge
	return &ch, c.do(ctx, http.MethodGet, "/v1/charges/"+url.PathEscape(id), url.Values{"expand[]": {"balance_transaction"}}, &ch)
}

// chargesOf lists the charges of a PaymentIntent.
func (c *stripeAPI) chargesOf(ctx context.Context, pi string) ([]stCharge, error) {
	var out []stCharge
	err := c.list(ctx, "/v1/charges", url.Values{"payment_intent": {pi}, "expand[]": {"data.balance_transaction"}}, func(raw json.RawMessage) error {
		var ch stCharge
		if err := json.Unmarshal(raw, &ch); err != nil {
			return err
		}
		out = append(out, ch)
		return nil
	})
	return out, err
}

func (c *stripeAPI) refundsOf(ctx context.Context, charge string) ([]stRefund, error) {
	var out []stRefund
	err := c.list(ctx, "/v1/refunds", url.Values{"charge": {charge}, "expand[]": {"data.balance_transaction"}}, func(raw json.RawMessage) error {
		var r stRefund
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		out = append(out, r)
		return nil
	})
	return out, err
}

func (c *stripeAPI) balanceTxn(ctx context.Context, id string) (*stBalanceTxn, error) {
	var bt stBalanceTxn
	return &bt, c.do(ctx, http.MethodGet, "/v1/balance_transactions/"+url.PathEscape(id), nil, &bt)
}

func (c *stripeAPI) customer(ctx context.Context, id string) (*stCustomer, error) {
	var cu stCustomer
	return &cu, c.do(ctx, http.MethodGet, "/v1/customers/"+url.PathEscape(id), nil, &cu)
}

func (c *stripeAPI) invoice(ctx context.Context, id string) (*stInvoice, error) {
	var in stInvoice
	return &in, c.do(ctx, http.MethodGet, "/v1/invoices/"+url.PathEscape(id), nil, &in)
}

// invoicePayments lists an invoice's paid payments.
func (c *stripeAPI) invoicePayments(ctx context.Context, invoice string) ([]stInvoicePayment, error) {
	var out []stInvoicePayment
	err := c.list(ctx, "/v1/invoice_payments", url.Values{"invoice": {invoice}, "status": {"paid"}}, func(raw json.RawMessage) error {
		var p stInvoicePayment
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		out = append(out, p)
		return nil
	})
	return out, err
}

func (c *stripeAPI) createWebhook(ctx context.Context, project, target string) (*stWebhookEndpoint, error) {
	f := url.Values{"url": {target}, "api_version": {stripeVersion}, "description": {"Tiffin analytics: revenue for project " + project},
		"metadata[tiffin_project]": {project}}
	for _, e := range stripeEvents {
		f.Add("enabled_events[]", e)
	}
	var we stWebhookEndpoint
	return &we, c.do(ctx, http.MethodPost, "/v1/webhook_endpoints", f, &we)
}

func (c *stripeAPI) webhookURL(ctx context.Context, id, target string) error {
	f := url.Values{"url": {target}, "disabled": {"false"}}
	for _, e := range stripeEvents {
		f.Add("enabled_events[]", e)
	}
	return c.do(ctx, http.MethodPost, "/v1/webhook_endpoints/"+url.PathEscape(id), f, nil)
}

func (c *stripeAPI) deleteWebhook(ctx context.Context, id string) error {
	err := c.do(ctx, http.MethodDelete, "/v1/webhook_endpoints/"+url.PathEscape(id), nil, nil)
	var se *stripeError
	if errors.As(err, &se) && se.missing() {
		return nil
	}
	return err
}

func since(t time.Time) url.Values {
	return url.Values{"created[gte]": {strconv.FormatInt(t.Unix(), 10)}}
}
