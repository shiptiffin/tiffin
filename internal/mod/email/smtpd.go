package email

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/ids"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

// smtpServer is Tiffin's own SMTP submission server. Apps authenticate with
// their project's credentials (AUTH PLAIN: username = project slug); every
// message goes through the same pipeline as the send API.
type smtpServer struct {
	m   *Module
	p   *platform.Platform
	srv *smtp.Server

	mu        sync.Mutex
	listeners map[string]net.Listener
}

func (s *smtpServer) start(ctx context.Context) error {
	s.srv = smtp.NewServer(smtp.BackendFunc(func(c *smtp.Conn) (smtp.Session, error) {
		return &session{s: s}, nil
	}))
	s.srv.Domain = s.p.Domain
	s.srv.MaxMessageBytes = MaxMessageBytes
	s.srv.MaxRecipients = 100
	s.srv.ReadTimeout = 2 * time.Minute
	s.srv.WriteTimeout = 2 * time.Minute
	// Loopback and the runtime's private bridge only: no TLS to offer.
	s.srv.AllowInsecureAuth = true
	s.srv.ErrorLog = discardLog{}
	s.listeners = map[string]net.Listener{}
	addr := s.m.smtpAddr
	if addr == "" {
		addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(SMTPPort))
	}
	if err := s.listen(addr); err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		_ = s.srv.Close()
	}()
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			if ip := s.m.containerHost(ctx, s.p); ip != "127.0.0.1" {
				if err := s.listen(net.JoinHostPort(ip, strconv.Itoa(s.m.smtpPort()))); err != nil {
					s.p.Log.Warn("email: listen for apps", "addr", ip, "err", err)
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return nil
}

func (s *smtpServer) listen(addr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.listeners[addr]; ok {
		return nil
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.listeners[addr] = ln
	go func() { _ = s.srv.Serve(ln) }()
	return nil
}

func (s *smtpServer) addrs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.listeners))
	for a := range s.listeners {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

type discardLog struct{}

func (discardLog) Printf(string, ...interface{}) {}
func (discardLog) Println(...interface{})        {}

type session struct {
	s       *smtpServer
	project string
	preview string
	from    string
	rcpt    []string
}

func smtpErr(code int, enh smtp.EnhancedCode, msg string) *smtp.SMTPError {
	return &smtp.SMTPError{Code: code, EnhancedCode: enh, Message: msg}
}

func (se *session) AuthMechanisms() []string { return []string{sasl.Plain} }

func (se *session) Auth(mech string) (sasl.Server, error) {
	if mech != sasl.Plain {
		return nil, smtp.ErrAuthUnknownMechanism
	}
	return sasl.NewPlainServer(func(identity, username, password string) error {
		if identity != "" && identity != username {
			return smtpErr(535, smtp.EnhancedCode{5, 7, 8}, "identity must match the username")
		}
		if !checkLogin(context.Background(), se.s.p, username, password) {
			return smtpErr(535, smtp.EnhancedCode{5, 7, 8}, "authentication failed: use your project's SMTP_URL credentials")
		}
		se.project, se.preview, _ = strings.Cut(username, "+")
		return nil
	}), nil
}

func (se *session) Mail(from string, _ *smtp.MailOptions) error {
	if se.project == "" {
		return smtpErr(530, smtp.EnhancedCode{5, 7, 0}, "authentication required: use your project's SMTP_URL credentials")
	}
	se.from = from
	return nil
}

func (se *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	if se.project == "" {
		return smtpErr(530, smtp.EnhancedCode{5, 7, 0}, "authentication required")
	}
	if _, err := mail.ParseAddress(to); err != nil {
		return smtpErr(501, smtp.EnhancedCode{5, 1, 3}, "bad recipient address")
	}
	// Suppressed recipients are accepted here, like the send API does: the
	// message is logged with them marked suppressed, and only the others get it.
	se.rcpt = append(se.rcpt, to)
	return nil
}

func (se *session) Data(r io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(r, MaxMessageBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > MaxMessageBytes {
		return smtpErr(552, smtp.EnhancedCode{5, 3, 4}, "message too big")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	p := se.s.p
	id := ids.New("msg")
	settings, err := se.s.m.settings(ctx, p, se.project)
	if err != nil || settings == nil {
		return smtpErr(550, smtp.EnhancedCode{5, 7, 1}, "email is not enabled for project "+se.project)
	}
	raw = fillHeaders(raw, settings.From, id, p.Domain)
	_, err = se.s.m.accept(ctx, p, se.project, se.preview, "smtp", id, se.from, se.rcpt, raw)
	var rl *RateLimitError
	var ve *ValidationError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &rl):
		return smtpErr(451, smtp.EnhancedCode{4, 7, 0}, rl.Error())
	case errors.As(err, &ve):
		return smtpErr(554, smtp.EnhancedCode{5, 6, 0}, ve.Error())
	}
	p.Log.Error("email: accept SMTP message", "project", se.project, "err", err)
	return smtpErr(451, smtp.EnhancedCode{4, 3, 0}, "temporary error, try again")
}

func (se *session) Reset() {
	se.from, se.rcpt = "", nil
}

func (se *session) Logout() error { return nil }

// fillHeaders adds what a submission client left out: From (the project's
// address), Date and Message-ID, plus Tiffin's message id.
func fillHeaders(raw []byte, from, id, domain string) []byte {
	tp := textproto.NewReader(bufio.NewReader(bytes.NewReader(raw)))
	h, _ := tp.ReadMIMEHeader()
	var add bytes.Buffer
	if h.Get("From") == "" {
		fmt.Fprintf(&add, "From: %s\r\n", from)
	}
	if h.Get("Date") == "" {
		fmt.Fprintf(&add, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	}
	if h.Get("Message-Id") == "" {
		fmt.Fprintf(&add, "Message-ID: <%s@%s>\r\n", id, domain)
	}
	fmt.Fprintf(&add, "X-Tiffin-Message-Id: %s\r\n", id)
	return append(add.Bytes(), raw...)
}
