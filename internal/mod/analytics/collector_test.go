package analytics

import (
	"testing"

	"github.com/shiptiffin/tiffin/internal/mod/observe/edgelog"
)

// A file opened as a document (a scanner, or someone opening an icon in a
// tab) is not a page view; a page is, with or without a query.
func TestEdgePageviewSkipsFiles(t *testing.T) {
	for uri, want := range map[string]bool{
		"/":                     true,
		"/pricing?ref=hn":       true,
		"/blog/v1.2-notes":      true,
		"/icon.svg":             false,
		"/apple-icon.png":       false,
		"/robots.txt":           false,
		"/_next/static/x/a.js":  false,
		"/_next/image?url=%2Fa": false,
	} {
		e := edgelog.Entry{Method: "GET", Status: 200, URI: uri,
			Headers: map[string][]string{"Sec-Fetch-Dest": {"document"}, "Sec-Fetch-Mode": {"navigate"}}}
		if got := EdgePageview(&e); got != want {
			t.Errorf("%s: pageview %v, want %v", uri, got, want)
		}
	}
}
