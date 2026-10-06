package storage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
)

// MaxAPIUpload is the largest object the JSON and form upload operations
// take. Bigger files go straight to S3 with a presigned PUT URL.
const MaxAPIUpload = 10 << 20

// BucketInfo is one bucket with its measured usage.
type BucketInfo struct {
	Name      string `json:"name" doc:"Bucket name in tiffin.config.ts"`
	S3Name    string `json:"s3Name" doc:"The S3 bucket name apps use (<project>-<name>), also in env S3_BUCKET_<NAME>"`
	Public    bool   `json:"public" doc:"Readable by anyone at publicURL without a signature"`
	PublicURL string `json:"publicUrl,omitempty" doc:"Base URL of public files: <publicUrl>/<key>"`
	Bytes     int64  `json:"bytes"`
	Objects   int64  `json:"objects"`
	State     string `json:"state" enum:"ready,pending" doc:"pending: applied but not created on the box yet"`
}

// Info is a project's storage overview.
type Info struct {
	Project          string       `json:"project"`
	Endpoint         string       `json:"endpoint" doc:"Public S3 endpoint (path-style), for browsers and tools off the box"`
	InternalEndpoint string       `json:"internalEndpoint" doc:"S3 endpoint apps on the box use (env S3_ENDPOINT)"`
	FilesURL         string       `json:"filesUrl" doc:"Public files base: <filesUrl>/<bucket>/<key> (public buckets only)"`
	Region           string       `json:"region"`
	AccessKeyID      string       `json:"accessKeyId,omitempty" doc:"The project's S3 access key id (the secret is in the app env, or GET .../storage/credentials)"`
	UsedBytes        int64        `json:"usedBytes" doc:"What the storage limit (quotaBytes) counts: databaseBytes plus filesBytes"`
	FilesBytes       int64        `json:"filesBytes" doc:"Bucket files"`
	DatabaseBytes    int64        `json:"databaseBytes" doc:"The project's databases (branches included), as the disk guard last measured them"`
	QuotaBytes       int64        `json:"quotaBytes" doc:"The project's storage limit, database and files together; 0 means none (the default)"`
	QuotaSource      string       `json:"quotaSource" enum:"project,box-default"`
	ReadOnly         string       `json:"readOnly,omitempty" doc:"Set while uploads are refused: which limit was reached and how to fix it"`
	MeasuredAt       time.Time    `json:"measuredAt" doc:"When usage was last measured (every minute, and after changes)"`
	Buckets          []BucketInfo `json:"buckets"`
}

// ObjectList is one page of a bucket listing.
type ObjectList struct {
	Bucket     string   `json:"bucket"`
	Prefix     string   `json:"prefix,omitempty"`
	Objects    []Object `json:"objects"`
	Prefixes   []string `json:"prefixes" doc:"\"Folders\" under prefix, when delimiter is set"`
	NextCursor string   `json:"nextCursor,omitempty" doc:"Pass as cursor for the next page; empty on the last page"`
}

// Presigned is a presigned URL.
type Presigned struct {
	URL       string            `json:"url"`
	Method    string            `json:"method"`
	ExpiresAt time.Time         `json:"expiresAt"`
	Headers   map[string]string `json:"headers,omitempty" doc:"Headers the client must send with the request"`
}

// Uploaded describes a stored object.
type Uploaded struct {
	Bucket string `json:"bucket"`
	Key    string `json:"key"`
	Size   int64  `json:"size"`
	ETag   string `json:"etag"`
	URL    string `json:"url,omitempty" doc:"Public URL (public buckets only)"`
}

