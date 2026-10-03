package analytics

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/mod/analytics/enrich"
)

// Hit is one raw observation before enrichment. IP and UA are used to
// derive the visitor hash, country and device, then dropped.
type Hit struct {
	At       time.Time
	Project  string
	App      string
	Kind     string // pageview | event
	Name     string
	URL      string // full URL, or a path with Host set
	Host     string
	Referrer string
	IP       string
	UA       string
	Props    map[string]any
	Src      string // edge | script | server
}

// SessionIdle ends a session after this much inactivity.
const SessionIdle = 30 * time.Minute

type sessKey struct {
	project, app string
	visitor      int64
}

type sess struct {
	id   int64
	last time.Time
}

// Stats count what the pipeline did since start.
type Stats struct {
	Accepted int64 `json:"accepted"`
	Bots     int64 `json:"bots" doc:"Hits dropped as bots, crawlers, monitors or scripts"`
	Invalid  int64 `json:"invalid"`
	Buffered int   `json:"buffered"`
	Failed   int64 `json:"failed" doc:"Events lost because the store refused them"`
}

// Pipeline enriches hits, assigns visitors and sessions, buffers events and
// writes them to the store in batches (every second or 1000 events). It
// also feeds the realtime view.
type Pipeline struct {
	Store  Store
	Bots   *enrich.Bots
	Agents *enrich.Agents
	Geo    *enrich.Geo
	RT     *Realtime
	Now    func() time.Time

	mu       sync.Mutex
	buf      []Event
	sessions map[sessKey]*sess
	dirty    map[[3]string]bool // project, app, day needing a rollup
	stats    Stats
	flushMu  sync.Mutex
}

func (pl *Pipeline) now() time.Time {
	if pl.Now != nil {
		return pl.Now()
	}
	return time.Now()
}

// Stats returns counters.
func (pl *Pipeline) Stats() Stats {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	s := pl.stats
	s.Buffered = len(pl.buf)
	return s
}

func (pl *Pipeline) visitor(ctx context.Context, day, project, app, ip, ua string) int64 {
	if ip == "" && ua == "" {
		return 0
	}
	salt, err := pl.Store.Salt(ctx, day)
	if err != nil {
		return 0
	}
	m := hmac.New(sha256.New, salt)
	m.Write([]byte(project + "\x00" + app + "\x00" + ip + "\x00" + ua))
	v := int64(binary.BigEndian.Uint64(m.Sum(nil)[:8]) >> 1)
	if v == 0 {
		v = 1
	}
	return v
}

func randID() int64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return int64(binary.BigEndian.Uint64(b[:]) >> 1)
}

// Add processes one hit. It returns false (and why) when the hit is dropped.
func (pl *Pipeline) Add(ctx context.Context, h Hit) (bool, string) {
	if h.At.IsZero() {
		h.At = pl.now()
	}
	h.At = h.At.UTC()
	if h.Src != "server" || h.UA != "" {
		if pl.Bots.IsBot(h.UA) {
			pl.mu.Lock()
			pl.stats.Bots++
			pl.mu.Unlock()
			return false, "bot"
		}
	}
	page := enrich.ParsePage(h.URL, h.Host)
	if !page.Valid && h.Kind == "pageview" {
		pl.mu.Lock()
		pl.stats.Invalid++
		pl.mu.Unlock()
		return false, "invalid url"
	}
	day := dayOf(h.At)
	vis := pl.visitor(ctx, day, h.Project, h.App, h.IP, h.UA)
	src, refHost := enrich.Referrer(h.Referrer, page.Host, page.UTM)
	ag := enrich.Agent{}
	if h.UA != "" {
		ag = pl.Agents.Parse(h.UA)
	}
	ev := Event{TS: h.At, Project: h.Project, App: h.App, Kind: h.Kind, Name: h.Name, Host: page.Host, Path: page.Path,
		RefSource: src, RefHost: refHost, UTMSource: page.UTM["utm_source"], UTMMedium: page.UTM["utm_medium"], UTMCamp: page.UTM["utm_campaign"],
		Country: pl.Geo.Country(h.IP), Browser: ag.Browser, OS: ag.OS, Device: ag.Device, Visitor: vis, Src: h.Src}
	if ev.Kind == "pageview" {
		ev.Name = "pageview"
	}
	if len(h.Props) > 0 {
		clean := map[string]any{}
		for k, v := range h.Props {
			if len(clean) >= 30 || len(k) > 64 {
				continue
			}
			switch x := v.(type) {
			case string:
				if len(x) > 500 {
					x = x[:500]
				}
				clean[k] = x
			case float64, bool, int, int64:
				clean[k] = x
			}
		}
		b, _ := json.Marshal(clean)
		ev.Props = string(b)
	}
	pl.mu.Lock()
	if vis != 0 {
		if pl.sessions == nil {
			pl.sessions = map[sessKey]*sess{}
		}
		k := sessKey{h.Project, h.App, vis}
		s := pl.sessions[k]
		if s == nil || h.At.Sub(s.last) > SessionIdle {
			s = &sess{id: randID()}
			pl.sessions[k] = s
		}
		if h.At.After(s.last) {
			s.last = h.At
		}
		ev.Session = s.id
	}
	pl.buf = append(pl.buf, ev)
	pl.stats.Accepted++
	if pl.dirty == nil {
		pl.dirty = map[[3]string]bool{}
	}
	pl.dirty[[3]string{h.Project, h.App, day}] = true
	full := len(pl.buf) >= 1000
	pl.mu.Unlock()
	if pl.RT != nil {
		pl.RT.Add(ev)
	}
	if full {
		go pl.Flush(context.WithoutCancel(ctx))
	}
	return true, ""
}

