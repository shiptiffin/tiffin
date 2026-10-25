package search

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// Status is how a project's apps stand with search engines.
type Status struct {
	Project  string       `json:"project"`
	Settings Settings     `json:"settings"`
	Hosts    []HostStatus `json:"hosts" doc:"Every production address of the project's apps"`
}

// HostStatus is one production address.
type HostStatus struct {
	Host     string `json:"host"`
	App      string `json:"app,omitempty" doc:"The app at the address's home page"`
	Findable bool   `json:"findable" doc:"Search engines may index it, and the box tells them about its pages"`
	Why      string `json:"why,omitempty" doc:"Why it is not findable"`
	Fix      string `json:"fix,omitempty"`
	// Since the address became findable.
	Since   *time.Time `json:"since,omitempty"`
	Checked *time.Time `json:"checked,omitempty" doc:"When the box last read its robots.txt, home page and sitemap"`
	Checks  *Checks    `json:"checks,omitempty"`
	Last    *Ping      `json:"last,omitempty" doc:"The last time the box told search engines about its pages"`
	History []Ping     `json:"history,omitempty" doc:"Earlier pings, newest first"`
	Pending *Pending   `json:"pending,omitempty" doc:"A check that waits (after a deploy the box waits a couple of minutes, so a burst of deploys makes one ping)"`
	Next    *time.Time `json:"next,omitempty" doc:"The next regular check"`
	Crawled []Crawler  `json:"crawled,omitempty" doc:"Search and AI bots that fetched its pages (last 30 days), most recent first. A bot counts only from the addresses its operator publishes."`
	Fakes7d int        `json:"fakes7d,omitempty" doc:"Requests in the last 7 days whose user agent claimed a known bot from an address outside its operator's list"`
}

// Pending is a check that waits.
type Pending struct {
	Reason string    `json:"reason" enum:"live,deploy,check,manual"`
	At     time.Time `json:"at"`
}

type projectPath struct {
	Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
}

type pingBody struct {
	Host string   `json:"host,omitempty" doc:"Only this address (default: every findable address of the project)"`
	URLs []string `json:"urls,omitempty" maxItems:"10000" doc:"Send exactly these URLs (on host) instead of what changed in the sitemap"`
	All  bool     `json:"all,omitempty" doc:"Send every page of the sitemap again, not just what changed (at most once an hour per address)"`
}

