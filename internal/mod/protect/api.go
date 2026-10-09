package protect

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/edge"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// Provision installs the firewall baseline and CrowdSec.
func (m *Module) Provision(ctx context.Context, s *platform.System) error {
	if err := provisionFirewall(ctx, s); err != nil {
		return err
	}
	if err := provisionCrowdSec(ctx, s); err != nil {
		return err
	}
	srv, err := platform.LoadServerConfig()
	if err != nil || srv == nil {
		return err
	}
	return provisionServerProtection(ctx, s, srv)
}

func (m *Module) crowdsec() crowdsec {
	if m.cs != nil {
		return m.cs
	}
	return cscliCrowdSec{}
}

// Status is everything the dashboard shows about protection.
type Status struct {
	Summary     string        `json:"summary" doc:"One line in plain words."`
	UnderAttack AttackState   `json:"underAttack"`
	Effective   Effective     `json:"effective" doc:"What the edge enforces right now (settings plus the under-attack switch)."`
	Settings    Settings      `json:"settings"`
	Edge        EdgeState     `json:"edge"`
	CrowdSec    CrowdSecState `json:"crowdsec"`
	Firewall    FirewallState `json:"firewall"`
}

// AttackState is the switch with the time left.
type AttackState struct {
	UnderAttack
	MinutesLeft int `json:"minutesLeft"`
}

// EdgeState says whether the edge runs the protection layer.
type EdgeState struct {
	Applied bool   `json:"applied"`
	Error   string `json:"error,omitempty" doc:"Why the edge is serving without protection, if it is."`
}

// CrowdSecState is the CrowdSec engine's state.
type CrowdSecState struct {
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	Detecting bool   `json:"detecting" doc:"Its parsers for the edge's access log are in place."`
	Enforced  bool   `json:"enforced" doc:"The edge has a bouncer key and blocks banned IPs."`
	Decisions int    `json:"decisions" doc:"Active bans."`
	Detail    string `json:"detail"`
}

var (
	countMu    sync.Mutex
	countAt    time.Time
	countValue int
	countErr   error
)

// decisionCount caches the active-ban count for a few seconds (cscli is slow-ish).
func (m *Module) decisionCount(ctx context.Context) (int, error) {
	countMu.Lock()
	defer countMu.Unlock()
	if time.Since(countAt) < 10*time.Second {
		return countValue, countErr
	}
	// Detached from the caller: a dashboard poll that is cancelled mid-way
	// must not leave a cached "unreadable" for everyone else.
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	ds, err := m.crowdsec().Decisions(dctx)
	if err != nil && ctx.Err() != nil {
		return 0, err // the caller went away; don't cache that
	}
	countAt, countValue, countErr = time.Now(), len(ds), err
	return countValue, countErr
}

func forgetCount() {
	countMu.Lock()
	countAt = time.Time{}
	countMu.Unlock()
}

