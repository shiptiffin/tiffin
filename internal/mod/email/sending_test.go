package email

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/dnskit"
	"github.com/shiptiffin/tiffin/internal/dnskit/dnstest"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// fakeDNS is a connected DNS provider holding some zones.
type fakeDNS struct {
	mu    sync.Mutex
	zones []string
	set   []dnskit.Record
	fail  error
}

func (f *fakeDNS) Manages(_ context.Context, name string) bool {
	_, ok := dnskit.ZoneFor(f.zones, name)
	return ok
}
func (f *fakeDNS) SetRecords(_ context.Context, recs []dnskit.Record, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.set = append(f.set, recs...)
	return nil
}
func (f *fakeDNS) DeleteRecords(context.Context, []dnskit.Record, string) error { return nil }

// fakeSendGrid is SendGrid's domain authentication API.
type fakeSendGrid struct {
	mu       sync.Mutex
	key      string // the key with Sender Authentication
	domains  []map[string]any
	creates  int
	validate int
	valid    bool
}

func (f *fakeSendGrid) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer "+f.key {
		w.WriteHeader(403)
		io.WriteString(w, `{"errors":[{"field":null,"message":"access forbidden"}]}`)
		return
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/v3/whitelabel/domains":
		out := []map[string]any{}
		for _, d := range f.domains {
			if d["domain"] == r.URL.Query().Get("domain") {
				d["valid"] = f.valid
				out = append(out, d)
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == "POST" && r.URL.Path == "/v3/whitelabel/domains":
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in["automatic_security"] != true {
			w.WriteHeader(400)
			return
		}
		f.creates++
		dom := in["domain"].(string)
		d := map[string]any{"id": 302183, "domain": dom, "subdomain": "em1234", "automatic_security": true, "valid": false,
			"dns": map[string]any{
				"mail_cname": map[string]any{"valid": false, "type": "cname", "host": "em1234." + dom, "data": "u1446226.wl.sendgrid.net"},
				"dkim1":      map[string]any{"valid": false, "type": "cname", "host": "s1._domainkey." + dom, "data": "s1.domainkey.u1446226.wl.sendgrid.net"},
				"dkim2":      map[string]any{"valid": false, "type": "cname", "host": "s2._domainkey." + dom, "data": "s2.domainkey.u1446226.wl.sendgrid.net"},
			}}
		f.domains = append(f.domains, d)
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(d)
	case r.Method == "POST" && r.URL.Path == "/v3/whitelabel/domains/302183/validate":
		f.validate++
		reason := "Expected CNAME for \"s1._domainkey.shop.test\" to match \"s1.domainkey.u1446226.wl.sendgrid.net\"."
		res := map[string]any{"valid": f.valid}
		if !f.valid {
			res["validation_results"] = map[string]any{"mail_cname": map[string]any{"valid": true, "reason": nil},
				"dkim1": map[string]any{"valid": false, "reason": reason}, "dkim2": map[string]any{"valid": false, "reason": nil}}
		}
		_ = json.NewEncoder(w).Encode(res)
	default:
		w.WriteHeader(404)
	}
}

func (f *fakeSendGrid) setValid(v bool) {
	f.mu.Lock()
	f.valid = v
	f.mu.Unlock()
}

type sendingRig struct {
	*rig
	a     *api.API
	owner string
	dns   *fakeDNS
	now   time.Time
	mu    sync.Mutex
}

func newSendingRig(t *testing.T, provider, key string) *sendingRig {
	r := newRig(t)
	s := &sendingRig{rig: r, dns: &fakeDNS{zones: []string{"shop.test"}}, now: time.Now()}
	mod.clock = func() time.Time { s.mu.Lock(); defer s.mu.Unlock(); return s.now }
	r.p.DNS = s.dns
	srv := dnstest.Start(t)
	srv.AddZone("shop.test")
	srv.AddZone("other.test")
	old := domainResolver
	domainResolver = &dnskit.Resolver{Servers: []string{srv.Addr()}, Timeout: time.Second}
	t.Cleanup(func() { domainResolver = old })
	if provider != "" {
		if err := setRelay(r.ctx, r.p, &Relay{Provider: provider, Host: "smtp.example.net", Port: 587, TLS: TLSStartTLS}, &key); err != nil {
			t.Fatal(err)
		}
	}
	s.owner, _, _ = r.tm.Bootstrap(r.ctx)
	s.a = api.New(api.Deps{DB: r.p.DB, Engine: r.p.Engine, Tokens: r.tm, Platform: r.p, PublicURL: r.p.PublicURL})
	return s
}

