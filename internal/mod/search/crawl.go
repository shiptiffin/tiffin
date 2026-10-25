package search

import (
	"bytes"
	"context"
	"encoding/json"
	"net/netip"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shiptiffin/tiffin/internal/mod/observe/edgelog"
	"github.com/shiptiffin/tiffin/internal/mod/observe/logtail"
)

// The crawler log: which search and AI bots fetched which pages of each
// findable host, from the edge's access log. A bot counts only when its
// address is in the list its operator publishes; a user agent that claims
// to be Googlebot from anywhere else is counted apart, as a fake.

// bot is a crawler the box recognises.
type bot struct {
	token    string // lowercased, matched in the user agent
	Name     string
	Operator string
	Purpose  string // search, user (fetches for someone's question) or training
	list     string
}

// Published address lists (all {"prefixes": [{"ipv4Prefix"|"ipv6Prefix"}]}).
const (
	listGoogle     = "https://developers.google.com/static/crawling/ipranges/common-crawlers.json"
	listBing       = "https://www.bing.com/toolbox/bingbot.json"
	listOAISearch  = "https://openai.com/searchbot.json"
	listChatGPT    = "https://openai.com/chatgpt-user.json"
	listGPTBot     = "https://openai.com/gptbot.json"
	listAnthropic  = "https://claude.com/crawling/bots.json"
	listPerplexity = "https://www.perplexity.com/perplexitybot.json"
	listPplxUser   = "https://www.perplexity.com/perplexity-user.json"
	listApple      = "https://search.developer.apple.com/applebot.json"
	listDDG        = "https://duckduckgo.com/duckduckbot.json"
)

// bots in match order: a more specific token before one it contains.
var bots = []bot{
	{"googlebot", "Googlebot", "Google", "search", listGoogle},
	{"bingbot", "Bingbot", "Bing", "search", listBing},
	{"oai-searchbot", "OAI-SearchBot", "OpenAI", "search", listOAISearch},
	{"chatgpt-user", "ChatGPT-User", "OpenAI", "user", listChatGPT},
	{"gptbot", "GPTBot", "OpenAI", "training", listGPTBot},
	{"claude-searchbot", "Claude-SearchBot", "Anthropic", "search", listAnthropic},
	{"claude-user", "Claude-User", "Anthropic", "user", listAnthropic},
	{"claudebot", "ClaudeBot", "Anthropic", "training", listAnthropic},
	{"perplexity-user", "Perplexity-User", "Perplexity", "user", listPplxUser},
	{"perplexitybot", "PerplexityBot", "Perplexity", "search", listPerplexity},
	{"applebot", "Applebot", "Apple", "search", listApple},
	{"duckduckbot", "DuckDuckBot", "DuckDuckGo", "search", listDDG},
}

func botFor(ua string) *bot {
	ua = strings.ToLower(ua)
	for i := range bots {
		if strings.Contains(ua, bots[i].token) {
			return &bots[i]
		}
	}
	return nil
}

const (
	crawlDays     = 30   // days of counts kept
	crawlPagesMax = 2000 // pages remembered per host and bot
	listsEvery    = 24 * time.Hour
)

// Crawler is one bot's visits to a host.
type Crawler struct {
	Bot        string    `json:"bot" doc:"Googlebot, Bingbot, OAI-SearchBot, ChatGPT-User, GPTBot, Claude-SearchBot, Claude-User, ClaudeBot, PerplexityBot, Perplexity-User, Applebot, DuckDuckBot"`
	Operator   string    `json:"operator"`
	Purpose    string    `json:"purpose" enum:"search,user,training" doc:"search: builds a search index; user: fetches a page for someone's question in a chat; training: collects pages to train models"`
	LastAt     time.Time `json:"lastAt"`
	LastPath   string    `json:"lastPath"`
	LastStatus int       `json:"lastStatus"`
	Hits7d     int       `json:"hits7d" doc:"Requests in the last 7 days"`
	Pages7d    int       `json:"pages7d" doc:"Different pages fetched in the last 7 days"`
	Errors7d   int       `json:"errors7d" doc:"Requests in the last 7 days that answered 4xx or 5xx"`
}

type dayCount struct {
	Hits   int `json:"h"`
	Errors int `json:"e,omitempty"`
}

type botSeen struct {
	LastAt     time.Time            `json:"lastAt"`
	LastPath   string               `json:"lastPath"`
	LastStatus int                  `json:"lastStatus"`
	Days       map[string]*dayCount `json:"days"`
	Pages      map[string]time.Time `json:"pages"`
}

