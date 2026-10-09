// Package objstoretest is an in-memory S3 bucket over HTTPS for tests: it
// checks every request's SigV4 signature itself (not with objstore's code),
// and knows temporary credentials as Cloudflare R2 hands them out: a
// session token that must come with (and be signed into) every request,
// an expiry, and key prefixes the credentials are limited to.
package objstoretest

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Cred is a key the bucket accepts.
type Cred struct {
	AccessKeyID     string
	SecretAccessKey string
	// SessionToken, when set, must come with every request, signed.
	SessionToken string
	// Expires, when set, is when the key stops working.
	Expires time.Time
	// Prefixes, when set, are the only keys the credentials reach
	// (objects whose keys start with one, listings of a prefix inside one).
	Prefixes []string
	ReadOnly bool
}

// Fake is one bucket.
type Fake struct {
	URL    string // https://127.0.0.1:port
	Bucket string
	srv    *httptest.Server
	mu     sync.Mutex
	creds  map[string]Cred
	objs   map[string][]byte
	// Requests is every request: "METHOD /path?query -> status".
	Requests []string
	now      func() time.Time
}

// New starts a bucket.
func New(bucket string) *Fake {
	f := &Fake{Bucket: bucket, creds: map[string]Cred{}, objs: map[string][]byte{}, now: time.Now}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	f.URL = f.srv.URL
	return f
}

// Close stops it.
func (f *Fake) Close() { f.srv.Close() }

// Client trusts the bucket's certificate.
func (f *Fake) Client() *http.Client { return f.srv.Client() }

// CAPEM is the bucket's certificate (it signs itself), for a client's CA list.
func (f *Fake) CAPEM() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.srv.Certificate().Raw}))
}

// SetNow sets the bucket's clock.
func (f *Fake) SetNow(now func() time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = now
}

// Allow adds (or replaces) a key.
func (f *Fake) Allow(c Cred) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creds[c.AccessKeyID] = c
}

// Keys lists the objects under prefix, sorted.
func (f *Fake) Keys(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k := range f.objs {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Seen is how many requests the bucket has answered.
func (f *Fake) Seen() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Requests)
}

// Put stores an object directly (no request).
func (f *Fake) Put(key string, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objs[key] = body
}

type s3err struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
	Message string   `xml:"Message"`
}

func fail(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_ = xml.NewEncoder(w).Encode(s3err{Code: code, Message: msg})
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	rec := &statusWriter{ResponseWriter: w, status: 200}
	f.handle(rec, r)
	f.mu.Lock()
	f.Requests = append(f.Requests, fmt.Sprintf("%s %s -> %d", r.Method, r.URL.RequestURI(), rec.status))
	f.mu.Unlock()
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) { s.status = code; s.ResponseWriter.WriteHeader(code) }

