package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/shiptiffin/tiffin/internal/objstore"
)

// R2 is ShipTiffin's bucket for managed boxes' off-site backups, in
// Cloudflare R2: one folder per box (its id), reached by each box with
// temporary credentials limited to its folder. The worker mints them with
// Cloudflare's API (POST /accounts/{account_id}/r2/temp-access-credentials,
// https://developers.cloudflare.com/api/resources/r2/subresources/temporary_credentials/methods/create/):
// the bucket, the parent R2 API token's access key ID (the credentials can
// do no more than it), a permission, a lifetime of at most 7 days and the
// folder's prefix. The answer is an access key ID, a secret and a session
// token that S3 clients send with every request
// (https://developers.cloudflare.com/r2/api/s3/temporary-credentials/).
// No long-lived bucket key ever reaches a box, and the website never holds
// a Cloudflare credential.
type R2 struct {
	AccountID string
	// Token is a Cloudflare API token with Workers R2 Storage · Edit.
	Token string
	// ParentAccessKeyID is the access key ID of the R2 API token the
	// temporary credentials derive from (for an API token, its id).
	ParentAccessKeyID string
	Bucket            string
	// Endpoint is the bucket's S3 endpoint (default
	// https://<account>.r2.cloudflarestorage.com).
	Endpoint string
	// API is Cloudflare's API (default https://api.cloudflare.com/client/v4).
	API    string
	Client interface {
		Do(*http.Request) (*http.Response, error)
	}
	// CACert signs the S3 endpoint's certificate (tests).
	CACert string
}

// TempCredentials are R2 temporary access credentials.
type TempCredentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	ExpiresAt       time.Time
}

// MaxTempTTL is the longest lifetime Cloudflare gives temporary credentials.
const MaxTempTTL = 7 * 24 * time.Hour

// S3Endpoint is the bucket's S3 endpoint.
func (r *R2) S3Endpoint() string {
	if r.Endpoint != "" {
		return strings.TrimRight(r.Endpoint, "/")
	}
	return "https://" + r.AccountID + ".r2.cloudflarestorage.com"
}

var folderRE = regexp.MustCompile(`^box_[a-z0-9]{1,60}/$`)

// Folder is a box's folder in the bucket.
func Folder(boxID string) string { return boxID + "/" }

// Mint makes credentials for one box's folder (prefix "<box id>/"), with
// permission object-read-write or object-read-only, lasting ttl.
func (r *R2) Mint(ctx context.Context, prefix, permission string, ttl time.Duration) (*TempCredentials, error) {
	if !folderRE.MatchString(prefix) {
		return nil, fmt.Errorf("r2: %q is not a box's folder", prefix)
	}
	if permission != "object-read-write" && permission != "object-read-only" {
		return nil, fmt.Errorf("r2: permission %q: a box's credentials reach objects only", permission)
	}
	if ttl <= 0 || ttl > MaxTempTTL {
		return nil, fmt.Errorf("r2: a lifetime of %s is outside (0, 7 days]", ttl)
	}
	body, _ := json.Marshal(map[string]any{"bucket": r.Bucket, "parentAccessKeyId": r.ParentAccessKeyID, "permission": permission,
		"ttlSeconds": int(ttl / time.Second), "prefixes": []string{prefix}})
	api := r.API
	if api == "" {
		api = "https://api.cloudflare.com/client/v4"
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(api, "/")+"/accounts/"+r.AccountID+"/r2/temp-access-credentials", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.Token)
	req.Header.Set("Content-Type", "application/json")
	var cl interface {
		Do(*http.Request) (*http.Response, error)
	} = http.DefaultClient
	if r.Client != nil {
		cl = r.Client
	}
	start := time.Now()
	res, err := cl.Do(req)
	if err != nil {
		return nil, fmt.Errorf("r2 temporary credentials: %w", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var out struct {
		Success bool `json:"success"`
		Errors  []struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
		Result struct {
			AccessKeyID     string `json:"accessKeyId"`
			SecretAccessKey string `json:"secretAccessKey"`
			SessionToken    string `json:"sessionToken"`
		} `json:"result"`
	}
	_ = json.Unmarshal(raw, &out)
	if res.StatusCode/100 != 2 || !out.Success {
		msg := res.Status
		if len(out.Errors) > 0 {
			msg = fmt.Sprintf("%s: %d %s", res.Status, out.Errors[0].Code, out.Errors[0].Message)
		}
		return nil, fmt.Errorf("r2 temporary credentials: %s", firstLine(msg))
	}
	c := out.Result
	if c.AccessKeyID == "" || c.SecretAccessKey == "" || c.SessionToken == "" {
		return nil, errors.New("r2 temporary credentials: the answer holds no credentials")
	}
	// Counted from before the request: never later than Cloudflare's own expiry.
	return &TempCredentials{AccessKeyID: c.AccessKeyID, SecretAccessKey: c.SecretAccessKey, SessionToken: c.SessionToken, ExpiresAt: start.Add(ttl).UTC()}, nil
}

// EmptyFolder deletes everything in a box's folder, with credentials of
// its own limited to that folder, and says how many objects went.
func (r *R2) EmptyFolder(ctx context.Context, prefix string) (int, error) {
	creds, err := r.Mint(ctx, prefix, "object-read-write", time.Hour)
	if err != nil {
		return 0, err
	}
	st, err := objstore.New(objstore.Config{Endpoint: r.S3Endpoint(), Region: "auto", Bucket: r.Bucket, AccessKeyID: creds.AccessKeyID,
		SecretAccessKey: creds.SecretAccessKey, SessionToken: creds.SessionToken, CACert: r.CACert})
	if err != nil {
		return 0, err
	}
	return st.DeletePrefix(ctx, prefix)
}
