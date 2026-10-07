package auth

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/tokens"
)

const social = `{"project":"shop","apps":{"web":{"routes":["shop","shop.example.com"]}},
 "services":{"postgres":{},"email":{},"auth":{"methods":["email","google","github","apple","oidc","discord"]}}}`

func appleKeyPEM(t *testing.T) (string, *ecdsa.PrivateKey) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), k
}

// Box-wide keys go to every project that turns the method on, through the
// one callback URL; a project's own complete keys win; partial ones don't.
func TestKeyPrecedence(t *testing.T) {
	p := newPlatform(t)
	ctx := t.Context()
	apply(t, p, social)
	keyPEM, _ := appleKeyPEM(t)
	google, _ := ProviderByID("google")
	github, _ := ProviderByID("github")
	apple, _ := ProviderByID("apple")
	oidc, _ := ProviderByID("oidc")
	for _, c := range []struct {
		prov Provider
		in   ProviderInput
	}{
		{google, ProviderInput{BoxProvider: BoxProvider{ClientID: "box-google"}, ClientSecret: "box-google-secret"}},
		{github, ProviderInput{BoxProvider: BoxProvider{ClientID: "box-gh"}, ClientSecret: "box-gh-secret"}},
		{apple, ProviderInput{BoxProvider: BoxProvider{ClientID: "com.example.signin", TeamID: "TEAM123456", KeyID: "KEY1234567"}, PrivateKey: keyPEM}},
		{oidc, ProviderInput{BoxProvider: BoxProvider{ClientID: "okta-id", Issuer: "https://example.okta.com/", Label: "Okta"}, ClientSecret: "okta-secret"}},
	} {
		if err := setProvider(ctx, p, c.prov, c.in, "owner"); err != nil {
			t.Fatalf("%s: %v", c.prov.ID, err)
		}
	}
	// The project's own GitHub keys win; a lone GOOGLE_CLIENT_ID doesn't.
	if _, err := p.SetSecrets(ctx, "shop", map[string]string{"GITHUB_CLIENT_ID": "own-gh", "GITHUB_CLIENT_SECRET": "own-gh-secret", "GOOGLE_CLIENT_ID": "half"}, "own keys"); err != nil {
		t.Fatal(err)
	}
	src, err := KeySources(ctx, p, "shop", []string{"email", "google", "github", "apple", "oidc", "discord"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"google": KeysBox, "github": KeysProject, "apple": KeysBox, "oidc": KeysBox, "discord": KeysNone}
	for k, v := range want {
		if src[k] != v {
			t.Errorf("%s keys from %q, want %q", k, src[k], v)
		}
	}
	if _, ok := src["email"]; ok {
		t.Error("email isn't a sign-in provider")
	}

	c, errs, err := buildEngineConfig(ctx, p)
	if err != nil || errs["shop"] != nil {
		t.Fatal(err, errs)
	}
	s := c.Projects["shop"]
	if g := s.Social["google"]; g == nil || g.ClientID != "box-google" || g.ClientSecret != "box-google-secret" || !g.Proxied {
		t.Fatalf("google: %+v", g)
	}
	if g := s.Social["github"]; g == nil || g.ClientID != "own-gh" || g.Proxied {
		t.Fatalf("github: %+v", g)
	}
	if o := s.Social["oidc"]; o == nil || o.Issuer != "https://example.okta.com" || o.Label != "Okta" || !o.Proxied {
		t.Fatalf("oidc: %+v", o)
	}
	if s.Social["discord"] != nil {
		t.Fatal("discord has no keys")
	}
	if s.OAuthProxy == nil || s.OAuthProxy.URL != "https://dashboard.tiffin.localhost:8443" || len(s.OAuthProxy.Secret) < 32 {
		t.Fatalf("oauthProxy: %+v", s.OAuthProxy)
	}
	px := c.Proxy
	if px == nil || px.Host != "dashboard.tiffin.localhost" || px.URL != s.OAuthProxy.URL || px.Secret != s.OAuthProxy.Secret {
		t.Fatalf("proxy: %+v", px)
	}
	if len(px.Social) != 4 || px.Social["github"].ClientID != "box-gh" {
		t.Fatalf("proxy serves every box-wide provider: %+v", px.Social)
	}
	if CallbackURL(p, "google") != "https://dashboard.tiffin.localhost:8443/api/auth/callback/google" {
		t.Fatal(CallbackURL(p, "google"))
	}

	// Apple: the box signs the client secret, and keeps it: rebuilding the
	// config (every minute) doesn't change it, so the engine isn't reloaded.
	jwt := s.Social["apple"].ClientSecret
	if strings.Count(jwt, ".") != 2 {
		t.Fatalf("apple secret isn't a JWT: %q", jwt)
	}
	c2, _, _ := buildEngineConfig(ctx, p)
	if c2.Projects["shop"].Social["apple"].ClientSecret != jwt || c2.Proxy.Secret != px.Secret {
		t.Fatal("the Apple secret or the proxy secret changed between builds")
	}
	a, _ := json.Marshal(c)
	b, _ := json.Marshal(c2)
	if !bytes.Equal(a, b) {
		t.Fatal("engine config changed between builds with nothing changed")
	}

	// Removing the box-wide Google keys: shop's Google button needs keys again.
	if had, err := removeProvider(ctx, p, google); err != nil || !had {
		t.Fatal(had, err)
	}
	c3, _, _ := buildEngineConfig(ctx, p)
	if c3.Projects["shop"].Social["google"] != nil || c3.Proxy.Social["google"] != nil {
		t.Fatal("removed keys still in use")
	}
}

func TestAppleClientSecret(t *testing.T) {
	keyPEM, key := appleKeyPEM(t)
	now := time.Unix(1_800_000_000, 0)
	jwt, err := AppleClientSecret("TEAM123456", "KEY1234567", "com.example.signin", keyPEM, now, appleSecretLife)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := VerifyAppleClientSecret(jwt, key)
	if err != nil {
		t.Fatal(err)
	}
	if claims["iss"] != "TEAM123456" || claims["sub"] != "com.example.signin" || claims["aud"] != "https://appleid.apple.com" {
		t.Fatalf("claims: %v", claims)
	}
	if exp := int64(claims["exp"].(float64)) - now.Unix(); exp <= 0 || exp > 15777000 {
		t.Fatalf("exp %d s: Apple allows at most 15777000", exp)
	}
	head, _, _ := strings.Cut(jwt, ".")
	if !strings.Contains(string(mustB64(t, head)), `"kid":"KEY1234567"`) {
		t.Fatal("no kid")
	}
	// The .p8 text without its BEGIN/END lines works too; anything else doesn't.
	bare := strings.Join(strings.Split(strings.TrimSpace(keyPEM), "\n")[1:len(strings.Split(strings.TrimSpace(keyPEM), "\n"))-1], "\n")
	if _, err := ParseAppleKey(bare); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "hello", "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----"} {
		if _, err := ParseAppleKey(bad); err == nil {
			t.Errorf("took %q", bad)
		}
	}
}

