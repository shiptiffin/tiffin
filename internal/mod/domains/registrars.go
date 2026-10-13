package domains

import (
	"context"
	"net/netip"
	"strings"
	"time"

	"github.com/shiptiffin/tiffin/internal/dnskit"
)

// Registrars and their parking records. A freshly bought domain usually
// points at its registrar's parking page (or a URL redirect) until someone
// deletes those records, and people add the box's records beside them. When
// a name still answers with one of these, the box says so plainly and where
// to click, instead of only listing addresses.
//
// Only targets confirmed by the registrar's own help pages or well-known
// public facts are listed:
//   - Namecheap: www CNAME parkingpage.namecheap.com and a URL Redirect
//     Record on @ (Namecheap's knowledge base); the redirect answers from
//     Namecheap's 162.255.119.0/24 (AS22612, seen in the 2026-10-13
//     walkthrough); BasicDNS is *.registrar-servers.com.
//   - GoDaddy: its help page "Park a domain" sets @ to 3.33.130.190 or
//     15.197.148.33; nameservers *.domaincontrol.com.
//   - Porkbun: default records point at pixie.porkbun.com (and
//     pixie-parking.porkbun.com), per its knowledge base; *.ns.porkbun.com.
//   - Squarespace (and the Google Domains it took over): the default site
//     records, A 198.185.159.144/145 and 198.49.23.144/145 and www CNAME
//     ext-cust.squarespace.com; nameservers *.googledomains.com.
//   - Gandi: the default zone's web redirect, webredir.vip.gandi.net
//     (217.70.184.38); nameservers *.gandi.net.
//   - Cloudflare (Registrar), Hostinger (*.dns-parking.com), IONOS
//     (*.ui-dns.*), Dynadot (*.dyna-ns.net), Name.com (*.name.com): by
//     nameserver only, for where to click.
//
// Left out as unverified: GoDaddy's older "Parked" addresses
// (34.102.136.180 and others seen in forums) and *.secureserver.net
// forwarding, Porkbun's uixie.porkbun.com, Hostinger's, IONOS's, Dynadot's
// and Name.com's parking addresses.
type registrar struct {
	Key   string
	Name  string
	Where string // where its DNS records are edited
	// Remove says which of its default records to delete.
	Remove  string
	NS      []string // nameserver host suffixes
	Targets []string // parking or forwarding CNAME targets (suffix match)
	Nets    []netip.Prefix
	What    string // what the parking records lead to: "Namecheap's parking page"
}

var registrars = []registrar{
	{Key: "namecheap", Name: "Namecheap", Where: "Domain List › Manage › Advanced DNS",
		Remove: "the URL Redirect Record on @ and the CNAME Record for www (parkingpage.namecheap.com)",
		NS:     []string{"registrar-servers.com"}, Targets: []string{"parkingpage.namecheap.com"},
		Nets: prefixes("162.255.119.0/24"), What: "Namecheap's parking page"},
	{Key: "godaddy", Name: "GoDaddy", Where: "My Products › your domain › DNS",
		Remove: "the A record on @ that points to GoDaddy's parking page (3.33.130.190 or 15.197.148.33), and any domain forwarding",
		NS:     []string{"domaincontrol.com"}, Nets: prefixes("3.33.130.190/32", "15.197.148.33/32"), What: "GoDaddy's parking page"},
	{Key: "porkbun", Name: "Porkbun", Where: "Domain Management › your domain › DNS",
		Remove: "the ALIAS and CNAME records that point to pixie.porkbun.com",
		NS:     []string{"ns.porkbun.com"}, Targets: []string{"pixie.porkbun.com", "pixie-parking.porkbun.com"}, What: "Porkbun's parking page"},
	{Key: "squarespace", Name: "Squarespace", Where: "Domains › your domain › DNS › DNS Settings",
		Remove: "the default Squarespace records on @ (A 198.185.159.x and 198.49.23.x) and www (CNAME ext-cust.squarespace.com)",
		NS:     []string{"googledomains.com"}, Targets: []string{"ext-cust.squarespace.com"},
		Nets: prefixes("198.185.159.144/31", "198.49.23.144/31"), What: "Squarespace's default site"},
	{Key: "gandi", Name: "Gandi", Where: "Domain › your domain › DNS Records",
		Remove: "the default @ A record (217.70.184.38) and the www CNAME (webredir.vip.gandi.net)",
		NS:     []string{"gandi.net"}, Targets: []string{"webredir.vip.gandi.net"}, Nets: prefixes("217.70.184.38/32"), What: "Gandi's web forwarding"},
	{Key: "cloudflare", Name: "Cloudflare", Where: "your domain › DNS › Records (set Proxy status to DNS only)", NS: []string{"ns.cloudflare.com"}},
	{Key: "hostinger", Name: "Hostinger", Where: "Domains › your domain › DNS / Nameservers", NS: []string{"dns-parking.com"}},
	{Key: "ionos", Name: "IONOS", Where: "Domains & SSL › your domain › DNS", NS: []string{"ui-dns.com", "ui-dns.de", "ui-dns.org", "ui-dns.biz"}},
	{Key: "dynadot", Name: "Dynadot", Where: "My Domains › Manage Domains › your domain › DNS Settings", NS: []string{"dyna-ns.net"}},
	{Key: "namecom", Name: "Name.com", Where: "My Domains › your domain › Manage DNS Records", NS: []string{"name.com"}},
}

func prefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

func registrarByKey(key string) *registrar {
	for i := range registrars {
		if registrars[i].Key == key {
			return &registrars[i]
		}
	}
	return nil
}

func hasSuffix(name, suffix string) bool {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	return name == suffix || strings.HasSuffix(name, "."+suffix)
}

// parkedBy finds the registrar whose parking or forwarding records an
// answer still holds (its CNAME, or one of its addresses), and the value
// that gave it away.
func parkedBy(ans dnskit.Answer) (*registrar, string) {
	for i := range registrars {
		r := &registrars[i]
		for _, t := range r.Targets {
			if ans.CNAME != "" && hasSuffix(ans.CNAME, t) {
				return r, ans.CNAME
			}
		}
		for _, a := range ans.Addrs {
			for _, n := range r.Nets {
				if n.Contains(a.Unmap()) {
					return r, a.String()
				}
			}
		}
	}
	return nil, ""
}

// registrarByNS is the registrar (DNS host) whose nameservers serve a zone.
func registrarByNS(ns []string) *registrar {
	for i := range registrars {
		for _, n := range ns {
			for _, s := range registrars[i].NS {
				if hasSuffix(n, s) {
					return &registrars[i]
				}
			}
		}
	}
	return nil
}

// dnsHost asks which registrar's nameservers hold host's zone ("" when
// none known or DNS does not say).
func (m *Module) dnsHost(ctx context.Context, host string) string {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ns, _, err := m.p.Reach.Resolver().Records(cctx, dnskit.Zone(host), "NS")
	if err != nil {
		return ""
	}
	if r := registrarByNS(ns); r != nil {
		return r.Key
	}
	return ""
}

// Registrar is the DNS host a domain's records are kept at, when the box
// recognises it, and its parking records when DNS still answers with them.
type Registrar struct {
	Name    string `json:"name" doc:"The registrar or DNS host, e.g. Namecheap."`
	Where   string `json:"where" doc:"Where its DNS records are edited, e.g. Domain List › Manage › Advanced DNS."`
	Parked  bool   `json:"parked,omitempty" doc:"DNS still answers with its parking page or forwarding: delete those records first (see remove)."`
	Parking string `json:"parking,omitempty" doc:"What its parking records lead to, when parked, e.g. Namecheap's parking page."`
	Remove  string `json:"remove,omitempty" doc:"Which of its default records to delete, when parked."`
}

func (r *registrar) hint(parked bool) *Registrar {
	h := &Registrar{Name: r.Name, Where: r.Where}
	if parked && r.Remove != "" {
		h.Parked, h.Parking, h.Remove = true, r.What, r.Remove
	}
	return h
}
