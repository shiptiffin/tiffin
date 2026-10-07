package api_test

import (
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

// On a box with no relay and no Google or GitHub keys, the owner's own
// `tiffin login` (a link the owner token made for the owner) is a strong
// sign-in: they add their first passkey and change their email in the
// dashboard. A link the owner's session makes for itself is not. API keys
// can't change anyone's email; admins can't change the owner's.
func TestOwnerTerminalSignInAndEmailGuards(t *testing.T) {
	api.SetBoxMailer(&fakeMailer{}) // no relay: emailed links are never made
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
	signIn := func(by string) string {
		t.Helper()
		code, out, _ := e.call(by, "POST", "/v1/login-links", nil)
		if code != 200 {
			t.Fatalf("login link: %d %v", code, out)
		}
		res, _ := e.post("/v1/session", "198.51.100.1", map[string]any{"code": out["code"]})
		return sessionCookie(t, res).Value
	}

	// `tiffin login` with the owner token: Sign-ins says so, and sudo passes.
	mine := signIn(e.owner)
	_, _, list := e.call(mine, "GET", "/v1/sessions", nil)
	if len(list) != 1 || list[0].(map[string]any)["method"] != tokens.MethodTerminal {
		t.Fatalf("sessions: %v", list)
	}
	if code, out, _ := e.addPasskeyFrom(mine, "198.51.100.1", "MacBook", a); code != 0 {
		t.Fatalf("first passkey after the owner's tiffin login: %d %v", code, out)
	}
	if code, out := e.callFrom(mine, "198.51.100.1", "PUT", "/v1/people/"+tokens.OwnerPerson+"/email", map[string]any{"email": "owner@example.com"}); code != 200 {
		t.Fatalf("owner's email after tiffin login: %d %v", code, out)
	}

	// A link that session makes for itself is not one: a stolen session
	// can't renew its own sudo.
	again := signIn(mine)
	if code, out, _ := e.addPasskeyFrom(again, "198.51.100.1", "Stolen", a); code != 403 || out["code"] != "reauth_required" {
		t.Fatalf("passkey from a session-made link: %d %v", code, out)
	}

	// An admin, even confirmed, can't change the owner's address.
	_, inv, _ := e.call(e.owner, "POST", "/v1/people", map[string]any{"name": "Ada", "email": "ada@example.com", "role": "admin", "notify": false})
	ada := inv["person"].(map[string]any)["id"].(string)
	res, _ := e.post("/v1/session", "198.51.100.4", map[string]any{"code": codeOf(inv["url"].(string))})
	adaSess := e.sudo(sessionCookie(t, res).Value)
	if code, out := e.callFrom(adaSess, "198.51.100.4", "PUT", "/v1/people/"+tokens.OwnerPerson+"/email", map[string]any{"email": "mallory@example.net"}); code != 403 ||
		out["code"] != "forbidden" || !strings.Contains(out["detail"].(string), "only the owner") {
		t.Fatalf("admin changes the owner's email: %d %v", code, out)
	}

	// An admin API key can't change anyone's: not the owner's, not Ada's.
	key := e.key("all", "full")
	for _, who := range []string{tokens.OwnerPerson, ada} {
		code, out := e.callFrom(key, "198.51.100.9", "PUT", "/v1/people/"+who+"/email", map[string]any{"email": "mallory@example.net"})
		if code != 403 || out["code"] != "forbidden" || !strings.Contains(out["detail"].(string), "API key can't change an email address") ||
			!strings.Contains(out["hint"].(string), "owner token") {
			t.Fatalf("admin key changes %s's email: %d %v", who, code, out)
		}
	}
	if p, _ := tm.GetPerson(t.Context(), tokens.OwnerPerson); p.Email != "owner@example.com" {
		t.Fatalf("owner's email: %q", p.Email)
	}

	// The owner token (the CLI's `tiffin people email`) still can.
	if code, out, _ := e.call(e.owner, "PUT", "/v1/people/"+ada+"/email", map[string]any{"email": "ada@example.org"}); code != 200 || out["email"] != "ada@example.org" {
		t.Fatalf("owner token sets Ada's email: %d %v", code, out)
	}
}
