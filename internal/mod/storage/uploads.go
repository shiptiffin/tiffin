package storage

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The front checks every write before the gateway sees it: the project's
// storage limit and read-only holds, and the bucket's maxFileSize and
// allowedTypes. A presigned upload URL can carry its own, smaller size cap
// (MaxSizeParam): the parameter is part of the signed query, so whoever
// holds the URL cannot remove or change it.

// MaxSizeParam is the query parameter of a presigned upload URL that caps
// the object's size in bytes (for multipart uploads: each part, and the
// whole object at completion).
const MaxSizeParam = "x-tiffin-max-size"

// s3Op is the kind of S3 request, as far as the front cares.
type s3Op int

const (
	opOther    s3Op = iota
	opPut           // PutObject
	opCopy          // CopyObject
	opPart          // UploadPart
	opPartCopy      // UploadPartCopy
	opCreate        // CreateMultipartUpload
	opComplete      // CompleteMultipartUpload
	opPostForm      // a browser form upload (POST policy) to the bucket
)

// classify returns the operation of a path-style S3 request with its bucket and key.
func classify(r *http.Request) (s3Op, string, string) {
	bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if bucket == "" {
		return opOther, "", ""
	}
	q := r.URL.Query()
	has := func(k string) bool { _, ok := q[k]; return ok }
	copySrc := r.Header.Get("X-Amz-Copy-Source") != ""
	switch {
	case r.Method == http.MethodPut && key != "":
		for _, sub := range []string{"tagging", "acl", "retention", "legal-hold"} {
			if has(sub) {
				return opOther, bucket, key
			}
		}
		switch {
		case has("uploadId") && copySrc:
			return opPartCopy, bucket, key
		case has("uploadId"):
			return opPart, bucket, key
		case copySrc:
			return opCopy, bucket, key
		}
		return opPut, bucket, key
	case r.Method == http.MethodPost && key != "":
		if has("uploads") {
			return opCreate, bucket, key
		}
		if has("uploadId") {
			return opComplete, bucket, key
		}
	case r.Method == http.MethodPost && !has("delete"):
		return opPostForm, bucket, ""
	}
	return opOther, bucket, key
}

// requestSize is the object bytes a request carries (-1: unknown).
func requestSize(r *http.Request) int64 {
	if d, err := strconv.ParseInt(r.Header.Get("X-Amz-Decoded-Content-Length"), 10, 64); err == nil {
		return d // aws-chunked: the body carries chunk signatures too
	}
	return r.ContentLength
}

// sizeLimit is the cap for an upload: the bucket's maxFileSize or the URL's
// MaxSizeParam, the smaller one (0: none).
func sizeLimit(b *bucketMeta, q url.Values) int64 {
	limit := b.MaxFileSize
	if v, err := strconv.ParseInt(q.Get(MaxSizeParam), 10, 64); err == nil && v > 0 && (limit == 0 || v < limit) {
		limit = v
	}
	return limit
}

// typeAllowed matches a Content-Type against MIME globs ("image/*").
func typeAllowed(globs []string, contentType string) bool {
	if len(globs) == 0 {
		return true
	}
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	for _, g := range globs {
		g = strings.ToLower(g)
		if g == mt || (strings.HasSuffix(g, "/*") && strings.HasPrefix(mt, strings.TrimSuffix(g, "*"))) {
			return true
		}
	}
	return false
}

// rejection is an S3 error the front answers with instead of proxying.
type rejection struct {
	status int
	code   string
	msg    string
}

// checkRules applies a bucket's maxFileSize and allowedTypes (and a URL's
// size cap) to one request.
func checkRules(op s3Op, b *bucketMeta, r *http.Request, size int64) *rejection {
	limit := sizeLimit(b, r.URL.Query())
	tooBig := func(what string) *rejection {
		if limit > 0 && size < 0 {
			return &rejection{http.StatusLengthRequired, "MissingContentLength", "uploads to this bucket need a Content-Length"}
		}
		if limit > 0 && size > limit {
			return &rejection{http.StatusRequestEntityTooLarge, "EntityTooLarge",
				fmt.Sprintf("%s is %s; the most this upload may be is %s", what, HumanBytes(size), HumanBytes(limit))}
		}
		return nil
	}
	badType := func() *rejection {
		ct := r.Header.Get("Content-Type")
		if typeAllowed(b.AllowedTypes, ct) {
			return nil
		}
		if ct == "" {
			ct = "none"
		}
		return &rejection{http.StatusUnsupportedMediaType, "InvalidContentType",
			fmt.Sprintf("Content-Type %s is not allowed in bucket %s; allowed: %s", ct, b.Name, strings.Join(b.AllowedTypes, ", "))}
	}
	switch op {
	case opPut:
		if rej := tooBig("the file"); rej != nil {
			return rej
		}
		return badType()
	case opPart:
		return tooBig("the part")
	case opCreate:
		return badType()
	case opPostForm:
		if b.MaxFileSize > 0 || len(b.AllowedTypes) > 0 {
			return &rejection{http.StatusForbidden, "AccessDenied",
				"bucket " + b.Name + " limits file sizes or types, which form uploads cannot be checked against; upload with a presigned PUT (createUpload in @shiptiffin/sdk/storage)"}
		}
	}
	return nil
}

