package analytics

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Web Vitals arrive as beacons on each app's own origin, at VitalsPath (the
// edge sends that path on every app host to the collector): no CORS, no
// third-party host. Samples are counted into log-scaled buckets (5% wide)
// per app, day, page and metric, so p75 and the share of good samples come
// from a few hundred small rows a day, never raw samples.

// VitalsPath is where browsers send Web Vitals.
const VitalsPath = "/_tiffin/vitals"

// Limits that keep beacons cheap.
const (
	maxVitalsBody   = 4 << 10
	vitalsPerIP     = 60   // beacons per minute from one address
	vitalsPerMinute = 6000 // beacons per minute for the whole box
	maxVitalPaths   = 200  // distinct pages per app and day; the rest count as (other)
	vitalGrowth     = 1.05 // bucket width
)

// vitalSpec is one metric: its unit scale, plausible maximum and Google's
// good / poor thresholds.
type vitalSpec struct {
	scale      float64 // stored value = reported value * scale (CLS has no unit)
	max        float64
	good, poor float64
	unit       string
}

var vitalSpecs = map[string]vitalSpec{
	"LCP":  {1, 120000, 2500, 4000, "ms"},
	"INP":  {1, 60000, 200, 500, "ms"},
	"CLS":  {1000, 100, 0.1, 0.25, ""},
	"FCP":  {1, 120000, 1800, 3000, "ms"},
	"TTFB": {1, 120000, 800, 1800, "ms"},
}

var vitalOrder = []string{"LCP", "INP", "CLS", "FCP", "TTFB"}

func vitalBucket(v float64) int {
	if v < 1 {
		return 0
	}
	return 1 + int(math.Log(v)/math.Log(vitalGrowth))
}

func bucketValue(b int) float64 {
	if b == 0 {
		return 0
	}
	return math.Pow(vitalGrowth, float64(b-1)+0.5)
}

// VitalCount is one stored bucket.
type VitalCount struct {
	Project, App, Day, Path, Metric string
	Bucket                          int
	N                               int64
}

type vitalKey struct {
	project, app, day, path, metric string
	bucket                          int
}

// Vitals counts beacons in memory and writes them every few seconds.
type Vitals struct {
	Store Store

	mu      sync.Mutex
	counts  map[vitalKey]int64
	minute  int64
	perIP   map[string]int
	total   int
	limited int64
}

func (v *Vitals) allow(ip string, now time.Time) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if m := now.Unix() / 60; m != v.minute {
		v.minute, v.perIP, v.total = m, map[string]int{}, 0
	}
	if v.total >= vitalsPerMinute || v.perIP[ip] >= vitalsPerIP {
		v.limited++
		return false
	}
	v.total++
	v.perIP[ip]++
	return true
}

func (v *Vitals) add(project, app, path string, at time.Time, metrics map[string]float64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.counts == nil {
		v.counts = map[vitalKey]int64{}
	}
	for name, val := range metrics {
		v.counts[vitalKey{project, app, dayOf(at), path, name, vitalBucket(val * vitalSpecs[name].scale)}]++
	}
}

// Flush writes the counted samples.
func (v *Vitals) Flush(ctx context.Context) error {
	v.mu.Lock()
	counts := v.counts
	v.counts = nil
	v.mu.Unlock()
	if len(counts) == 0 {
		return nil
	}
	rows := make([]VitalCount, 0, len(counts))
	for k, n := range counts {
		rows = append(rows, VitalCount{k.project, k.app, k.day, k.path, k.metric, k.bucket, n})
	}
	return v.Store.AddVitals(ctx, rows)
}

// Run flushes every five seconds until ctx ends.
func (v *Vitals) Run(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = v.Flush(context.Background())
			return
		case <-t.C:
			_ = v.Flush(ctx)
		}
	}
}

var (
	hexID      = regexp.MustCompile(`^[0-9a-fA-F-]{16,}$`)
	pathClean  = regexp.MustCompile(`[^A-Za-z0-9/_.~\[\]:@!$&'()*+,;=%-]`)
	multiSlash = regexp.MustCompile(`/{2,}`)
)

