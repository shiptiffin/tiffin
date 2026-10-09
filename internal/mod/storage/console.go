package storage

// The Files console's operations: read a file (thumbnails and previews),
// links that work without signing in, renames, moves and deletes with
// Undo, large uploads in parts through the API (so the dashboard needs no
// CORS or DNS for s3.<domain>).

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/mod/datakit"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

const (
	// maxPart is the largest part an upload part may be.
	maxPart = 64 << 20
	// minPart is the part size uploads start from (S3's own minimum is 5 MiB).
	minPart = 8 << 20
	// maxParts is S3's limit on parts per upload.
	maxParts = 10_000
	// maxLinkTTL is the longest a signed link works.
	maxLinkTTL = 7 * 24 * time.Hour
)

// FileLink is a link to a file that works without signing in.
type FileLink struct {
	URL       string     `json:"url"`
	Public    bool       `json:"public" doc:"The bucket is public: the link never expires"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty" doc:"Private buckets: when the signed link stops working"`
}

// UploadSession is a large upload in parts.
type UploadSession struct {
	UploadID string         `json:"uploadId"`
	Key      string         `json:"key"`
	PartSize int64          `json:"partSize" doc:"Every part but the last is this big"`
	Parts    []UploadedPart `json:"parts" doc:"Parts already stored (when resuming): send only the others"`
}

type BucketPath struct {
	Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
	Bucket  string `path:"bucket" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Bucket name"`
}

// partIn is one part's bytes, read raw (huma reads no body without a Body field).
type partIn struct {
	BucketPath
	UploadID string `path:"uploadId" maxLength:"1024" doc:"From storage-upload-start"`
	N        int    `path:"n" minimum:"1" maximum:"10000" doc:"Part number, from 1"`
	Key      string `query:"key" required:"true" minLength:"1" maxLength:"1024" doc:"Object key"`
	body     io.Reader
}

func (u *partIn) Resolve(ctx huma.Context) []error {
	u.body = api.UploadBody(ctx)
	return nil
}

