package runtime

import (
	"path"
	"strings"
)

// Watch paths: an app with `watch` patterns deploys from a GitHub push or
// pull request only when it changes a file one of them matches (a monorepo
// whose other apps changed builds nothing). Patterns are relative to the
// top of the repository and read like .gitignore lines:
//
//   - `*` and `?` match within one folder, `**` across folders;
//   - a pattern without a slash matches at any depth ("*.md");
//   - a pattern matches a file or any folder above it ("packages/ui");
//   - a leading `!` excludes, and the last pattern that matches decides.

// watchHits reports whether any of files is matched by patterns.
func watchHits(patterns, files []string) bool {
	for _, f := range files {
		in := false
		for _, p := range patterns {
			neg := strings.HasPrefix(p, "!")
			if watchMatch(strings.TrimPrefix(p, "!"), f) {
				in = !neg
			}
		}
		if in {
			return true
		}
	}
	return false
}

// watchMatch reports whether pattern matches file or one of its folders.
func watchMatch(pattern, file string) bool {
	pattern = strings.Trim(pattern, "/")
	file = strings.Trim(file, "/")
	if pattern == "" || file == "" {
		return false
	}
	if !strings.Contains(pattern, "/") {
		pattern = "**/" + pattern
	}
	pat, segs := strings.Split(pattern, "/"), strings.Split(file, "/")
	for n := len(segs); n > 0; n-- { // the file, then each folder above it
		if globSegs(pat, segs[:n]) {
			return true
		}
	}
	return false
}

// globSegs matches path segments against pattern segments, "**" standing
// for any number of segments (none included).
func globSegs(pat, segs []string) bool {
	if len(pat) == 0 {
		return len(segs) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(segs); i++ {
			if globSegs(pat[1:], segs[i:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	if ok, err := path.Match(pat[0], segs[0]); err != nil || !ok {
		return false
	}
	return globSegs(pat[1:], segs[1:])
}

// changeSet is what a push or pull request changed, as far as GitHub said.
type changeSet struct {
	// load fetches the changed files once, when an app with watch paths
	// asks; ok false means they are unknown (a new branch, a comparison
	// GitHub cut short or could not make), and every app deploys.
	load  func() (files []string, ok bool)
	files []string
	ok    bool
	done  bool
}

// touches reports whether the change deploys an app with these watch
// paths: always without any, or when the changed files are unknown.
func (c *changeSet) touches(watch []string) bool {
	if len(watch) == 0 {
		return true
	}
	if !c.done {
		c.files, c.ok = c.load()
		c.done = true
	}
	return !c.ok || watchHits(watch, c.files)
}
