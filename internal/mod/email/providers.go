package email

import (
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"

	"github.com/emersion/go-smtp"
)

// Relay providers: the mail services the box knows how to talk to. Picking
// one fills the SMTP host, port, security and username, so the owner only
// pastes a key. Every value here was checked against the provider's own
// docs (October 2026); the links say where.

// Provider IDs.
const (
	ProviderSendGrid = "sendgrid"
	ProviderResend   = "resend"
	ProviderPostmark = "postmark"
	ProviderSES      = "ses"
	ProviderMailgun  = "mailgun"
	ProviderBrevo    = "brevo"
	// ProviderCloudflare is Cloudflare Email Service (public beta since April 2026).
	ProviderCloudflare = "cloudflare"
	ProviderOther      = "other"
)

// Region is one place a provider sends from, with its SMTP host.
type Region struct {
	ID    string `json:"id" doc:"Region code, e.g. eu-west-1"`
	Label string `json:"label" doc:"Plain name, e.g. Europe (Ireland)"`
	Host  string `json:"host"`
}

// EventsSetup says how a provider reports delivery events to the box.
type EventsSetup struct {
	Security  string   `json:"security" enum:"ecdsa,svix,basic" doc:"ecdsa: signed with the provider's private key, checked with the public key you paste; svix: HMAC-SHA256 with the signing secret you paste; basic: the box makes a password that goes in the webhook address"`
	SetupURL  string   `json:"setupUrl" doc:"Where to add the webhook in the provider's dashboard"`
	Enable    []string `json:"enable" doc:"Which events to turn on, in the provider's own words"`
	KeyLabel  string   `json:"keyLabel,omitempty" doc:"What the provider calls the key to paste back (empty for basic: the box makes it)"`
	KeyHint   string   `json:"keyHint,omitempty" doc:"Where the key is shown"`
	Docs      string   `json:"docs" doc:"The provider's webhook documentation"`
	Steps     []string `json:"steps" doc:"The steps in the provider's dashboard, in order"`
	OpenNotes string   `json:"openNotes,omitempty" doc:"About open and click tracking, when the provider has it"`
}

// Preset is a relay provider the box can fill in.
type Preset struct {
	ID       string `json:"id" enum:"sendgrid,resend,postmark,ses,mailgun,brevo,cloudflare,other"`
	Name     string `json:"name"`
	Beta     bool   `json:"beta,omitempty" doc:"The provider calls this service a beta"`
	Note     string `json:"note,omitempty" doc:"Plan, price or limits worth knowing before choosing it"`
	Host     string `json:"host,omitempty" doc:"SMTP host; empty when it depends on the region"`
	Port     int    `json:"port,omitempty"`
	TLS      string `json:"tls,omitempty" enum:"starttls,tls,none"`
	Username string `json:"username,omitempty" doc:"The fixed SMTP username, when the provider has one"`
	// UsernameIsKey: the provider wants the key as both username and password.
	UsernameIsKey bool     `json:"usernameIsKey,omitempty" doc:"The key is the username too (Postmark server tokens)"`
	UsernameLabel string   `json:"usernameLabel,omitempty" doc:"What to ask for when the username is the owner's to give"`
	UsernameHint  string   `json:"usernameHint,omitempty"`
	KeyLabel      string   `json:"keyLabel" doc:"What the provider calls the password"`
	KeyHint       string   `json:"keyHint,omitempty" doc:"A short note on the key, e.g. its prefix or that it is not the API key"`
	KeyURL        string   `json:"keyUrl,omitempty" doc:"Where to create the key"`
	Permission    string   `json:"permission,omitempty" doc:"The permission the key needs"`
	DomainURL     string   `json:"domainUrl,omitempty" doc:"Where to verify a sending domain"`
	SMTPDocs      string   `json:"smtpDocs,omitempty" doc:"The provider's SMTP documentation"`
	Regions       []Region `json:"regions,omitempty" doc:"Regions to choose from; the host follows the region"`
	// Events is set for providers whose delivery events the box can read.
	Events *EventsSetup `json:"events,omitempty"`
	// EventsNote says why delivery events are not available, when they are not.
	EventsNote string   `json:"eventsNote,omitempty" doc:"Why the box can't read this provider's delivery events, when it can't"`
	hosts      []string // host suffixes that identify the provider on an old relay
}