func (m *Module) registerConsole(a huma.API, p *platform.Platform, need func() (*platform.Platform, error), tag string) {
	fo := api.Untrusted(api.Op("storage-object-file", http.MethodGet, "/v1/projects/{project}/storage/buckets/{bucket}/file", "-", api.RiskRead,
		"Read a file", "The file's bytes, for the dashboard's previews and downloads (Range requests work). Images can be resized with w, q and f as on "+
			"files.<domain>. Only images, video, audio and PDFs show inline; everything else downloads. Tools use a presigned URL instead.", tag))
	fo.Responses = map[string]*huma.Response{"200": {Description: "The file", Content: map[string]*huma.MediaType{"application/octet-stream": {Schema: &huma.Schema{Type: "string", Format: "binary"}}}}}
	fo.Errors = append(fo.Errors, 404)
	huma.Register(a, fo, api.Wrap(func(ctx context.Context, in *struct {
		BucketPath
		Key      string `query:"key" required:"true" minLength:"1" maxLength:"1024" doc:"Object key"`
		W        int    `query:"w" doc:"Resize images to this width"`
		Q        int    `query:"q" doc:"Image quality: 50, 75, 90 or 100"`
		F        string `query:"f" enum:"webp,avif,original," doc:"Image format"`
		Download bool   `query:"download" doc:"Save it rather than show it"`
	}) (*huma.StreamResponse, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
			return nil, err
		}
		p, err := need()
		if err != nil {
			return nil, err
		}
		_, meta, err := m.readyBucket(ctx, p, in.Project, in.Bucket)
		if err != nil {
			return nil, err
		}
		m.mu.Lock()
		f := m.front
		m.mu.Unlock()
		if f == nil {
			return nil, api.NewProblem(503, "precondition", "storage is not running")
		}
		q := url.Values{}
		if in.W > 0 {
			q.Set("w", strconv.Itoa(in.W))
		}
		if in.Q > 0 {
			q.Set("q", strconv.Itoa(in.Q))
		}
		if in.F != "" {
			q.Set("f", in.F)
		}
		if !meta.Public {
			c, ok, err := credsFor(ctx, p, in.Project, false)
			if err != nil || !ok {
				return nil, noStorage(in.Project)
			}
			exp := time.Now().Add(10 * time.Minute).Unix()
			q.Set("exp", strconv.FormatInt(exp, 10))
			q.Set("sig", FilesSignature(c.Secret, in.Project, in.Bucket, in.Key, exp))
		}
		return &huma.StreamResponse{Body: func(hctx huma.Context) {
			r, w := humago.Unwrap(hctx)
			r2 := r.Clone(r.Context())
			r2.Method = http.MethodGet
			r2.URL = &url.URL{Path: "/" + in.Project + "/" + in.Bucket + "/" + in.Key, RawQuery: q.Encode()}
			f.serveFile(&fileWriter{ResponseWriter: w, name: path.Base(in.Key), download: in.Download}, r2)
		}}, nil
	}))

	huma.Register(a, api.Op("storage-link", http.MethodPost, "/v1/projects/{project}/storage/buckets/{bucket}/link", "storage link", api.RiskRead,
		"Make a link to a file", "A files.<domain> link anyone can open: the plain public URL for a public bucket, or for a private one a signed URL that works "+
			"until expiresIn runs out (default an hour, at most 7 days). w, q and f resize an image (w: a Next.js width such as 640 or 1080; "+
			"q: 50, 75, 90 or 100; f: webp, avif or original).", tag),
		api.Wrap(func(ctx context.Context, in *struct {
			BucketPath
			Body struct {
				Key       string `json:"key" minLength:"1" maxLength:"1024" doc:"Object key"`
				ExpiresIn int    `json:"expiresIn,omitempty" minimum:"0" maximum:"604800" doc:"Private buckets: seconds the link works (default 3600)"`
				W         int    `json:"w,omitempty" doc:"Resize to this width"`
				Q         int    `json:"q,omitempty" doc:"Quality"`
				F         string `json:"f,omitempty" enum:"webp,avif,original," doc:"Format"`
			}
		}) (*struct{ Body FileLink }, error) {
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
				return nil, err
			}
			p, err := need()
			if err != nil {
				return nil, err
			}
			b := in.Body
			out, err := m.link(ctx, p, in.Project, in.Bucket, b.Key, time.Duration(b.ExpiresIn)*time.Second, b.W, b.Q, b.F)
			if err != nil {
				return nil, err
			}
			return &struct{ Body FileLink }{*out}, nil
		}))

	mv := api.Op("storage-objects-move", http.MethodPost, "/v1/projects/{project}/storage/buckets/{bucket}/move", "storage objects move", api.RiskWrite,
		"Rename or move files", "Renames one file (to is its new key), moves files into a folder (to ends with /, they keep their names), or moves "+
			"everything under prefix to under to instead (renaming or moving a folder). Nothing is overwritten: a file already there refuses it. "+
			"The reply's undo id puts everything back for an hour.", tag)
	mv.Errors = append(mv.Errors, 404, 409)
	huma.Register(a, mv, api.Wrap(func(ctx context.Context, in *struct {
		BucketPath
		Body struct {
			Keys   []string `json:"keys,omitempty" maxItems:"50000" doc:"Files to move"`
			Prefix string   `json:"prefix,omitempty" maxLength:"1024" doc:"Or a folder (ending in /): everything under it"`
			To     string   `json:"to" maxLength:"1024" doc:"The new key, or a folder ending in /"`
		}
	}) (*struct{ Body *FilesResult }, error) {
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		p, err := need()
		if err != nil {
			return nil, err
		}
		res, err := m.moveObjects(ctx, p, in.Project, in.Bucket, in.Body.Keys, in.Body.Prefix, in.Body.To)
		if err != nil {
			return nil, err
		}
		auditFiles(ctx, p, pr, "storage.objects.move", in.Project, in.Bucket, res)
		return &struct{ Body *FilesResult }{res}, nil
	}))

	dl := api.Op("storage-objects-delete", http.MethodPost, "/v1/projects/{project}/storage/buckets/{bucket}/delete", "storage objects remove", api.RiskWrite,
		"Delete files", fmt.Sprintf("Deletes files, or everything under a folder (prefix ending in /). They are kept for %d minutes: the reply's undo id "+
			"puts them back (storage-undo); after that they are gone for good. A folder takes two steps: without confirm nothing changes and the reply is "+
			"428 with how many files and bytes would go; repeat with its confirm value.", int(holdKeep.Minutes())), tag)
	dl.Errors = append(dl.Errors, 404, 409, 428)
	huma.Register(a, dl, api.Wrap(func(ctx context.Context, in *struct {
		BucketPath
		Body struct {
			Keys    []string `json:"keys,omitempty" maxItems:"50000" doc:"Files to delete"`
			Prefix  string   `json:"prefix,omitempty" maxLength:"1024" doc:"Or a folder (ending in /): everything under it"`
			Confirm string   `json:"confirm,omitempty" doc:"For a folder: the confirm value from the 428 reply"`
		}
	}) (*struct{ Body *FilesResult }, error) {
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		p, err := need()
		if err != nil {
			return nil, err
		}
		b := in.Body
		if len(b.Keys) == 0 && b.Prefix == "" {
			return nil, api.NewProblem(422, "validation", "send keys, or a prefix ending in /")
		}
		if b.Prefix != "" && !strings.HasSuffix(b.Prefix, "/") {
			return nil, api.NewProblem(422, "validation", "prefix is a folder: it must end with /")
		}
		if b.Prefix != "" {
			if err := m.confirmFolder(ctx, p, in.Project, in.Bucket, b.Prefix, b.Confirm); err != nil {
				return nil, err
			}
		}
		res, err := m.deleteObjects(ctx, p, in.Project, in.Bucket, b.Keys, b.Prefix)
		if err != nil {
			return nil, err
		}
		auditFiles(ctx, p, pr, "storage.objects.delete", in.Project, in.Bucket, res)
		return &struct{ Body *FilesResult }{res}, nil
	}))

	un := api.Op("storage-undo", http.MethodPost, "/v1/projects/{project}/storage/undo", "storage undo", api.RiskWrite,
		"Undo a change to files", "Puts back what a move or delete did, once, within the hour. Refused when one of its files changed since, "+
			"or a new file took an old place. The reply has its own undo id, to redo.", tag)
	un.Errors = append(un.Errors, 404, 409)
	huma.Register(a, un, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Body    struct {
			ID string `json:"id" pattern:"^stu_[0-9a-f]{20}$" doc:"The undo id from the change"`
		}
	}) (*struct{ Body *FilesResult }, error) {
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		p, err := need()
		if err != nil {
			return nil, err
		}
		res, err := m.undoFiles(ctx, p, in.Project, in.Body.ID)
		if err != nil {
			return nil, err
		}
		auditFiles(ctx, p, pr, "storage.undo", in.Project, "", res)
		return &struct{ Body *FilesResult }{res}, nil
	}))

	m.registerUploads(a, need, tag)
}

