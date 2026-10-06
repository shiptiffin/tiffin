// Package edge runs Caddy v2 as an embedded library: automatic HTTPS from a
// local CA, host routing and security headers for the Tiffin box.
//
// Caddy keeps process-wide state, so at most one Edge can run per process.
package edge

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"

	// Only the Caddy modules the generated config uses.
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/encode"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/encode/brotli" // precompressed .br files only
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/encode/gzip"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/encode/zstd"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/fileserver"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/headers"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/logging"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/rewrite"
	_ "github.com/caddyserver/caddy/v2/modules/caddypki"
	_ "github.com/caddyserver/caddy/v2/modules/caddytls"
	_ "github.com/caddyserver/caddy/v2/modules/filestorage"
	_ "github.com/caddyserver/caddy/v2/modules/logging"
)

var (
	activeMu sync.Mutex
	active   *Edge
)

// Edge is a running embedded Caddy, configured in this process (tests and
// the edge's own process run it; the box's control plane drives an edge
// process through a Client instead).
type Edge struct {
	mu      sync.Mutex
	cfg     Config
	stopped bool
}

// Start loads the Caddy config and returns once the edge is serving HTTPS.
// Cancelling ctx stops the edge.
func Start(ctx context.Context, cfg Config) (*Edge, error) {
	r, err := render(cfg)
	if err != nil {
		return nil, err
	}
	activeMu.Lock()
	defer activeMu.Unlock()
	if active != nil {
		return nil, errors.New("edge: already running in this process")
	}
	if err := os.MkdirAll(r.cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("edge: create data dir: %w", err)
	}
	e := &Edge{cfg: r.cfg}
	if err := r.load(ctx); err != nil {
		_ = caddy.Stop()
		return nil, err
	}
	active = e
	if done := ctx.Done(); done != nil {
		go func() {
			<-done
			_ = e.Stop()
		}()
	}
	return e, nil
}

// Reload atomically swaps in a new config (for example with more routes).
// The DataDir cannot change on reload.
func (e *Edge) Reload(cfg Config) error {
	r, err := render(cfg)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopped {
		return errors.New("edge: stopped")
	}
	if r.cfg.DataDir != e.cfg.DataDir {
		return errors.New("edge: DataDir cannot change on reload")
	}
	e.cfg = r.cfg
	return r.load(context.Background())
}

// Stop shuts the edge down and releases its ports. It is safe to call twice.
func (e *Edge) Stop() error {
	activeMu.Lock()
	defer activeMu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopped {
		return nil
	}
	e.stopped = true
	if active == e {
		active = nil
	}
	return caddy.Stop()
}

// RootCAPEM returns the internal CA root certificate (PEM) clients should trust.
func (e *Edge) RootCAPEM() ([]byte, error) {
	e.mu.Lock()
	dir := e.cfg.DataDir
	e.mu.Unlock()
	return RootCAPEM(dir)
}

// RootCAPEM reads the internal CA root certificate (PEM) from a Caddy data
// directory. It works while the edge runs in another process or not at all.
func RootCAPEM(dataDir string) ([]byte, error) {
	pem, err := os.ReadFile(filepath.Join(dataDir, "pki", "authorities", caID, "root.crt"))
	if err != nil {
		return nil, fmt.Errorf("edge: root CA not available: %w", err)
	}
	return pem, nil
}

// Rendered is the edge's configuration as Caddy loads it. The control
// plane renders it from a Config (render) and the edge process loads it
// (load), so a new Tiffin build changes what the edge serves without the
// edge process restarting.
type Rendered struct {
	Config json.RawMessage `json:"config"`
	// Fallback is Config without the protection layer, loaded when Caddy
	// refuses Config, so the box stays reachable (see ProtectionStatus).
	Fallback json.RawMessage `json:"fallback,omitempty"`
	// Allowed are the hosts that may get a certificate (the ask gate).
	Allowed []string `json:"allowed,omitempty"`
	// DNS names the DNS provider of the wildcard certificate's challenge;
	// the process that rendered the config holds it.
	DNS string `json:"dns,omitempty"`
	// Probe is how load knows the config is served.
	Probe Probe `json:"probe"`
}

// Probe: the HTTPS listener (127.0.0.1:<port>) answers, and with the
// internal CA every one of Hosts presents a certificate. Caddy issues
// certificates asynchronously after it loads a config. With ACME, public
// certificates take seconds to minutes and some names only get one on
// their first visit, so the listener is enough.
type Probe struct {
	Addr  string   `json:"addr"`
	Hosts []string `json:"hosts,omitempty"`
}

// rendered is a render's result with what the renderer needs afterwards.
type rendered struct {
	Rendered
	cfg     Config      // normalized, with the cert source merged in
	prot    *Protection // the protection layer in Config
	protErr error       // why the registered protection is not in Config
}

