package protect

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
	"github.com/btahir/tiffin/internal/tokens"
)

// Real `cscli decisions list -o json` output (CrowdSec 1.8.1), trimmed.
const cscliDecisionsJSON = `[
 {"capacity": 0, "created_at": "2026-10-03T01:19:23Z",
  "decisions": [{"duration": "59m59s", "id": 3, "origin": "cscli", "scenario": "fixture", "scope": "Ip", "simulated": false, "type": "ban", "value": "192.0.2.9"}],
  "events_count": 1, "id": 3, "kind": "cscli", "message": "fixture", "scenario": "fixture",
  "source": {"ip": "192.0.2.9", "scope": "Ip", "value": "192.0.2.9"}, "start_at": "2026-10-03T01:19:23Z"},
 {"created_at": "2026-10-03T01:18:37Z",
  "decisions": [{"duration": "3h59m39s", "id": 1, "origin": "crowdsec", "scenario": "tiffin/http-auth-bruteforce", "scope": "Ip", "type": "ban", "value": "203.0.113.7"}],
  "events_count": 11, "id": 1, "message": "Ip 203.0.113.7 performed 'tiffin/http-auth-bruteforce' (11 events over 54ms)", "scenario": "tiffin/http-auth-bruteforce",
  "source": {"ip": "203.0.113.7", "scope": "Ip", "value": "203.0.113.7", "cn": "ZZ", "as_name": "Test Net"}},
 {"created_at": "2026-10-03T01:19:07Z",
  "decisions": [{"duration": "-10s", "id": 2, "origin": "cscli", "scenario": "test range", "scope": "Range", "type": "ban", "value": "198.51.100.0/24"}],
  "events_count": 1, "id": 2, "scenario": "test range", "source": {"scope": "Range", "value": "198.51.100.0/24"}}
]`

func TestParseCscli(t *testing.T) {
	alerts, err := parseAlerts([]byte(cscliDecisionsJSON))
	if err != nil {
		t.Fatal(err)
	}
	ds := decisionsFrom(alerts)
	if len(ds) != 2 || ds[0].ID != 3 || ds[1].Value != "203.0.113.7" || ds[1].Scenario != "tiffin/http-auth-bruteforce" || ds[1].AS != "Test Net" {
		t.Fatalf("decisions: %+v", ds)
	}
	al := alertsFrom(alerts)
	if len(al) != 3 || al[1].Events != 11 || al[1].Source != "203.0.113.7" || al[1].At.IsZero() || al[2].Decisions != 0 {
		t.Fatalf("alerts: %+v", al)
	}
	for _, empty := range []string{"", "null", " null\n"} {
		if a, err := parseAlerts([]byte(empty)); err != nil || a != nil {
			t.Errorf("%q: %v %v", empty, a, err)
		}
	}
}