// Flush writes buffered events. On failure they stay buffered (capped).
func (pl *Pipeline) Flush(ctx context.Context) error {
	pl.flushMu.Lock()
	defer pl.flushMu.Unlock()
	pl.mu.Lock()
	batch := pl.buf
	pl.buf = nil
	pl.mu.Unlock()
	if len(batch) == 0 {
		return nil
	}
	if err := pl.Store.Insert(ctx, batch); err != nil {
		pl.mu.Lock()
		if len(pl.buf)+len(batch) <= 100000 {
			pl.buf = append(batch, pl.buf...)
		} else {
			pl.stats.Failed += int64(len(batch))
		}
		pl.mu.Unlock()
		return err
	}
	return nil
}

// RollupDirty flushes and recomputes the rollups of every day that got
// events since the last call.
func (pl *Pipeline) RollupDirty(ctx context.Context) error {
	if err := pl.Flush(ctx); err != nil {
		return err
	}
	pl.mu.Lock()
	dirty := pl.dirty
	pl.dirty = nil
	pl.mu.Unlock()
	for k := range dirty {
		if err := pl.Store.Rollup(ctx, k[0], k[1], k[2]); err != nil {
			pl.mu.Lock()
			if pl.dirty == nil {
				pl.dirty = map[[3]string]bool{}
			}
			pl.dirty[k] = true
			pl.mu.Unlock()
			return err
		}
	}
	return nil
}

// Restore rebuilds sessions and the realtime view from recent events after
// a restart.
func (pl *Pipeline) Restore(ctx context.Context) error {
	evs, err := pl.Store.Recent(ctx, pl.now().Add(-time.Hour))
	if err != nil {
		return err
	}
	pl.mu.Lock()
	if pl.sessions == nil {
		pl.sessions = map[sessKey]*sess{}
	}
	for _, e := range evs {
		if e.Visitor != 0 && e.Session != 0 {
			pl.sessions[sessKey{e.Project, e.App, e.Visitor}] = &sess{id: e.Session, last: e.TS}
		}
	}
	pl.mu.Unlock()
	if pl.RT != nil {
		for _, e := range evs {
			pl.RT.Add(e)
		}
	}
	return nil
}

// Prune forgets idle sessions.
func (pl *Pipeline) Prune() {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	cut := pl.now().Add(-SessionIdle)
	for k, s := range pl.sessions {
		if s.last.Before(cut) {
			delete(pl.sessions, k)
		}
	}
}

// Run flushes every second, rolls up every 10 seconds and prunes sessions.
func (pl *Pipeline) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	n := 0
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = pl.RollupDirty(fctx)
			cancel()
			return
		case <-t.C:
		}
		n++
		_ = pl.Flush(ctx)
		if n%10 == 0 {
			_ = pl.RollupDirty(ctx)
		}
		if n%60 == 0 {
			pl.Prune()
		}
	}
}

// ---- realtime ----

const rtMinutes = 60

type rtMinute struct {
	minute    int64
	pageviews int64
	events    int64
	visitors  map[int64]struct{}
	pages     map[string]int64
	refs      map[string]int64
	countries map[string]int64
}

