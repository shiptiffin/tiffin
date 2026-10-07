package storage

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Creds is an S3 access key pair.
type Creds struct {
	AccessKey string `json:"accessKey"`
	Secret    string `json:"secret"`
}

const (
	sigAlgorithm    = "AWS4-HMAC-SHA256"
	amzDateFormat   = "20060102T150405Z"
	unsignedPayload = "UNSIGNED-PAYLOAD"
	emptySHA256     = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

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
			b.WriteString("%" + strings.ToUpper(hex.EncodeToString([]byte{c})))
		}
	}
	return b.String()
}

// objectPath is the path-style request path for a bucket and key.
func objectPath(bucket, key string) string {
	if key == "" {
		return "/" + bucket
	}
	return "/" + bucket + "/" + s3Escape(key, true)
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

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func signingKey(secret, date, region string) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), date)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, "s3")
	return hmacSHA256(k, "aws4_request")
}

// signature computes the SigV4 signature over a canonical request.
func signature(c Creds, region string, now time.Time, canonicalRequest string) string {
	date := now.UTC().Format("20060102")
	scope := date + "/" + region + "/s3/aws4_request"
	sts := sigAlgorithm + "\n" + now.UTC().Format(amzDateFormat) + "\n" + scope + "\n" + sha256Hex([]byte(canonicalRequest))
	return hex.EncodeToString(hmacSHA256(signingKey(c.Secret, date, region), sts))
}

// Presign returns a presigned URL for method on endpoint (scheme://host[:port])
// + path-style bucket/key, valid for expires (1s–7 days).
func Presign(method, endpoint, bucket, key string, c Creds, region string, expires time.Duration, now time.Time) (string, error) {
	return PresignWith(method, endpoint, bucket, key, nil, nil, c, region, expires, now)
}

// PresignWith is Presign with extra query parameters (an S3 subresource
// such as uploadId, or the front's x-tiffin-* upload limits) and extra
// signed headers (content-type, content-length). The signature covers all
// of them, so whoever holds the URL cannot change them.
func PresignWith(method, endpoint, bucket, key string, extra url.Values, headers map[string]string, c Creds, region string, expires time.Duration, now time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	path := objectPath(bucket, key)
	date := now.UTC().Format("20060102")
	q := url.Values{}
	for k, vs := range extra {
		q[k] = append([]string(nil), vs...)
	}
	hs := map[string]string{"host": u.Host}
	for k, v := range headers {
		hs[strings.ToLower(k)] = strings.TrimSpace(v)
	}
	names := make([]string, 0, len(hs))
	for k := range hs {
		names = append(names, k)
	}
	sort.Strings(names)
	var ch strings.Builder
	for _, k := range names {
		ch.WriteString(k + ":" + hs[k] + "\n")
	}
	signed := strings.Join(names, ";")
	q.Set("X-Amz-Algorithm", sigAlgorithm)
	q.Set("X-Amz-Credential", c.AccessKey+"/"+date+"/"+region+"/s3/aws4_request")
	q.Set("X-Amz-Date", now.UTC().Format(amzDateFormat))
	q.Set("X-Amz-Expires", strconv.Itoa(int(expires/time.Second)))
	q.Set("X-Amz-SignedHeaders", signed)
	cr := method + "\n" + path + "\n" + canonicalQuery(q) + "\n" + ch.String() + "\n" + signed + "\n" + unsignedPayload
	q.Set("X-Amz-Signature", signature(c, region, now, cr))
	return u.Scheme + "://" + u.Host + path + "?" + canonicalQuery(q), nil
}

// signRequest adds SigV4 header authentication to req. payloadHash is the
// hex SHA-256 of the body (or UNSIGNED-PAYLOAD). It signs host, every
// x-amz-* header and range, content-type and content-md5 when present.
func signRequest(req *http.Request, c Creds, region, payloadHash string, now time.Time) {
	req.Header.Set("X-Amz-Date", now.UTC().Format(amzDateFormat))
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	headers := map[string]string{"host": host}
	for k, vs := range req.Header {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-amz-") || lk == "range" || lk == "content-type" || lk == "content-md5" {
			headers[lk] = strings.TrimSpace(strings.Join(vs, ","))
		}
	}
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var ch strings.Builder
	for _, k := range names {
		ch.WriteString(k + ":" + headers[k] + "\n")
	}
	signed := strings.Join(names, ";")
	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	cr := req.Method + "\n" + path + "\n" + canonicalQuery(req.URL.Query()) + "\n" + ch.String() + "\n" + signed + "\n" + payloadHash
	date := now.UTC().Format("20060102")
	req.Header.Set("Authorization", sigAlgorithm+" Credential="+c.AccessKey+"/"+date+"/"+region+"/s3/aws4_request, SignedHeaders="+signed+", Signature="+signature(c, region, now, cr))
}

