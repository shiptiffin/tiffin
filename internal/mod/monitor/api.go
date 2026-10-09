package monitor

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// Monitor is the outside check as the API shows it.
type Monitor struct {
	On               bool       `json:"on" doc:"The box pings an outside URL about once a minute"`
	URL              string     `json:"url,omitempty" doc:"Where the pings go. Treat it as a secret: anyone with it can send pings."`
	Receiver         string     `json:"receiver,omitempty" enum:"healthchecks,uptime-kuma" doc:"healthchecks: POST <url>, <url>/fail, <url>/start (healthchecks.io, tiffin watch). uptime-kuma: GET with status=up|down."`
	Details          bool       `json:"details" doc:"Pings also carry project names and what failing checks say"`
	EverySeconds     int        `json:"everySeconds"`
	FailAfterMinutes int        `json:"failAfterMinutes" doc:"Checks failing this long turn the ping into a failure ping"`
	FailingSince     *time.Time `json:"failingSince,omitempty" doc:"Since when the box's own checks have been failing"`
	Last             *Beat      `json:"last,omitempty" doc:"The last ping sent since Tiffin started"`
	SetAt            *time.Time `json:"setAt,omitempty"`
	Payload          Payload    `json:"payload" doc:"What a ping carries right now"`
}

type setBody struct {
	URL     string `json:"url" minLength:"1" maxLength:"2000" doc:"The ping URL: a healthchecks.io check (https://hc-ping.com/<uuid>), an Uptime Kuma push URL (https://kuma.example.com/api/push/<token>) or a tiffin watch collector"`
	Details *bool  `json:"details,omitempty" doc:"Also send project names and what failing checks say (default false)"`
}

