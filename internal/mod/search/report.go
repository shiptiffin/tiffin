package search

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// The report behind Analytics › Search: for any days, how the project's
// findable addresses stand (IndexNow, Google), what people searched to find
// them (Google's numbers, from the history store) and which search engines
// and AI companies read their pages (the crawler log).

// Report is a project's search over some days.
type Report struct {
	Project string `json:"project"`
	From    string `json:"from" doc:"The first day of Google's numbers (YYYY-MM-DD). Google's days run on Pacific time and its numbers 2 to 3 days behind, so a period ends on the last day Google has."`
	To      string `json:"to"`
	// The days before, for the comparison.
	PrevFrom string `json:"prevFrom"`
	PrevTo   string `json:"prevTo"`
	// The crawler log is live, so its days end today.
	CrawlFrom string       `json:"crawlFrom"`
	CrawlTo   string       `json:"crawlTo"`
	Hosts     []ReportHost `json:"hosts" doc:"Every production address of the project's apps; the report covers the findable ones (or host)"`
	Host      string       `json:"host,omitempty" doc:"The one address shown, when asked for"`
	IndexNow  ReportIndex  `json:"indexNow"`
	Google    ReportGoogle `json:"google"`
	Searches  *Searches    `json:"searches,omitempty" doc:"What people searched on Google to find the pages; missing until an address is verified in Search Console"`
	Crawlers  []Crawled    `json:"crawlers" doc:"Search and AI bots that fetched pages in the crawler days, most requests first. A bot counts only from the addresses its operator publishes."`
	CrawlDays []CrawlDay   `json:"crawlDays,omitempty" doc:"Requests per day: search engines and AI apart"`
	Fakes     int          `json:"fakes" doc:"Requests whose user agent claimed a known bot from an address outside its operator's list"`
}

// ReportHost is one production address.
type ReportHost struct {
	Host     string `json:"host"`
	App      string `json:"app,omitempty"`
	Findable bool   `json:"findable"`
	Why      string `json:"why,omitempty" doc:"Why it is not findable"`
	Fix      string `json:"fix,omitempty"`
	Hidden   bool   `json:"hidden,omitempty" doc:"Kept out of search results by the project's settings"`
}

// ReportIndex is IndexNow on the findable addresses.
type ReportIndex struct {
	On       bool       `json:"on" doc:"The box tells IndexNow about new, changed and removed pages (every findable address, no setup)"`
	Last     *Ping      `json:"last,omitempty" doc:"The newest ping"`
	Pages    int        `json:"pages" doc:"Pages in the sitemaps the box read"`
	Checked  *time.Time `json:"checked,omitempty" doc:"When the box last read them"`
	Problems []string   `json:"problems,omitempty" doc:"What the last look found wrong: robots.txt, the home page, the sitemap or the key file"`
}

// ReportGoogle is the Google connection for the addresses shown.
type ReportGoogle struct {
	State    string       `json:"state" enum:"not_connected,verifying,verified,failed,none" doc:"none: no findable address to verify"`
	Property string       `json:"property,omitempty"`
	Detail   string       `json:"detail,omitempty"`
	Error    string       `json:"error,omitempty"`
	PulledAt *time.Time   `json:"pulledAt,omitempty" doc:"When Google was last read"`
	Filling  bool         `json:"filling,omitempty" doc:"The box is still reading back Google's 16 months"`
	First    string       `json:"first,omitempty" doc:"The first day the box holds Google's numbers for"`
	Sitemap  *GSitemap    `json:"sitemap,omitempty" doc:"What Google did with the sitemaps (pages found, errors), summed"`
	Index    *GIndexCount `json:"index,omitempty" doc:"What URL Inspection said about the pages checked so far (about 20 a day per address)"`
}

// Searches are Google's numbers for the days shown.
type Searches struct {
	Totals   SearchTotals `json:"totals"`
	Previous SearchTotals `json:"previous" doc:"The same number of days before"`
	Daily    []SearchDay  `json:"daily"`
	PrevDays []SearchDay  `json:"prevDaily"`
	Queries  []GTop       `json:"queries" doc:"What people searched, most clicks first (50 at most). Google leaves out rare searches, so these add up to less than the totals."`
	Pages    []GTop       `json:"pages" doc:"Pages that showed up, as paths (with the host when the report covers several addresses)"`
	Through  string       `json:"through,omitempty" doc:"The last day Google has numbers for"`
	Moved    *Moved       `json:"moved,omitempty" doc:"The searches and pages that gained or lost the most against the days before; missing when Google had nothing then"`
}

