package backup

import (
	"context"

	"github.com/btahir/tiffin/internal/objstore"
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

var (
	errNoObject   = objstore.ErrNoObject
	errBodyTooBig = objstore.ErrBodyTooBig
)

// s3Client is the S3 client (internal/objstore).
type s3Client = objstore.Client

// newS3 is a client for destination c with a long-lived key.
func newS3(c *OffsiteConfig, secret string) (*s3Client, error) {
	return newS3For(c, &offsiteSecrets{SecretAccessKey: secret})
}

// newS3For is a client for destination c with its secrets (and, for
// temporary credentials, their session token).
func newS3For(c *OffsiteConfig, s *offsiteSecrets) (*s3Client, error) {
	return objstore.New(objstore.Config{Endpoint: c.Endpoint, Region: c.Region, Bucket: c.Bucket, AccessKeyID: c.AccessKeyID,
		SecretAccessKey: s.SecretAccessKey, SessionToken: s.SessionToken, HostStyle: c.URIStyle == "host", CACert: c.CACert})
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
