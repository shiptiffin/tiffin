package email

import (
	"fmt"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/mod/email/templates"
)

// The box's own messages. Their HTML and plain text are React Email
// templates in packages/emails, compiled to Go templates (package
// templates); this file only works out the values. No tracking: no pixels,
// no rewritten links (see boxHeaders). The one image, the mark, comes from
// the box's own dashboard and the email reads the same without it.

// boxEmail is one rendered message.
type boxEmail = templates.Email

func firstName(name string) string {
	name = strings.TrimSpace(name)
	if f, _, ok := strings.Cut(name, " "); ok && f != "" {
		return f
	}
	if name == "" || name == "Owner" {
		return "there"
	}
	return name
}

// until says how long a link lasts: "for the next 15 minutes", or "until 13 October 2026, 21:05 UTC".
func until(exp, now time.Time) string {
	d := exp.Sub(now).Round(time.Minute)
	if d > 0 && d <= 2*time.Hour {
		return fmt.Sprintf("for the next %d minutes", int(d/time.Minute))
	}
	return "until " + templates.When(exp)
}

// renderBoxMail writes one of the box's messages.
func renderBoxMail(m api.BoxMail, boxDomain string, now time.Time) (*boxEmail, error) {
	dash := strings.TrimRight(m.Dashboard, "/")
	brand, host, mark := templates.Brand(boxDomain), templates.Host(m.Dashboard, "dashboard."+boxDomain), templates.MarkURL(dash)
	by := m.By
	if by == "" || by == "owner" {
		by = "The box's owner"
	}
	switch m.Kind {
	case api.BoxMailInvite:
		return templates.Invite(templates.InviteData{Brand: brand, Host: host, MarkURL: mark, First: firstName(m.Name), By: by,
			Role: m.Role, URL: m.URL, Until: until(m.ExpiresAt, now)})
	case api.BoxMailLink:
		return templates.Link(templates.LinkData{Brand: brand, Host: host, MarkURL: mark, First: firstName(m.Name), By: by,
			URL: m.URL, Until: until(m.ExpiresAt, now)})
	case api.BoxMailSignIn:
		return templates.SignIn(templates.SignInData{Brand: brand, Host: host, MarkURL: mark, First: firstName(m.Name),
			URL: m.URL, Until: until(m.ExpiresAt, now), IP: m.IP})
	case api.BoxMailNewDevice:
		via := m.Via
		if via == "" {
			via = "a sign-in link"
		}
		review := "" // owners and admins get a button to Settings, where passkeys and keys live
		if dash != "" && (m.Role == "owner" || m.Role == "admin") {
			review = dash + "/settings"
		}
		return templates.NewSignIn(templates.NewSignInData{Brand: brand, Host: host, MarkURL: mark, First: firstName(m.Name),
			Device: m.Device, When: templates.When(m.At), Via: via, IP: m.IP, Owner: m.Role == "owner", URL: review})
	default:
		return nil, fmt.Errorf("unknown box mail %q", m.Kind)
	}
}
