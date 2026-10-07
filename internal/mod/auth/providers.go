package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/platform"
)

// Sign-in providers: the services people can sign in to apps with (Google,
// GitHub, Apple...). Each needs an OAuth app's client ID and secret, from
// one of two places:
//
//   - box-wide: the box owner sets them once (Box settings → Sign-in
//     providers) and every project that turns the method on uses them. The
//     provider sends people back to ONE callback URL on the dashboard host
//     (CallbackURL); Better Auth's OAuth proxy plugin hands the sign-in on
//     to the app host it started on.
//   - per project: the project's own <ENV>_CLIENT_ID and <ENV>_CLIENT_SECRET
//     secrets. They win over the box's. The provider then calls back to each
//     app host (https://<app host>/api/auth/callback/<provider>).
//
// Box-wide secrets are age-encrypted in the "_auth" pseudo-project, like the
// email relay's password; the rest of a provider's settings (client ID,
// tenant, issuer...) are in KV. The API never returns a secret.

// Provider describes one sign-in provider.
type Provider struct {
	ID   string // Better Auth's provider ID and the manifest method
	Name string // what people call it
	Env  string // secret name prefix: GOOGLE_CLIENT_ID...
}

// Providers lists every sign-in provider, in the order the dashboard shows them.
var Providers = []Provider{
	{manifest.AuthGoogle, "Google", "GOOGLE"},
	{manifest.AuthGitHub, "GitHub", "GITHUB"},
	{manifest.AuthApple, "Apple", "APPLE"},
	{manifest.AuthMicrosoft, "Microsoft", "MICROSOFT"},
	{manifest.AuthDiscord, "Discord", "DISCORD"},
	{manifest.AuthFacebook, "Facebook", "FACEBOOK"},
	{manifest.AuthTwitter, "X (Twitter)", "TWITTER"},
	{manifest.AuthLinkedIn, "LinkedIn", "LINKEDIN"},
	{manifest.AuthGitLab, "GitLab", "GITLAB"},
	{manifest.AuthSlack, "Slack", "SLACK"},
	{manifest.AuthTwitch, "Twitch", "TWITCH"},
	{manifest.AuthOIDC, "OpenID Connect", "OIDC"},
}

