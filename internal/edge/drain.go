package edge

import (
	"encoding/json"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/caddyserver/caddy/v2"
)

// Draining. caddy.Stop stops Caddy's servers from accepting and returns:
// unless Caddy itself exits the process, it does not wait for the requests
// under way. The edge process counts the connections Caddy's servers hold
// open (a listener wrapper, outside TLS) and waits for them on shutdown
// before it closes the switchboard those requests go through; otherwise
// they fail with a 502, or reach the next edge's switchboard before it has
// a table (404). Caddy's shutdown closes each connection once its requests
// finish, so the count drains to zero. HTTP/3 connections are not counted.

func init() { caddy.RegisterModule(connCounter{}) }

var openConns atomic.Int64

// connCounter is the listener wrapper that counts accepted connections.
type connCounter struct{}

func (connCounter) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "caddy.listeners.tiffin_conns", New: func() caddy.Module { return connCounter{} }}
}

func (connCounter) WrapListener(ln net.Listener) net.Listener { return countedListener{ln} }

type countedListener struct{ net.Listener }

func (l countedListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return c, err
	}
	openConns.Add(1)
	return &countedConn{Conn: c}, nil
}

type countedConn struct {
	net.Conn
	once sync.Once
}

func (c *countedConn) Close() error {
	c.once.Do(func() { openConns.Add(-1) })
	return c.Conn.Close()
}

// waitConns waits until Caddy holds no connection, for up to max. It
// reports whether they all closed.
func waitConns(max time.Duration) bool {
	deadline := time.Now().Add(max)
	for openConns.Load() > 0 {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
	return true
}

// counted adds the connection counter to every server of a Caddy config,
// ahead of TLS (Go's HTTP server needs the TLS connection itself to see
// HTTP/2).
func counted(raw json.RawMessage) (json.RawMessage, error) {
	if raw == nil {
		return nil, nil
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	apps, _ := cfg["apps"].(map[string]any)
	httpApp, _ := apps["http"].(map[string]any)
	servers, _ := httpApp["servers"].(map[string]any)
	for _, v := range servers {
		if srv, ok := v.(map[string]any); ok {
			srv["listener_wrappers"] = []any{map[string]any{"wrapper": "tiffin_conns"}, map[string]any{"wrapper": "tls"}}
		}
	}
	return json.Marshal(cfg)
}
