// Package protect is the Tiffin protect module: per-IP rate limits, the
// proof-of-work challenge and the "under attack" switch in the edge,
// CrowdSec (reads the edge's access log, bans abusive IPs; the edge enforces
// its decisions), an nftables baseline firewall and the opt-in Coraza WAF.
//
// Settings live in the platform KV ("protect" namespace). Every change
// reloads the edge, which asks this module for the current edge.Protection.
package protect

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/edge"
	"github.com/btahir/tiffin/internal/platform"
)

func init() { platform.Register(&Module{}) }

// Module implements the protect module.
type Module struct {
	mu       sync.Mutex
	loaded   bool
	settings Settings
	attack   UnderAttack
	secret   []byte
	now      func() time.Time
	cs       crowdsec // CrowdSec on the box (cscli); replaced in tests
	timer    *time.Timer
	runCtx   context.Context
	expireMu sync.Mutex
}

func (*Module) Name() string { return "protect" }
func (*Module) Order() int   { return 5 }

const kvNS = "protect"

// Limit is a per-client-IP request budget over a sliding window.
type Limit struct {
	Requests      int `json:"requests" minimum:"0" maximum:"1000000" doc:"Requests allowed per client IP in the window. 0 turns this limit off."`
	WindowSeconds int `json:"windowSeconds" minimum:"1" maximum:"3600" doc:"The sliding window, in seconds."`
}

// Limits are the three zones.
type Limits struct {
	App       Limit `json:"app" doc:"Every app host (not the dashboard)."`
	Auth      Limit `json:"auth" doc:"POST/PUT/PATCH to sign-in, sign-up and password-reset paths (/api/auth/*, /login, ...). Counts on top of app."`
	Dashboard Limit `json:"dashboard" doc:"The dashboard and API host. Kept generous so the owner is never locked out (at least 5 requests per second)."`
}

// AttackLimits are the app and auth limits while under attack.
type AttackLimits struct {
	App  Limit `json:"app"`
	Auth Limit `json:"auth"`
}

// ChallengeSettings say which hosts always get the proof-of-work challenge.
type ChallengeSettings struct {
	Hosts      []string `json:"hosts" doc:"Hosts that always get the proof-of-work challenge: full host names (shop.tiffin.localhost) or first-level names (shop). \"*\" means every app host. Empty: only while under attack."`
	Difficulty int      `json:"difficulty" minimum:"8" maximum:"24" doc:"Leading zero bits the browser's SHA-256 proof needs. 16 takes a phone well under a second; each +1 doubles the work."`
}

// AttackSettings are what the under-attack switch tightens to.
type AttackSettings struct {
	Limits     AttackLimits `json:"limits" doc:"Limits while under attack. They only ever tighten the normal ones; the dashboard's are never tightened."`
	Difficulty int          `json:"difficulty" minimum:"8" maximum:"24" doc:"Challenge difficulty while under attack."`
}

// Settings are the protection settings the owner (or an agent) controls.
type Settings struct {
	Limits      Limits            `json:"limits"`
	Challenge   ChallengeSettings `json:"challenge"`
	UnderAttack AttackSettings    `json:"underAttack"`
	WAF         bool              `json:"waf" doc:"Coraza web application firewall with the OWASP core rule set on every app host. Off by default: it can block unusual but legitimate requests."`
}

// DefaultSettings are what a new box starts with.
func DefaultSettings() Settings {
	var s Settings
	s.Limits = Limits{
		App:       Limit{Requests: 300, WindowSeconds: 10},
		Auth:      Limit{Requests: 10, WindowSeconds: 60},
		Dashboard: Limit{Requests: 1200, WindowSeconds: 10},
	}
	s.Challenge.Hosts = []string{}
	s.Challenge.Difficulty = 16
	s.UnderAttack.Limits = AttackLimits{
		App:  Limit{Requests: 60, WindowSeconds: 10},
		Auth: Limit{Requests: 5, WindowSeconds: 60},
	}
	s.UnderAttack.Difficulty = 18
	return s
}

