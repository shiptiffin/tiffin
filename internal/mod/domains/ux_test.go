package domains

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/dnskit"
	"github.com/shiptiffin/tiffin/internal/platform"
)

// TestCheckSchedule: a waiting domain is looked up every 30 s for the
// first 30 minutes after it is added, then less and less often; a change in
// what DNS answers, or a check by hand, starts the fast window again.
func TestCheckSchedule(t *testing.T) {
	h := newHarness(t)
	h.project("shop")
	h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "example.test", "app": "web"})
	start := h.clock
	r := h.m.recs["example.test"]
	if !r.FastUntil.Equal(start.Add(fastWindow)) {
		t.Fatalf("fast until %s", r.FastUntil)
	}
	step := func() time.Duration {
		h.clock = h.m.recs["example.test"].NextCheckAt
		h.m.tick(t.Context())
		return h.m.recs["example.test"].NextCheckAt.Sub(h.clock)
	}
	n := 0
	for h.clock.Before(start.Add(fastWindow - fastCheck)) {
		if w := step(); w != fastCheck {
			t.Fatalf("check %d at +%s: wait %s, want 30s", n, h.clock.Sub(start), w)
		}
		n++
	}
	if n < 55 {
		t.Errorf("only %d checks in the fast window", n)
	}
	// Then backoff: 1, 2, 5, 10, 15, then every 30 minutes.
	h.clock = start.Add(fastWindow)
	h.m.mu.Lock()
	h.m.recs["example.test"].NextCheckAt = h.clock
	h.m.mu.Unlock()
	h.m.tick(t.Context())
	got := []time.Duration{h.m.recs["example.test"].NextCheckAt.Sub(h.clock)}
	for range 6 {
		got = append(got, step())
	}
	want := []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 30 * time.Minute, 30 * time.Minute}
	if !equalDurations(got, want) {
		t.Errorf("slow waits %v, want %v", got, want)
	}
	// Someone changes its records (still not pointing here): fast again.
	h.dns.Set("example.test", "A", "5.6.7.8")
	if w := step(); w != fastCheck {
		t.Errorf("after a DNS change: wait %s", w)
	}
	// Status in words, with the time of the next check.
	d := h.domain("shop", "example.test")
	if d["checked"] != "Checked just now · next check in 30 s" || d["nextCheckAt"] == nil || d["checkedAt"] == nil {
		t.Errorf("checked: %v %v %v", d["checked"], d["checkedAt"], d["nextCheckAt"])
	}
	// A check by hand after the window ran out: fast again too.
	h.clock = h.clock.Add(2 * fastWindow)
	h.m.tick(t.Context())
	h.call("POST", "/v1/projects/shop/domains/example.test/check", nil)
	if w := h.m.recs["example.test"].NextCheckAt.Sub(h.clock); w != fastCheck {
		t.Errorf("after a check by hand: wait %s", w)
	}
}

func equalDurations(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSiblingRecheck: when the domain starts pointing here, its www is
// looked up at once instead of at its own (later) next check.
func TestSiblingRecheck(t *testing.T) {
	h := newHarness(t)
	h.project("shop")
	h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "example.test", "app": "web", "www": true})
	// Both waited a long time: the next checks are far off and apart.
	h.clock = h.clock.Add(3 * time.Hour)
	h.m.mu.Lock()
	h.m.recs["example.test"].NextCheckAt = h.clock.Add(time.Minute)
	h.m.recs["www.example.test"].NextCheckAt = h.clock.Add(25 * time.Minute)
	h.m.mu.Unlock()
	h.dns.Set("example.test", "A", boxIP)
	h.dns.Set("www.example.test", "CNAME", "example.test.")
	h.clock = h.clock.Add(time.Minute)
	h.m.tick(t.Context())
	for _, host := range []string{"example.test", "www.example.test"} {
		if r := h.m.recs[host]; !r.Ready || r.State != StateIssuing || !r.CheckedAt.Equal(h.clock) {
			t.Errorf("%s: ready %v state %s checked %s", host, r.Ready, r.State, r.CheckedAt)
		}
	}
	if s := siblings("www.example.test"); len(s) != 1 || s[0] != "example.test" {
		t.Errorf("siblings of www: %v", s)
	}
	if s := siblings("example.test"); len(s) != 1 || s[0] != "www.example.test" {
		t.Errorf("siblings: %v", s)
	}
	// Live tells the runtime which custom domains work.
	if Live("example.test") {
		t.Error("issuing is not live")
	}
	h.m.mu.Lock()
	h.m.recs["example.test"].State = StateLive
	h.m.mu.Unlock()
	if !Live("Example.test") || Live("nope.test") {
		t.Error("Live")
	}
}

