package projicon

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// Inference: after a production deploy the box asks the running app for
// its icon, on the box's own network, the way a browser would find it: the
// home page's <link rel="icon">, apple-touch-icon and web manifest icons,
// then /favicon.ico. Only the app itself is asked: links to other sites
// are ignored, and so are redirects that leave the app.

// Limits on inference.
const (
	fetchTimeout = 3 * time.Second
	maxHTML      = 1 << 20
	maxManifest  = 256 << 10
	maxRedirects = 3
	maxFetches   = 8
)

// Fetcher reaches one running app.
type Fetcher struct {
	// Client sends requests to the app (its instance, or its files).
	// Redirects are followed by Infer itself, so Client must not follow them.
	Client *http.Client
	// Base is the app's address on the box, e.g. http://127.0.0.1:41003.
	Base *url.URL
	// Hosts are the app's public hostnames: absolute links on them are the
	// app too, and are fetched from Base.
	Hosts []string
	// Timeout per request; fetchTimeout when zero.
	Timeout time.Duration
}

// Result is what inference found.
type Result struct {
	// Icon is the best icon found, nil when none.
	Icon *Image
	// Raster is a PNG of the same icon for places SVG can't go (email),
	// when Icon is an SVG and the app has a raster icon as well.
	Raster *Image
	// Source is where Icon came from: a path on the app ("/icon.svg"), or
	// "inline" for a data: URL.
	Source string
	// Reason says why there is no icon, for people.
	Reason string
	// None: the app answered and has no icon (or no web pages), so an icon
	// found earlier is gone. False when the app didn't answer.
	None bool
}

// candidate is one place an icon may be.
type candidate struct {
	url      *url.URL // nil for an inline data: URL
	data     []byte   // inline
	declared int      // the size the page declares, 0 when it doesn't
	svg      bool     // declared or named as SVG
	rel      string   // icon, apple-touch-icon, manifest, default
	maskable bool
}

// score orders candidates before fetching: SVG first, then big rasters.
func (c candidate) score() int {
	s := c.declared
	switch {
	case c.svg:
		s = 10000
	case s == 0 && c.rel == "apple-touch-icon":
		s = 180 // its usual size
	case s == 0 && c.rel == "default":
		s = 16
	case s == 0:
		s = 32
	}
	if s > 1024 {
		s = 1024 // bigger isn't better past this, just heavier
	}
	if c.maskable {
		s -= 200 // padded for a mask: fine, not first choice
	}
	if c.rel == "default" {
		s -= 1 // a declared icon wins a tie with /favicon.ico
	}
	return s
}

// quality ranks what was actually fetched.
func quality(i *Image) int {
	if i.SVG() {
		return 10000
	}
	s := min(i.Side(), 1024)
	if i.Width*10 < i.Height*9 || i.Height*10 < i.Width*9 {
		s /= 2 // not square: padded, so the mark is smaller
	}
	return s
}

// Infer looks for the app's icon.
func Infer(ctx context.Context, f Fetcher) (Result, error) {
	if f.Timeout == 0 {
		f.Timeout = fetchTimeout
	}
	page, ctype, at, err := f.get(ctx, f.Base.ResolveReference(&url.URL{Path: "/"}), maxHTML)
	if err != nil {
		return Result{Reason: "the home page did not answer: " + err.Error()}, nil
	}
	if mt, _, _ := mime.ParseMediaType(ctype); mt != "text/html" && mt != "application/xhtml+xml" {
		return Result{Reason: "the home page isn't HTML (an API keeps the letter icon)", None: true}, nil
	}
	cands, manifests := f.parseHead(page, at)
	for _, m := range manifests {
		cands = append(cands, f.manifestIcons(ctx, m)...)
	}
	cands = append(cands, candidate{url: f.Base.ResolveReference(&url.URL{Path: "/favicon.ico"}), rel: "default"})
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].score() > cands[j].score() })

	var best, raster *Image
	var source string
	var first error // the best candidate's failure says the most
	fail := func(err error) {
		if first == nil {
			first = err
		}
	}
	seen := map[string]bool{}
	fetched := 0
	for _, c := range cands {
		if fetched >= maxFetches || ctx.Err() != nil {
			break
		}
		if best != nil && quality(best) >= GoodSide && (!best.SVG() || (raster != nil && raster.Side() >= 96)) {
			break // as good as it gets
		}
		if best != nil && best.SVG() && c.svg {
			continue // one SVG is enough; what's missing is a raster
		}
		key := "inline:" + string(c.data)
		if c.url != nil {
			key = c.url.String()
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		b, from := c.data, "inline"
		if c.url != nil {
			fetched++
			var ct string
			var at *url.URL
			b, ct, at, err = f.get(ctx, c.url, MaxBytes)
			if err != nil {
				fail(err)
				continue
			}
			from = at.Path
			if mt, _, _ := mime.ParseMediaType(ct); !strings.HasPrefix(mt, "image/") {
				fail(fmt.Errorf("%s is %s, not an image", from, orWord(mt, "untyped")))
				continue
			}
		}
		img, err := Normalize(b)
		if err != nil {
			fail(fmt.Errorf("%s: %w", from, err))
			continue
		}
		if !img.SVG() && (raster == nil || quality(img) > quality(raster)) {
			raster = img
		}
		if best == nil || quality(img) > quality(best) {
			best, source = img, from
		}
	}
	if best == nil {
		r := Result{Reason: "the app has no icon the box could use", None: true}
		if first != nil {
			r.Reason += " (" + first.Error() + ")"
		}
		return r, nil
	}
	res := Result{Icon: best, Source: source}
	if best.SVG() && raster != nil {
		res.Raster = raster
	}
	return res, nil
}

