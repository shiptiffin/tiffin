package email

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/emersion/go-message/mail"
	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/change"
	"github.com/shiptiffin/tiffin/internal/dnskit"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// "Send from my domain": one click on a project's Email page sets a domain
// up with the relay's mail service through its API (SendGrid, Resend),
// writes the DNS records through the connected DNS provider when it holds
// the domain (or lists them to copy), re-checks in the background until the
// provider says the domain is verified, then makes it the project's sender
// with a change in History. Providers without an API path get the DNS steps.

// Sending-domain states.
const (
	SendingVerifying = "verifying" // waiting for the provider to see the records
	SendingVerified  = "verified"  // the provider verified it; it is the sender
	SendingFailed    = "failed"    // gave up (48 hours) or the provider refused: needs a person
	SendingManual    = "manual"    // the provider has no API path here: follow the steps
)

// SendingGiveUp is how long the box keeps checking a domain.
const SendingGiveUp = 48 * time.Hour

// SendingDomain is a project's own sending domain and how far its setup is.
type SendingDomain struct {
	Project      string          `json:"project"`
	Domain       string          `json:"domain"`
	From         string          `json:"from" doc:"The sender the project gets once the domain is verified"`
	Provider     string          `json:"provider" doc:"The relay's mail service"`
	ProviderName string          `json:"providerName"`
	ProviderID   string          `json:"providerId,omitempty" doc:"The domain's ID at the provider"`
	State        string          `json:"state" enum:"verifying,verified,failed,manual"`
	Detail       string          `json:"detail" doc:"Where it stands, in plain words"`
	Hint         string          `json:"hint,omitempty" doc:"What to do, when it needs a person"`
	DNS          string          `json:"dns" enum:"auto,manual" doc:"auto: the box wrote the records through the connected DNS provider; manual: add them where the domain's DNS lives"`
	DNSError     string          `json:"dnsError,omitempty" doc:"Why the box could not write the records itself"`
	Records      []SendingRecord `json:"records"`
	DomainURL    string          `json:"domainUrl,omitempty" doc:"The provider's page for sending domains"`
	SenderSet    bool            `json:"senderSet" doc:"The project's sender is on this domain"`
	SenderChange string          `json:"senderChange,omitempty" doc:"The change in History that set the sender"`
	StartedAt    time.Time       `json:"startedAt"`
	CheckedAt    time.Time       `json:"checkedAt,omitzero"`
	NextCheckAt  time.Time       `json:"nextCheckAt,omitzero" doc:"When the box checks again"`
	VerifiedAt   time.Time       `json:"verifiedAt,omitzero"`
	GiveUpAt     time.Time       `json:"giveUpAt,omitzero" doc:"When the box stops checking"`
	Checks       int             `json:"checks"`
	StartedBy    string          `json:"startedBy,omitempty"`
}

// SendingView is a project's Email page answer: the setup, if any, and
// what the box can do with the relay it has.
type SendingView struct {
	Setup        *SendingDomain `json:"setup,omitempty"`
	Relay        bool           `json:"relay" doc:"The box has a relay; without one there is nothing to set up yet"`
	Provider     string         `json:"provider,omitempty"`
	ProviderName string         `json:"providerName,omitempty"`
	Automatic    bool           `json:"automatic" doc:"The box can set the domain up through the provider's API"`
	DomainURL    string         `json:"domainUrl,omitempty"`
	From         string         `json:"from,omitempty" doc:"The project's sender now"`
	Suggested    string         `json:"suggested,omitempty" doc:"A domain to offer: the domain of the project's sender, when it isn't the box's"`
	Domain       string         `json:"domain,omitempty" doc:"The domain asked about (?domain=)"`
	DNSManaged   bool           `json:"dnsManaged" doc:"The box's connected DNS provider holds that domain, so the box writes its records"`
}

var domainNameRE = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)
var localPartRE = regexp.MustCompile(`^[a-zA-Z0-9.!#$%&'*+/=?^_{|}~-]{1,64}$`)

func sendingKey(project string) string { return "sending/" + project }

func (m *Module) now() time.Time {
	if m.clock != nil {
		return m.clock()
	}
	return time.Now()
}

