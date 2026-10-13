// Package peer checks who answers a TCP connection the box opens to an app.
//
// Apps run on the host network, so any app can listen on any free port. An
// app's port is free while it sleeps or restarts, and the app of another
// project could take it and answer in its place: it would get the requests
// meant for the app, cookies and job payloads included. So every connection
// the box opens to an app port is checked before anything is sent on it.
// The kernel's socket diagnostics (sock_diag) name the cgroup of the socket
// at the far end of the connection, which is the cgroup of the process that
// created the listening socket; it must lie inside the app's project slice.
// The check is about this very connection, so nothing can change between
// the check and the request, and it costs some 10-60µs per new connection.
package peer

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
)

// ForeignError is a connection answered by a process outside the cgroup
// that owns the port: another project's app holds it.
type ForeignError struct {
	Addr string // the address dialed
	Want string // the cgroup directory that owns it
}

func (e *ForeignError) Error() string {
	return fmt.Sprintf("%s is answered by a process outside %s (another app holds the port)", e.Addr, filepath.Base(e.Want))
}

// Check verifies that c, a TCP connection to a loopback address, was
// accepted by a socket a process in the cgroup directory dir (or below it)
// created. dir "" checks nothing (an engine whose apps are not containers:
// tests). Off Linux it checks nothing either: apps run on Linux boxes only.
func Check(c net.Conn, dir string) error {
	if dir == "" {
		return nil
	}
	return check(c, dir)
}

// Dialer dials app ports and checks every connection with Check against
// the cgroup that owns the port: the one WithOwner put in the dial's
// context, else the one Owner names.
type Dialer struct {
	Dialer net.Dialer
	// Owner is the cgroup directory whose processes may answer port. ok
	// false refuses the port (no app has it); dir "" skips the check.
	// Nil refuses every port the context names no owner for.
	Owner func(port int) (dir string, ok bool)
}

type ownerKey struct{}

// WithOwner names the cgroup directory whose processes may answer the
// connections dialed with ctx ("" checks nothing). http.Transport dials
// with the request's context, so a request can carry it.
func WithOwner(ctx context.Context, dir string) context.Context {
	return context.WithValue(ctx, ownerKey{}, dir)
}

// DialContext is for http.Transport.
func (d *Dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	_, ps, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(ps)
	if err != nil {
		return nil, fmt.Errorf("peer: port of %s: %w", addr, err)
	}
	dir, ok := ctx.Value(ownerKey{}).(string)
	if !ok && d.Owner != nil {
		dir, ok = d.Owner(port)
	}
	if !ok {
		return nil, fmt.Errorf("peer: no app has port %d", port)
	}
	c, err := d.Dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	if err := Check(c, dir); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}
