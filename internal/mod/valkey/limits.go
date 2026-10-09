package valkey

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/manifest"
	"github.com/shiptiffin/tiffin/internal/mod/budget"
	"github.com/shiptiffin/tiffin/internal/platform"
)

// The cache's share of a project's limit. Valkey has one maxmemory for the
// whole server and no limit per key prefix, so for every project with a
// limit (budget.SharedLimit: N% of the box) the box measures its prefix
// every 30 seconds and holds it to its cache limit: the smaller of its
// maxMemoryMB and N% of Valkey's maxmemory. Over the limit it first clears
// the project's keys that have an expiry (they are caches by definition),
// soonest to expire first; if that is not enough it refuses the project's
// writes through its ACL user (reads and deletes still work) until it is
// back under nine tenths of the limit.
//
// Why not a Valkey server per limited project: it would be one more
// process and unit per project, its data would have to move every time a
// limit is set or lifted, and REDIS_URL would change under running apps.
// Measuring the prefix needs none of that, and a project at its limit slows
// to "cache misses, then refused writes" instead of losing its data.

const cacheEvery = 30 * time.Second

// holdRules refuse writes but keep deletes and expiries, so an app can
// still make room.
var holdRules = []string{"-@write", "+del", "+unlink", "+expire", "+pexpire", "+expireat", "+pexpireat", "+getdel"}

// CacheState is a limited project's cache against its limit.
type CacheState struct {
	UsedBytes     int64
	LimitBytes    int64 // 0: not held to one (no limit)
	WritesRefused bool
	MeasuredAt    time.Time
}

type limiter struct {
	mu    sync.Mutex
	state map[string]CacheState
}

var limits = &limiter{state: map[string]CacheState{}}

func (l *limiter) held(project string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state[project].WritesRefused
}

func (l *limiter) forget(project string) {
	l.mu.Lock()
	delete(l.state, project)
	l.mu.Unlock()
}

// CacheLimit returns a project's cache against its limit, as last measured.
func CacheLimit(project string) (CacheState, bool) {
	limits.mu.Lock()
	defer limits.mu.Unlock()
	s, ok := limits.state[project]
	return s, ok
}

// cacheLimitBytes is a project's cache limit: the smaller of its
// maxMemoryMB and share percent of the server's maxmemory (bytes).
func cacheLimitBytes(maxMemoryMB, share int, serverMax int64) int64 {
	lim := int64(maxMemoryMB) << 20
	if serverMax > 0 {
		lim = min(lim, max(1<<20, serverMax*int64(share)/100))
	}
	return lim
}

// decideCache says what to do for a limited project using used of limit
// bytes: how much to clear of its expiring keys, and whether to refuse or
// allow its writes again.
func decideCache(used, limit int64, held bool) (free int64, hold, release bool) {
	resume := limit * 9 / 10
	switch {
	case used > limit:
		return used - resume, !held, false
	case held && used <= resume:
		return 0, false, true
	}
	return 0, false, false
}

// Start runs the cache limits, the REST endpoint (the box serves), the
// script guard and the pruning of keys saved by Delete all data.
func (*Module) Start(ctx context.Context, p *platform.Platform) error {
	go serveREST(ctx, p)
	go guardScripts(ctx, p, Admin)
	go func() {
		t := time.NewTicker(cacheEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if err := limits.round(ctx, p); err != nil && ctx.Err() == nil {
				p.Log.Debug("valkey: cache limits", "err", err)
			}
			pruneSaved(ctx, p)
		}
	}()
	return nil
}