func (f *Fake) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	cred, code, msg := f.authenticate(r, body)
	if code != "" {
		status := http.StatusForbidden
		if code == "ExpiredToken" {
			status = http.StatusBadRequest
		}
		fail(w, status, code, msg)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	bucket, key, _ := strings.Cut(path, "/")
	if bucket != f.Bucket {
		fail(w, http.StatusNotFound, "NoSuchBucket", "no bucket "+bucket)
		return
	}
	allowed := func(k string) bool {
		if len(cred.Prefixes) == 0 {
			return true
		}
		return slices.ContainsFunc(cred.Prefixes, func(p string) bool { return strings.HasPrefix(k, p) })
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	q := r.URL.Query()
	switch {
	case key == "" && r.Method == http.MethodGet && q.Get("list-type") == "2":
		prefix := q.Get("prefix")
		if !allowed(prefix) {
			fail(w, http.StatusForbidden, "AccessDenied", "listing "+prefix+" is outside the credentials' prefixes")
			return
		}
		var keys []string
		for k := range f.objs {
			if strings.HasPrefix(k, prefix) && k > q.Get("continuation-token") {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		type obj struct {
			Key  string `xml:"Key"`
			Size int    `xml:"Size"`
		}
		out := struct {
			XMLName     xml.Name `xml:"ListBucketResult"`
			Contents    []obj    `xml:"Contents"`
			IsTruncated bool     `xml:"IsTruncated"`
			Next        string   `xml:"NextContinuationToken,omitempty"`
		}{}
		for i, k := range keys {
			if i == 1000 {
				out.IsTruncated, out.Next = true, keys[i-1]
				break
			}
			out.Contents = append(out.Contents, obj{Key: k, Size: len(f.objs[k])})
		}
		_ = xml.NewEncoder(w).Encode(out)
	case key == "" && r.Method == http.MethodPost && q.Has("delete"):
		if cred.ReadOnly {
			fail(w, http.StatusForbidden, "AccessDenied", "read-only credentials")
			return
		}
		var in struct {
			Objects []struct{ Key string } `xml:"Object"`
		}
		if err := xml.Unmarshal(body, &in); err != nil {
			fail(w, http.StatusBadRequest, "MalformedXML", err.Error())
			return
		}
		var errs strings.Builder
		for _, o := range in.Objects {
			if !allowed(o.Key) {
				errs.WriteString("<Error><Key>" + o.Key + "</Key><Code>AccessDenied</Code><Message>outside the prefixes</Message></Error>")
				continue
			}
			delete(f.objs, o.Key)
		}
		fmt.Fprint(w, "<DeleteResult>"+errs.String()+"</DeleteResult>")
	case key == "":
		fail(w, http.StatusForbidden, "AccessDenied", "bucket-level "+r.Method+" is not allowed")
	case !allowed(key):
		fail(w, http.StatusForbidden, "AccessDenied", key+" is outside the credentials' prefixes")
	case r.Method == http.MethodPut:
		if cred.ReadOnly {
			fail(w, http.StatusForbidden, "AccessDenied", "read-only credentials")
			return
		}
		f.objs[key] = body
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		b, ok := f.objs[key]
		if !ok {
			fail(w, http.StatusNotFound, "NoSuchKey", key)
			return
		}
		_, _ = w.Write(b)
	case r.Method == http.MethodDelete:
		if cred.ReadOnly {
			fail(w, http.StatusForbidden, "AccessDenied", "read-only credentials")
			return
		}
		delete(f.objs, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		fail(w, http.StatusMethodNotAllowed, "MethodNotAllowed", r.Method)
	}
}

// authenticate checks the request's SigV4 signature and its credentials.
// code is "" when they check out.
func (f *Fake) authenticate(r *http.Request, body []byte) (c Cred, code, msg string) {
	auth := r.Header.Get("Authorization")
	rest, ok := strings.CutPrefix(auth, "AWS4-HMAC-SHA256 ")
	if !ok {
		return c, "AccessDenied", "no SigV4 authorization"
	}
	parts := map[string]string{}
	for _, p := range strings.Split(rest, ", ") {
		k, v, _ := strings.Cut(p, "=")
		parts[k] = v
	}
	scope := strings.Split(parts["Credential"], "/")
	if len(scope) != 5 || scope[3] != "s3" || scope[4] != "aws4_request" {
		return c, "AuthorizationHeaderMalformed", parts["Credential"]
	}
	f.mu.Lock()
	c, ok = f.creds[scope[0]]
	now := f.now()
	f.mu.Unlock()
	if !ok {
		return c, "InvalidAccessKeyId", scope[0]
	}
	signed := strings.Split(parts["SignedHeaders"], ";")
	if c.SessionToken != "" {
		if r.Header.Get("X-Amz-Security-Token") != c.SessionToken || !slices.Contains(signed, "x-amz-security-token") {
			return c, "InvalidToken", "temporary credentials need their session token, signed"
		}
	}
	if !c.Expires.IsZero() && now.After(c.Expires) {
		return c, "ExpiredToken", "the credentials expired at " + c.Expires.Format(time.RFC3339)
	}
	sum := sha256.Sum256(body)
	if r.Header.Get("X-Amz-Content-Sha256") != hex.EncodeToString(sum[:]) {
		return c, "XAmzContentSHA256Mismatch", "payload hash"
	}
	var ch strings.Builder
	for _, h := range signed {
		v := r.Header.Get(h)
		if h == "host" {
			v = r.Host
		}
		ch.WriteString(h + ":" + strings.TrimSpace(v) + "\n")
	}
	canonical := strings.Join([]string{r.Method, r.URL.EscapedPath(), query(r.URL.Query()), ch.String(), parts["SignedHeaders"], r.Header.Get("X-Amz-Content-Sha256")}, "\n")
	amzDate := r.Header.Get("X-Amz-Date")
	cs := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + strings.Join(scope[1:], "/") + "\n" + hex.EncodeToString(cs[:])
	k := mac([]byte("AWS4"+c.SecretAccessKey), scope[1])
	k = mac(k, scope[2])
	k = mac(k, "s3")
	k = mac(k, "aws4_request")
	if !hmac.Equal([]byte(hex.EncodeToString(mac(k, toSign))), []byte(parts["Signature"])) {
		return c, "SignatureDoesNotMatch", "signature"
	}
	return c, "", ""
}

func mac(key []byte, s string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return m.Sum(nil)
}

func query(q url.Values) string {
	var parts []string
	for k, vs := range q {
		for _, v := range vs {
			parts = append(parts, esc(k)+"="+esc(v))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "&")
}

func esc(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(url.QueryEscape(s), "+", "%20"), "%7E", "~")
}
