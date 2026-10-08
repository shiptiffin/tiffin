package email

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/danielgtaylor/huma/v2/sse"
	"github.com/emersion/go-message/mail"
)

// Detail is one message, ready to show.
type Detail struct {
	Summary
	Envelope struct {
		From string   `json:"from"`
		To   []string `json:"to"`
	} `json:"envelope" doc:"SMTP envelope: who it is actually delivered to (includes Bcc)"`
	Headers    []Header         `json:"headers"`
	Text       string           `json:"text"`
	HTML       string           `json:"html" doc:"The HTML body, sanitised (no scripts, forms, iframes or event handlers). Show it in a sandboxed iframe"`
	Links      []string         `json:"links" doc:"http(s) links in the message, e.g. sign-in or verification links"`
	AttachList []AttachmentInfo `json:"attachmentList"`
	RawURL     string           `json:"rawUrl" doc:"Download the raw .eml (API path)"`
	Events     []Event          `json:"events" doc:"What the relay's provider reported after accepting it, oldest first (needs its webhook)"`
	Provider   string           `json:"provider,omitempty" doc:"The mail service it was relayed through"`
}

// relayTestResult is what a relay test found.
type relayTestResult struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail" doc:"What the relay said"`
	Hint   string `json:"hint,omitempty" doc:"What it means and what to try, in plain words, when the test failed"`
}

// Status is the box's email setup.
type Status struct {
	Mode      string    `json:"mode" enum:"inbox,relay" doc:"inbox: every message is captured in the dev inbox; relay: production mail is sent through the relay"`
	Relay     *Relay    `json:"relay,omitempty"`
	SMTP      []string  `json:"smtp" doc:"Addresses of Tiffin's SMTP submission server"`
	Queued    int       `json:"queued" doc:"Messages waiting for a relay attempt"`
	FailedDay int       `json:"failedLastDay" doc:"Deliveries that failed for good in the last 24 hours"`
	Webhooks  []Webhook `json:"webhooks" doc:"Delivery events: the relay provider's webhook, and any other with a key saved"`
}

type projectIn struct {
	Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
}

type messageIn struct {
	Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
	ID      string `path:"id" pattern:"^msg_[0-9A-Z]{26}$" doc:"Message ID"`
}

func boxOnly(p *platform.Platform) error {
	if p == nil {
		return api.NewProblem(501, "internal", "email is only available on a box")
	}
	return ensureSchema(context.Background(), p.DB.SQL())
}

// toProblem maps module errors to API problems.
func toProblem(err error) error {
	var ve *ValidationError
	var rl *RateLimitError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ve):
		return api.NewProblem(422, "validation", ve.Msg)
	case errors.As(err, &rl):
		pr := api.NewProblem(429, "precondition", rl.Error())
		pr.Hint = fmt.Sprintf("wait %s, or ask the box owner to raise the limit (tiffin email rate-limit set)", rl.RetryAfter.Round(time.Second))
		return pr
	case errors.Is(err, errNoProject):
		pr := api.NewProblem(404, "not_found", err.Error())
		pr.Hint = "every project has email; it is set up with the project's next apply (or within a minute)"
		return pr
	case errors.Is(err, errNotFound):
		return api.NewProblem(404, "not_found", err.Error())
	}
	return err
}

