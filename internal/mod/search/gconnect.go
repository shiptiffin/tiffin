package search

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/dnskit"
	"github.com/shiptiffin/tiffin/internal/edge"
	"github.com/shiptiffin/tiffin/internal/platform"
	"golang.org/x/net/publicsuffix"
)

// The Google connection. With a service account key on the box, each
// findable address is verified in Search Console without a click: through
// a TXT record on the domain when its zone is on a connected DNS provider
// (one Domain property covers every name under it), else through Google's
// verification file, which the edge serves on the host. Then the box adds
// the property, submits the sitemap, and twice a day reads the searches,
// pages and index status back.

const (
	gPullEvery    = 12 * time.Hour
	gInspectDaily = 20 // pages per host per day sent to URL Inspection
	gReinspect    = 7 * 24 * time.Hour
	gVerifyFor    = 24 * time.Hour // keep trying while DNS reaches Google
)

// Google property states.
const (
	GVerifying = "verifying"
	GVerified  = "verified"
	GFailed    = "failed"
)

// googleConn is the stored connection.
type googleConn struct {
	Sealed      []byte    `json:"sealed"`
	ClientEmail string    `json:"clientEmail"`
	ProjectID   string    `json:"projectId"`
	Owner       string    `json:"owner,omitempty"`
	At          time.Time `json:"at"`
	By          string    `json:"by,omitempty"`
	// Error is why Google refused the key or its calls (a deleted key, an
	// API turned off), since ErrorAt; cleared by the next call that works.
	Error   string    `json:"error,omitempty"`
	ErrorAt time.Time `json:"errorAt,omitzero"`
}

// gprop is one Search Console property the box looks after.
type gprop struct {
	Prop       string            `json:"prop"` // sc-domain:example.com or https://shop.example.com/
	Identifier string            `json:"identifier"`
	Method     string            `json:"method"` // DNS_TXT or FILE
	Host       string            `json:"host,omitempty"`
	Token      string            `json:"token,omitempty"`
	State      string            `json:"state"`
	Detail     string            `json:"detail,omitempty"`
	Tries      int               `json:"tries,omitempty"`
	Since      time.Time         `json:"since"`
	Next       time.Time         `json:"next,omitzero"`
	VerifiedAt time.Time         `json:"verifiedAt,omitzero"`
	Added      bool              `json:"added,omitempty"`
	OwnerAdded string            `json:"ownerAdded,omitempty"`
	Sitemaps   map[string]string `json:"sitemaps,omitempty"` // host → sitemap submitted
}

// GoogleHost is what Google says about one address.
type GoogleHost struct {
	State    string     `json:"state" enum:"not_connected,verifying,verified,failed" doc:"not_connected: no Google service account on the box (Settings › Search engines)"`
	Property string     `json:"property,omitempty" doc:"The Search Console property: sc-domain:example.com covers every name under it"`
	Detail   string     `json:"detail,omitempty"`
	Error    string     `json:"error,omitempty" doc:"Why the box could not read Google's data lately (the key, an API turned off, a lost permission)"`
	PulledAt *time.Time `json:"pulledAt,omitempty" doc:"When the search data below was read"`
	// Last 28 days, web search, this address only.
	Clicks      int          `json:"clicks"`
	Impressions int          `json:"impressions"`
	CTR         float64      `json:"ctr"`
	Position    float64      `json:"position" doc:"Average position (1 is the top)"`
	Daily       []GDay       `json:"daily,omitempty"`
	Sitemap     *GSitemap    `json:"sitemap,omitempty" doc:"The sitemap the box submitted (once, when the address was verified; again only when someone asks with search ping) and what Google did with it"`
	Queries     []GTop       `json:"queries,omitempty" doc:"Searches it showed up for, most clicks first"`
	Pages       []GTop       `json:"pages,omitempty"`
	Index       *GIndexCount `json:"index,omitempty" doc:"What URL Inspection said about the pages checked so far (about 20 a day)"`
}