func orWord(s, w string) string {
	if s == "" {
		return w
	}
	return s
}

// get fetches u from the app, following redirects that stay on it, and
// reads at most limit bytes. It returns the body, its Content-Type and
// the URL it finally came from.
func (f Fetcher) get(ctx context.Context, u *url.URL, limit int64) ([]byte, string, *url.URL, error) {
	for hop := 0; ; hop++ {
		b, ct, loc, err := f.once(ctx, u, limit)
		if err != nil {
			return nil, "", nil, err
		}
		if loc == "" {
			return b, ct, u, nil
		}
		if hop >= maxRedirects {
			return nil, "", nil, errors.New("too many redirects")
		}
		next, err := u.Parse(loc)
		if err != nil {
			return nil, "", nil, errors.New("a redirect to a bad address")
		}
		if u = f.local(next); u == nil {
			return nil, "", nil, errors.New("a redirect away from the app")
		}
	}
}

func (f Fetcher) once(ctx context.Context, u *url.URL, limit int64) ([]byte, string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, f.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", "", err
	}
	req.Header.Set("User-Agent", "tiffin-icon")
	req.Header.Set("Accept", "text/html,image/svg+xml,image/*,application/manifest+json,*/*;q=0.5")
	if len(f.Hosts) > 0 {
		req.Host = f.Hosts[0] // what the app would see from a visitor
	}
	res, err := f.Client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", "", fmt.Errorf("%s took longer than %s", u.Path, f.Timeout)
		}
		return nil, "", "", fmt.Errorf("%s: %w", u.Path, err)
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode >= 300 && res.StatusCode < 400 && res.Header.Get("Location") != "":
		return nil, "", res.Header.Get("Location"), nil
	case res.StatusCode != http.StatusOK:
		return nil, "", "", fmt.Errorf("%s answered %d", u.Path, res.StatusCode)
	}
	if res.ContentLength > limit {
		return nil, "", "", fmt.Errorf("%s is larger than %d KB", u.Path, limit>>10)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", "", fmt.Errorf("%s took longer than %s", u.Path, f.Timeout)
		}
		return nil, "", "", fmt.Errorf("%s: %w", u.Path, err)
	}
	if int64(len(b)) > limit {
		return nil, "", "", fmt.Errorf("%s is larger than %d KB", u.Path, limit>>10)
	}
	return b, res.Header.Get("Content-Type"), "", nil
}

// local maps a link to the app's own address, or nil when it is on
// another site. Links on the app's public hostnames count as the app.
func (f Fetcher) local(u *url.URL) *url.URL {
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil
	}
	if u.Host == f.Base.Host && u.Scheme == f.Base.Scheme {
		return u
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range f.Hosts {
		if strings.EqualFold(h, host) {
			c := *u
			c.Scheme, c.Host, c.User = f.Base.Scheme, f.Base.Host, nil
			return &c
		}
	}
	return nil
}