// ObjectContent is a small object's bytes.
type ObjectContent struct {
	Key         string `json:"key"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	ETag        string `json:"etag"`
	Text        string `json:"text,omitempty" doc:"The content, when it is UTF-8 text"`
	Base64      string `json:"base64,omitempty" doc:"The content, base64-encoded, when it is binary"`
}

// AuditReport is the outcome of a checksum audit.
type AuditReport struct {
	Project   string       `json:"project"`
	OK        bool         `json:"ok" doc:"True when every object's bytes match its recorded checksum"`
	Objects   int          `json:"objects"`
	Bytes     int64        `json:"bytes"`
	Verified  int          `json:"verified" doc:"Objects whose MD5 matched their ETag"`
	Multipart int          `json:"multipart" doc:"Multipart objects: no whole-object MD5 to compare, SHA-256 recorded in the manifest"`
	Problems  []AuditIssue `json:"problems"`
	Manifest  string       `json:"manifest" doc:"Checksum manifest on the box (bucket → key → sha256, size) for verifying mirrors"`
	Backup    string       `json:"backup" doc:"Off-box mirror status"`
	Took      string       `json:"took"`
}

// AuditIssue is one object that failed the audit.
type AuditIssue struct {
	Bucket string `json:"bucket"`
	Key    string `json:"key"`
	Issue  string `json:"issue" enum:"checksum_mismatch,missing,unreadable"`
	Detail string `json:"detail,omitempty"`
}

type projectIn struct {
	Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
}

// RegisterAPI adds the storage operations.
func (m *Module) RegisterAPI(a huma.API, p *platform.Platform) {
	tag := "storage"
	need := func() (*platform.Platform, error) {
		if p == nil {
			return nil, api.NewProblem(501, "internal", "storage is only available on a box")
		}
		return p, nil
	}

	huma.Register(a, api.Op("storage-get", http.MethodGet, "/v1/projects/{project}/storage", "storage show", api.RiskRead,
		"Show a project's storage", "Buckets with their size and object count, the S3 endpoints, the public files URL and the quota.", tag),
		api.Wrap(func(ctx context.Context, in *projectIn) (*struct{ Body Info }, error) {
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
				return nil, err
			}
			p, err := need()
			if err != nil {
				return nil, err
			}
			info, err := m.info(ctx, p, in.Project)
			if err != nil {
				return nil, err
			}
			return &struct{ Body Info }{*info}, nil
		}))

	huma.Register(a, api.Untrusted(api.Op("storage-objects-list", http.MethodGet, "/v1/projects/{project}/storage/buckets/{bucket}/objects", "storage objects list", api.RiskRead,
		"List objects in a bucket", "One page of objects, optionally under a prefix. With delimiter \"/\" you get one folder level and the sub-folders in prefixes.", tag)),
		api.Wrap(func(ctx context.Context, in *struct {
			Project   string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
			Bucket    string `path:"bucket" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Bucket name"`
			Prefix    string `query:"prefix" maxLength:"1024" doc:"Only keys starting with this"`
			Delimiter string `query:"delimiter" maxLength:"1" doc:"Group keys by this character (usually /)"`
			Cursor    string `query:"cursor" maxLength:"2048" doc:"nextCursor from the previous page"`
			Limit     int    `query:"limit" minimum:"1" maximum:"1000" default:"100" doc:"Objects per page"`
		}) (*struct{ Body ObjectList }, error) {
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
				return nil, err
			}
			p, err := need()
			if err != nil {
				return nil, err
			}
			s3name, _, err := m.readyBucket(ctx, p, in.Project, in.Bucket)
			if err != nil {
				return nil, err
			}
			gw, _ := m.gateway(p)
			res, err := gw.list(ctx, s3name, in.Prefix, in.Delimiter, in.Cursor, in.Limit)
			if err != nil {
				return nil, gwProblem(err)
			}
			return &struct{ Body ObjectList }{ObjectList{Bucket: in.Bucket, Prefix: in.Prefix, Objects: res.Objects, Prefixes: res.Prefixes, NextCursor: res.Next}}, nil
		}))

	up := api.Op("storage-object-put", http.MethodPut, "/v1/projects/{project}/storage/buckets/{bucket}/objects", "storage objects put", api.RiskWrite,
		"Upload a small object", fmt.Sprintf("Stores text or base64 content (up to %d MiB) at key, replacing any object there. For bigger files use a presigned PUT URL (storage presign).", MaxAPIUpload>>20), tag)
	up.MaxBodyBytes = MaxAPIUpload*4/3 + 64<<10
	huma.Register(a, up, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Bucket  string `path:"bucket" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Bucket name"`
		Body    struct {
			Key         string `json:"key" minLength:"1" maxLength:"1024" doc:"Object key, e.g. avatars/42.png"`
			Text        string `json:"text,omitempty" doc:"Content as text (UTF-8)"`
			Base64      string `json:"base64,omitempty" doc:"Content, base64-encoded (for binary files)"`
			ContentType string `json:"contentType,omitempty" maxLength:"255" doc:"Default: guessed from the content"`
		}
	}) (*struct{ Body Uploaded }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		p, err := need()
		if err != nil {
			return nil, err
		}
		data := []byte(in.Body.Text)
		if in.Body.Base64 != "" {
			if in.Body.Text != "" {
				return nil, api.NewProblem(422, "validation", "send text or base64, not both")
			}
			if data, err = base64.StdEncoding.DecodeString(in.Body.Base64); err != nil {
				return nil, api.NewProblem(422, "validation", "base64 is not valid standard base64: "+err.Error())
			}
		}
		out, err := m.upload(ctx, p, in.Project, in.Bucket, in.Body.Key, in.Body.ContentType, data)
		if err != nil {
			return nil, err
		}
		return &struct{ Body Uploaded }{*out}, nil
	}))

	// Browser uploads from the dashboard: multipart/form-data with fields
	// "key" and "file". Hidden from the CLI and MCP (they use storage-object-put).
	form := api.Op("storage-object-upload", http.MethodPost, "/v1/projects/{project}/storage/buckets/{bucket}/objects", "storage objects upload", api.RiskWrite,
		"Upload a file (multipart form)", "For browsers: multipart/form-data with a \"key\" field and a \"file\" field.", tag)
	form.Hidden = true
	form.MaxBodyBytes = MaxAPIUpload + 1<<20
	huma.Register(a, form, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$"`
		Bucket  string `path:"bucket" pattern:"^[a-z][a-z0-9-]{0,39}$"`
		RawBody huma.MultipartFormFiles[struct {
			Key  string        `form:"key" required:"true"`
			File huma.FormFile `form:"file" required:"true"`
		}]
	}) (*struct{ Body Uploaded }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		p, err := need()
		if err != nil {
			return nil, err
		}
		d := in.RawBody.Data()
		if d.File.Size > MaxAPIUpload {
			return nil, api.NewProblem(413, "validation", fmt.Sprintf("files over %d MiB need a presigned PUT URL", MaxAPIUpload>>20))
		}
		data, err := io.ReadAll(io.LimitReader(d.File, MaxAPIUpload+1))
		if err != nil {
			return nil, err
		}
		out, err := m.upload(ctx, p, in.Project, in.Bucket, d.Key, d.File.ContentType, data)
		if err != nil {
			return nil, err
		}
		return &struct{ Body Uploaded }{*out}, nil
	}))

	huma.Register(a, api.Untrusted(api.Op("storage-object-get", http.MethodGet, "/v1/projects/{project}/storage/buckets/{bucket}/object", "storage objects get", api.RiskRead,
		"Read a small object", "Returns an object's content (up to 1 MiB) as text, or base64 when it is binary. For anything bigger use a presigned GET URL.", tag)),
		api.Wrap(func(ctx context.Context, in *struct {
			Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
			Bucket  string `path:"bucket" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Bucket name"`
			Key     string `query:"key" required:"true" minLength:"1" maxLength:"1024" doc:"Object key"`
		}) (*struct{ Body ObjectContent }, error) {
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
				return nil, err
			}
			p, err := need()
			if err != nil {
				return nil, err
			}
			s3name, _, err := m.readyBucket(ctx, p, in.Project, in.Bucket)
			if err != nil {
				return nil, err
			}
			gw, _ := m.gateway(p)
			res, err := gw.do(ctx, http.MethodGet, s3name, in.Key, nil, nil, nil)
			if err != nil {
				return nil, gwProblem(err)
			}
			defer res.Body.Close()
			if res.ContentLength > 1<<20 {
				return nil, api.NewProblem(413, "validation", fmt.Sprintf("object is %s; read objects over 1 MiB with a presigned GET URL", HumanBytes(res.ContentLength)))
			}
			data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20+1))
			if err != nil {
				return nil, err
			}
			out := ObjectContent{Key: in.Key, ContentType: res.Header.Get("Content-Type"), Size: int64(len(data)), ETag: strings.Trim(res.Header.Get("ETag"), `"`)}
			if utf8.Valid(data) && !strings.ContainsRune(string(data), 0) {
				out.Text = string(data)
			} else {
				out.Base64 = base64.StdEncoding.EncodeToString(data)
			}
			return &struct{ Body ObjectContent }{out}, nil
		}))

	del := api.Op("storage-object-delete", http.MethodDelete, "/v1/projects/{project}/storage/buckets/{bucket}/objects", "storage objects delete", api.RiskDestructive,
		"Delete an object", "Deletes one object for good. There is no undo for objects (deleting a whole bucket goes to the trash).", tag)
	del.Errors = append(del.Errors, 404)
	huma.Register(a, del, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Bucket  string `path:"bucket" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Bucket name"`
		Key     string `query:"key" required:"true" minLength:"1" maxLength:"1024" doc:"Object key"`
	}) (*struct{}, error) {
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyIrreversible, in.Project); err != nil {
			return nil, err
		}
		p, err := need()
		if err != nil {
			return nil, err
		}
		s3name, _, err := m.readyBucket(ctx, p, in.Project, in.Bucket)
		if err != nil {
			return nil, err
		}
		gw, _ := m.gateway(p)
		if _, err := gw.headObject(ctx, s3name, in.Key); err != nil {
			return nil, gwProblem(err)
		}
		if err := gw.deleteObject(ctx, s3name, in.Key); err != nil {
			return nil, gwProblem(err)
		}
		_ = p.DB.Audit(ctx, pr.TokenID, "storage.object.delete", in.Project+"/"+in.Bucket+"/"+in.Key, map[string]any{"session": pr.Session})
		m.tracker().invalidate()
		return &struct{}{}, nil
	}))

	huma.Register(a, api.Op("storage-presign", http.MethodPost, "/v1/projects/{project}/storage/buckets/{bucket}/presign", "storage presign", api.RiskRead,
		"Create a presigned URL", "A time-limited URL on the public S3 endpoint that lets anyone holding it GET (download) or PUT (upload) one object without credentials. "+
			"PUT URLs need full access.", tag),
		api.Wrap(func(ctx context.Context, in *struct {
			Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
			Bucket  string `path:"bucket" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Bucket name"`
			Body    struct {
				Key       string `json:"key" minLength:"1" maxLength:"1024" doc:"Object key"`
				Method    string `json:"method,omitempty" enum:"GET,PUT" default:"GET" doc:"GET to download, PUT to upload"`
				ExpiresIn int    `json:"expiresIn,omitempty" minimum:"0" maximum:"604800" doc:"Seconds the URL stays valid (default 3600, max 7 days)"`
				// PUT only: bound into the signature.
				ContentType string `json:"contentType,omitempty" maxLength:"255" doc:"PUT only: the Content-Type the upload must send (signed into the URL)"`
				MaxSize     int64  `json:"maxSize,omitempty" minimum:"0" doc:"PUT only: the largest file the URL accepts, in bytes (signed into the URL; the bucket's maxFileSize applies too)"`
			}
		}) (*struct{ Body Presigned }, error) {
			method := in.Body.Method
			if method == "" {
				method = http.MethodGet
			}
			scope := tokens.ScopeRead
			if method == http.MethodPut {
				scope = tokens.ScopeApplyReversible
			}
			if err := api.PrincipalFrom(ctx).Require(scope, in.Project); err != nil {
				return nil, err
			}
			p, err := need()
			if err != nil {
				return nil, err
			}
			if method != http.MethodPut && (in.Body.ContentType != "" || in.Body.MaxSize != 0) {
				return nil, api.NewProblem(422, "validation", "contentType and maxSize are for PUT URLs")
			}
			out, err := m.presign(ctx, p, in.Project, in.Bucket, in.Body.Key, method, time.Duration(in.Body.ExpiresIn)*time.Second, in.Body.ContentType, in.Body.MaxSize)
			if err != nil {
				return nil, err
			}
			return &struct{ Body Presigned }{*out}, nil
		}))

	huma.Register(a, api.Op("storage-credentials", http.MethodGet, "/v1/projects/{project}/storage/credentials", "storage credentials", api.RiskRead,
		"Show a project's S3 credentials", "The S3 env vars the project's apps get (S3_*, AWS_*), including the secret key, for tools and local development. "+
			"Needs full access because the key can read and delete every object.", tag),
		api.Wrap(func(ctx context.Context, in *projectIn) (*struct{ Body map[string]string }, error) {
			pr := api.PrincipalFrom(ctx)
			if err := pr.Require(tokens.ScopeApplyIrreversible, in.Project); err != nil {
				return nil, err
			}
			p, err := need()
			if err != nil {
				return nil, err
			}
			env, err := m.Env(ctx, p, in.Project, "")
			if err != nil {
				return nil, err
			}
			if env == nil {
				return nil, noStorage(in.Project)
			}
			_ = p.DB.Audit(ctx, pr.TokenID, "storage.credentials.read", in.Project, map[string]any{"session": pr.Session})
			return &struct{ Body map[string]string }{env}, nil
		}))

	q := api.Op("storage-quota-set", http.MethodPut, "/v1/projects/{project}/storage/quota", "storage quota set", api.RiskWrite,
		"Set a project's storage limit", "Caps what a project stores on the box: its databases (branches included) and its files together. "+
			"Off by default. Uploads that would go over are refused with QuotaExceeded (files measured every minute plus uploads since, databases "+
			"every 30 seconds); a project that reaches its limit becomes read-only (its database refuses writes, its buckets refuse uploads) until it "+
			"is under the limit again, and the box's change log records both. Raising or clearing the limit lifts it within seconds. "+
			"Setting it is a change in History too: undo puts the previous limit back. "+
			"maxBytes: >0 sets the limit, 0 returns to the box default, -1 means none. Box admins only.", tag)
	q.Errors = append(q.Errors, 404)
	huma.Register(a, q, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Body    struct {
			MaxBytes int64 `json:"maxBytes" minimum:"-1" doc:">0: limit in bytes; 0: box default; -1: unlimited"`
		}
	}) (*struct{ Body Info }, error) {
		pr := api.PrincipalFrom(ctx)
		if !pr.BoxAdmin() {
			return nil, fmt.Errorf("%w: storage quotas are set by the box owner", tokens.ErrForbidden)
		}
		p, err := need()
		if err != nil {
			return nil, err
		}
		if v, _, err := p.DB.Load(ctx, in.Project); err != nil {
			return nil, err
		} else if v == 0 {
			return nil, api.NewProblem(404, "not_found", "project "+in.Project+" does not exist")
		}
		// The limit is a resource of the project, so setting it is a change
		// in History that undo reverts.
		var spec json.RawMessage
		intent := in.Project + "'s storage limit follows the box default again"
		switch n := in.Body.MaxBytes; {
		case n > 0:
			intent = fmt.Sprintf("Set %s's storage limit to %s", in.Project, HumanBytes(n))
		case n < 0:
			intent = "Removed " + in.Project + "'s storage limit"
		}
		if in.Body.MaxBytes != 0 {
			spec, _ = json.Marshal(limitSpec{MaxBytes: in.Body.MaxBytes})
		}
		plan, err := p.Engine.PlanEdit(ctx, in.Project, func(cur map[string]change.Resource) (map[string]change.Resource, error) {
			if spec == nil {
				delete(cur, change.KindStorageLimit)
			} else {
				cur[change.KindStorageLimit] = change.Resource{Address: change.KindStorageLimit, Spec: spec}
			}
			return cur, nil
		})
		if err != nil {
			return nil, err
		}
		c, err := p.Engine.Apply(ctx, change.ApplyRequest{Plan: plan, Confirm: plan.Hash, Actor: pr.Actor(), Intent: intent, Authorize: pr.Authorizer()})
		if err != nil {
			return nil, err
		}
		if err := setLimit(ctx, p, in.Project, spec); err != nil { // now, so the answer shows it; converging writes the same
			return nil, err
		}
		if c != nil {
			p.AfterApply(c)
		}
		info, err := m.info(ctx, p, in.Project)
		if err != nil {
			// A project without buckets has a limit too: its databases count.
			n, own, _ := quotaFor(ctx, p, in.Project)
			db := databaseBytes(in.Project)
			info = &Info{Project: in.Project, UsedBytes: db, DatabaseBytes: db, QuotaBytes: n, QuotaSource: "box-default",
				ReadOnly: ReadOnly(in.Project), Buckets: []BucketInfo{}}
			if own {
				info.QuotaSource = "project"
			}
		}
		return &struct{ Body Info }{*info}, nil
	}))

	huma.Register(a, api.Op("storage-quota-default-set", http.MethodPut, "/v1/storage/quota", "storage quota default", api.RiskWrite,
		"Set the box's default storage limit", "The storage limit (database and files together) for projects without their own. 0 means none, which is where a box starts. Box admins only.", tag),
		api.Wrap(func(ctx context.Context, in *struct {
			Body struct {
				MaxBytes int64 `json:"maxBytes" minimum:"0" doc:"Bytes per project; 0 means unlimited"`
			}
		}) (*struct {
			Body struct {
				MaxBytes int64 `json:"maxBytes"`
			}
		}, error) {
			pr := api.PrincipalFrom(ctx)
			if !pr.BoxAdmin() {
				return nil, fmt.Errorf("%w: storage quotas are set by the box owner", tokens.ErrForbidden)
			}
			p, err := need()
			if err != nil {
				return nil, err
			}
			if err := p.DB.KVPut(ctx, kvNS, "quota-default", []byte(strconv.FormatInt(in.Body.MaxBytes, 10))); err != nil {
				return nil, err
			}
			limitsChanged()
			out := &struct {
				Body struct {
					MaxBytes int64 `json:"maxBytes"`
				}
			}{}
			out.Body.MaxBytes = in.Body.MaxBytes
			return out, nil
		}))

	huma.Register(a, api.Untrusted(api.Op("storage-audit", http.MethodPost, "/v1/projects/{project}/storage/audit", "storage audit", api.RiskRead,
		"Audit a project's stored bytes", "Reads every object and checks it against its recorded checksum (MD5 ETag), "+
			"then writes a SHA-256 manifest a backup mirror can be verified against. Takes as long as reading the data.", tag)),
		api.Wrap(func(ctx context.Context, in *projectIn) (*struct{ Body AuditReport }, error) {
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
				return nil, err
			}
			p, err := need()
			if err != nil {
				return nil, err
			}
			rep, err := m.audit(ctx, p, in.Project)
			if err != nil {
				return nil, err
			}
			return &struct{ Body AuditReport }{*rep}, nil
		}))

	huma.Register(a, api.Op("storage-trash-list", http.MethodGet, "/v1/storage/trash", "storage trash list", api.RiskRead,
		"List deleted buckets", fmt.Sprintf("Buckets deleted in the last %d days. Undoing the change that deleted one (or adding it back) restores it with its files.", int(TrashRetention.Hours()/24)), tag),
		api.Wrap(func(ctx context.Context, in *struct {
			Project string `query:"project" doc:"Only this project"`
		}) (*struct{ Body []TrashEntry }, error) {
			pr := api.PrincipalFrom(ctx)
			if err := pr.Require(tokens.ScopeRead, in.Project); err != nil {
				return nil, err
			}
			p, err := need()
			if err != nil {
				return nil, err
			}
			all, err := trashEntries(ctx, p)
			if err != nil {
				return nil, err
			}
			out := []TrashEntry{}
			for _, e := range all {
				if (in.Project == "" || e.Project == in.Project) && pr.CanProject(e.Project) {
					out = append(out, e)
				}
			}
			return &struct{ Body []TrashEntry }{out}, nil
		}))

	pg := api.Op("storage-trash-purge", http.MethodDelete, "/v1/storage/trash/{id}", "storage trash purge", api.RiskDestructive,
		"Delete a trashed bucket for good", "Frees the space now instead of after 7 days. The bucket's files cannot be recovered afterwards.", tag)
	pg.Errors = append(pg.Errors, 404)
	huma.Register(a, pg, api.Wrap(func(ctx context.Context, in *struct {
		ID string `path:"id" pattern:"^trs_[0-9A-Z]{26}$" doc:"Trash entry ID"`
	}) (*struct{}, error) {
		pr := api.PrincipalFrom(ctx)
		p, err := need()
		if err != nil {
			return nil, err
		}
		all, err := trashEntries(ctx, p)
		if err != nil {
			return nil, err
		}
		for _, e := range all {
			if e.ID != in.ID || !pr.CanProject(e.Project) {
				continue
			}
			if err := pr.Require(tokens.ScopeApplyIrreversible, e.Project); err != nil {
				return nil, err
			}
			if err := purgeEntry(ctx, p, e); err != nil {
				return nil, err
			}
			_ = p.DB.Audit(ctx, pr.TokenID, "storage.trash.purge", e.Project+"/"+e.Bucket, map[string]any{"id": e.ID, "bytes": e.Bytes})
			return &struct{}{}, nil
		}
		return nil, api.NewProblem(404, "not_found", "no trash entry "+in.ID)
	}))
}

