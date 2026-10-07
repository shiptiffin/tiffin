package email

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/textproto"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	_ "github.com/emersion/go-message/charset" // decode non-UTF-8 parts
	"github.com/emersion/go-message/mail"
	"github.com/microcosm-cc/bluemonday"
)

// Message is an email to send.
type Message struct {
	From        string            `json:"from,omitempty" doc:"Sender, e.g. \"Shop <hello@shop.example>\". Default: the project's from address"`
	To          []string          `json:"to" minItems:"1" maxItems:"50" doc:"Recipients"`
	Cc          []string          `json:"cc,omitempty" maxItems:"50"`
	Bcc         []string          `json:"bcc,omitempty" maxItems:"50" doc:"Hidden recipients (envelope only)"`
	ReplyTo     string            `json:"replyTo,omitempty"`
	Subject     string            `json:"subject" maxLength:"998"`
	Text        string            `json:"text,omitempty" doc:"Plain-text body"`
	HTML        string            `json:"html,omitempty" doc:"HTML body (render react-email templates with @shiptiffin/sdk/email render())"`
	Headers     map[string]string `json:"headers,omitempty" doc:"Extra headers, e.g. List-Unsubscribe. Structural headers (From, To, Subject, Content-*, ...) are refused"`
	Attachments []Attachment      `json:"attachments,omitempty" maxItems:"20"`

	private bool // box mail only: hide its body from box mail history (see privateHeader)
}

// Attachment is a file attached to a message.
type Attachment struct {
	Filename    string `json:"filename" minLength:"1" maxLength:"255"`
	ContentType string `json:"contentType,omitempty" doc:"Default: from the filename"`
	Base64      string `json:"base64" doc:"File content, base64-encoded"`
}

// reservedHeaders may not be set through Message.Headers.
var reservedHeaders = map[string]bool{
	"From": true, "To": true, "Cc": true, "Bcc": true, "Subject": true, "Date": true, "Message-Id": true, "Reply-To": true,
	"Mime-Version": true, "Content-Type": true, "Content-Transfer-Encoding": true, "Content-Disposition": true, "Sender": true, "Return-Path": true,
}

// ValidationError is a problem with a message the caller must fix.
type ValidationError struct {
	Msg    string
	sender bool // the sender is not one the project may use
}

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, a ...any) error { return &ValidationError{Msg: fmt.Sprintf(format, a...)} }

// parseAddrs parses address strings ("a@b.c" or "Name <a@b.c>").
func parseAddrs(field string, in []string) ([]*mail.Address, error) {
	var out []*mail.Address
	for _, s := range in {
		a, err := mail.ParseAddress(s)
		if err != nil || !strings.Contains(a.Address, "@") {
			return nil, invalid("%s: %q is not an email address", field, s)
		}
		out = append(out, a)
	}
	return out, nil
}

