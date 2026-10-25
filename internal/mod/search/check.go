package search

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxSitemaps  = 50     // child sitemaps read from an index
	maxURLs      = 50_000 // URLs kept per host
	maxFile      = 50 << 20
	batch        = 10_000 // URLs per IndexNow request (the protocol's limit)
	manualAllGap = time.Hour
)

// Check is one thing the box looked at, in words.
type Check struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// Checks are what the box found on its last look at a host.
type Checks struct {
	Robots  Check        `json:"robots" doc:"robots.txt lets search engines in"`
	Home    Check        `json:"home" doc:"The home page answers and does not say noindex"`
	Sitemap SitemapCheck `json:"sitemap"`
	Key     Check        `json:"key" doc:"The IndexNow key file answers on the host"`
	Google  Check        `json:"google" doc:"A google-site-verification TXT record is on the domain (or a parent): it is verified in Google Search Console"`
}

// SitemapCheck is what the box read from the sitemap.
type SitemapCheck struct {
	Check
	URL   string `json:"url,omitempty" doc:"Where the sitemap is (from robots.txt, else /sitemap.xml)"`
	Pages int    `json:"pages" doc:"Pages it lists on this host"`
	// SameDates: every page has the same lastmod (usually the build time),
	// so lastmod says nothing about which pages changed.
	SameDates bool `json:"sameDates,omitempty" doc:"Every page has the same lastmod (usually the build time), so changed pages can't be told apart; only new and removed pages are sent"`
}

// Ping is one message to the search engines.
type Ping struct {
	At     time.Time `json:"at"`
	Reason string    `json:"reason" enum:"live,deploy,check,manual" doc:"live: the host became findable (every page); deploy: after a deploy; check: the regular check; manual: someone asked"`
	Pages  int       `json:"pages" doc:"URLs sent"`
	Sample []string  `json:"sample,omitempty" doc:"Some of the URLs sent"`
	OK     bool      `json:"ok"`
	Status int       `json:"status,omitempty" doc:"What the engine answered (200 or 202 is fine)"`
	Detail string    `json:"detail,omitempty"`
}

type pingOpts struct {
	reason string
	urls   []string // send exactly these (manual)
	all    bool     // send every page again (manual)
}

