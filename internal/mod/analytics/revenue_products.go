package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// Which payments are the project's. One Stripe account often sells several
// products (one per app), and every project connected to it sees all of
// its payments. A connection can count only some products: the payments
// for them (through their subscription, invoice or Checkout Session) and,
// if chosen, payments with no product at all. No choice counts every
// payment, as before. Payments keep their products whatever the choice, so
// changing it changes the report at once.

func init() {
	registerAttrSchema([]string{"stripe_items", "stripe_products"},
		// The products a subscription, an invoice or a Checkout Session was
		// for (kind sub, invoice or checkout; ref its id).
		`CREATE TABLE IF NOT EXISTS stripe_items (
			project TEXT NOT NULL, kind TEXT NOT NULL, ref TEXT NOT NULL, product TEXT NOT NULL,
			PRIMARY KEY(project, kind, ref, product)) WITHOUT ROWID`,
		// A product's name, from Stripe's products (with Products: Read) or a
		// Checkout line item's description.
		`CREATE TABLE IF NOT EXISTS stripe_products (
			project TEXT NOT NULL, id TEXT NOT NULL, name TEXT NOT NULL DEFAULT '', active INTEGER NOT NULL DEFAULT 1,
			PRIMARY KEY(project, id)) WITHOUT ROWID`,
	)
}

// Item kinds.
const (
	itemSub      = "sub"
	itemInvoice  = "invoice"
	itemCheckout = "checkout"
)

