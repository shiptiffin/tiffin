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
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

	conns atomic.Int64 // open connections, across every listener

	dataMu    sync.Mutex
	inData    map[string]int // project → messages being received now
	dataTotal int
	process   chan struct{} // a slot per message held in memory at once
}

// Limits on what SMTP clients can make the control plane hold. A message is
// streamed to a spool file while it arrives, so a slow or stalled client
// costs disk, not memory; only smtpProcessing messages are in memory at once.
const (
	smtpMaxConns       = 256              // open connections, all projects
	smtpDataPerProject = 4                // messages arriving at once, per project
	smtpDataTotal      = 16               // messages arriving at once, all projects
	smtpDataTime       = 10 * time.Minute // longest one message may take to arrive
	smtpProcessing     = 2                // messages parsed and stored at once
)

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
	s.inData = map[string]int{}
	s.process = make(chan struct{}, smtpProcessing)
	_ = os.RemoveAll(spoolDir(s.p.DataRoot)) // leftovers from a crash
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
	go func() { _ = s.srv.Serve(&capListener{Listener: ln, n: &s.conns, max: smtpMaxConns}) }()
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

// capListener turns away connections beyond max (with a 421) instead of
// letting them pile up.
type capListener struct {
	net.Listener
	n   *atomic.Int64
	max int64
}

func (l *capListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if l.n.Add(1) > l.max {
			l.n.Add(-1)
			_ = c.SetWriteDeadline(time.Now().Add(time.Second))
			_, _ = io.WriteString(c, "421 4.3.2 Too many connections, try again later\r\n")
			_ = c.Close()
			continue
		}
		return &countedConn{Conn: c, n: l.n}, nil
	}
}

type countedConn struct {
	net.Conn
	n    *atomic.Int64
	once sync.Once
}

func (c *countedConn) Close() error {
	c.once.Do(func() { c.n.Add(-1) })
	return c.Conn.Close()
}

// reserve takes a receiving slot for one of project's messages.
func (s *smtpServer) reserve(project string) bool {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	if s.dataTotal >= smtpDataTotal || s.inData[project] >= smtpDataPerProject {
		return false
	}
	s.dataTotal++
	s.inData[project]++
	return true
}

func (s *smtpServer) release(project string) {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	s.dataTotal--
	if s.inData[project]--; s.inData[project] <= 0 {
		delete(s.inData, project)
	}
}

func spoolDir(root string) string { return filepath.Join(root, "email", "spool") }

var errDataTimeout = smtpErr(451, smtp.EnhancedCode{4, 4, 2}, "the message took too long to arrive")

// deadlineReader fails once until has passed, so a client trickling bytes
// cannot hold a receiving slot for ever.
type deadlineReader struct {
	r     io.Reader
	until time.Time
}

func (d *deadlineReader) Read(b []byte) (int, error) {
	if time.Now().After(d.until) {
		return 0, errDataTimeout
	}
	return d.r.Read(b)
}

// spool streams a message into a temporary file (at most MaxMessageBytes+1
// bytes) and returns it rewound, with its size. The caller removes it.
func spool(root string, r io.Reader) (*os.File, int64, error) {
	dir := spoolDir(root)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, 0, err
	}
	f, err := os.CreateTemp(dir, "msg-*")
	if err != nil {
		return nil, 0, err
	}
	n, err := io.Copy(f, io.LimitReader(&deadlineReader{r: r, until: time.Now().Add(smtpDataTime)}, MaxMessageBytes+1))
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, 0, err
	}
	return f, n, nil
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
	if !se.s.reserve(se.project) {
		return smtpErr(451, smtp.EnhancedCode{4, 3, 2}, "too many messages are arriving at once; try again shortly")
	}
	defer se.s.release(se.project)
	p := se.s.p
	f, n, err := spool(p.DataRoot, r)
	if err != nil {
		var serr *smtp.SMTPError
		if errors.As(err, &serr) {
			return err
		}
		p.Log.Error("email: spool SMTP message", "err", err)
		return smtpErr(451, smtp.EnhancedCode{4, 3, 0}, "temporary error, try again")
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if n > MaxMessageBytes {
		return smtpErr(552, smtp.EnhancedCode{5, 3, 4}, "message too big")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// Only a few messages are held in memory at once.
	select {
	case se.s.process <- struct{}{}:
		defer func() { <-se.s.process }()
	case <-ctx.Done():
		return smtpErr(451, smtp.EnhancedCode{4, 3, 2}, "the box is busy; try again shortly")
	}
	raw := make([]byte, n)
	if _, err := io.ReadFull(f, raw); err != nil {
		p.Log.Error("email: read spooled SMTP message", "err", err)
		return smtpErr(451, smtp.EnhancedCode{4, 3, 0}, "temporary error, try again")
	}
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
	case errors.As(err, &ve) && ve.sender:
		return smtpErr(550, smtp.EnhancedCode{5, 7, 1}, ve.Error())
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
