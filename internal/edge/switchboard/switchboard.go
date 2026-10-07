// Package switchboard is the edge's own reverse proxy between Caddy and app
// instances. The edge routes every container app's hosts to it, once;
// deploys, rollbacks and restarts then switch instances here, in memory,
// without reloading Caddy. That makes the switch atomic (each request is
// counted in on exactly one instance), lets old instances drain precisely
// (the control plane stops them once their in-flight requests finish), and
// avoids Caddy reloads, which can reset connections that arrive mid-reload.
//
// It runs in the edge process, apart from the control plane (the runtime
// module), which sends it a new Table on every change. What only the
// control plane can do (wake a sleeping app, live progress, bucket images)
// goes through Control; while the control plane is down those get a 503 and
// everything else keeps being served.
package switchboard

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btahir/tiffin/internal/peer"
)

// Instance is one running app container.
type Instance struct {
	Name string `json:"name"`
	Port int    `json:"port"`
}

// Retired is an earlier release whose hashed client assets are still served.
type Retired struct {
	Deploy string    `json:"deploy"`
	At     time.Time `json:"at"`
}

// Env is one app environment (production or a preview) as it is served.
type Env struct {
	Project   string     `json:"project"`
	App       string     `json:"app"`
	Preview   string     `json:"preview,omitempty"`
	Live      string     `json:"live,omitempty"`
	Stopped   bool       `json:"stopped,omitempty"`
	Sleeping  bool       `json:"sleeping,omitempty"`
	Instances []Instance `json:"instances,omitempty"`
	Retired   []Retired  `json:"retired,omitempty"`
	// Assets is the directory of its extracted client assets (one
	// subdirectory per release, see ServeAsset).
	Assets string `json:"assets,omitempty"`
	// Timeout is how long one request may take (the app's timeoutSeconds).
	Timeout time.Duration `json:"timeout"`
	// Cgroup is the cgroup directory whose processes may answer the
	// instances' ports: the project's slice. Every connection to an
	// instance is checked against it (internal/peer), so an app of another
	// project that took a port (while its app slept) gets no request. ""
	// checks nothing (tests).
	Cgroup string `json:"cgroup,omitempty"`
}

// Route sends a path prefix of a host to an environment ("" matches every path).
type Route struct {
	Prefix string `json:"prefix,omitempty"`
	Env    string `json:"env"`
}

// Table is what the switchboard serves from. Envs are keyed
// "project/app" or "project/app@preview".
type Table struct {
	Hosts map[string][]Route `json:"hosts"`
	Envs  map[string]*Env    `json:"envs"`
	// Files is the box's files origin (https://files.<domain>[:port]):
	// next/image requests for a project's own files there go to the
	// control plane, which resizes them (see IsBucketImage).
	Files string `json:"files,omitempty"`
}

// Control is what the switchboard needs the control plane for.
type Control interface {
	// Wake starts a sleeping app environment and returns once the
	// switchboard has its instances; woke says whether this call started it.
	Wake(ctx context.Context, env string) (woke bool, err error)
	// WokeFirstByte records how long a request that woke its app waited
	// for its first byte.
	WokeFirstByte(env string, secs float64)
	// Forward serves a request only the control plane can answer: live
	// progress (LivePrefix) and bucket images.
	Forward(w http.ResponseWriter, r *http.Request, env, prefix string)
}

// LivePrefix is where browsers watch jobs and workflow runs on every app
// host (the queue module's LivePath, without its slash).
const LivePrefix = "/_tiffin/runs"

// WorkflowQueueRoute matches the Workflow DevKit's queue routes: they run
// flows and steps, so only the box's own deliveries may reach them.
var WorkflowQueueRoute = regexp.MustCompile(`/\.well-known/workflow/v\d+/(flow|step)(/|$)`)

