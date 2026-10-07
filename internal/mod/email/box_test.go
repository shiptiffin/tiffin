package email

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/tokens"
)

func sinkRelay(t *testing.T, r *rig, provider string) *sink {
	t.Helper()
	s := newSink(t)
	host, port, _ := net.SplitHostPort(s.addr)
	pn, _ := strconv.Atoi(port)
	pw := "relaypass"
	if err := setRelay(r.ctx, r.p, &Relay{Provider: provider, Host: host, Port: pn, Username: "relayuser", TLS: TLSNone}, &pw); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRenderBoxMail(t *testing.T) {
	now := time.Date(2026, 10, 6, 21, 0, 0, 0, time.UTC)
	link := "https://dashboard.shiptiffin.com/login#tfl_abc"
	cases := []struct {
		m       api.BoxMail
		subject string
		has     []string
	}{
		{api.BoxMail{Kind: api.BoxMailInvite, Name: "Maya Okafor", Role: "member", URL: link, ExpiresAt: now.Add(7 * 24 * time.Hour), By: "Bilal", Dashboard: "https://dashboard.shiptiffin.com"},
			"You're invited to dashboard.shiptiffin.com", []string{"Hi Maya,", "Bilal added you", "as a member", link, "until 13 October 2026, 21:00 UTC"}},
		{api.BoxMail{Kind: api.BoxMailLink, Name: "Maya", URL: link, ExpiresAt: now.Add(10 * time.Minute), By: "Bilal", Dashboard: "https://dashboard.shiptiffin.com"},
			"Your new sign-in link for dashboard.shiptiffin.com", []string{"Bilal made you a new sign-in link", "for the next 10 minutes"}},
		{api.BoxMail{Kind: api.BoxMailSignIn, Name: "Maya", URL: link, ExpiresAt: now.Add(15 * time.Minute), IP: "203.0.113.9", Dashboard: "https://dashboard.shiptiffin.com"},
			"Sign in to dashboard.shiptiffin.com", []string{"the sign-in link you asked for", "for the next 15 minutes", "203.0.113.9", "Nobody can sign in without the link"}},
		{api.BoxMail{Kind: api.BoxMailNewDevice, Name: "Owner", Role: "owner", Device: "Safari on iPhone", IP: "198.51.100.7", Where: "United Kingdom", Via: "Passkey", At: now, Dashboard: "https://dashboard.shiptiffin.com"},
			"New sign-in to ShipTiffin from Safari on iPhone", []string{"\nThere's a new sign-in to your box from a browser it hasn't seen before.", "Device: Safari on iPhone", "When: Tuesday 6 October, 21:00 UTC",
				"How: Passkey", "Where: United Kingdom · 198.51.100.7", "Review sign-ins: https://dashboard.shiptiffin.com/settings/sign-ins",
				"Wasn't you? Choose Sign out everywhere else on that page, then remove any passkey you don't recognise (https://dashboard.shiptiffin.com/settings/passkeys) and",
				"revoke API keys you didn't make: https://dashboard.shiptiffin.com/settings/keys", "Sent once per new browser · ShipTiffin"}},
	}
	exp := time.Date(2027, 1, 4, 21, 0, 0, 0, time.UTC)
	cases = append(cases,
		struct {
			m       api.BoxMail
			subject string
			has     []string
		}{api.BoxMail{Kind: api.BoxMailNewKey, Name: "Maya Okafor", Role: "admin", Device: "Chrome on macOS", IP: "198.51.100.7", Where: "United Kingdom", At: now,
			Dashboard: "https://dashboard.shiptiffin.com", Key: &tokens.Key{Name: "ci", Projects: tokens.Projects{tokens.AllProjects}, Access: "full", Admin: true}},
			`New API key "ci" on ShipTiffin`, []string{"\nMaya, an API key was just created on your box from your dashboard sign-in. It works until it expires or you revoke it, even after you sign out.",
				"Key: ci", "Access: Full access to all projects (admin", "Expires: Never", "By: Maya Okafor", "When: Tuesday 6 October, 21:00 UTC",
				"Device: Chrome on macOS", "Where: United Kingdom · 198.51.100.7", "Review API keys: https://dashboard.shiptiffin.com/settings/keys",
				"Wasn't you? Revoke the key on that page, then sign out everywhere else: https://dashboard.shiptiffin.com/settings/sign-ins",
				"Sent for every API key made in the dashboard · ShipTiffin"}},
		struct {
			m       api.BoxMail
			subject string
			has     []string
		}{api.BoxMail{Kind: api.BoxMailNewKey, Name: "Maya", Role: "admin", At: now, Dashboard: "https://dashboard.shiptiffin.com",
			Key: &tokens.Key{Name: "reader", Projects: tokens.Projects{"shop", "blog"}, Access: "read", ExpiresAt: &exp}},
			`New API key "reader" on ShipTiffin`, []string{"Access: Read only: shop, blog", "Expires: 4 January 2027, 21:00 UTC"}},
	)
	for _, c := range cases {
		e, err := renderBoxMail(c.m, "shiptiffin.com", now)
		if err != nil {
			t.Fatal(err)
		}
		if e.Subject != c.subject {
			t.Errorf("%s subject: %q", c.m.Kind, e.Subject)
		}
		for _, h := range c.has {
			if !strings.Contains(e.Text, h) {
				t.Errorf("%s text lacks %q:\n%s", c.m.Kind, h, e.Text)
			}
		}
		if c.m.URL != "" && !strings.Contains(e.HTML, `href="`+c.m.URL+`"`) {
			t.Errorf("%s html has no button to the link", c.m.Kind)
		}
		// No tracking: the one image is the mark, from the box's own dashboard.
		if n := strings.Count(e.HTML, "src="); n != 1 || !strings.Contains(e.HTML, `src="https://dashboard.shiptiffin.com/email-mark.png"`) ||
			strings.Contains(e.HTML, "<script") {
			t.Errorf("%s html loads something else (%d sources)", c.m.Kind, n)
		}
		if !strings.Contains(e.HTML, "ShipTiffin") || strings.Contains(e.Text, "{{") {
			t.Errorf("%s: brand missing or placeholder left", c.m.Kind)
		}
	}
	if _, err := renderBoxMail(api.BoxMail{Kind: api.BoxMailNewKey}, "x", now); err == nil {
		t.Fatal("new-key without a key")
	}
	// The new-key notice has the button, and no device or address rows when unknown.
	nk, _ := renderBoxMail(api.BoxMail{Kind: api.BoxMailNewKey, Name: "Maya", At: now, Dashboard: "https://dashboard.shiptiffin.com",
		Key: &tokens.Key{Name: "<b>k</b>", Projects: tokens.Projects{"shop"}, Access: "read"}}, "shiptiffin.com", now)
	if !strings.Contains(nk.HTML, `href="https://dashboard.shiptiffin.com/settings/keys"`) || !strings.Contains(nk.HTML, "Review API keys") ||
		strings.Contains(nk.Text, "Device:") || strings.Contains(nk.Text, "Where:") || strings.Contains(nk.HTML, "<b>k</b>") {
		t.Fatalf("new-key without device:\n%s", nk.Text)
	}
	// Passkey notices: the passkey, when, the browser and address, the button
	// to the passkeys page and "Wasn't you?" to the sign-ins page.
	np, err := renderBoxMail(api.BoxMail{Kind: api.BoxMailNewPasskey, Name: "Maya Okafor", Role: "member", Passkey: "MacBook", Device: "Chrome on macOS",
		IP: "198.51.100.7", Where: "United Kingdom", At: now, Dashboard: "https://dashboard.shiptiffin.com"}, "shiptiffin.com", now)
	if err != nil {
		t.Fatal(err)
	}
	if np.Subject != `New passkey "MacBook" on ShipTiffin` {
		t.Errorf("new-passkey subject: %q", np.Subject)
	}
	for _, want := range []string{"\nMaya, a passkey was just added to your sign-ins. From now on it signs in to your box as you.", "Passkey: MacBook",
		"When: Tuesday 6 October, 21:00 UTC", "Device: Chrome on macOS", "Where: United Kingdom · 198.51.100.7", "If you added it, there's nothing to do.",
		"Review passkeys: https://dashboard.shiptiffin.com/settings/passkeys",
		"Wasn't you? Remove it on that page, then sign out everywhere else: https://dashboard.shiptiffin.com/settings/sign-ins",
		"Sent for every passkey added to your sign-ins · ShipTiffin"} {
		if !strings.Contains(np.Text, want) {
			t.Errorf("new-passkey text lacks %q:\n%s", want, np.Text)
		}
	}
	for _, want := range []string{`href="https://dashboard.shiptiffin.com/settings/passkeys"`, "Review passkeys", `href="https://dashboard.shiptiffin.com/settings/sign-ins"`,
		"rgb(242,176,54)", "rgb(37,23,12)", "MacBook", "United Kingdom"} {
		if !strings.Contains(np.HTML, want) {
			t.Errorf("new-passkey HTML lacks %q", want)
		}
	}
	pr, err := renderBoxMail(api.BoxMail{Kind: api.BoxMailPasskeyRemoved, Name: "Owner", Passkey: "<b>Old phone</b>", At: now,
		Dashboard: "https://dashboard.shiptiffin.com"}, "shiptiffin.com", now)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Subject != `Passkey "<b>Old phone</b>" removed on ShipTiffin` {
		t.Errorf("passkey-removed subject: %q", pr.Subject)
	}
	for _, want := range []string{"\nA passkey was just removed from your sign-ins. It no longer signs in to your box.", "Passkey: <b>Old phone</b>",
		"Review passkeys: https://dashboard.shiptiffin.com/settings/passkeys",
		"Wasn't you? Someone may be signed in as you: sign out everywhere else (https://dashboard.shiptiffin.com/settings/sign-ins), then remove any passkey you don't recognise.",
		"Sent for every passkey removed from your sign-ins · ShipTiffin"} {
		if !strings.Contains(pr.Text, want) {
			t.Errorf("passkey-removed text lacks %q:\n%s", want, pr.Text)
		}
	}
	// Unknown browser and address leave their rows out; the name is escaped.
	if strings.Contains(pr.Text, "Device:") || strings.Contains(pr.Text, "Where:") || strings.Contains(pr.HTML, "<b>Old phone</b>") || !strings.Contains(pr.HTML, "&lt;b&gt;Old phone&lt;/b&gt;") {
		t.Errorf("passkey-removed without device:\n%s", pr.Text)
	}
	if _, err := renderBoxMail(api.BoxMail{Kind: api.BoxMailNewPasskey}, "x", now); err == nil {
		t.Fatal("new-passkey without a passkey")
	}
	if _, err := renderBoxMail(api.BoxMail{Kind: "nope"}, "x", now); err == nil {
		t.Fatal("unknown kind")
	}
	// HTML escapes what people typed.
	e, _ := renderBoxMail(api.BoxMail{Kind: api.BoxMailInvite, Name: "<b>x</b>", By: "<script>", URL: link, Role: "viewer"}, "x", now)
	if strings.Contains(e.HTML, "<script>") || strings.Contains(e.HTML, "<b>x</b>") {
		t.Fatal("html not escaped")
	}
}

// The new sign-in notice: one opening line, the facts, and what to do, with
// the dashboard's address written once (under the button), never as bare text.
func TestNewSignInVariants(t *testing.T) {
	now := time.Date(2026, 10, 7, 19, 54, 0, 0, time.UTC)
	base := api.BoxMail{Kind: api.BoxMailNewDevice, Name: "Maya Okafor", Role: "member", Device: "Chrome on macOS", IP: "203.0.113.4",
		Via: "Google", At: now, Dashboard: "https://dashboard.shiptiffin.com"}
	e, err := renderBoxMail(base, "shiptiffin.com", now)
	if err != nil {
		t.Fatal(err)
	}
	if e.Subject != "New sign-in to ShipTiffin from Chrome on macOS" {
		t.Errorf("subject: %q", e.Subject)
	}
	for _, h := range []string{"Maya, there's a new sign-in to your box from a browser it hasn't seen before.", "How: Google", "Where: 203.0.113.4",
		"and tell the box's owner.", "Review sign-ins: https://dashboard.shiptiffin.com/settings/sign-ins"} {
		if !strings.Contains(e.Text, h) {
			t.Errorf("member text lacks %q:\n%s", h, e.Text)
		}
	}
	if strings.Contains(e.Text, "/settings/keys") || strings.Contains(e.HTML, "/settings/keys") {
		t.Error("a member was sent to API keys")
	}
	// The address shows once, as the link under the button; nothing else names the dashboard.
	if n := strings.Count(e.HTML, ">dashboard.shiptiffin.com"); n != 1 || !strings.Contains(e.HTML, ">dashboard.shiptiffin.com/settings/sign-ins</a>") {
		t.Errorf("the address shows %d times", n)
	}
	// "Wasn't you?" links sign out everywhere else and passkeys.
	if !strings.Contains(e.HTML, `href="https://dashboard.shiptiffin.com/settings/sign-ins"`) || !strings.Contains(e.HTML, `href="https://dashboard.shiptiffin.com/settings/passkeys"`) ||
		!strings.Contains(e.HTML, "Sign out everywhere else</a>") {
		t.Error("no links to sign out everywhere else and to passkeys")
	}
	if strings.Contains(e.HTML, "Security notice") || strings.Contains(e.Text, "Hi ") {
		t.Error("old copy left")
	}

	// Without a dashboard address: no button, no links; an unknown browser reads in the subject.
	e, _ = renderBoxMail(api.BoxMail{Kind: api.BoxMailNewDevice, Name: "Owner", Role: "owner", At: now}, "shiptiffin.com", now)
	if e.Subject != "New sign-in to ShipTiffin from an unknown browser" || strings.Contains(e.HTML, "href=") ||
		!strings.Contains(e.Text, "Device: An unknown browser") || !strings.Contains(e.Text, "How: Sign-in link") || strings.Contains(e.Text, "Where:") {
		t.Errorf("bare notice: %q\n%s", e.Subject, e.Text)
	}
}

func TestBoxMailInboxAndRelay(t *testing.T) {
	r := newRig(t)
	link := "https://dashboard.tiffin.localhost:8443/login#tfl_abc"
	res, err := mod.SendBoxMail(r.ctx, r.p, api.BoxMail{Kind: api.BoxMailSignIn, To: "maya@inbox.dev", Name: "Maya", URL: link, ExpiresAt: time.Now().Add(15 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Delivery != DeliveryInbox || !strings.Contains(res.Detail, "dev inbox") {
		t.Fatalf("no relay: %+v", res)
	}
	if mod.BoxMailRelayed(r.ctx, r.p) {
		t.Fatal("relayed without a relay")
	}
	recs, _ := listRecords(r.ctx, r.p.DB.SQL(), ListFilter{Project: boxProject, All: true})
	if len(recs) != 1 || recs[0].Source != "box" || !strings.Contains(recs[0].From, "Tiffin") || !strings.Contains(recs[0].From, "<hello@tiffin.localhost>") || recs[0].Subject != "Sign in to dashboard.tiffin.localhost" {
		t.Fatalf("box inbox: %+v", recs)
	}
	d, _ := mod.detail(r.ctx, r.p, boxProject, recs[0].ID, true)
	if len(d.Links) == 0 || d.Links[0] != link {
		t.Fatalf("links: %v", d.Links)
	}
	// Project inboxes don't see box mail.
	if len(r.inbox(true)) != 0 {
		t.Fatal("box mail leaked into a project")
	}

	// Through a SendGrid relay: tracking is off for box mail and it is marked automatic.
	s := sinkRelay(t, r, ProviderSendGrid)
	if !mod.BoxMailRelayed(r.ctx, r.p) {
		t.Fatal("not relayed")
	}
	res, err = mod.SendBoxMail(r.ctx, r.p, api.BoxMail{Kind: api.BoxMailInvite, To: "kai@inbox.dev", Name: "Kai", Role: "viewer", URL: link, ExpiresAt: time.Now().Add(time.Hour), By: "Owner"})
	if err != nil || res.Delivery != DeliveryRelay {
		t.Fatalf("relay: %+v %v", res, err)
	}
	waitFor(t, "relay delivery", func() bool { return s.count() == 1 })
	s.mu.Lock()
	got := s.got[0]
	s.mu.Unlock()
	for _, want := range []string{`"clicktrack":{"settings":{"enable":0}}`, `"opentrack":{"settings":{"enable":0}}`, `"tiffin_id":"msg_`, "Auto-Submitted: auto-generated", "To: \"Kai\" <kai@inbox.dev>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("relayed message lacks %q:\n%s", want, got)
		}
	}
}

func TestBoxSenderAPIAndInvites(t *testing.T) {
	r := newRig(t)
	owner, _, _ := r.tm.Bootstrap(r.ctx)
	op, _ := r.tm.Authenticate(r.ctx, owner)
	agent, _, _ := r.tm.Create(r.ctx, op, tokens.CreateRequest{Name: "agent"})
	a := api.New(api.Deps{DB: r.p.DB, Engine: r.p.Engine, Tokens: r.tm, Platform: r.p, PublicURL: r.p.PublicURL})
	call := func(tok, method, path, body string) (int, map[string]any, []any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, req)
		var m map[string]any
		var l []any
		if strings.HasPrefix(w.Body.String(), "[") {
			_ = json.Unmarshal(w.Body.Bytes(), &l)
		} else {
			_ = json.Unmarshal(w.Body.Bytes(), &m)
		}
		return w.Code, m, l
	}
	if code, m, _ := call(agent, "GET", "/v1/email/box", ""); code != 200 || m["from"] != "Tiffin <hello@tiffin.localhost>" || m["default"] != true || m["mode"] != "inbox" {
		t.Fatalf("default sender: %d %v", code, m)
	}
	if code, _, _ := call(agent, "PUT", "/v1/email/box", `{"from":"x@y.z"}`); code != 403 {
		t.Fatalf("agent sets sender: %d", code)
	}
	if code, m, _ := call(owner, "PUT", "/v1/email/box", `{"from":"not an address"}`); code != 422 {
		t.Fatalf("bad sender: %d %v", code, m)
	}
	if code, m, _ := call(owner, "PUT", "/v1/email/box", `{"from":"ShipTiffin <hello@shiptiffin.com>","replyTo":"help@shiptiffin.com"}`); code != 200 ||
		m["from"] != "ShipTiffin <hello@shiptiffin.com>" || m["replyTo"] != "help@shiptiffin.com" || m["default"] != false {
		t.Fatalf("set sender: %d %v", code, m)
	}

	// An invite with an address is emailed (into the box's dev inbox: no relay).
	code, inv, _ := call(owner, "POST", "/v1/people", `{"name":"Maya Okafor","email":"maya@inbox.dev","role":"member"}`)
	if code != 200 {
		t.Fatalf("invite: %d %v", code, inv)
	}
	if e, _ := inv["email"].(map[string]any); e["delivery"] != "inbox" {
		t.Fatalf("invite email: %v", inv)
	}
	if code, _, _ := call(agent, "GET", "/v1/email/box/messages", ""); code != 403 {
		t.Fatalf("agent reads box mail: %d", code)
	}
	code, _, list := call(owner, "GET", "/v1/email/box/messages", "")
	if code != 200 || len(list) != 1 {
		t.Fatalf("box messages: %d %v", code, list)
	}
	msg := list[0].(map[string]any)
	if msg["subject"] != "You're invited to dashboard.tiffin.localhost" || !strings.Contains(msg["from"].(string), "<hello@shiptiffin.com>") {
		t.Fatalf("invite message: %v", msg)
	}
	code, d, _ := call(owner, "GET", "/v1/email/box/messages/"+msg["id"].(string), "")
	if code != 200 || d["links"].([]any)[0] != inv["url"] || !strings.Contains(d["text"].(string), "as a member") {
		t.Fatalf("invite detail: %d %v", code, d)
	}
	headers, _ := d["headers"].([]any)
	reply := false
	for _, h := range headers {
		if hm := h.(map[string]any); hm["name"] == "Reply-To" && strings.Contains(hm["value"].(string), "help@shiptiffin.com") {
			reply = true
		}
	}
	if !reply {
		t.Fatalf("no Reply-To: %v", headers)
	}
	_ = http.MethodGet
}