func TestTargets(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.7": "--ip 203.0.113.7", "::ffff:203.0.113.7": "--ip 203.0.113.7",
		"198.51.100.9/24": "--range 198.51.100.0/24", "2001:db8::/48": "--range 2001:db8::/48",
	} {
		got, err := targetArgs(in)
		if err != nil || strings.Join(got, " ") != want {
			t.Errorf("targetArgs(%q) = %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"127.0.0.1", "127.0.0.0/8", "::1", "10.0.0.0/4", "0.0.0.0/0", "nope", "2001::/8"} {
		if _, err := checkBanTarget(bad); err == nil {
			t.Errorf("checkBanTarget(%q) should refuse", bad)
		}
	}
	for _, ok := range []string{"203.0.113.7", "192.168.1.0/24", "2001:db8::1"} {
		if _, err := checkBanTarget(ok); err != nil {
			t.Errorf("checkBanTarget(%q): %v", ok, err)
		}
	}
}

func TestRenderFirewall(t *testing.T) {
	got, err := renderFirewall(firewallPlan{Interfaces: []string{"eth0", "ens5"}, EdgePorts: []int{80, 443, 8080, 8443}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"table inet tiffin_guard\ndelete table inet tiffin_guard\n", // atomic replace
		"policy drop;",
		`iifname != { "eth0", "ens5" } accept`,
		"ct state established,related accept",
		"tcp dport 22 accept",
		"tcp dport { 80, 443, 8080, 8443 } accept",
		"icmp type echo-request limit rate",
		"udp dport { 68, 546 } accept",
		"ct count over 256",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ruleset lacks %q", want)
		}
	}
	// SSH is accepted before anything that could drop it, except the per-IP limit.
	if strings.Index(got, "tcp dport 22 accept") > strings.Index(got, "tcp dport { 80") {
		t.Error("SSH rule must come before the edge rules")
	}
	if _, err := renderFirewall(firewallPlan{EdgePorts: []int{443}}); err == nil {
		t.Error("no interfaces must be an error, not a ruleset that filters nothing")
	}
	if _, err := renderFirewall(firewallPlan{Interfaces: []string{`eth0"; flush ruleset; "`}}); err == nil {
		t.Error("odd interface names must be refused")
	}
	st := FirewallState{}
	if m := ifacesLineRE.FindStringSubmatch(got); m == nil || !strings.Contains(m[1], "ens5") {
		t.Errorf("status parser cannot read the interfaces back: %v", st)
	}
	if m := edgeLineRE.FindStringSubmatch(got); m == nil || m[1] != "80, 443, 8080, 8443" {
		t.Errorf("status parser cannot read the ports back: %v", m)
	}
}

func TestTighter(t *testing.T) {
	a := Limit{Requests: 300, WindowSeconds: 10}
	b := Limit{Requests: 60, WindowSeconds: 10}
	if tighter(a, b) != b || tighter(b, a) != b || tighter(a, Limit{}) != a || tighter(Limit{}, b) != b {
		t.Error("tighter picks the stricter limit; off never tightens")
	}
}

// fakeCS stands in for cscli.
type fakeCS struct {
	mu   sync.Mutex
	bans map[string]time.Duration
}

func (f *fakeCS) Installed() bool                              { return true }
func (f *fakeCS) Running(context.Context) bool                 { return true }
func (f *fakeCS) Alerts(context.Context, int) ([]Alert, error) { return []Alert{}, nil }
func (f *fakeCS) Decisions(context.Context) ([]Decision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []Decision{}
	for v, d := range f.bans {
		out = append(out, Decision{Value: v, Type: "ban", Origin: "cscli", Expires: d.String()})
	}
	return out, nil
}
func (f *fakeCS) Ban(_ context.Context, target string, d time.Duration, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bans[target] = d
	return nil
}
func (f *fakeCS) Unban(_ context.Context, target string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.bans[target]; !ok {
		return 0, nil
	}
	delete(f.bans, target)
	return 1, nil
}

type env struct {
	t     *testing.T
	srv   *httptest.Server
	owner string
	m     *Module
	p     *platform.Platform
	clock time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tm := tokens.NewManager(db)
	owner, _, err := tm.Bootstrap(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var m *Module
	for _, x := range platform.Modules() {
		if pm, ok := x.(*Module); ok {
			m = pm
		}
	}
	e := &env{t: t, owner: owner, m: m, clock: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)}
	*m = Module{cs: &fakeCS{bans: map[string]time.Duration{}}, now: func() time.Time { return e.clock }}
	e.p = &platform.Platform{DB: db, Engine: change.NewEngine(db), Tokens: tm, Domain: "tiffin.localhost", PublicURL: "https://dashboard.tiffin.localhost:8443"}
	a := api.New(api.Deps{DB: db, Engine: e.p.Engine, Tokens: tm, Version: "test", Platform: e.p})
	e.srv = httptest.NewServer(a.Handler())
	t.Cleanup(e.srv.Close)
	return e
}

func (e *env) call(token, method, path string, body any) (int, map[string]any, []any) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var obj map[string]any
	var arr []any
	if len(raw) > 0 && raw[0] == '[' {
		_ = json.Unmarshal(raw, &arr)
	} else {
		_ = json.Unmarshal(raw, &obj)
	}
	return res.StatusCode, obj, arr
}