// GDay is one day's search totals.
type GDay struct {
	Date        string `json:"date"`
	Clicks      int    `json:"clicks"`
	Impressions int    `json:"impressions"`
}

// GTop is one query or page with its totals.
type GTop struct {
	Key         string  `json:"key"`
	Clicks      int     `json:"clicks"`
	Impressions int     `json:"impressions"`
	Position    float64 `json:"position"`
}

// GIndexCount sums the URL Inspection answers.
type GIndexCount struct {
	Checked    int       `json:"checked"`
	Indexed    int       `json:"indexed"`
	NotIndexed []GNotYet `json:"notIndexed,omitempty" doc:"Pages Google has not indexed, with its reason"`
}

// GNotYet is a page Google has not indexed.
type GNotYet struct {
	URL      string `json:"url"`
	Coverage string `json:"coverage"`
	At       string `json:"at"`
}

type gdata struct {
	At          time.Time               `json:"at"`
	Error       string                  `json:"error,omitempty"`
	TriedAt     time.Time               `json:"triedAt,omitzero"` // the last read that failed
	Clicks      int                     `json:"clicks"`
	Impressions int                     `json:"impressions"`
	CTR         float64                 `json:"ctr"`
	Position    float64                 `json:"position"`
	Daily       []GDay                  `json:"daily,omitempty"`
	Sitemap     *GSitemap               `json:"sitemap,omitempty"`
	Queries     []GTop                  `json:"queries,omitempty"`
	Pages       []GTop                  `json:"pages,omitempty"`
	Inspected   map[string]gInspectSeen `json:"inspected,omitempty"`
	InspectDay  string                  `json:"inspectDay,omitempty"`
	InspectUsed int                     `json:"inspectUsed,omitempty"`
}

type gInspectSeen struct {
	At       time.Time `json:"at"`
	Verdict  string    `json:"verdict"`
	Coverage string    `json:"coverage"`
}

func (m *Module) googleConn(ctx context.Context) (*googleConn, error) {
	raw, ok, err := m.p.DB.KVGet(ctx, kvNS, "google")
	if err != nil || !ok {
		return nil, err
	}
	var c googleConn
	return &c, json.Unmarshal(raw, &c)
}

