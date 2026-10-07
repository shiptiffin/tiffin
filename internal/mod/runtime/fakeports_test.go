package runtime

import (
	"net"
	"os"
	"strconv"
	"syscall"
)

// The fake engine holds each app port from allocation (HoldPort) until its
// container is removed: first a socket bound to it, then the container's
// listener. No other process can bind the port meanwhile. Without this,
// another test binary using the same range (go test ./..., or -count runs
// side by side) could start a server on a crashed instance's port, whose
// health check then passed: the crash went live.

// HoldPort binds port on 127.0.0.1 for a container to come.
func (e *fakeEngine) HoldPort(port int) bool {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		return false
	}
	syscall.CloseOnExec(fd)
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Port: port, Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		syscall.Close(fd)
		return false // something else has it
	}
	e.mu.Lock()
	e.held[port] = fd
	e.mu.Unlock()
	return true
}

// ReleasePort unbinds a held port no container took.
func (e *fakeEngine) ReleasePort(port int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if fd, ok := e.held[port]; ok {
		syscall.Close(fd)
		delete(e.held, port)
	}
}

// closeAll stops every container's server and unbinds every port (a
// test's end).
func (e *fakeEngine) closeAll() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, c := range e.ctrs {
		if c.srv != nil {
			c.srv.Close()
		}
		if c.dead != nil {
			c.dead.Close()
		}
	}
	for p, fd := range e.held {
		syscall.Close(fd)
		delete(e.held, p)
	}
}

// listen gives a container its port's listener: the held socket, now
// listening, so the port is never free in between. A port not held (a
// container started again after a stop) is listened on afresh. Call with
// e.mu held.
func (e *fakeEngine) listen(port int) (net.Listener, error) {
	fd, ok := e.held[port]
	if !ok {
		return net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	}
	delete(e.held, port)
	// As real servers do (Go, Node, Bun): the connections it accepts then
	// leave the port free for a new server as soon as it closes.
	_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	if err := syscall.Listen(fd, syscall.SOMAXCONN); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "port")
	defer f.Close() // FileListener has its own copy
	return net.FileListener(f)
}

// refuse answers every connection with a reset, as the port of a container
// whose process died does (macOS drops connects to a port that is bound but
// not listening, so a held socket alone would hang the health check).
func refuse(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		if tc, ok := c.(*net.TCPConn); ok {
			_ = tc.SetLinger(0)
		}
		c.Close()
	}
}
