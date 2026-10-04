// Package domains gives the box real names: its own domain (`tiffin domain
// set`, or <ip>.sslip.io with zero setup), projects' custom domains with
// DNS checks and certificate status, and optional DNS providers (Cloudflare
// first) that create records and unlock wildcard certificates.
//
// Certificates themselves are the edge's job (internal/edge, Caddy +
// certmagic). This module decides which names the edge may get
// certificates for: a custom domain is handed to the edge only once its DNS
// points at the box (so a waiting domain never burns Let's Encrypt's
// failed-validation limit), and it follows each certificate to "live".
package domains

import (
	"context"
	"encoding/json"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/dnskit"
	"github.com/btahir/tiffin/internal/edge"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/platform"
)

func init() { platform.Register(New()) }

// Domain states.
const (
	StateWaiting = "waiting_for_dns"
	StateIssuing = "issuing"
	StateLive    = "live"
	StateError   = "error"
)

// AliasGrace is how long the previous box domain keeps working after the
// new one's certificates are live.
const AliasGrace = time.Hour

// Module is the domains module.
type Module struct {
	now func() time.Time

	mu       sync.Mutex
	p        *platform.Platform
	recs     map[string]*record // custom host → its check state
	box      platform.BoxDomain
	provs    map[string]*providerState
	wake     chan struct{}
	started  bool
	lastScan []want
}

// New returns a module (tests use their own).
func New() *Module {
	return &Module{now: time.Now, recs: map[string]*record{}, provs: map[string]*providerState{}, wake: make(chan struct{}, 1)}
}

// Name implements platform.Module.
func (*Module) Name() string { return "domains" }

// Order: after the runtime (40), whose routes it reads.
func (*Module) Order() int { return 45 }

// record is what the box remembers about one custom domain.
type record struct {
	Host        string    `json:"host"`
	State       string    `json:"state"`
	Reason      string    `json:"reason,omitempty"`
	Found       []string  `json:"found,omitempty"`
	CNAME       string    `json:"cname,omitempty"`
	Ready       bool      `json:"ready"` // handed to the edge for a certificate
	Since       time.Time `json:"since"`
	CheckedAt   time.Time `json:"checkedAt,omitzero"`
	NextCheckAt time.Time `json:"nextCheckAt,omitzero"`
	Attempts    int       `json:"attempts"`
	LiveAt      time.Time `json:"liveAt,omitzero"`
}

const ns = "domains"

func (m *Module) save(ctx context.Context, r *record) {
	if m.p == nil || m.p.DB == nil {
		return
	}
	raw, _ := json.Marshal(r)
	_ = m.p.DB.KVPut(ctx, ns, "custom/"+r.Host, raw)
}

func (m *Module) load(ctx context.Context) error {
	all, err := m.p.DB.KVList(ctx, ns)
	if err != nil {
		return err
	}
	for k, v := range all {
		if host, ok := strings.CutPrefix(k, "custom/"); ok {
			var r record
			if json.Unmarshal(v, &r) == nil && r.Host == host {
				m.recs[host] = &r
			}
		}
	}
	box, err := platform.LoadBoxDomain(ctx, m.p.DB)
	if err != nil {
		return err
	}
	m.box = box
	return m.loadProviders(ctx)
}

// ---- what the projects want ----

// route is one path of a custom domain and the app serving it.
type route struct {
	Path string `json:"path" doc:"Path prefix; / is everything else."`
	App  string `json:"app"`
}

// want is one custom host as the manifests describe it.
type want struct {
	Host       string
	Project    string
	Routes     []route
	RedirectTo string // www.<d> → <d>
	WWW        bool   // the domains entry asks for the www redirect
}

