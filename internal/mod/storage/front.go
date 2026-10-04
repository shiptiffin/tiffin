package storage

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/platform"
)

// frontServer is the storage front: S3 with quota checks (any host but
// files.<domain>) and public files (files.<domain>).
type frontServer struct {
	m     *Module
	p     *platform.Platform
	proxy *httputil.ReverseProxy

	mu        sync.Mutex
	listeners map[string]net.Listener
	srv       *http.Server
}

func (f *frontServer) start(ctx context.Context) error {
	gw, err := f.m.gateway(f.p)
	if err != nil {
		return err
	}
	target := &url.URL{Scheme: "http", Host: gw.addr}
	f.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			// The client signed the Host it used (s3.<domain>, or the
			// internal endpoint); keep it so the signature verifies.
			pr.Out.Host = pr.In.Host
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			writeS3Error(w, http.StatusBadGateway, "ServiceUnavailable", "the storage gateway is not answering; check `tiffin status`")
		},
	}
	f.listeners = map[string]net.Listener{}
	f.srv = &http.Server{Handler: f, ReadHeaderTimeout: 30 * time.Second}
	addr := f.m.frontAddr
	if addr == "" {
		addr = fmt.Sprintf("127.0.0.1:%d", FrontPort)
	}
	if err := f.listen(addr); err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = f.srv.Shutdown(sctx)
	}()
	// Apps may reach the box on another address (the runtime's bridge);
	// listen there too once it is known.
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			if ip := f.m.containerHost(ctx, f.p); ip != "127.0.0.1" {
				if err := f.listen(net.JoinHostPort(ip, strconv.Itoa(FrontPort))); err != nil {
					f.p.Log.Warn("storage: listen for apps", "addr", ip, "err", err)
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return nil
}

// listen adds a listener once per address.
func (f *frontServer) listen(addr string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.listeners[addr]; ok {
		return nil
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	f.listeners[addr] = ln
	go func() { _ = f.srv.Serve(ln) }()
	return nil
}

func hostOnly(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}

func (f *frontServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.EqualFold(hostOnly(r.Host), f.p.Host("files")) {
		f.serveFile(w, r)
		return
	}
	f.serveS3(w, r)
}

func writeS3Error(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_ = xml.NewEncoder(w).Encode(struct {
		XMLName xml.Name `xml:"Error"`
		Code    string   `xml:"Code"`
		Message string   `xml:"Message"`
	}{Code: code, Message: msg})
}

// bucketOf returns the path-style bucket of an S3 request.
func bucketOf(r *http.Request) string {
	b, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	return b
}

func (f *frontServer) serveS3(w http.ResponseWriter, r *http.Request) {
	bucket := bucketOf(r)
	if (r.Method == http.MethodPut || r.Method == http.MethodPost) && bucket != "" && strings.Contains(strings.TrimPrefix(r.URL.Path, "/"), "/") {
		size := r.ContentLength
		if d, err := strconv.ParseInt(r.Header.Get("X-Amz-Decoded-Content-Length"), 10, 64); err == nil {
			size = d // aws-chunked: the body carries chunk signatures too
		}
		if msg := f.overQuota(r.Context(), bucket, size); msg != "" {
			_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20))
			writeS3Error(w, http.StatusForbidden, "QuotaExceeded", msg)
			return
		}
		rec := &statusRecorder{ResponseWriter: w}
		f.proxy.ServeHTTP(rec, r)
		if rec.status < 300 {
			f.m.tracker().add(bucket, size)
		}
		return
	}
	f.proxy.ServeHTTP(w, r)
}

