package email

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/dnskit"
	"github.com/shiptiffin/tiffin/internal/dnskit/dnstest"
)

func byKind(c *DomainCheck) map[string]DomainRecord {
	out := map[string]DomainRecord{}
	for _, r := range c.Records {
		out[r.Kind] = r
	}
	return out
}

func TestCheckDomain(t *testing.T) {
	srv := dnstest.Start(t)
	srv.AddZone("shop.test")
	res := &dnskit.Resolver{Servers: []string{srv.Addr()}, Timeout: time.Second}
	ctx := t.Context()

	// Nothing published, unknown relay: everything missing; DMARC has a starter value.
	c, err := checkDomain(ctx, res, "Shop <hello@Shop.test>", "tiffin.localhost", "", "")
	if err != nil {
		t.Fatal(err)
	}
	k := byKind(c)
	if c.Domain != "shop.test" || c.BoxDomain || c.Provider != "" || len(c.Records) != 3 ||
		k["spf"].State != "missing" || k["spf"].Want != "" || k["dkim"].State != "missing" || !strings.HasPrefix(k["dkim"].Name, "<selector>") ||
		k["dmarc"].State != "missing" || k["dmarc"].Want != "v=DMARC1; p=none;" {
		b, _ := json.MarshalIndent(c, "", " ")
		t.Fatalf("empty domain: %s", b)
	}

	// Resend: SPF on send.<domain> with its include, DKIM at resend._domainkey.
	srv.Set("send.shop.test", "TXT", "v=spf1 include:amazonses.com ~all")
	srv.Set("resend._domainkey.shop.test", "TXT", "p=MIGfMA0GCSq")
	srv.Set("_dmarc.shop.test", "TXT", "v=DMARC1; p=none; rua=mailto:d@shop.test")
	c, _ = checkDomain(ctx, res, "hello@shop.test", "tiffin.localhost", "smtp.resend.com", "")
	k = byKind(c)
	if c.Provider != "Resend" || k["spf"].Name != "send.shop.test" || k["spf"].State != "ok" ||
		k["dkim"].State != "ok" || k["dkim"].Name != "resend._domainkey.shop.test" || k["dmarc"].State != "ok" || !strings.Contains(k["dmarc"].Detail, "p=none") {
		b, _ := json.MarshalIndent(c, "", " ")
		t.Fatalf("resend: %s", b)
	}

	// An SPF record without the relay's include is a warning with a merged value; two are an error.
	srv.Set("shop.test", "TXT", "v=spf1 include:_spf.google.com ~all", "google-site-verification=x")
	c, _ = checkDomain(ctx, res, "hello@shop.test", "tiffin.localhost", "smtp.sendgrid.net", "")
	if s := byKind(c)["spf"]; s.State != "warn" || s.Want != "v=spf1 include:sendgrid.net include:_spf.google.com ~all" || len(s.Found) != 1 {
		t.Fatalf("spf without include: %+v", s)
	}
	srv.Set("shop.test", "TXT", "v=spf1 include:sendgrid.net ~all", "v=spf1 -all")
	if s := byKind(mustCheck(t, res, "smtp.sendgrid.net", ""))["spf"]; s.State != "warn" || !strings.Contains(s.Detail, "two SPF") {
		t.Fatalf("two spf: %+v", s)
	}

	// Postmark handles SPF itself; a given selector is checked as given.
	srv.Set("20240101pm._domainkey.shop.test", "TXT", "k=rsa; p=MIGf")
	c = mustCheck(t, res, "smtp.postmarkapp.com", "20240101pm")
	k = byKind(c)
	if _, ok := k["spf"]; ok || k["dkim"].State != "ok" || k["dkim"].Name != "20240101pm._domainkey.shop.test" {
		b, _ := json.MarshalIndent(c, "", " ")
		t.Fatalf("postmark: %s", b)
	}

	// DNS that doesn't answer is "unknown", not "missing".
	dead := &dnskit.Resolver{Servers: []string{"127.0.0.1:1"}, Timeout: 100 * time.Millisecond}
	c, _ = checkDomain(ctx, dead, "hello@shop.test", "tiffin.localhost", "", "x1")
	for _, r := range c.Records {
		if r.State != "unknown" {
			t.Fatalf("dead dns: %+v", r)
		}
	}

	if _, err := checkDomain(ctx, res, "not an address", "", "", ""); err == nil {
		t.Fatal("bad from accepted")
	}
}

func mustCheck(t *testing.T, res *dnskit.Resolver, relay, sel string) *DomainCheck {
	t.Helper()
	c, err := checkDomain(t.Context(), res, "hello@shop.test", "tiffin.localhost", relay, sel)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDomainCheckAPI(t *testing.T) {
	r := newRig(t)
	srv := dnstest.Start(t)
	srv.AddZone("tiffin.localhost")
	srv.Set("_dmarc.tiffin.localhost", "TXT", "v=DMARC1; p=reject")
	old := domainResolver
	domainResolver = &dnskit.Resolver{Servers: []string{srv.Addr()}, Timeout: time.Second}
	t.Cleanup(func() { domainResolver = old })

	owner, _, err := r.tm.Bootstrap(r.ctx)
	if err != nil {
		t.Fatal(err)
	}
	a := api.New(api.Deps{DB: r.p.DB, Engine: r.p.Engine, Tokens: r.tm, Platform: r.p})
	get := func(path string) (int, DomainCheck) {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+owner)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, req)
		var c DomainCheck
		_ = json.Unmarshal(w.Body.Bytes(), &c)
		return w.Code, c
	}
	code, c := get("/v1/projects/shop/email/domain")
	if code != 200 || c.From != "shop@tiffin.localhost" || !c.BoxDomain || byKind(&c)["dmarc"].State != "ok" {
		t.Fatalf("check: %d %+v", code, c)
	}
	if code, _ := get("/v1/projects/shop/email/domain?selector=bad%20one"); code != 422 {
		t.Fatalf("bad selector: %d", code)
	}
	if code, _ := get("/v1/projects/nope/email/domain"); code != 404 {
		t.Fatalf("no project: %d", code)
	}
}