// scan reads every project's apps and domains: the hosts outside the box
// domain they serve, and the www redirects they ask for.
func (m *Module) scan(ctx context.Context, p *platform.Platform) []want {
	projects, err := p.DB.ListProjects(ctx)
	if err != nil {
		return nil
	}
	sort.Strings(projects)
	byHost := map[string]*want{}
	var order []string
	claimed := map[string]bool{} // app route hosts, every project
	var redirects []want
	for _, project := range projects {
		_, res, err := p.DB.Load(ctx, project)
		if err != nil {
			continue
		}
		addrs := make([]string, 0, len(res))
		for a := range res {
			addrs = append(addrs, a)
		}
		sort.Strings(addrs)
		for _, addr := range addrs {
			switch change.Kind(addr) {
			case change.KindApp:
				var a manifest.App
				if json.Unmarshal(res[addr].Spec, &a) != nil || a.Role == manifest.RoleWorker {
					continue
				}
				for _, rt := range a.Routes {
					host, path := splitRoute(rt)
					if !strings.Contains(host, ".") || p.IsBoxHost(host) {
						continue
					}
					claimed[host] = true
					w := byHost[host]
					if w == nil {
						w = &want{Host: host, Project: project}
						byHost[host] = w
						order = append(order, host)
					}
					if w.Project == project {
						w.Routes = append(w.Routes, route{Path: path, App: change.Name(addr)})
					}
				}
			case change.KindDomain:
				var d manifest.Domain
				if json.Unmarshal(res[addr].Spec, &d) == nil && d.WWW == manifest.WWWRedirect {
					redirects = append(redirects, want{Host: "www." + change.Name(addr), Project: project, RedirectTo: change.Name(addr)})
				}
			}
		}
	}
	out := make([]want, 0, len(order)+len(redirects))
	for _, h := range order {
		w := byHost[h]
		sort.Slice(w.Routes, func(i, j int) bool { return w.Routes[i].Path < w.Routes[j].Path })
		out = append(out, *w)
	}
	for _, r := range redirects {
		if claimed[r.Host] || p.IsBoxHost(r.Host) {
			continue // an app serves it; the manifest check refuses this in one project
		}
		for i := range out {
			if out[i].Host == r.RedirectTo && out[i].Project == r.Project {
				out[i].WWW = true
			}
		}
		claimed[r.Host] = true
		out = append(out, r)
	}
	return out
}

// splitRoute: "example.com/api" → ("example.com", "/api"); no path → "/".
func splitRoute(rt string) (host, path string) {
	host, rest, has := strings.Cut(rt, "/")
	path = "/"
	if has && strings.Trim(rest, "/") != "" {
		path = "/" + strings.Trim(rest, "/")
	}
	return strings.ToLower(host), path
}

// ---- the background loop ----

// backoff is the wait before the next DNS check of a domain that does not
// point here yet: quick at first (people add the record, then look), then
// patient (every 30 minutes) so a forgotten domain costs nothing.
func backoff(attempts int) time.Duration {
	steps := []time.Duration{15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute}
	if attempts < len(steps) {
		return steps[attempts]
	}
	return 30 * time.Minute
}

// recheckLive is how often a domain that works is checked again (to notice
// its DNS moving away).
const recheckLive = 15 * time.Minute

// Start loads the state, plugs into the edge and runs the checker.
func (m *Module) Start(ctx context.Context, p *platform.Platform) error {
	if err := m.attach(ctx, p); err != nil {
		return err
	}
	go m.loop(ctx)
	return nil
}

// attach loads the state and plugs the module into the platform and the
// edge (everything Start does but the background loop).
func (m *Module) attach(ctx context.Context, p *platform.Platform) error {
	m.mu.Lock()
	m.p = p
	m.mu.Unlock()
	if err := m.load(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	m.started = true
	m.mu.Unlock()
	p.DNS = &dnsManager{m: m}
	edge.SetCertSource(m.certState)
	cancel := edge.OnCertEvent(func(ev edge.CertEvent) { m.poke() })
	go func() {
		<-ctx.Done()
		cancel()
		edge.SetCertSource(nil)
	}()
	return nil
}

func (m *Module) poke() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Module) loop(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		m.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-m.wake:
		}
	}
}

// tick brings every custom domain's state up to date and reloads the edge
// when the set of names it may get certificates for changed.
func (m *Module) tick(ctx context.Context) {
	p := m.p
	if p == nil {
		return
	}
	wants := m.scan(ctx, p)
	now := m.now()
	m.mu.Lock()
	redirectsChanged := !slices.Equal(redirectKeys(m.lastScan), redirectKeys(wants))
	m.lastScan = wants
	before := m.readyLocked()
	seen := map[string]bool{}
	var due []string
	for _, w := range wants {
		seen[w.Host] = true
		r := m.recs[w.Host]
		if r == nil {
			r = &record{Host: w.Host, State: StateWaiting, Since: now}
			m.recs[w.Host] = r
		}
		if !now.Before(r.NextCheckAt) {
			due = append(due, w.Host)
		}
	}
	for h := range m.recs {
		if !seen[h] {
			delete(m.recs, h)
			if p.DB != nil {
				_ = p.DB.KVDelete(ctx, ns, "custom/"+h)
			}
		}
	}
	m.mu.Unlock()

	for _, h := range due {
		m.checkDNS(ctx, h)
	}
	m.followCerts(ctx)
	changed := m.boxDomainTick(ctx)

	m.mu.Lock()
	after := m.readyLocked()
	m.mu.Unlock()
	if changed || redirectsChanged || !slices.Equal(before, after) {
		if err := p.RefreshRoutes(ctx); err != nil && p.Log != nil {
			p.Log.Error("domains: reload the edge", "err", err)
		}
	}
}