// Moved is what changed most between the days shown and the days before:
// clicks first, then impressions.
type Moved struct {
	QueriesUp   []Mover `json:"queriesUp"`
	QueriesDown []Mover `json:"queriesDown"`
	PagesUp     []Mover `json:"pagesUp"`
	PagesDown   []Mover `json:"pagesDown"`
}

// Mover is one search or page in both periods.
type Mover struct {
	Key             string  `json:"key"`
	Clicks          int     `json:"clicks"`
	PrevClicks      int     `json:"prevClicks"`
	Impressions     int     `json:"impressions"`
	PrevImpressions int     `json:"prevImpressions"`
	Position        float64 `json:"position" doc:"0 when it didn't show"`
	PrevPosition    float64 `json:"prevPosition" doc:"0 when it didn't show the days before"`
}

// SearchTotals sum some days.
type SearchTotals struct {
	Clicks      int     `json:"clicks"`
	Impressions int     `json:"impressions" doc:"Times a page showed in results"`
	CTR         float64 `json:"ctr" doc:"Clicks per impression"`
	Position    float64 `json:"position" doc:"Average position (1 is the top)"`
}

// SearchDay is one day's numbers.
type SearchDay struct {
	Date        string  `json:"date"`
	Clicks      int     `json:"clicks"`
	Impressions int     `json:"impressions"`
	Position    float64 `json:"position"`
}

// Crawled is one bot over the crawler days.
type Crawled struct {
	Bot        string     `json:"bot"`
	Operator   string     `json:"operator"`
	Purpose    string     `json:"purpose" enum:"search,user,training" doc:"search: builds a search index; user: fetches a page for someone's question in a chat; training: collects pages to train models"`
	AI         bool       `json:"ai" doc:"An AI company's bot (OpenAI, Anthropic, Perplexity), not a search engine's"`
	Hits       int        `json:"hits"`
	Errors     int        `json:"errors" doc:"Requests answered 4xx or 5xx"`
	LastAt     *time.Time `json:"lastAt,omitempty"`
	LastPath   string     `json:"lastPath,omitempty"`
	LastStatus int        `json:"lastStatus,omitempty"`
}

// CrawlDay is one day's bot requests.
type CrawlDay struct {
	Date   string `json:"date"`
	Search int    `json:"search"`
	AI     int    `json:"ai"`
}

// Report periods, as Analytics names them.
var reportDays = map[string]int{"7d": 7, "30d": 30, "90d": 90, "12mo": 365}

type reportIn struct {
	Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
	Period  string `query:"period" enum:"7d,30d,90d,12mo" doc:"The days to show, ending on the last day Google has numbers for (default 30d)"`
	From    string `query:"from" pattern:"^\\d{4}-\\d{2}-\\d{2}$" doc:"Or the first day (YYYY-MM-DD)..."`
	To      string `query:"to" pattern:"^\\d{4}-\\d{2}-\\d{2}$" doc:"...and the last (default today)"`
	Host    string `query:"host" doc:"Only this address (default: every findable one)"`
}

func (m *Module) registerReport(a huma.API, ready func() error) {
	op := api.Op("search-report", http.MethodGet, "/v1/projects/{project}/search/report", "search report", api.RiskRead,
		"Show what people searched to find a project, and which bots read it",
		"For some days: Google's clicks, impressions, average position, top searches and pages (from Search Console, kept on the box for 16 months, "+
			"with the days before for comparison); whether IndexNow and Google are set up and what Google's index says; and which search engines and AI bots "+
			"fetched the project's pages (only bots from their operators' published addresses count). Google's numbers run 2 to 3 days behind.", "search")
	op.Errors = append(op.Errors, 404, 409, 422)
	huma.Register(a, op, api.Wrap(func(ctx context.Context, in *reportIn) (*struct{ Body Report }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
			return nil, err
		}
		if err := ready(); err != nil {
			return nil, err
		}
		r, err := m.report(ctx, in)
		return &struct{ Body Report }{r}, err
	}))
}