func TestParkingTable(t *testing.T) {
	a := netip.MustParseAddr
	for _, tc := range []struct {
		name  string
		ans   dnskit.Answer
		want  string // registrar key, "" for none
		value string
	}{
		{"namecheap www", dnskit.Answer{CNAME: "parkingpage.namecheap.com", Addrs: []netip.Addr{a("91.195.240.12")}}, "namecheap", "parkingpage.namecheap.com"},
		{"namecheap redirect", dnskit.Answer{Addrs: []netip.Addr{a("162.255.119.104")}}, "namecheap", "162.255.119.104"},
		{"godaddy", dnskit.Answer{Addrs: []netip.Addr{a("3.33.130.190"), a("15.197.148.33")}}, "godaddy", "3.33.130.190"},
		{"porkbun", dnskit.Answer{CNAME: "pixie.porkbun.com."}, "porkbun", "pixie.porkbun.com."},
		{"porkbun parking", dnskit.Answer{CNAME: "pixie-parking.porkbun.com"}, "porkbun", "pixie-parking.porkbun.com"},
		{"squarespace", dnskit.Answer{Addrs: []netip.Addr{a("198.49.23.145")}}, "squarespace", "198.49.23.145"},
		{"squarespace www", dnskit.Answer{CNAME: "ext-cust.squarespace.com"}, "squarespace", "ext-cust.squarespace.com"},
		{"gandi", dnskit.Answer{CNAME: "webredir.vip.gandi.net"}, "gandi", "webredir.vip.gandi.net"},
		{"gandi ip", dnskit.Answer{Addrs: []netip.Addr{a("217.70.184.38")}}, "gandi", "217.70.184.38"},
		{"elsewhere", dnskit.Answer{Addrs: []netip.Addr{a("5.6.7.8")}}, "", ""},
		{"near miss", dnskit.Answer{CNAME: "notparkingpage.namecheap.com.evil.test", Addrs: []netip.Addr{a("198.49.23.146")}}, "", ""},
		{"mixed with the box", dnskit.Answer{Addrs: []netip.Addr{a(boxIP), a("162.255.119.10")}}, "namecheap", "162.255.119.10"},
	} {
		r, v := parkedBy(tc.ans)
		got := ""
		if r != nil {
			got = r.Key
		}
		if got != tc.want || v != tc.value {
			t.Errorf("%s: %q %q, want %q %q", tc.name, got, v, tc.want, tc.value)
		}
	}
	for ns, want := range map[string]string{
		"dns1.registrar-servers.com.": "namecheap", "ns45.domaincontrol.com": "godaddy", "curitiba.ns.porkbun.com": "porkbun",
		"ns-cloud-a1.googledomains.com": "squarespace", "ns-123-a.gandi.net": "gandi", "ada.ns.cloudflare.com": "cloudflare",
		"ns1.dns-parking.com": "hostinger", "ns1045.ui-dns.de": "ionos", "ns1.dyna-ns.net": "dynadot", "ns1kmc.name.com": "namecom",
		"ns1.example.net": "", "fakename.com": "",
	} {
		got := ""
		if r := registrarByNS([]string{ns}); r != nil {
			got = r.Key
		}
		if got != want {
			t.Errorf("NS %s: %q, want %q", ns, got, want)
		}
	}
	for _, r := range registrars {
		if r.Remove != "" && (len(r.Targets) == 0 && len(r.Nets) == 0 || r.What == "") {
			t.Errorf("%s: says what to remove but nothing to recognise", r.Key)
		}
		if r.Where == "" || len(r.NS) == 0 {
			t.Errorf("%s: no where or nameservers", r.Key)
		}
	}
}

