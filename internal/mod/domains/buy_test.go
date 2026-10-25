package domains

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/shiptiffin/tiffin/internal/dnskit"
)

// fakeRegistrar is Cloudflare's Registrar API for one account, acct1.
type fakeRegistrar struct {
	mu      sync.Mutex
	price   string            // registration_cost of every free name
	taken   map[string]bool   // names someone has
	regs    map[string]string // domain → workflow state
	asks    []string          // names registered, in order
	statusN map[string]int    // status polls per domain
	body    map[string]any    // the last registration request
}

func (f *fakeRegistrar) offer(name string) map[string]any {
	switch {
	case f.taken[name] || f.regs[name] != "":
		return map[string]any{"name": name, "registrable": false, "reason": "domain_unavailable"}
	case strings.HasSuffix(name, ".uk"):
		return map[string]any{"name": name, "registrable": false, "reason": "extension_not_supported_via_api"}
	case strings.HasPrefix(name, "gold."):
		return map[string]any{"name": name, "registrable": false, "reason": "domain_premium", "tier": "premium"}
	}
	return map[string]any{"name": name, "registrable": true, "tier": "standard",
		"pricing": map[string]any{"currency": "USD", "registration_cost": f.price, "renewal_cost": "12.00"}}
}

func (f *fakeRegistrar) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ok := func(v any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": v})
	}
	fail := func(code int, msg string) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []any{map[string]any{"code": 10000, "message": msg}}, "result": nil})
	}
	if r.Header.Get("Authorization") != "Bearer cf-token" {
		fail(403, "Authentication error")
		return
	}
	p := r.URL.Path
	const reg = "/accounts/acct1/registrar"
	switch {
	case p == "/zones":
		ok([]any{map[string]any{"id": "z1", "account": map[string]any{"id": "acct1", "name": "Mine"}}})
	case p == reg+"/domain-search":
		q := r.URL.Query().Get("q")
		label := strings.Split(q, ".")[0]
		var out []any
		for _, ext := range []string{"com", "app", "dev", "uk"} {
			out = append(out, f.offer(label+"."+ext))
		}
		out = append(out, f.offer("gold."+label))
		ok(map[string]any{"domains": out})
	case p == reg+"/domain-check":
		var in struct{ Domains []string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		var out []any
		for _, d := range in.Domains {
			out = append(out, f.offer(d))
		}
		ok(map[string]any{"domains": out})
	case p == reg+"/registrations" && r.Method == http.MethodPost:
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.body = in
		d := in["domain_name"].(string)
		f.regs[d] = "in_progress"
		f.asks = append(f.asks, d)
		w.WriteHeader(202)
		ok(map[string]any{"state": "in_progress", "completed": false, "context": map[string]any{"domain_name": d}})
	case p == reg+"/registrations" && r.Method == http.MethodGet:
		var out []any
		for d, st := range f.regs {
			if st == "succeeded" {
				out = append(out, map[string]any{"domain_name": d, "status": "active", "auto_renew": true, "expires_at": "2027-10-24T00:00:00Z"})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": out, "result_info": map[string]any{"cursor": ""}})
	case strings.HasPrefix(p, reg+"/registrations/") && strings.HasSuffix(p, "/registration-status"):
		d := strings.TrimSuffix(strings.TrimPrefix(p, reg+"/registrations/"), "/registration-status")
		f.statusN[d]++
		if f.statusN[d] >= 2 {
			f.regs[d] = "succeeded"
		}
		res := map[string]any{"state": f.regs[d], "completed": f.regs[d] == "succeeded", "context": map[string]any{"domain_name": d}}
		if f.regs[d] == "succeeded" {
			res["context"].(map[string]any)["registration"] = map[string]any{"domain_name": d, "status": "active", "expires_at": "2027-10-24T00:00:00Z"}
		}
		ok(res)
	case strings.HasPrefix(p, reg+"/registrations/"):
		d := strings.TrimPrefix(p, reg+"/registrations/")
		if f.regs[d] == "" {
			fail(404, "Domain not found")
			return
		}
		ok(map[string]any{"domain_name": d, "status": "active"})
	default:
		fail(404, "no route "+p)
	}
}

// buyHarness: a box connected to "Cloudflare" (the fake DNS provider under
// that name) whose registrar is the fake above.
func buyHarness(t *testing.T) (*harness, *fakeRegistrar) {
	t.Helper()
	h := newHarness(t)
	fake, _ := dnskit.Lookup("fake")
	real, _ := dnskit.Lookup("cloudflare")
	cf := fake
	cf.Name, cf.Label = "cloudflare", "Cloudflare"
	dnskit.Register(cf)
	t.Cleanup(func() { dnskit.Register(real) })
	f := &fakeRegistrar{price: "10.44", taken: map[string]bool{"acme.com": true}, regs: map[string]string{}, statusN: map[string]int{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	h.m.buy = &buyKit{base: srv.URL, http: srv.Client()}
	if code, out := h.call("PUT", "/v1/dns/providers/cloudflare", map[string]any{"token": "cf-token"}); code != 200 {
		t.Fatalf("connect: %d %v", code, out)
	}
	return h, f
}

func TestDomainSearch(t *testing.T) {
	h, _ := buyHarness(t)
	code, out := h.call("GET", "/v1/domains/search?q=acme.app", nil)
	if code != 200 || out["buy"].(map[string]any)["canBuy"] != true {
		t.Fatalf("search: %d %v", code, out)
	}
	offers := out["offers"].([]any)
	first := offers[0].(map[string]any)
	if first["domain"] != "acme.app" || first["exact"] != true || first["available"] != true || first["firstYear"] != "10.44" || first["renewal"] != "12.00" || first["checked"] != true {
		t.Fatalf("exact name first, live-checked: %v", first)
	}
	by := map[string]map[string]any{}
	for _, o := range offers {
		o := o.(map[string]any)
		by[o["domain"].(string)] = o
	}
	if o := by["acme.com"]; o["available"] != false || o["reason"] != "domain_unavailable" {
		t.Errorf("taken: %v", o)
	}
	if o := by["acme.uk"]; o["available"] != false || !strings.Contains(o["link"].(string), "acct1/domains/registrations") {
		t.Errorf("dashboard-only ending: %v", o)
	}
	if o := by["gold.acme"]; o["premium"] != true || o["available"] != false {
		t.Errorf("premium: %v", o)
	}
}

func TestDomainSearchWord(t *testing.T) {
	h, _ := buyHarness(t)
	// A plain word: its .com, .app, .dev and .io first; the first free one is the big one.
	code, out := h.call("GET", "/v1/domains/search?q=acme", nil)
	if code != 200 {
		t.Fatalf("search: %d %v", code, out)
	}
	var names []string
	for _, o := range out["offers"].([]any) {
		names = append(names, o.(map[string]any)["domain"].(string))
	}
	first := out["offers"].([]any)[0].(map[string]any)
	if first["domain"] != "acme.app" || first["exact"] != true {
		t.Fatalf("first free top ending: %v (all %v)", first, names)
	}
	if strings.Join(names[:4], " ") != "acme.app acme.com acme.dev acme.io" {
		t.Errorf("order: %v", names)
	}
	if c := strings.Count(strings.Join(names, " "), "acme.com"); c != 1 {
		t.Errorf("acme.com %d times: %v", c, names)
	}
}

func TestDomainSearchQuickThenCheck(t *testing.T) {
	h, _ := buyHarness(t)
	_, out := h.call("GET", "/v1/domains/search?q=acme.app&quick=true", nil)
	var unchecked []string
	for _, o := range out["offers"].([]any) {
		o := o.(map[string]any)
		if o["domain"] == "acme.app" && o["checked"] != true {
			t.Errorf("the name itself is checked live: %v", o)
		}
		if o["checked"] != true {
			unchecked = append(unchecked, o["domain"].(string))
		}
	}
	if len(unchecked) == 0 {
		t.Fatalf("quick: everything checked: %v", out)
	}
	code, out := h.call("POST", "/v1/domains/check", map[string]any{"domains": unchecked})
	if code != 200 || len(out["offers"].([]any)) != len(unchecked) || out["offers"].([]any)[0].(map[string]any)["checked"] != true {
		t.Fatalf("check: %d %v", code, out)
	}
}

func TestDomainSearchNeedsCloudflare(t *testing.T) {
	h := newHarness(t)
	code, out := h.call("GET", "/v1/domains/search?q=acme", nil)
	buy := out["buy"].(map[string]any)
	if code != 200 || buy["canBuy"] != false || buy["fixUrl"] != "/settings/dns" {
		t.Fatalf("not connected: %d %v", code, out)
	}
}

func TestDomainSearchTokenWithoutRegistrar(t *testing.T) {
	h, _ := buyHarness(t)
	// The account is remembered; a token that lost its Registrar permission gets a 403.
	h.m.buy.base = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":10000,"message":"Authentication error"}],"result":null}`))
	})).URL
	h.m.buy.http = nil
	h.m.provs["cloudflare"].rec.Account = "acct1"
	code, out := h.call("GET", "/v1/domains/search?q=acme", nil)
	buy := out["buy"].(map[string]any)
	if code != 200 || buy["canBuy"] != false || !strings.Contains(buy["fix"].(string), "Registrar Domains") {
		t.Fatalf("forbidden token: %d %v", code, out)
	}
}

func TestBuyDomain(t *testing.T) {
	h, f := buyHarness(t)
	body := map[string]any{"domain": "acme.app", "autoRenew": true, "project": "shop"}

	// No price: the quote, nothing bought.
	code, out := h.call("POST", "/v1/domains/buy", body)
	if code != 428 || out["code"] != "confirm_required" {
		t.Fatalf("without price: %d %v", code, out)
	}
	q := out["quote"].(map[string]any)
	if q["total"] != "10.44" || q["renewal"] != "12.00" || q["currency"] != "USD" || len(f.asks) != 0 {
		t.Fatalf("quote: %v asks %v", q, f.asks)
	}
	// Two years: the first year plus one renewal.
	body2 := map[string]any{"domain": "acme.app", "autoRenew": true, "years": 2}
	if _, out := h.call("POST", "/v1/domains/buy", body2); out["quote"].(map[string]any)["total"] != "22.44" {
		t.Errorf("two years: %v", out["quote"])
	}
	// A price that went up is refused.
	f.mu.Lock()
	f.price = "11.00"
	f.mu.Unlock()
	body["price"] = "10.44"
	if code, out := h.call("POST", "/v1/domains/buy", body); code != 428 || !strings.Contains(out["detail"].(string), "price changed") || len(f.asks) != 0 {
		t.Fatalf("stale price: %d %v", code, out)
	}
	body["price"] = "11.00"
	code, out = h.call("POST", "/v1/domains/buy", body)
	if code != 200 || out["state"] != BuyRegistering || out["total"] != "11.00" || out["project"] != "shop" {
		t.Fatalf("buy: %d %v", code, out)
	}
	if len(f.asks) != 1 || f.body["auto_renew"] != true || f.body["privacy_mode"] != "redaction" || f.body["contacts"] != nil {
		t.Fatalf("registration request: %v %v", f.asks, f.body)
	}
	// Buying again while it's on its way registers nothing new.
	if code, _ := h.call("POST", "/v1/domains/buy", body); code != 200 || len(f.asks) != 1 {
		t.Fatalf("again: %d asks %v", code, f.asks)
	}

	// The registry finishes; the zone shows up in the account; then it's ready.
	h.clock = h.clock.Add(3e9)
	_, out = h.call("GET", "/v1/domains/buy/acme.app", nil)
	if out["state"] != BuyDNS {
		t.Fatalf("after registering: %v", out)
	}
	h.dns.AddZone("acme.app")
	h.clock = h.clock.Add(6e9)
	_, out = h.call("GET", "/v1/domains/buy/acme.app", nil)
	if out["state"] != BuyReady || !strings.Contains(out["detail"].(string), "renews on 24 October 2027") {
		t.Fatalf("ready: %v", out)
	}

	// Your domains: the new one, registered, unused; then on a project with its records set by the box.
	code, out = h.call("GET", "/v1/domains/owned", nil)
	if code != 200 {
		t.Fatalf("owned: %d %v", code, out)
	}
	var mine map[string]any
	for _, d := range out["domains"].([]any) {
		if d := d.(map[string]any); d["domain"] == "acme.app" {
			mine = d
		}
	}
	if mine == nil || mine["registered"] != true || mine["manageable"] != true || mine["autoRenew"] != true || len(mine["usedBy"].([]any)) != 0 {
		t.Fatalf("owned acme.app: %v", out)
	}
	h.project("shop")
	h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "acme.app", "app": "web", "createRecords": true})
	if got := h.dns.Get("acme.app", "A"); len(got) != 1 || got[0] != boxIP {
		t.Errorf("records the box set: %v", got)
	}
	_, out = h.call("GET", "/v1/domains/owned", nil)
	for _, d := range out["domains"].([]any) {
		if d := d.(map[string]any); d["domain"] == "acme.app" {
			use := d["usedBy"].([]any)
			if len(use) != 1 || use[0].(map[string]any)["project"] != "shop" || use[0].(map[string]any)["app"] != "web" {
				t.Errorf("used by: %v", use)
			}
		}
	}
}

