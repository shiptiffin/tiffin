// Package linkpreview reads a web app's home page the way link unfurlers
// (X, Facebook, LinkedIn, Slack, iMessage) and search engines do, and says
// what a shared link to it will look like and what is missing.
//
// After each deploy of a web app goes live the box asks that deploy for its
// home page on the box's own network, as a crawler: the title, description,
// og:* and twitter:* tags, canonical, robots and icon. It fetches the share
// image too (from the app, or from another public site), checks it against
// what the big unfurlers accept, and keeps a small copy for the dashboard.
// Only the home page is read: other pages may share differently.
package linkpreview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Limits on a check.
const (
	fetchTimeout = 5 * time.Second
	maxHTML      = 2 << 20
	maxRedirects = 3
	// MaxImageBytes is X's limit for a card image (Facebook and LinkedIn
	// take up to 8 and 5 MB): an image over it shows on none of them.
	MaxImageBytes = 5 << 20
	maxTags       = 80
	maxValue      = 500
)

// userAgent names the check, and the unfurlers it stands in for: apps that
// stream metadata to browsers (Next.js) put it in <head> for these.
const userAgent = "Mozilla/5.0 (compatible; Tiffin-LinkPreview/1.0; like Twitterbot facebookexternalhit Slackbot)"

// Status of a check, and of a whole report (its worst check).
const (
	StatusOK   = "ok"
	StatusWarn = "warn"
	StatusBad  = "bad"
	// StatusUnreadable: the home page did not answer, or isn't a web page.
	StatusUnreadable = "unreadable"
)

// LinkReport is what a deploy's home page shares as.
type LinkReport struct {
	Deploy    string    `json:"deploy" doc:"The deploy that was checked"`
	CheckedAt time.Time `json:"checkedAt" doc:"When the box read the page"`
	// URL is the page's public address.
	URL    string `json:"url" doc:"The page that was read (the home page, at the address visitors use)"`
	Status string `json:"status" enum:"ok,warn,bad,unreadable" doc:"The worst of the checks; unreadable when the page did not answer or isn't a web page"`
	Reason string `json:"reason,omitempty" doc:"Why the page couldn't be read (status unreadable)"`

	// What the unfurlers show, after their usual fallbacks.
	Title       string      `json:"title,omitempty" doc:"The title a shared link shows: og:title, else twitter:title, else <title>"`
	Description string      `json:"description,omitempty" doc:"The description a shared link shows: og:description, else twitter:description, else the meta description"`
	SiteName    string      `json:"siteName,omitempty" doc:"og:site_name"`
	PageTitle   string      `json:"pageTitle,omitempty" doc:"The page's <title>, which search results show"`
	MetaDesc    string      `json:"metaDescription,omitempty" doc:"The meta description, which search results show"`
	XTitle      string      `json:"xTitle,omitempty" doc:"The title X shows: twitter:title, else the title above"`
	XDesc       string      `json:"xDescription,omitempty" doc:"The description X shows: twitter:description, else the description above"`
	Card        string      `json:"twitterCard,omitempty" doc:"twitter:card (summary_large_image shows the image full width on X)"`
	Canonical   string      `json:"canonical,omitempty" doc:"The page's canonical address, as written"`
	Icon        string      `json:"icon,omitempty" doc:"Where the page's icon is (its <link rel=icon>, else /favicon.ico), when it answers"`
	ThemeColor  string      `json:"themeColor,omitempty"`
	Image       *ShareImage `json:"image,omitempty" doc:"The share image (og:image, else twitter:image), when the page names one"`

	Checks []LinkCheck `json:"checks" doc:"What was checked, worst first within the list's order"`
	Tags   []PageTag   `json:"tags,omitempty" doc:"The page's title, meta and link tags that bear on sharing, as written"`

	// Compared is the report this one was compared with.
	Compared *LinkBaseline `json:"compared,omitempty" doc:"The earlier report the changes are against: the version before for production, production's live version for a preview"`
	Changes  []LinkChange  `json:"changes,omitempty" doc:"What changed against compared; empty when nothing did"`
}

