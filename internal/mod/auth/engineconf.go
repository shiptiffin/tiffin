package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/projicon"
)

// OAuthApp is a sign-in provider's client credentials and settings.
type OAuthApp struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	TenantID     string `json:"tenantId,omitempty"` // microsoft
	Issuer       string `json:"issuer,omitempty"`   // gitlab (self-managed), oidc
	Label        string `json:"label,omitempty"`    // oidc: the button's name
	// Proxied: the box-wide keys, so the provider calls back to the one
	// callback URL on the dashboard host and the OAuth proxy hands the
	// sign-in back to the app host it started on.
	Proxied bool `json:"proxied,omitempty"`
}

// OAuthProxy is what a project's instance needs to send sign-ins out
// through one callback URL. URL: the box's (dashboard host), for providers
// on the box-wide keys. AppURL: the app's own sign-in address, for providers
// on the project's own keys: sign-ins started on any other host of the app
// (box subdomain, www, previews) come back through it, so the provider only
// ever needs that one redirect URI. The secret stays in the engine config.
type OAuthProxy struct {
	URL    string `json:"url,omitempty"`
	AppURL string `json:"appUrl,omitempty"`
	Secret string `json:"secret"`
}

// ProxyConfig is the engine's callback endpoint for box-wide keys: the
// dashboard host's /api/auth/callback/<provider>, served by the box (which
// forwards it to the engine, see DashboardHandler).
type ProxyConfig struct {
	URL    string               `json:"url"`
	Host   string               `json:"host"`
	Secret string               `json:"secret"`
	Social map[string]*OAuthApp `json:"social"`
}

// ProjectConfig is one project's entry in the engine config. It mirrors
// packages/auth-engine/src/config.ts.
type ProjectConfig struct {
	Secret           string               `json:"secret"`
	DatabaseURL      string               `json:"databaseUrl"`
	SMTPURL          string               `json:"smtpUrl"`
	EmailFrom        string               `json:"emailFrom"`
	AppName          string               `json:"appName"`
	EmailBrand       *EmailBrand          `json:"emailBrand,omitempty"`
	Hosts            []string             `json:"hosts"`
	PrimaryURL       string               `json:"primaryUrl"`
	Origins          []string             `json:"origins"`
	Methods          []string             `json:"methods"`
	Organizations    bool                 `json:"organizations"`
	Social           map[string]*OAuthApp `json:"social"`
	OAuthProxy       *OAuthProxy          `json:"oauthProxy,omitempty"`
	Captcha          bool                 `json:"captcha"`
	RateLimit        bool                 `json:"rateLimit"`
	AcceptInvitePath string               `json:"acceptInvitePath"`
	// RequireEmailVerification: new users confirm their address before
	// signing in (see EmailVerification).
	RequireEmailVerification bool `json:"requireEmailVerification"`
	// EmailBlocked: production mail can't reach people yet, so email sign-up,
	// links, codes and resets are refused on every host but PreviewHosts.
	EmailBlocked bool     `json:"emailBlocked"`
	PreviewHosts []string `json:"previewHosts"`
}

// EmailBrand is how an app's auth emails look: the project's icon, and the
// button colour the manifest sets (auth.emailAccent). Without one the engine
// draws the dashboard's brass button.
type EmailBrand struct {
	LogoURL string `json:"logoUrl,omitempty"`
	Accent  string `json:"accent,omitempty"`
}

// emailBrand is the project's icon (its public PNG, when the box is served
// over https; mail clients block plain http images) and the accent the
// manifest asks for, if any. The enamel the dashboard draws a project with
// is only for telling projects apart there: it is not the app's brand.
func emailBrand(ctx context.Context, p *platform.Platform, project string, a *manifest.Auth) *EmailBrand {
	b := &EmailBrand{Accent: a.EmailAccent}
	if u, err := url.Parse(strings.TrimRight(p.PublicURL, "/")); err == nil && u.Scheme == "https" && u.Host != "" {
		if id, err := projicon.PublicID(ctx, p.DB, project); err == nil && id != "" {
			b.LogoURL = u.String() + "/v1/icons/" + id + ".png"
		}
	}
	return b
}

// EngineConfig is the whole engine config file.
type EngineConfig struct {
	Version  int                       `json:"version"`
	Listen   []string                  `json:"listen"`
	Projects map[string]*ProjectConfig `json:"projects"`
	Proxy    *ProxyConfig              `json:"proxy,omitempty"`
}

