package linkpreview

import (
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"unicode/utf8"
)

// What the big unfurlers accept, from their docs (October 2026): X cards
// take JPEG, PNG, WebP and GIF up to 5 MB, 4096 px a side, at least
// 300×157 for a large card; Facebook wants 1200×630 (at least 600×315 for
// a large preview, 200×200 at all); LinkedIn 1200×627. None shows SVG.
const (
	minSide        = 200 // under it, Facebook shows no image
	largeW, largeH = 600, 315
	maxSide        = 4096
	titleShort     = 10  // under it, a title can't say what the page is
	titleLong      = 60  // cards and search results cut titles off around here
	descShort      = 50  // under it, search engines often write their own snippet
	descLong       = 160 // search results cut descriptions off around here
	socialLong     = 200 // cards show about two lines
)

func (t Target) checks(r *LinkReport, robots string) []LinkCheck {
	var out []LinkCheck
	add := func(id, label, status, detail, fix string) {
		out = append(out, LinkCheck{ID: id, Label: label, Status: status, Detail: detail, Fix: fix})
	}

	n := utf8.RuneCountInString
	ogTitle, ogDesc := r.tag("og:title"), r.tag("og:description")

	// The share title (og:title), which every card shows in bold.
	switch {
	case r.Title == "":
		add("title", "Title", StatusBad, "The page has no og:title and no <title>, so shares show its address instead.",
			"Give the page an og:title and a <title> (Next.js: title and openGraph.title in metadata).")
	case ogTitle == "":
		add("title", "Title", StatusWarn, fmt.Sprintf("There is no og:title. Most apps fall back to the <title>, “%s”; some show nothing.", r.Title),
			"Add an og:title (Next.js: openGraph.title in metadata).")
	case n(ogTitle) > titleLong:
		add("title", "Title", StatusWarn, fmt.Sprintf("“%s” is %d characters; cards cut titles off after about %d.", ogTitle, n(ogTitle), titleLong),
			"Shorten the og:title, with the words that matter first.")
	case n(ogTitle) < titleShort:
		add("title", "Title", StatusWarn, fmt.Sprintf("“%s” is %d characters, too short to say what the page is.", ogTitle, n(ogTitle)),
			fmt.Sprintf("Write %d to %d characters: what it is, for whom.", titleShort, titleLong))
	default:
		add("title", "Title", StatusOK, fmt.Sprintf("“%s”, %d characters.", ogTitle, n(ogTitle)), "")
	}

	// The search result's title (<title>). With neither title, the check above says so.
	switch pt := r.PageTitle; {
	case pt == "" && r.Title != "":
		add("page-title", "Page title", StatusWarn, "There is no <title>, so search results and browser tabs make one up.",
			"Add a <title> (Next.js: title in metadata).")
	case pt == "":
	case n(pt) > titleLong:
		add("page-title", "Page title", StatusWarn, fmt.Sprintf("“%s” is %d characters; search results cut titles off after about %d.", pt, n(pt), titleLong),
			"Shorten the <title>, with the words that matter first.")
	default:
		add("page-title", "Page title", StatusOK, fmt.Sprintf("“%s”, %d characters.", pt, n(pt)), "")
	}

	// The share description (og:description): a line or two under the title.
	switch {
	case r.Description == "":
		add("description", "Description", StatusWarn, "There is no og:description and no meta description, so shares show only the title.",
			"Add a description (Next.js: description and openGraph.description in metadata).")
	case ogDesc == "":
		add("description", "Description", StatusOK, "No og:description; apps use the meta description instead.", "")
	case n(ogDesc) < descShort:
		add("description", "Description", StatusOK, fmt.Sprintf("%d characters: short, but fine. %d to %d say more.", n(ogDesc), descShort, socialLong), "")
	case n(ogDesc) > socialLong:
		add("description", "Description", StatusOK, fmt.Sprintf("%d characters: cards show about the first %d.", n(ogDesc), socialLong), "")
	default:
		add("description", "Description", StatusOK, fmt.Sprintf("%d characters.", n(ogDesc)), "")
	}

	// The search result's snippet (meta description).
	switch md := r.MetaDesc; {
	case md == "" && r.Description == "":
	case md == "":
		add("meta-description", "Meta description", StatusWarn, "There is none, so search results pick a snippet from the page.",
			"Add a meta description (Next.js: description in metadata).")
	case n(md) > descLong:
		add("meta-description", "Meta description", StatusWarn, fmt.Sprintf("%d characters; search results cut it off after about %d.", n(md), descLong),
			fmt.Sprintf("Keep it under %d characters.", descLong))
	case n(md) < descShort:
		add("meta-description", "Meta description", StatusOK, fmt.Sprintf("%d characters: short, so search engines may write their own snippet.", n(md)), "")
	default:
		add("meta-description", "Meta description", StatusOK, fmt.Sprintf("%d characters.", n(md)), "")
	}

	// Site name: the small line above the title on LinkedIn, Slack and Discord.
	if r.SiteName != "" {
		add("site-name", "Site name", StatusOK, "“"+r.SiteName+"”", "")
	} else {
		add("site-name", "Site name", StatusOK, "No og:site_name; LinkedIn, Slack and Discord show "+t.Page.Hostname()+" instead.", "")
	}

	// X reads its own twitter: tags first and falls back to og: ones, so a
	// missing one is fine as long as there is something to fall back on.
	xLine := func(id, label, own, ogName, ogVal string) {
		switch {
		case own != "":
			add(id, label, StatusOK, "Uses its own "+strings.TrimPrefix(id, "x-")+": “"+clip(own, 80)+"”", "")
		case ogVal != "":
			add(id, label, StatusOK, "No twitter:"+strings.TrimPrefix(id, "x-")+"; X uses "+ogName+".", "")
		}
	}
	xLine("x-title", "X title", r.tag("twitter:title"), "og:title", ogTitle)
	xLine("x-description", "X description", r.tag("twitter:description"), "og:description", ogDesc)
	if r.Image != nil {
		xi := first(r.tag("twitter:image"), r.tag("twitter:image:src"))
		switch {
		case xi == "":
			add("x-image", "X image", StatusOK, "No twitter:image; X uses og:image.", "")
		case xi == first(r.tag("og:image"), r.tag("og:image:url"), r.tag("og:image:secure_url")):
			add("x-image", "X image", StatusOK, "twitter:image is the same as og:image.", "")
		default:
			add("x-image", "X image", StatusOK, "X shows its own image, "+clip(xi, 120)+" (not measured).", "")
		}
	}

	// Image.
	im := r.Image
	switch {
	case im == nil:
		add("image", "Share image", StatusBad, "The page names no og:image, so shares show no picture.",
			"Add an og:image, 1200×630 PNG or JPEG (Next.js: app/opengraph-image.png, or openGraph.images in metadata).")
	case im.Written != "" && !strings.HasPrefix(strings.ToLower(im.Written), "http"):
		add("image", "Share image", StatusBad, "og:image is “"+im.Written+"”, a relative path: unfurlers need a full address.",
			"Write it as a full https:// address (Next.js: set metadataBase, or use app/opengraph-image).")
	case t.misplaced(im.URL) != "":
		add("image", "Share image", StatusBad, "og:image points at "+t.misplaced(im.URL)+".",
			"Point it at the app's public address (Next.js: set metadataBase to it).")
	case im.Error != "" && im.Status == 0 && strings.HasPrefix(im.Error, "the box doesn't fetch"):
		add("image", "Share image", StatusWarn, im.Error+", so it wasn't checked.", "")
	case im.Error != "":
		add("image", "Share image", StatusBad, "The image at "+im.URL+" can't be used: "+im.Error+".", "Make sure the address serves the image to anyone, without a sign-in.")
	case im.MIME == "image/svg+xml":
		add("image", "Share image", StatusBad, "The image is an SVG, which X, Facebook, LinkedIn and Slack don't show.", "Use a PNG or JPEG.")
	case im.MIME == "image/avif":
		add("image", "Share image", StatusWarn, "The image is AVIF, which several unfurlers don't show yet.", "Use a PNG or JPEG.")
	case strings.HasPrefix(im.URL, "http://"):
		add("image", "Share image", StatusWarn, "The image's address is http://; some apps only load https images.", "Serve it from an https:// address.")
	default:
		add("image", "Share image", StatusOK, im.URL, "")
	}

	// Image size (only once the image was read).
	if im != nil && im.Error == "" && im.Width > 0 {
		w, h := im.Width, im.Height
		ratio := float64(w) / float64(h)
		dims := fmt.Sprintf("%d×%d, %s", w, h, size(im.Bytes))
		switch {
		case w < minSide || h < minSide:
			add("image-size", "Image size", StatusBad, dims+": Facebook shows no image under 200×200, X only a small one.", "Use 1200×630.")
		case w < largeW || h < largeH:
			add("image-size", "Image size", StatusWarn, dims+": too small for a large card, so it shows as a thumbnail.", "Use 1200×630.")
		case w > maxSide || h > maxSide:
			add("image-size", "Image size", StatusWarn, dims+": X takes at most 4096 px a side.", "Use 1200×630.")
		case ratio < 1.6 || ratio > 2.2:
			add("image-size", "Image size", StatusWarn, dims+": large cards are about 1.91:1, so it will be cropped.", "Use 1200×630.")
		default:
			add("image-size", "Image size", StatusOK, dims, "")
		}
	}

	// X card.
	switch r.Card {
	case "summary_large_image":
		add("twitter-card", "X card", StatusOK, "summary_large_image: the image shows full width.", "")
	case "":
		if im != nil {
			add("twitter-card", "X card", StatusWarn, "There is no twitter:card, so X shows the image small, beside the title.",
				"Add twitter:card summary_large_image (Next.js: twitter.card in metadata).")
		} else {
			add("twitter-card", "X card", StatusWarn, "There is no twitter:card.", "Add twitter:card summary_large_image once the page has an image.")
		}
	default:
		add("twitter-card", "X card", StatusOK, r.Card+".", "")
	}

	// Canonical.
	switch c := r.Canonical; {
	case c == "" && t.Preview:
	case c == "" && len(t.Hosts) > 1:
		add("canonical", "Canonical address", StatusWarn, fmt.Sprintf("There is none, and the app answers at %d addresses.", len(t.Hosts)),
			"Add <link rel=canonical> to the main address, so search engines list that one (Next.js: alternates.canonical).")
	case c == "":
		add("canonical", "Canonical address", StatusOK, "None; the app has one address.", "")
	default:
		u, err := t.Page.Parse(c)
		switch {
		case err != nil || (u.Scheme != "http" && u.Scheme != "https"):
			add("canonical", "Canonical address", StatusBad, "“"+c+"” is not a web address.", "Use the page's full https:// address.")
		case t.Preview:
			add("canonical", "Canonical address", StatusOK, u.String(), "")
		case t.misplaced(u.String()) != "":
			add("canonical", "Canonical address", StatusBad, "It points at "+t.misplaced(u.String())+".", "Point it at the app's main address.")
		case !strings.EqualFold(u.Hostname(), t.Page.Hostname()):
			add("canonical", "Canonical address", StatusWarn, "It points at "+u.Hostname()+", not the app's main address, "+t.Page.Hostname()+".",
				"Point it at "+t.Page.Scheme+"://"+t.Page.Host+u.EscapedPath()+".")
		default:
			add("canonical", "Canonical address", StatusOK, u.String(), "")
		}
	}

	// Robots.
	if !t.Preview {
		if noindex(robots) {
			add("robots", "Search engines", StatusBad, "The page tells search engines not to list it (noindex).",
				"Remove noindex from the robots meta tag or X-Robots-Tag header, unless that's on purpose.")
		} else {
			add("robots", "Search engines", StatusOK, "Allowed to list the page.", "")
		}
	}

	// Icon.
	if r.Icon == "" {
		add("icon", "Icon", StatusWarn, "The page has no icon (no <link rel=icon> that answers, no /favicon.ico).",
			"Add one (Next.js: app/icon.png or app/favicon.ico).")
	} else {
		add("icon", "Icon", StatusOK, r.Icon, "")
	}
	return out
}