// parseHead reads the page up to <body> for icon links and manifests.
func (f Fetcher) parseHead(page []byte, at *url.URL) (cands []candidate, manifests []*url.URL) {
	base := at
	z := html.NewTokenizer(bytes.NewReader(page))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			continue
		}
		name, hasAttr := z.TagName()
		switch string(name) {
		case "body":
			return
		case "base", "link":
		default:
			continue
		}
		attrs := map[string]string{}
		for hasAttr {
			var k, v []byte
			k, v, hasAttr = z.TagAttr()
			attrs[string(k)] = string(v)
		}
		href := strings.TrimSpace(attrs["href"])
		if href == "" {
			continue
		}
		if string(name) == "base" {
			if u, err := at.Parse(href); err == nil && f.local(u) != nil {
				base = u
			}
			continue
		}
		rels := strings.Fields(strings.ToLower(attrs["rel"]))
		rel := ""
		for _, r := range rels {
			switch r {
			case "manifest":
				rel = "manifest"
			case "apple-touch-icon", "apple-touch-icon-precomposed":
				rel = "apple-touch-icon"
			case "icon":
				if rel == "" {
					rel = "icon"
				}
			}
		}
		if rel == "" {
			continue // mask-icon (one colour, for Safari's pinned tabs) and the rest
		}
		if rel == "manifest" {
			if u, err := base.Parse(href); err == nil {
				if l := f.local(u); l != nil {
					manifests = append(manifests, l)
				}
			}
			continue
		}
		c := candidate{rel: rel, declared: largestSize(attrs["sizes"]), svg: strings.Contains(attrs["type"], "svg")}
		if strings.HasPrefix(strings.ToLower(href), "data:") {
			b, svg, ok := dataURL(href)
			if !ok {
				continue
			}
			c.data, c.svg = b, c.svg || svg
		} else {
			u, err := base.Parse(href)
			if err != nil {
				continue
			}
			if c.url = f.local(u); c.url == nil {
				continue
			}
			c.svg = c.svg || strings.HasSuffix(strings.ToLower(c.url.Path), ".svg")
		}
		if strings.EqualFold(attrs["sizes"], "any") && c.svg {
			c.declared = 0
		}
		cands = append(cands, c)
	}
}

// manifestIcons reads a web app manifest's icons.
func (f Fetcher) manifestIcons(ctx context.Context, u *url.URL) []candidate {
	b, _, at, err := f.get(ctx, u, maxManifest)
	if err != nil {
		return nil
	}
	var m struct {
		Icons []struct {
			Src     string `json:"src"`
			Sizes   string `json:"sizes"`
			Type    string `json:"type"`
			Purpose string `json:"purpose"`
		} `json:"icons"`
	}
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	var out []candidate
	for _, ic := range m.Icons {
		purpose := strings.Fields(strings.ToLower(ic.Purpose))
		if len(purpose) > 0 && !contains(purpose, "any") && !contains(purpose, "maskable") {
			continue // monochrome only
		}
		s, err := at.Parse(strings.TrimSpace(ic.Src))
		if err != nil {
			continue
		}
		l := f.local(s)
		if l == nil {
			continue
		}
		out = append(out, candidate{
			url: l, rel: "manifest", declared: largestSize(ic.Sizes),
			svg:      strings.Contains(ic.Type, "svg") || strings.HasSuffix(strings.ToLower(l.Path), ".svg"),
			maskable: len(purpose) > 0 && !contains(purpose, "any"),
		})
	}
	return out
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// largestSize reads sizes="16x16 32x32" as 32 (the smaller side of the
// largest).
func largestSize(s string) int {
	best := 0
	for _, f := range strings.Fields(strings.ToLower(s)) {
		w, h, ok := strings.Cut(f, "x")
		if !ok {
			continue
		}
		wi, e1 := strconv.Atoi(w)
		hi, e2 := strconv.Atoi(h)
		if e1 == nil && e2 == nil {
			best = max(best, min(wi, hi))
		}
	}
	return best
}

// dataURL decodes an inline image link (data:image/png;base64,...).
func dataURL(s string) (b []byte, svg, ok bool) {
	meta, payload, found := strings.Cut(s[len("data:"):], ",")
	if !found || len(payload) > MaxBytes*4/3+4 {
		return nil, false, false
	}
	parts := strings.Split(meta, ";")
	if !strings.HasPrefix(strings.ToLower(parts[0]), "image/") {
		return nil, false, false
	}
	svg = strings.Contains(strings.ToLower(parts[0]), "svg")
	if parts[len(parts)-1] == "base64" {
		b, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return nil, false, false
		}
		return b, svg, true
	}
	p, err := url.PathUnescape(payload)
	if err != nil {
		return nil, false, false
	}
	return []byte(p), svg, true
}