// ProviderByID finds a provider.
func ProviderByID(id string) (Provider, bool) {
	for _, p := range Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// Where a project's keys for a provider come from.
const (
	KeysProject = "project" // the project's own secrets (they win)
	KeysBox     = "box"     // the box-wide keys, through the one callback URL
	KeysNone    = "none"    // neither: the button explains how to set it up
)

const (
	nsProviders     = "auth/providers" // key: provider ID → BoxProvider (no secrets)
	nsAppleJWT      = "auth/apple-jwt" // key: credential fingerprint → sealed appleJWT
	secretsProject  = "_auth"          // pseudo-project for box-wide secrets; never a real slug
	proxySecretName = "OAUTH_PROXY_SECRET"
	applePrivateKey = "APPLE_PRIVATE_KEY"
	// CallbackPathPrefix is where providers send people back, on app hosts
	// and on the dashboard host alike.
	CallbackPathPrefix = PathPrefix + "/callback/"
)

// BoxProvider is a provider's box-wide settings, without its secret.
type BoxProvider struct {
	ClientID string `json:"clientId" doc:"OAuth client ID (Apple: the Services ID). Not a secret: it is part of every sign-in URL"`
	TenantID string `json:"tenantId,omitempty" doc:"Microsoft: the directory (tenant) ID, or common, organizations or consumers. Default common: work, school and personal accounts"`
	Issuer   string `json:"issuer,omitempty" doc:"OpenID Connect: the issuer URL (its /.well-known/openid-configuration is read). GitLab: a self-managed GitLab's URL; default gitlab.com"`
	Label    string `json:"label,omitempty" doc:"OpenID Connect: the button's name, e.g. Okta or Acme SSO"`
	// ConsentName is what the provider shows people when they sign in with
	// these keys (the OAuth app's name in its console), so a project can
	// tell whether that suits it or it needs its own keys.
	ConsentName string `json:"consentName,omitempty" doc:"The OAuth app's name as the provider shows it on its sign-in screen, e.g. Acme Labs. For your own reference"`
	TeamID      string `json:"teamId,omitempty" doc:"Apple: the Team ID"`
	KeyID       string `json:"keyId,omitempty" doc:"Apple: the Key ID of the Sign in with Apple private key"`
	// Set by the box.
	UpdatedAt time.Time `json:"updatedAt"`
	UpdatedBy string    `json:"updatedBy,omitempty"`
}

func secretName(prov Provider) string {
	if prov.ID == manifest.AuthApple {
		return applePrivateKey
	}
	return prov.Env + "_CLIENT_SECRET"
}

// boxProviders reads every box-wide provider whose secret is stored too.
func boxProviders(ctx context.Context, p *platform.Platform) (map[string]*BoxProvider, error) {
	out := map[string]*BoxProvider{}
	if p == nil || p.DB == nil || p.Secrets == nil {
		return out, nil
	}
	names, err := p.Secrets.List(ctx, secretsProject)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, s := range names {
		have[s.Name] = true
	}
	for _, prov := range Providers {
		raw, ok, err := p.DB.KVGet(ctx, nsProviders, prov.ID)
		if err != nil {
			return nil, err
		}
		if !ok || !have[secretName(prov)] {
			continue
		}
		var b BoxProvider
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, fmt.Errorf("box-wide %s settings: %w", prov.Name, err)
		}
		out[prov.ID] = &b
	}
	return out, nil
}

// boxApp builds the engine's OAuth app for a box-wide provider (secrets decrypted).
func boxApp(ctx context.Context, p *platform.Platform, prov Provider, b *BoxProvider, sec map[string]string) (*OAuthApp, error) {
	a := &OAuthApp{ClientID: b.ClientID, TenantID: b.TenantID, Issuer: b.Issuer, Label: b.Label}
	if prov.ID == manifest.AuthApple {
		jwt, _, err := appleSecret(ctx, p, b.TeamID, b.KeyID, b.ClientID, sec[applePrivateKey])
		if err != nil {
			return nil, fmt.Errorf("box-wide Apple keys: %w", err)
		}
		a.ClientSecret = jwt
		return a, nil
	}
	a.ClientSecret = sec[secretName(prov)]
	return a, nil
}

// projectHas reports whether a project's secrets (by name) hold complete
// keys for the provider. Partial keys don't count: the box's are used.
func projectHas(prov Provider, have func(string) bool) bool {
	e := prov.Env
	switch prov.ID {
	case manifest.AuthApple:
		return have(e+"_CLIENT_ID") && (have(e+"_CLIENT_SECRET") || (have(e+"_TEAM_ID") && have(e+"_KEY_ID") && have(e+"_PRIVATE_KEY")))
	case manifest.AuthOIDC:
		return have(e+"_ISSUER") && have(e+"_CLIENT_ID") && have(e+"_CLIENT_SECRET")
	}
	return have(e+"_CLIENT_ID") && have(e+"_CLIENT_SECRET")
}

