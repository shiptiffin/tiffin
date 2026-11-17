package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The box signs in to Google as a person. Whoever pressed Connect with
// Google gave the box's own Google OAuth app (the sign-in keys in Box
// settings › Sign-in providers) a refresh token for their Search Console.
// With it the box verifies their domains, adds them to their Search
// Console, submits sitemaps and reads search data back, all as them.

const (
	gScopeSearch  = "https://www.googleapis.com/auth/webmasters"
	gScopeVerify  = "https://www.googleapis.com/auth/siteverification"
	googleScopes  = "openid email " + gScopeSearch + " " + gScopeVerify
	googleAuth    = "https://accounts.google.com/o/oauth2/v2/auth"
	googleToken   = "https://oauth2.googleapis.com/token"
	googleRevoke  = "https://oauth2.googleapis.com/revoke"
	verifyAPI     = "https://www.googleapis.com/siteVerification/v1"
	webmastersAPI = "https://www.googleapis.com/webmasters/v3"
	searchAPI     = "https://searchconsole.googleapis.com/v1"
)

// googleApp is the OAuth app the box asks Google with.
type googleApp struct {
	ClientID, ClientSecret, RedirectURL string
}

// project is the app's Google Cloud project number: the client ID starts
// with it ("334146733800-abc.apps.googleusercontent.com").
func (a googleApp) project() string {
	n, _, ok := strings.Cut(a.ClientID, "-")
	if !ok || n == "" || strings.Trim(n, "0123456789") != "" {
		return ""
	}
	return n
}

var errNoGoogleKeys = errors.New("the box has no Google sign-in keys (Box settings › Sign-in providers)")

// gclient calls Google's APIs as the person who connected.
type gclient struct {
	refresh string
	signin  func(context.Context) (gsignin, error) // the app that renews the access
	http    *http.Client
	// base overrides the API hosts in tests ("" = Google's).
	base string

	mu      sync.Mutex
	token   string
	expires time.Time
}

func (g *gclient) url(u string) string {
	return rebase(g.base, u)
}

// rebase moves a Google URL to base (tests), keeping its path and query.
func rebase(base, u string) string {
	if base == "" {
		return u
	}
	pu, _ := url.Parse(u)
	out := strings.TrimSuffix(base, "/") + pu.Path
	if pu.RawQuery != "" {
		out += "?" + pu.RawQuery
	}
	return out
}

// gtoken is an answer of Google's token endpoint.
type gtoken struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	IDToken      string `json:"id_token"`
	Email        string `json:"email"` // from the control plane of a managed box
	Error        string `json:"error"`
	Desc         string `json:"error_description"`
}

// googleTokenCall posts a grant to Google's token endpoint.
func googleTokenCall(ctx context.Context, hc *http.Client, tokenURL string, form url.Values) (*gtoken, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("google sign-in: %w", err)
	}
	defer resp.Body.Close()
	var out gtoken
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
	if resp.StatusCode != 200 || out.AccessToken == "" {
		msg := out.Desc
		if msg == "" {
			msg = out.Error
		}
		if msg == "" {
			msg = resp.Status
		}
		if out.Error == "invalid_grant" {
			msg = "Google no longer accepts the connection (" + msg + "). Connect with Google again"
		}
		return nil, &GoogleError{Status: resp.StatusCode, Message: "sign-in refused: " + msg}
	}
	return &out, nil
}

// accessToken trades the refresh token for an hour's token.
func (g *gclient) accessToken(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.token != "" && time.Until(g.expires) > time.Minute {
		return g.token, nil
	}
	s, err := g.signin(ctx)
	if errors.Is(err, errNoGoogleKeys) {
		return "", &GoogleError{Status: 401, Message: "sign-in refused: " + err.Error()}
	} else if err != nil {
		return "", err
	}
	now := time.Now()
	out, err := s.renew(ctx, g.refresh)
	if err != nil {
		return "", err
	}
	g.token, g.expires = out.AccessToken, now.Add(time.Duration(out.ExpiresIn)*time.Second)
	return g.token, nil
}

// GoogleError is an error answer from a Google API.
type GoogleError struct {
	Status  int
	Message string
}

func (e *GoogleError) Error() string {
	return fmt.Sprintf("Google answered %d: %s", e.Status, e.Message)
}

