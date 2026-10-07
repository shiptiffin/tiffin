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
		return newSignIn(m, dash, brand, host, mark)
	default:
		return nil, fmt.Errorf("unknown box mail %q", m.Kind)
	}
}

// newSignIn writes the "New sign-in" notice. The dashboard has no list of
// sign-ins, so the button opens the person's passkeys (what can sign them in)
// and owners and admins also get a link to API keys.
func newSignIn(m api.BoxMail, dash, brand, host, mark string) (*boxEmail, error) {
	first := firstName(m.Name)
	if first == "there" {
		first = "" // "Someone just signed in as you", not "there, someone…"
	}
	device := m.Device
	if device == "" {
		device = "An unknown browser"
	}
	from := device // inside the subject: "from an unknown browser"
	if strings.HasPrefix(from, "A ") || strings.HasPrefix(from, "An ") {
		from = strings.ToLower(from[:1]) + from[1:]
	}
	how := m.Via
	if how == "" {
		how = "Sign-in link"
	}
	admin := m.Role == "owner" || m.Role == "admin"
	var url, shown, keys string
	if dash != "" {
		url = dash + "/settings/passkeys"
		shown = strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
		if admin {
			keys = dash + "/settings/keys"
		}
	}
	return templates.NewSignIn(templates.NewSignInData{Brand: brand, Host: host, MarkURL: mark, First: first,
		Device: device, From: from, When: friendlyWhen(m.At), How: how, Where: m.Where, IP: m.IP,
		Admin: admin, URL: url, ShownURL: shown, KeysURL: keys})
}

// friendlyWhen: "Wednesday 7 October, 19:54 UTC". The email's own date gives the year.
func friendlyWhen(t time.Time) string { return t.UTC().Format("Monday 2 January, 15:04 UTC") }