// Board is a running switchboard.
type Board struct {
	ctl Control
	log *slog.Logger

	mu       sync.RWMutex
	t        Table
	inflight map[string]*atomic.Int64 // instance name → requests in flight
	rr       atomic.Uint64            // rotates ties between instances
	proxy    *http.Transport

	seenMu sync.Mutex
	seen   map[string]time.Time // env → last request

	metaMu sync.Mutex
	metas  map[string]releaseMeta // release dir → its assets.json and pages.json
	// served: the release dirs ServeAsset may read (live releases and
	// those retired within AssetsKept, as of the last Set); only these
	// are cached.
	served map[string]bool
}

// New returns an empty switchboard: every request is a 404 until Set.
func New(ctl Control, log *slog.Logger) *Board {
	if log == nil {
		log = slog.Default()
	}
	b := &Board{ctl: ctl, log: log, inflight: map[string]*atomic.Int64{}, seen: map[string]time.Time{}, metas: map[string]releaseMeta{}}
	// Each request names its app's cgroup (serveApp); the dialer checks
	// every new connection against it.
	d := &peer.Dialer{Dialer: net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}}
	b.proxy = &http.Transport{
		DialContext:           d.DialContext,
		MaxIdleConns:          1024,
		MaxIdleConnsPerHost:   128,
		IdleConnTimeout:       upstreamIdle,
		ResponseHeaderTimeout: 0, // apps may stream
		ForceAttemptHTTP2:     false,
	}
	return b
}

// Set switches to a new table. Selection and counting (acquire) happen
// under the same lock, so once Set returns no new request lands on an
// instance the table no longer has. Counters of instances still busy are
// kept until they drain.
func (b *Board) Set(t Table) {
	hosts := make(map[string][]Route, len(t.Hosts))
	for h, rs := range t.Hosts {
		rs = append([]Route(nil), rs...)
		sort.SliceStable(rs, func(i, j int) bool { return len(rs[i].Prefix) > len(rs[j].Prefix) })
		hosts[h] = rs
	}
	t.Hosts = hosts
	if t.Envs == nil {
		t.Envs = map[string]*Env{}
	}
	live := map[string]bool{}
	served := map[string]bool{}
	for _, e := range t.Envs {
		for _, in := range e.Instances {
			live[in.Name] = true
		}
		if e.Assets == "" {
			continue
		}
		if e.Live != "" {
			served[filepath.Join(e.Assets, e.Live)] = true
		}
		for _, rt := range e.Retired {
			if time.Since(rt.At) < AssetsKept {
				served[filepath.Join(e.Assets, rt.Deploy)] = true
			}
		}
	}
	b.mu.Lock()
	b.t = t
	for _, e := range t.Envs {
		for _, in := range e.Instances {
			if b.inflight[in.Name] == nil {
				b.inflight[in.Name] = &atomic.Int64{}
			}
		}
	}
	for name, n := range b.inflight {
		if !live[name] && n.Load() == 0 {
			delete(b.inflight, name)
		}
	}
	b.mu.Unlock()
	b.seenMu.Lock()
	for k := range b.seen {
		if t.Envs[k] == nil {
			delete(b.seen, k)
		}
	}
	b.seenMu.Unlock()
	b.metaMu.Lock()
	b.served = served
	for d := range b.metas {
		if !served[d] {
			delete(b.metas, d) // a retired release's pages map can be large
		}
	}
	b.metaMu.Unlock()
}

// Busy counts the requests in flight on the named instances.
func (b *Board) Busy(names []string) int64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	var n int64
	for _, name := range names {
		if c := b.inflight[name]; c != nil {
			n += c.Load()
		}
	}
	return n
}

// Activity is when each environment last had a request (or one ended).
func (b *Board) Activity() map[string]time.Time {
	b.seenMu.Lock()
	defer b.seenMu.Unlock()
	out := make(map[string]time.Time, len(b.seen))
	for k, t := range b.seen {
		out[k] = t
	}
	return out
}

func (b *Board) touch(env string) {
	now := time.Now()
	b.seenMu.Lock()
	b.seen[env] = now
	b.seenMu.Unlock()
}

func (b *Board) env(key string) *Env {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.t.Envs[key]
}