// misplaced says what is wrong with an address a page points visitors at:
// this computer or a private network, or (production) another copy of the
// app on the box. Empty when it's fine.
func (t Target) misplaced(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if t.own(host) {
		return "" // the app's own address (on a box on this computer, .localhost)
	}
	if privateHost(host) {
		return host + ", which nobody else can reach"
	}
	if !t.Preview && t.BoxHost != nil && t.BoxHost(host) {
		return host + ", another copy of the app (a preview or an old version) rather than its public address"
	}
	return ""
}

// privateHost reports whether a host names this computer or a private
// network: localhost, .local and .internal names, or a non-public IP.
func privateHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".lan") || !strings.Contains(host, ".") && !strings.Contains(host, ":") {
		return true
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || !ip.IsGlobalUnicast()
	}
	return false
}

// noindex reports whether robots directives keep the page out of search.
func noindex(robots string) bool {
	for _, f := range strings.FieldsFunc(robots, func(r rune) bool { return r == ',' || r == ':' || r == ' ' || r == ';' }) {
		if f == "noindex" || f == "none" {
			return true
		}
	}
	return false
}

// own reports whether host is one of the app's own public hostnames.
func (t Target) own(host string) bool {
	if t.Page != nil && strings.EqualFold(t.Page.Hostname(), host) {
		return true
	}
	for _, h := range t.Hosts {
		if strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}

// tag is the first non-empty value of a tag the page wrote.
func (r *LinkReport) tag(name string) string {
	for _, t := range r.Tags {
		if t.Name == name && t.Value != "" {
			return t.Value
		}
	}
	return ""
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
