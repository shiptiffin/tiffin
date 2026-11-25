package dnskit_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/libdns/libdns"
	"github.com/shiptiffin/tiffin/internal/dnskit"
	"github.com/shiptiffin/tiffin/internal/dnskit/dnstest"
)

// cloudflareLike is the test provider with libdns/cloudflare v0.2.2's
// SetRecords: it finds existing records by name and type only and refuses
// when there is more than one.
type cloudflareLike struct{ *dnstest.Provider }

func (p cloudflareLike) SetRecords(ctx context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	have, err := p.Provider.GetRecords(ctx, zone)
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		rr, n := r.RR(), 0
		for _, h := range have {
			if strings.EqualFold(h.RR().Name, rr.Name) && strings.EqualFold(h.RR().Type, rr.Type) {
				n++
			}
		}
		if n > 1 {
			return nil, fmt.Errorf("unexpectedly found more than 1 record for %v", rr)
		}
	}
	return p.Provider.SetRecords(ctx, zone, recs)
}

func TestSetRecordsSeveralValues(t *testing.T) {
	spf := "v=spf1 include:_spf.google.com ~all"
	gsv := "google-site-verification=abc"
	bing := "MS=ms12345"
	mcp := "v=MCPv1; k=ed25519; p=xyz"
	s := dnstest.Start(t)
	s.AddZone("example.test")
	s.Set("example.test", "TXT", spf, gsv, bing)
	s.Set("example.test", "A", "198.51.100.7")
	s.Set("_dmarc.example.test", "TXT", "v=DMARC1; p=none")
	fake := dnstest.NewProvider(s)
	p := cloudflareLike{fake}
	ctx := context.Background()
	txt := func(vals ...string) []dnskit.Record {
		var out []dnskit.Record
		for _, v := range vals {
			out = append(out, dnskit.Record{Type: "TXT", Name: "example.test", Value: v, TTL: 60})
		}
		return out
	}
	sorted := func(v []string) []string { v = slices.Clone(v); slices.Sort(v); return v }

	// The three values there plus one new: the new one is added, nothing else moves.
	if err := dnskit.SetRecords(ctx, p, "example.test", txt(spf, gsv, bing, mcp)); err != nil {
		t.Fatalf("set 3+1: %v", err)
	}
	if got, want := sorted(s.Get("example.test", "TXT")), sorted([]string{spf, gsv, bing, mcp}); !slices.Equal(got, want) {
		t.Fatalf("apex TXT = %v, want %v", got, want)
	}
	if fake.Calls["AppendRecords"] != 1 || fake.Calls["DeleteRecords"] != 0 || fake.Calls["SetRecords"] != 0 {
		t.Errorf("calls = %v, want one append only", fake.Calls)
	}
	if got := s.Get("example.test", "A"); !slices.Equal(got, []string{"198.51.100.7"}) {
		t.Errorf("A touched: %v", got)
	}
	if got := s.Get("_dmarc.example.test", "TXT"); !slices.Equal(got, []string{"v=DMARC1; p=none"}) {
		t.Errorf("other name touched: %v", got)
	}

	// Fewer values: the ones left out go, repeats count once.
	if err := dnskit.SetRecords(ctx, p, "example.test", txt(spf, mcp, mcp)); err != nil {
		t.Fatalf("set 2: %v", err)
	}
	if got, want := sorted(s.Get("example.test", "TXT")), sorted([]string{spf, mcp}); !slices.Equal(got, want) {
		t.Fatalf("apex TXT = %v, want %v", got, want)
	}

	// Down to one value, and a single record replaced in place.
	if err := dnskit.SetRecords(ctx, p, "example.test", txt(spf)); err != nil {
		t.Fatalf("set 1: %v", err)
	}
	if got := s.Get("example.test", "TXT"); !slices.Equal(got, []string{spf}) {
		t.Fatalf("apex TXT = %v", got)
	}
	if err := dnskit.SetRecords(ctx, p, "example.test", []dnskit.Record{{Type: "A", Name: "example.test", Value: "203.0.113.9"}}); err != nil {
		t.Fatalf("set A: %v", err)
	}
	if got := s.Get("example.test", "A"); !slices.Equal(got, []string{"203.0.113.9"}) {
		t.Errorf("A = %v", got)
	}

	// A kept value whose TTL changes is written again with it.
	before := fake.Calls["DeleteRecords"]
	recs := txt(spf, gsv)
	recs[0].TTL = 300
	if err := dnskit.SetRecords(ctx, p, "example.test", recs); err != nil {
		t.Fatalf("set TTL: %v", err)
	}
	if got, want := sorted(s.Get("example.test", "TXT")), sorted([]string{spf, gsv}); !slices.Equal(got, want) {
		t.Fatalf("apex TXT = %v, want %v", got, want)
	}
	if fake.Calls["DeleteRecords"] != before+1 {
		t.Errorf("TTL change: %d deletes, want 1", fake.Calls["DeleteRecords"]-before)
	}
}
