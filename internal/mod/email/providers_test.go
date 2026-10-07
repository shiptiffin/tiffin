package email

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-smtp"
)

func TestPresetsResolve(t *testing.T) {
	cases := []struct {
		in                   relayInput
		host, user, tls, err string
		port                 int
	}{
		{in: relayInput{Provider: "sendgrid"}, host: "smtp.sendgrid.net", port: 587, tls: "starttls", user: "apikey"},
		{in: relayInput{Provider: "sendgrid", Username: "someone"}, host: "smtp.sendgrid.net", port: 587, tls: "starttls", user: "apikey"},
		{in: relayInput{Provider: "resend"}, host: "smtp.resend.com", port: 587, tls: "starttls", user: "resend"},
		{in: relayInput{Provider: "resend", TLS: "tls"}, host: "smtp.resend.com", port: 465, tls: "tls", user: "resend"},
		{in: relayInput{Provider: "resend", Port: 2587}, host: "smtp.resend.com", port: 2587, tls: "starttls", user: "resend"},
		{in: relayInput{Provider: "postmark", Username: "ignored"}, host: "smtp.postmarkapp.com", port: 587, tls: "starttls", user: ""},
		{in: relayInput{Provider: "ses", Username: "AKIAEXAMPLE"}, host: "email-smtp.us-east-1.amazonaws.com", port: 587, tls: "starttls", user: "AKIAEXAMPLE"},
		{in: relayInput{Provider: "ses", Region: "eu-west-1", Username: "AKIAEXAMPLE"}, host: "email-smtp.eu-west-1.amazonaws.com", port: 587, tls: "starttls", user: "AKIAEXAMPLE"},
		{in: relayInput{Provider: "ses", Region: "mars-1", Username: "x"}, err: "region"},
		{in: relayInput{Provider: "ses"}, err: "username"},
		{in: relayInput{Provider: "mailgun", Region: "eu", Username: "postmaster@mg.example.com"}, host: "smtp.eu.mailgun.org", port: 587, tls: "starttls", user: "postmaster@mg.example.com"},
		{in: relayInput{Provider: "mailgun", Username: "postmaster@mg.example.com"}, host: "smtp.mailgun.org", port: 587, tls: "starttls", user: "postmaster@mg.example.com"},
		{in: relayInput{Provider: "brevo", Username: "8a1b2c001@smtp-brevo.com"}, host: "smtp-relay.brevo.com", port: 587, tls: "starttls", user: "8a1b2c001@smtp-brevo.com"},
		{in: relayInput{Provider: "brevo"}, err: "username"},
		// Cloudflare takes port 465 with implicit TLS only; the username is the literal api_token.
		{in: relayInput{Provider: "cloudflare", Username: "someone"}, host: "smtp.mx.cloudflare.net", port: 465, tls: "tls", user: "api_token"},
		{in: relayInput{Provider: "other", Host: "mail.example.com", Username: "u"}, host: "mail.example.com", port: 587, tls: "starttls", user: "u"},
		{in: relayInput{Provider: "other", Host: "mail.example.com", TLS: "tls"}, host: "mail.example.com", port: 465, tls: "tls"},
		{in: relayInput{Provider: "other"}, err: "host"},
		{in: relayInput{Provider: "other", Host: "smtp://x"}, err: "host"},
		{in: relayInput{Provider: "nope"}, err: "provider"},
		// No provider named: a known host is recognised.
		{in: relayInput{Host: "smtp.sendgrid.net", Username: "x"}, host: "smtp.sendgrid.net", port: 587, tls: "starttls", user: "apikey"},
		{in: relayInput{Host: "127.0.0.1", Port: 2, TLS: "none"}, host: "127.0.0.1", port: 2, tls: "none"},
	}
	for _, c := range cases {
		r, err := resolve(c.in)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%+v: want error about %s, got %v", c.in, c.err, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%+v: %v", c.in, err)
			continue
		}
		if r.Host != c.host || r.Port != c.port || r.TLS != c.tls || r.Username != c.user {
			t.Errorf("%+v: got %s:%d %s user %q", c.in, r.Host, r.Port, r.TLS, r.Username)
		}
	}
	// Postmark logs in with the server token as username and password.
	r, _ := resolve(relayInput{Provider: "postmark"})
	if r.smtpUser("pm-token") != "pm-token" {
		t.Fatal("postmark username should be the token")
	}
	if r2, _ := resolve(relayInput{Provider: "resend"}); r2.smtpUser("re_x") != "resend" {
		t.Fatal("resend username")
	}
	// Every real preset says where the key comes from and where domains are verified.
	for _, p := range Presets {
		if p.ID == ProviderOther {
			continue
		}
		for _, u := range []string{p.KeyURL, p.DomainURL, p.SMTPDocs} {
			if !strings.HasPrefix(u, "https://") {
				t.Errorf("%s: link %q", p.ID, u)
			}
		}
		if p.KeyLabel == "" || p.Permission == "" {
			t.Errorf("%s: key label or permission missing", p.ID)
		}
		if p.Events != nil && (len(p.Events.Steps) == 0 || len(p.Events.Enable) == 0 || !strings.HasPrefix(p.Events.SetupURL, "https://")) {
			t.Errorf("%s: events setup incomplete", p.ID)
		}
	}
	if cf := PresetByID(ProviderCloudflare); !cf.Beta || cf.Events != nil || cf.EventsNote == "" || cf.Note == "" {
		t.Error("cloudflare: beta, no events (with a note why) and a plan note")
	}
	for _, id := range eventProviders {
		if PresetByID(id).Events == nil {
			t.Errorf("%s reads events but has no setup", id)
		}
	}
}

