package search

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/api"
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
	mu         sync.Mutex
	dns        *fakeDNS
	files      func(host, path string) string // what the box serves
	owned      map[string]bool                // sites the account is a verified owner of
	scope      string                         // what the person granted ("": everything asked for)
	codes      []url.Values                   // the code exchanges
	revokes    int
	sites      []string
	sitemaps   []string
	inspects   int
	inspected  []string // the pages asked about, in order
	newIndexed bool     // Google has indexed /new by now
	queries    []string // the dimensions of each Search Analytics query
	incomplete string   // the first day Google says it is still filling in
	apiOff     bool     // the Site Verification API is off
	revoked    bool     // the person removed the box's access
	tokenOff   bool     // a managed box never asks Google's token endpoint itself
}

func (g *fakeGoogle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var body map[string]any
	var form url.Values
	if r.Body != nil {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		form, _ = url.ParseQuery(string(raw))
	}
	if g.owned == nil {
		g.owned = map[string]bool{}
	}
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	p := r.URL.Path
	switch {
	case p == "/token" && g.tokenOff:
		w.WriteHeader(418)
	case p == "/token" && form.Get("grant_type") == "authorization_code":
		g.codes = append(g.codes, form)
		if form.Get("code") != "good-code" || form.Get("client_secret") != "box-secret" || form.Get("code_verifier") == "" {
			w.WriteHeader(400)
			reply(map[string]any{"error": "invalid_grant", "error_description": "Bad Request"})
			return
		}
		scope := g.scope
		if scope == "" {
			scope = googleScopes
		}
		claims, _ := json.Marshal(map[string]any{"email": "Owner@Gmail.com", "email_verified": true})
		reply(map[string]any{"access_token": "tok", "expires_in": 3600, "refresh_token": "refresh-1", "scope": scope,
			"id_token": "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"})
	case p == "/token":
		if g.revoked || form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") == "" || form.Get("client_id") != testApp.ClientID {
			w.WriteHeader(400)
			reply(map[string]any{"error": "invalid_grant", "error_description": "Token has been expired or revoked."})
			return
		}
		reply(map[string]any{"access_token": "tok", "expires_in": 3600})
	case p == "/revoke":
		g.revokes++
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
		if site["type"] == "INET_DOMAIN" {
			id = "dns://" + id
		}
		g.owned[id] = true
		reply(body)
	case strings.HasPrefix(p, "/siteVerification/v1/webResource/") && r.Method == http.MethodGet:
		// Google answers for a site only to its owners.
		if !g.owned[strings.TrimPrefix(p, "/siteVerification/v1/webResource/")] {
			w.WriteHeader(403)
			reply(map[string]any{"error": map[string]string{"message": "You are not an owner of this site."}})
			return
		}
		reply(map[string]any{"owners": []string{"owner@gmail.com"}})
	case r.Method == http.MethodGet && p == "/webmasters/v3/sites":
		reply(map[string]any{})
	case strings.Contains(p, "/sitemaps/") && r.Method == http.MethodGet:
		reply(map[string]any{"lastSubmitted": "2026-10-24T12:00:00Z", "lastDownloaded": "2026-10-24T12:05:00Z", "errors": "0", "warnings": "1",
			"contents": []map[string]string{{"type": "web", "submitted": "2"}}})
	case strings.Contains(p, "/sitemaps/"):
		g.sitemaps = append(g.sitemaps, p)
		w.WriteHeader(204)
	case strings.HasSuffix(p, "/searchAnalytics/query"):
		var dims []string
		for _, d := range body["dimensions"].([]any) {
			dims = append(dims, d.(string))
		}
		g.queries = append(g.queries, strings.Join(dims, ","))
		rows := map[string][]map[string]any{
			"date":  {{"keys": []string{"2026-10-20"}, "clicks": 3, "impressions": 40, "position": 8.0}, {"keys": []string{"2026-10-21"}, "clicks": 1, "impressions": 60, "position": 12.0}},
			"query": {{"keys": []string{"a tie"}, "clicks": 3, "impressions": 10, "position": 4.0}, {"keys": []string{"self hosted vercel"}, "clicks": 3, "impressions": 50, "position": 7.5}},
			"page":  {{"keys": []string{"https://shop.example.com/"}, "clicks": 4, "impressions": 100, "position": 10.4}},
			// Day by day (the history): Google answers whatever days were asked
			// for with these, so the store must keep only the days it asked for.
			"date,query": {{"keys": []string{"2026-10-20", "self hosted vercel"}, "clicks": 3, "impressions": 30, "position": 7.0},
				{"keys": []string{"2026-10-21", "tiffin box"}, "clicks": 1, "impressions": 20, "position": 10.0}},
			"date,page": {{"keys": []string{"2026-10-20", "https://shop.example.com/"}, "clicks": 3, "impressions": 40, "position": 8.0},
				{"keys": []string{"2026-10-21", "https://shop.example.com/pricing"}, "clicks": 1, "impressions": 60, "position": 12.0}},
		}[strings.Join(dims, ",")]
		out := map[string]any{"rows": rows}
		if g.incomplete != "" {
			out["metadata"] = map[string]string{"firstIncompleteDate": g.incomplete}
		}
		reply(out)
	case r.Method == http.MethodPut && strings.HasPrefix(p, "/webmasters/v3/sites/"):
		g.sites = append(g.sites, strings.TrimPrefix(p, "/webmasters/v3/sites/"))
		w.WriteHeader(204)
	case p == "/v1/urlInspection/index:inspect":
		g.inspects++
		page := body["inspectionUrl"].(string)
		g.inspected = append(g.inspected, page)
		res := map[string]any{"verdict": "PASS", "coverageState": "Submitted and indexed", "crawledAs": "MOBILE", "indexingState": "INDEXING_ALLOWED",
			"sitemap": []string{"https://shop.example.com/sitemap.xml"}, "referringUrls": []string{"https://shop.example.com/"}}
		if strings.HasSuffix(page, "/new") && !g.newIndexed {
			res = map[string]any{"verdict": "NEUTRAL", "coverageState": "Discovered - currently not indexed", "crawledAs": "CRAWLING_USER_AGENT_UNSPECIFIED",
				"indexingState": "INDEXING_STATE_UNSPECIFIED", "sitemap": []string{"https://shop.example.com/sitemap.xml"}}
		}
		reply(map[string]any{"inspectionResult": map[string]any{"indexStatusResult": res}})
	default:
		w.WriteHeader(404)
	}
}

