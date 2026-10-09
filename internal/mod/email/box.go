package email

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/emersion/go-message/mail"
	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/ids"
	"github.com/shiptiffin/tiffin/internal/mod/email/templates"
	"github.com/shiptiffin/tiffin/internal/page"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// Box mail: the dashboard's own messages to the people who use it (invites,
// sign-in links, new sign-in notices). It goes the way project mail goes:
// through the relay when the box has one, else into a dev inbox of its own
// (the pseudo-project boxProject), which only owners and admins can read
// because it holds sign-in links.

const boxProject = "_box"

func init() { api.SetBoxMailer(mod) }

// privateHeader marks box mail whose body holds a credential nobody but its
// recipient may see: a sign-in link the person asked for by email, which
// counts as a strong sign-in. Box mail history (which every owner and admin
// can read) shows such a message's metadata only: no text, HTML, links or
// snippet, while it waits in the relay queue or after.
const privateHeader = "X-Tiffin-Private"

// hiddenBody stands in for a private box message's text.
const hiddenBody = "Hidden: this email holds a sign-in link for its recipient only."

// privateBox reports whether a message of project is private box mail.
func privateBox(project string, parsed *Parsed) bool {
	if project != boxProject {
		return false
	}
	for _, h := range parsed.Headers {
		if strings.EqualFold(h.Name, privateHeader) {
			return true
		}
	}
	return false
}

// BoxSender is who the box's own mail comes from.
type BoxSender struct {
	From        string    `json:"from" doc:"The sender, e.g. \"ShipTiffin <hello@shiptiffin.com>\""`
	ReplyTo     string    `json:"replyTo,omitempty" doc:"Where replies go, when not to the sender"`
	Default     bool      `json:"default" doc:"No sender is saved: the box uses defaultFrom"`
	DefaultFrom string    `json:"defaultFrom" doc:"The sender the box uses when none is saved, from the box's domain"`
	Mode        string    `json:"mode" enum:"inbox,relay" doc:"relay: box mail is sent through the relay; inbox: no relay, so it waits in the box's dev inbox"`
	Relay       string    `json:"relay,omitempty" doc:"The relay's mail service, when there is one"`
	UpdatedAt   time.Time `json:"updatedAt,omitzero"`
}

