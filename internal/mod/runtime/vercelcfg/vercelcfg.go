// Package vercelcfg reads an app's vercel.json and translates what the box
// can honour: build settings (buildCommand, installCommand,
// outputDirectory), crons, and headers, redirects, rewrites, cleanUrls and
// trailingSlash as edge rules. Everything else is listed as ignored, so a
// deploy can say what it left out.
package vercelcfg

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/btahir/tiffin/internal/edge"
	"github.com/btahir/tiffin/internal/mod/runtime/srcpack"
	"github.com/robfig/cron/v3"
)

// File is the name the box looks for at the top of an app.
const File = "vercel.json"

// Cron is a vercel.json cron: a GET to Path on Schedule (UTC).
type Cron struct {
	Name     string `json:"name" doc:"Cron name on the box, from the path"`
	Path     string `json:"path"`
	Schedule string `json:"schedule"`
}

// Config is what the box takes from a vercel.json.
type Config struct {
	BuildCommand    string      `json:"buildCommand,omitempty"`
	InstallCommand  string      `json:"installCommand,omitempty"`
	OutputDirectory string      `json:"outputDirectory,omitempty"`
	Crons           []Cron      `json:"crons,omitempty"`
	Rules           *edge.Rules `json:"rules,omitempty" doc:"Headers, redirects and rewrites the edge applies"`
	// Ignored lists what the box does not apply: keys it does not use and
	// rules it cannot translate.
	Ignored []string `json:"ignored,omitempty" doc:"Keys and rules of vercel.json the box does not apply"`
}

// Read reads dir/vercel.json; it returns nil, nil when there is none.
func Read(dir string) (*Config, error) {
	raw, err := srcpack.ReadFile(filepath.Join(dir, File), true)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

type rule struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Headers     []struct{ Key, Value string }
	Permanent   *bool           `json:"permanent"`
	StatusCode  int             `json:"statusCode"`
	Has         json.RawMessage `json:"has"`
	Missing     json.RawMessage `json:"missing"`
}

// Parse translates a vercel.json document.
func Parse(raw []byte) (*Config, error) {
	var doc map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%s: %w", File, err)
	}
	c := &Config{}
	rules := &edge.Rules{}
	str := func(k string) (string, error) {
		var s string
		if err := json.Unmarshal(doc[k], &s); err != nil {
			return "", fmt.Errorf("%s: %s must be a string", File, k)
		}
		return strings.TrimSpace(s), nil
	}
	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := doc[k]
		if string(v) == "null" {
			continue
		}
		var err error
		switch k {
		case "$schema":
		case "buildCommand":
			c.BuildCommand, err = str(k)
		case "installCommand":
			c.InstallCommand, err = str(k)
		case "outputDirectory":
			if c.OutputDirectory, err = str(k); err == nil {
				o := filepath.ToSlash(filepath.Clean(c.OutputDirectory))
				if filepath.IsAbs(c.OutputDirectory) || o == ".." || strings.HasPrefix(o, "../") {
					return nil, fmt.Errorf("%s: outputDirectory must be inside the app", File)
				}
				c.OutputDirectory = o
			}
		case "cleanUrls":
			err = json.Unmarshal(v, &rules.CleanURLs)
		case "trailingSlash":
			var b bool
			err = json.Unmarshal(v, &b)
			rules.TrailingSlash = &b
		case "crons":
			err = c.crons(v)
		case "headers", "redirects", "rewrites":
			var list []rule
			if err = json.Unmarshal(v, &list); err != nil {
				return nil, fmt.Errorf("%s: %s: %w", File, k, err)
			}
			for i, r := range list {
				if note := c.add(rules, k, r); note != "" {
					c.Ignored = append(c.Ignored, fmt.Sprintf("%s[%d] (%s)", k, i, note))
				}
			}
		default:
			c.Ignored = append(c.Ignored, k)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", File, k, err)
		}
	}
	if len(rules.Headers)+len(rules.Redirects)+len(rules.Rewrites) > 0 || rules.CleanURLs || rules.TrailingSlash != nil {
		c.Rules = rules
	}
	return c, nil
}

