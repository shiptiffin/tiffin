package domains

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/dnskit"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// Buying a domain: Cloudflare Registrar, in the owner's own Cloudflare
// account (the token connected for DNS), paid with the card on that
// account at cost. The box never resells or holds domains; it searches,
// shows the live price, and registers only with that price confirmed.

// Purchase states, as the dashboard shows them.
const (
	BuyRegistering = "registering"
	BuyAction      = "action_required"
	BuyBlocked     = "blocked"
	BuyFailed      = "failed"
	BuyDNS         = "setting_up_dns"
	BuyReady       = "ready"
)

// buyKit is how the box reaches the registrar; tests point it at a fake.
type buyKit struct {
	base    string
	http    *http.Client
	sandbox bool

	buying   sync.Mutex // one purchase decision at a time
	mu       sync.Mutex // the times below
	zonesAt  time.Time  // the last zone refresh while waiting for a new zone
	statusAt map[string]time.Time
}

// TIFFIN_REGISTRAR_SANDBOX=1 buys in Cloudflare's registrar sandbox (no
// charge, .com and .net only, no zone), for trying the flow end to end.
var buyKitDefault = buyKit{sandbox: os.Getenv("TIFFIN_REGISTRAR_SANDBOX") == "1"}

// BuyReadiness says whether this box can buy domains, and if not, the fix.
type BuyReadiness struct {
	CanBuy  bool   `json:"canBuy"`
	Why     string `json:"why,omitempty" doc:"Why it can't, in a sentence."`
	Fix     string `json:"fix,omitempty" doc:"What to do about it."`
	FixURL  string `json:"fixUrl,omitempty" doc:"Where to do it: a dashboard path (/settings/dns) or Cloudflare's dashboard."`
	Sandbox bool   `json:"sandbox,omitempty" doc:"Purchases go to Cloudflare's registrar sandbox: nothing is charged and no zone is made."`
}

// Offer is one name from a search, with its live price when it can be bought here.
type Offer struct {
	Domain    string `json:"domain"`
	Available bool   `json:"available" doc:"It can be bought here now."`
	Exact     bool   `json:"exact,omitempty" doc:"The name searched for."`
	Premium   bool   `json:"premium,omitempty"`
	Checked   bool   `json:"checked" doc:"Availability and price come from a live check with the registry (otherwise from Cloudflare's search, which can lag)."`
	FirstYear string `json:"firstYear,omitempty" doc:"First-year price, e.g. 10.44"`
	Renewal   string `json:"renewal,omitempty" doc:"Price of each later year"`
	Currency  string `json:"currency,omitempty"`
	Reason    string `json:"reason,omitempty" doc:"Why it can't be bought here: domain_unavailable, domain_premium, extension_not_supported_via_api, extension_not_supported, extension_disallows_registration"`
	Why       string `json:"why,omitempty" doc:"The reason in words."`
	Link      string `json:"link,omitempty" doc:"Where it can be bought instead (Cloudflare's dashboard), when it can."`
}

// SearchResult is a domain search.
type SearchResult struct {
	Query  string       `json:"query"`
	Offers []Offer      `json:"offers"`
	Buy    BuyReadiness `json:"buy"`
}

// Use is a name under a domain that a project's app serves.
type Use struct {
	Project string `json:"project"`
	App     string `json:"app,omitempty"`
	Host    string `json:"host"`
}

// OwnedDomain is a domain the box can put on projects.
type OwnedDomain struct {
	Domain     string     `json:"domain"`
	Manageable bool       `json:"manageable" doc:"The connected DNS provider holds its zone, so adding it to a project sets the records too."`
	Registered bool       `json:"registered" doc:"Registered with Cloudflare Registrar in this account (renewal and auto-renew below)."`
	Status     string     `json:"status,omitempty" doc:"The registration's status: active, expired, …"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	AutoRenew  *bool      `json:"autoRenew,omitempty"`
	UsedBy     []Use      `json:"usedBy"`
	Box        string     `json:"box,omitempty" doc:"The box itself uses it: dashboard, apps, or both."`
	Purchase   *Purchase  `json:"purchase,omitempty" doc:"Bought from this box: who, when, and how far setup got."`
}

// Owned is every domain the box can put on projects.
type Owned struct {
	Domains []OwnedDomain `json:"domains"`
	Buy     BuyReadiness  `json:"buy"`
}

// Purchase is a domain bought from this box and how far it got.
type Purchase struct {
	Domain    string     `json:"domain"`
	State     string     `json:"state" enum:"registering,action_required,blocked,failed,setting_up_dns,ready"`
	Detail    string     `json:"detail" doc:"Where it is, in a sentence."`
	SentTo    string     `json:"sentTo,omitempty" doc:"action_required: the inbox the registry's confirmation email went to."`
	Years     int        `json:"years"`
	Total     string     `json:"total"`
	Currency  string     `json:"currency"`
	AutoRenew bool       `json:"autoRenew"`
	Project   string     `json:"project,omitempty" doc:"The project it was bought for, if any."`
	App       string     `json:"app,omitempty"`
	By        string     `json:"by"`
	At        time.Time  `json:"at"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	Sandbox   bool       `json:"sandbox,omitempty"`
	Error     string     `json:"error,omitempty"`
}