type boxSenderStored struct {
	From      string    `json:"from"`
	ReplyTo   string    `json:"replyTo,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
	UpdatedBy string    `json:"updatedBy,omitempty"`
}

// defaultBoxFrom is "Tiffin <hello@<box domain>>" ("ShipTiffin" on shiptiffin.com).
func defaultBoxFrom(p *platform.Platform) string {
	return templates.Brand(p.Domain) + " <hello@" + p.Domain + ">"
}

func (m *Module) boxSender(ctx context.Context, p *platform.Platform) (*BoxSender, error) {
	out := &BoxSender{DefaultFrom: defaultBoxFrom(p), Mode: DeliveryInbox}
	raw, ok, err := p.DB.KVGet(ctx, kvNS, "box-sender")
	if err != nil {
		return nil, err
	}
	var s boxSenderStored
	if ok {
		_ = json.Unmarshal(raw, &s)
	}
	out.From, out.ReplyTo, out.UpdatedAt = s.From, s.ReplyTo, s.UpdatedAt
	if out.From == "" {
		out.From, out.Default = out.DefaultFrom, true
	}
	if r, _ := getRelay(ctx, p); r != nil {
		out.Mode = DeliveryRelay
		if pr := PresetByID(r.provider()); pr != nil && pr.ID != ProviderOther {
			out.Relay = pr.Name
		} else {
			out.Relay = r.Host
		}
	}
	return out, nil
}

// boxHeaders are extra headers for box mail: it is automatic, and SendGrid
// must not rewrite its sign-in links or add an open pixel.
func boxHeaders(provider string) map[string]string {
	h := map[string]string{"Auto-Submitted": "auto-generated"}
	if provider == ProviderSendGrid {
		h["X-SMTPAPI"] = `{"filters":{"clicktrack":{"settings":{"enable":0}},"opentrack":{"settings":{"enable":0}},` +
			`"ganalytics":{"settings":{"enable":0}},"subscriptiontrack":{"settings":{"enable":0}}}}`
	}
	return h
}

// SendBoxMail sends one of the box's own messages (api.BoxMailer).
func (m *Module) SendBoxMail(ctx context.Context, p *platform.Platform, bm api.BoxMail) (*api.BoxMailResult, error) {
	if p == nil {
		return nil, fmt.Errorf("email is only available on a box")
	}
	if err := ensureSchema(ctx, p.DB.SQL()); err != nil {
		return nil, err
	}
	if bm.Dashboard == "" {
		bm.Dashboard = p.PublicURL
	}
	e, err := renderBoxMail(bm, p.Domain, time.Now())
	if err != nil {
		return nil, err
	}
	s, err := m.boxSender(ctx, p)
	if err != nil {
		return nil, err
	}
	provider := ""
	if r, _ := getRelay(ctx, p); r != nil {
		provider = r.provider()
	}
	to := bm.To
	if bm.Name != "" {
		to = (&mail.Address{Name: bm.Name, Address: bm.To}).String()
	}
	id := ids.New("msg")
	// A link the person asked for is a strong sign-in: its body must not be
	// readable in box mail history (see privateBox).
	raw, from, rcpt, err := compose(Message{To: []string{to}, ReplyTo: s.ReplyTo, Subject: e.Subject, Text: e.Text, HTML: e.HTML,
		Headers: boxHeaders(provider), private: bm.Kind == api.BoxMailSignIn}, s.From, id, p.Domain, time.Now())
	if err != nil {
		return nil, err
	}
	res, err := m.accept(ctx, p, boxProject, "", "box", id, from, rcpt, raw)
	if err != nil {
		return nil, err
	}
	detail := "Sent through " + s.Relay + "."
	switch res.Delivery {
	case DeliveryInbox:
		detail = "No mail service is connected, so the email waits in the box's dev inbox (Settings › Email)."
		if s.Mode == DeliveryRelay {
			detail = bm.To + " is at a domain reserved for examples and tests, so the email waits in the box's dev inbox instead."
		}
	case DeliverySuppressed:
		detail = bm.To + " bounced before, so the box doesn't send to it any more."
	}
	return &api.BoxMailResult{Delivery: res.Delivery, To: bm.To, Detail: detail}, nil
}

// BoxMailRelayed reports whether box mail leaves the box now (api.BoxMailer).
func (m *Module) BoxMailRelayed(ctx context.Context, p *platform.Platform) bool {
	if p == nil {
		return false
	}
	r, err := getRelay(ctx, p)
	return err == nil && r != nil
}

func adminOnly(ctx context.Context, why string) error {
	if !api.PrincipalFrom(ctx).BoxAdmin() {
		return fmt.Errorf("%w: %s", tokens.ErrForbidden, why)
	}
	return nil
}

// registerBoxAPI adds the box mail operations.
func (m *Module) registerBoxAPI(a huma.API, p *platform.Platform, tag string) {
	huma.Register(a, api.Op("email-box-get", http.MethodGet, "/v1/email/box", "email box get", api.RiskRead, "Show who box mail comes from",
		"The sender (and Reply-To) of the box's own mail: invites, sign-in links and new sign-in notices; and whether it is sent through "+
			"the relay or kept in the box's dev inbox.", tag),
		api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body BoxSender }, error) {
			if err := boxOnly(p); err != nil {
				return nil, err
			}
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, ""); err != nil {
				return nil, err
			}
			s, err := m.boxSender(ctx, p)
			if err != nil {
				return nil, err
			}
			return &struct{ Body BoxSender }{*s}, nil
		}))

	huma.Register(a, api.Op("email-box-set", http.MethodPut, "/v1/email/box", "email box set", api.RiskWrite, "Set who box mail comes from",
		"Sets the sender of the box's own mail, e.g. \"ShipTiffin <hello@shiptiffin.com>\", and an optional Reply-To. An empty from goes back "+
			"to the default, hello@ the box's domain. The relay's mail service must accept the sender's domain. Box admins only.", tag),
		api.Wrap(func(ctx context.Context, in *struct {
			Body struct {
				From    string `json:"from" maxLength:"320" doc:"The sender, with or without a name; empty for the default"`
				ReplyTo string `json:"replyTo,omitempty" maxLength:"320" doc:"Where replies go; empty for the sender"`
			}
		}) (*struct{ Body BoxSender }, error) {
			if err := boxOnly(p); err != nil {
				return nil, err
			}
			if err := adminOnly(ctx, "box mail's sender is set by the box owner"); err != nil {
				return nil, err
			}
			from, reply := strings.TrimSpace(in.Body.From), strings.TrimSpace(in.Body.ReplyTo)
			if from != "" {
				if _, err := mail.ParseAddress(from); err != nil {
					return nil, api.NewProblem(422, "validation", "from: that isn't an email address, e.g. Shop <hello@example.com>")
				}
			}
			if reply != "" {
				if _, err := mail.ParseAddress(reply); err != nil {
					return nil, api.NewProblem(422, "validation", "replyTo: that isn't an email address")
				}
			}
			pr := api.PrincipalFrom(ctx)
			raw, _ := json.Marshal(boxSenderStored{From: from, ReplyTo: reply, UpdatedAt: time.Now().UTC(), UpdatedBy: pr.TokenID})
			if err := p.DB.KVPut(ctx, kvNS, "box-sender", raw); err != nil {
				return nil, err
			}
			_ = p.DB.Audit(ctx, pr.TokenID, "email.box.set", "box", map[string]any{"from": from, "replyTo": reply, "session": pr.Session})
			s, err := m.boxSender(ctx, p)
			if err != nil {
				return nil, err
			}
			return &struct{ Body BoxSender }{*s}, nil
		}))

	huma.Register(a, api.Untrusted(api.Op("email-box-messages-list", http.MethodGet, "/v1/email/box/messages", "email box messages", api.RiskRead,
		"List box mail", "The box's own mail, newest first, a page at a time: invites, sign-in links and new sign-in notices, sent or kept in its dev inbox. "+
			"More follow when nextCursor is set: pass it as cursor. "+
			"Box admins only: it holds invites. A sign-in link someone asked for by email shows its metadata only (the link is theirs alone).", tag)),
		api.Wrap(func(ctx context.Context, in *struct {
			page.Params
		}) (*struct{ Body page.Page[Summary] }, error) {
			if err := boxOnly(p); err != nil {
				return nil, err
			}
			if err := adminOnly(ctx, "box mail holds sign-in links: only owners and admins can read it"); err != nil {
				return nil, err
			}
			pg, err := listPage(ctx, p.DB.SQL(), ListFilter{Project: boxProject, All: true}, in.Params)
			if err != nil {
				return nil, err
			}
			return &struct{ Body page.Page[Summary] }{pg}, nil
		}))

	get := api.Op("email-box-message-get", http.MethodGet, "/v1/email/box/messages/{id}", "email box message", api.RiskRead, "Read box mail",
		"One of the box's own messages: text, sanitised HTML, its links and delivery status. Box admins only.", tag)
	get.Errors = append(get.Errors, 404)
	huma.Register(a, api.Untrusted(get), api.Wrap(func(ctx context.Context, in *struct {
		ID string `path:"id" pattern:"^msg_[0-9A-Z]{26}$" doc:"Message ID"`
	}) (*struct{ Body Detail }, error) {
		if err := boxOnly(p); err != nil {
			return nil, err
		}
		if err := adminOnly(ctx, "box mail holds sign-in links: only owners and admins can read it"); err != nil {
			return nil, err
		}
		d, err := m.detail(ctx, p, boxProject, in.ID, true)
		if err != nil {
			return nil, toProblem(err)
		}
		d.RawURL = ""
		return &struct{ Body Detail }{*d}, nil
	}))
}
