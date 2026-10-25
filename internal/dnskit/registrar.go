package dnskit

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
	"time"
)

// CloudflareAPI is the base of Cloudflare's v4 API.
const CloudflareAPI = "https://api.cloudflare.com/client/v4"

// Registrar talks to Cloudflare Registrar's API (beta since 2026-04): search,
// live availability with prices, register, and the registration workflow.
// Domains land in the token's own account and its card pays, at cost.
// The token needs Registrar Domains: Admin (account) to register, Read to
// search; Sandbox uses the registrar-sandbox paths and permissions instead.
type Registrar struct {
	Token   string
	Account string // the Cloudflare account id
	Sandbox bool
	Base    string       // default CloudflareAPI
	HTTP    *http.Client // default: a 30 s client
}

// Offer is one name the registrar answered for, from search or a live check.
type Offer struct {
	Name        string   `json:"name"`
	Registrable bool     `json:"registrable"`
	Tier        string   `json:"tier,omitempty"` // standard | premium
	Reason      string   `json:"reason,omitempty"`
	Pricing     *Pricing `json:"pricing,omitempty"`
}

// Pricing: per-year prices as decimal strings ("8.57"), never floats.
type Pricing struct {
	Currency         string `json:"currency"`
	RegistrationCost string `json:"registration_cost"`
	RenewalCost      string `json:"renewal_cost"`
}

// Registration is a domain registered in the account.
type Registration struct {
	DomainName  string     `json:"domain_name"`
	Status      string     `json:"status"` // active, registration_pending, expired, …
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
	AutoRenew   bool       `json:"auto_renew"`
	PrivacyMode string     `json:"privacy_mode"`
	Locked      bool       `json:"locked"`
}

// Workflow is a registration's progress. Completed only for succeeded and failed.
type Workflow struct {
	State     string `json:"state"` // pending, in_progress, action_required, blocked, succeeded, failed
	Completed bool   `json:"completed"`
	Context   struct {
		DomainName         string        `json:"domain_name"`
		Registration       *Registration `json:"registration,omitempty"`
		Action             string        `json:"action,omitempty"`
		ConfirmationSentTo string        `json:"confirmation_sent_to,omitempty"`
	} `json:"context"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NewRegistration asks for one domain. Only DomainName is required; contacts
// default to the account's address book entry (production only).
type NewRegistration struct {
	DomainName  string `json:"domain_name"`
	Years       int    `json:"years,omitempty"`
	AutoRenew   bool   `json:"auto_renew"`
	PrivacyMode string `json:"privacy_mode,omitempty"`
	// Contacts is passed through as is: the sandbox has no default contact.
	Contacts json.RawMessage `json:"contacts,omitempty"`
}

// RegistrarError is a refusal from Cloudflare: its HTTP status and first error.
type RegistrarError struct {
	Status  int
	Code    int
	Message string
}

func (e *RegistrarError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("Cloudflare answered %d", e.Status)
	}
	return "Cloudflare: " + e.Message
}

// Forbidden reports whether Cloudflare refused the token (missing permissions).
func (e *RegistrarError) Forbidden() bool {
	return e.Status == http.StatusForbidden || e.Status == http.StatusUnauthorized ||
		strings.Contains(strings.ToLower(e.Message), "authentication") || strings.Contains(strings.ToLower(e.Message), "permission")
}

type envelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result     json.RawMessage `json:"result"`
	ResultInfo *struct {
		Cursor string `json:"cursor"`
	} `json:"result_info"`
}

func (r *Registrar) base() string {
	if r.Base != "" {
		return strings.TrimSuffix(r.Base, "/")
	}
	return CloudflareAPI
}

func (r *Registrar) path(rest string) string {
	part := "registrar"
	if r.Sandbox {
		part = "registrar-sandbox"
	}
	return "/accounts/" + url.PathEscape(r.Account) + "/" + part + rest
}

// call sends one request and decodes result into out. prefer sets the Prefer header.
func (r *Registrar) call(ctx context.Context, method, path string, body, out any, prefer string) (*envelope, error) {
	if r.Account == "" {
		return nil, errors.New("no Cloudflare account id")
	}
	return cfCall(ctx, r.HTTP, r.Token, method, r.base()+path, body, out, prefer)
}

func cfCall(ctx context.Context, hc *http.Client, token, method, u string, body, out any, prefer string) (*envelope, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if prefer != "" {
		req.Header.Set("Prefer", prefer)
	}
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reach Cloudflare: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, &RegistrarError{Status: res.StatusCode, Message: strings.TrimSpace(string(raw[:min(len(raw), 200)]))}
	}
	if res.StatusCode >= 300 || !env.Success {
		e := &RegistrarError{Status: res.StatusCode}
		if len(env.Errors) > 0 {
			e.Code, e.Message = env.Errors[0].Code, env.Errors[0].Message
		}
		return &env, e
	}
	if out != nil && len(env.Result) > 0 && string(env.Result) != "null" {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return &env, fmt.Errorf("read Cloudflare's answer: %w", err)
		}
	}
	return &env, nil
}

// Search suggests names for a keyword, phrase or domain, in relevance order,
// from cached data: check before buying.
func (r *Registrar) Search(ctx context.Context, q string, limit int) ([]Offer, error) {
	v := url.Values{"q": {q}}
	if limit > 0 {
		v.Set("limit", strconv.Itoa(min(limit, 50)))
	}
	var out struct {
		Domains []Offer `json:"domains"`
	}
	_, err := r.call(ctx, http.MethodGet, r.path("/domain-search?"+v.Encode()), nil, &out, "")
	return out.Domains, err
}

// Check asks the registries, live, for up to 20 names.
func (r *Registrar) Check(ctx context.Context, names []string) ([]Offer, error) {
	if len(names) > 20 {
		names = names[:20]
	}
	var out struct {
		Domains []Offer `json:"domains"`
	}
	_, err := r.call(ctx, http.MethodPost, r.path("/domain-check"), map[string]any{"domains": names}, &out, "")
	return out.Domains, err
}

// Register starts a billable, non-refundable registration and returns at once
// (Prefer: respond-async); follow it with Status.
func (r *Registrar) Register(ctx context.Context, in NewRegistration) (*Workflow, error) {
	var w Workflow
	_, err := r.call(ctx, http.MethodPost, r.path("/registrations"), in, &w, "respond-async")
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// Status is a registration workflow's state.
func (r *Registrar) Status(ctx context.Context, domain string) (*Workflow, error) {
	var w Workflow
	_, err := r.call(ctx, http.MethodGet, r.path("/registrations/"+url.PathEscape(domain)+"/registration-status"), nil, &w, "")
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// Registration is one domain registered in the account; nil when it isn't.
func (r *Registrar) Registration(ctx context.Context, domain string) (*Registration, error) {
	var reg Registration
	_, err := r.call(ctx, http.MethodGet, r.path("/registrations/"+url.PathEscape(domain)), nil, &reg, "")
	var re *RegistrarError
	if errors.As(err, &re) && (re.Status == http.StatusNotFound || strings.Contains(strings.ToLower(re.Message), "not found")) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &reg, nil
}

// Registrations lists every domain registered in the account (up to 500).
func (r *Registrar) Registrations(ctx context.Context) ([]Registration, error) {
	var all []Registration
	cursor := ""
	for range 20 {
		v := url.Values{"per_page": {"50"}}
		if cursor != "" {
			v.Set("cursor", cursor)
		}
		var page []Registration
		env, err := r.call(ctx, http.MethodGet, r.path("/registrations?"+v.Encode()), nil, &page, "")
		if err != nil {
			return all, err
		}
		all = append(all, page...)
		if env.ResultInfo == nil || env.ResultInfo.Cursor == "" || len(page) == 0 {
			break
		}
		cursor = env.ResultInfo.Cursor
	}
	return all, nil
}

// CloudflareAccount finds the account a token works in: the account of its
// first zone (Zone · Read), else the only account it can list.
func CloudflareAccount(ctx context.Context, hc *http.Client, base, token string) (id, name string, err error) {
	if base == "" {
		base = CloudflareAPI
	}
	var zones []struct {
		Account struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"account"`
	}
	if _, zerr := cfCall(ctx, hc, token, http.MethodGet, base+"/zones?per_page=5", nil, &zones, ""); zerr == nil {
		for _, z := range zones {
			if z.Account.ID != "" {
				return z.Account.ID, z.Account.Name, nil
			}
		}
	}
	var accts []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if _, err := cfCall(ctx, hc, token, http.MethodGet, base+"/accounts?per_page=5", nil, &accts, ""); err != nil {
		return "", "", err
	}
	if len(accts) == 0 {
		return "", "", errors.New("the token reaches no Cloudflare account")
	}
	return accts[0].ID, accts[0].Name, nil
}