var errNoCloudflare = errors.New("no Cloudflare connected")

// word is a search for one name without an ending: acme, my-shop.
var word = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// cfState is the connected Cloudflare provider, nil without one.
func (m *Module) cfState() *providerState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.provs["cloudflare"]
}

// registrar opens Cloudflare Registrar with the stored token, finding (and
// remembering) the account id the first time.
func (m *Module) registrar(ctx context.Context) (*dnskit.Registrar, error) {
	st := m.cfState()
	if st == nil || st.err != nil {
		return nil, errNoCloudflare
	}
	raw, err := m.p.Secrets.Unseal(st.rec.Sealed)
	if err != nil {
		return nil, fmt.Errorf("read the stored Cloudflare token: %w", err)
	}
	var creds map[string]string
	if err := json.Unmarshal(raw, &creds); err != nil {
		return nil, err
	}
	k := m.kit()
	m.mu.Lock()
	acct := st.rec.Account
	m.mu.Unlock()
	if acct == "" {
		actx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		id, _, err := dnskit.CloudflareAccount(actx, k.http, k.base, creds["token"])
		if err != nil {
			return nil, fmt.Errorf("find the Cloudflare account: %w", err)
		}
		acct = id
		m.mu.Lock()
		st.rec.Account = id
		rec := st.rec
		m.mu.Unlock()
		b, _ := json.Marshal(rec)
		_ = m.p.DB.KVPut(ctx, ns, "provider/cloudflare", b)
	}
	return &dnskit.Registrar{Token: creds["token"], Account: acct, Sandbox: k.sandbox, Base: k.base, HTTP: k.http}, nil
}

func (m *Module) kit() *buyKit {
	if m.buy == nil {
		return &buyKitDefault
	}
	return m.buy
}

// readiness turns a registrar error into what the person should do.
func (m *Module) readiness(err error) BuyReadiness {
	r := BuyReadiness{CanBuy: err == nil, Sandbox: m.kit().sandbox}
	var re *dnskit.RegistrarError
	switch {
	case err == nil:
	case errors.Is(err, errNoCloudflare):
		r.Why = "Domains are bought through your own Cloudflare account, and this box isn't connected to Cloudflare."
		r.Fix = "Connect Cloudflare in Settings › DNS."
		r.FixURL = "/settings/dns"
	case errors.As(err, &re) && re.Forbidden():
		r.Why = "Your Cloudflare token can't search for or buy domains."
		r.Fix = "In Cloudflare, edit the token: add Account › Registrar Domains › Admin, and set Zone Resources to all zones so the box reaches domains you buy. Its value stays the same, so there's nothing to paste again."
		r.FixURL = "https://dash.cloudflare.com/profile/api-tokens"
	default:
		r.Why = err.Error()
		r.Fix = "Try again in a minute."
	}
	return r
}

func dashLink(acct string) string {
	return "https://dash.cloudflare.com/" + acct + "/domains/registrations"
}

