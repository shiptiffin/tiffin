package vercelcfg

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/shiptiffin/tiffin/internal/edge"
)

func TestSourceRegexp(t *testing.T) {
	for _, tc := range []struct {
		src     string
		match   map[string][]string // path → wanted groups (index 1...)
		noMatch []string
	}{
		{"/(.*)", map[string][]string{"/": {""}, "/a/b": {"a/b"}}, nil},
		{"/assets/(.*)", map[string][]string{"/assets/x.js": {"x.js"}}, []string{"/a/assets/x.js", "/assets"}},
		{"/(models|wasm)/(.*)", map[string][]string{"/wasm/a.wasm": {"wasm", "a.wasm"}}, []string{"/other/a"}},
		{"/sw.js", map[string][]string{"/sw.js": nil}, []string{"/swxjs", "/sw.js/x"}},
		{"/blog/:slug", map[string][]string{"/blog/hello": {"hello"}}, []string{"/blog/a/b", "/blog/"}},
		{"/docs/:path*", map[string][]string{"/docs": {""}, "/docs/a/b": {"a/b"}}, []string{"/docsx"}},
		{"/docs/:path+", map[string][]string{"/docs/a/b": {"a/b"}}, []string{"/docs"}},
		{"/u/:id?", map[string][]string{"/u": {""}, "/u/7": {"7"}}, nil},
		{"/post/:id(\\d+)", map[string][]string{"/post/42": {"42"}}, []string{"/post/abc"}},
		{"/a.b", map[string][]string{"/a.b": nil}, []string{"/axb"}},
	} {
		re, err := SourceRegexp(tc.src)
		if err != nil {
			t.Errorf("%s: %v", tc.src, err)
			continue
		}
		rx := regexp.MustCompile(re)
		for p, groups := range tc.match {
			m := rx.FindStringSubmatch(p)
			if m == nil {
				t.Errorf("%s (%s) does not match %s", tc.src, re, p)
				continue
			}
			if groups != nil && !reflect.DeepEqual(m[1:], groups) {
				t.Errorf("%s on %s: groups %q, want %q", tc.src, p, m[1:], groups)
			}
		}
		for _, p := range tc.noMatch {
			if rx.MatchString(p) {
				t.Errorf("%s (%s) matches %s", tc.src, re, p)
			}
		}
	}
	for _, bad := range []string{"no-slash", "/(unclosed"} {
		if _, err := SourceRegexp(bad); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
}

// The owner's portraitpass vercel.json, plus the rest of what the box reads.
const sample = `{
  "$schema": "https://openapi.vercel.sh/vercel.json",
  "trailingSlash": true,
  "cleanUrls": true,
  "buildCommand": "pnpm build && pnpm pagefind --site dist",
  "installCommand": "pnpm install --frozen-lockfile",
  "outputDirectory": "dist",
  "framework": "vite",
  "regions": ["iad1"],
  "functions": {"api/*.ts": {"memory": 1024}},
  "crons": [
    {"path": "/api/cron/daily-digest", "schedule": "0 5 * * *"},
    {"path": "/api/cron/daily-digest?full=1", "schedule": "0 6 * * 1"},
    {"path": "/api/bad", "schedule": "every day"}
  ],
  "headers": [
    {"source": "/(.*)", "headers": [
      {"key": "Content-Security-Policy", "value": "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'"},
      {"key": "X-Frame-Options", "value": "DENY"},
      {"key": "Cross-Origin-Opener-Policy", "value": "same-origin"}
    ]},
    {"source": "/assets/(.*)", "headers": [{"key": "Cache-Control", "value": "public, max-age=31536000, immutable"}]},
    {"source": "/x", "has": [{"type": "header", "key": "x-a"}], "headers": [{"key": "A", "value": "b"}]}
  ],
  "redirects": [
    {"source": "/old/:slug", "destination": "/new/:slug"},
    {"source": "/tmp", "destination": "https://example.com/tmp", "permanent": false},
    {"source": "/gone", "destination": "/", "statusCode": 301}
  ],
  "rewrites": [
    {"source": "/app/:path*", "destination": "/app.html"},
    {"source": "/api/(.*)", "destination": "https://api.example.com/$1"}
  ]
}`

func TestParse(t *testing.T) {
	c, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if c.BuildCommand != "pnpm build && pnpm pagefind --site dist" || c.InstallCommand != "pnpm install --frozen-lockfile" || c.OutputDirectory != "dist" {
		t.Errorf("build settings: %+v", c)
	}
	wantCrons := []Cron{{"api-cron-daily-digest", "/api/cron/daily-digest", "0 5 * * *"}, {"api-cron-daily-digest-2", "/api/cron/daily-digest?full=1", "0 6 * * 1"}}
	if !reflect.DeepEqual(c.Crons, wantCrons) {
		t.Errorf("crons %+v", c.Crons)
	}
	r := c.Rules
	if r == nil || !r.CleanURLs || r.TrailingSlash == nil || !*r.TrailingSlash {
		t.Fatalf("rules %+v", r)
	}
	if len(r.Headers) != 2 || r.Headers[0].Set["X-Frame-Options"] != "DENY" || r.Headers[1].Source != `^/assets/(.*)$` {
		t.Errorf("headers %+v", r.Headers)
	}
	wantRedirects := []edge.Redirect{
		{Source: `^/old/(?P<slug>[^/]+)$`, Destination: "/new/$slug", Status: 308},
		{Source: `^/tmp$`, Destination: "https://example.com/tmp", Status: 307},
		{Source: `^/gone$`, Destination: "/", Status: 301},
	}
	if !reflect.DeepEqual(r.Redirects, wantRedirects) {
		t.Errorf("redirects %+v", r.Redirects)
	}
	if len(r.Rewrites) != 1 || r.Rewrites[0].Destination != "/app.html" {
		t.Errorf("rewrites %+v", r.Rewrites)
	}
	ignored := strings.Join(c.Ignored, "; ")
	for _, want := range []string{"framework", "regions", "functions", "crons[2]", "headers[2] (has/missing", "rewrites[1] (rewrites to another site"} {
		if !strings.Contains(ignored, want) {
			t.Errorf("ignored %q lacks %q", ignored, want)
		}
	}
	if strings.Contains(ignored, "$schema") {
		t.Errorf("$schema listed as ignored: %q", ignored)
	}
	sum := c.Summary()
	if sum != `buildCommand "pnpm build && pnpm pagefind --site dist", installCommand "pnpm install --frozen-lockfile", outputDirectory "dist", 2 crons, 2 header rules, 3 redirects, 1 rewrite, cleanUrls, trailingSlash true` {
		t.Errorf("summary %q", sum)
	}
	// The rules are valid for the edge.
	cfg := edge.Config{Domain: "tiffin.localhost", Upstream: "127.0.0.1:1", DataDir: "/x", Internal: true,
		Routes: []edge.Route{{Host: "a.tiffin.localhost", FileRoot: "/srv", Rules: r}}}
	if _, err := edge.ConfigJSON(cfg); err != nil {
		t.Errorf("edge refuses the rules: %v", err)
	}

	c.ForContainer()
	if c.OutputDirectory != "" || c.Rules.Rewrites != nil || c.Rules.CleanURLs || c.Rules.TrailingSlash != nil || len(c.Rules.Headers) != 2 {
		t.Errorf("for a container: %+v %+v", c, c.Rules)
	}
	if !strings.Contains(strings.Join(c.Ignored, "; "), "rewrites, cleanUrls, trailingSlash (static sites only") {
		t.Errorf("ignored %q", c.Ignored)
	}
}

func TestParseErrors(t *testing.T) {
	for _, bad := range []string{`{`, `{"buildCommand": 1}`, `{"outputDirectory": "../x"}`, `{"crons": {}}`, `{"trailingSlash": "yes"}`} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
	c, err := Parse([]byte(`{"framework": null}`))
	if err != nil || c.Summary() != "" || c.Rules != nil || len(c.Ignored) != 0 {
		t.Errorf("empty: %+v %v", c, err)
	}
}

func TestCronName(t *testing.T) {
	for in, want := range map[string]string{
		"/api/cron/Daily_Digest":              "api-cron-daily-digest",
		"/":                                   "root",
		"/2fa/cleanup":                        "cron-2fa-cleanup",
		"/a?b=c":                              "a",
		"/" + strings.Repeat("abcdefghij", 5): "abcdefghijabcdefghijabcdefghijabcdefghij",
	} {
		if got := CronName(in); got != want {
			t.Errorf("CronName(%q) = %q, want %q", in, got, want)
		}
	}
}
