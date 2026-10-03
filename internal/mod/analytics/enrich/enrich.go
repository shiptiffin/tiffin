// Package enrich turns a raw hit (IP, user agent, URL, referrer) into the
// privacy-safe fields analytics stores: country, browser, OS, device type,
// referrer source, UTM tags and a clean path. Raw IPs and user agents are
// never returned from here, only what is derived from them.
//
// Data sources and their licences:
//   - bot list: isbot (Unlicense), isbot-patterns.json
//   - user agents: uap-go and its regexes (Apache-2.0)
//   - countries: DB-IP Lite (CC-BY-4.0, attribution required:
//     "IP Geolocation by DB-IP", https://db-ip.com)
package enrich

import (
	_ "embed"
	"encoding/json"
	"net"
	"net/url"
	"strings"
	"sync"

	"github.com/dlclark/regexp2/v2"
	"github.com/oschwald/maxminddb-golang/v2"
	"github.com/ua-parser/uap-go/uaparser"
	"net/netip"
)

//go:embed isbot-patterns.json
var isbotJSON []byte

// Bots detects crawlers, monitors, previews and scripts by user agent.
type Bots struct {
	re    *regexp2.Regexp
	mu    sync.Mutex
	cache map[string]bool
}

// NewBots compiles the isbot list (it uses lookarounds, hence regexp2).
func NewBots() *Bots {
	var pats []string
	if err := json.Unmarshal(isbotJSON, &pats); err != nil {
		panic("enrich: isbot-patterns.json: " + err.Error())
	}
	re := regexp2.MustCompile(strings.Join(pats, "|"), regexp2.IgnoreCase|regexp2.ECMAScript)
	return &Bots{re: re, cache: map[string]bool{}}
}

// IsBot reports whether ua looks automated. An empty user agent is a bot.
func (b *Bots) IsBot(ua string) bool {
	ua = strings.TrimSpace(ua)
	if ua == "" || len(ua) < 8 {
		return true
	}
	b.mu.Lock()
	v, ok := b.cache[ua]
	b.mu.Unlock()
	if ok {
		return v
	}
	m, err := b.re.MatchString(ua)
	v = err == nil && m
	lower := strings.ToLower(ua)
	if strings.Contains(lower, "headless") || strings.Contains(lower, "lighthouse") || strings.Contains(lower, "pingdom") {
		v = true
	}
	b.mu.Lock()
	if len(b.cache) > 20000 {
		b.cache = map[string]bool{}
	}
	b.cache[ua] = v
	b.mu.Unlock()
	return v
}

// Agent is what we keep from a user agent.
type Agent struct {
	Browser string `json:"browser"`
	OS      string `json:"os"`
	Device  string `json:"device"` // desktop, mobile, tablet
}

// Agents parses user agents with uap-go, cached.
type Agents struct {
	p     *uaparser.Parser
	mu    sync.Mutex
	cache map[string]Agent
}

// NewAgents builds the parser from uap-go's embedded regexes.
func NewAgents() *Agents {
	p, err := uaparser.New(uaparser.WithCacheSize(64))
	if err != nil {
		panic("enrich: uap-go: " + err.Error())
	}
	return &Agents{p: p, cache: map[string]Agent{}}
}

// Parse returns the browser, OS and device type for ua.
func (a *Agents) Parse(ua string) Agent {
	a.mu.Lock()
	v, ok := a.cache[ua]
	a.mu.Unlock()
	if ok {
		return v
	}
	c := a.p.Parse(ua)
	v = Agent{Browser: orUnknown(c.UserAgent.Family), OS: orUnknown(c.Os.Family), Device: deviceType(ua, c)}
	a.mu.Lock()
	if len(a.cache) > 20000 {
		a.cache = map[string]Agent{}
	}
	a.cache[ua] = v
	a.mu.Unlock()
	return v
}

func orUnknown(s string) string {
	if s == "" || s == "Other" {
		return "Unknown"
	}
	return s
}

func deviceType(ua string, c *uaparser.Client) string {
	l := strings.ToLower(ua)
	fam := strings.ToLower(c.Device.Family)
	switch {
	case strings.Contains(l, "ipad") || strings.Contains(l, "tablet") || strings.Contains(fam, "tablet") || fam == "ipad" ||
		(strings.Contains(l, "android") && !strings.Contains(l, "mobile")):
		return "tablet"
	case strings.Contains(l, "mobi") || strings.Contains(l, "iphone") || strings.Contains(l, "ipod"):
		return "mobile"
	}
	return "desktop"
}

// Geo maps IPs to ISO country codes with a DB-IP Lite MMDB file.
type Geo struct {
	db *maxminddb.Reader
}

// OpenGeo opens an MMDB file. A missing file is not an error: lookups then
// return "" (shown as "Unknown").
func OpenGeo(path string) (*Geo, error) {
	db, err := maxminddb.Open(path)
	if err != nil {
		return &Geo{}, err
	}
	return &Geo{db: db}, nil
}

// Loaded reports whether a database is open.
func (g *Geo) Loaded() bool { return g != nil && g.db != nil }