// offer words a registrar answer.
func offer(o dnskit.Offer, checked bool, acct string) Offer {
	out := Offer{Domain: o.Name, Checked: checked, Premium: o.Tier == "premium", Reason: o.Reason}
	ext := o.Name[strings.Index(o.Name, ".")+1:]
	if o.Registrable && o.Pricing != nil && !out.Premium {
		out.Available = true
		out.FirstYear, out.Renewal, out.Currency = money(o.Pricing.RegistrationCost), money(o.Pricing.RenewalCost), o.Pricing.Currency
		return out
	}
	switch {
	case out.Premium || o.Reason == "domain_premium":
		out.Premium, out.Reason = true, "domain_premium"
		out.Why = "A premium name: the registry sets a higher price, and Cloudflare sells those only in its dashboard."
		out.Link = dashLink(acct)
	case o.Reason == "extension_not_supported_via_api":
		out.Why = "Cloudflare sells ." + ext + " only in its own dashboard for now."
		out.Link = dashLink(acct)
	case o.Reason == "extension_not_supported":
		out.Why = "Cloudflare doesn't sell ." + ext + " names."
	case o.Reason == "extension_disallows_registration":
		out.Why = "The ." + ext + " registry isn't taking new names."
	default:
		out.Reason = "domain_unavailable"
		out.Why = "Someone already has it."
	}
	return out
}

func money(s string) string {
	if c, ok := dnskit.Cents(s); ok {
		return dnskit.Money(c)
	}
	return s
}

// topEndings are checked for a plain word (acme → acme.com, acme.app, …),
// so the name itself shows even when Cloudflare's suggestions skip it.
var topEndings = []string{"com", "app", "dev", "io"}

// checkWait is how long a search waits for live answers before it shows
// Cloudflare's cached ones (buying checks again anyway).
const checkWait = 2500 * time.Millisecond

// search asks Cloudflare for names like q and, at the same time, checks the
// name itself live; with all, it then checks the suggestions live too, in
// parallel batches, for at most checkWait (else the caller checks them).
func (m *Module) search(ctx context.Context, q string, all bool) (SearchResult, error) {
	res := SearchResult{Query: q, Offers: []Offer{}}
	reg, err := m.registrar(ctx)
	if err != nil {
		res.Buy = m.readiness(err)
		return res, nil
	}
	var top []string
	exact := ""
	if d, verr := validDomain(q); verr == nil && !strings.Contains(q, " ") {
		exact, top = d, []string{d}
	} else if word.MatchString(q) {
		for _, e := range topEndings {
			top = append(top, q+"."+e)
		}
	}
	live := map[string]dnskit.Offer{}
	var liveMu sync.Mutex
	check := func(names []string, wait time.Duration) {
		cctx, cancel := context.WithTimeout(ctx, wait)
		defer cancel()
		if got, err := reg.Check(cctx, names); err == nil {
			liveMu.Lock()
			for _, o := range got {
				live[o.Name] = o
			}
			liveMu.Unlock()
		}
	}
	var wg sync.WaitGroup
	if len(top) > 0 {
		wg.Go(func() { check(top, 8*time.Second) })
	}
	t0 := time.Now()
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	found, err := reg.Search(sctx, q, 20)
	took := time.Since(t0)
	if err != nil {
		wg.Wait()
		res.Buy = m.readiness(err)
		if res.Buy.CanBuy || res.Buy.FixURL == "" {
			return res, err
		}
		return res, nil
	}
	var rest []string
	for _, o := range found {
		if !slices.Contains(top, o.Name) {
			rest = append(rest, o.Name)
		}
	}
	if all {
		for b := range slices.Chunk(rest, 10) {
			wg.Go(func() { check(b, checkWait) })
		}
	}
	wg.Wait()
	if m.p.Log != nil {
		m.p.Log.Info("domains: search", "q", q, "search", took.Round(time.Millisecond), "total", time.Since(t0).Round(time.Millisecond), "live", len(live), "found", len(found))
	}

	cached := map[string]dnskit.Offer{}
	for _, o := range found {
		cached[o.Name] = o
	}
	add := func(name string) {
		o, checked := live[name]
		if !checked {
			var ok bool
			if o, ok = cached[name]; !ok {
				return // neither answered for it
			}
		}
		res.Offers = append(res.Offers, offer(o, checked, reg.Account))
	}
	// The name asked for (or a word's top endings) first, then Cloudflare's suggestions in its order.
	for _, n := range top {
		add(n)
	}
	for _, n := range rest {
		add(n)
	}
	// The one to show big: the domain typed, or a word's first top ending that's free.
	for i := range res.Offers {
		o := &res.Offers[i]
		if (exact != "" && o.Domain == exact) || (exact == "" && slices.Contains(top, o.Domain) && o.Available) {
			o.Exact = true
			break
		}
	}
	sort.SliceStable(res.Offers, func(i, j int) bool { return res.Offers[i].Exact && !res.Offers[j].Exact })
	res.Buy = BuyReadiness{CanBuy: true, Sandbox: m.kit().sandbox}
	return res, nil
}