func noStorage(project string) error {
	p := api.NewProblem(404, "not_found", "project "+project+" has no storage")
	p.Hint = "add services.storage (with buckets) to tiffin.config.ts, then plan and apply"
	return p
}

// gwProblem maps gateway errors to API problems.
func gwProblem(err error) error {
	var se *s3Error
	if errors.As(err, &se) {
		switch {
		case se.Code == "NoSuchKey" || se.Status == 404:
			return api.NewProblem(404, "not_found", "no such object")
		case se.Code == "NoSuchBucket":
			return api.NewProblem(404, "not_found", "no such bucket on the box yet")
		}
		return api.NewProblem(502, "internal", se.Error())
	}
	if errors.Is(err, ErrNotInstalled) {
		return api.NewProblem(503, "precondition", err.Error())
	}
	return err
}

// readyBucket checks the bucket is declared and converged; it returns the S3 name.
func (m *Module) readyBucket(ctx context.Context, p *platform.Platform, project, bucket string) (string, *bucketMeta, error) {
	on, buckets, err := projectStorage(ctx, p, project)
	if err != nil {
		return "", nil, err
	}
	if !on {
		return "", nil, noStorage(project)
	}
	if _, ok := buckets[bucket]; !ok {
		pr := api.NewProblem(404, "not_found", "project "+project+" has no bucket "+bucket)
		pr.Hint = "add it under services.storage.buckets in tiffin.config.ts, then plan and apply"
		return "", nil, pr
	}
	s3name := S3Name(project, bucket)
	meta, err := getMeta(ctx, p, s3name)
	if err != nil {
		return "", nil, err
	}
	if meta == nil {
		pr := api.NewProblem(409, "precondition", "bucket "+bucket+" is not created on the box yet")
		pr.Hint = "check its state with `tiffin projects get " + project + "`; reconcile retries on the next apply or restart"
		return "", nil, pr
	}
	if _, err := m.gateway(p); err != nil {
		return "", nil, gwProblem(err)
	}
	return s3name, meta, nil
}

