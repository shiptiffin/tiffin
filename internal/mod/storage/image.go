package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Images in buckets can be resized and re-encoded on the fly:
// files.<domain>/<project>/<bucket>/<key>?w=640&q=75&f=webp. The engine is
// libvips' command-line tool, installed at provision time: tiffin is a
// static, CGO-free binary, so linking libvips (or any codec) is not an
// option, and pure-Go encoders for WebP and AVIF do not exist. A process
// per transform also isolates the decoders: it runs as nobody, one thread,
// at low priority, under a time limit. Results are cached on disk by the
// object's ETag and the parameters, so a transform runs once per object
// version and size; the cache drops the least recently used files past
// its cap.

var (
	// imageWidths are the widths w may take: Next.js's default deviceSizes
	// and imageSizes, so next/image with the @shiptiffin/sdk loader always fits.
	imageWidths = []int{16, 32, 48, 64, 96, 128, 256, 384, 640, 750, 828, 1080, 1200, 1920, 2048, 3840}
	// imageQualities are the qualities q may take.
	imageQualities = []int{50, 75, 90, 100}
	// imageSources are the stored types that can be transformed, with the
	// extension "original" output keeps.
	imageSources = map[string]string{"image/jpeg": "jpg", "image/png": "png", "image/webp": "webp", "image/avif": "avif", "image/gif": "gif"}
	imageTypes   = map[string]string{"jpg": "image/jpeg", "png": "image/png", "webp": "image/webp", "avif": "image/avif", "gif": "image/gif"}
)

const (
	imageDefaultQuality = 75
	// imageMaxSource is the largest stored image that is transformed.
	imageMaxSource = 50 << 20
	// imageCacheMax is the default cap of the transform cache.
	imageCacheMax = 2 << 30
	imageTimeout  = 30 * time.Second
)

// imageParams is a validated transform request.
type imageParams struct {
	Width   int    // 0: keep the width
	Quality int    // one of imageQualities
	Format  string // "webp", "avif" or "original"
}

// parseImageParams reads w, q and f. ok is false when the request asks for
// no transform (none of them is set).
func parseImageParams(q url.Values) (imageParams, bool, error) {
	p := imageParams{Quality: imageDefaultQuality, Format: "original"}
	if !q.Has("w") && !q.Has("q") && !q.Has("f") {
		return p, false, nil
	}
	if v := q.Get("w"); v != "" || q.Has("w") {
		w, err := strconv.Atoi(v)
		if err != nil || !slices.Contains(imageWidths, w) {
			return p, true, fmt.Errorf("w must be one of %s", joinInts(imageWidths))
		}
		p.Width = w
	}
	if v := q.Get("q"); v != "" || q.Has("q") {
		n, err := strconv.Atoi(v)
		if err != nil || !slices.Contains(imageQualities, n) {
			return p, true, fmt.Errorf("q must be one of %s", joinInts(imageQualities))
		}
		p.Quality = n
	}
	if v := q.Get("f"); v != "" || q.Has("f") {
		if v != "webp" && v != "avif" && v != "original" {
			return p, true, errors.New("f must be webp, avif or original")
		}
		p.Format = v
	}
	return p, true, nil
}

func joinInts(ns []int) string {
	s := make([]string, len(ns))
	for i, n := range ns {
		s[i] = strconv.Itoa(n)
	}
	return strings.Join(s, ", ")
}

// outputExt is the file extension (and so the type) a transform produces.
func (ip imageParams) outputExt(srcType string) string {
	if ip.Format != "original" {
		return ip.Format
	}
	return imageSources[srcType]
}

// cacheKey identifies a transform of one version of one object.
func (ip imageParams) cacheKey(s3name, key, etag string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("v1\n%s\n%s\n%s\n%d\n%d\n%s", s3name, key, etag, ip.Width, ip.Quality, ip.Format)))
	return hex.EncodeToString(h[:])
}

// imageEngine turns src (of type srcType) into the requested image.
type imageEngine func(ctx context.Context, src []byte, srcType string, ip imageParams) ([]byte, error)

// vipsBin is libvips' command-line tool, from the libvips-tools package.
var vipsBin = sync.OnceValue(func() string {
	p, _ := exec.LookPath("vips")
	return p
})

// errNoEngine means the box has no image tool installed.
var errNoEngine = errors.New("image transforms need libvips on the box (the libvips-tools package): run `tiffin up` to provision it")