// accessKeyOf extracts the access key id from a signed S3 request (header
// or presigned query), or "" for anonymous requests.
func accessKeyOf(r *http.Request) string {
	cred := ""
	if a := r.Header.Get("Authorization"); strings.HasPrefix(a, sigAlgorithm) {
		if i := strings.Index(a, "Credential="); i >= 0 {
			cred = a[i+len("Credential="):]
		}
	} else if strings.HasPrefix(a, "AWS ") { // SigV2
		cred = strings.TrimPrefix(a, "AWS ")
		if i := strings.IndexByte(cred, ':'); i >= 0 {
			return cred[:i]
		}
	} else {
		cred = r.URL.Query().Get("X-Amz-Credential")
	}
	if i := strings.IndexByte(cred, '/'); i >= 0 {
		return cred[:i]
	}
	return ""
}

// verifySigV4 reports whether r carries a valid SigV4 signature by c: in
// its Authorization header (made within 15 minutes) or as a presigned URL
// (not expired). The front uses it before acting as root for a request.
func verifySigV4(r *http.Request, c Creds, now time.Time) bool {
	q := r.URL.Query()
	var cred, signedHeaders, sig, amzDate, payload string
	if alg := q.Get("X-Amz-Algorithm"); alg != "" {
		if alg != sigAlgorithm {
			return false
		}
		cred, signedHeaders, sig, amzDate = q.Get("X-Amz-Credential"), q.Get("X-Amz-SignedHeaders"), q.Get("X-Amz-Signature"), q.Get("X-Amz-Date")
		payload = unsignedPayload
	} else {
		rest, ok := strings.CutPrefix(r.Header.Get("Authorization"), sigAlgorithm+" ")
		if !ok {
			return false
		}
		for _, part := range strings.Split(rest, ",") {
			k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
			switch k {
			case "Credential":
				cred = v
			case "SignedHeaders":
				signedHeaders = v
			case "Signature":
				sig = v
			}
		}
		amzDate, payload = r.Header.Get("X-Amz-Date"), r.Header.Get("X-Amz-Content-Sha256")
		if payload == "" {
			return false
		}
	}
	t, err := time.Parse(amzDateFormat, amzDate)
	if err != nil || sig == "" || signedHeaders == "" {
		return false
	}
	scope := strings.Split(cred, "/")
	if len(scope) != 5 || scope[0] != c.AccessKey || scope[1] != t.Format("20060102") || scope[3] != "s3" || scope[4] != "aws4_request" {
		return false
	}
	if q.Has("X-Amz-Algorithm") {
		exp, err := strconv.Atoi(q.Get("X-Amz-Expires"))
		if err != nil || exp < 1 || exp > 7*24*3600 || now.After(t.Add(time.Duration(exp)*time.Second)) || t.After(now.Add(15*time.Minute)) {
			return false
		}
	} else if d := now.Sub(t); d > 15*time.Minute || d < -15*time.Minute {
		return false
	}
	var ch strings.Builder
	for _, h := range strings.Split(signedHeaders, ";") {
		var v string
		switch h {
		case "host":
			v = r.Host
		case "content-length": // net/http moves it out of the header map
			if r.ContentLength >= 0 {
				v = strconv.FormatInt(r.ContentLength, 10)
			}
		default:
			v = strings.Join(r.Header.Values(h), ",")
		}
		ch.WriteString(h + ":" + strings.Join(strings.Fields(v), " ") + "\n")
	}
	cq := url.Values{}
	for k, vs := range q {
		if k != "X-Amz-Signature" {
			cq[k] = vs
		}
	}
	// The path as the client sent (and signed) it: S3 does not normalise it.
	path, _, _ := strings.Cut(r.RequestURI, "?")
	if _, rest, ok := strings.Cut(path, "://"); ok { // absolute form
		path = "/"
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			path = rest[i:]
		}
	}
	if path == "" {
		path = r.URL.EscapedPath()
	}
	cr := r.Method + "\n" + path + "\n" + canonicalQuery(cq) + "\n" + ch.String() + "\n" + signedHeaders + "\n" + payload
	return hmac.Equal([]byte(signature(c, scope[2], t, cr)), []byte(sig))
}