func TestBuyDomainRefusals(t *testing.T) {
	h, f := buyHarness(t)
	if code, _ := h.call("POST", "/v1/domains/buy", map[string]any{"domain": "acme.com", "autoRenew": true, "price": "10.44"}); code != 409 {
		t.Errorf("taken: %d", code)
	}
	if code, _ := h.call("POST", "/v1/domains/buy", map[string]any{"domain": "gold.acme", "autoRenew": true, "price": "10.44"}); code != 409 {
		t.Errorf("premium: %d", code)
	}
	if code, _ := h.call("POST", "/v1/domains/buy", map[string]any{"domain": "acme.app", "price": "10.44"}); code != 422 {
		t.Errorf("autoRenew is required: %d", code)
	}
	if len(f.asks) != 0 {
		t.Errorf("registered %v", f.asks)
	}
}

func TestCents(t *testing.T) {
	for in, want := range map[string]int64{"8.57": 857, "10": 1000, "10.5": 1050, "$1,234.50": 123450, "0.09": 9} {
		if got, ok := dnskit.Cents(in); !ok || got != want {
			t.Errorf("Cents(%q) = %d %v, want %d", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "abc", "1.234", "-1"} {
		if _, ok := dnskit.Cents(bad); ok {
			t.Errorf("Cents(%q) should fail", bad)
		}
	}
}
