package switchboard

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// boardFor serves host app.test from one instance listening on port.
func boardFor(t *testing.T, port int) *httptest.Server {
	t.Helper()
	b := New(nil, nil)
	b.Set(Table{
		Hosts: map[string][]Route{"app.test": {{Env: "p/a"}}},
		Envs:  map[string]*Env{"p/a": {Project: "p", App: "a", Live: "d1", Instances: []Instance{{Name: "p-a-1", Port: port}}}},
	})
	srv := httptest.NewServer(b)
	t.Cleanup(srv.Close)
	return srv
}

func portOf(t *testing.T, addr string) int {
	t.Helper()
	_, p, _ := net.SplitHostPort(addr)
	var n int
	fmt.Sscan(p, &n)
	return n
}

// keepAliveServer answers HTTP/1.1 on persistent connections the way Node's
// http server (and uvicorn) does, closing one that sat idle for keepAlive.
// It plays the race that close loses against a request already on its way:
// a request that arrives on a connection idle that long finds it closed,
// unanswered, as when the server's timer fires while the request is in
// flight. It counts the connections it took.
func keepAliveServer(t *testing.T, keepAlive time.Duration) (port int, conns *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	conns = &atomic.Int32{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns.Add(1)
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				var answered time.Time
				for {
					req, err := http.ReadRequest(r)
					if err != nil {
						return
					}
					if !answered.IsZero() && time.Since(answered) >= keepAlive {
						return // closed for idleness: the request gets no answer
					}
					n, _ := io.Copy(io.Discard, req.Body)
					body := fmt.Sprintf("got %d bytes", n)
					fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nContent-Type: text/plain\r\n\r\n%s", len(body), body)
					answered = time.Now()
				}
			}()
		}
	}()
	return portOf(t, ln.Addr().String()), conns
}

// A POST after a quiet spell longer than the app's keep-alive timeout must
// not land on the connection the app closed: Go does not retry a request
// with a body, so it would be a 502. The switchboard drops idle
// connections first (upstreamIdle, under Node's and uvicorn's 5s).
func TestPostAfterIdleGapIsNot502(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out a 5s keep-alive timeout")
	}
	if upstreamIdle >= 5*time.Second {
		t.Fatalf("upstreamIdle %s is not below the 5s keep-alive of Node and uvicorn", upstreamIdle)
	}
	port, conns := keepAliveServer(t, 5*time.Second)
	srv := boardFor(t, port)
	post := func() (int, string) {
		req, _ := http.NewRequest("POST", srv.URL+"/actions", strings.NewReader(`{"form":"data"}`))
		req.Host = "app.test"
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if code, body := post(); code != 200 || body != "got 15 bytes" {
		t.Fatalf("first POST: %d %q", code, body)
	}
	time.Sleep(5*time.Second + 300*time.Millisecond)
	if code, body := post(); code != 200 || body != "got 15 bytes" {
		t.Fatalf("POST after an idle gap: %d %q (the pooled connection outlived the app's keep-alive)", code, body)
	}
	if n := conns.Load(); n != 2 {
		t.Errorf("%d upstream connections, want 2 (a fresh one after the gap)", n)
	}
	// Within the window the connection is reused, as before.
	if code, _ := post(); code != 200 || conns.Load() != 2 {
		t.Errorf("a quick follow-up: %d, %d connections", code, conns.Load())
	}
}

// Apps are asked for identity bodies (the edge compresses); one that
// compresses anyway passes through untouched; streams are not buffered.
func TestUpstreamAcceptEncodingIdentity(t *testing.T) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	io.WriteString(zw, strings.Repeat("already compressed ", 100))
	zw.Close()
	release := make(chan struct{})
	seen := make(chan string, 8)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Accept-Encoding")
		switch r.URL.Path {
		case "/gzipped": // an app that compresses whatever it is asked
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Encoding", "gzip")
			w.Write(gz.Bytes())
		case "/sse":
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: first\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-time.After(5 * time.Second):
			}
			io.WriteString(w, "data: last\n\n")
		default:
			io.WriteString(w, "plain")
		}
	}))
	t.Cleanup(up.Close)
	srv := boardFor(t, portOf(t, up.Listener.Addr().String()))
	get := func(p string) *http.Response {
		req, _ := http.NewRequest("GET", srv.URL+p, nil)
		req.Host = "app.test"
		req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd") // set by hand: Go leaves the body as it came
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	res := get("/")
	res.Body.Close()
	if ae := <-seen; ae != "identity" {
		t.Errorf("the app was sent Accept-Encoding %q, want identity", ae)
	}

	res = get("/gzipped")
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	<-seen
	if res.Header.Get("Content-Encoding") != "gzip" || !bytes.Equal(b, gz.Bytes()) {
		t.Errorf("an app's own gzip: Content-Encoding %q, %d bytes (want its %d bytes as they were)", res.Header.Get("Content-Encoding"), len(b), gz.Len())
	}

	res = get("/sse")
	defer res.Body.Close()
	<-seen
	line := make(chan string, 1)
	go func() {
		l, _ := bufio.NewReader(res.Body).ReadString('\n')
		line <- l
	}()
	select {
	case l := <-line:
		if l != "data: first\n" {
			t.Errorf("first event %q", l)
		}
	case <-time.After(3 * time.Second):
		t.Error("the first event did not arrive while the app held the rest: the stream was buffered")
	}
	close(release)
}

// After the apps domain changed, the edge still serves app hosts under the
// old one for a while: the switchboard finds their app too.
func TestOldDomainAliasesReachTheApp(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "shop") }))
	defer app.Close()
	b := New(nil, nil)
	b.Set(Table{
		Hosts:      map[string][]Route{"shop.new.example": {{Env: "p/a"}}},
		Envs:       map[string]*Env{"p/a": {Project: "p", App: "a", Live: "d1", Instances: []Instance{{Name: "p-a-1", Port: portOf(t, app.Listener.Addr().String())}}}},
		AppsDomain: "new.example",
		Aliases:    []string{"old.example"},
	})
	srv := httptest.NewServer(b)
	defer srv.Close()
	for host, want := range map[string]int{"shop.new.example": 200, "shop.old.example": 200, "shop.other.example": 404, "a.shop.old.example": 404} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
		req.Host = host
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != want {
			t.Errorf("%s: %d, want %d", host, res.StatusCode, want)
		}
	}
}
