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

const pkOrigin = "https://dashboard.tiffin.localhost:8443"

func newPasskeyEnv(t *testing.T) (*env, *state.DB) {
	t.Helper()
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
	a := api.New(api.Deps{DB: db, Engine: change.NewEngine(db), Tokens: tm, Passkeys: am, PublicURL: pkOrigin})
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, owner: owner, tm: tm}, db
}

// post sends an unauthenticated JSON request from client IP ip (through the
// loopback "edge", which sets X-Forwarded-For).
func (e *env) post(path, ip string, body any) (*http.Response, map[string]any) {
	e.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", e.srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", ip)
	req.Header.Set(api.EdgeKeyHeader, testEdgeKey)
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

// passkeySignIn runs options → authenticator → finish.
func (e *env) passkeySignIn(a *passkeytest.Authenticator, c *passkeytest.Credential, ip string) (*http.Response, map[string]any, json.RawMessage) {
	e.t.Helper()
	res, opts := e.post("/v1/session/passkey/options", ip, nil)
	if res.StatusCode != 200 {
		e.t.Fatalf("options: %d %v", res.StatusCode, opts)
	}
	pk := opts["publicKey"].(map[string]any)
	if _, ok := pk["allowCredentials"]; ok || pk["userVerification"] != "required" {
		e.t.Fatalf("options are not for a discoverable, verified passkey: %v", pk)
	}
	raw, _ := json.Marshal(opts)
	cred, err := a.Get(raw, c)
	if err != nil {
		e.t.Fatal(err)
	}
	res, out := e.post("/v1/session/passkey", ip, map[string]any{"credential": cred})
	return res, out, cred
}

func (e *env) addPasskey(token string, a *passkeytest.Authenticator) *passkeytest.Credential {
	e.t.Helper()
	code, opts, _ := e.call(token, "POST", "/v1/passkeys/register", nil)
	if code != 200 {
		e.t.Fatalf("register begin: %d %v", code, opts)
	}
	if sel := opts["publicKey"].(map[string]any)["authenticatorSelection"].(map[string]any); sel["residentKey"] != "required" {
		e.t.Fatalf("registration does not require a discoverable passkey: %v", sel)
	}
	raw, _ := json.Marshal(opts)
	resp, c, err := a.Create(raw)
	if err != nil {
		e.t.Fatal(err)
	}
	if code, out, _ := e.call(token, "POST", "/v1/passkeys", map[string]any{"name": "Phone", "credential": json.RawMessage(resp)}); code != 200 {
		e.t.Fatalf("register finish: %d %v", code, out)
	}
	return c
}

// sudo marks a dashboard session as freshly signed in (as a passkey, Google,
// GitHub or emailed-link sign-in would), for tests that aren't about that.
func (e *env) sudo(session string) string {
	e.t.Helper()
	p, err := e.tm.Authenticate(e.t.Context(), session)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.tm.ConfirmSession(e.t.Context(), p, p.Person); err != nil {
		e.t.Fatal(err)
	}
	return session
}

func sessionCookie(t *testing.T, res *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range res.Cookies() {
		if c.Name == api.SessionCookie {
			return c
		}
	}
	t.Fatalf("no %s cookie", api.SessionCookie)
	return nil
}

func TestPasskeySignIn(t *testing.T) {
	e, _ := newPasskeyEnv(t)
	a := passkeytest.New(pkOrigin)

	// Sam is invited as a member, signs in with the link and (having just
	// proved it's him) adds a passkey.
	_, inv, _ := e.call(e.owner, "POST", "/v1/people", map[string]any{"name": "Sam", "role": "member"})
	person := inv["person"].(map[string]any)["id"].(string)
	link := strings.SplitN(inv["url"].(string), "#", 2)[1]
	res, _ := e.post("/v1/session", "198.51.100.1", map[string]any{"code": link})
	linkSession := sessionCookie(t, res).Value
	cred := e.addPasskey(e.sudo(linkSession), a)
	if code, l, _ := e.call(linkSession, "GET", "/v1/passkeys", nil); code != 200 {
		t.Fatalf("member lists passkeys: %d %v", code, l)
	}
	e.call(linkSession, "DELETE", "/v1/session", nil)

	// Later Sam signs in with the passkey alone.
	res, who, assertion := e.passkeySignIn(a, cred, "198.51.100.1")
	if res.StatusCode != 200 || who["person"] != person || who["name"] != "Sam" || who["role"] != "member" || who["expiresAt"] == nil {
		t.Fatalf("passkey sign-in: %d %v", res.StatusCode, who)
	}
	c := sessionCookie(t, res)
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Expires.IsZero() {
		t.Fatalf("cookie attributes: %+v", c)
	}
	// The cookie is a session as that person, with the member role.
	req, _ := http.NewRequest("GET", e.srv.URL+"/v1/whoami", nil)
	req.AddCookie(&http.Cookie{Name: api.SessionCookie, Value: c.Value})
	wres, err := http.DefaultClient.Do(req)
	if err != nil || wres.StatusCode != 200 {
		t.Fatalf("whoami with cookie: %v %d", err, wres.StatusCode)
	}
	var me tokens.Principal
	_ = json.NewDecoder(wres.Body).Decode(&me)
	wres.Body.Close()
	if me.Person != person || me.PersonName != "Sam" || me.Name != "Sam" || me.Role != "member" || me.Kind != tokens.KindHuman || me.BoxAdmin() {
		t.Fatalf("whoami: %+v", me)
	}
	if code, _, _ := e.call(c.Value, "POST", "/v1/people", map[string]any{"name": "X", "role": "admin"}); code != 403 {
		t.Fatalf("member session invited someone: %d", code)
	}

	// Replaying the same assertion fails: its challenge was spent.
	res, prob := e.post("/v1/session/passkey", "198.51.100.1", map[string]any{"credential": assertion})
	if res.StatusCode != 401 || prob["code"] != "unauthenticated" || len(res.Cookies()) != 0 {
		t.Fatalf("replay: %d %v", res.StatusCode, prob)
	}

	// The sign-in is in the audit log.
	_, _, events := e.call(e.owner, "GET", "/v1/audit", nil)
	found := false
	for _, ev := range events {
		m := ev.(map[string]any)
		if m["action"] == "session.passkey" && m["target"] == person {
			d := m["detail"].(map[string]any)
			found = d["summary"] == "Sam signed in with a passkey" && d["passkey"] == "Phone" && d["ip"] == "198.51.100.1"
		}
	}
	if !found {
		t.Fatalf("no session.passkey audit event: %v", events)
	}

	// Removed people cannot sign in, even with a valid passkey.
	if code, _, _ := e.call(e.owner, "DELETE", "/v1/people/"+person, nil); code != 204 && code != 200 {
		t.Fatalf("remove: %d", code)
	}
	res, prob, _ = e.passkeySignIn(a, cred, "198.51.100.2")
	if res.StatusCode != 401 || !strings.Contains(prob["detail"].(string), "no longer has access") || len(res.Cookies()) != 0 {
		t.Fatalf("removed person: %d %v", res.StatusCode, prob)
	}
}

