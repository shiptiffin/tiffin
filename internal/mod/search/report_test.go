package search

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/platform"
)

func TestReport(t *testing.T) {
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
	h.m.crawl.lists = parseLists(map[string][]string{listGoogle: {"66.249.64.0/19"}, listChatGPT: {"23.98.142.176/28"}})
	h.s.set("shop.example.com", "/sitemap.xml", sitemap(map[string]string{"https://shop.example.com/": "2026-10-01", "https://shop.example.com/pricing": "2026-10-02"}))
	h.serve("shop.example.com", "d1")
	h.serve("shop.shiptiffin.app", "d1")
	report := func(in reportIn) Report {
		t.Helper()
		in.Project = "shop"
		r, err := h.m.report(h.ctx, &in)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	// Before Google: IndexNow only, and the hidden name listed with why.
	h.pass()
	r := report(reportIn{})
	if !r.IndexNow.On || r.IndexNow.Last == nil || r.IndexNow.Pages != 2 || r.Google.State != "not_connected" || r.Searches != nil {
		t.Fatalf("before google %+v", r)
	}
	if len(r.Hosts) != 2 || r.Hosts[1].Host != "shop.shiptiffin.app" || r.Hosts[1].Findable || r.Hosts[1].Why == "" {
		t.Fatalf("hosts %+v", r.Hosts)
	}
	if v, err := h.m.indexNowView(h.ctx); err != nil || !v.On || len(v.Addresses) != 1 || v.Addresses[0].Last == nil || v.Addresses[0].Last.Pages != 2 {
		t.Fatalf("indexnow %+v %v", v, err)
	}

	// Connected and verified: the first pull fills 16 months back.
	if _, err := h.m.connectGoogle(h.ctx, testKey(t), "", "t"); err != nil {
		t.Fatal(err)
	}
	h.pass()
	h.now = h.now.Add(20 * time.Second)
	h.pass()
	d := h.m.gdataFor(h.ctx, "shop.example.com")
	if d == nil || !d.HistDone || d.HistFrom > "2025-06-26" {
		t.Fatalf("history %+v", d)
	}
	r = report(reportIn{})
	s := r.Searches
	if r.Google.State != GVerified || r.Google.Filling || s == nil {
		t.Fatalf("verified %+v", r.Google)
	}
	// A period ends on Google's last day (the 21st), not today (the 24th).
	if r.To != "2026-10-21" || r.From != "2026-09-22" || r.PrevTo != "2026-09-21" || r.CrawlTo != "2026-10-24" || s.Through != "2026-10-21" {
		t.Fatalf("days %s..%s before %s..%s crawl ..%s", r.From, r.To, r.PrevFrom, r.PrevTo, r.CrawlTo)
	}
	if s.Totals.Clicks != 4 || s.Totals.Impressions != 100 || s.Totals.Position != 10.4 || s.Totals.CTR != 0.04 || len(s.Daily) != 2 || s.Previous.Clicks != 0 {
		t.Fatalf("totals %+v daily %+v", s.Totals, s.Daily)
	}
	if len(s.Queries) != 2 || s.Queries[0].Key != "self hosted vercel" || len(s.Pages) != 2 || s.Pages[0].Key != "/" || s.Pages[1].Key != "/pricing" {
		t.Fatalf("queries %+v pages %+v", s.Queries, s.Pages)
	}
	// Custom days: only the 21st.
	r = report(reportIn{From: "2026-10-21", To: "2026-10-21"})
	if r.Searches.Totals.Clicks != 1 || r.Searches.Previous.Clicks != 3 || len(r.Searches.Queries) != 1 || r.Searches.Queries[0].Key != "tiffin box" {
		t.Fatalf("one day %+v", r.Searches)
	}
	// What moved against the 20th: "tiffin box" and /pricing came, the rest fell.
	mv := r.Searches.Moved
	if mv == nil || len(mv.QueriesUp) != 1 || mv.QueriesUp[0].Key != "tiffin box" || mv.QueriesUp[0].PrevClicks != 0 ||
		len(mv.QueriesDown) != 1 || mv.QueriesDown[0].Key != "self hosted vercel" || mv.QueriesDown[0].Clicks != 0 || mv.QueriesDown[0].PrevClicks != 3 ||
		len(mv.PagesUp) != 1 || mv.PagesUp[0].Key != "/pricing" || len(mv.PagesDown) != 1 || mv.PagesDown[0].Key != "/" {
		t.Fatalf("moved %+v", mv)
	}
	// Nothing the days before: nothing to compare.
	if r = report(reportIn{From: "2026-10-20", To: "2026-10-20"}); r.Searches.Moved != nil {
		t.Fatalf("moved with no days before %+v", r.Searches.Moved)
	}
	if _, err := h.m.report(h.ctx, &reportIn{Project: "shop", From: "2026-10-22", To: "2026-10-21"}); err == nil {
		t.Fatal("days the wrong way round were taken")
	}
	if _, err := h.m.report(h.ctx, &reportIn{Project: "shop", Host: "shop.shiptiffin.app"}); err == nil {
		t.Fatal("a hidden host was reported")
	}

	// Later pulls read only the last days again.
	n := len(fg.queries)
	h.now = h.now.Add(gPullEvery)
	h.pass()
	if hist := strings.Count(strings.Join(fg.queries[n:], " "), "date,query"); hist != 1 {
		t.Fatalf("history queries after the fill: %v", fg.queries[n:])
	}

	// Crawlers: kept by day, search engines and AI apart, fakes counted.
	gb := "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"
	at := h.now.Add(-time.Hour)
	for _, l := range [][]byte{
		accessLine(at, "66.249.66.1", "shop.example.com", "/", gb, 200),
		accessLine(at.Add(time.Minute), "66.249.66.1", "shop.example.com", "/gone", gb, 404),
		accessLine(at, "203.0.113.9", "shop.example.com", "/", gb, 200),
		accessLine(at, "23.98.142.180", "shop.example.com", "/pricing", "Mozilla/5.0; compatible; ChatGPT-User/1.0", 200),
	} {
		h.m.crawlLine(h.ctx, l)
	}
	h.pass()
	r = report(reportIn{Period: "7d"})
	if len(r.Crawlers) != 2 || r.Crawlers[0].Bot != "Googlebot" || r.Crawlers[0].Hits != 2 || r.Crawlers[0].Errors != 1 || r.Crawlers[0].AI || r.Crawlers[0].LastPath != "/gone" {
		t.Fatalf("crawlers %+v", r.Crawlers)
	}
	if c := r.Crawlers[1]; c.Bot != "ChatGPT-User" || !c.AI || c.Purpose != "user" {
		t.Fatalf("chatgpt %+v", c)
	}
	if r.Fakes != 1 || len(r.CrawlDays) != 1 || r.CrawlDays[0].Search != 2 || r.CrawlDays[0].AI != 1 {
		t.Fatalf("fakes %d days %+v", r.Fakes, r.CrawlDays)
	}

	// Disconnecting drops Google's numbers, not the crawler log's.
	if err := h.m.disconnectGoogle(h.ctx); err != nil {
		t.Fatal(err)
	}
	r = report(reportIn{Period: "7d"})
	if r.Searches != nil || r.Google.First != "" || len(r.Crawlers) != 2 {
		t.Fatalf("after disconnect %+v", r)
	}
	// Counts saved before the store existed are copied in at start.
	h.m.hist.forget(h.ctx, "shop.example.com", true)
	h.m.seedCrawl(h.ctx)
	if r = report(reportIn{Period: "7d"}); len(r.Crawlers) != 2 || r.Crawlers[0].Hits != 2 || r.Fakes != 1 {
		t.Fatalf("seeded %+v fakes %d", r.Crawlers, r.Fakes)
	}
	// A host gone for good takes its history with it.
	h.m.forget(context.Background(), "shop.example.com")
	if days, _ := h.m.hist.crawlDays(h.ctx, []string{"shop.example.com"}, "2026-01-01", "2026-12-31"); len(days) != 0 {
		t.Fatalf("crawl history after forget %+v", days)
	}
}

func TestMovers(t *testing.T) {
	now := []GTop{{Key: "a", Clicks: 5, Impressions: 50}, {Key: "b", Clicks: 2, Impressions: 300}, {Key: "c", Clicks: 2, Impressions: 40}, {Key: "new", Clicks: 0, Impressions: 9}}
	before := []GTop{{Key: "a", Clicks: 1, Impressions: 20}, {Key: "b", Clicks: 2, Impressions: 100}, {Key: "c", Clicks: 4, Impressions: 40}, {Key: "gone", Clicks: 1, Impressions: 5}}
	up, down := movers(now, before)
	var u, d []string
	for _, x := range up {
		u = append(u, x.Key)
	}
	for _, x := range down {
		d = append(d, x.Key)
	}
	// Clicks first (a +4), then impressions (b +200 at the same clicks, new +9).
	if strings.Join(u, ",") != "a,b,new" || strings.Join(d, ",") != "c,gone" {
		t.Fatalf("up %v down %v", u, d)
	}
}
