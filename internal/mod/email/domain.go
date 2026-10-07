package email

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/dnskit"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
	"github.com/emersion/go-message/mail"
)

// domainResolver answers the sending-domain check (tests point it at dnstest).
var domainResolver = &dnskit.Resolver{Timeout: 2 * time.Second}

// DomainCheck is what public DNS says about the domain a project sends from:
// the SPF, DKIM and DMARC records receivers look at before they trust mail.
type DomainCheck struct {
	From      string         `json:"from" doc:"The project's sender address (EMAIL_FROM)"`
	Domain    string         `json:"domain" doc:"The domain of the sender address"`
	BoxDomain bool           `json:"boxDomain" doc:"The sender is on the box's own domain (the default)"`
	Relay     string         `json:"relay,omitempty" doc:"The relay host, when the box has one"`
	Provider  string         `json:"provider,omitempty" doc:"The mail service the relay belongs to, when the box recognises it"`
	Records   []DomainRecord `json:"records"`
	CheckedAt time.Time      `json:"checkedAt"`
}

// DomainRecord is one record a receiver checks, and what DNS has for it now.
type DomainRecord struct {
	Kind   string   `json:"kind" enum:"spf,dkim,dmarc"`
	Type   string   `json:"type" doc:"The DNS record type to add (TXT)"`
	Name   string   `json:"name" doc:"The full name the record lives at"`
	Want   string   `json:"want,omitempty" doc:"A value to publish, when the box can say (empty: your mail service gives it)"`
	Found  []string `json:"found" doc:"The matching values DNS answers with now"`
	State  string   `json:"state" enum:"ok,missing,warn,unknown" doc:"ok; missing; warn (there, but with a problem); unknown (DNS did not answer)"`
	Detail string   `json:"detail,omitempty"`
}

// mailService is what the box knows about a relay provider's DNS setup.
type mailService struct {
	name      string
	hosts     []string // relay host suffixes
	spfSub    string   // SPF lives on this subdomain ("" = the domain itself, "-" = the provider handles SPF)
	spfInc    string   // the include: the SPF record needs
	selectors []string // DKIM selectors the provider uses
}

var mailServices = []mailService{
	{name: "Resend", hosts: []string{"resend.com"}, spfSub: "send", spfInc: "amazonses.com", selectors: []string{"resend"}},
	{name: "Postmark", hosts: []string{"postmarkapp.com"}, spfSub: "-"},
	{name: "Amazon SES", hosts: []string{"amazonaws.com"}, spfInc: "amazonses.com"},
	{name: "SendGrid", hosts: []string{"sendgrid.net"}, spfInc: "sendgrid.net", selectors: []string{"s1", "s2"}},
	{name: "Mailgun", hosts: []string{"mailgun.org", "mailgun.net"}, spfInc: "mailgun.org", selectors: []string{"smtp", "mx", "k1", "pic", "krs"}},
	{name: "Brevo", hosts: []string{"brevo.com", "sendinblue.com"}, spfInc: "spf.brevo.com", selectors: []string{"brevo1", "brevo2", "mail"}},
	{name: "Mailjet", hosts: []string{"mailjet.com"}, spfInc: "spf.mailjet.com", selectors: []string{"mailjet"}},
	{name: "Google", hosts: []string{"gmail.com", "google.com", "googlemail.com"}, spfInc: "_spf.google.com", selectors: []string{"google"}},
}

// commonSelectors are tried when the provider is unknown and none was given.
var commonSelectors = []string{"default", "selector1", "selector2", "google", "k1", "s1", "mail", "dkim"}

func serviceFor(host string) *mailService {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for i := range mailServices {
		for _, h := range mailServices[i].hosts {
			if host == h || strings.HasSuffix(host, "."+h) {
				return &mailServices[i]
			}
		}
	}
	return nil
}

var selectorRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)

