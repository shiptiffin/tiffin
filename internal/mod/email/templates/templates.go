// Package templates renders the box's own email (invites, sign-in links,
// new sign-in notices, the relay test, alerts) from templates compiled out of
// the React Email sources in packages/emails. The .tmpl files and gen.go are
// generated there (bun run build); this file only loads and runs them.
//
// HTML goes through html/template, so every value is escaped for where it
// lands (text, attribute, link). Subjects and plain text go through
// text/template: they are not HTML.
package templates

import (
	"bytes"
	"embed"
	htmltemplate "html/template"
	"net/url"
	"regexp"
	"strings"
	texttemplate "text/template"
	"time"
)

//go:embed box/*.tmpl
var files embed.FS

// funcs: comment re-inserts Outlook's conditional comments, which
// html/template would otherwise strip. Only the generated templates call it,
// with constants written at build time, never with data.
var funcs = htmltemplate.FuncMap{"comment": func(s string) htmltemplate.HTML { return htmltemplate.HTML(s) }}

var (
	htmlT = htmltemplate.Must(htmltemplate.New("box").Funcs(funcs).ParseFS(files, "box/*.html.tmpl"))
	textT = texttemplate.Must(texttemplate.New("box").ParseFS(files, "box/*.txt.tmpl"))
)

// Email is one rendered message.
type Email struct {
	Subject string
	Text    string
	HTML    string
}

func render(name string, data any) (*Email, error) {
	var h, t, s bytes.Buffer
	if err := htmlT.ExecuteTemplate(&h, name+".html", data); err != nil {
		return nil, err
	}
	if err := textT.ExecuteTemplate(&t, name+".text", data); err != nil {
		return nil, err
	}
	if err := textT.ExecuteTemplate(&s, name+".subject", data); err != nil {
		return nil, err
	}
	return &Email{Subject: strings.Join(strings.Fields(s.String()), " "), Text: tidyText(t.String()), HTML: h.String()}, nil
}

var (
	trailingSpace = regexp.MustCompile(`[ \t]+\n`)
	blankRun      = regexp.MustCompile(`\n{3,}`)
)

// tidyText drops the blank lines a branch that wasn't taken leaves behind.
func tidyText(s string) string {
	s = trailingSpace.ReplaceAllString(s, "\n")
	return strings.TrimSpace(blankRun.ReplaceAllString(s, "\n\n")) + "\n"
}

// Brand is who box mail is from: "ShipTiffin" on shiptiffin.com, "Tiffin" elsewhere.
func Brand(boxDomain string) string {
	if d := strings.ToLower(boxDomain); d == "shiptiffin.com" || strings.HasSuffix(d, ".shiptiffin.com") {
		return "ShipTiffin"
	}
	return "Tiffin"
}

// Host is the dashboard's host, for "the box at dashboard.example.com".
func Host(dashboard, fallback string) string {
	if u, err := url.Parse(dashboard); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return fallback
}

// MarkURL is the mark the dashboard serves for mail (apps/dashboard/public/email-mark.png,
// copied from packages/emails/assets). Empty without a dashboard address: the wordmark reads on its own.
func MarkURL(dashboard string) string {
	u, err := url.Parse(strings.TrimRight(dashboard, "/"))
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return ""
	}
	return u.String() + "/email-mark.png"
}

// When is a time as the emails write it: "7 October 2026, 14:32 UTC".
func When(t time.Time) string { return t.UTC().Format("2 January 2006, 15:04 UTC") }