// UnderAttack is the switch's state.
type UnderAttack struct {
	On    bool      `json:"on"`
	Since time.Time `json:"since,omitzero"`
	Until time.Time `json:"until,omitzero" doc:"When it turns itself off."`
	By    string    `json:"by,omitempty" doc:"The token that turned it on."`
}

func (u UnderAttack) active(now time.Time) bool { return u.On && now.Before(u.Until) }

func (m *Module) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// load reads settings, the switch and the challenge secret from the KV once.
func (m *Module) load(ctx context.Context, p *platform.Platform) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loaded {
		return nil
	}
	s := DefaultSettings()
	if raw, ok, err := p.DB.KVGet(ctx, kvNS, "settings"); err != nil {
		return err
	} else if ok {
		if err := json.Unmarshal(raw, &s); err != nil {
			return fmt.Errorf("protect: stored settings: %w", err)
		}
	}
	var ua UnderAttack
	if raw, ok, err := p.DB.KVGet(ctx, kvNS, "under-attack"); err != nil {
		return err
	} else if ok {
		_ = json.Unmarshal(raw, &ua)
	}
	raw, ok, err := p.DB.KVGet(ctx, kvNS, "challenge-secret")
	if err != nil {
		return err
	}
	secret, _ := hex.DecodeString(string(raw))
	if !ok || len(secret) < 32 {
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return err
		}
		if err := p.DB.KVPut(ctx, kvNS, "challenge-secret", []byte(hex.EncodeToString(secret))); err != nil {
			return err
		}
	}
	m.settings, m.attack, m.secret, m.loaded = s, ua, secret, true
	return nil
}

func (m *Module) snapshot() (Settings, UnderAttack) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings, m.attack
}

func (m *Module) saveSettings(ctx context.Context, p *platform.Platform, s Settings) error {
	raw, _ := json.Marshal(s)
	if err := p.DB.KVPut(ctx, kvNS, "settings", raw); err != nil {
		return err
	}
	m.mu.Lock()
	m.settings = s
	m.mu.Unlock()
	return nil
}

func (m *Module) saveAttack(ctx context.Context, p *platform.Platform, ua UnderAttack) error {
	raw, _ := json.Marshal(ua)
	if err := p.DB.KVPut(ctx, kvNS, "under-attack", raw); err != nil {
		return err
	}
	m.mu.Lock()
	m.attack = ua
	m.mu.Unlock()
	m.schedule(p)
	return nil
}

// schedule arms a timer for the moment "under attack" ends, so the edge
// relaxes on time (the 5-second loop in Start is the fallback).
func (m *Module) schedule(p *platform.Platform) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
	if !m.attack.On || m.runCtx == nil {
		return
	}
	ctx := m.runCtx
	m.timer = time.AfterFunc(time.Until(m.attack.Until)+50*time.Millisecond, func() { m.expire(ctx, p) })
}

func (l Limit) edge() edge.Limit {
	return edge.Limit{Events: l.Requests, Window: time.Duration(l.WindowSeconds) * time.Second}
}

// Effective is what the edge enforces right now.
type Effective struct {
	Limits              Limits   `json:"limits"`
	ChallengeHosts      []string `json:"challengeHosts" doc:"Hosts behind the challenge; \"*\" means every app host."`
	ChallengeDifficulty int      `json:"challengeDifficulty"`
	WAF                 bool     `json:"waf"`
	CrowdSec            bool     `json:"crowdsec" doc:"Whether the edge enforces CrowdSec decisions."`
}

func (m *Module) effective(p *platform.Platform) Effective {
	s, ua := m.snapshot()
	e := Effective{Limits: s.Limits, ChallengeDifficulty: s.Challenge.Difficulty, WAF: s.WAF, ChallengeHosts: []string{}}
	for _, h := range s.Challenge.Hosts {
		e.ChallengeHosts = append(e.ChallengeHosts, fullHost(p, h))
	}
	if ua.active(m.clock()) {
		e.Limits.App = tighter(s.Limits.App, s.UnderAttack.Limits.App)
		e.Limits.Auth = tighter(s.Limits.Auth, s.UnderAttack.Limits.Auth)
		e.ChallengeHosts = []string{"*"}
		if s.UnderAttack.Difficulty > e.ChallengeDifficulty {
			e.ChallengeDifficulty = s.UnderAttack.Difficulty
		}
	}
	e.CrowdSec = m.bouncer() != nil
	return e
}

