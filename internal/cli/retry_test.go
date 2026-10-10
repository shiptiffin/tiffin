package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/shiptiffin/tiffin/internal/api"
)

func init() {
	retryWaits = []time.Duration{10 * time.Millisecond, 20 * time.Millisecond}
}

// fakeBox answers like a box for the retry tests: creates under
// /v1/things (each run counted), uploads under /v1/uploads, and the
// Idempotency-Key lookup.
type fakeBox struct {
	mu      sync.Mutex
	runs    int
	bodies  []string
	answers map[string]string // Idempotency-Key -> stored answer
}

func newFakeBox() *fakeBox { return &fakeBox{answers: map[string]string{}} }

func (f *fakeBox) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if key, ok := strings.CutPrefix(r.URL.Path, "/v1/idempotency-keys/"); ok {
		st := keyState{Status: "none"}
		if ans, ok := f.answers[key]; ok {
			st = keyState{Status: "done", Method: http.MethodPost, Path: "/v1/things", ResponseStatus: 200, Response: json.RawMessage(ans)}
		}
		_ = json.NewEncoder(w).Encode(st)
		return
	}
	key := r.Header.Get(api.IdempotencyHeader)
	if ans, ok := f.answers[key]; ok && key != "" {
		w.Header().Set(api.IdempotencyStatusHeader, "replayed")
		w.WriteHeader(200)
		io.WriteString(w, ans)
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		panic(http.ErrAbortHandler)
	}
	f.runs++
	f.bodies = append(f.bodies, string(raw))
	sum := sha256.Sum256(raw)
	ans := fmt.Sprintf(`{"id":"t%d","sha256":%q}`, f.runs, hex.EncodeToString(sum[:]))
	if key != "" {
		f.answers[key] = ans
	}
	w.WriteHeader(200)
	io.WriteString(w, ans)
}

func (f *fakeBox) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs
}

// h2Server is an HTTPS test server speaking HTTP/2, as the box's edge does.
func h2Server(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	s := httptest.NewUnstartedServer(h)
	s.EnableHTTP2 = true
	s.StartTLS()
	t.Cleanup(s.Close)
	return s
}

// testClient talks to srv over HTTP/2 through base (nil: srv's transport),
// noting retries in notes.
func testClient(srv *httptest.Server, wrap func(http.RoundTripper) http.RoundTripper, notes *[]string) *client {
	tr := srv.Client().Transport.(*http.Transport).Clone()
	tr.ForceAttemptHTTP2 = true
	var base http.RoundTripper = tr
	if wrap != nil {
		base = wrap(tr)
	}
	var mu sync.Mutex
	return &client{base: srv.URL, token: "tfn_test", transport: base, close: func() error { return nil },
		note: func(s string) { mu.Lock(); *notes = append(*notes, s); mu.Unlock() }}
}

// rtFunc is a RoundTripper from a function.
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// goAwayThenBox is a client whose first connection goes to a server that
// takes the request (handled first by got, when set), sends GOAWAY and
// closes the connection, as Caddy does when the box reloads its edge; later
// connections reach box. errs collects the transport's errors.
func goAwayThenBox(t *testing.T, box http.Handler, got http.Handler, errs *[]error, notes *[]string) *client {
	t.Helper()
	good := h2Server(t, box)
	var reloading *httptest.Server
	reloading = h2Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got != nil {
			got.ServeHTTP(httptest.NewRecorder(), r)
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = reloading.Config.Shutdown(ctx) // GOAWAY to every connection
		}()
		time.Sleep(200 * time.Millisecond)
		reloading.CloseClientConnections() // and close them, this request unanswered
		<-r.Context().Done()
	}))
	var mu sync.Mutex
	dials := 0
	tr := good.Client().Transport.(*http.Transport).Clone()
	tr.ForceAttemptHTTP2 = true
	d := &net.Dialer{}
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		mu.Lock()
		dials++
		target := good.Listener.Addr().String()
		if dials == 1 {
			target = reloading.Listener.Addr().String()
		}
		mu.Unlock()
		return d.DialContext(ctx, network, target)
	}
	rec := rtFunc(func(r *http.Request) (*http.Response, error) {
		res, err := tr.RoundTrip(r)
		if err != nil {
			mu.Lock()
			*errs = append(*errs, err)
			mu.Unlock()
		}
		return res, err
	})
	return &client{base: good.URL, token: "tfn_test", transport: rec, close: func() error { return nil },
		note: func(s string) { mu.Lock(); *notes = append(*notes, s); mu.Unlock() }}
}