// checkDomain looks up the SPF, DKIM and DMARC records for from's domain.
// selector, when set, is the DKIM selector to check (from the mail service's
// dashboard); otherwise the provider's known selectors, or common ones.
func checkDomain(ctx context.Context, r *dnskit.Resolver, from, boxDomain, relayHost, selector string) (*DomainCheck, error) {
	addr, err := mail.ParseAddress(from)
	if err != nil {
		return nil, &ValidationError{Msg: "the sender address " + from + " is not an email address"}
	}
	_, domain, _ := strings.Cut(addr.Address, "@")
	domain = strings.ToLower(domain)
	out := &DomainCheck{From: from, Domain: domain, BoxDomain: domain == strings.ToLower(boxDomain), Relay: relayHost, CheckedAt: time.Now().UTC()}
	svc := serviceFor(relayHost)
	if svc != nil {
		out.Provider = svc.name
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	var (
		wg              sync.WaitGroup
		spf, dkim, dmrc DomainRecord
	)
	wg.Add(3)
	go func() { defer wg.Done(); spf = checkSPF(ctx, r, domain, svc) }()
	go func() { defer wg.Done(); dkim = checkDKIM(ctx, r, domain, svc, selector) }()
	go func() { defer wg.Done(); dmrc = checkDMARC(ctx, r, domain) }()
	wg.Wait()
	for _, rec := range []DomainRecord{spf, dkim, dmrc} {
		if rec.Kind != "" {
			out.Records = append(out.Records, rec)
		}
	}
	return out, nil
}

// lookupPrefixed returns name's TXT values that start with prefix (case-insensitive).
func lookupPrefixed(ctx context.Context, r *dnskit.Resolver, name, prefix string) ([]string, error) {
	all, err := r.TXT(ctx, name)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, v := range all {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(v)), prefix) {
			out = append(out, v)
		}
	}
	return out, nil
}

func unknown(rec DomainRecord, err error) DomainRecord {
	rec.State, rec.Found = "unknown", []string{}
	rec.Detail = "DNS didn't answer in time. Try again in a moment."
	if errors.Is(err, context.Canceled) {
		rec.Detail = "The check was cancelled."
	}
	return rec
}

func checkSPF(ctx context.Context, r *dnskit.Resolver, domain string, svc *mailService) DomainRecord {
	if svc != nil && svc.spfSub == "-" {
		return DomainRecord{} // the provider's own bounce domain carries SPF
	}
	name := domain
	if svc != nil && svc.spfSub != "" {
		name = svc.spfSub + "." + domain
	}
	rec := DomainRecord{Kind: "spf", Type: "TXT", Name: name}
	if svc != nil && svc.spfInc != "" {
		rec.Want = "v=spf1 include:" + svc.spfInc + " ~all"
	}
	found, err := lookupPrefixed(ctx, r, name, "v=spf1")
	if err != nil {
		return unknown(rec, err)
	}
	rec.Found = found
	switch {
	case len(found) == 0:
		rec.State = "missing"
		if rec.Want == "" {
			rec.Detail = "No SPF record. Your mail service's domain settings give the include to use."
		} else {
			rec.Detail = "No SPF record yet. Add this one."
		}
	case len(found) > 1:
		rec.State, rec.Detail = "warn", "There are two SPF records here. Receivers treat that as an error: merge them into one."
	case svc != nil && svc.spfInc != "" && !strings.Contains(strings.ToLower(found[0]), "include:"+svc.spfInc):
		rec.State = "warn"
		rec.Detail = "There's an SPF record, but it doesn't include " + svc.name + " (include:" + svc.spfInc + "). Add the include to the record you have."
		rec.Want = strings.Replace(found[0], "v=spf1", "v=spf1 include:"+svc.spfInc, 1)
	default:
		rec.State = "ok"
	}
	return rec
}