func (m *Module) RegisterAPI(a huma.API, p *platform.Platform) {
	const tag = "search"
	ready := func() error {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.p == nil {
			pb := api.NewProblem(409, "precondition", errNotReady.Error()+"; this server was started without --box")
			pb.Hint = "use a box: tiffin up"
			return pb
		}
		return nil
	}

	st := api.Op("search-status", http.MethodGet, "/v1/projects/{project}/search", "search status", api.RiskRead,
		"Show how a project's apps stand with search engines",
		"Every production address of the project's apps: whether search engines may find it (why not, and the fix: addresses on shiptiffin.app, sslip.io and local names are always hidden; "+
			"an app is found on a domain of its own), what the box saw on its last look (robots.txt, the home page, the sitemap and its page count, the IndexNow key file), "+
			"and when it last told search engines about new, changed or removed pages (IndexNow: Bing, Yandex, Seznam, Naver, Yep and others; Google reads the sitemap itself). "+
			"The box checks each findable address when it goes live, a couple of minutes after each deploy and every 6 hours.", tag)
	st.Errors = append(st.Errors, 409)
	huma.Register(a, st, api.Wrap(func(ctx context.Context, in *projectPath) (*struct{ Body Status }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
			return nil, err
		}
		if err := ready(); err != nil {
			return nil, err
		}
		s, err := m.status(ctx, in.Project)
		return &struct{ Body Status }{s}, err
	}))

	pg := api.Outbound(api.Op("search-ping", http.MethodPost, "/v1/projects/{project}/search/ping", "search ping", api.RiskWrite,
		"Check now and tell search engines what changed",
		"Reads the sitemap of the project's findable addresses now and sends IndexNow the new, changed and removed pages, as the box does after a deploy. "+
			"With urls, sends exactly those (for pages the sitemap does not date, such as ones a database changes); with all, every page again. "+
			"Rarely needed: the box does this on its own. Returns the status with the new ping.", tag))
	pg.Errors = append(pg.Errors, 404, 409, 422, 429)
	huma.Register(a, pg, api.Wrap(func(ctx context.Context, in *struct {
		projectPath
		Body pingBody
	}) (*struct{ Body Status }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeApplyOutbound, in.Project); err != nil {
			return nil, err
		}
		if err := ready(); err != nil {
			return nil, err
		}
		views, err := m.hosts(ctx)
		if err != nil {
			return nil, err
		}
		host := strings.ToLower(strings.TrimSpace(in.Body.Host))
		var targets []string
		for _, v := range views {
			if v.Project == in.Project && v.Findable && (host == "" || v.Host == host) {
				targets = append(targets, v.Host)
			}
		}
		if len(targets) == 0 {
			if host != "" {
				return nil, api.NewProblem(404, "not_found", host+" is not a findable address of project "+in.Project)
			}
			pb := api.NewProblem(409, "precondition", "project "+in.Project+" has no findable address")
			pb.Hint = "add a domain of your own to an app: tiffin domains add " + in.Project + " --domain example.com --app web"
			return nil, pb
		}
		if len(in.Body.URLs) > 0 {
			if host == "" && len(targets) > 1 {
				return nil, api.NewProblem(422, "validation", "with urls, say which address they are on (host)")
			}
			for _, u := range in.Body.URLs {
				pu, err := url.Parse(u)
				if err != nil || pu.Scheme != "https" || !strings.EqualFold(pu.Host, targets[0]) {
					return nil, api.NewProblem(422, "validation", "urls must be https URLs on "+targets[0]+": "+u)
				}
			}
		}
		for _, h := range targets {
			if err := m.ensure(ctx, h, views); err != nil {
				return nil, err
			}
			if in.Body.All {
				if r, _ := m.record(ctx, h); r != nil {
					for _, pg := range r.History {
						if pg.Reason == ReasonManual && pg.Pages > 0 && m.now().Sub(pg.At) < manualAllGap && len(in.Body.URLs) == 0 {
							pb := api.NewProblem(429, "rate_limited", "every page of "+h+" was sent less than an hour ago")
							pb.Hint = "engines refuse the same URLs sent again and again; send what changed instead (leave out all)"
							return nil, pb
						}
					}
				}
			}
			if _, err := m.check(ctx, h, pingOpts{reason: ReasonManual, urls: in.Body.URLs, all: in.Body.All}); err != nil {
				return nil, err
			}
		}
		pr := api.PrincipalFrom(ctx)
		_ = p.DB.Audit(ctx, pr.TokenID, "search.ping", in.Project, map[string]any{"session": pr.Session, "hosts": targets, "urls": len(in.Body.URLs), "all": in.Body.All})
		s, err := m.status(ctx, in.Project)
		return &struct{ Body Status }{s}, err
	}))

	set := api.Op("search-set", http.MethodPut, "/v1/projects/{project}/search/settings", "search set", api.RiskWrite,
		"Turn IndexNow pings on or off for a project",
		"indexNow: false stops the box from telling search engines about the project's pages (the checks go on). On by default. "+
			"It does not hide the apps: search engines still find them through links and sitemaps.", tag)
	set.Errors = append(set.Errors, 409)
	huma.Register(a, set, api.Wrap(func(ctx context.Context, in *struct {
		projectPath
		Body Settings
	}) (*struct{ Body Status }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		if err := ready(); err != nil {
			return nil, err
		}
		if err := m.saveSettings(ctx, in.Project, in.Body); err != nil {
			return nil, err
		}
		pr := api.PrincipalFrom(ctx)
		_ = p.DB.Audit(ctx, pr.TokenID, "search.set", in.Project, map[string]any{"session": pr.Session, "indexNow": in.Body.IndexNow})
		s, err := m.status(ctx, in.Project)
		return &struct{ Body Status }{s}, err
	}))
}

// ensure makes sure a findable host has a record (a manual ping before the
// loop's first pass).
func (m *Module) ensure(ctx context.Context, host string, views []hostView) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, err := m.record(ctx, host); err != nil || r != nil {
		return err
	}
	for _, v := range views {
		if v.Host == host {
			now := m.now()
			return m.saveRecord(ctx, &hostRecord{Host: host, Project: v.Project, App: v.App, Deploys: v.deploys, Since: now, Reason: ReasonLive})
		}
	}
	return nil
}

func (m *Module) status(ctx context.Context, project string) (Status, error) {
	s := Status{Project: project, Settings: m.settings(ctx, project), Hosts: []HostStatus{}}
	views, err := m.hosts(ctx)
	if err != nil {
		return s, err
	}
	local := !m.acme()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, v := range views {
		if v.Project != project {
			continue
		}
		h := HostStatus{Host: v.Host, App: v.App, Findable: v.Findable && !local, Why: v.Why, Fix: v.Fix}
		if local && v.Findable {
			h.Why = "This is a local box: nothing outside it can reach its apps."
		}
		if h.Findable {
			if r, _ := m.record(ctx, v.Host); r != nil {
				since := r.Since
				h.Since = &since
				if !r.Checked.IsZero() {
					c, cs := r.Checked, r.Checks
					h.Checked, h.Checks = &c, &cs
					next := r.Checked.Add(CheckEvery)
					h.Next = &next
				}
				h.Last, h.History = r.Last, r.History
				if len(h.History) > 1 {
					h.History = h.History[1:]
				} else {
					h.History = nil
				}
				if !r.Due.IsZero() {
					h.Pending = &Pending{Reason: r.Reason, At: r.Due}
				}
			}
			h.Crawled, h.Fakes7d = m.crawled(ctx, v.Host, m.now())
		}
		s.Hosts = append(s.Hosts, h)
	}
	return s, nil
}
