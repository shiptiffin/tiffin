package edge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/certmagic"
	"github.com/libdns/libdns"

	"github.com/btahir/tiffin/internal/edge/switchboard"
)

// Server is the edge process: Caddy and the switchboard, serving the
// snapshots the control plane sends (see proto.go). Run it once per box.
type Server struct {
	Socket      string // the API the control plane drives
	Control     string // the control plane's socket
	State       string // where the last snapshot is kept
	Switchboard string // where the switchboard listens: "unix/<path>" or a loopback host:port (tests)
	Build       string
	Log         *slog.Logger

	board    *switchboard.Board
	ctl      *controlClient
	sbAddr   string
	fds      map[string]int // socket activation: "tcp/443" → fd
	mu       sync.Mutex     // one snapshot at a time
	cur      Snapshot       // what it serves
	caddyOn  bool
	caddySum [32]byte
	fallback string
	events   eventLog
	done     chan struct{}
}

// Start loads the last snapshot, if any, and serves until ctx ends; Wait
// returns once the edge has drained.
func (s *Server) Start(ctx context.Context) error {
	if s.Log == nil {
		s.Log = slog.Default()
	}
	var err error
	if s.fds == nil {
		s.fds, err = activationFDs()
	}
	if err != nil {
		return err
	}
	s.ctl = newControlClient(s.Control)
	s.board = switchboard.New(s.ctl, s.Log)
	remoteDNS = func(key string) certmagic.DNSProvider { return &controlDNS{c: s.ctl, key: key} }
	s.events.init()
	cancelEvents := OnCertEvent(s.events.add)
	var sbln net.Listener
	if sock, ok := strings.CutPrefix(s.Switchboard, "unix/"); ok {
		sbln, err = listenUnix(sock)
		s.sbAddr = s.Switchboard
	} else {
		sbln, err = net.Listen("tcp", s.Switchboard)
		if err == nil {
			s.sbAddr = sbln.Addr().String()
		}
	}
	if err != nil {
		return fmt.Errorf("edge: switchboard: %w", err)
	}
	sb := &http.Server{Handler: s.board, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = sb.Serve(sbln) }()
	if raw, err := os.ReadFile(s.State); err == nil {
		var snap Snapshot
		if err := json.Unmarshal(raw, &snap); err != nil {
			s.Log.Error("edge: the saved snapshot is unreadable; waiting for the control plane", "err", err)
		} else if _, err := s.apply(ctx, snap); err != nil {
			s.Log.Error("edge: serve the saved snapshot", "version", snap.Version, "err", err)
		} else {
			s.Log.Info("edge: serving the saved snapshot", "version", snap.Version)
		}
	}
	apiln, err := listenUnix(s.Socket)
	if err != nil {
		sb.Close()
		return err
	}
	api := &http.Server{Handler: s.api(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = api.Serve(apiln) }()
	s.done = make(chan struct{})
	go func() {
		defer close(s.done)
		<-ctx.Done()
		cancelEvents()
		_ = api.Close()
		// Caddy first: it finishes the requests under way, through the
		// switchboard, and then the switchboard has nothing left (see
		// drain.go). Bounded: a stream left open would hold the restart up
		// for good. With socket activation, connections that arrive
		// meanwhile wait in the kernel for the next edge.
		_ = caddy.Stop()
		if !waitConns(10 * time.Second) {
			s.Log.Warn("edge: requests still under way after 10s; stopping anyway")
		}
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = sb.Shutdown(sctx)
	}()
	return nil
}

// Wait blocks until the edge stopped.
func (s *Server) Wait() { <-s.done }

// SwitchboardAddr is the address the switchboard listens on.
func (s *Server) SwitchboardAddr() string { return s.sbAddr }

// errStale refuses a snapshot older than the one served.
var errStale = errors.New("edge: snapshot older than the one served")

// apply serves snap: the switchboard table, then Caddy's config when it
// changed (so Caddy never routes to a table that lacks the hosts), then
// saves it. All or nothing: a config Caddy refuses changes nothing.
func (s *Server) apply(ctx context.Context, snap Snapshot) (Ack, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if snap.Version <= s.cur.Version {
		return Ack{Version: s.cur.Version}, errStale
	}
	next := s.cur
	next.Version = snap.Version
	if snap.Table != nil {
		s.board.Set(*snap.Table)
		next.Table = snap.Table
	}
	if snap.Caddy != nil {
		if err := s.loadCaddy(ctx, *snap.Caddy); err != nil {
			if s.cur.Table != nil {
				s.board.Set(*s.cur.Table)
			} else {
				s.board.Set(switchboard.Table{})
			}
			return Ack{}, err
		}
		next.Caddy = snap.Caddy
	}
	s.cur = next
	if s.State != "" {
		if err := writeFileAtomic(s.State, next); err != nil {
			s.Log.Warn("edge: save the snapshot", "err", err)
		}
	}
	return Ack{Version: next.Version, Fallback: s.fallback}, nil
}

// loadCaddy loads r unless Caddy already runs it.
func (s *Server) loadCaddy(ctx context.Context, r Rendered) error {
	raw, _ := json.Marshal(r)
	sum := sha256.Sum256(raw)
	if s.caddyOn && sum == s.caddySum {
		return nil
	}
	if !s.caddyOn && len(s.fds) > 0 {
		if err := s.warm(ctx, r); err != nil {
			s.Log.Warn("edge: load certificates before taking connections", "err", err)
		}
	}
	var err error
	if r.Config, err = s.listeners(r.Config); err != nil {
		return err
	}
	if r.Fallback, err = s.listeners(r.Fallback); err != nil {
		return err
	}
	if r.Config, err = counted(r.Config); err != nil {
		return err
	}
	if r.Fallback, err = counted(r.Fallback); err != nil {
		return err
	}
	fallback, err := r.load(ctx)
	if err != nil {
		return err
	}
	s.Log.Info("edge: caddy config loaded", "first", !s.caddyOn, "protection_fallback", fallback != nil)
	s.caddyOn, s.caddySum, s.fallback = true, sum, ""
	if fallback != nil {
		s.fallback = fallback.Error()
	}
	return nil
}

func writeFileAtomic(path string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Server) api() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/hello", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		h := Hello{Protocol: Protocol, Build: s.Build, PID: os.Getpid(), Version: s.cur.Version, Switchboard: s.sbAddr, Caddy: s.caddyOn, Fallback: s.fallback}
		s.mu.Unlock()
		writeJSON(w, h)
	})
	mux.HandleFunc("PUT /v1/snapshot", func(w http.ResponseWriter, r *http.Request) {
		var snap Snapshot
		if err := json.NewDecoder(r.Body).Decode(&snap); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ack, err := s.apply(r.Context(), snap)
		switch {
		case errors.Is(err, errStale):
			w.WriteHeader(http.StatusConflict)
			writeJSON(w, ack)
		case err != nil:
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		default:
			writeJSON(w, ack)
		}
	})
	mux.HandleFunc("GET /v1/busy", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.board.Busy(r.URL.Query()["name"]))
	})
	mux.HandleFunc("GET /v1/activity", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.board.Activity())
	})
	mux.HandleFunc("GET /v1/cert", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, CertStatus(r.URL.Query().Get("host")))
	})
	mux.HandleFunc("GET /v1/events", func(w http.ResponseWriter, r *http.Request) {
		after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
		writeJSON(w, s.events.since(r.Context(), after, 25*time.Second))
	})
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ---- certificate events, for the control plane ----