// add translates one header, redirect or rewrite rule into rules; it
// returns why it could not, or "".
func (c *Config) add(rules *edge.Rules, kind string, r rule) string {
	if len(r.Has) > 0 || len(r.Missing) > 0 {
		return "has/missing conditions are not supported"
	}
	src, err := SourceRegexp(r.Source)
	if err != nil {
		return err.Error()
	}
	switch kind {
	case "headers":
		set := map[string]string{}
		for _, h := range r.Headers {
			if h.Key != "" {
				set[h.Key] = h.Value
			}
		}
		if len(set) == 0 {
			return "no headers"
		}
		rules.Headers = append(rules.Headers, edge.HeaderRule{Source: src, Set: set})
	case "redirects":
		if r.Destination == "" {
			return "no destination"
		}
		status := 308
		if r.Permanent != nil && !*r.Permanent {
			status = 307
		}
		if r.StatusCode != 0 {
			status = r.StatusCode
		}
		if status < 300 || status > 399 {
			return fmt.Sprintf("status %d is not a redirect", status)
		}
		rules.Redirects = append(rules.Redirects, edge.Redirect{Source: src, Destination: destination(r.Destination), Status: status})
	case "rewrites":
		if !strings.HasPrefix(r.Destination, "/") {
			return "rewrites to another site (a proxy) are not supported"
		}
		rules.Rewrites = append(rules.Rewrites, edge.Rewrite{Source: src, Destination: destination(r.Destination)})
	}
	return ""
}

var cronPath = regexp.MustCompile(`^/[^\s]*$`)