// fileWriter makes a file safe to show on the dashboard's own origin:
// only media and PDFs inline, never sniffed, sandboxed.
type fileWriter struct {
	http.ResponseWriter
	name     string
	download bool
	wrote    bool
}

func (f *fileWriter) WriteHeader(code int) {
	if !f.wrote {
		f.wrote = true
		h := f.Header()
		h.Del("Access-Control-Allow-Origin")
		h.Set("X-Content-Type-Options", "nosniff")
		if code < 300 {
			ct := strings.ToLower(h.Get("Content-Type"))
			inline := strings.HasPrefix(ct, "image/") && !strings.HasPrefix(ct, "image/svg") || strings.HasPrefix(ct, "video/") ||
				strings.HasPrefix(ct, "audio/") || strings.HasPrefix(ct, "application/pdf")
			if f.download || !inline {
				h.Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(f.name))
			}
			if strings.HasPrefix(ct, "application/pdf") && !f.download {
				// Browsers refuse to show a PDF in a sandbox; nothing else in it runs.
				h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:")
			}
			h.Set("Cache-Control", "private, max-age=300")
		}
	}
	f.ResponseWriter.WriteHeader(code)
}

func (f *fileWriter) Write(b []byte) (int, error) {
	if !f.wrote {
		f.WriteHeader(http.StatusOK)
	}
	return f.ResponseWriter.Write(b)
}

