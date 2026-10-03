//go:build e2e

// Command smtpsink is a test SMTP relay for the email e2e test: it accepts
// AUTH PLAIN sink/sinkpass, writes every message to <dir>/<n>.eml with the
// envelope in <n>.rcpt, and refuses recipients starting with "bounce" (550).
//
//	smtpsink -addr 127.0.0.1:2626 -dir /tmp/sink
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

var n atomic.Int64

type session struct {
	dir  string
	rcpt []string
}

func (s *session) AuthMechanisms() []string { return []string{sasl.Plain} }
func (s *session) Auth(string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(_, u, p string) error {
		if u != "sink" || p != "sinkpass" {
			return errors.New("bad credentials")
		}
		return nil
	}), nil
}
func (s *session) Mail(string, *smtp.MailOptions) error { return nil }
func (s *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	if strings.HasPrefix(to, "bounce") {
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "no such user"}
	}
	s.rcpt = append(s.rcpt, to)
	return nil
}
func (s *session) Data(r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	i := n.Add(1)
	_ = os.WriteFile(filepath.Join(s.dir, fmt.Sprintf("%d.rcpt", i)), []byte(strings.Join(s.rcpt, "\n")), 0o644)
	return os.WriteFile(filepath.Join(s.dir, fmt.Sprintf("%d.eml", i)), b, 0o644)
}
func (s *session) Reset()        { s.rcpt = nil }
func (s *session) Logout() error { return nil }

func main() {
	addr := flag.String("addr", "127.0.0.1:2626", "listen address")
	dir := flag.String("dir", "/tmp/smtpsink", "where to write messages")
	flag.Parse()
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		log.Fatal(err)
	}
	srv := smtp.NewServer(smtp.BackendFunc(func(*smtp.Conn) (smtp.Session, error) { return &session{dir: *dir}, nil }))
	srv.Domain = "sink.test"
	srv.AllowInsecureAuth = true
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("smtpsink on %s writing to %s", *addr, *dir)
	log.Fatal(srv.Serve(ln))
}
