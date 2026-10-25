package search

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// A Google service account: a robot account in the owner's own Google
// Cloud project, which the box signs in as (a key file, no browser, no
// app review). It verifies the owner's domains through DNS, adds the owner
// as an owner too (so they show in the owner's own Search Console), submits
// sitemaps and reads search data back.

const (
	googleScopes  = "https://www.googleapis.com/auth/webmasters https://www.googleapis.com/auth/siteverification"
	googleToken   = "https://oauth2.googleapis.com/token"
	verifyAPI     = "https://www.googleapis.com/siteVerification/v1"
	webmastersAPI = "https://www.googleapis.com/webmasters/v3"
	searchAPI     = "https://searchconsole.googleapis.com/v1"
)

// googleKey is the parts of a service account key file the box uses.
type googleKey struct {
	Type        string `json:"type"`
	ProjectID   string `json:"project_id"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

func parseGoogleKey(raw []byte) (*googleKey, *rsa.PrivateKey, error) {
	var k googleKey
	if err := json.Unmarshal(raw, &k); err != nil {
		return nil, nil, errors.New("not a JSON key file")
	}
	if k.Type != "service_account" || k.ClientEmail == "" || k.PrivateKey == "" {
		return nil, nil, errors.New(`not a service account key (download it from IAM › Service accounts › Keys › Add key › JSON)`)
	}
	block, _ := pem.Decode([]byte(k.PrivateKey))
	if block == nil {
		return nil, nil, errors.New("the key file's private_key is not a PEM key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("the key file's private_key: %w", err)
	}
	rk, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, nil, errors.New("the key file's private_key is not an RSA key")
	}
	if k.TokenURI == "" {
		k.TokenURI = googleToken
	}
	return &k, rk, nil
}

// gclient calls Google's APIs as the service account.
type gclient struct {
	key  *googleKey
	rsa  *rsa.PrivateKey
	http *http.Client
	// base overrides the API hosts in tests ("" = Google's).
	base string

	mu      sync.Mutex
	token   string
	expires time.Time
}

func newGClient(raw []byte, hc *http.Client) (*gclient, error) {
	k, rk, err := parseGoogleKey(raw)
	if err != nil {
		return nil, err
	}
	return &gclient{key: k, rsa: rk, http: hc}, nil
}

func (g *gclient) url(u string) string {
	if g.base == "" {
		return u
	}
	pu, _ := url.Parse(u)
	return strings.TrimSuffix(g.base, "/") + pu.Path + func() string {
		if pu.RawQuery != "" {
			return "?" + pu.RawQuery
		}
		return ""
	}()
}

// accessToken signs a JWT with the key and trades it for an hour's token.
func (g *gclient) accessToken(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.token != "" && time.Until(g.expires) > time.Minute {
		return g.token, nil
	}
	now := time.Now()
	enc := base64.RawURLEncoding
	head := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{
		"iss": g.key.ClientEmail, "scope": googleScopes, "aud": g.key.TokenURI,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})
	signing := head + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, g.rsa, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {signing + "." + enc.EncodeToString(sig)}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, g.url(g.key.TokenURI), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := g.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("Google sign-in: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
		Desc        string `json:"error_description"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
	if resp.StatusCode != 200 || out.AccessToken == "" {
		msg := out.Desc
		if msg == "" {
			msg = out.Error
		}
		if msg == "" {
			msg = resp.Status
		}
		return "", &GoogleError{Status: resp.StatusCode, Message: "sign-in refused: " + msg}
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
		return fmt.Errorf("Google could not be reached: %w", err)
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

// verify asks Google to check the token now; the service account becomes
// an owner, and owners (the person's Google account) are added with it.
func (g *gclient) verify(ctx context.Context, site gsite, method string, owners []string) error {
	body := map[string]any{"site": site}
	if len(owners) > 0 {
		body["owners"] = append([]string{g.key.ClientEmail}, owners...)
	}
	return g.call(ctx, http.MethodPost, verifyAPI+"/webResource?verificationMethod="+method, body, nil)
}

// addSite adds a property to the service account's Search Console.
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
	body := map[string]any{
		"startDate": from.Format(time.DateOnly), "endDate": to.Format(time.DateOnly),
		"dimensions": dims, "rowLimit": limit, "dataState": "all", "type": "web",
	}
	if filter != nil {
		body["dimensionFilterGroups"] = []any{map[string]any{"filters": []any{filter}}}
	}
	var out struct {
		Rows []gRow `json:"rows"`
	}
	err := g.call(ctx, http.MethodPost, webmastersAPI+"/sites/"+url.PathEscape(siteURL)+"/searchAnalytics/query", body, &out)
	return out.Rows, err
}

// GInspection is what Google's URL Inspection says about one page.
type GInspection struct {
	Verdict       string `json:"verdict" doc:"PASS (indexed), NEUTRAL (not indexed, no problem) or FAIL"`
	Coverage      string `json:"coverage" doc:"Google's words: \"Submitted and indexed\", \"Crawled - currently not indexed\", \"Discovered - currently not indexed\"..."`
	LastCrawl     string `json:"lastCrawl,omitempty"`
	RobotsTxt     string `json:"robotsTxt,omitempty"`
	Indexing      string `json:"indexing,omitempty"`
	PageFetch     string `json:"pageFetch,omitempty"`
	GoogleCanon   string `json:"googleCanonical,omitempty"`
	UserCanonical string `json:"userCanonical,omitempty"`
}

// inspect asks URL Inspection about a page (2,000 a day per property).
func (g *gclient) inspect(ctx context.Context, siteURL, page string) (*GInspection, error) {
	var out struct {
		InspectionResult struct {
			IndexStatusResult struct {
				Verdict         string `json:"verdict"`
				CoverageState   string `json:"coverageState"`
				RobotsTxtState  string `json:"robotsTxtState"`
				IndexingState   string `json:"indexingState"`
				LastCrawlTime   string `json:"lastCrawlTime"`
				PageFetchState  string `json:"pageFetchState"`
				GoogleCanonical string `json:"googleCanonical"`
				UserCanonical   string `json:"userCanonical"`
			} `json:"indexStatusResult"`
		} `json:"inspectionResult"`
	}
	if err := g.call(ctx, http.MethodPost, searchAPI+"/urlInspection/index:inspect", map[string]any{"inspectionUrl": page, "siteUrl": siteURL}, &out); err != nil {
		return nil, err
	}
	r := out.InspectionResult.IndexStatusResult
	return &GInspection{Verdict: r.Verdict, Coverage: r.CoverageState, LastCrawl: r.LastCrawlTime, RobotsTxt: r.RobotsTxtState,
		Indexing: r.IndexingState, PageFetch: r.PageFetchState, GoogleCanon: r.GoogleCanonical, UserCanonical: r.UserCanonical}, nil
}
