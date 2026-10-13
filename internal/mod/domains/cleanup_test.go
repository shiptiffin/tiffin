package domains

import (
	"errors"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"testing"
)

// recs turns a cleanup list (dns.delete, dns.byHand...) into "name TYPE value".
func recs(c map[string]any, key string) []string {
	var out []string
	l, _ := c[key].([]any)
	for _, r := range l {
		r := r.(map[string]any)
		out = append(out, r["name"].(string)+" "+r["type"].(string)+" "+strings.TrimSuffix(r["value"].(string), "."))
	}
	sort.Strings(out)
	return out
}

func dnsOf(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	c, ok := out["dns"].(map[string]any)
	if !ok {
		t.Fatalf("no dns in %v", out)
	}
	return c
}

func eq(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Errorf("%s: %q, want %q", what, got, want)
	}
}

// connected: a box with the fake DNS provider connected and project shop.
func connected(t *testing.T) *harness {
	h := newHarness(t)
	if code, out := h.call("PUT", "/v1/dns/providers/fake", map[string]any{"token": "t"}); code != 200 {
		t.Fatalf("connect: %d %v", code, out)
	}
	h.project("shop")
	return h
}

// Removing a domain deletes the records the box created for it (they
// still point here), lists them in the plan first, and leaves a record the
// owner re-pointed elsewhere alone.
func TestRemoveDomainDeletesCreatedRecords(t *testing.T) {
	h := connected(t)
	h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "shop.test", "app": "web", "www": true, "createRecords": true})
	if got := h.dns.Get("shop.test", "A"); !slices.Equal(got, []string{boxIP}) {
		t.Fatalf("created: %v", got)
	}
	// The records list says which the box created.
	_, zr := h.call("GET", "/v1/dns/records?name=shop.test", nil)
	created := 0
	for _, r := range zr["records"].([]any) {
		if r.(map[string]any)["created"] == true {
			created++
		}
	}
	if created != 2 {
		t.Errorf("created records listed: %d, want 2: %v", created, zr)
	}

	code, plan := h.call("POST", "/v1/projects/shop/domains/shop.test/remove", map[string]any{})
	if code != 428 {
		t.Fatalf("plan: %d %v", code, plan)
	}
	c := dnsOf(t, plan)
	eq(t, "plan delete", recs(c, "delete"), "shop.test A "+boxIP, "www.shop.test CNAME shop.test")
	eq(t, "plan byHand", recs(c, "byHand"))
	if !strings.Contains(c["summary"].(string), "deletes the 2 DNS record(s)") {
		t.Errorf("plan summary: %v", c["summary"])
	}
	// Nothing changed yet.
	if len(h.dns.Get("shop.test", "A")) != 1 {
		t.Fatal("the plan deleted a record")
	}

	out := h.confirmed("POST", "/v1/projects/shop/domains/shop.test/remove", map[string]any{})
	c = dnsOf(t, out)
	eq(t, "deleted", recs(c, "deleted"), "shop.test A "+boxIP, "www.shop.test CNAME shop.test")
	if len(h.dns.Get("shop.test", "A")) != 0 || len(h.dns.Get("www.shop.test", "CNAME")) != 0 {
		t.Errorf("records left: A %v CNAME %v", h.dns.Get("shop.test", "A"), h.dns.Get("www.shop.test", "CNAME"))
	}
	if n := len(h.m.created(t.Context())); n != 0 {
		t.Errorf("the box still remembers %d created records", n)
	}
}

func TestRemoveDomainKeepsRepointedAndHandRecords(t *testing.T) {
	h := connected(t)
	h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "shop.test", "app": "web", "www": true, "createRecords": true})
	// The owner points www somewhere else, and adds an IPv6 record by hand.
	h.dns.Set("www.shop.test", "CNAME", "elsewhere.example.")
	h.dns.Set("shop.test", "AAAA", "2001:db8::1")
	h.p.Reach.PublicIPs = append(h.p.Reach.PublicIPs, netip.MustParseAddr("2001:db8::1"))
	// A hand-made record in the same zone, for a name the box does not serve.
	h.dns.Set("blog.shop.test", "A", boxIP)

	code, plan := h.call("POST", "/v1/projects/shop/domains/shop.test/remove", map[string]any{})
	if code != 428 {
		t.Fatalf("plan: %d %v", code, plan)
	}
	c := dnsOf(t, plan)
	eq(t, "delete", recs(c, "delete"), "shop.test A "+boxIP)
	eq(t, "byHand", recs(c, "byHand"), "shop.test AAAA 2001:db8::1")
	eq(t, "kept", recs(c, "kept"), "www.shop.test CNAME elsewhere.example")

	out := h.confirmed("POST", "/v1/projects/shop/domains/shop.test/remove", map[string]any{})
	c = dnsOf(t, out)
	eq(t, "deleted", recs(c, "deleted"), "shop.test A "+boxIP)
	if got := h.dns.Get("www.shop.test", "CNAME"); !slices.Equal(got, []string{"elsewhere.example."}) {
		t.Errorf("re-pointed www: %v", got)
	}
	if got := h.dns.Get("shop.test", "AAAA"); len(got) != 1 {
		t.Errorf("hand-made AAAA: %v", got)
	}
	if got := h.dns.Get("blog.shop.test", "A"); len(got) != 1 {
		t.Errorf("unrelated record: %v", got)
	}
	if !strings.Contains(c["summary"].(string), "Delete these at your DNS host") {
		t.Errorf("summary: %v", c["summary"])
	}
}