func getSending(ctx context.Context, p *platform.Platform, project string) (*SendingDomain, error) {
	raw, ok, err := p.DB.KVGet(ctx, kvNS, sendingKey(project))
	if err != nil || !ok {
		return nil, err
	}
	var s SendingDomain
	return &s, json.Unmarshal(raw, &s)
}

func putSending(ctx context.Context, p *platform.Platform, s *SendingDomain) error {
	if s.Records == nil {
		s.Records = []SendingRecord{}
	}
	raw, _ := json.Marshal(s)
	return p.DB.KVPut(ctx, kvNS, sendingKey(s.Project), raw)
}

// nextCheck is the wait after n checks: 1, 2, 4, 8, 15, 30 minutes, then hourly.
func nextCheck(n int) time.Duration {
	steps := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 30 * time.Minute}
	if n < len(steps) {
		return steps[n]
	}
	return time.Hour
}

// relayClient returns the relay, its provider's domain API (nil when it has
// none) and the provider's preset.
func relayClient(ctx context.Context, p *platform.Platform) (*Relay, domainAPI, *Preset, error) {
	r, err := getRelay(ctx, p)
	if err != nil || r == nil {
		return nil, nil, nil, err
	}
	pw, err := relayPassword(ctx, p)
	if err != nil {
		return nil, nil, nil, err
	}
	pr := PresetByID(r.provider())
	if pw == "" {
		return r, nil, pr, nil
	}
	return r, domainClient(r.provider(), pw), pr, nil
}

// dmarcRecord is the DMARC policy to add when the domain has none (nil when
// it has one, or DNS did not answer). It is p=none: receivers report but
// still deliver, which is safe to start with.
func dmarcRecord(ctx context.Context, domain string) (*SendingRecord, bool) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	found, err := lookupPrefixed(ctx, domainResolver, "_dmarc."+domain, "v=dmarc1")
	if err != nil {
		return nil, false
	}
	if len(found) > 0 {
		return &SendingRecord{Purpose: "dmarc", Type: "TXT", Name: "_dmarc." + domain, Value: found[0], Status: "ok",
			Detail: "Already in place."}, true
	}
	return &SendingRecord{Purpose: "dmarc", Type: "TXT", Name: "_dmarc." + domain, Value: "v=DMARC1; p=none;", Status: "pending",
		Detail: "No DMARC policy yet. p=none is safe to start with: receivers report failures but still deliver."}, false
}

func withHosts(recs []SendingRecord, domain string) []SendingRecord {
	zone := dnskit.Zone(domain)
	for i := range recs {
		recs[i].Host = dnskit.RelHost(recs[i].Name, zone)
	}
	return recs
}

// writeDNS writes recs through the connected DNS provider when it holds the
// domain. It reports whether it did, and why not when it tried and failed.
func writeDNS(ctx context.Context, p *platform.Platform, domain string, recs []SendingRecord, by string) (bool, string) {
	if p.DNS == nil || !p.DNS.Manages(ctx, domain) {
		return false, ""
	}
	var out []dnskit.Record
	for _, r := range recs {
		if r.Status == "ok" && r.Purpose == "dmarc" {
			continue // there already
		}
		out = append(out, dnskit.Record{Type: r.Type, Name: r.Name, Value: r.Value})
	}
	if len(out) == 0 {
		return true, ""
	}
	if err := p.DNS.SetRecords(ctx, out, by); err != nil {
		return false, "The box could not write the records: " + err.Error()
	}
	for i := range recs {
		recs[i].Written = true
	}
	return true, ""
}