// gclientFor returns the client for the stored key (cached while it is the same).
func (m *Module) gclientFor(ctx context.Context) (*gclient, *googleConn, error) {
	c, err := m.googleConn(ctx)
	if err != nil || c == nil {
		return nil, nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.gc != nil && m.gcAt.Equal(c.At) {
		return m.gc, c, nil
	}
	if m.p.Secrets == nil {
		return nil, nil, errors.New("the box's secrets key is not available")
	}
	raw, err := m.p.Secrets.Unseal(c.Sealed)
	if err != nil {
		return nil, nil, err
	}
	g, err := newGClient(raw, m.client)
	if err != nil {
		return nil, nil, err
	}
	g.base = m.googleBase
	m.gc, m.gcAt = g, c.At
	return g, c, nil
}

// connectGoogle checks a key by signing in and listing its sites, then
// stores it sealed.
func (m *Module) connectGoogle(ctx context.Context, raw []byte, owner, by string) (*googleConn, error) {
	g, err := newGClient(raw, m.client)
	if err != nil {
		return nil, err
	}
	g.base = m.googleBase
	if _, err := g.sites(ctx); err != nil {
		return nil, apiOff(err, "Google Search Console API", g.key.ProjectID)
	}
	// Any domain gets a token; Google refuses only when the API is off.
	if _, err := g.verificationToken(ctx, gsite{Type: "INET_DOMAIN", Identifier: "example.com"}, "DNS_TXT"); err != nil {
		return nil, apiOff(err, "Site Verification API", g.key.ProjectID)
	}
	prev, _ := m.googleConn(ctx)
	if m.p.Secrets == nil {
		return nil, errors.New("the box's secrets key is not available")
	}
	sealed, err := m.p.Secrets.Seal(raw)
	if err != nil {
		return nil, err
	}
	c := &googleConn{Sealed: sealed, ClientEmail: g.key.ClientEmail, ProjectID: g.key.ProjectID, Owner: strings.ToLower(strings.TrimSpace(owner)), At: m.now().UTC(), By: by}
	b, _ := json.Marshal(c)
	if err := m.p.DB.KVPut(ctx, kvNS, "google", b); err != nil {
		return nil, err
	}
	// A different service account owns nothing yet: every property starts
	// again under it. The same account (a new key) keeps them, and those
	// that failed get another go.
	other := prev != nil && prev.ClientEmail != c.ClientEmail
	m.mu.Lock()
	props, _ := m.gprops(ctx)
	for _, gp := range props {
		if other || gp.State == GFailed {
			*gp = gprop{Prop: gp.Prop, Identifier: gp.Identifier, Method: gp.Method, Host: gp.Host, State: GVerifying, Since: c.At}
			_ = m.saveGprop(ctx, gp)
		}
	}
	if other {
		all, _ := m.p.DB.KVList(ctx, kvNS)
		for k := range all {
			if strings.HasPrefix(k, "gdata/") {
				_ = m.p.DB.KVDelete(ctx, kvNS, k)
			}
		}
	}
	m.mu.Unlock()
	if other {
		_ = m.p.RefreshRoutes(ctx) // the old account's verification files go
	}
	m.poke()
	return c, nil
}

// setGoogleOwner changes the Google account added as an owner; verified
// properties get it at the next pass.
func (m *Module) setGoogleOwner(ctx context.Context, owner string) (*googleConn, error) {
	c, err := m.googleConn(ctx)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, api.NewProblem(409, "precondition", "Google is not connected: send the key file first")
	}
	c.Owner = strings.ToLower(owner)
	b, _ := json.Marshal(c)
	if err := m.p.DB.KVPut(ctx, kvNS, "google", b); err != nil {
		return nil, err
	}
	// Saving asks Google again for every property, the same owner included.
	m.mu.Lock()
	props, _ := m.gprops(ctx)
	for _, gp := range props {
		gp.OwnerAdded = ""
		_ = m.saveGprop(ctx, gp)
	}
	m.mu.Unlock()
	m.poke()
	return c, nil
}

// apiOff explains a refusal at connect time.
func apiOff(err error, api, project string) error {
	var ge *GoogleError
	if errors.As(err, &ge) && ge.Status == 403 {
		return errors.New("the key signs in, but Google refused: turn on the " + api + " in the Google Cloud project " + project + ", wait a minute and try again (" + ge.Message + ")")
	}
	return err
}

// noteGoogle records whether Google took the key's calls: an auth or
// permission refusal is kept on the connection (Settings, the box's
// checks); a call that works clears it.
func (m *Module) noteGoogle(ctx context.Context, err error) {
	c, _ := m.googleConn(ctx)
	if c == nil {
		return
	}
	msg := ""
	if err != nil {
		var ge *GoogleError
		if !errors.As(err, &ge) || (ge.Status != 401 && ge.Status != 403 && !strings.HasPrefix(ge.Message, "sign-in refused")) {
			return // a network blip or a 5xx: not the key's fault
		}
		msg = err.Error()
	}
	if c.Error == msg {
		return
	}
	c.Error = msg
	c.ErrorAt = time.Time{}
	if msg != "" {
		c.ErrorAt = m.now().UTC()
	}
	b, _ := json.Marshal(c)
	_ = m.p.DB.KVPut(ctx, kvNS, "google", b)
}

func (m *Module) disconnectGoogle(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	all, err := m.p.DB.KVList(ctx, kvNS)
	if err != nil {
		return err
	}
	for k := range all {
		if k == "google" || strings.HasPrefix(k, "gprop/") || strings.HasPrefix(k, "gdata/") {
			_ = m.p.DB.KVDelete(ctx, kvNS, k)
		}
	}
	m.gc = nil
	return nil
}

