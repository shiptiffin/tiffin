package email

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shiptiffin/tiffin/internal/dnskit"
)

// Domain authentication through the relay provider's own API, with the key
// the relay already has. Checked against the providers' docs (October 2026):
//
//   - SendGrid: POST /v3/whitelabel/domains {domain, automatic_security: true}
//     answers with three CNAMEs (mail_cname, dkim1, dkim2); POST
//     /v3/whitelabel/domains/{id}/validate checks them. GET
//     /v3/whitelabel/domains?domain= finds an existing one. The key needs the
//     "Sender Authentication" permission (Mail Send alone answers 403).
//     https://www.twilio.com/docs/sendgrid/api-reference/domain-authentication
//   - Resend: POST /domains {name} answers with its records (DKIM TXT, SPF MX
//     and TXT on send.<domain>); POST /domains/{id}/verify starts a check
//     and GET /domains/{id} reads the status (not_started, pending,
//     verified, partially_verified, temporary_failure, failed). A
//     "Sending access" key answers 401 restricted_api_key.
//     https://resend.com/docs/api-reference/domains/create-domain

// Provider API base addresses (tests point them at httptest servers).
var (
	sendgridAPI = "https://api.sendgrid.com"
	resendAPI   = "https://api.resend.com"
	apiClient   = &http.Client{Timeout: 20 * time.Second}
)

// SendingRecord is one DNS record a provider needs for a sending domain.
type SendingRecord struct {
	Purpose string `json:"purpose" enum:"dkim,spf,return-path,dmarc,other" doc:"What it is for: dkim signs the mail, spf and return-path let the provider send for the domain, dmarc tells receivers what to do with mail that fails"`
	Type    string `json:"type" doc:"CNAME, TXT or MX"`
	Name    string `json:"name" doc:"The full name, e.g. s1._domainkey.example.com"`
	Host    string `json:"host" doc:"The name relative to the domain's zone, as most DNS panels want it (@ for the zone itself)"`
	Value   string `json:"value" doc:"The value; for MX, the priority then the host"`
	Status  string `json:"status" enum:"ok,pending,failed" doc:"ok: the provider (or public DNS, for dmarc) sees it; pending: not seen yet; failed: seen, but wrong"`
	Detail  string `json:"detail,omitempty" doc:"What the provider said about it"`
	Written bool   `json:"written,omitempty" doc:"The box wrote it through the connected DNS provider"`
	key     string // the provider's own name for the record (SendGrid: dkim1...)
}

// providerDomain is a sending domain as the provider sees it.
type providerDomain struct {
	ID      string
	Domain  string
	Valid   bool
	Status  string // the provider's word, for the detail line
	Records []SendingRecord
}

// domainAPI is a provider whose API can authenticate a sending domain.
type domainAPI interface {
	name() string
	find(ctx context.Context, domain string) (*providerDomain, error)
	create(ctx context.Context, domain string) (*providerDomain, error)
	// verify asks the provider to check the records now and returns what it found.
	verify(ctx context.Context, d *providerDomain) (*providerDomain, error)
}

// providerError is a refusal from a provider's API, explained.
type providerError struct {
	Status     int
	Permission bool // the key works but lacks the permission
	Msg        string
	Hint       string
}

func (e *providerError) Error() string { return e.Msg }

// domainClient returns the API client for the relay's provider, or nil
// when the box can't set domains up there (the owner follows DNS steps).
func domainClient(provider, key string) domainAPI {
	switch provider {
	case ProviderSendGrid:
		return &sendgridClient{key: key}
	case ProviderResend:
		return &resendClient{key: key}
	}
	return nil
}

// callJSON does one API request and decodes a JSON answer into out.
func callJSON(ctx context.Context, method, u, key string, body, out any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "tiffin-box")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := apiClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 300 && out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return res.StatusCode, raw, fmt.Errorf("unexpected answer from %s: %w", req.URL.Host, err)
		}
	}
	return res.StatusCode, raw, nil
}

// fullName makes a provider's record name absolute. Names may be absolute
// already, relative to the domain ("send"), or relative to the zone apex
// ("send.mail" for mail.example.com).
func fullName(name, domain string) string {
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	switch {
	case name == "" || name == "@":
		return domain
	case name == domain || strings.HasSuffix(name, "."+domain):
		return name
	}
	if apex := dnskit.Zone(domain); apex != domain {
		if c := name + "." + apex; strings.HasSuffix(c, "."+domain) {
			return c
		}
	}
	return name + "." + domain
}

// ---- SendGrid ----

type sendgridClient struct{ key string }

func (*sendgridClient) name() string { return "SendGrid" }

type sgRecord struct {
	Valid bool   `json:"valid"`
	Type  string `json:"type"`
	Host  string `json:"host"`
	Data  string `json:"data"`
}

type sgDomain struct {
	ID                int64               `json:"id"`
	Domain            string              `json:"domain"`
	Subdomain         string              `json:"subdomain"`
	Valid             bool                `json:"valid"`
	AutomaticSecurity bool                `json:"automatic_security"`
	DNS               map[string]sgRecord `json:"dns"`
}