// lookup finds the environment serving host and path, and the route's path prefix.
func (b *Board) lookup(host, p string) (env, prefix string, ok bool) {
	b.mu.RLock()
	rs := b.t.Hosts[host]
	b.mu.RUnlock()
	for _, r := range rs {
		if r.Prefix == "" || p == r.Prefix || strings.HasPrefix(p, r.Prefix+"/") {
			return r.Env, r.Prefix, true
		}
	}
	return "", "", false
}

// acquire counts a request in on the least busy instance of an
// environment, and returns the environment as the table had it then.
func (b *Board) acquire(key string) (*Env, Instance, func(), bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	st := b.t.Envs[key]
	if st == nil || st.Stopped || st.Sleeping || len(st.Instances) == 0 {
		return nil, Instance{}, nil, false
	}
	best, bestN := -1, int64(0)
	start := int(b.rr.Add(1) % uint64(len(st.Instances)))
	for i := range st.Instances {
		j := (start + i) % len(st.Instances)
		n := b.inflight[st.Instances[j].Name].Load()
		if best < 0 || n < bestN {
			best, bestN = j, n
		}
	}
	in := st.Instances[best]
	cnt := b.inflight[in.Name]
	cnt.Add(1)
	return st, in, func() { cnt.Add(-1) }, true
}

// ServeHTTP is the switchboard's front door: it finds the app environment a
// request belongs to (by host and path, as the edge routed it), wakes it if
// it sleeps, and proxies to the least busy instance. Every request counts
// as activity, those the box answers itself included.
func (b *Board) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	began := time.Now()
	host := strings.ToLower(req.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	key, prefix, ok := b.lookup(host, req.URL.Path)
	if strings.HasPrefix(req.URL.Path, LivePrefix+"/") {
		if ok {
			b.touch(key)
		}
		b.ctl.Forward(w, req, key, prefix)
		return
	}
	if !ok {
		http.Error(w, "no app is served here", http.StatusNotFound)
		return
	}
	if WorkflowQueueRoute.MatchString(path.Clean(req.URL.Path)) {
		http.NotFound(w, req)
		return
	}
	b.touch(key)
	defer b.touch(key) // a long request keeps it awake until it ends
	st := b.env(key)
	if st != nil && !st.Stopped {
		// Client assets come from the box's copy of them; a sleeping app
		// need not wake for them.
		if b.ServeAsset(w, req, st, prefix) {
			return
		}
		b.mu.RLock()
		files := b.t.Files
		b.mu.RUnlock()
		if IsBucketImage(req, st.Project, prefix, files) {
			b.ctl.Forward(w, req, key, prefix)
			return
		}
	}
	if st != nil && !st.Stopped && (st.Sleeping || (st.Preview != "" && len(st.Instances) == 0)) {
		woke, err := b.ctl.Wake(req.Context(), key)
		if err != nil {
			w.Header().Set("Retry-After", "5")
			http.Error(w, "the app could not start: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		if woke {
			fb := &firstByte{ResponseWriter: w}
			b.serveApp(fb, req, key)
			if !fb.at.IsZero() {
				b.ctl.WokeFirstByte(key, math.Round(fb.at.Sub(began).Seconds()*1000)/1000)
			}
			return
		}
	}
	b.serveApp(w, req, key)
}

// upstreamIdle is how long a connection to an app stays pooled unused. It
// is below the shortest keep-alive timeout of the servers apps run (Node's
// http and uvicorn close idle connections after 5s, Bun.serve after 10s):
// a connection the app closed while it sat in the pool would take the next
// request and fail it, and Go retries only requests without a body, so a
// POST after a quiet spell would get a 502. A new connection on localhost
// costs some 50µs.
const upstreamIdle = 4 * time.Second

// errTooLong ends a request that reached its app's time limit.
var errTooLong = errors.New("the request reached its time limit")

// serveApp proxies one request to an instance of the app environment. A
// request that reaches the app's time limit is answered 504, or cut if the
// response has begun (the client sees the stream end early).
func (b *Board) serveApp(w http.ResponseWriter, req *http.Request, key string) {
	st, in, done, ok := b.acquire(key)
	if !ok {
		http.Error(w, "this app has no running instances right now", http.StatusServiceUnavailable)
		return
	}
	defer done()
	var limit time.Duration
	ctx := peer.WithOwner(req.Context(), st.Cgroup)
	if st.Timeout > 0 {
		limit = st.Timeout
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeoutCause(ctx, limit, errTooLong)
		defer cancel()
	}
	req = req.WithContext(ctx)
	target := &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(in.Port)}
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
			if tp := traceparent(pr.In.Header); tp != "" {
				pr.Out.Header.Set("Traceparent", tp)
			}
			// The edge compresses (zstd or gzip, as the client takes it), so
			// apps need not: no CPU spent twice, and no gzip-only response
			// where the client takes zstd. One an app compresses anyway
			// passes through as it is.
			pr.Out.Header.Set("Accept-Encoding", "identity")
		},
		Transport:     b.proxy,
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			if errors.Is(context.Cause(req.Context()), errTooLong) {
				http.Error(w, tooLong(limit), http.StatusGatewayTimeout)
				return
			}
			var fe *peer.ForeignError
			if errors.As(err, &fe) {
				b.log.Warn("switchboard: an app's port is answered by another project's process", "host", req.Host, "addr", fe.Addr, "project", st.Project)
			}
			http.Error(w, "the app did not answer: "+err.Error(), http.StatusBadGateway)
		},
	}
	defer func() { // also when a response under way is cut (a panic that aborts it)
		if errors.Is(context.Cause(req.Context()), errTooLong) {
			b.log.Info("switchboard: request reached its time limit", "host", req.Host, "path", req.URL.Path, "limit", limit, "request_id", req.Header.Get("X-Request-Id"))
		}
	}()
	rp.ServeHTTP(w, req)
}