func (m *Module) status(ctx context.Context, p *platform.Platform) Status {
	s, ua := m.snapshot()
	now := m.clock()
	st := Status{Settings: s, Effective: m.effective(p), UnderAttack: AttackState{UnderAttack: ua}}
	if ua.active(now) {
		st.UnderAttack.MinutesLeft = int(ua.Until.Sub(now).Round(time.Minute) / time.Minute)
	} else {
		st.UnderAttack = AttackState{}
	}
	if p != nil && p.Edge != nil {
		active, err := edge.ProtectionStatus()
		st.Edge.Applied = active != nil && err == nil
		if err != nil {
			st.Edge.Error = err.Error()
		}
	}
	cs := m.crowdsec()
	st.CrowdSec.Installed = cs.Installed()
	if st.CrowdSec.Installed {
		st.CrowdSec.Running = cs.Running(ctx)
		st.CrowdSec.Enforced = st.Effective.CrowdSec && st.Edge.Applied
		n, err := m.decisionCount(ctx)
		st.CrowdSec.Decisions = n
		perr := cs.Pipeline()
		st.CrowdSec.Detecting = st.CrowdSec.Running && perr == nil
		switch {
		case !st.CrowdSec.Running:
			st.CrowdSec.Detail = "CrowdSec is not running; banned IPs from before stay blocked, new attacks are not detected. Try `sudo systemctl restart crowdsec`."
		case err != nil:
			st.CrowdSec.Detail = "running, but its decisions could not be read: " + err.Error()
		case perr != nil:
			st.CrowdSec.Detail = "running, but it cannot read the edge's log: " + perr.Error()
		case !st.CrowdSec.Enforced:
			st.CrowdSec.Detail = fmt.Sprintf("running with %d active ban(s), but the edge is not enforcing them (no bouncer key; run `tiffin provision`)", n)
		default:
			st.CrowdSec.Detail = fmt.Sprintf("running; %d IP ban(s) active", n)
		}
	} else {
		st.CrowdSec.Detail = "not installed on this machine"
	}
	st.Firewall = firewallState(ctx)
	st.Summary = summary(st)
	return st
}

func summary(st Status) string {
	var parts []string
	if st.UnderAttack.On {
		parts = append(parts, fmt.Sprintf("UNDER ATTACK mode for %d more min: every app host is challenged and limits are tighter", st.UnderAttack.MinutesLeft))
	} else {
		parts = append(parts, "normal mode")
	}
	e := st.Effective
	parts = append(parts, fmt.Sprintf("app limit %s, sign-in limit %s", e.Limits.App, e.Limits.Auth))
	switch {
	case st.UnderAttack.On:
	case len(e.ChallengeHosts) == 0:
		parts = append(parts, "challenge off")
	default:
		parts = append(parts, "challenge on "+strings.Join(e.ChallengeHosts, ", "))
	}
	if e.WAF {
		parts = append(parts, "WAF on")
	}
	if st.CrowdSec.Installed {
		parts = append(parts, fmt.Sprintf("CrowdSec %d ban(s)", st.CrowdSec.Decisions))
	}
	if st.Edge.Error != "" {
		parts = append(parts, "WARNING: edge serving without protection: "+st.Edge.Error)
	}
	return strings.Join(parts, "; ")
}

func (l Limit) String() string {
	if l.Requests == 0 {
		return "off"
	}
	return fmt.Sprintf("%d per %ds", l.Requests, l.WindowSeconds)
}

// requireBox lets read through for any token; changes need apply:reversible
// on every project, since protection is box-wide.
func requireBox(ctx context.Context, write bool) error {
	p := api.PrincipalFrom(ctx)
	if !write {
		return p.Require(tokens.ScopeRead, "")
	}
	if err := p.Require(tokens.ScopeApplyReversible, ""); err != nil {
		return err
	}
	if !p.CanProject("*") {
		return api.NewProblem(http.StatusForbidden, "forbidden", "protection is box-wide; this needs a token for all projects")
	}
	return nil
}

func actor(ctx context.Context) string {
	if p := api.PrincipalFrom(ctx); p != nil {
		return p.Name
	}
	return "unknown"
}

// apply reloads the edge so a change takes effect, and returns the status.
func (m *Module) apply(ctx context.Context, p *platform.Platform) (*Status, error) {
	if err := p.RefreshRoutes(ctx); err != nil {
		return nil, api.NewProblem(http.StatusInternalServerError, "internal", "the setting was saved but the edge did not reload: "+err.Error())
	}
	st := m.status(ctx, p)
	return &st, nil
}