// projectApp builds the engine's OAuth app from a project's secrets. The
// secrets API takes any value, so they are checked here as the box-wide
// keys are when stored: the engine leaves out a project whose settings it
// can't use, so a bad value fails this project's apply, never the engine.
func projectApp(ctx context.Context, p *platform.Platform, prov Provider, sec map[string]string) (*OAuthApp, error) {
	e := prov.Env
	a := &OAuthApp{ClientID: strings.TrimSpace(sec[e+"_CLIENT_ID"]), ClientSecret: strings.TrimSpace(sec[e+"_CLIENT_SECRET"])}
	if prov.ID == manifest.AuthApple {
		if a.ClientID == "" {
			return nil, errors.New("the project's APPLE_CLIENT_ID secret is empty")
		}
		if a.ClientSecret == "" {
			jwt, _, err := appleSecret(ctx, p, sec[e+"_TEAM_ID"], sec[e+"_KEY_ID"], a.ClientID, sec[e+"_PRIVATE_KEY"])
			if err != nil {
				return nil, fmt.Errorf("the project's Apple keys (APPLE_TEAM_ID, APPLE_KEY_ID, APPLE_PRIVATE_KEY): %w", err)
			}
			a.ClientSecret = jwt
		}
		return a, nil
	}
	in := ProviderInput{BoxProvider: BoxProvider{ClientID: a.ClientID}, ClientSecret: a.ClientSecret}
	switch prov.ID {
	case manifest.AuthMicrosoft:
		in.TenantID = sec[e+"_TENANT_ID"]
	case manifest.AuthGitLab:
		in.Issuer = sec[e+"_ISSUER"]
	case manifest.AuthOIDC:
		in.Issuer, in.Label = sec[e+"_ISSUER"], sec[e+"_NAME"]
	}
	if err := validateInput(prov, &in, false); err != nil {
		return nil, fmt.Errorf("the project's %s keys (its %s_* secrets): %w", prov.Name, e, err)
	}
	a.ClientID, a.ClientSecret, a.TenantID, a.Issuer, a.Label = in.ClientID, in.ClientSecret, in.TenantID, in.Issuer, in.Label
	return a, nil
}

// KeySources says, for each sign-in provider among methods, where the
// project's keys come from (KeysProject, KeysBox or KeysNone). It reads
// secret names only, never values.
func KeySources(ctx context.Context, p *platform.Platform, project string, methods []string) (map[string]string, error) {
	out := map[string]string{}
	social := false
	for _, m := range methods {
		social = social || manifest.IsSocialAuthMethod(m)
	}
	if !social || p == nil || p.Secrets == nil {
		for _, m := range methods {
			if manifest.IsSocialAuthMethod(m) {
				out[m] = KeysNone
			}
		}
		return out, nil
	}
	names, err := p.Secrets.List(ctx, project)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, s := range names {
		have[s.Name] = true
	}
	box, err := boxProviders(ctx, p)
	if err != nil {
		return nil, err
	}
	for _, m := range methods {
		prov, ok := ProviderByID(m)
		if !ok {
			continue
		}
		switch {
		case projectHas(prov, func(n string) bool { return have[n] }):
			out[m] = KeysProject
		case box[m] != nil:
			out[m] = KeysBox
		default:
			out[m] = KeysNone
		}
	}
	return out, nil
}

// CallbackURL is the one redirect URI to register with a provider for the
// box-wide keys: on the dashboard host, which every box has, with a
// certificate, whatever its projects and their domains.
func CallbackURL(p *platform.Platform, provider string) string {
	return ProxyURL(p) + CallbackPathPrefix + provider
}

// signInHost is the one host a project's own provider keys call back to:
// its first custom domain (it outlives a change of the box's domain), else
// its primary app host. Sign-ins on every other host of the app (the box
// subdomain, www, previews) come back through it.
func signInHost(p *platform.Platform, project string, res map[string]change.Resource) string {
	hosts := webHosts(p, res)
	for _, h := range hosts {
		if !p.IsBoxHost(h.Host) {
			return h.Host
		}
	}
	if len(hosts) > 0 {
		return hosts[0].Host
	}
	return p.Host(project)
}

// AppCallbackURL is the one redirect URI to register with a provider for a
// project's own keys.
func AppCallbackURL(p *platform.Platform, project string, res map[string]change.Resource, provider string) string {
	return p.URL(signInHost(p, project, res)) + CallbackPathPrefix + provider
}

// ProxyURL is the origin box-wide sign-ins come back to.
func ProxyURL(p *platform.Platform) string { return p.URL(p.DashboardHost()) }