func (s *sendingRig) advance(d time.Duration) {
	s.mu.Lock()
	s.now = s.now.Add(d)
	s.mu.Unlock()
}

func (s *sendingRig) call(tok, method, path, body string) (int, map[string]any) {
	s.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	s.a.Handler().ServeHTTP(w, req)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

func setupOf(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	s, ok := m["setup"].(map[string]any)
	if !ok {
		t.Fatalf("no setup: %v", m)
	}
	return s
}

func (s *sendingRig) from() string {
	st, _ := mod.settings(s.ctx, s.p, "shop")
	return st.From
}

func TestSendFromMyDomainSendGrid(t *testing.T) {
	fake := &fakeSendGrid{key: "SG.full"}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	old := sendgridAPI
	sendgridAPI = srv.URL
	t.Cleanup(func() { sendgridAPI = old })
	s := newSendingRig(t, ProviderSendGrid, "SG.full")

	code, v := s.call(s.owner, "GET", "/v1/projects/shop/email/sending-domain?domain=shop.test", "")
	if code != 200 || v["automatic"] != true || v["providerName"] != "SendGrid" || v["dnsManaged"] != true || v["setup"] != nil {
		t.Fatalf("view before: %d %v", code, v)
	}
	if code, m := s.call(s.owner, "POST", "/v1/projects/shop/email/sending-domain", `{"domain":"not a domain"}`); code != 422 {
		t.Fatalf("bad domain: %d %v", code, m)
	}

	code, v = s.call(s.owner, "POST", "/v1/projects/shop/email/sending-domain", `{"domain":"Shop.test."}`)
	if code != 200 {
		t.Fatalf("start: %d %v", code, v)
	}
	sd := setupOf(t, v)
	recs := sd["records"].([]any)
	if sd["state"] != "verifying" || sd["dns"] != "auto" || sd["from"] != "hello@shop.test" || sd["providerId"] != "302183" || len(recs) != 4 {
		t.Fatalf("setup: %v", sd)
	}
	// Three CNAMEs from SendGrid and a DMARC policy (there was none), all written.
	if len(s.dns.set) != 4 || s.dns.set[3].Name != "_dmarc.shop.test" || s.dns.set[3].Value != "v=DMARC1; p=none;" {
		t.Fatalf("dns written: %+v", s.dns.set)
	}
	for _, r := range s.dns.set[:3] {
		if r.Type != "CNAME" || !strings.HasSuffix(r.Value, "wl.sendgrid.net") {
			t.Fatalf("record: %+v", r)
		}
	}
	var dkim1 map[string]any
	for _, r := range recs {
		if r.(map[string]any)["name"] == "s1._domainkey.shop.test" {
			dkim1 = r.(map[string]any)
		}
	}
	if dkim1["name"] != "s1._domainkey.shop.test" || dkim1["host"] != "s1._domainkey" || dkim1["status"] != "failed" || dkim1["written"] != true ||
		!strings.Contains(dkim1["detail"].(string), "Expected CNAME") {
		t.Fatalf("dkim1 after the first validate: %v", dkim1)
	}
	if s.from() != "shop@tiffin.localhost" {
		t.Fatalf("sender changed before verification: %s", s.from())
	}

	// Not due yet: no call. Due: one call, still waiting, and the next wait grows.
	mod.checkDue(s.ctx, s.p)
	if fake.validate != 1 {
		t.Fatalf("checked early: %d", fake.validate)
	}
	s.advance(61 * time.Second)
	mod.checkDue(s.ctx, s.p)
	got, _ := getSending(s.ctx, s.p, "shop")
	if fake.validate != 2 || got.State != SendingVerifying || got.NextCheckAt.Sub(s.now) != 2*time.Minute {
		t.Fatalf("second check: %d %+v", fake.validate, got)
	}

	// SendGrid sees the records: the domain becomes the sender, with a change in History.
	fake.setValid(true)
	s.advance(3 * time.Minute)
	mod.checkDue(s.ctx, s.p)
	got, _ = getSending(s.ctx, s.p, "shop")
	if got.State != SendingVerified || !got.SenderSet || got.SenderChange == "" || got.VerifiedAt.IsZero() || s.from() != "hello@shop.test" {
		t.Fatalf("verified: %+v from=%s", got, s.from())
	}
	c, err := s.p.DB.GetChange(s.ctx, got.SenderChange)
	if err != nil || c.Actor.Kind != "system" || !strings.Contains(c.Intent, "SendGrid verified shop.test") {
		t.Fatalf("change: %+v %v", c, err)
	}
	if _, res, _ := s.p.DB.Load(s.ctx, "shop"); !strings.Contains(string(res[change.KindService+"/email"].Spec), "hello@shop.test") {
		t.Fatalf("manifest: %s", res[change.KindService+"/email"].Spec)
	}

	// Starting again reuses SendGrid's domain (no second create) and, being
	// verified already, keeps the sender as it is.
	if code, _ := s.call(s.owner, "DELETE", "/v1/projects/shop/email/sending-domain", ""); code != 200 {
		t.Fatal("delete")
	}
	n := len(s.dns.set)
	code, v = s.call(s.owner, "POST", "/v1/projects/shop/email/sending-domain", `{"domain":"shop.test","local":"team"}`)
	sd = setupOf(t, v)
	if code != 200 || fake.creates != 1 || sd["state"] != "verified" || sd["senderSet"] != true || sd["from"] != "hello@shop.test" || len(s.dns.set) != n+1 {
		t.Fatalf("idempotent start: %d creates=%d dns=%d %v", code, fake.creates, len(s.dns.set)-n, sd)
	}
}

func TestSendFromMyDomainGivesUpAndPermissions(t *testing.T) {
	fake := &fakeSendGrid{key: "SG.full"}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	old := sendgridAPI
	sendgridAPI = srv.URL
	t.Cleanup(func() { sendgridAPI = old })

	// A Mail Send-only key: a plain message naming the permission.
	s := newSendingRig(t, ProviderSendGrid, "SG.sendonly")
	code, m := s.call(s.owner, "POST", "/v1/projects/shop/email/sending-domain", `{"domain":"shop.test"}`)
	if code != 422 || !strings.Contains(m["detail"].(string), "can't set up domains") || !strings.Contains(m["hint"].(string), "Sender Authentication") {
		t.Fatalf("permission: %d %v", code, m)
	}
	if g, _ := getSending(s.ctx, s.p, "shop"); g != nil {
		t.Fatal("a refused start was saved")
	}

	// A domain whose DNS isn't connected: the records are listed to copy.
	pw := "SG.full"
	_ = setRelay(s.ctx, s.p, &Relay{Provider: ProviderSendGrid, Host: "smtp.sendgrid.net", Port: 587, TLS: TLSStartTLS, Username: "apikey"}, &pw)
	code, m = s.call(s.owner, "POST", "/v1/projects/shop/email/sending-domain", `{"domain":"other.test"}`)
	sd := setupOf(t, m)
	if code != 200 || sd["dns"] != "manual" || len(s.dns.set) != 0 || !strings.Contains(sd["detail"].(string), "Add the records") {
		t.Fatalf("manual dns: %d %v", code, sd)
	}

	// 48 hours without the records: the box stops, then a person can ask again.
	for range 80 {
		s.advance(time.Hour)
		mod.checkDue(s.ctx, s.p)
	}
	got, _ := getSending(s.ctx, s.p, "shop")
	if got.State != SendingFailed || !strings.Contains(got.Detail, "48 hours") || got.Hint == "" || !got.NextCheckAt.IsZero() {
		t.Fatalf("give up: %+v", got)
	}
	before := fake.validate
	mod.checkDue(s.ctx, s.p)
	if fake.validate != before {
		t.Fatal("checked after giving up")
	}
	code, m = s.call(s.owner, "POST", "/v1/projects/shop/email/sending-domain/check", "")
	if sd := setupOf(t, m); code != 200 || sd["state"] != "verifying" || fake.validate != before+1 {
		t.Fatalf("check again: %d %v", code, m)
	}

	// The key loses the permission mid-way: a plain stop, not endless retries.
	fake.mu.Lock()
	fake.key = "SG.rotated"
	fake.mu.Unlock()
	s.advance(2 * time.Hour)
	mod.checkDue(s.ctx, s.p)
	got, _ = getSending(s.ctx, s.p, "shop")
	if got.State != SendingFailed || !strings.Contains(got.Hint, "Sender Authentication") {
		t.Fatalf("lost permission: %+v", got)
	}

	// Who may: read keys can't start one.
	op, _ := s.tm.Authenticate(s.ctx, s.owner)
	agent, _, _ := s.tm.Create(s.ctx, op, tokens.CreateRequest{Name: "agent"})
	if code, _ := s.call(agent, "POST", "/v1/projects/shop/email/sending-domain", `{"domain":"shop.test"}`); code != 403 {
		t.Fatalf("agent start: %d", code)
	}
	if code, _ := s.call(agent, "GET", "/v1/projects/shop/email/sending-domain", ""); code != 200 {
		t.Fatalf("agent read: %d", code)
	}
	// Nor can a key with every right on the project: the domain it names
	// may be anyone's, and the box's provider accounts and DNS are box-wide.
	shopKey, _, _ := s.tm.Create(s.ctx, op, tokens.CreateRequest{Name: "shop-all", Projects: []string{"shop"}, Scopes: []tokens.Scope{tokens.ScopeAll}})
	if code, _ := s.call(shopKey, "POST", "/v1/projects/shop/email/sending-domain", `{"domain":"company.test","local":"billing"}`); code != 403 {
		t.Fatalf("project key start: %d", code)
	}
	if got, _ := getSending(s.ctx, s.p, "shop"); got != nil && got.Domain == "company.test" {
		t.Fatalf("a project key set up company.test: %+v", got)
	}
}

// fakeResend is Resend's domains API.
type fakeResend struct {
	mu       sync.Mutex
	status   string
	verifies int
	created  bool
}

func (f *fakeResend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") == "Bearer re_send" {
		w.WriteHeader(401)
		io.WriteString(w, `{"statusCode":401,"name":"restricted_api_key","message":"This API key is restricted to only send emails"}`)
		return
	}
	dom := func() map[string]any {
		st := "pending"
		if f.status == "verified" {
			st = "verified"
		}
		ten := 10
		return map[string]any{"object": "domain", "id": "d91cd9bd", "name": "mail.shop.test", "status": f.status, "region": "us-east-1",
			"records": []map[string]any{
				{"record": "SPF", "name": "send.mail", "type": "MX", "ttl": "Auto", "status": st, "value": "feedback-smtp.us-east-1.amazonses.com", "priority": ten},
				{"record": "SPF", "name": "send.mail", "value": "\"v=spf1 include:amazonses.com ~all\"", "type": "TXT", "ttl": "Auto", "status": st},
				{"record": "DKIM", "name": "resend._domainkey.mail", "value": "p=MIGfMA0GCSqGSIb3DQEB", "type": "TXT", "status": st, "ttl": "Auto"},
				{"record": "Receiving", "name": "mail", "value": "inbound-smtp.us-east-1.amazonaws.com", "type": "MX", "status": "not_started", "priority": ten},
			}}
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/domains":
		data := []any{}
		if f.created {
			data = append(data, map[string]any{"id": "d91cd9bd", "name": "mail.shop.test", "status": f.status})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
	case r.Method == "POST" && r.URL.Path == "/domains":
		f.created, f.status = true, "not_started"
		_ = json.NewEncoder(w).Encode(dom())
	case r.Method == "GET" && r.URL.Path == "/domains/d91cd9bd":
		_ = json.NewEncoder(w).Encode(dom())
	case r.Method == "POST" && r.URL.Path == "/domains/d91cd9bd/verify":
		f.verifies++
		if f.status == "not_started" {
			f.status = "pending"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "domain", "id": "d91cd9bd"})
	default:
		w.WriteHeader(404)
	}
}