// vipsTransform runs `vips thumbnail_source` on src (stdin to stdout).
func vipsTransform(ctx context.Context, src []byte, srcType string, ip imageParams) ([]byte, error) {
	bin := vipsBin()
	if bin == "" {
		return nil, errNoEngine
	}
	ext := ip.outputExt(srcType)
	save := "." + ext
	switch ext {
	case "webp", "avif", "jpg":
		save += fmt.Sprintf("[Q=%d,keep=none]", ip.Quality)
	default:
		save += "[keep=none]"
	}
	width := ip.Width
	if width == 0 {
		width = 100000
	}
	args := []string{"-n", "10", bin, "thumbnail_source", "[descriptor=0]", save, strconv.Itoa(width), "--height", "100000", "--size", "down"}
	if srcType == "image/gif" || srcType == "image/webp" {
		args = append(args, "--option-string", "n=-1") // every frame of an animation
	}
	ctx, cancel := context.WithTimeout(ctx, imageTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nice", args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/tmp", "VIPS_CONCURRENCY=1", "VIPS_BLOCK_UNTRUSTED=1"}
	cmd.Stdin = bytes.NewReader(src)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if cred := nobody(); cred != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: cred}
	}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("the transform took longer than %s", imageTimeout)
		}
		return nil, fmt.Errorf("vips: %v: %s", err, strings.TrimSpace(errb.String()))
	}
	if out.Len() == 0 {
		return nil, fmt.Errorf("vips wrote nothing: %s", strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// nobody is the unprivileged user transforms run as (nil when tiffin is
// not root, as in tests).
var nobody = sync.OnceValue(func() *syscall.Credential {
	if os.Geteuid() != 0 {
		return nil
	}
	u, err := user.Lookup("nobody")
	if err != nil {
		return nil
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), NoSetGroups: true}
})

// imageWork bounds transforms (box-wide and per project), shares one
// transform among concurrent requests for the same result, and keeps the
// disk cache.
type imageWork struct {
	engine imageEngine
	cache  *imageCache
	slots  chan struct{} // box-wide

	mu       sync.Mutex
	projects map[string]chan struct{}
	perProj  int
	inflight map[string]*flight
}

type flight struct {
	done chan struct{}
	path string
	err  error
}

func newImageWork(engine imageEngine, dir string, capBytes int64) *imageWork {
	n := max(1, runtime.NumCPU()/2)
	return &imageWork{engine: engine, cache: &imageCache{dir: dir, max: capBytes}, slots: make(chan struct{}, n),
		projects: map[string]chan struct{}{}, perProj: max(1, n/2), inflight: map[string]*flight{}}
}

// errBusy means the box is already transforming as much as it allows.
var errBusy = errors.New("too many image transforms at once; retry shortly")

// acquire takes a box-wide and a per-project slot, waiting up to 20 s.
func (iw *imageWork) acquire(ctx context.Context, project string) (func(), error) {
	iw.mu.Lock()
	ps, ok := iw.projects[project]
	if !ok {
		ps = make(chan struct{}, iw.perProj)
		iw.projects[project] = ps
	}
	iw.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	select {
	case ps <- struct{}{}:
	case <-ctx.Done():
		return nil, errBusy
	}
	select {
	case iw.slots <- struct{}{}:
	case <-ctx.Done():
		<-ps
		return nil, errBusy
	}
	return func() { <-iw.slots; <-ps }, nil
}

// get returns the cached file for key, transforming once (fetch reads the
// source) when it is missing. hit says it came from the cache.
func (iw *imageWork) get(ctx context.Context, project, key, ext string, fetch func() ([]byte, string, error), ip imageParams) (path string, hit bool, err error) {
	if p, ok := iw.cache.get(key); ok {
		return p, true, nil
	}
	iw.mu.Lock()
	if fl, ok := iw.inflight[key]; ok {
		iw.mu.Unlock()
		select {
		case <-fl.done:
			return fl.path, false, fl.err
		case <-ctx.Done():
			return "", false, ctx.Err()
		}
	}
	fl := &flight{done: make(chan struct{})}
	iw.inflight[key] = fl
	iw.mu.Unlock()
	defer func() {
		fl.path, fl.err = path, err
		close(fl.done)
		iw.mu.Lock()
		delete(iw.inflight, key)
		iw.mu.Unlock()
	}()
	release, err := iw.acquire(ctx, project)
	if err != nil {
		return "", false, err
	}
	defer release()
	src, srcType, err := fetch()
	if err != nil {
		return "", false, err
	}
	// A detached context: the result is cached for the next request even
	// if this client goes away.
	out, err := iw.engine(context.WithoutCancel(ctx), src, srcType, ip)
	if err != nil {
		return "", false, err
	}
	path, err = iw.cache.put(key, ext, out)
	return path, false, err
}

// imageCache is a disk cache with a size cap, evicting the least recently
// used files. File times carry the use order across restarts.
type imageCache struct {
	dir string
	max int64

	mu     sync.Mutex
	loaded bool
	items  map[string]*cacheItem
	total  int64
}

type cacheItem struct {
	path string
	size int64
	used time.Time
}

func (c *imageCache) load() {
	if c.loaded {
		return
	}
	c.loaded, c.items = true, map[string]*cacheItem{}
	_ = filepath.WalkDir(c.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".tmp") {
			_ = os.Remove(p)
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		k, _, _ := strings.Cut(name, ".")
		c.items[k] = &cacheItem{path: p, size: fi.Size(), used: fi.ModTime()}
		c.total += fi.Size()
		return nil
	})
}

