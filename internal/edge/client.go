package edge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libdns/libdns"

	"github.com/btahir/tiffin/internal/edge/switchboard"
)

// Client drives an edge process from the control plane (it is the
// platform's EdgeController). Every change goes out as a whole Snapshot
// and returns once the edge serves it; a background loop sends the latest
// again whenever the edge restarted or missed one.
type Client struct {
	sock string
	base Config // routes come from SetRoutes
	log  *slog.Logger
	// local: the edge runs in this process, which already sees its
	// certificate state and events.
	local bool
	// restartEdge restarts an edge of another protocol version (once).
	restartEdge func()
	restarted   bool
	hc          *http.Client // snapshots and queries
	poll        *http.Client // long polls

	mu       sync.Mutex
	source   func() switchboard.Table
	sb       string    // the switchboard's address
	caddy    *rendered // the config to serve (the last one the edge did not refuse)
	version  uint64    // the last snapshot sent
	acked    uint64    // the last snapshot the edge served
	dirty    bool      // the last send failed: send again
	up       bool
	pushMu   sync.Mutex
	hello    Hello
	helloErr error
}

// ClientOptions configure NewClient.
type ClientOptions struct {
	Socket string // the edge's socket
	Base   Config // the box's edge config, without routes
	// Switchboard is the switchboard's address until the edge says (Hello).
	Switchboard string
	Local       bool // the edge runs in this process
	// RestartEdge restarts the edge process; the client asks once when the
	// edge speaks another protocol version (an update changed it).
	RestartEdge func()
	Log         *slog.Logger
}

// NewClient returns a client; Start connects it.
func NewClient(o ClientOptions) *Client {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Client{sock: o.Socket, base: o.Base, sb: o.Switchboard, local: o.Local, restartEdge: o.RestartEdge, log: o.Log,
		hc: unixClient(o.Socket, 2*time.Minute), poll: unixClient(o.Socket, 0)}
}

var remote atomic.Pointer[Client]

// Start waits up to wait for the edge to answer, then keeps it current in
// the background until ctx ends. It is not an error for the edge to be
// down: everything is sent once it is back.
func (c *Client) Start(ctx context.Context, wait time.Duration) {
	deadline := time.Now().Add(wait)
	for c.check(ctx) != nil && time.Now().Before(deadline) && ctx.Err() == nil {
		time.Sleep(200 * time.Millisecond)
	}
	if !c.local {
		remote.Store(c)
		go c.followEvents(ctx)
	}
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				remote.CompareAndSwap(c, nil)
				return
			case <-t.C:
				if c.check(ctx) != nil {
					continue
				}
				c.mu.Lock()
				behind := c.dirty || c.hello.Version != c.acked
				c.mu.Unlock()
				if behind {
					if err := c.send(ctx, nil); err != nil {
						c.log.Warn("edge: send the snapshot", "err", err)
					}
				}
			}
		}
	}()
}

// check asks the edge how it is, logging when it goes or comes back.
func (c *Client) check(ctx context.Context) error {
	var h Hello
	err := c.get(ctx, c.hc, "/v1/hello", &h)
	if err == nil && h.Protocol != Protocol {
		err = fmt.Errorf("the edge speaks protocol %d, this build %d: restart it (systemctl restart tiffin-edge)", h.Protocol, Protocol)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil && h.Protocol != 0 && h.Protocol != Protocol && c.restartEdge != nil && !c.restarted {
		c.restarted = true
		c.log.Warn("edge: restarting it on this build", "edge_protocol", h.Protocol, "protocol", Protocol)
		go c.restartEdge()
	}
	was := c.up
	c.up, c.helloErr = err == nil, err
	if err == nil {
		c.hello = h
		if h.Switchboard != "" {
			c.sb = h.Switchboard
		}
	}
	switch {
	case err == nil && !was:
		c.log.Info("edge: connected", "build", h.Build, "pid", h.PID, "version", h.Version)
	case err != nil && was:
		c.log.Warn("edge: not answering", "err", err)
	}
	return err
}

// Config is the box's edge config (without routes).
func (c *Client) Config() Config { return c.base }

// Status reports whether the edge answers and how it is.
func (c *Client) Status() (Hello, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hello, c.helloErr
}

// SetRoutes renders the box's config with routes and sends it.
func (c *Client) SetRoutes(routes []Route) error {
	cfg := c.base
	cfg.Routes = routes
	r, err := render(cfg)
	if err != nil {
		return err
	}
	return c.send(context.Background(), r)
}

// Switchboard is the address the switchboard listens on.
func (c *Client) Switchboard() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sb
}

