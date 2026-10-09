package storage

import (
	"context"
	"crypto/hmac"
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/shiptiffin/tiffin/internal/platform"
)

// Signed file URLs let a private bucket's object (or a resized copy of it)
// be read at files.<domain>/<project>/<bucket>/<key>?exp=<unix>&sig=<sig>.
// The signature covers the path and the expiry, not w, q and f, so an image
// loader can add those to a signed URL. The key is derived from the
// project's S3 secret, which its apps already hold (@shiptiffin/sdk's
// signedUrl computes the same).

// filesSigningKey derives the files signing key from an S3 secret.
func filesSigningKey(secret string) []byte {
	return hmacSHA256([]byte(secret), "tiffin-files-v1")
}

// FilesSignature signs project/bucket/key until exp (unix seconds).
func FilesSignature(secret, project, bucket, key string, exp int64) string {
	mac := hmacSHA256(filesSigningKey(secret), project+"/"+bucket+"/"+key+"\n"+strconv.FormatInt(exp, 10))
	return base64.RawURLEncoding.EncodeToString(mac)
}

// filesKeys caches each project's S3 secret for checking signed URLs.
type filesKeys struct {
	mu sync.Mutex
	m  map[string]filesKey
}

type filesKey struct {
	secret string
	at     time.Time
}

func (k *filesKeys) secret(ctx context.Context, p *platform.Platform, project string) (string, bool) {
	k.mu.Lock()
	c, ok := k.m[project]
	k.mu.Unlock()
	if ok && time.Since(c.at) < time.Minute {
		return c.secret, c.secret != ""
	}
	creds, found, err := credsFor(ctx, p, project, false)
	if err != nil {
		return "", false
	}
	if !found {
		creds.Secret = ""
	}
	k.mu.Lock()
	if k.m == nil {
		k.m = map[string]filesKey{}
	}
	k.m[project] = filesKey{secret: creds.Secret, at: time.Now()}
	k.mu.Unlock()
	return creds.Secret, creds.Secret != ""
}

// checkSigned verifies a signed file URL. It returns the seconds left, or
// an error saying what is wrong.
func (f *frontServer) checkSigned(ctx context.Context, project, bucket, key string, q url.Values, now time.Time) (int64, error) {
	exp, err := strconv.ParseInt(q.Get("exp"), 10, 64)
	if err != nil || q.Get("sig") == "" {
		return 0, fmt.Errorf("this file is not public")
	}
	left := exp - now.Unix()
	if left <= 0 {
		return 0, fmt.Errorf("this signed link has expired")
	}
	secret, ok := f.keys.secret(ctx, f.p, project)
	if !ok {
		return 0, fmt.Errorf("this file is not public")
	}
	want := FilesSignature(secret, project, bucket, key, exp)
	if !hmac.Equal([]byte(want), []byte(q.Get("sig"))) {
		return 0, fmt.Errorf("the link's signature does not match")
	}
	return left, nil
}
