package auth

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/edge"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/projicon"
	"github.com/btahir/tiffin/internal/state"
)

// Fake postgres and email modules: the auth module asks them for env by name.
type fakeModule struct {
	name string
	env  func(project string) map[string]string
}

func (f *fakeModule) Name() string { return f.name }
func (f *fakeModule) Env(_ context.Context, _ *platform.Platform, project, _ string) (map[string]string, error) {
	return f.env(project), nil
}

var fakeDB = map[string]string{} // project → DATABASE_URL

var fakeRelay bool // whether the fake email module's mail leaves the box

func (f *fakeModule) WillSend(context.Context, *platform.Platform, string) (bool, error) {
	return fakeRelay, nil
}

func init() {
	platform.Register(&fakeModule{name: "postgres", env: func(project string) map[string]string {
		if u, ok := fakeDB[project]; ok {
			return map[string]string{"DATABASE_URL": u}
		}
		return map[string]string{"DATABASE_URL": "postgresql://" + project + ":pw@127.0.0.1:5432/" + project}
	}})
	platform.Register(&fakeModule{name: "email", env: func(project string) map[string]string {
		return map[string]string{"SMTP_URL": "smtp://127.0.0.1:2525", "EMAIL_FROM": "hello@" + project + ".test"}
	}})
}