// ShareImage is the share image.
type ShareImage struct {
	URL     string `json:"url" doc:"The image's address, resolved against the page"`
	Written string `json:"written,omitempty" doc:"The address as the page wrote it, when it differs (a relative path)"`
	Alt     string `json:"alt,omitempty" doc:"og:image:alt or twitter:image:alt"`
	Status  int    `json:"status,omitempty" doc:"What the image's address answered (200 when it was fetched)"`
	MIME    string `json:"mime,omitempty"`
	Bytes   int64  `json:"bytes,omitempty"`
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
	Hash    string `json:"hash,omitempty" doc:"Identifies the image's bytes: it changes only when they do"`
	// Thumb says whether the box kept a small copy (the dashboard's card).
	Thumb bool   `json:"thumb,omitempty" doc:"Whether the box kept a small copy to show"`
	Error string `json:"error,omitempty" doc:"Why the image couldn't be read"`
}

// LinkCheck is one thing the page gets right or wrong.
type LinkCheck struct {
	ID     string `json:"id" enum:"title,page-title,description,meta-description,site-name,image,image-size,twitter-card,x-title,x-description,x-image,canonical,robots,icon"`
	Label  string `json:"label" doc:"What was checked, for people"`
	Status string `json:"status" enum:"ok,warn,bad"`
	Detail string `json:"detail" doc:"What the box found"`
	Fix    string `json:"fix,omitempty" doc:"What to change, when it isn't ok"`
}

// PageTag is one tag as written.
type PageTag struct {
	Name  string `json:"name" doc:"title, a meta tag's name or property (og:image), or link rel=… (link canonical)"`
	Value string `json:"value"`
}

// LinkBaseline names an earlier report.
type LinkBaseline struct {
	Deploy  string `json:"deploy"`
	Version int    `json:"version,omitempty" doc:"Its production version"`
	Preview string `json:"preview,omitempty"`
	// Production: the comparison is a preview against production.
	Production bool `json:"production,omitempty" doc:"True when a preview is compared with production's live version"`
}

