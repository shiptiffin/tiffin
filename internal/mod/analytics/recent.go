package analytics

import (
	"hash/maphash"
	"sync"
	"time"
)

// recent remembers which visitors (a project, IP address and user agent)
// requested anything from a project's hosts lately: pages, scripts, styles,
// images. It keeps a hash per visitor, in memory only, in two generations
// that turn every half window, so it remembers between half a window and a
// window.
//
// The edge uses it to spot crawlers that hop between home IP addresses:
// each of their requests claims to follow a link on the site (it sends a
// page of the site as Referer) from an address that never loaded that page.
type recent struct {
	mu        sync.Mutex
	seed      maphash.Seed
	cur, prev map[uint64]struct{}
	from      time.Time // the oldest request the two generations remember
	turned    time.Time // when cur began
}

const (
	recentWindow = 12 * time.Hour
	// recentMax is the most visitors one generation holds; past it the
	// generation turns early (and remembers less far back).
	recentMax = 200000
	// recentWarmup is how far back the history must reach before it is
	// trusted: after a restart, or when a flood turned the generations
	// early, a visitor it does not know may still have loaded a page.
	recentWarmup = SessionIdle
)

// seen notes a request at at and reports whether the visitor requested
// anything before it, as far as recentWindow back. sure is false while the
// history is too short to tell (then before is false too).
func (r *recent) seen(project, ip, ua string, at time.Time) (before, sure bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur == nil {
		r.seed = maphash.MakeSeed()
		r.cur, r.prev = map[uint64]struct{}{}, map[uint64]struct{}{}
		r.from, r.turned = at, at
	}
	if at.Sub(r.turned) >= recentWindow/2 || len(r.cur) >= recentMax {
		r.prev, r.cur = r.cur, make(map[uint64]struct{}, len(r.cur))
		r.from, r.turned = r.turned, at
	}
	var h maphash.Hash
	h.SetSeed(r.seed)
	h.WriteString(project)
	h.WriteByte(0)
	h.WriteString(ip)
	h.WriteByte(0)
	h.WriteString(ua)
	k := h.Sum64()
	_, inCur := r.cur[k]
	_, inPrev := r.prev[k]
	r.cur[k] = struct{}{}
	if at.Sub(r.from) < recentWarmup {
		return false, false
	}
	return inCur || inPrev, true
}