func newPlatform(t *testing.T) *platform.Platform {
	t.Helper()
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	sec, err := platform.OpenSecrets(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &platform.Platform{DB: db, Engine: change.NewEngine(db), Secrets: sec, DataRoot: t.TempDir(), Domain: "tiffin.localhost",
		PublicURL: "https://dashboard.tiffin.localhost:8443", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// apply commits a manifest the way the API does (no reconcile).
func apply(t *testing.T, p *platform.Platform, raw string) *change.Plan {
	t.Helper()
	mf, err := manifest.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	desired, err := change.Resources(mf)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := p.Engine.Plan(t.Context(), mf.Project, desired)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Engine.Apply(t.Context(), change.ApplyRequest{Plan: plan, Confirm: plan.Hash, Authorize: func(*change.Plan) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestEngineMemoryFollowsTheBox(t *testing.T) {
	small, big := unitFile("/bun", "/engine.js", "/conf.json", 4096), unitFile("/bun", "/engine.js", "/conf.json", 16384)
	if !strings.Contains(small, "\nMemoryMax=384M\n") || !strings.Contains(big, "\nMemoryMax=1024M\n") {
		t.Fatalf("MemoryMax on 4 GB / 16 GB:\n%s\n%s", small, big)
	}
}

func TestParseRoute(t *testing.T) {
	p := &platform.Platform{Domain: "tiffin.localhost"}
	for _, c := range []struct{ in, host, prefix string }{
		{"shop", "shop.tiffin.localhost", ""},
		{"Shop", "shop.tiffin.localhost", ""},
		{"example.com", "example.com", ""},
		{"example.com/api/", "example.com", "/api"},
		{"admin/x", "admin.tiffin.localhost", "/x"},
	} {
		h, pre := ParseRoute(p, c.in)
		if h != c.host || pre != c.prefix {
			t.Errorf("ParseRoute(%q) = %q %q, want %q %q", c.in, h, pre, c.host, c.prefix)
		}
	}
}

const shop = `{"project":"shop","apps":{
  "web":{"routes":["shop","shop.example.com/app"]},
  "admin":{},
  "jobs":{"role":"worker"}
 },"services":{"postgres":{},"email":{},"auth":{"methods":["email","google","otp"]}}}`

func TestEngineConfig(t *testing.T) {
	p := newPlatform(t)
	ctx := t.Context()
	apply(t, p, shop)
	if _, err := p.SetSecrets(ctx, "shop", map[string]string{"GOOGLE_CLIENT_ID": "gid", "GOOGLE_CLIENT_SECRET": "gsecret"}, "google sign-in"); err != nil {
		t.Fatal(err)
	}
	c, errs, err := buildEngineConfig(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	s := c.Projects["shop"]
	if s == nil {
		t.Fatalf("shop missing: %v", errs)
	}
	wantHosts := []string{"shop.tiffin.localhost", "shop.example.com", "shop-admin.tiffin.localhost"}
	if !slices.Equal(s.Hosts, wantHosts) {
		t.Fatalf("hosts = %v, want %v (web first, workers excluded)", s.Hosts, wantHosts)
	}
	if s.PrimaryURL != "https://shop.tiffin.localhost:8443" || s.Origins[1] != "https://shop.example.com:8443" {
		t.Fatalf("urls: %s %v", s.PrimaryURL, s.Origins)
	}
	if s.DatabaseURL != "postgresql://shop:pw@127.0.0.1:5432/shop" || s.SMTPURL != "smtp://127.0.0.1:2525" || s.EmailFrom != "hello@shop.test" {
		t.Fatalf("wiring: %+v", s)
	}
	if !slices.Equal(s.Methods, []string{"email", "google", "otp"}) || !s.Organizations || !s.Captcha {
		t.Fatalf("settings: %+v", s)
	}
	if s.Social["google"] == nil || s.Social["google"].ClientSecret != "gsecret" || s.Social["github"] != nil {
		t.Fatalf("social: %+v", s.Social)
	}
	if s.AppName != "shop" || len(s.Secret) < 32 { // the project's name as written, not "Shop"
		t.Fatalf("name/secret: %q %d", s.AppName, len(s.Secret))
	}
	// Emails carry the project's own icon (its public PNG) and no accent of
	// their own: the engine draws the dashboard's brass button. The enamel
	// the dashboard picked for the project is not its brand.
	id, err := projicon.PublicID(ctx, p.DB, "shop")
	if err != nil {
		t.Fatal(err)
	}
	wantBrand := EmailBrand{LogoURL: "https://dashboard.tiffin.localhost:8443/v1/icons/" + id + ".png"}
	if s.EmailBrand == nil || *s.EmailBrand != wantBrand {
		t.Fatalf("email brand: %+v, want %+v", s.EmailBrand, wantBrand)
	}
	// The secret is stable across rebuilds.
	c2, _, _ := buildEngineConfig(ctx, p)
	if c2.Projects["shop"].Secret != s.Secret {
		t.Fatal("secret changed between builds")
	}
	// Written 0600, and only rewritten when something changed.
	changed, err := writeEngineConfig(p, c)
	if err != nil || !changed {
		t.Fatalf("write: %v %v", changed, err)
	}
	fi, _ := os.Stat(ConfigPath(p))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	if changed, _ := writeEngineConfig(p, c2); changed {
		t.Fatal("rewrote an unchanged config")
	}
	var back EngineConfig
	raw, _ := os.ReadFile(ConfigPath(p))
	if err := json.Unmarshal(raw, &back); err != nil || back.Version != 1 {
		t.Fatalf("config file: %v", err)
	}
}

func TestEnvAndRoutes(t *testing.T) {
	p := newPlatform(t)
	ctx := t.Context()
	apply(t, p, shop)
	apply(t, p, `{"project":"plain","apps":{"web":{}}}`)
	env, err := (&Module{}).Env(ctx, p, "shop", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if env["TIFFIN_AUTH_URL"] != "https://shop-admin.tiffin.localhost:8443/api/auth" {
		t.Fatalf("admin app gets its own host: %v", env)
	}
	if env["TIFFIN_AUTH_INTERNAL_URL"] != "http://127.0.0.1:7393/api/auth" || env["TIFFIN_AUTH_HOST"] != "shop-admin.tiffin.localhost" {
		t.Fatalf("internal: %v", env)
	}
	jobs, _ := (&Module{}).Env(ctx, p, "shop", "jobs")
	if jobs["TIFFIN_AUTH_URL"] != "https://shop.tiffin.localhost:8443/api/auth" {
		t.Fatalf("workers get the primary host: %v", jobs)
	}
	if env, _ := (&Module{}).Env(ctx, p, "plain", "web"); env != nil {
		t.Fatalf("no auth, no env: %v", env)
	}
	routes, err := (&Module{}).Routes(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 3 {
		t.Fatalf("routes: %+v", routes)
	}
	for _, r := range routes {
		if r.PathPrefix != "/api/auth" || r.Upstream != EngineAddr {
			t.Fatalf("route: %+v", r)
		}
	}
}

func TestEngineBundleEmbedded(t *testing.T) {
	js, err := EngineBundle()
	if err != nil {
		t.Fatal(err)
	}
	if len(js) < 100_000 {
		t.Fatalf("engine bundle looks empty: %d bytes", len(js))
	}
}

func TestDuplicateHostsDontBreakTheEdge(t *testing.T) {
	p := newPlatform(t)
	ctx := t.Context()
	apply(t, p, `{"project":"aaa","apps":{"web":{"routes":["web"]}},"services":{"postgres":{},"auth":{}}}`)
	apply(t, p, `{"project":"bbb","apps":{"web":{"routes":["web"]}},"services":{"postgres":{},"auth":{}}}`)
	routes, err := (&Module{}).Routes(ctx, p)
	if err != nil || len(routes) != 1 || routes[0].Host != "web.tiffin.localhost" {
		t.Fatalf("routes: %+v %v", routes, err)
	}
	c, errs, err := buildEngineConfig(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Projects["aaa"] == nil || c.Projects["bbb"] != nil || errs["bbb"] == nil {
		t.Fatalf("aaa keeps the host, bbb reports it: %v %v", c.Projects, errs)
	}
}

// Email verification: automatic by default (required only once real mail
// goes out through a relay), or what auth.emailVerification says.
func TestEmailVerificationSetting(t *testing.T) {
	p := newPlatform(t)
	ctx := t.Context()
	required := func() bool {
		t.Helper()
		c, errs, err := buildEngineConfig(ctx, p)
		if err != nil || c.Projects["shop"] == nil {
			t.Fatalf("config: %v %v", err, errs)
		}
		return c.Projects["shop"].RequireEmailVerification
	}
	apply(t, p, `{"project":"shop","apps":{"web":{}},"services":{"postgres":{},"email":{},"auth":{}}}`)
	if required() {
		t.Fatal("without a relay, sign-ups must not wait for a confirmation that only reaches the dev inbox")
	}
	fakeRelay = true
	t.Cleanup(func() { fakeRelay = false })
	if !required() {
		t.Fatal("with a relay, new users confirm their address")
	}
	apply(t, p, `{"project":"shop","apps":{"web":{}},"services":{"postgres":{},"email":{},"auth":{"emailVerification":false}}}`)
	if required() {
		t.Fatal("emailVerification:false must turn it off")
	}
	_, res, _ := p.DB.Load(ctx, "shop")
	if o := overview(p, "shop", res); o.EmailVerification.Required || o.EmailVerification.Source != VerifyManifest {
		t.Fatalf("overview: %+v", o.EmailVerification)
	}
	fakeRelay = false
	apply(t, p, `{"project":"shop","apps":{"web":{}},"services":{"postgres":{},"email":{},"auth":{"emailVerification":true}}}`)
	if !required() {
		t.Fatal("emailVerification:true must turn it on")
	}
}

// The email button: the dashboard's brass unless auth.emailAccent says
// otherwise. The project's enamel never leaks into it.
func TestEmailAccentSetting(t *testing.T) {
	p := newPlatform(t)
	ctx := t.Context()
	accent := func() string {
		t.Helper()
		c, errs, err := buildEngineConfig(ctx, p)
		if err != nil || c.Projects["shop"] == nil || c.Projects["shop"].EmailBrand == nil {
			t.Fatalf("config: %v %v", err, errs)
		}
		return c.Projects["shop"].EmailBrand.Accent
	}
	apply(t, p, `{"project":"shop","apps":{"web":{}},"services":{"postgres":{},"email":{},"auth":{}}}`)
	if a := accent(); a != "" {
		t.Fatalf("no emailAccent: the engine's default (brass) must be used, got %q", a)
	}
	apply(t, p, `{"project":"shop","apps":{"web":{}},"services":{"postgres":{},"email":{},"auth":{"emailAccent":"#2f6b4f"}}}`)
	if a := accent(); a != "#2f6b4f" {
		t.Fatalf("emailAccent: got %q", a)
	}
}

// A fake runtime: the preview hosts of each project.
type fakeRuntime struct{ previews map[string]map[string]string }

func (*fakeRuntime) Name() string { return "runtime" }
func (f *fakeRuntime) PreviewHosts(_ context.Context, _ *platform.Platform, project string) (map[string]string, error) {
	return f.previews[project], nil
}

var runtimeFake = &fakeRuntime{previews: map[string]map[string]string{}}

func init() { platform.Register(runtimeFake) }

// Previews: the edge sends /api/auth on their hosts to the engine, and the
// engine serves them as more hosts of the project, on the same users. A
// preview appearing or going away rewrites the engine config on the route
// refresh it causes, before the edge gets the route.
func TestPreviewHosts(t *testing.T) {
	p := newPlatform(t)
	ctx := t.Context()
	apply(t, p, shop)
	t.Cleanup(func() {
		runtimeFake.previews = map[string]map[string]string{}
		lastPreviews.key, lastPreviews.synced = "", false
	})
	const preview = "pr-3--shop.tiffin.localhost"
	runtimeFake.previews["shop"] = map[string]string{preview: "web"}

	routes, err := (&Module{}).Routes(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(routes, func(r edge.Route) bool {
		return r.Host == preview && r.PathPrefix == "/api/auth" && r.Upstream == EngineAddr
	}) {
		t.Fatalf("no /api/auth route on the preview: %+v", routes)
	}
	written := func() *ProjectConfig {
		t.Helper()
		var c EngineConfig
		raw, err := os.ReadFile(ConfigPath(p))
		if err != nil || json.Unmarshal(raw, &c) != nil || c.Projects["shop"] == nil {
			t.Fatalf("engine config: %v %s", err, raw)
		}
		return c.Projects["shop"]
	}
	s := written()
	if s.Hosts[len(s.Hosts)-1] != preview || s.Origins[len(s.Origins)-1] != "https://"+preview+":8443" {
		t.Fatalf("the engine config lists the preview last: %v %v", s.Hosts, s.Origins)
	}
	if s.PrimaryURL != "https://shop.tiffin.localhost:8443" {
		t.Fatalf("the primary host stays production's: %s", s.PrimaryURL)
	}

	runtimeFake.previews["shop"] = nil // the preview was deleted
	routes, _ = (&Module{}).Routes(ctx, p)
	if slices.ContainsFunc(routes, func(r edge.Route) bool { return r.Host == preview }) || slices.Contains(written().Hosts, preview) {
		t.Fatalf("a deleted preview keeps auth: %+v %v", routes, written().Hosts)
	}
}

func TestPreviewEnv(t *testing.T) {
	env := map[string]string{"TIFFIN_AUTH_URL": "https://shop.tiffin.localhost:8443/api/auth", "TIFFIN_AUTH_HOST": "shop.tiffin.localhost", "TIFFIN_AUTH_INTERNAL_URL": "http://10.0.0.1:7393/api/auth"}
	PreviewEnv(env, "https://pr-3--shop.tiffin.localhost:8443")
	if env["TIFFIN_AUTH_URL"] != "https://pr-3--shop.tiffin.localhost:8443/api/auth" || env["TIFFIN_AUTH_HOST"] != "pr-3--shop.tiffin.localhost" ||
		env["TIFFIN_AUTH_JWKS_URL"] != "https://pr-3--shop.tiffin.localhost:8443/api/auth/jwks" || env["TIFFIN_AUTH_INTERNAL_URL"] != "http://10.0.0.1:7393/api/auth" {
		t.Fatalf("preview env: %v", env)
	}
	plain := map[string]string{"TIFFIN_URL": "https://x"}
	PreviewEnv(plain, "https://pr-3--shop.tiffin.localhost:8443")
	if len(plain) != 1 {
		t.Fatalf("no auth, no change: %v", plain)
	}
}