// Cents turns a decimal price ("8.57", "10", "1,234.5") into cents.
func Cents(s string) (int64, bool) {
	s = strings.ReplaceAll(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "$")), ",", "")
	if s == "" {
		return 0, false
	}
	whole, frac, _ := strings.Cut(s, ".")
	if len(frac) > 2 {
		return 0, false
	}
	frac += strings.Repeat("0", 2-len(frac))
	w, err1 := strconv.ParseInt(whole, 10, 64)
	f, err2 := strconv.ParseInt(frac, 10, 64)
	if err1 != nil || err2 != nil || w < 0 {
		return 0, false
	}
	return w*100 + f, true
}

// Quote is what registering a domain costs, from a live check.
type Quote struct {
	Domain    string `json:"domain"`
	Years     int    `json:"years"`
	FirstYear string `json:"firstYear" doc:"The first year's price, e.g. 10.44"`
	Renewal   string `json:"renewal" doc:"Each later year's price"`
	Total     string `json:"total" doc:"What is charged now: the first year plus renewal × (years − 1). Send it back as price."`
	Currency  string `json:"currency" doc:"e.g. USD"`
}

// QuoteFor prices years of o (registrable, with pricing).
func QuoteFor(o Offer, years int) (Quote, bool) {
	if !o.Registrable || o.Pricing == nil {
		return Quote{}, false
	}
	first, ok1 := Cents(o.Pricing.RegistrationCost)
	renew, ok2 := Cents(o.Pricing.RenewalCost)
	if !ok1 || !ok2 {
		return Quote{}, false
	}
	years = max(years, 1)
	return Quote{
		Domain: o.Name, Years: years, Currency: o.Pricing.Currency,
		FirstYear: Money(first), Renewal: Money(renew), Total: Money(first + renew*int64(years-1)),
	}, true
}

// Money writes cents as a decimal price: 1044 → "10.44".
func Money(c int64) string { return fmt.Sprintf("%d.%02d", c/100, c%100) }