func (m *Module) report(ctx context.Context, in *reportIn) (Report, error) {
	r := Report{Project: in.Project, Hosts: []ReportHost{}, Crawlers: []Crawled{}}
	views, err := m.hosts(ctx)
	if err != nil {
		return r, err
	}
	local := !m.acme()
	want := strings.ToLower(strings.TrimSpace(in.Host))
	var hosts []string
	for _, v := range views {
		if v.Project != in.Project {
			continue
		}
		h := ReportHost{Host: v.Host, App: v.App, Findable: v.Findable && !local, Why: v.Why, Fix: v.Fix, Hidden: v.Hidden}
		if local && v.Findable {
			h.Why = "This is a local box: nothing outside it can reach its apps."
		}
		r.Hosts = append(r.Hosts, h)
		if h.Findable && (want == "" || want == h.Host) {
			hosts = append(hosts, h.Host)
		}
	}
	sort.Slice(r.Hosts, func(i, j int) bool { return r.Hosts[i].Host < r.Hosts[j].Host })
	sort.Strings(hosts)
	if want != "" {
		if len(hosts) == 0 {
			return r, api.NewProblem(404, "not_found", want+" is not a findable address of project "+in.Project)
		}
		r.Host = want
	}

	// The days: a period ends on Google's last day (its numbers lag); the
	// crawler log's days end today.
	now := m.now().UTC()
	today := now.Format(time.DateOnly)
	var first, last string
	if m.hist != nil {
		first, last = m.hist.gscSpan(ctx, hosts)
	}
	var from, to time.Time
	switch {
	case in.From != "":
		f, err1 := time.Parse(time.DateOnly, in.From)
		t, err2 := time.Parse(time.DateOnly, in.To)
		if in.To == "" {
			t, err2 = now.Truncate(24*time.Hour), nil
		}
		if err1 != nil || err2 != nil || t.Before(f) || t.Sub(f) > histKeep {
			return r, api.NewProblem(422, "validation", "from must be a day before to, at most 16 months apart")
		}
		from, to = f, t
		r.CrawlFrom, r.CrawlTo = from.Format(time.DateOnly), to.Format(time.DateOnly)
	default:
		n := reportDays[in.Period]
		if n == 0 {
			n = 30
		}
		to = now.Truncate(24*time.Hour).AddDate(0, 0, -1)
		if lt, err := time.Parse(time.DateOnly, last); err == nil && now.Sub(lt) < 7*24*time.Hour {
			to = lt
		}
		from = to.AddDate(0, 0, 1-n)
		r.CrawlFrom, r.CrawlTo = now.AddDate(0, 0, 1-n).Format(time.DateOnly), today
	}
	days := int(to.Sub(from)/(24*time.Hour)) + 1
	r.From, r.To = from.Format(time.DateOnly), to.Format(time.DateOnly)
	r.PrevTo = from.AddDate(0, 0, -1).Format(time.DateOnly)
	r.PrevFrom = from.AddDate(0, 0, -days).Format(time.DateOnly)

	m.reportIndex(ctx, &r, hosts)
	m.reportGoogle(ctx, &r, hosts, first)
	if r.Google.State == GVerified && m.hist != nil {
		s, err := m.reportSearches(ctx, &r, hosts, last)
		if err != nil {
			return r, err
		}
		r.Searches = s
	}
	return r, m.reportCrawlers(ctx, &r, hosts, now)
}

func (m *Module) reportIndex(ctx context.Context, r *Report, hosts []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r.IndexNow.On = len(hosts) > 0
	var checked time.Time
	for _, h := range hosts {
		rec, _ := m.record(ctx, h)
		if rec == nil {
			continue
		}
		if rec.Last != nil && (r.IndexNow.Last == nil || rec.Last.At.After(r.IndexNow.Last.At)) {
			last := *rec.Last
			last.Sample = nil
			r.IndexNow.Last = &last
		}
		if rec.Checked.IsZero() {
			continue
		}
		if rec.Checked.After(checked) {
			checked = rec.Checked
		}
		c := rec.Checks
		r.IndexNow.Pages += c.Sitemap.Pages
		for _, x := range []struct {
			what string
			c    Check
		}{{"robots.txt", c.Robots}, {"Home page", c.Home}, {"Sitemap", c.Sitemap.Check}, {"IndexNow key file", c.Key}} {
			if !x.c.OK {
				p := x.what + ": " + x.c.Detail
				if len(hosts) > 1 {
					p = h + " · " + p
				}
				r.IndexNow.Problems = append(r.IndexNow.Problems, p)
			}
		}
	}
	if !checked.IsZero() {
		r.IndexNow.Checked = &checked
	}
}