func (f *fileWriter) Flush() {
	if fl, ok := f.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

// link makes a files.<domain> link to one file.
func (m *Module) link(ctx context.Context, p *platform.Platform, project, bucket, key string, ttl time.Duration, w, q int, f string) (*FileLink, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	_, meta, err := m.readyBucket(ctx, p, project, bucket)
	if err != nil {
		return nil, err
	}
	v := url.Values{}
	if w > 0 {
		v.Set("w", strconv.Itoa(w))
	}
	if q > 0 {
		v.Set("q", strconv.Itoa(q))
	}
	if f != "" {
		v.Set("f", f)
	}
	if _, _, err := parseImageParams(v); err != nil {
		return nil, api.NewProblem(422, "validation", err.Error())
	}
	out := &FileLink{Public: meta.Public}
	if !meta.Public {
		if ttl <= 0 {
			ttl = time.Hour
		}
		ttl = min(ttl, maxLinkTTL)
		c, ok, err := credsFor(ctx, p, project, false)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, noStorage(project)
		}
		exp := time.Now().Add(ttl).UTC().Truncate(time.Second)
		out.ExpiresAt = &exp
		v.Set("exp", strconv.FormatInt(exp.Unix(), 10))
		v.Set("sig", FilesSignature(c.Secret, project, bucket, key, exp.Unix()))
	}
	out.URL = p.URL(p.Host("files")) + "/" + project + "/" + bucket + "/" + s3Escape(key, true)
	// w, q and f first, so a link reads naturally: …/a.jpg?w=1080&f=webp&exp=…
	var parts []string
	for _, k := range []string{"w", "q", "f", "exp", "sig"} {
		if v.Has(k) {
			parts = append(parts, k+"="+url.QueryEscape(v.Get(k)))
		}
	}
	if len(parts) > 0 {
		out.URL += "?" + strings.Join(parts, "&")
	}
	return out, nil
}

// confirmFolder asks first before a folder's files go: how many, how big.
func (m *Module) confirmFolder(ctx context.Context, p *platform.Platform, project, bucket, prefix, confirm string) error {
	dir, _, err := m.filesBucket(ctx, p, project, bucket)
	if err != nil {
		return err
	}
	files, err := filesUnder(dir, prefix, maxFileOp)
	if err != nil {
		return err
	}
	var size int64
	examples := []string{}
	for _, f := range files {
		size += f.Size
		if len(examples) < 5 {
			examples = append(examples, f.From)
		}
	}
	preview := map[string]any{"prefix": prefix, "count": len(files), "bytes": size, "examples": examples, "undo": true}
	return datakit.RequireConfirm(confirm, []any{"storage-delete-folder", project, bucket, prefix}, preview)
}

func auditFiles(ctx context.Context, p *platform.Platform, pr *tokens.Principal, what, project, bucket string, res *FilesResult) {
	keys := res.Keys
	if len(keys) > 20 {
		keys = keys[:20]
	}
	target := project
	if bucket != "" {
		target += "/" + bucket
	}
	_ = p.DB.Audit(ctx, pr.TokenID, what, target, map[string]any{"keys": keys, "files": res.Files, "bytes": res.Bytes, "undo": res.Undo, "session": pr.Session})
}

// ---- large uploads ----

// partSizeFor picks the part size for a file: 8 MiB, or bigger (in whole
// MiB) to stay within S3's 10,000 parts.
func partSizeFor(size int64) int64 {
	n := int64(math.Ceil(float64(size) / maxParts / (1 << 20)))
	return max(minPart, n<<20)
}

// uploadRules checks a file against a bucket's rules before any byte moves.
func uploadRules(bucket string, meta *bucketMeta, size int64, contentType string) error {
	if meta.MaxFileSize > 0 && size > meta.MaxFileSize {
		return api.NewProblem(413, "validation", fmt.Sprintf("bucket %s takes files up to %s; this one is %s", bucket, HumanBytes(meta.MaxFileSize), HumanBytes(size)))
	}
	if !typeAllowed(meta.AllowedTypes, contentType) {
		return api.NewProblem(422, "validation", fmt.Sprintf("bucket %s takes %s; this file is %s", bucket, strings.Join(meta.AllowedTypes, ", "), contentType))
	}
	return nil
}