// check reads a host's robots.txt, home page and sitemap, and sends what
// changed since the last check. It returns the updated record.
func (m *Module) check(ctx context.Context, host string, o pingOpts) (*hostRecord, error) {
	m.runMu.Lock()
	defer m.runMu.Unlock()
	m.mu.Lock()
	r, err := m.record(ctx, host)
	m.mu.Unlock()
	if err != nil || r == nil {
		return r, err
	}
	reason := o.reason
	if reason == "" {
		reason = r.Reason
	}
	base := "https://" + host
	var c Checks
	robots, sitemaps := m.robots(ctx, base)
	c.Robots = robots
	c.Home = m.home(ctx, base)
	m.mu.Lock()
	key := m.key
	m.mu.Unlock()
	c.Key = m.keyFile(ctx, base, key)
	c.Google = m.googleVerified(ctx, host)
	pages, sc := m.sitemap(ctx, base, sitemaps)
	c.Sitemap = sc

	old := m.snapshot(ctx, host)
	var send []string
	switch {
	case len(o.urls) > 0:
		send = o.urls
	case !sc.OK && old == nil && reason == ReasonLive:
		send = []string{base + "/"} // no sitemap: the home page, once
	case !sc.OK:
		// Keep the last snapshot: the next good read compares against it.
	case old == nil || o.all:
		send = sortedKeys(pages)
	default:
		send = diff(old, pages, sc.SameDates)
	}

	now := m.now()
	var ping *Ping
	if len(send) > 0 && m.settings(ctx, r.Project).IndexNow {
		ping = m.submit(ctx, host, key, send)
		ping.At, ping.Reason = now, reason
	}
	if sc.OK && len(o.urls) == 0 && (ping == nil || ping.OK) {
		m.saveSnapshot(ctx, host, pages)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	cur, err := m.record(ctx, host)
	if err != nil || cur == nil {
		return cur, err
	}
	cur.Checks, cur.Checked = c, now
	if cur.Reason == r.Reason && !cur.Due.After(r.Due) {
		cur.Due, cur.DueSince, cur.Reason = time.Time{}, time.Time{}, "" // nothing came in meanwhile
	}
	if ping != nil {
		cur.Last = ping
		cur.History = append([]Ping{*ping}, cur.History...)
		if len(cur.History) > historyMax {
			cur.History = cur.History[:historyMax]
		}
	}
	return cur, m.saveRecord(ctx, cur)
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// diff is what changed between two snapshots (URL → lastmod): new pages,
// removed pages (engines learn they are gone) and pages whose lastmod moved,
// unless every page carries the same date.
func diff(old, cur map[string]string, sameDates bool) []string {
	var out []string
	for u, lm := range cur {
		prev, had := old[u]
		if !had || (!sameDates && lm != "" && lm != prev) {
			out = append(out, u)
		}
	}
	for u := range old {
		if _, ok := cur[u]; !ok {
			out = append(out, u)
		}
	}
	sort.Strings(out)
	return out
}

func (m *Module) snapshot(ctx context.Context, host string) map[string]string {
	raw, ok, err := m.p.DB.KVGet(ctx, kvNS, "urls/"+host)
	if err != nil || !ok {
		return nil
	}
	var s map[string]string
	if json.Unmarshal(raw, &s) != nil {
		return nil
	}
	return s
}

func (m *Module) saveSnapshot(ctx context.Context, host string, pages map[string]string) {
	raw, _ := json.Marshal(pages)
	_ = m.p.DB.KVPut(ctx, kvNS, "urls/"+host, raw)
}

func (m *Module) get(ctx context.Context, u string, limit int64) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := m.client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	return resp.StatusCode, resp.Header, body, err
}

// robots reads robots.txt: whether it shuts search engines out, and the
// sitemaps it names.
func (m *Module) robots(ctx context.Context, base string) (Check, []string) {
	status, _, body, err := m.get(ctx, base+"/robots.txt", 512<<10)
	switch {
	case err != nil:
		return Check{Detail: "Could not be read: " + short(err)}, nil
	case status == 404:
		return Check{OK: true, Detail: "None, so search engines may read every page."}, nil
	case status != 200:
		return Check{Detail: fmt.Sprintf("Answers %d; Google treats 5xx as \"stay away\".", status)}, nil
	}
	var sitemaps []string
	blocked := blocksAll(string(body), &sitemaps)
	if len(blocked) > 0 {
		return Check{Detail: "Shuts out " + strings.Join(blocked, ", ") + " (Disallow: /)."}, sitemaps
	}
	return Check{OK: true, Detail: "Lets search engines in."}, sitemaps
}

// blocksAll reports which of the agents that matter robots.txt shuts out
// of the whole site, and collects its Sitemap lines.
func blocksAll(txt string, sitemaps *[]string) []string {
	type group struct {
		agents []string
		all    bool // Disallow: / with no Allow
		allow  bool
	}
	var groups []*group
	var g *group
	inAgents := false
	sc := bufio.NewScanner(strings.NewReader(txt))
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		switch k {
		case "sitemap":
			if v != "" {
				*sitemaps = append(*sitemaps, v)
			}
			continue
		case "user-agent":
			if !inAgents {
				g = &group{}
				groups = append(groups, g)
			}
			g.agents = append(g.agents, strings.ToLower(v))
			inAgents = true
			continue
		}
		inAgents = false
		if g == nil {
			continue
		}
		switch k {
		case "disallow":
			if v == "/" || v == "/*" {
				g.all = true
			}
		case "allow":
			if v != "" {
				g.allow = true
			}
		}
	}
	var out []string
	for _, bot := range []string{"googlebot", "bingbot"} {
		var match, star *group
		for _, g := range groups {
			for _, a := range g.agents {
				if a == "*" && star == nil {
					star = g
				}
				if a != "*" && strings.Contains(bot, a) && match == nil {
					match = g
				}
			}
		}
		if match == nil {
			match = star
		}
		if match != nil && match.all && !match.allow {
			out = append(out, map[string]string{"googlebot": "Google", "bingbot": "Bing"}[bot])
		}
	}
	return out
}