// tighter returns the stricter of two limits (by requests per second); an
// "off" limit (0 requests) is the loosest.
func tighter(a, b Limit) Limit {
	if b.Requests == 0 {
		return a
	}
	if a.Requests == 0 {
		return b
	}
	if float64(b.Requests)/float64(b.WindowSeconds) < float64(a.Requests)/float64(a.WindowSeconds) {
		return b
	}
	return a
}

func fullHost(p *platform.Platform, h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if h == "*" || strings.Contains(h, ".") || p == nil || p.Domain == "" {
		return h
	}
	return p.Host(h)
}

// protection builds the edge's protection layer. The edge calls it on every load.
func (m *Module) protection(p *platform.Platform) *edge.Protection {
	m.mu.Lock()
	loaded, secret := m.loaded, m.secret
	m.mu.Unlock()
	if !loaded {
		return nil
	}
	e := m.effective(p)
	out := &edge.Protection{
		App:       e.Limits.App.edge(),
		Auth:      e.Limits.Auth.edge(),
		Dashboard: e.Limits.Dashboard.edge(),
		WAF:       e.WAF,
		CrowdSec:  m.bouncer(),
	}
	if len(e.ChallengeHosts) > 0 {
		out.Challenge = &edge.Challenge{Hosts: e.ChallengeHosts, Secret: secret, Difficulty: e.ChallengeDifficulty}
	}
	return out
}

// bouncer is the CrowdSec bouncer config, when the provisioner created a key.
func (m *Module) bouncer() *edge.CrowdSec {
	key, err := os.ReadFile(bouncerKeyFile)
	if err != nil || strings.TrimSpace(string(key)) == "" {
		return nil
	}
	return &edge.CrowdSec{APIURL: "http://" + lapiAddr + "/", APIKey: strings.TrimSpace(string(key))}
}

// Start loads the settings, hands the edge its protection source and runs
// the loop that turns "under attack" off when its time is up.
func (m *Module) Start(ctx context.Context, p *platform.Platform) error {
	if err := m.load(ctx, p); err != nil {
		return err
	}
	edge.SetProtectionSource(func() *edge.Protection { return m.protection(p) })
	m.mu.Lock()
	m.runCtx = ctx
	m.mu.Unlock()
	m.schedule(p)
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.expire(ctx, p)
			}
		}
	}()
	return nil
}

// expire turns an elapsed "under attack" switch off and reloads the edge.
func (m *Module) expire(ctx context.Context, p *platform.Platform) {
	m.expireMu.Lock()
	defer m.expireMu.Unlock()
	_, ua := m.snapshot()
	if !ua.On || ua.active(m.clock()) {
		return
	}
	if err := m.saveAttack(ctx, p, UnderAttack{}); err != nil {
		p.Log.Error("protect: turn off under attack", "err", err)
		return
	}
	_ = p.DB.Audit(ctx, "protect", "protect.under-attack.expired", "box", map[string]any{"since": ua.Since, "until": ua.Until})
	if err := p.RefreshRoutes(ctx); err != nil {
		p.Log.Error("protect: reload edge", "err", err)
	}
}

// Checks reports protection health for /v1/status.
func (m *Module) Checks(ctx context.Context, p *platform.Platform) []platform.Check {
	if err := m.load(ctx, p); err != nil {
		return []platform.Check{{Name: "protection", OK: false, Detail: err.Error()}}
	}
	st := m.status(ctx, p)
	out := []platform.Check{{Name: "protection", OK: st.Edge.Applied || p.Edge == nil, Detail: st.Summary}}
	if st.CrowdSec.Installed {
		out = append(out, platform.Check{Name: "crowdsec", OK: st.CrowdSec.Detecting && st.CrowdSec.Enforced, Detail: st.CrowdSec.Detail})
	}
	if st.Firewall.Installed {
		out = append(out, platform.Check{Name: "firewall", OK: st.Firewall.Active || st.Firewall.Off, Detail: st.Firewall.Detail})
	}
	return out
}