func TestAppleSecretRenews(t *testing.T) {
	p := newPlatform(t)
	ctx := t.Context()
	keyPEM, _ := appleKeyPEM(t)
	defer func() { timeNow = time.Now }()
	t0 := time.Unix(1_800_000_000, 0)
	timeNow = func() time.Time { return t0 }
	a, exp, err := appleSecret(ctx, p, "T", "K", "S", keyPEM)
	if err != nil || !exp.Equal(t0.Add(appleSecretLife)) {
		t.Fatal(exp, err)
	}
	timeNow = func() time.Time { return t0.Add(100 * 24 * time.Hour) }
	if b, _, _ := appleSecret(ctx, p, "T", "K", "S", keyPEM); b != a {
		t.Fatal("renewed with 80 days left")
	}
	timeNow = func() time.Time { return t0.Add(155 * 24 * time.Hour) }
	c, exp2, _ := appleSecret(ctx, p, "T", "K", "S", keyPEM)
	if c == a || !exp2.After(exp) {
		t.Fatal("kept a secret with 25 days left")
	}
	// The cache holds it sealed, never in the clear.
	raw, _, _ := p.DB.KVGet(ctx, nsAppleJWT, appleFingerprint("T", "K", "S", keyPEM))
	if bytes.Contains(raw, []byte(c)) {
		t.Fatal("client secret stored in the clear")
	}
}