// Country returns the two-letter country code for ip, or "".
func (g *Geo) Country(ip string) string {
	if !g.Loaded() {
		return ""
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return ""
	}
	var rec struct {
		CountryCode string `maxminddb:"country_code"`
		Country     struct {
			ISOCode string `maxminddb:"iso_code"`
		} `maxminddb:"country"`
	}
	if err := g.db.Lookup(addr.Unmap()).Decode(&rec); err != nil {
		return ""
	}
	if rec.CountryCode != "" {
		return strings.ToUpper(rec.CountryCode)
	}
	return strings.ToUpper(rec.Country.ISOCode)
}

// Close closes the database.
func (g *Geo) Close() error {
	if g.Loaded() {
		return g.db.Close()
	}
	return nil
}

// Page is a cleaned page URL.
type Page struct {
	Host     string            // lowercased, without port
	Path     string            // path only, no query or fragment; "/" when empty
	UTM      map[string]string // utm_source, utm_medium, utm_campaign, utm_term, utm_content, ref
	Valid    bool
	RawQuery string // kept query: only utm_* and ref
}

// ParsePage parses a full URL (or a request URI with a separate host).
// Query strings are dropped except utm_* and ref.
func ParsePage(rawURL, host string) Page {
	u, err := url.Parse(rawURL)
	if err != nil {
		return Page{}
	}
	h := u.Host
	if h == "" {
		h = host
	}
	p := Page{Host: StripPort(strings.ToLower(h)), Path: u.EscapedPath(), UTM: map[string]string{}, Valid: true}
	if p.Path == "" {
		p.Path = "/"
	}
	if len(p.Path) > 512 {
		p.Path = p.Path[:512]
	}
	keep := url.Values{}
	for k, vs := range u.Query() {
		lk := strings.ToLower(k)
		if (strings.HasPrefix(lk, "utm_") || lk == "ref") && len(vs) > 0 {
			v := vs[0]
			if len(v) > 200 {
				v = v[:200]
			}
			switch lk {
			case "utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content", "ref":
				p.UTM[lk] = v
				keep.Set(lk, v)
			}
		}
	}
	p.RawQuery = keep.Encode()
	return p
}

// StripPort removes a :port suffix.
func StripPort(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return strings.TrimSuffix(h, ".")
}

// knownSources maps referrer hosts (without www.) to friendly names.
var knownSources = map[string]string{
	"google":               "Google",
	"bing.com":             "Bing",
	"duckduckgo.com":       "DuckDuckGo",
	"search.yahoo.com":     "Yahoo",
	"yandex":               "Yandex",
	"baidu.com":            "Baidu",
	"ecosia.org":           "Ecosia",
	"search.brave.com":     "Brave Search",
	"kagi.com":             "Kagi",
	"t.co":                 "X (Twitter)",
	"twitter.com":          "X (Twitter)",
	"x.com":                "X (Twitter)",
	"facebook.com":         "Facebook",
	"m.facebook.com":       "Facebook",
	"l.facebook.com":       "Facebook",
	"lm.facebook.com":      "Facebook",
	"instagram.com":        "Instagram",
	"l.instagram.com":      "Instagram",
	"linkedin.com":         "LinkedIn",
	"lnkd.in":              "LinkedIn",
	"reddit.com":           "Reddit",
	"old.reddit.com":       "Reddit",
	"out.reddit.com":       "Reddit",
	"news.ycombinator.com": "Hacker News",
	"github.com":           "GitHub",
	"youtube.com":          "YouTube",
	"m.youtube.com":        "YouTube",
	"bsky.app":             "Bluesky",
	"threads.net":          "Threads",
	"chatgpt.com":          "ChatGPT",
	"chat.openai.com":      "ChatGPT",
	"claude.ai":            "Claude",
	"perplexity.ai":        "Perplexity",
	"gemini.google.com":    "Gemini",
}

// Referrer normalises a referrer URL relative to the page host. It returns
// the source name ("Google", "news.example.com") and the referrer host, or
// empty strings for direct and internal traffic.
func Referrer(ref, pageHost string, utm map[string]string) (source, host string) {
	if s := utm["utm_source"]; s != "" {
		source = s
	} else if s := utm["ref"]; s != "" {
		source = s
	}
	ref = strings.TrimSpace(ref)
	if ref != "" {
		if u, err := url.Parse(ref); err == nil && (u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "android-app") {
			host = strings.TrimPrefix(StripPort(strings.ToLower(u.Host)), "www.")
		}
	}
	if host != "" && (host == strings.TrimPrefix(pageHost, "www.")) {
		host = "" // internal navigation
	}
	if source == "" && host != "" {
		source = sourceName(host)
	}
	return source, host
}

func sourceName(host string) string {
	if n, ok := knownSources[host]; ok {
		return n
	}
	// google.com, google.co.uk, www.google.de → Google; yandex.ru → Yandex.
	labels := strings.Split(host, ".")
	for _, l := range labels {
		if n, ok := knownSources[l]; ok && (l == "google" || l == "yandex") {
			return n
		}
	}
	return host
}
