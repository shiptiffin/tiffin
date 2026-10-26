package analytics

// Revenue attribution: payments from Stripe (stripe_connect.go,
// stripe_webhook.go) or from server code (revenue() in @shiptiffin/sdk,
// POST /revenue on the collector) are matched to the account that paid and
// so to where that account came from. Only with marketing attribution on.
//
// The matching chain, first hit wins for the account (matched_by):
//   - reference: the payment names the account (client_reference_id or
//     metadata.tiffin_user on Stripe; user in revenue());
//   - email: the Stripe customer's email is a sign-in user of the project;
//   - checkout: no account, but the buyer came back from this Checkout
//     Session to a visit (its touch is the credit);
//   - visit: no account, but revenue() was called in a visit going on;
//   - customer: an earlier payment by the same customer, whose match it
//     takes (it is a renewal).
// A payment that is not the customer's first is a renewal. Credit: first
// touch = the account's first visit (cookie) or sign-up visit; last touch =
// the visit the payment was made in (checkout return, revenue() request),
// else the sign-up visit.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shiptiffin/tiffin/internal/mod/auth"
)

const (
	providerStripe = "stripe"
	providerApp    = "app"
	stripePath     = "/_tiffin/stripe"
)

func init() {
	appPaths = append(appPaths, stripePath)
	collectorRoutes = append(collectorRoutes, func(m *Module, mux *http.ServeMux) {
		mux.HandleFunc(stripePath, m.stripeWebhook)
		mux.HandleFunc("/revenue", m.serverRevenue)
	})
	revenueConnected = func(ctx context.Context, m *Module, project string) bool {
		if _, ok := m.stripeConn(ctx, project); ok {
			return true
		}
		return m.attr != nil && m.attr.hasPayments(ctx, project, providerApp)
	}
}

// userByEmail finds a project's sign-in user by email, and userExists
// checks an id is one; tests replace them.
var (
	userByEmail = auth.UserByEmail
	userExists  = auth.UserExists
)

// revenueState is the module's revenue bookkeeping that is not in a store.
type revenueState struct {
	mu        sync.Mutex
	connMu    sync.Mutex // one change to a stored connection at a time
	wake      chan struct{}
	worker    sync.Once
	backfills map[string]*BackfillState // running back-fills by project
	lastSweep time.Time
	purged    time.Time
}

var revStates sync.Map // *Module → *revenueState

func (m *Module) rev() *revenueState {
	v, _ := revStates.LoadOrStore(m, &revenueState{wake: make(chan struct{}, 1), backfills: map[string]*BackfillState{}})
	return v.(*revenueState)
}

// hints are what other Stripe objects say about a charge.
type hints struct {
	Invoice, Subscription, Checkout, RefUser, Email, BillingReason string
}

// renewalReasons are invoice billing reasons of an existing subscription.
var renewalReasons = map[string]bool{"subscription_cycle": true, "subscription_update": true, "subscription_threshold": true}

