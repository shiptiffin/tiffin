package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
	"github.com/btahir/tiffin/internal/tokens"
)

// fakeMailer records box mail instead of sending it.
type fakeMailer struct {
	mu      sync.Mutex
	sent    []api.BoxMail
	relayed bool
}

func (f *fakeMailer) SendBoxMail(_ context.Context, _ *platform.Platform, m api.BoxMail) (*api.BoxMailResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m)
	d := "inbox"
	if f.relayed {
		d = "relay"
	}
	return &api.BoxMailResult{Delivery: d, To: m.To}, nil
}

func (f *fakeMailer) BoxMailRelayed(context.Context, *platform.Platform) bool { return f.relayed }

func (f *fakeMailer) all() []api.BoxMail {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]api.BoxMail(nil), f.sent...)
}

type mailEnv struct {
	*env
	a *api.API
	f *fakeMailer
}

func newMailEnv(t *testing.T) *mailEnv {
	t.Helper()
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tm := tokens.NewManager(db)
	owner, _, _ := tm.Bootstrap(t.Context())
	f := &fakeMailer{}
	api.SetBoxMailer(f)
	t.Cleanup(func() { api.SetBoxMailer(nil) })
	a := api.New(api.Deps{DB: db, Engine: change.NewEngine(db), Tokens: tm, PublicURL: pkOrigin})
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	return &mailEnv{env: &env{t: t, srv: srv, owner: owner, tm: tm}, a: a, f: f}
}

// send posts JSON from client ip with extra headers and cookies.
func (e *mailEnv) send(path, ip string, body any, hdr map[string]string, cookies ...*http.Cookie) (*http.Response, map[string]any) {
	e.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", e.srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", ip)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return res, out
}