func (c *Config) crons(raw json.RawMessage) error {
	var list []struct {
		Path     string `json:"path"`
		Schedule string `json:"schedule"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return err
	}
	used := map[string]bool{}
	for i, cr := range list {
		if !cronPath.MatchString(cr.Path) {
			c.Ignored = append(c.Ignored, fmt.Sprintf("crons[%d] (path %q must start with /)", i, cr.Path))
			continue
		}
		if _, err := cron.ParseStandard(cr.Schedule); err != nil || strings.HasPrefix(cr.Schedule, "@every") {
			c.Ignored = append(c.Ignored, fmt.Sprintf("crons[%d] (schedule %q is not a 5-field cron expression)", i, cr.Schedule))
			continue
		}
		name := CronName(cr.Path)
		for n := 2; used[name]; n++ {
			name = fmt.Sprintf("%s-%d", CronName(cr.Path), n)
		}
		used[name] = true
		c.Crons = append(c.Crons, Cron{Name: name, Path: cr.Path, Schedule: cr.Schedule})
	}
	return nil
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// CronName names a vercel.json cron on the box after its path:
// /api/cron/daily-digest → api-cron-daily-digest.
func CronName(path string) string {
	p, _, _ := strings.Cut(path, "?")
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(p), "-"), "-")
	if s == "" {
		s = "root"
	}
	if s[0] >= '0' && s[0] <= '9' {
		s = "cron-" + s
	}
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	return s
}

// ForContainer drops what applies to static sites only (rewrites,
// cleanUrls, trailingSlash, outputDirectory) from a container app's config,
// listing them as ignored: the app's own routing (next.config) runs in it.
func (c *Config) ForContainer() {
	if c.OutputDirectory != "" {
		c.Ignored = append(c.Ignored, "outputDirectory (an app's server serves its own output)")
		c.OutputDirectory = ""
	}
	r := c.Rules
	if r == nil {
		return
	}
	var dropped []string
	if len(r.Rewrites) > 0 {
		dropped = append(dropped, "rewrites")
	}
	if r.CleanURLs {
		dropped = append(dropped, "cleanUrls")
	}
	if r.TrailingSlash != nil {
		dropped = append(dropped, "trailingSlash")
	}
	if len(dropped) > 0 {
		c.Ignored = append(c.Ignored, strings.Join(dropped, ", ")+" (static sites only; the app's own routing runs in it)")
	}
	r.Rewrites, r.CleanURLs, r.TrailingSlash = nil, false, nil
	if len(r.Headers)+len(r.Redirects) == 0 {
		c.Rules = nil
	}
}

// Summary says in one line what the box took from vercel.json ("" for nothing).
func (c *Config) Summary() string {
	var parts []string
	for _, kv := range [][2]string{{"buildCommand", c.BuildCommand}, {"installCommand", c.InstallCommand}, {"outputDirectory", c.OutputDirectory}} {
		if kv[1] != "" {
			parts = append(parts, fmt.Sprintf("%s %q", kv[0], kv[1]))
		}
	}
	count := func(n int, what string) {
		switch {
		case n == 1:
			parts = append(parts, "1 "+what)
		case n > 1:
			parts = append(parts, fmt.Sprintf("%d %ss", n, what))
		}
	}
	count(len(c.Crons), "cron")
	if r := c.Rules; r != nil {
		count(len(r.Headers), "header rule")
		count(len(r.Redirects), "redirect")
		count(len(r.Rewrites), "rewrite")
		if r.CleanURLs {
			parts = append(parts, "cleanUrls")
		}
		if r.TrailingSlash != nil {
			parts = append(parts, fmt.Sprintf("trailingSlash %t", *r.TrailingSlash))
		}
	}
	return strings.Join(parts, ", ")
}

// SourceRegexp turns a vercel.json source (path-to-regexp: /blog/:slug,
// /docs/:path*, /(.*), /:id(\d+)) into a Go regular expression for the
// whole path. Named parameters become named groups, so a destination can
// use them as $name; every group counts for $1, $2, ... as on Vercel.
func SourceRegexp(src string) (string, error) {
	if !strings.HasPrefix(src, "/") {
		return "", fmt.Errorf("source %q must start with /", src)
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '\\' && i+1 < len(src):
			b.WriteString(regexp.QuoteMeta(src[i+1 : i+2]))
			i += 2
		case c == ':' && i+1 < len(src) && isWord(src[i+1]):
			j := i + 1
			for j < len(src) && isWord(src[j]) {
				j++
			}
			name, pat := src[i+1:j], "[^/]+"
			if j < len(src) && src[j] == '(' {
				end, err := closing(src, j)
				if err != nil {
					return "", err
				}
				pat, j = src[j+1:end], end+1
			}
			mod := byte(0)
			if j < len(src) && strings.IndexByte("?*+", src[j]) >= 0 {
				mod, j = src[j], j+1
			}
			param(&b, name, pat, mod)
			i = j
		case c == '(':
			end, err := closing(src, i)
			if err != nil {
				return "", err
			}
			b.WriteString(src[i : end+1])
			i = end + 1
		case c == '*':
			b.WriteString("(.*)")
			i++
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
			i++
		}
	}
	b.WriteString("$")
	if _, err := regexp.Compile(b.String()); err != nil {
		return "", fmt.Errorf("source %q: %w", src, err)
	}
	return b.String(), nil
}

// param writes a named parameter; with ? or * the slash before it is part
// of what is optional (/docs/:path* matches /docs too).
func param(b *strings.Builder, name, pat string, mod byte) {
	group := "(?P<" + name + ">" + pat + ")"
	if mod == '*' || mod == '+' {
		group = "(?P<" + name + ">" + pat + "(?:/" + pat + ")*)"
	}
	if mod != '?' && mod != '*' {
		b.WriteString(group)
		return
	}
	s := b.String()
	if strings.HasSuffix(s, "/") {
		b.Reset()
		b.WriteString(s[:len(s)-1])
		b.WriteString("(?:/" + group + ")?")
		return
	}
	b.WriteString(group + "?")
}

func closing(s string, open int) (int, error) {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i, nil
			}
		}
	}
	return 0, fmt.Errorf("source %q: unbalanced parenthesis", s)
}

func isWord(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

var destParam = regexp.MustCompile(`:([A-Za-z_][A-Za-z0-9_]*)[*+?]?`)

// destination turns a vercel.json destination's :name references into the
// edge's $name; $1 stays. "https://" keeps its colon (no name follows).
func destination(d string) string {
	return destParam.ReplaceAllString(d, "$$$1")
}