var sesRegions = []Region{
	{"us-east-1", "US East (N. Virginia)", ""}, {"us-east-2", "US East (Ohio)", ""}, {"us-west-1", "US West (N. California)", ""},
	{"us-west-2", "US West (Oregon)", ""}, {"ca-central-1", "Canada (Central)", ""}, {"sa-east-1", "South America (São Paulo)", ""},
	{"eu-west-1", "Europe (Ireland)", ""}, {"eu-west-2", "Europe (London)", ""}, {"eu-west-3", "Europe (Paris)", ""},
	{"eu-central-1", "Europe (Frankfurt)", ""}, {"eu-north-1", "Europe (Stockholm)", ""}, {"ap-south-1", "Asia Pacific (Mumbai)", ""},
	{"ap-northeast-1", "Asia Pacific (Tokyo)", ""}, {"ap-northeast-2", "Asia Pacific (Seoul)", ""}, {"ap-northeast-3", "Asia Pacific (Osaka)", ""},
	{"ap-southeast-1", "Asia Pacific (Singapore)", ""}, {"ap-southeast-2", "Asia Pacific (Sydney)", ""},
}

func init() {
	for i := range sesRegions {
		sesRegions[i].Host = "email-smtp." + sesRegions[i].ID + ".amazonaws.com"
	}
}