type crawlRecord struct {
	Bots  map[string]*botSeen `json:"bots"`
	Fakes map[string]int      `json:"fakes,omitempty"` // day → claims from outside the lists
}

type crawlLog struct {
	mu      sync.Mutex
	tracked map[string]bool // findable hosts
	recs    map[string]*crawlRecord
	dirty   map[string]bool
	lists   map[string][]netip.Prefix
}

func (m *Module) crawlKV(host string) string { return "crawl/" + host }

// startCrawlLog loads the address lists and starts reading the access log.
func (m *Module) startCrawlLog(ctx context.Context, root string) {
	cl := m.crawl
	if raw, ok, err := m.p.DB.KVGet(ctx, kvNS, "iplists"); err == nil && ok {
		var stored map[string][]string
		if json.Unmarshal(raw, &stored) == nil {
			cl.mu.Lock()
			cl.lists = parseLists(stored)
			cl.mu.Unlock()
		}
	}
	go m.listsLoop(ctx)
	tl := &logtail.Tailer{Path: filepath.Join(root, "logs", "access.log"), FromStart: true,
		Load: func() (logtail.Position, bool) {
			var pos logtail.Position
			b, ok, err := m.p.DB.KVGet(ctx, kvNS, "access-log")
			if err != nil || !ok || json.Unmarshal(b, &pos) != nil {
				return pos, false
			}
			return pos, true
		},
		Save: func(pos logtail.Position) {
			b, _ := json.Marshal(pos)
			_ = m.p.DB.KVPut(ctx, kvNS, "access-log", b)
		},
		Line: func(line []byte) { m.crawlLine(ctx, line) }}
	go tl.Run(ctx)
}

func parseLists(stored map[string][]string) map[string][]netip.Prefix {
	out := map[string][]netip.Prefix{}
	for u, ps := range stored {
		for _, s := range ps {
			if p, err := netip.ParsePrefix(s); err == nil {
				out[u] = append(out[u], p)
			}
		}
	}
	return out
}

// listsLoop fetches the published address lists now and once a day; a
// list that fails to load keeps its last copy.
func (m *Module) listsLoop(ctx context.Context) {
	for {
		m.fetchLists(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(listsEvery):
		}
	}
}

func (m *Module) fetchLists(ctx context.Context) {
	cl := m.crawl
	cl.mu.Lock()
	stored := map[string][]string{}
	for u, ps := range cl.lists {
		for _, p := range ps {
			stored[u] = append(stored[u], p.String())
		}
	}
	cl.mu.Unlock()
	seen := map[string]bool{}
	for _, b := range bots {
		if seen[b.list] {
			continue
		}
		seen[b.list] = true
		status, _, body, err := m.get(ctx, b.list, 4<<20)
		if err != nil || status != 200 {
			continue
		}
		var doc struct {
			Prefixes []struct {
				V4 string `json:"ipv4Prefix"`
				V6 string `json:"ipv6Prefix"`
			} `json:"prefixes"`
		}
		if json.Unmarshal(body, &doc) != nil || len(doc.Prefixes) == 0 {
			continue
		}
		var ps []string
		for _, p := range doc.Prefixes {
			if s := p.V4 + p.V6; s != "" {
				ps = append(ps, s)
			}
		}
		stored[b.list] = ps
	}
	raw, _ := json.Marshal(stored)
	_ = m.p.DB.KVPut(ctx, kvNS, "iplists", raw)
	parsed := parseLists(stored)
	cl.mu.Lock()
	cl.lists = parsed
	cl.mu.Unlock()
}

