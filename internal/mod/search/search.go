// Package search gets apps found. A production app on a domain of its own
// is findable: the box serves an IndexNow key on every app host, reads the
// app's robots.txt and sitemap, and tells search engines which pages are
// new, changed or gone: once when the domain goes live, after each deploy
// and every few hours (pages a database makes change without a deploy).
// One IndexNow ping reaches Bing, Yandex, Seznam, Naver, Yep, Amazon and the
// Internet Archive; Google takes no pings and reads the sitemap itself.
//
// Names on ShipTiffin's shared domains (shiptiffin.app), sslip.io and
// local names are never findable: the edge says noindex there
// (platform.HiddenFromSearch).
package search

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shiptiffin/tiffin/internal/edge"
	"github.com/shiptiffin/tiffin/internal/mod/domains"
	"github.com/shiptiffin/tiffin/internal/mod/runtime"
	"github.com/shiptiffin/tiffin/internal/platform"
)

func init() { platform.Register(New()) }

const (
	kvNS = "search"
	// CheckEvery is how often a findable host's sitemap is read again.
	CheckEvery = 6 * time.Hour
	// Settle is how long the box waits after a deploy before reading the
	// sitemap, so a burst of deploys makes one ping; SettleMax caps the
	// wait while deploys keep coming.
	Settle    = 2 * time.Minute
	SettleMax = 10 * time.Minute
	// tick is how often the loop looks for new hosts and deploys.
	tick = time.Minute
	// ForgetAfter is how long a host stays unfindable before the box forgets
	// it (and sends every page again if it comes back): a restart, a
	// certificate renewal or a brief outage changes nothing.
	ForgetAfter = 24 * time.Hour
	// Endpoint is where pings go: any IndexNow engine shares them with the rest.
	Endpoint = "https://www.bing.com/indexnow"
	// userAgent is what the box sends when it reads its apps' files.
	userAgent = "TiffinSearch/1 (+https://shiptiffin.com/docs/search)"
)

// Why a ping was sent.
const (
	ReasonLive   = "live"   // the host became findable: every page
	ReasonDeploy = "deploy" // an app on the host deployed
	ReasonCheck  = "check"  // the regular sitemap check
	ReasonManual = "manual" // someone asked
)

// Module is the search module.
type Module struct {
	mu     sync.Mutex // the stored records
	runMu  sync.Mutex // one host check at a time
	p      *platform.Platform
	key    string
	pokeCh chan struct{}
	crawl  *crawlLog

	// Seams for tests.
	now        func() time.Time
	client     *http.Client
	endpoint   string
	liveHosts  func(context.Context) ([]runtime.LiveHost, error)
	routes     func() []edge.Route
	domainLive func(host string) bool
	acme       func() bool
	lookupTXT  func(context.Context, string) ([]string, error)
	googleBase string // Google's API hosts, overridden in tests

	gc   *gclient // the Google client for the stored key
	gcAt time.Time
}

// New returns the module wired to the box.
func New() *Module {
	return &Module{
		pokeCh:     make(chan struct{}, 1),
		crawl:      &crawlLog{tracked: map[string]bool{}, recs: map[string]*crawlRecord{}, dirty: map[string]bool{}},
		now:        time.Now,
		client:     &http.Client{Timeout: 20 * time.Second, CheckRedirect: sameHostRedirects},
		endpoint:   Endpoint,
		liveHosts:  runtime.LiveHosts,
		domainLive: domains.Live,
		lookupTXT:  net.DefaultResolver.LookupTXT,
	}
}

// sameHostRedirects follows a few redirects that stay on the host (a
// sitemap moved to /sitemap-index.xml), never to another one.
func sameHostRedirects(req *http.Request, via []*http.Request) error {
	if len(via) >= 3 || req.URL.Host != via[0].URL.Host {
		return http.ErrUseLastResponse
	}
	return nil
}

func (*Module) Name() string { return "search" }

// Start loads (or makes) the box's IndexNow key and starts the loop.
func (m *Module) Start(ctx context.Context, p *platform.Platform) error {
	if p.DB == nil {
		return nil
	}
	if err := m.init(ctx, p); err != nil {
		return err
	}
	root := p.DataRoot
	if root == "" {
		root = "/var/lib/tiffin"
	}
	m.startCrawlLog(ctx, root)
	go m.loop(ctx)
	return nil
}

func (m *Module) init(ctx context.Context, p *platform.Platform) error {
	key, err := loadKey(ctx, p)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.p, m.key = p, key
	if m.routes == nil {
		m.routes = p.LoadedRoutes
	}
	if m.acme == nil {
		m.acme = func() bool { return p.Reach.ACME }
	}
	m.mu.Unlock()
	return nil
}