// ---- purchases ----

type purchaseRec struct {
	Purchase
	Action string `json:"action,omitempty"`
}

func (m *Module) loadPurchase(ctx context.Context, domain string) *purchaseRec {
	b, ok, err := m.p.DB.KVGet(ctx, ns, "purchase/"+domain)
	if err != nil || !ok {
		return nil
	}
	var r purchaseRec
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	return &r
}

func (m *Module) savePurchase(ctx context.Context, r *purchaseRec) {
	b, _ := json.Marshal(r)
	_ = m.p.DB.KVPut(ctx, ns, "purchase/"+r.Domain, b)
}

func (m *Module) purchases(ctx context.Context) map[string]*purchaseRec {
	out := map[string]*purchaseRec{}
	all, err := m.p.DB.KVList(ctx, ns)
	if err != nil {
		return out
	}
	for k, v := range all {
		d, ok := strings.CutPrefix(k, "purchase/")
		if !ok {
			continue
		}
		var r purchaseRec
		if json.Unmarshal(v, &r) == nil {
			out[d] = &r
		}
	}
	return out
}

// detail words a purchase's state.
func detail(r *purchaseRec) string {
	switch r.State {
	case BuyRegistering:
		return "Cloudflare is registering it. This usually takes under a minute."
	case BuyAction:
		if r.SentTo != "" {
			return "Registered, but the registry wants the owner's email confirmed. Open the email sent to " + r.SentTo + " and confirm it; until then the domain may not work."
		}
		return "Cloudflare needs something from you before it can finish. Open your Cloudflare dashboard to see what."
	case BuyBlocked:
		return "Waiting on the registry. This can take a while; the box keeps checking."
	case BuyFailed:
		if r.Error != "" {
			return "It didn't go through: " + strings.TrimSuffix(r.Error, ".") + "."
		}
		return "It didn't go through."
	case BuyDNS:
		return "Registered. Cloudflare is setting up its DNS, which takes a minute or two."
	case BuyReady:
		if r.Sandbox {
			return r.Domain + " is registered in Cloudflare's sandbox (nothing was charged, and sandbox domains have no DNS)."
		}
		if r.ExpiresAt != nil {
			return r.Domain + " is yours. It renews on " + r.ExpiresAt.Format("2 January 2006") + "."
		}
		return r.Domain + " is yours."
	}
	return ""
}

// advance asks Cloudflare how a purchase is getting on and moves it along.
func (m *Module) advance(ctx context.Context, reg *dnskit.Registrar, r *purchaseRec) {
	before := r.State
	switch r.State {
	case BuyRegistering, BuyAction, BuyBlocked:
		k := m.kit()
		k.mu.Lock()
		if k.statusAt == nil {
			k.statusAt = map[string]time.Time{}
		}
		recent := m.now().Sub(k.statusAt[r.Domain]) < 2*time.Second
		if !recent {
			k.statusAt[r.Domain] = m.now()
		}
		k.mu.Unlock()
		if recent {
			break
		}
		sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		w, err := reg.Status(sctx, r.Domain)
		cancel()
		if err != nil {
			break // try again on the next look
		}
		m.applyWorkflow(r, w)
	}
	if r.State == BuyDNS {
		if r.Sandbox {
			r.State = BuyReady
		} else if m.zoneHeld(r.Domain) || m.refreshZones(ctx) && m.zoneHeld(r.Domain) {
			r.State = BuyReady
		}
	}
	if r.State == BuyDNS && !r.Sandbox && m.now().Sub(r.At) > 10*time.Minute {
		// Registered long ago and the token still can't see the zone: it's limited to some zones.
		if d := "Registered, but the box's Cloudflare token can't reach " + r.Domain + ". In Cloudflare, edit the token and set Zone Resources to all zones; the box picks it up within a minute."; r.Detail != d {
			r.Detail = d
			m.savePurchase(ctx, r)
		}
	}
	if r.State != before {
		r.Detail = detail(r)
		m.savePurchase(ctx, r)
		if r.State == BuyReady || r.State == BuyFailed {
			_ = m.p.DB.Audit(ctx, r.By, "domains.buy."+r.State, r.Domain, map[string]any{"total": r.Total, "currency": r.Currency, "years": r.Years})
		}
	}
}