// round measures every project with a limit and a cache, and holds it to
// its limit; a project whose limit was lifted gets its writes back.
func (l *limiter) round(ctx context.Context, p *platform.Platform) error {
	if !budget.Synced() {
		return nil
	}
	projects, err := p.DB.ListProjects(ctx)
	if err != nil {
		return err
	}
	type want struct {
		project string
		maxMB   int
		share   int
	}
	var todo []want
	for _, pr := range projects {
		_, res, err := p.DB.Load(ctx, pr)
		if err != nil {
			return err
		}
		r, ok := res[change.KindService+"/valkey"]
		if !ok {
			continue
		}
		var s manifest.Valkey
		_ = json.Unmarshal(r.Spec, &s)
		todo = append(todo, want{pr, max(s.MaxMemoryMB, 1), budget.SharedLimit(pr).Percent})
	}
	l.mu.Lock()
	known := map[string]CacheState{}
	for k, v := range l.state {
		known[k] = v
	}
	l.mu.Unlock()
	busy := len(known) > 0
	for _, w := range todo {
		busy = busy || w.share > 0
	}
	if !busy {
		return nil // no project with a limit has a cache: nothing to measure
	}
	c, err := Admin(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	info, err := c.String(ctx, "INFO", "memory")
	if err != nil {
		return err
	}
	serverMax, _ := strconv.ParseInt(parseInfo(info)["maxmemory"], 10, 64)
	for _, w := range todo {
		st := known[w.project]
		if w.share == 0 {
			if st.WritesRefused {
				if err := setHold(ctx, c, w.project, false); err != nil {
					return err
				}
			}
			l.forget(w.project)
			continue
		}
		if err := l.enforce(ctx, c, w.project, cacheLimitBytes(w.maxMB, w.share, serverMax), st); err != nil {
			return err
		}
	}
	return nil
}

// enforce measures one limited project and holds it to limit.
func (l *limiter) enforce(ctx context.Context, c *Client, project string, limit int64, st CacheState) error {
	_, used, _, err := prefixUsage(ctx, c, Prefix(project))
	if err != nil {
		return err
	}
	free, hold, release := decideCache(used, limit, st.WritesRefused)
	if free > 0 {
		freed, n, err := freeExpiring(ctx, c, Prefix(project), free)
		if err != nil {
			return err
		}
		if n > 0 {
			used = max(0, used-freed)
			budget.RecordEvent(ctx, project, budget.EventCacheFreed, fmt.Sprintf(
				"%s's cache reached its %s limit, so %d of its keys with an expiry were cleared early. Give it a bigger cache limit if it needs more.", project, mbWords(limit), n))
		}
		hold = used > limit && !st.WritesRefused
	}
	if hold {
		if err := setHold(ctx, c, project, true); err != nil {
			return err
		}
		st.WritesRefused = true
		budget.RecordEvent(ctx, project, budget.EventCacheFull, fmt.Sprintf(
			"%s's cache is over its %s limit with keys that never expire, so new writes are refused until it is under %s (reads and deletes still work). Delete keys, give them an expiry, or give it a bigger cache limit.",
			project, mbWords(limit), mbWords(limit*9/10)))
	}
	if release {
		if err := setHold(ctx, c, project, false); err != nil {
			return err
		}
		st.WritesRefused = false
	}
	l.mu.Lock()
	l.state[project] = CacheState{UsedBytes: used, LimitBytes: limit, WritesRefused: st.WritesRefused, MeasuredAt: time.Now().UTC()}
	l.mu.Unlock()
	return nil
}

// setHold refuses a project's writes (on) or gives them back.
func setHold(ctx context.Context, c *Client, project string, on bool) error {
	rules := commandRules
	if on {
		rules = holdRules
	}
	if _, err := c.Do(ctx, append([]string{"ACL", "SETUSER", User(project)}, rules...)...); err != nil {
		return fmt.Errorf("ACL SETUSER: %w", err)
	}
	_, err := c.Do(ctx, "ACL", "SAVE")
	return err
}

// prefixUsage counts the keys under prefix and estimates their memory from
// a sample of up to 2000 keys, for at most 5 seconds.
func prefixUsage(ctx context.Context, c *Client, prefix string) (keys, bytes int64, approx bool, err error) {
	u, err := measure(ctx, c, prefix, false)
	return u.keys, u.bytes, u.approx, err
}

type usage struct {
	keys, bytes, kept int64
	approx            bool
}

// measure is prefixUsage, and with ttls it also counts the keys that never
// expire (kept). Each SCAN page's lookups go in one round trip.
func measure(ctx context.Context, c *Client, prefix string, ttls bool) (u usage, err error) {
	const sampleMax = 2000
	cursor := "0"
	var sampled, sampledBytes int64
	deadline := time.Now().Add(5 * time.Second)
	for {
		v, err := c.Do(ctx, "SCAN", cursor, "MATCH", prefix+"*", "COUNT", "1000")
		if err != nil {
			return u, err
		}
		next, ks := scanReply(v)
		cursor = next
		var cmds [][]string
		for _, k := range ks {
			if sampled+int64(len(cmds)) < sampleMax {
				cmds = append(cmds, []string{"MEMORY", "USAGE", k, "SAMPLES", "5"})
			}
		}
		nMem := len(cmds)
		if ttls {
			for _, k := range ks {
				cmds = append(cmds, []string{"PTTL", k})
			}
		}
		r, err := c.Pipe(ctx, cmds...)
		if err != nil {
			return u, err
		}
		for i, x := range r {
			n, ok := x.(int64)
			switch {
			case i < nMem && ok:
				sampledBytes += n
				sampled++
			case i >= nMem && ok && n == -1:
				u.kept++
			}
		}
		u.keys += int64(len(ks))
		if cursor == "0" {
			break
		}
		if time.Now().After(deadline) {
			u.approx = true // stopped counting early
			break
		}
	}
	if sampled > 0 {
		u.bytes = sampledBytes * u.keys / sampled
	}
	u.approx = u.approx || sampled < u.keys
	return u, nil
}

// freeExpiring clears the prefix's keys that have an expiry, soonest to
// expire first, until about need bytes are freed. It returns the bytes
// freed and how many keys.
func freeExpiring(ctx context.Context, c *Client, prefix string, need int64) (int64, int, error) {
	type key struct {
		name string
		ttl  int64
	}
	var expiring []key
	cursor := "0"
	deadline := time.Now().Add(5 * time.Second)
	for len(expiring) < 50000 {
		v, err := c.Do(ctx, "SCAN", cursor, "MATCH", prefix+"*", "COUNT", "1000")
		if err != nil {
			return 0, 0, err
		}
		next, ks := scanReply(v)
		cursor = next
		cmds := make([][]string, len(ks))
		for i, k := range ks {
			cmds[i] = []string{"PTTL", k}
		}
		ttls, err := c.Pipe(ctx, cmds...) // one round trip per page
		if err != nil {
			return 0, 0, err
		}
		for i, t := range ttls {
			if ttl, ok := t.(int64); ok && ttl > 0 {
				expiring = append(expiring, key{ks[i], ttl})
			}
		}
		if cursor == "0" || time.Now().After(deadline) {
			break
		}
	}
	sort.Slice(expiring, func(i, j int) bool { return expiring[i].ttl < expiring[j].ttl })
	var freed int64
	n := 0
	for _, k := range expiring {
		if freed >= need {
			break
		}
		size, _ := c.Int(ctx, "MEMORY", "USAGE", k.name, "SAMPLES", "5")
		gone, err := unlinkExpiring(ctx, c, k.name)
		if err != nil {
			return freed, n, err
		}
		if gone {
			freed += size
			n++
		}
	}
	return freed, n, nil
}

// unlinkExpiringLua deletes KEYS[1] only if it still has an expiry, in one
// step: since it was listed, the app may have made it permanent (PERSIST, a
// SET without one), and the limit only ever clears cache.
const unlinkExpiringLua = "if redis.call('PTTL', KEYS[1]) > 0 then return redis.call('UNLINK', KEYS[1]) end return 0"

// unlinkExpiring deletes key if it still expires, and reports whether it did.
func unlinkExpiring(ctx context.Context, c *Client, key string) (bool, error) {
	n, err := c.Int(ctx, "EVAL", unlinkExpiringLua, "1", key)
	return n == 1, err
}

func mbWords(b int64) string {
	if b >= 1<<30 {
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	}
	return fmt.Sprintf("%d MB", (b+(1<<19))>>20)
}
