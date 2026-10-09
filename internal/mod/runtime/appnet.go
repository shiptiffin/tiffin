package runtime

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/shiptiffin/tiffin/internal/peer"
)

// Apps listen on loopback ports of the host network, where any app could
// take a port another app left free (asleep, restarting). Every connection
// the box opens to an app goes through appDialer, which only dials ports of
// the runtime's port table and checks that the process answering belongs
// to the project that owns the port (internal/peer). The switchboard does
// the same for requests (switchboard.Env.Cgroup).

// peerDir is the cgroup whose processes may answer the project's app
// ports ("" checks nothing: see Options.PeerCgroup).
func (r *rt) peerDir(project string) string {
	if r.opt.PeerCgroup == nil {
		return ""
	}
	return r.opt.PeerCgroup(project)
}

// portOwner is the cgroup that may answer an allocated app port; a port
// the runtime did not allocate is nobody's.
func (r *rt) portOwner(port int) (string, bool) {
	r.mu.Lock()
	name, ok := r.ports[port]
	r.mu.Unlock()
	if !ok {
		return "", false
	}
	return r.peerDir(projectOfContainer(name)), true
}

// projectOfContainer reads the project from a container name (containerName).
func projectOfContainer(name string) string {
	parts := strings.SplitN(name, ".", 3)
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

func (r *rt) appDialer(timeout time.Duration) *peer.Dialer {
	return &peer.Dialer{Dialer: net.Dialer{Timeout: timeout}, Owner: r.portOwner}
}

// appClient is an HTTP client for the box's own requests to app instances
// (health checks, smoke tests, icons): one connection per request, so each
// is checked.
func (r *rt) appClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		Transport:     &http.Transport{DialContext: r.appDialer(5 * time.Second).DialContext, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// DialApp dials an app instance's port ("127.0.0.1:PORT", as AppEndpoint
// gives it) for the queue module's deliveries, checking who answers.
func (m *Module) DialApp(ctx context.Context, network, addr string) (net.Conn, error) {
	r, err := m.rt()
	if err != nil {
		return nil, err
	}
	return r.appDialer(10*time.Second).DialContext(ctx, network, addr)
}