// loadKey reads the box's IndexNow key, making one the first time.
func loadKey(ctx context.Context, p *platform.Platform) (string, error) {
	raw, ok, err := p.DB.KVGet(ctx, kvNS, "key")
	if err != nil {
		return "", err
	}
	if ok && len(raw) >= 8 {
		return string(raw), nil
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	key := hex.EncodeToString(b)
	if _, err := p.DB.KVPutIfAbsent(ctx, kvNS, "key", []byte(key)); err != nil {
		return "", err
	}
	raw, _, err = p.DB.KVGet(ctx, kvNS, "key")
	return string(raw), err
}

// Routes serve the key file on every app host: the proof IndexNow engines
// fetch before they take a host's URLs from this box.
func (m *Module) Routes(ctx context.Context, p *platform.Platform) ([]edge.Route, error) {
	m.mu.Lock()
	key := m.key
	m.mu.Unlock()
	if key == "" {
		return nil, nil
	}
	return append([]edge.Route{{PathPrefix: "/" + key + ".txt", Body: key}}, m.googleRoutes(ctx)...), nil
}

// RoutesLoaded wakes the loop: routes change when a domain goes live or an
// app deploys.
func (m *Module) RoutesLoaded() { m.poke() }

func (m *Module) poke() {
	select {
	case m.pokeCh <- struct{}{}:
	default:
	}
}

func (m *Module) loop(ctx context.Context) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		m.pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-m.pokeCh:
		}
	}
}

// hostRecord is what the box keeps about one findable host.
type hostRecord struct {
	Host    string    `json:"host"`
	Project string    `json:"project"`
	App     string    `json:"app,omitempty"`
	Deploys string    `json:"deploys,omitempty"`
	Since   time.Time `json:"since"`
	// Due: a check (and ping) waits until then; zero when none waits.
	Due      time.Time `json:"due,omitzero"`
	DueSince time.Time `json:"dueSince,omitzero"`
	Reason   string    `json:"reason,omitempty"`
	Checked  time.Time `json:"checked,omitzero"`
	Checks   Checks    `json:"checks"`
	Last     *Ping     `json:"last,omitempty"`
	History  []Ping    `json:"history,omitempty"`
	// Gone: since when the host is not findable (zero while it is).
	Gone time.Time `json:"gone,omitzero"`
}

const historyMax = 10

// Settings are a project's choices.
type Settings struct {
	IndexNow bool `json:"indexNow" doc:"Tell search engines about new and changed pages (IndexNow). On by default."`
}

