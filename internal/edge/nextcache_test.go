package edge

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

// Next.js's CDN-only Cache-Control becomes what browsers get on Vercel; a
// header the app wrote itself, and other apps' headers, pass unchanged.
func TestNextCacheControl(t *testing.T) {
	isolate(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", r.URL.Query().Get("cc"))
		w.Header().Set("Content-Type", "text/html")
	}))
	t.Cleanup(up.Close)
	cfg := testConfig(t, addr(up))
	cfg.Routes = []Route{
		{Host: "next.tiffin.localhost", Upstream: addr(up), NextCache: true},
		{Host: "bun.tiffin.localhost", Upstream: addr(up)},
	}
	c := startEdge(t, cfg)
	port := ":" + strconv.Itoa(cfg.HTTPSPort)
	const vercel = "public, max-age=0, must-revalidate"
	for _, tc := range []struct{ host, cc, want string }{
		{"next", "s-maxage=31536000", vercel},
		{"next", "s-maxage=60, stale-while-revalidate=31535940", vercel},
		{"next", "s-maxage=60, stale-while-revalidate", vercel},
		{"next", "public, max-age=3600, s-maxage=86400", "public, max-age=3600, s-maxage=86400"},
		{"next", "private, no-cache, no-store, max-age=0, must-revalidate", "private, no-cache, no-store, max-age=0, must-revalidate"},
		{"bun", "s-maxage=31536000", "s-maxage=31536000"},
	} {
		resp, _ := fetch(t, c, "https://"+tc.host+".tiffin.localhost"+port+"/?cc="+url.QueryEscape(tc.cc), "")
		if got := resp.Header.Get("Cache-Control"); got != tc.want {
			t.Errorf("%s with %q: Cache-Control %q, want %q", tc.host, tc.cc, got, tc.want)
		}
	}
}