// match runs the matching chain for p and saves the result. emailOf gives
// the payer's email for the email matcher (nil: none).
func (m *Module) match(ctx context.Context, project string, p *payment, emailOf func(context.Context) (string, error)) error {
	np := *p
	np.UserID, np.MatchedBy, np.Unmatched = "", "", ""
	np.FirstFrom, np.LastFrom, np.First, np.Last = "", "", Touch{}, Touch{}
	var why []string
	named := false
	if p.RefUser != "" {
		// The reference must be an account: one that signed up with
		// attribution on, or a sign-in user of the project.
		su, err := m.attr.signup(ctx, project, p.RefUser)
		if err != nil {
			return err
		}
		named = su != nil
		if !named {
			if ok, err := userExists(ctx, project, p.RefUser); err != nil {
				why = append(why, "sign-in users could not be checked ("+err.Error()+")")
			} else {
				named = ok
			}
		}
		if named {
			np.UserID, np.MatchedBy = p.RefUser, "reference"
		} else {
			why = append(why, "its reference ("+clip(p.RefUser, 40)+") is not an account of the project: set metadata.tiffin_user to the user id")
		}
	} else {
		why = append(why, "it names no account (metadata.tiffin_user or client_reference_id)")
	}
	if !named {
		if emailOf != nil {
			email, err := emailOf(ctx)
			switch {
			case err != nil:
				why = append(why, "the customer's email could not be read ("+err.Error()+")")
			case email == "":
				why = append(why, "the customer has no email")
			default:
				id, err := userByEmail(ctx, project, email)
				switch {
				case err != nil:
					why = append(why, "sign-in users could not be searched ("+err.Error()+")")
				case id == "":
					why = append(why, "no sign-in account has the customer's email")
				default:
					np.UserID, np.MatchedBy = id, "email"
				}
			}
		}
	}
	if np.UserID != "" {
		if su, err := m.attr.signup(ctx, project, np.UserID); err != nil {
			return err
		} else if su != nil {
			np.First, np.FirstFrom = su.Visit, "signup"
			if su.First != nil {
				np.First, np.FirstFrom = *su.First, "first-visit"
			}
			np.Last, np.LastFrom = su.Visit, "signup"
		}
	}
	// The visit the payment was made in: its last touch.
	switch {
	case p.Provider == providerApp && p.LastFrom == "visit":
		np.Last, np.LastFrom = p.Last, "visit"
		if np.FirstFrom == "" {
			np.First, np.FirstFrom = p.Last, "visit"
		}
		if np.MatchedBy == "" {
			np.MatchedBy = "visit"
		}
	case p.Checkout != "":
		t, ok, err := m.attr.checkout(ctx, project, p.Checkout)
		if err != nil {
			return err
		}
		if ok {
			np.Last, np.LastFrom = t, "checkout"
			if np.FirstFrom == "" {
				np.First, np.FirstFrom = t, "checkout"
			}
			if np.MatchedBy == "" {
				np.MatchedBy = "checkout"
			}
		} else if np.MatchedBy == "" {
			why = append(why, "no return from its Checkout page was seen")
		}
	default:
		if np.MatchedBy == "" {
			if p.Provider == providerApp {
				why = append(why, "no user was given and the call came from no visit going on")
			} else {
				why = append(why, "it did not come through Checkout")
			}
		}
	}
	if p.Kind != kindRefund {
		prior, err := m.attr.firstPayment(ctx, project, p.ID, p.Customer, np.UserID, p.At)
		if err != nil {
			return err
		}
		np.Kind = kindPayment
		if prior != nil || p.Renewal || renewalReasons[p.BillingReason] {
			np.Kind = kindRenewal
		}
		if np.MatchedBy == "" {
			if prior != nil && prior.MatchedBy != "" {
				np.UserID, np.MatchedBy = prior.UserID, "customer"
				np.First, np.FirstFrom, np.Last, np.LastFrom = prior.First, prior.FirstFrom, prior.Last, prior.LastFrom
			} else {
				why = append(why, "no earlier payment by this customer is matched")
			}
		} else if prior != nil && np.FirstFrom == "" && np.LastFrom == "" {
			// A renewal with nothing of its own to credit (no sign-up source,
			// no checkout) carries the first payment's credit.
			np.First, np.FirstFrom, np.Last, np.LastFrom = prior.First, prior.FirstFrom, prior.Last, prior.LastFrom
		}
	}
	if np.MatchedBy == "" {
		np.Unmatched = "Not matched: " + strings.Join(why, "; ") + "."
		// Never trade a match for none: a checkout return is kept 30 days,
		// sign-in users can be searched again later.
		if p.MatchedBy != "" {
			np.UserID, np.MatchedBy, np.Unmatched = p.UserID, p.MatchedBy, ""
			np.First, np.FirstFrom, np.Last, np.LastFrom = p.First, p.FirstFrom, p.Last, p.LastFrom
		}
	}
	if err := m.attr.setMatch(ctx, project, &np); err != nil {
		return err
	}
	*p = np
	return nil
}

// inherit gives a refund its payment's match.
func (m *Module) inherit(ctx context.Context, project string, r *payment) error {
	parent, err := m.attr.getPayment(ctx, project, r.Parent)
	if err != nil || parent == nil {
		return err
	}
	r.UserID, r.MatchedBy, r.Unmatched = parent.UserID, parent.MatchedBy, parent.Unmatched
	r.First, r.FirstFrom, r.Last, r.LastFrom = parent.First, parent.FirstFrom, parent.Last, parent.LastFrom
	return m.attr.setMatch(ctx, project, r)
}

// ---- revenue() from server code ----

type revenueBody struct {
	ID       string          `json:"id"`
	Amount   json.Number     `json:"amount"`
	Currency string          `json:"currency"`
	User     string          `json:"user"`
	Refund   json.RawMessage `json:"refund"` // true, or the id of the payment it refunds
	Renewal  bool            `json:"renewal"`
	At       *time.Time      `json:"at"`
	IP       string          `json:"ip"`
	UA       string          `json:"ua"`
	GPC      bool            `json:"gpc"`
}

var currencyRe = regexp.MustCompile(`^[a-zA-Z]{3}$`)