func (m *Module) gprops(ctx context.Context) (map[string]*gprop, error) {
	all, err := m.p.DB.KVList(ctx, kvNS)
	if err != nil {
		return nil, err
	}
	out := map[string]*gprop{}
	for k, raw := range all {
		if prop, ok := strings.CutPrefix(k, "gprop/"); ok {
			var gp gprop
			if json.Unmarshal(raw, &gp) == nil {
				out[prop] = &gp
			}
		}
	}
	return out, nil
}

func (m *Module) saveGprop(ctx context.Context, gp *gprop) error {
	b, _ := json.Marshal(gp)
	return m.p.DB.KVPut(ctx, kvNS, "gprop/"+gp.Prop, b)
}

func (m *Module) gdataFor(ctx context.Context, host string) *gdata {
	raw, ok, err := m.p.DB.KVGet(ctx, kvNS, "gdata/"+host)
	if err != nil || !ok {
		return nil
	}
	var d gdata
	if json.Unmarshal(raw, &d) != nil {
		return nil
	}
	return &d
}

func (m *Module) saveGdata(ctx context.Context, host string, d *gdata) {
	b, _ := json.Marshal(d)
	_ = m.p.DB.KVPut(ctx, kvNS, "gdata/"+host, b)
}

// propFor is the property that covers host: the registrable domain's
// Domain property when a connected DNS provider holds its zone, else a
// URL-prefix property for the host alone.
func (m *Module) propFor(ctx context.Context, host string) gprop {
	apex, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err == nil && m.dnsAppender() != nil && m.p.DNS.Manages(ctx, apex) {
		return gprop{Prop: "sc-domain:" + apex, Identifier: apex, Method: "DNS_TXT"}
	}
	return gprop{Prop: "https://" + host + "/", Identifier: "https://" + host + "/", Method: "FILE", Host: host}
}

func (m *Module) dnsAppender() platform.DNSAppender {
	if m.p == nil || m.p.DNS == nil {
		return nil
	}
	a, _ := m.p.DNS.(platform.DNSAppender)
	return a
}

// googlePass verifies new properties, submits sitemaps and pulls data.
func (m *Module) googlePass(ctx context.Context, findable []string) {
	g, conn, err := m.gclientFor(ctx)
	if err != nil {
		m.p.Log.Warn("search: google key", "err", err)
		return
	}
	if g == nil {
		return
	}
	if _, err := g.accessToken(ctx); err != nil {
		m.noteGoogle(ctx, err)
		return
	}
	now := m.now()
	m.mu.Lock()
	props, err := m.gprops(ctx)
	m.mu.Unlock()
	if err != nil {
		return
	}
	hostsOf := map[string][]string{}
	for _, h := range findable {
		want := m.propFor(ctx, h)
		gp := props[want.Prop]
		if gp == nil {
			gp = &want
			gp.State, gp.Since = GVerifying, now
			props[gp.Prop] = gp
			m.mu.Lock()
			_ = m.saveGprop(ctx, gp)
			m.mu.Unlock()
		}
		hostsOf[gp.Prop] = append(hostsOf[gp.Prop], h)
	}
	for prop, gp := range props {
		if ctx.Err() != nil {
			return
		}
		if len(hostsOf[prop]) == 0 {
			continue // no findable address under it now; kept for when one comes back
		}
		switch gp.State {
		case GVerifying:
			if now.Before(gp.Next) {
				continue
			}
			m.verifyStep(ctx, g, conn, gp, now)
		case GFailed:
			if now.Before(gp.Next) {
				continue
			}
			gp.State, gp.Tries = GVerifying, 0
			m.verifyStep(ctx, g, conn, gp, now)
		}
		if gp.State == GVerified && conn.Owner != "" && gp.OwnerAdded != conn.Owner {
			site := gsite{Type: "INET_DOMAIN", Identifier: gp.Identifier}
			if gp.Method == "FILE" {
				site.Type = "SITE"
			}
			if err := g.addOwner(ctx, site, conn.Owner); err == nil {
				gp.Detail = "Verified; " + conn.Owner + " is an owner."
			} else {
				gp.Detail = "Verified, but Google would not add " + conn.Owner + " as an owner: " + err.Error()
			}
			gp.OwnerAdded = conn.Owner // asked once; a new owner asks again
		}
		if gp.State == GVerified {
			m.afterVerified(ctx, g, gp, hostsOf[prop], now)
		}
		m.mu.Lock()
		_ = m.saveGprop(ctx, gp)
		m.mu.Unlock()
	}
}