func (m *Module) applyWorkflow(r *purchaseRec, w *dnskit.Workflow) {
	switch w.State {
	case "succeeded":
		r.State = BuyDNS
		if w.Context.Registration != nil {
			r.ExpiresAt = w.Context.Registration.ExpiresAt
		}
	case "failed":
		r.State = BuyFailed
		if w.Error != nil {
			r.Error = w.Error.Message
		}
	case "action_required":
		r.State, r.Action, r.SentTo = BuyAction, w.Context.Action, w.Context.ConfirmationSentTo
	case "blocked":
		r.State = BuyBlocked
	default:
		r.State = BuyRegistering
	}
	r.Detail = detail(r)
}

// zoneHeld: the connected Cloudflare token reaches the domain's zone.
func (m *Module) zoneHeld(domain string) bool {
	st := m.cfState()
	if st == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Contains(st.rec.Zones, domain)
}

// refreshZones lists the Cloudflare token's zones again (at most every 5 s),
// so a domain just bought can be put on projects. It reports whether it ran.
func (m *Module) refreshZones(ctx context.Context) bool {
	st := m.cfState()
	if st == nil || st.prov == nil {
		return false
	}
	k := m.kit()
	k.mu.Lock()
	if m.now().Sub(k.zonesAt) < 5*time.Second {
		k.mu.Unlock()
		return false
	}
	k.zonesAt = m.now()
	k.mu.Unlock()
	zctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	zones, err := dnskit.Zones(zctx, st.prov)
	if err != nil {
		return false
	}
	m.mu.Lock()
	st.rec.Zones, st.rec.ZonesAt = zones, m.now()
	rec := st.rec
	m.mu.Unlock()
	b, _ := json.Marshal(rec)
	_ = m.p.DB.KVPut(ctx, ns, "provider/cloudflare", b)
	return true
}

// sandboxContact: the registrar sandbox has no default contact, so test
// purchases there send this made-up one.
var sandboxContact = json.RawMessage(`{"registrant":{"email":"sandbox@example.com","phone":"+1.5555555555",` +
	`"postal_info":{"name":"Tiffin Sandbox","address":{"street":"1 Test Street","city":"Testville","state":"CA","postal_code":"94000","country_code":"US"}}}}`)

type buyIn struct {
	Body struct {
		Domain    string `json:"domain" maxLength:"253" doc:"The name to buy, e.g. acme.app"`
		Years     int    `json:"years,omitempty" minimum:"1" maximum:"10" doc:"How many years to register for (default 1)."`
		AutoRenew bool   `json:"autoRenew" doc:"Renew it every year with the card on the Cloudflare account, 30 days before it expires. Required: say yes or no."`
		Price     string `json:"price,omitempty" maxLength:"20" doc:"The total you agreed to pay now (the quote's total). Without it, or when the live price differs, nothing is bought: you get 428 with the quote."`
		Project   string `json:"project,omitempty" maxLength:"64" doc:"The project it's for (remembered for the dashboard; adding it to the project is a separate domains add)."`
		App       string `json:"app,omitempty" maxLength:"64"`
	}
}