// proxySecret is the key the engine's OAuth proxy encrypts sign-in state
// and profiles with, made once. It never leaves the engine config.
func proxySecret(ctx context.Context, p *platform.Platform) (string, error) {
	all, err := p.Secrets.All(ctx, secretsProject)
	if err != nil {
		return "", err
	}
	if s := all[proxySecretName]; len(s) >= 32 {
		return s, nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	s := base64.RawURLEncoding.EncodeToString(b)
	return s, p.Secrets.Set(ctx, secretsProject, proxySecretName, s, "box")
}

// ---------------------------------------------------------------- Apple

// Apple's client secret is a JWT the app signs with its Sign in with Apple
// key (ES256), valid for at most six months. The box makes it from the Team
// ID, Key ID, Services ID and .p8 key, keeps it (sealed) and makes a new
// one when less than appleRenewBefore is left.
const (
	appleSecretLife  = 180 * 24 * time.Hour // Apple allows up to 15777000 s (about 182 days)
	appleRenewBefore = 30 * 24 * time.Hour
	appleAudience    = "https://appleid.apple.com"
)

var timeNow = time.Now

// ParseAppleKey reads a Sign in with Apple private key: the .p8 file's text
// (PKCS #8, P-256), with or without its BEGIN/END lines.
func ParseAppleKey(text string) (*ecdsa.PrivateKey, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("the private key is empty: paste the whole .p8 file")
	}
	var der []byte
	if blk, _ := pem.Decode([]byte(text)); blk != nil {
		der = blk.Bytes
	} else {
		b, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(text), ""))
		if err != nil {
			return nil, errors.New("that isn't a .p8 key: paste the whole file, from -----BEGIN PRIVATE KEY----- to -----END PRIVATE KEY-----")
		}
		der = b
	}
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, errors.New("that isn't a .p8 key: paste the whole file, from -----BEGIN PRIVATE KEY----- to -----END PRIVATE KEY-----")
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok || ec.Curve != elliptic.P256() {
		return nil, errors.New("that key isn't a Sign in with Apple key (an EC P-256 key from Certificates, Identifiers & Profiles → Keys)")
	}
	return ec, nil
}

// AppleClientSecret signs the client secret JWT Apple expects: header
// {alg: ES256, kid: keyID}, claims {iss: teamID, iat, exp, aud:
// https://appleid.apple.com, sub: the Services ID}.
func AppleClientSecret(teamID, keyID, servicesID, keyText string, now time.Time, life time.Duration) (string, error) {
	if teamID == "" || keyID == "" || servicesID == "" {
		return "", errors.New("sign in with Apple needs the Team ID, the Key ID and the Services ID")
	}
	key, err := ParseAppleKey(keyText)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	h, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": keyID})
	c, _ := json.Marshal(map[string]any{"iss": teamID, "iat": now.Unix(), "exp": now.Add(life).Unix(), "aud": appleAudience, "sub": servicesID})
	signing := enc.EncodeToString(h) + "." + enc.EncodeToString(c)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + enc.EncodeToString(sig), nil
}

