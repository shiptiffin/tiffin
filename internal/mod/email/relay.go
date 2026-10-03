package email

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/btahir/tiffin/internal/platform"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

// Relay TLS modes.
const (
	TLSStartTLS = "starttls" // plain connect, then STARTTLS (required); port 587
	TLSImplicit = "tls"      // TLS from the first byte; port 465
	TLSNone     = "none"     // no TLS: only for local test sinks
)

// Relay is the box's outbound SMTP relay (Resend, SES, Postmark, your ISP...).
type Relay struct {
	Host        string    `json:"host" doc:"Relay hostname, e.g. smtp.resend.com"`
	Port        int       `json:"port" doc:"Usually 587 (starttls) or 465 (tls)"`
	Username    string    `json:"username,omitempty"`
	TLS         string    `json:"tls" enum:"starttls,tls,none"`
	PasswordSet bool      `json:"passwordSet" doc:"Whether a password is stored (it is never shown)"`
	UpdatedAt   time.Time `json:"updatedAt"`
	UpdatedBy   string    `json:"updatedBy,omitempty"`
}

const (
	kvNS            = "email"
	secretsProject  = "_email" // pseudo-project for the module's secrets; never a real slug
	relayPassSecret = "RELAY_PASSWORD"
)

func getRelay(ctx context.Context, p *platform.Platform) (*Relay, error) {
	raw, ok, err := p.DB.KVGet(ctx, kvNS, "relay")
	if err != nil || !ok {
		return nil, err
	}
	var r Relay
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func relayPassword(ctx context.Context, p *platform.Platform) (string, error) {
	if p.Secrets == nil {
		return "", nil
	}
	all, err := p.Secrets.All(ctx, secretsProject)
	if err != nil {
		return "", err
	}
	return all[relayPassSecret], nil
}

func setRelay(ctx context.Context, p *platform.Platform, r *Relay, password *string) error {
	if password != nil {
		if *password == "" {
			if _, err := p.Secrets.Delete(ctx, secretsProject, relayPassSecret); err != nil {
				return err
			}
		} else if err := p.Secrets.Set(ctx, secretsProject, relayPassSecret, *password, r.UpdatedBy); err != nil {
			return err
		}
	}
	pw, err := relayPassword(ctx, p)
	if err != nil {
		return err
	}
	r.PasswordSet = pw != ""
	raw, _ := json.Marshal(r)
	return p.DB.KVPut(ctx, kvNS, "relay", raw)
}

func deleteRelay(ctx context.Context, p *platform.Platform) error {
	if p.Secrets != nil {
		if _, err := p.Secrets.Delete(ctx, secretsProject, relayPassSecret); err != nil {
			return err
		}
	}
	return p.DB.KVDelete(ctx, kvNS, "relay")
}

// sendResult is the outcome of one relay attempt.
type sendResult struct {
	Accepted []string          // recipients the relay took
	Rejected map[string]string // permanently rejected recipient → reply (5xx)
	Reply    string            // the relay's reply to DATA
}

// errTemporary marks failures worth retrying (4xx, network, timeouts).
type tempError struct{ err error }

func (e *tempError) Error() string { return e.err.Error() }
func (e *tempError) Unwrap() error { return e.err }

func isTemporary(err error) bool {
	var te *tempError
	return errors.As(err, &te)
}

func classify(err error) error {
	var se *smtp.SMTPError
	if errors.As(err, &se) {
		if se.Code >= 400 && se.Code < 500 {
			return &tempError{err}
		}
		return err
	}
	return &tempError{err} // network, TLS, timeouts
}

// deliver sends one message through the relay. Recipients the relay refuses
// permanently are reported in Rejected and skipped; any temporary refusal
// fails the whole attempt so it is retried later.
func deliver(ctx context.Context, r *Relay, password, helo, from string, rcpt []string, raw []byte) (*sendResult, error) {
	addr := net.JoinHostPort(r.Host, strconv.Itoa(r.Port))
	d := net.Dialer{Timeout: 20 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, &tempError{fmt.Errorf("connect to relay %s: %w", addr, err)}
	}
	deadline := time.Now().Add(2 * time.Minute)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)
	tc := &tls.Config{ServerName: r.Host, MinVersion: tls.VersionTLS12}
	var c *smtp.Client
	switch r.TLS {
	case TLSImplicit, TLSNone:
		if r.TLS == TLSImplicit {
			c = smtp.NewClient(tls.Client(conn, tc))
		} else {
			c = smtp.NewClient(conn)
		}
		if err := c.Hello(helo); err != nil {
			c.Close()
			return nil, classify(fmt.Errorf("relay %s EHLO: %w", addr, err))
		}
	default:
		// go-smtp greets as "localhost" before STARTTLS; relays accept that.
		c, err = smtp.NewClientStartTLS(conn, tc)
		if err != nil {
			conn.Close()
			return nil, classify(fmt.Errorf("relay %s STARTTLS: %w", addr, err))
		}
	}
	defer c.Close()
	if r.Username != "" {
		if err := c.Auth(sasl.NewPlainClient("", r.Username, password)); err != nil {
			return nil, classify(fmt.Errorf("relay %s login as %s: %w", addr, r.Username, err))
		}
	}
	if err := c.Mail(from, nil); err != nil {
		return nil, classify(fmt.Errorf("relay %s refused sender %s: %w", addr, from, err))
	}
	res := &sendResult{Rejected: map[string]string{}}
	for _, to := range rcpt {
		if err := c.Rcpt(to, nil); err != nil {
			if isTemporary(classify(err)) {
				return nil, classify(fmt.Errorf("relay %s deferred %s: %w", addr, to, err))
			}
			res.Rejected[to] = err.Error()
			continue
		}
		res.Accepted = append(res.Accepted, to)
	}
	if len(res.Accepted) == 0 {
		_ = c.Reset()
		_ = c.Quit()
		return res, nil
	}
	w, err := c.Data()
	if err != nil {
		return nil, classify(fmt.Errorf("relay %s DATA: %w", addr, err))
	}
	if _, err := w.Write(raw); err != nil {
		return nil, &tempError{err}
	}
	resp, err := w.CloseWithResponse()
	if err != nil {
		return nil, classify(fmt.Errorf("relay %s refused the message: %w", addr, err))
	}
	if resp != nil {
		res.Reply = resp.StatusText
	}
	_ = c.Quit()
	return res, nil
}

// testMessage builds the relay test message.
func testMessage(from, to, domain, id string) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: Tiffin relay test\r\nDate: %s\r\nMessage-ID: <%s@%s>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n"+
		"This is a test from your Tiffin box (%s). If you can read it, the SMTP relay works.\r\n",
		from, to, time.Now().UTC().Format(time.RFC1123Z), id, domain, domain)
	return b.Bytes()
}
