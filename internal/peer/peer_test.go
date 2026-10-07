package peer

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
)

// A port no app has is never dialed.
func TestDialerRefusesPortsNoAppHas(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	d := &Dialer{Owner: func(p int) (string, bool) { return "", p != port }}
	if _, err := d.DialContext(context.Background(), "tcp", ln.Addr().String()); err == nil || !strings.Contains(err.Error(), "no app has port "+strconv.Itoa(port)) {
		t.Fatalf("dial = %v, want refused", err)
	}
	d.Owner = func(int) (string, bool) { return "", true }
	c, err := d.DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("unchecked dial: %v", err)
	}
	c.Close()
}