// RegisterAPI adds the email operations.
func (m *Module) RegisterAPI(a huma.API, p *platform.Platform) {
	tag := "email"
	base := "/v1/projects/{project}/email"

	send := api.Op("email-send", http.MethodPost, base+"/send", "email send", api.RiskWrite, "Send an email",
		"Sends a message from the project. Until the box has an SMTP relay (and always for previews) it is captured in the dev inbox instead: "+
			"the reply says where it went. Suppressed recipients are skipped. Needs full access.", tag)
	send.MaxBodyBytes = MaxMessageBytes * 4 / 3
	send.Errors = append(send.Errors, 404, 429)
	huma.Register(a, send, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Body    Message
	}) (*struct{ Body Result }, error) {
		if err := boxOnly(p); err != nil {
			return nil, err
		}
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeRead, in.Project); err != nil {
			return nil, err
		}
		outbound, err := m.willSend(ctx, p, in.Project)
		if err != nil {
			return nil, err
		}
		scope := tokens.ScopeApplyReversible
		if outbound {
			scope = tokens.ScopeApplyOutbound
		}
		if err := pr.Require(scope, in.Project); err != nil {
			return nil, err
		}
		res, err := m.send(ctx, p, in.Project, "api", in.Body)
		if err != nil {
			return nil, toProblem(err)
		}
		_ = p.DB.Audit(ctx, pr.TokenID, "email.send", in.Project+"/"+res.ID, map[string]any{"delivery": res.Delivery, "to": len(res.Recipients), "session": pr.Session})
		return &struct{ Body Result }{*res}, nil
	}))

	m.registerDomainAPI(a, p, base, tag)
	m.registerSendingAPI(a, p, base, tag)
	m.registerBoxAPI(a, p, tag)

	huma.Register(a, api.Untrusted(api.Op("email-messages-list", http.MethodGet, base+"/messages", "email messages list", api.RiskRead, "List the dev inbox",
		"Messages captured in the project's dev inbox, newest first. With all=true: every message, including ones sent through the relay or suppressed, with delivery status. "+
			"With read-only access, subjects and text are left out (hidden: true) and q searches sender and recipients only.", tag)),
		api.Wrap(func(ctx context.Context, in *struct {
			Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
			Q       string `query:"q" maxLength:"200" doc:"Search subject, from, to and text"`
			All     bool   `query:"all" doc:"Include relayed and suppressed messages"`
			Before  string `query:"before" pattern:"^msg_[0-9A-Z]{26}$" doc:"Page: messages older than this ID"`
			Limit   int    `query:"limit" minimum:"1" maximum:"200" default:"50"`
		}) (*struct{ Body []Summary }, error) {
			if err := boxOnly(p); err != nil {
				return nil, err
			}
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
				return nil, err
			}
			full := fullAccess(ctx, in.Project)
			recs, err := listRecords(ctx, p.DB.SQL(), ListFilter{Project: in.Project, All: in.All, Query: in.Q, Before: in.Before, Limit: in.Limit, EnvelopeOnly: !full})
			if err != nil {
				return nil, err
			}
			out := make([]Summary, 0, len(recs))
			for _, r := range recs {
				if full {
					out = append(out, r.Summary)
				} else {
					out = append(out, r.Summary.envelope())
				}
			}
			return &struct{ Body []Summary }{out}, nil
		}))

	get := api.Op("email-message-get", http.MethodGet, base+"/messages/{id}", "email messages get", api.RiskRead, "Read a message",
		"One message: headers, text, sanitised HTML, attachments, the links in it (handy for sign-in and verification links) and its delivery status. "+
			"With read-only access, only its envelope and delivery (hidden: true): what mail says needs full access.", tag)
	get.Errors = append(get.Errors, 404)
	huma.Register(a, api.Untrusted(get), api.Wrap(func(ctx context.Context, in *messageIn) (*struct{ Body Detail }, error) {
		if err := boxOnly(p); err != nil {
			return nil, err
		}
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
			return nil, err
		}
		d, err := m.detail(ctx, p, in.Project, in.ID, fullAccess(ctx, in.Project))
		if err != nil {
			return nil, toProblem(err)
		}
		return &struct{ Body Detail }{*d}, nil
	}))

	raw := api.Op("email-message-raw", http.MethodGet, base+"/messages/{id}/raw", "email messages raw", api.RiskRead, "Download a message (.eml)",
		"The raw RFC 5322 message as received.", tag)
	raw.Hidden = true
	huma.Register(a, raw, api.Wrap(func(ctx context.Context, in *messageIn) (*struct {
		ContentType        string `header:"Content-Type"`
		ContentDisposition string `header:"Content-Disposition"`
		Body               []byte
	}, error) {
		if err := boxOnly(p); err != nil {
			return nil, err
		}
		// What a message says needs full access (see Summary.envelope).
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		if _, err := getRecord(ctx, p.DB.SQL(), in.Project, in.ID); err != nil {
			return nil, toProblem(err)
		}
		b, err := readRaw(p.DataRoot, in.Project, in.ID)
		if err != nil {
			return nil, api.NewProblem(404, "not_found", "the raw message is no longer kept")
		}
		return &struct {
			ContentType        string `header:"Content-Type"`
			ContentDisposition string `header:"Content-Disposition"`
			Body               []byte
		}{"message/rfc822", `attachment; filename="` + in.ID + `.eml"`, b}, nil
	}))

	att := api.Op("email-attachment-get", http.MethodGet, base+"/messages/{id}/attachments/{index}", "email attachments get", api.RiskRead,
		"Download an attachment", "One attachment's bytes, by index from the message's attachmentList.", tag)
	att.Hidden = true
	huma.Register(a, att, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$"`
		ID      string `path:"id" pattern:"^msg_[0-9A-Z]{26}$"`
		Index   int    `path:"index" minimum:"0"`
	}) (*struct {
		ContentType        string `header:"Content-Type"`
		ContentDisposition string `header:"Content-Disposition"`
		Body               []byte
	}, error) {
		if err := boxOnly(p); err != nil {
			return nil, err
		}
		// What a message says needs full access (see Summary.envelope).
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		if _, err := getRecord(ctx, p.DB.SQL(), in.Project, in.ID); err != nil {
			return nil, toProblem(err)
		}
		b, err := readRaw(p.DataRoot, in.Project, in.ID)
		if err != nil {
			return nil, api.NewProblem(404, "not_found", "the raw message is no longer kept")
		}
		parsed, err := parse(b)
		if err != nil || in.Index >= len(parsed.parts) {
			return nil, api.NewProblem(404, "not_found", "no such attachment")
		}
		info := parsed.Attachments[in.Index]
		name := strings.NewReplacer(`"`, "", "\r", "", "\n", "").Replace(info.Filename)
		if name == "" {
			name = "attachment-" + strconv.Itoa(in.Index)
		}
		return &struct {
			ContentType        string `header:"Content-Type"`
			ContentDisposition string `header:"Content-Disposition"`
			Body               []byte
		}{info.ContentType, `attachment; filename="` + name + `"`, parsed.parts[in.Index]}, nil
	}))

	del := api.Op("email-message-delete", http.MethodDelete, base+"/messages/{id}", "email messages delete", api.RiskWrite,
		"Delete a message from the dev inbox", "Removes one captured message. Only dev-inbox messages can be deleted.", tag)
	del.Errors = append(del.Errors, 404)
	huma.Register(a, del, api.Wrap(func(ctx context.Context, in *messageIn) (*struct{}, error) {
		if err := boxOnly(p); err != nil {
			return nil, err
		}
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		n, err := deleteMessages(ctx, p, in.Project, in.ID)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, api.NewProblem(404, "not_found", "no message "+in.ID+" in the dev inbox of "+in.Project)
		}
		return &struct{}{}, nil
	}))

	huma.Register(a, api.Op("email-messages-clear", http.MethodDelete, base+"/messages", "email messages clear", api.RiskWrite,
		"Empty the dev inbox", "Removes every captured message of the project. Relayed-mail history is kept.", tag),
		api.Wrap(func(ctx context.Context, in *projectIn) (*struct {
			Body struct {
				Deleted int `json:"deleted"`
			}
		}, error) {
			if err := boxOnly(p); err != nil {
				return nil, err
			}
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeApplyReversible, in.Project); err != nil {
				return nil, err
			}
			n, err := deleteMessages(ctx, p, in.Project, "")
			if err != nil {
				return nil, err
			}
			out := &struct {
				Body struct {
					Deleted int `json:"deleted"`
				}
			}{}
			out.Body.Deleted = n
			return out, nil
		}))

	// Live updates for the dashboard (server-sent events). Hidden from the
	// CLI and MCP: they poll email-messages-list.
	stream := api.Op("email-stream", http.MethodGet, base+"/stream", "email stream", api.RiskRead, "Stream new mail",
		"Server-sent events: a \"message\" event (a message summary) whenever mail is captured or its delivery status changes.", tag)
	stream.Hidden = true
	sse.Register(a, stream, map[string]any{"message": Summary{}, "error": api.Problem{}}, func(ctx context.Context, in *projectIn, send sse.Sender) {
		if p == nil {
			return
		}
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
			_ = send(sse.Message{Data: api.NewProblem(403, "forbidden", err.Error())})
			return
		}
		full := fullAccess(ctx, in.Project)
		_, h := m.state()
		ch, cancel := h.subscribe(in.Project)
		defer cancel()
		_ = send.Comment("connected")
		ping := time.NewTicker(25 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case s := <-ch:
				if !full {
					s = s.envelope()
				}
				if send.Data(s) != nil {
					return
				}
			case <-ping.C:
				if send.Comment("ping") != nil {
					return
				}
			}
		}
	})

	huma.Register(a, api.Untrusted(api.Op("email-suppressions-list", http.MethodGet, base+"/suppressions", "email suppressions list", api.RiskRead,
		"List suppressed addresses", "Addresses the project will not send to: hard bounces (added automatically), complaints, unsubscribes and manual entries.", tag)),
		api.Wrap(func(ctx context.Context, in *projectIn) (*struct{ Body []Suppression }, error) {
			if err := boxOnly(p); err != nil {
				return nil, err
			}
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
				return nil, err
			}
			l, err := listSuppressions(ctx, p.DB.SQL(), in.Project)
			return &struct{ Body []Suppression }{l}, err
		}))

	huma.Register(a, api.Op("email-suppression-add", http.MethodPost, base+"/suppressions", "email suppressions add", api.RiskWrite,
		"Suppress an address", "Stops all mail from the project to this address, e.g. after an unsubscribe or a spam complaint.", tag),
		api.Wrap(func(ctx context.Context, in *struct {
			Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
			Body    struct {
				Address string `json:"address" minLength:"3" maxLength:"320" doc:"Email address"`
				Reason  string `json:"reason,omitempty" enum:"bounce,complaint,unsubscribe,manual" doc:"Default manual"`
				Detail  string `json:"detail,omitempty" maxLength:"500"`
			}
		}) (*struct{ Body Suppression }, error) {
			if err := boxOnly(p); err != nil {
				return nil, err
			}
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeApplyReversible, in.Project); err != nil {
				return nil, err
			}
			a, err := mail.ParseAddress(in.Body.Address)
			if err != nil {
				return nil, api.NewProblem(422, "validation", "address: not an email address")
			}
			s := Suppression{Address: normAddr(a.Address), Reason: in.Body.Reason, Detail: in.Body.Detail, CreatedAt: time.Now().UTC()}
			if s.Reason == "" {
				s.Reason = "manual"
			}
			if err := addSuppression(ctx, p.DB.SQL(), in.Project, s); err != nil {
				return nil, err
			}
			return &struct{ Body Suppression }{s}, nil
		}))

	sd := api.Op("email-suppression-delete", http.MethodDelete, base+"/suppressions/{address}", "email suppressions delete", api.RiskWrite,
		"Unsuppress an address", "Lets the project send to this address again. Only do this when the person asked for mail again.", tag)
	sd.Errors = append(sd.Errors, 404)
	huma.Register(a, sd, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Address string `path:"address" maxLength:"320" doc:"Email address"`
	}) (*struct{}, error) {
		if err := boxOnly(p); err != nil {
			return nil, err
		}
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		ok, err := deleteSuppression(ctx, p.DB.SQL(), in.Project, in.Address)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, api.NewProblem(404, "not_found", in.Address+" is not suppressed in "+in.Project)
		}
		return &struct{}{}, nil
	}))

	huma.Register(a, api.Op("email-smtp", http.MethodGet, base+"/smtp", "email smtp", api.RiskRead, "Show a project's SMTP credentials",
		"The SMTP env vars the project's apps get (SMTP_URL, EMAIL_FROM, ...), including the password, for tools and local development. "+
			"Needs full access because the credentials can send real mail once a relay is configured.", tag),
		api.Wrap(func(ctx context.Context, in *projectIn) (*struct{ Body map[string]string }, error) {
			if err := boxOnly(p); err != nil {
				return nil, err
			}
			pr := api.PrincipalFrom(ctx)
			if err := pr.Require(tokens.ScopeApplyOutbound, in.Project); err != nil {
				return nil, err
			}
			env, err := m.Env(ctx, p, in.Project, "")
			if err != nil {
				return nil, err
			}
			if env == nil {
				return nil, toProblem(errNoProject)
			}
			_ = p.DB.Audit(ctx, pr.TokenID, "email.smtp.read", in.Project, map[string]any{"session": pr.Session})
			return &struct{ Body map[string]string }{env}, nil
		}))

	huma.Register(a, api.Op("email-rate-limit-set", http.MethodPut, base+"/rate-limit", "email rate-limit set", api.RiskWrite,
		"Set a project's send rate limit", fmt.Sprintf("Messages per hour (bursts up to 60 at once). Default %d; 0 means unlimited. Box admins only.", DefaultRatePerHour), tag),
		api.Wrap(func(ctx context.Context, in *struct {
			Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
			Body    struct {
				PerHour int `json:"perHour" minimum:"0" maximum:"1000000" doc:"Messages per hour; 0 means unlimited"`
			}
		}) (*struct {
			Body struct {
				PerHour int `json:"perHour"`
			}
		}, error) {
			if err := boxOnly(p); err != nil {
				return nil, err
			}
			pr := api.PrincipalFrom(ctx)
			if !pr.BoxAdmin() {
				return nil, fmt.Errorf("%w: rate limits are set by the box owner", tokens.ErrForbidden)
			}
			if err := p.DB.KVPut(ctx, kvNS, "rate/"+in.Project, []byte(strconv.Itoa(in.Body.PerHour))); err != nil {
				return nil, err
			}
			out := &struct {
				Body struct {
					PerHour int `json:"perHour"`
				}
			}{}
			out.Body.PerHour = in.Body.PerHour
			return out, nil
		}))

	huma.Register(a, api.Op("email-status", http.MethodGet, "/v1/email", "email status", api.RiskRead, "Show how the box sends mail",
		"Whether mail is captured in the dev inbox or sent through a relay, the relay settings (never the password), the SMTP submission addresses and the queue.", tag),
		api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body Status }, error) {
			if err := boxOnly(p); err != nil {
				return nil, err
			}
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, ""); err != nil {
				return nil, err
			}
			st, err := m.status(ctx, p)
			if err != nil {
				return nil, err
			}
			return &struct{ Body Status }{*st}, nil
		}))

	huma.Register(a, api.Op("email-relay-set", http.MethodPut, "/v1/email/relay", "email relay set", api.RiskWrite, "Configure the SMTP relay",
		"Sends production mail through this SMTP relay instead of capturing it. Name a provider (sendgrid, resend, postmark, ses, mailgun, brevo, cloudflare) "+
			"and the box fills the host, port, security and username: send only the key (and region for ses or mailgun, username for ses, mailgun and brevo). "+
			"Use provider other with host, port, tls and username for any other SMTP server. The password is stored encrypted and never shown; "+
			"omit it to keep the stored one. Preview mail is still captured. Box admins only. Try it with email relay test.", tag),
		api.Wrap(func(ctx context.Context, in *struct {
			Body struct {
				Provider string  `json:"provider,omitempty" enum:"sendgrid,resend,postmark,ses,mailgun,brevo,cloudflare,other" doc:"Default other"`
				Region   string  `json:"region,omitempty" maxLength:"32" doc:"ses: an AWS region such as eu-west-1; mailgun: us or eu"`
				Host     string  `json:"host,omitempty" maxLength:"253" doc:"Relay hostname; filled from the provider when omitted"`
				Port     int     `json:"port,omitempty" minimum:"1" maximum:"65535" doc:"Default: the provider's, else 587 (starttls), 465 for tls, 25 for none"`
				Username string  `json:"username,omitempty" maxLength:"320" doc:"Ignored for sendgrid, resend and postmark, whose username is fixed"`
				Password *string `json:"password,omitempty" maxLength:"4096" doc:"The API key, SMTP key or password. Stored encrypted; \"\" removes it"`
				TLS      string  `json:"tls,omitempty" enum:"starttls,tls,none" doc:"Default: the provider's, else starttls. none sends credentials and mail in the clear: only for local test sinks"`
			}
		}) (*struct{ Body Status }, error) {
			if err := boxOnly(p); err != nil {
				return nil, err
			}
			pr := api.PrincipalFrom(ctx)
			if !pr.BoxAdmin() {
				return nil, fmt.Errorf("%w: the SMTP relay is set by the box owner", tokens.ErrForbidden)
			}
			b := in.Body
			r, err := resolve(relayInput{Provider: b.Provider, Region: b.Region, Host: b.Host, Port: b.Port, Username: b.Username, TLS: b.TLS})
			if err != nil {
				return nil, toProblem(err)
			}
			r.UpdatedAt, r.UpdatedBy = time.Now().UTC(), pr.TokenID
			if r.Provider != ProviderOther && r.Provider != "" {
				if old, _ := getRelay(ctx, p); b.Password == nil && (old == nil || !old.PasswordSet) {
					return nil, api.NewProblem(422, "validation", "password: paste the "+lowerFirst(PresetByID(r.Provider).KeyLabel))
				}
			}
			if err := setRelay(ctx, p, r, b.Password); err != nil {
				return nil, err
			}
			_ = p.DB.Audit(ctx, pr.TokenID, "email.relay.set", r.Host, map[string]any{"provider": r.Provider, "port": r.Port, "tls": r.TLS, "session": pr.Session})
			m.kick() // queued mail waiting for a relay goes now
			st, err := m.status(ctx, p)
			if err != nil {
				return nil, err
			}
			return &struct{ Body Status }{*st}, nil
		}))

	huma.Register(a, api.Op("email-providers", http.MethodGet, "/v1/email/providers", "email providers", api.RiskRead, "List relay providers",
		"The mail services the box can fill in: SMTP host, port, security and username, what the key is called, where to create it and the "+
			"permission it needs, where to verify a sending domain, and (SendGrid, Resend, Postmark) how to send delivery events back to the box.", tag),
		api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body []Preset }, error) {
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, ""); err != nil {
				return nil, err
			}
			return &struct{ Body []Preset }{Presets}, nil
		}))

	type webhookOut struct {
		Webhook
		SecretURL string `json:"secretUrl,omitempty" doc:"Postmark: the address with its user name and password. Shown this once; paste it into Postmark now"`
	}
	ws := api.Op("email-webhook-set", http.MethodPut, "/v1/email/webhooks/{provider}", "email webhooks set", api.RiskWrite, "Turn on delivery events",
		"Saves the key the box checks the provider's event requests with: SendGrid's verification key (Signed Event Webhook), or Resend's signing secret. "+
			"For Postmark, which has no signatures, the box makes a password and returns the webhook address with it, once (calling again makes a new one). "+
			"The key is stored encrypted and never shown. Box admins only.", tag)
	ws.Errors = append(ws.Errors, 404)
	huma.Register(a, ws, api.Wrap(func(ctx context.Context, in *struct {
		Provider string `path:"provider" enum:"sendgrid,resend,postmark"`
		Body     struct {
			Key string `json:"key,omitempty" maxLength:"4096" doc:"SendGrid: the verification key; Resend: the signing secret (whsec_...); Postmark: leave out"`
		}
	}) (*struct{ Body webhookOut }, error) {
		if err := boxOnly(p); err != nil {
			return nil, err
		}
		pr := api.PrincipalFrom(ctx)
		if !pr.BoxAdmin() {
			return nil, fmt.Errorf("%w: delivery events are set up by the box owner", tokens.ErrForbidden)
		}
		key := strings.TrimSpace(in.Body.Key)
		secretURL := ""
		if in.Provider == ProviderPostmark {
			key = randomSecret() + randomSecret()
			u, _ := url.Parse(strings.TrimSuffix(p.PublicURL, "/") + eventsPath(ProviderPostmark))
			u.User = url.UserPassword(postmarkUser, key)
			secretURL = u.String()
		} else if key == "" {
			return nil, api.NewProblem(422, "validation", "key: paste the "+lowerFirst(PresetByID(in.Provider).Events.KeyLabel))
		} else if err := checkKey(in.Provider, key); err != nil {
			return nil, toProblem(err)
		}
		if err := p.Secrets.Set(ctx, secretsProject, webhookSecretName(in.Provider), key, pr.TokenID); err != nil {
			return nil, err
		}
		m.updateHook(ctx, p, in.Provider, func(s *hookState) {
			*s = hookState{ConfiguredAt: time.Now().UTC(), ConfiguredBy: pr.TokenID}
		})
		_ = p.DB.Audit(ctx, pr.TokenID, "email.webhook.set", in.Provider, map[string]any{"session": pr.Session})
		return &struct{ Body webhookOut }{webhookOut{Webhook: m.webhook(ctx, p, in.Provider), SecretURL: secretURL}}, nil
	}))

	wd := api.Op("email-webhook-delete", http.MethodDelete, "/v1/email/webhooks/{provider}", "email webhooks delete", api.RiskWrite, "Turn off delivery events",
		"Forgets the provider's key: its event requests are refused from now on. Remove the webhook in the provider's dashboard too. Box admins only.", tag)
	huma.Register(a, wd, api.Wrap(func(ctx context.Context, in *struct {
		Provider string `path:"provider" enum:"sendgrid,resend,postmark"`
	}) (*struct{}, error) {
		if err := boxOnly(p); err != nil {
			return nil, err
		}
		pr := api.PrincipalFrom(ctx)
		if !pr.BoxAdmin() {
			return nil, fmt.Errorf("%w: delivery events are set up by the box owner", tokens.ErrForbidden)
		}
		if _, err := p.Secrets.Delete(ctx, secretsProject, webhookSecretName(in.Provider)); err != nil {
			return nil, err
		}
		_ = p.DB.KVDelete(ctx, kvNS, "webhook/"+in.Provider)
		_ = p.DB.Audit(ctx, pr.TokenID, "email.webhook.delete", in.Provider, map[string]any{"session": pr.Session})
		return &struct{}{}, nil
	}))

	// The providers' own calls. They carry no Tiffin credentials (each is
	// signed, or carries Postmark's password), so they bypass the API's auth.
	for _, prov := range eventProviders {
		a.Adapter().Handle(&huma.Operation{Method: http.MethodPost, Path: eventsPath(prov)}, func(hctx huma.Context) {
			req, w := humago.Unwrap(hctx)
			m.handleEvents(p, prov, w, req)
		})
	}

	huma.Register(a, api.Op("email-relay-delete", http.MethodDelete, "/v1/email/relay", "email relay delete", api.RiskWrite, "Remove the SMTP relay",
		"Back to capturing every message in the dev inbox. Messages already queued for the relay wait until a relay is configured again. Box admins only.", tag),
		api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body Status }, error) {
			if err := boxOnly(p); err != nil {
				return nil, err
			}
			pr := api.PrincipalFrom(ctx)
			if !pr.BoxAdmin() {
				return nil, fmt.Errorf("%w: the SMTP relay is set by the box owner", tokens.ErrForbidden)
			}
			if err := deleteRelay(ctx, p); err != nil {
				return nil, err
			}
			_ = p.DB.Audit(ctx, pr.TokenID, "email.relay.delete", "", map[string]any{"session": pr.Session})
			st, err := m.status(ctx, p)
			if err != nil {
				return nil, err
			}
			return &struct{ Body Status }{*st}, nil
		}))

	huma.Register(a, api.Op("email-relay-test", http.MethodPost, "/v1/email/relay/test", "email relay test", api.RiskWrite, "Send a test email through the relay",
		"Connects to the relay now and sends one short test message, reporting exactly what the relay said. Box admins only.", tag),
		api.Wrap(func(ctx context.Context, in *struct {
			Body struct {
				To   string `json:"to" minLength:"3" maxLength:"320" doc:"Where to send the test"`
				From string `json:"from,omitempty" maxLength:"320" doc:"Default: the box's sender (email box get)"`
			}
		}) (*struct {
			Body relayTestResult
		}, error) {
			if err := boxOnly(p); err != nil {
				return nil, err
			}
			pr := api.PrincipalFrom(ctx)
			if !pr.BoxAdmin() {
				return nil, fmt.Errorf("%w: only the box owner can test the relay", tokens.ErrForbidden)
			}
			out := &struct {
				Body relayTestResult
			}{}
			from := in.Body.From
			if from == "" {
				// The sender box mail uses: the one the relay must accept.
				if s, err := m.boxSender(ctx, p); err == nil {
					from = s.From
				}
			}
			res, err := testRelay(ctx, p, in.Body.To, from)
			relay, _ := getRelay(ctx, p)
			switch {
			case err != nil:
				out.Body.Detail = err.Error()
				out.Body.Hint = explainRelayError(err, relay)
			case len(res.Accepted) == 0:
				out.Body.Detail = "the relay refused the recipient: " + res.Rejected[in.Body.To]
				out.Body.Hint = "That address was refused for good. Try another one."
			default:
				out.Body.OK, out.Body.Detail = true, strings.TrimSpace("accepted by the relay. "+res.Reply)
			}
			return out, nil
		}))
}