func (m *Module) reportGoogle(ctx context.Context, r *Report, hosts []string, first string) {
	g := &r.Google
	if len(hosts) == 0 {
		g.State = "none"
		return
	}
	g.First = first
	var sm GSitemap
	var smOK bool
	var idx GIndexCount
	for i, h := range hosts {
		gh := m.googleHost(ctx, h)
		// One verified address makes the report Google's; else the first one's state.
		if i == 0 || gh.State == GVerified && g.State != GVerified {
			g.State, g.Property, g.Detail = gh.State, gh.Property, gh.Detail
		}
		if gh.Error != "" && g.Error == "" {
			g.Error = gh.Error
		}
		if gh.PulledAt != nil && (g.PulledAt == nil || gh.PulledAt.After(*g.PulledAt)) {
			g.PulledAt = gh.PulledAt
		}
		if gh.Sitemap != nil {
			smOK = true
			sm.Pages += gh.Sitemap.Pages
			sm.Errors += gh.Sitemap.Errors
			sm.Warnings += gh.Sitemap.Warnings
			sm.Pending = sm.Pending || gh.Sitemap.Pending
			if gh.Sitemap.LastRead > sm.LastRead {
				sm.LastRead = gh.Sitemap.LastRead
			}
			if sm.URL == "" {
				sm.URL = gh.Sitemap.URL
			}
		}
		if gh.Index != nil {
			idx.Checked += gh.Index.Checked
			idx.Indexed += gh.Index.Indexed
			idx.NotIndexed = append(idx.NotIndexed, gh.Index.NotIndexed...)
		}
		if d := m.gdataFor(ctx, h); d != nil && gh.State == GVerified && !d.HistDone {
			g.Filling = true
		}
	}
	if smOK {
		g.Sitemap = &sm
	}
	if idx.Checked > 0 {
		sort.Slice(idx.NotIndexed, func(i, j int) bool { return idx.NotIndexed[i].URL < idx.NotIndexed[j].URL })
		g.Index = &idx
	}
}

func totals(days []SearchDay) SearchTotals {
	var t SearchTotals
	var pos float64
	for _, d := range days {
		t.Clicks += d.Clicks
		t.Impressions += d.Impressions
		pos += d.Position * float64(d.Impressions)
	}
	if t.Impressions > 0 {
		t.CTR = round3(float64(t.Clicks) / float64(t.Impressions))
		t.Position = round1(pos / float64(t.Impressions))
	}
	return t
}

func (m *Module) reportSearches(ctx context.Context, r *Report, hosts []string, last string) (*Searches, error) {
	s := &Searches{Through: last, Daily: []SearchDay{}, PrevDays: []SearchDay{}, Queries: []GTop{}, Pages: []GTop{}}
	var err error
	if s.Daily, err = m.hist.gscDays(ctx, hosts, r.From, r.To); err != nil {
		return nil, err
	}
	if s.PrevDays, err = m.hist.gscDays(ctx, hosts, r.PrevFrom, r.PrevTo); err != nil {
		return nil, err
	}
	s.Totals, s.Previous = totals(s.Daily), totals(s.PrevDays)
	if s.Queries, err = m.hist.gscTop(ctx, hosts, "gsc_query", "query", r.From, r.To, 50); err != nil {
		return nil, err
	}
	if s.Pages, err = m.hist.gscTop(ctx, hosts, "gsc_page", "page", r.From, r.To, 50); err != nil {
		return nil, err
	}
	for i := range s.Pages {
		s.Pages[i].Key = pageKey(s.Pages[i].Key, hosts)
	}
	if s.Previous.Impressions > 0 {
		if s.Moved, err = m.moved(ctx, r, hosts); err != nil {
			return nil, err
		}
	}
	// Never null: an empty list reads as "nothing in these days".
	for _, l := range []*[]SearchDay{&s.Daily, &s.PrevDays} {
		if *l == nil {
			*l = []SearchDay{}
		}
	}
	for _, l := range []*[]GTop{&s.Queries, &s.Pages} {
		if *l == nil {
			*l = []GTop{}
		}
	}
	return s, nil
}

// pageKey is a page as the report shows it: a path for one address, host
// and path for several.
func pageKey(u string, hosts []string) string {
	k := strings.TrimPrefix(u, "https://")
	if len(hosts) == 1 {
		k = strings.TrimPrefix(k, hosts[0])
	}
	return k
}

// movedMax is how many risers and fallers each list keeps; moveDepth how
// many searches or pages per period are compared.
const (
	movedMax  = 10
	moveDepth = 1000
)