// compose builds an RFC 5322 message. It returns the raw bytes, the
// envelope sender and the envelope recipients (To, Cc and Bcc).
func compose(m Message, defaultFrom, id, domain string, now time.Time) ([]byte, string, []string, error) {
	if len(m.To) == 0 {
		return nil, "", nil, invalid("to: at least one recipient is required")
	}
	if strings.TrimSpace(m.Text) == "" && strings.TrimSpace(m.HTML) == "" {
		return nil, "", nil, invalid("send text, html or both")
	}
	if strings.ContainsAny(m.Subject, "\r\n") {
		return nil, "", nil, invalid("subject: must be one line")
	}
	fromS := m.From
	if fromS == "" {
		fromS = defaultFrom
	}
	from, err := parseAddrs("from", []string{fromS})
	if err != nil {
		return nil, "", nil, err
	}
	to, err := parseAddrs("to", m.To)
	if err != nil {
		return nil, "", nil, err
	}
	cc, err := parseAddrs("cc", m.Cc)
	if err != nil {
		return nil, "", nil, err
	}
	bcc, err := parseAddrs("bcc", m.Bcc)
	if err != nil {
		return nil, "", nil, err
	}
	var h mail.Header
	h.SetDate(now)
	h.SetAddressList("From", from)
	h.SetAddressList("To", to)
	if len(cc) > 0 {
		h.SetAddressList("Cc", cc)
	}
	if m.ReplyTo != "" {
		rt, err := parseAddrs("replyTo", []string{m.ReplyTo})
		if err != nil {
			return nil, "", nil, err
		}
		h.SetAddressList("Reply-To", rt)
	}
	h.SetSubject(m.Subject)
	h.SetMessageID(id + "@" + domain)
	h.Set("X-Tiffin-Message-Id", id)
	if m.private {
		h.Set(privateHeader, "sign-in link")
	}
	names := make([]string, 0, len(m.Headers))
	for k := range m.Headers {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		v := m.Headers[k]
		ck := textproto.CanonicalMIMEHeaderKey(k)
		if reservedHeaders[ck] || strings.HasPrefix(ck, "X-Tiffin-") {
			return nil, "", nil, invalid("headers: %s is set by Tiffin; use the matching field instead", k)
		}
		if k == "" || strings.ContainsAny(k, ": \r\n") || strings.ContainsAny(v, "\r\n") {
			return nil, "", nil, invalid("headers: %q is not a valid one-line header", k)
		}
		h.Set(k, v)
	}

	var buf bytes.Buffer
	writeBodies := func(iw *mail.InlineWriter) error {
		if m.Text != "" {
			var th mail.InlineHeader
			th.Set("Content-Type", "text/plain; charset=utf-8")
			w, err := iw.CreatePart(th)
			if err != nil {
				return err
			}
			_, _ = io.WriteString(w, m.Text)
			w.Close()
		}
		if m.HTML != "" {
			var hh mail.InlineHeader
			hh.Set("Content-Type", "text/html; charset=utf-8")
			w, err := iw.CreatePart(hh)
			if err != nil {
				return err
			}
			_, _ = io.WriteString(w, m.HTML)
			w.Close()
		}
		return iw.Close()
	}
	if len(m.Attachments) == 0 {
		iw, err := mail.CreateInlineWriter(&buf, h)
		if err != nil {
			return nil, "", nil, err
		}
		if err := writeBodies(iw); err != nil {
			return nil, "", nil, err
		}
	} else {
		mw, err := mail.CreateWriter(&buf, h)
		if err != nil {
			return nil, "", nil, err
		}
		iw, err := mw.CreateInline()
		if err != nil {
			return nil, "", nil, err
		}
		if err := writeBodies(iw); err != nil {
			return nil, "", nil, err
		}
		for i, a := range m.Attachments {
			data, err := base64.StdEncoding.DecodeString(a.Base64)
			if err != nil {
				return nil, "", nil, invalid("attachments[%d]: base64 is not valid", i)
			}
			ct := a.ContentType
			if ct == "" {
				ct = mime.TypeByExtension(extOf(a.Filename))
			}
			if ct == "" {
				ct = "application/octet-stream"
			}
			var ah mail.AttachmentHeader
			ah.Set("Content-Type", ct)
			ah.SetFilename(a.Filename)
			w, err := mw.CreateAttachment(ah)
			if err != nil {
				return nil, "", nil, err
			}
			_, _ = w.Write(data)
			w.Close()
		}
		if err := mw.Close(); err != nil {
			return nil, "", nil, err
		}
	}
	var rcpt []string
	for _, l := range [][]*mail.Address{to, cc, bcc} {
		for _, a := range l {
			rcpt = append(rcpt, a.Address)
		}
	}
	return buf.Bytes(), from[0].Address, rcpt, nil
}

func extOf(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i:]
	}
	return ""
}

// Parsed is a message taken apart for display.
type Parsed struct {
	From        string
	To          []string
	Subject     string
	Date        time.Time
	MessageID   string
	Headers     []Header
	Text        string
	HTML        string // as sent (never shown unsanitised)
	Attachments []AttachmentInfo
	parts       [][]byte // attachment bytes, by index
}

// Header is one header field.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// AttachmentInfo describes an attachment.
type AttachmentInfo struct {
	Index       int    `json:"index"`
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
}

const maxPartBytes = 25 << 20