func TestProviderForHost(t *testing.T) {
	for host, want := range map[string]string{
		"smtp.sendgrid.net": "sendgrid", "smtp.resend.com": "resend", "SMTP.POSTMARKAPP.COM.": "postmark",
		"email-smtp.eu-west-1.amazonaws.com": "ses", "ec2.amazonaws.com": "", "smtp.eu.mailgun.org": "mailgun",
		"smtp-relay.brevo.com": "brevo", "smtp.mx.cloudflare.net": "cloudflare", "cloudflare.net": "", "smtp-relay.sendinblue.com": "brevo", "mail.example.com": "", "notsendgrid.net": "",
	} {
		if got := providerForHost(host); got != want {
			t.Errorf("%s: %q, want %q", host, got, want)
		}
	}
}

// Relays saved before providers existed still load, and say what they are.
func TestRelayJSONCompatibility(t *testing.T) {
	r := newRig(t)
	old := `{"host":"smtp.resend.com","port":587,"username":"resend","tls":"starttls","passwordSet":true,"updatedAt":"2026-10-01T10:00:00Z","updatedBy":"tok_1"}`
	if err := r.p.DB.KVPut(r.ctx, kvNS, "relay", []byte(old)); err != nil {
		t.Fatal(err)
	}
	got, err := getRelay(r.ctx, r.p)
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if got.Provider != ProviderResend || got.Host != "smtp.resend.com" || got.Username != "resend" || !got.PasswordSet || got.UpdatedAt.IsZero() {
		t.Fatalf("old relay: %+v", got)
	}
	// An old Postmark relay kept the token as its username: it still logs in with it.
	pm := &Relay{Host: "smtp.postmarkapp.com", Port: 587, TLS: TLSStartTLS, Username: "pm-token"}
	if pm.smtpUser("pm-token") != "pm-token" || pm.provider() != ProviderPostmark {
		t.Fatalf("old postmark: %q %q", pm.smtpUser("pm-token"), pm.provider())
	}
	// Unknown hosts are "other".
	_ = r.p.DB.KVPut(r.ctx, kvNS, "relay", []byte(`{"host":"mail.isp.example","port":25,"tls":"none","passwordSet":false,"updatedAt":"2026-10-01T10:00:00Z"}`))
	if got, _ := getRelay(r.ctx, r.p); got.Provider != ProviderOther {
		t.Fatalf("unknown host: %+v", got)
	}
	// New relays round-trip provider and region.
	pw := "secret"
	nr := &Relay{Provider: ProviderSES, Region: "eu-west-1", Host: "email-smtp.eu-west-1.amazonaws.com", Port: 587, TLS: TLSStartTLS, Username: "AKIA", UpdatedAt: time.Now().UTC()}
	if err := setRelay(r.ctx, r.p, nr, &pw); err != nil {
		t.Fatal(err)
	}
	raw, _, _ := r.p.DB.KVGet(r.ctx, kvNS, "relay")
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if m["provider"] != "ses" || m["region"] != "eu-west-1" || m["passwordSet"] != true {
		t.Fatalf("stored: %s", raw)
	}
	if bytes.Contains(raw, []byte("secret")) {
		t.Fatal("password stored in the relay record")
	}
}

func TestExplainRelayError(t *testing.T) {
	sg := &Relay{Provider: ProviderSendGrid, Host: "smtp.sendgrid.net", Port: 587}
	other := &Relay{Provider: ProviderOther, Host: "mail.example.com", Port: 25}
	cases := []struct {
		err  error
		r    *Relay
		want string
	}{
		{fmt.Errorf("relay smtp.sendgrid.net:587 login as apikey: %w", &smtp.SMTPError{Code: 535, Message: "Authentication failed"}), sg, "SendGrid refused the API key"},
		{fmt.Errorf("relay smtp.sendgrid.net:587 refused sender a@b.c: %w", &smtp.SMTPError{Code: 550, Message: "The from address does not match a verified Sender Identity"}), sg, "app.sendgrid.com/settings/sender_auth"},
		{&tempError{fmt.Errorf("connect to relay smtp.sendgrid.net:587: dial tcp: i/o timeout")}, sg, "could not reach smtp.sendgrid.net:587"},
		{&tempError{fmt.Errorf("connect to relay mail.example.com:25: dial tcp 1.2.3.4:25: connect: connection refused")}, other, "Nothing answered at mail.example.com:25"},
		{&tempError{fmt.Errorf("connect to relay x: %w", &net.DNSError{Err: "no such host", Name: "mail.example.com"})}, other, "does not resolve"},
		{&tempError{fmt.Errorf("relay mail.example.com:25 STARTTLS: tls: first record does not look like a TLS handshake")}, other, "secure connection failed"},
		{fmt.Errorf("relay x DATA: %w", &smtp.SMTPError{Code: 451, Message: "try later"}), other, "temporary"},
		{errors.New("something new"), other, ""},
	}
	for _, c := range cases {
		got := explainRelayError(c.err, c.r)
		if c.want == "" && got != "" || !strings.Contains(got, c.want) {
			t.Errorf("%v: %q, want %q", c.err, got, c.want)
		}
	}
}