// EventBatch is certificate events after a sequence number.
type EventBatch struct {
	Seq    uint64      `json:"seq"`
	Events []CertEvent `json:"events,omitempty"`
}

type eventLog struct {
	mu     sync.Mutex
	seq    uint64
	recent []CertEvent // the last 100, the newest last; seq is the newest's
	wake   chan struct{}
}

func (e *eventLog) init() { e.wake = make(chan struct{}) }

func (e *eventLog) add(ev CertEvent) {
	e.mu.Lock()
	e.seq++
	e.recent = append(e.recent, ev)
	if len(e.recent) > 100 {
		e.recent = e.recent[len(e.recent)-100:]
	}
	close(e.wake)
	e.wake = make(chan struct{})
	e.mu.Unlock()
}

// since returns the events after seq, waiting up to wait for one.
func (e *eventLog) since(ctx context.Context, seq uint64, wait time.Duration) EventBatch {
	t := time.NewTimer(wait)
	defer t.Stop()
	for {
		e.mu.Lock()
		cur, wake := e.seq, e.wake
		var out []CertEvent
		if seq < cur {
			n := min(int(cur-seq), len(e.recent))
			out = append(out, e.recent[len(e.recent)-n:]...)
		}
		e.mu.Unlock()
		if len(out) > 0 || seq > cur {
			return EventBatch{Seq: cur, Events: out}
		}
		select {
		case <-wake:
		case <-t.C:
			return EventBatch{Seq: cur}
		case <-ctx.Done():
			return EventBatch{Seq: cur}
		}
	}
}