// Presets are the known providers, SendGrid and Resend first.
var Presets = []Preset{
	{
		ID: ProviderSendGrid, Name: "SendGrid", Host: "smtp.sendgrid.net", Port: 587, TLS: TLSStartTLS, Username: "apikey",
		KeyLabel: "API key", KeyHint: "Starts with SG.", KeyURL: "https://app.sendgrid.com/settings/api_keys",
		Permission: "Restricted Access, with Mail Send on",
		DomainURL:  "https://app.sendgrid.com/settings/sender_auth",
		SMTPDocs:   "https://www.twilio.com/docs/sendgrid/for-developers/sending-email/integrating-with-the-smtp-api",
		Events: &EventsSetup{
			Security: "ecdsa", SetupURL: "https://app.sendgrid.com/settings/mail_settings/webhook_settings",
			Enable:   []string{"Delivered", "Deferred", "Bounced", "Dropped", "Spam Reports", "Unsubscribes", "Opened (optional)", "Clicked (optional)"},
			KeyLabel: "Verification key", KeyHint: "Shown on the webhook once Signed Event Webhook is on. It is a public key, starting with MFkw.",
			Docs: "https://www.twilio.com/docs/sendgrid/for-developers/tracking-events/getting-started-event-webhook-security-features",
			Steps: []string{
				"Open Settings, Mail Settings, Event Webhooks and choose Create new webhook.",
				"Paste the address below as the Post URL.",
				"Tick the actions listed below.",
				"Under Security features, turn on Signed Event Webhook and save.",
				"Open the webhook again, copy the verification key and paste it here.",
				"Use Test Integration: the box shows Receiving events when the first signed request arrives.",
			},
			OpenNotes: "Opens and clicks only arrive when open and click tracking are on in SendGrid's Tracking settings.",
		},
		hosts: []string{"sendgrid.net"},
	},
	{
		ID: ProviderResend, Name: "Resend", Host: "smtp.resend.com", Port: 587, TLS: TLSStartTLS, Username: "resend",
		KeyLabel: "API key", KeyHint: "Starts with re_", KeyURL: "https://resend.com/api-keys",
		Permission: "Sending access (it can be limited to one domain)",
		DomainURL:  "https://resend.com/domains",
		SMTPDocs:   "https://resend.com/docs/send-with-smtp",
		Events: &EventsSetup{
			Security: "svix", SetupURL: "https://resend.com/webhooks",
			Enable:   []string{"email.delivered", "email.delivery_delayed", "email.bounced", "email.complained", "email.failed", "email.suppressed", "email.opened (optional)", "email.clicked (optional)"},
			KeyLabel: "Signing secret", KeyHint: "On the webhook's page in Resend. It starts with whsec_",
			Docs: "https://resend.com/docs/webhooks/verify-webhooks-requests",
			Steps: []string{
				"Open Webhooks and choose Add webhook.",
				"Paste the address below as the endpoint.",
				"Pick the events listed below and add it.",
				"Open the webhook, reveal the signing secret and paste it here.",
				"Send a message: the box shows Receiving events when the first signed request arrives.",
			},
			OpenNotes: "Opens and clicks only arrive when tracking is on for the domain in Resend.",
		},
		hosts: []string{"resend.com"},
	},
	{
		ID: ProviderPostmark, Name: "Postmark", Host: "smtp.postmarkapp.com", Port: 587, TLS: TLSStartTLS, UsernameIsKey: true,
		KeyLabel: "Server API token", KeyHint: "From the server's API Tokens tab. Postmark uses it as the username and the password.",
		KeyURL:     "https://account.postmarkapp.com/servers",
		Permission: "A server token; mail goes out on its default transactional stream",
		DomainURL:  "https://account.postmarkapp.com/signature_domains",
		SMTPDocs:   "https://postmarkapp.com/developer/user-guide/send-email-with-smtp",
		Events: &EventsSetup{
			Security: "basic", SetupURL: "https://account.postmarkapp.com/servers",
			Enable: []string{"Delivery", "Bounce", "Spam Complaint", "Open (optional)", "Click (optional)"},
			Docs:   "https://postmarkapp.com/developer/webhooks/webhooks-overview",
			Steps: []string{
				"Open your server, then the transactional stream's Webhooks tab, and choose Add webhook.",
				"Paste the address below, with its user name and password in it. Postmark has no signatures, so the password is the check.",
				"Tick the events listed below and save.",
				"Use Send test: the box shows Receiving events when the first request with the right password arrives.",
			},
		},
		hosts: []string{"postmarkapp.com"},
	},
	{
		ID: ProviderSES, Name: "Amazon SES", Port: 587, TLS: TLSStartTLS,
		UsernameLabel: "SMTP username", UsernameHint: "Shown with the password when you create SMTP credentials. Not your AWS access key.",
		KeyLabel: "SMTP password", KeyHint: "SES SMTP credentials are their own pair and belong to one region.",
		KeyURL:     "https://console.aws.amazon.com/ses/home#/smtp",
		Permission: "ses:SendRawEmail (the SES console's Create SMTP credentials sets it up)",
		DomainURL:  "https://console.aws.amazon.com/ses/home#/identities",
		SMTPDocs:   "https://docs.aws.amazon.com/ses/latest/dg/smtp-credentials.html",
		Regions:    sesRegions,
		hosts:      []string{"amazonaws.com"},
	},
	{
		ID: ProviderMailgun, Name: "Mailgun", Port: 587, TLS: TLSStartTLS,
		UsernameLabel: "SMTP username", UsernameHint: "Usually postmaster@ your sending domain.",
		KeyLabel: "SMTP password", KeyHint: "Per domain: Sending, Domain settings, SMTP credentials. Not the API key.",
		KeyURL:     "https://app.mailgun.com/mg/sending/domains",
		Permission: "SMTP credentials for the sending domain",
		DomainURL:  "https://app.mailgun.com/mg/sending/domains",
		SMTPDocs:   "https://documentation.mailgun.com/docs/mailgun/user-manual/sending-messages/send-smtp",
		Regions: []Region{
			{"us", "US", "smtp.mailgun.org"},
			{"eu", "EU", "smtp.eu.mailgun.org"},
		},
		hosts: []string{"mailgun.org", "mailgun.net"},
	},
	{
		ID: ProviderBrevo, Name: "Brevo", Host: "smtp-relay.brevo.com", Port: 587, TLS: TLSStartTLS,
		UsernameLabel: "SMTP login", UsernameHint: "Shown on the SMTP tab, e.g. 8a1b2c001@smtp-brevo.com.",
		KeyLabel: "SMTP key", KeyHint: "An SMTP key, not an API key.",
		KeyURL:     "https://app.brevo.com/settings/keys/smtp",
		Permission: "An SMTP key",
		DomainURL:  "https://app.brevo.com/senders/domain/list",
		SMTPDocs:   "https://developers.brevo.com/docs/smtp-integration",
		hosts:      []string{"brevo.com", "sendinblue.com"},
	},
	{
		// https://developers.cloudflare.com/email-service/api/send-emails/smtp/ (checked 2026-10-06): port 465 with
		// implicit TLS only, username the literal "api_token", password an API token with Email Sending: Edit.
		ID: ProviderCloudflare, Name: "Cloudflare Email Service", Beta: true, Host: "smtp.mx.cloudflare.net", Port: 465, TLS: TLSImplicit, Username: "api_token",
		KeyLabel: "API token", KeyHint: "An account API token. Cloudflare takes messages up to 5 MiB with at most 50 recipients.",
		KeyURL:     "https://dash.cloudflare.com/?to=/:account/api-tokens",
		Permission: "Email Sending: Edit (an account-owned token is best)",
		DomainURL:  "https://dash.cloudflare.com/?to=/:account/email-service/sending",
		SMTPDocs:   "https://developers.cloudflare.com/email-service/api/send-emails/smtp/",
		Note: "Needs the Workers Paid plan: 3,000 emails a month included, then $0.35 per 1,000. " +
			"New accounts start with a small daily quota, and the sending domain must be onboarded in Cloudflare.",
		EventsNote: "Cloudflare sends delivery events to Cloudflare Queues, not to a webhook, so the box can't read them yet.",
		hosts:      []string{"mx.cloudflare.net"},
	},
	{ID: ProviderOther, Name: "Other SMTP", KeyLabel: "Password"},
}