func (c *imageCache) get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	it, ok := c.items[key]
	if !ok {
		return "", false
	}
	if _, err := os.Stat(it.path); err != nil {
		c.total -= it.size
		delete(c.items, key)
		return "", false
	}
	now := time.Now()
	if now.Sub(it.used) > time.Minute { // keep disk writes down for hot files
		_ = os.Chtimes(it.path, now, now)
	}
	it.used = now
	return it.path, true
}

func (c *imageCache) put(key, ext string, data []byte) (string, error) {
	dir := filepath.Join(c.dir, key[:2])
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".tmp")
	if err != nil {
		return "", err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	tmp.Close()
	path := filepath.Join(dir, key+"."+ext)
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	if old, ok := c.items[key]; ok {
		c.total -= old.size
	}
	c.items[key] = &cacheItem{path: path, size: int64(len(data)), used: time.Now()}
	c.total += int64(len(data))
	c.evict(key)
	return path, nil
}

// evict drops the least recently used files until the cache fits (never keep).
func (c *imageCache) evict(keep string) {
	if c.total <= c.max {
		return
	}
	keys := make([]string, 0, len(c.items))
	for k := range c.items {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return c.items[keys[i]].used.Before(c.items[keys[j]].used) })
	for _, k := range keys {
		if c.total <= c.max {
			break
		}
		if k == keep {
			continue
		}
		_ = os.Remove(c.items[k].path)
		c.total -= c.items[k].size
		delete(c.items, k)
	}
}

// serveImage answers a transform request for an object the caller may read.
// It reports false when the object is not an image it transforms, so the
// caller serves the original.
func (f *frontServer) serveImage(w http.ResponseWriter, r *http.Request, meta *bucketMeta, s3name, key string, ip imageParams, cacheControl string) bool {
	gw, err := f.m.gateway(f.p)
	if err != nil {
		plain(w, http.StatusServiceUnavailable, err.Error())
		return true
	}
	h, err := gw.headObject(r.Context(), s3name, key)
	if err != nil {
		if errorsIsNotFound(err) || isCode(err, "NoSuchKey") {
			plain(w, http.StatusNotFound, "not found: no object "+key+" in "+meta.Project+"/"+meta.Name)
		} else {
			plain(w, http.StatusBadGateway, "the storage gateway is not answering")
		}
		return true
	}
	srcType := strings.ToLower(strings.TrimSpace(strings.Split(h.Get("Content-Type"), ";")[0]))
	if _, ok := imageSources[srcType]; !ok {
		return false // SVG, PDF, ...: served as stored
	}
	if n, _ := strconv.ParseInt(h.Get("Content-Length"), 10, 64); n > imageMaxSource {
		plain(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the image is %s; images up to %s are transformed", HumanBytes(n), HumanBytes(imageMaxSource)))
		return true
	}
	etag := strings.Trim(h.Get("ETag"), `"`)
	ck := ip.cacheKey(s3name, key, etag)
	ext := ip.outputExt(srcType)
	tag := `"` + ck[:32] + `"`
	w.Header().Set("ETag", tag)
	w.Header().Set("Cache-Control", cacheControl)
	w.Header().Set("Content-Security-Policy", filesCSP)
	if inm := r.Header.Get("If-None-Match"); inm != "" && strings.Contains(inm, tag) {
		w.Header().Set("X-Tiffin-Cache", "HIT")
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	fetch := func() ([]byte, string, error) {
		res, err := gw.do(r.Context(), http.MethodGet, s3name, key, nil, nil, http.Header{"If-Match": {`"` + etag + `"`}})
		if err != nil {
			return nil, "", err
		}
		defer res.Body.Close()
		data, err := io.ReadAll(io.LimitReader(res.Body, imageMaxSource+1))
		return data, srcType, err
	}
	path, hit, err := f.img.get(r.Context(), meta.Project, ck, ext, fetch, ip)
	switch {
	case errors.Is(err, errBusy):
		w.Header().Set("Retry-After", "2")
		plain(w, http.StatusServiceUnavailable, err.Error())
		return true
	case errors.Is(err, errNoEngine):
		plain(w, http.StatusNotImplemented, err.Error())
		return true
	case err != nil:
		f.p.Log.Warn("storage: image transform", "bucket", s3name, "key", key, "err", err)
		plain(w, http.StatusUnprocessableEntity, "this image could not be transformed")
		return true
	}
	file, err := os.Open(path)
	if err != nil {
		plain(w, http.StatusInternalServerError, "image cache unavailable")
		return true
	}
	defer file.Close()
	fi, _ := file.Stat()
	w.Header().Set("Content-Type", imageTypes[ext])
	w.Header().Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	if hit {
		w.Header().Set("X-Tiffin-Cache", "HIT")
	} else {
		w.Header().Set("X-Tiffin-Cache", "MISS")
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, file)
	}
	return true
}

// transforms reports whether images can be resized on this box.
func (m *Module) transforms() bool { return m.imgEngine != nil || vipsBin() != "" }
