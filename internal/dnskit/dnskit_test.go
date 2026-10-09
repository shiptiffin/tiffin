package dnskit_test

import (
	"context"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/shiptiffin/tiffin/internal/dnskit"
	"github.com/shiptiffin/tiffin/internal/dnskit/dnstest"
)

var box = []netip.Addr{netip.MustParseAddr("198.51.100.7")}

func TestLookupAndPointsHere(t *testing.T) {
	s := dnstest.Start(t)
	s.AddZone("example.test")
	s.Set("example.test", "A", "198.51.100.7")
	s.Set("*.example.test", "A", "198.51.100.7")
	s.Set("other.example.test", "A", "5.6.7.8")
	s.Set("www.example.test", "CNAME", "example.test.")
	s.Set("v6.example.test", "AAAA", "2001:db8::1")
	s.Set("cf.example.test", "A", "104.21.3.4")
	s.Set("dangling.example.test", "CNAME", "nowhere.example.invalid.")
	r := &dnskit.Resolver{Servers: []string{s.Addr()}}
	ctx := context.Background()
	for _, tc := range []struct {
		name, want string
		ok         bool
	}{
		{"example.test", "", true},
		{"anything.example.test", "", true}, // the wildcard
		{"www.example.test", "", true},      // through the alias
		{"other.example.test", "points to 5.6.7.8, not this box (198.51.100.7)", false},
		{"v6.example.test", "this box has no IPv6 address", false},
		{"cf.example.test", "Cloudflare's proxy", false},
		{"dangling.example.test", "alias of nowhere.example.invalid", false},
		{"missing.nowhere.test", "no A or AAAA record yet", false},
	} {
		ans, err := r.Lookup(ctx, tc.name)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		ok, why := dnskit.PointsHere(ans, box)
		if ok != tc.ok || !strings.Contains(why, tc.want) {
			t.Errorf("%s: ok=%v %q, want ok=%v %q (answer %+v)", tc.name, ok, why, tc.ok, tc.want, ans)
		}
	}
	ans, _ := r.Lookup(ctx, "www.example.test")
	if ans.CNAME != "example.test" {
		t.Errorf("CNAME = %q", ans.CNAME)
	}
}

func TestCAA(t *testing.T) {
	s := dnstest.Start(t)
	s.AddZone("example.test")
	s.Set("example.test", "CAA", `0 issue "digicert.com"`)
	s.Set("le.example.test", "CAA", `0 issue "letsencrypt.org; validationmethods=http-01"`)
	s.Set("wild.example.test", "CAA", `0 issue "letsencrypt.org"`, `0 issuewild ";"`)
	r := &dnskit.Resolver{Servers: []string{s.Addr()}}
	ctx := context.Background()
	recs, owner, err := r.CAASet(ctx, "shop.example.test")
	if err != nil || owner != "example.test" || len(recs) != 1 {
		t.Fatalf("CAASet = %v %q %v", recs, owner, err)
	}
	if dnskit.CAAAllows(recs, false, "letsencrypt.org") {
		t.Error("digicert-only CAA must refuse letsencrypt.org")
	}
	recs, owner, _ = r.CAASet(ctx, "le.example.test")
	if owner != "le.example.test" || !dnskit.CAAAllows(recs, false, "letsencrypt.org") {
		t.Errorf("le: %v %q", recs, owner)
	}
	recs, _, _ = r.CAASet(ctx, "wild.example.test")
	if !dnskit.CAAAllows(recs, false, "letsencrypt.org") || dnskit.CAAAllows(recs, true, "letsencrypt.org") {
		t.Errorf("issuewild ';' must forbid wildcards only: %v", recs)
	}
	if recs, owner, _ := r.CAASet(ctx, "free.nowhere.test"); recs != nil || owner != "" {
		t.Errorf("no CAA anywhere: %v %q", recs, owner)
	}
	if !dnskit.CAAAllows([]dnskit.CAA{{Tag: "iodef", Value: "mailto:x@y"}}, false, "letsencrypt.org") {
		t.Error("only iodef records: issuance is unrestricted")
	}
}

