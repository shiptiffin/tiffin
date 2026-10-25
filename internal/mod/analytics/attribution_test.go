package analytics

import (
	"context"
	"strings"
	"testing"
	"time"
)

const testUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"

func browserHit(at time.Time, app, url, ref string) Hit {
	return Hit{At: at, Project: "shop", App: app, Kind: "pageview", Name: "pageview", URL: url, Referrer: ref,
		IP: "203.0.113.7", UA: testUA, Src: "edge", Lang: true}
}

func sqlitePipeline(t *testing.T, set Settings) (*Pipeline, Store) {
	t.Helper()
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	pl := testPipeline(st)
	pl.Settings = func(context.Context, string) Settings { return set }
	return pl, st
}

// Basic analytics keeps no campaign tags: the visit's source is its referrer.
func TestBasicDropsCampaignTags(t *testing.T) {
	ctx := context.Background()
	pl, _ := sqlitePipeline(t, Settings{})
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	if ok, why := pl.Add(ctx, browserHit(at, "web", "https://shop.box.test/?utm_source=launch&utm_campaign=oct&ref=pal", "https://news.ycombinator.com/item?id=1")); !ok {
		t.Fatal(why)
	}
	ev := pl.buf[0]
	if ev.UTMSource != "" || ev.UTMCamp != "" || ev.RefSource != "Hacker News" {
		t.Fatalf("basic event %+v", ev)
	}
	pl2, _ := sqlitePipeline(t, Settings{Attribution: true, Campaigns: true})
	pl2.Add(ctx, browserHit(at, "web", "https://shop.box.test/?utm_source=launch&utm_campaign=oct", ""))
	if ev := pl2.buf[0]; ev.UTMSource != "launch" || ev.UTMCamp != "oct" || ev.RefSource != "launch" {
		t.Fatalf("attribution event %+v", ev)
	}
}

// A sign-up joins the visit going on for the same person, on any app of
// the project, and gets where that visit came from.
func TestVisitLookup(t *testing.T) {
	ctx := context.Background()
	pl, _ := sqlitePipeline(t, Settings{Attribution: true, Campaigns: true})
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	pl.Now = func() time.Time { return at }
	pl.Add(ctx, browserHit(at, "web", "https://shop.box.test/pricing?utm_campaign=launch", "https://news.ycombinator.com/"))
	pl.Add(ctx, browserHit(at.Add(5*time.Minute), "web", "https://shop.box.test/docs", "https://shop.box.test/pricing"))
	pl.Add(ctx, browserHit(at.Add(8*time.Minute), "app", "https://app.shop.test/welcome", "https://shop.box.test/docs"))

	// The app visit is the latest, and it carries on the web visit's source.
	tc, ok := pl.Visit(ctx, "shop", "203.0.113.7", testUA, at.Add(9*time.Minute))
	if !ok || tc.Source != "Hacker News" || tc.RefHost != "news.ycombinator.com" || tc.UTMCampaign != "launch" || tc.Landing != "/pricing" {
		t.Fatalf("visit %+v %v", tc, ok)
	}
	if _, ok := pl.Visit(ctx, "shop", "198.51.100.1", testUA, at.Add(9*time.Minute)); ok {
		t.Fatal("another person's sign-up joined this visit")
	}
	if _, ok := pl.Visit(ctx, "shop", "203.0.113.7", testUA, at.Add(40*time.Minute)); ok {
		t.Fatal("a visit idle for half an hour is over")
	}
	if _, ok := pl.Visit(ctx, "other", "203.0.113.7", testUA, at.Add(9*time.Minute)); ok {
		t.Fatal("visits don't cross projects")
	}
}