func TestValidateInput(t *testing.T) {
	for _, c := range []struct {
		id     string
		in     ProviderInput
		stored bool
		bad    string
	}{
		{"google", ProviderInput{BoxProvider: BoxProvider{ClientID: ""}, ClientSecret: "s"}, false, "client ID"},
		{"google", ProviderInput{BoxProvider: BoxProvider{ClientID: "id"}}, false, "client secret"},
		{"google", ProviderInput{BoxProvider: BoxProvider{ClientID: "id"}}, true, ""}, // keeps the stored secret
		{"google", ProviderInput{BoxProvider: BoxProvider{ClientID: "a b"}, ClientSecret: "s"}, false, "no spaces"},
		{"oidc", ProviderInput{BoxProvider: BoxProvider{ClientID: "id"}, ClientSecret: "s"}, false, "issuer"},
		{"oidc", ProviderInput{BoxProvider: BoxProvider{ClientID: "id", Issuer: "http://idp.example"}, ClientSecret: "s"}, false, "https"},
		{"apple", ProviderInput{BoxProvider: BoxProvider{ClientID: "com.x", TeamID: "T"}, PrivateKey: "x"}, false, "Key ID"},
		{"apple", ProviderInput{BoxProvider: BoxProvider{ClientID: "com.x", TeamID: "T", KeyID: "K"}, PrivateKey: "not a key"}, false, ".p8"},
		{"microsoft", ProviderInput{BoxProvider: BoxProvider{ClientID: "id", TenantID: "https://x"}, ClientSecret: "s"}, false, "tenant"},
	} {
		prov, _ := ProviderByID(c.id)
		err := validateInput(prov, &c.in, c.stored)
		switch {
		case c.bad == "" && err != nil:
			t.Errorf("%s %+v: %v", c.id, c.in, err)
		case c.bad != "" && (err == nil || !strings.Contains(err.Error(), c.bad)):
			t.Errorf("%s %+v: got %v, want %q", c.id, c.in, err, c.bad)
		}
	}
}

