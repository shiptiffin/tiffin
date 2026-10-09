package cli

// Requests to a box outlive its edge reloading. Deploys and imports change
// routes, the box then reloads Caddy, and Caddy shuts the old server down
// gracefully: it sends HTTP/2 GOAWAY and closes the old connections. Go's
// transport sends a request again on its own only when the server provably
// did not process it and the body can be read again (Request.GetBody), so
// every request here gets a GetBody (JSON bodies by net/http, uploads by
// reopening the file or packing the directory again), and retryTransport
// handles what is left:
//
//   - not processed (the dial failed, the stream was refused, or Go ran out
//     of its own GOAWAY retries): sent again, whatever the method;
//   - outcome unknown (the connection dropped after the box got the request:
//     GOAWAY then close, connection reset, EOF): sent again only when that
//     is safe, an idempotent method (GET, HEAD, OPTIONS, PUT, DELETE), or a
//     request with an Idempotency-Key. For those the box is asked first what
//     became of the key; its stored answer is used, or the request is sent
//     again (with the same key) only when the box never ran it.
//
// Up to three attempts, a short wait between them.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/shiptiffin/tiffin/internal/api"
)

// retryNote is what the CLI says when it sends a request again.
const retryNote = "retrying: the box reloaded its edge"

type retryTransport struct {
	base    http.RoundTripper
	note    func(string)    // says why a request is sent again (nil: quiet)
	waits   []time.Duration // before the second, third... attempt
	poll    time.Duration   // between lookups of a keyed request still running
	pollFor time.Duration   // how long to wait for one
}

// retryWaits are the waits before the second and third attempts (tests
// shorten them).
var retryWaits = []time.Duration{300 * time.Millisecond, time.Second}

func newRetryTransport(base http.RoundTripper, note func(string)) *retryTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	return &retryTransport{base: base, note: note, waits: retryWaits, poll: time.Second, pollFor: 15 * time.Minute}
}

// failure is what a transport error says about the request.
type failure int

const (
	fatal       failure = iota // not a dropped connection: report it
	unprocessed                // the box never got it: safe to send again
	ambiguous                  // the box may have got it and acted on it
)

// h2StreamError has the shape of net/http's HTTP/2 StreamError, which
// converts itself into any struct like it (errors.As).
type h2StreamError struct {
	StreamID uint32
	Code     uint32
	Cause    error
}

func (e h2StreamError) Error() string {
	return fmt.Sprintf("stream error: stream ID %d; code %d", e.StreamID, e.Code)
}

const h2ErrCodeRefusedStream = 0x7

// classify says what a transport error means for a request that got it.
func classify(err error) failure {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fatal
	}
	var (
		certErr *tls.CertificateVerificationError
		unknown x509.UnknownAuthorityError
		host    x509.HostnameError
		dnsErr  *net.DNSError
		opErr   *net.OpError
		se      h2StreamError
	)
	switch {
	case errors.As(err, &certErr), errors.As(err, &unknown), errors.As(err, &host):
		return fatal
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return fatal
	case errors.As(err, &opErr) && opErr.Op == "dial":
		return unprocessed // no connection, so nothing was sent
	case errors.As(err, &se) && se.Code == h2ErrCodeRefusedStream:
		return unprocessed // the server says it did not process the stream
	case strings.Contains(err.Error(), "received Server's graceful shutdown GOAWAY"):
		// The stream was above the GOAWAY's last stream ID: never processed
		// (Go's own retries for it ran out, or the body could not be rewound).
		return unprocessed
	case isGoAway(err):
		// GOAWAY, then the connection closed with this stream unanswered:
		// the box got it (its ID was within the GOAWAY's last stream ID).
		return ambiguous
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE), errors.Is(err, syscall.ECONNABORTED),
		errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.As(err, &opErr), errors.As(err, &se),
		strings.Contains(err.Error(), "server closed idle connection"),
		strings.Contains(err.Error(), "connection reset by peer"):
		return ambiguous
	}
	return fatal
}

// isGoAway reports whether err is HTTP/2's GoAwayError ("server sent GOAWAY
// and closed the connection"). net/http keeps the type internal, so it is
// matched by name.
func isGoAway(err error) bool {
	found := false
	walkErr(err, func(e error) {
		t := reflect.TypeOf(e)
		for t != nil && t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t != nil && t.Name() == "GoAwayError" && strings.HasSuffix(t.PkgPath(), "http2") {
			found = true
		}
	})
	return found || strings.Contains(err.Error(), "server sent GOAWAY and closed the connection")
}

