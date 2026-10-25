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
	apiOff   bool // the Site Verification API is off
	revoked  bool // the key was deleted
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
		if g.revoked {
			w.WriteHeader(400)
			reply(map[string]any{"error": "invalid_grant", "error_description": "Invalid JWT Signature."})
			return
		}
		reply(map[string]any{"access_token": "tok", "expires_in": 3600})
	case g.apiOff && strings.HasPrefix(p, "/siteVerification/"):
		w.WriteHeader(403)
		reply(map[string]any{"error": map[string]string{"message": "Site Verification API has not been used in project 1 before or it is disabled."}})
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
		if body["owners"] != nil {
			w.WriteHeader(503) // what Google does
			return
		}
		reply(body)
	case strings.HasPrefix(p, "/siteVerification/v1/webResource/") && r.Method == http.MethodGet:
		reply(map[string]any{"owners": []string{"tiffin@tiffin-test.iam.gserviceaccount.com"}})
	case strings.HasPrefix(p, "/siteVerification/v1/webResource/") && r.Method == http.MethodPut:
		var owners []string
		for _, o := range body["owners"].([]any) {
			owners = append(owners, o.(string))
		}
		g.owners = append(g.owners, owners)
		reply(body)
	case r.Method == http.MethodGet && p == "/webmasters/v3/sites":
		reply(map[string]any{})
	case strings.Contains(p, "/sitemaps/") && r.Method == http.MethodGet:
		reply(map[string]any{"lastSubmitted": "2026-10-24T12:00:00Z", "lastDownloaded": "2026-10-24T12:05:00Z", "errors": "0", "warnings": "1",
			"contents": []map[string]string{{"type": "web", "submitted": "2"}}})
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

func testKey(t *testing.T) []byte { return testKeyFor(t, "tiffin@tiffin-test.iam.gserviceaccount.com") }

func testKeyFor(t *testing.T, email string) []byte {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	raw, _ := json.Marshal(map[string]string{
		"type": "service_account", "project_id": "tiffin-test", "client_email": email,
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
	fg.apiOff = true
	if _, err := h.m.connectGoogle(h.ctx, testKey(t), "owner@gmail.com", "t"); err == nil || !strings.Contains(err.Error(), "Site Verification API") {
		t.Fatalf("connected with the API off: %v", err)
	}
	fg.apiOff = false
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
	if sm := gh.Sitemap; sm == nil || sm.Pages != 2 || sm.Warnings != 1 || sm.LastRead == "" {
		t.Fatalf("sitemap status %+v", gh.Sitemap)
	}
	// Once: a deploy and the regular checks never submit it again; asking does.
	h.serve("shop.example.com", "d2")
	h.now = h.now.Add(Settle)
	h.pass()
	if len(fg.sitemaps) != 2 {
		t.Fatalf("resubmitted by itself: %v", fg.sitemaps)
	}
	if err := h.m.resubmitSitemap(h.ctx, "shop.example.com"); err != nil || len(fg.sitemaps) != 3 {
		t.Fatalf("resubmit: %v %v", err, fg.sitemaps)
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
	// The key is deleted in Google Cloud: said on the connection, in the
	// box's checks and on each address; a working key clears it.
	fg.revoked = true
	h.m.gc.token = "" // the next call signs in again
	h.now = h.now.Add(gPullEvery)
	h.pass()
	if c := h.m.Checks(h.ctx, h.m.p); len(c) != 1 || c[0].OK || !strings.Contains(c[0].Detail, "Invalid JWT") {
		t.Fatalf("checks %+v", c)
	}
	if gh := h.host("shop.example.com").Google; !strings.Contains(gh.Error, "Invalid JWT") {
		t.Fatalf("host error %+v", gh)
	}
	fg.revoked = false
	h.pass()
	if c := h.m.Checks(h.ctx, h.m.p); !c[0].OK {
		t.Fatalf("checks after a working call %+v", c)
	}
	v, _ := h.m.googleView(h.ctx)
	if !v.Connected || v.ClientEmail != "tiffin@tiffin-test.iam.gserviceaccount.com" || len(v.Properties) != 2 {
		t.Fatalf("view %+v", v)
	}
	// Another service account: every property starts again under it.
	if _, err := h.m.connectGoogle(h.ctx, testKeyFor(t, "other@p.iam.gserviceaccount.com"), "", "t"); err != nil {
		t.Fatal(err)
	}
	if gh := h.host("shop.example.com").Google; gh.State != GVerifying || gh.PulledAt != nil {
		t.Fatalf("after another account %+v", gh)
	}
	h.pass()
	h.now = h.now.Add(20 * time.Second)
	h.pass()
	if gh := h.host("shop.example.com").Google; gh.State != GVerified {
		t.Fatalf("verified again %+v", gh)
	}
	if err := h.m.disconnectGoogle(h.ctx); err != nil {
		t.Fatal(err)
	}
	if gh := h.host("shop.example.com").Google; gh.State != "not_connected" {
		t.Fatalf("after disconnect %+v", gh)
	}
}