func (m *Module) buyDomain(ctx context.Context, in *buyIn) (*Purchase, error) {
	d, err := validDomain(in.Body.Domain)
	if err != nil {
		return nil, err
	}
	reg, err := m.registrar(ctx)
	if err != nil {
		rd := m.readiness(err)
		return nil, &api.Problem{Status: http.StatusPreconditionFailed, Title: "Can't buy domains yet", Code: "precondition", Detail: rd.Why, Hint: rd.Fix}
	}
	k := m.kit()
	k.buying.Lock()
	defer k.buying.Unlock()
	if r := m.loadPurchase(ctx, d); r != nil && r.State != BuyFailed {
		m.advance(ctx, reg, r)
		return &r.Purchase, nil // already bought (or on its way) from this box
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if have, err := reg.Registration(cctx, d); err == nil && have != nil {
		return nil, api.NewProblem(http.StatusConflict, "conflict", d+" is already registered in your Cloudflare account; add it to a project from Domains")
	}
	checked, err := reg.Check(cctx, []string{d})
	if err != nil {
		rd := m.readiness(err)
		return nil, &api.Problem{Status: http.StatusBadGateway, Title: "Couldn't check the price", Code: "precondition", Detail: rd.Why, Hint: rd.Fix}
	}
	if len(checked) == 0 {
		return nil, api.NewProblem(http.StatusConflict, "conflict", "Cloudflare can't sell "+d)
	}
	of := offer(checked[0], true, reg.Account)
	if !of.Available {
		return nil, api.NewProblem(http.StatusConflict, "conflict", of.Why)
	}
	q, _ := dnskit.QuoteFor(checked[0], in.Body.Years)
	agreed, ok := dnskit.Cents(in.Body.Price)
	total, _ := dnskit.Cents(q.Total)
	if !ok || agreed != total {
		detailText := fmt.Sprintf("%s costs %s %s now (renews at %s a year). Send price %q to buy it.", d, q.Total, q.Currency, q.Renewal, q.Total)
		if ok {
			detailText = fmt.Sprintf("The price changed: %s now costs %s %s, not %s. Send price %q to buy it at that price.", d, q.Total, q.Currency, in.Body.Price, q.Total)
		}
		return nil, &api.Problem{Status: http.StatusPreconditionRequired, Title: "Confirm the price", Code: "confirm_required", Detail: detailText,
			Hint: "Show the person the domain, the price now and what it renews at, and buy only once they agree. It can't be refunded.", Quote: &q}
	}
	if m.p.Tokens == nil {
		return nil, api.NewProblem(http.StatusPreconditionFailed, "precondition", "the box's sign-ins aren't available")
	}
	if err := m.p.Tokens.RequireSudo(ctx, api.PrincipalFrom(ctx), tokens.ErrReauthBuy); err != nil {
		return nil, err
	}
	in1 := dnskit.NewRegistration{DomainName: d, Years: q.Years, AutoRenew: in.Body.AutoRenew, PrivacyMode: "redaction"}
	if reg.Sandbox {
		in1.Contacts = sandboxContact
	}
	rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer rcancel()
	w, err := reg.Register(rctx, in1)
	if err != nil {
		var re *dnskit.RegistrarError
		pr := &api.Problem{Status: http.StatusBadGateway, Title: "Cloudflare didn't register it", Code: "precondition", Detail: err.Error(),
			Hint: "Nothing was registered. Check the Cloudflare account has a card, a default contact and the registration agreement accepted (Domains › Registrations), then try again."}
		if errors.As(err, &re) && re.Forbidden() {
			pr.Hint = m.readiness(err).Fix
		}
		return nil, pr
	}
	r := &purchaseRec{Purchase: Purchase{
		Domain: d, Years: q.Years, Total: q.Total, Currency: q.Currency, AutoRenew: in.Body.AutoRenew,
		Project: in.Body.Project, App: in.Body.App, By: actor(ctx), At: m.now().UTC(), Sandbox: reg.Sandbox,
	}}
	m.applyWorkflow(r, w)
	m.savePurchase(ctx, r)
	_ = m.p.DB.Audit(ctx, r.By, "domains.buy", d, map[string]any{"total": q.Total, "currency": q.Currency, "years": q.Years, "autoRenew": in.Body.AutoRenew, "sandbox": reg.Sandbox})
	m.advance(ctx, reg, r)
	return &r.Purchase, nil
}

// owned joins the provider's zones, the account's registrations and this
// box's purchases with where each domain is used.
func (m *Module) owned(ctx context.Context, p *platform.Platform) Owned {
	out := Owned{Domains: []OwnedDomain{}}
	byName := map[string]*OwnedDomain{}
	get := func(d string) *OwnedDomain {
		if o := byName[d]; o != nil {
			return o
		}
		o := &OwnedDomain{Domain: d, UsedBy: []Use{}}
		byName[d] = o
		return o
	}
	m.mu.Lock()
	for _, st := range m.provs {
		for _, z := range st.rec.Zones {
			get(z).Manageable = true
		}
	}
	m.mu.Unlock()
	reg, err := m.registrar(ctx)
	out.Buy = m.readiness(err)
	if err == nil {
		lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		regs, lerr := reg.Registrations(lctx)
		cancel()
		if lerr != nil {
			out.Buy = m.readiness(lerr)
		}
		for _, r := range regs {
			o := get(strings.ToLower(r.DomainName))
			o.Registered, o.Status, o.ExpiresAt = true, r.Status, r.ExpiresAt
			ar := r.AutoRenew
			o.AutoRenew = &ar
		}
	}
	for d, r := range m.purchases(ctx) {
		if reg != nil {
			m.advance(ctx, reg, r)
		}
		if r.State == BuyFailed && !r.At.After(m.now().Add(-24*time.Hour)) {
			continue
		}
		o := get(d)
		pu := r.Purchase
		o.Purchase = &pu
		o.Manageable = o.Manageable || m.zoneHeld(d)
	}
	wants := m.scan(ctx, p)
	under := func(host, d string) bool { return host == d || strings.HasSuffix(host, "."+d) }
	for d, o := range byName {
		for _, w := range wants {
			if !under(w.Host, d) || w.RedirectTo != "" {
				continue
			}
			app := ""
			if len(w.Routes) > 0 {
				app = w.Routes[0].App
			}
			o.UsedBy = append(o.UsedBy, Use{Project: w.Project, App: app, Host: w.Host})
		}
		var box []string
		if under(p.Domain, d) {
			box = append(box, "dashboard")
		}
		if under(p.AppsDomain(), d) {
			box = append(box, "apps")
		}
		o.Box = strings.Join(box, ",")
	}
	for _, o := range byName {
		out.Domains = append(out.Domains, *o)
	}
	sort.Slice(out.Domains, func(i, j int) bool {
		a, b := out.Domains[i], out.Domains[j]
		if (a.Purchase != nil && a.Purchase.State != BuyReady) != (b.Purchase != nil && b.Purchase.State != BuyReady) {
			return a.Purchase != nil && a.Purchase.State != BuyReady // in-flight purchases first
		}
		return a.Domain < b.Domain
	})
	return out
}

func (m *Module) registerBuy(a huma.API, p *platform.Platform, ready func() error) {
	const tag = "domains"
	admin := func(ctx context.Context) error {
		if err := requireAdmin(ctx); err != nil {
			return err
		}
		return ready()
	}

	huma.Register(a, api.Outbound(api.Op("domains-search", http.MethodGet, "/v1/domains/search", "domains search", api.RiskRead,
		"Search for a domain to buy",
		"Names like q (a word, a phrase or a domain) from Cloudflare Registrar, with each one's live availability and price: first year, then each later year. "+
			"Bought through the Cloudflare account connected for DNS, at cost. buy says whether this box can buy, and the fix if not. Box admins only.", tag)),
		api.Wrap(func(ctx context.Context, in *struct {
			Q     string `query:"q" required:"true" minLength:"1" maxLength:"100" doc:"A word, a phrase or a domain, e.g. acme or acme.app"`
			Quick bool   `query:"quick" doc:"Answer as soon as Cloudflare's search does: only the name itself is checked live, the rest is checked:false (follow with domains availability). The dashboard does this to show results sooner."`
		}) (*struct{ Body SearchResult }, error) {
			if err := admin(ctx); err != nil {
				return nil, err
			}
			q := strings.ToLower(strings.TrimSpace(in.Q))
			q = strings.TrimPrefix(strings.TrimPrefix(q, "https://"), "http://")
			q = strings.TrimSuffix(strings.TrimPrefix(q, "www."), "/")
			if q == "" {
				return nil, api.NewProblem(422, "validation", "search for a word or a domain")
			}
			res, err := m.search(ctx, q, !in.Quick)
			if err != nil {
				return nil, &api.Problem{Status: http.StatusBadGateway, Title: "Search didn't work", Code: "precondition", Detail: err.Error(), Hint: "Try again in a minute."}
			}
			return &struct{ Body SearchResult }{res}, nil
		}))

	huma.Register(a, api.Outbound(api.Op("domains-owned", http.MethodGet, "/v1/domains/owned", "domains owned", api.RiskRead,
		"List your domains",
		"Every domain the box can put on projects: the zones the connected DNS provider holds, the domains registered in the Cloudflare account (with renewal and auto-renew), "+
			"and domains bought from this box, each with the projects and apps that use names under it. Box admins only.", tag)),
		api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body Owned }, error) {
			if err := admin(ctx); err != nil {
				return nil, err
			}
			return &struct{ Body Owned }{m.owned(ctx, p)}, nil
		}))

	huma.Register(a, api.Outbound(api.Op("domains-check", http.MethodPost, "/v1/domains/check", "domains availability", api.RiskRead,
		"Check domains live",
		"Asks the registries, live, whether up to 20 names can be bought here and what each costs (first year, then each later year). Read-only. Box admins only.", tag)),
		api.Wrap(func(ctx context.Context, in *struct {
			Body struct {
				Domains []string `json:"domains" minItems:"1" maxItems:"20" doc:"Full names, e.g. acme.app"`
			}
		}) (*struct {
			Body struct {
				Offers []Offer `json:"offers"`
			}
		}, error) {
			if err := admin(ctx); err != nil {
				return nil, err
			}
			reg, err := m.registrar(ctx)
			if err != nil {
				rd := m.readiness(err)
				return nil, &api.Problem{Status: http.StatusPreconditionFailed, Title: "Can't check domains", Code: "precondition", Detail: rd.Why, Hint: rd.Fix}
			}
			var names []string
			for _, d := range in.Body.Domains {
				if v, err := validDomain(d); err == nil {
					names = append(names, v)
				}
			}
			out := &struct {
				Body struct {
					Offers []Offer `json:"offers"`
				}
			}{}
			out.Body.Offers = []Offer{}
			if len(names) == 0 {
				return out, nil
			}
			cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			var mu sync.Mutex
			var wg sync.WaitGroup
			got := map[string]dnskit.Offer{}
			for b := range slices.Chunk(names, 5) {
				wg.Go(func() {
					if r, err := reg.Check(cctx, b); err == nil {
						mu.Lock()
						for _, o := range r {
							got[o.Name] = o
						}
						mu.Unlock()
					}
				})
			}
			wg.Wait()
			for _, n := range names {
				if o, ok := got[n]; ok {
					out.Body.Offers = append(out.Body.Offers, offer(o, true, reg.Account))
				}
			}
			return out, nil
		}))

	buy := api.Outbound(api.Op("domains-buy", http.MethodPost, "/v1/domains/buy", "domains buy", api.RiskDestructive,
		"Buy a domain",
		"Registers a domain with Cloudflare Registrar in the Cloudflare account connected for DNS, charged to that account's card. It can't be refunded. "+
			"Without price, or when the live price differs from it, nothing is bought: 428 confirm_required with the quote. Show a person the domain, its price and renewal, "+
			"and send the quote's total as price only once they agree. Answers with the purchase; follow it with domains buy-status until ready, then domains add to put it on a project. "+
			"Box admins only; in the dashboard it also needs a recent sign-in (reauth_required).", tag))
	buy.Errors = append(buy.Errors, 409, 412, 428, 502)
	huma.Register(a, buy, api.Wrap(func(ctx context.Context, in *buyIn) (*struct{ Body Purchase }, error) {
		if err := admin(ctx); err != nil {
			return nil, err
		}
		pu, err := m.buyDomain(ctx, in)
		if err != nil {
			return nil, err
		}
		return &struct{ Body Purchase }{*pu}, nil
	}))

	st := api.Outbound(api.Op("domains-buy-status", http.MethodGet, "/v1/domains/buy/{domain}", "domains buy-status", api.RiskRead,
		"Follow a domain purchase",
		"How a domain bought from this box is getting on: registering, action_required (sentTo: confirm the registry's email), blocked, failed, setting_up_dns, then ready "+
			"(the box can set its records, so domains add works with createRecords). Box admins only.", tag))
	st.Errors = append(st.Errors, 404)
	huma.Register(a, st, api.Wrap(func(ctx context.Context, in *struct {
		Domain string `path:"domain" maxLength:"253"`
	}) (*struct{ Body Purchase }, error) {
		if err := admin(ctx); err != nil {
			return nil, err
		}
		d, err := validDomain(in.Domain)
		if err != nil {
			return nil, err
		}
		r := m.loadPurchase(ctx, d)
		if r == nil {
			return nil, api.NewProblem(404, "not_found", d+" wasn't bought from this box")
		}
		if reg, err := m.registrar(ctx); err == nil {
			m.advance(ctx, reg, r)
		}
		return &struct{ Body Purchase }{r.Purchase}, nil
	}))
}
