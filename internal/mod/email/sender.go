package email

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-message/textproto"
	"github.com/shiptiffin/tiffin/internal/platform"
)

// Who a project may send as. Every project shares the box's relay account,
// so the relay would sign and send whatever sender a project names: the
// box checks it before mail leaves. A project may send as
//
//   - <project>@<box domain>, its default sender, and
//   - any address at its own sending domain, once that is verified with the
//     relay's provider (or set up by hand, for providers without an API),
//
// and never as the box's own sender, which is for the box's mail (sign-in
// links, invites). Mail kept in the dev inbox never leaves, so it is not
// checked.

// senderRule returns the check for project's senders.
func (m *Module) senderRule(ctx context.Context, p *platform.Platform, project string) (func(addr string) bool, error) {
	box := map[string]bool{}
	bs, err := m.boxSender(ctx, p)
	if err != nil {
		return nil, err
	}
	for _, f := range []string{bs.From, bs.DefaultFrom} {
		if a, err := mail.ParseAddress(f); err == nil {
			box[normAddr(a.Address)] = true
		}
	}
	own := normAddr(project + "@" + p.Domain)
	domain := ""
	sd, err := getSending(ctx, p, project)
	if err != nil {
		return nil, err
	}
	if sd != nil && (sd.State == SendingVerified || sd.State == SendingManual) && !strings.EqualFold(sd.Domain, p.Domain) {
		domain = strings.ToLower(sd.Domain)
	}
	return func(addr string) bool {
		a := normAddr(addr)
		if box[a] {
			return false
		}
		if a == own {
			return true
		}
		at := strings.LastIndexByte(a, '@')
		return domain != "" && at > 0 && a[at+1:] == domain
	}, nil
}

// checkSender refuses a message whose envelope sender, From or Sender is
// not one project may use. It returns the envelope sender to use: the
// From address when the client gave none.
func (m *Module) checkSender(ctx context.Context, p *platform.Platform, project, mailFrom string, raw []byte) (string, error) {
	th, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil {
		return "", invalid("message header could not be read: %v", err)
	}
	if n := len(th.Values("From")); n != 1 {
		return "", invalid("from: a message needs exactly one From header, not %d", n)
	}
	if len(th.Values("Sender")) > 1 {
		return "", invalid("sender: a message may have one Sender header")
	}
	h := mail.Header{Header: message.Header{Header: th}}
	from, err := h.AddressList("From")
	if err != nil || len(from) == 0 {
		return "", invalid("from: %q is not an email address", th.Get("From"))
	}
	addrs := []string{}
	for _, a := range from {
		addrs = append(addrs, a.Address)
	}
	if th.Has("Sender") {
		s, err := h.AddressList("Sender")
		if err != nil || len(s) != 1 {
			return "", invalid("sender: %q is not one email address", th.Get("Sender"))
		}
		addrs = append(addrs, s[0].Address)
	}
	if strings.TrimSpace(mailFrom) == "" {
		mailFrom = from[0].Address
	}
	addrs = append(addrs, mailFrom)
	ok, err := m.senderRule(ctx, p, project)
	if err != nil {
		return "", err
	}
	for _, a := range addrs {
		if !ok(a) {
			return "", &ValidationError{sender: true, Msg: fmt.Sprintf("from: %s may not send as %s: it may send as %s@%s, or from its "+
				"own sending domain once that is verified (Email › Sending domain)", project, a, project, p.Domain)}
		}
	}
	return mailFrom, nil
}