// startSending sets domain up as project's sending domain.
func (m *Module) startSending(ctx context.Context, p *platform.Platform, project, domain, local string, by *tokens.Principal) (*SendingDomain, error) {
	m.sendMu.Lock()
	defer m.sendMu.Unlock()
	s, err := m.settings(ctx, p, project)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errNoProject
	}
	name := ""
	if a, err := mail.ParseAddress(s.From); err == nil {
		name = a.Name
	}
	from := local + "@" + domain
	if name != "" {
		from = (&mail.Address{Name: name, Address: from}).String()
	}
	relay, client, preset, err := relayClient(ctx, p)
	if err != nil {
		return nil, err
	}
	if relay == nil {
		return nil, &problemError{status: 422, code: "precondition", msg: "Connect a mail service first: the box sets the domain up with it.",
			hint: "Settings › Email › Connect a mail service"}
	}
	now := m.now().UTC()
	sd := &SendingDomain{Project: project, Domain: domain, From: from, Provider: relay.provider(), ProviderName: preset.Name,
		DomainURL: preset.DomainURL, DNS: "manual", StartedAt: now, StartedBy: by.Name, GiveUpAt: now.Add(SendingGiveUp)}
	dmarc, _ := dmarcRecord(ctx, domain)

	if client == nil {
		// No API path: the owner adds the domain at the provider; the box
		// shows the records receivers check, and DMARC if it is missing.
		sd.State = SendingManual
		sd.Detail = "Add " + domain + " in " + preset.Name + ", then add the records it gives you where the domain's DNS lives."
		chk, _ := checkDomain(ctx, domainResolver, from, p.Domain, relay.Host, "")
		if chk != nil {
			for _, r := range chk.Records {
				st := "pending"
				if r.State == "ok" {
					st = "ok"
				} else if r.State == "warn" {
					st = "failed"
				}
				v := r.Want
				if v == "" && len(r.Found) > 0 {
					v = r.Found[0]
				}
				sd.Records = append(sd.Records, SendingRecord{Purpose: r.Kind, Type: r.Type, Name: r.Name, Value: v, Status: st, Detail: r.Detail})
			}
		}
		sd.Records = withHosts(sd.Records, domain)
		if err := putSending(ctx, p, sd); err != nil {
			return nil, err
		}
		_ = p.DB.Audit(ctx, by.TokenID, "email.sending.start", project+"/"+domain, map[string]any{"provider": sd.Provider, "state": sd.State, "session": by.Session})
		return sd, nil
	}

	actx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	pd, err := client.find(actx, domain)
	if err != nil {
		return nil, err
	}
	reused := pd != nil
	if pd == nil {
		if pd, err = client.create(actx, domain); err != nil {
			return nil, err
		}
	}
	sd.ProviderID = pd.ID
	sd.Records = pd.Records
	if dmarc != nil {
		sd.Records = append(sd.Records, *dmarc)
	}
	sd.Records = withHosts(sd.Records, domain)
	if !pd.Valid {
		auto, why := writeDNS(ctx, p, domain, sd.Records, by.TokenID)
		if auto {
			sd.DNS = "auto"
		}
		sd.DNSError = why
		// Ask the provider to look now; DNS may take a while, so a "no" is normal.
		if v, err := client.verify(actx, pd); err == nil {
			pd = v
			mergeRecords(sd, v.Records)
		}
	} else {
		if p.DNS != nil && p.DNS.Manages(ctx, domain) {
			sd.DNS = "auto"
		}
		if dmarc != nil && dmarc.Status != "ok" {
			if auto, why := writeDNS(ctx, p, domain, []SendingRecord{*dmarc}, by.TokenID); auto {
				for i := range sd.Records {
					if sd.Records[i].Purpose == "dmarc" {
						sd.Records[i].Written = true
					}
				}
			} else {
				sd.DNSError = why
			}
		}
	}
	sd.CheckedAt, sd.Checks = now, 1
	_ = p.DB.Audit(ctx, by.TokenID, "email.sending.start", project+"/"+domain,
		map[string]any{"provider": sd.Provider, "providerId": sd.ProviderID, "reused": reused, "dns": sd.DNS, "session": by.Session})
	if pd.Valid {
		m.verified(ctx, p, sd)
	} else {
		sd.State = SendingVerifying
		sd.NextCheckAt = now.Add(nextCheck(0))
		sd.Detail = waitingDetail(sd)
	}
	if err := putSending(ctx, p, sd); err != nil {
		return nil, err
	}
	return sd, nil
}

func waitingDetail(sd *SendingDomain) string {
	if sd.DNS == "auto" {
		return "The records are in your DNS. " + sd.ProviderName + " usually sees them within a few minutes; the box keeps checking."
	}
	return "Add the records below where " + sd.Domain + "'s DNS lives. The box checks with " + sd.ProviderName + " until it sees them."
}