type settingsPatch struct {
	Limits *struct {
		App       *Limit `json:"app,omitempty"`
		Auth      *Limit `json:"auth,omitempty"`
		Dashboard *Limit `json:"dashboard,omitempty"`
	} `json:"limits,omitempty" doc:"Per-IP limits; omitted zones keep their value."`
	Challenge *struct {
		Hosts       *[]string `json:"hosts,omitempty" doc:"Hosts that always get the challenge (full names or first-level names; \"*\" for every app host; [] for none)."`
		Difficulty  *int      `json:"difficulty,omitempty" minimum:"8" maximum:"24"`
		ExemptPaths *[]string `json:"exemptPaths,omitempty" doc:"Path prefixes API clients call (\"/api/v1/\"): never challenged, still rate limited ([] for none)."`
	} `json:"challenge,omitempty"`
	UnderAttack *struct {
		App        *Limit `json:"app,omitempty"`
		Auth       *Limit `json:"auth,omitempty"`
		Difficulty *int   `json:"difficulty,omitempty" minimum:"8" maximum:"24"`
	} `json:"underAttack,omitempty" doc:"What the under-attack switch tightens to."`
	WAF   *bool `json:"waf,omitempty" doc:"Turn the Coraza WAF (OWASP core rule set) on or off for every app host."`
	Reset bool  `json:"reset,omitempty" doc:"Start from the defaults before applying the other fields."`
}

func (m *Module) patch(p *platform.Platform, cur Settings, in settingsPatch) (Settings, error) {
	s := cur
	if in.Reset {
		s = DefaultSettings()
	}
	if l := in.Limits; l != nil {
		for _, x := range []struct {
			src *Limit
			dst *Limit
		}{{l.App, &s.Limits.App}, {l.Auth, &s.Limits.Auth}, {l.Dashboard, &s.Limits.Dashboard}} {
			if x.src != nil {
				*x.dst = *x.src
			}
		}
	}
	if c := in.Challenge; c != nil {
		if c.Hosts != nil {
			hosts := []string{}
			for _, h := range *c.Hosts {
				h = fullHost(p, h)
				if h == "" {
					continue
				}
				if h != "*" {
					if _, err := netip.ParseAddr(h); err == nil || strings.ContainsAny(h, " /:*") {
						return s, api.NewProblem(422, "validation", fmt.Sprintf("challenge host %q is not a host name", h))
					}
				}
				if p != nil && h == p.DashboardHost() {
					return s, api.NewProblem(422, "validation", "the dashboard is never challenged; it has its own sign-in")
				}
				if !slices.Contains(hosts, h) {
					hosts = append(hosts, h)
				}
			}
			s.Challenge.Hosts = hosts
		}
		if c.Difficulty != nil {
			s.Challenge.Difficulty = *c.Difficulty
		}
		if c.ExemptPaths != nil {
			paths := []string{}
			for _, x := range *c.ExemptPaths {
				x = strings.TrimSpace(x)
				if !strings.HasPrefix(x, "/") || x == "/" || strings.ContainsAny(x, " ?#*") {
					return s, api.NewProblem(422, "validation", fmt.Sprintf("challenge exempt path %q: want a path prefix such as /api/v1/ (not / itself)", x))
				}
				if !slices.Contains(paths, x) {
					paths = append(paths, x)
				}
			}
			s.Challenge.ExemptPaths = paths
		}
	}
	if u := in.UnderAttack; u != nil {
		if u.App != nil {
			s.UnderAttack.Limits.App = *u.App
		}
		if u.Auth != nil {
			s.UnderAttack.Limits.Auth = *u.Auth
		}
		if u.Difficulty != nil {
			s.UnderAttack.Difficulty = *u.Difficulty
		}
	}
	if in.WAF != nil {
		s.WAF = *in.WAF
	}
	d := s.Limits.Dashboard
	if d.Requests == 0 || float64(d.Requests)/float64(d.WindowSeconds) < 5 {
		p := api.NewProblem(422, "validation", fmt.Sprintf("the dashboard limit %s is too low: it must allow at least 5 requests per second so you are never locked out of your box", d))
		p.Hint = "use e.g. {\"requests\": 1200, \"windowSeconds\": 10}"
		return s, p
	}
	for _, l := range []Limit{s.Limits.App, s.Limits.Auth, s.Limits.Dashboard, s.UnderAttack.Limits.App, s.UnderAttack.Limits.Auth} {
		if l.Requests < 0 || l.WindowSeconds < 1 || l.WindowSeconds > 3600 {
			return s, api.NewProblem(422, "validation", "limits need requests >= 0 and a window of 1–3600 seconds")
		}
	}
	for _, d := range []int{s.Challenge.Difficulty, s.UnderAttack.Difficulty} {
		if d < 8 || d > 24 {
			return s, api.NewProblem(422, "validation", "challenge difficulty must be 8–24 bits")
		}
	}
	return s, nil
}