func TestSendFromMyDomainResend(t *testing.T) {
	fake := &fakeResend{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	old := resendAPI
	resendAPI = srv.URL
	t.Cleanup(func() { resendAPI = old })

	s := newSendingRig(t, ProviderResend, "re_send")
	code, m := s.call(s.owner, "POST", "/v1/projects/shop/email/sending-domain", `{"domain":"mail.shop.test"}`)
	if code != 422 || !strings.Contains(m["hint"].(string), "Full access") {
		t.Fatalf("sending-only key: %d %v", code, m)
	}
	pw := "re_full"
	_ = setRelay(s.ctx, s.p, &Relay{Provider: ProviderResend, Host: "smtp.resend.com", Port: 587, TLS: TLSStartTLS, Username: "resend"}, &pw)
	code, m = s.call(s.owner, "POST", "/v1/projects/shop/email/sending-domain", `{"domain":"mail.shop.test","local":"orders"}`)
	sd := setupOf(t, m)
	if code != 200 || sd["state"] != "verifying" || sd["dns"] != "auto" || fake.verifies != 1 {
		t.Fatalf("start: %d %v", code, m)
	}
	// Names relative to the apex become absolute; MX gets its priority; receiving is left out.
	byName := map[string]dnskit.Record{}
	for _, r := range s.dns.set {
		byName[r.Type+" "+r.Name] = r
	}
	if r := byName["MX send.mail.shop.test"]; r.Value != "10 feedback-smtp.us-east-1.amazonses.com" {
		t.Fatalf("mx: %+v", s.dns.set)
	}
	if r := byName["TXT resend._domainkey.mail.shop.test"]; r.Value == "" {
		t.Fatalf("dkim: %+v", s.dns.set)
	}
	if _, ok := byName["TXT _dmarc.mail.shop.test"]; !ok || len(s.dns.set) != 4 {
		t.Fatalf("records: %+v", s.dns.set)
	}

	// Pending at Resend: the box doesn't ask it to verify again, it just reads.
	s.advance(2 * time.Minute)
	mod.checkDue(s.ctx, s.p)
	if fake.verifies != 1 {
		t.Fatalf("verify called again while pending: %d", fake.verifies)
	}
	fake.mu.Lock()
	fake.status = "verified"
	fake.mu.Unlock()
	s.advance(5 * time.Minute)
	mod.checkDue(s.ctx, s.p)
	got, _ := getSending(s.ctx, s.p, "shop")
	if got.State != SendingVerified || s.from() != "orders@mail.shop.test" {
		t.Fatalf("verified: %+v from=%s", got, s.from())
	}
}

func TestSendFromMyDomainManualAndNoRelay(t *testing.T) {
	s := newSendingRig(t, "", "")
	if code, m := s.call(s.owner, "POST", "/v1/projects/shop/email/sending-domain", `{"domain":"shop.test"}`); code != 422 || !strings.Contains(m["detail"].(string), "Connect a mail service") {
		t.Fatalf("no relay: %d %v", code, m)
	}
	code, v := s.call(s.owner, "GET", "/v1/projects/shop/email/sending-domain", "")
	if code != 200 || v["relay"] != false || v["automatic"] != false {
		t.Fatalf("view: %v", v)
	}
	pw := "server-token"
	_ = setRelay(s.ctx, s.p, &Relay{Provider: ProviderPostmark, Host: "smtp.postmarkapp.com", Port: 587, TLS: TLSStartTLS}, &pw)
	code, v = s.call(s.owner, "POST", "/v1/projects/shop/email/sending-domain", `{"domain":"shop.test"}`)
	sd := setupOf(t, v)
	if code != 200 || sd["state"] != "manual" || v["automatic"] != false || !strings.Contains(sd["domainUrl"].(string), "postmarkapp.com") ||
		!strings.Contains(sd["detail"].(string), "Add shop.test in Postmark") || len(sd["records"].([]any)) == 0 || len(s.dns.set) != 0 {
		t.Fatalf("manual: %d %v", code, v)
	}
	if s.from() != "shop@tiffin.localhost" {
		t.Fatal("manual setup changed the sender")
	}
}

func TestFullName(t *testing.T) {
	for _, c := range [][3]string{
		{"send", "example.com", "send.example.com"},
		{"send", "mail.example.com", "send.mail.example.com"},
		{"send.mail", "mail.example.com", "send.mail.example.com"},
		{"s1._domainkey.example.com", "example.com", "s1._domainkey.example.com"},
		{"@", "example.com", "example.com"},
		{"resend._domainkey.mail", "mail.example.co.uk", "resend._domainkey.mail.example.co.uk"},
	} {
		if got := fullName(c[0], c[1]); got != c[2] {
			t.Errorf("fullName(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}