func (m *Module) settings(ctx context.Context, project string) Settings {
	s := Settings{IndexNow: true}
	if raw, ok, err := m.p.DB.KVGet(ctx, kvNS, "settings/"+project); err == nil && ok {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

func (m *Module) saveSettings(ctx context.Context, project string, s Settings) error {
	raw, _ := json.Marshal(s)
	return m.p.DB.KVPut(ctx, kvNS, "settings/"+project, raw)
}

func (m *Module) records(ctx context.Context) (map[string]*hostRecord, error) {
	all, err := m.p.DB.KVList(ctx, kvNS)
	if err != nil {
		return nil, err
	}
	out := map[string]*hostRecord{}
	for k, raw := range all {
		host, ok := strings.CutPrefix(k, "host/")
		if !ok {
			continue
		}
		var r hostRecord
		if json.Unmarshal(raw, &r) == nil {
			out[host] = &r
		}
	}
	return out, nil
}

func (m *Module) record(ctx context.Context, host string) (*hostRecord, error) {
	raw, ok, err := m.p.DB.KVGet(ctx, kvNS, "host/"+host)
	if err != nil || !ok {
		return nil, err
	}
	var r hostRecord
	return &r, json.Unmarshal(raw, &r)
}

func (m *Module) saveRecord(ctx context.Context, r *hostRecord) error {
	raw, _ := json.Marshal(r)
	return m.p.DB.KVPut(ctx, kvNS, "host/"+r.Host, raw)
}

func (m *Module) forget(ctx context.Context, host string) {
	_ = m.p.DB.KVDelete(ctx, kvNS, "host/"+host)
	_ = m.p.DB.KVDelete(ctx, kvNS, "urls/"+host)
	_ = m.p.DB.KVDelete(ctx, kvNS, "gdata/"+host)
}

// pass finds new findable hosts and deploys, then checks the hosts due.
func (m *Module) pass(ctx context.Context) {
	if m.p == nil || !m.acme() {
		return // a local box: nothing out there could fetch its pages
	}
	if m.routes() == nil {
		return // the edge's routes are not loaded yet (the box is starting)
	}
	views, err := m.hosts(ctx)
	if err != nil {
		m.p.Log.Warn("search: hosts", "err", err)
		return
	}
	now := m.now()
	m.mu.Lock()
	recs, err := m.records(ctx)
	if err != nil {
		m.mu.Unlock()
		return
	}
	findable := map[string]bool{}
	for _, v := range views {
		if !v.Findable {
			continue
		}
		findable[v.Host] = true
		r := recs[v.Host]
		if r != nil && !r.Gone.IsZero() {
			r.Gone = time.Time{}
			_ = m.saveRecord(ctx, r)
		}
		switch {
		case r == nil:
			r = &hostRecord{Host: v.Host, Project: v.Project, App: v.App, Deploys: v.deploys, Since: now, Due: now, DueSince: now, Reason: ReasonLive}
		case r.Deploys != v.deploys:
			r.Project, r.App, r.Deploys = v.Project, v.App, v.deploys
			if r.Due.IsZero() || r.Reason == ReasonCheck {
				r.DueSince, r.Reason = now, ReasonDeploy
			}
			if r.Reason != ReasonLive {
				r.Due = minTime(now.Add(Settle), r.DueSince.Add(SettleMax))
			}
		case r.Due.IsZero() && now.Sub(r.Checked) >= CheckEvery:
			r.Due, r.DueSince, r.Reason = now, now, ReasonCheck
		default:
			continue
		}
		recs[v.Host] = r
		_ = m.saveRecord(ctx, r)
	}
	tracked := map[string]bool{}
	for host := range findable {
		tracked[host] = true
	}
	for host, r := range recs {
		if r.Gone.IsZero() || now.Sub(r.Gone) < ForgetAfter {
			tracked[host] = true // keep its crawler log while it may come back
		}
	}
	m.track(ctx, tracked, now)
	var due []string
	for host, r := range recs {
		if !findable[host] {
			switch {
			case r.Gone.IsZero():
				r.Gone, r.Due, r.DueSince, r.Reason = now, time.Time{}, time.Time{}, ""
				_ = m.saveRecord(ctx, r)
			case now.Sub(r.Gone) >= ForgetAfter:
				m.forget(ctx, host) // gone for good: start afresh if it comes back
			}
			continue
		}
		if !r.Due.IsZero() && !now.Before(r.Due) {
			due = append(due, host)
		}
	}
	m.mu.Unlock()
	sort.Strings(due)
	for _, host := range due {
		if ctx.Err() != nil {
			return
		}
		m.check(ctx, host, pingOpts{})
	}
	hosts := make([]string, 0, len(findable))
	for h := range findable {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	m.googlePass(ctx, hosts)
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// HostView is a production host and whether search engines may find it.
type hostView struct {
	Host     string
	Project  string
	App      string
	Findable bool
	Why      string // why not
	Fix      string
	deploys  string
}

// hosts are every production host with its findability, read from the
// runtime (what is deployed) and the edge's routes (what it says).
func (m *Module) hosts(ctx context.Context) ([]hostView, error) {
	live, err := m.liveHosts(ctx)
	if err != nil {
		return nil, err
	}
	root := map[string]edge.Route{}
	redirect := map[string]string{}
	for _, rt := range m.routes() {
		if rt.Host == "" || rt.PathPrefix != "" {
			continue
		}
		if rt.RedirectTo != "" {
			redirect[rt.Host] = rt.RedirectTo
			continue
		}
		root[rt.Host] = rt
	}
	out := make([]hostView, 0, len(live))
	for _, h := range live {
		v := hostView{Host: h.Host, Project: h.Project, App: h.App, deploys: h.Deploys}
		rt, served := root[h.Host]
		switch {
		case platform.HiddenFromSearch(h.Host):
			v.Why = "Addresses on " + sharedName(h.Host) + " are hidden from search engines."
			v.Fix = "Add a domain of your own to be found."
		case redirect[h.Host] != "":
			v.Why = "It redirects to " + redirect[h.Host] + ", which search engines see instead."
		case !m.p.IsBoxHost(h.Host) && !m.domainLive(h.Host):
			v.Why = "The domain is not live yet (DNS or its certificate)."
		case !served:
			v.Why = "No app serves the domain's home page."
		case rt.NoIndex:
			v.Why = "The address says noindex."
		case rt.Gate:
			v.Why = "Only people signed in to the box can open it."
		default:
			v.Findable = true
		}
		out = append(out, v)
	}
	return out, nil
}

// sharedName is the domain a hidden host is on, in words.
func sharedName(host string) string {
	if z := platform.SharedZone(host); z != "" {
		return z
	}
	if strings.HasSuffix(host, ".sslip.io") {
		return "sslip.io"
	}
	return "this local name"
}

// ProjectDeleted forgets a project's hosts and settings.
func (m *Module) ProjectDeleted(ctx context.Context, p *platform.Platform, project string) error {
	if m.p == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	recs, err := m.records(ctx)
	if err != nil {
		return err
	}
	for host, r := range recs {
		if r.Project == project {
			m.forget(ctx, host)
		}
	}
	return m.p.DB.KVDelete(ctx, kvNS, "settings/"+project)
}

var errNotReady = errors.New("search runs on a Tiffin box")
