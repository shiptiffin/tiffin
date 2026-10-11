package api_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/sshkeys"
	"github.com/shiptiffin/tiffin/internal/state"
	"github.com/shiptiffin/tiffin/internal/tokens"
	"golang.org/x/crypto/ssh"
)

func sshKey(t *testing.T) (line, fingerprint string) {
	t.Helper()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp))) + " me@laptop", ssh.FingerprintSHA256(sp)
}

func serverAccessBox(t *testing.T, keys *sshkeys.Store) (*env, *api.API, *fakeMailer) {
	t.Helper()
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
	box := api.New(api.Deps{DB: db, Engine: change.NewEngine(db), Tokens: tm, PublicURL: pkOrigin, ServerKeys: keys})
	srv := httptest.NewServer(box.Handler())
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, owner: owner, tm: tm, db: db}, box, f
}

func (e *env) audits(action string) int {
	e.t.Helper()
	var n int
	if err := e.db.SQL().QueryRowContext(e.t.Context(), `SELECT count(*) FROM audit WHERE action = ?`, action).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func keysOf(out map[string]any) []any { l, _ := out["keys"].([]any); return l }

// sshMail is the SSH key notices f sent (sign-in notices left out).
func sshMail(f *fakeMailer) []api.BoxMail {
	var out []api.BoxMail
	for _, m := range f.all() {
		if m.Kind == api.BoxMailNewSSHKey || m.Kind == api.BoxMailSSHKeyRemoved {
			out = append(out, m)
		}
	}
	return out
}

// Only the owner sees or changes the server's SSH keys: their dashboard
// session or the owner token. Admins and API keys (even an admin key) are
// refused; changing them in a session needs a recent strong sign-in.
// Every change is audited and emailed to the owner.
func TestServerAccess(t *testing.T) {
	store := &sshkeys.Store{Path: filepath.Join(t.TempDir(), "authorized_keys.d", "root")}
	e, box, mail := serverAccessBox(t, store)
	if code, out, _ := e.call(e.owner, "PUT", "/v1/people/"+tokens.OwnerPerson+"/email", map[string]any{"email": "owner@example.com"}); code != 200 {
		t.Fatalf("owner's email: %d %v", code, out)
	}
	signIn := func(by string) string {
		t.Helper()
		code, out, _ := e.call(by, "POST", "/v1/login-links", nil)
		if code != 200 {
			t.Fatalf("login link: %d %v", code, out)
		}
		res, _ := e.post("/v1/session", "198.51.100.1", map[string]any{"code": out["code"]})
		return sessionCookie(t, res).Value
	}
	strong := signIn(e.owner) // the owner's own `tiffin login`: a strong sign-in
	weak := signIn(strong)    // a link that session made for itself: not one
	k1, fp1 := sshKey(t)

	// API keys are refused, even one with full access to everything.
	key := e.key("all", "full")
	if code, out := e.callFrom(key, "198.51.100.9", "GET", "/v1/server-access", nil); code != 403 || out["code"] != "forbidden" || !strings.Contains(out["detail"].(string), "API key") {
		t.Fatalf("API key lists: %d %v", code, out)
	}
	if code, out := e.callFrom(key, "198.51.100.9", "POST", "/v1/server-access/keys", map[string]any{"keys": k1}); code != 403 || !strings.Contains(out["hint"].(string), "Server access") {
		t.Fatalf("API key adds: %d %v", code, out)
	}

	// An admin, even confirmed, is refused.
	_, inv, _ := e.call(e.owner, "POST", "/v1/people", map[string]any{"name": "Ada", "role": "admin", "notify": false})
	res, _ := e.post("/v1/session", "198.51.100.4", map[string]any{"code": codeOf(inv["url"].(string))})
	ada := e.sudo(sessionCookie(t, res).Value)
	for _, c := range []struct{ method, path string }{{"GET", "/v1/server-access"}, {"POST", "/v1/server-access/keys"}} {
		if code, out := e.callFrom(ada, "198.51.100.4", c.method, c.path, map[string]any{"keys": k1}); code != 403 || !strings.Contains(out["detail"].(string), "only the box's owner") {
			t.Fatalf("admin %s %s: %d %v", c.method, c.path, code, out)
		}
	}

	// The owner's session reads without a fresh sign-in, but changes need one.
	code, out := e.callFrom(weak, "198.51.100.1", "GET", "/v1/server-access", nil)
	if code != 200 || len(keysOf(out)) != 0 || out["host"] != "tiffin.localhost" || out["file"] != store.Path {
		t.Fatalf("owner lists: %d %v", code, out)
	}
	if code, out := e.callFrom(weak, "198.51.100.1", "POST", "/v1/server-access/keys", map[string]any{"keys": k1}); code != 403 || out["code"] != "reauth_required" ||
		!strings.Contains(out["detail"].(string), "SSH keys") || !strings.Contains(out["hint"].(string), "passkey") {
		t.Fatalf("owner adds without a fresh sign-in: %d %v", code, out)
	}

	// Signed in just now: added, written as Tiffin's own line, audited, mailed.
	code, out = e.callFrom(strong, "198.51.100.1", "POST", "/v1/server-access/keys", map[string]any{"keys": k1, "source": "github:sam-o"})
	if code != 200 || len(keysOf(out)) != 1 {
		t.Fatalf("owner adds: %d %v", code, out)
	}
	got := keysOf(out)[0].(map[string]any)
	if got["fingerprint"] != fp1 || got["type"] != "ssh-ed25519" || got["source"] != "github:sam-o" || !strings.HasSuffix(got["added"].(string), "T00:00:00Z") {
		t.Fatalf("key: %v", got)
	}
	raw, _ := os.ReadFile(store.Path)
	if !strings.Contains(string(raw), strings.Fields(k1)[1]+" tiffin github:sam-o ") || strings.Contains(string(raw), "laptop") {
		t.Fatalf("key file:\n%s", raw)
	}
	if n := e.audits("server_key.add"); n != 1 {
		t.Fatalf("%d server_key.add events", n)
	}
	box.WaitBackground()
	if m := sshMail(mail); len(m) != 1 || m[0].Kind != api.BoxMailNewSSHKey || m[0].To != "owner@example.com" || m[0].Server != "tiffin.localhost" ||
		len(m[0].SSHKeys) != 1 || m[0].SSHKeys[0].Fingerprint != fp1 || m[0].IP != "198.51.100.1" {
		t.Fatalf("mail: %+v", m)
	}

	// Adding it again changes nothing: no event, no mail.
	if code, out := e.callFrom(strong, "198.51.100.1", "POST", "/v1/server-access/keys", map[string]any{"keys": k1}); code != 200 || len(keysOf(out)) != 1 ||
		keysOf(out)[0].(map[string]any)["source"] != "github:sam-o" {
		t.Fatalf("add again: %d %v", code, out)
	}
	box.WaitBackground()
	if e.audits("server_key.add") != 1 || len(sshMail(mail)) != 1 {
		t.Fatalf("adding again was recorded: %d events, %d mails", e.audits("server_key.add"), len(sshMail(mail)))
	}

	// What isn't an accepted key is refused with the reason.
	k2, fp2 := sshKey(t)
	for body, want := range map[string]map[string]any{
		"not an SSH public key":            {"keys": k2 + "\nhello"},
		"options before the key":           {"keys": `command="sh" ` + k2},
		"that's a private key":             {"keys": "-----BEGIN OPENSSH PRIVATE KEY-----"},
		"github:<user>":                    {"keys": k2, "source": "github:-sam"},
		"to match pattern":                 {"keys": k2, "source": "hetzner"},
		"expected string to match pattern": {"keys": k2, "source": "github:bad user"},
	} {
		if code, out := e.callFrom(strong, "198.51.100.1", "POST", "/v1/server-access/keys", want); code != 422 || !strings.Contains(strings.ToLower(jsonText(out)), strings.ToLower(body)) {
			t.Errorf("%q: %d %v", body, code, out)
		}
	}
	many := ""
	for range sshkeys.MaxKeys {
		l, _ := sshKey(t)
		many += l + "\n"
	}
	if code, out := e.callFrom(strong, "198.51.100.1", "POST", "/v1/server-access/keys", map[string]any{"keys": many}); code != 422 || !strings.Contains(out["detail"].(string), "at most 10") {
		t.Fatalf("an 11th key: %d %v", code, out)
	}

	// The owner token (the CLI) needs no fresh sign-in.
	if code, out, _ := e.call(e.owner, "POST", "/v1/server-access/keys", map[string]any{"keys": k2}); code != 200 || len(keysOf(out)) != 2 {
		t.Fatalf("owner token adds: %d %v", code, out)
	}
	box.WaitBackground()
	if m := sshMail(mail); len(m) != 2 || m[1].Device != "The tiffin CLI (owner token)" {
		t.Fatalf("mail for the owner token's key: %+v", m)
	}

	// Removing: by fingerprint, same rules; an unknown one is 404.
	if code, out := e.callFrom(weak, "198.51.100.1", "POST", "/v1/server-access/keys/remove", map[string]any{"fingerprint": fp1}); code != 403 || out["code"] != "reauth_required" {
		t.Fatalf("remove without a fresh sign-in: %d %v", code, out)
	}
	if code, out := e.callFrom(key, "198.51.100.9", "POST", "/v1/server-access/keys/remove", map[string]any{"fingerprint": fp1}); code != 403 {
		t.Fatalf("API key removes: %d %v", code, out)
	}
	_, unknown := sshKey(t)
	if code, out := e.callFrom(strong, "198.51.100.1", "POST", "/v1/server-access/keys/remove", map[string]any{"fingerprint": unknown}); code != 404 || out["code"] != "not_found" {
		t.Fatalf("remove an unknown key: %d %v", code, out)
	}
	if code, out := e.callFrom(strong, "198.51.100.1", "POST", "/v1/server-access/keys/remove", map[string]any{"fingerprint": "MD5:aa:bb"}); code != 422 {
		t.Fatalf("remove by a malformed fingerprint: %d %v", code, out)
	}
	code, out = e.callFrom(strong, "198.51.100.1", "POST", "/v1/server-access/keys/remove", map[string]any{"fingerprint": fp1})
	if code != 200 || len(keysOf(out)) != 1 || keysOf(out)[0].(map[string]any)["fingerprint"] != fp2 {
		t.Fatalf("remove: %d %v", code, out)
	}
	if n := e.audits("server_key.delete"); n != 1 {
		t.Fatalf("%d server_key.delete events", n)
	}
	box.WaitBackground()
	if m := sshMail(mail); len(m) != 3 || m[2].Kind != api.BoxMailSSHKeyRemoved || m[2].SSHKeys[0].Fingerprint != fp1 {
		t.Fatalf("removal mail: %+v", m)
	}
	if keys, _ := store.Read(); len(keys) != 1 || keys[0].Fingerprint != fp2 {
		t.Fatalf("key file after removal: %v", keys)
	}
}

// A box that isn't on a server (a local VM) has no server access to manage.
func TestServerAccessOnlyOnAServer(t *testing.T) {
	e, _, _ := serverAccessBox(t, nil)
	if code, out, _ := e.call(e.owner, "GET", "/v1/server-access", nil); code != 501 || !strings.Contains(out["detail"].(string), "on a server") {
		t.Fatalf("no server: %d %v", code, out)
	}
	// The caller is checked first: an API key learns nothing.
	if code, _, _ := e.call(e.key("all", "full"), "GET", "/v1/server-access", nil); code != 403 {
		t.Fatalf("API key: %d", code)
	}
}

// jsonText is a problem's detail and field messages, for matching.
func jsonText(out map[string]any) string {
	s, _ := out["detail"].(string)
	if errs, ok := out["errors"].([]any); ok {
		for _, e := range errs {
			if m, ok := e.(map[string]any); ok {
				s += " " + m["message"].(string)
			}
		}
	}
	return s
}
