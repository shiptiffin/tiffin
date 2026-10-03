package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// gateway drives the local versitygw: its admin CLI for accounts and bucket
// owners, and signed S3 calls (as the root account) for everything else.
type gateway struct {
	bin    string // versitygw binary
	addr   string // S3 listen address, e.g. 127.0.0.1:7480
	root   Creds
	region string
	client *http.Client
	now    func() time.Time
}

func (g *gateway) endpoint() string { return "http://" + g.addr }

// s3Error is an S3 XML error response.
type s3Error struct {
	Status  int    `xml:"-"`
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

func (e *s3Error) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("storage gateway: HTTP %d", e.Status)
	}
	return "storage gateway: " + e.Code + ": " + e.Message
}

func isCode(err error, code string) bool {
	var se *s3Error
	return errors.As(err, &se) && se.Code == code
}

// admin runs `versitygw admin <args>` against the local gateway.
func (g *gateway) admin(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, g.bin, append([]string{"admin"}, args...)...)
	cmd.Env = []string{
		"ADMIN_ACCESS_KEY=" + g.root.AccessKey,
		"ADMIN_SECRET_KEY=" + g.root.Secret,
		"ADMIN_ENDPOINT_URL=" + g.endpoint(),
		"ADMIN_REGION=" + g.region,
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		// "2026/10/02 17:58:02 api error XAdminUserExists: ..." → the code.
		if i := strings.Index(msg, "api error "); i >= 0 {
			rest := msg[i+len("api error "):]
			code, detail, _ := strings.Cut(rest, ":")
			return "", &s3Error{Code: strings.TrimSpace(code), Message: strings.TrimSpace(detail)}
		}
		return "", fmt.Errorf("versitygw admin %s: %v: %s", args[0], err, msg)
	}
	return string(out), nil
}

// ensureUser creates the account, or resets its secret if it already exists.
func (g *gateway) ensureUser(ctx context.Context, c Creds) error {
	_, err := g.admin(ctx, "create-user", "--access", c.AccessKey, "--secret", c.Secret, "--role", "user")
	if isCode(err, "XAdminUserExists") {
		_, err = g.admin(ctx, "update-user", "--access", c.AccessKey, "--secret", c.Secret, "--role", "user")
	}
	return err
}

func (g *gateway) deleteUser(ctx context.Context, access string) error {
	_, err := g.admin(ctx, "delete-user", "--access", access)
	if isCode(err, "XAdminUserNotFound") {
		return nil
	}
	return err
}

// createBucket creates a bucket owned by owner; an existing bucket is
// (re)assigned to owner.
func (g *gateway) createBucket(ctx context.Context, bucket, owner string) error {
	_, err := g.admin(ctx, "create-bucket", "--owner", owner, "--bucket", bucket)
	if isCode(err, "BucketAlreadyOwnedByYou") || isCode(err, "BucketAlreadyExists") {
		return g.changeOwner(ctx, bucket, owner)
	}
	return err
}

func (g *gateway) changeOwner(ctx context.Context, bucket, owner string) error {
	_, err := g.admin(ctx, "change-bucket-owner", "--bucket", bucket, "--owner", owner)
	return err
}

// do performs a root-signed S3 request against the gateway.
func (g *gateway) do(ctx context.Context, method, bucket, key string, q url.Values, body []byte, hdr http.Header) (*http.Response, error) {
	u := g.endpoint() + objectPath(bucket, key)
	if len(q) > 0 {
		u += "?" + canonicalQuery(q)
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	for k, vs := range hdr {
		req.Header[k] = vs
	}
	sum := emptySHA256
	if body != nil {
		sum = sha256Hex(body)
	}
	now := time.Now
	if g.now != nil {
		now = g.now
	}
	signRequest(req, g.root, g.region, sum, now())
	res, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("storage gateway unreachable at %s: %w", g.addr, err)
	}
	if res.StatusCode >= 300 && res.StatusCode != http.StatusNotModified {
		defer res.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		se := &s3Error{Status: res.StatusCode}
		_ = xml.Unmarshal(raw, se)
		return nil, se
	}
	return res, nil
}

func (g *gateway) call(ctx context.Context, method, bucket, key string, q url.Values, body []byte, hdr http.Header) error {
	res, err := g.do(ctx, method, bucket, key, q, body, hdr)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, res.Body)
	return res.Body.Close()
}

