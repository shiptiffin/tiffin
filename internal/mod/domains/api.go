package domains

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/dnskit"
	"github.com/btahir/tiffin/internal/edge"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
)

// ---- shapes ----

// RecordCheck is one DNS record the box needs, and what DNS says now.
type RecordCheck struct {
	dnskit.Record
	OK        bool     `json:"ok" doc:"DNS already answers this way."`
	Found     []string `json:"found" doc:"The addresses DNS gives for the name now."`
	Reason    string   `json:"reason,omitempty" doc:"What is wrong, in plain words."`
	ManagedBy string   `json:"managedBy,omitempty" doc:"The connected DNS provider that holds this record's zone (createRecords sets it); absent: add it by hand."`
}

// Previous is the box domain before the last switch.
type Previous struct {
	Domain     string     `json:"domain"`
	AppsDomain string     `json:"appsDomain,omitempty" doc:"The earlier apps domain, when it was another domain; its names keep working just as long."`
	Until      *time.Time `json:"until,omitempty" doc:"When its names stop working; absent while the new names have no certificate yet."`
}

// Wildcard is the wildcard certificate a DNS provider makes possible.
type Wildcard struct {
	Provider    string        `json:"provider"`
	Certificate edge.CertInfo `json:"certificate"`
}

// BoxDomain is the box domain's status.
type BoxDomain struct {
	Domain       string `json:"domain" doc:"The box's domain: the dashboard, API and webhooks are under it, and apps too unless appsDomain is another domain."`
	AppsDomain   string `json:"appsDomain" doc:"Apps and previews are at <project>.<appsDomain>: the box domain, or a separate one (tiffin domain set --apps-domain) so app code cannot set cookies on the dashboard's domain."`
	Source       string `json:"source" enum:"set,sslip,flag" doc:"set: chosen with tiffin domain set; sslip: automatic, from the server's public IPv4 (no DNS setup); flag: the service's --domain (a local box)."`
	Default      string `json:"default" doc:"The domain without tiffin domain set."`
	Dashboard    string `json:"dashboard" doc:"The dashboard's host."`
	DashboardURL string `json:"dashboardUrl"`
	Certificates string `json:"certificates" enum:"acme,internal" doc:"acme: publicly trusted certificates (Let's Encrypt, ZeroSSL fallback); internal: the box's own CA (a local box)."`
	CA           string `json:"ca,omitempty" doc:"The ACME directory, when it is not Let's Encrypt."`
	// State: live (the dashboard has a public certificate), issuing,
	// error, or internal (a local box with its own CA).
	State                string        `json:"state" enum:"live,issuing,error,internal"`
	PublicIPs            []string      `json:"publicIps"`
	DashboardCertificate edge.CertInfo `json:"dashboardCertificate"`
	Wildcard             *Wildcard     `json:"wildcard,omitempty" doc:"Present when a connected DNS provider holds the apps domain's zone: one *.<appsDomain> certificate covers every app and preview."`
	Records              []RecordCheck `json:"records,omitempty" doc:"For a set domain: the records it needs and whether DNS answers them."`
	Previous             *Previous     `json:"previous,omitempty" doc:"The domains before the last switch, still served for a while."`
	Restarting           bool          `json:"restarting,omitempty" doc:"The service restarts (a few seconds) to switch domains; apps keep running."`
	Summary              string        `json:"summary"`
}

// DomainCheck is what a box domain needs before tiffin domain set.
type DomainCheck struct {
	Domain     string        `json:"domain"`
	AppsDomain string        `json:"appsDomain,omitempty" doc:"The separate apps domain checked with it, if any."`
	OK         bool          `json:"ok" doc:"Every record points at this box and nothing blocks certificates."`
	Records    []RecordCheck `json:"records" doc:"Add these at your DNS host."`
	CAA        string        `json:"caa,omitempty" doc:"A CAA record that blocks the box's certificate authority, and the fix."`
	ManagedBy  string        `json:"managedBy,omitempty" doc:"The connected DNS provider that holds every record's zone: createRecords sets them all for you. Records whose own managedBy is empty must be added by hand."`
	Summary    string        `json:"summary"`
}

// Domain is one of a project's own domains.
type Domain struct {
	Domain     string  `json:"domain"`
	Project    string  `json:"project"`
	Routes     []route `json:"routes" doc:"Path prefixes and the app serving each."`
	RedirectTo string  `json:"redirectTo,omitempty" doc:"For www.<domain>: where it redirects."`
	WWW        bool    `json:"wwwRedirect" doc:"www.<domain> redirects here."`
	// State: waiting_for_dns (it does not point at the box yet; checked
	// again with backoff, from 15 s up to every 30 min), issuing (the
	// certificate is being obtained), live, or error (see reason).
	State       string          `json:"state" enum:"waiting_for_dns,issuing,live,error"`
	Reason      string          `json:"reason,omitempty"`
	Since       time.Time       `json:"since"`
	Records     []dnskit.Record `json:"records" doc:"Point the name at the box with these (all of them)."`
	Alternative []dnskit.Record `json:"alternative,omitempty" doc:"For a subdomain, instead of records: one CNAME to the box's own name."`
	Found       []string        `json:"found" doc:"The addresses DNS gives for the name now."`
	CNAME       string          `json:"cname,omitempty"`
	ManagedBy   string          `json:"managedBy,omitempty" doc:"The connected DNS provider that holds its zone (records can be created for you)."`
	Certificate *edge.CertInfo  `json:"certificate,omitempty"`
	CheckedAt   time.Time       `json:"checkedAt,omitzero"`
	NextCheckAt time.Time       `json:"nextCheckAt,omitzero"`
	URL         string          `json:"url"`
	Summary     string          `json:"summary"`
}