func TestNames(t *testing.T) {
	a := netip.MustParseAddr("203.0.113.7")
	if d := dnskit.SslipDomain(a); d != "203-0-113-7.sslip.io" {
		t.Errorf("SslipDomain = %q", d)
	}
	if got, ok := dnskit.SslipAddr("Dashboard.203-0-113-7.sslip.io"); !ok || got != a {
		t.Errorf("SslipAddr = %v %v", got, ok)
	}
	if _, ok := dnskit.SslipAddr("dashboard.example.com"); ok {
		t.Error("not an sslip name")
	}
	for name, want := range map[string]string{"shop.example.co.uk": "example.co.uk", "example.com": "example.com", "*.apps.example.com": "example.com"} {
		if z := dnskit.Zone(name); z != want {
			t.Errorf("Zone(%s) = %s", name, z)
		}
	}
	if dnskit.RelHost("example.com", "example.com") != "@" || dnskit.RelHost("*.apps.example.com", "example.com") != "*.apps" {
		t.Error("RelHost")
	}
	if !dnskit.IsApex("example.com") || dnskit.IsApex("shop.example.com") {
		t.Error("IsApex")
	}
	recs := dnskit.AddressRecords("shop.example.com", []netip.Addr{a, netip.MustParseAddr("2a01:4f8::1")})
	if len(recs) != 2 || recs[0].Type != "A" || recs[1].Type != "AAAA" || recs[0].Host != "shop" {
		t.Errorf("AddressRecords = %+v", recs)
	}
	for addr, want := range map[string]bool{"203.0.113.7": false, "10.0.0.1": false, "100.64.1.1": false, "127.0.0.1": false, "95.216.1.2": true, "2a01:4f8::1": true, "fe80::1": false} {
		if got := dnskit.IsPublic(netip.MustParseAddr(addr)); got != want {
			t.Errorf("IsPublic(%s) = %v", addr, got)
		}
	}
}

func TestProviderRecords(t *testing.T) {
	s := dnstest.Start(t)
	s.AddZone("example.test")
	s.AddZone("other.test")
	p := dnstest.NewProvider(s)
	ctx := context.Background()
	zones, err := dnskit.Zones(ctx, p)
	if err != nil || !slices.Equal(zones, []string{"example.test", "other.test"}) {
		t.Fatalf("Zones = %v %v", zones, err)
	}
	if z, ok := dnskit.ZoneFor(zones, "*.apps.example.test"); !ok || z != "example.test" {
		t.Errorf("ZoneFor = %q %v", z, ok)
	}
	if _, ok := dnskit.ZoneFor(zones, "example.com"); ok {
		t.Error("ZoneFor matched a foreign name")
	}
	recs := []dnskit.Record{
		{Type: "A", Name: "example.test", Value: "198.51.100.7"},
		{Type: "A", Name: "*.example.test", Value: "198.51.100.7"},
		{Type: "TXT", Name: "_dmarc.example.test", Value: "v=DMARC1; p=none"},
		{Type: "CNAME", Name: "www.example.test", Value: "example.test"},
	}
	if err := dnskit.SetRecords(ctx, p, "example.test", recs); err != nil {
		t.Fatal(err)
	}
	s.Set("example.test", "A", "198.51.100.7") // idempotent with what the provider wrote
	if err := dnskit.SetRecords(ctx, p, "example.test", recs[:1]); err != nil {
		t.Fatal(err)
	}
	if got := s.Get("example.test", "A"); !slices.Equal(got, []string{"198.51.100.7"}) {
		t.Errorf("A = %v (set must replace, not append)", got)
	}
	r := &dnskit.Resolver{Servers: []string{s.Addr()}}
	if ans, _ := r.Lookup(ctx, "shop.example.test"); len(ans.Addrs) != 1 {
		t.Errorf("wildcard record not served: %+v", ans)
	}
	if txt, _ := r.TXT(ctx, "_dmarc.example.test"); !slices.Equal(txt, []string{"v=DMARC1; p=none"}) {
		t.Errorf("TXT = %v", txt)
	}
	if err := dnskit.DeleteRecords(ctx, p, "example.test", []dnskit.Record{{Type: "TXT", Name: "_dmarc.example.test"}}); err != nil {
		t.Fatal(err)
	}
	if txt, _ := r.TXT(ctx, "_dmarc.example.test"); len(txt) != 0 {
		t.Errorf("TXT after delete = %v", txt)
	}
	if _, err := dnskit.Open("cloudflare", map[string]string{}); err == nil || !strings.Contains(err.Error(), "token") {
		t.Errorf("Open without a token: %v", err)
	}
	if _, err := dnskit.Open("nope", nil); err == nil {
		t.Error("unknown provider opened")
	}
}