// LinkChange is one difference from the compared report.
type LinkChange struct {
	Field string `json:"field" doc:"What changed, for people (Title, Share image…)"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
	Worse bool   `json:"worse,omitempty" doc:"True when the change made the link preview worse (something went missing or a check got worse)"`
}

// LinkSummary is a report in brief, for deploy lists and the CLI.
type LinkSummary struct {
	Status    string    `json:"status" enum:"ok,warn,bad,unreadable"`
	CheckedAt time.Time `json:"checkedAt"`
	Title     string    `json:"title,omitempty"`
	Image     string    `json:"image,omitempty" doc:"The share image's address and size, e.g. https://x.com/og.png (1200×630)"`
	Bad       int       `json:"bad,omitempty" doc:"How many checks are bad"`
	Warn      int       `json:"warn,omitempty" doc:"How many checks are warnings"`
	Problems  []string  `json:"problems,omitempty" doc:"Each check that isn't ok: what it found and what to change"`
	Changes   []string  `json:"changes,omitempty" doc:"What changed against the version before (production) or production (a preview)"`
	Worse     bool      `json:"worse,omitempty" doc:"The deploy made the link preview worse than the one before"`
}

// Summary briefs a report.
func (r *LinkReport) Summary() *LinkSummary {
	s := &LinkSummary{Status: r.Status, CheckedAt: r.CheckedAt, Title: r.Title}
	if r.Status == StatusUnreadable {
		s.Problems = []string{r.Reason}
	}
	if im := r.Image; im != nil {
		s.Image = im.URL
		if im.Width > 0 {
			s.Image += fmt.Sprintf(" (%d×%d)", im.Width, im.Height)
		}
	}
	for _, c := range r.Checks {
		switch c.Status {
		case StatusBad:
			s.Bad++
		case StatusWarn:
			s.Warn++
		}
		if c.Status != StatusOK {
			p := c.Label + ": " + c.Detail
			if c.Fix != "" {
				p += " " + c.Fix
			}
			s.Problems = append(s.Problems, p)
		}
	}
	for _, c := range r.Changes {
		s.Changes = append(s.Changes, c.String())
		s.Worse = s.Worse || c.Worse
	}
	return s
}

func (c LinkChange) String() string {
	switch {
	case c.From == "":
		return c.Field + " added: " + c.To
	case c.To == "":
		return c.Field + " removed (was " + c.From + ")"
	}
	return c.Field + ": " + c.From + " → " + c.To
}

// Target is the deploy to check.
type Target struct {
	// Client reaches the deploy on the box (its instance, or its files). It
	// must not follow redirects: Check follows the ones that stay on the app.
	Client *http.Client
	// Base is the deploy's address on the box, e.g. http://127.0.0.1:41003.
	Base *url.URL
	// Page is the home page's public address, e.g. https://dibs.page/: the
	// Host the app sees, and what relative links resolve against.
	Page *url.URL
	// Hosts are all of the app's public hostnames: links on them are the app
	// and are fetched from Base.
	Hosts []string
	// BoxHost reports hostnames on the box that are not this app's public
	// address: other versions' and previews' addresses. A production page
	// that points its image or canonical there points at the wrong copy.
	BoxHost func(host string) bool
	// Outside fetches images on other sites; nil leaves them unfetched.
	Outside *http.Client
	// Preview: the deploy is a preview, whose canonical and robots are
	// expected to point elsewhere and keep it out of search.
	Preview bool
	// Timeout per request; fetchTimeout when zero.
	Timeout time.Duration
}

// Run reads the deploy's home page and checks it. It returns the report
// and a small copy of the share image (nil when there is none).
func Run(ctx context.Context, t Target, deploy string) (*LinkReport, *Thumb) {
	if t.Timeout == 0 {
		t.Timeout = fetchTimeout
	}
	r := &LinkReport{Deploy: deploy, CheckedAt: time.Now().UTC(), URL: t.Page.String()}
	res, err := t.get(ctx, t.Base.ResolveReference(&url.URL{Path: "/"}), maxHTML, "text/html,application/xhtml+xml;q=0.9,*/*;q=0.5")
	if err != nil {
		r.Status, r.Reason = StatusUnreadable, "the home page did not answer: "+err.Error()
		return r, nil
	}
	if mt, _, _ := mime.ParseMediaType(res.ctype); mt != "text/html" && mt != "application/xhtml+xml" {
		r.Status, r.Reason = StatusUnreadable, "the home page isn't a web page (it is "+orWord(mt, "untyped")+"), so links to it show no preview"
		return r, nil
	}
	page := parse(res.body)
	r.Tags = page.tags
	r.PageTitle = page.title
	r.MetaDesc = page.first("description")
	r.Title = first(page.first("og:title"), page.first("twitter:title"), page.title)
	r.Description = first(page.first("og:description"), page.first("twitter:description"), r.MetaDesc)
	r.SiteName = page.first("og:site_name")
	r.XTitle = first(page.first("twitter:title"), r.Title)
	r.XDesc = first(page.first("twitter:description"), r.Description)
	r.Card = page.first("twitter:card")
	r.Canonical = page.link("canonical")
	r.ThemeColor = page.first("theme-color")
	robots := strings.ToLower(page.first("robots") + "," + page.first("googlebot") + "," + strings.Join(res.header.Values("X-Robots-Tag"), ","))

	var thumb *Thumb
	if raw := first(page.first("og:image"), page.first("og:image:url"), page.first("og:image:secure_url"), page.first("twitter:image"), page.first("twitter:image:src")); raw != "" {
		r.Image, thumb = t.image(ctx, raw)
		r.Image.Alt = first(page.first("og:image:alt"), page.first("twitter:image:alt"))
	}
	r.Icon = t.icon(ctx, page)

	r.Checks = t.checks(r, robots)
	r.Status = StatusOK
	for _, c := range r.Checks {
		r.Status = worse(r.Status, c.Status)
	}
	return r, thumb
}

// worse is the worse of two statuses.
func worse(a, b string) string {
	rank := map[string]int{StatusOK: 0, StatusWarn: 1, StatusBad: 2, StatusUnreadable: 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// image fetches the share image and reads its size.
func (t Target) image(ctx context.Context, raw string) (*ShareImage, *Thumb) {
	im := &ShareImage{}
	u, err := t.Page.Parse(strings.TrimSpace(raw))
	if err != nil {
		im.URL, im.Error = raw, "not a web address"
		return im, nil
	}
	im.URL = u.String()
	if im.URL != raw {
		im.Written = raw
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		im.Error = "not a web address"
		return im, nil
	}
	var res *fetched
	if local := t.local(u); local != nil {
		res, err = t.get(ctx, local, MaxImageBytes, "image/*")
	} else if t.Outside != nil && !privateHost(u.Hostname()) {
		res, err = t.outside(ctx, u)
	} else {
		im.Error = "the box doesn't fetch images from " + u.Hostname()
		return im, nil
	}
	if err != nil {
		var se statusError
		if errors.As(err, &se) {
			im.Status = int(se)
		}
		im.Error = err.Error()
		return im, nil
	}
	im.Status = http.StatusOK
	im.Bytes = int64(len(res.body))
	sum := sha256.Sum256(res.body)
	im.Hash = hex.EncodeToString(sum[:8])
	im.MIME, _, _ = mime.ParseMediaType(res.ctype)
	if sniff := sniffImage(res.body); sniff != "" {
		im.MIME = sniff
	}
	if !strings.HasPrefix(im.MIME, "image/") {
		im.Error = "it is " + orWord(im.MIME, "untyped") + ", not an image"
		return im, nil
	}
	if im.MIME == "image/svg+xml" {
		return im, nil // no size to read; the check says unfurlers skip it
	}
	th, w, h, err := makeThumb(res.body)
	im.Width, im.Height = w, h
	if err != nil {
		if w == 0 {
			im.Error = err.Error()
		}
		return im, nil
	}
	th.Hash = im.Hash
	im.Thumb = true
	return im, th
}

// icon is where the page's icon is, when it answers.
func (t Target) icon(ctx context.Context, p *parsed) string {
	cands := p.icons
	cands = append(cands, "/favicon.ico")
	for i, raw := range cands {
		if i >= 3 {
			break
		}
		u, err := t.Page.Parse(raw)
		if err != nil {
			continue
		}
		if strings.HasPrefix(raw, "data:") {
			return "inline"
		}
		local := t.local(u)
		if local == nil {
			if u.Scheme == "http" || u.Scheme == "https" {
				return u.String() // on another site: taken as is
			}
			continue
		}
		if res, err := t.get(ctx, local, MaxImageBytes, "image/*"); err == nil && len(res.body) > 0 {
			return u.String()
		}
	}
	return ""
}

// ---- fetching ----

type fetched struct {
	body   []byte
	ctype  string
	header http.Header
	at     *url.URL
}

type statusError int

func (e statusError) Error() string { return fmt.Sprintf("it answered %d", int(e)) }

// get fetches u from the app, following redirects that stay on it.
func (t Target) get(ctx context.Context, u *url.URL, limit int64, accept string) (*fetched, error) {
	for hop := 0; ; hop++ {
		res, loc, err := t.once(ctx, t.Client, u, limit, accept, true)
		if err != nil {
			return nil, err
		}
		if loc == "" {
			res.at = u
			return res, nil
		}
		if hop >= maxRedirects {
			return nil, errors.New("too many redirects")
		}
		next, err := u.Parse(loc)
		if err != nil {
			return nil, errors.New("a redirect to a bad address")
		}
		if next.Host == u.Host && next.Scheme == u.Scheme {
			u = next
		} else if u = t.local(next); u == nil {
			return nil, errors.New("it redirects away from the app, to " + next.Host)
		}
	}
}

// outside fetches an image on another site.
func (t Target) outside(ctx context.Context, u *url.URL) (*fetched, error) {
	for hop := 0; ; hop++ {
		res, loc, err := t.once(ctx, t.Outside, u, MaxImageBytes, "image/*", false)
		if err != nil {
			return nil, err
		}
		if loc == "" {
			res.at = u
			return res, nil
		}
		if hop >= maxRedirects {
			return nil, errors.New("too many redirects")
		}
		next, err := u.Parse(loc)
		if err != nil || (next.Scheme != "http" && next.Scheme != "https") || privateHost(next.Hostname()) {
			return nil, errors.New("a redirect to an address the box doesn't fetch")
		}
		u = next
	}
}

func (t Target) once(ctx context.Context, c *http.Client, u *url.URL, limit int64, accept string, local bool) (*fetched, string, error) {
	ctx, cancel := context.WithTimeout(ctx, t.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", accept)
	if local && t.Page != nil {
		req.Host = t.Page.Host // what the app would see from a visitor
	}
	res, err := c.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", fmt.Errorf("it took longer than %s", t.Timeout)
		}
		return nil, "", trimErr(err)
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode >= 300 && res.StatusCode < 400 && res.Header.Get("Location") != "":
		return nil, res.Header.Get("Location"), nil
	case res.StatusCode != http.StatusOK:
		return nil, "", statusError(res.StatusCode)
	}
	if res.ContentLength > limit {
		return nil, "", fmt.Errorf("it is %s, over the %s unfurlers take", size(res.ContentLength), size(limit))
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", fmt.Errorf("it took longer than %s", t.Timeout)
		}
		return nil, "", trimErr(err)
	}
	if int64(len(b)) > limit {
		return nil, "", fmt.Errorf("it is over the %s unfurlers take", size(limit))
	}
	return &fetched{body: b, ctype: res.Header.Get("Content-Type"), header: res.Header}, "", nil
}

// trimErr drops the method and address Go puts in front of a client error.
func trimErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// local maps a link to the deploy's own address, or nil when it is on
// another site. Links on the app's public hostnames count as the app.
func (t Target) local(u *url.URL) *url.URL {
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil
	}
	if u.Host == t.Base.Host && u.Scheme == t.Base.Scheme {
		return u
	}
	host := strings.ToLower(u.Hostname())
	if t.Page != nil && strings.EqualFold(t.Page.Hostname(), host) {
		return t.rebase(u)
	}
	for _, h := range t.Hosts {
		if strings.EqualFold(h, host) {
			return t.rebase(u)
		}
	}
	return nil
}

func (t Target) rebase(u *url.URL) *url.URL {
	c := *u
	c.Scheme, c.Host, c.User = t.Base.Scheme, t.Base.Host, nil
	return &c
}

func size(n int64) string {
	if n >= 1<<20 {
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KB", n>>10)
}

func orWord(s, w string) string {
	if s == "" {
		return w
	}
	return s
}

func first(vs ...string) string {
	for _, v := range vs {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// sniffImage names an image's type from its first bytes ("" when unknown).
func sniffImage(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(b, []byte("\xff\xd8\xff")):
		return "image/jpeg"
	case bytes.HasPrefix(b, []byte("GIF8")):
		return "image/gif"
	case len(b) > 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	case len(b) > 12 && string(b[4:8]) == "ftyp" && (string(b[8:12]) == "avif" || string(b[8:12]) == "avis"):
		return "image/avif"
	}
	head := bytes.ToLower(bytes.TrimSpace(b[:min(len(b), 512)]))
	if bytes.HasPrefix(head, []byte("<svg")) || (bytes.HasPrefix(head, []byte("<?xml")) && bytes.Contains(head, []byte("<svg"))) {
		return "image/svg+xml"
	}
	return ""
}