// isID reports a path segment that is an ID rather than a name: a number,
// a UUID or hex hash, or a long token with digits that is not a slug.
func isID(s string) bool {
	digits := strings.ContainsAny(s, "0123456789")
	switch {
	case s == "" || !digits:
		return false
	case strings.Trim(s, "0123456789") == "", hexID.MatchString(s):
		return true
	}
	return len(s) >= 20 && !strings.Contains(s, "-")
}

// vitalPath turns what the page reports (a route like /products/[id], or
// a path) into a bounded page key: no query, IDs folded into [id].
func vitalPath(p string) string {
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = multiSlash.ReplaceAllString(pathClean.ReplaceAllString(p, ""), "/")
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if isID(s) {
			segs[i] = "[id]"
		}
	}
	if len(segs) > 8 {
		segs = append(segs[:8], "…")
	}
	return clipString(strings.Join(segs, "/"), 200)
}

func clipString(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

type vitalsBeacon struct {
	Path    string             `json:"path"`
	Metrics map[string]float64 `json:"metrics"`
}

// vitalsBeacon accepts Web Vitals from a page of an app with analytics.
func (m *Module) vitalsBeacon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		reply(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var b vitalsBeacon
	if err := json.NewDecoder(io.LimitReader(r.Body, maxVitalsBody)).Decode(&b); err != nil || len(b.Metrics) == 0 {
		reply(w, http.StatusBadRequest, map[string]string{"error": `body must be JSON {"path": "/", "metrics": {"LCP": 1840, "CLS": 0.02}}`})
		return
	}
	path := vitalPath(b.Path)
	site, ok := m.sites.Lookup(r.Context(), r.Host, path)
	if !ok || !site.Analytics {
		reply(w, http.StatusNotFound, map[string]string{"error": "no app with analytics serves " + r.Host})
		return
	}
	if m.pipe.Bots.IsBot(r.Header.Get("User-Agent")) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !m.vit.allow(clientIP(r), time.Now()) {
		w.Header().Set("Retry-After", "60")
		reply(w, http.StatusTooManyRequests, map[string]string{"error": "too many beacons"})
		return
	}
	good := map[string]float64{}
	for name, v := range b.Metrics {
		spec, known := vitalSpecs[strings.ToUpper(name)]
		if !known || v < 0 || v > spec.max || math.IsNaN(v) {
			continue
		}
		good[strings.ToUpper(name)] = v
	}
	if len(good) == 0 {
		reply(w, http.StatusBadRequest, map[string]string{"error": "metrics are LCP, INP, CLS, FCP and TTFB, in milliseconds (CLS without a unit)"})
		return
	}
	m.vit.add(site.Project, site.App, path, time.Now(), good)
	w.WriteHeader(http.StatusNoContent)
}

// ---- reading ----

// VitalSummary is one metric over a period.
type VitalSummary struct {
	Name    string  `json:"name" enum:"LCP,INP,CLS,FCP,TTFB"`
	P75     float64 `json:"p75" doc:"75th percentile: milliseconds, or a score for CLS"`
	Unit    string  `json:"unit" doc:"ms, or empty for CLS"`
	Rating  string  `json:"rating" enum:"good,needs-improvement,poor" doc:"Google's thresholds applied to the p75"`
	Good    float64 `json:"good" doc:"Share of samples rated good, 0-1"`
	Samples int64   `json:"samples"`
}

// PageVitals is the p75 of each metric for one page.
type PageVitals struct {
	Path    string             `json:"path" doc:"The page, as the page reported it (a route like /products/[id] with the SDK), with IDs folded into [id]"`
	Samples int64              `json:"samples"`
	P75     map[string]float64 `json:"p75"`
	Ratings map[string]string  `json:"ratings" doc:"Google's rating of each p75: good, needs-improvement or poor"`
}

// DayVitals is the p75 of each metric for one day.
type DayVitals struct {
	Day string             `json:"day"`
	P75 map[string]float64 `json:"p75"`
}

// VitalsView is Web Vitals for a project or one app over a period.
type VitalsView struct {
	Project string         `json:"project"`
	App     string         `json:"app,omitempty"`
	Period  string         `json:"period"`
	From    time.Time      `json:"from"`
	To      time.Time      `json:"to"`
	Metrics []VitalSummary `json:"metrics" doc:"Only metrics with samples"`
	Pages   []PageVitals   `json:"pages" doc:"Pages with the most samples first"`
	Days    []DayVitals    `json:"days"`
}

type hist map[int]int64

func (h hist) total() int64 {
	var n int64
	for _, c := range h {
		n += c
	}
	return n
}

// quantile returns the value at q by nearest rank, in reported units.
func (h hist) quantile(q, scale float64) float64 {
	n := h.total()
	if n == 0 {
		return 0
	}
	bs := make([]int, 0, len(h))
	for b := range h {
		bs = append(bs, b)
	}
	sort.Ints(bs)
	rank := int64(math.Ceil(q * float64(n)))
	var cum int64
	for _, b := range bs {
		if cum += h[b]; cum >= rank {
			return roundVital(bucketValue(b)/scale, scale)
		}
	}
	return 0
}

func (h hist) share(atMost, scale float64) float64 {
	n := h.total()
	if n == 0 {
		return 0
	}
	var good int64
	for b, c := range h {
		if bucketValue(b)/scale <= atMost {
			good += c
		}
	}
	return math.Round(float64(good)/float64(n)*1000) / 1000
}

func roundVital(v, scale float64) float64 {
	if scale > 1 {
		return math.Round(v*1000) / 1000
	}
	return math.Round(v)
}

func rating(name string, p75 float64) string {
	s := vitalSpecs[name]
	switch {
	case p75 <= s.good:
		return "good"
	case p75 <= s.poor:
		return "needs-improvement"
	}
	return "poor"
}

// VitalsFor summarises stored vitals between two days.
func (m *Module) VitalsFor(ctx context.Context, q Query, limit int) (*VitalsView, error) {
	if err := m.vit.Flush(ctx); err != nil {
		return nil, err
	}
	rows, err := m.store.Vitals(ctx, q)
	if err != nil {
		return nil, err
	}
	all := map[string]hist{}
	pages := map[string]map[string]hist{}
	days := map[string]map[string]hist{}
	get := func(m map[string]map[string]hist, k, metric string) hist {
		if m[k] == nil {
			m[k] = map[string]hist{}
		}
		if m[k][metric] == nil {
			m[k][metric] = hist{}
		}
		return m[k][metric]
	}
	for _, r := range rows {
		if all[r.Metric] == nil {
			all[r.Metric] = hist{}
		}
		all[r.Metric][r.Bucket] += r.N
		get(pages, r.Path, r.Metric)[r.Bucket] += r.N
		get(days, r.Day, r.Metric)[r.Bucket] += r.N
	}
	v := &VitalsView{Project: q.Project, App: q.App, From: q.From, To: q.To, Metrics: []VitalSummary{}, Pages: []PageVitals{}, Days: []DayVitals{}}
	for _, name := range vitalOrder {
		h := all[name]
		if h.total() == 0 {
			continue
		}
		s := vitalSpecs[name]
		p := h.quantile(0.75, s.scale)
		v.Metrics = append(v.Metrics, VitalSummary{Name: name, P75: p, Unit: s.unit, Rating: rating(name, p), Good: h.share(s.good, s.scale), Samples: h.total()})
	}
	p75s := func(hs map[string]hist) (map[string]float64, int64) {
		out := map[string]float64{}
		var most int64
		for name, h := range hs {
			out[name] = h.quantile(0.75, vitalSpecs[name].scale)
			most = max(most, h.total())
		}
		return out, most
	}
	for path, hs := range pages {
		p, n := p75s(hs)
		r := map[string]string{}
		for name, x := range p {
			r[name] = rating(name, x)
		}
		v.Pages = append(v.Pages, PageVitals{Path: path, Samples: n, P75: p, Ratings: r})
	}
	sort.Slice(v.Pages, func(i, j int) bool {
		if v.Pages[i].Samples != v.Pages[j].Samples {
			return v.Pages[i].Samples > v.Pages[j].Samples
		}
		return v.Pages[i].Path < v.Pages[j].Path
	})
	if len(v.Pages) > limit {
		v.Pages = v.Pages[:limit]
	}
	for day, hs := range days {
		p, _ := p75s(hs)
		v.Days = append(v.Days, DayVitals{Day: day, P75: p})
	}
	sort.Slice(v.Days, func(i, j int) bool { return v.Days[i].Day < v.Days[j].Day })
	return v, nil
}
