package enrich

import (
	"os"
	"testing"
)

func TestBots(t *testing.T) {
	b := NewBots()
	bots := []string{
		"Googlebot/2.1 (+http://www.google.com/bot.html)",
		"Mozilla/5.0 (compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm)",
		"curl/8.5.0",
		"python-requests/2.31.0",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/120.0.0.0 Safari/537.36",
		"facebookexternalhit/1.1 (+http://www.facebook.com/externalhit_uatext.php)",
		"",
	}
	humans := []string{
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:131.0) Gecko/20100101 Firefox/131.0",
		"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Mobile Safari/537.36",
	}
	for _, ua := range bots {
		if !b.IsBot(ua) {
			t.Errorf("want bot: %q", ua)
		}
	}
	for _, ua := range humans {
		if b.IsBot(ua) {
			t.Errorf("want human: %q", ua)
		}
	}
}

func TestAgents(t *testing.T) {
	a := NewAgents()
	got := a.Parse("Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1")
	if got.Browser != "Mobile Safari" || got.OS != "iOS" || got.Device != "mobile" {
		t.Fatalf("%+v", got)
	}
	got = a.Parse("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36")
	if got.Browser != "Chrome" || got.OS != "Mac OS X" || got.Device != "desktop" {
		t.Fatalf("%+v", got)
	}
}

func TestPageAndReferrer(t *testing.T) {
	p := ParsePage("https://Shop.Example.com:8443/pricing?utm_source=newsletter&utm_medium=email&token=secret#x", "")
	if p.Host != "shop.example.com" || p.Path != "/pricing" || p.UTM["utm_source"] != "newsletter" || p.RawQuery != "utm_medium=email&utm_source=newsletter" {
		t.Fatalf("%+v", p)
	}
	if s, h := Referrer("https://www.google.co.uk/search?q=x", "shop.example.com", nil); s != "Google" || h != "google.co.uk" {
		t.Fatal(s, h)
	}
	if s, h := Referrer("https://shop.example.com/other", "shop.example.com", nil); s != "" || h != "" {
		t.Fatal("internal referrer must be direct", s, h)
	}
	if s, _ := Referrer("https://t.co/abc", "a.com", nil); s != "X (Twitter)" {
		t.Fatal(s)
	}
	if s, _ := Referrer("", "a.com", map[string]string{"utm_source": "launch"}); s != "launch" {
		t.Fatal(s)
	}
	for in, want := range map[string]string{
		"/confirm/ada@example.com":        "/confirm/[email]",
		"/u/ada%40example.co.uk/settings": "/u/[email]/settings",
		"/@ada":                           "/@ada",
		"/@ada.dev":                       "/@ada.dev",
	} {
		if p := ParsePage("https://a.com"+in, ""); p.Path != want {
			t.Errorf("%s → %s, want %s", in, p.Path, want)
		}
	}
	if s := StripQuery("https://user:pw@partner.example/x?invite=1#t"); s != "https://partner.example/x" {
		t.Fatal(s)
	}
}

func TestGeo(t *testing.T) {
	path := os.Getenv("TIFFIN_TEST_MMDB")
	if path == "" {
		t.Skip("set TIFFIN_TEST_MMDB to a DB-IP country mmdb")
	}
	g, err := OpenGeo(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if c := g.Country("8.8.8.8"); c != "US" {
		t.Fatalf("8.8.8.8 → %q", c)
	}
	if c := g.Country("2a00:1450:4001:80b::200e"); c == "" {
		t.Fatal("ipv6 lookup failed")
	}
	if c := g.Country("127.0.0.1"); c != "" && c != "ZZ" {
		t.Fatalf("loopback → %q", c)
	}
}