// render builds the Caddy configs for cfg, with the registered protection
// layer when cfg has none of its own. It registers the wildcard's DNS
// provider in this process.
func render(cfg Config) (*rendered, error) {
	c, err := withCertSource(cfg).normalized()
	if err != nil {
		return nil, err
	}
	r := &rendered{cfg: c, prot: c.Protect}
	protectMu.Lock()
	src := protectSource
	protectMu.Unlock()
	if c.Protect == nil && src != nil {
		if p := src(); p != nil {
			if err := p.validate(); err != nil {
				r.prot, r.protErr = nil, fmt.Errorf("edge: protection not applied: %w", err)
			} else {
				pc := c
				pc.Protect = p
				if r.Config, err = json.Marshal(buildConfig(pc)); err != nil {
					return nil, err
				}
				r.prot = p
			}
		}
	}
	plain, err := json.Marshal(buildConfig(c))
	if err != nil {
		return nil, err
	}
	if r.Config == nil {
		r.Config = plain
	} else {
		r.Fallback = plain
	}
	for h := range c.Allowed() {
		r.Allowed = append(r.Allowed, h)
	}
	sort.Strings(r.Allowed)
	if c.ACME != nil && c.ACME.Wildcard != nil {
		r.DNS = c.ACME.Wildcard.key()
		providersMu.Lock()
		providers[r.DNS] = c.ACME.Wildcard.Provider
		providersMu.Unlock()
	}
	r.Probe.Addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(c.HTTPSPort))
	if c.ACME == nil {
		r.Probe.Hosts = c.dashboardHosts()
		for _, rt := range c.Routes {
			r.Probe.Hosts = append(r.Probe.Hosts, c.hostsFor(rt.Host)...)
		}
	}
	return r, nil
}

// load loads r into this process's Caddy, records what protection is
// enforced, and waits until the config is served.
func (r *rendered) load(ctx context.Context) error {
	fallback, err := r.Rendered.load(ctx)
	switch {
	case err != nil:
	case fallback != nil:
		setProtectState(nil, fallback)
	case r.protErr != nil:
		setProtectState(nil, r.protErr)
	default:
		setProtectState(r.prot, nil)
	}
	return err
}

// load loads r into this process's Caddy and waits until it is served.
// fallback says why Caddy refused Config when it serves Fallback instead.
func (r *Rendered) load(ctx context.Context) (fallback, err error) {
	set := make(map[string]bool, len(r.Allowed))
	for _, h := range r.Allowed {
		set[h] = true
	}
	allowed.Store(&set)
	if r.DNS != "" {
		providersMu.Lock()
		if providers[r.DNS] == nil && remoteDNS != nil {
			providers[r.DNS] = remoteDNS(r.DNS)
		}
		providersMu.Unlock()
	}
	if err = caddyLoad(r.Config, r.Fallback != nil); err != nil && r.Fallback != nil {
		fallback = fmt.Errorf("edge: protection not applied: %w", err)
		err = caddyLoad(r.Fallback, false)
	}
	if err != nil {
		return nil, err
	}
	return fallback, waitReady(ctx, r.Probe)
}

// caddyLoad loads a Caddy JSON config. Not forced: an identical config is
// a no-op, so the frequent route refreshes do not interrupt certificates
// being obtained. safe turns a panic in a third-party Caddy module's setup
// into an error instead of taking the edge down.
func caddyLoad(raw json.RawMessage, safe bool) (err error) {
	if safe {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("edge: caddy module panicked while loading: %v", r)
			}
		}()
	}
	if err := caddy.Load(raw, false); err != nil {
		return fmt.Errorf("edge: load caddy config: %w", err)
	}
	return nil
}

// waitReady blocks until the probe passes (see Probe).
func waitReady(ctx context.Context, p Probe) error {
	if len(p.Hosts) == 0 {
		return waitListening(ctx, p.Addr)
	}
	deadline := time.Now().Add(15 * time.Second)
	for _, h := range p.Hosts {
		var lastErr error
		for {
			if lastErr = probe(p.Addr, h); lastErr == nil {
				break
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("edge: not ready for %s: %w", h, lastErr)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	return nil
}

func waitListening(ctx context.Context, addr string) error {
	deadline := time.Now().Add(15 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			return conn.Close()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("edge: HTTPS listener not up: %w", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Config returns the config the edge runs now (normalized, with the cert
// source merged in).
func (e *Edge) Config() Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg
}

func probe(addr, host string) error {
	d := net.Dialer{Timeout: time.Second}
	conn, err := tls.DialWithDialer(&d, "tcp", addr, &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: true, // only checking that a certificate is served
	})
	if err != nil {
		return err
	}
	return conn.Close()
}
