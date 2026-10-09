package analytics

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/state"
)

// accuracyFixture runs analytics for project shop (apps web at
// shop.box.test and docs at docs-shop.box.test) and project blog.
func accuracyFixture(t *testing.T) *Module {
	t.Helper()
	root := t.TempDir()
	db, err := state.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	p := &platform.Platform{DB: db, Engine: change.NewEngine(db), Home: root, DataRoot: root, Domain: "box.test", Log: slog.Default()}
	applyManifest(t, db, `{"project":"shop","apps":{"web":{"routes":["shop"]},"docs":{"routes":["docs-shop"]}},"services":{"analytics":{}}}`)
	applyManifest(t, db, `{"project":"blog","apps":{"site":{"routes":["blog"]}},"services":{"analytics":{}}}`)
	st, err := OpenSQLite(filepath.Join(root, "analytics.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m := &Module{}
	m.setup(p, st, nil)
	return m
}

// Cloud addresses, requests without Accept-Language and referrer spam are
// bots; homes, loopback and private networks are people.
func TestBotChecks(t *testing.T) {
	ctx := context.Background()
	m := accuracyFixture(t)
	hit := func(ip, ref string, lang bool) Hit {
		return Hit{Project: "shop", App: "web", Kind: "pageview", URL: "https://shop.box.test/", IP: ip, UA: human(1), Referrer: ref, Src: "edge", Lang: lang}
	}
	server := Hit{Project: "shop", App: "web", Kind: "event", Name: "Signup", URL: "/", Src: "server"}
	cloudServer := server
	cloudServer.IP, cloudServer.UA = "3.5.0.1", human(1)
	for name, c := range map[string]struct {
		h    Hit
		want bool
	}{
		"home":                 {hit("81.2.69.142", "", true), true},
		"loopback":             {hit("127.0.0.1", "", true), true},
		"private network":      {hit("192.168.1.20", "", true), true},
		"AWS":                  {hit("3.5.0.1", "", true), false},
		"Hetzner over IPv6":    {hit("2a01:4f8::1", "", true), false},
		"no Accept-Language":   {hit("81.2.69.142", "", false), false},
		"referrer spam":        {hit("81.2.69.142", "https://www.0-0.fr/", true), false},
		"server event":         {server, true},
		"server event, cloud":  {cloudServer, false},
		"script, no language":  {Hit{Project: "shop", App: "web", Kind: "pageview", URL: "https://shop.box.test/a", IP: "81.2.69.142", UA: human(1), Src: "script"}, false},
		"script with language": {Hit{Project: "shop", App: "web", Kind: "pageview", URL: "https://shop.box.test/a", IP: "81.2.69.142", UA: human(1), Src: "script", Lang: true}, true},
	} {
		if ok, why := m.pipe.Add(ctx, c.h); ok != c.want {
			t.Errorf("%s: accepted %v (%s), want %v", name, ok, why, c.want)
		}
	}
	// At the edge: the same page view without Accept-Language is dropped.
	m.handleEdge(ctx, access("shop.box.test", "/", human(2), 200, map[string]string{"Sec-Fetch-Dest": "document", "Sec-Fetch-Mode": "navigate"}))
	m.handleEdge(ctx, access("shop.box.test", "/", human(2), 200, doc))
	if v := m.rt.View("shop", "", time.Now()); v.Pageviews30m != 5 {
		t.Fatalf("%d page views, want 5", v.Pageviews30m)
	}
}

// A person on two apps of a project is one visitor of the project, and
// arriving from another of the project's hosts is not a referral.
func TestProjectVisitorsAndSelfReferrals(t *testing.T) {
	ctx := context.Background()
	m := accuracyFixture(t)
	add := func(project, app, url, ref string) {
		t.Helper()
		if ok, why := m.pipe.Add(ctx, Hit{Project: project, App: app, Kind: "pageview", URL: url, Referrer: ref, IP: "81.2.69.142", UA: human(1), Src: "edge", Lang: true}); !ok {
			t.Fatal(why)
		}
	}
	add("shop", "web", "https://shop.box.test/", "https://blog.box.test/post")
	add("shop", "docs", "https://docs-shop.box.test/guide", "https://shop.box.test/")
	add("shop", "web", "https://shop.box.test/pricing", "https://docs-shop.box.test:8443/guide")
	add("blog", "site", "https://blog.box.test/", "https://shop.box.test/")
	now := time.Now()
	v := m.rt.View("shop", "", now)
	if v.VisitorsNow != 1 || v.Pageviews30m != 3 {
		t.Fatalf("project: %d visitors, %d page views", v.VisitorsNow, v.Pageviews30m)
	}
	if len(v.TopSources) != 1 || v.TopSources[0] != (RTCount{Value: "blog.box.test", Pageviews: 1}) {
		t.Fatalf("shop sources %+v", v.TopSources)
	}
	if d := m.rt.View("shop", "docs", now); d.VisitorsNow != 1 || d.Pageviews30m != 1 {
		t.Fatalf("docs app %+v", d)
	}
	if b := m.rt.View("blog", "", now); len(b.TopSources) != 1 || b.TopSources[0].Value != "shop.box.test" {
		t.Fatalf("another project is a referral: %+v", b.TopSources)
	}
	if err := m.pipe.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	vis, pv, _, err := m.store.Counts(ctx, Query{Project: "shop", From: now.Add(-time.Hour), To: now.Add(time.Hour)})
	if err != nil || vis != 1 || pv != 3 {
		t.Fatalf("stored: %d visitors, %d page views, %v", vis, pv, err)
	}
}

// A visit going on at midnight UTC stays one session under the new day's
// visitor hash; a gap of more than 30 minutes still starts a new one.
func TestSessionsBridgeMidnight(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "analytics.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	pl := testPipeline(st)
	midnight := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	pl.Now = func() time.Time { return midnight.Add(time.Hour) }
	add := func(ua string, at time.Duration) Event {
		t.Helper()
		if ok, why := pl.Add(ctx, Hit{At: midnight.Add(at), Project: "shop", App: "web", Kind: "pageview", URL: "https://shop.box.test/", IP: "81.2.69.142", UA: ua, Src: "edge", Lang: true}); !ok {
			t.Fatal(why)
		}
		return pl.buf[len(pl.buf)-1]
	}
	a1, a2 := add(human(1), -10*time.Minute), add(human(1), 10*time.Minute)
	if a1.Session != a2.Session || a1.Visitor == a2.Visitor {
		t.Fatalf("across midnight: sessions %d %d, visitors %d %d", a1.Session, a2.Session, a1.Visitor, a2.Visitor)
	}
	if a3 := add(human(1), 20*time.Minute); a3.Session != a2.Session || a3.Visitor != a2.Visitor {
		t.Fatalf("after the bridge: %+v", a3)
	}
	b1, b2 := add(human(2), -30*time.Minute), add(human(2), 5*time.Minute)
	if b1.Session == b2.Session {
		t.Fatal("a 35-minute gap kept the session")
	}
}

// Server events without an IP or user agent are a visitor and a visit
// each, not one shared visitor.
func TestAnonymousServerEvents(t *testing.T) {
	pl := testPipeline(&fakeStore{})
	now := time.Now()
	pl.Add(context.Background(), serverHit("shop", now, "/a"))
	pl.Add(context.Background(), serverHit("shop", now, "/b"))
	a, b := pl.buf[0], pl.buf[1]
	if a.Visitor == 0 || a.Session == 0 || a.Visitor == b.Visitor || a.Session == b.Session {
		t.Fatalf("visitors %d %d, sessions %d %d", a.Visitor, b.Visitor, a.Session, b.Session)
	}
}

// A beacon whose Origin is not the page's host was not sent from that page.
func TestBeaconOrigin(t *testing.T) {
	m := accuracyFixture(t)
	coll := httptest.NewServer(m.collectorHandler())
	defer coll.Close()
	send := func(origin string) string {
		req, _ := http.NewRequest("POST", coll.URL+"/e", strings.NewReader(`{"n":"pageview","u":"https://shop.box.test/docs"}`))
		req.Header.Set("User-Agent", human(1))
		req.Header.Set("Accept-Language", "en-GB")
		req.Header.Set("X-Forwarded-For", "81.2.69.142")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.Status + " " + string(b)
	}
	for origin, counted := range map[string]bool{
		"https://evil.example":   false,
		"null":                   false,
		"https://shop.box.test":  true,
		"":                       true, // no Origin, no check
		"https://blog.box.test":  false,
		"https://SHOP.box.test/": true,
	} {
		before := m.pipe.Stats().Accepted
		res := send(origin)
		if got := m.pipe.Stats().Accepted > before; got != counted {
			t.Errorf("Origin %q: counted %v, want %v (%s)", origin, got, counted, res)
		}
	}
}