func (m *Module) registerUploads(a huma.API, need func() (*platform.Platform, error), tag string) {
	write := func(ctx context.Context, project, bucket string) (*platform.Platform, string, *bucketMeta, *gateway, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeApplyReversible, project); err != nil {
			return nil, "", nil, nil, err
		}
		p, err := need()
		if err != nil {
			return nil, "", nil, nil, err
		}
		s3name, meta, err := m.readyBucket(ctx, p, project, bucket)
		if err != nil {
			return nil, "", nil, nil, err
		}
		gw, _ := m.gateway(p)
		return p, s3name, meta, gw, nil
	}

	st := api.Op("storage-upload-start", http.MethodPost, "/v1/projects/{project}/storage/buckets/{bucket}/uploads", "-", api.RiskWrite,
		"Start a large upload", "Starts an upload in parts for the dashboard, after checking the bucket's size and type rules and the storage limit. "+
			"With uploadId it resumes instead and lists the parts already stored. Tools upload with a presigned URL instead.", tag)
	st.Errors = append(st.Errors, 404, 409, 413)
	huma.Register(a, st, api.Wrap(func(ctx context.Context, in *struct {
		BucketPath
		Body struct {
			Key         string `json:"key" minLength:"1" maxLength:"1024"`
			Size        int64  `json:"size" minimum:"0" doc:"The whole file's size in bytes"`
			ContentType string `json:"contentType,omitempty" maxLength:"255"`
			UploadID    string `json:"uploadId,omitempty" maxLength:"1024" doc:"Resume this upload"`
		}
	}) (*struct{ Body UploadSession }, error) {
		p, s3name, meta, gw, err := write(ctx, in.Project, in.Bucket)
		if err != nil {
			return nil, err
		}
		b := in.Body
		if err := checkKey(b.Key); err != nil {
			return nil, err
		}
		ct := b.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		if b.Size > maxPart*maxParts {
			return nil, api.NewProblem(413, "validation", "files up to "+HumanBytes(maxPart*maxParts)+" can be uploaded")
		}
		if err := uploadRules(in.Bucket, meta, b.Size, ct); err != nil {
			return nil, err
		}
		if what, fix := m.refusal(ctx, p, in.Project, b.Size); what != "" {
			return nil, conflict(what, fix)
		}
		out := UploadSession{UploadID: b.UploadID, Key: b.Key, PartSize: partSizeFor(b.Size), Parts: []UploadedPart{}}
		if b.UploadID != "" {
			parts, err := gw.listParts(ctx, s3name, b.Key, b.UploadID)
			if err != nil {
				return nil, uploadGone(err)
			}
			out.Parts = append(out.Parts, parts...)
		} else if out.UploadID, err = gw.createUpload(ctx, s3name, b.Key, ct); err != nil {
			return nil, gwProblem(err)
		}
		return &struct{ Body UploadSession }{out}, nil
	}))

	pt := api.Op("storage-upload-part", http.MethodPut, "/v1/projects/{project}/storage/buckets/{bucket}/uploads/{uploadId}/parts/{n}", "-", api.RiskWrite,
		"Upload one part", fmt.Sprintf("The raw bytes of part n (up to %d MiB) of an upload from storage-upload-start.", maxPart>>20), tag)
	pt.RequestBody = &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{"application/octet-stream": {Schema: &huma.Schema{Type: "string", Format: "binary"}}}}
	pt.MaxBodyBytes = maxPart + 1<<20
	pt.Errors = append(pt.Errors, 404, 409, 413)
	huma.Register(a, pt, api.Wrap(func(ctx context.Context, in *partIn) (*struct{ Body UploadedPart }, error) {
		p, s3name, meta, gw, err := write(ctx, in.Project, in.Bucket)
		if err != nil {
			return nil, err
		}
		if err := checkKey(in.Key); err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(in.body, maxPart+1))
		if err != nil {
			return nil, err
		}
		if len(data) > maxPart {
			return nil, api.NewProblem(413, "validation", fmt.Sprintf("parts are at most %d MiB", maxPart>>20))
		}
		if meta.MaxFileSize > 0 && int64(len(data)) > meta.MaxFileSize {
			return nil, uploadRules(in.Bucket, meta, int64(len(data)), "")
		}
		done, what, fix := m.admit(ctx, p, in.Project, s3name, int64(len(data)))
		if what != "" {
			return nil, conflict(what, fix)
		}
		etag, err := gw.uploadPart(ctx, s3name, in.Key, in.UploadID, in.N, data)
		done(err == nil, int64(len(data)))
		if err != nil {
			return nil, uploadGone(err)
		}
		return &struct{ Body UploadedPart }{UploadedPart{N: in.N, Size: int64(len(data)), ETag: etag}}, nil
	}))

	co := api.Op("storage-upload-complete", http.MethodPost, "/v1/projects/{project}/storage/buckets/{bucket}/uploads/{uploadId}/complete", "-", api.RiskWrite,
		"Finish a large upload", "Joins the stored parts into the file, after checking the whole size against the bucket's rules (an upload over them is aborted).", tag)
	co.Errors = append(co.Errors, 404, 409, 413)
	huma.Register(a, co, api.Wrap(func(ctx context.Context, in *struct {
		BucketPath
		UploadID string `path:"uploadId" maxLength:"1024"`
		Body     struct {
			Key string `json:"key" minLength:"1" maxLength:"1024"`
		}
	}) (*struct{ Body Uploaded }, error) {
		p, s3name, meta, gw, err := write(ctx, in.Project, in.Bucket)
		if err != nil {
			return nil, err
		}
		key := in.Body.Key
		if err := checkKey(key); err != nil {
			return nil, err
		}
		parts, err := gw.listParts(ctx, s3name, key, in.UploadID)
		if err != nil {
			return nil, uploadGone(err)
		}
		if len(parts) == 0 {
			return nil, api.NewProblem(409, "conflict", "no parts were uploaded yet")
		}
		sort.Slice(parts, func(i, j int) bool { return parts[i].N < parts[j].N })
		var total int64
		for _, pt := range parts {
			total += pt.Size
		}
		if meta.MaxFileSize > 0 && total > meta.MaxFileSize {
			_ = gw.abortUpload(ctx, s3name, key, in.UploadID)
			return nil, uploadRules(in.Bucket, meta, total, "")
		}
		etag, err := gw.completeUpload(ctx, s3name, key, in.UploadID, parts)
		if err != nil {
			return nil, gwProblem(err)
		}
		m.objectCreated(meta, s3name, key, "")
		out := Uploaded{Bucket: in.Bucket, Key: key, Size: total, ETag: etag}
		if meta.Public {
			out.URL = p.URL(p.Host("files")) + "/" + in.Project + "/" + in.Bucket + "/" + s3Escape(key, true)
		}
		return &struct{ Body Uploaded }{out}, nil
	}))

	ab := api.Op("storage-upload-abort", http.MethodDelete, "/v1/projects/{project}/storage/buckets/{bucket}/uploads/{uploadId}", "-", api.RiskWrite,
		"Cancel a large upload", "Gives up on an upload and frees the parts stored so far.", tag)
	ab.Errors = append(ab.Errors, 404)
	huma.Register(a, ab, api.Wrap(func(ctx context.Context, in *struct {
		BucketPath
		UploadID string `path:"uploadId" maxLength:"1024"`
		Key      string `query:"key" required:"true" minLength:"1" maxLength:"1024"`
	}) (*struct{}, error) {
		_, s3name, _, gw, err := write(ctx, in.Project, in.Bucket)
		if err != nil {
			return nil, err
		}
		if err := gw.abortUpload(ctx, s3name, in.Key, in.UploadID); err != nil && !isCode(err, "NoSuchUpload") {
			return nil, gwProblem(err)
		}
		m.tracker().invalidate()
		return &struct{}{}, nil
	}))
}

// uploadGone words an upload the gateway no longer has.
func uploadGone(err error) error {
	if isCode(err, "NoSuchUpload") || errorsIsNotFound(err) {
		pr := api.NewProblem(404, "not_found", "that upload was cancelled or finished already")
		pr.Hint = "start the upload again"
		return pr
	}
	return gwProblem(err)
}