// testApp is the box's Google sign-in keys in tests.
var testApp = googleApp{ClientID: "1234567-box.apps.googleusercontent.com", ClientSecret: "box-secret",
	RedirectURL: "https://dashboard.box.example.net/api/auth/callback/google"}

// connect stores a connection as owner@gmail.com, as the callback does.
func (h *harness) connect() error {
	_, err := h.m.connectGoogle(h.ctx, "refresh-1", "owner@gmail.com", "t")
	return err
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

	fg.apiOff = true
	if err := h.connect(); err == nil || !strings.Contains(err.Error(), "Site Verification API") ||
		!strings.Contains(err.Error(), "siteverification.googleapis.com?project=1234567") {
		t.Fatalf("connected with the API off: %v", err)
	}
	fg.apiOff = false
	if err := h.connect(); err != nil {
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
	if gh.State != GVerified || gh.Clicks != 4 || gh.Impressions != 100 || gh.Position != 10.4 || len(gh.Queries) != 2 || gh.Queries[0].Key != "self hosted vercel" {
		t.Fatalf("verified %+v", gh) // the same clicks: more impressions first
	}
	// 28 days to the read, the last ones still filling in.
	if gh.From != "2026-09-27" || gh.To != "2026-10-24" || gh.Complete != "2026-10-21" {
		t.Fatalf("days %s..%s complete %s", gh.From, gh.To, gh.Complete)
	}
	if gh.Index == nil || gh.Index.Checked != 2 || gh.Index.Indexed != 1 || len(gh.Index.NotIndexed) != 1 || gh.Index.NotIndexed[0].Coverage != "Discovered - currently not indexed" {
		t.Fatalf("index %+v", gh.Index)
	}
	if len(fg.sites) != 2 { // each property is in the person's own Search Console
		t.Fatalf("sites %v", fg.sites)
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
	// The person removes the box in their Google account: said on the
	// connection, in the box's checks and on each address; a call that works
	// clears it.
	fg.revoked = true
	h.m.gc.token = "" // the next call signs in again
	h.now = h.now.Add(gPullEvery)
	h.pass()
	if c := h.m.Checks(h.ctx, h.m.p); len(c) != 1 || c[0].OK || !strings.Contains(c[0].Detail, "Connect with Google again") {
		t.Fatalf("checks %+v", c)
	}
	if gh := h.host("shop.example.com").Google; !strings.Contains(gh.Error, "revoked") {
		t.Fatalf("host error %+v", gh)
	}
	fg.revoked = false
	h.pass()
	if c := h.m.Checks(h.ctx, h.m.p); !c[0].OK {
		t.Fatalf("checks after a working call %+v", c)
	}
	v, _ := h.m.googleView(h.ctx)
	if !v.Connected || v.Email != "owner@gmail.com" || !v.Ready || v.Project != "1234567" || len(v.Properties) != 2 {
		t.Fatalf("view %+v", v)
	}
	// Connecting again (the same account or another): every property is
	// looked at again, and what Google said before stays. One the account
	// owns needs no new proof and goes into its Search Console.
	if err := h.connect(); err != nil {
		t.Fatal(err)
	}
	if gh := h.host("shop.example.com").Google; gh.State != GVerifying || gh.PulledAt == nil || gh.Clicks != 4 {
		t.Fatalf("after connecting again %+v", gh)
	}
	txts, sites := len(dns.txts["example.com"]), len(fg.sites)
	h.pass()
	if gh := h.host("shop.example.com").Google; gh.State != GVerified || !strings.Contains(gh.Detail, "owns it") {
		t.Fatalf("an owner verified again %+v", gh)
	}
	if len(dns.txts["example.com"]) != txts || len(fg.sites) != sites+2 {
		t.Fatalf("an owned property waited for its proof, or was not added: %v %v", dns.txts, fg.sites)
	}
	// Then the account gets its own proof, so it doesn't depend on whoever
	// added it as an owner; the property stays verified meanwhile.
	h.pass()
	if gh := h.host("shop.example.com").Google; gh.State != GVerified || len(dns.txts["example.com"]) != txts+1 {
		t.Fatalf("the proof of an owned property %+v %v", gh, dns.txts)
	}
	h.now = h.now.Add(20 * time.Second)
	h.pass()
	if gh := h.host("shop.example.com").Google; gh.State != GVerified || gh.Detail != "Verified." {
		t.Fatalf("an owned property proved %+v", gh)
	}
	if v, _ := h.m.googleView(h.ctx); len(v.Properties) != 2 || v.Properties[0].State != GVerified || v.Properties[1].State != GVerified {
		t.Fatalf("view after proving %+v", v)
	}
	if err := h.m.disconnectGoogle(h.ctx); err != nil {
		t.Fatal(err)
	}
	// Disconnecting forgets the token; it never revokes at Google, which
	// would end the person's other connections and sign-ins with the app.
	if gh := h.host("shop.example.com").Google; gh.State != "not_connected" || fg.revokes != 0 {
		t.Fatalf("after disconnect %+v, %d revokes", gh, fg.revokes)
	}
}

// back sends the browser back from Google to the box's callback.
func (h *harness) back(query, cookie string) string {
	r := httptest.NewRequest(http.MethodGet, "/api/auth/callback/google?"+query, nil)
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: googleStateCookie, Value: cookie})
	}
	w := httptest.NewRecorder()
	h.m.googleCallback(w, r)
	body := w.Body.String()
	_, after, _ := strings.Cut(body, googleSettings+"?google=")
	code, _, _ := strings.Cut(after, `"`)
	return code
}

