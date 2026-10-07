package email

import (
	"errors"
	"strings"
	"testing"
)

// A project's SMTP credentials and send API may only send as its own
// addresses: never as the box's sender or another project's domain.
func TestSenderIsTheProjects(t *testing.T) {
	r := newRig(t)
	r.apply(`{"project":"blog","services":{"email":{}}}`)
	s := sinkRelay(t, r, ProviderOther)
	env, _ := mod.Env(r.ctx, r.p, "shop", "")
	pw := env["SMTP_PASSWORD"]
	if err := putSending(r.ctx, r.p, &SendingDomain{Project: "blog", Domain: "blog.example.org", State: SendingVerified}); err != nil {
		t.Fatal(err)
	}
	refused := []struct{ name, env, body string }{
		{"box sender in From", "shop@tiffin.localhost", "From: Tiffin <hello@tiffin.localhost>\r\nSubject: x\r\n\r\nx\r\n"},
		{"box sender as envelope", "hello@tiffin.localhost", "Subject: x\r\n\r\nx\r\n"},
		{"another project's address", "shop@tiffin.localhost", "From: blog@tiffin.localhost\r\nSubject: x\r\n\r\nx\r\n"},
		{"another project's domain", "shop@tiffin.localhost", "From: hello@blog.example.org\r\nSubject: x\r\n\r\nx\r\n"},
		{"a Sender header", "shop@tiffin.localhost", "From: shop@tiffin.localhost\r\nSender: billing@bank.example.net\r\nSubject: x\r\n\r\nx\r\n"},
		{"two From headers", "shop@tiffin.localhost", "From: shop@tiffin.localhost\r\nFrom: hello@tiffin.localhost\r\nSubject: x\r\n\r\nx\r\n"},
		{"someone else's From", "shop@tiffin.localhost", "From: security@paypal.example.net\r\nSubject: x\r\n\r\nx\r\n"},
	}
	for _, c := range refused {
		err := r.smtpSend("shop", pw, c.env, []string{"bob@inbox.dev"}, c.body)
		if err == nil || (!strings.Contains(err.Error(), "may not send as") && !strings.Contains(err.Error(), "From header")) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	_, err := Send(r.ctx, r.p, "shop", Message{From: "Tiffin <hello@tiffin.localhost>", To: []string{"bob@inbox.dev"}, Subject: "x", Text: "x"})
	var ve *ValidationError
	if !errors.As(err, &ve) || !ve.sender {
		t.Fatalf("API as the box sender: %v", err)
	}
	if s.count() != 0 || len(r.inbox(true)) != 0 {
		t.Fatalf("refused mail went somewhere: relayed %d, logged %d", s.count(), len(r.inbox(true)))
	}

	// Its own address, and its own verified domain, go out.
	if err := r.smtpSend("shop", pw, "shop@tiffin.localhost", []string{"bob@inbox.dev"}, "Subject: own\r\n\r\nx\r\n"); err != nil {
		t.Fatalf("own address: %v", err)
	}
	if _, err := Send(r.ctx, r.p, "blog", Message{From: "Blog <news@blog.example.org>", To: []string{"bob@inbox.dev"}, Subject: "x", Text: "x"}); err != nil {
		t.Fatalf("own domain: %v", err)
	}
	// No envelope sender: the From address is used.
	if err := r.smtpSend("shop", pw, "", []string{"bob@inbox.dev"}, "From: shop@tiffin.localhost\r\nSubject: null\r\n\r\nx\r\n"); err != nil {
		t.Fatalf("null sender: %v", err)
	}
	waitFor(t, "relay delivery", func() bool { return s.count() == 3 })

	// Mail kept in the dev inbox never leaves, so it is not checked.
	if err := deleteRelay(r.ctx, r.p); err != nil {
		t.Fatal(err)
	}
	if _, err := Send(r.ctx, r.p, "shop", Message{From: "hi@shop.example.net", To: []string{"bob@inbox.dev"}, Subject: "x", Text: "x"}); err != nil {
		t.Fatalf("dev inbox: %v", err)
	}
}