func (m *Module) readyLocked() []string {
	var out []string
	for h, r := range m.recs {
		if r.Ready {
			out = append(out, h)
		}
	}
	sort.Strings(out)
	return out
}

// checkDNS looks a custom domain up and moves it between waiting and ready.
func (m *Module) checkDNS(ctx context.Context, host string) {
	p := m.p
	res := p.Reach.Resolver()
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	ans, err := res.Lookup(cctx, host)
	cancel()
	now := m.now()
	ok, why := false, ""
	if err == nil {
		ok, why = dnskit.PointsHere(ans, p.Reach.PublicIPs)
	} else {
		why = "could not ask DNS: " + err.Error()
	}
	var caaWhy string
	if ok {
		caaWhy = m.caaProblem(ctx, host)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.recs[host]
	if r == nil {
		return
	}
	r.CheckedAt = now
	r.Found, r.CNAME = addrStrings(ans.Addrs), ans.CNAME
	switch {
	case !ok:
		if r.Ready && p.Log != nil {
			p.Log.Warn("domains: DNS no longer points here", "host", host, "why", why)
		}
		m.setState(r, StateWaiting, why, now)
		r.Ready = false
		r.NextCheckAt = now.Add(backoff(r.Attempts))
		r.Attempts++
	case caaWhy != "":
		m.setState(r, StateError, caaWhy, now)
		r.Ready = false
		r.NextCheckAt = now.Add(backoff(r.Attempts))
		r.Attempts++
	default:
		if !r.Ready {
			r.Ready = true
			m.setState(r, StateIssuing, "", now)
		}
		r.Attempts = 0
		r.NextCheckAt = now.Add(recheckLive)
	}
	m.save(ctx, r)
}

// caaProblem says why CAA records stop the box's CA, or "".
func (m *Module) caaProblem(ctx context.Context, host string) string {
	ids := m.p.Reach.CAAIdentities()
	if len(ids) == 0 {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	recs, owner, err := m.p.Reach.Resolver().CAASet(cctx, host)
	if err != nil || dnskit.CAAAllows(recs, false, ids...) {
		return ""
	}
	return "a CAA record on " + owner + " does not allow " + ids[0] + " to issue certificates; add: " +
		owner + " CAA 0 issue \"" + ids[0] + "\""
}

func (m *Module) setState(r *record, state, reason string, now time.Time) {
	if r.State != state {
		r.Since = now
	}
	r.State, r.Reason = state, reason
}

// followCerts moves ready domains to live or error from the edge's view.
func (m *Module) followCerts(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	for _, r := range m.recs {
		if !r.Ready {
			continue
		}
		ci := edge.CertStatus(r.Host)
		prev := r.State
		switch ci.State {
		case "live":
			if r.LiveAt.IsZero() || prev != StateLive {
				r.LiveAt = now
			}
			reason := ""
			if ci.Renewing && ci.Error != "" {
				reason = "renewal is failing (the current certificate still works until " + ci.NotAfter.Format("2 Jan") + "): " + explain(ci.Error)
			}
			m.setState(r, StateLive, reason, now)
		case "error":
			m.setState(r, StateError, explain(ci.Error), now)
		default:
			m.setState(r, StateIssuing, "", now)
		}
		if r.State != prev {
			m.save(ctx, r)
		}
	}
}

// explain turns a CA or solver error into what to do about it.
func explain(err string) string {
	low := strings.ToLower(err)
	short := err
	if len(short) > 300 {
		short = short[:300] + "…"
	}
	switch {
	case strings.Contains(low, "ratelimited") || strings.Contains(low, "rate limit") || strings.Contains(low, "too many"):
		return "the certificate authority's rate limit was hit; the box retries on its own with backoff (Let's Encrypt allows 5 failed attempts per name per hour and 5 identical certificates per week): " + short
	case strings.Contains(low, "caa"):
		return "a CAA record forbids the box's certificate authority; allow letsencrypt.org in your CAA records: " + short
	case strings.Contains(low, "connection refused") || strings.Contains(low, "timeout") || strings.Contains(low, "connection reset") || strings.Contains(low, "no route"):
		return "the certificate authority could not reach this box on ports 80 and 443; make sure both are open in the server's firewall: " + short
	case strings.Contains(low, "nxdomain") || strings.Contains(low, "no valid a records") || strings.Contains(low, "dns problem"):
		return "the certificate authority's DNS check failed; the record may still be spreading: " + short
	}
	return short
}

func addrStrings(as []netip.Addr) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = a.String()
	}
	return out
}