func TestConnectWithGoogle(t *testing.T) {
	h := newHarness(t)
	sec, err := platform.OpenSecrets(h.m.p.DB, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dns := &fakeDNS{txts: map[string][]string{}}
	h.m.p.Secrets, h.m.p.DNS = sec, dns
	fg := &fakeGoogle{dns: dns, files: func(string, string) string { return "" }}
	h.s.google = fg
	h.m.googleBase = "https://google.test"

	// Without Google sign-in keys there is nothing to connect with.
	h.m.gapp = func(context.Context) (googleApp, error) { return googleApp{}, errNoGoogleKeys }
	if _, _, err := h.m.startGoogle(h.ctx, "Ada", "tok_1", ""); err == nil {
		t.Fatal("started without keys")
	}
	if v, _ := h.m.googleView(h.ctx); v.Ready || v.Connected {
		t.Fatalf("view without keys %+v", v)
	}
	h.m.gapp = func(context.Context) (googleApp, error) { return testApp, nil }

	start := func() (url.Values, string) {
		t.Helper()
		u, state, err := h.m.startGoogle(h.ctx, "Ada", "tok_1", "")
		if err != nil {
			t.Fatal(err)
		}
		pu, _ := url.Parse(u)
		return pu.Query(), state
	}
	q, state := start()
	if !strings.HasPrefix(state, GoogleStatePrefix) || q.Get("state") != state || q.Get("client_id") != testApp.ClientID ||
		q.Get("redirect_uri") != testApp.RedirectURL || q.Get("access_type") != "offline" || q.Get("prompt") != "consent" ||
		q.Get("code_challenge_method") != "S256" || !strings.Contains(q.Get("scope"), gScopeSearch) || !strings.Contains(q.Get("scope"), gScopeVerify) {
		t.Fatalf("consent page %v", q)
	}
	// Another browser (no cookie, or another state's) connects nothing, and
	// the state is spent.
	if code := h.back("code=good-code&state="+state, ""); code != gcExpired {
		t.Fatalf("without the cookie: %s", code)
	}
	_, state = start()
	if code := h.back("code=good-code&state="+state, GoogleStatePrefix+"other"); code != gcExpired {
		t.Fatalf("with another cookie: %s", code)
	}
	// The right browser with a code Google refuses; then the state is spent.
	if code := h.back("code=bad&state="+state, state); code != gcFailed {
		t.Fatalf("a bad code: %s", code)
	}
	if code := h.back("code=good-code&state="+state, state); code != gcExpired {
		t.Fatalf("a state worked twice: %s", code)
	}
	// Saying no, and leaving a permission unticked.
	_, state = start()
	if code := h.back("error=access_denied&state="+state, state); code != gcDenied {
		t.Fatalf("denied: %s", code)
	}
	_, state = start()
	fg.scope = "openid email " + gScopeSearch
	if code := h.back("code=good-code&state="+state, state); code != gcScopes || fg.revokes != 0 {
		t.Fatalf("one permission: %s, %d revokes", code, fg.revokes)
	}
	fg.scope = ""
	// An API off in the keys' project: nothing stored, and Settings says why.
	_, state = start()
	fg.apiOff = true
	if code := h.back("code=good-code&state="+state, state); code != gcFailed {
		t.Fatalf("API off: %s", code)
	}
	if v, _ := h.m.googleView(h.ctx); v.Connected || !strings.Contains(v.LastTry, "Site Verification API") {
		t.Fatalf("view after a failed try %+v", v)
	}
	fg.apiOff = false
	// Yes to everything.
	_, state = start()
	if code := h.back("code=good-code&state="+state, state); code != gcConnected {
		t.Fatalf("connected: %s", code)
	}
	if f := fg.codes[len(fg.codes)-1]; f.Get("redirect_uri") != testApp.RedirectURL || f.Get("client_id") != testApp.ClientID {
		t.Fatalf("code exchange %v", f)
	}
	v, _ := h.m.googleView(h.ctx)
	if !v.Connected || v.Email != "owner@gmail.com" || v.By != "Ada" || v.LastTry != "" {
		t.Fatalf("view %+v", v)
	}
	// A domain the person verified in Search Console themselves needs no
	// proof from the box.
	fg.owned["dns://example.com"] = true
	h.s.set("shop.example.com", "/sitemap.xml", sitemap(map[string]string{"https://shop.example.com/": "2026-10-01"}))
	h.serve("shop.example.com", "d1")
	h.pass()
	if gh := h.host("shop.example.com").Google; gh.State != GVerified || gh.Clicks != 4 || len(dns.txts["example.com"]) != 0 {
		t.Fatalf("an owned domain %+v %v", gh, dns.txts)
	}
	// Google refusing the proof for now doesn't unverify a domain they own.
	fg.apiOff = true
	h.pass()
	fg.apiOff = false
	if gh := h.host("shop.example.com").Google; gh.State != GVerified {
		t.Fatalf("an owned domain while its proof fails %+v", gh)
	}
	h.now = h.now.Add(time.Hour)
	h.pass()
	h.now = h.now.Add(20 * time.Second)
	h.pass()
	if gh := h.host("shop.example.com").Google; gh.State != GVerified || gh.Detail != "Verified." || !dns.has("example.com", "google-site-verification=abc") {
		t.Fatalf("an owned domain proved %+v %v", gh, dns.txts)
	}
}

// fakeRelay is the control plane of a managed box (site/app/api/box/google):
// it holds ShipTiffin's Google app, so it hands out the consent page, the
// grant (for the verifier whose hash started it) and each hour's token.
type fakeRelay struct {
	mu        sync.Mutex
	challenge string
	lapsed    bool // the subscription is not active
	revoked   bool
	renews    int
}

func (f *fakeRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]string
	_ = json.NewDecoder(r.Body).Decode(&body)
	refuse := func(status int, code, why string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": why})
	}
	switch {
	case r.Header.Get("Authorization") != "Bearer tl1.test":
		refuse(401, "bad_licence", "This box's ShipTiffin licence did not check out.")
	case f.lapsed:
		refuse(403, "not_active", "This box's ShipTiffin subscription is not active.")
	case r.URL.Path == "/api/box/google/start":
		f.challenge = body["challenge"]
		_ = json.NewEncoder(w).Encode(map[string]string{"url": "https://accounts.google.com/o/oauth2/v2/auth?client_id=shiptiffin&state=signed." + body["state"]})
	case r.URL.Path == "/api/box/google/token" && body["grant"] == "code":
		sum := sha256.Sum256([]byte(body["verifier"]))
		if body["code"] != "handoff-1" || base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge {
			refuse(400, "invalid_grant", "That code is not this box's.")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"refresh_token": "refresh-1", "scope": googleScopes, "email": "owner@gmail.com"})
	case r.URL.Path == "/api/box/google/token" && body["grant"] == "refresh":
		if f.revoked || body["refresh_token"] != "refresh-1" {
			refuse(400, "invalid_grant", "Token has been expired or revoked.")
			return
		}
		f.renews++
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	default:
		w.WriteHeader(404)
	}
}

