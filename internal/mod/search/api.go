package search

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"sort"
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
	Hidden   bool   `json:"hidden,omitempty" doc:"Kept out of search results by the project's settings (search set)"`
	// Since the address became findable.
	Since   *time.Time  `json:"since,omitempty"`
	Checked *time.Time  `json:"checked,omitempty" doc:"When the box last read its robots.txt, home page and sitemap"`
	Checks  *Checks     `json:"checks,omitempty"`
	Last    *Ping       `json:"last,omitempty" doc:"The last time the box told search engines about its pages"`
	History []Ping      `json:"history,omitempty" doc:"Earlier pings, newest first"`
	Pending *Pending    `json:"pending,omitempty" doc:"A check that waits (after a deploy the box waits a couple of minutes, so a burst of deploys makes one ping)"`
	Next    *time.Time  `json:"next,omitempty" doc:"The next regular check"`
	Crawled []Crawler   `json:"crawled,omitempty" doc:"Search and AI bots that fetched its pages (last 30 days), most recent first. A bot counts only from the addresses its operator publishes."`
	Google  *GoogleHost `json:"google,omitempty" doc:"Google Search Console: verification, the last 28 days of searches and what URL Inspection says (with a Google service account connected)"`
	Fakes7d int         `json:"fakes7d,omitempty" doc:"Requests in the last 7 days whose user agent claimed a known bot from an address outside its operator's list"`
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
			"With Google connected, it also submits the sitemap to Google again (the box submits it once by itself, when the address is verified). "+
			"With urls, sends exactly those (for pages the sitemap does not date, such as ones a database changes); with all, every page again. "+
			"Rarely needed: the box does this on its own. Returns the status with the new ping.", tag))
	pg.Errors = append(pg.Errors, 404, 409, 422, 429)
	huma.Register(a, pg, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Body    pingBody
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
			if len(in.Body.URLs) == 0 {
				if err := m.resubmitSitemap(ctx, h); err != nil {
					return nil, api.NewProblem(502, "upstream", "IndexNow is done, but Google refused the sitemap: "+err.Error())
				}
			}
		}
		pr := api.PrincipalFrom(ctx)
		_ = p.DB.Audit(ctx, pr.TokenID, "search.ping", in.Project, map[string]any{"session": pr.Session, "hosts": targets, "urls": len(in.Body.URLs), "all": in.Body.All})
		s, err := m.status(ctx, in.Project)
		return &struct{ Body Status }{s}, err
	}))

	m.registerGoogle(a, p, ready)

	set := api.Op("search-set", http.MethodPut, "/v1/projects/{project}/search/settings", "search set", api.RiskWrite,
		"Keep a project's addresses out of search results, or let them back in",
		"hidden: true keeps every address of the project out of search results; hiddenHosts does it address by address (a staging subdomain in a public project). "+
			"The apps still open for anyone with the link: their responses say X-Robots-Tag: noindex, so search engines drop them, and the box sends them no IndexNow pings and verifies nothing with Google. "+
			"The settings are replaced as a whole: send both. Apps on a domain of their own are findable by default. Names on shiptiffin.app are always kept out.", tag)
	set.Errors = append(set.Errors, 409, 422)
	huma.Register(a, set, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Body    Settings
	}) (*struct{ Body Status }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		if err := ready(); err != nil {
			return nil, err
		}
		live, err := m.liveHosts(ctx)
		if err != nil {
			return nil, err
		}
		mine := map[string]bool{}
		for _, h := range live {
			if h.Project == in.Project {
				mine[h.Host] = true
			}
		}
		set := Settings{Hidden: in.Body.Hidden}
		for _, h := range in.Body.HiddenHosts {
			h = strings.ToLower(strings.TrimSpace(h))
			if h == "" {
				continue
			}
			if !mine[h] {
				return nil, api.NewProblem(422, "validation", h+" is not an address of project "+in.Project)
			}
			if !slices.Contains(set.HiddenHosts, h) {
				set.HiddenHosts = append(set.HiddenHosts, h)
			}
		}
		sort.Strings(set.HiddenHosts)
		if err := m.saveSettings(ctx, in.Project, set); err != nil {
			return nil, err
		}
		if err := p.RefreshRoutes(ctx); err != nil {
			p.Log.Warn("search: routes", "err", err)
		}
		m.poke()
		pr := api.PrincipalFrom(ctx)
		_ = p.DB.Audit(ctx, pr.TokenID, "search.set", in.Project, map[string]any{"session": pr.Session, "hidden": set.Hidden, "hiddenHosts": set.HiddenHosts})
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
		h := HostStatus{Host: v.Host, App: v.App, Findable: v.Findable && !local, Why: v.Why, Fix: v.Fix, Hidden: v.Hidden}
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
			m.mu.Unlock()
			h.Google = m.googleHost(ctx, v.Host)
			m.mu.Lock()
		}
		s.Hosts = append(s.Hosts, h)
	}
	return s, nil
}

