package dnskit_test

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/dnskit"
)

// TestCloudflareLive talks to the real Cloudflare API. It is skipped unless
// CLOUDFLARE_API_TOKEN is set (a token with Zone · DNS · Edit; account-owned
// tokens work). CLOUDFLARE_TEST_ZONE picks the zone (default: the first one
// the token sees). It lists zones, then creates, reads back and deletes one
// TXT record named _tiffin-test-<time>.<zone>, so it leaves nothing behind.
func TestCloudflareLive(t *testing.T) {
	token := os.Getenv("CLOUDFLARE_API_TOKEN")
	if token == "" {
		t.Skip("CLOUDFLARE_API_TOKEN not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p, err := dnskit.Open("cloudflare", map[string]string{"token": token})
	if err != nil {
		t.Fatal(err)
	}
	zones, err := dnskit.Zones(ctx, p)
	if err != nil {
		t.Fatalf("list zones (the box's token check): %v", err)
	}
	t.Logf("token sees %d zone(s): %s", len(zones), strings.Join(zones, ", "))
	zone := os.Getenv("CLOUDFLARE_TEST_ZONE")
	if zone == "" {
		if len(zones) == 0 {
			t.Fatal("the token sees no zones")
		}
		zone = zones[0]
	} else if !slices.Contains(zones, zone) {
		t.Fatalf("zone %s is not among %v", zone, zones)
	}
	name := "_tiffin-test-" + strings.ToLower(time.Now().UTC().Format("20060102t150405")) + "." + zone
	rec := dnskit.Record{Type: "TXT", Name: name, Value: "tiffin dns test, safe to delete", TTL: 60}
	if err := dnskit.SetRecords(ctx, p, zone, []dnskit.Record{rec}); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	defer func() {
		if err := dnskit.DeleteRecords(context.Background(), p, zone, []dnskit.Record{rec}); err != nil {
			t.Errorf("delete %s: %v (delete it by hand)", name, err)
		}
	}()
	have, err := p.GetRecords(ctx, zone+".")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range have {
		rr := r.RR()
		if rr.Type == "TXT" && strings.HasPrefix(name, rr.Name+".") && strings.Contains(rr.Data, "tiffin dns test") {
			found = true
		}
	}
	if !found {
		t.Errorf("created %s but it is not listed", name)
	}
}
