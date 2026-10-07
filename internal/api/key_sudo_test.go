package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
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

// callFrom is call from client address ip (through the loopback edge).
func (e *env) callFrom(token, ip, method, path string, body any) (int, map[string]any) {
	e.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, e.srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", ip)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return res.StatusCode, out
}

// confirm runs confirm options → authenticator → confirm in session token.
func (e *env) confirm(token string, a *passkeytest.Authenticator, c *passkeytest.Credential) (int, map[string]any) {
	e.t.Helper()
	code, opts, _ := e.call(token, "POST", "/v1/session/confirm/options", nil)
	if code != 200 {
		return code, opts
	}
	raw, _ := json.Marshal(opts)
	cred, err := a.Get(raw, c)
	if err != nil {
		e.t.Fatal(err)
	}
	code, out, _ := e.call(token, "POST", "/v1/session/confirm", map[string]any{"credential": json.RawMessage(cred)})
	return code, out
}

// Creating a long-lived or full-access key in the dashboard needs a recent
// strong sign-in; the key outlives its session; the person is emailed and
// the audit log records it. Keys and the owner token are not asked.
func TestDashboardKeySudoMailAndLifetime(t *testing.T) {
	f := &fakeMailer{}
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
	link := sessionCookie(t, res).Value // signed in with a link someone made: not strong

	// Refused until Ada confirms it's her; a read-only key for a day is fine.
	for _, body := range []map[string]any{
		{"name": "ci", "projects": "all", "access": "full", "expiresInDays": 1},
		{"name": "ci", "projects": "all", "access": "read"},
		{"name": "ci", "projects": "all", "access": "read", "expiresInDays": 0},
		{"name": "ci", "projects": []string{"shop"}, "access": "full", "expiresInDays": 30},
	} {
		code, out := e.callFrom(link, "198.51.100.4", "POST", "/v1/tokens", body)
		if code != 403 || out["code"] != "reauth_required" || !strings.Contains(out["hint"].(string), "passkey") {
			t.Fatalf("%v: %d %v", body, code, out)
		}
	}
	if code, out := e.callFrom(link, "198.51.100.4", "POST", "/v1/tokens", map[string]any{"name": "peek", "projects": "all", "access": "read", "expiresInDays": 1}); code != 200 {
		t.Fatalf("read key for a day: %d %v", code, out)
	}

	// Someone else's passkey doesn't confirm Ada; API keys can't confirm at all.
	_, binv, _ := e.call(e.owner, "POST", "/v1/people", map[string]any{"name": "Bob", "role": "admin", "notify": false})
	res, _ = e.post("/v1/session", "198.51.100.5", map[string]any{"code": codeOf(binv["url"].(string))})
	bobCred := e.addPasskey(sessionCookie(t, res).Value, a)
	if code, out := e.confirm(link, a, bobCred); code != 403 || !strings.Contains(out["detail"].(string), "someone else") {
		t.Fatalf("confirmed with Bob's passkey: %d %v", code, out)
	}
	if code, _, _ := e.call(e.owner, "POST", "/v1/session/confirm/options", nil); code != 403 {
		t.Fatalf("owner token asked to confirm: %d", code)
	}

	// Ada adds a passkey and confirms with it: 10 minutes to make keys.
	adaCred := e.addPasskey(link, a)
	code, out := e.confirm(link, a, adaCred)
	if code != 200 || out["confirmedUntil"] == nil {
		t.Fatalf("confirm: %d %v", code, out)
	}
	code, out = e.callFrom(link, "198.51.100.4", "POST", "/v1/tokens", map[string]any{"name": "ci", "projects": "all", "access": "full", "expiresInDays": 0})
	if code != 200 || out["key"].(map[string]any)["expiresAt"] != nil || out["key"].(map[string]any)["admin"] != true {
		t.Fatalf("never-expiring admin key: %d %v", code, out)
	}
	ci := out["secret"].(string)

	// A passkey sign-in is strong on its own.
	res, _, _ = e.passkeySignIn(a, adaCred, "198.51.100.6")
	pk := sessionCookie(t, res).Value
	code, out = e.callFrom(pk, "198.51.100.6", "POST", "/v1/tokens", map[string]any{"name": "mcp", "projects": []string{"shop"}, "access": "full", "expiresInDays": 365})
	if code != 200 {
		t.Fatalf("passkey session: %d %v", code, out)
	}
	mcp := out["secret"].(string)

	// The owner token isn't a session: no confirmation, no email.
	if code, out, _ := e.call(e.owner, "POST", "/v1/tokens", map[string]any{"name": "cli", "projects": "all", "access": "full", "expiresInDays": 0}); code != 200 {
		t.Fatalf("owner token: %d %v", code, out)
	}

	// Ada was emailed about each key made in her sessions, with where from.
	box.WaitBackground()
	var keys []api.BoxMail
	for _, m := range f.all() {
		if m.Kind == api.BoxMailNewKey {
			keys = append(keys, m)
		}
	}
	if len(keys) != 3 {
		t.Fatalf("%d new-key emails, want 3 (peek, ci, mcp): %+v", len(keys), keys)
	}
	m := keys[1]
	if m.To != "ada@example.com" || m.Name != "Ada Byron" || m.Key == nil || m.Key.Name != "ci" || !m.Key.Admin || m.Key.ExpiresAt != nil ||
		m.IP != "198.51.100.4" || m.At.IsZero() {
		t.Fatalf("new-key email: %+v", m)
	}
	if keys[2].Key.Name != "mcp" || keys[2].IP != "198.51.100.6" || keys[2].Key.ExpiresAt == nil {
		t.Fatalf("second email: %+v", keys[2])
	}

	// The audit log says who made it.
	_, _, events := e.call(e.owner, "GET", "/v1/audit?limit=200", nil)
	var made, confirmed bool
	for _, ev := range events {
		ev := ev.(map[string]any)
		d, _ := ev["detail"].(map[string]any)
		if ev["action"] == "token.create" && d["name"] == "ci" {
			made = d["person"] == ada && d["summary"] == "Ada Byron created the API key ci" && d["admin"] == true
		}
		if ev["action"] == "session.confirm" && ev["target"] == ada {
			confirmed = true
		}
	}
	if !made || !confirmed {
		t.Fatalf("audit: made %v, confirmed %v: %v", made, confirmed, events)
	}

	// Signing out, and ending every session, leaves the keys working.
	e.call(link, "DELETE", "/v1/session", nil)
	if c, _, _ := e.call(e.owner, "POST", "/v1/sessions/end-others?person="+ada, nil); c != 200 {
		t.Fatalf("end Ada's sessions: %d", c)
	}
	if c, _, _ := e.call(pk, "GET", "/v1/whoami", nil); c != 401 {
		t.Fatalf("ended session works: %d", c)
	}
	for _, s := range []string{ci, mcp} {
		if c, _, _ := e.call(s, "GET", "/v1/whoami", nil); c != 200 {
			t.Fatalf("key stopped with its session: %d", c)
		}
	}
}
