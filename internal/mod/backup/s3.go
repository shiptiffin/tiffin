package backup

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// objectStore is what off-box copies need from a bucket. s3Client is the
// real one; tests use a map.
type objectStore interface {
	Put(ctx context.Context, key string, body []byte) error
	// Get returns errNoObject when key does not exist, and an error for an
	// object of more than limit bytes (nothing past limit is read).
	Get(ctx context.Context, key string, limit int64) ([]byte, error)
	Delete(ctx context.Context, key string) error
	// List calls fn for every key under prefix, in key order.
	List(ctx context.Context, prefix string, fn func(key string, size int64) error) error
}

var errNoObject = errors.New("no such object")

// Every response body is bounded: in size (an answer bigger than its
// kind of object or listing can be is refused, never buffered), and in
// time (bodyStall without a byte fails it; the header wait is
// ResponseHeaderTimeout). A store that misbehaves or stalls cannot exhaust
// memory or hold a copy, restore or prune (and their locks) forever.
var bodyStall = 2 * time.Minute

const (
	maxSmallBody = 64 << 10 // error, PUT and DELETE answers
	maxListBody  = 16 << 20 // one page of a listing or a DeleteObjects answer
)

// s3Client is a small S3 client (SigV4, path- or host-style requests):
// enough for R2, AWS S3, Hetzner Object Storage, MinIO and versitygw.
type s3Client struct {
	base      *url.URL // scheme://host[:port]
	region    string
	bucket    string
	access    string
	secret    string
	hostStyle bool
	hc        *http.Client
	now       func() time.Time
}

func newS3(c *OffsiteConfig, secret string) (*s3Client, error) {
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return nil, err
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = time.Minute
	if c.CACert != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(c.CACert)) {
			return nil, errors.New("caCert holds no PEM certificate")
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &s3Client{base: &url.URL{Scheme: u.Scheme, Host: u.Host}, region: c.Region, bucket: c.Bucket,
		access: c.AccessKeyID, secret: secret, hostStyle: c.URIStyle == "host", hc: &http.Client{Transport: tr}, now: time.Now}, nil
}

// Bucket is the S3 client for code outside the package (the e2e tests):
// Put, Get, Delete, DeleteMany, DeletePrefix and List.
type Bucket = s3Client

// NewBucket is a client for in's endpoint and bucket, with the same
// defaults as a destination (in.Prefix is ignored).
func NewBucket(in OffsiteInput) (*Bucket, error) {
	c, err := normalize(in, nil)
	if err != nil {
		return nil, err
	}
	return newS3(c, in.SecretAccessKey)
}

// s3Error is an error response from the store.
type s3Error struct {
	Status  int
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

func (e *s3Error) Error() string {
	msg := strings.TrimSpace(e.Code + ": " + e.Message)
	if e.Code == "" {
		msg = http.StatusText(e.Status)
	}
	return fmt.Sprintf("%s (HTTP %d)", msg, e.Status)
}

func (c *s3Client) Put(ctx context.Context, key string, body []byte) error {
	resp, err := c.do(ctx, http.MethodPut, key, nil, body, maxSmallBody)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (c *s3Client) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	resp, err := c.do(ctx, http.MethodGet, key, nil, nil, limit)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", key, err)
	}
	return body, nil
}

