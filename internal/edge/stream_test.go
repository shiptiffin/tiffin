package edge

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A config reload (a preview's first deploy adds a host) leaves the
// WebSockets of other apps open.
func TestReloadKeepsWebSocketsOpen(t *testing.T) {
	isolate(t)
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "websocket" {
			http.Error(w, "upgrade only", 400)
			return
		}
		c, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer c.Close()
		rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		rw.Flush()
		io.Copy(c, rw)
	}))
	t.Cleanup(echo.Close)
	cfg := testConfig(t, addr(upstreamServer(t, "platform")))
	cfg.Routes = []Route{{Host: "chat.tiffin.localhost", Upstream: addr(echo)}}
	e, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Stop() })
	ca, _ := e.RootCAPEM()
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)
	c, err := tls.Dial("tcp", "127.0.0.1:"+strconv.Itoa(cfg.HTTPSPort), &tls.Config{RootCAs: pool, ServerName: "chat.tiffin.localhost", NextProtos: []string{"http/1.1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	io.WriteString(c, "GET /ws HTTP/1.1\r\nHost: chat.tiffin.localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
	br := bufio.NewReader(c)
	res, err := http.ReadResponse(br, nil)
	if err != nil || res.StatusCode != 101 {
		t.Fatalf("upgrade: %v %v", res, err)
	}
	roundTrip := func(msg string) error {
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := io.WriteString(c, msg); err != nil {
			return err
		}
		buf := make([]byte, len(msg))
		if _, err := io.ReadFull(br, buf); err != nil {
			return err
		}
		if string(buf) != msg {
			return io.ErrUnexpectedEOF
		}
		return nil
	}
	if err := roundTrip("before"); err != nil {
		t.Fatal(err)
	}
	// Another app's preview goes live: the routes change and Caddy reloads.
	next := cfg
	next.Routes = append(append([]Route{}, cfg.Routes...), Route{Host: "pr-1--shop.tiffin.localhost", Upstream: addr(echo)})
	if err := e.Reload(next); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := roundTrip("after"); err != nil && !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("the WebSocket was closed by the reload: %v", err)
	} else if err != nil {
		t.Fatalf("the WebSocket stopped answering after the reload: %v", err)
	}
}
