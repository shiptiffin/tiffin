package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// TestGoogleLive runs the real flow against Google with a service account
// key and a domain on Cloudflare: sign in, get a DNS token, add the TXT
// record (added beside the zone's other TXT records, never replacing them),
// verify, add the property, submit the sitemap, read search data and
// inspect the home page. It runs only when asked:
//
//	TIFFIN_GOOGLE_LIVE=1 TIFFIN_GOOGLE_KEY=.secrets/google-service-account.json \
//	TIFFIN_GOOGLE_DOMAIN=example.com CLOUDFLARE_API_TOKEN=... go test ./internal/mod/search -run GoogleLive -v
func TestGoogleLive(t *testing.T) {
	if os.Getenv("TIFFIN_GOOGLE_LIVE") == "" {
		t.Skip("set TIFFIN_GOOGLE_LIVE=1 (see the comment) to run against Google")
	}
	raw, err := os.ReadFile(os.Getenv("TIFFIN_GOOGLE_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	domain := os.Getenv("TIFFIN_GOOGLE_DOMAIN")
	ctx := context.Background()
	hc := &http.Client{Timeout: 30 * time.Second}
	g, err := newGClient(raw, hc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.accessToken(ctx); err != nil {
		t.Fatalf("sign-in: %v", err)
	}
	sites, err := g.sites(ctx)
	t.Logf("sites before: %+v (err %v)", sites, err)

	site := gsite{Type: "INET_DOMAIN", Identifier: domain}
	tok, err := g.verificationToken(ctx, site, "DNS_TXT")
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	t.Logf("token: %s", tok)
	if err := cfAddTXT(ctx, hc, os.Getenv("CLOUDFLARE_API_TOKEN"), domain, tok); err != nil {
		t.Fatalf("TXT: %v", err)
	}
	var verr error
	for i := 0; i < 12; i++ {
		if verr = g.verify(ctx, site, "DNS_TXT"); verr == nil {
			break
		}
		t.Logf("verify try %d: %v", i+1, verr)
		time.Sleep(10 * time.Second)
	}
	if verr != nil {
		t.Fatalf("verify: %v", verr)
	}
	prop := "sc-domain:" + domain
	if err := g.addSite(ctx, prop); err != nil {
		t.Fatalf("add site: %v", err)
	}
	if err := g.submitSitemap(ctx, prop, "https://"+domain+"/sitemap.xml"); err != nil {
		t.Errorf("sitemap: %v", err)
	}
	sites, _ = g.sites(ctx)
	t.Logf("sites after: %+v", sites)
	rows, err := g.query(ctx, prop, time.Now().AddDate(0, 0, -28), time.Now(), []string{"query"}, nil, 10)
	t.Logf("top queries (err %v):", err)
	for _, r := range rows {
		t.Logf("  %v clicks %.0f impressions %.0f position %.1f", r.Keys, r.Clicks, r.Impressions, r.Position)
	}
	ins, err := g.inspect(ctx, prop, "https://"+domain+"/")
	t.Logf("inspect home: %+v (err %v)", ins, err)
}

// cfAddTXT adds one TXT value at name through Cloudflare's API, unless it
// is there already.
func cfAddTXT(ctx context.Context, hc *http.Client, token, name, value string) error {
	do := func(method, path string, body any, out any) error {
		var rd *strings.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = strings.NewReader(string(b))
		} else {
			rd = strings.NewReader("")
		}
		req, _ := http.NewRequestWithContext(ctx, method, "https://api.cloudflare.com/client/v4"+path, rd)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := hc.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("cloudflare %s %s: %s", method, path, resp.Status)
		}
		return json.NewDecoder(resp.Body).Decode(out)
	}
	var zones struct {
		Result []struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	labels := strings.Split(name, ".")
	zone := strings.Join(labels[len(labels)-2:], ".")
	if err := do("GET", "/zones?name="+url.QueryEscape(zone), nil, &zones); err != nil || len(zones.Result) == 0 {
		return fmt.Errorf("zone %s: %v", zone, err)
	}
	id := zones.Result[0].ID
	var recs struct {
		Result []struct {
			Content string `json:"content"`
		} `json:"result"`
	}
	if err := do("GET", "/zones/"+id+"/dns_records?type=TXT&name="+url.QueryEscape(name), nil, &recs); err != nil {
		return err
	}
	for _, r := range recs.Result {
		if strings.Trim(r.Content, `"`) == value {
			return nil
		}
	}
	var out any
	return do("POST", "/zones/"+id+"/dns_records", map[string]any{"type": "TXT", "name": name, "content": `"` + value + `"`, "ttl": 1}, &out)
}