// healthy checks the gateway answers its health endpoint.
func (g *gateway) healthy(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, g.endpoint()+healthPath, nil)
	res, err := g.client.Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("health HTTP %d", res.StatusCode)
	}
	return nil
}

// waitHealthy waits up to d for the gateway to answer.
func (g *gateway) waitHealthy(ctx context.Context, d time.Duration) error {
	deadline := time.Now().Add(d)
	for {
		err := g.healthy(ctx)
		if err == nil || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// publicPolicy lets anyone read objects and keeps full access for the owner
// (versitygw evaluates a bucket policy instead of the owner's implicit access).
func publicPolicy(bucket, owner string) []byte {
	arn := "arn:aws:s3:::" + bucket
	p := map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{
			{"Sid": "TiffinPublicRead", "Effect": "Allow", "Principal": "*", "Action": []string{"s3:GetObject"}, "Resource": []string{arn + "/*"}},
			{"Sid": "TiffinOwner", "Effect": "Allow", "Principal": map[string]any{"AWS": []string{owner}}, "Action": []string{"s3:*"}, "Resource": []string{arn, arn + "/*"}},
		},
	}
	b, _ := json.Marshal(p)
	return b
}

func (g *gateway) setPublic(ctx context.Context, bucket, owner string, public bool) error {
	q := url.Values{"policy": {""}}
	if public {
		return g.call(ctx, http.MethodPut, bucket, "", q, publicPolicy(bucket, owner), http.Header{"Content-Type": {"application/json"}})
	}
	err := g.call(ctx, http.MethodDelete, bucket, "", q, nil, nil)
	if isCode(err, "NoSuchBucketPolicy") {
		return nil
	}
	return err
}

// Object is one object in a listing.
type Object struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	ETag         string    `json:"etag"`
	LastModified time.Time `json:"lastModified"`
}

type listResult struct {
	Objects   []Object
	Prefixes  []string
	Next      string
	Truncated bool
}

func (g *gateway) list(ctx context.Context, bucket, prefix, delimiter, token string, max int) (*listResult, error) {
	q := url.Values{"list-type": {"2"}, "max-keys": {strconv.Itoa(max)}}
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	if delimiter != "" {
		q.Set("delimiter", delimiter)
	}
	if token != "" {
		q.Set("continuation-token", token)
	}
	res, err := g.do(ctx, http.MethodGet, bucket, "", q, nil, nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var v struct {
		Contents []struct {
			Key          string `xml:"Key"`
			Size         int64  `xml:"Size"`
			ETag         string `xml:"ETag"`
			LastModified string `xml:"LastModified"`
		} `xml:"Contents"`
		CommonPrefixes []struct {
			Prefix string `xml:"Prefix"`
		} `xml:"CommonPrefixes"`
		IsTruncated           bool   `xml:"IsTruncated"`
		NextContinuationToken string `xml:"NextContinuationToken"`
	}
	if err := xml.NewDecoder(res.Body).Decode(&v); err != nil {
		return nil, fmt.Errorf("storage gateway: bad listing: %w", err)
	}
	out := &listResult{Objects: []Object{}, Prefixes: []string{}, Truncated: v.IsTruncated, Next: v.NextContinuationToken}
	for _, c := range v.Contents {
		t, _ := time.Parse(time.RFC3339Nano, c.LastModified)
		out.Objects = append(out.Objects, Object{Key: c.Key, Size: c.Size, ETag: strings.Trim(c.ETag, `"`), LastModified: t})
	}
	for _, p := range v.CommonPrefixes {
		out.Prefixes = append(out.Prefixes, p.Prefix)
	}
	return out, nil
}

func (g *gateway) putObject(ctx context.Context, bucket, key, contentType string, body []byte) (string, error) {
	h := http.Header{}
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	res, err := g.do(ctx, http.MethodPut, bucket, key, nil, body, h)
	if err != nil {
		return "", err
	}
	res.Body.Close()
	return strings.Trim(res.Header.Get("ETag"), `"`), nil
}

func (g *gateway) deleteObject(ctx context.Context, bucket, key string) error {
	return g.call(ctx, http.MethodDelete, bucket, key, nil, nil, nil)
}

func (g *gateway) headObject(ctx context.Context, bucket, key string) (http.Header, error) {
	res, err := g.do(ctx, http.MethodHead, bucket, key, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	res.Body.Close()
	return res.Header, nil
}
