// Package dashboard serves the built web dashboard (apps/dashboard) from
// inside the binary. The dashboard uses only the public API.
package dashboard

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	tiffin "github.com/shiptiffin/tiffin"
	"github.com/shiptiffin/tiffin/internal/version"
)

//go:embed all:dist
var dist embed.FS

// FormOrigins are the other origins the dashboard may submit forms to:
// GitHub's website, where Connect GitHub POSTs the app manifest (GitHub's
// manifest flow needs a real form post). The runtime points it at the
// GitHub the box uses (GitHub Enterprise Server too).
var FormOrigins atomic.Pointer[func() []string]

var originRe = regexp.MustCompile(`^https://[A-Za-z0-9.-]+(:[0-9]{1,5})?$`)

// csp is the dashboard's Content-Security-Policy.
func csp() string {
	fa := "'self'"
	if f := FormOrigins.Load(); f != nil {
		for _, o := range (*f)() {
			if originRe.MatchString(o) {
				fa += " " + o
			}
		}
	}
	return "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action " + fa
}

func init() {
	gh := func() []string { return []string{"https://github.com"} }
	FormOrigins.Store(&gh)
}

// licenses is what /licenses.txt serves (and the /licenses page shows):
// Tiffin's licence, where this build's source is and the third-party notices.
var licenses = sync.OnceValue(func() string { return tiffin.Licenses(version.Version, version.SourceURL()) })

// Handler serves the single-page app: real files when they exist, otherwise
// index.html so client-side routes work. Hashed assets are cached forever.
func Handler() http.Handler {
	root, _ := fs.Sub(dist, "dist")
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp())
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "licenses.txt" {
			h.Set("Content-Type", "text/plain; charset=utf-8")
			h.Set("Cache-Control", "no-cache")
			_, _ = io.WriteString(w, licenses())
			return
		}
		if p != "" {
			if _, err := fs.Stat(root, p); err == nil {
				if strings.HasPrefix(p, "assets/") {
					h.Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
			// A missing file is a 404, not the app. So is /.well-known/: clients
			// probe it (MCP for OAuth discovery) and must not get a page back.
			if strings.HasPrefix(p, "assets/") || strings.HasPrefix(p, ".well-known/") || path.Ext(p) != "" {
				http.NotFound(w, r)
				return
			}
		}
		h.Set("Cache-Control", "no-cache")
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		files.ServeHTTP(w, r2)
	})
}