// TableSource registers where the switchboard table comes from.
func (c *Client) TableSource(fn func() switchboard.Table) {
	c.mu.Lock()
	c.source = fn
	c.mu.Unlock()
}

// SyncTable sends the switchboard table as it is now.
func (c *Client) SyncTable() error { return c.send(context.Background(), nil) }

// errRefused is a config the edge would not load.
type errRefused struct{ msg string }

func (e *errRefused) Error() string { return e.msg }

// send sends a snapshot of the current config (or r, a new one) and table.
// A config the edge refuses is dropped: the edge serves the one before.
func (c *Client) send(ctx context.Context, r *rendered) error {
	c.pushMu.Lock()
	defer c.pushMu.Unlock()
	c.mu.Lock()
	prev, src := c.caddy, c.source
	if r != nil {
		c.caddy = r
	}
	cur := c.caddy
	c.mu.Unlock()
	snap := Snapshot{}
	if cur != nil {
		snap.Caddy = &cur.Rendered
	}
	if src != nil {
		t := src()
		snap.Table = &t
	}
	if snap.Caddy == nil && snap.Table == nil {
		return nil
	}
	var ack Ack
	var err error
	for try := 0; try < 2; try++ {
		c.mu.Lock()
		snap.Version = max(c.version+1, uint64(time.Now().UnixNano()))
		c.version = snap.Version
		c.mu.Unlock()
		ack, err = c.put(ctx, snap)
		var stale *staleError
		if !errors.As(err, &stale) {
			break
		}
		c.mu.Lock() // the edge served a newer one (a clock step back): go past it
		c.version = max(c.version, stale.version)
		c.mu.Unlock()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var refused *errRefused
	if errors.As(err, &refused) && r != nil {
		c.caddy = prev
	}
	c.dirty = err != nil && !errors.As(err, &refused)
	if err != nil {
		return err
	}
	c.acked = ack.Version
	c.hello.Version = ack.Version
	if cur != nil {
		switch {
		case ack.Fallback != "":
			setProtectState(nil, errors.New(ack.Fallback))
		case cur.protErr != nil:
			setProtectState(nil, cur.protErr)
		default:
			setProtectState(cur.prot, nil)
		}
	}
	return nil
}

type staleError struct{ version uint64 }

func (e *staleError) Error() string { return "edge: snapshot refused as stale" }

func (c *Client) put(ctx context.Context, snap Snapshot) (Ack, error) {
	raw, err := json.Marshal(snap)
	if err != nil {
		return Ack{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://edge/v1/snapshot", bytes.NewReader(raw))
	if err != nil {
		return Ack{}, err
	}
	res, err := c.hc.Do(req)
	if err != nil {
		return Ack{}, fmt.Errorf("edge: not answering: %w", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var ack Ack
	switch res.StatusCode {
	case http.StatusOK:
		return ack, json.Unmarshal(body, &ack)
	case http.StatusConflict:
		_ = json.Unmarshal(body, &ack)
		return ack, &staleError{ack.Version}
	case http.StatusUnprocessableEntity:
		return ack, &errRefused{strings.TrimSpace(string(body))}
	}
	return ack, fmt.Errorf("edge: %s: %s", res.Status, bytes.TrimSpace(body))
}

func (c *Client) get(ctx context.Context, hc *http.Client, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://edge"+path, nil)
	if err != nil {
		return err
	}
	res, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("edge: not answering: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("edge: %s: %s", res.Status, bytes.TrimSpace(b))
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// Busy counts the requests in flight on the named app instances.
func (c *Client) Busy(names []string) (int64, error) {
	q := url.Values{"name": names}
	var n int64
	err := c.get(context.Background(), c.hc, "/v1/busy?"+q.Encode(), &n)
	return n, err
}

// Activity is when each app environment last had a request.
func (c *Client) Activity() (map[string]time.Time, error) {
	var m map[string]time.Time
	err := c.get(context.Background(), c.hc, "/v1/activity", &m)
	return m, err
}

// certStatus asks the edge for a host's certificate.
func (c *Client) certStatus(host string) CertInfo {
	var ci CertInfo
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.get(ctx, c.hc, "/v1/cert?host="+url.QueryEscape(host), &ci); err != nil {
		return CertInfo{Host: strings.ToLower(host), State: "none", Error: err.Error(), CheckedAt: time.Now()}
	}
	return ci
}

// followEvents passes the edge's certificate events to this process's
// OnCertEvent hooks.
func (c *Client) followEvents(ctx context.Context) {
	var seq uint64
	first := true
	for ctx.Err() == nil {
		var b EventBatch
		if err := c.get(ctx, c.poll, "/v1/events?after="+strconv.FormatUint(seq, 10), &b); err != nil {
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
			continue
		}
		if !first { // events from before this process started are old news
			for _, ev := range b.Events {
				recordEvent(ev)
			}
		}
		seq, first = b.Seq, false
	}
}

// ControlHandler answers the edge's calls into the control plane (see
// proto.go); ctl is the runtime's side of the switchboard, nil without one.
func ControlHandler(ctl switchboard.Control) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/wake", func(w http.ResponseWriter, r *http.Request) {
		if ctl == nil {
			http.Error(w, "this box runs no apps", http.StatusServiceUnavailable)
			return
		}
		woke, err := ctl.Wake(r.Context(), r.URL.Query().Get("env"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, map[string]bool{"woke": woke})
	})
	mux.HandleFunc("POST /v1/woke", func(w http.ResponseWriter, r *http.Request) {
		secs, _ := strconv.ParseFloat(r.URL.Query().Get("secs"), 64)
		if ctl != nil {
			ctl.WokeFirstByte(r.URL.Query().Get("env"), secs)
		}
		writeJSON(w, true)
	})
	mux.HandleFunc("POST /v1/dns", func(w http.ResponseWriter, r *http.Request) {
		var call dnsCall
		if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		providersMu.Lock()
		p := providers[call.Key]
		providersMu.Unlock()
		if p == nil {
			http.Error(w, "no DNS provider "+call.Key, http.StatusNotFound)
			return
		}
		recs, err := parseRRs(call.Records)
		if err == nil {
			if call.Delete {
				recs, err = p.DeleteRecords(r.Context(), call.Zone, recs)
			} else {
				recs, err = p.AppendRecords(r.Context(), call.Zone, recs)
			}
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		out := make([]libdns.RR, len(recs))
		for i, rec := range recs {
			out[i] = rec.RR()
		}
		writeJSON(w, out)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Header[ForwardEnv]; ok {
			env := r.Header.Get(ForwardEnv)
			prefix := r.Header.Get(ForwardPrefix)
			r.Header.Del(ForwardEnv)
			r.Header.Del(ForwardPrefix)
			if ctl == nil {
				http.NotFound(w, r)
				return
			}
			ctl.Forward(w, r, env, prefix)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// ServeControl answers the edge's calls on the control socket at path
// until ctx ends.
func ServeControl(ctx context.Context, path string, ctl switchboard.Control) error {
	ln, err := listenUnix(path)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: ControlHandler(ctl), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	return nil
}
