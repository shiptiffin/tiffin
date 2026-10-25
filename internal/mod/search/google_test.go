package search

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/dnskit"
	"github.com/shiptiffin/tiffin/internal/platform"
)

// fakeDNS is a connected provider holding example.com.
type fakeDNS struct {
	mu   sync.Mutex
	txts map[string][]string
}

func (f *fakeDNS) Manages(_ context.Context, name string) bool {
	return name == "example.com" || strings.HasSuffix(name, ".example.com")
}
func (f *fakeDNS) SetRecords(context.Context, []dnskit.Record, string) error    { return nil }
func (f *fakeDNS) DeleteRecords(context.Context, []dnskit.Record, string) error { return nil }
func (f *fakeDNS) AddRecords(_ context.Context, recs []dnskit.Record, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range recs {
		f.txts[r.Name] = append(f.txts[r.Name], r.Value)
	}
	return nil
}

func (f *fakeDNS) has(name, value string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, v := range f.txts[name] {
		if v == value {
			return true
		}
	}
	return false
}

// fakeGoogle answers the Google APIs the box calls.
type fakeGoogle struct {
	mu       sync.Mutex
	dns      *fakeDNS
	files    func(host, path string) string // what the box serves
	owners   [][]string
	sites    []string
	sitemaps []string
	inspects int
}

func (g *fakeGoogle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var body map[string]any
	if r.Body != nil {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
	}
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	p := r.URL.Path
	switch {
	case p == "/token":
		reply(map[string]any{"access_token": "tok", "expires_in": 3600})
	case p == "/siteVerification/v1/token":
		if body["verificationMethod"] == "FILE" {
			reply(map[string]string{"token": "google0123.html"})
		} else {
			reply(map[string]string{"token": "google-site-verification=abc"})
		}
	case p == "/siteVerification/v1/webResource":
		site := body["site"].(map[string]any)
		id := site["identifier"].(string)
		ok := false
		if r.URL.Query().Get("verificationMethod") == "DNS_TXT" {
			ok = g.dns.has(id, "google-site-verification=abc")
		} else {
			host := strings.TrimSuffix(strings.TrimPrefix(id, "https://"), "/")
			ok = g.files(host, "/google0123.html") == "google-site-verification: google0123.html"
		}
		if !ok {
			w.WriteHeader(400)
			reply(map[string]any{"error": map[string]string{"message": "The necessary verification token could not be found on your site."}})
			return
		}
		var owners []string
		for _, o := range body["owners"].([]any) {
			owners = append(owners, o.(string))
		}
		g.owners = append(g.owners, owners)
		reply(body)
	case r.Method == http.MethodGet && p == "/webmasters/v3/sites":
		reply(map[string]any{})
	case strings.Contains(p, "/sitemaps/"):
		g.sitemaps = append(g.sitemaps, p)
		w.WriteHeader(204)
	case strings.HasSuffix(p, "/searchAnalytics/query"):
		dim := body["dimensions"].([]any)[0].(string)
		rows := map[string][]map[string]any{
			"date":  {{"keys": []string{"2026-10-20"}, "clicks": 3, "impressions": 40, "position": 8.0}, {"keys": []string{"2026-10-21"}, "clicks": 1, "impressions": 60, "position": 12.0}},
			"query": {{"keys": []string{"self hosted vercel"}, "clicks": 3, "impressions": 50, "position": 7.5}},
			"page":  {{"keys": []string{"https://shop.example.com/"}, "clicks": 4, "impressions": 100, "position": 10.4}},
		}[dim]
		reply(map[string]any{"rows": rows})
	case r.Method == http.MethodPut && strings.HasPrefix(p, "/webmasters/v3/sites/"):
		g.sites = append(g.sites, strings.TrimPrefix(p, "/webmasters/v3/sites/"))
		w.WriteHeader(204)
	case p == "/v1/urlInspection/index:inspect":
		g.inspects++
		verdict, cov := "PASS", "Submitted and indexed"
		if strings.HasSuffix(body["inspectionUrl"].(string), "/new") {
			verdict, cov = "NEUTRAL", "Discovered - currently not indexed"
		}
		reply(map[string]any{"inspectionResult": map[string]any{"indexStatusResult": map[string]any{"verdict": verdict, "coverageState": cov}}})
	default:
		w.WriteHeader(404)
	}
}

func testKey(t *testing.T) []byte {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	raw, _ := json.Marshal(map[string]string{
		"type": "service_account", "project_id": "tiffin-test", "client_email": "tiffin@tiffin-test.iam.gserviceaccount.com",
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), "token_uri": "https://oauth2.googleapis.com/token",
	})
	return raw
}

