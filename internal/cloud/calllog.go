package cloud

import (
	"net/http"
	"strings"
	"time"
)

// Call is one request made with a customer's Hetzner token. Every one is
// recorded and shown to the customer in their account.
type Call struct {
	At     time.Time
	Method string
	Path   string // "/v1/servers?label_selector=…": the URL without host
	Status int    // 0 when no answer came
	Ms     int64
	Error  string
}

// Recorder is a Transport that records every request before returning its
// answer. Recording must not fail the call, and the token itself is never
// recorded (it travels in the Authorization header, which is not kept).
type Recorder struct {
	Next   http.RoundTripper
	Record func(Call)
}

func (r *Recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	next := r.Next
	if next == nil {
		next = http.DefaultTransport
	}
	start := time.Now()
	res, err := next.RoundTrip(req)
	c := Call{At: start.UTC(), Method: req.Method, Path: CallPath(req.URL.Path, req.URL.RawQuery), Ms: time.Since(start).Milliseconds()}
	if res != nil {
		c.Status = res.StatusCode
	}
	if err != nil {
		c.Error = err.Error()
		if len(c.Error) > 300 {
			c.Error = c.Error[:300]
		}
	}
	if r.Record != nil {
		r.Record(c)
	}
	return res, err
}

// CallPath is the path as shown to the customer: the API path from /v1 on,
// with its query (label selectors, pagination: nothing secret).
func CallPath(path, query string) string {
	if i := strings.Index(path, "/v1/"); i > 0 {
		path = path[i:]
	}
	if query != "" {
		path += "?" + query
	}
	if len(path) > 400 {
		path = path[:400]
	}
	return path
}