type banBody struct {
	IP       string `json:"ip" minLength:"2" maxLength:"64" doc:"An IP address (203.0.113.7) or a range (203.0.113.0/24)."`
	Duration string `json:"duration,omitempty" doc:"How long, e.g. 30m, 4h, 168h. Default 4h; at most 8760h (a year)."`
	Reason   string `json:"reason,omitempty" maxLength:"200" doc:"Why, shown in the decisions list."`
}

// checkBanTarget refuses targets that would cut the box off from itself.
func checkBanTarget(target string) (netip.Prefix, error) {
	var pfx netip.Prefix
	if a, err := netip.ParseAddr(target); err == nil {
		a = a.Unmap()
		pfx = netip.PrefixFrom(a, a.BitLen())
	} else if p, err := netip.ParsePrefix(target); err == nil {
		pfx = p.Masked()
	} else {
		return pfx, api.NewProblem(422, "validation", fmt.Sprintf("%q is not an IP address or range", target))
	}
	if (pfx.Addr().Is4() && pfx.Bits() < 8) || (pfx.Addr().Is6() && pfx.Bits() < 16) {
		return pfx, api.NewProblem(422, "validation", "that range is too wide to ban (at most /8 for IPv4, /16 for IPv6)")
	}
	for _, guard := range []string{"127.0.0.0/8", "::1/128", "0.0.0.0/32", "::/128"} {
		g := netip.MustParsePrefix(guard)
		if g.Overlaps(pfx) {
			return pfx, api.NewProblem(422, "validation", "banning loopback would cut the box off from itself (and from you on a local box)")
		}
	}
	return pfx, nil
}