func checkDKIM(ctx context.Context, r *dnskit.Resolver, domain string, svc *mailService, selector string) DomainRecord {
	var sels []string
	switch {
	case selector != "":
		sels = []string{selector}
	case svc != nil && len(svc.selectors) > 0:
		sels = svc.selectors
	default:
		sels = commonSelectors
	}
	type answer struct {
		sel   string
		found []string
		err   error
	}
	answers := make([]answer, len(sels))
	var wg sync.WaitGroup
	for i, s := range sels {
		wg.Add(1)
		go func() {
			defer wg.Done()
			all, err := r.TXT(ctx, s+"._domainkey."+domain)
			var keys []string
			for _, v := range all {
				if strings.Contains(v, "p=") {
					keys = append(keys, v)
				}
			}
			answers[i] = answer{s, keys, err}
		}()
	}
	wg.Wait()
	rec := DomainRecord{Kind: "dkim", Type: "TXT", Name: sels[0] + "._domainkey." + domain, Found: []string{}}
	failed := 0
	for _, a := range answers {
		if a.err != nil {
			failed++
			continue
		}
		if len(a.found) > 0 {
			rec.Name, rec.Found, rec.State = a.sel+"._domainkey."+domain, a.found, "ok"
			return rec
		}
	}
	if failed == len(answers) {
		return unknown(rec, answers[0].err)
	}
	rec.State = "missing"
	switch {
	case selector != "":
		rec.Detail = "No DKIM key at this selector yet. Your mail service gives the value to add."
	case svc != nil && len(svc.selectors) > 0:
		rec.Detail = svc.name + " gives you this key when you add the domain there. Copy it from its dashboard."
	default:
		rec.Name = "<selector>._domainkey." + domain
		rec.Detail = "No DKIM key at the usual names. Enter the selector your mail service gave you to check it."
	}
	return rec
}

func checkDMARC(ctx context.Context, r *dnskit.Resolver, domain string) DomainRecord {
	rec := DomainRecord{Kind: "dmarc", Type: "TXT", Name: "_dmarc." + domain, Want: "v=DMARC1; p=none;"}
	found, err := lookupPrefixed(ctx, r, rec.Name, "v=dmarc1")
	if err != nil {
		return unknown(rec, err)
	}
	rec.Found = found
	switch len(found) {
	case 0:
		rec.State, rec.Detail = "missing", "No DMARC policy. Start with p=none, then tighten it once mail is passing."
	case 1:
		rec.State, rec.Want = "ok", ""
		if strings.Contains(strings.ReplaceAll(strings.ToLower(found[0]), " ", ""), "p=none") {
			rec.Detail = "The policy is p=none: receivers report failures but still deliver them."
		}
	default:
		rec.State, rec.Detail = "warn", "There are two DMARC records here. Receivers ignore both: keep one."
	}
	return rec
}

// registerDomainAPI adds the sending-domain check.
func (m *Module) registerDomainAPI(a huma.API, p *platform.Platform, base, tag string) {
	huma.Register(a, api.Op("email-domain-check", http.MethodGet, base+"/domain", "email domain check", api.RiskRead,
		"Check the sending domain's DNS",
		"Looks up the SPF, DKIM and DMARC records for the domain of the project's sender address (EMAIL_FROM) in public DNS, "+
			"and says which are missing. With a relay from a known mail service (Resend, Postmark, SES, SendGrid, Mailgun, Brevo, Mailjet, Google), "+
			"the SPF include and DKIM selectors are that service's.", tag),
		api.Wrap(func(ctx context.Context, in *struct {
			Project  string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
			Selector string `query:"selector" maxLength:"63" doc:"The DKIM selector your mail service gave you (the part before ._domainkey)"`
		}) (*struct{ Body DomainCheck }, error) {
			if err := boxOnly(p); err != nil {
				return nil, err
			}
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
				return nil, err
			}
			if in.Selector != "" && !selectorRE.MatchString(in.Selector) {
				return nil, api.NewProblem(422, "validation", "a DKIM selector is letters, digits, dots and dashes")
			}
			s, err := m.settings(ctx, p, in.Project)
			if err != nil {
				return nil, err
			}
			if s == nil {
				return nil, toProblem(errNoProject)
			}
			relay, err := getRelay(ctx, p)
			if err != nil {
				return nil, err
			}
			host := ""
			if relay != nil {
				host = relay.Host
			}
			out, err := checkDomain(ctx, domainResolver, s.From, p.Domain, host, in.Selector)
			if err != nil {
				return nil, toProblem(err)
			}
			return &struct{ Body DomainCheck }{*out}, nil
		}))
}
