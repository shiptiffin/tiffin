// Package srcpack packs an app's source directory into a gzipped tar for
// `tiffin deploy` (respecting .gitignore, .tiffinignore and a default ignore
// list) and safely unpacks such archives on the box.
package srcpack

import (
	"bufio"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// DefaultIgnore is always skipped: build output, dependencies, VCS data and
// local secret files. Apps are rebuilt on the box from source.
var DefaultIgnore = []string{
	".git/",
	".hg/",
	".svn/",
	"node_modules/",
	".next/",
	".turbo/",
	".vercel/",
	".tiffin/",
	".venv/", // Python: the box installs from pyproject.toml/uv.lock or requirements.txt
	"__pycache__/",
	".pytest_cache/",
	".mypy_cache/",
	".ruff_cache/",
	".DS_Store",
	".env",
	".env.local",
	".env.*.local",
}

// rule is one ignore pattern, relative to the directory of the file it came from.
type rule struct {
	base    string // directory (slash path, "" for the root) the pattern is relative to
	pattern string // without leading "!" and trailing "/"
	negate  bool
	dirOnly bool
	// anchored patterns (with a slash before the end) match from base;
	// others match a name at any depth below base.
	anchored bool
}

// Matcher decides which paths to skip. Rules are evaluated in order; the
// last matching rule wins, as in git.
type Matcher struct {
	rules []rule
}

// NewMatcher returns a matcher with the default rules plus extra patterns
// (gitignore syntax) relative to the root.
func NewMatcher(extra ...string) *Matcher {
	m := &Matcher{}
	for _, p := range DefaultIgnore {
		m.Add("", p)
	}
	for _, p := range extra {
		m.Add("", p)
	}
	return m
}

// Add parses one gitignore line for the directory base (slash separated,
// relative to the root, "" for the root itself).
func (m *Matcher) Add(base, line string) {
	line = strings.TrimRight(line, " \t\r")
	if line == "" || strings.HasPrefix(line, "#") {
		return
	}
	r := rule{base: strings.Trim(base, "/")}
	if strings.HasPrefix(line, `\#`) || strings.HasPrefix(line, `\!`) {
		line = line[1:]
	} else if strings.HasPrefix(line, "!") {
		r.negate = true
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") {
		r.dirOnly = true
		line = strings.TrimRight(line, "/")
	}
	if line == "" {
		return
	}
	if strings.Contains(line, "/") {
		r.anchored = true
		line = strings.TrimPrefix(line, "/")
	}
	r.pattern = line
	m.rules = append(m.rules, r)
}

// AddFile reads a .gitignore-style file found in directory base.
func (m *Matcher) AddFile(base, file string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		m.Add(base, sc.Text())
	}
	return sc.Err()
}

// Ignored reports whether rel (slash separated, relative to the root) is ignored.
// Callers walk top-down and skip ignored directories, so a file inside an
// ignored directory is never asked about (git cannot re-include those either).
func (m *Matcher) Ignored(rel string, isDir bool) bool {
	rel = strings.Trim(filepath.ToSlash(rel), "/")
	ignored := false
	for _, r := range m.rules {
		if r.dirOnly && !isDir {
			continue
		}
		if r.match(rel) {
			ignored = !r.negate
		}
	}
	return ignored
}

func (r rule) match(rel string) bool {
	sub := rel
	if r.base != "" {
		if !strings.HasPrefix(rel, r.base+"/") {
			return false
		}
		sub = strings.TrimPrefix(rel, r.base+"/")
	}
	if r.anchored {
		return globMatch(r.pattern, sub)
	}
	// Unanchored: match the last path element, or any trailing sub-path for
	// patterns that contain "**".
	if strings.Contains(r.pattern, "**") {
		return globMatch("**/"+r.pattern, sub)
	}
	return globMatch(r.pattern, path.Base(sub))
}

// globMatch matches a slash path against a pattern where "*" and "?" stay
// within one segment and "**" spans any number of segments.
func globMatch(pattern, name string) bool {
	return matchSegs(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegs(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			rest := pat[1:]
			if len(rest) == 0 {
				return true // "**" at the end matches everything below
			}
			for i := 0; i <= len(segs); i++ {
				if matchSegs(rest, segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], segs[0])
		if err != nil || !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}