// VerifyAppleClientSecret checks a client secret JWT's signature with the
// key's public half (tests and the set endpoint use it).
func VerifyAppleClientSecret(jwt string, key *ecdsa.PrivateKey) (map[string]any, error) {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return nil, errors.New("not a JWT")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return nil, errors.New("bad signature encoding")
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(&key.PublicKey, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		return nil, errors.New("signature does not verify")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims map[string]any
	return claims, json.Unmarshal(raw, &claims)
}

type appleJWT struct {
	JWT string    `json:"jwt"`
	Exp time.Time `json:"exp"`
}

func appleFingerprint(teamID, keyID, servicesID, keyText string) string {
	sum := sha256.Sum256([]byte(teamID + "\x00" + keyID + "\x00" + servicesID + "\x00" + strings.TrimSpace(keyText)))
	return hex.EncodeToString(sum[:16])
}

// appleSecret returns a current client secret for these credentials, from
// the sealed cache when it has more than appleRenewBefore left, else a new
// one (kept for next time). The engine config only changes when it renews.
func appleSecret(ctx context.Context, p *platform.Platform, teamID, keyID, servicesID, keyText string) (string, time.Time, error) {
	fp := appleFingerprint(teamID, keyID, servicesID, keyText)
	now := timeNow()
	if raw, ok, err := p.DB.KVGet(ctx, nsAppleJWT, fp); err == nil && ok {
		if plain, err := p.Secrets.Unseal(raw); err == nil {
			var c appleJWT
			if json.Unmarshal(plain, &c) == nil && c.Exp.Sub(now) > appleRenewBefore {
				return c.JWT, c.Exp, nil
			}
		}
	}
	jwt, err := AppleClientSecret(teamID, keyID, servicesID, keyText, now, appleSecretLife)
	if err != nil {
		return "", time.Time{}, err
	}
	c := appleJWT{JWT: jwt, Exp: now.Add(appleSecretLife).UTC().Truncate(time.Second)}
	plain, _ := json.Marshal(c)
	sealed, err := p.Secrets.Seal(plain)
	if err != nil {
		return "", time.Time{}, err
	}
	if err := p.DB.KVPut(ctx, nsAppleJWT, fp, sealed); err != nil {
		return "", time.Time{}, err
	}
	return c.JWT, c.Exp, nil
}

// appleSecretExpiry is when the cached client secret for these credentials
// expires (zero if none is cached yet).
func appleSecretExpiry(ctx context.Context, p *platform.Platform, teamID, keyID, servicesID, keyText string) time.Time {
	raw, ok, err := p.DB.KVGet(ctx, nsAppleJWT, appleFingerprint(teamID, keyID, servicesID, keyText))
	if err != nil || !ok {
		return time.Time{}
	}
	plain, err := p.Secrets.Unseal(raw)
	if err != nil {
		return time.Time{}
	}
	var c appleJWT
	_ = json.Unmarshal(plain, &c)
	return c.Exp
}

// ---------------------------------------------------------------- set / remove

// ProviderInput is what the box owner sets for a provider. Empty secret
// fields keep the stored value.
type ProviderInput struct {
	BoxProvider
	ClientSecret string // OAuth client secret (not Apple)
	PrivateKey   string // Apple: the .p8 key's text
}

// validateInput checks a provider's settings before they are stored.
// stored says whether a secret is already kept (so it may be omitted).
func validateInput(prov Provider, in *ProviderInput, stored bool) error {
	in.ClientID = strings.TrimSpace(in.ClientID)
	in.TenantID = strings.TrimSpace(in.TenantID)
	in.Issuer = strings.TrimRight(strings.TrimSpace(in.Issuer), "/")
	in.Label = strings.TrimSpace(in.Label)
	in.TeamID = strings.TrimSpace(in.TeamID)
	in.KeyID = strings.TrimSpace(in.KeyID)
	in.ClientSecret = strings.TrimSpace(in.ClientSecret)
	in.ConsentName = strings.TrimSpace(in.ConsentName)
	if in.ClientID == "" {
		if prov.ID == manifest.AuthApple {
			return errors.New("services ID: required (the identifier of the Services ID, like com.example.signin)")
		}
		return errors.New("client ID: required")
	}
	if strings.ContainsAny(in.ClientID, " \t\n") {
		return errors.New("client ID: no spaces; copy it again from the provider's console")
	}
	switch prov.ID {
	case manifest.AuthApple:
		if in.TeamID == "" || in.KeyID == "" {
			return errors.New("sign in with Apple needs the Team ID and the Key ID")
		}
		if in.PrivateKey == "" && !stored {
			return errors.New("private key: paste the .p8 file you downloaded from Apple")
		}
		if in.PrivateKey != "" {
			if _, err := ParseAppleKey(in.PrivateKey); err != nil {
				return fmt.Errorf("private key: %w", err)
			}
		}
		in.ClientSecret = ""
		in.TenantID, in.Issuer, in.Label = "", "", ""
		return nil
	case manifest.AuthMicrosoft:
		if in.TenantID != "" && strings.ContainsAny(in.TenantID, " /:") {
			return errors.New("tenant: a directory (tenant) ID, a domain, or common, organizations or consumers")
		}
	case manifest.AuthGitLab, manifest.AuthOIDC:
		if in.Issuer == "" && prov.ID == manifest.AuthOIDC {
			return errors.New("issuer URL: required, e.g. https://example.okta.com or https://example.us.auth0.com")
		}
		if in.Issuer != "" {
			u, err := url.Parse(in.Issuer)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
				return errors.New("issuer URL: an https:// URL with no query, like https://example.okta.com")
			}
		}
	}
	if prov.ID != manifest.AuthMicrosoft {
		in.TenantID = ""
	}
	if prov.ID != manifest.AuthGitLab && prov.ID != manifest.AuthOIDC {
		in.Issuer = ""
	}
	if prov.ID != manifest.AuthOIDC {
		in.Label = ""
	}
	in.TeamID, in.KeyID, in.PrivateKey = "", "", ""
	if in.ClientSecret == "" && !stored {
		return errors.New("client secret: required")
	}
	return nil
}