// overQuota returns a message when writing n more bytes to bucket is
// refused (its project is read-only or would go over its storage limit),
// else "".
func (f *frontServer) overQuota(ctx context.Context, bucket string, n int64) string {
	meta, err := allMeta(ctx, f.p)
	if err != nil {
		return ""
	}
	b, ok := meta[bucket]
	if !ok {
		return "" // not ours to judge; the gateway decides
	}
	what, fix := f.m.refusal(ctx, f.p, meta, b.Project, n)
	return strings.TrimSpace(what + " " + fix)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Flush() {
	if fl, ok := s.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

// ---- public files ----

// hashedKey matches keys that carry a content hash (app.3f9a2c1d.js,
// 9f86d081884c7d65.../photo.png): their bytes never change, so they are
// cached for a year.
var hashedKey = regexp.MustCompile(`(^|[./_-])[0-9a-fA-F]{8,}([./_-]|$)`)

// CacheControl is the Cache-Control header public files get.
func CacheControl(key string) string {
	if hashedKey.MatchString(key) {
		return "public, max-age=31536000, immutable"
	}
	return "public, max-age=300, stale-while-revalidate=86400"
}

// filesCSP sandboxes whatever a public bucket holds: an uploaded HTML file
// cannot run script against files.<domain> or read its cookies.
const filesCSP = "default-src 'none'; img-src 'self' data:; media-src 'self'; style-src 'unsafe-inline'; font-src 'self'; sandbox"

func plain(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, msg+"\n")
}

func (f *frontServer) serveFile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	switch r.Method {
	case http.MethodGet, http.MethodHead:
	case http.MethodOptions:
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD")
		w.Header().Set("Access-Control-Allow-Headers", "Range, If-None-Match, If-Modified-Since")
		w.WriteHeader(http.StatusNoContent)
		return
	default:
		w.Header().Set("Allow", "GET, HEAD")
		plain(w, http.StatusMethodNotAllowed, "public files are read-only; upload through the S3 API or a presigned URL")
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 3)
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" || strings.HasSuffix(parts[2], "/") {
		plain(w, http.StatusNotFound, "not found: public files live at /<project>/<bucket>/<key>")
		return
	}
	project, bucket, key := parts[0], parts[1], parts[2]
	s3name := S3Name(project, bucket)
	meta, err := getMeta(r.Context(), f.p, s3name)
	if err != nil {
		plain(w, http.StatusInternalServerError, "storage metadata unavailable")
		return
	}
	if meta == nil || meta.Project != project || meta.Name != bucket {
		plain(w, http.StatusNotFound, "not found: no bucket "+bucket+" in project "+project)
		return
	}
	if !meta.Public {
		plain(w, http.StatusForbidden, "forbidden: this file is not public")
		return
	}
	gw, err := f.m.gateway(f.p)
	if err != nil {
		plain(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	// Anonymous on purpose: the bucket policy is the authority for public reads.
	req, err := http.NewRequestWithContext(r.Context(), r.Method, gw.endpoint()+objectPath(s3name, key), nil)
	if err != nil {
		plain(w, http.StatusBadRequest, "bad key")
		return
	}
	for _, h := range []string{"Range", "If-None-Match", "If-Modified-Since", "If-Match", "If-Unmodified-Since", "If-Range"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	res, err := gw.client.Do(req)
	if err != nil {
		plain(w, http.StatusBadGateway, "the storage gateway is not answering")
		return
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusNotFound:
		plain(w, http.StatusNotFound, "not found: no object "+key+" in "+project+"/"+bucket)
		return
	case res.StatusCode == http.StatusForbidden:
		plain(w, http.StatusForbidden, "forbidden")
		return
	case res.StatusCode >= 400:
		plain(w, res.StatusCode, http.StatusText(res.StatusCode))
		return
	}
	for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "ETag", "Last-Modified", "Accept-Ranges",
		"Content-Encoding", "Content-Disposition", "Content-Language"} {
		if v := res.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.Header().Set("Cache-Control", CacheControl(key))
	w.Header().Set("Content-Security-Policy", filesCSP)
	w.WriteHeader(res.StatusCode)
	if r.Method != http.MethodHead {
		_, err = io.Copy(w, res.Body)
		if err != nil && !errors.Is(err, context.Canceled) {
			f.p.Log.Debug("storage: files copy", "err", err)
		}
	}
}

// HumanBytes formats a size the way messages say it: "1.5 GiB".
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