// PresetByID returns the known provider, or nil.
func PresetByID(id string) *Preset {
	for i := range Presets {
		if Presets[i].ID == id {
			return &Presets[i]
		}
	}
	return nil
}

// providerForHost names the provider a relay host belongs to ("" when none):
// relays saved before providers were recorded still show their service.
func providerForHost(host string) string {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	for _, pr := range Presets {
		for _, h := range pr.hosts {
			if host == h || strings.HasSuffix(host, "."+h) {
				// amazonaws.com is SES only for its SMTP hosts.
				if pr.ID == ProviderSES && !strings.HasPrefix(host, "email-smtp.") {
					continue
				}
				return pr.ID
			}
		}
	}
	return ""
}

// relayInput is what the owner gives when setting a relay.
type relayInput struct {
	Provider, Region, Host, Username, TLS string
	Port                                  int
}

// resolve fills a relay from a provider preset. Fields the owner gave win
// over the preset's (a different port, say), except that a preset's fixed
// username always applies.
func resolve(in relayInput) (*Relay, error) {
	r := &Relay{Provider: in.Provider, Region: in.Region, Host: strings.TrimSpace(in.Host), Port: in.Port, Username: in.Username, TLS: in.TLS}
	if r.Provider == "" {
		// No provider named: recognise a known host (an older client, or the
		// CLI with --host), else it is any SMTP server.
		r.Provider = providerForHost(r.Host)
		if r.Provider == "" {
			r.Provider = ProviderOther
		}
	}
	pr := PresetByID(r.Provider)
	if pr == nil {
		return nil, invalid("provider: one of %s", strings.Join(presetIDs(), ", "))
	}
	if pr.ID != ProviderOther {
		if len(pr.Regions) > 0 {
			if r.Region == "" {
				r.Region = pr.Regions[0].ID
			}
			i := slices.IndexFunc(pr.Regions, func(x Region) bool { return x.ID == r.Region })
			if i < 0 {
				return nil, invalid("region: %s has no region %q", pr.Name, r.Region)
			}
			if r.Host == "" {
				r.Host = pr.Regions[i].Host
			}
		} else {
			r.Region = ""
			if r.Host == "" {
				r.Host = pr.Host
			}
		}
		if r.TLS == "" {
			r.TLS = pr.TLS
		}
		if r.Port == 0 && r.TLS == pr.TLS {
			r.Port = pr.Port
		}
		switch {
		case pr.Username != "":
			r.Username = pr.Username
		case pr.UsernameIsKey:
			r.Username = "" // the key is sent as the username at delivery time; never stored or shown twice
		case strings.TrimSpace(r.Username) == "":
			return nil, invalid("username: %s needs its %s", pr.Name, lowerFirst(pr.UsernameLabel))
		}
	} else {
		r.Region = ""
	}
	if r.Host == "" {
		return nil, invalid("host: the relay's SMTP hostname, e.g. smtp.example.com")
	}
	if strings.ContainsAny(r.Host, " /:@") {
		return nil, invalid("host: a hostname or IP address, without scheme or port")
	}
	if r.TLS == "" {
		r.TLS = TLSStartTLS
	}
	if r.Port == 0 {
		r.Port = map[string]int{TLSStartTLS: 587, TLSImplicit: 465, TLSNone: 25}[r.TLS]
	}
	return r, nil
}