func (c *s3Client) Delete(ctx context.Context, key string) error {
	resp, err := c.do(ctx, http.MethodDelete, key, nil, nil, maxSmallBody)
	if errors.Is(err, errNoObject) {
		return nil
	}
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// DeleteMany deletes keys, up to 1000 per request (DeleteObjects).
func (c *s3Client) DeleteMany(ctx context.Context, keys []string) error {
	for len(keys) > 0 {
		n := min(len(keys), 1000)
		var body bytes.Buffer
		body.WriteString(`<Delete xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Quiet>true</Quiet>`)
		for _, k := range keys[:n] {
			body.WriteString("<Object><Key>")
			_ = xml.EscapeText(&body, []byte(k))
			body.WriteString("</Key></Object>")
		}
		body.WriteString("</Delete>")
		resp, err := c.do(ctx, http.MethodPost, "", url.Values{"delete": {""}}, body.Bytes(), maxListBody)
		if err != nil {
			return err
		}
		// A 200 answer can still list keys that were not deleted.
		var out struct {
			Errors []struct {
				Key     string `xml:"Key"`
				Code    string `xml:"Code"`
				Message string `xml:"Message"`
			} `xml:"Error"`
		}
		err = xml.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("delete: %w", err)
		}
		if len(out.Errors) > 0 {
			e := out.Errors[0]
			return fmt.Errorf("delete %s: %s: %s (%d keys failed)", e.Key, e.Code, e.Message, len(out.Errors))
		}
		keys = keys[n:]
	}
	return nil
}

// CreateBucket creates the bucket (for tests against a local store).
func (c *s3Client) CreateBucket(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodPut, "", nil, nil, maxSmallBody)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// DeletePrefix deletes every object under prefix and returns how many.
func (c *s3Client) DeletePrefix(ctx context.Context, prefix string) (int, error) {
	var keys []string
	if err := c.List(ctx, prefix, func(k string, _ int64) error { keys = append(keys, k); return nil }); err != nil {
		return 0, err
	}
	return len(keys), c.DeleteMany(ctx, keys)
}

func (c *s3Client) List(ctx context.Context, prefix string, fn func(key string, size int64) error) error {
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}, "max-keys": {"1000"}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		resp, err := c.do(ctx, http.MethodGet, "", q, nil, maxListBody)
		if err != nil {
			return err
		}
		var out struct {
			Contents []struct {
				Key  string `xml:"Key"`
				Size int64  `xml:"Size"`
			} `xml:"Contents"`
			IsTruncated bool   `xml:"IsTruncated"`
			Next        string `xml:"NextContinuationToken"`
		}
		err = xml.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("list %s: %w", prefix, err)
		}
		for _, o := range out.Contents {
			if err := fn(o.Key, o.Size); err != nil {
				return err
			}
		}
		if !out.IsTruncated || out.Next == "" {
			return nil
		}
		token = out.Next
	}
}

// do sends a signed request, retrying network errors and 5xx/429 answers.
// A 404 on an object is errNoObject; other failures are *s3Error. The
// answer's body is bounded (limit bytes, bodyStall between reads).
func (c *s3Client) do(ctx context.Context, method, key string, q url.Values, body []byte, limit int64) (*http.Response, error) {
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * 500 * time.Millisecond):
			}
		}
		rctx, cancel := context.WithCancel(ctx)
		req, err := c.request(rctx, method, key, q, body)
		if err != nil {
			cancel()
			return nil, err
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			cancel()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			last = err
			continue
		}
		resp.Body = newBoundedBody(resp.Body, cancel, max(limit, maxSmallBody))
		if resp.StatusCode < 300 {
			if resp.ContentLength > limit {
				resp.Body.Close()
				return nil, fmt.Errorf("%s: the store answered with %d bytes, more than %d", strings.TrimSpace(key+" "+q.Encode()), resp.ContentLength, limit)
			}
			return resp, nil
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxSmallBody))
		resp.Body.Close()
		e := &s3Error{Status: resp.StatusCode}
		_ = xml.Unmarshal(raw, e)
		if resp.StatusCode == http.StatusNotFound && key != "" && e.Code != "NoSuchBucket" {
			return nil, errNoObject
		}
		last = e
		if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			break
		}
	}
	return nil, last
}

// boundedBody is a response body that fails past limit bytes, or when no
// byte arrives for bodyStall (it cancels the request). Close releases it.
type boundedBody struct {
	r      io.ReadCloser
	cancel context.CancelFunc
	stall  *time.Timer
	left   int64
	timed  atomic.Bool
}

