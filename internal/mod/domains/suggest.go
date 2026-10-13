package domains

import (
	"fmt"
	"strings"
	"time"

	"github.com/shiptiffin/tiffin/internal/dnskit"
	"github.com/shiptiffin/tiffin/internal/platform"
)

// neutralTarget is a name of the box that a project's subdomain may CNAME
// to: the box domain itself, which the box's own records point here. None
// when apps live on a domain of their own (only dashboard.<domain> and
// *.<apps domain> must point here then; the dashboard's host is never a
// target, and a name under the apps domain may belong to another box when
// that domain is shared, like shiptiffin.app), nor on a local box.
func neutralTarget(p *platform.Platform) string {
	if !p.Reach.ACME || platform.IsLocalDomain(p.Domain) || p.AppsDomain() != p.Domain {
		return ""
	}
	return p.Domain
}

// suggest is how to point host at the box: records (the recommended way)
// and alternative (another way, or none).
//
//   - example.com: A (and AAAA) records to the box.
//   - www.example.com, when example.com is (or will be) served here: one
//     CNAME to example.com, which then carries the box's address; or A/AAAA.
//   - shop.example.com: A/AAAA records; or one CNAME to the box domain
//     (neutralTarget) when there is one.
//
// apexHere says whether the name www.<d> leads to, <d>, is served here.
func suggest(p *platform.Platform, host string, apexHere bool) (records, alternative []dnskit.Record) {
	addr := dnskit.AddressRecords(host, p.Reach.PublicIPs)
	if len(addr) == 0 {
		return []dnskit.Record{}, nil // a local box: nothing can point here
	}
	if dnskit.IsApex(host) {
		return addr, nil
	}
	if apex, ok := strings.CutPrefix(host, "www."); ok && apexHere && strings.Contains(apex, ".") {
		return []dnskit.Record{dnskit.CNAMERecord(host, apex)}, addr
	}
	if t := neutralTarget(p); t != "" && t != host {
		return addr, []dnskit.Record{dnskit.CNAMERecord(host, t)}
	}
	return addr, nil
}

// servedHosts are the custom hosts the box's projects serve or redirect.
func servedHosts(wants []want) map[string]bool {
	out := map[string]bool{}
	for _, w := range wants {
		out[w.Host] = true
	}
	return out
}

// apexHere: host is www.<d> and <d> is served by this box.
func apexHere(host string, served map[string]bool) bool {
	apex, ok := strings.CutPrefix(host, "www.")
	return ok && served[apex]
}

// checkedWords says when a domain was last looked up and when it will be
// next: "Checked 2 min ago · next check in 30 s".
func checkedWords(checked, next, now time.Time) string {
	if checked.IsZero() {
		if next.IsZero() || !next.After(now) {
			return "Not checked yet · checking now"
		}
		return "Not checked yet · first check in " + dur(next.Sub(now))
	}
	s := "Checked " + dur(now.Sub(checked)) + " ago"
	if s == "Checked 0 s ago" {
		s = "Checked just now"
	}
	switch {
	case next.IsZero():
	case !next.After(now):
		s += " · checking again now"
	default:
		s += " · next check in " + dur(next.Sub(now))
	}
	return s
}

// dur: 30 s, 2 min, 1 h 5 min.
func dur(d time.Duration) string {
	d = max(d, 0).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Round(time.Second)/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Round(time.Minute)/time.Minute))
	}
	h, mins := int(d/time.Hour), int((d%time.Hour).Round(time.Minute)/time.Minute)
	if mins == 0 {
		return fmt.Sprintf("%d h", h)
	}
	return fmt.Sprintf("%d h %d min", h, mins)
}