// mergeRecords takes the provider's latest record states, keeping what the box knows (written, DMARC).
func mergeRecords(sd *SendingDomain, latest []SendingRecord) {
	for _, l := range latest {
		for i := range sd.Records {
			r := &sd.Records[i]
			if r.Purpose != "dmarc" && strings.EqualFold(r.Name, l.Name) && strings.EqualFold(r.Type, l.Type) {
				r.Status, r.Detail, r.Value = l.Status, l.Detail, l.Value
			}
		}
	}
}

// checkSending asks the provider about a project's domain once.
func (m *Module) checkSending(ctx context.Context, p *platform.Platform, project string, byHand bool) (*SendingDomain, error) {
	m.sendMu.Lock()
	defer m.sendMu.Unlock()
	sd, err := getSending(ctx, p, project)
	if err != nil || sd == nil {
		return sd, err
	}
	now := m.now().UTC()
	if sd.State == SendingManual {
		return sd, nil
	}
	if sd.State == SendingVerified {
		if !sd.SenderSet && byHand {
			m.verified(ctx, p, sd)
			return sd, putSending(ctx, p, sd)
		}
		return sd, nil
	}
	if sd.State == SendingFailed {
		if !byHand {
			return sd, nil
		}
		// A person asked again: another 48 hours.
		sd.GiveUpAt, sd.Hint = now.Add(SendingGiveUp), ""
	}
	relay, client, _, err := relayClient(ctx, p)
	if err != nil {
		return nil, err
	}
	sd.CheckedAt = now
	sd.Checks++
	switch {
	case relay == nil:
		sd.State, sd.Detail, sd.Hint = SendingFailed, "The box no longer has a mail service.", "Connect one in Settings › Email, then set the domain up again."
	case relay.provider() != sd.Provider:
		name := relay.provider()
		if pr := PresetByID(name); pr != nil {
			name = pr.Name
		}
		sd.State, sd.Detail = SendingFailed, "The box now sends through "+name+", not "+sd.ProviderName+"."
		sd.Hint = "Set the domain up again: the box does it with " + name + "."
	case client == nil:
		sd.State, sd.Detail, sd.Hint = SendingFailed, "The relay's key is gone.", "Paste the key in Settings › Email, then check again."
	default:
		actx, cancel := context.WithTimeout(ctx, 45*time.Second)
		v, err := client.verify(actx, &providerDomain{ID: sd.ProviderID, Domain: sd.Domain, Records: sd.Records})
		cancel()
		var pe *providerError
		switch {
		case errors.As(err, &pe) && (pe.Permission || pe.Status == 401 || pe.Status == 404):
			sd.State, sd.Detail, sd.Hint = SendingFailed, pe.Msg, pe.Hint
			if pe.Status == 404 {
				sd.Detail, sd.Hint = sd.ProviderName+" no longer has "+sd.Domain+".", "Set the domain up again."
			}
		case err != nil:
			sd.Detail = "Couldn't reach " + sd.ProviderName + " (" + err.Error() + "). The box tries again."
		default:
			mergeRecords(sd, v.Records)
			if d, ok := dmarcRecord(ctx, sd.Domain); ok {
				for i := range sd.Records {
					if sd.Records[i].Purpose == "dmarc" && sd.Records[i].Status != "ok" {
						sd.Records[i].Status, sd.Records[i].Detail = "ok", d.Detail
					}
				}
			}
			if v.Valid {
				m.verified(ctx, p, sd)
				return sd, putSending(ctx, p, sd)
			}
			sd.State = SendingVerifying
			sd.Detail = waitingDetail(sd)
		}
	}
	if sd.State == SendingVerifying {
		if now.After(sd.GiveUpAt) {
			sd.State = SendingFailed
			sd.Detail = sd.ProviderName + " still doesn't see the records after 48 hours, so the box stopped checking."
			sd.Hint = "Check each record below is in " + sd.Domain + "'s DNS exactly as shown, then check again."
			sd.NextCheckAt = time.Time{}
		} else {
			sd.NextCheckAt = now.Add(nextCheck(sd.Checks - 1))
		}
	} else {
		sd.NextCheckAt = time.Time{}
	}
	return sd, putSending(ctx, p, sd)
}