// verifyStep moves a property one step towards verified: get the token,
// publish it, ask Google to check.
func (m *Module) verifyStep(ctx context.Context, g *gclient, conn *googleConn, gp *gprop, now time.Time) {
	site := gsite{Type: "INET_DOMAIN", Identifier: gp.Identifier}
	if gp.Method == "FILE" {
		site.Type = "SITE"
	}
	if gp.Token == "" {
		tok, err := g.verificationToken(ctx, site, gp.Method)
		if err != nil {
			m.gfail(gp, now, "Google gave no verification token: "+err.Error())
			return
		}
		gp.Token = tok
		if gp.Method == "DNS_TXT" {
			rec := dnskit.Record{Type: "TXT", Name: gp.Identifier, Value: tok}
			if err := m.dnsAppender().AddRecords(ctx, []dnskit.Record{rec}, "search"); err != nil {
				gp.Token = ""
				m.gfail(gp, now, "The TXT record could not be added: "+err.Error())
				return
			}
			gp.Detail = "Added Google's TXT record to " + gp.Identifier + "; waiting for Google to see it."
		} else {
			gp.Detail = "Serving Google's verification file on " + gp.Host + "; waiting for Google to read it."
			if err := m.p.RefreshRoutes(ctx); err != nil {
				m.p.Log.Warn("search: routes", "err", err)
			}
		}
		gp.Next = now.Add(15 * time.Second)
		return
	}
	err := g.verify(ctx, site, gp.Method)
	if err != nil {
		gp.Tries++
		if now.Sub(gp.Since) > gVerifyFor {
			m.gfail(gp, now, "Google still can't see the proof after a day: "+err.Error())
			return
		}
		wait := time.Duration(min(gp.Tries, 30)) * 30 * time.Second
		gp.Next = now.Add(wait)
		if gp.Method == "DNS_TXT" {
			gp.Detail = "Waiting for Google to see the TXT record on " + gp.Identifier + " (tried " + itoa(gp.Tries) + " times)."
		} else {
			gp.Detail = "Waiting for Google to read the verification file on " + gp.Host + " (tried " + itoa(gp.Tries) + " times)."
		}
		return
	}
	gp.State, gp.VerifiedAt, gp.Tries, gp.Next, gp.Detail = GVerified, now, 0, time.Time{}, "Verified."
}

func (m *Module) gfail(gp *gprop, now time.Time, why string) {
	gp.State, gp.Detail, gp.Next = GFailed, why, now.Add(time.Hour)
}

// afterVerified adds the property, submits each host's sitemap and pulls
// the search data when it is due.
func (m *Module) afterVerified(ctx context.Context, g *gclient, gp *gprop, hosts []string, now time.Time) {
	if !gp.Added {
		if err := g.addSite(ctx, gp.Prop); err != nil {
			gp.Detail = "Verified, but adding it to Search Console failed: " + err.Error()
			return
		}
		gp.Added = true
	}
	if gp.Sitemaps == nil {
		gp.Sitemaps = map[string]string{}
	}
	for _, h := range hosts {
		// The sitemap goes to Google once; after that only when someone asks
		// (search ping). Google reads a submitted sitemap again by itself.
		r, _ := m.record(ctx, h)
		if r != nil && r.Checks.Sitemap.OK && r.Checks.Sitemap.URL != "" && gp.Sitemaps[h] == "" {
			if err := g.submitSitemap(ctx, gp.Prop, r.Checks.Sitemap.URL); err == nil {
				gp.Sitemaps[h] = r.Checks.Sitemap.URL
			}
		}
		d := m.gdataFor(ctx, h)
		if d == nil || now.Sub(d.At) >= gPullEvery && (d.Error == "" || now.Sub(d.TriedAt) >= time.Hour) {
			m.pull(ctx, g, gp.Prop, h, gp.Sitemaps[h], d, now)
		}
	}
}

