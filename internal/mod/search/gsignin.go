package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/platform"
)

// gsignin is the Google OAuth app the box connects with, and how it is
// reached. A box of your own uses its own Google sign-in keys (Box settings
// › Sign-in providers) and talks to Google itself. A managed box uses
// ShipTiffin's app through the control plane, so its owner sets nothing up:
// Google only sends people back to addresses registered in advance, and
// only the app's secret can trade a code or renew a token, so the control
// plane does those two things for the box. The refresh token stays on the
// box either way.
type gsignin interface {
	// id names the app: a refresh token works only with the app that got it.
	id() string
	// project is the app's Google Cloud project number ("" when it isn't the
	// box owner's to look after).
	project() string
	// consent is Google's page for a state and the S256 hash of a verifier.
	consent(ctx context.Context, state, challenge string) (string, error)
	// exchange trades what came back for the grant.
	exchange(ctx context.Context, code, verifier string) (*gtoken, error)
	// renew trades the refresh token for an hour's access token.
	renew(ctx context.Context, refresh string) (*gtoken, error)
}

// signin picks the app: ShipTiffin's on a managed box, else the box's own.
func (m *Module) signin(ctx context.Context) (gsignin, error) {
	if cfg := m.managedConfig(); cfg != nil {
		return &relaySignin{cfg: cfg, http: m.client}, nil
	}
	app, err := m.googleApp(ctx)
	if err != nil {
		return nil, err
	}
	return &ownSignin{app: app, http: m.client, base: m.googleBase}, nil
}

func (m *Module) managedConfig() *platform.ManagedConfig {
	if m.managed != nil {
		return m.managed()
	}
	cfg, _ := platform.LoadManagedConfig()
	return cfg
}

// googleApp is the box's own Google sign-in keys.
func (m *Module) googleApp(ctx context.Context) (googleApp, error) {
	if m.gapp != nil {
		return m.gapp(ctx)
	}
	c, ok, err := api.BoxOAuthClient(ctx, "google")
	if err != nil {
		return googleApp{}, err
	}
	if !ok {
		return googleApp{}, errNoGoogleKeys
	}
	return googleApp{ClientID: c.ClientID, ClientSecret: c.ClientSecret, RedirectURL: c.RedirectURL}, nil
}

// ownSignin asks Google with the box's own keys.
type ownSignin struct {
	app  googleApp
	http *http.Client
	base string // Google's hosts, overridden in tests
}

func (s *ownSignin) id() string      { return s.app.ClientID }
func (s *ownSignin) project() string { return s.app.project() }

func (s *ownSignin) consent(_ context.Context, state, challenge string) (string, error) {
	q := url.Values{
		"client_id": {s.app.ClientID}, "redirect_uri": {s.app.RedirectURL}, "response_type": {"code"},
		"scope": {googleScopes}, "state": {state},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
		// offline + consent: Google gives a refresh token every time, not
		// only the first time this account meets the app.
		"access_type": {"offline"}, "prompt": {"consent"},
	}
	return googleAuth + "?" + q.Encode(), nil
}

func (s *ownSignin) exchange(ctx context.Context, code, verifier string) (*gtoken, error) {
	return googleTokenCall(ctx, s.http, rebase(s.base, googleToken), url.Values{"grant_type": {"authorization_code"},
		"code": {code}, "code_verifier": {verifier}, "redirect_uri": {s.app.RedirectURL},
		"client_id": {s.app.ClientID}, "client_secret": {s.app.ClientSecret}})
}

func (s *ownSignin) renew(ctx context.Context, refresh string) (*gtoken, error) {
	return googleTokenCall(ctx, s.http, rebase(s.base, googleToken), url.Values{"grant_type": {"refresh_token"},
		"refresh_token": {refresh}, "client_id": {s.app.ClientID}, "client_secret": {s.app.ClientSecret}})
}

// relaySignin asks through the control plane of a managed box, which holds
// ShipTiffin's Google app. The box proves itself with its licence.
type relaySignin struct {
	cfg  *platform.ManagedConfig
	http *http.Client
}

func (s *relaySignin) id() string      { return "shiptiffin:" + s.cfg.ControlPlane }
func (s *relaySignin) project() string { return "" }

func (s *relaySignin) consent(ctx context.Context, state, challenge string) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	if err := s.post(ctx, "/api/box/google/start", map[string]string{"state": state, "challenge": challenge}, &out); err != nil {
		return "", err
	}
	if u, err := url.Parse(out.URL); err != nil || u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("ShipTiffin gave no Google page to open")
	}
	return out.URL, nil
}

func (s *relaySignin) exchange(ctx context.Context, code, verifier string) (*gtoken, error) {
	var out gtoken
	err := s.post(ctx, "/api/box/google/token", map[string]string{"grant": "code", "code": code, "verifier": verifier}, &out)
	return &out, err
}

func (s *relaySignin) renew(ctx context.Context, refresh string) (*gtoken, error) {
	var out gtoken
	if err := s.post(ctx, "/api/box/google/token", map[string]string{"grant": "refresh", "refresh_token": refresh}, &out); err != nil {
		return nil, err
	}
	if out.AccessToken == "" {
		return nil, fmt.Errorf("ShipTiffin gave no access token")
	}
	return &out, nil
}

// post sends the box's licence and a JSON body. A refusal (Google's own, or
// the control plane's: a lapsed subscription, another box's licence) is a
// GoogleError, so it shows on the connection; an answer that didn't come,
// or a control plane in trouble, is a passing error.
func (s *relaySignin) post(ctx context.Context, path string, body, out any) error {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.cfg.ControlPlane, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.Licence)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("ShipTiffin could not be reached: %w", err)
	}
	defer resp.Body.Close()
	ans, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusOK {
		return json.Unmarshal(ans, out)
	}
	var e struct {
		Error string `json:"error"`
		Desc  string `json:"error_description"`
	}
	_ = json.Unmarshal(ans, &e)
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests || e.Error == "" {
		return fmt.Errorf("ShipTiffin answered %d: %s", resp.StatusCode, strings.TrimSpace(e.Desc+" "+e.Error))
	}
	msg := e.Desc
	if msg == "" {
		msg = e.Error
	}
	if e.Error == "invalid_grant" && path == "/api/box/google/token" && body.(map[string]string)["grant"] == "refresh" {
		msg = "Google no longer accepts the connection (" + msg + "). Connect with Google again"
	}
	return &GoogleError{Status: resp.StatusCode, Message: "sign-in refused: " + msg}
}