var errBodyTooBig = errors.New("the store's answer is bigger than this kind of object can be")

func newBoundedBody(r io.ReadCloser, cancel context.CancelFunc, limit int64) *boundedBody {
	b := &boundedBody{r: r, cancel: cancel, left: limit}
	b.stall = time.AfterFunc(bodyStall, func() { b.timed.Store(true); cancel() })
	return b
}

func (b *boundedBody) Read(p []byte) (int, error) {
	// One byte past the limit tells a body of exactly limit bytes from a bigger one.
	if int64(len(p)) > b.left+1 {
		p = p[:b.left+1]
	}
	n, err := b.r.Read(p)
	if int64(n) > b.left {
		return 0, errBodyTooBig
	}
	b.left -= int64(n)
	if n > 0 {
		b.stall.Reset(bodyStall)
	}
	if err != nil && b.timed.Load() {
		err = fmt.Errorf("the store sent nothing for %s: %w", bodyStall, err)
	}
	return n, err
}

func (b *boundedBody) Close() error {
	b.stall.Stop()
	err := b.r.Close()
	b.cancel()
	return err
}

func (c *s3Client) request(ctx context.Context, method, key string, q url.Values, body []byte) (*http.Request, error) {
	u := *c.base
	host := u.Host
	path := "/" + c.bucket
	if c.hostStyle {
		host = c.bucket + "." + u.Host
		path = ""
	}
	if key != "" {
		path += "/" + s3Escape(key, true)
	} else if path == "" {
		path = "/"
	}
	u.Host, u.RawPath, u.Path = host, path, path
	if p, err := url.PathUnescape(path); err == nil {
		u.Path = p
	}
	u.RawQuery = canonicalQuery(q)
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.ContentLength = int64(len(body))
	if body == nil {
		req.Body = http.NoBody
	}
	if len(body) > 0 {
		// DeleteObjects requires it; on a PUT the store checks the body with it.
		m := md5.Sum(body)
		req.Header.Set("Content-MD5", base64.StdEncoding.EncodeToString(m[:]))
	}
	sum := sha256.Sum256(body)
	payload := hex.EncodeToString(sum[:])
	now := c.now().UTC()
	amzDate := now.Format("20060102T150405Z")
	day := now.Format("20060102")
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payload)
	signed := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	headers := map[string]string{"host": host, "x-amz-content-sha256": payload, "x-amz-date": amzDate}
	var ch strings.Builder
	for _, h := range signed {
		ch.WriteString(h + ":" + headers[h] + "\n")
	}
	canonical := strings.Join([]string{method, path, u.RawQuery, ch.String(), strings.Join(signed, ";"), payload}, "\n")
	scope := day + "/" + c.region + "/s3/aws4_request"
	cs := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(cs[:])
	k := hmacSHA([]byte("AWS4"+c.secret), day)
	k = hmacSHA(k, c.region)
	k = hmacSHA(k, "s3")
	k = hmacSHA(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA(k, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+c.access+"/"+scope+", SignedHeaders="+strings.Join(signed, ";")+", Signature="+sig)
	return req, nil
}

func hmacSHA(key []byte, s string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return m.Sum(nil)
}

// s3Escape URI-encodes s the way SigV4 wants: every byte except the
// unreserved characters, and '/' too unless keepSlash.
func s3Escape(s string, keepSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '.', c == '_', c == '~':
			b.WriteByte(c)
		case c == '/' && keepSlash:
			b.WriteByte(c)
		default:
			b.WriteString("%" + strings.ToUpper(strconv.FormatUint(uint64(c)>>4, 16)+strconv.FormatUint(uint64(c)&15, 16)))
		}
	}
	return b.String()
}

func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, s3Escape(k, false)+"="+s3Escape(v, false))
		}
	}
	return strings.Join(parts, "&")
}
