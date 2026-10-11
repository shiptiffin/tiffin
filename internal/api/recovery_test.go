package api_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/passkeys/passkeytest"
	"github.com/shiptiffin/tiffin/internal/state"
	"golang.org/x/crypto/ssh"
)

// ownerSession signs the owner in with a `tiffin login` link (a strong
// sign-in) and returns the session, backdated by ago.
func (e *env) ownerSession(db *state.DB, ago time.Duration) string {
	e.t.Helper()
	_, l, _ := e.call(e.owner, "POST", "/v1/login-links", nil)
	res, out := e.post("/v1/session", "198.51.100.1", map[string]any{"code": codeOf(l["url"].(string))})
	if res.StatusCode != 200 {
		e.t.Fatalf("sign in: %d %v", res.StatusCode, out)
	}
	sess := sessionCookie(e.t, res).Value
	backdate(e.t, e, db, sess, ago)
	return sess
}

// backdate moves session sess's sign-in ago into the past.
func backdate(t *testing.T, e *env, db *state.DB, sess string, ago time.Duration) {
	t.Helper()
	p, err := e.tm.Authenticate(t.Context(), sess)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-ago).UTC().Format(time.RFC3339Nano)
	if _, err := db.SQL().ExecContext(t.Context(), `UPDATE tokens SET created_at = ? WHERE id = ?`, at, p.TokenID); err != nil {
		t.Fatal(err)
	}
}

// A person with no passkey may add their first within an hour of a strong
// sign-in (the owner who opened the hand-off link and came back to it later);
// a second passkey, or the first after the hour, still needs the 10 minutes.
func TestFirstPasskeyWithinAnHour(t *testing.T) {
	e, db := newPasskeyEnv(t)
	a := passkeytest.New(pkOrigin)

	// Signed in 61 minutes ago: too late, at either step.
	late := e.ownerSession(db, 61*time.Minute)
	if _, out, _ := e.call(late, "GET", "/v1/me/recovery", nil); out["firstPasskeyOpen"] != false || out["passkeys"] != float64(0) {
		t.Fatalf("recovery after an hour: %v", out)
	}
	if code, out, _ := e.addPasskeyFrom(late, "198.51.100.1", "Late", a); code != 403 || out["code"] != "reauth_required" {
		t.Fatalf("first passkey after an hour: %d %v", code, out)
	}

	// Started in time, finished after the hour: refused at the finish.
	slow := e.ownerSession(db, 59*time.Minute)
	code, opts := e.callFrom(slow, "198.51.100.1", "POST", "/v1/passkeys/register", nil)
	if code != 200 {
		t.Fatalf("begin within the hour: %d %v", code, opts)
	}
	raw, _ := json.Marshal(opts)
	resp, _, err := a.Create(raw)
	if err != nil {
		t.Fatal(err)
	}
	backdate(t, e, db, slow, 61*time.Minute)
	if code, out := e.callFrom(slow, "198.51.100.1", "POST", "/v1/passkeys", map[string]any{"name": "Slow", "credential": json.RawMessage(resp)}); code != 403 || out["code"] != "reauth_required" {
		t.Fatalf("finish after the hour: %d %v", code, out)
	}

	// Signed in 30 minutes ago with no passkey: the first one goes through.
	sess := e.ownerSession(db, 30*time.Minute)
	if code, out, _ := e.call(sess, "GET", "/v1/me/recovery", nil); code != 200 || out["firstPasskeyOpen"] != true || out["passkeys"] != float64(0) {
		t.Fatalf("recovery before the first passkey: %d %v", code, out)
	}
	if code, out, _ := e.addPasskeyFrom(sess, "198.51.100.1", "MacBook", a); code != 0 {
		t.Fatalf("first passkey within the hour: %d %v", code, out)
	}
	if _, out, _ := e.call(sess, "GET", "/v1/me/recovery", nil); out["firstPasskeyOpen"] != false || out["passkeys"] != float64(1) {
		t.Fatalf("recovery after the first passkey: %v", out)
	}

	// The second needs a sign-in in the last 10 minutes, at either step.
	if code, out, _ := e.addPasskeyFrom(sess, "198.51.100.1", "Phone", a); code != 403 || out["code"] != "reauth_required" {
		t.Fatalf("second passkey 30 minutes after signing in: %d %v", code, out)
	}
	fresh := e.ownerSession(db, 0)
	code, opts = e.callFrom(fresh, "198.51.100.1", "POST", "/v1/passkeys/register", nil)
	if code != 200 {
		t.Fatalf("begin, fresh: %d %v", code, opts)
	}
	raw, _ = json.Marshal(opts)
	resp, _, err = a.Create(raw)
	if err != nil {
		t.Fatal(err)
	}
	backdate(t, e, db, fresh, 11*time.Minute)
	if code, out := e.callFrom(fresh, "198.51.100.1", "POST", "/v1/passkeys", map[string]any{"name": "Phone", "credential": json.RawMessage(resp)}); code != 403 || out["code"] != "reauth_required" {
		t.Fatalf("second passkey finished after 10 minutes: %d %v", code, out)
	}
	if code, out, _ := e.addPasskeyFrom(e.ownerSession(db, 5*time.Minute), "198.51.100.1", "Phone", a); code != 0 {
		t.Fatalf("second passkey within 10 minutes: %d %v", code, out)
	}

	// API keys are not people: no passkeys, no recovery.
	key := e.key("all", "full")
	if code, _, _ := e.call(key, "GET", "/v1/me/recovery", nil); code != 403 {
		t.Fatalf("recovery with an API key: %d", code)
	}
	if code, _ := e.callFrom(key, "198.51.100.1", "POST", "/v1/passkeys/register", nil); code != 403 {
		t.Fatalf("passkey with an API key: %d", code)
	}
}