func tooLong(limit time.Duration) string {
	return fmt.Sprintf("the app took longer than its time limit for one request (%s), so the box stopped waiting. "+
		"Make the request shorter (a queue job or a workflow can run longer), or raise the limit in tiffin.config.ts: timeoutSeconds (up to 86400).\n", limit)
}

// traceparent starts the request's trace with the edge's request ID as its
// trace ID (W3C trace context), so an app's OpenTelemetry spans and the
// request's access log line share one ID. A request that carries its own
// trace context keeps it.
func traceparent(h http.Header) string {
	if h.Get("Traceparent") != "" {
		return ""
	}
	id := strings.ReplaceAll(h.Get("X-Request-Id"), "-", "")
	if len(id) != 32 || strings.Trim(strings.ToLower(id), "0123456789abcdef") != "" || strings.Trim(id, "0") == "" {
		return ""
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "00-" + strings.ToLower(id) + "-" + hex.EncodeToString(b[:]) + "-01"
}

// NextImagePath is Next.js's image optimizer, which next/image's default
// loader points every image at (/_next/image?url=<src>&w=&q=).
const NextImagePath = "/_next/image"

// IsBucketImage reports whether req asks Next.js's image optimizer for a
// file in the project's own buckets (files.<domain>/<project>/...): the
// box answers those with its image transforms (the app cannot fetch them).
func IsBucketImage(req *http.Request, project, prefix, files string) bool {
	if files == "" || (req.Method != http.MethodGet && req.Method != http.MethodHead) || strings.TrimPrefix(req.URL.Path, prefix) != NextImagePath {
		return false
	}
	src, err := url.Parse(req.URL.Query().Get("url"))
	if err != nil || !src.IsAbs() {
		return false
	}
	f, err := url.Parse(files)
	return err == nil && src.Scheme == f.Scheme && strings.EqualFold(src.Host, f.Host) && strings.HasPrefix(src.Path, "/"+project+"/")
}

// firstByte notes when a response's first byte went out.
type firstByte struct {
	http.ResponseWriter
	at time.Time
}

func (f *firstByte) WriteHeader(code int) {
	if f.at.IsZero() {
		f.at = time.Now()
	}
	f.ResponseWriter.WriteHeader(code)
}

func (f *firstByte) Write(b []byte) (int, error) {
	if f.at.IsZero() {
		f.at = time.Now()
	}
	return f.ResponseWriter.Write(b)
}

func (f *firstByte) Unwrap() http.ResponseWriter { return f.ResponseWriter }