// call sends a JSON request and decodes a JSON answer into out (if any).
func (g *gclient) call(ctx context.Context, method, u string, body, out any) error {
	tok, err := g.accessToken(ctx)
	if err != nil {
		return err
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.url(u), rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return fmt.Errorf("google could not be reached: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := e.Error.Message
		if msg == "" {
			msg = strings.TrimSpace(string(raw[:min(len(raw), 200)]))
		}
		return &GoogleError{Status: resp.StatusCode, Message: msg}
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

type gsite struct {
	Type       string `json:"type"` // INET_DOMAIN or SITE
	Identifier string `json:"identifier"`
}

// verificationToken asks Google for the value that proves ownership: a TXT
// record's value (DNS_TXT) or a file name (FILE).
func (g *gclient) verificationToken(ctx context.Context, site gsite, method string) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	err := g.call(ctx, http.MethodPost, verifyAPI+"/token", map[string]any{"site": site, "verificationMethod": method}, &out)
	return out.Token, err
}

// verify asks Google to check the token now; the person becomes a verified
// owner of the site.
func (g *gclient) verify(ctx context.Context, site gsite, method string) error {
	return g.call(ctx, http.MethodPost, verifyAPI+"/webResource?verificationMethod="+method, map[string]any{"site": site}, nil)
}

// owns reports whether the person is already a verified owner of the site
// (they verified it themselves once, or another owner added them): Google
// answers for a site only to its owners.
func (g *gclient) owns(ctx context.Context, site gsite) (bool, error) {
	id := site.Identifier
	if site.Type == "INET_DOMAIN" {
		id = "dns://" + site.Identifier
	}
	err := g.call(ctx, http.MethodGet, verifyAPI+"/webResource/"+url.PathEscape(id), nil, nil)
	var ge *GoogleError
	if errors.As(err, &ge) && (ge.Status == 403 || ge.Status == 404) {
		return false, nil
	}
	return err == nil, err
}

// addSite adds a property to the person's Search Console.
func (g *gclient) addSite(ctx context.Context, siteURL string) error {
	return g.call(ctx, http.MethodPut, webmastersAPI+"/sites/"+url.PathEscape(siteURL), nil, nil)
}

type gsiteEntry struct {
	SiteURL         string `json:"siteUrl"`
	PermissionLevel string `json:"permissionLevel"`
}

func (g *gclient) sites(ctx context.Context) ([]gsiteEntry, error) {
	var out struct {
		SiteEntry []gsiteEntry `json:"siteEntry"`
	}
	err := g.call(ctx, http.MethodGet, webmastersAPI+"/sites", nil, &out)
	return out.SiteEntry, err
}

// submitSitemap tells Google where the sitemap is.
func (g *gclient) submitSitemap(ctx context.Context, siteURL, feed string) error {
	return g.call(ctx, http.MethodPut, webmastersAPI+"/sites/"+url.PathEscape(siteURL)+"/sitemaps/"+url.PathEscape(feed), nil, nil)
}

type gRow struct {
	Keys        []string `json:"keys"`
	Clicks      float64  `json:"clicks"`
	Impressions float64  `json:"impressions"`
	CTR         float64  `json:"ctr"`
	Position    float64  `json:"position"`
}

// query runs a Search Analytics query (web search, final and fresh data).
func (g *gclient) query(ctx context.Context, siteURL string, from, to time.Time, dims []string, filter map[string]any, limit int) ([]gRow, error) {
	rows, _, err := g.queryAt(ctx, siteURL, from, to, dims, filter, limit, 0)
	return rows, err
}

// queryAt is query from row start on (Google pages its answers). It also
// returns the first day Google is still filling in (its numbers for that
// day and later will grow), when Google says.
func (g *gclient) queryAt(ctx context.Context, siteURL string, from, to time.Time, dims []string, filter map[string]any, limit, start int) ([]gRow, string, error) {
	body := map[string]any{
		"startDate": from.Format(time.DateOnly), "endDate": to.Format(time.DateOnly),
		"dimensions": dims, "rowLimit": limit, "dataState": "all", "type": "web",
	}
	if start > 0 {
		body["startRow"] = start
	}
	if filter != nil {
		body["dimensionFilterGroups"] = []any{map[string]any{"filters": []any{filter}}}
	}
	var out struct {
		Rows     []gRow `json:"rows"`
		Metadata struct {
			FirstIncompleteDate string `json:"firstIncompleteDate"`
		} `json:"metadata"`
	}
	err := g.call(ctx, http.MethodPost, webmastersAPI+"/sites/"+url.PathEscape(siteURL)+"/searchAnalytics/query", body, &out)
	return out.Rows, out.Metadata.FirstIncompleteDate, err
}

// GInspection is what Google's URL Inspection says about one page.
type GInspection struct {
	URL           string   `json:"url,omitempty" doc:"The page asked about"`
	Verdict       string   `json:"verdict" doc:"PASS (indexed), NEUTRAL (not indexed, no problem) or FAIL"`
	Coverage      string   `json:"coverage" doc:"Google's words: \"Submitted and indexed\", \"Crawled - currently not indexed\", \"Discovered - currently not indexed\"..."`
	LastCrawl     string   `json:"lastCrawl,omitempty"`
	CrawledAs     string   `json:"crawledAs,omitempty" doc:"MOBILE or DESKTOP: the Googlebot that crawled it last"`
	RobotsTxt     string   `json:"robotsTxt,omitempty" doc:"Missing, like the two states below, when Google doesn't say"`
	Indexing      string   `json:"indexing,omitempty"`
	PageFetch     string   `json:"pageFetch,omitempty"`
	GoogleCanon   string   `json:"googleCanonical,omitempty"`
	UserCanonical string   `json:"userCanonical,omitempty"`
	Sitemaps      []string `json:"sitemaps,omitempty" doc:"The sitemaps Google found the page in (Google's sitemap field)"`
	ReferringURLs []string `json:"referringUrls,omitempty" doc:"Pages Google knows link to it; a page Google only discovered often has none"`
	InSitemap     *bool    `json:"inSitemap,omitempty" doc:"Whether the sitemap the box last read on the host lists the page; missing when the host has no sitemap"`
	Warnings      []string `json:"warnings,omitempty" doc:"What makes the answer less useful: the page isn't in the sitemap, or the sitemap spells it differently"`
}

// inspect asks URL Inspection about a page (2,000 a day per property).
func (g *gclient) inspect(ctx context.Context, siteURL, page string) (*GInspection, error) {
	var out struct {
		InspectionResult struct {
			IndexStatusResult struct {
				Verdict         string   `json:"verdict"`
				CoverageState   string   `json:"coverageState"`
				RobotsTxtState  string   `json:"robotsTxtState"`
				IndexingState   string   `json:"indexingState"`
				LastCrawlTime   string   `json:"lastCrawlTime"`
				PageFetchState  string   `json:"pageFetchState"`
				GoogleCanonical string   `json:"googleCanonical"`
				UserCanonical   string   `json:"userCanonical"`
				CrawledAs       string   `json:"crawledAs"`
				Sitemap         []string `json:"sitemap"`
				ReferringURLs   []string `json:"referringUrls"`
			} `json:"indexStatusResult"`
		} `json:"inspectionResult"`
	}
	if err := g.call(ctx, http.MethodPost, searchAPI+"/urlInspection/index:inspect", map[string]any{"inspectionUrl": page, "siteUrl": siteURL}, &out); err != nil {
		return nil, err
	}
	r := out.InspectionResult.IndexStatusResult
	return &GInspection{Verdict: r.Verdict, Coverage: r.CoverageState, LastCrawl: r.LastCrawlTime, CrawledAs: said(r.CrawledAs),
		RobotsTxt: said(r.RobotsTxtState), Indexing: said(r.IndexingState), PageFetch: said(r.PageFetchState),
		GoogleCanon: r.GoogleCanonical, UserCanonical: r.UserCanonical, Sitemaps: r.Sitemap, ReferringURLs: r.ReferringURLs}, nil
}

// said is one of Google's states, or "" for its *_UNSPECIFIED (Google doesn't say).
func said(state string) string {
	if strings.HasSuffix(state, "_UNSPECIFIED") {
		return ""
	}
	return state
}

// GSitemap is what Google says about a submitted sitemap.
type GSitemap struct {
	URL       string `json:"url"`
	Submitted string `json:"submitted,omitempty" doc:"When it was last submitted"`
	LastRead  string `json:"lastRead,omitempty" doc:"When Google last downloaded it"`
	Pending   bool   `json:"pending,omitempty" doc:"Google has not processed it yet"`
	Pages     int    `json:"pages" doc:"Web pages Google found in it"`
	Errors    int    `json:"errors"`
	Warnings  int    `json:"warnings"`
}

// sitemapStatus reads a submitted sitemap's status.
func (g *gclient) sitemapStatus(ctx context.Context, siteURL, feed string) (*GSitemap, error) {
	var out struct {
		LastSubmitted  string `json:"lastSubmitted"`
		LastDownloaded string `json:"lastDownloaded"`
		IsPending      bool   `json:"isPending"`
		Errors         string `json:"errors"`
		Warnings       string `json:"warnings"`
		Contents       []struct {
			Type      string `json:"type"`
			Submitted string `json:"submitted"`
		} `json:"contents"`
	}
	if err := g.call(ctx, http.MethodGet, webmastersAPI+"/sites/"+url.PathEscape(siteURL)+"/sitemaps/"+url.PathEscape(feed), nil, &out); err != nil {
		return nil, err
	}
	sm := &GSitemap{URL: feed, Submitted: out.LastSubmitted, LastRead: out.LastDownloaded, Pending: out.IsPending}
	sm.Errors, _ = strconv.Atoi(out.Errors)
	sm.Warnings, _ = strconv.Atoi(out.Warnings)
	for _, c := range out.Contents {
		if c.Type == "web" {
			n, _ := strconv.Atoi(c.Submitted)
			sm.Pages += n
		}
	}
	return sm, nil
}