// The recovery summary counts each way back in.
func TestRecoveryCounts(t *testing.T) {
	e, _ := newPasskeyEnv(t)
	keys := filepath.Join(t.TempDir(), "root")
	old := api.ServerKeysFile
	api.ServerKeysFile = keys
	t.Cleanup(func() { api.ServerKeysFile = old })

	if _, out, _ := e.call(e.owner, "GET", "/v1/me/recovery", nil); out["sshKeys"] != float64(0) || out["email"] != false || out["providers"] != float64(0) || out["firstPasskeyOpen"] != false {
		t.Fatalf("nothing set up: %v", out)
	}

	var lines []string
	for range 2 {
		pub, _, _ := ed25519.GenerateKey(rand.Reader)
		k, _ := ssh.NewPublicKey(pub)
		lines = append(lines, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))+" tiffin pasted 2026-10-11")
	}
	lines = append(lines, "ssh-ed25519 AAAAhand-edited root@laptop") // not Tiffin's: not counted
	if err := os.WriteFile(keys, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	api.SetBoxMailer(&fakeMailer{relayed: true})
	t.Cleanup(func() { api.SetBoxMailer(nil) })
	api.SetDashboardOAuth(fakeKeys{map[string]bool{"github": true}})
	t.Cleanup(func() { api.SetDashboardOAuth(nil) })

	code, out, _ := e.call(e.owner, "GET", "/v1/me/recovery", nil)
	if code != 200 || out["sshKeys"] != float64(2) || out["email"] != true || out["providers"] != float64(1) || out["passkeys"] != float64(0) {
		t.Fatalf("recovery: %d %v", code, out)
	}
	if code, _, _ := e.call("", "GET", "/v1/me/recovery", nil); code != 401 {
		t.Fatalf("recovery signed out: %d", code)
	}
}

// The login page learns whether the box is managed and its domain, before
// anyone signs in, and nothing about anyone's ways in.
func TestSessionBox(t *testing.T) {
	e, _ := newPasskeyEnv(t)
	managedBox(t, "")
	code, out, _ := e.call("", "GET", "/v1/session/box", nil)
	if code != 200 || out["managed"] != false || out["host"] != "tiffin.localhost" || len(out) != 2 {
		t.Fatalf("self-hosted: %d %v", code, out)
	}
	managedBox(t, "https://shiptiffin.com")
	if code, out, _ := e.call("", "GET", "/v1/session/box", nil); code != 200 || out["managed"] != true || out["host"] != "tiffin.localhost" {
		t.Fatalf("managed: %d %v", code, out)
	}
}