// GoogleConnection is the box's Google service account, as the API shows it.
type GoogleConnection struct {
	Connected   bool             `json:"connected"`
	ClientEmail string           `json:"clientEmail,omitempty" doc:"The service account the box signs in as"`
	ProjectID   string           `json:"projectId,omitempty" doc:"Its Google Cloud project"`
	Owner       string           `json:"owner,omitempty" doc:"The Google account added as an owner of each property, so they show in that account's Search Console"`
	ConnectedAt *time.Time       `json:"connectedAt,omitempty"`
	By          string           `json:"by,omitempty"`
	Error       string           `json:"error,omitempty" doc:"Why Google refuses the key lately: replace it (a new key for the same account keeps every property)"`
	ErrorAt     *time.Time       `json:"errorAt,omitempty"`
	Properties  []GoogleProperty `json:"properties"`
}

// GoogleProperty is one Search Console property the box looks after.
type GoogleProperty struct {
	Property   string     `json:"property"`
	Method     string     `json:"method" enum:"DNS_TXT,FILE" doc:"DNS_TXT: a TXT record on the domain (its zone is on a connected DNS provider; the property covers every name under it). FILE: Google's verification file, served by the box on the host."`
	State      string     `json:"state" enum:"verifying,verified,failed"`
	Detail     string     `json:"detail,omitempty"`
	VerifiedAt *time.Time `json:"verifiedAt,omitempty"`
}

type googleBody struct {
	Key   string `json:"key,omitempty" maxLength:"20000" doc:"The service account's JSON key file, as text. Leave it out to change only owner on a connected box."`
	Owner string `json:"owner,omitempty" maxLength:"320" doc:"Your Google account's email: the box adds it as an owner of each property, so it shows in your Search Console"`
}

func (m *Module) registerGoogle(a huma.API, p *platform.Platform, ready func() error) {
	const tag = "search"
	admin := func(ctx context.Context) error {
		if !api.PrincipalFrom(ctx).BoxAdmin() {
			return api.NewProblem(403, "forbidden", "the Google connection is the box owner's; it needs a key with full access to all projects")
		}
		return ready()
	}

	sh := api.Op("search-google", http.MethodGet, "/v1/search/google", "search google", api.RiskRead,
		"Show the Google Search Console connection",
		"Whether the box has a Google service account, which, and the Search Console properties it looks after with their state. Box admins only.", tag)
	sh.Errors = append(sh.Errors, 409)
	huma.Register(a, sh, api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body GoogleConnection }, error) {
		if err := admin(ctx); err != nil {
			return nil, err
		}
		v, err := m.googleView(ctx)
		return &struct{ Body GoogleConnection }{v}, err
	}))

	cn := api.Outbound(api.Op("search-google-connect", http.MethodPut, "/v1/search/google", "search google connect", api.RiskWrite,
		"Connect Google Search Console with a service account key",
		"Stores a Google service account's JSON key, encrypted with the box's key, after signing in with it. "+
			"Make it once: in a Google Cloud project turn on the Google Search Console API and the Site Verification API, create a service account (no roles), "+
			"and add a JSON key (IAM › Service accounts › Keys). The box then verifies every findable address on its own: a TXT record on the domain when its zone is on a connected DNS provider, "+
			"else Google's verification file on the host. It adds owner as an owner too, submits sitemaps and reads searches and index status twice a day. Box admins only.", tag))
	cn.Errors = append(cn.Errors, 409, 422)
	huma.Register(a, cn, api.Wrap(func(ctx context.Context, in *struct{ Body googleBody }) (*struct{ Body GoogleConnection }, error) {
		if err := admin(ctx); err != nil {
			return nil, err
		}
		owner := strings.TrimSpace(in.Body.Owner)
		if owner != "" && (!strings.Contains(owner, "@") || strings.ContainsAny(owner, " ,;")) {
			return nil, api.NewProblem(422, "validation", "owner must be one email address (your Google account)")
		}
		pr := api.PrincipalFrom(ctx)
		if strings.TrimSpace(in.Body.Key) == "" {
			c, err := m.setGoogleOwner(ctx, owner)
			if err != nil {
				return nil, err
			}
			_ = p.DB.Audit(ctx, pr.TokenID, "search.google.owner", c.ClientEmail, map[string]any{"session": pr.Session, "owner": c.Owner})
			v, err := m.googleView(ctx)
			return &struct{ Body GoogleConnection }{v}, err
		}
		c, err := m.connectGoogle(ctx, []byte(in.Body.Key), owner, pr.Name)
		if err != nil {
			pb := api.NewProblem(422, "validation", err.Error())
			pb.Hint = "make a JSON key at console.cloud.google.com › IAM › Service accounts › Keys › Add key, with the Search Console and Site Verification APIs turned on"
			return nil, pb
		}
		_ = p.DB.Audit(ctx, pr.TokenID, "search.google.connect", c.ClientEmail, map[string]any{"session": pr.Session, "project": c.ProjectID, "owner": c.Owner})
		v, err := m.googleView(ctx)
		return &struct{ Body GoogleConnection }{v}, err
	}))

	dc := api.Op("search-google-disconnect", http.MethodDelete, "/v1/search/google", "search google disconnect", api.RiskWrite,
		"Disconnect Google Search Console",
		"Deletes the service account key and what the box read from Google. The properties stay in Search Console, and the verification TXT records stay in DNS (remove them by hand if you like). Box admins only.", tag)
	dc.Errors = append(dc.Errors, 409)
	huma.Register(a, dc, api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body GoogleConnection }, error) {
		if err := admin(ctx); err != nil {
			return nil, err
		}
		if err := m.disconnectGoogle(ctx); err != nil {
			return nil, err
		}
		_ = p.RefreshRoutes(ctx)
		pr := api.PrincipalFrom(ctx)
		_ = p.DB.Audit(ctx, pr.TokenID, "search.google.disconnect", "google", map[string]any{"session": pr.Session})
		v, err := m.googleView(ctx)
		return &struct{ Body GoogleConnection }{v}, err
	}))

	ins := api.Outbound(api.Op("search-inspect", http.MethodPost, "/v1/projects/{project}/search/inspect", "search inspect", api.RiskRead,
		"Ask Google whether a page is indexed",
		"Google's URL Inspection for one page of a findable address: indexed or not and why (\"Crawled - currently not indexed\", \"Discovered - currently not indexed\", "+
			"blocked by robots.txt, noindex, a different canonical), when Google last crawled it, and the canonical it chose. Needs the Google connection and a verified property. "+
			"Google allows 2,000 a day per property.", tag))
	ins.Errors = append(ins.Errors, 404, 409, 422)
	huma.Register(a, ins, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Body    struct {
			Page string `json:"page" doc:"The page's full https URL"`
		}
	}) (*struct{ Body GInspection }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
			return nil, err
		}
		if err := ready(); err != nil {
			return nil, err
		}
		pu, err := url.Parse(in.Body.Page)
		if err != nil || pu.Scheme != "https" || pu.Host == "" {
			return nil, api.NewProblem(422, "validation", "page must be a full https URL")
		}
		host := strings.ToLower(pu.Host)
		views, err := m.hosts(ctx)
		if err != nil {
			return nil, err
		}
		found := false
		for _, v := range views {
			if v.Host == host && v.Project == in.Project && v.Findable {
				found = true
			}
		}
		if !found {
			return nil, api.NewProblem(404, "not_found", host+" is not a findable address of project "+in.Project)
		}
		g, _, err := m.gclientFor(ctx)
		if err != nil {
			return nil, err
		}
		if g == nil {
			pb := api.NewProblem(409, "precondition", "Google Search Console is not connected on this box")
			pb.Hint = "the box owner connects it in Settings › Search engines (tiffin search google connect)"
			return nil, pb
		}
		gh := m.googleHost(ctx, host)
		if gh.State != GVerified {
			pb := api.NewProblem(409, "precondition", host+" is not verified in Search Console yet: "+gh.Detail)
			return nil, pb
		}
		r, err := g.inspect(ctx, gh.Property, in.Body.Page)
		if err != nil {
			return nil, api.NewProblem(502, "upstream", err.Error())
		}
		return &struct{ Body GInspection }{*r}, nil
	}))
}