// DomainChange is the outcome of adding or removing a domain.
type DomainChange struct {
	Applied bool            `json:"applied" doc:"False when nothing needed to change."`
	Change  *change.Change  `json:"change,omitempty"`
	Plan    *change.Plan    `json:"plan"`
	Domain  *Domain         `json:"domain,omitempty" doc:"The domain's status and the records to add (after an add)."`
	Created []dnskit.Record `json:"created,omitempty" doc:"Records the box created through the connected DNS provider (createRecords)."`
}

// ---- box domain ----

func (m *Module) boxStatus(ctx context.Context, p *platform.Platform) BoxDomain {
	r := p.Reach
	apps := p.AppsDomain()
	b := BoxDomain{Domain: p.Domain, AppsDomain: apps, Source: orDefault(r.DomainSource, "flag"), Default: orDefault(r.DefaultDomain, p.Domain),
		Dashboard: p.DashboardHost(), DashboardURL: strings.TrimRight(p.PublicURL, "/"), PublicIPs: []string{}}
	if b.DashboardURL == "" {
		b.DashboardURL = p.URL(p.DashboardHost())
	}
	for _, a := range r.PublicIPs {
		b.PublicIPs = append(b.PublicIPs, a.String())
	}
	m.mu.Lock()
	box := m.box
	aliases := m.aliasesLocked()
	wild := m.wildcardLocked()
	m.mu.Unlock()
	if len(aliases) > 0 {
		b.Previous = &Previous{Domain: aliases[0]}
		if len(aliases) > 1 {
			b.Previous.AppsDomain = aliases[1]
		}
		if !box.PreviousUntil.IsZero() {
			u := box.PreviousUntil
			b.Previous.Until = &u
		}
	}
	if !r.ACME {
		b.Certificates, b.State = "internal", "internal"
		b.Summary = fmt.Sprintf("A local box: %s and <project>.%s use the box's own certificate authority (trusted by your computer after tiffin up). Real domains and public certificates need a server.", p.DashboardHost(), apps)
		return b
	}
	b.Certificates = "acme"
	if r.ACMEDirectory != "" && r.ACMEDirectory != edge.LetsEncrypt {
		b.CA = r.ACMEDirectory
	}
	b.DashboardCertificate = edge.CertStatus(p.DashboardHost())
	switch b.DashboardCertificate.State {
	case "live":
		b.State = "live"
	case "error":
		b.State = "error"
	default:
		b.State = "issuing"
	}
	if wild != nil && r.ACME {
		// Any one-label name finds the wildcard certificate.
		b.Wildcard = &Wildcard{Provider: wild.Name, Certificate: edge.CertStatus("wildcard-probe." + apps)}
		b.Wildcard.Certificate.Host = "*." + apps
	}
	if b.Source == "set" {
		b.Records, _, _ = m.checkRecords(ctx, p, p.Domain, r.AppsDomain, r.Dashboard)
	}
	var s []string
	switch b.State {
	case "live":
		s = append(s, fmt.Sprintf("%s has a public certificate (%s, until %s).", b.DashboardURL, b.DashboardCertificate.Issuer, b.DashboardCertificate.NotAfter.Format("2 Jan 2006")))
	case "error":
		s = append(s, fmt.Sprintf("The dashboard's certificate failed: %s", explain(b.DashboardCertificate.Error)))
	default:
		s = append(s, fmt.Sprintf("Getting a certificate for %s (usually under a minute).", p.DashboardHost()))
	}
	if b.Wildcard != nil {
		s = append(s, fmt.Sprintf("Apps and previews are at <project>.%s and share one *.%s certificate (DNS-01 through %s).", apps, apps, b.Wildcard.Provider))
	} else {
		s = append(s, fmt.Sprintf("Apps are at <project>.%s and get their certificate on their first visit.", apps))
	}
	if b.Source == "sslip" {
		s = append(s, "Use your own domain with `tiffin domain set example.com` (two DNS records).")
	}
	if b.Previous != nil {
		old := b.Previous.Domain
		if b.Previous.AppsDomain != "" {
			old += " and " + b.Previous.AppsDomain
		}
		if b.Previous.Until == nil {
			s = append(s, fmt.Sprintf("The old names under %s keep working until the new ones have certificates.", old))
		} else {
			s = append(s, fmt.Sprintf("The old names under %s keep working until %s.", old, b.Previous.Until.Format("15:04 MST")))
		}
	}
	b.Summary = strings.Join(s, " ")
	return b
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// recordNames are the names a box domain needs pointing at the box: the
// domain and its wildcard; with a separate apps domain, the dashboard's host
// and the apps domain's wildcard.
func recordNames(domain, apps, dash string) []string {
	if apps == "" || apps == domain {
		return []string{domain, "*." + domain}
	}
	return []string{orDefault(dash, "dashboard") + "." + domain, "*." + apps}
}

// checkRecords checks the records a box domain needs (recordNames; a
// wildcard is probed with a random name), all pointing at the box.
func (m *Module) checkRecords(ctx context.Context, p *platform.Platform, domain, apps, dash string) ([]RecordCheck, bool, error) {
	res := p.Reach.Resolver()
	var b [4]byte
	_, _ = rand.Read(b[:])
	all := true
	var out []RecordCheck
	for _, name := range recordNames(domain, apps, dash) {
		q := name
		if rest, ok := strings.CutPrefix(name, "*."); ok {
			q = "tiffin-check-" + hex.EncodeToString(b[:]) + "." + rest
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		ans, err := res.Lookup(cctx, q)
		cancel()
		if err != nil {
			return nil, false, err
		}
		ok, why := dnskit.PointsHere(ans, p.Reach.PublicIPs)
		all = all && ok
		managed := ""
		if st, _ := m.providerFor(name); st != nil {
			managed = st.rec.Name
		}
		for _, rec := range dnskit.AddressRecords(name, p.Reach.PublicIPs) {
			out = append(out, RecordCheck{Record: rec, OK: ok, Found: addrStrings(ans.Addrs), Reason: why, ManagedBy: managed})
		}
	}
	return out, all, nil
}

// domainCheck is GET /v1/domain/check. apps is a separate apps domain
// ("" or domain: none) and dash the dashboard's name.
func (m *Module) domainCheck(ctx context.Context, p *platform.Platform, domain, apps, dash string) (*DomainCheck, error) {
	if apps == domain {
		apps = ""
	}
	recs, ok, err := m.checkRecords(ctx, p, domain, apps, dash)
	if err != nil {
		return nil, api.NewProblem(http.StatusBadGateway, "internal", "could not ask DNS about "+domain+": "+err.Error())
	}
	c := &DomainCheck{Domain: domain, AppsDomain: apps, Records: recs, OK: ok}
	var managers []string
	for _, r := range recs {
		if r.ManagedBy == "" {
			managers = nil
			break
		}
		if !slices.Contains(managers, r.ManagedBy) {
			managers = append(managers, r.ManagedBy)
		}
	}
	c.ManagedBy = strings.Join(managers, " and ")
	if ids := p.Reach.CAAIdentities(); len(ids) > 0 {
		// The dashboard (HTTP-01) and the apps' names (DNS-01 wildcard when
		// a provider holds their zone).
		type target struct {
			name string
			wild bool
		}
		ts := []target{{"x." + domain, false}}
		if apps != "" {
			ts = []target{{orDefault(dash, "dashboard") + "." + domain, false}, {"x." + apps, false}}
		}
		last := &ts[len(ts)-1]
		if st, _ := m.providerFor(last.name); st != nil {
			last.wild = true
		}
		var problems []string
		for _, t := range ts {
			cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			caa, owner, err := p.Reach.Resolver().CAASet(cctx, t.name)
			cancel()
			if err == nil && (!dnskit.CAAAllows(caa, false, ids...) || (t.wild && !dnskit.CAAAllows(caa, true, ids...))) {
				s := fmt.Sprintf("CAA records on %s do not allow %s; add: %s CAA 0 issue \"%s\"", owner, ids[0], owner, ids[0])
				if t.wild {
					s += fmt.Sprintf(" (and 0 issuewild \"%s\")", ids[0])
				}
				problems = append(problems, s)
			}
		}
		if len(problems) > 0 {
			c.CAA, c.OK = strings.Join(problems, "; "), false
		}
	}
	names := recordNames(domain, apps, dash)
	switch {
	case c.OK:
		cmd := "tiffin domain set " + domain
		if apps != "" {
			cmd += " --apps-domain " + apps
		}
		if dash != "" && dash != "dashboard" {
			cmd += " --dashboard " + dash
		}
		c.Summary = fmt.Sprintf("%s and %s point at this box: %s", names[0], names[1], cmd)
	case c.CAA != "":
		c.Summary = c.CAA
	default:
		var lines, byHand []string
		managed := map[string]bool{}
		for _, r := range recs {
			if r.OK {
				continue
			}
			line := fmt.Sprintf("%s %s %s (name %q in most DNS panels)", r.Name, r.Type, r.Value, r.Host)
			lines = append(lines, line)
			if r.ManagedBy == "" {
				byHand = append(byHand, line)
			} else {
				managed[r.ManagedBy] = true
			}
		}
		c.Summary = "Add these records at your DNS host, then run this again (new records usually show up within minutes): " + strings.Join(lines, "; ")
		switch {
		case c.ManagedBy != "":
			c.Summary += fmt.Sprintf(". Or let the box do it: %s holds these zones, so pass createRecords.", c.ManagedBy)
		case len(managed) > 0:
			c.Summary += ". createRecords sets the ones in zones a connected DNS provider holds; add these by hand: " + strings.Join(byHand, "; ")
		}
	}
	return c, nil
}

// validAppsDomain is validDomain for the optional apps domain ("" stays "").
func validAppsDomain(d string) (string, error) {
	if strings.TrimSpace(d) == "" {
		return "", nil
	}
	d, err := validDomain(d)
	if err == nil && strings.HasSuffix(d, ".sslip.io") {
		return "", api.NewProblem(422, "validation", "sslip.io names are automatic; the apps domain must be one of your own")
	}
	return d, err
}

// onlyManagedMissing: every record DNS does not answer yet is one a
// connected provider was asked to create (worth waiting for).
func onlyManagedMissing(c *DomainCheck) bool {
	for _, r := range c.Records {
		if !r.OK && r.ManagedBy == "" {
			return false
		}
	}
	return c.CAA == ""
}

// validDomain cleans and checks a domain name typed by someone.
func validDomain(d string) (string, error) {
	d = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
	d = strings.TrimPrefix(strings.TrimPrefix(d, "https://"), "http://")
	if i := strings.IndexAny(d, "/:"); i >= 0 {
		d = d[:i]
	}
	if !strings.Contains(d, ".") || len(d) > 253 || strings.Contains(d, "*") || strings.Contains(d, "..") {
		return "", api.NewProblem(422, "validation", fmt.Sprintf("%q is not a domain name; use a name like example.com", d))
	}
	for _, r := range d {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.') {
			return "", api.NewProblem(422, "validation", fmt.Sprintf("%q is not a domain name (letters, digits, dashes and dots only)", d))
		}
	}
	if platform.IsLocalDomain(d) {
		return "", api.NewProblem(422, "validation", fmt.Sprintf("%s is a local-only name; certificates need a public domain", d))
	}
	return d, nil
}

// ---- project domains ----

func (m *Module) projectDomains(ctx context.Context, p *platform.Platform, project string) []Domain {
	wants := m.scan(ctx, p)
	out := []Domain{}
	for _, w := range wants {
		if w.Project == project {
			out = append(out, m.domainStatus(ctx, p, w))
		}
	}
	return out
}

func (m *Module) domainStatus(ctx context.Context, p *platform.Platform, w want) Domain {
	d := Domain{Domain: w.Host, Project: w.Project, Routes: w.Routes, RedirectTo: w.RedirectTo, WWW: w.WWW,
		State: StateWaiting, Records: dnskit.AddressRecords(w.Host, p.Reach.PublicIPs), Found: []string{}, URL: p.URL(w.Host)}
	if d.Routes == nil {
		d.Routes = []route{}
	}
	if d.Records == nil {
		d.Records = []dnskit.Record{}
	}
	if !dnskit.IsApex(w.Host) && p.Reach.ACME && !platform.IsLocalDomain(p.Domain) {
		// A name the box's own records point here: the box domain, or the
		// dashboard's host when apps live elsewhere (the apex is not needed then).
		target := p.Domain
		if p.AppsDomain() != p.Domain {
			target = p.DashboardHost()
		}
		d.Alternative = []dnskit.Record{dnskit.CNAMERecord(w.Host, target)}
	}
	if st, _ := m.providerFor(w.Host); st != nil {
		d.ManagedBy = st.rec.Name
	}
	m.mu.Lock()
	if r := m.recs[w.Host]; r != nil {
		d.State, d.Reason, d.Since = r.State, r.Reason, r.Since
		d.CheckedAt, d.NextCheckAt, d.CNAME = r.CheckedAt, r.NextCheckAt, r.CNAME
		if r.Found != nil {
			d.Found = r.Found
		}
		if r.Ready {
			ci := edge.CertStatus(w.Host)
			d.Certificate = &ci
		}
	} else {
		d.Reason = "not checked yet"
	}
	m.mu.Unlock()
	if len(p.Reach.PublicIPs) == 0 {
		d.Reason = "this box has no public IP address (a local box); custom domains need a server"
	}
	switch d.State {
	case StateLive:
		d.Summary = fmt.Sprintf("%s is live with a public certificate.", d.URL)
	case StateIssuing:
		d.Summary = fmt.Sprintf("%s points at this box; its certificate is on the way (usually under a minute).", w.Host)
	case StateError:
		d.Summary = fmt.Sprintf("%s: %s", w.Host, d.Reason)
	default:
		var recs []string
		for _, r := range d.Records {
			recs = append(recs, r.String())
		}
		d.Summary = fmt.Sprintf("Waiting for DNS (%s). Add: %s", d.Reason, strings.Join(recs, "; "))
		if len(d.Alternative) > 0 {
			d.Summary += fmt.Sprintf(" — or instead one record: %s", d.Alternative[0].String())
		}
		if d.ManagedBy != "" {
			d.Summary += fmt.Sprintf(". %s holds this zone: add the domain again with createRecords to set them for you.", d.ManagedBy)
		}
	}
	return d
}

// editManifest loads a project's manifest, lets edit change it, and plans
// and applies the result like any other change.
func (m *Module) editManifest(ctx context.Context, p *platform.Platform, project, confirm, intent string, edit func(*manifest.Manifest) error) (*DomainChange, error) {
	pr := api.PrincipalFrom(ctx)
	if err := pr.Require(tokens.ScopePlan, project); err != nil {
		return nil, err
	}
	v, res, err := p.DB.Load(ctx, project)
	if err != nil {
		return nil, err
	}
	if v == 0 {
		return nil, api.NewProblem(404, "not_found", "project "+project+" does not exist")
	}
	man, err := change.ManifestFromResources(project, res)
	if err != nil {
		return nil, err
	}
	if err := edit(man); err != nil {
		return nil, err
	}
	manifest.Normalize(man)
	if err := manifest.Validate(man); err != nil {
		return nil, err
	}
	desired, err := change.Resources(man)
	if err != nil {
		return nil, err
	}
	if err := p.CheckPlan(ctx, project, desired); err != nil {
		return nil, err
	}
	plan, err := p.Engine.Plan(ctx, project, desired)
	if err != nil {
		return nil, err
	}
	c, err := p.Engine.Apply(ctx, change.ApplyRequest{Plan: plan, Confirm: confirm, Actor: pr.Actor(), Intent: intent, Authorize: pr.Authorizer()})
	if err != nil {
		return nil, err
	}
	if c != nil {
		p.AfterApply(c)
	}
	m.poke()
	return &DomainChange{Applied: c != nil, Change: c, Plan: plan}, nil
}

type addBody struct {
	Domain        string `json:"domain" minLength:"3" maxLength:"253" doc:"The host name, e.g. example.com or shop.example.com."`
	App           string `json:"app" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"The app that serves it (a web app of this project)."`
	Path          string `json:"path,omitempty" maxLength:"200" doc:"Only this path prefix goes to the app, e.g. /api (another app can serve the rest). Default: everything."`
	WWW           bool   `json:"www,omitempty" doc:"Also serve www.<domain> and redirect it here (point www at the box too)."`
	CreateRecords bool   `json:"createRecords,omitempty" doc:"Create the DNS records through the connected DNS provider that holds the zone (after the change is applied). Box admins only."`
	Confirm       string `json:"confirm,omitempty" doc:"The plan hash (or its first 8+ characters) you reviewed. Without it nothing changes: you get status 428 with the plan."`
	Intent        string `json:"intent,omitempty" maxLength:"500" doc:"Why, in one sentence (shown in the activity timeline)."`
}

type removeBody struct {
	Confirm string `json:"confirm,omitempty" doc:"The plan hash (or its first 8+ characters) you reviewed. Without it nothing changes: you get status 428 with the plan."`
	Intent  string `json:"intent,omitempty" maxLength:"500" doc:"Why, in one sentence."`
}

// ---- registration ----

func requireAdmin(ctx context.Context) error {
	if !api.PrincipalFrom(ctx).BoxAdmin() {
		return api.NewProblem(http.StatusForbidden, "forbidden", "the box domain and DNS providers are box-wide; this needs a key with full access to all projects (or the owner)")
	}
	return nil
}

func actor(ctx context.Context) string {
	if p := api.PrincipalFrom(ctx); p != nil {
		return p.Name
	}
	return "unknown"
}

// RegisterAPI adds the domain operations.
func (m *Module) RegisterAPI(a huma.API, p *platform.Platform) {
	ready := func() error {
		if p == nil || p.DB == nil {
			return api.NewProblem(http.StatusPreconditionFailed, "precondition", "domains are managed on the box")
		}
		m.mu.Lock()
		started := m.started
		m.mu.Unlock()
		if !started {
			return api.NewProblem(http.StatusPreconditionFailed, "precondition", "the domains module has not started yet; try again in a few seconds")
		}
		return nil
	}
	const tag = "domains"

	huma.Register(a, api.Op("domain-get", http.MethodGet, "/v1/domain", "domain status", api.RiskRead,
		"Show the box's domain",
		"The box's domain, dashboard address and apps domain, where certificates come from (Let's Encrypt or the box's own CA on a local box), whether the dashboard's certificate is live, "+
			"the wildcard certificate when a DNS provider is connected, and during a switch the previous domain and until when it still works. "+
			"Without tiffin domain set, a server uses <its-ipv4-with-dashes>.sslip.io: real names and real certificates with no DNS setup.", tag),
		api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body BoxDomain }, error) {
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, ""); err != nil {
				return nil, err
			}
			if err := ready(); err != nil {
				return nil, err
			}
			return &struct{ Body BoxDomain }{m.boxStatus(ctx, p)}, nil
		}))

	huma.Register(a, api.Outbound(api.Op("domain-check", http.MethodGet, "/v1/domain/check", "domain check", api.RiskRead,
		"Check a domain before using it for the box",
		"Read-only: the DNS records a box domain needs (A and AAAA to this box's public addresses) with what DNS answers now: <domain> and *.<domain>; "+
			"with a separate appsDomain, <dashboard>.<domain> and *.<appsDomain>. Also CAA records that would block certificates, and which records a connected DNS provider can set. Asks public DNS resolvers.", tag)),
		api.Wrap(func(ctx context.Context, in *struct {
			Domain     string `query:"domain" required:"true" doc:"e.g. example.com"`
			AppsDomain string `query:"appsDomain" doc:"A separate domain for apps, e.g. example.app (see domain set)."`
			Dashboard  string `query:"dashboard" pattern:"^([a-z][a-z0-9-]{0,39})?$" doc:"The dashboard's first-level name, with appsDomain. Default dashboard."`
		}) (*struct{ Body DomainCheck }, error) {
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, ""); err != nil {
				return nil, err
			}
			if err := ready(); err != nil {
				return nil, err
			}
			d, err := validDomain(in.Domain)
			if err != nil {
				return nil, err
			}
			apps, err := validAppsDomain(in.AppsDomain)
			if err != nil {
				return nil, err
			}
			c, err := m.domainCheck(ctx, p, d, apps, in.Dashboard)
			if err != nil {
				return nil, err
			}
			return &struct{ Body DomainCheck }{*c}, nil
		}))

	set := api.Outbound(api.Op("domain-set", http.MethodPost, "/v1/domain", "domain set", api.RiskWrite,
		"Use your own domain for the box",
		"Switches the box to <domain>: the dashboard moves to dashboard.<domain> (or <dashboard>.<domain>) and apps to <project>.<domain>, "+
			"or with appsDomain to <project>.<appsDomain> (like vercel.com and vercel.app: app code on another registrable domain cannot set cookies on the dashboard's). "+
			"First checks that <domain> and *.<domain> (with appsDomain: <dashboard>.<domain> and *.<appsDomain>) point at this box (A/AAAA records); if not, nothing changes and the answer (status 412) lists exactly the records to add. "+
			"With createRecords, the box creates the ones in zones a connected DNS provider holds. "+
			"The service restarts (a few seconds; apps keep running), new certificates are obtained, and the old names keep working until the new ones have certificates, then for another hour. "+
			"Passkey sign-ins belong to the dashboard's address: add them again on the new one. Box admins only.", tag))
	set.Errors = append(set.Errors, 409, 412)
	huma.Register(a, set, api.Wrap(func(ctx context.Context, in *struct {
		Body struct {
			Domain        string `json:"domain" minLength:"3" maxLength:"253" doc:"e.g. example.com (or apps.example.com to keep the apex for something else)."`
			Dashboard     string `json:"dashboard,omitempty" pattern:"^([a-z][a-z0-9-]{0,39})?$" doc:"The dashboard's first-level name. Default dashboard."`
			AppsDomain    string `json:"appsDomain,omitempty" maxLength:"253" doc:"Serve apps and previews at <project>.<appsDomain> instead of <project>.<domain>, e.g. example.app beside example.com. Default: domain itself."`
			Email         string `json:"email,omitempty" maxLength:"200" doc:"A contact for the certificate authority (optional; also enables the ZeroSSL fallback)."`
			CreateRecords bool   `json:"createRecords,omitempty" doc:"Create the records through the connected DNS provider first (those in zones it holds)."`
			Force         bool   `json:"force,omitempty" doc:"Switch even if DNS does not point here yet (certificates fail until it does)."`
		}
	}) (*struct{ Body BoxDomain }, error) {
		if err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		if err := ready(); err != nil {
			return nil, err
		}
		d, err := validDomain(in.Body.Domain)
		if err != nil {
			return nil, err
		}
		apps, err := validAppsDomain(in.Body.AppsDomain)
		if err != nil {
			return nil, err
		}
		if apps == d {
			apps = ""
		}
		if !p.Reach.ACME || len(p.Reach.PublicIPs) == 0 {
			pr := api.NewProblem(http.StatusPreconditionFailed, "precondition", "this box has no public IP address (it is a local VM), so no domain can point at it")
			pr.Hint = "real domains need a server: tiffin up --provider hetzner (or ssh), then tiffin domain set"
			return nil, pr
		}
		dash := orDefault(in.Body.Dashboard, "dashboard")
		if d == p.Domain && dash == orDefault(p.Reach.Dashboard, "dashboard") && apps == p.Reach.AppsDomain {
			return nil, api.NewProblem(http.StatusConflict, "conflict", "the box already uses "+d)
		}
		if strings.HasSuffix(d, ".sslip.io") {
			pr := api.NewProblem(422, "validation", "sslip.io names are automatic")
			pr.Hint = "tiffin domain unset goes back to the automatic sslip.io name"
			return nil, pr
		}
		check := "tiffin domain check --domain " + d
		if apps != "" {
			check += " --apps-domain " + apps
		}
		if in.Body.CreateRecords {
			var recs []dnskit.Record
			for _, name := range recordNames(d, apps, dash) {
				if st, _ := m.providerFor(name); st != nil {
					recs = append(recs, dnskit.AddressRecords(name, p.Reach.PublicIPs)...)
				}
			}
			if len(recs) == 0 {
				pr := api.NewProblem(http.StatusPreconditionFailed, "precondition", "no connected DNS provider holds the zone of "+strings.Join(recordNames(d, apps, dash), " or "))
				pr.Hint = "connect one with tiffin dns connect cloudflare --token <token>, or add the records yourself (" + check + ")"
				return nil, pr
			}
			if err := p.DNS.SetRecords(ctx, recs, actor(ctx)); err != nil {
				return nil, api.NewProblem(http.StatusBadGateway, "internal", "the DNS provider refused the records: "+err.Error())
			}
		}
		chk, err := m.domainCheck(ctx, p, d, apps, dash)
		if err != nil {
			return nil, err
		}
		for i := 0; !chk.OK && in.Body.CreateRecords && onlyManagedMissing(chk) && i < 10; i++ {
			// Records just created take a moment to show up in public DNS.
			time.Sleep(3 * time.Second)
			if chk, err = m.domainCheck(ctx, p, d, apps, dash); err != nil {
				return nil, err
			}
		}
		if !chk.OK && !in.Body.Force {
			pr := api.NewProblem(http.StatusPreconditionFailed, "precondition", chk.Summary)
			var byHand []string
			for _, r := range chk.Records {
				if !r.OK {
					pr.Errors = append(pr.Errors, api.FieldError{Path: r.Type + " " + r.Name, Message: fmt.Sprintf("add %s %s %s (DNS panel name %q); now: %s", r.Name, r.Type, r.Value, r.Host, r.Reason)})
					if r.ManagedBy == "" {
						byHand = append(byHand, r.Name+" "+r.Type+" "+r.Value)
					}
				}
			}
			pr.Hint = "add the records, wait a minute, then run tiffin domain set again (" + check + " shows progress)"
			switch {
			case chk.ManagedBy != "":
				pr.Hint += "; or pass createRecords: " + chk.ManagedBy + " holds these zones"
			case in.Body.CreateRecords && len(byHand) > 0:
				pr.Hint = "no connected DNS provider holds these, so add them by hand: " + strings.Join(byHand, "; ") + "; then run tiffin domain set again (" + check + " shows progress)"
			}
			return nil, pr
		}
		m.mu.Lock()
		box := m.box
		m.mu.Unlock()
		next := platform.BoxDomain{Domain: d, Dashboard: in.Body.Dashboard, Apps: apps, Email: orDefault(in.Body.Email, box.Email), SetAt: m.now(), SetBy: actor(ctx)}
		if d != p.Domain {
			next.Previous = p.Domain
		} else if box.Previous != "" && box.Previous != d && (box.PreviousUntil.IsZero() || m.now().Before(box.PreviousUntil)) {
			// A switch inside another's grace period (a new apps domain or
			// dashboard name) keeps the older names working too, until the
			// new certificates are live and a fresh grace period ends.
			next.Previous = box.Previous
		}
		if old := p.AppsDomain(); old != orDefault(apps, d) && old != next.Previous {
			next.PreviousApps = old
		}
		if err := m.saveBox(ctx, next); err != nil {
			return nil, err
		}
		_ = p.DB.Audit(ctx, actor(ctx), "domain.set", "box", map[string]any{"domain": d, "previous": p.Domain, "dashboard": dash, "appsDomain": orDefault(apps, d)})
		st := m.boxStatus(ctx, p)
		st.Domain, st.AppsDomain, st.Source, st.Dashboard = d, orDefault(apps, d), "set", dash+"."+d
		st.DashboardURL = p.URL(st.Dashboard)
		st.Records, st.State = chk.Records, "issuing"
		st.Summary = fmt.Sprintf("Switching to %s: the service restarts, then gets certificates for %s (usually under a minute). %s keeps working meanwhile.", d, st.Dashboard, p.DashboardHost())
		if apps != "" {
			st.Summary += fmt.Sprintf(" Apps move to <project>.%s.", apps)
		}
		st.Restarting = m.restart(p, "box domain set to "+d)
		return &struct{ Body BoxDomain }{st}, nil
	}))

	un := api.Op("domain-unset", http.MethodDelete, "/v1/domain", "domain unset", api.RiskWrite,
		"Go back to the automatic domain",
		"Stops using the domain set with tiffin domain set: the box goes back to its automatic name (<ipv4-with-dashes>.sslip.io on a server). "+
			"The service restarts; the old names keep working until the new ones have certificates. Box admins only.", tag)
	un.Errors = append(un.Errors, 409)
	huma.Register(a, un, api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body BoxDomain }, error) {
		if err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		if err := ready(); err != nil {
			return nil, err
		}
		m.mu.Lock()
		box := m.box
		m.mu.Unlock()
		if box.Domain == "" {
			return nil, api.NewProblem(http.StatusConflict, "conflict", "the box already uses its automatic domain "+p.Domain)
		}
		next := platform.BoxDomain{Email: box.Email, Previous: p.Domain, SetAt: m.now(), SetBy: actor(ctx)}
		if p.AppsDomain() != p.Domain {
			next.PreviousApps = p.AppsDomain()
		}
		if err := m.saveBox(ctx, next); err != nil {
			return nil, err
		}
		_ = p.DB.Audit(ctx, actor(ctx), "domain.unset", "box", map[string]any{"previous": p.Domain, "next": p.Reach.DefaultDomain})
		st := m.boxStatus(ctx, p)
		st.Domain, st.AppsDomain, st.Source, st.Dashboard = p.Reach.DefaultDomain, p.Reach.DefaultDomain, "sslip", "dashboard."+p.Reach.DefaultDomain
		st.DashboardURL, st.State, st.Records = p.URL(st.Dashboard), "issuing", nil
		st.Summary = fmt.Sprintf("Switching back to %s; the service restarts. %s keeps working meanwhile.", p.Reach.DefaultDomain, p.DashboardHost())
		st.Restarting = m.restart(p, "box domain unset")
		return &struct{ Body BoxDomain }{st}, nil
	}))

	type projectPath struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
	}
	huma.Register(a, api.Op("domains-list", http.MethodGet, "/v1/projects/{project}/domains", "domains list", api.RiskRead,
		"List a project's own domains",
		"Every host outside the box's own names that the project's apps serve (their routes) or redirect (www), with its state: "+
			"waiting_for_dns (with the records to add and what DNS says now), issuing, live or error (with the reason: points elsewhere, a CAA record, a rate limit...). "+
			"The box re-checks waiting domains on its own with backoff (15 s, then up to every 30 min).", tag),
		api.Wrap(func(ctx context.Context, in *projectPath) (*struct{ Body []Domain }, error) {
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
				return nil, err
			}
			if err := ready(); err != nil {
				return nil, err
			}
			return &struct{ Body []Domain }{m.projectDomains(ctx, p, in.Project)}, nil
		}))

	add := api.Op("domain-add", http.MethodPost, "/v1/projects/{project}/domains", "domains add", api.RiskDestructive,
		"Add a domain to a project",
		"Serves <domain> (optionally only <path>) from one of the project's apps: adds \"<domain>[/path]\" to the app's routes in the manifest (and www: \"redirect\" under domains), "+
			"through plan and apply like any change, so tiffin pull captures it. Without confirm you get status 428 with the plan. "+
			"The answer lists the DNS records to add (A/AAAA to this box; for a subdomain, or one CNAME to the box's own name). "+
			"The box then watches DNS and gets the certificate: waiting_for_dns → issuing → live. With createRecords and a connected DNS provider it adds the records itself (box admins only).", tag)
	add.Errors = append(add.Errors, 404, 409, 428)
	add.Extensions[api.ExtConfirm] = true
	huma.Register(a, add, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Body    addBody
	}) (*struct{ Body DomainChange }, error) {
		if err := ready(); err != nil {
			return nil, err
		}
		// The records go through the box's DNS provider, which holds zones
		// no project owns: as for dns-records-set, box admins only. Checked
		// first, as an unchanged manifest applies nothing (and needs no
		// confirm).
		if in.Body.CreateRecords && !api.PrincipalFrom(ctx).BoxAdmin() {
			pr := api.NewProblem(http.StatusForbidden, "forbidden", "createRecords writes through the box's DNS provider, which is box-wide: it needs a key with full access to all projects (or the owner)")
			pr.Hint = "add the domain without createRecords and add the records it lists yourself, or ask the box owner"
			return nil, pr
		}
		d, err := validDomain(in.Body.Domain)
		if err != nil {
			return nil, err
		}
		if p.IsBoxHost(d) || d == p.Domain || d == p.AppsDomain() || d == p.DashboardHost() {
			pr := api.NewProblem(422, "validation", d+" is one of the box's own names, which already point here")
			pr.Hint = "use the app's routes for first-level names (\"shop\" is " + p.Host("shop") + "), or pick a domain of your own"
			return nil, pr
		}
		path := strings.TrimRight(strings.TrimSpace(in.Body.Path), "/")
		if path != "" && !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		rt := d + path
		intent := orDefault(in.Body.Intent, "serve "+rt+" from app "+in.Body.App)
		out, err := m.editManifest(ctx, p, in.Project, in.Body.Confirm, intent, func(man *manifest.Manifest) error {
			app, ok := man.Apps[in.Body.App]
			if !ok {
				return api.NewProblem(404, "not_found", fmt.Sprintf("project %s has no app %q", in.Project, in.Body.App))
			}
			if app.Role == manifest.RoleWorker {
				return api.NewProblem(422, "validation", fmt.Sprintf("app %q is a worker; only web apps serve domains", in.Body.App))
			}
			for name, other := range man.Apps {
				if name != in.Body.App && slices.Contains(other.Routes, rt) {
					return api.NewProblem(409, "conflict", fmt.Sprintf("%s is already served by app %q", rt, name))
				}
			}
			if !slices.Contains(app.Routes, rt) {
				app.Routes = append(app.Routes, rt)
			}
			man.Apps[in.Body.App] = app
			if in.Body.WWW {
				if man.Domains == nil {
					man.Domains = map[string]manifest.Domain{}
				}
				man.Domains[d] = manifest.Domain{WWW: manifest.WWWRedirect}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		names := []string{d}
		if in.Body.WWW {
			names = append(names, "www."+d)
		}
		if in.Body.CreateRecords {
			var recs []dnskit.Record
			for _, n := range names {
				recs = append(recs, dnskit.AddressRecords(n, p.Reach.PublicIPs)...)
			}
			if st, _ := m.providerFor(d); st == nil || len(recs) == 0 {
				pr := api.NewProblem(http.StatusPreconditionFailed, "precondition", "the domain was added, but no connected DNS provider holds the zone of "+d+" (or the box has no public IP), so add the records yourself")
				pr.Hint = "tiffin domains list " + in.Project + " shows the records"
				return nil, pr
			}
			if err := p.DNS.SetRecords(ctx, recs, actor(ctx)); err != nil {
				return nil, api.NewProblem(http.StatusBadGateway, "internal", "the domain was added, but the DNS provider refused the records: "+err.Error())
			}
			out.Created = recs
		}
		m.recheck(ctx, names...)
		for _, ds := range m.projectDomains(ctx, p, in.Project) {
			if ds.Domain == d {
				out.Domain = &ds
			}
		}
		return &struct{ Body DomainChange }{*out}, nil
	}))

	rm := api.Op("domain-remove", http.MethodPost, "/v1/projects/{project}/domains/{domain}/remove", "domains remove", api.RiskDestructive,
		"Remove a domain from a project",
		"Stops serving <domain>: removes every route of the project's apps on that host (all paths) and its domains entry (www redirect), through plan and apply. "+
			"Without confirm you get status 428 with the plan. DNS records are left alone.", tag)
	rm.Errors = append(rm.Errors, 404, 409, 428)
	rm.Extensions[api.ExtConfirm] = true
	huma.Register(a, rm, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Domain  string `path:"domain" maxLength:"253" doc:"The domain, e.g. example.com"`
		Body    removeBody
	}) (*struct{ Body DomainChange }, error) {
		if err := ready(); err != nil {
			return nil, err
		}
		d := strings.TrimSuffix(strings.ToLower(in.Domain), ".")
		out, err := m.editManifest(ctx, p, in.Project, in.Body.Confirm, orDefault(in.Body.Intent, "stop serving "+d), func(man *manifest.Manifest) error {
			found := false
			for name, app := range man.Apps {
				kept := app.Routes[:0:0]
				for _, rt := range app.Routes {
					if h, _ := splitRoute(rt); h == d {
						found = true
						continue
					}
					kept = append(kept, rt)
				}
				if len(kept) == 0 {
					kept = nil // back to its default address under the box domain (Normalize)
				}
				app.Routes = kept
				man.Apps[name] = app
			}
			if _, ok := man.Domains[d]; ok {
				found = true
				delete(man.Domains, d)
			}
			if !found {
				return api.NewProblem(404, "not_found", fmt.Sprintf("project %s does not serve %s", in.Project, d))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		return &struct{ Body DomainChange }{*out}, nil
	}))

	ck := api.Outbound(api.Op("domain-recheck", http.MethodPost, "/v1/projects/{project}/domains/{domain}/check", "domains check", api.RiskWrite,
		"Check a domain's DNS now",
		"Looks the domain up right away instead of waiting for the next background check (after you add its records, say), and resets its backoff. "+
			"If it now points here, the certificate is requested at once.", tag))
	ck.Errors = append(ck.Errors, 404)
	huma.Register(a, ck, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Domain  string `path:"domain" maxLength:"253" doc:"The domain, e.g. example.com"`
	}) (*struct{ Body Domain }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		if err := ready(); err != nil {
			return nil, err
		}
		d := strings.TrimSuffix(strings.ToLower(in.Domain), ".")
		for _, w := range m.scan(ctx, p) {
			if w.Host == d && w.Project == in.Project {
				m.recheck(ctx, d)
				return &struct{ Body Domain }{m.domainStatus(ctx, p, w)}, nil
			}
		}
		return nil, api.NewProblem(404, "not_found", fmt.Sprintf("project %s does not serve %s", in.Project, d))
	}))

	m.registerDNS(a, p, ready)
}

// recheck checks hosts now and reloads the edge if one became ready.
func (m *Module) recheck(ctx context.Context, hosts ...string) {
	m.tickScanOnly(ctx)
	m.mu.Lock()
	for _, h := range hosts {
		if r := m.recs[h]; r != nil {
			r.NextCheckAt, r.Attempts = time.Time{}, 0
		}
	}
	m.mu.Unlock()
	m.tick(ctx)
}

// tickScanOnly registers newly declared hosts without checking anything.
func (m *Module) tickScanOnly(ctx context.Context) {
	wants := m.scan(ctx, m.p)
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, w := range wants {
		if m.recs[w.Host] == nil {
			m.recs[w.Host] = &record{Host: w.Host, State: StateWaiting, Since: now}
		}
	}
}

func (m *Module) saveBox(ctx context.Context, b platform.BoxDomain) error {
	if err := platform.SaveBoxDomain(ctx, m.p.DB, b); err != nil {
		return err
	}
	m.mu.Lock()
	m.box = b
	m.mu.Unlock()
	return nil
}

// restart asks the service to restart soon (after the answer is sent).
func (m *Module) restart(p *platform.Platform, reason string) bool {
	if p.Restart == nil {
		return false
	}
	p.Restart(reason)
	return true
}