// A GET cut off by GOAWAY (Go's GoAwayError: the box got it, the
// connection closed before the answer) is simply asked again.
func TestGetSurvivesGoAway(t *testing.T) {
	var errs []error
	var notes []string
	c := goAwayThenBox(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"ok":true}`) }), nil, &errs, &notes)
	status, raw, err := c.do(context.Background(), http.MethodGet, "/v1/status", nil, nil)
	if err != nil || status != 200 || string(raw) != `{"ok":true}` {
		t.Fatalf("do: %d %s %v", status, raw, err)
	}
	if len(errs) != 1 || !isGoAway(errs[0]) || classify(errs[0]) != ambiguous {
		t.Fatalf("the first attempt should end in GoAwayError: %v", errs)
	}
	if len(notes) == 0 || notes[0] != retryNote {
		t.Fatalf("notes: %q", notes)
	}
}

// A create cut off the same way is not sent again blindly: without a key
// it fails; with one, the box is asked, and its stored answer is used.
func TestCreateAfterGoAway(t *testing.T) {
	t.Run("no key", func(t *testing.T) {
		box := newFakeBox()
		var errs []error
		var notes []string
		c := goAwayThenBox(t, box, box, &errs, &notes)
		_, _, err := c.do(context.Background(), http.MethodPost, "/v1/things", nil, map[string]any{"name": "a"})
		if err == nil || !isUnreachable(err) || !strings.Contains(err.Error(), "GOAWAY") {
			t.Fatalf("err = %v", err)
		}
		if box.count() != 1 {
			t.Fatalf("runs = %d, want 1 (never sent twice)", box.count())
		}
	})
	t.Run("key, done on the box", func(t *testing.T) {
		box := newFakeBox()
		var errs []error
		var notes []string
		c := goAwayThenBox(t, box, box, &errs, &notes)
		status, raw, err := c.doOnce(context.Background(), http.MethodPost, "/v1/things", nil, map[string]any{"name": "a"})
		if err != nil || status != 200 || !strings.Contains(string(raw), `"id":"t1"`) {
			t.Fatalf("doOnce: %d %s %v", status, raw, err)
		}
		if box.count() != 1 {
			t.Fatalf("runs = %d, want 1", box.count())
		}
	})
	t.Run("key, never ran", func(t *testing.T) {
		box := newFakeBox()
		var errs []error
		var notes []string
		c := goAwayThenBox(t, box, nil, &errs, &notes)
		status, raw, err := c.doOnce(context.Background(), http.MethodPost, "/v1/things", nil, map[string]any{"name": "a"})
		if err != nil || status != 200 || !strings.Contains(string(raw), `"id":"t1"`) || box.count() != 1 {
			t.Fatalf("doOnce: %d %s %v, runs %d", status, raw, err, box.count())
		}
	})
}

// An upload the box never processed (here: its stream refused part way) is
// sent again from the start of the file.
func TestFileUploadSentAgainFromStart(t *testing.T) {
	box := newFakeBox()
	srv := h2Server(t, box)
	content := bytes.Repeat([]byte("tiffin archive "), 50_000)
	path := filepath.Join(t.TempDir(), "a.tiffin")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(path)
	defer f.Close()
	attempts := 0
	var notes []string
	c := testClient(srv, func(next http.RoundTripper) http.RoundTripper {
		return rtFunc(func(r *http.Request) (*http.Response, error) {
			attempts++
			if attempts == 1 {
				_, _ = io.CopyN(io.Discard, r.Body, 100_000) // part of it went out
				r.Body.Close()
				return nil, h2StreamError{StreamID: 1, Code: h2ErrCodeRefusedStream}
			}
			return next.RoundTrip(r)
		})
	}, &notes)
	var sent atomic.Int64
	body := fileBody(f, int64(len(content)), func(n int64) {
		if n < 0 {
			sent.Store(0)
		} else {
			sent.Add(n)
		}
	})
	res, err := c.stream(context.Background(), http.MethodPost, "/v1/uploads", body, "application/octet-stream", "")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	sum := sha256.Sum256(content)
	if res.StatusCode != 200 || !strings.Contains(string(raw), hex.EncodeToString(sum[:])) || box.count() != 1 || attempts != 2 {
		t.Fatalf("upload: %d %s, runs %d, attempts %d", res.StatusCode, raw, box.count(), attempts)
	}
	if sent.Load() != int64(len(content)) {
		t.Fatalf("progress counted %d bytes, want %d", sent.Load(), len(content))
	}
}

// The import symptom: the archive arrived and the box stored it, then the
// connection broke before the answer. The CLI finds the upload by its key
// instead of failing, and does not upload it again.
func TestUploadFoundAfterLostAnswer(t *testing.T) {
	box := newFakeBox()
	var cut atomic.Bool
	cut.Store(true)
	srv := h2Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && cut.CompareAndSwap(true, false) {
			box.ServeHTTP(httptest.NewRecorder(), r) // processed...
			panic(http.ErrAbortHandler)              // ...and the answer lost
		}
		box.ServeHTTP(w, r)
	}))
	path := filepath.Join(t.TempDir(), "a.tiffin")
	_ = os.WriteFile(path, []byte("an archive"), 0o600)
	f, _ := os.Open(path)
	defer f.Close()
	var notes []string
	c := testClient(srv, nil, &notes)
	a := &app{io: IO{Err: io.Discard}}
	var job struct {
		ID string `json:"id"`
	}
	if err := a.uploadArchive(context.Background(), c, "/v1/things", f, 10, &job); err != nil {
		t.Fatal(err)
	}
	if job.ID != "t1" || box.count() != 1 {
		t.Fatalf("job %+v, uploads %d", job, box.count())
	}
	// Without a key, the same failure is reported, not sent again.
	cut.Store(true)
	res, err := c.stream(context.Background(), http.MethodPost, "/v1/things", fileBody(f, 10, nil), "application/octet-stream", "")
	if err == nil {
		res.Body.Close()
		t.Fatal("a create without a key was sent again")
	}
	if box.count() != 2 {
		t.Fatalf("uploads %d, want 2", box.count())
	}
}

// An answer that breaks off after its headers is asked for again (GET).
func TestDoRetriesBrokenAnswer(t *testing.T) {
	var hits atomic.Int32
	srv := h2Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(200)
			io.WriteString(w, `{"partial`)
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}
		io.WriteString(w, `{"ok":true}`)
	}))
	var notes []string
	c := testClient(srv, nil, &notes)
	status, raw, err := c.do(context.Background(), http.MethodGet, "/v1/status", nil, nil)
	if err != nil || status != 200 || string(raw) != `{"ok":true}` || hits.Load() != 2 {
		t.Fatalf("do: %d %s %v, hits %d", status, raw, err, hits.Load())
	}
	hits.Store(0)
	if _, _, err := c.do(context.Background(), http.MethodPost, "/v1/things", nil, map[string]any{}); err == nil || hits.Load() != 1 {
		t.Fatalf("a POST whose answer broke off: %v, hits %d", err, hits.Load())
	}
}