// putItems records the products ref was for.
func (s *attrStore) putItems(ctx context.Context, project, kind, ref string, products []string) error {
	if ref == "" || len(products) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range products {
		if p == "" {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO stripe_items (project, kind, ref, product) VALUES (?,?,?,?) ON CONFLICT DO NOTHING`, project, kind, ref, p); err != nil {
			return err
		}
	}
	return nil
}

// putProductName keeps a product's name; a name from Stripe's product list
// (fromCatalog) replaces one taken from a line item.
func (s *attrStore) putProductName(ctx context.Context, project, id, name string, fromCatalog, active bool) error {
	name = strings.TrimSpace(name)
	if id == "" || name == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	q := `INSERT INTO stripe_products (project, id, name, active) VALUES (?,?,?,?) ON CONFLICT(project, id) DO NOTHING`
	if fromCatalog {
		q = `INSERT INTO stripe_products (project, id, name, active) VALUES (?,?,?,?) ON CONFLICT(project, id) DO UPDATE SET name = excluded.name, active = excluded.active`
	}
	_, err := s.db.ExecContext(ctx, q, project, id, name, active)
	return err
}

// itemIndex is a project's products by kind and ref.
type itemIndex map[[2]string][]string

func (s *attrStore) items(ctx context.Context, project string) (itemIndex, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, ref, product FROM stripe_items WHERE project = ?`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := itemIndex{}
	for rows.Next() {
		var kind, ref, product string
		if err := rows.Scan(&kind, &ref, &product); err != nil {
			return nil, err
		}
		k := [2]string{kind, ref}
		out[k] = append(out[k], product)
	}
	return out, rows.Err()
}

// productsOf are the products a payment was for, through its subscription,
// invoice or Checkout Session.
func (ix itemIndex) productsOf(p *payment) []string {
	var out []string
	for _, k := range [][2]string{{itemSub, p.Subscription}, {itemInvoice, p.Invoice}, {itemCheckout, p.Checkout}} {
		if k[1] == "" {
			continue
		}
		for _, pr := range ix[k] {
			if !slices.Contains(out, pr) {
				out = append(out, pr)
			}
		}
	}
	return out
}

// productFilter decides which payments a connection counts. The zero
// value (no choice) counts every payment.
type productFilter struct {
	on        bool
	products  map[string]bool
	noProduct bool
	ix        itemIndex
	parents   map[string]*payment // refunds' payments, looked up once
	store     *attrStore
	project   string
}

func (m *Module) productFilter(ctx context.Context, project string, c *stripeConn) (*productFilter, error) {
	f := &productFilter{store: m.attr, project: project, parents: map[string]*payment{}}
	if c == nil || len(c.Products) == 0 && !c.NoProduct {
		return f, nil
	}
	ix, err := m.attr.items(ctx, project)
	if err != nil {
		return nil, err
	}
	f.on, f.ix, f.noProduct, f.products = true, ix, c.NoProduct, map[string]bool{}
	for _, p := range c.Products {
		f.products[p] = true
	}
	return f, nil
}

// keep reports whether the filter counts p (a refund counts as its payment does).
func (f *productFilter) keep(ctx context.Context, p *payment) bool {
	if f == nil || !f.on {
		return true
	}
	if p.Kind == kindRefund && p.Parent != "" {
		parent, ok := f.parents[p.Parent]
		if !ok {
			parent, _ = f.store.getPayment(ctx, f.project, p.Parent)
			f.parents[p.Parent] = parent
		}
		if parent != nil {
			p = parent
		}
	}
	prods := f.ix.productsOf(p)
	if len(prods) == 0 {
		return f.noProduct
	}
	for _, pr := range prods {
		if f.products[pr] {
			return true
		}
	}
	return false
}

// keepSub reports whether the filter counts a subscription in MRR.
func (f *productFilter) keepSub(id string) bool {
	if f == nil || !f.on {
		return true
	}
	prods := f.ix[[2]string{itemSub, id}]
	if len(prods) == 0 {
		return f.noProduct
	}
	for _, pr := range prods {
		if f.products[pr] {
			return true
		}
	}
	return false
}

func (f *productFilter) filter(ctx context.Context, rows []*payment) []*payment {
	if f == nil || !f.on {
		return rows
	}
	out := rows[:0:0]
	for _, p := range rows {
		if f.keep(ctx, p) {
			out = append(out, p)
		}
	}
	return out
}

// ---- products from Stripe objects ----

type stLineItem struct {
	Description string `json:"description"`
	Price       *struct {
		Product ref[struct {
			Name string `json:"name"`
		}] `json:"product"`
	} `json:"price"`
	// Invoice lines (API 2025-03-31.basil and later) name it under pricing.
	Pricing *struct {
		PriceDetails *struct {
			Product string `json:"product"`
		} `json:"price_details"`
	} `json:"pricing"`
}

func (li *stLineItem) product() string {
	if li.Pricing != nil && li.Pricing.PriceDetails != nil && li.Pricing.PriceDetails.Product != "" {
		return li.Pricing.PriceDetails.Product
	}
	if li.Price != nil {
		return li.Price.Product.ID
	}
	return ""
}

// recordLineItems keeps the products of a list of line items (and names
// from Checkout's line item descriptions, which are the product's name).
func (m *Module) recordLineItems(ctx context.Context, project, kind, ref string, items []stLineItem) error {
	var prods []string
	for i := range items {
		p := items[i].product()
		if p == "" {
			continue
		}
		prods = append(prods, p)
		name := ""
		if items[i].Price != nil && items[i].Price.Product.Obj != nil {
			name = items[i].Price.Product.Obj.Name
		}
		if name == "" && kind == itemCheckout {
			name = items[i].Description
		}
		if err := m.attr.putProductName(ctx, project, p, name, false, true); err != nil {
			return err
		}
	}
	return m.attr.putItems(ctx, project, kind, ref, prods)
}

// sessionItems fetches and records a Checkout Session's products (its event
// carries no line items).
func (m *Module) sessionItems(ctx context.Context, project string, sa *stripeAPI, id string) error {
	var items []stLineItem
	err := sa.list(ctx, "/v1/checkout/sessions/"+url.PathEscape(id)+"/line_items", nil, func(raw json.RawMessage) error {
		var li stLineItem
		if err := json.Unmarshal(raw, &li); err != nil {
			return err
		}
		items = append(items, li)
		return nil
	})
	if err != nil {
		return err
	}
	return m.recordLineItems(ctx, project, itemCheckout, id, items)
}

// catalog reads the account's products into stripe_products. It needs
// Products: Read, which keys made before the product choice may lack: then
// names come from Checkout line items only, and ok is false.
func (m *Module) catalog(ctx context.Context, project string, sa *stripeAPI) (ok bool, err error) {
	err = sa.list(ctx, "/v1/products", nil, func(raw json.RawMessage) error {
		var p struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Active bool   `json:"active"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		return m.attr.putProductName(ctx, project, p.ID, p.Name, true, p.Active)
	})
	var se *stripeError
	if errors.As(err, &se) && (se.Status == http.StatusForbidden || se.Status == http.StatusUnauthorized) {
		return false, nil
	}
	return err == nil, err
}

// ---- the choice ----

// StripeProduct is one product the project's Stripe account sells or sold.
type StripeProduct struct {
	ID       string    `json:"id"`
	Name     string    `json:"name" doc:"Its name in Stripe (the id when the box doesn't know it)"`
	Active   bool      `json:"active"`
	Payments int64     `json:"payments" doc:"Payments for it imported so far (all time, refunds left out)"`
	Last     time.Time `json:"last,omitzero" doc:"Its latest payment"`
	Counted  bool      `json:"counted" doc:"The project's revenue counts it"`
}

// StripeProducts is the connection's product choice.
type StripeProducts struct {
	Project   string          `json:"project"`
	All       bool            `json:"all" doc:"Every payment in the Stripe account counts (no choice made)"`
	NoProduct bool            `json:"noProduct" doc:"Payments with no product (a charge outside Checkout and invoices) count"`
	NoneCount int64           `json:"noProductPayments" doc:"Payments with no product, imported so far"`
	Catalog   bool            `json:"catalog" doc:"Names come from Stripe's product list (the key has Products: Read); otherwise only from Checkout line items"`
	Products  []StripeProduct `json:"products"`
}

func (m *Module) stripeProducts(ctx context.Context, project string, c *stripeConn) (*StripeProducts, error) {
	out := &StripeProducts{Project: project, All: len(c.Products) == 0 && !c.NoProduct, NoProduct: c.NoProduct, Catalog: c.Catalog, Products: []StripeProduct{}}
	ix, err := m.attr.items(ctx, project)
	if err != nil {
		return nil, err
	}
	byID := map[string]*StripeProduct{}
	get := func(id string) *StripeProduct {
		p := byID[id]
		if p == nil {
			p = &StripeProduct{ID: id, Name: id, Active: true, Counted: out.All || slices.Contains(c.Products, id)}
			byID[id] = p
		}
		return p
	}
	rows, err := m.attr.db.QueryContext(ctx, `SELECT id, name, active FROM stripe_products WHERE project = ?`, project)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, name string
		var active bool
		if err := rows.Scan(&id, &name, &active); err != nil {
			rows.Close()
			return nil, err
		}
		p := get(id)
		p.Name, p.Active = name, active
	}
	rows.Close()
	pays, err := m.attr.paymentsWhere(ctx, `project = ? AND provider = ? AND kind != 'refund' AND status NOT IN ('failed', 'canceled')`, project, providerStripe)
	if err != nil {
		return nil, err
	}
	for _, pay := range pays {
		prods := ix.productsOf(pay)
		if len(prods) == 0 {
			out.NoneCount++
		}
		for _, id := range prods {
			p := get(id)
			p.Payments++
			if pay.At.After(p.Last) {
				p.Last = pay.At
			}
		}
	}
	for _, c := range c.Products { // chosen, but nothing known about it yet
		get(c)
	}
	for _, p := range byID {
		out.Products = append(out.Products, *p)
	}
	sort.Slice(out.Products, func(i, j int) bool {
		a, b := out.Products[i], out.Products[j]
		if a.Active != b.Active {
			return a.Active
		}
		if a.Payments != b.Payments {
			return a.Payments > b.Payments
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return out, nil
}

type productsIn struct {
	Body struct {
		Project   string   `json:"project" doc:"Project"`
		All       bool     `json:"all,omitempty" doc:"Count every payment in the Stripe account (clears the choice)"`
		Products  []string `json:"products,omitempty" maxItems:"200" doc:"Stripe product ids (prod_...) whose payments count"`
		NoProduct bool     `json:"noProduct,omitempty" doc:"Also count payments with no product"`
	}
}

func (m *Module) registerProductsAPI(a huma.API) {
	huma.Register(a, api.Untrusted(api.Op("analytics-stripe-products", http.MethodGet, "/v1/analytics/stripe/products", "analytics stripe products", api.RiskRead,
		"List the Stripe products and which count",
		"The products the project's Stripe account sells or sold (names from Stripe's product list when the key has Products: Read, else from Checkout line items), "+
			"with how many imported payments each had, and which the project's revenue counts. One Stripe account often sells several products, one per app: "+
			"choose this project's with analytics stripe products set; with no choice every payment counts.", "analytics")),
		api.Wrap(func(ctx context.Context, in *struct {
			Project string `query:"project" required:"true" doc:"Project"`
		}) (*struct{ Body *StripeProducts }, error) {
			if err := m.ready(ctx, in.Project); err != nil {
				return nil, err
			}
			c, ok := m.stripeConn(ctx, in.Project)
			if !ok {
				return nil, api.NewProblem(404, "not_found", "Stripe is not connected to project "+in.Project)
			}
			if !c.Catalog {
				if sa, err := m.stripeClient(c); err == nil {
					if got, err := m.catalog(ctx, in.Project, sa); err == nil && got {
						_ = m.setCatalog(ctx, in.Project)
						c.Catalog = true
					}
				}
			}
			out, err := m.stripeProducts(ctx, in.Project, c)
			if err != nil {
				return nil, err
			}
			return &struct{ Body *StripeProducts }{out}, nil
		}))

	huma.Register(a, api.Op("analytics-stripe-products-set", http.MethodPost, "/v1/analytics/stripe/products", "analytics stripe products set", api.RiskWrite,
		"Choose which Stripe products count",
		"Sets which payments the project's revenue counts: those for the given products (through their subscription, invoice or Checkout Session), "+
			"plus payments with no product when noProduct is set; all: true counts every payment again. Nothing is deleted or imported again: the report changes at once.", "analytics"),
		api.Wrap(func(ctx context.Context, in *productsIn) (*struct{ Body *StripeProducts }, error) {
			b := in.Body
			if err := m.ready(ctx, b.Project); err != nil {
				return nil, err
			}
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeApplyReversible, b.Project); err != nil {
				return nil, err
			}
			if !b.All && len(b.Products) == 0 && !b.NoProduct {
				p := api.NewProblem(422, "validation", "choose at least one product, or noProduct, or all")
				p.Hint = "analytics stripe products lists them"
				return nil, p
			}
			var prods []string
			if !b.All {
				for _, id := range b.Products {
					id = strings.TrimSpace(id)
					if !strings.HasPrefix(id, "prod_") || len(id) > 100 {
						p := api.NewProblem(422, "validation", "not a Stripe product id: "+id)
						p.Hint = "product ids start with prod_; analytics stripe products lists them"
						return nil, p
					}
					if !slices.Contains(prods, id) {
						prods = append(prods, id)
					}
				}
				sort.Strings(prods)
			}
			c, err := m.setProducts(ctx, b.Project, prods, !b.All && b.NoProduct)
			if err != nil {
				return nil, err
			}
			out, err := m.stripeProducts(ctx, b.Project, c)
			if err != nil {
				return nil, err
			}
			return &struct{ Body *StripeProducts }{out}, nil
		}))
}

// setProducts saves the connection's product choice (none: every payment).
func (m *Module) setProducts(ctx context.Context, project string, products []string, noProduct bool) (*stripeConn, error) {
	var out *stripeConn
	err := m.updateStripeConn(ctx, project, func(c *stripeConn) bool {
		c.Products, c.NoProduct = products, noProduct
		cp := *c
		out = &cp
		return true
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, api.NewProblem(404, "not_found", "Stripe is not connected to project "+project)
	}
	return out, nil
}

func (m *Module) setCatalog(ctx context.Context, project string) error {
	return m.updateStripeConn(ctx, project, func(c *stripeConn) bool {
		c.Catalog = true
		return true
	})
}

// firstPaymentsFiltered is firstPayments counting only the payments the
// product choice keeps (without a choice, firstPayments itself).
func (m *Module) firstPaymentsFiltered(ctx context.Context, project, provider, currency string, pf *productFilter) (map[string]time.Time, error) {
	if pf == nil || !pf.on {
		return m.attr.firstPayments(ctx, project, provider, currency)
	}
	rows, err := m.attr.paymentsWhere(ctx, `project = ? AND provider = ? AND account_currency = ? AND kind != 'refund'`, project, provider, currency)
	if err != nil {
		return nil, err
	}
	out := map[string]time.Time{}
	for _, p := range pf.filter(ctx, rows) {
		k := p.customerKey()
		if t, ok := out[k]; !ok || p.At.Before(t) {
			out[k] = p.At
		}
	}
	return out, nil
}