// parse reads a raw message.
func parse(raw []byte) (*Parsed, error) {
	r, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil && r == nil {
		return nil, err
	}
	defer r.Close()
	p := &Parsed{To: []string{}, Attachments: []AttachmentInfo{}}
	h := r.Header
	if l, err := h.AddressList("From"); err == nil && len(l) > 0 {
		p.From = l[0].Address
		if l[0].Name != "" {
			p.From = l[0].String()
		}
	} else {
		p.From = h.Get("From")
	}
	for _, k := range []string{"To", "Cc"} {
		if l, err := h.AddressList(k); err == nil {
			for _, a := range l {
				p.To = append(p.To, a.Address)
			}
		}
	}
	p.Subject, _ = h.Subject()
	p.Date, _ = h.Date()
	p.MessageID, _ = h.MessageID()
	fields := h.Fields()
	for fields.Next() {
		v, err := fields.Text()
		if err != nil {
			v = fields.Value()
		}
		p.Headers = append(p.Headers, Header{Name: fields.Key(), Value: v})
	}
	for {
		part, err := r.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// A malformed part: keep what we have.
			break
		}
		switch ph := part.Header.(type) {
		case *mail.InlineHeader:
			ct, _, _ := ph.ContentType()
			body, _ := io.ReadAll(io.LimitReader(part.Body, maxPartBytes))
			switch {
			case ct == "text/html" && p.HTML == "":
				p.HTML = string(body)
			case (ct == "text/plain" || ct == "") && p.Text == "":
				p.Text = string(body)
			default:
				p.addAttachment("", ct, body)
			}
		case *mail.AttachmentHeader:
			name, _ := ph.Filename()
			ct, _, _ := ph.ContentType()
			body, _ := io.ReadAll(io.LimitReader(part.Body, maxPartBytes))
			p.addAttachment(name, ct, body)
		}
	}
	if !utf8.ValidString(p.Text) {
		p.Text = strings.ToValidUTF8(p.Text, "�")
	}
	return p, nil
}

func (p *Parsed) addAttachment(name, ct string, body []byte) {
	if ct == "" {
		ct = "application/octet-stream"
	}
	p.Attachments = append(p.Attachments, AttachmentInfo{Index: len(p.Attachments), Filename: name, ContentType: ct, Size: int64(len(body))})
	p.parts = append(p.parts, body)
}

var (
	tagRE   = regexp.MustCompile(`(?s)<(style|script|head)[^>]*>.*?</(style|script|head)>|<[^>]+>`)
	spaceRE = regexp.MustCompile(`\s+`)
	urlRE   = regexp.MustCompile(`https?://[^\s"'<>()\[\]{}]+`)
	hrefRE  = regexp.MustCompile(`(?i)href\s*=\s*["']([^"']+)["']`)
)

// snippet is the first ~160 characters of the text (or the HTML's text).
func (p *Parsed) snippet() string {
	s := p.Text
	if strings.TrimSpace(s) == "" {
		s = html.UnescapeString(tagRE.ReplaceAllString(p.HTML, " "))
	}
	s = strings.TrimSpace(spaceRE.ReplaceAllString(s, " "))
	if utf8.RuneCountInString(s) > 160 {
		s = string([]rune(s)[:160]) + "…"
	}
	return s
}

// links lists the http(s) links in the message, HTML hrefs first.
func (p *Parsed) links() []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(u string) {
		u = strings.TrimRight(html.UnescapeString(u), ".,;:!?")
		if !seen[u] && (strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")) {
			seen[u] = true
			out = append(out, u)
		}
	}
	for _, m := range hrefRE.FindAllStringSubmatch(p.HTML, -1) {
		add(m[1])
	}
	for _, u := range urlRE.FindAllString(p.Text, -1) {
		add(u)
	}
	return out
}

var sanitizer = func() *bluemonday.Policy {
	pol := bluemonday.UGCPolicy()
	pol.AllowStyling()
	pol.AllowAttrs("style").Globally()
	pol.AllowAttrs("align", "valign", "bgcolor", "width", "height", "border", "cellpadding", "cellspacing", "color").Globally()
	pol.AllowElements("center", "font", "table", "thead", "tbody", "tfoot", "tr", "td", "th")
	pol.AllowURLSchemes("http", "https", "mailto", "cid")
	pol.RequireNoReferrerOnLinks(true)
	pol.AddTargetBlankToFullyQualifiedLinks(true)
	return pol
}()

// sanitize makes message HTML safe to show (no script, handlers, forms or iframes).
func sanitize(s string) string { return sanitizer.Sanitize(s) }