func TestPasskeySignInUnknownCredential(t *testing.T) {
	e, _ := newPasskeyEnv(t)
	a := passkeytest.New(pkOrigin)
	cred := e.addPasskey(e.owner, a) // the owner's CLI token adds the owner's passkey
	res, who, _ := e.passkeySignIn(a, cred, "198.51.100.3")
	if res.StatusCode != 200 || who["person"] != tokens.OwnerPerson || who["role"] != "owner" {
		t.Fatalf("owner sign-in: %d %v", res.StatusCode, who)
	}
	// Remove the passkey: it no longer signs in.
	_, _, list := e.call(e.owner, "GET", "/v1/passkeys", nil)
	id := list[0].(map[string]any)["id"].(string)
	if code, _, _ := e.call(e.owner, "DELETE", "/v1/passkeys/"+id, nil); code != 204 && code != 200 {
		t.Fatalf("delete passkey: %d", code)
	}
	res, prob, _ := e.passkeySignIn(a, cred, "198.51.100.3")
	if res.StatusCode != 401 || prob["code"] != "unauthenticated" || !strings.Contains(prob["detail"].(string), "not registered") || prob["hint"] == nil {
		t.Fatalf("unknown credential: %d %v", res.StatusCode, prob)
	}
	// Garbage is refused too.
	res, prob = e.post("/v1/session/passkey", "198.51.100.3", map[string]any{"credential": map[string]any{"id": "x"}})
	if res.StatusCode != 401 {
		t.Fatalf("garbage: %d %v", res.StatusCode, prob)
	}
}

func TestPasskeySignInRateLimit(t *testing.T) {
	e, _ := newPasskeyEnv(t)
	for i := range api.PasskeySignInRate {
		if res, out := e.post("/v1/session/passkey/options", "203.0.113.9", nil); res.StatusCode != 200 {
			t.Fatalf("call %d: %d %v", i, res.StatusCode, out)
		}
	}
	res, prob := e.post("/v1/session/passkey/options", "203.0.113.9", nil)
	if res.StatusCode != 429 || prob["code"] != "rate_limited" || res.Header.Get("Retry-After") == "" || prob["hint"] == nil {
		t.Fatalf("over the limit: %d %v %v", res.StatusCode, prob, res.Header)
	}
	// Another address is not affected, and the finish step has its own budget.
	if res, _ := e.post("/v1/session/passkey/options", "203.0.113.10", nil); res.StatusCode != 200 {
		t.Fatalf("other IP: %d", res.StatusCode)
	}
	for range api.PasskeySignInRate {
		e.post("/v1/session/passkey", "203.0.113.9", map[string]any{"credential": map[string]any{"id": "x"}})
	}
	if res, prob := e.post("/v1/session/passkey", "203.0.113.9", map[string]any{"credential": map[string]any{"id": "x"}}); res.StatusCode != 429 || prob["code"] != "rate_limited" {
		t.Fatalf("finish over the limit: %d %v", res.StatusCode, prob)
	}
}

func TestPasskeySignInNeedsABox(t *testing.T) {
	e := newEnv(t) // no passkeys manager: no public URL
	res, prob := e.post("/v1/session/passkey/options", "198.51.100.4", nil)
	if res.StatusCode != 501 {
		t.Fatalf("off-box options: %d %v", res.StatusCode, prob)
	}
}