// TestParkedDomain: the walkthrough's Namecheap domain, as the box says it.
func TestParkedDomain(t *testing.T) {
	h := newHarness(t)
	h.project("shop")
	h.dns.Set("example.test", "A", "162.255.119.104")
	h.dns.Set("www.example.test", "CNAME", "parkingpage.namecheap.com.")
	h.dns.Set("example.test", "NS", "dns1.registrar-servers.com.", "dns2.registrar-servers.com.")
	h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "example.test", "app": "web", "www": true})
	d := h.domain("shop", "example.test")
	rg, _ := d["registrar"].(map[string]any)
	if rg == nil || rg["name"] != "Namecheap" || rg["parked"] != true || !strings.Contains(rg["remove"].(string), "URL Redirect Record") {
		t.Fatalf("registrar: %v", d["registrar"])
	}
	if s := d["summary"].(string); !strings.HasPrefix(s, "example.test still points to Namecheap's parking page. In Namecheap › Domain List › Manage › Advanced DNS, delete the URL Redirect Record on @") ||
		!strings.Contains(s, "then add: example.test A "+boxIP) {
		t.Errorf("summary: %s", s)
	}
	if r := d["reason"].(string); !strings.Contains(r, "points to Namecheap's parking page (162.255.119.104), not this box") {
		t.Errorf("reason: %s", r)
	}
	w := h.domain("shop", "www.example.test")
	if rg, _ := w["registrar"].(map[string]any); rg == nil || rg["parked"] != true || !strings.Contains(w["reason"].(string), "parkingpage.namecheap.com") {
		t.Errorf("www: %v", w)
	}
	// Elsewhere, but at a known DNS host: where to click, nothing parked.
	h.dns.Set("example.test", "A", "5.6.7.8")
	h.call("POST", "/v1/projects/shop/domains/example.test/check", nil)
	d = h.domain("shop", "example.test")
	if rg, _ := d["registrar"].(map[string]any); rg == nil || rg["name"] != "Namecheap" || rg["parked"] != nil || !strings.Contains(d["summary"].(string), "In Namecheap › Domain List › Manage › Advanced DNS, add:") {
		t.Errorf("not parked: %v / %s", d["registrar"], d["summary"])
	}
	// Pointing here: no registrar advice left.
	h.dns.Set("example.test", "A", boxIP)
	h.call("POST", "/v1/projects/shop/domains/example.test/check", nil)
	if d := h.domain("shop", "example.test"); d["registrar"] != nil || d["state"] != StateIssuing {
		t.Errorf("pointing here: %v", d)
	}
}

func TestSuggest(t *testing.T) {
	ips := []netip.Addr{netip.MustParseAddr("198.51.100.7"), netip.MustParseAddr("2001:db8::7")}
	one := &platform.Platform{Domain: "box.test", Reach: platform.Reach{ACME: true, PublicIPs: ips}}
	split := &platform.Platform{Domain: "example.test", Reach: platform.Reach{ACME: true, PublicIPs: ips, AppsDomain: "apps.test"}}
	local := &platform.Platform{Domain: "tiffin.localhost"}
	str := func(rs []dnskit.Record) string {
		var s []string
		for _, r := range rs {
			s = append(s, r.Host+" "+r.Type+" "+r.Value)
		}
		return strings.Join(s, "; ")
	}
	for _, tc := range []struct {
		name      string
		p         *platform.Platform
		host      string
		apexHere  bool
		recs, alt string
	}{
		{"apex", one, "reelbird.app", false, "@ A 198.51.100.7; @ AAAA 2001:db8::7", ""},
		{"www of a domain served here", one, "www.reelbird.app", true, "www CNAME reelbird.app", "www A 198.51.100.7; www AAAA 2001:db8::7"},
		{"www of a domain served elsewhere", one, "www.example.com", false, "www A 198.51.100.7; www AAAA 2001:db8::7", "www CNAME box.test"},
		{"subdomain", one, "shop.example.com", false, "shop A 198.51.100.7; shop AAAA 2001:db8::7", "shop CNAME box.test"},
		{"subdomain, apps elsewhere", split, "shop.example.com", false, "shop A 198.51.100.7; shop AAAA 2001:db8::7", ""},
		{"www, apps elsewhere", split, "www.reelbird.app", true, "www CNAME reelbird.app", "www A 198.51.100.7; www AAAA 2001:db8::7"},
		{"the box domain itself", &platform.Platform{Domain: "acme.box.test", Reach: one.Reach}, "acme.box.test", false, "acme A 198.51.100.7; acme AAAA 2001:db8::7", ""},
		{"local box", local, "www.reelbird.app", true, "", ""},
	} {
		recs, alt := suggest(tc.p, tc.host, tc.apexHere)
		if str(recs) != tc.recs || str(alt) != tc.alt {
			t.Errorf("%s: records %q alt %q, want %q / %q", tc.name, str(recs), str(alt), tc.recs, tc.alt)
		}
		for _, r := range append(recs, alt...) {
			if strings.HasPrefix(r.Value, "dashboard.") {
				t.Errorf("%s: suggests the dashboard's host: %v", tc.name, r)
			}
		}
	}
}