func cookieNamed(res *http.Response, name string) *http.Cookie {
	for _, c := range res.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func codeOf(url string) string { return strings.SplitN(url, "#", 2)[1] }

func TestInviteEmailsTheLink(t *testing.T) {
	e := newMailEnv(t)
	code, inv, _ := e.call(e.owner, "POST", "/v1/people", map[string]any{"name": "Maya Okafor", "email": "maya@example.com", "role": "member"})
	if code != 200 {
		t.Fatalf("invite: %d %v", code, inv)
	}
	mail, _ := inv["email"].(map[string]any)
	if mail["delivery"] != "inbox" || mail["to"] != "maya@example.com" || !strings.Contains(inv["url"].(string), "/login#tfl_") {
		t.Fatalf("invite answer: %v", inv)
	}
	sent := e.f.all()
	if len(sent) != 1 || sent[0].Kind != api.BoxMailInvite || sent[0].URL != inv["url"] || sent[0].Name != "Maya Okafor" ||
		sent[0].Role != "member" || sent[0].ExpiresAt.IsZero() || sent[0].Dashboard != pkOrigin {
		t.Fatalf("mail: %+v", sent)
	}

	// No address, or notify false: nothing is sent and the answer says nothing about mail.
	_, inv, _ = e.call(e.owner, "POST", "/v1/people", map[string]any{"name": "Sam", "role": "viewer"})
	if _, ok := inv["email"]; ok {
		t.Fatalf("no address: %v", inv)
	}
	_, inv, _ = e.call(e.owner, "POST", "/v1/people", map[string]any{"name": "Kai", "email": "kai@example.com", "role": "viewer", "notify": false})
	if _, ok := inv["email"]; ok || len(e.f.all()) != 1 {
		t.Fatalf("notify false: %v", inv)
	}
	kai := inv["person"].(map[string]any)["id"].(string)

	// A fresh link is emailed only when asked.
	_, l, _ := e.call(e.owner, "POST", "/v1/people/"+kai+"/login-link", nil)
	if _, ok := l["email"]; ok || len(e.f.all()) != 1 {
		t.Fatalf("link without email=true: %v", l)
	}
	_, l, _ = e.call(e.owner, "POST", "/v1/people/"+kai+"/login-link?email=true", nil)
	if sent := e.f.all(); len(sent) != 2 || sent[1].Kind != api.BoxMailLink || sent[1].To != "kai@example.com" || l["email"] == nil {
		t.Fatalf("link with email=true: %v %+v", l, sent)
	}

	// Addresses are unique, and people can set their own.
	if code, out, _ := e.call(e.owner, "POST", "/v1/people", map[string]any{"name": "Copy", "email": "MAYA@example.com", "role": "viewer"}); code != 422 {
		t.Fatalf("duplicate address: %d %v", code, out)
	}
	if code, p, _ := e.call(e.owner, "PUT", "/v1/people/usr_owner/email", map[string]any{"email": "owner@example.com"}); code != 200 || p["email"] != "owner@example.com" {
		t.Fatalf("owner email: %d %v", code, p)
	}
}

func TestEmailSignInLink(t *testing.T) {
	e := newMailEnv(t)
	if res, st := e.send("/v1/session/email", "203.0.113.1", nil, nil); res.StatusCode == 200 {
		t.Fatalf("POST without a body: %v", st)
	}
	req, _ := http.NewRequest("GET", e.srv.URL+"/v1/session/email", nil)
	res, _ := http.DefaultClient.Do(req)
	var st map[string]any
	_ = json.NewDecoder(res.Body).Decode(&st)
	res.Body.Close()
	if st["available"] != false {
		t.Fatalf("no relay: %v", st)
	}
	e.f.relayed = true

	e.call(e.owner, "POST", "/v1/people", map[string]any{"name": "Maya", "email": "maya@example.com", "role": "admin", "notify": false})

	// Known and unknown addresses get the same answer.
	known, kb := e.send("/v1/session/email", "203.0.113.1", map[string]any{"email": " Maya@Example.com "}, nil)
	unknown, ub := e.send("/v1/session/email", "203.0.113.2", map[string]any{"email": "nobody@example.com"}, nil)
	e.a.WaitBackground()
	kj, _ := json.Marshal(kb)
	uj, _ := json.Marshal(ub)
	if known.StatusCode != 202 || unknown.StatusCode != 202 || string(kj) != string(uj) {
		t.Fatalf("answers differ: %d %s / %d %s", known.StatusCode, kj, unknown.StatusCode, uj)
	}
	sent := e.f.all()
	if len(sent) != 1 || sent[0].Kind != api.BoxMailSignIn || sent[0].To != "maya@example.com" || sent[0].IP != "203.0.113.1" {
		t.Fatalf("mail: %+v", sent)
	}

	// The link signs Maya in once, as herself.
	res, who := e.send("/v1/session", "203.0.113.1", map[string]any{"code": codeOf(sent[0].URL)}, nil)
	if res.StatusCode != 200 || who["name"] != "Maya" || cookieNamed(res, api.SessionCookie) == nil {
		t.Fatalf("redeem: %d %v", res.StatusCode, who)
	}
	if dc := cookieNamed(res, api.DeviceCookie); dc == nil || !dc.HttpOnly || !dc.Secure || dc.MaxAge <= 0 {
		t.Fatalf("device cookie: %+v", dc)
	}
	if res, _ := e.send("/v1/session", "203.0.113.1", map[string]any{"code": codeOf(sent[0].URL)}, nil); res.StatusCode != 401 {
		t.Fatalf("second use: %d", res.StatusCode)
	}

	// Malformed addresses are refused (that says nothing about anyone).
	if res, _ := e.send("/v1/session/email", "203.0.113.3", map[string]any{"email": "not-an-address"}, nil); res.StatusCode != 422 {
		t.Fatalf("bad address: %d", res.StatusCode)
	}
	// Another site can't ask on someone's behalf.
	if res, _ := e.send("/v1/session/email", "203.0.113.4", map[string]any{"email": "maya@example.com"}, map[string]string{"Origin": "https://evil.example"}); res.StatusCode != 403 {
		t.Fatalf("cross-site: %d", res.StatusCode)
	}
}

func TestEmailSignInRateLimits(t *testing.T) {
	e := newMailEnv(t)
	e.call(e.owner, "POST", "/v1/people", map[string]any{"name": "Maya", "email": "maya@example.com", "role": "member", "notify": false})

	// Per address: the fourth request within the hour is refused, for a
	// known address and an unknown one alike.
	for j, addr := range []string{"maya@example.com", "ghost@example.com"} {
		for i := range api.EmailSignInPerAddress + 1 {
			ip := fmt.Sprintf("198.51.%d.%d", 100+j, i+1) // a different client each time
			res, out := e.send("/v1/session/email", ip, map[string]any{"email": addr}, nil)
			want := 202
			if i == api.EmailSignInPerAddress {
				want = 429
			}
			if res.StatusCode != want {
				t.Fatalf("%s request %d: %d %v", addr, i+1, res.StatusCode, out)
			}
		}
	}
	e.a.WaitBackground()
	if n := len(e.f.all()); n != api.EmailSignInPerAddress {
		t.Fatalf("emails sent: %d", n)
	}

	// Per client address: the sixth request from one IP is refused, whatever it asks for.
	for i := range api.EmailSignInPerIP + 1 {
		res, _ := e.send("/v1/session/email", "192.0.2.9", map[string]any{"email": "user" + string(rune('a'+i)) + "@example.com"}, nil)
		want := 202
		if i == api.EmailSignInPerIP {
			want = 429
		}
		if res.StatusCode != want {
			t.Fatalf("ip request %d: %d", i+1, res.StatusCode)
		}
		if want == 429 && res.Header.Get("Retry-After") == "" {
			t.Fatal("no Retry-After")
		}
	}
	e.a.WaitBackground()
}

func TestNewSignInNotice(t *testing.T) {
	e := newMailEnv(t)
	_, inv, _ := e.call(e.owner, "POST", "/v1/people", map[string]any{"name": "Maya", "email": "maya@example.com", "role": "member", "notify": false})
	id := inv["person"].(map[string]any)["id"].(string)
	mac := map[string]string{"User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Chrome/129.0 Safari/537.36"}
	phone := map[string]string{"User-Agent": "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1"}

	// The first sign-in (the invite) is quiet.
	res, _ := e.send("/v1/session", "203.0.113.5", map[string]any{"code": codeOf(inv["url"].(string))}, mac)
	device := cookieNamed(res, api.DeviceCookie)
	e.a.WaitBackground()
	if res.StatusCode != 200 || device == nil || len(e.f.all()) != 0 {
		t.Fatalf("first sign-in: %d %+v", res.StatusCode, e.f.all())
	}
	// The same browser again: quiet.
	_, l, _ := e.call(e.owner, "POST", "/v1/people/"+id+"/login-link", nil)
	res, _ = e.send("/v1/session", "203.0.113.5", map[string]any{"code": codeOf(l["url"].(string))}, mac, device)
	e.a.WaitBackground()
	if res.StatusCode != 200 || len(e.f.all()) != 0 {
		t.Fatalf("same browser: %d %+v", res.StatusCode, e.f.all())
	}
	// A new browser: Maya hears about it.
	_, l, _ = e.call(e.owner, "POST", "/v1/people/"+id+"/login-link", nil)
	res, _ = e.send("/v1/session", "198.51.100.77", map[string]any{"code": codeOf(l["url"].(string))}, phone)
	e.a.WaitBackground()
	sent := e.f.all()
	if res.StatusCode != 200 || len(sent) != 1 || sent[0].Kind != api.BoxMailNewDevice || sent[0].To != "maya@example.com" ||
		sent[0].Device != "Safari on iPhone" || sent[0].IP != "198.51.100.77" || sent[0].Via != "a sign-in link" {
		t.Fatalf("new browser: %d %+v", res.StatusCode, sent)
	}
	// Someone without an address is never mailed.
	_, inv, _ = e.call(e.owner, "POST", "/v1/people", map[string]any{"name": "Sam", "role": "viewer"})
	sam := inv["person"].(map[string]any)["id"].(string)
	e.send("/v1/session", "203.0.113.6", map[string]any{"code": codeOf(inv["url"].(string))}, mac)
	_, l, _ = e.call(e.owner, "POST", "/v1/people/"+sam+"/login-link", nil)
	e.send("/v1/session", "203.0.113.6", map[string]any{"code": codeOf(l["url"].(string))}, phone)
	e.a.WaitBackground()
	if len(e.f.all()) != 1 {
		t.Fatalf("mailed someone without an address: %+v", e.f.all())
	}
}
