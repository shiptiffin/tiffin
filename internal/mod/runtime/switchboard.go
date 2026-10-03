package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btahir/tiffin/internal/edge"
)

// The switchboard is the runtime's own reverse proxy between the edge and
// app instances. The edge routes every container app's hosts to it, once;
// deploys, rollbacks and restarts then switch instances here, in memory,
// without reloading the edge. That makes the switch atomic (each request is
// counted in on exactly one instance), lets old instances drain precisely
// (they stop once their in-flight requests finish), and avoids edge reloads,
// which can reset connections that arrive mid-reload.

// stateCache mirrors app states for the request path (the database stays
// the source of truth) and counts in-flight requests per instance.
type stateCache struct {
	mu       sync.RWMutex
	m        map[string]*AppState
	inflight map[string]*atomic.Int64 // container name → requests in flight
}

func newStateCache() *stateCache {
	return &stateCache{m: map[string]*AppState{}, inflight: map[string]*atomic.Int64{}}
}

func (c *stateCache) put(st *AppState) {
	cp := *st
	cp.Instances = append([]Instance(nil), st.Instances...)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[envKey(st.Project, st.App, st.Preview)] = &cp
	for _, in := range cp.Instances {
		if c.inflight[in.Name] == nil {
			c.inflight[in.Name] = &atomic.Int64{}
		}
	}
}

func (c *stateCache) del(key string) {
	c.mu.Lock()
	delete(c.m, key)
	c.mu.Unlock()
}

func (c *stateCache) get(key string) *AppState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.m[key]
}

// acquire counts a request in on the least busy instance of an environment.
// Selection and counting happen under the same lock a switch takes, so after
// a switch no new request can land on an old instance.
func (c *stateCache) acquire(key string) (Instance, func(), bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	st := c.m[key]
	if st == nil || st.Stopped || st.Sleeping || len(st.Instances) == 0 {
		return Instance{}, nil, false
	}
	best, bestN := -1, int64(0)
	start := int(rrCounter.Add(1)) // rotate ties
	for i := range st.Instances {
		j := (start + i) % len(st.Instances)
		n := c.inflight[st.Instances[j].Name].Load()
		if best < 0 || n < bestN {
			best, bestN = j, n
		}
	}
	in := st.Instances[best]
	cnt := c.inflight[in.Name]
	cnt.Add(1)
	return in, func() { cnt.Add(-1) }, true
}

func (c *stateCache) busy(ins []Instance) int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var n int64
	for _, in := range ins {
		if cnt := c.inflight[in.Name]; cnt != nil {
			n += cnt.Load()
		}
	}
	return n
}

func (c *stateCache) forget(ins []Instance) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, in := range ins {
		delete(c.inflight, in.Name)
	}
}

// drainThenRemove stops old instances once they have no requests in flight
// (at most Drain later).
func (r *rt) drainThenRemove(old []Instance) {
	deadline := time.Now().Add(r.opt.Drain)
	time.Sleep(50 * time.Millisecond) // let responses being written finish flushing
	for r.st.cache.busy(old) > 0 && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	r.removeInstances(r.ctx, old)
}

// dispatch maps edge routes to app environments.
type dispatchEntry struct {
	prefix string
	env    string
}

func (r *rt) setDispatch(t map[string][]dispatchEntry) {
	for _, es := range t {
		sort.Slice(es, func(i, j int) bool { return len(es[i].prefix) > len(es[j].prefix) })
	}
	r.mu.Lock()
	r.dispatch = t
	r.mu.Unlock()
}

func (r *rt) lookup(host, path string) (string, bool) {
	r.mu.Lock()
	es := r.dispatch[host]
	r.mu.Unlock()
	for _, e := range es {
		if e.prefix == "" || path == e.prefix || strings.HasPrefix(path, e.prefix+"/") {
			return e.env, true
		}
	}
	return "", false
}

// routesChanged reports whether the edge needs new routes (hosts, paths,
// file roots), as opposed to only instances changing behind the switchboard.
func (r *rt) routesChanged(ctx context.Context) bool {
	routes, _ := r.routes(ctx)
	h := routesHash(routes)
	r.mu.Lock()
	defer r.mu.Unlock()
	return h != r.loadedRoutes
}

func routesHash(rs []edge.Route) string {
	b, _ := json.Marshal(rs)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// refreshIfNeeded reloads the edge only when the runtime's routes changed.
func (r *rt) refreshIfNeeded(ctx context.Context) error {
	if !r.routesChanged(ctx) {
		return nil
	}
	return r.p.RefreshRoutes(ctx)
}

var proxyTransport = &http.Transport{
	DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	MaxIdleConns:          1024,
	MaxIdleConnsPerHost:   128,
	IdleConnTimeout:       60 * time.Second,
	ResponseHeaderTimeout: 0, // apps may stream
	ForceAttemptHTTP2:     false,
}

// serveApp proxies one request to an instance of the app environment.
func (r *rt) serveApp(w http.ResponseWriter, req *http.Request, key string) {
	in, done, ok := r.st.cache.acquire(key)
	if !ok {
		http.Error(w, "this app has no running instances right now", http.StatusServiceUnavailable)
		return
	}
	defer done()
	target := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", in.Port)}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = pr.In.Host
			// The edge already set the client's forwarding headers: pass them on as they are.
			for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-Port"} {
				if v := pr.In.Header.Values(h); len(v) > 0 {
					pr.Out.Header[h] = v
				}
			}
		},
		Transport:     proxyTransport,
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, "the app did not answer: "+err.Error(), http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(w, req)
}

// staticLink is the stable path the edge serves a static environment from;
// promote points it at the live deploy's files with an atomic rename.
func (r *rt) staticLink(project, app, preview string) string {
	env := "prod"
	if preview != "" {
		env = "pr-" + preview
	}
	return filepath.Join(r.opt.DataDir, "static", project, app, "live-"+env)
}

func swapSymlink(link, target string) error {
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	tmp := link + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, link)
}
