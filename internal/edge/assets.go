package edge

import (
	"net/http"
	"path"
	"strings"

	"github.com/caddyserver/caddy/v2"
)

func init() { caddy.RegisterModule(HashedAssetMatcher{}) }

// HashedAssetMatcher is the Caddy module http.matchers.tiffin_hashed_asset:
// it matches requests for fingerprinted build output (see hashedAsset).
type HashedAssetMatcher struct{}

// CaddyModule returns the Caddy module information.
func (HashedAssetMatcher) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.matchers.tiffin_hashed_asset",
		New: func() caddy.Module { return new(HashedAssetMatcher) },
	}
}

// MatchWithError reports whether the request is for a hashed asset.
func (HashedAssetMatcher) MatchWithError(r *http.Request) (bool, error) {
	return hashedAsset(r.URL.Path), nil
}

// hashedDirs only ever hold content-hashed files: Next.js, SvelteKit and
// Astro build output.
var hashedDirs = []string{"/_next/static/", "/_app/immutable/", "/_astro/"}

// hashedAsset reports whether a path names a file whose name changes with
// its content, so it can be cached for good: one under hashedDirs, or one
// whose name carries a hash after a "-" or "." (Vite's index-B1x9Qa2c.js,
// webpack's main.8f3a2b1c.chunk.js). A hash is at least 8 letters, digits
// or underscores with both a letter and a digit, so words ("my-component")
// and dates ("photo-20240115") are not taken for one. HTML never is.
// Getting it wrong one way only costs a revalidation; the other way would
// pin a stale file in browsers for a year, so when unsure: no.
func hashedAsset(p string) bool {
	for _, d := range hashedDirs {
		if strings.Contains(p, d) {
			return true
		}
	}
	name := path.Base(p)
	ext := path.Ext(name)
	if ext == "" || ext == ".html" || ext == ".htm" {
		return false
	}
	stem := strings.TrimSuffix(name, ext)
	i := strings.IndexAny(stem, ".-")
	if i < 0 {
		return false
	}
	for _, part := range strings.FieldsFunc(stem[i+1:], func(r rune) bool { return r == '.' || r == '-' }) {
		if looksLikeHash(part) {
			return true
		}
	}
	return false
}

func looksLikeHash(s string) bool {
	if len(s) < 8 {
		return false
	}
	var letter, digit bool
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
			digit = true
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
			letter = true
		case c != '_':
			return false
		}
	}
	return letter && digit
}