// Coming back from Stripe Checkout keeps the visit's source (the checkout
// page is not a referral) and notes the checkout session for the payment.
func TestCheckoutReturn(t *testing.T) {
	ctx := context.Background()
	pl, _ := sqlitePipeline(t, Settings{Attribution: true, Campaigns: true})
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	type note struct {
		id string
		t  Touch
	}
	var got []note
	pl.Checkout = func(_ string, id string, tc Touch, _ time.Time) { got = append(got, note{id, tc}) }
	pl.Add(ctx, browserHit(at, "web", "https://shop.box.test/", "https://www.reddit.com/r/selfhosted"))
	pl.Add(ctx, browserHit(at.Add(3*time.Minute), "web", "https://shop.box.test/thanks?session_id=cs_test_a1b2", "https://checkout.stripe.com/c/pay/cs_test_a1b2"))
	if len(got) != 1 || got[0].id != "cs_test_a1b2" || got[0].t.Source != "Reddit" || got[0].t.Landing != "/" {
		t.Fatalf("checkout notes %+v", got)
	}
	last := pl.buf[len(pl.buf)-1]
	if last.Path != "/thanks" || last.RefSource != "" || last.Session != pl.buf[0].Session {
		t.Fatalf("return page %+v", last)
	}

	// Without attribution nothing is noted.
	pl2, _ := sqlitePipeline(t, Settings{})
	pl2.Checkout = pl.Checkout
	pl2.Add(ctx, browserHit(at, "web", "https://shop.box.test/thanks?session_id=cs_test_zz", ""))
	if len(got) != 1 {
		t.Fatalf("basic analytics noted a checkout: %+v", got)
	}
}

func TestAttrStore(t *testing.T) {
	ctx := context.Background()
	s, err := openAttrStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	first := &Touch{At: at.Add(-72 * time.Hour), Source: "Hacker News", Landing: "/"}
	if err := s.putSignup(ctx, "shop", Signup{UserID: "u1", At: at, Matched: "visit", Visit: Touch{Session: 9, Source: "Google", Landing: "/pricing"}, First: first}); err != nil {
		t.Fatal(err)
	}
	// A second sign-up event for the same account keeps the first.
	_ = s.putSignup(ctx, "shop", Signup{UserID: "u1", At: at.Add(time.Hour), Matched: "none"})
	su, err := s.signup(ctx, "shop", "u1")
	if err != nil || su == nil || su.Matched != "visit" || su.Visit.Source != "Google" || su.First == nil || su.First.Source != "Hacker News" || !su.First.At.Equal(first.At) {
		t.Fatalf("signup %+v %v", su, err)
	}
	_ = s.putCheckout(ctx, "shop", "cs_test_1", Touch{Session: 9, Source: "Google"}, at)
	if tc, ok, err := s.checkout(ctx, "shop", "cs_test_1"); !ok || err != nil || tc.Source != "Google" {
		t.Fatalf("checkout %+v %v %v", tc, ok, err)
	}
	_ = s.putFirstVisit(ctx, "shop", "fv1", Touch{At: at, Source: "Bluesky"})
	if fv, _ := s.firstVisit(ctx, "shop", "fv1"); fv == nil || fv.Source != "Bluesky" {
		t.Fatalf("first visit %+v", fv)
	}
	rows, _ := s.projectRows(ctx, "shop")
	if rows["signups"] != 1 || rows["checkouts"] != 1 || rows["first_visits"] != 1 {
		t.Fatalf("rows %v", rows)
	}
	if err := s.purge(ctx, at.Add(100*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.projectRows(ctx, "shop"); rows["signups"] != 1 || rows["checkouts"] != 0 || rows["first_visits"] != 0 {
		t.Fatalf("after purge %v", rows)
	}
	if err := s.deleteProject(ctx, "shop"); err != nil {
		t.Fatal(err)
	}
	if su, _ := s.signup(ctx, "shop", "u1"); su != nil {
		t.Fatal("deleteProject kept a sign-up")
	}
}

func TestPrivacyText(t *testing.T) {
	basic := privacyText(Settings{FirstVisitCookie: "off"}, false)
	if strings.Contains(basic, "campaign") || strings.Contains(basic, "tiffin_first_visit") || !strings.Contains(basic, "no cookies") {
		t.Fatalf("basic text:\n%s", basic)
	}
	full := privacyText(Settings{Attribution: true, Campaigns: true, Signups: true, FirstVisitCookie: "ask-eu"}, true)
	for _, want := range []string{"utm_source", "saved with your account", "when you pay", "Article 6(1)(f)", "tiffin_first_visit", "unless they agree", "/_tiffin/opt-out"} {
		if !strings.Contains(full, want) {
			t.Errorf("full text lacks %q:\n%s", want, full)
		}
	}
	if noCookie := privacyText(Settings{Attribution: true, Campaigns: true, Signups: true, FirstVisitCookie: "off"}, false); strings.Contains(noCookie, "/_tiffin/opt-out") || strings.Contains(noCookie, "when you pay") {
		t.Fatalf("no-cookie text:\n%s", noCookie)
	}
}
