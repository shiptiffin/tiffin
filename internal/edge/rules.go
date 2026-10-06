package edge

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Rules are a site's own redirects, rewrites and response headers (from
// its vercel.json), applied at the edge. Sources are Go regular
// expressions matched against the whole request path; a destination refers
// to their groups as $1 or $name.
type Rules struct {
	// Headers are set on every response whose path matches, after the
	// app or file server answered, so they win over its own. A later rule
	// wins over an earlier one for the same header.
	Headers []HeaderRule `json:"headers,omitempty"`
	// Redirects answer before anything is served; the first match wins.
	// The query string is kept.
	Redirects []Redirect `json:"redirects,omitempty"`
	// Rewrites serve another path of a static site when no file matches the
	// request; the first match wins. Ignored for apps.
	Rewrites []Rewrite `json:"rewrites,omitempty"`
	// CleanURLs redirects /page.html to /page (static sites).
	CleanURLs bool `json:"cleanUrls,omitempty"`
	// TrailingSlash, when set, redirects every path that does not name a
	// file to its form with (true) or without (false) a trailing slash
	// (static sites).
	TrailingSlash *bool `json:"trailingSlash,omitempty"`
}

// HeaderRule sets headers on responses to matching paths.
type HeaderRule struct {
	Source string            `json:"source"`
	Set    map[string]string `json:"set"`
}

// Redirect sends matching paths elsewhere with Status (301, 302, 303, 307
// or 308).
type Redirect struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Status      int    `json:"status"`
}

// Rewrite serves Destination (a path on the same site) for matching paths.
type Rewrite struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

// Validate checks the rules the way the edge does before it loads them.
func (r *Rules) Validate() error {
	if r == nil {
		return nil
	}
	check := func(what, src string) error {
		if _, err := regexp.Compile(src); err != nil {
			return fmt.Errorf("%s source %q: %w", what, src, err)
		}
		return nil
	}
	for _, h := range r.Headers {
		if err := check("header", h.Source); err != nil {
			return err
		}
	}
	for _, d := range r.Redirects {
		if err := check("redirect", d.Source); err != nil {
			return err
		}
		if d.Status < 300 || d.Status > 399 || d.Destination == "" {
			return fmt.Errorf("redirect %q: needs a destination and a 3xx status", d.Source)
		}
	}
	for _, w := range r.Rewrites {
		if err := check("rewrite", w.Source); err != nil {
			return err
		}
		if !strings.HasPrefix(w.Destination, "/") {
			return fmt.Errorf("rewrite %q: the destination must be a path", w.Source)
		}
	}
	return nil
}

var groupRef = regexp.MustCompile(`\$(\d+|[A-Za-z_][A-Za-z0-9_]*)`)

// placeholders escapes Caddy's braces in s and turns $1 and $name into the
// placeholders of the path_regexp matcher named m.
func placeholders(s, m string) string {
	s = strings.NewReplacer(`{`, `\{`, `}`, `\}`).Replace(s)
	if m == "" {
		return s
	}
	return groupRef.ReplaceAllStringFunc(s, func(ref string) string {
		return "{http.regexp." + m + "." + ref[1:] + "}"
	})
}

func pathRE(name, pattern string) obj {
	return obj{"path_regexp": obj{"name": name, "pattern": pattern}}
}

// headerHandlers sets each rule's headers on matching responses. Deferred
// handlers that run first have the last word, so the rules go in reverse:
// a later rule wins over an earlier one.
func (r *Rules) headerHandlers() []obj {
	if r == nil || len(r.Headers) == 0 {
		return nil
	}
	var routes []obj
	for i := len(r.Headers) - 1; i >= 0; i-- {
		h := r.Headers[i]
		set := obj{}
		for k, v := range h.Set {
			set[k] = []string{placeholders(v, "")}
		}
		routes = append(routes, obj{
			"match":  []obj{pathRE("h"+strconv.Itoa(i), h.Source)},
			"handle": []obj{{"handler": "headers", "response": obj{"set": set, "deferred": true}}},
		})
	}
	return []obj{{"handler": "subroute", "routes": routes}}
}

