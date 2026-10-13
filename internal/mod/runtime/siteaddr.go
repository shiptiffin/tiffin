package runtime

import (
	"slices"
	"strings"

	"github.com/shiptiffin/tiffin/internal/manifest"
	"github.com/shiptiffin/tiffin/internal/mod/domains"
)

// liveDomain reports whether a project's own domain is live on the box
// (DNS points here, certificate served). Tests replace it.
var liveDomain = domains.Live

// siteURLs is where people see a production app: main, the address to
// name (its first own domain that is live, else its first route, as
// before a domain is set up), and its other working addresses. An own
// domain that is not live yet is left out: it does not answer.
func (r *rt) siteURLs(project, app string, spec *manifest.App) (main string, others []string) {
	type addr struct {
		url  string
		own  bool // a domain of its own (not a name on the box)
		live bool
	}
	var all []addr
	for _, rt := range appRoutes(project, app, spec) {
		host, prefix := r.splitRoute(rt)
		own := strings.Contains(strings.SplitN(rt, "/", 2)[0], ".") && !r.p.IsBoxHost(host)
		a := addr{url: r.p.URL(host) + prefix, own: own, live: !own || liveDomain(host)}
		if !slices.ContainsFunc(all, func(b addr) bool { return b.url == a.url }) {
			all = append(all, a)
		}
	}
	if len(all) == 0 {
		return "", nil
	}
	// The one place that picks: the first live own domain. (edge-primary:
	// prefer the domain marked primary in the manifest here, when live.)
	pick := 0
	for i, a := range all {
		if a.own && a.live {
			pick = i
			break
		}
	}
	main = all[pick].url
	for i, a := range all {
		if i != pick && a.live {
			others = append(others, a.url)
		}
	}
	return main, others
}
