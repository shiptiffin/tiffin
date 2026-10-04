//go:build e2e

package e2e

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAuth is the auth acceptance test, on a fresh box through the CLI
// and the box's HTTPS edge:
//
//	apply postgres+email+auth → engine ready → sign up over HTTPS (ALTCHA
//	solved) → verification email in the dev inbox → verify → sign in →
//	create org → invite by email → accept → role change → the demoted admin
//	can't invite → an API key capped below its sponsor can't escalate or mint
//	keys → RLS isolates two orgs' rows → dashboard API: list, ban (sign-in
//	refused), unban → remove auth (irreversible) drops the schema.
//
// TIFFIN_E2E_REUSE_CLI=/path/to/tiffin reuses the box the current
// TIFFIN_CONFIG_DIR / TIFFIN_LIMA_* environment points at, instead of
// creating one (for iterating on a dev box).
func TestAuth(t *testing.T) {
	RequireLima(t)
	start := time.Now()
	phase := func(name string, since time.Time) {
		t.Logf("PHASE %-18s %s", name, time.Since(since).Round(100*time.Millisecond))
	}
	dir := t.TempDir()
	var cli string
	env := os.Environ()
	configDir := os.Getenv("TIFFIN_CONFIG_DIR")
	port := os.Getenv("TIFFIN_LIMA_PORT")
	if reuse := os.Getenv("TIFFIN_E2E_REUSE_CLI"); reuse != "" {
		// Never fall through to your default box (~/.tiffin): reuse needs an
		// explicit dev-box config dir and Lima instance.
		home, _ := os.UserHomeDir()
		if configDir == "" || port == "" || os.Getenv("TIFFIN_LIMA_INSTANCE") == "" || os.Getenv("TIFFIN_LIMA_INSTANCE") == "tiffin" ||
			filepath.Clean(configDir) == filepath.Join(home, ".tiffin") {
			t.Fatal("TIFFIN_E2E_REUSE_CLI needs TIFFIN_CONFIG_DIR, TIFFIN_LIMA_INSTANCE and TIFFIN_LIMA_PORT for a dev box (never your default box)")
		}
		cli = reuse
	} else {
		cli = buildTiffin(t, dir, "", "")
		bin := buildTiffin(t, dir, "linux", "0.0.1-auth")
		instance, disk, p := newName(), newDiskName(), freePort(t)
		configDir, port = filepath.Join(dir, "config"), fmt.Sprint(p)
		env = append(env, "TIFFIN_CONFIG_DIR="+configDir, "TIFFIN_LIMA_INSTANCE="+instance, "TIFFIN_LIMA_DISK="+disk,
			"TIFFIN_LIMA_PORT="+port, "TIFFIN_LIMA_MEMORY=3GiB", "TIFFIN_HOME=", "TIFFIN_URL=", "TIFFIN_TOKEN=")
		t.Cleanup(func() {
			if os.Getenv("TIFFIN_E2E_KEEP") != "1" {
				_ = exec.Command("limactl", "delete", "-f", instance).Run()
				_ = exec.Command("limactl", "disk", "delete", "-f", disk).Run()
			}
		})
		up := exec.Command(cli, "up", "--binary", bin)
		up.Env, up.Dir = env, dir
		if out, err := up.CombinedOutput(); err != nil {
			t.Fatalf("tiffin up: %v\n%s", err, out)
		}
		phase("up", start)
	}

	run := func(args ...string) (int, string) {
		t.Helper()
		cmd := exec.Command(cli, args...)
		cmd.Env, cmd.Dir = env, dir
		out, err := cmd.Output()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("tiffin %v: %v", args, err)
		}
		return code, string(out)
	}
	ok := func(v any, args ...string) {
		t.Helper()
		code, out := run(args...)
		if code != 0 {
			t.Fatalf("tiffin %s: exit %d\n%s", strings.Join(args, " "), code, out)
		}
		if v != nil {
			if err := json.Unmarshal([]byte(out), v); err != nil {
				t.Fatalf("tiffin %s: %v\n%s", strings.Join(args, " "), err, out)
			}
		}
	}
	project := fmt.Sprintf("auth%d", time.Now().Unix()%100000)
	writeConfig := func(services string) string {
		path := filepath.Join(dir, "tiffin.config.ts")
		cfg := `import { defineConfig } from "tiffin-sdk";
export default defineConfig({ project: "` + project + `", apps: { web: { framework: "static", routes: ["` + project + `"] } }, services: {` + services + `} });
`
		if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	apply := func(path string) {
		t.Helper()
		var plan struct {
			Hash string `json:"hash"`
		}
		ok(&plan, "plan", path)
		var res struct {
			Applied bool `json:"applied"`
		}
		ok(&res, "apply", path, "--confirm", plan.Hash[:12], "-m", "e2e auth")
		if !res.Applied {
			t.Fatalf("not applied")
		}
	}
	waitReady := func(addr string) {
		t.Helper()
		deadline := time.Now().Add(4 * time.Minute)
		for {
			_, out := run("projects", "get", project)
			var st struct {
				Status map[string]struct{ State, Message string } `json:"status"`
			}
			_ = json.Unmarshal([]byte(out), &st)
			s := st.Status[addr]
			if s.State == "ready" {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s not ready: %+v\n%s", addr, s, out)
			}
			time.Sleep(time.Second)
		}
	}

	// The protect module limits every POST under /api/auth to 10 per minute
	// per IP (sign-in brute-force protection). This test makes ~30 from one
	// IP within seconds, so it lifts that limit for its run.
	ok(nil, "protect", "set", "--body", `{"limits":{"auth":{"requests":500,"windowSeconds":60}}}`)
	t.Cleanup(func() { run("protect", "set", "--body", `{"limits":{"auth":{"requests":10,"windowSeconds":60}}}`) })

	// ---- apply ----
	t0 := time.Now()
	apply(writeConfig(`postgres: {}, email: {}, auth: { methods: ["email", "magic-link"] }`))
	waitReady("service/auth")
	phase("auth ready", t0)

	// ---- an HTTPS client that trusts the box's CA ----
	host := project + ".tiffin.localhost:" + port
	base := "https://" + host + "/api/auth"
	ca, err := os.ReadFile(filepath.Join(configDir, "boxes", "local", "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)
	newBrowser := func() *http.Client {
		jar, _ := cookiejar.New(nil)
		return &http.Client{Jar: jar, Timeout: 30 * time.Second,
			Transport:     &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	type resp struct {
		code int
		body map[string]any
		raw  string
	}
	call := func(c *http.Client, method, url string, body any, hdr map[string]string) resp {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		if !strings.HasPrefix(url, "https://") {
			url = base + url
		}
		req, _ := http.NewRequest(method, url, rd)
		req.Header.Set("Origin", "https://"+host)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := c.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, url, err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		r := resp{code: res.StatusCode, raw: string(raw)}
		_ = json.Unmarshal(raw, &r.body)
		return r
	}
	captcha := func(c *http.Client) map[string]string {
		t.Helper()
		req, _ := http.NewRequest("GET", base+"/altcha/challenge", nil)
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		sol, err := solveAltcha(raw)
		if err != nil {
			t.Fatal(err)
		}
		return map[string]string{"x-captcha-response": sol}
	}
	// The newest dev-inbox message to an address, and its links.
	inbox := func(to, subject string) []string {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			var msgs []struct {
				ID      string   `json:"id"`
				To      []string `json:"to"`
				Subject string   `json:"subject"`
			}
			ok(&msgs, "email", "messages", "list", project, "--q", to)
			for _, m := range msgs {
				if strings.Contains(strings.Join(m.To, ","), to) && strings.Contains(m.Subject, subject) {
					var d struct {
						Links []string `json:"links"`
					}
					ok(&d, "email", "messages", "get", project, m.ID)
					return d.Links
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Fatalf("no %q email to %s in the dev inbox", subject, to)
		return nil
	}
	signUp := func(email, name string) *http.Client {
		t.Helper()
		c := newBrowser()
		r := call(c, "POST", "/sign-up/email", map[string]any{"email": email, "password": "correct horse battery", "name": name}, captcha(c))
		if r.code != 200 {
			t.Fatalf("sign-up %s: %d %s", email, r.code, r.raw)
		}
		links := inbox(email, "Confirm your email")
		if len(links) == 0 || !strings.HasPrefix(links[0], base+"/verify-email?token=") {
			t.Fatalf("verification link: %v", links)
		}
		if v := call(c, "GET", links[0], nil, nil); v.code != 302 && v.code != 200 {
			t.Fatalf("verify: %d %s", v.code, v.raw)
		}
		return c
	}

	// The edge picks up the /api/auth route at the end of the converge.
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(500 * time.Millisecond) {
		if r := call(newBrowser(), "GET", "/tiffin/config", nil, nil); r.code == 200 && r.body["appName"] != nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("auth endpoint not served on %s: %d %s", host, r.code, r.raw)
		}
	}

	// ---- sign up → verify (dev inbox) → sign in ----
	t0 = time.Now()
	if r := call(newBrowser(), "POST", "/sign-up/email", map[string]any{"email": "x@example.com", "password": "correct horse battery", "name": "X"}, nil); r.code != 400 || r.body["code"] != "CAPTCHA_REQUIRED" {
		t.Fatalf("sign-up without captcha: %d %s", r.code, r.raw)
	}
	alice := signUp("alice@example.com", "Alice")
	if s := call(alice, "GET", "/get-session", nil, nil); s.body == nil || s.body["user"].(map[string]any)["emailVerified"] != true {
		t.Fatalf("session after verify: %s", s.raw)
	}
	alice = newBrowser()
	if r := call(alice, "POST", "/sign-in/email", map[string]any{"email": "alice@example.com", "password": "correct horse battery"}, captcha(alice)); r.code != 200 {
		t.Fatalf("sign-in: %d %s", r.code, r.raw)
	}
	phase("signup→signin", t0)

	// ---- org → invite → accept → role change ----
	t0 = time.Now()
	org := call(alice, "POST", "/organization/create", map[string]any{"name": "Acme", "slug": "acme-" + project}, nil)
	if org.code != 200 {
		t.Fatalf("create org: %d %s", org.code, org.raw)
	}
	acme := org.body["id"].(string)
	bob := signUp("bob@example.com", "Bob")
	inv := call(alice, "POST", "/organization/invite-member", map[string]any{"email": "bob@example.com", "role": "admin", "organizationId": acme}, nil)
	if inv.code != 200 {
		t.Fatalf("invite: %d %s", inv.code, inv.raw)
	}
	links := inbox("bob@example.com", "invited you to Acme")
	if len(links) == 0 || !strings.Contains(links[0], "/accept-invite?invitation="+inv.body["id"].(string)) {
		t.Fatalf("invite link: %v", links)
	}
	acc := call(bob, "POST", "/organization/accept-invitation", map[string]any{"invitationId": inv.body["id"]}, nil)
	if acc.code != 200 {
		t.Fatalf("accept: %d %s", acc.code, acc.raw)
	}
	bobMember := acc.body["member"].(map[string]any)["id"].(string)
	if r := call(bob, "POST", "/organization/invite-member", map[string]any{"email": "carol@example.com", "role": "owner", "organizationId": acme}, nil); r.code != 403 {
		t.Fatalf("admin invited an owner: %d %s", r.code, r.raw)
	}
	if r := call(alice, "POST", "/organization/update-member-role", map[string]any{"memberId": bobMember, "role": "member", "organizationId": acme}, nil); r.code != 200 {
		t.Fatalf("role change: %d %s", r.code, r.raw)
	}
	if r := call(bob, "POST", "/organization/invite-member", map[string]any{"email": "carol@example.com", "role": "viewer", "organizationId": acme}, nil); r.code != 403 {
		t.Fatalf("member invited someone: %d %s", r.code, r.raw)
	}
	phase("org/invite/role", t0)

	// ---- an agent (API key) can't exceed its sponsor ----
	t0 = time.Now()
	key := call(alice, "POST", "/api-key/create", map[string]any{"name": "agent", "metadata": map[string]any{"maxRole": "member"}}, nil)
	if key.code != 200 {
		t.Fatalf("api key: %d %s", key.code, key.raw)
	}
	agent := map[string]string{"x-api-key": key.body["key"].(string)}
	s := call(newBrowser(), "GET", "/tiffin/session?organizationId="+acme, nil, agent)
	if o, _ := s.body["organization"].(map[string]any); o == nil || o["role"] != "member" || o["memberRole"] != "owner" {
		t.Fatalf("agent session: %s", s.raw)
	}
	if r := call(newBrowser(), "POST", "/organization/invite-member", map[string]any{"email": "eve@example.com", "role": "viewer", "organizationId": acme}, agent); r.code != 403 {
		t.Fatalf("capped key invited: %d %s", r.code, r.raw)
	}
	if r := call(newBrowser(), "POST", "/api-key/create", map[string]any{"name": "child"}, agent); r.code != 403 {
		t.Fatalf("key minted a key: %d %s", r.code, r.raw)
	}
	phase("agent caps", t0)

	// ---- RLS isolates two orgs' rows ----
	t0 = time.Now()
	sqlRun := func(q string) string {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"sql": q})
		var res struct {
			Results []struct {
				Rows [][]any `json:"rows"`
			} `json:"results"`
		}
		ok(&res, "sql", "write", project, "--body", string(raw))
		if len(res.Results) == 0 {
			return ""
		}
		b, _ := json.Marshal(res.Results[len(res.Results)-1].Rows)
		return string(b)
	}
	sqlRun(`CREATE TABLE notes (id serial primary key, org_id text not null, body text not null); SELECT auth.enable_org_rls('notes')`)
	sqlRun(`SELECT set_config('app.org_id', 'org_a', true); INSERT INTO notes (org_id, body) VALUES ('org_a', 'a1')`)
	sqlRun(`SELECT set_config('app.org_id', 'org_b', true); INSERT INTO notes (org_id, body) VALUES ('org_b', 'b1')`)
	if got := sqlRun(`SELECT set_config('app.org_id', 'org_a', true); SELECT body FROM notes`); got != `[["a1"]]` {
		t.Fatalf("org_a sees %s", got)
	}
	if got := sqlRun(`SELECT body FROM notes`); got != `null` && got != `[]` {
		t.Fatalf("no org set, saw %s", got)
	}
	raw, _ := json.Marshal(map[string]any{"sql": `SELECT set_config('app.org_id', 'org_a', true); INSERT INTO notes (org_id, body) VALUES ('org_b', 'sneak')`})
	if code, out := run("sql", "write", project, "--body", string(raw)); code == 0 || !strings.Contains(out, "row-level security") {
		t.Fatalf("cross-org insert: exit %d %s", code, out)
	}
	phase("rls", t0)

	// ---- dashboard API: users, ban, unban ----
	t0 = time.Now()
	var users struct {
		Users []struct{ ID, Email string } `json:"users"`
		Total int                          `json:"total"`
	}
	ok(&users, "auth", "users", "list", project)
	if users.Total != 2 {
		t.Fatalf("users: %+v", users)
	}
	ok(&users, "auth", "users", "list", project, "--search", "bob")
	bobID := users.Users[0].ID
	ok(nil, "auth", "users", "ban", project, bobID, "--body", `{"reason":"e2e"}`)
	if r := call(newBrowser(), "POST", "/sign-in/email", map[string]any{"email": "bob@example.com", "password": "correct horse battery"}, captcha(newBrowser())); r.code != 403 || r.body["code"] != "BANNED" {
		t.Fatalf("banned sign-in: %d %s", r.code, r.raw)
	}
	ok(nil, "auth", "users", "unban", project, bobID)
	var orgs struct{ Total int }
	ok(&orgs, "auth", "orgs", "list", project)
	if orgs.Total != 3 { // two personal orgs + Acme
		t.Fatalf("orgs: %d", orgs.Total)
	}
	phase("admin api", t0)

	// ---- removing auth is irreversible and drops the schema ----
	t0 = time.Now()
	path := writeConfig(`postgres: {}, email: {}`)
	var plan struct {
		Hash string `json:"hash"`
		Risk string `json:"risk"`
	}
	ok(&plan, "plan", path)
	if plan.Risk != "irreversible" {
		t.Fatalf("removing auth should be irreversible, plan risk %q", plan.Risk)
	}
	ok(nil, "apply", path, "--confirm", plan.Hash[:12], "-m", "e2e remove auth")
	deadline := time.Now().Add(time.Minute)
	for {
		if got := sqlRun(`SELECT to_regclass('auth."user"')::text`); got == `[[null]]` {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("auth schema still there after removing auth")
		}
		time.Sleep(time.Second)
	}
	if code, _ := run("auth", "show", project); code == 0 {
		t.Fatal("auth show still works after removing auth")
	}
	phase("delete", t0)
	phase("total", start)
}

// solveAltcha solves the engine's proof-of-work challenge (see
// packages/auth-engine/src/altcha.ts): the counter whose
// sha256(salt || nonce || uint32be(counter)) starts with keyPrefix.
func solveAltcha(challenge []byte) (string, error) {
	var c struct {
		Parameters struct{ Nonce, Salt, KeyPrefix string } `json:"parameters"`
	}
	if err := json.Unmarshal(challenge, &c); err != nil {
		return "", fmt.Errorf("challenge %s: %w", challenge, err)
	}
	nonce, _ := hex.DecodeString(c.Parameters.Nonce)
	salt, _ := hex.DecodeString(c.Parameters.Salt)
	buf := append(append(append([]byte{}, salt...), nonce...), 0, 0, 0, 0)
	for n := uint32(0); n < 10_000_000; n++ {
		binary.BigEndian.PutUint32(buf[len(buf)-4:], n)
		sum := sha256.Sum256(buf)
		if h := hex.EncodeToString(sum[:]); strings.HasPrefix(h, c.Parameters.KeyPrefix) {
			p, _ := json.Marshal(map[string]any{"challenge": json.RawMessage(challenge), "solution": map[string]any{"counter": n, "derivedKey": h}})
			return base64.StdEncoding.EncodeToString(p), nil
		}
	}
	return "", fmt.Errorf("no solution")
}
