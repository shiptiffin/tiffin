package edge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type nextFunc func(w http.ResponseWriter, r *http.Request) error

func (f nextFunc) ServeHTTP(w http.ResponseWriter, r *http.Request) error { return f(w, r) }

func TestFirstVisitCookie(t *testing.T) {
	countries := map[string]string{"203.0.113.1": "US", "203.0.113.2": "DE", "203.0.113.3": ""}
	run := func(mode, ip string, hdr map[string]string) (setCookie, logged string) {
		h := &FirstVisitHandler{Mode: mode, country: func(ip string) string { return countries[ip] }}
		r := httptest.NewRequest(http.MethodGet, "https://shop.example.com/", nil)
		r.RemoteAddr = ip + ":5555"
		r.Header.Set("Sec-Fetch-Dest", "document")
		r.Header.Set(FirstVisitHeader, "spoofed-by-the-client")
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		_ = h.ServeHTTP(w, r, nextFunc(func(w http.ResponseWriter, r *http.Request) error {
			logged = r.Header.Get(FirstVisitHeader)
			return nil
		}))
		return w.Header().Get("Set-Cookie"), logged
	}
	sc, id := run(FirstVisitOutsideEU, "203.0.113.1", nil)
	if !strings.HasPrefix(sc, FirstVisitCookie+"=") || !strings.Contains(sc, "HttpOnly") || !strings.Contains(sc, "Secure") || !strings.Contains(sc, "Max-Age=7776000") || !ValidFirstVisitID(id) || !strings.Contains(sc, id) {
		t.Fatalf("US visitor: Set-Cookie %q, logged %q", sc, id)
	}
	for _, c := range []struct {
		name, mode, ip string
		hdr            map[string]string
	}{
		{"EU visitor", FirstVisitOutsideEU, "203.0.113.2", nil},
		{"unknown country", FirstVisitOutsideEU, "203.0.113.3", nil},
		{"EU visitor who hasn't agreed", FirstVisitAskEU, "203.0.113.2", nil},
		{"Global Privacy Control", FirstVisitOutsideEU, "203.0.113.1", map[string]string{"Sec-GPC": "1"}},
		{"opted out", FirstVisitOutsideEU, "203.0.113.1", map[string]string{"Cookie": ConsentCookie + "=no"}},
		{"not a page load", FirstVisitOutsideEU, "203.0.113.1", map[string]string{"Sec-Fetch-Dest": "image"}},
		{"EU agreed, but outside-eu never asks", FirstVisitOutsideEU, "203.0.113.2", map[string]string{"Cookie": ConsentCookie + "=yes"}},
	} {
		if sc, id := run(c.mode, c.ip, c.hdr); sc != "" || id != "" {
			t.Errorf("%s: Set-Cookie %q, logged %q", c.name, sc, id)
		}
	}
	if sc, id := run(FirstVisitAskEU, "203.0.113.2", map[string]string{"Cookie": ConsentCookie + "=yes"}); sc == "" || id == "" {
		t.Errorf("EU visitor who agreed (ask-eu): Set-Cookie %q, logged %q", sc, id)
	}
	const have = "AAAAAAAAAAAAAAAAAAAAAA"
	if sc, id := run(FirstVisitOutsideEU, "203.0.113.2", map[string]string{"Cookie": FirstVisitCookie + "=" + have}); sc != "" || id != have {
		t.Errorf("an existing cookie is named, not replaced: Set-Cookie %q, logged %q", sc, id)
	}
	if sc, id := run(FirstVisitOutsideEU, "203.0.113.1", map[string]string{"Cookie": FirstVisitCookie + "=" + have + "; " + ConsentCookie + "=no"}); sc != "" || id != "" {
		t.Errorf("an opt-out silences an existing cookie: Set-Cookie %q, logged %q", sc, id)
	}
}

func TestFirstVisitRoutes(t *testing.T) {
	c := Config{Domain: "box.test", FirstVisit: &FirstVisit{Hosts: map[string]string{"shop.example.com": FirstVisitOutsideEU}, GeoPath: "/geo.mmdb"}}
	on := routeFor(c, Route{Host: "shop.example.com", Upstream: "127.0.0.1:3000"}, "")
	off := routeFor(c, Route{Host: "blog.example.com", Upstream: "127.0.0.1:3001"}, "")
	if !strings.Contains(jsonOf(t, on), "tiffin_first_visit") || strings.Contains(jsonOf(t, off), "tiffin_first_visit") {
		t.Fatalf("routes:\n%s\n%s", jsonOf(t, on), jsonOf(t, off))
	}
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
