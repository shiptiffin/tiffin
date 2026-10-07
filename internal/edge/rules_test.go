package edge

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestSiteRules(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	for name, body := range map[string]string{
		"index.html":                "home",
		"about.html":                "about",
		"docs/index.html":           "docs",
		"404.html":                  "custom not found",
		"app.html":                  "app shell",
		"assets/index-B1x9Qa2c.js":  "js",
		"new/hello/index.html":      "new hello",
		"blog/post/index.html":      "post",
		"wasm/engine-0000ffff.wasm": "wasm",
	} {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	plain := t.TempDir() // no 404.html
	if err := os.WriteFile(filepath.Join(plain, "index.html"), []byte("plain"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Cache-Control", "private")
		io.WriteString(w, "app "+r.URL.Path)
	}))
	t.Cleanup(app.Close)
	no := false
	rules := &Rules{
		Headers: []HeaderRule{
			{Source: `^/(.*)$`, Set: map[string]string{"Content-Security-Policy": "script-src 'self' 'wasm-unsafe-eval'", "X-Frame-Options": "SAMEORIGIN", "X-Json": `{"a":1}`}},
			{Source: `^/assets/(.*)$`, Set: map[string]string{"Cache-Control": "public, max-age=60"}},
			{Source: `^/(models|wasm)/(.*)$`, Set: map[string]string{"Cache-Control": "public, max-age=604800"}},
		},
		Redirects: []Redirect{{Source: `^/old/(?P<slug>[^/]+)$`, Destination: "/new/$slug/", Status: 307}},
		Rewrites:  []Rewrite{{Source: `^/app/(.*)$`, Destination: "/app.html"}},
		CleanURLs: true,
	}
	cfg := testConfig(t, addr(upstreamServer(t, "platform")))
	cfg.Routes = []Route{
		{Host: "site.tiffin.localhost", FileRoot: root, Rules: rules},
		// A site mounted at a path: its files sit at the root of its folder.
		{Host: "mount.tiffin.localhost", PathPrefix: "/site", FileRoot: root, Rules: &Rules{CleanURLs: true}},
		{Host: "slash.tiffin.localhost", FileRoot: root, Rules: &Rules{TrailingSlash: &no}},
		{Host: "plain.tiffin.localhost", FileRoot: plain},
		{Host: "app.tiffin.localhost", Upstream: addr(app), Rules: &Rules{
			Headers:   []HeaderRule{{Source: `^/(.*)$`, Set: map[string]string{"X-From-Config": "yes", "Cache-Control": "no-store"}}},
			Redirects: []Redirect{{Source: `^/go$`, Destination: "https://example.com/", Status: 308}},
		}},
	}
	c := startEdge(t, cfg)
	port := ":" + strconv.Itoa(cfg.HTTPSPort)
	u := func(host, p string) string { return "https://" + host + ".tiffin.localhost" + port + p }
	const csp = "script-src 'self' 'wasm-unsafe-eval'"
	for _, tc := range []struct {
		url        string
		code       int
		body, loc  string
		hdr, value string
	}{
		{u("site", "/"), 200, "home", "", "Content-Security-Policy", csp},
		{u("site", "/about"), 200, "about", "", "X-Frame-Options", "SAMEORIGIN"},
		{u("site", "/about.html"), 308, "", "/about", "", ""},
		{u("site", "/about.html?x=1"), 308, "", "/about?x=1", "", ""},
		{u("site", "/docs/index.html"), 308, "", "/docs/", "", ""},
		{u("site", "/docs"), 308, "", "/docs/", "", ""}, // the file server's canonical folder URL
		{u("site", "/docs/"), 200, "docs", "", "Cache-Control", "no-cache"},
		{u("site", "/nope"), 404, "custom not found", "", "X-Content-Type-Options", "nosniff"},
		{u("site", "/assets/index-B1x9Qa2c.js"), 200, "js", "", "Cache-Control", "public, max-age=60"},
		{u("site", "/wasm/engine-0000ffff.wasm"), 200, "wasm", "", "Cache-Control", "public, max-age=604800"},
		{u("site", "/old/hello"), 307, "", "/new/hello/", "", ""},
		{u("site", "/old/hello?utm=1"), 307, "", "/new/hello/?utm=1", "", ""},
		{u("site", "/app/settings"), 200, "app shell", "", "", ""},
		{u("site", "/"), 200, "home", "", "X-Json", `{"a":1}`},
		{u("slash", "/docs"), 200, "docs", "", "", ""},
		{u("slash", "/docs/"), 308, "", "/docs", "", ""},
		{u("slash", "/about"), 200, "about", "", "X-Frame-Options", "DENY"},
		{u("plain", "/nope"), 404, "", "", "", ""},
		{u("plain", "/"), 200, "plain", "", "Content-Security-Policy", cspValue},
		{u("mount", "/site"), 200, "home", "", "", ""},
		{u("mount", "/site/"), 200, "home", "", "", ""},
		{u("mount", "/site/about"), 200, "about", "", "", ""},
		{u("mount", "/site/assets/index-B1x9Qa2c.js"), 200, "js", "", "", ""},
		{u("mount", "/site/docs"), 308, "", "/site/docs/", "", ""},
		{u("mount", "/site/docs/"), 200, "docs", "", "", ""},
		{u("mount", "/site/nope"), 404, "custom not found", "", "", ""},
		{u("mount", "/site/about.html"), 308, "", "/site/about", "", ""},
		{u("app", "/x"), 200, "app /x", "", "X-Frame-Options", "SAMEORIGIN"},
		{u("app", "/x"), 200, "app /x", "", "X-From-Config", "yes"},
		{u("app", "/x"), 200, "app /x", "", "Cache-Control", "no-store"},
		{u("app", "/go"), 308, "", "https://example.com/", "", ""},
	} {
		resp, body := fetch(t, c, tc.url, "")
		if resp.StatusCode != tc.code || (tc.body != "" && body != tc.body) || resp.Header.Get("Location") != tc.loc ||
			(tc.hdr != "" && resp.Header.Get(tc.hdr) != tc.value) {
			t.Errorf("%s: %d %q Location %q %s %q; want %d %q %q %q", tc.url, resp.StatusCode, body, resp.Header.Get("Location"),
				tc.hdr, resp.Header.Get(tc.hdr), tc.code, tc.body, tc.loc, tc.value)
		}
	}
}

func TestRulesValidation(t *testing.T) {
	base := Config{Domain: "tiffin.localhost", Upstream: "127.0.0.1:7070", DataDir: "/x", Internal: true}
	for name, r := range map[string]*Rules{
		"bad regexp":    {Headers: []HeaderRule{{Source: "(", Set: map[string]string{"a": "b"}}}},
		"bad status":    {Redirects: []Redirect{{Source: "^/a$", Destination: "/b", Status: 200}}},
		"rewrite a URL": {Rewrites: []Rewrite{{Source: "^/a$", Destination: "https://example.com/"}}},
	} {
		c := base
		c.Routes = []Route{{Host: "a.tiffin.localhost", FileRoot: "/srv", Rules: r}}
		if _, err := ConfigJSON(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