// resubmitSitemap sends a host's sitemap to Google again (someone asked)
// and reads back its status. Nothing happens without a verified property.
func (m *Module) resubmitSitemap(ctx context.Context, host string) error {
	g, _, err := m.gclientFor(ctx)
	if err != nil || g == nil {
		return err
	}
	r, _ := m.record(ctx, host)
	if r == nil || !r.Checks.Sitemap.OK || r.Checks.Sitemap.URL == "" {
		return nil
	}
	want := m.propFor(ctx, host)
	m.mu.Lock()
	props, err := m.gprops(ctx)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	gp := props[want.Prop]
	if gp == nil || gp.State != GVerified {
		return nil
	}
	feed := r.Checks.Sitemap.URL
	if err := g.submitSitemap(ctx, gp.Prop, feed); err != nil {
		return err
	}
	if gp.Sitemaps == nil {
		gp.Sitemaps = map[string]string{}
	}
	gp.Sitemaps[host] = feed
	m.mu.Lock()
	_ = m.saveGprop(ctx, gp)
	m.mu.Unlock()
	d := m.gdataFor(ctx, host)
	if d == nil {
		d = &gdata{}
	}
	if sm, err := g.sitemapStatus(ctx, gp.Prop, feed); err == nil {
		d.Sitemap = sm
		m.saveGdata(ctx, host, d)
	}
	return nil
}

// pull reads 28 days of web search for one host, and asks URL Inspection
// about a few pages.
func (m *Module) pull(ctx context.Context, g *gclient, prop, host, sitemap string, old *gdata, now time.Time) {
	d := &gdata{At: now}
	if old != nil {
		d.Inspected, d.InspectDay, d.InspectUsed, d.Sitemap = old.Inspected, old.InspectDay, old.InspectUsed, old.Sitemap
	}
	if sitemap != "" {
		if sm, err := g.sitemapStatus(ctx, prop, sitemap); err == nil {
			d.Sitemap = sm
		}
	}
	from, to := now.AddDate(0, 0, -28), now
	filter := map[string]any{"dimension": "page", "operator": "includingRegex", "expression": "^https://" + regexpQuote(host) + "/"}
	if rows, err := g.query(ctx, prop, from, to, []string{"date"}, filter, 100); err == nil {
		var pos, impr float64
		for _, r := range rows {
			d.Daily = append(d.Daily, GDay{Date: r.Keys[0], Clicks: int(r.Clicks), Impressions: int(r.Impressions)})
			d.Clicks += int(r.Clicks)
			d.Impressions += int(r.Impressions)
			pos += r.Position * r.Impressions
			impr += r.Impressions
		}
		if impr > 0 {
			d.Position = round1(pos / impr)
			d.CTR = round3(float64(d.Clicks) / impr)
		}
	} else {
		m.noteGoogle(ctx, err)
		if old == nil {
			old = &gdata{}
		}
		old.Error, old.TriedAt = "Google's search data could not be read: "+err.Error(), now
		m.saveGdata(ctx, host, old) // keep what was there; try at the next pass (old.At stays)
		return
	}
	m.noteGoogle(ctx, nil)
	top := func(dim string) []GTop {
		rows, err := g.query(ctx, prop, from, to, []string{dim}, filter, 10)
		if err != nil {
			return nil
		}
		var out []GTop
		for _, r := range rows {
			k := r.Keys[0]
			if dim == "page" {
				k = strings.TrimPrefix(k, "https://"+host)
			}
			out = append(out, GTop{Key: k, Clicks: int(r.Clicks), Impressions: int(r.Impressions), Position: round1(r.Position)})
		}
		return out
	}
	d.Queries, d.Pages = top("query"), top("page")
	m.inspectSome(ctx, g, prop, host, d, now)
	m.saveGdata(ctx, host, d)
}

