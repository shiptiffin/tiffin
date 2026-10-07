package api_test

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/passkeys"
	"github.com/btahir/tiffin/internal/passkeys/passkeytest"
	"github.com/btahir/tiffin/internal/state"
	"github.com/btahir/tiffin/internal/tokens"
)

// addPasskeyFrom adds a passkey named name in session token, from client ip,
// and returns the HTTP status and body of each step's refusal (0, nil when
// both steps succeed) and the credential.
func (e *env) addPasskeyFrom(token, ip, name string, a *passkeytest.Authenticator) (int, map[string]any, *passkeytest.Credential) {
	e.t.Helper()
	code, opts := e.callFrom(token, ip, "POST", "/v1/passkeys/register", nil)
	if code != 200 {
		return code, opts, nil
	}
	raw, _ := json.Marshal(opts)
	resp, c, err := a.Create(raw)
	if err != nil {
		e.t.Fatal(err)
	}
	if code, out := e.callFrom(token, ip, "POST", "/v1/passkeys", map[string]any{"name": name, "credential": json.RawMessage(resp)}); code != 200 {
		return code, out, nil
	}
	return 0, nil, c
}

// Adding a passkey in a dashboard session needs a recent strong sign-in (or
// a confirmation with one of the person's passkeys), like creating a
// long-lived key; the person is emailed about every passkey added or
// removed, and the audit log records both. The owner token is not asked.
func TestPasskeyAddNeedsSudoAndMails(t *testing.T) {
	f := &fakeMailer{relayed: true}
	api.SetBoxMailer(f)
	t.Cleanup(func() { api.SetBoxMailer(nil) })
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tm := tokens.NewManager(db)
	owner, _, _ := tm.Bootstrap(t.Context())
	am, err := passkeys.New(db, "dashboard.tiffin.localhost", pkOrigin)
	if err != nil {
		t.Fatal(err)
	}
	box := api.New(api.Deps{DB: db, Engine: change.NewEngine(db), Tokens: tm, Passkeys: am, PublicURL: pkOrigin})
	srv := httptest.NewServer(box.Handler())
	t.Cleanup(srv.Close)
	e := &env{t: t, srv: srv, owner: owner, tm: tm}
	a := passkeytest.New(pkOrigin)

	_, inv, _ := e.call(e.owner, "POST", "/v1/people", map[string]any{"name": "Ada Byron", "email": "ada@example.com", "role": "admin", "notify": false})
	ada := inv["person"].(map[string]any)["id"].(string)
	res, _ := e.post("/v1/session", "198.51.100.4", map[string]any{"code": codeOf(inv["url"].(string))})
	link := sessionCookie(t, res).Value // a link someone else made: not a strong sign-in

	// Ada has no passkey and signed in with an invite: both steps refuse.
	code, out, _ := e.addPasskeyFrom(link, "198.51.100.4", "Stolen", a)
	if code != 403 || out["code"] != "reauth_required" || !strings.Contains(out["detail"].(string), "adding a passkey") ||
		!strings.Contains(out["hint"].(string), "/v1/session/confirm") || strings.Contains(out["hint"].(string), "read-only key") {
		t.Fatalf("begin without sudo: %d %v", code, out)
	}
	if code, out := e.callFrom(link, "198.51.100.4", "POST", "/v1/passkeys", map[string]any{"name": "Stolen", "credential": map[string]any{"id": "x"}}); code != 403 || out["code"] != "reauth_required" {
		t.Fatalf("finish without sudo: %d %v", code, out)
	}

	// Nor can that session point Ada's emailed sign-in links (and notices)
	// at another inbox, or clear the address.
	for _, addr := range []string{"mallory@example.net", ""} {
		if code, out := e.callFrom(link, "198.51.100.4", "PUT", "/v1/people/"+ada+"/email", map[string]any{"email": addr}); code != 403 || out["code"] != "reauth_required" ||
			!strings.Contains(out["detail"].(string), "changing an email address") {
			t.Fatalf("email %q without sudo: %d %v", addr, code, out)
		}
	}
	if code, out := e.callFrom(link, "198.51.100.4", "PUT", "/v1/people/"+ada+"/email", map[string]any{"email": "ADA@example.com"}); code != 200 {
		t.Fatalf("same address: %d %v", code, out)
	}
	if code, out, _ := e.call(e.owner, "PUT", "/v1/people/"+ada+"/email", map[string]any{"email": "ada@example.com"}); code != 200 {
		t.Fatalf("owner token sets an email: %d %v", code, out)
	}

	// The owner token adds the owner's passkey: it never confirms.
	if code, out, _ := e.addPasskeyFrom(e.owner, "198.51.100.1", "Owner Mac", a); code != 0 {
		t.Fatalf("owner token: %d %v", code, out)
	}

	// An emailed link she asked for is a strong sign-in: she adds a passkey.
	if res, out := e.post("/v1/session/email", "198.51.100.8", map[string]any{"email": "ada@example.com"}); res.StatusCode != 202 {
		t.Fatalf("email me a link: %d %v", res.StatusCode, out)
	}
	box.WaitBackground()
	var signIn *api.BoxMail
	for _, m := range f.all() {
		if m.Kind == api.BoxMailSignIn {
			signIn = &m
		}
	}
	if signIn == nil {
		t.Fatalf("no sign-in email: %+v", f.all())
	}
	res, _ = e.post("/v1/session", "198.51.100.8", map[string]any{"code": codeOf(signIn.URL)})
	emailed := sessionCookie(t, res).Value
	code, out, mac := e.addPasskeyFrom(emailed, "198.51.100.8", "MacBook", a)
	if code != 0 {
		t.Fatalf("emailed-link session: %d %v", code, out)
	}

	// Back in the invite session, her new passkey confirms it's her; then
	// that session may add one too.
	if code, out := e.confirm(link, a, mac); code != 200 {
		t.Fatalf("confirm: %d %v", code, out)
	}
	if code, out, _ := e.addPasskeyFrom(link, "198.51.100.4", "Phone", a); code != 0 {
		t.Fatalf("confirmed session: %d %v", code, out)
	}

	// Removing one is allowed (and mailed).
	_, _, list := e.call(link, "GET", "/v1/passkeys", nil)
	var phone string
	for _, p := range list {
		if p := p.(map[string]any); p["name"] == "Phone" {
			phone = p["id"].(string)
		}
	}
	if code, out := e.callFrom(link, "198.51.100.4", "DELETE", "/v1/passkeys/"+phone, nil); code != 204 && code != 200 {
		t.Fatalf("remove: %d %v", code, out)
	}

	// Ada was told about each change to her passkeys, with where from.
	box.WaitBackground()
	var added, removed []api.BoxMail
	for _, m := range f.all() {
		switch m.Kind {
		case api.BoxMailNewPasskey:
			added = append(added, m)
		case api.BoxMailPasskeyRemoved:
			removed = append(removed, m)
		}
	}
	if len(added) != 2 || len(removed) != 1 {
		t.Fatalf("passkey emails: %d added (want MacBook, Phone), %d removed: %+v", len(added), len(removed), f.all())
	}
	if m := added[0]; m.To != "ada@example.com" || m.Name != "Ada Byron" || m.Passkey != "MacBook" || m.IP != "198.51.100.8" || m.Device == "" || m.At.IsZero() {
		t.Fatalf("new-passkey email: %+v", m)
	}
	if m := added[1]; m.Passkey != "Phone" || m.IP != "198.51.100.4" {
		t.Fatalf("second new-passkey email: %+v", m)
	}
	if m := removed[0]; m.To != "ada@example.com" || m.Passkey != "Phone" || m.IP != "198.51.100.4" || m.At.IsZero() {
		t.Fatalf("passkey-removed email: %+v", m)
	}
	for _, m := range f.all() {
		if (m.Kind == api.BoxMailNewPasskey || m.Kind == api.BoxMailPasskeyRemoved) && m.Passkey == "Stolen" {
			t.Fatalf("mail for a refused passkey: %+v", m)
		}
	}

	// The audit log says who added and removed what.
	_, _, events := e.call(e.owner, "GET", "/v1/audit?limit=200", nil)
	var add, del bool
	for _, ev := range events {
		ev := ev.(map[string]any)
		d, _ := ev["detail"].(map[string]any)
		if ev["action"] == "passkey.add" && d["name"] == "MacBook" {
			add = d["person"] == ada && d["summary"] == "Ada Byron added the passkey MacBook"
		}
		if ev["action"] == "passkey.delete" && ev["target"] == phone {
			del = d["person"] == ada && d["name"] == "Phone" && d["summary"] == "Ada Byron removed the passkey Phone"
		}
	}
	if !add || !del {
		t.Fatalf("audit: add %v, delete %v: %v", add, del, events)
	}
}