func (m *Module) RegisterAPI(a huma.API, p *platform.Platform) {
	const tag = "monitor"
	admin := func(ctx context.Context) error {
		if !api.PrincipalFrom(ctx).BoxAdmin() {
			return fmt.Errorf("%w: the outside check is the box owner's; it needs a key with full access to all projects", tokens.ErrForbidden)
		}
		if p == nil || p.DB == nil {
			pb := api.NewProblem(409, "precondition", "the outside check runs on a Tiffin box; this server was started without --box")
			pb.Hint = "use a box: tiffin up"
			return pb
		}
		return nil
	}

	sh := api.Op("monitor-show", http.MethodGet, "/v1/monitor", "monitor show", api.RiskRead,
		"Show the outside check",
		"Whether the box pings an outside URL (healthchecks.io, Uptime Kuma, tiffin watch) so someone notices when it stops, "+
			"the last ping and exactly what a ping carries. Box owner only.", tag)
	sh.Errors = append(sh.Errors, 409)
	huma.Register(a, sh, api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body Monitor }, error) {
		if err := admin(ctx); err != nil {
			return nil, err
		}
		v, err := m.view(ctx, p)
		return &struct{ Body Monitor }{v}, err
	}))

	st := api.Outbound(api.Op("monitor-set", http.MethodPut, "/v1/monitor", "monitor set", api.RiskWrite,
		"Ping an outside URL while the box is alive",
		"Sets the URL the box pings about once a minute, so a service outside the box tells you when the pings stop: "+
			"a healthchecks.io check (free tier), an Uptime Kuma push monitor or a tiffin watch collector. "+
			"A ping is sent first and the URL is kept only if it answers 2xx. When the box's own checks fail for 10 minutes, "+
			"pings become failure pings (<url>/fail; Kuma: status=down). Pings carry the version, uptime and the names of failing checks; "+
			"project names and check details only with details. Box owner only.", tag))
	st.Errors = append(st.Errors, 409)
	huma.Register(a, st, api.Wrap(func(ctx context.Context, in *struct{ Body setBody }) (*struct{ Body Monitor }, error) {
		if err := admin(ctx); err != nil {
			return nil, err
		}
		raw := strings.TrimSpace(in.Body.URL)
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, api.NewProblem(422, "validation", "url must be an http(s) URL, e.g. https://hc-ping.com/<uuid>")
		}
		cfg := Config{URL: raw, SetAt: time.Now().UTC(), SetBy: api.PrincipalFrom(ctx).Name}
		if old, _ := load(ctx, p); old != nil {
			cfg.Details = old.Details
		}
		if in.Body.Details != nil {
			cfg.Details = *in.Body.Details
		}
		m.mu.Lock()
		prev := m.last
		m.mu.Unlock()
		if b := m.ping(ctx, p, &cfg, ""); !b.OK {
			m.mu.Lock()
			m.last = prev // the kept URL's last ping, not this one
			m.mu.Unlock()
			pb := api.NewProblem(422, "validation", "the ping to "+u.Host+" failed: "+b.Error)
			pb.Hint = "check the URL (copy it again from the monitoring service) and retry; nothing was saved"
			return nil, pb
		}
		if err := save(ctx, p, &cfg); err != nil {
			return nil, err
		}
		pr := api.PrincipalFrom(ctx)
		_ = p.DB.Audit(ctx, pr.TokenID, "monitor.set", u.Host, map[string]any{"session": pr.Session, "receiver": Receiver(u), "details": cfg.Details})
		v, err := m.view(ctx, p)
		return &struct{ Body Monitor }{v}, err
	}))

	ts := api.Outbound(api.Op("monitor-test", http.MethodPost, "/v1/monitor/test", "monitor test", api.RiskWrite,
		"Send a ping now",
		"Pings the outside URL now with the box's current status and returns the outcome (last). Box owner only.", tag))
	ts.Errors = append(ts.Errors, 409)
	huma.Register(a, ts, api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body Monitor }, error) {
		if err := admin(ctx); err != nil {
			return nil, err
		}
		cfg, err := load(ctx, p)
		if err != nil {
			return nil, err
		}
		if cfg == nil {
			pb := api.NewProblem(409, "precondition", "the outside check is off")
			pb.Hint = "set a ping URL first: tiffin monitor set <url>"
			return nil, pb
		}
		m.ping(ctx, p, cfg, "")
		v, err := m.view(ctx, p)
		return &struct{ Body Monitor }{v}, err
	}))

	of := api.Op("monitor-off", http.MethodDelete, "/v1/monitor", "monitor off", api.RiskWrite,
		"Stop pinging the outside URL",
		"Forgets the ping URL; the box stops pinging. Pause or delete the check at the monitoring service too, or it will report the box down. Box owner only.", tag)
	of.Errors = append(of.Errors, 409)
	huma.Register(a, of, api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body Monitor }, error) {
		if err := admin(ctx); err != nil {
			return nil, err
		}
		old, _ := load(ctx, p)
		if err := save(ctx, p, nil); err != nil {
			return nil, err
		}
		if old != nil {
			pr := api.PrincipalFrom(ctx)
			host := ""
			if u, err := url.Parse(old.URL); err == nil {
				host = u.Host
			}
			_ = p.DB.Audit(ctx, pr.TokenID, "monitor.off", host, map[string]any{"session": pr.Session})
		}
		m.mu.Lock()
		m.last = nil
		m.mu.Unlock()
		v, err := m.view(ctx, p)
		return &struct{ Body Monitor }{v}, err
	}))
}

func (m *Module) view(ctx context.Context, p *platform.Platform) (Monitor, error) {
	cfg, err := load(ctx, p)
	if err != nil {
		return Monitor{}, err
	}
	v := Monitor{EverySeconds: int(Every / time.Second), FailAfterMinutes: int(FailAfter / time.Minute)}
	v.Payload = m.payload(ctx, p, cfg != nil && cfg.Details)
	m.mu.Lock()
	if !m.badSince.IsZero() {
		t := m.badSince.UTC()
		v.FailingSince = &t
	}
	if cfg != nil {
		v.Last = m.last
	}
	m.mu.Unlock()
	if cfg != nil {
		v.On, v.URL, v.Details, v.SetAt = true, cfg.URL, cfg.Details, &cfg.SetAt
		if u, err := url.Parse(cfg.URL); err == nil {
			v.Receiver = Receiver(u)
		}
	}
	return v, nil
}