// inspectSome asks URL Inspection about up to gInspectDaily pages a day:
// pages never asked about first, then the oldest answers.
func (m *Module) inspectSome(ctx context.Context, g *gclient, prop, host string, d *gdata, now time.Time) {
	day := now.UTC().Format(time.DateOnly)
	if d.InspectDay != day {
		d.InspectDay, d.InspectUsed = day, 0
	}
	if d.Inspected == nil {
		d.Inspected = map[string]gInspectSeen{}
	}
	pages := m.snapshot(ctx, host)
	urls := sortedKeys(pages)
	if len(urls) == 0 {
		urls = []string{"https://" + host + "/"}
	}
	for u := range d.Inspected {
		if _, ok := pages[u]; !ok && len(pages) > 0 {
			delete(d.Inspected, u) // gone from the sitemap
		}
	}
	sort.SliceStable(urls, func(i, j int) bool { return d.Inspected[urls[i]].At.Before(d.Inspected[urls[j]].At) })
	for _, u := range urls {
		if d.InspectUsed >= gInspectDaily || ctx.Err() != nil {
			return
		}
		if seen, ok := d.Inspected[u]; ok && now.Sub(seen.At) < gReinspect {
			continue
		}
		r, err := g.inspect(ctx, prop, u)
		d.InspectUsed++
		if err != nil {
			return
		}
		d.Inspected[u] = gInspectSeen{At: now, Verdict: r.Verdict, Coverage: r.Coverage}
	}
}

// googleHost is the Google part of a host's status.
func (m *Module) googleHost(ctx context.Context, host string) *GoogleHost {
	conn, _ := m.googleConn(ctx)
	if conn == nil {
		return &GoogleHost{State: "not_connected"}
	}
	want := m.propFor(ctx, host)
	m.mu.Lock()
	props, _ := m.gprops(ctx)
	m.mu.Unlock()
	gp := props[want.Prop]
	if gp == nil {
		return &GoogleHost{State: GVerifying, Property: want.Prop, Detail: "Starting in a moment."}
	}
	out := &GoogleHost{State: gp.State, Property: gp.Prop, Detail: gp.Detail, Error: conn.Error}
	if d := m.gdataFor(ctx, host); d != nil {
		if out.Error == "" {
			out.Error = d.Error
		}
		if !d.At.IsZero() {
			at := d.At
			out.PulledAt = &at
		}
		out.Clicks, out.Impressions, out.CTR, out.Position = d.Clicks, d.Impressions, d.CTR, d.Position
		out.Daily, out.Queries, out.Pages, out.Sitemap = d.Daily, d.Queries, d.Pages, d.Sitemap
		if len(d.Inspected) > 0 {
			ic := &GIndexCount{}
			for u, s := range d.Inspected {
				ic.Checked++
				if s.Verdict == "PASS" {
					ic.Indexed++
				} else {
					ic.NotIndexed = append(ic.NotIndexed, GNotYet{URL: u, Coverage: s.Coverage, At: s.At.Format(time.RFC3339)})
				}
			}
			sort.Slice(ic.NotIndexed, func(i, j int) bool { return ic.NotIndexed[i].URL < ic.NotIndexed[j].URL })
			if len(ic.NotIndexed) > 20 {
				ic.NotIndexed = ic.NotIndexed[:20]
			}
			out.Index = ic
		}
	}
	return out
}

// googleRoutes serve Google's verification files on hosts verified by FILE.
func (m *Module) googleRoutes(ctx context.Context) []edge.Route {
	if m.p == nil || m.p.DB == nil {
		return nil
	}
	props, err := m.gprops(ctx)
	if err != nil {
		return nil
	}
	var out []edge.Route
	for _, gp := range props {
		if gp.Method == "FILE" && gp.Token != "" && gp.Host != "" {
			out = append(out, edge.Route{Host: gp.Host, PathPrefix: "/" + gp.Token, Body: "google-site-verification: " + gp.Token})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

func regexpQuote(s string) string {
	return strings.NewReplacer(".", `\.`, "-", `\-`).Replace(s)
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }
func round3(f float64) float64 { return float64(int(f*1000+0.5)) / 1000 }

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