// RegisterAPI adds the protection operations.
func (m *Module) RegisterAPI(a huma.API, p *platform.Platform) {
	ready := func(ctx context.Context) error {
		if p == nil || p.DB == nil {
			return api.NewProblem(http.StatusPreconditionFailed, "precondition", "protection runs on the box only")
		}
		return m.load(ctx, p)
	}

	huma.Register(a, api.Op("protect-get", http.MethodGet, "/v1/protect", "protect status", api.RiskRead,
		"Show protection status",
		"Rate limits, the proof-of-work challenge, the under-attack switch, the WAF, CrowdSec (bans) and the firewall: settings, what the edge enforces right now and their health. underAttack.on is what the dashboard shows as the alarm state.",
		"protect"),
		api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body Status }, error) {
			if err := requireBox(ctx, false); err != nil {
				return nil, err
			}
			if err := ready(ctx); err != nil {
				return nil, err
			}
			return &struct{ Body Status }{m.status(ctx, p)}, nil
		}))

	huma.Register(a, api.Op("protect-update", http.MethodPut, "/v1/protect", "protect set", api.RiskWrite,
		"Change protection settings",
		"Change per-IP rate limits (app, auth, dashboard), which hosts always get the challenge and its difficulty, what under-attack tightens to, and the WAF. Omitted fields keep their value; reset:true starts from the defaults. Takes effect at once (the edge reloads). Needs full access to all projects.",
		"protect"),
		api.Wrap(func(ctx context.Context, in *struct{ Body settingsPatch }) (*struct{ Body Status }, error) {
			if err := requireBox(ctx, true); err != nil {
				return nil, err
			}
			if err := ready(ctx); err != nil {
				return nil, err
			}
			cur, _ := m.snapshot()
			next, err := m.patch(p, cur, in.Body)
			if err != nil {
				return nil, err
			}
			if err := m.saveSettings(ctx, p, next); err != nil {
				return nil, err
			}
			_ = p.DB.Audit(ctx, actor(ctx), "protect.update", "box", next)
			st, err := m.apply(ctx, p)
			if err != nil {
				return nil, err
			}
			return &struct{ Body Status }{*st}, nil
		}))

	huma.Register(a, api.Op("protect-under-attack", http.MethodPost, "/v1/protect/under-attack", "protect under-attack", api.RiskWrite,
		"Turn under-attack mode on or off",
		"On: every app host gets the proof-of-work challenge and the app and sign-in limits tighten, for `minutes` (default 60, at most 1440) or until turned off. The dashboard is never challenged. Requests are challenged whatever headers they carry: API clients that cannot run the check need their paths in challenge.exemptPaths (still rate limited).",
		"protect"),
		api.Wrap(func(ctx context.Context, in *struct {
			Body struct {
				On      bool `json:"on" doc:"true to turn it on, false to turn it off."`
				Minutes int  `json:"minutes,omitempty" minimum:"0" maximum:"1440" doc:"How long before it turns itself off. Default 60."`
			}
		}) (*struct{ Body Status }, error) {
			if err := requireBox(ctx, true); err != nil {
				return nil, err
			}
			if err := ready(ctx); err != nil {
				return nil, err
			}
			ua := UnderAttack{}
			if in.Body.On {
				mins := in.Body.Minutes
				if mins == 0 {
					mins = 60
				}
				now := m.clock()
				ua = UnderAttack{On: true, Since: now, Until: now.Add(time.Duration(mins) * time.Minute), By: actor(ctx)}
				if _, cur := m.snapshot(); cur.active(now) {
					ua.Since = cur.Since
				}
			}
			if err := m.saveAttack(ctx, p, ua); err != nil {
				return nil, err
			}
			action := "protect.under-attack.off"
			if ua.On {
				action = "protect.under-attack.on"
			}
			_ = p.DB.Audit(ctx, actor(ctx), action, "box", ua)
			st, err := m.apply(ctx, p)
			if err != nil {
				return nil, err
			}
			return &struct{ Body Status }{*st}, nil
		}))

	huma.Register(a, api.Untrusted(api.Op("protect-decisions", http.MethodGet, "/v1/protect/decisions", "protect decisions", api.RiskRead,
		"List banned IPs",
		"Active CrowdSec decisions: IPs and ranges the edge answers with 403, why, and for how long. Scenario-made bans come from the edge's access log (attacker-controlled data).",
		"protect")),
		api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body []Decision }, error) {
			if err := requireBox(ctx, false); err != nil {
				return nil, err
			}
			ds, err := m.crowdsec().Decisions(ctx)
			if err != nil {
				return nil, csProblem(err)
			}
			return &struct{ Body []Decision }{ds}, nil
		}))

	huma.Register(a, api.Untrusted(api.Op("protect-alerts", http.MethodGet, "/v1/protect/alerts", "protect alerts", api.RiskRead,
		"List recent attacks CrowdSec noticed",
		"Recent CrowdSec alerts, newest first: the scenario (e.g. tiffin/http-auth-bruteforce, crowdsecurity/http-probing), the source IP, how many events and whether it led to a ban.",
		"protect")),
		api.Wrap(func(ctx context.Context, in *struct {
			Limit int `query:"limit" minimum:"1" maximum:"500" default:"50" doc:"How many alerts."`
		}) (*struct{ Body []Alert }, error) {
			if err := requireBox(ctx, false); err != nil {
				return nil, err
			}
			al, err := m.crowdsec().Alerts(ctx, in.Limit)
			if err != nil {
				return nil, csProblem(err)
			}
			return &struct{ Body []Alert }{al}, nil
		}))

	huma.Register(a, api.Op("protect-ban", http.MethodPost, "/v1/protect/bans", "protect ban", api.RiskWrite,
		"Ban an IP or range",
		"Adds a CrowdSec ban: the edge answers that IP or range with 403 on every host (the dashboard too) within a few seconds. Loopback cannot be banned.",
		"protect"),
		api.Wrap(func(ctx context.Context, in *struct{ Body banBody }) (*struct{ Body []Decision }, error) {
			if err := requireBox(ctx, true); err != nil {
				return nil, err
			}
			pfx, err := checkBanTarget(strings.TrimSpace(in.Body.IP))
			if err != nil {
				return nil, err
			}
			d := 4 * time.Hour
			if in.Body.Duration != "" {
				if d, err = time.ParseDuration(in.Body.Duration); err != nil || d < time.Minute || d > 8760*time.Hour {
					return nil, api.NewProblem(422, "validation", "duration must look like 30m or 4h, between 1m and 8760h")
				}
			}
			reason := strings.TrimSpace(in.Body.Reason)
			if reason == "" {
				reason = "banned by " + actor(ctx) + " via tiffin"
			}
			target := pfx.String()
			if pfx.IsSingleIP() {
				target = pfx.Addr().String()
			}
			if err := m.crowdsec().Ban(ctx, target, d, reason); err != nil {
				return nil, csProblem(err)
			}
			forgetCount()
			if p != nil && p.DB != nil {
				_ = p.DB.Audit(ctx, actor(ctx), "protect.ban", target, map[string]any{"duration": d.String(), "reason": reason})
			}
			ds, err := m.crowdsec().Decisions(ctx)
			if err != nil {
				return nil, csProblem(err)
			}
			out := []Decision{}
			for _, x := range ds {
				if x.Value == target {
					out = append(out, x)
				}
			}
			return &struct{ Body []Decision }{out}, nil
		}))

	huma.Register(a, api.Op("protect-unban", http.MethodPost, "/v1/protect/unban", "protect unban", api.RiskWrite,
		"Lift bans on an IP or range",
		"Deletes every CrowdSec decision for exactly that IP or range (manual or automatic). The edge lets it through again within a few seconds.",
		"protect"),
		api.Wrap(func(ctx context.Context, in *struct {
			Body struct {
				IP string `json:"ip" minLength:"2" maxLength:"64" doc:"The IP address or range, as listed by protect decisions."`
			}
		}) (*struct {
			Body struct {
				Deleted int    `json:"deleted"`
				Summary string `json:"summary"`
			}
		}, error) {
			if err := requireBox(ctx, true); err != nil {
				return nil, err
			}
			target := strings.TrimSpace(in.Body.IP)
			if _, err := targetArgs(target); err != nil {
				return nil, api.NewProblem(422, "validation", err.Error())
			}
			n, err := m.crowdsec().Unban(ctx, target)
			if err != nil {
				return nil, csProblem(err)
			}
			forgetCount()
			if p != nil && p.DB != nil {
				_ = p.DB.Audit(ctx, actor(ctx), "protect.unban", target, map[string]any{"deleted": n})
			}
			out := &struct {
				Body struct {
					Deleted int    `json:"deleted"`
					Summary string `json:"summary"`
				}
			}{}
			out.Body.Deleted = n
			out.Body.Summary = fmt.Sprintf("%d decision(s) for %s deleted", n, target)
			if n == 0 {
				out.Body.Summary = "no active decision for " + target + "; nothing to lift"
			}
			return out, nil
		}))
}

func csProblem(err error) error {
	if errors.Is(err, ErrNotInstalled) {
		p := api.NewProblem(http.StatusPreconditionFailed, "precondition", err.Error())
		p.Hint = "CrowdSec is installed on the box by `tiffin provision` (run by `tiffin up`)"
		return p
	}
	return api.NewProblem(http.StatusInternalServerError, "internal", err.Error())
}