func (c *sendgridClient) err(code int, raw []byte, doing string) error {
	var body struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(raw, &body)
	said := ""
	if len(body.Errors) > 0 {
		said = body.Errors[0].Message
	}
	switch code {
	case http.StatusUnauthorized:
		return &providerError{Status: code, Msg: "SendGrid refused the API key while " + doing + ".",
			Hint: "Check the key in Settings › Email is still active."}
	case http.StatusForbidden:
		return &providerError{Status: code, Permission: true,
			Msg:  "The SendGrid key can send mail but can't set up domains.",
			Hint: "In SendGrid, open Settings › API Keys, edit the key and give it Sender Authentication (Full Access). Then try again here."}
	case http.StatusTooManyRequests:
		return &providerError{Status: code, Msg: "SendGrid is rate limiting the box. Try again in a minute."}
	}
	if said == "" {
		said = "status " + strconv.Itoa(code)
	}
	return &providerError{Status: code, Msg: "SendGrid answered while " + doing + ": " + said + "."}
}

func sgPurpose(key string) string {
	switch {
	case strings.HasPrefix(key, "dkim"):
		return "dkim"
	case key == "mail_cname" || key == "mail_server" || key == "subdomain_spf":
		return "return-path"
	case strings.Contains(key, "spf"):
		return "spf"
	case strings.Contains(key, "dmarc"):
		return "dmarc"
	}
	return "other"
}

func (d *sgDomain) toDomain() *providerDomain {
	out := &providerDomain{ID: strconv.FormatInt(d.ID, 10), Domain: strings.ToLower(d.Domain), Valid: d.Valid, Status: "not verified yet"}
	if d.Valid {
		out.Status = "verified"
	}
	keys := make([]string, 0, len(d.DNS))
	for k := range d.DNS {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		r := d.DNS[k]
		if r.Host == "" || r.Data == "" {
			continue
		}
		st := "pending"
		if r.Valid {
			st = "ok"
		}
		out.Records = append(out.Records, SendingRecord{Purpose: sgPurpose(k), Type: strings.ToUpper(r.Type), Name: fullName(r.Host, out.Domain),
			Value: strings.TrimSuffix(r.Data, "."), Status: st, key: k})
	}
	return out
}

func (c *sendgridClient) find(ctx context.Context, domain string) (*providerDomain, error) {
	var list []sgDomain
	code, raw, err := callJSON(ctx, http.MethodGet, sendgridAPI+"/v3/whitelabel/domains?limit=50&domain="+url.QueryEscape(domain), c.key, nil, &list)
	if err != nil {
		return nil, err
	}
	if code >= 300 {
		return nil, c.err(code, raw, "looking for the domain")
	}
	var best *sgDomain
	for i := range list {
		d := &list[i]
		if !strings.EqualFold(d.Domain, domain) {
			continue
		}
		// Prefer a verified one, then one SendGrid manages the keys for.
		if best == nil || (d.Valid && !best.Valid) || (d.Valid == best.Valid && d.AutomaticSecurity && !best.AutomaticSecurity) {
			best = d
		}
	}
	if best == nil {
		return nil, nil
	}
	return best.toDomain(), nil
}

func (c *sendgridClient) create(ctx context.Context, domain string) (*providerDomain, error) {
	var d sgDomain
	code, raw, err := callJSON(ctx, http.MethodPost, sendgridAPI+"/v3/whitelabel/domains", c.key,
		map[string]any{"domain": domain, "automatic_security": true, "custom_spf": false, "default": false}, &d)
	if err != nil {
		return nil, err
	}
	if code >= 300 {
		return nil, c.err(code, raw, "adding the domain")
	}
	return d.toDomain(), nil
}

func (c *sendgridClient) verify(ctx context.Context, d *providerDomain) (*providerDomain, error) {
	var v struct {
		Valid   bool `json:"valid"`
		Results map[string]struct {
			Valid  bool    `json:"valid"`
			Reason *string `json:"reason"`
		} `json:"validation_results"`
	}
	code, raw, err := callJSON(ctx, http.MethodPost, sendgridAPI+"/v3/whitelabel/domains/"+url.PathEscape(d.ID)+"/validate", c.key, nil, &v)
	if err != nil {
		return nil, err
	}
	if code >= 300 {
		return nil, c.err(code, raw, "checking the domain")
	}
	out := *d
	out.Records = append([]SendingRecord(nil), d.Records...)
	out.Valid = v.Valid
	out.Status = "not verified yet"
	if v.Valid {
		out.Status = "verified"
	}
	for i := range out.Records {
		r, ok := v.Results[out.Records[i].key]
		if !ok {
			continue
		}
		out.Records[i].Status, out.Records[i].Detail = "pending", ""
		if r.Valid {
			out.Records[i].Status = "ok"
		} else if r.Reason != nil && *r.Reason != "" {
			out.Records[i].Detail = *r.Reason
			if !strings.Contains(strings.ToLower(*r.Reason), "does not exist") && !strings.Contains(strings.ToLower(*r.Reason), "no record") {
				out.Records[i].Status = "failed"
			}
		}
	}
	return &out, nil
}

