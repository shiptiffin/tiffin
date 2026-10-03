package api

import (
	"testing"
	"time"
)

func TestClientIP(t *testing.T) {
	for _, c := range []struct{ remote, xff, want string }{
		{"127.0.0.1:5555", "203.0.113.7", "203.0.113.7"},           // behind the edge
		{"127.0.0.1:5555", "10.0.0.1, 203.0.113.7", "203.0.113.7"}, // the edge appends the real address
		{"127.0.0.1:5555", "", "127.0.0.1"},                        // local caller
		{"198.51.100.5:4444", "203.0.113.7", "198.51.100.5"},       // not from the edge: header ignored
		{"[::1]:80", "2001:db8:1:2:3:4:5:6", "2001:db8:1:2::/64"},  // IPv6 per /64
		{"[2001:db8:1:2::9]:80", "", "2001:db8:1:2::/64"},
		{"127.0.0.1:5555", "not-an-ip", "127.0.0.1"}, // junk header
	} {
		if got := clientIP(c.remote, c.xff); got != c.want {
			t.Errorf("clientIP(%q, %q) = %q, want %q", c.remote, c.xff, got, c.want)
		}
	}
}

func TestIPLimiterRefills(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newIPLimiter(10)
	l.now = func() time.Time { return now }
	for i := range 10 {
		if ok, _ := l.allow("a"); !ok {
			t.Fatalf("call %d refused", i)
		}
	}
	ok, wait := l.allow("a")
	if ok || wait <= 0 || wait > 6*time.Second {
		t.Fatalf("11th call: %v %v", ok, wait)
	}
	if ok, _ := l.allow("b"); !ok {
		t.Fatal("other key refused")
	}
	now = now.Add(6 * time.Second) // one token back
	if ok, _ := l.allow("a"); !ok {
		t.Fatal("no refill")
	}
	if ok, _ := l.allow("a"); ok {
		t.Fatal("refilled too much")
	}
}
