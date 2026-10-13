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

// redirectTarget is the primary domain a host redirects to ("" when it
// serves). Tests replace it.
var redirectTarget = domains.RedirectTarget

// siteURLs is where people see a production app: main, the address to
// name (its first own domain that is live, else its first route, as
// before a domain is set up), and its other working addresses. An own
// domain that is not live yet is left out: it does not answer.
func (r *rt) siteURLs(project, app string, spec *manifest.App) (main string, others []string) {
	type addr struct {
		url  string
		host string
		own  bool // a domain of its own (not a name on the box)
		live bool
		// redirects: it answers a redirect to the app's primary domain.
		redirects bool
	}
	var all []addr
	for _, rt := range appRoutes(project, app, spec) {
		host, prefix := r.splitRoute(rt)
		own := strings.Contains(strings.SplitN(rt, "/", 2)[0], ".") && !r.p.IsBoxHost(host)
		a := addr{url: r.p.URL(host) + prefix, host: host, own: own, live: !own || liveDomain(host)}
		a.redirects = prefix == "" && redirectTarget(host) != ""
		if !slices.ContainsFunc(all, func(b addr) bool { return b.url == a.url }) {
			all = append(all, a)
		}
	}
	if len(all) == 0 {
		return "", nil
	}
	// The one place that picks: the live primary domain (the others redirect
	// to it), else the first live own domain.
	pick := -1
	for _, a := range all {
		if t := redirectTarget(a.host); t != "" {
			pick = slices.IndexFunc(all, func(b addr) bool { return b.host == t && b.live })
			if pick >= 0 {
				break
			}
		}
	}
	for i, a := range all {
		if pick >= 0 {
			break
		}
		if a.own && a.live {
			pick = i
		}
	}
	if pick < 0 {
		pick = 0
	}
	main = all[pick].url
	for i, a := range all {
		if i != pick && a.live && !a.redirects {
			others = append(others, a.url)
		}
	}
	return main, others
}