// The API: box admins set and remove keys; nobody ever reads a secret back;
// every change is audited; the project's Auth overview says where keys come from.
func TestProvidersAPI(t *testing.T) {
	p := newPlatform(t)
	ctx := t.Context()
	apply(t, p, social)
	tm := tokens.NewManager(p.DB)
	owner, _, err := tm.Bootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.New(api.Deps{DB: p.DB, Engine: p.Engine, Tokens: tm, Version: "test", Platform: p}).Handler())
	t.Cleanup(srv.Close)
	call := func(tok, method, path string, body any) (int, string) {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rd)
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	_, made := call(owner, "POST", "/v1/tokens", map[string]any{"name": "shop agent", "projects": []string{"shop"}, "access": "full"})
	var key struct{ Secret string }
	_ = json.Unmarshal([]byte(made), &key)

	const secret = "super-secret-google-value"
	if code, out := call(key.Secret, "PUT", "/v1/auth/providers/google", map[string]any{"clientId": "gid", "clientSecret": secret}); code != 403 {
		t.Fatalf("a project key set box-wide keys: %d %s", code, out)
	}
	code, out := call(owner, "PUT", "/v1/auth/providers/google", map[string]any{"clientId": "gid.apps.googleusercontent.com", "clientSecret": secret})
	if code != 200 || !strings.Contains(out, `"set":true`) || !strings.Contains(out, `"secretSet":true`) || strings.Contains(out, secret) {
		t.Fatalf("set: %d %s", code, out)
	}
	if !strings.Contains(out, `"callbackUrl":"https://dashboard.tiffin.localhost:8443/api/auth/callback/google"`) || !strings.Contains(out, `"usedBy":["shop"]`) {
		t.Fatalf("set: %s", out)
	}
	// Replacing the client ID keeps the stored secret.
	if code, out := call(owner, "PUT", "/v1/auth/providers/google", map[string]any{"clientId": "gid2"}); code != 200 || !strings.Contains(out, `"clientId":"gid2"`) {
		t.Fatalf("replace: %d %s", code, out)
	}
	if code, out := call(owner, "PUT", "/v1/auth/providers/oidc", map[string]any{"clientId": "x", "clientSecret": "y"}); code != 422 || !strings.Contains(out, "issuer") {
		t.Fatalf("oidc without issuer: %d %s", code, out)
	}
	code, out = call(owner, "GET", "/v1/auth/providers", nil)
	if code != 200 || strings.Contains(out, secret) || !strings.Contains(out, `"callbackBase":"https://dashboard.tiffin.localhost:8443"`) {
		t.Fatalf("list: %d %s", code, out)
	}
	_, res, err := p.DB.Load(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	o := overview(p, "shop", res)
	ob, _ := json.Marshal(o)
	if strings.Contains(string(ob), secret) {
		t.Fatal("overview leaks the secret")
	}
	keys := map[string]ProviderState{}
	for _, s := range o.Providers {
		keys[s.ID] = s
	}
	if g := keys["google"]; !g.On || g.Keys != KeysBox || !g.BoxKeys || g.CallbackURL != "https://dashboard.tiffin.localhost:8443/api/auth/callback/google" {
		t.Fatalf("google: %+v", g)
	}
	if d := keys["discord"]; !d.On || d.Keys != KeysNone || d.CallbackURL != "https://shop.example.com:8443/api/auth/callback/discord" {
		t.Fatalf("discord: %+v", d)
	}
	if !o.Social["google"] || o.Social["discord"] {
		t.Fatalf("social: %v", o.Social)
	}
	if code, _ := call(key.Secret, "DELETE", "/v1/auth/providers/google", nil); code != 403 {
		t.Fatalf("a project key removed box-wide keys: %d", code)
	}
	// This app's own keys: one change, the one redirect URI on its custom domain.
	if code, out := call(owner, "PUT", "/v1/projects/shop/auth/providers/google/keys", map[string]any{"clientId": "own-gid"}); code != 422 || !strings.Contains(out, "client secret") {
		t.Fatalf("own keys without a secret: %d %s", code, out)
	}
	const own = "own-google-secret-value"
	code, out = call(owner, "PUT", "/v1/projects/shop/auth/providers/google/keys", map[string]any{"clientId": "own-gid", "clientSecret": own})
	if code != 200 || strings.Contains(out, own) || !strings.Contains(out, `"keys":"project"`) ||
		!strings.Contains(out, `"callbackUrl":"https://shop.example.com:8443/api/auth/callback/google"`) || !strings.Contains(out, `"callbackChanged":false`) {
		t.Fatalf("own keys: %d %s", code, out)
	}
	ec, _, _ := buildEngineConfig(ctx, p)
	if g := ec.Projects["shop"].Social["google"]; g == nil || g.ClientID != "own-gid" || g.Proxied || ec.Projects["shop"].OAuthProxy.AppURL != "https://shop.example.com:8443" {
		t.Fatalf("engine config for own keys: %+v %+v", g, ec.Projects["shop"].OAuthProxy)
	}
	// Only the client ID changes: the stored secret stays.
	if code, out := call(owner, "PUT", "/v1/projects/shop/auth/providers/google/keys", map[string]any{"clientId": "own-gid-2"}); code != 200 {
		t.Fatalf("own keys, new client ID: %d %s", code, out)
	}
	if sec, _ := p.Secrets.All(ctx, "shop"); sec["GOOGLE_CLIENT_SECRET"] != own || sec["GOOGLE_CLIENT_ID"] != "own-gid-2" {
		t.Fatal("own keys not stored as the project's secrets")
	}
	// The sign-in host changes (the custom domain goes): the page says so until confirmed.
	apply(t, p, strings.Replace(social, `"shop","shop.example.com"`, `"shop"`, 1))
	_, res2, _ := p.DB.Load(ctx, "shop")
	var g2 ProviderState
	for _, s := range overview(p, "shop", res2).Providers {
		if s.ID == "google" {
			g2 = s
		}
	}
	if !g2.CallbackChanged || g2.CallbackConfirmed != "https://shop.example.com:8443/api/auth/callback/google" || g2.AppCallbackURL != "https://shop.tiffin.localhost:8443/api/auth/callback/google" {
		t.Fatalf("changed redirect URI: %+v", g2)
	}
	if code, out := call(owner, "POST", "/v1/projects/shop/auth/providers/google/callback-confirm", nil); code != 200 || !strings.Contains(out, `"callbackChanged":false`) {
		t.Fatalf("confirm: %d %s", code, out)
	}
	if code, out := call(key.Secret, "DELETE", "/v1/projects/shop/auth/providers/google/keys", nil); code != 200 || !strings.Contains(out, `"keys":"box"`) {
		t.Fatalf("back to the box's keys: %d %s", code, out)
	}
	if code, _ := call(owner, "DELETE", "/v1/projects/shop/auth/providers/google/keys", nil); code != 404 {
		t.Fatalf("remove own keys twice: %d", code)
	}

	if code, out := call(owner, "DELETE", "/v1/auth/providers/google", nil); code != 200 || !strings.Contains(out, `"set":false`) {
		t.Fatalf("remove: %d %s", code, out)
	}
	if code, _ := call(owner, "DELETE", "/v1/auth/providers/google", nil); code != 404 {
		t.Fatalf("remove twice: %d", code)
	}
	if code, _ := call(owner, "PUT", "/v1/auth/providers/myspace", map[string]any{"clientId": "x"}); code != 422 {
		t.Fatalf("unknown provider: %d", code)
	}

	// Audited, without the secret.
	_, audit := call(owner, "GET", "/v1/audit?limit=50", nil)
	if !strings.Contains(audit, "auth.provider.set") || !strings.Contains(audit, "auth.provider.remove") || strings.Contains(audit, secret) || strings.Contains(audit, own) {
		t.Fatalf("audit: %s", audit)
	}
	// The config file the engine reads has it while set; nothing else on disk does.
	raw, _ := os.ReadFile(ConfigPath(p))
	if strings.Contains(string(raw), secret) {
		t.Fatal("removed secret still in the engine config")
	}
}