// redirectHandlers answers the first matching redirect: the site's
// cleanUrls and trailingSlash ones first (static sites), then its own.
func (r *Rules) redirectHandlers(static bool) []obj {
	if r == nil {
		return nil
	}
	list := []Redirect{}
	if static && r.CleanURLs {
		tail := ""
		if r.TrailingSlash != nil && *r.TrailingSlash {
			tail = "/"
		}
		list = append(list,
			Redirect{Source: `^(.*/)index\.html$`, Destination: "$1", Status: 308},
			Redirect{Source: `^(.+)\.html$`, Destination: "$1" + tail, Status: 308})
	}
	if static && r.TrailingSlash != nil {
		if *r.TrailingSlash {
			list = append(list, Redirect{Source: `^(.*/[^/.]+)$`, Destination: "$1/", Status: 308})
		} else {
			list = append(list, Redirect{Source: `^(.+)/$`, Destination: "$1", Status: 308})
		}
	}
	list = append(list, r.Redirects...)
	if len(list) == 0 {
		return nil
	}
	var routes []obj
	for i, d := range list {
		name := "r" + strconv.Itoa(i)
		to := placeholders(d.Destination, name)
		sep := "?"
		if strings.Contains(d.Destination, "?") {
			sep = "&"
		}
		answer := func(loc string) []obj {
			return []obj{{"handler": "static_response", "status_code": d.Status, "headers": obj{"Location": []string{loc}}}}
		}
		// One group: only the first matching route runs. The query string
		// travels along when there is one.
		routes = append(routes,
			obj{"group": "redirect", "match": []obj{{"path_regexp": obj{"name": name, "pattern": d.Source}, "query": obj{}}}, "handle": answer(to)},
			obj{"group": "redirect", "match": []obj{{"path_regexp": obj{"name": name, "pattern": d.Source}, "not": []obj{{"query": obj{}}}}},
				"handle": answer(to + sep + "{http.request.uri.query}")})
	}
	return []obj{{"handler": "subroute", "routes": routes}}
}

// fileHandlers resolve a static site's request to a file: the path itself,
// path.html, then (for an SPA, or a site that wants no trailing slash)
// path/index.html; a folder asked for without its slash is redirected to it
// by the file server. Without a file, the site's rewrites apply, then its
// 404.html (with status 404) when it has one, or index.html for an SPA.
func fileHandlers(rt Route) []obj {
	tries := []string{"{http.request.uri.path}", "{http.request.uri.path}.html"}
	exists := []string{tries[0], tries[1], "{http.request.uri.path}/index.html"}
	r := rt.Rules
	if rt.SPA || (r != nil && r.TrailingSlash != nil && !*r.TrailingSlash) {
		tries = append(tries, exists[2])
	}
	if rt.SPA {
		tries = append(tries, "/index.html")
	}
	file := func(try []string) obj { return obj{"root": rt.FileRoot, "try_files": try} }
	routes := []obj{{
		"group":  "file",
		"match":  []obj{{"file": file(tries)}},
		"handle": []obj{{"handler": "rewrite", "uri": "{http.matchers.file.relative}"}},
	}}
	if r != nil {
		for i, w := range r.Rewrites {
			name := "w" + strconv.Itoa(i)
			routes = append(routes, obj{
				"group":  "file",
				"match":  []obj{{"path_regexp": obj{"name": name, "pattern": w.Source}, "not": []obj{{"file": file(exists)}}}},
				"handle": []obj{{"handler": "rewrite", "uri": placeholders(w.Destination, name)}},
			})
		}
	}
	if !rt.SPA {
		routes = append(routes, obj{
			"group": "file",
			"match": []obj{{"not": []obj{{"file": file(exists)}}, "file": file([]string{"/404.html"})}},
			"handle": []obj{
				{"handler": "rewrite", "uri": "/404.html"},
				{"handler": "headers", "response": obj{"set": obj{"Cache-Control": []string{"no-cache"}}}},
				{"handler": "file_server", "root": rt.FileRoot, "status_code": 404},
			},
		})
	}
	return []obj{{"handler": "subroute", "routes": routes}}
}
