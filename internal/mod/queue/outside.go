package queue

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/shiptiffin/tiffin/internal/dnskit"
)

// Calls to a URL outside the box (a cron's or queue's url). They go through
// their own client whose every connection, redirects included, is checked
// against the addresses the host resolves to at that moment: the box's own
// addresses and private, loopback, link-local and other non-public ranges are
// refused, so a job can never reach a service inside the box or its network.
// The connection goes to the address that was checked, so DNS cannot switch
// it in between. Up to maxRedirects redirects to other public addresses are
// followed; the signature is not passed on to a different host.

const maxRedirects = 3

// notPublic are ranges dnskit.IsPublic lets through that still must not be
// called: "this network", IETF protocol space, NAT64 and 6to4 (which embed
// any IPv4 address) and the IPv6 discard prefix.
var notPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("100::/64"),
}

// errRefused is an address a job may not call. Retrying cannot help, so the
// job goes to the dead letters with the reason.
type errRefused struct{ host, addr, why string }

func (e *errRefused) Error() string {
	if e.addr == "" || e.addr == e.host {
		return fmt.Sprintf("refused to call %s: %s; schedules and queues call only public addresses outside the box", e.host, e.why)
	}
	return fmt.Sprintf("refused to call %s: it resolves to %s, %s; schedules and queues call only public addresses outside the box", e.host, e.addr, e.why)
}

// guard decides which addresses a job may call.
type guard struct {
	allow []netip.Prefix      // non-public ranges allowed anyway (Config.AllowNets)
	self  func() []netip.Addr // the box's own addresses
	// lookup resolves host names (nil: the system resolver).
	lookup func(ctx context.Context, host string) ([]netip.Addr, error)
}

func (g *guard) check(host string, ip netip.Addr) error {
	ip = ip.Unmap()
	for _, p := range g.allow {
		if p.Contains(ip) {
			return nil
		}
	}
	why := ""
	switch {
	case ip.IsLoopback():
		why = "the box itself"
	case ip.IsPrivate():
		why = "a private address"
	case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast():
		why = "a link-local address"
	case !dnskit.IsPublic(ip):
		why = "not a public address"
	}
	for _, p := range notPublic {
		if why == "" && p.Contains(ip) {
			why = "not a public address"
		}
	}
	if g.self != nil {
		for _, s := range g.self() {
			if why == "" && s.Unmap() == ip {
				why = "the box's own address"
			}
		}
	}
	if why != "" {
		return &errRefused{host: host, addr: ip.String(), why: why}
	}
	return nil
}

// checkURL refuses at once what can be told without DNS: other schemes, and
// hosts that are a non-public address literal or name this machine.
func (g *guard) checkURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return invalid(fmt.Sprintf("%q is not a web address; use a full address such as https://hooks.example.com/digest", raw), "")
	}
	if u.User != nil {
		return invalid("the url must not carry a user name or password", "put credentials in the receiving service, and check the Tiffin-Signature header there")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if ip, err := netip.ParseAddr(host); err == nil {
		if err := g.check(host, ip); err != nil {
			return invalid(err.Error(), "")
		}
		return nil
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		if err := g.check(host, netip.AddrFrom4([4]byte{127, 0, 0, 1})); err != nil {
			return invalid(err.Error(), "")
		}
	}
	return nil
}

func (g *guard) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip}, nil
	}
	if g.lookup != nil {
		return g.lookup(ctx, host)
	}
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// client is the HTTP client for outside calls.
func (g *guard) client() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		Proxy: nil, // never through a proxy that could reach inside
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := g.resolve(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips { // one bad address refuses the host
				if err := g.check(host, ip); err != nil {
					return nil, err
				}
			}
			var last error
			for _, ip := range ips {
				c, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
				if err == nil {
					return c, nil
				}
				last = err
			}
			return nil, last
		},
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConns:        64,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     60 * time.Second,
	}
	return &http.Client{Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return fmt.Errorf("stopped after %d redirects", maxRedirects)
		}
		if req.URL.Host != via[0].URL.Host {
			req.Header.Del(HeaderSignature)
		}
		return nil
	}}
}

// refusal reports whether err is a refused address, and which.
func refusal(err error) (*errRefused, bool) {
	var r *errRefused
	return r, errors.As(err, &r)
}

// allowNets reads TIFFIN_QUEUE_ALLOW_NETS: comma-separated address ranges
// outside calls may reach although they are not public (a test receiver on
// the host, a service on the LAN). Default none.
func allowNets() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range strings.Split(os.Getenv("TIFFIN_QUEUE_ALLOW_NETS"), ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !strings.Contains(s, "/") {
			s += "/32"
			if strings.Contains(s, ":") {
				s = strings.TrimSuffix(s, "/32") + "/128"
			}
		}
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p.Masked())
		}
	}
	return out
}
