package edge

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestLongRequests: nothing at the edge cuts a request for taking long or
// for pausing. A response that comes after a silence, a stream with gaps
// and an upload the app reads in full and answers later all succeed, over
// HTTP/1.1 and HTTP/2, with and without the WAF, and a body past the WAF's
// inspection limit reaches the app whole. With Caddy's default stall
// timeouts (1 minute; tried at 1s) the h1 upload, the h2 stream and the
// WAF's idle response were cut, so the config turns them off; the e2e
// runtime test covers minutes.
func TestLongRequests(t *testing.T) {
	isolate(t)
	const wait = 1500 * time.Millisecond

	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/idle":
			time.Sleep(wait)
			io.WriteString(w, "done")
		case "/stream":
			for i := range 3 {
				fmt.Fprintf(w, "part %d\n", i)
				w.(http.Flusher).Flush()
				time.Sleep(wait / 2)
			}
		case "/upload":
			n, err := io.Copy(io.Discard, r.Body)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			time.Sleep(wait)
			fmt.Fprintf(w, "got %d", n)
		}
	}))
	t.Cleanup(app.Close)
	up := upstreamServer(t, "platform")
	cfg := testConfig(t, addr(up))
	cfg.Routes = []Route{{Host: "shop.tiffin.localhost", Upstream: addr(app)}}
	waf := false
	SetProtectionSource(func() *Protection {
		return &Protection{App: Limit{Events: 0}, Dashboard: Limit{Events: 0}, WAF: waf}
	})
	t.Cleanup(func() { SetProtectionSource(nil) })
	e, err := Start(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Stop() })
	testEdge, testEdgeCfg = e, cfg
	ca, err := e.RootCAPEM()
	if err != nil {
		t.Fatal(err)
	}
	base := "https://shop.tiffin.localhost:" + strconv.Itoa(cfg.HTTPSPort)
	// A browser's file upload, 21 MB: past the WAF's 12.5 MB inspection limit.
	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	fw, _ := mw.CreateFormFile("file", "big.bin")
	fw.Write(bytes.Repeat([]byte("upload "), 3<<20))
	mw.Close()
	body := form.Bytes()

	run := func(t *testing.T, h2 bool) {
		c := client(t, ca, cfg.HTTPSPort)
		c.Timeout = 30 * time.Second
		if !h2 {
			tr := c.Transport.(*http.Transport)
			tr.ForceAttemptHTTP2 = false
			tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
		}
		proto := func(r *http.Response) {
			t.Helper()
			if want := map[bool]int{true: 2, false: 1}[h2]; r.ProtoMajor != want {
				t.Fatalf("spoke HTTP/%d, want HTTP/%d", r.ProtoMajor, want)
			}
		}
		if r, got := get(t, c, base+"/idle"); r.StatusCode != 200 || got != "done" {
			t.Fatalf("idle: %d %q", r.StatusCode, got)
		} else {
			proto(r)
		}
		if r, got := get(t, c, base+"/stream"); r.StatusCode != 200 || strings.Count(got, "part") != 3 {
			t.Fatalf("stream: %d %q", r.StatusCode, got)
		}
		for range 2 { // the second on a reused connection
			r, err := c.Post(base+"/upload", mw.FormDataContentType(), bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			got, _ := io.ReadAll(r.Body)
			r.Body.Close()
			if want := fmt.Sprintf("got %d", len(body)); r.StatusCode != 200 || string(got) != want {
				t.Fatalf("upload: %d %q, want %q", r.StatusCode, got, want)
			}
		}
	}
	for _, w := range []bool{false, true} {
		waf = w
		if err := edgeReload(t); err != nil {
			t.Fatal(err)
		}
		t.Run(fmt.Sprintf("waf=%v/h1", w), func(t *testing.T) { run(t, false) })
		t.Run(fmt.Sprintf("waf=%v/h2", w), func(t *testing.T) { run(t, true) })
	}
}