func (m *Module) status(ctx context.Context, p *platform.Platform) (*Status, error) {
	r, err := getRelay(ctx, p)
	if err != nil {
		return nil, err
	}
	st := &Status{Mode: DeliveryInbox, Relay: r, SMTP: []string{}, Webhooks: m.webhooks(ctx, p, r)}
	if r != nil {
		st.Mode = DeliveryRelay
	}
	m.mu.Lock()
	srv := m.smtpd
	m.mu.Unlock()
	if srv != nil {
		st.SMTP = srv.addrs()
	}
	_ = p.DB.SQL().QueryRowContext(ctx, `SELECT count(*) FROM email_messages WHERE status = ?`, StatusQueued).Scan(&st.Queued)
	st.FailedDay = failedCount(ctx, p)
	return st, nil
}

// detail is one message; with full false (read-only access), only its
// envelope and delivery: no subject, headers, text, links or attachments.
func (m *Module) detail(ctx context.Context, p *platform.Platform, project, id string, full bool) (*Detail, error) {
	rec, err := getRecord(ctx, p.DB.SQL(), project, id)
	if err != nil {
		return nil, err
	}
	if !full {
		rec.Summary = rec.Summary.envelope()
	}
	d := &Detail{Summary: rec.Summary, Headers: []Header{}, Links: []string{}, AttachList: []AttachmentInfo{},
		RawURL: "/v1/projects/" + project + "/email/messages/" + id + "/raw"}
	d.Envelope.From, d.Envelope.To = rec.MailFrom, rec.Rcpt
	if d.Events, err = messageEvents(ctx, p.DB.SQL(), id); err != nil {
		return nil, err
	}
	_ = p.DB.SQL().QueryRowContext(ctx, `SELECT provider FROM email_tracking WHERE id = ?`, id).Scan(&d.Provider)
	if d.Envelope.To == nil {
		d.Envelope.To = []string{}
	}
	if !full {
		d.RawURL = ""
		return d, nil
	}
	raw, err := readRaw(p.DataRoot, project, id)
	if err != nil {
		return d, nil // relayed long ago: metadata only
	}
	parsed, err := parse(raw)
	if err != nil {
		return d, nil
	}
	if privateBox(project, parsed) {
		d.Headers, d.Text = parsed.Headers, hiddenBody
		return d, nil
	}
	d.Headers, d.Text, d.HTML, d.Links, d.AttachList = parsed.Headers, parsed.Text, sanitize(parsed.HTML), parsed.links(), parsed.Attachments
	if d.Headers == nil {
		d.Headers = []Header{}
	}
	return d, nil
}

// fullAccess reports whether the caller may read what the project's mail
// says (see Summary.envelope): full access, not read-only.
func fullAccess(ctx context.Context, project string) bool {
	return api.PrincipalFrom(ctx).Require(tokens.ScopeApplyReversible, project) == nil
}