// moved compares the searches and pages of the days shown with the days before.
func (m *Module) moved(ctx context.Context, r *Report, hosts []string) (*Moved, error) {
	out := &Moved{}
	for _, t := range []struct {
		table, col string
		up, down   *[]Mover
	}{{"gsc_query", "query", &out.QueriesUp, &out.QueriesDown}, {"gsc_page", "page", &out.PagesUp, &out.PagesDown}} {
		now, err := m.hist.gscTop(ctx, hosts, t.table, t.col, r.From, r.To, moveDepth)
		if err != nil {
			return nil, err
		}
		before, err := m.hist.gscTop(ctx, hosts, t.table, t.col, r.PrevFrom, r.PrevTo, moveDepth)
		if err != nil {
			return nil, err
		}
		*t.up, *t.down = movers(now, before)
		if t.col == "page" {
			for _, l := range []*[]Mover{t.up, t.down} {
				for i := range *l {
					(*l)[i].Key = pageKey((*l)[i].Key, hosts)
				}
			}
		}
	}
	return out, nil
}

// movers ranks what changed between two periods: gains (most clicks gained,
// then impressions) and losses, movedMax of each.
func movers(now, before []GTop) (up, down []Mover) {
	all := map[string]*Mover{}
	get := func(k string) *Mover {
		if all[k] == nil {
			all[k] = &Mover{Key: k}
		}
		return all[k]
	}
	for _, t := range now {
		x := get(t.Key)
		x.Clicks, x.Impressions, x.Position = t.Clicks, t.Impressions, t.Position
	}
	for _, t := range before {
		x := get(t.Key)
		x.PrevClicks, x.PrevImpressions, x.PrevPosition = t.Clicks, t.Impressions, t.Position
	}
	// A move: clicks decide; with the same clicks, impressions do.
	gain := func(x *Mover) (int, int) { return x.Clicks - x.PrevClicks, x.Impressions - x.PrevImpressions }
	up, down = []Mover{}, []Mover{}
	for _, x := range all {
		c, i := gain(x)
		switch {
		case c > 0 || c == 0 && i > 0:
			up = append(up, *x)
		case c < 0 || c == 0 && i < 0:
			down = append(down, *x)
		}
	}
	rank := func(l []Mover, sign int) {
		sort.Slice(l, func(a, b int) bool {
			ca, ia := gain(&l[a])
			cb, ib := gain(&l[b])
			if ca != cb {
				return sign*ca > sign*cb
			}
			if ia != ib {
				return sign*ia > sign*ib
			}
			return l[a].Key < l[b].Key
		})
	}
	rank(up, 1)
	rank(down, -1)
	return up[:min(len(up), movedMax)], down[:min(len(down), movedMax)]
}

func (m *Module) reportCrawlers(ctx context.Context, r *Report, hosts []string, now time.Time) error {
	if len(hosts) == 0 {
		return nil
	}
	sums := map[string][2]int{}
	if m.hist != nil {
		var err error
		if sums, r.Fakes, err = m.hist.crawlTotals(ctx, hosts, r.CrawlFrom, r.CrawlTo); err != nil {
			return err
		}
		if r.CrawlDays, err = m.hist.crawlDays(ctx, hosts, r.CrawlFrom, r.CrawlTo); err != nil {
			return err
		}
	}
	// The last visit of each bot, from the crawler log (30 days).
	lastSeen := map[string]Crawler{}
	for _, h := range hosts {
		cs, _ := m.crawled(ctx, h, now)
		for _, c := range cs {
			if c.LastAt.After(lastSeen[c.Bot].LastAt) {
				lastSeen[c.Bot] = c
			}
		}
	}
	for _, b := range bots {
		n := sums[b.Name]
		if n[0] == 0 {
			continue // not in these days
		}
		c, seen := lastSeen[b.Name]
		x := Crawled{Bot: b.Name, Operator: b.Operator, Purpose: b.Purpose, AI: aiBot(b.Name), Hits: n[0], Errors: n[1]}
		if seen {
			at := c.LastAt
			x.LastAt, x.LastPath, x.LastStatus = &at, c.LastPath, c.LastStatus
		}
		r.Crawlers = append(r.Crawlers, x)
	}
	sort.SliceStable(r.Crawlers, func(i, j int) bool { return r.Crawlers[i].Hits > r.Crawlers[j].Hits })
	return nil
}