func (e *env) token(scopes, projects []string) string {
	e.t.Helper()
	code, out, _ := e.call(e.owner, "POST", "/v1/tokens", map[string]any{"name": "agent", "scopes": scopes, "projects": projects})
	if code != 200 {
		e.t.Fatalf("create token: %d %v", code, out)
	}
	return out["secret"].(string)
}

func dig(m map[string]any, path ...string) any {
	var cur any = m
	for _, k := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

func TestAPI(t *testing.T) {
	e := newEnv(t)

	code, st, _ := e.call(e.owner, "GET", "/v1/protect", nil)
	if code != 200 || dig(st, "effective", "limits", "app", "requests") != float64(300) || dig(st, "underAttack", "on") != false {
		t.Fatalf("status: %d %v", code, st)
	}
	if !strings.Contains(st["summary"].(string), "normal mode") {
		t.Errorf("summary: %v", st["summary"])
	}

	// Settings: partial update, host names, the dashboard floor.
	code, st, _ = e.call(e.owner, "PUT", "/v1/protect", map[string]any{
		"limits":    map[string]any{"auth": map[string]any{"requests": 5, "windowSeconds": 60}},
		"challenge": map[string]any{"hosts": []string{"shop", "blog.tiffin.localhost", "shop"}},
	})
	if code != 200 || dig(st, "settings", "limits", "auth", "requests") != float64(5) || dig(st, "settings", "limits", "app", "requests") != float64(300) {
		t.Fatalf("update: %d %v", code, st)
	}
	if hosts := dig(st, "effective", "challengeHosts").([]any); len(hosts) != 2 || hosts[0] != "shop.tiffin.localhost" {
		t.Errorf("hosts: %v", hosts)
	}
	for name, body := range map[string]any{
		"dashboard floor":     map[string]any{"limits": map[string]any{"dashboard": map[string]any{"requests": 10, "windowSeconds": 10}}},
		"dashboard off":       map[string]any{"limits": map[string]any{"dashboard": map[string]any{"requests": 0, "windowSeconds": 10}}},
		"challenge dashboard": map[string]any{"challenge": map[string]any{"hosts": []string{"dashboard"}}},
		"challenge ip":        map[string]any{"challenge": map[string]any{"hosts": []string{"10.0.0.1"}}},
		"difficulty":          map[string]any{"challenge": map[string]any{"difficulty": 40}},
	} {
		if code, out, _ := e.call(e.owner, "PUT", "/v1/protect", body); code != 422 {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	p := e.m.protection(e.p)
	if p.Challenge == nil || p.Challenge.Difficulty != 16 || len(p.Challenge.Secret) != 32 || p.Auth.Events != 5 || p.Dashboard.Events != 1200 {
		t.Fatalf("edge protection: %+v", p)
	}

	// Scoped and read-only tokens cannot change box-wide protection.
	reader := e.token([]string{"read"}, []string{"*"})
	scoped := e.token([]string{"apply:reversible"}, []string{"shop"})
	if code, _, _ := e.call(reader, "GET", "/v1/protect", nil); code != 200 {
		t.Errorf("reader status: %d", code)
	}
	if code, _, _ := e.call(reader, "POST", "/v1/protect/under-attack", map[string]any{"on": true}); code != 403 {
		t.Errorf("reader under-attack: %d", code)
	}
	if code, _, _ := e.call(scoped, "POST", "/v1/protect/under-attack", map[string]any{"on": true}); code != 403 {
		t.Errorf("single-project token under-attack: %d", code)
	}

	// Under attack: challenge everywhere, tighter limits, then it expires.
	agent := e.token([]string{"apply:reversible"}, []string{"*"})
	code, st, _ = e.call(agent, "POST", "/v1/protect/under-attack", map[string]any{"on": true, "minutes": 30})
	if code != 200 || dig(st, "underAttack", "on") != true || dig(st, "underAttack", "minutesLeft") != float64(30) || dig(st, "underAttack", "by") != "agent" {
		t.Fatalf("under attack: %d %v", code, st)
	}
	p = e.m.protection(e.p)
	if p.Challenge == nil || p.Challenge.Hosts[0] != "*" || p.Challenge.Difficulty != 18 || p.App.Events != 60 || p.Auth.Events != 5 || p.Dashboard.Events != 1200 {
		t.Fatalf("under-attack protection: %+v %+v", p, p.Challenge)
	}
	if !strings.Contains(st["summary"].(string), "UNDER ATTACK") {
		t.Errorf("summary: %v", st["summary"])
	}
	e.clock = e.clock.Add(31 * time.Minute)
	e.m.expire(t.Context(), e.p)
	_, st, _ = e.call(e.owner, "GET", "/v1/protect", nil)
	if dig(st, "underAttack", "on") != false {
		t.Fatalf("after expiry: %v", st["underAttack"])
	}
	if p := e.m.protection(e.p); p.App.Events != 300 || p.Challenge.Hosts[0] != "shop.tiffin.localhost" {
		t.Errorf("after expiry: %+v", p)
	}
	// Off explicitly.
	e.call(agent, "POST", "/v1/protect/under-attack", map[string]any{"on": true})
	if code, st, _ := e.call(agent, "POST", "/v1/protect/under-attack", map[string]any{"on": false}); code != 200 || dig(st, "underAttack", "on") != false {
		t.Errorf("off: %d %v", code, st)
	}

	// Bans.
	if code, _, arr := e.call(agent, "POST", "/v1/protect/bans", map[string]any{"ip": "203.0.113.7", "duration": "30m"}); code != 200 || len(arr) != 1 {
		t.Errorf("ban: %d %v", code, arr)
	}
	for _, bad := range []map[string]any{{"ip": "127.0.0.1"}, {"ip": "203.0.113.7", "duration": "forever"}, {"ip": "nope"}} {
		if code, out, _ := e.call(agent, "POST", "/v1/protect/bans", bad); code != 422 {
			t.Errorf("ban %v: %d %v", bad, code, out)
		}
	}
	if code, _, arr := e.call(reader, "GET", "/v1/protect/decisions", nil); code != 200 || len(arr) != 1 {
		t.Errorf("decisions: %d %v", code, arr)
	}
	if code, out, _ := e.call(agent, "POST", "/v1/protect/unban", map[string]any{"ip": "203.0.113.7"}); code != 200 || out["deleted"] != float64(1) {
		t.Errorf("unban: %d %v", code, out)
	}
	if code, out, _ := e.call(agent, "POST", "/v1/protect/unban", map[string]any{"ip": "203.0.113.7"}); code != 200 || out["deleted"] != float64(0) {
		t.Errorf("second unban: %d %v", code, out)
	}

	// Settings survive a restart (the KV is the source of truth).
	*e.m = Module{cs: e.m.cs, now: e.m.now}
	if err := e.m.load(t.Context(), e.p); err != nil {
		t.Fatal(err)
	}
	if s, _ := e.m.snapshot(); s.Limits.Auth.Requests != 5 || len(s.Challenge.Hosts) != 2 {
		t.Errorf("reloaded settings: %+v", s)
	}

	// Audit trail.
	ev, _ := e.p.DB.AuditLog(t.Context(), 50)
	var actions []string
	for _, x := range ev {
		actions = append(actions, x.Action)
	}
	for _, want := range []string{"protect.update", "protect.under-attack.on", "protect.under-attack.expired", "protect.ban", "protect.unban"} {
		if !strings.Contains(strings.Join(actions, " "), want) {
			t.Errorf("audit lacks %s: %v", want, actions)
		}
	}
}