// A sign-in link asked for by email counts as proof the person reads that
// inbox only if the mail left the box: with no mail service, or when it
// stays in the dev inbox (which admins can read), there's no working link.
func TestEmailSignInLinkMustLeaveTheBox(t *testing.T) {
	e := newMailEnv(t)
	e.call(e.owner, "POST", "/v1/people", map[string]any{"name": "Maya", "email": "maya@example.com", "role": "admin", "notify": false})

	// No relay: nothing is made or sent.
	if res, _ := e.send("/v1/session/email", "203.0.113.1", map[string]any{"email": "maya@example.com"}, nil); res.StatusCode != 202 {
		t.Fatalf("no relay: %d", res.StatusCode)
	}
	e.a.WaitBackground()
	if sent := e.f.all(); len(sent) != 0 {
		t.Fatalf("mailed without a relay: %+v", sent)
	}

	// A relay, but this address's mail stays in the dev inbox: the link is dead.
	e.f.relayed, e.f.inboxTo = true, "maya@example.com"
	if res, _ := e.send("/v1/session/email", "203.0.113.1", map[string]any{"email": "maya@example.com"}, nil); res.StatusCode != 202 {
		t.Fatalf("inbox: %d", res.StatusCode)
	}
	e.a.WaitBackground()
	sent := e.f.all()
	if len(sent) != 1 || sent[0].Kind != api.BoxMailSignIn {
		t.Fatalf("mail: %+v", sent)
	}
	if res, _ := e.send("/v1/session", "203.0.113.1", map[string]any{"code": codeOf(sent[0].URL)}, nil); res.StatusCode != 401 {
		t.Fatalf("a link left in the dev inbox signed in: %d", res.StatusCode)
	}
}