// verified marks a domain verified and makes it the project's sender, with
// a change in History. A sender already on the domain is kept as it is.
func (m *Module) verified(ctx context.Context, p *platform.Platform, sd *SendingDomain) {
	now := m.now().UTC()
	if sd.State != SendingVerified {
		sd.VerifiedAt = now
	}
	sd.State, sd.NextCheckAt, sd.Hint = SendingVerified, time.Time{}, ""
	for i := range sd.Records {
		if sd.Records[i].Purpose != "dmarc" {
			sd.Records[i].Status, sd.Records[i].Detail = "ok", ""
		}
	}
	s, err := m.settings(ctx, p, sd.Project)
	if err == nil && s != nil {
		if a, err := mail.ParseAddress(s.From); err == nil && strings.HasSuffix(strings.ToLower(a.Address), "@"+sd.Domain) {
			sd.SenderSet, sd.From = true, s.From
			sd.Detail = sd.ProviderName + " verified " + sd.Domain + ". Mail is sent from " + s.From + "."
			return
		}
	}
	id, err := m.setSender(ctx, p, sd)
	switch {
	case err == nil:
		sd.SenderSet, sd.SenderChange = true, id
		sd.Detail = sd.ProviderName + " verified " + sd.Domain + ". Mail is now sent from " + sd.From + "."
	case errors.Is(err, errNoProject):
		sd.Detail = sd.ProviderName + " verified " + sd.Domain + ". Turn email on for " + sd.Project + " to send from it."
	default:
		sd.Detail = sd.ProviderName + " verified " + sd.Domain + ", but the box couldn't change the sender: " + err.Error()
		sd.Hint = "Change the sender to " + sd.From + " under Sending domain."
	}
}

// setSender applies services.email.from = sd.From as a change.
func (m *Module) setSender(ctx context.Context, p *platform.Platform, sd *SendingDomain) (string, error) {
	if p.Engine == nil {
		return "", errors.New("the change engine is not available")
	}
	addr := change.KindService + "/email"
	var spec json.RawMessage
	plan, err := p.Engine.PlanEdit(ctx, sd.Project, func(cur map[string]change.Resource) (map[string]change.Resource, error) {
		r, ok := cur[addr]
		if !ok {
			return nil, errNoProject
		}
		v := map[string]any{}
		if len(r.Spec) > 0 {
			if err := json.Unmarshal(r.Spec, &v); err != nil {
				return nil, err
			}
		}
		v["from"] = sd.From
		spec, _ = json.Marshal(v)
		r.Spec = spec
		cur[addr] = r
		return cur, nil
	})
	if err != nil {
		return "", err
	}
	c, err := p.Engine.Apply(ctx, change.ApplyRequest{Plan: plan, Confirm: plan.Hash,
		Actor:  change.Actor{Kind: "system", ID: "system", Name: "Email"},
		Intent: fmt.Sprintf("Send %s's mail from %s: %s verified %s", sd.Project, sd.From, sd.ProviderName, sd.Domain)})
	if err != nil {
		return "", err
	}
	// Now, so the page and new mail use it at once; converging writes the same.
	if err := m.Reconcile(ctx, p, sd.Project, addr, spec); err != nil {
		return "", err
	}
	if c == nil {
		return "", nil
	}
	go p.AfterApply(c)
	return c.ID, nil
}

// sendingLoop re-checks domains that are waiting for their records.
func (m *Module) sendingLoop(ctx context.Context, p *platform.Platform) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.checkDue(ctx, p)
		}
	}
}

// checkDue checks every waiting domain whose next check is due.
func (m *Module) checkDue(ctx context.Context, p *platform.Platform) {
	all, err := p.DB.KVList(ctx, kvNS)
	if err != nil {
		return
	}
	now := m.now()
	for k, raw := range all {
		project, ok := strings.CutPrefix(k, "sending/")
		if !ok {
			continue
		}
		var sd SendingDomain
		if json.Unmarshal(raw, &sd) != nil || sd.State != SendingVerifying || sd.NextCheckAt.After(now) {
			continue
		}
		if _, err := m.checkSending(ctx, p, project, false); err != nil && p.Log != nil {
			p.Log.Warn("email: sending domain check failed", "project", project, "err", err)
		}
	}
}