func walkErr(err error, fn func(error)) {
	if err == nil {
		return
	}
	fn(err)
	switch u := err.(type) {
	case interface{ Unwrap() error }:
		walkErr(u.Unwrap(), fn)
	case interface{ Unwrap() []error }:
		for _, e := range u.Unwrap() {
			walkErr(e, fn)
		}
	}
}

// idempotentMethod reports whether sending a request twice does no more
// than sending it once.
func idempotentMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete:
		return true
	}
	return false
}

// newIdempotencyKey is a fresh Idempotency-Key for one create request.
func newIdempotencyKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "cli-" + hex.EncodeToString(b)
}

// maxBuffered is the largest body without GetBody that is kept in memory
// so it can be sent again (the MCP proxy's JSON bodies).
const maxBuffered = 8 << 20

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req, err := rewindable(req)
	if err != nil {
		return nil, err
	}
	key := req.Header.Get(api.IdempotencyHeader)
	var lastErr error
	for attempt := 0; ; attempt++ {
		r := req
		if attempt > 0 {
			if r, err = again(req); err != nil {
				return nil, lastErr
			}
		}
		res, err := t.base.RoundTrip(r)
		if err == nil {
			if key == "" || res.StatusCode != http.StatusConflict || res.Header.Get(api.IdempotencyStatusHeader) != "running" {
				return res, nil
			}
			// An earlier attempt with this key is still running on the box.
			res.Body.Close()
			if res, done, serr := t.settle(req, key); serr != nil || done {
				return res, serr
			}
			lastErr = errors.New("the box dropped an earlier attempt of this request")
		} else {
			lastErr = err
			kind := classify(err)
			if kind == fatal || req.Context().Err() != nil {
				return nil, err
			}
			if kind == ambiguous && !idempotentMethod(req.Method) {
				if key == "" {
					return nil, err // it may have run: sending it again could do it twice
				}
				// The box may have done it: ask before sending it again.
				res, done, serr := t.settle(req, key)
				if done {
					return res, nil
				}
				if serr != nil {
					return nil, err
				}
			}
		}
		if attempt >= len(t.waits) {
			return nil, lastErr
		}
		if t.note != nil {
			t.note(retryNote)
		}
		if err := sleepCtx(req.Context(), t.waits[attempt]); err != nil {
			return nil, lastErr
		}
	}
}

// rewindable gives a request whose body cannot be read again a GetBody, by
// keeping the body in memory, when it is small.
func rewindable(req *http.Request) (*http.Request, error) {
	if req.Body == nil || req.Body == http.NoBody || req.GetBody != nil || req.ContentLength <= 0 || req.ContentLength > maxBuffered {
		return req, nil
	}
	b, err := io.ReadAll(io.LimitReader(req.Body, maxBuffered+1))
	req.Body.Close()
	if err != nil {
		return nil, err
	}
	r := req.Clone(req.Context())
	r.Body = io.NopCloser(bytes.NewReader(b))
	r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
	return r, nil
}