// ---- Resend ----

type resendClient struct{ key string }

func (*resendClient) name() string { return "Resend" }

type rsRecord struct {
	Record   string `json:"record"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Value    string `json:"value"`
	Status   string `json:"status"`
	Priority *int   `json:"priority"`
}

type rsDomain struct {
	ID      string     `json:"id"`
	Name    string     `json:"name"`
	Status  string     `json:"status"`
	Records []rsRecord `json:"records"`
}

func (c *resendClient) err(code int, raw []byte, doing string) error {
	var body struct {
		Name    string `json:"name"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &body)
	switch {
	case body.Name == "restricted_api_key":
		return &providerError{Status: code, Permission: true,
			Msg:  "The Resend key has Sending access only, so it can't set up domains.",
			Hint: "In Resend, create an API key with Full access (API Keys › Create API key) and paste it in Settings › Email. It sends mail too. Then try again here."}
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return &providerError{Status: code, Msg: "Resend refused the API key while " + doing + ".",
			Hint: "Check the key in Settings › Email is still active."}
	case code == http.StatusTooManyRequests:
		return &providerError{Status: code, Msg: "Resend is rate limiting the box. Try again in a minute."}
	}
	said := body.Message
	if said == "" {
		said = "status " + strconv.Itoa(code)
	}
	return &providerError{Status: code, Msg: "Resend answered while " + doing + ": " + strings.TrimSuffix(said, ".") + "."}
}

func rsStatus(s string) string {
	switch s {
	case "verified":
		return "ok"
	case "failed", "temporary_failure":
		return "failed"
	}
	return "pending"
}

func (d *rsDomain) toDomain() *providerDomain {
	out := &providerDomain{ID: d.ID, Domain: strings.ToLower(d.Name), Status: strings.ReplaceAll(d.Status, "_", " ")}
	sending := true
	for _, r := range d.Records {
		purpose := "other"
		switch strings.ToUpper(r.Record) {
		case "DKIM":
			purpose = "dkim"
		case "SPF":
			purpose = "spf"
		case "DMARC":
			purpose = "dmarc"
		case "RECEIVING", "TRACKING":
			continue // the box only sends
		}
		v := strings.TrimSuffix(r.Value, ".")
		if strings.EqualFold(r.Type, "MX") && r.Priority != nil {
			v = strconv.Itoa(*r.Priority) + " " + v
		}
		st := rsStatus(r.Status)
		if (purpose == "dkim" || purpose == "spf") && st != "ok" {
			sending = false
		}
		out.Records = append(out.Records, SendingRecord{Purpose: purpose, Type: strings.ToUpper(r.Type), Name: fullName(r.Name, out.Domain),
			Value: v, Status: st, key: strings.ToLower(r.Record + " " + r.Type + " " + r.Name)})
	}
	out.Valid = d.Status == "verified" || (d.Status == "partially_verified" && sending)
	return out
}

func (c *resendClient) get(ctx context.Context, id string) (*providerDomain, error) {
	var d rsDomain
	code, raw, err := callJSON(ctx, http.MethodGet, resendAPI+"/domains/"+url.PathEscape(id), c.key, nil, &d)
	if err != nil {
		return nil, err
	}
	if code >= 300 {
		return nil, c.err(code, raw, "reading the domain")
	}
	return d.toDomain(), nil
}

func (c *resendClient) find(ctx context.Context, domain string) (*providerDomain, error) {
	var list struct {
		Data []rsDomain `json:"data"`
	}
	code, raw, err := callJSON(ctx, http.MethodGet, resendAPI+"/domains?limit=100", c.key, nil, &list)
	if err != nil {
		return nil, err
	}
	if code >= 300 {
		return nil, c.err(code, raw, "looking for the domain")
	}
	for _, d := range list.Data {
		if strings.EqualFold(d.Name, domain) {
			return c.get(ctx, d.ID)
		}
	}
	return nil, nil
}

func (c *resendClient) create(ctx context.Context, domain string) (*providerDomain, error) {
	var d rsDomain
	code, raw, err := callJSON(ctx, http.MethodPost, resendAPI+"/domains", c.key, map[string]any{"name": domain}, &d)
	if err != nil {
		return nil, err
	}
	if code >= 300 {
		return nil, c.err(code, raw, "adding the domain")
	}
	if len(d.Records) == 0 && d.ID != "" {
		return c.get(ctx, d.ID)
	}
	return d.toDomain(), nil
}

func (c *resendClient) verify(ctx context.Context, d *providerDomain) (*providerDomain, error) {
	cur, err := c.get(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	if cur.Valid || cur.Status == "pending" {
		return cur, nil // verified, or Resend is checking already
	}
	code, raw, err := callJSON(ctx, http.MethodPost, resendAPI+"/domains/"+url.PathEscape(d.ID)+"/verify", c.key, nil, nil)
	if err != nil {
		return nil, err
	}
	if code >= 300 {
		return nil, c.err(code, raw, "checking the domain")
	}
	return c.get(ctx, d.ID)
}