func TestDashboardHandler(t *testing.T) {
	var got *http.Request
	eng := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		http.SetCookie(w, &http.Cookie{Name: "x", Value: "y"})
		http.Redirect(w, r, "https://shop.example.com/api/auth/callback/google/oauth-proxy?profile=p", http.StatusFound)
	}))
	defer eng.Close()
	old := engineUpstream
	engineUpstream = eng.URL
	defer func() { engineUpstream = old }()
	h := DashboardHandler()

	req := httptest.NewRequest("GET", "https://dashboard.tiffin.localhost/api/auth/callback/google?code=c&state=s", nil)
	req.Header.Set("Cookie", "tiffin_session=dashboard")
	req.Header.Set("Authorization", "Bearer tfn_x")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 302 || rec.Header().Get("Set-Cookie") != "" || rec.Header().Get("Location") == "" {
		t.Fatalf("callback: %d %v", rec.Code, rec.Header())
	}
	if got.Host != "dashboard.tiffin.localhost" || got.Header.Get("Cookie") != "" || got.Header.Get("Authorization") != "" || got.URL.RawQuery != "code=c&state=s" {
		t.Fatalf("forwarded: host %q cookie %q auth %q query %q", got.Host, got.Header.Get("Cookie"), got.Header.Get("Authorization"), got.URL.RawQuery)
	}
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/auth/get-session"},
		{"POST", "/api/auth/sign-in/social"},
		{"GET", "/api/auth/callback/myspace"},
		{"GET", "/api/auth/callback/google/oauth-proxy"},
		{"DELETE", "/api/auth/callback/google"},
		{"POST", "/api/auth/error"},
	} {
		got = nil
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, "https://dashboard.tiffin.localhost"+c.path, nil))
		if rec.Code != 404 || got != nil {
			t.Errorf("%s %s: %d, forwarded %v", c.method, c.path, rec.Code, got != nil)
		}
	}
	// Apple posts its callback.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "https://dashboard.tiffin.localhost/api/auth/callback/apple", strings.NewReader("code=c&state=s")))
	if rec.Code != 302 {
		t.Fatalf("apple post: %d", rec.Code)
	}
}

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Removing auth with its database (a destroyed project) doesn't try to drop
// the schema: it goes with the database, and the engine can't log in to it.
func TestRemoveAuthWithItsDatabase(t *testing.T) {
	p := newPlatform(t)
	ctx := t.Context()
	apply(t, p, `{"project":"gone","apps":{"web":{}},"services":{"postgres":{},"auth":{}}}`)
	apply(t, p, `{"project":"gone","apps":{"web":{}}}`)
	if err := (&Module{}).Reconcile(ctx, p, "gone", change.KindService+"/auth", nil); err != nil {
		t.Fatalf("auth removed with postgres: %v", err)
	}
	// Postgres stays: the schema must be dropped, so a down engine is an error.
	apply(t, p, `{"project":"kept","apps":{"web":{}},"services":{"postgres":{},"auth":{}}}`)
	apply(t, p, `{"project":"kept","apps":{"web":{}},"services":{"postgres":{}}}`)
	if err := (&Module{}).Reconcile(ctx, p, "kept", change.KindService+"/auth", nil); err == nil {
		t.Fatal("auth removed, postgres kept: the schema drop was skipped")
	}
	for _, msg := range []string{
		`auth engine: password authentication failed for user "p_gone"`,
		"SASL authentication failed",
		"database removed (500 internal)",
		`schema "tiffin_auth" does not exist`,
	} {
		if !gone(errors.New(msg)) {
			t.Errorf("gone(%q) = false", msg)
		}
	}
	if gone(errors.New("connection refused")) {
		t.Error("a down engine isn't gone")
	}
}