var metaNoindex = regexp.MustCompile(`(?is)<meta[^>]+name=["']?(robots|googlebot)["']?[^>]*content=["'][^"']*noindex`)

// home checks the home page answers and does not say noindex.
func (m *Module) home(ctx context.Context, base string) Check {
	status, hdr, body, err := m.get(ctx, base+"/", 2<<20)
	switch {
	case err != nil:
		return Check{Detail: "Could not be read: " + short(err)}
	case status >= 400:
		return Check{Detail: fmt.Sprintf("Answers %d.", status)}
	case strings.Contains(strings.ToLower(strings.Join(hdr.Values("X-Robots-Tag"), ",")), "noindex"):
		return Check{Detail: "Sends X-Robots-Tag: noindex."}
	case metaNoindex.Match(body):
		return Check{Detail: "Has <meta name=\"robots\" content=\"noindex\">."}
	}
	return Check{OK: true, Detail: "Answers and may be indexed."}
}

// keyFile checks the IndexNow key answers on the host, as an engine sees it.
func (m *Module) keyFile(ctx context.Context, base, key string) Check {
	status, _, body, err := m.get(ctx, base+"/"+key+".txt", 1024)
	if err != nil {
		return Check{Detail: "Could not be read: " + short(err)}
	}
	if status != 200 || strings.TrimSpace(string(body)) != key {
		return Check{Detail: fmt.Sprintf("Answers %d without the key: engines will refuse the pings.", status)}
	}
	return Check{OK: true, Detail: "Answers on the domain."}
}

type urlset struct {
	URLs     []entry `xml:"url"`
	Sitemaps []entry `xml:"sitemap"`
}

type entry struct {
	Loc     string `xml:"loc"`
	Lastmod string `xml:"lastmod"`
}

// sitemap reads the sitemap (following an index): the host's pages and
// their lastmod.
func (m *Module) sitemap(ctx context.Context, base string, named []string) (map[string]string, SitemapCheck) {
	host := strings.TrimPrefix(base, "https://")
	first := base + "/sitemap.xml"
	for _, s := range named {
		if u, err := url.Parse(s); err == nil && strings.EqualFold(u.Host, host) {
			first = s
			break
		}
	}
	sc := SitemapCheck{URL: first}
	pages := map[string]string{}
	queue := []string{first}
	seen := map[string]bool{}
	read := 0
	for len(queue) > 0 && read < maxSitemaps+1 && len(pages) < maxURLs {
		u := queue[0]
		queue = queue[1:]
		if seen[u] {
			continue
		}
		seen[u] = true
		read++
		status, _, body, err := m.get(ctx, u, maxFile)
		if err != nil {
			if read == 1 {
				sc.Detail = "Could not be read: " + short(err)
				return nil, sc
			}
			continue
		}
		if status != 200 {
			if read == 1 {
				if status == 404 {
					sc.Detail = "No sitemap at " + strings.TrimPrefix(u, base) + ". Without one, only the home page is sent, once."
				} else {
					sc.Detail = fmt.Sprintf("Answers %d.", status)
				}
				return nil, sc
			}
			continue
		}
		if len(body) > 2 && body[0] == 0x1f && body[1] == 0x8b {
			if zr, err := gzip.NewReader(bytes.NewReader(body)); err == nil {
				body, _ = io.ReadAll(io.LimitReader(zr, maxFile))
			}
		}
		var us urlset
		if err := xml.Unmarshal(body, &us); err != nil {
			if read == 1 {
				sc.Detail = "Not valid XML: " + short(err)
				return nil, sc
			}
			continue
		}
		for _, s := range us.Sitemaps {
			if su, err := url.Parse(strings.TrimSpace(s.Loc)); err == nil && strings.EqualFold(su.Host, host) {
				queue = append(queue, su.String())
			}
		}
		for _, e := range us.URLs {
			loc := strings.TrimSpace(e.Loc)
			pu, err := url.Parse(loc)
			if err != nil || pu.Scheme != "https" || !strings.EqualFold(pu.Host, host) || len(pages) >= maxURLs {
				continue
			}
			pages[loc] = strings.TrimSpace(e.Lastmod)
		}
	}
	sc.OK, sc.Pages = true, len(pages)
	sc.SameDates = sameDates(pages)
	switch {
	case len(pages) == 0:
		sc.OK = false
		sc.Detail = "Lists no pages on " + host + "."
	case sc.SameDates:
		sc.Detail = fmt.Sprintf("%d pages. Every page has the same lastmod, usually the build time, so only new and removed pages are sent: set each page's real date.", len(pages))
	default:
		sc.Detail = fmt.Sprintf("%d pages.", len(pages))
	}
	return pages, sc
}