// verified reports whether ip is in the bot's published list.
func (cl *crawlLog) verified(b *bot, ip string) bool {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, p := range cl.lists[b.list] {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// crawlLine records one access log line when it is a known bot on a
// findable host.
func (m *Module) crawlLine(ctx context.Context, line []byte) {
	if !bytes.Contains(line, []byte("bot")) && !bytes.Contains(line, []byte("Bot")) && !bytes.Contains(line, []byte("-User")) {
		return // cheap: every bot here says one of these
	}
	e, ok := edgelog.Parse(line)
	if !ok {
		return
	}
	b := botFor(e.Header("User-Agent"))
	if b == nil {
		return
	}
	m.mu.Lock()
	keyFile := "/" + m.key + ".txt"
	m.mu.Unlock()
	if strings.HasPrefix(e.URI, keyFile) {
		return // an engine checking the key, not reading a page
	}
	cl := m.crawl
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if !cl.tracked[e.Host] {
		return
	}
	r := cl.load(ctx, m, e.Host)
	day := e.Time.UTC().Format(time.DateOnly)
	if !cl.verified(b, e.ClientIP) {
		if r.Fakes == nil {
			r.Fakes = map[string]int{}
		}
		r.Fakes[day]++
		cl.dirty[e.Host] = true
		return
	}
	s := r.Bots[b.Name]
	if s == nil {
		s = &botSeen{Days: map[string]*dayCount{}, Pages: map[string]time.Time{}}
		r.Bots[b.Name] = s
	}
	path, _, _ := strings.Cut(e.URI, "?")
	if e.Time.After(s.LastAt) {
		s.LastAt, s.LastPath, s.LastStatus = e.Time, path, e.Status
	}
	d := s.Days[day]
	if d == nil {
		d = &dayCount{}
		s.Days[day] = d
	}
	d.Hits++
	if e.Status >= 400 {
		d.Errors++
	}
	if t, ok := s.Pages[path]; !ok || e.Time.After(t) {
		s.Pages[path] = e.Time
	}
	if len(s.Pages) > crawlPagesMax {
		trimPages(s.Pages, crawlPagesMax*9/10)
	}
	cl.dirty[e.Host] = true
}

// load returns host's record, read from the store on first use (cl.mu held).
func (cl *crawlLog) load(ctx context.Context, m *Module, host string) *crawlRecord {
	if r := cl.recs[host]; r != nil {
		return r
	}
	r := &crawlRecord{}
	if raw, ok, err := m.p.DB.KVGet(ctx, kvNS, m.crawlKV(host)); err == nil && ok {
		_ = json.Unmarshal(raw, r)
	}
	if r.Bots == nil {
		r.Bots = map[string]*botSeen{}
	}
	cl.recs[host] = r
	return r
}

func trimPages(pages map[string]time.Time, keep int) {
	type pt struct {
		p string
		t time.Time
	}
	all := make([]pt, 0, len(pages))
	for p, t := range pages {
		all = append(all, pt{p, t})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].t.After(all[j].t) })
	for _, x := range all[keep:] {
		delete(pages, x.p)
	}
}

// track sets the findable hosts, saves what changed and drops old days.
func (m *Module) track(ctx context.Context, hosts map[string]bool, now time.Time) {
	cl := m.crawl
	cl.mu.Lock()
	defer cl.mu.Unlock()
	cl.tracked = hosts
	oldest := now.AddDate(0, 0, -crawlDays).Format(time.DateOnly)
	for host := range cl.recs {
		if !hosts[host] {
			delete(cl.recs, host)
			delete(cl.dirty, host)
			_ = m.p.DB.KVDelete(ctx, kvNS, m.crawlKV(host))
		}
	}
	for host := range cl.dirty {
		r := cl.recs[host]
		if r == nil {
			continue
		}
		for _, s := range r.Bots {
			for d := range s.Days {
				if d < oldest {
					delete(s.Days, d)
				}
			}
		}
		for d := range r.Fakes {
			if d < oldest {
				delete(r.Fakes, d)
			}
		}
		raw, _ := json.Marshal(r)
		_ = m.p.DB.KVPut(ctx, kvNS, m.crawlKV(host), raw)
	}
	cl.dirty = map[string]bool{}
}

// crawled is what bots did on host, most recent first, and how many fake
// claims came in the last 7 days.
func (m *Module) crawled(ctx context.Context, host string, now time.Time) ([]Crawler, int) {
	cl := m.crawl
	cl.mu.Lock()
	defer cl.mu.Unlock()
	r := cl.load(ctx, m, host)
	week := now.AddDate(0, 0, -6).Format(time.DateOnly)
	since := now.Add(-7 * 24 * time.Hour)
	out := []Crawler{}
	for _, b := range bots {
		s := r.Bots[b.Name]
		if s == nil {
			continue
		}
		c := Crawler{Bot: b.Name, Operator: b.Operator, Purpose: b.Purpose, LastAt: s.LastAt, LastPath: s.LastPath, LastStatus: s.LastStatus}
		for d, n := range s.Days {
			if d >= week {
				c.Hits7d += n.Hits
				c.Errors7d += n.Errors
			}
		}
		for _, t := range s.Pages {
			if t.After(since) {
				c.Pages7d++
			}
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastAt.After(out[j].LastAt) })
	fakes := 0
	for d, n := range r.Fakes {
		if d >= week {
			fakes += n
		}
	}
	return out, fakes
}