// again is req ready to send once more, with its body from the start.
func again(req *http.Request) (*http.Request, error) {
	r := req.Clone(req.Context())
	if req.Body == nil || req.Body == http.NoBody {
		return r, nil
	}
	if req.GetBody == nil {
		return nil, errors.New("the request body cannot be sent again")
	}
	b, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	r.Body = b
	return r, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// keyState is what the box says became of a keyed request.
type keyState struct {
	Status         string          `json:"status"`
	Method         string          `json:"method"`
	Path           string          `json:"path"`
	ResponseStatus int             `json:"responseStatus"`
	Response       json.RawMessage `json:"response"`
}

// errNoLookup: the box cannot say what became of a keyed request (it is
// older than idempotency keys).
var errNoLookup = errors.New("the box cannot look up requests by key")

// settle asks the box what became of the request sent with key, waiting
// while it still runs. done: res is the answer it got. Not done and no
// error: it never ran (or failed), so it may be sent again with the key.
func (t *retryTransport) settle(req *http.Request, key string) (res *http.Response, done bool, err error) {
	if t.note != nil {
		t.note("the connection dropped: asking the box whether it got the request")
	}
	deadline := time.Now().Add(t.pollFor)
	misses := 0
	for {
		st, err := t.lookup(req, key)
		switch {
		case errors.Is(err, errNoLookup):
			return nil, false, err
		case err != nil:
			// The box is reloading, or restarting: ask again shortly.
			if misses++; misses > 30 {
				return nil, false, err
			}
		case st.Status == "done":
			if st.Method != req.Method || st.Path != req.URL.Path {
				return nil, false, fmt.Errorf("the box has key %s for another request (%s %s)", key, st.Method, st.Path)
			}
			return replay(req, st), true, nil
		case st.Status == "running":
			misses = 0
		default:
			return nil, false, nil
		}
		if time.Now().After(deadline) {
			return nil, false, errors.New("the box is still working on the request")
		}
		if err := sleepCtx(req.Context(), t.poll); err != nil {
			return nil, false, err
		}
	}
}

func (t *retryTransport) lookup(req *http.Request, key string) (*keyState, error) {
	u := url.URL{Scheme: req.URL.Scheme, Host: req.URL.Host, Path: "/v1/idempotency-keys/" + key}
	lr, err := http.NewRequestWithContext(req.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	for _, h := range []string{"Authorization", "User-Agent", "Cookie", api.SessionHeader} {
		if v := req.Header.Get(h); v != "" {
			lr.Header.Set(h, v)
		}
	}
	lr.Header.Set("Accept", "application/json")
	lr.Host = req.Host
	res, err := t.base.RoundTrip(lr)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	switch {
	case res.StatusCode == http.StatusOK:
		var st keyState
		if err := json.Unmarshal(raw, &st); err != nil {
			return nil, err
		}
		return &st, nil
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusMethodNotAllowed ||
		res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return nil, errNoLookup
	}
	return nil, fmt.Errorf("looking up the request: HTTP %d", res.StatusCode)
}

// replay is the stored answer of a keyed request, as a response to req.
func replay(req *http.Request, st *keyState) *http.Response {
	body := []byte(st.Response)
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set(api.IdempotencyStatusHeader, "replayed")
	return &http.Response{Status: fmt.Sprintf("%d %s", st.ResponseStatus, http.StatusText(st.ResponseStatus)), StatusCode: st.ResponseStatus,
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Header: h, Body: io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)), Request: req}
}

// unreachableError is a request that got no answer from the box.
type unreachableError struct {
	base string
	err  error
}

func (e *unreachableError) Error() string { return fmt.Sprintf("cannot reach %s: %v", e.base, e.err) }
func (e *unreachableError) Unwrap() error { return e.err }

func (c *client) unreachable(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return &unreachableError{c.base, err}
}

// isUnreachable reports whether err is a request that got no answer (as
// opposed to an answer saying no).
func isUnreachable(err error) bool {
	var u *unreachableError
	return errors.As(err, &u)
}

// patience lets a wait outlast dropped connections: a poll that cannot reach
// the box is simply made again, until the box has not answered for limit.
type patience struct {
	limit time.Duration
	since time.Time
}

// again reports whether to poll again after err (nil resets the clock).
func (p *patience) again(err error) bool {
	if err == nil {
		p.since = time.Time{}
		return false
	}
	if !isUnreachable(err) {
		return false
	}
	if p.since.IsZero() {
		p.since = time.Now()
	}
	return time.Since(p.since) < p.limit
}

// sendBody is a request body the client can send again.
type sendBody struct {
	first  io.Reader                     // the body
	reopen func() (io.ReadCloser, error) // the body from the start, for another attempt (nil: once only)
	size   int64                         // bytes, or -1 when not known
	key    string                        // Idempotency-Key, for a create
}

// fileBody sends f (size bytes) and can send it again: each attempt reads
// it through its own section, so attempts never share a file offset. count
// sees the bytes read (reset to 0 with -1 on a new attempt).
func fileBody(f io.ReaderAt, size int64, count func(int64)) *sendBody {
	open := func() io.Reader {
		r := io.Reader(io.NewSectionReader(f, 0, size))
		if count != nil {
			r = &countingReader{r: r, f: count}
		}
		return r
	}
	return &sendBody{first: open(), size: size, reopen: func() (io.ReadCloser, error) {
		if count != nil {
			count(-1)
		}
		return io.NopCloser(open()), nil
	}}
}