// ---- calls into the control plane ----

// errControlDown is what a request that needs the control plane gets
// while it restarts.
var errControlDown = errors.New("the box is restarting; try again in a few seconds")

type controlClient struct {
	hc    *http.Client
	proxy *httputil.ReverseProxy
}

func newControlClient(path string) *controlClient {
	hc := unixClient(path, 0)
	c := &controlClient{hc: hc}
	c.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme, pr.Out.URL.Host = "http", "control"
			pr.Out.Host = pr.In.Host
			for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-Port"} {
				if v := pr.In.Header.Values(h); len(v) > 0 {
					pr.Out.Header[h] = v
				}
			}
		},
		Transport:     hc.Transport,
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			w.Header().Set("Retry-After", "5")
			http.Error(w, errControlDown.Error(), http.StatusServiceUnavailable)
		},
	}
	return c
}

func (c *controlClient) call(ctx context.Context, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://control"+path, rd)
	if err != nil {
		return err
	}
	res, err := c.hc.Do(req)
	if err != nil {
		return errControlDown
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return errors.New(string(bytes.TrimSpace(raw)))
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (c *controlClient) Wake(ctx context.Context, env string) (bool, error) {
	var out struct {
		Woke bool `json:"woke"`
	}
	err := c.call(ctx, "/v1/wake?env="+url.QueryEscape(env), nil, &out)
	return out.Woke, err
}

func (c *controlClient) WokeFirstByte(env string, secs float64) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = c.call(ctx, "/v1/woke?env="+url.QueryEscape(env)+"&secs="+strconv.FormatFloat(secs, 'f', 3, 64), nil, nil)
	}()
}

func (c *controlClient) Forward(w http.ResponseWriter, r *http.Request, env, prefix string) {
	r = r.Clone(r.Context())
	r.Header.Set(ForwardEnv, env)
	r.Header.Set(ForwardPrefix, prefix)
	c.proxy.ServeHTTP(w, r)
}

// controlDNS solves DNS-01 challenges through the provider the control
// plane holds (the credentials stay there).
type controlDNS struct {
	c   *controlClient
	key string
}

type dnsCall struct {
	Key     string      `json:"key"`
	Delete  bool        `json:"delete,omitempty"`
	Zone    string      `json:"zone"`
	Records []libdns.RR `json:"records"`
}

func (d *controlDNS) do(ctx context.Context, del bool, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	call := dnsCall{Key: d.key, Delete: del, Zone: zone}
	for _, r := range recs {
		call.Records = append(call.Records, r.RR())
	}
	var out []libdns.RR
	if err := d.c.call(ctx, "/v1/dns", call, &out); err != nil {
		return nil, err
	}
	return parseRRs(out)
}

func parseRRs(rrs []libdns.RR) ([]libdns.Record, error) {
	out := make([]libdns.Record, 0, len(rrs))
	for _, rr := range rrs {
		r, err := rr.Parse()
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func (d *controlDNS) AppendRecords(ctx context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	return d.do(ctx, false, zone, recs)
}

func (d *controlDNS) DeleteRecords(ctx context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	return d.do(ctx, true, zone, recs)
}
