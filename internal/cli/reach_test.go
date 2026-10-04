package cli

import (
	"net/netip"
	"testing"
)

func TestAnyRoutable(t *testing.T) {
	for ip, want := range map[string]bool{
		"203.0.113.7": true, "2001:db8::1": true,
		"192.168.64.2": false, "10.0.0.5": false, "127.0.0.1": false, "100.72.1.2": false, "fd00::1": false,
	} {
		if got := anyRoutable([]netip.Addr{netip.MustParseAddr(ip)}); got != want {
			t.Errorf("%s: %v, want %v", ip, got, want)
		}
	}
}