func TestRemoveDomainKeepDNS(t *testing.T) {
	h := connected(t)
	h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "shop.test", "app": "web", "createRecords": true})
	code, plan := h.call("POST", "/v1/projects/shop/domains/shop.test/remove", map[string]any{"keepDns": true})
	if code != 428 {
		t.Fatalf("plan: %d %v", code, plan)
	}
	c := dnsOf(t, plan)
	eq(t, "delete", recs(c, "delete"))
	eq(t, "kept", recs(c, "kept"), "shop.test A "+boxIP)
	out := h.confirmed("POST", "/v1/projects/shop/domains/shop.test/remove", map[string]any{"keepDns": true})
	c = dnsOf(t, out)
	eq(t, "deleted", recs(c, "deleted"))
	if c["keepDns"] != true || len(h.dns.Get("shop.test", "A")) != 1 {
		t.Errorf("keepDns: %v, A %v", c, h.dns.Get("shop.test", "A"))
	}
}

// A DNS provider that fails never blocks the removal: the answer says what
// to delete by hand.
func TestRemoveDomainProviderFails(t *testing.T) {
	h := connected(t)
	h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "shop.test", "app": "web", "createRecords": true})
	h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "api.shop.test", "app": "api", "createRecords": true})

	// The provider lists but refuses to delete.
	fakeProvider.SetFail(errors.New("503 try later"), "DeleteRecords")
	out := h.confirmed("POST", "/v1/projects/shop/domains/shop.test/remove", map[string]any{})
	c := dnsOf(t, out)
	eq(t, "deleted", recs(c, "deleted"))
	eq(t, "byHand", recs(c, "byHand"), "shop.test A "+boxIP)
	if !strings.Contains(c["error"].(string), "503 try later") || h.domain("shop", "shop.test") != nil {
		t.Errorf("after a failed delete: %v; domain %v", c, h.domain("shop", "shop.test"))
	}

	// The provider fails outright: the plan and the answer list the
	// records the box made, from what it remembers.
	fakeProvider.SetFail(errors.New("401 token revoked"))
	code, plan := h.call("POST", "/v1/projects/shop/domains/api.shop.test/remove", map[string]any{})
	if code != 428 {
		t.Fatalf("plan: %d %v", code, plan)
	}
	eq(t, "plan byHand", recs(dnsOf(t, plan), "byHand"), "api.shop.test A "+boxIP)
	out = h.confirmed("POST", "/v1/projects/shop/domains/api.shop.test/remove", map[string]any{})
	c = dnsOf(t, out)
	eq(t, "byHand", recs(c, "byHand"), "api.shop.test A "+boxIP)
	if !strings.Contains(c["error"].(string), "401 token revoked") || h.domain("shop", "api.shop.test") != nil {
		t.Errorf("after a failing provider: %v", c)
	}
	fakeProvider.SetFail(nil)
	if len(h.dns.Get("api.shop.test", "A")) != 1 {
		t.Error("a record went although the provider failed")
	}
}

// A zone no connected provider holds: what public DNS has pointing here is
// listed to delete by hand.
func TestRemoveDomainUnmanagedZone(t *testing.T) {
	h := connected(t)
	h.dns.AddZone("hand.test") // after connecting: the provider does not hold it
	h.dns.Set("hand.test", "A", boxIP)
	h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "hand.test", "app": "web"})
	out := h.confirmed("POST", "/v1/projects/shop/domains/hand.test/remove", map[string]any{})
	c := dnsOf(t, out)
	eq(t, "byHand", recs(c, "byHand"), "hand.test A "+boxIP)
	eq(t, "deleted", recs(c, "deleted"))
}