// ErrNeedsPostgres is returned for projects with auth but no postgres.
var ErrNeedsPostgres = errors.New("services.auth keeps users in the project's own Postgres database, which is not set up yet: it is with the project's next apply")

// hostIP is the address app containers reach box services on (published by
// the runtime module as KV runtime/host-ip; 127.0.0.1 until then).
func hostIP(ctx context.Context, p *platform.Platform) string {
	if raw, ok, _ := p.DB.KVGet(ctx, "runtime", "host-ip"); ok && strings.TrimSpace(string(raw)) != "" {
		return strings.TrimSpace(string(raw))
	}
	return "127.0.0.1"
}

// moduleEnv asks another module (by name) for its env, without importing it.
func moduleEnv(ctx context.Context, p *platform.Platform, name, project string) (map[string]string, error) {
	for _, m := range platform.Modules() {
		if m.Name() != name {
			continue
		}
		if ep, ok := m.(platform.EnvProvider); ok {
			return ep.Env(ctx, p, project, "")
		}
	}
	return nil, nil
}

// projectSecret returns the project's engine secret, creating it once.
func projectSecret(ctx context.Context, p *platform.Platform, project string) (string, error) {
	v, ok, err := p.DB.KVGet(ctx, nsSecret, project)
	if err != nil {
		return "", err
	}
	if ok && len(v) >= 32 {
		return string(v), nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	s := base64.RawURLEncoding.EncodeToString(b)
	return s, p.DB.KVPut(ctx, nsSecret, project, []byte(s))
}

// Why email verification is on or off for a project.
const (
	VerifyManifest = "manifest" // auth.emailVerification says so
	VerifyRelay    = "relay"    // automatic: on, mail leaves the box
	VerifyNoRelay  = "no-relay" // automatic: off, mail only reaches the dev inbox
	VerifyNoEmail  = "no-email" // automatic: off, the project has no email service
)

// EmailVerification decides whether a project's new users must confirm their
// address: what the manifest says, else on exactly when the project's mail
// leaves the box (an SMTP relay is set up). Without a relay a required
// confirmation would only land in the dev inbox and block every sign-up.
func EmailVerification(ctx context.Context, p *platform.Platform, project string, a *manifest.Auth, hasEmail bool) (bool, string) {
	if a.EmailVerification != nil {
		return *a.EmailVerification, VerifyManifest
	}
	if !hasEmail {
		return false, VerifyNoEmail
	}
	for _, m := range platform.Modules() {
		if s, ok := m.(interface {
			WillSend(context.Context, *platform.Platform, string) (bool, error)
		}); ok && m.Name() == "email" {
			if sends, err := s.WillSend(ctx, p, project); err == nil && sends {
				return true, VerifyRelay
			}
		}
	}
	return false, VerifyNoRelay
}

// projectConfig builds one project's engine entry from its resources.
func projectConfig(ctx context.Context, p *platform.Platform, project string, res map[string]change.Resource) (*ProjectConfig, error) {
	var a manifest.Auth
	if r, ok := res[change.KindService+"/auth"]; ok {
		if err := json.Unmarshal(r.Spec, &a); err != nil {
			return nil, fmt.Errorf("auth spec: %w", err)
		}
	}
	if len(a.Methods) == 0 {
		a.Methods = manifest.DefaultAuthMethods
	}
	if _, ok := res[change.KindService+"/postgres"]; !ok {
		return nil, ErrNeedsPostgres
	}
	pgEnv, err := moduleEnv(ctx, p, "postgres", project)
	if err != nil {
		return nil, fmt.Errorf("postgres connection: %w", err)
	}
	dbURL := pgEnv["DATABASE_URL"]
	if dbURL == "" {
		return nil, errors.New("the postgres module gave no DATABASE_URL for this project yet; it is retried on the next apply")
	}
	secret, err := projectSecret(ctx, p, project)
	if err != nil {
		return nil, err
	}
	c := &ProjectConfig{
		Secret:           secret,
		DatabaseURL:      dbURL,
		AppName:          project, // the name people gave it: APP_NAME below, else the project's own
		Methods:          append([]string(nil), a.Methods...),
		Organizations:    a.Organizations,
		Social:           map[string]*OAuthApp{},
		Captcha:          true,
		RateLimit:        true,
		AcceptInvitePath: "/accept-invite",
	}
	if v := res[change.KindEnv+"/APP_NAME"]; len(v.Spec) > 0 {
		var name string
		if json.Unmarshal(v.Spec, &name) == nil && strings.TrimSpace(name) != "" {
			c.AppName = strings.TrimSpace(name)
		}
	}
	c.EmailBrand = emailBrand(ctx, p, project, &a)
	for _, h := range webHosts(p, res) {
		c.Hosts = append(c.Hosts, h.Host)
		c.Origins = append(c.Origins, p.URL(h.Host))
	}
	if len(c.Hosts) == 0 {
		// No web app yet: the engine still needs a base URL for links.
		c.Hosts = []string{p.Host(project)}
		c.Origins = []string{p.URL(p.Host(project))}
	}
	c.PrimaryURL = c.Origins[0]
	// Previews come after: the engine treats them as more hosts of the app
	// (own cookies, own passkey rpID), on the same users.
	previewed, err := previewHosts(ctx, p, project)
	if err != nil {
		return nil, err
	}
	c.PreviewHosts = []string{}
	for _, h := range previewed {
		if !contains(c.Hosts, h.Host) {
			c.Hosts = append(c.Hosts, h.Host)
			c.Origins = append(c.Origins, p.URL(h.Host))
			c.PreviewHosts = append(c.PreviewHosts, h.Host)
		}
	}

	// Email: the email module's relay or dev inbox.
	mailEnv, err := moduleEnv(ctx, p, "email", project)
	if err != nil {
		return nil, fmt.Errorf("email settings: %w", err)
	}
	c.SMTPURL = mailEnv["SMTP_URL"]
	c.EmailFrom = firstNonEmpty(mailEnv["EMAIL_FROM"], mailEnv["SMTP_FROM"])
	if c.EmailFrom == "" {
		if r, ok := res[change.KindService+"/email"]; ok {
			var e manifest.Email
			_ = json.Unmarshal(r.Spec, &e)
			c.EmailFrom = e.From
		}
	}
	if c.EmailFrom == "" {
		c.EmailFrom = project + "@" + p.Domain
	}
	c.RequireEmailVerification, _ = EmailVerification(ctx, p, project, &a, c.SMTPURL != "")
	c.EmailBlocked = EmailBlocked(ctx, p, project, c.SMTPURL != "")

	// Sign-in providers: the project's own keys win, else the box-wide ones.
	if err := socialApps(ctx, p, project, res, c); err != nil {
		return nil, err
	}
	return c, nil
}

// socialApps fills c.Social for the sign-in providers among c.Methods, and
// c.OAuthProxy: the box's callback URL for those on box-wide keys, the
// app's own sign-in address for those on the project's keys.
func socialApps(ctx context.Context, p *platform.Platform, project string, res map[string]change.Resource, c *ProjectConfig) error {
	src, err := KeySources(ctx, p, project, c.Methods)
	if err != nil {
		return err
	}
	var sec, boxSec map[string]string
	var box map[string]*BoxProvider
	for _, m := range c.Methods {
		prov, ok := ProviderByID(m)
		if !ok {
			continue
		}
		switch src[m] {
		case KeysProject:
			if sec == nil {
				if sec, err = p.Secrets.All(ctx, project); err != nil {
					return err
				}
			}
			a, err := projectApp(ctx, p, prov, sec)
			if err != nil {
				return err
			}
			c.Social[m] = a
			if err := withProxy(ctx, p, c); err != nil {
				return err
			}
			c.OAuthProxy.AppURL = p.URL(signInHost(p, project, res))
		case KeysBox:
			if box == nil {
				if box, err = boxProviders(ctx, p); err != nil {
					return err
				}
				if boxSec, err = p.Secrets.All(ctx, secretsProject); err != nil {
					return err
				}
			}
			a, err := boxApp(ctx, p, prov, box[m], boxSec)
			if err != nil {
				// The rest of sign-in still works; this button explains it isn't set up.
				if p.Log != nil {
					p.Log.Warn("auth: box-wide sign-in keys unusable", "provider", m, "project", project, "err", err)
				}
				continue
			}
			a.Proxied = true
			c.Social[m] = a
			if err := withProxy(ctx, p, c); err != nil {
				return err
			}
			c.OAuthProxy.URL = ProxyURL(p)
		}
	}
	return nil
}

func withProxy(ctx context.Context, p *platform.Platform, c *ProjectConfig) error {
	if c.OAuthProxy != nil {
		return nil
	}
	s, err := proxySecret(ctx, p)
	if err != nil {
		return err
	}
	c.OAuthProxy = &OAuthProxy{Secret: s}
	return nil
}

// projectMethods is a project's sign-in methods, nil without auth.
func projectMethods(ctx context.Context, p *platform.Platform, project string) ([]string, error) {
	_, res, err := p.DB.Load(ctx, project)
	if err != nil {
		return nil, err
	}
	r, ok := res[change.KindService+"/auth"]
	if !ok {
		return nil, nil
	}
	var a manifest.Auth
	if err := json.Unmarshal(r.Spec, &a); err != nil {
		return nil, err
	}
	if len(a.Methods) == 0 {
		return manifest.DefaultAuthMethods, nil
	}
	return a.Methods, nil
}

// proxyConfig is the engine's callback endpoint for every box-wide
// provider, nil when the box has none.
func proxyConfig(ctx context.Context, p *platform.Platform) (*ProxyConfig, error) {
	box, err := boxProviders(ctx, p)
	if err != nil || len(box) == 0 {
		return nil, err
	}
	sec, err := p.Secrets.All(ctx, secretsProject)
	if err != nil {
		return nil, err
	}
	s, err := proxySecret(ctx, p)
	if err != nil {
		return nil, err
	}
	out := &ProxyConfig{URL: ProxyURL(p), Host: p.DashboardHost(), Secret: s, Social: map[string]*OAuthApp{}}
	for _, prov := range Providers {
		b := box[prov.ID]
		if b == nil {
			continue
		}
		a, err := boxApp(ctx, p, prov, b, sec)
		if err != nil {
			if p.Log != nil {
				p.Log.Warn("auth: box-wide sign-in keys unusable", "provider", prov.ID, "err", err)
			}
			continue
		}
		out.Social[prov.ID] = a
	}
	return out, nil
}

// buildEngineConfig collects every auth-enabled project. Projects that
// can't be configured yet are left out and reported in errs.
func buildEngineConfig(ctx context.Context, p *platform.Platform) (*EngineConfig, map[string]error, error) {
	projects, err := p.DB.ListProjects(ctx)
	if err != nil {
		return nil, nil, err
	}
	sort.Strings(projects)
	out := &EngineConfig{Version: 1, Listen: []string{}, Projects: map[string]*ProjectConfig{}}
	if ip := hostIP(ctx, p); ip != "127.0.0.1" {
		out.Listen = append(out.Listen, net.JoinHostPort(ip, enginePort))
	}
	errs := map[string]error{}
	claimed := map[string]string{} // host → project, first by name wins (as in Routes)
	for _, project := range projects {
		_, res, err := p.DB.Load(ctx, project)
		if err != nil {
			return nil, nil, err
		}
		if _, ok := res[change.KindService+"/auth"]; !ok {
			continue
		}
		c, err := projectConfig(ctx, p, project, res)
		if err != nil {
			errs[project] = err
			continue
		}
		var hosts, origins []string
		for i, h := range c.Hosts {
			if other, ok := claimed[h]; ok && other != project {
				continue
			}
			claimed[h] = project
			hosts, origins = append(hosts, h), append(origins, c.Origins[i])
		}
		if len(hosts) == 0 {
			errs[project] = fmt.Errorf("every host of this project is already served by project %s; give its apps their own routes", claimed[c.Hosts[0]])
			continue
		}
		c.Hosts, c.Origins, c.PrimaryURL = hosts, origins, origins[0]
		if px := c.OAuthProxy; px != nil && px.AppURL != "" && !contains(origins, px.AppURL) {
			px.AppURL = c.PrimaryURL // its sign-in host is another project's: use one it has
		}
		out.Projects[project] = c
	}
	if p.Secrets != nil {
		if out.Proxy, err = proxyConfig(ctx, p); err != nil {
			return nil, nil, err
		}
	}
	return out, errs, nil
}

// writeEngineConfig writes the config atomically, readable only by the engine.
func writeEngineConfig(p *platform.Platform, c *EngineConfig) (changed bool, err error) {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return false, err
	}
	path := ConfigPath(p)
	if cur, err := os.ReadFile(path); err == nil && string(cur) == string(b) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return false, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return false, err
	}
	if u, err := user.Lookup(EngineUser); err == nil {
		uid, _ := strconv.Atoi(u.Uid)
		gid, _ := strconv.Atoi(u.Gid)
		_ = os.Chown(tmp, uid, gid)
	}
	return true, os.Rename(tmp, path)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}
