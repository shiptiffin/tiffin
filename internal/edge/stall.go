package edge

import (
	"io"
	"net/http"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// The stall guard cuts a connection whose client stops sending a request
// body, or stops reading a response, for stallTimeout: a slowloris holds
// connections open that way. It bounds each read of the body and each write
// of the response, and nothing between them, so an app may take as long as
// its time limit (enforced by the runtime's switchboard) to answer, and pause
// a stream for as long as it likes.
//
// Caddy's own read_idle_timeout and write_idle_timeout do the same but leave
// the last deadline set: as of 2.11 that cut a request a minute after its
// body was read (HTTP/1.1), a response stream that paused for a minute
// (HTTP/2), and every request over a minute behind the WAF, which reads the
// body first. So the server's are off and this handler clears its deadline
// when each read or write returns. Request headers keep Caddy's own limit
// (read_header_timeout, 1 minute), and an idle keep-alive connection closes
// after 5 minutes (idle_timeout).

// stallTimeout is how long a request body or a response may stall.
var stallTimeout = 5 * time.Minute

func init() { caddy.RegisterModule(StallGuard{}) }

// StallGuard is the Caddy module http.handlers.tiffin_stall.
type StallGuard struct {
	Timeout caddy.Duration `json:"timeout"`
}

// CaddyModule returns the Caddy module information.
func (StallGuard) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.tiffin_stall",
		New: func() caddy.Module { return new(StallGuard) },
	}
}

func stallHandler() obj {
	return obj{"handler": "tiffin_stall", "timeout": int64(stallTimeout)}
}

// ServeHTTP bounds the request's body reads and the response's writes.
func (g StallGuard) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	d := time.Duration(g.Timeout)
	if d <= 0 {
		return next.ServeHTTP(w, r)
	}
	rc := http.NewResponseController(w)
	if r.Body != nil && r.Body != http.NoBody {
		r.Body = &stallReader{ReadCloser: r.Body, rc: rc, d: d}
	}
	return next.ServeHTTP(&stallWriter{ResponseWriter: w, rc: rc, d: d}, r)
}

type stallReader struct {
	io.ReadCloser
	rc  *http.ResponseController
	d   time.Duration
	off bool // deadlines unsupported (HTTP/3)
}

func (s *stallReader) Read(p []byte) (int, error) {
	if !s.off {
		s.off = s.rc.SetReadDeadline(time.Now().Add(s.d)) != nil
	}
	n, err := s.ReadCloser.Read(p)
	if !s.off && (err == nil || err == io.EOF) {
		// Left set, the deadline would end the request later: HTTP/1.1
		// watches the connection for a client that goes away once the
		// body is read, and that read would time out. After a stall it
		// stays, so the server's own reads of the rest fail too.
		_ = s.rc.SetReadDeadline(time.Time{})
	}
	return n, err
}

// stallChunk bounds one write, so a big one cannot outlast the deadline
// while the client keeps reading.
const stallChunk = 64 << 10

type stallWriter struct {
	http.ResponseWriter
	rc  *http.ResponseController
	d   time.Duration
	off bool
}

func (s *stallWriter) bound(f func() error) error {
	if !s.off {
		s.off = s.rc.SetWriteDeadline(time.Now().Add(s.d)) != nil
	}
	err := f()
	if !s.off {
		// Left set, HTTP/2 would reset the stream at the deadline even
		// while the app is quiet.
		_ = s.rc.SetWriteDeadline(time.Time{})
	}
	return err
}

func (s *stallWriter) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		chunk := p[:min(len(p), stallChunk)]
		var n int
		err := s.bound(func() (err error) { n, err = s.ResponseWriter.Write(chunk); return })
		total += n
		p = p[n:]
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// ReadFrom keeps sendfile for files, a chunk at a time.
func (s *stallWriter) ReadFrom(r io.Reader) (int64, error) {
	var total int64
	for {
		var n int64
		err := s.bound(func() (err error) {
			n, err = io.Copy(s.ResponseWriter, io.LimitReader(r, stallChunk))
			return
		})
		total += n
		if err != nil || n < stallChunk {
			return total, err
		}
	}
}

func (s *stallWriter) FlushError() error {
	return s.bound(func() error { return http.NewResponseController(s.ResponseWriter).Flush() })
}

func (s *stallWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

var (
	_ caddyhttp.MiddlewareHandler = StallGuard{}
	_ io.ReaderFrom               = (*stallWriter)(nil)
)