// problemError is an API problem raised from module code.
type problemError struct {
	status    int
	code, msg string
	hint      string
}

func (e *problemError) Error() string { return e.msg }

func sendingProblem(err error) error {
	var pe *problemError
	var prov *providerError
	switch {
	case errors.As(err, &pe):
		out := api.NewProblem(pe.status, pe.code, pe.msg)
		out.Hint = pe.hint
		return out
	case errors.As(err, &prov):
		out := api.NewProblem(422, "precondition", prov.Msg)
		out.Hint = prov.Hint
		return out
	}
	return toProblem(err)
}

// suggestDomain offers the sender's domain, unless it is the box's.
func suggestDomain(from, boxDomain string) string {
	if a, err := mail.ParseAddress(from); err == nil {
		_, d, _ := strings.Cut(strings.ToLower(a.Address), "@")
		if d != "" && d != strings.ToLower(boxDomain) && !strings.HasSuffix(d, "."+strings.ToLower(boxDomain)) {
			return d
		}
	}
	return ""
}

func (m *Module) sendingView(ctx context.Context, p *platform.Platform, project, domain string) (*SendingView, error) {
	sd, err := getSending(ctx, p, project)
	if err != nil {
		return nil, err
	}
	v := &SendingView{Setup: sd}
	relay, client, preset, err := relayClient(ctx, p)
	if err != nil {
		return nil, err
	}
	if relay != nil {
		v.Relay, v.Provider, v.Automatic = true, relay.provider(), client != nil
		if preset != nil {
			v.ProviderName, v.DomainURL = preset.Name, preset.DomainURL
		}
	}
	if s, _ := m.settings(ctx, p, project); s != nil {
		v.From, v.Suggested = s.From, suggestDomain(s.From, p.Domain)
	}
	if domain == "" && sd != nil {
		domain = sd.Domain
	}
	if domain == "" {
		domain = v.Suggested
	}
	if domain != "" && domainNameRE.MatchString(domain) {
		v.Domain = domain
		v.DNSManaged = p.DNS != nil && p.DNS.Manages(ctx, domain)
	}
	return v, nil
}