// ---- the edge: names, aliases, wildcard ----

// certState is the edge's CertSource.
func (m *Module) certState() edge.CertState {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := edge.CertState{Ready: m.readyLocked(), Aliases: m.aliasesLocked()}
	if st.Ready == nil {
		st.Ready = []string{}
	}
	if m.p != nil && m.p.Reach.ACME {
		st.Wildcard = m.wildcardLocked()
	}
	return st
}

// aliasesLocked: the previous box domain, while it is still served.
func (m *Module) aliasesLocked() []string {
	b := m.box
	if b.Previous == "" || (m.p != nil && b.Previous == m.p.Domain) {
		return []string{}
	}
	if !b.PreviousUntil.IsZero() && m.now().After(b.PreviousUntil) {
		return []string{}
	}
	return []string{b.Previous}
}

// boxDomainTick starts the previous domain's grace period once the new
// names have certificates, and ends it. It reports whether the aliases
// changed.
func (m *Module) boxDomainTick(ctx context.Context) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.box
	p := m.p
	if b.Previous == "" || p == nil {
		return false
	}
	now := m.now()
	switch {
	case b.PreviousUntil.IsZero():
		live := !p.Reach.ACME || edge.CertStatus(p.DashboardHost()).State == "live"
		if !live {
			return false
		}
		b.PreviousUntil = now.Add(AliasGrace)
	case now.After(b.PreviousUntil):
		b.Previous, b.PreviousUntil = "", time.Time{}
	default:
		return false
	}
	m.box = b
	if p.DB != nil {
		_ = platform.SaveBoxDomain(ctx, p.DB, b)
	}
	return b.Previous == ""
}

// Routes are the www redirects (the apps' own routes come from the runtime).
func (m *Module) Routes(ctx context.Context, p *platform.Platform) ([]edge.Route, error) {
	if p.DB == nil {
		return nil, nil
	}
	return redirectRoutes(m.scan(ctx, p)), nil
}

func redirectKeys(wants []want) []string {
	var out []string
	for _, r := range redirectRoutes(wants) {
		out = append(out, r.Host+">"+r.RedirectTo)
	}
	return out
}

func redirectRoutes(wants []want) []edge.Route {
	var out []edge.Route
	for _, w := range wants {
		if w.RedirectTo != "" {
			out = append(out, edge.Route{Host: w.Host, RedirectTo: w.RedirectTo})
		}
	}
	return out
}

// Kinds: domain resources (their options); the work happens in the loop.
func (*Module) Kinds() []string { return []string{change.KindDomain} }

// Reconcile wakes the checker so a new domain is looked at right away.
func (m *Module) Reconcile(ctx context.Context, p *platform.Platform, project, address string, spec json.RawMessage) error {
	m.poke()
	return nil
}

// Checks reports domains that need attention.
func (m *Module) Checks(ctx context.Context, p *platform.Platform) []platform.Check {
	if !p.Reach.ACME {
		return []platform.Check{{Name: "certificates", OK: true, Detail: "internal CA for " + p.Domain + " (a local box)"}}
	}
	out := []platform.Check{}
	ci := edge.CertStatus(p.DashboardHost())
	c := platform.Check{Name: "certificates", OK: ci.State == "live", Detail: "dashboard " + p.DashboardHost() + ": " + ci.State}
	if ci.Error != "" {
		c.Detail += ": " + explain(ci.Error)
	}
	out = append(out, c)
	m.mu.Lock()
	var bad []string
	for _, r := range m.recs {
		if r.State == StateError {
			bad = append(bad, r.Host+" ("+r.Reason+")")
		}
	}
	m.mu.Unlock()
	if len(bad) > 0 {
		sort.Strings(bad)
		out = append(out, platform.Check{Name: "domains", OK: false, Detail: strings.Join(bad, "; ")})
	}
	return out
}