// Waiting on a job outlasts a box that does not answer for a few polls.
func TestWaitOutlastsDroppedConnections(t *testing.T) {
	var polls atomic.Int32
	srv := h2Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if polls.Add(1) <= 4 { // more than one call's attempts
			panic(http.ErrAbortHandler)
		}
		io.WriteString(w, `{"id":"pj_1","status":"done"}`)
	}))
	var notes []string
	c := testClient(srv, nil, &notes)
	a := &app{io: IO{Err: io.Discard}}
	job := projectJob{ID: "pj_1", Status: "running"}
	if err := a.waitProjectJob(context.Background(), c, &job); err != nil || job.Status != "done" {
		t.Fatalf("wait: %v %+v", err, job)
	}
}

// The MCP proxy's bodies can be sent again too.
func TestProxyResendsUnprocessed(t *testing.T) {
	box := newFakeBox()
	srv := httptest.NewServer(box)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	attempts := 0
	p := proxyTo(u, rtFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			_, _ = io.ReadAll(r.Body)
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
		}
		return http.DefaultTransport.RoundTrip(r)
	}))
	req := httptest.NewRequest(http.MethodPost, "http://box/v1/things", strings.NewReader(`{"name":"a"}`))
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != 200 || attempts != 2 || box.count() != 1 || box.bodies[0] != `{"name":"a"}` {
		t.Fatalf("proxy: %d %s, attempts %d, bodies %q", rec.Code, rec.Body, attempts, box.bodies)
	}
}