func presetIDs() []string {
	out := make([]string, len(Presets))
	for i, p := range Presets {
		out[i] = p.ID
	}
	return out
}

// smtpUser is the username to log in with.
func (r *Relay) smtpUser(password string) string {
	if r.Username == "" && r.provider() == ProviderPostmark {
		return password
	}
	return r.Username
}

// provider is the relay's provider, recorded or recognised from the host.
func (r *Relay) provider() string {
	if r.Provider != "" {
		return r.Provider
	}
	if p := providerForHost(r.Host); p != "" {
		return p
	}
	return ProviderOther
}

// label is how the relay reads in plain words: "SendGrid (smtp.sendgrid.net)".
func (r *Relay) label() string {
	if pr := PresetByID(r.provider()); pr != nil && pr.ID != ProviderOther {
		return fmt.Sprintf("%s (%s)", pr.Name, r.Host)
	}
	return r.Host
}

// explainRelayError says in plain words what a failed relay attempt means
// and what to try, from the stage it failed at and the SMTP reply code.
func explainRelayError(err error, r *Relay) string {
	if err == nil || r == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	name, keyLabel, perm, domainURL := r.Host, "password", "", ""
	if pr := PresetByID(r.provider()); pr != nil && pr.ID != ProviderOther {
		name, keyLabel, perm, domainURL = pr.Name, lowerFirst(pr.KeyLabel), pr.Permission, pr.DomainURL
	}
	addr := fmt.Sprintf("%s:%d", r.Host, r.Port)
	code := 0
	var se *smtp.SMTPError
	if errors.As(err, &se) {
		code = se.Code
	}
	var dnsErr *net.DNSError
	switch {
	case errors.As(err, &dnsErr) || strings.Contains(msg, "no such host"):
		return r.Host + " does not resolve. Check the spelling of the host."
	case strings.Contains(msg, "connection refused"):
		return "Nothing answered at " + addr + ". Check the host and the port."
	case strings.Contains(msg, "connect to relay") || strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "deadline exceeded"):
		return "The box could not reach " + addr + " in time. Some servers block outgoing mail ports: try another port the provider offers (2587, 2525 or 465), or ask your host to open it."
	case strings.Contains(msg, "starttls") || strings.Contains(msg, "x509") || strings.Contains(msg, "certificate") || strings.Contains(msg, "tls: "):
		return "The secure connection failed. Match the security to the port: STARTTLS on 587, 2587 or 2525; TLS on 465 or 2465."
	case strings.Contains(msg, "login as") || code == 535 || code == 534 || code == 530:
		s := name + " refused the " + keyLabel + ". Check you pasted all of it and that it is still active"
		if perm != "" {
			s += ". The key needs: " + perm
		}
		return s + "."
	case strings.Contains(msg, "refused sender"):
		s := name + " refused the sender address. Send from an address on a domain you have verified with " + name
		if domainURL != "" {
			s += " (" + domainURL + ")"
		}
		return s + "."
	case code >= 400 && code < 500:
		return "A temporary refusal: wait a minute and try again. Real mail is retried on its own."
	case code == 550 || code == 554 || code == 553:
		return name + " refused the message. Its reply above says why; often the sender's domain is not verified, or the account is limited."
	}
	return ""
}

// lowerFirst lowercases a label's first letter for use mid-sentence, but
// leaves acronyms ("API key", "SMTP password") alone.
func lowerFirst(s string) string {
	if len(s) > 1 && s[1] >= 'A' && s[1] <= 'Z' {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}