// Without a mail service, production refuses email sign-in (the engine is
// told), plans warn, and the Auth page knows; a local box and a box with a
// relay don't.
func TestEmailBlockedWithoutRelay(t *testing.T) {
	p := newPlatform(t)
	ctx := t.Context()
	p.Domain = "box.example.org"
	defer func() { fakeRelay = false }()
	fakeRelay = false
	apply(t, p, social)
	c, _, err := buildEngineConfig(ctx, p)
	if err != nil || !c.Projects["shop"].EmailBlocked || c.Projects["shop"].PreviewHosts == nil {
		t.Fatalf("no relay: %+v %v", c.Projects["shop"], err)
	}
	_, res, _ := p.DB.Load(ctx, "shop")
	if !overview(p, "shop", res).EmailBlocked {
		t.Fatal("overview doesn't say email is blocked")
	}
	w := (&Module{}).PlanWarnings(ctx, p, "shop", nil, res)
	if len(w) != 1 || !strings.Contains(w[0], "Settings › Email") {
		t.Fatalf("plan warnings: %v", w)
	}
	// Only passkeys and providers: nothing to warn about.
	apply(t, p, strings.Replace(social, `"email","google","github","apple","oidc","discord"`, `"passkey","google"`, 1))
	_, res, _ = p.DB.Load(ctx, "shop")
	if w := (&Module{}).PlanWarnings(ctx, p, "shop", nil, res); len(w) != 0 {
		t.Fatalf("no email methods, warned: %v", w)
	}
	fakeRelay = true
	if c, _, _ := buildEngineConfig(ctx, p); c.Projects["shop"].EmailBlocked {
		t.Fatal("blocked with a relay")
	}
	// A relay but no email service in the project: refused, and the plan
	// does not claim the box has no relay (the manifest says add email).
	delete(res, change.KindService+"/email")
	if !EmailBlocked(ctx, p, "shop", false) {
		t.Fatal("no email service, not blocked")
	}
	res[change.KindService+"/auth"] = change.Resource{Spec: []byte(`{"methods":["email"]}`)}
	if w := (&Module{}).PlanWarnings(ctx, p, "shop", nil, res); len(w) != 0 {
		t.Fatalf("relay set, no email service: %v", w)
	}
	fakeRelay = false
	p.Domain = "tiffin.localhost"
	if c, _, _ := buildEngineConfig(ctx, p); c.Projects["shop"].EmailBlocked {
		t.Fatal("blocked on a local box")
	}
}
