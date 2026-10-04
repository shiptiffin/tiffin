// Package dnskit answers the DNS questions a box asks about its names (where
// does this name point, may Let's Encrypt issue for it) and manages records
// through DNS providers (libdns) such as Cloudflare.
package dnskit

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// DefaultServers are the recursive resolvers the box asks. Public ones, not
// the machine's own cache, so a record someone just added is seen as soon
// as the world sees it (and a stale negative answer does not linger).
var DefaultServers = []string{"1.1.1.1:53", "8.8.8.8:53", "9.9.9.9:53"}

// Resolver queries recursive DNS servers directly.
type Resolver struct {
	// Servers are host:port addresses. Empty: DefaultServers.
	Servers []string
	// Timeout per query. Default 3s.
	Timeout time.Duration
}

// ErrDNS is a lookup that got no usable answer (timeouts, SERVFAIL). A name
// that does not exist is not an error: it just has no records.
var ErrDNS = errors.New("DNS lookup failed")

func (r *Resolver) servers() []string {
	if len(r.Servers) > 0 {
		return r.Servers
	}
	return DefaultServers
}

// query asks each server in turn until one answers.
func (r *Resolver) query(ctx context.Context, name string, qtype uint16) (*dns.Msg, error) {
	timeout := r.Timeout
	if timeout == 0 {
		timeout = 3 * time.Second
	}
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), qtype)
	m.RecursionDesired = true
	m.SetEdns0(1232, false)
	var last error
	for _, s := range r.servers() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c := &dns.Client{Net: "udp", Timeout: timeout}
		in, _, err := c.ExchangeContext(ctx, m, s)
		if err == nil && in.Truncated {
			c.Net = "tcp"
			in, _, err = c.ExchangeContext(ctx, m, s)
		}
		if err != nil {
			last = err
			continue
		}
		switch in.Rcode {
		case dns.RcodeSuccess, dns.RcodeNameError:
			return in, nil
		default:
			last = fmt.Errorf("%s answered %s", s, dns.RcodeToString[in.Rcode])
		}
	}
	return nil, fmt.Errorf("%w for %s: %v", ErrDNS, name, last)
}

// Answer is where a name points.
type Answer struct {
	// Addrs are the A and AAAA addresses the name finally resolves to.
	Addrs []netip.Addr
	// CNAME is the first alias in the chain, if the name is an alias.
	CNAME string
}

// Lookup resolves name's A and AAAA records, following aliases.
func (r *Resolver) Lookup(ctx context.Context, name string) (Answer, error) {
	var out Answer
	for _, qt := range []uint16{dns.TypeA, dns.TypeAAAA} {
		in, err := r.query(ctx, name, qt)
		if err != nil {
			return out, err
		}
		for _, rr := range in.Answer {
			switch v := rr.(type) {
			case *dns.A:
				if a, ok := netip.AddrFromSlice(v.A.To4()); ok && !slices.Contains(out.Addrs, a) {
					out.Addrs = append(out.Addrs, a)
				}
			case *dns.AAAA:
				if a, ok := netip.AddrFromSlice(v.AAAA); ok && !slices.Contains(out.Addrs, a) {
					out.Addrs = append(out.Addrs, a)
				}
			case *dns.CNAME:
				if out.CNAME == "" && strings.EqualFold(dns.Fqdn(v.Hdr.Name), dns.Fqdn(name)) {
					out.CNAME = strings.TrimSuffix(strings.ToLower(v.Target), ".")
				}
			}
		}
	}
	return out, nil
}

// TXT returns name's TXT records (each record's strings joined).
func (r *Resolver) TXT(ctx context.Context, name string) ([]string, error) {
	in, err := r.query(ctx, name, dns.TypeTXT)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, rr := range in.Answer {
		if t, ok := rr.(*dns.TXT); ok {
			out = append(out, strings.Join(t.Txt, ""))
		}
	}
	return out, nil
}

// CAA is one CAA record.
type CAA struct {
	Flag  uint8  `json:"flag"`
	Tag   string `json:"tag"`
	Value string `json:"value"`
}

// CAASet returns the CAA records that govern name (RFC 8659): those of the
// closest name, walking up from name itself, that has any. owner is that
// name; empty when no CAA records exist anywhere (any CA may issue).
func (r *Resolver) CAASet(ctx context.Context, name string) (recs []CAA, owner string, err error) {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	for n := name; strings.Contains(n, "."); {
		in, err := r.query(ctx, n, dns.TypeCAA)
		if err != nil {
			return nil, "", err
		}
		for _, rr := range in.Answer {
			if c, ok := rr.(*dns.CAA); ok {
				recs = append(recs, CAA{Flag: c.Flag, Tag: strings.ToLower(c.Tag), Value: c.Value})
			}
		}
		if len(recs) > 0 {
			return recs, n, nil
		}
		_, n, _ = strings.Cut(n, ".")
	}
	return nil, "", nil
}

// CAAAllows reports whether a CAA set lets a CA with any of the given
// identifiers ("letsencrypt.org") issue for a name (wildcard: the
// issuewild records if there are any, else issue).
func CAAAllows(recs []CAA, wildcard bool, identifiers ...string) bool {
	tag := "issue"
	if wildcard && slices.ContainsFunc(recs, func(c CAA) bool { return c.Tag == "issuewild" }) {
		tag = "issuewild"
	}
	any := false
	for _, c := range recs {
		if c.Tag != tag {
			continue
		}
		any = true
		dom, _, _ := strings.Cut(c.Value, ";")
		dom = strings.ToLower(strings.TrimSpace(dom))
		if dom != "" && slices.Contains(identifiers, dom) {
			return true
		}
	}
	// Records exist but none of the issue kind: issuance is unrestricted.
	return !any
}

// IsPublic reports whether an address is reachable from the internet (not
// private, loopback, link-local, CGNAT or documentation space).
func IsPublic(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() {
		return false
	}
	for _, p := range nonPublic {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),   // carrier-grade NAT
	netip.MustParsePrefix("192.0.2.0/24"),    // TEST-NET-1
	netip.MustParsePrefix("198.51.100.0/24"), // TEST-NET-2
	netip.MustParsePrefix("203.0.113.0/24"),  // TEST-NET-3
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("fec0::/10"),       // site-local (old; Lima and QEMU user networking)
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved
}

// LocalPublicIPs lists this machine's public addresses, from its network
// interfaces. On most servers (Hetzner, DigitalOcean, OVH) the public
// address sits on the interface; behind 1:1 NAT (AWS, GCP) it does not,
// and it must be passed in (tiffin serve --public-ip).
func LocalPublicIPs() []netip.Addr {
	ifs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, ia := range ifs {
		p, err := netip.ParsePrefix(ia.String())
		if err != nil {
			continue
		}
		if a := p.Addr().Unmap(); IsPublic(a) && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	slices.SortFunc(out, func(a, b netip.Addr) int {
		if a.Is4() != b.Is4() {
			if a.Is4() {
				return -1
			}
			return 1
		}
		return a.Compare(b)
	})
	return out
}