// TestPlanRecords: the plan answer of domains add (428, nothing changed
// yet) already lists the DNS records to add.
func TestPlanRecords(t *testing.T) {
	h := newHarness(t)
	h.project("shop")
	code, out := h.call("POST", "/v1/projects/shop/domains", map[string]any{"domain": "example.test", "app": "web", "www": true})
	if code != 428 || out["code"] != "confirm_required" || out["plan"] == nil {
		t.Fatalf("plan: %d %v", code, out)
	}
	var got []string
	for _, r := range out["records"].([]any) {
		r := r.(map[string]any)
		got = append(got, r["name"].(string)+" "+r["type"].(string)+" "+r["value"].(string))
	}
	if strings.Join(got, "; ") != "example.test A "+boxIP+"; www.example.test CNAME example.test" {
		t.Errorf("records: %v", got)
	}
	// A name on the box needs none.
	if code, out := h.call("POST", "/v1/projects/shop/domains", map[string]any{"domain": "blog.box.test", "app": "web"}); code != 428 || out["records"] != nil {
		t.Errorf("box name: %d %v", code, out["records"])
	}
	// The applied answer lists the same records under the domain.
	out = h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "example.test", "app": "web", "www": true})
	if d := out["domain"].(map[string]any); d["records"].([]any)[0].(map[string]any)["value"] != boxIP {
		t.Errorf("after confirm: %v", d)
	}
	if w := h.domain("shop", "www.example.test"); w["records"].([]any)[0].(map[string]any)["type"] != "CNAME" || w["alternative"].([]any)[0].(map[string]any)["type"] != "A" {
		t.Errorf("www records: %v / %v", w["records"], w["alternative"])
	}
}

// TestCertificateStateWords: a domain handed to the edge whose certificate
// the edge has not started on reads issuing, like the domain.
func TestCertificateStateWords(t *testing.T) {
	h := newHarness(t)
	h.project("shop")
	h.dns.Set("example.test", "A", boxIP)
	h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "example.test", "app": "web"})
	d := h.domain("shop", "example.test")
	if c, _ := d["certificate"].(map[string]any); d["state"] != StateIssuing || c == nil || c["state"] != "issuing" {
		t.Errorf("state %v, certificate %v", d["state"], d["certificate"])
	}
}

func TestCheckedWords(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		checked, next time.Time
		want          string
	}{
		{now.Add(-2 * time.Minute), now.Add(30 * time.Second), "Checked 2 min ago · next check in 30 s"},
		{now.Add(-1 * time.Second), now.Add(29*time.Second + 600*time.Millisecond), "Checked 1 s ago · next check in 30 s"},
		{now, now.Add(90 * time.Minute), "Checked just now · next check in 1 h 30 min"},
		{now.Add(-time.Hour), now.Add(-time.Second), "Checked 1 h ago · checking again now"},
		{time.Time{}, time.Time{}, "Not checked yet · checking now"},
	} {
		if got := checkedWords(tc.checked, tc.next, now); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}
