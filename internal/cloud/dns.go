package cloud

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	"github.com/shiptiffin/tiffin/internal/dnskit"
)

// DNS keeps the records of managed boxes in the shiptiffin.app zone. Each box
// has two names, both pointing at its server (A and, with IPv6, AAAA):
//
//	<name>.shiptiffin.app      the box's domain
//	*.<name>.shiptiffin.app    dashboard.<name>…, <project>.<name>…, previews
//
// They are "DNS only" records: the box serves HTTPS itself and gets a
// certificate for each name it serves over HTTP-01, so it never holds a DNS
// credential. The zone's token lives only in the control plane's secrets.
type DNS struct {
	P    dnskit.Provider
	Zone string // "shiptiffin.app"
}

// Domain is a box's domain.
func (d DNS) Domain(name string) string { return name + "." + d.Zone }

// Records are the records a box with these addresses has.
func (d DNS) Records(name, ipv4, ipv6 string) ([]dnskit.Record, error) {
	if err := ValidName(name); err != nil {
		return nil, fmt.Errorf("box name %q: %w", name, err)
	}
	var out []dnskit.Record
	for _, h := range []string{d.Domain(name), "*." + d.Domain(name)} {
		if ipv4 != "" {
			a, err := netip.ParseAddr(ipv4)
			if err != nil || !a.Is4() {
				return nil, fmt.Errorf("%q is not an IPv4 address", ipv4)
			}
			out = append(out, dnskit.Record{Name: h, Type: "A", Value: a.String(), TTL: 300})
		}
		if ipv6 != "" {
			a, err := netip.ParseAddr(ipv6)
			if err != nil || !a.Is6() {
				return nil, fmt.Errorf("%q is not an IPv6 address", ipv6)
			}
			out = append(out, dnskit.Record{Name: h, Type: "AAAA", Value: a.String(), TTL: 300})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("box %s has no address", name)
	}
	return out, nil
}

// Set points the box's names at its addresses (replacing what was there:
// a name with only IPv4 loses a stale AAAA).
func (d DNS) Set(ctx context.Context, name, ipv4, ipv6 string) error {
	recs, err := d.Records(name, ipv4, ipv6)
	if err != nil {
		return err
	}
	if err := d.Remove(ctx, name); err != nil {
		return err
	}
	return dnskit.SetRecords(ctx, d.P, d.Zone, recs)
}

// Remove deletes every A and AAAA record of the box's two names. It never
// touches another name, even one that ends the same way.
func (d DNS) Remove(ctx context.Context, name string) error {
	if err := ValidName(name); err != nil && !strings.Contains(err.Error(), "reserved") {
		return fmt.Errorf("box name %q: %w", name, err)
	}
	var recs []dnskit.Record
	for _, h := range []string{d.Domain(name), "*." + d.Domain(name)} {
		recs = append(recs, dnskit.Record{Name: h, Type: "A"}, dnskit.Record{Name: h, Type: "AAAA"})
	}
	return dnskit.DeleteRecords(ctx, d.P, d.Zone, recs)
}