func (m *Module) googleView(ctx context.Context) (GoogleConnection, error) {
	v := GoogleConnection{Properties: []GoogleProperty{}}
	c, err := m.googleConn(ctx)
	if err != nil || c == nil {
		return v, err
	}
	at := c.At
	v.Connected, v.ClientEmail, v.ProjectID, v.Owner, v.ConnectedAt, v.By = true, c.ClientEmail, c.ProjectID, c.Owner, &at, c.By
	if c.Error != "" {
		et := c.ErrorAt
		v.Error, v.ErrorAt = c.Error, &et
	}
	m.mu.Lock()
	props, err := m.gprops(ctx)
	m.mu.Unlock()
	if err != nil {
		return v, err
	}
	for _, gp := range props {
		x := GoogleProperty{Property: gp.Prop, Method: gp.Method, State: gp.State, Detail: gp.Detail}
		if !gp.VerifiedAt.IsZero() {
			t := gp.VerifiedAt
			x.VerifiedAt = &t
		}
		v.Properties = append(v.Properties, x)
	}
	sort.Slice(v.Properties, func(i, j int) bool { return v.Properties[i].Property < v.Properties[j].Property })
	return v, nil
}

// Checks reports the Google connection when the box has one: a refused key
// shows on the box's status page and alerts the owner.
func (m *Module) Checks(ctx context.Context, p *platform.Platform) []platform.Check {
	if m.p == nil {
		return nil
	}
	c, _ := m.googleConn(ctx)
	if c == nil {
		return nil
	}
	if c.Error != "" {
		return []platform.Check{{Name: "search.google", OK: false, Detail: c.Error + " (Settings › Search engines: replace the key)"}}
	}
	return []platform.Check{{Name: "search.google", OK: true, Detail: "signed in as " + c.ClientEmail}}
}