func (m *Module) info(ctx context.Context, p *platform.Platform, project string) (*Info, error) {
	on, buckets, err := projectStorage(ctx, p, project)
	if err != nil {
		return nil, err
	}
	if !on {
		return nil, noStorage(project)
	}
	meta, err := allMeta(ctx, p)
	if err != nil {
		return nil, err
	}
	t := m.tracker()
	info := &Info{Project: project, Endpoint: p.URL(p.Host("s3")), InternalEndpoint: fmt.Sprintf("http://%s:%d", m.containerHost(ctx, p), FrontPort),
		FilesURL: p.URL(p.Host("files")) + "/" + project, Region: Region, Buckets: []BucketInfo{}, MeasuredAt: t.at()}
	if c, ok, _ := credsFor(ctx, p, project, false); ok {
		info.AccessKeyID = c.AccessKey
	}
	names := make([]string, 0, len(buckets))
	for n := range buckets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		s3name := S3Name(project, n)
		bi := BucketInfo{Name: n, S3Name: s3name, Public: buckets[n].Public, State: "pending"}
		if mt, ok := meta[s3name]; ok && mt.Project == project {
			bi.State = "ready"
			u := t.bucket(s3name)
			bi.Bytes, bi.Objects = u.Bytes, u.Objects
		}
		if bi.Public {
			bi.PublicURL = info.FilesURL + "/" + n
		}
		info.Buckets = append(info.Buckets, bi)
	}
	info.FilesBytes, info.DatabaseBytes, info.ReadOnly = t.project(meta, project), databaseBytes(project), ReadOnly(project)
	info.UsedBytes = info.FilesBytes + info.DatabaseBytes
	q, own, err := quotaFor(ctx, p, project)
	if err != nil {
		return nil, err
	}
	info.QuotaBytes, info.QuotaSource = q, "box-default"
	if own {
		info.QuotaSource = "project"
	}
	return info, nil
}