// projects destroy plans and deletes its domains' records too.
func TestDestroyProjectDeletesCreatedRecords(t *testing.T) {
	h := connected(t)
	h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "shop.test", "app": "web", "www": true, "createRecords": true})
	h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "api.example.test", "app": "api", "createRecords": true})
	h.dns.Set("api.example.test", "AAAA", "2001:db8::9") // not this box: not listed

	code, plan := h.call("POST", "/v1/projects/shop/destroy", map[string]any{})
	if code != 428 {
		t.Fatalf("plan: %d %v", code, plan)
	}
	c := dnsOf(t, plan)
	eq(t, "plan delete", recs(c, "delete"), "shop.test A "+boxIP, "www.shop.test CNAME shop.test", "api.example.test A "+boxIP)
	eq(t, "hosts", toStrings(c["hosts"]), "api.example.test", "shop.test", "www.shop.test")

	out := h.confirmed("POST", "/v1/projects/shop/destroy", map[string]any{})
	c = dnsOf(t, out)
	eq(t, "deleted", recs(c, "deleted"), "shop.test A "+boxIP, "www.shop.test CNAME shop.test", "api.example.test A "+boxIP)
	if len(h.dns.Get("shop.test", "A"))+len(h.dns.Get("api.example.test", "A")) != 0 || len(h.dns.Get("api.example.test", "AAAA")) != 1 {
		t.Error("records left after destroy")
	}

	// keepDns on destroy keeps them.
	h.project("blog")
	h.confirmed("POST", "/v1/projects/blog/domains", map[string]any{"domain": "blog.example.test", "app": "web", "createRecords": true})
	out = h.confirmed("POST", "/v1/projects/blog/destroy", map[string]any{"keepDns": true})
	eq(t, "kept", recs(dnsOf(t, out), "kept"), "blog.example.test A "+boxIP)
	if len(h.dns.Get("blog.example.test", "A")) != 1 {
		t.Error("keepDns deleted the record")
	}
}

// dns records delete takes type and name (every value) or a value too.
func TestRecordsDeleteByTypeAndName(t *testing.T) {
	h := connected(t)
	h.dns.Set("old.shop.test", "A", "203.0.113.1", "203.0.113.2")
	h.dns.Set("shop.test", "TXT", "google-site-verification=abc", "v=spf1 -all")

	code, out := h.call("POST", "/v1/dns/records/delete", map[string]any{"records": []map[string]any{{"type": "TXT", "name": "shop.test", "value": "google-site-verification=abc"}}})
	if code != 200 || out["deleted"] != float64(1) || !slices.Equal(h.dns.Get("shop.test", "TXT"), []string{"v=spf1 -all"}) {
		t.Fatalf("by value: %d %v", code, out)
	}
	code, out = h.call("POST", "/v1/dns/records/delete", map[string]any{"records": []map[string]any{{"type": "a", "name": "Old.Shop.Test."}, {"type": "CNAME", "name": "gone.shop.test"}}})
	if code != 200 || out["deleted"] != float64(2) || len(h.dns.Get("old.shop.test", "A")) != 0 {
		t.Fatalf("by type and name: %d %v", code, out)
	}
	eq(t, "records", recs(out, "records"), "old.shop.test A 203.0.113.1", "old.shop.test A 203.0.113.2")
	if nf, _ := out["notFound"].([]any); len(nf) != 1 || !strings.Contains(out["summary"].(string), "Not there: gone.shop.test CNAME") {
		t.Errorf("not found: %v", out)
	}

	// Records the box relies on are still refused, by name too.
	h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "shop.test", "app": "web", "createRecords": true})
	if code, out := h.call("POST", "/v1/dns/records/delete", map[string]any{"records": []map[string]any{{"type": "A", "name": "shop.test"}}}); code != 409 || len(h.dns.Get("shop.test", "A")) != 1 {
		t.Errorf("in use: %d %v", code, out)
	}
	// Outside every connected zone.
	if code, _ := h.call("POST", "/v1/dns/records/delete", map[string]any{"records": []map[string]any{{"type": "A", "name": "x.example.com"}}}); code != 412 {
		t.Errorf("unconnected zone: %d", code)
	}
	// A name and a type are needed.
	if code, _ := h.call("POST", "/v1/dns/records/delete", map[string]any{"records": []map[string]any{{"name": "shop.test"}}}); code != 422 {
		t.Errorf("no type: %d", code)
	}
}
