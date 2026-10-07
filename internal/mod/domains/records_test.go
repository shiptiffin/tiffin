package domains

import (
	"slices"
	"strings"
	"testing"
)

func TestZoneRecords(t *testing.T) {
	h := newHarness(t)
	// No provider yet: the records live at the DNS host.
	if code, out := h.call("GET", "/v1/dns/records?name=shop.test", nil); code != 412 || !strings.Contains(out["detail"].(string), "DNS host") {
		t.Fatalf("unconnected: %d %v", code, out)
	}
	if code, out := h.call("PUT", "/v1/dns/providers/fake", map[string]any{"token": "t"}); code != 200 {
		t.Fatalf("connect: %d %v", code, out)
	}
	h.project("shop")
	h.confirmed("POST", "/v1/projects/shop/domains", map[string]any{"domain": "shop.test", "app": "web", "www": true, "createRecords": true})
	h.dns.Set("shop.test", "TXT", "google-site-verification=abc", "v=spf1 -all")
	h.dns.Set("shop.test", "MX", "10 mail.shop.test.")
	h.dns.Set("shop.test", "NS", "ns1.shop.test.")
	h.dns.Set("blog.shop.test", "A", "203.0.113.50")

	code, out := h.call("GET", "/v1/dns/records?name=www.shop.test", nil)
	if code != 200 || out["zone"] != "shop.test" || out["provider"] != "fake" || out["providerLabel"] != "Fake DNS" {
		t.Fatalf("list: %d %v", code, out)
	}
	managed := map[string]string{}
	for _, r := range out["records"].([]any) {
		r := r.(map[string]any)
		k := r["type"].(string) + " " + r["host"].(string) + " " + r["value"].(string)
		m, _ := r["managed"].(string)
		managed[k] = m
	}
	for k, want := range map[string]string{
		"A @ " + boxIP:                       "tiffin", // the box's own records for the domain
		"A www " + boxIP:                     "tiffin",
		"TXT @ google-site-verification=abc": "",
		"MX @ 10 mail.shop.test":             "",
		"NS @ ns1.shop.test":                 "host",
		"A blog 203.0.113.50":                "", // not a name the box serves
	} {
		got, ok := managed[k]
		if !ok || got != want {
			t.Errorf("%s: managed %q (present %v), want %q; all %v", k, got, ok, want, managed)
		}
	}

	// Deleting one verification value keeps the SPF record under the same name.
	code, out = h.call("POST", "/v1/dns/records/delete", map[string]any{"records": []map[string]any{{"type": "TXT", "name": "shop.test", "value": "google-site-verification=abc"}}})
	if code != 200 || out["deleted"] != float64(1) {
		t.Fatalf("delete: %d %v", code, out)
	}
	if got := h.dns.Get("shop.test", "TXT"); !slices.Equal(got, []string{"v=spf1 -all"}) {
		t.Errorf("TXT after delete: %v", got)
	}
	// The box's own records are refused.
	if code, _ := h.call("POST", "/v1/dns/records/delete", map[string]any{"records": []map[string]any{{"type": "A", "name": "shop.test", "value": boxIP}}}); code != 409 || !slices.Equal(h.dns.Get("shop.test", "A"), []string{boxIP}) {
		t.Errorf("deleting a managed record: %d", code)
	}
	if code, _ := h.call("POST", "/v1/dns/records/delete", map[string]any{"records": []map[string]any{{"type": "TXT", "name": "x.example.com", "value": "v"}}}); code != 412 {
		t.Errorf("a zone no provider holds: %d", code)
	}

	// Lookup asks public DNS (the test server) and answers for any name.
	code, out = h.call("GET", "/v1/dns/lookup?name=shop.test&type=TXT", nil)
	if code != 200 || !slices.Equal(toStrings(out["values"]), []string{"v=spf1 -all"}) {
		t.Errorf("lookup: %d %v", code, out)
	}
	if code, out := h.call("GET", "/v1/dns/lookup?name=_github-pages-challenge-me.shop.test&type=TXT", nil); code != 200 || len(toStrings(out["values"])) != 0 || !strings.Contains(out["summary"].(string), "no TXT record") {
		t.Errorf("lookup missing: %d %v", code, out)
	}
	if code, _ := h.call("GET", "/v1/dns/lookup?name=not%20a%20name&type=TXT", nil); code != 422 {
		t.Errorf("bad name: %d", code)
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}