// A managed box has no Google keys of its own: it connects through
// ShipTiffin's app, and the refresh token still stays on the box.
func TestConnectThroughShipTiffin(t *testing.T) {
	h := newHarness(t)
	sec, err := platform.OpenSecrets(h.m.p.DB, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dns := &fakeDNS{txts: map[string][]string{}}
	h.m.p.Secrets, h.m.p.DNS = sec, dns
	fg := &fakeGoogle{dns: dns, files: func(string, string) string { return "" }, tokenOff: true}
	fr := &fakeRelay{}
	h.s.google, h.s.relay = fg, fr
	h.m.googleBase = "https://google.test"
	h.m.gapp = func(context.Context) (googleApp, error) { return googleApp{}, errNoGoogleKeys }
	h.m.managed = func() *platform.ManagedConfig {
		return &platform.ManagedConfig{ControlPlane: "https://cp.test", BoxID: "box_1", Licence: "tl1.test"}
	}

	if v, _ := h.m.googleView(h.ctx); !v.Ready || !v.Managed || v.Project != "" {
		t.Fatalf("view on a managed box %+v", v)
	}
	// A lapsed subscription: the button says why, and nothing is pending.
	fr.lapsed = true
	if _, _, err := h.m.startGoogle(h.ctx, "Ada", "tok_1", ""); err == nil || !strings.Contains(err.Error(), "subscription is not active") {
		t.Fatalf("started on a lapsed box: %v", err)
	}
	fr.lapsed = false
	u, state, err := h.m.startGoogle(h.ctx, "Ada", "tok_1", "")
	if err != nil || !strings.Contains(u, "client_id=shiptiffin") || !strings.HasSuffix(u, state) {
		t.Fatalf("consent page %q: %v", u, err)
	}
	// A code that isn't this start's connects nothing.
	if code := h.back("code=handoff-2&state="+state, state); code != gcFailed {
		t.Fatalf("another code: %s", code)
	}
	_, state, _ = h.m.startGoogle(h.ctx, "Ada", "tok_1", "")
	if code := h.back("code=handoff-1&state="+state, state); code != gcConnected {
		v, _ := h.m.googleView(h.ctx)
		t.Fatalf("connected: %s (%s)", code, v.LastTry)
	}
	v, _ := h.m.googleView(h.ctx)
	if !v.Connected || v.Email != "owner@gmail.com" || v.By != "Ada" {
		t.Fatalf("view %+v", v)
	}
	h.s.set("shop.example.com", "/sitemap.xml", sitemap(map[string]string{"https://shop.example.com/": "2026-10-01"}))
	h.serve("shop.example.com", "d1")
	h.pass()
	h.now = h.now.Add(20 * time.Second)
	h.pass()
	if gh := h.host("shop.example.com").Google; gh.State != GVerified || gh.Clicks != 4 || fr.renews == 0 {
		t.Fatalf("a managed box's domain %+v (%d renewals)", gh, fr.renews)
	}
	// The subscription lapses, or the person removes the box at Google: said
	// on the connection; a renewal that works clears it.
	fr.lapsed = true
	h.m.gc.token = ""
	h.now = h.now.Add(gPullEvery)
	h.pass()
	if c := h.m.Checks(h.ctx, h.m.p); len(c) != 1 || c[0].OK || !strings.Contains(c[0].Detail, "subscription is not active") {
		t.Fatalf("checks on a lapsed box %+v", c)
	}
	fr.lapsed, fr.revoked = false, true
	h.pass()
	if c := h.m.Checks(h.ctx, h.m.p); c[0].OK || !strings.Contains(c[0].Detail, "Connect with Google again") {
		t.Fatalf("checks after a removal %+v", c)
	}
	fr.revoked = false
	h.pass()
	if c := h.m.Checks(h.ctx, h.m.p); !c[0].OK {
		t.Fatalf("checks after a renewal %+v", c)
	}
}

// A box connected with a service account key (earlier versions) is not
// connected: it says so, and keeps its domains and what Google said.
func TestServiceAccountKeyEnded(t *testing.T) {
	h, _ := googleShop(t)
	raw, _, _ := h.m.p.DB.KVGet(h.ctx, kvNS, "google")
	var old map[string]any
	_ = json.Unmarshal(raw, &old)
	delete(old, "kind")
	old["clientEmail"] = "tiffin@p.iam.gserviceaccount.com"
	raw, _ = json.Marshal(old)
	_ = h.m.p.DB.KVPut(h.ctx, kvNS, "google", raw)
	h.m.gc = nil
	h.pass()
	v, _ := h.m.googleView(h.ctx)
	if v.Connected || !v.Legacy || len(v.Properties) != 1 {
		t.Fatalf("view %+v", v)
	}
	if c := h.m.Checks(h.ctx, h.m.p); len(c) != 1 || c[0].OK || !strings.Contains(c[0].Detail, "Connect with Google") {
		t.Fatalf("checks %+v", c)
	}
	if gh := h.host("shop.example.com").Google; gh.State != "not_connected" {
		t.Fatalf("host %+v", gh)
	}
	if err := h.connect(); err != nil {
		t.Fatal(err)
	}
	h.pass()
	if gh := h.host("shop.example.com").Google; gh.State != GVerified || gh.Clicks != 4 {
		t.Fatalf("after connecting %+v", gh)
	}
}

// googleShop is a harness with Google connected and shop.example.com
// verified (its zone on the DNS provider), its sitemap / and /new, read once.
func googleShop(t *testing.T) (*harness, *fakeGoogle) {
	t.Helper()
	h := newHarness(t)
	sec, err := platform.OpenSecrets(h.m.p.DB, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dns := &fakeDNS{txts: map[string][]string{}}
	h.m.p.Secrets, h.m.p.DNS = sec, dns
	fg := &fakeGoogle{dns: dns, files: func(string, string) string { return "" }}
	h.s.google = fg
	h.m.googleBase = "https://google.test"
	if err := h.connect(); err != nil {
		t.Fatal(err)
	}
	h.s.set("shop.example.com", "/sitemap.xml", sitemap(map[string]string{"https://shop.example.com/": "2026-10-01", "https://shop.example.com/new": "2026-10-24"}))
	h.serve("shop.example.com", "d1")
	h.pass()
	h.now = h.now.Add(20 * time.Second)
	h.pass()
	if gh := h.host("shop.example.com").Google; gh.State != GVerified || gh.Index == nil || gh.Index.Checked != 2 {
		t.Fatalf("set up %+v", gh)
	}
	return h, fg
}

// A page Google hasn't indexed is asked about again after a day; one it has,
// after a week.
func TestInspectAgain(t *testing.T) {
	h, fg := googleShop(t)
	n := len(fg.inspected)
	h.now = h.now.Add(gPullEvery)
	h.pass()
	if len(fg.inspected) != n {
		t.Fatalf("asked again after 12 hours: %v", fg.inspected[n:])
	}
	h.now = h.now.Add(gPullEvery)
	h.pass()
	if got := fg.inspected[n:]; len(got) != 1 || got[0] != "https://shop.example.com/new" {
		t.Fatalf("after a day %v", got)
	}
	for range 12 { // a week on: the indexed page too, and the other again
		h.now = h.now.Add(gPullEvery)
		h.pass()
	}
	asked := map[string]int{}
	for _, u := range fg.inspected[n:] {
		asked[u]++
	}
	if asked["https://shop.example.com/new"] != 7 || asked["https://shop.example.com/"] != 1 {
		t.Fatalf("after a week %v", asked)
	}
}

// search inspect: a malformed page is refused before Google; the answer
// carries Google's diagnostics and, for a sitemap page, is kept.
func TestManualInspect(t *testing.T) {
	h, fg := googleShop(t)
	n := fg.inspects
	for _, bad := range []string{
		"https://shop.example.com/ /new /pricing", "https://shop.example.com//", "https://shop.example.com/a//b", "http://shop.example.com/",
		"/pricing", "shop.example.com/pricing", "https://shop.example.com:8443/", "https://u@shop.example.com/", "https://shop.example.com/#top",
	} {
		_, err := h.m.inspectOne(h.ctx, "shop", bad)
		var pb *api.Problem
		if !errors.As(err, &pb) || pb.Status != 422 || pb.Hint == "" {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	if _, err := h.m.inspectOne(h.ctx, "shop", "https://shop.example.com//"); !strings.Contains(err.(*api.Problem).Hint, "inspect https://shop.example.com/") {
		t.Fatalf("doubled slash hint %+v", err)
	}
	if _, err := h.m.inspectOne(h.ctx, "shop", "https://other.example.com/"); err == nil || err.(*api.Problem).Status != 404 {
		t.Fatalf("another host %v", err)
	}
	if fg.inspects != n {
		t.Fatalf("Google was asked about %d malformed pages", fg.inspects-n)
	}

	// Not indexed: Google's diagnostics come through, its *_UNSPECIFIED left out.
	r, err := h.m.inspectOne(h.ctx, "shop", "https://Shop.Example.com/new")
	if err != nil {
		t.Fatal(err)
	}
	if r.URL != "https://shop.example.com/new" || r.Verdict != "NEUTRAL" || r.CrawledAs != "" || r.Indexing != "" || len(r.Sitemaps) != 1 ||
		r.InSitemap == nil || !*r.InSitemap || len(r.Warnings) != 0 {
		t.Fatalf("not indexed %+v", r)
	}
	// Google indexes it: the answer someone asked for shows at once.
	fg.newIndexed = true
	h.now = h.now.Add(time.Hour)
	if r, err = h.m.inspectOne(h.ctx, "shop", "https://shop.example.com/new"); err != nil || r.CrawledAs != "MOBILE" || len(r.ReferringURLs) != 1 {
		t.Fatalf("indexed %+v %v", r, err)
	}
	if ix := h.host("shop.example.com").Google.Index; ix.Checked != 2 || ix.Indexed != 2 || len(ix.NotIndexed) != 0 {
		t.Fatalf("status after the inspect %+v", ix)
	}
	if rp, err := h.m.report(h.ctx, &reportIn{Project: "shop"}); err != nil || rp.Google.Index.Indexed != 2 {
		t.Fatalf("report after the inspect %+v %v", rp.Google.Index, err)
	}
	// The next pull keeps it and doesn't ask again before its turn.
	m := len(fg.inspected)
	h.now = h.now.Add(gPullEvery)
	h.pass()
	if d := h.m.gdataFor(h.ctx, "shop.example.com"); d.Inspected["https://shop.example.com/new"].Verdict != "PASS" || len(fg.inspected) != m {
		t.Fatalf("after the next pull %+v %v", d.Inspected, fg.inspected[m:])
	}

	// Off the sitemap: answered with a warning, not kept.
	for page, want := range map[string]string{
		"https://shop.example.com/pricing": "doesn't list this page",
		"https://shop.example.com/new/":    "the sitemap lists https://shop.example.com/new,",
	} {
		r, err := h.m.inspectOne(h.ctx, "shop", page)
		if err != nil || r.InSitemap == nil || *r.InSitemap || len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], want) {
			t.Fatalf("%s: %+v %v", page, r, err)
		}
		if _, kept := h.m.gdataFor(h.ctx, "shop.example.com").Inspected[page]; kept {
			t.Fatalf("%s was kept", page)
		}
	}
	// The home page without a slash is the home page.
	if r, err := h.m.inspectOne(h.ctx, "shop", "https://shop.example.com"); err != nil || r.URL != "https://shop.example.com/" || !*r.InSitemap {
		t.Fatalf("home page %+v %v", r, err)
	}
}

func TestPageURL(t *testing.T) {
	for raw, want := range map[string]string{
		"https://Shop.Example.com":           "https://shop.example.com/",
		" https://shop.example.com/a?b=1 ":   "https://shop.example.com/a?b=1",
		"https://shop.example.com/caf%C3%A9": "https://shop.example.com/caf%C3%A9",
	} {
		if pu, pb := pageURL(raw); pb != nil || pu.String() != want {
			t.Fatalf("%q: %v %v", raw, pu, pb)
		}
	}
	if _, pb := pageURL("https://shop.example.com/a b"); pb == nil || !strings.Contains(pb.Detail, "space") {
		t.Fatalf("space %v", pb)
	}
}