func (f *frontServer) serveS3(w http.ResponseWriter, r *http.Request) {
	op, bucket, key := classify(r)
	var b *bucketMeta
	if bucket != "" {
		// One record, read directly: not every bucket's per request.
		if mt, _ := getMeta(r.Context(), f.p, bucket); mt != nil && S3Name(mt.Project, mt.Name) == bucket {
			b = mt
		}
	}
	if f.cors(w, r, b) {
		return
	}
	if b == nil || op == opOther {
		f.proxy.ServeHTTP(w, r) // not ours to judge: the gateway decides
		return
	}
	size := requestSize(r)
	refuse := func(rej *rejection) {
		// Read some of the body first: a browser still sending it sees a
		// reset connection, not the answer, if the server stops reading.
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 8<<20))
		writeS3Error(w, rej.status, rej.code, rej.msg)
	}
	var done func(ok bool, delta int64)
	var delta int64
	switch op {
	case opPut, opPart, opPostForm, opCopy, opPartCopy:
		n := size // -1: unknown, refused when a limit applies
		switch op {
		case opCopy, opPartCopy:
			n = 0 // the size is the source's; a held project is still refused
		case opPut:
			if n > 0 {
				n -= objectSize(f.p, bucket, key) // a replaced object frees its own size
			}
		}
		delta = max(n, 0)
		if op == opPut && size > 0 {
			delta = n // may be negative: a smaller replacement
		}
		var what, fix string
		if done, what, fix = f.m.admit(r.Context(), f.p, b.Project, bucket, n); what != "" {
			refuse(&rejection{http.StatusForbidden, "QuotaExceeded", strings.TrimSpace(what + " " + fix)})
			return
		}
	}
	ok := false
	if done != nil {
		defer func() { done(ok, delta) }()
	}
	if rej := checkRules(op, b, r, size); rej != nil {
		refuse(rej)
		return
	}
	if op == opComplete {
		if rej := f.checkComplete(r, b, bucket, key); rej != nil {
			refuse(rej)
			return
		}
	}
	rec := &statusRecorder{ResponseWriter: w}
	f.proxy.ServeHTTP(rec, r)
	if rec.status >= 300 {
		return
	}
	ok = true
	switch op {
	case opPut, opCopy, opComplete:
		f.m.objectCreated(b, bucket, key, rec.Header().Get("X-Amz-Request-Id"))
	}
}

// checkComplete refuses to complete a multipart upload whose parts add up
// to more than the cap, and aborts it so its parts do not linger. It acts
// (as root: listing the parts, aborting) only on a request signed with the
// bucket owner's own key: anything else goes to the gateway, which refuses
// it, so nobody else can make the box inspect or abort an upload.
func (f *frontServer) checkComplete(r *http.Request, b *bucketMeta, bucket, key string) *rejection {
	ctx, q := r.Context(), r.URL.Query()
	limit := sizeLimit(b, q)
	if limit == 0 {
		return nil
	}
	c, ok, err := credsFor(ctx, f.p, b.Project, false)
	if err != nil || !ok || !verifySigV4(r, c, time.Now()) {
		return nil
	}
	gw, err := f.m.gateway(f.p)
	if err != nil {
		return &rejection{http.StatusServiceUnavailable, "ServiceUnavailable", err.Error()}
	}
	total, err := gw.partsSize(ctx, bucket, key, q.Get("uploadId"))
	if err != nil {
		return nil // let the gateway answer (NoSuchUpload, ...)
	}
	if total <= limit {
		return nil
	}
	_ = gw.abortUpload(ctx, bucket, key, q.Get("uploadId"))
	return &rejection{http.StatusRequestEntityTooLarge, "EntityTooLarge",
		fmt.Sprintf("the parts add up to %s; the most this upload may be is %s. The upload was aborted", HumanBytes(total), HumanBytes(limit))}
}