// serverRevenue records a payment from server code (@shiptiffin/sdk/analytics
// revenue()): for payment providers other than Stripe.
func (m *Module) serverRevenue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		reply(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	key := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	project, _, ok := m.store.LookupKey(r.Context(), key)
	if !ok {
		reply(w, http.StatusUnauthorized, map[string]string{"error": "send Authorization: Bearer $TIFFIN_ANALYTICS_KEY"})
		return
	}
	var b revenueBody
	dec := json.NewDecoder(io.LimitReader(r.Body, 16<<10))
	dec.UseNumber()
	if err := dec.Decode(&b); err != nil {
		reply(w, http.StatusBadRequest, map[string]string{"error": "body must be JSON {id, amount, currency, user, refund, renewal, at}"})
		return
	}
	b.ID = strings.TrimSpace(b.ID)
	amount, err := strconv.ParseInt(b.Amount.String(), 10, 64)
	refundOf, isRefund := "", false
	if s := strings.TrimSpace(string(b.Refund)); s == "true" {
		isRefund = true
	} else if strings.HasPrefix(s, `"`) {
		_ = json.Unmarshal(b.Refund, &refundOf)
		isRefund = refundOf != ""
	}
	at := time.Now().UTC()
	if b.At != nil {
		at = b.At.UTC()
	}
	switch {
	case b.ID == "" || len(b.ID) > 200:
		reply(w, http.StatusBadRequest, map[string]string{"error": "id is required (1-200 chars): your payment's own id, so a retry is not counted twice"})
		return
	case err != nil || amount <= 0 || amount > 1e12:
		reply(w, http.StatusBadRequest, map[string]string{"error": "amount is a whole number in the currency's smallest unit (cents): 1999 for 19.99"})
		return
	case !currencyRe.MatchString(b.Currency):
		reply(w, http.StatusBadRequest, map[string]string{"error": "currency is a three-letter ISO code, like usd or eur"})
		return
	case len(b.User) > 200 || len(refundOf) > 200:
		reply(w, http.StatusBadRequest, map[string]string{"error": "user and refund are ids of at most 200 chars"})
		return
	case time.Until(at) > time.Minute || time.Since(at) > 366*24*time.Hour:
		reply(w, http.StatusBadRequest, map[string]string{"error": "at must be in the last year, not in the future"})
		return
	}
	ctx := r.Context()
	if !m.sites.AnalyticsEnabled(ctx, project) {
		reply(w, http.StatusConflict, map[string]string{"error": "analytics is not enabled for project " + project})
		return
	}
	if !m.Settings(ctx, project).Attribution {
		reply(w, http.StatusConflict, map[string]string{"error": "revenue needs marketing attribution, which is off for project " + project +
			": a person turns it on in the dashboard (the project's Analytics page, Attribution)"})
		return
	}
	if _, ok := m.stripeConn(ctx, project); ok {
		reply(w, http.StatusAccepted, map[string]string{"ignored": "stripe connected"})
		return
	}
	id := "app:" + b.ID
	if old, err := m.attr.getPayment(ctx, project, id); err != nil {
		reply(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	} else if old != nil {
		reply(w, http.StatusAccepted, map[string]any{"ok": true, "duplicate": true, "matched": old.MatchedBy})
		return
	}
	cur := strings.ToLower(b.Currency)
	p := payment{ID: id, Provider: providerApp, Kind: kindPayment, At: at, Amount: amount, Currency: cur, AccountAmount: amount, AccountCurrency: cur,
		Status: "succeeded", RefUser: strings.TrimSpace(b.User), Renewal: b.Renewal}
	if isRefund {
		p.Kind, p.Amount, p.AccountAmount = kindRefund, -amount, -amount
		if refundOf != "" {
			p.Parent = "app:" + refundOf
		}
	}
	if !b.GPC && b.IP != "" && b.UA != "" { // the visitor did not object
		if t, ok := m.pipe.Visit(ctx, project, b.IP, b.UA, at); ok {
			p.Last, p.LastFrom = t, "visit"
		}
	}
	saved, err := m.attr.putPayment(ctx, project, p)
	if err == nil {
		if parent, _ := m.attr.getPayment(ctx, project, saved.Parent); parent != nil {
			err = m.inherit(ctx, project, saved)
		} else {
			err = m.match(ctx, project, saved, nil)
		}
	}
	if err != nil {
		reply(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	reply(w, http.StatusAccepted, map[string]any{"ok": true, "matched": saved.MatchedBy})
}

// clip shortens s to n runes for a reason shown to people.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