func TestClassify(t *testing.T) {
	for _, c := range []struct {
		err  error
		want failure
	}{
		{&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, unprocessed},
		{h2StreamError{StreamID: 3, Code: h2ErrCodeRefusedStream}, unprocessed},
		{errors.New("http2: Transport received Server's graceful shutdown GOAWAY"), unprocessed},
		{&net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, ambiguous},
		{fmt.Errorf("net/http: HTTP/1.x transport connection broken: %w", io.ErrUnexpectedEOF), ambiguous},
		{h2StreamError{StreamID: 3, Code: 2}, ambiguous},
		{errors.New("http2: server sent GOAWAY and closed the connection; LastStreamID=1, ErrCode=NO, debug=\"\""), ambiguous},
		{&net.OpError{Op: "dial", Err: &net.DNSError{Err: "no such host", IsNotFound: true}}, fatal},
		{x509.UnknownAuthorityError{}, fatal},
		{context.Canceled, fatal},
		{&url.Error{Op: "Get", Err: context.DeadlineExceeded}, fatal},
		{errors.New("boom"), fatal},
	} {
		if got := classify(c.err); got != c.want {
			t.Errorf("classify(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

// A poll that cannot reach the box is made again, for a while; an answer
// that says no is not.
func TestPatience(t *testing.T) {
	p := patience{limit: time.Hour}
	if !p.again(&unreachableError{"b", io.EOF}) || p.again(errors.New("404")) {
		t.Fatal("patience: unreachable should be retried, an answer not")
	}
	p = patience{limit: time.Millisecond}
	p.again(&unreachableError{"b", io.EOF})
	time.Sleep(5 * time.Millisecond)
	if p.again(&unreachableError{"b", io.EOF}) {
		t.Fatal("patience never ran out")
	}
}

// A deploy's source, packed as it uploads, is packed again for a second
// attempt, and the box gets all of it.
func TestDeploySourcePackedAgain(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "index.html"), bytes.Repeat([]byte("<p>hello</p>\n"), 20_000), 0o644)
	var got atomic.Value
	srv := h2Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		tr := tar.NewReader(zr)
		var names []string
		for {
			h, err := tr.Next()
			if err != nil {
				if err != io.EOF {
					http.Error(w, err.Error(), 400)
					return
				}
				break
			}
			names = append(names, h.Name)
		}
		got.Store(strings.Join(names, ","))
		if r.Header.Get(api.IdempotencyHeader) == "" {
			http.Error(w, "no key", 400)
			return
		}
		w.WriteHeader(202)
		io.WriteString(w, `{"id":"dep_1","project":"shop","app":"web","status":"queued"}`)
	}))
	attempts := 0
	var notes []string
	c := testClient(srv, func(next http.RoundTripper) http.RoundTripper {
		return rtFunc(func(r *http.Request) (*http.Response, error) {
			if attempts++; attempts == 1 {
				_, _ = io.CopyN(io.Discard, r.Body, 100)
				r.Body.Close()
				return nil, h2StreamError{StreamID: 1, Code: h2ErrCodeRefusedStream}
			}
			return next.RoundTrip(r)
		})
	}, &notes)
	a := &app{io: IO{Err: io.Discard}}
	d, err := a.upload(context.Background(), c, "shop", "web", dir, "", "")
	if err != nil || d.ID != "dep_1" || attempts != 2 || got.Load() != "index.html" {
		t.Fatalf("upload: %+v %v, attempts %d, box got %v", d, err, attempts, got.Load())
	}
}

// Inside Codex's sandbox a box that cannot be reached says how to let the
// command through.
func TestUnreachableInCodexSandbox(t *testing.T) {
	c := &client{base: "https://dashboard.example.com"}
	if err := c.unreachable(io.EOF); strings.Contains(err.Error(), "Codex") {
		t.Fatalf("outside Codex: %v", err)
	}
	t.Setenv("CODEX_SANDBOX_NETWORK_DISABLED", "1")
	err := c.unreachable(&url.Error{Op: "Get", URL: c.base, Err: io.EOF})
	if !isUnreachable(err) || !errors.Is(err, io.EOF) || !strings.HasSuffix(err.Error(), "EOF. "+codexSandboxHint) {
		t.Fatalf("in Codex: %v", err)
	}
}
