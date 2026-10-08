package edge

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/btahir/tiffin/internal/edge/switchboard"
)

// The box runs its edge in its own process (`tiffin edge`, unit
// tiffin-edge), apart from the control plane (`tiffin serve`, unit tiffin):
// restarting, upgrading or crashing the control plane never interrupts what
// the edge serves. The two talk over unix sockets in the platform directory.
//
// The control plane drives the edge (edge.sock, see Server):
//
//	GET  /v1/hello          the edge's protocol, build, switchboard address and applied snapshot version
//	PUT  /v1/snapshot       a Snapshot; the answer (an Ack) comes once the edge serves it
//	GET  /v1/busy?name=...  requests in flight on app instances (drains wait for 0)
//	GET  /v1/activity       when each app environment last had a request
//	GET  /v1/cert?host=     the certificate the edge holds for a host
//	GET  /v1/events?after=  certificate events (long poll)
//
// The edge calls back for what needs the control plane (control.sock, see
// ControlHandler):
//
//	POST /v1/wake?env=      start a sleeping app; answered once the edge has its instances
//	POST /v1/woke?env=&secs= a woken app's first byte
//	POST /v1/dns            ACME DNS-01 records, through the provider the control plane holds
//	(any)                   requests carrying ForwardEnv: live progress and bucket images
//
// While the control plane is down the edge serves on from its last
// snapshot (kept on disk, so a restarted edge does too); a sleeping app
// gets a 503 with Retry-After, and activity waits in the edge until the
// control plane asks for it.

// Protocol is the version of the API between the two processes. A change
// that an edge of the previous version would misread bumps it. 2: the
// switchboard's address may be a Unix socket ("unix/<path>"). 3: the
// deploy-address gate (tiffin_gate) and the switchboard's Gone pages.
const Protocol = 3

// Socket paths in the platform directory.
const (
	// SwitchboardSocket is where the box's edge process runs the
	// switchboard: a socket, not a loopback port, which an app (they share
	// the host network) could take while the edge restarts.
	SwitchboardSocket = "switchboard.sock"
	EdgeSocket        = "edge.sock"
	ControlSocket     = "control.sock"
	SnapshotFile      = "edge-snapshot.json"
)

// Snapshot is everything the edge serves. Version increases with every
// snapshot the control plane sends; the edge refuses an older one.
type Snapshot struct {
	Version uint64 `json:"version"`
	// Caddy is nil until the control plane has routes: the edge keeps the
	// config it has (or starts Caddy with the first one).
	Caddy *Rendered `json:"caddy,omitempty"`
	// Table is nil until the runtime has one: the edge keeps its own.
	Table *switchboard.Table `json:"table,omitempty"`
}

// Ack is the edge's answer to a snapshot it serves.
type Ack struct {
	Version uint64 `json:"version"`
	// Fallback says why Caddy refused the protected config, when the edge
	// serves the unprotected one instead.
	Fallback string `json:"fallback,omitempty"`
}

// Hello describes a running edge.
type Hello struct {
	Protocol int    `json:"protocol"`
	Build    string `json:"build"`
	PID      int    `json:"pid"`
	// Started tells one run of the edge from the next (a restart keeps the
	// PID in tests, where every edge runs in the test's process).
	Started     time.Time `json:"started"`
	Version     uint64    `json:"version"` // the snapshot it serves (0: none)
	Switchboard string    `json:"switchboard"`
	Caddy       bool      `json:"caddy"` // whether Caddy runs (it starts with the first config)
	Fallback    string    `json:"fallback,omitempty"`
}

// ForwardEnv and ForwardPrefix carry a request the switchboard forwards to
// the control plane, with its app environment and route prefix.
const (
	ForwardEnv    = "Tiffin-Forward-Env"
	ForwardPrefix = "Tiffin-Forward-Prefix"
)

// unixClient talks HTTP over a unix socket.
func unixClient(path string, timeout time.Duration) *http.Client {
	d := net.Dialer{Timeout: 2 * time.Second}
	return &http.Client{Timeout: timeout, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return d.DialContext(ctx, "unix", path)
		},
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     30 * time.Second,
	}}
}

// listenUnix listens on a unix socket only root (the owner) may use,
// replacing a stale one.
func listenUnix(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}
