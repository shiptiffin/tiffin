package email

import (
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

// A project cannot hold more than smtpDataPerProject messages open at once:
// stalled DATA streams would otherwise each pin up to MaxMessageBytes.
func TestSMTPDataIsBounded(t *testing.T) {
	r := newRig(t)
	env, _ := mod.Env(r.ctx, r.p, "shop", "")
	pw := env["SMTP_PASSWORD"]
	open := func() *smtp.Client {
		c, err := smtp.Dial(mod.smtpAddr)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		if err := c.Auth(sasl.NewPlainClient("", "shop", pw)); err != nil {
			t.Fatal(err)
		}
		if err := c.Mail("shop@tiffin.localhost", nil); err != nil {
			t.Fatal(err)
		}
		if err := c.Rcpt("bob@inbox.dev", nil); err != nil {
			t.Fatal(err)
		}
		return c
	}
	var held []io.WriteCloser
	for range smtpDataPerProject {
		w, err := open().Data()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, "Subject: held\r\n\r\n"+strings.Repeat(strings.Repeat("x", 76)+"\r\n", 1000)); err != nil {
			t.Fatal(err)
		}
		held = append(held, w)
	}
	srv := mod.smtpd
	deadline := time.Now().Add(5 * time.Second)
	for {
		srv.dataMu.Lock()
		n := srv.inData["shop"]
		srv.dataMu.Unlock()
		if n == smtpDataPerProject {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d messages arriving", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Bytes of the held messages wait on disk, not in memory.
	if ents, _ := os.ReadDir(spoolDir(r.p.DataRoot)); len(ents) != smtpDataPerProject {
		t.Fatalf("spool files: %d", len(ents))
	}
	err := r.smtpSend("shop", pw, "shop@tiffin.localhost", []string{"bob@inbox.dev"}, "Subject: one too many\r\n\r\nx\r\n")
	if err == nil || !strings.Contains(err.Error(), "too many messages") {
		t.Fatalf("a fifth concurrent message: %v", err)
	}
	for _, w := range held {
		if err := w.Close(); err != nil {
			t.Fatalf("held message: %v", err)
		}
	}
	if len(r.inbox(false)) != smtpDataPerProject {
		t.Fatalf("inbox: %d", len(r.inbox(false)))
	}
	if ents, _ := os.ReadDir(spoolDir(r.p.DataRoot)); len(ents) != 0 {
		t.Fatalf("spool not cleaned: %d", len(ents))
	}
	if err := r.smtpSend("shop", pw, "shop@tiffin.localhost", []string{"bob@inbox.dev"}, "Subject: after\r\n\r\nx\r\n"); err != nil {
		t.Fatalf("after the held ones finished: %v", err)
	}
}

func TestSMTPConnectionCap(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &smtpServer{}
	cl := &capListener{Listener: ln, n: &s.conns, max: 1}
	defer cl.Close()
	go func() {
		for {
			c, err := cl.Accept()
			if err != nil {
				return
			}
			_, _ = io.WriteString(c, "220 hi\r\n")
			go func() { _, _ = io.Copy(io.Discard, c); c.Close() }()
		}
	}()
	read := func(c net.Conn) string {
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		b := make([]byte, 64)
		n, _ := c.Read(b)
		return string(b[:n])
	}
	a, _ := net.Dial("tcp", ln.Addr().String())
	if got := read(a); !strings.HasPrefix(got, "220") {
		t.Fatalf("first: %q", got)
	}
	b, _ := net.Dial("tcp", ln.Addr().String())
	if got := read(b); !strings.HasPrefix(got, "421") {
		t.Fatalf("second: %q", got)
	}
	b.Close()
	a.Close()
	deadline := time.Now().Add(2 * time.Second)
	for s.conns.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	c, _ := net.Dial("tcp", ln.Addr().String())
	defer c.Close()
	if got := read(c); !strings.HasPrefix(got, "220") {
		t.Fatalf("after closing: %q", got)
	}
}