// sameDates: three or more pages, all with one lastmod.
func sameDates(pages map[string]string) bool {
	if len(pages) < 3 {
		return false
	}
	first := "\x00"
	for _, lm := range pages {
		if first == "\x00" {
			first = lm
		}
		if lm != first {
			return false
		}
	}
	return first != ""
}

// submit sends URLs to IndexNow, at most batch per request.
func (m *Module) submit(ctx context.Context, host, key string, urls []string) *Ping {
	p := &Ping{Pages: len(urls), OK: true}
	p.Sample = urls[:min(5, len(urls))]
	for i := 0; i < len(urls); i += batch {
		part := urls[i:min(i+batch, len(urls))]
		body, _ := json.Marshal(map[string]any{
			"host":        host,
			"key":         key,
			"keyLocation": "https://" + host + "/" + key + ".txt",
			"urlList":     part,
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint, bytes.NewReader(body))
		if err != nil {
			p.OK, p.Detail = false, err.Error()
			return p
		}
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		req.Header.Set("User-Agent", userAgent)
		resp, err := m.client.Do(req)
		if err != nil {
			p.OK, p.Detail = false, "IndexNow could not be reached: "+short(err)
			return p
		}
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		p.Status = resp.StatusCode
		if resp.StatusCode != 200 && resp.StatusCode != 202 {
			p.OK, p.Detail = false, indexNowError(resp.StatusCode, msg)
			return p
		}
	}
	if p.Status == 202 {
		p.Detail = "Accepted; the engine checks the key file before it uses the URLs."
	}
	return p
}

func indexNowError(status int, msg []byte) string {
	why := map[int]string{
		400: "the request was malformed",
		403: "the key file did not prove the host is this box's",
		422: "some URLs are not on the host, or the key does not match",
		429: "too many requests: the engine thinks this is spam; pings continue at the next check",
	}[status]
	if why == "" {
		why = "unexpected answer"
	}
	s := "IndexNow answered " + strconv.Itoa(status) + ": " + why
	if t := strings.TrimSpace(string(msg)); t != "" && len(t) < 200 {
		s += " (" + t + ")"
	}
	return s
}

func short(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 && len(s) > 120 {
		s = s[i+2:]
	}
	if len(s) > 160 {
		s = s[:160]
	}
	return s
}

// googleVerified looks for a google-site-verification TXT record on host
// or a parent domain: Search Console verified it (a Domain property covers
// every name under it).
func (m *Module) googleVerified(ctx context.Context, host string) Check {
	labels := strings.Split(host, ".")
	for i := 0; i+2 <= len(labels); i++ {
		name := strings.Join(labels[i:], ".")
		txts, err := m.lookupTXT(ctx, name)
		if err != nil {
			continue
		}
		for _, t := range txts {
			if strings.HasPrefix(t, "google-site-verification=") {
				return Check{OK: true, Detail: "Verified in Search Console (a google-site-verification record is on " + name + ")."}
			}
		}
	}
	return Check{Detail: "Not verified in Search Console yet."}
}