func (m *Module) upload(ctx context.Context, p *platform.Platform, project, bucket, key, contentType string, data []byte) (*Uploaded, error) {
	if len(data) > MaxAPIUpload {
		return nil, api.NewProblem(413, "validation", fmt.Sprintf("objects over %d MiB need a presigned PUT URL", MaxAPIUpload>>20))
	}
	if strings.HasSuffix(key, "/") || strings.HasPrefix(key, "/") || strings.Contains(key, "//") || strings.Contains("/"+key+"/", "/../") || strings.Contains("/"+key+"/", "/./") {
		return nil, api.NewProblem(422, "validation", "key must not start or end with /, or contain empty, . or .. segments")
	}
	s3name, meta, err := m.readyBucket(ctx, p, project, bucket)
	if err != nil {
		return nil, err
	}
	all, err := allMeta(ctx, p)
	if err != nil {
		return nil, err
	}
	if what, fix := m.refusal(ctx, p, all, project, int64(len(data))); what != "" {
		pr := api.NewProblem(409, "precondition", what)
		pr.Hint = fix
		return nil, pr
	}
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	if meta.MaxFileSize > 0 && int64(len(data)) > meta.MaxFileSize {
		return nil, api.NewProblem(413, "validation", fmt.Sprintf("bucket %s takes files up to %s; this one is %s", bucket, HumanBytes(meta.MaxFileSize), HumanBytes(int64(len(data)))))
	}
	if !typeAllowed(meta.AllowedTypes, contentType) {
		return nil, api.NewProblem(422, "validation", fmt.Sprintf("bucket %s takes %s; this file is %s", bucket, strings.Join(meta.AllowedTypes, ", "), contentType))
	}
	gw, _ := m.gateway(p)
	etag, err := gw.putObject(ctx, s3name, key, contentType, data)
	if err != nil {
		return nil, gwProblem(err)
	}
	m.tracker().add(s3name, int64(len(data)))
	m.objectCreated(meta, s3name, key, "")
	out := &Uploaded{Bucket: bucket, Key: key, Size: int64(len(data)), ETag: etag}
	if meta.Public {
		out.URL = p.URL(p.Host("files")) + "/" + project + "/" + bucket + "/" + s3Escape(key, true)
	}
	return out, nil
}

func (m *Module) presign(ctx context.Context, p *platform.Platform, project, bucket, key, method string, expires time.Duration, contentType string, maxSize int64) (*Presigned, error) {
	s3name, _, err := m.readyBucket(ctx, p, project, bucket)
	if err != nil {
		return nil, err
	}
	c, ok, err := credsFor(ctx, p, project, false)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, noStorage(project)
	}
	if expires <= 0 {
		expires = time.Hour
	}
	now := time.Now().UTC()
	var q url.Values
	var hdr map[string]string
	if maxSize > 0 {
		q = url.Values{MaxSizeParam: {strconv.FormatInt(maxSize, 10)}}
	}
	if contentType != "" {
		hdr = map[string]string{"content-type": contentType}
	}
	u, err := PresignWith(method, p.URL(p.Host("s3")), s3name, key, q, hdr, c, Region, expires, now)
	if err != nil {
		return nil, err
	}
	out := &Presigned{URL: u, Method: method, ExpiresAt: now.Add(expires)}
	if contentType != "" {
		out.Headers = map[string]string{"Content-Type": contentType}
	}
	return out, nil
}
