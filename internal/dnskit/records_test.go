package dnskit_test

import (
	"context"
	"slices"
	"testing"

	"github.com/shiptiffin/tiffin/internal/dnskit"
	"github.com/shiptiffin/tiffin/internal/dnskit/dnstest"
)

func TestListAndDeleteValues(t *testing.T) {
	s := dnstest.Start(t)
	s.AddZone("example.test")
	s.Set("example.test", "A", "198.51.100.7")
	s.Set("example.test", "TXT", "v=spf1 -all", "google-site-verification=abc")
	s.Set("example.test", "MX", "10 mail.example.test.")
	s.Set("www.example.test", "CNAME", "example.test.")
	s.Set("_dmarc.example.test", "TXT", "v=DMARC1; p=none")
	p := dnstest.NewProvider(s)
	ctx := context.Background()

	recs, err := dnskit.ListRecords(ctx, p, "example.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 6 || recs[0].Host != "@" {
		t.Fatalf("list: %+v", recs)
	}
	find := func(name, typ string) []string {
		var out []string
		for _, r := range recs {
			if r.Name == name && r.Type == typ {
				out = append(out, r.Value)
			}
		}
		return out
	}
	if got := find("www.example.test", "CNAME"); !slices.Equal(got, []string{"example.test"}) {
		t.Errorf("CNAME without the root dot: %v", got)
	}
	if got := find("example.test", "MX"); !slices.Equal(got, []string{"10 mail.example.test"}) {
		t.Errorf("MX: %v", got)
	}
	for _, r := range recs {
		if r.Name == "_dmarc.example.test" && r.Host != "_dmarc" {
			t.Errorf("host: %+v", r)
		}
	}

	// One TXT value goes; the other value of the same name stays.
	n, err := dnskit.DeleteValues(ctx, p, "example.test", []dnskit.Record{{Type: "TXT", Name: "example.test", Value: "google-site-verification=abc"}})
	if err != nil || n != 1 {
		t.Fatalf("delete: %d %v", n, err)
	}
	if got := s.Get("example.test", "TXT"); !slices.Equal(got, []string{"v=spf1 -all"}) {
		t.Errorf("after delete: %v", got)
	}
	// A value that isn't there, or no value at all, removes nothing.
	for _, r := range []dnskit.Record{{Type: "TXT", Name: "example.test", Value: "nope"}, {Type: "A", Name: "example.test"}} {
		if n, err := dnskit.DeleteValues(ctx, p, "example.test", []dnskit.Record{r}); err != nil || n != 0 {
			t.Errorf("%+v: %d %v", r, n, err)
		}
	}
	// The root dot and case don't matter outside TXT.
	if n, _ := dnskit.DeleteValues(ctx, p, "example.test", []dnskit.Record{{Type: "cname", Name: "WWW.example.test", Value: "Example.test."}}); n != 1 {
		t.Errorf("CNAME delete: %d", n)
	}
}

func TestResolverRecords(t *testing.T) {
	s := dnstest.Start(t)
	s.AddZone("example.test")
	s.Set("example.test", "TXT", "google-site-verification=abc", "v=spf1 -all")
	s.Set("example.test", "MX", "10 mail.example.test.")
	s.Set("alias.example.test", "CNAME", "example.test.")
	r := &dnskit.Resolver{Servers: []string{s.Addr()}}
	ctx := context.Background()

	vals, alias, err := r.Records(ctx, "example.test", "txt")
	if err != nil || len(vals) != 2 || alias != "" || !slices.Contains(vals, "google-site-verification=abc") {
		t.Fatalf("TXT: %v %q %v", vals, alias, err)
	}
	if vals, _, _ := r.Records(ctx, "example.test", "MX"); !slices.Equal(vals, []string{"10 mail.example.test"}) {
		t.Errorf("MX: %v", vals)
	}
	if vals, alias, _ := r.Records(ctx, "alias.example.test", "TXT"); alias != "example.test" || len(vals) != 2 {
		t.Errorf("through an alias: %v %q", vals, alias)
	}
	if vals, _, _ := r.Records(ctx, "alias.example.test", "CNAME"); !slices.Equal(vals, []string{"example.test"}) {
		t.Errorf("CNAME: %v", vals)
	}
	if vals, _, err := r.Records(ctx, "missing.example.test", "TXT"); err != nil || len(vals) != 0 {
		t.Errorf("missing: %v %v", vals, err)
	}
	if _, _, err := r.Records(ctx, "example.test", "SOA"); err == nil {
		t.Error("SOA is not a lookup type")
	}
}
