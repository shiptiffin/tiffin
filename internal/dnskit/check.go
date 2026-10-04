package dnskit

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// Record is one DNS record, in the words DNS panels use.
type Record struct {
	Type  string `json:"type" doc:"A, AAAA, CNAME, TXT or CAA."`
	Name  string `json:"name" doc:"The full name, e.g. shop.example.com or *.example.com."`
	Host  string `json:"host" doc:"The name as most DNS panels want it, relative to the zone: @ for the zone itself, * for the wildcard, shop for shop.example.com."`
	Value string `json:"value"`
	TTL   int    `json:"ttl,omitempty" doc:"Seconds; 0 or absent means the provider's default (automatic)."`
}

func (r Record) String() string { return fmt.Sprintf("%s %s %s", r.Name, r.Type, r.Value) }

// Zone returns the registrable domain a name belongs to ("shop.example.co.uk"
// → "example.co.uk"), which is where its records usually live.
func Zone(name string) string {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	z, err := publicsuffix.EffectiveTLDPlusOne(strings.TrimPrefix(name, "*."))
	if err != nil {
		return name
	}
	return z
}

// RelHost is name relative to zone: "@" for the zone itself.
func RelHost(name, zone string) string {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	if name == zone {
		return "@"
	}
	return strings.TrimSuffix(name, "."+zone)
}

// IsApex reports whether name is a registrable domain (no CNAME allowed).
func IsApex(name string) bool { return Zone(name) == strings.TrimSuffix(strings.ToLower(name), ".") }

// AddressRecords are the A/AAAA records that point name at the box.
func AddressRecords(name string, box []netip.Addr) []Record {
	zone := Zone(name)
	var out []Record
	for _, a := range box {
		t := "A"
		if a.Is6() && !a.Is4In6() {
			t = "AAAA"
		}
		out = append(out, Record{Type: t, Name: name, Host: RelHost(name, zone), Value: a.Unmap().String()})
	}
	return out
}

// CNAMERecord points name at target (a name that resolves to the box).
func CNAMERecord(name, target string) Record {
	return Record{Type: "CNAME", Name: name, Host: RelHost(name, Zone(name)), Value: target}
}

// PointsHere decides whether an answer reaches the box: at least one of its
// addresses is the box's, and none is someone else's (browsers and Let's
// Encrypt may pick any of them, IPv6 first). reason says what is wrong in
// plain words.
func PointsHere(ans Answer, box []netip.Addr) (ok bool, reason string) {
	if len(ans.Addrs) == 0 {
		if ans.CNAME != "" {
			return false, fmt.Sprintf("it is an alias of %s, which has no address", ans.CNAME)
		}
		return false, "no A or AAAA record yet"
	}
	boxV6 := slices.ContainsFunc(box, func(a netip.Addr) bool { return a.Is6() && !a.Is4In6() })
	var wrong, cf []string
	match := false
	for _, a := range ans.Addrs {
		switch {
		case slices.Contains(box, a):
			match = true
		case IsCloudflare(a):
			cf = append(cf, a.String())
		case a.Is6() && !boxV6:
			return false, fmt.Sprintf("it has an AAAA record (%s) but this box has no IPv6 address; delete that AAAA record (Let's Encrypt and many browsers try IPv6 first)", a)
		default:
			wrong = append(wrong, a.String())
		}
	}
	switch {
	case len(cf) > 0:
		return false, fmt.Sprintf("it points to Cloudflare's proxy (%s), so the box cannot answer for it; set the record to \"DNS only\" (grey cloud) in Cloudflare", strings.Join(cf, ", "))
	case len(wrong) > 0:
		return false, fmt.Sprintf("it points to %s, not this box (%s)", strings.Join(wrong, ", "), joinAddrs(box))
	case !match:
		return false, "no A or AAAA record yet"
	}
	return true, ""
}

func joinAddrs(as []netip.Addr) string {
	s := make([]string, len(as))
	for i, a := range as {
		s[i] = a.String()
	}
	return strings.Join(s, ", ")
}

var cloudflareRanges = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18",
		"108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17",
		"162.158.0.0/15", "104.16.0.0/13", "104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
		"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32", "2405:8100::/32",
		"2a06:98c0::/29", "2c0f:f248::/32",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// IsCloudflare reports whether an address is in Cloudflare's proxy ranges
// (an "orange cloud" record).
func IsCloudflare(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range cloudflareRanges {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// SslipDomain is the zero-setup box domain for a public IPv4 address:
// 203.0.113.7 → "203-0-113-7.sslip.io". sslip.io answers every name under
// it with the address it spells, so <app>.203-0-113-7.sslip.io reaches the
// box without any DNS setup. Empty for anything but IPv4.
func SslipDomain(a netip.Addr) string {
	a = a.Unmap()
	if !a.Is4() {
		return ""
	}
	return strings.ReplaceAll(a.String(), ".", "-") + ".sslip.io"
}

// SslipAddr parses the address out of an sslip.io name
// ("dashboard.203-0-113-7.sslip.io" → 203.0.113.7), so a client can reach
// it without asking DNS.
func SslipAddr(host string) (netip.Addr, bool) {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	rest, ok := strings.CutSuffix(host, ".sslip.io")
	if !ok {
		return netip.Addr{}, false
	}
	if i := strings.LastIndex(rest, "."); i >= 0 {
		rest = rest[i+1:]
	}
	a, err := netip.ParseAddr(strings.ReplaceAll(rest, "-", "."))
	if err != nil || !a.Is4() {
		return netip.Addr{}, false
	}
	return a, true
}
