// Package edge runs Caddy v2 as an embedded library: automatic HTTPS from a
// local CA, host routing and security headers for the Tiffin box.
//
// Caddy keeps process-wide state, so at most one Edge can run per process.
package edge

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"

	// Only the Caddy modules the generated config uses.
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/headers"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy"
	_ "github.com/caddyserver/caddy/v2/modules/caddypki"
	_ "github.com/caddyserver/caddy/v2/modules/caddytls"
	_ "github.com/caddyserver/caddy/v2/modules/filestorage"
	_ "github.com/caddyserver/caddy/v2/modules/logging"
)

var (
	activeMu sync.Mutex
	active   *Edge
)

// Edge is a running embedded Caddy.
type Edge struct {
	mu      sync.Mutex
	cfg     Config
	stopped bool
}

// Start loads the Caddy config and returns once the edge is serving HTTPS.
// Cancelling ctx stops the edge.
func Start(ctx context.Context, cfg Config) (*Edge, error) {
	c, err := cfg.normalized()
	if err != nil {
		return nil, err
	}
	activeMu.Lock()
	defer activeMu.Unlock()
	if active != nil {
		return nil, errors.New("edge: already running in this process")
	}
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("edge: create data dir: %w", err)
	}
	if err := load(c); err != nil {
		_ = caddy.Stop()
		return nil, err
	}
	e := &Edge{cfg: c}
	if err := e.waitReady(ctx, c); err != nil {
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
	c, err := cfg.normalized()
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopped {
		return errors.New("edge: stopped")
	}
	if c.DataDir != e.cfg.DataDir {
		return errors.New("edge: DataDir cannot change on reload")
	}
	if err := load(c); err != nil {
		return err
	}
	e.cfg = c
	return e.waitReady(context.Background(), c)
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

func load(c Config) error {
	raw, err := ConfigJSON(c)
	if err != nil {
		return err
	}
	if err := caddy.Load(raw, true); err != nil {
		return fmt.Errorf("edge: load caddy config: %w", err)
	}
	return nil
}

// waitReady blocks until every managed host presents a certificate, because
// Caddy issues certificates asynchronously after Load returns.
func (e *Edge) waitReady(ctx context.Context, c Config) error {
	hosts := []string{c.DashboardHost()}
	for _, r := range c.Routes {
		hosts = append(hosts, r.Host)
	}
	deadline := time.Now().Add(15 * time.Second)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(c.HTTPSPort))
	for _, h := range hosts {
		var lastErr error
		for {
			if lastErr = probe(addr, h); lastErr == nil {
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