func TestGoogleConnection(t *testing.T) {
	h := newHarness(t)
	sec, err := platform.OpenSecrets(h.m.p.DB, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dns := &fakeDNS{txts: map[string][]string{"example.com": {"v=spf1 -all"}}}
	h.m.p.Secrets, h.m.p.DNS = sec, dns
	fg := &fakeGoogle{dns: dns, files: func(host, path string) string {
		for _, rt := range h.m.googleRoutes(context.Background()) {
			if rt.Host == host && rt.PathPrefix == path {
				return rt.Body
			}
		}
		return ""
	}}
	h.s.google = fg
	h.m.googleBase = "https://google.test"

	if _, err := h.m.connectGoogle(h.ctx, []byte(`{"type":"authorized_user"}`), "", "t"); err == nil {
		t.Fatal("a user key was taken")
	}
	if _, err := h.m.connectGoogle(h.ctx, testKey(t), "owner@gmail.com", "t"); err != nil {
		t.Fatal(err)
	}
	pages := map[string]string{"https://shop.example.com/": "2026-10-01", "https://shop.example.com/new": "2026-10-24"}
	h.s.set("shop.example.com", "/sitemap.xml", sitemap(pages))
	h.s.set("blog.other.org", "/sitemap.xml", sitemap(map[string]string{"https://blog.other.org/": "2026-10-01"}))
	h.serve("shop.example.com", "d1")
	h.serve("blog.other.org", "d1")

	h.pass() // IndexNow, then Google: a token, its TXT record beside SPF, the file for blog.other.org
	if !dns.has("example.com", "google-site-verification=abc") || !dns.has("example.com", "v=spf1 -all") {
		t.Fatalf("TXT records %v", dns.txts)
	}
	if gh := h.host("shop.example.com").Google; gh == nil || gh.State != GVerifying || gh.Property != "sc-domain:example.com" {
		t.Fatalf("before verifying %+v", gh)
	}
	h.now = h.now.Add(20 * time.Second)
	h.pass() // verify, add, sitemap, data
	gh := h.host("shop.example.com").Google
	if gh.State != GVerified || gh.Clicks != 4 || gh.Impressions != 100 || gh.Position != 10.4 || len(gh.Queries) != 1 || gh.Queries[0].Key != "self hosted vercel" {
		t.Fatalf("verified %+v", gh)
	}
	if gh.Index == nil || gh.Index.Checked != 2 || gh.Index.Indexed != 1 || len(gh.Index.NotIndexed) != 1 || gh.Index.NotIndexed[0].Coverage != "Discovered - currently not indexed" {
		t.Fatalf("index %+v", gh.Index)
	}
	if len(fg.owners) != 2 || fg.owners[0][1] != "owner@gmail.com" {
		t.Fatalf("owners %v", fg.owners)
	}
	bg := h.host("blog.other.org").Google
	if bg.State != GVerified || bg.Property != "https://blog.other.org/" {
		t.Fatalf("file-verified host %+v", bg)
	}
	if len(fg.sitemaps) != 2 {
		t.Fatalf("sitemaps %v", fg.sitemaps)
	}
	// A host without a sitemap still gets its data, and its home page inspected.
	h.serve("nomap.example.com", "d1")
	h.pass()
	if nm := h.host("nomap.example.com").Google; nm.State != GVerified || nm.PulledAt == nil || nm.Index == nil || nm.Index.Checked != 1 {
		t.Fatalf("no-sitemap host %+v", nm)
	}
	// Twice a day, not every pass.
	n := fg.inspects
	h.now = h.now.Add(time.Hour)
	h.pass()
	if fg.inspects != n {
		t.Fatal("pulled again within the hour")
	}
	// A new owner later: added to the verified properties at the next pass.
	if _, err := h.m.setGoogleOwner(h.ctx, "Second@Gmail.com"); err != nil {
		t.Fatal(err)
	}
	h.pass()
	if last := fg.owners[len(fg.owners)-1]; last[1] != "second@gmail.com" {
		t.Fatalf("new owner %v", fg.owners)
	}
	v, _ := h.m.googleView(h.ctx)
	if !v.Connected || v.ClientEmail != "tiffin@tiffin-test.iam.gserviceaccount.com" || len(v.Properties) != 2 {
		t.Fatalf("view %+v", v)
	}
	if err := h.m.disconnectGoogle(h.ctx); err != nil {
		t.Fatal(err)
	}
	if gh := h.host("shop.example.com").Google; gh.State != "not_connected" {
		t.Fatalf("after disconnect %+v", gh)
	}
}