// registerSendingAPI adds the "send from my domain" operations.
func (m *Module) registerSendingAPI(a huma.API, p *platform.Platform, base, tag string) {
	huma.Register(a, api.Op("email-sending-domain-get", http.MethodGet, base+"/sending-domain", "email sending-domain get", api.RiskRead,
		"Show the project's own sending domain",
		"The domain the project sends from once it is set up, where its setup stands (verifying, verified, failed, manual), the DNS records "+
			"and their state, and whether the box can set a domain up through the relay provider's API (SendGrid, Resend).", tag),
		api.Wrap(func(ctx context.Context, in *struct {
			Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
			Domain  string `query:"domain" maxLength:"253" doc:"Also say whether the box's DNS provider holds this domain"`
		}) (*struct{ Body SendingView }, error) {
			if err := boxOnly(p); err != nil {
				return nil, err
			}
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
				return nil, err
			}
			v, err := m.sendingView(ctx, p, in.Project, strings.ToLower(strings.TrimSpace(in.Domain)))
			if err != nil {
				return nil, err
			}
			return &struct{ Body SendingView }{*v}, nil
		}))

	start := api.Op("email-sending-domain-set", http.MethodPost, base+"/sending-domain", "email sending-domain set", api.RiskWrite,
		"Send from my domain",
		"Sets a domain up with the relay's mail service through its API (SendGrid: domain authentication with automatic security; Resend: "+
			"a domain), reusing it when the provider already has it. When the box's connected DNS provider holds the domain, the box writes "+
			"the records (and a DMARC p=none policy if there is none); otherwise the answer lists them to add. The box then checks until the "+
			"provider verifies the domain (up to 48 hours) and makes local@domain the project's sender, as a change in History. "+
			"For providers without an API path the answer lists the steps. Box admins only (the owner, or a key with full access to all projects).", tag)
	start.Errors = append(start.Errors, 404)
	huma.Register(a, api.Outbound(start), api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Body    struct {
			Domain string `json:"domain" minLength:"3" maxLength:"253" doc:"The domain to send from, e.g. example.com or mail.example.com"`
			Local  string `json:"local,omitempty" maxLength:"64" doc:"The part before the @. Default hello"`
		}
	}) (*struct{ Body SendingView }, error) {
		if err := boxOnly(p); err != nil {
			return nil, err
		}
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyOutbound, in.Project); err != nil {
			return nil, err
		}
		// The domain may be anyone's: setting it up creates it in the
		// box's mail-provider account and writes its records through the
		// box's DNS provider, and the project then sends as it. A key for
		// the project can't vouch for that; the owner can.
		if !pr.BoxAdmin() {
			return nil, fmt.Errorf("%w: a sending domain is set up by the box owner (or a key with full access to all projects): it uses the box's mail and DNS accounts for a domain no project owns", tokens.ErrForbidden)
		}
		domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(in.Body.Domain)), ".")
		domain = strings.TrimPrefix(strings.TrimPrefix(domain, "https://"), "http://")
		if !domainNameRE.MatchString(domain) || len(domain) > 253 {
			return nil, api.NewProblem(422, "validation", "domain: a domain name, e.g. example.com")
		}
		local := strings.TrimSpace(in.Body.Local)
		if local == "" {
			local = "hello"
		}
		if !localPartRE.MatchString(local) || strings.HasPrefix(local, ".") || strings.HasSuffix(local, ".") || strings.Contains(local, "..") {
			return nil, api.NewProblem(422, "validation", "local: the part before the @, e.g. hello")
		}
		if _, err := m.startSending(ctx, p, in.Project, domain, local, pr); err != nil {
			return nil, sendingProblem(err)
		}
		v, err := m.sendingView(ctx, p, in.Project, "")
		if err != nil {
			return nil, err
		}
		return &struct{ Body SendingView }{*v}, nil
	}))

	check := api.Op("email-sending-domain-check", http.MethodPost, base+"/sending-domain/check", "email sending-domain check", api.RiskWrite,
		"Check the sending domain now",
		"Asks the provider about the domain now instead of waiting for the next background check. On a setup that stopped (48 hours "+
			"without the records), it starts another 48 hours of checks.", tag)
	check.Errors = append(check.Errors, 404)
	huma.Register(a, api.Outbound(check), api.Wrap(func(ctx context.Context, in *projectIn) (*struct{ Body SendingView }, error) {
		if err := boxOnly(p); err != nil {
			return nil, err
		}
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeApplyOutbound, in.Project); err != nil {
			return nil, err
		}
		sd, err := m.checkSending(ctx, p, in.Project, true)
		if err != nil {
			return nil, sendingProblem(err)
		}
		if sd == nil {
			return nil, api.NewProblem(404, "not_found", in.Project+" has no sending domain set up")
		}
		v, err := m.sendingView(ctx, p, in.Project, "")
		if err != nil {
			return nil, err
		}
		return &struct{ Body SendingView }{*v}, nil
	}))

	del := api.Op("email-sending-domain-delete", http.MethodDelete, base+"/sending-domain", "email sending-domain delete", api.RiskWrite,
		"Stop setting up the sending domain",
		"Forgets the setup and stops the checks. The domain stays at the provider and the records stay in DNS; the project's sender is "+
			"not changed (change it under Sending domain).", tag)
	huma.Register(a, del, api.Wrap(func(ctx context.Context, in *projectIn) (*struct{ Body SendingView }, error) {
		if err := boxOnly(p); err != nil {
			return nil, err
		}
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		m.sendMu.Lock()
		err := p.DB.KVDelete(ctx, kvNS, sendingKey(in.Project))
		m.sendMu.Unlock()
		if err != nil {
			return nil, err
		}
		_ = p.DB.Audit(ctx, pr.TokenID, "email.sending.delete", in.Project, map[string]any{"session": pr.Session})
		v, err := m.sendingView(ctx, p, in.Project, "")
		if err != nil {
			return nil, err
		}
		return &struct{ Body SendingView }{*v}, nil
	}))
}