// setProvider stores a provider's box-wide settings and secret.
func setProvider(ctx context.Context, p *platform.Platform, prov Provider, in ProviderInput, by string) error {
	all, err := p.Secrets.All(ctx, secretsProject)
	if err != nil {
		return err
	}
	stored := all[secretName(prov)] != ""
	if err := validateInput(prov, &in, stored); err != nil {
		return err
	}
	if prov.ID == manifest.AuthApple {
		key := in.PrivateKey
		if key == "" {
			key = all[applePrivateKey]
		}
		// Sign once now, so a wrong key fails here rather than at sign-in.
		if _, _, err := appleSecret(ctx, p, in.TeamID, in.KeyID, in.ClientID, key); err != nil {
			return err
		}
	}
	secret := in.ClientSecret
	if prov.ID == manifest.AuthApple {
		secret = in.PrivateKey
	}
	if secret != "" {
		if err := p.Secrets.Set(ctx, secretsProject, secretName(prov), secret, by); err != nil {
			return err
		}
	}
	b := in.BoxProvider
	b.UpdatedAt, b.UpdatedBy = timeNow().UTC(), by
	raw, _ := json.Marshal(b)
	return p.DB.KVPut(ctx, nsProviders, prov.ID, raw)
}

// removeProvider deletes a provider's box-wide settings and secret.
func removeProvider(ctx context.Context, p *platform.Platform, prov Provider) (bool, error) {
	had, err := p.Secrets.Delete(ctx, secretsProject, secretName(prov))
	if err != nil {
		return false, err
	}
	_, ok, err := p.DB.KVGet(ctx, nsProviders, prov.ID)
	if err != nil {
		return false, err
	}
	if err := p.DB.KVDelete(ctx, nsProviders, prov.ID); err != nil {
		return false, err
	}
	return had || ok, nil
}

// usedBy lists the projects that sign in with a provider on the box-wide
// keys (they lose that button when it is removed).
func usedBy(ctx context.Context, p *platform.Platform, provider string) ([]string, error) {
	projects, err := p.DB.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, project := range projects {
		methods, err := projectMethods(ctx, p, project)
		if err != nil || methods == nil {
			continue
		}
		if !contains(methods, provider) {
			continue
		}
		src, err := KeySources(ctx, p, project, []string{provider})
		if err == nil && src[provider] == KeysBox {
			out = append(out, project)
		}
	}
	sort.Strings(out)
	return out, nil
}