// Realtime keeps the last hour per app in memory, minute by minute. It is
// what "right now" reads, so dashboards never touch the event store.
type Realtime struct {
	mu   sync.Mutex
	apps map[[2]string]*[rtMinutes]rtMinute
}

// Add records one event.
func (r *Realtime) Add(e Event) {
	min := e.TS.Unix() / 60
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.apps == nil {
		r.apps = map[[2]string]*[rtMinutes]rtMinute{}
	}
	k := [2]string{e.Project, e.App}
	ring := r.apps[k]
	if ring == nil {
		ring = &[rtMinutes]rtMinute{}
		r.apps[k] = ring
	}
	b := &ring[min%rtMinutes]
	if b.minute != min {
		if b.minute > min {
			return // older than the window
		}
		*b = rtMinute{minute: min, visitors: map[int64]struct{}{}, pages: map[string]int64{}, refs: map[string]int64{}, countries: map[string]int64{}}
	}
	if e.Kind == "event" {
		b.events++
		return
	}
	b.pageviews++
	if e.Visitor != 0 {
		b.visitors[e.Visitor] = struct{}{}
	}
	b.pages[e.Path]++
	if e.RefSource != "" {
		b.refs[e.RefSource]++
	}
	if e.Country != "" {
		b.countries[e.Country]++
	}
}

// RealtimeView is "right now" for one app or a whole project.
type RealtimeView struct {
	Project      string        `json:"project"`
	App          string        `json:"app,omitempty"`
	At           time.Time     `json:"at"`
	VisitorsNow  int           `json:"visitorsNow" doc:"Distinct visitors in the last 5 minutes"`
	Visitors30m  int           `json:"visitors30m" doc:"Distinct visitors in the last 30 minutes"`
	Pageviews30m int64         `json:"pageviews30m"`
	Events30m    int64         `json:"events30m" doc:"Custom events in the last 30 minutes"`
	PerMinute    []MinutePoint `json:"perMinute" doc:"The last 30 minutes, oldest first"`
	TopPages     []Count       `json:"topPages"`
	TopSources   []Count       `json:"topSources"`
	TopCountries []Count       `json:"topCountries"`
}

// MinutePoint is one minute of realtime.
type MinutePoint struct {
	T         time.Time `json:"t"`
	Pageviews int64     `json:"pageviews"`
	Visitors  int       `json:"visitors"`
}

// View aggregates the last 30 minutes for project (and app, if set).
func (r *Realtime) View(project, app string, now time.Time) RealtimeView {
	v := RealtimeView{Project: project, App: app, At: now.UTC(), PerMinute: []MinutePoint{}}
	cur := now.Unix() / 60
	vis5, vis30 := map[int64]struct{}{}, map[int64]struct{}{}
	pages, refs, countries := map[string]int64{}, map[string]int64{}, map[string]int64{}
	per := make([]MinutePoint, 30)
	r.mu.Lock()
	for k, ring := range r.apps {
		if k[0] != project || (app != "" && k[1] != app) {
			continue
		}
		for i := int64(0); i < 30; i++ {
			min := cur - i
			b := &ring[((min%rtMinutes)+rtMinutes)%rtMinutes]
			if b.minute != min {
				continue
			}
			idx := 29 - i
			per[idx].Pageviews += b.pageviews
			v.Pageviews30m += b.pageviews
			v.Events30m += b.events
			for id := range b.visitors {
				vis30[id] = struct{}{}
				if i < 5 {
					vis5[id] = struct{}{}
				}
			}
			per[idx].Visitors += len(b.visitors)
			for p, n := range b.pages {
				pages[p] += n
			}
			for p, n := range b.refs {
				refs[p] += n
			}
			for p, n := range b.countries {
				countries[p] += n
			}
		}
	}
	r.mu.Unlock()
	for i := range per {
		per[i].T = time.Unix((cur-int64(29-i))*60, 0).UTC()
	}
	v.PerMinute = per
	v.VisitorsNow, v.Visitors30m = len(vis5), len(vis30)
	v.TopPages, v.TopSources, v.TopCountries = topN(pages, 10), topN(refs, 10), topN(countries, 10)
	return v
}

func topN(m map[string]int64, n int) []Count {
	out := make([]Count, 0, len(m))
	for k, v := range m {
		out = append(out, Count{Value: k, Pageviews: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Pageviews != out[j].Pageviews {
			return out[i].Pageviews > out[j].Pageviews
		}
		return strings.Compare(out[i].Value, out[j].Value) < 0
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}
