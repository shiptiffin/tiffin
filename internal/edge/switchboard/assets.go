package switchboard

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Client assets: the hashed JS and CSS a server app's pages load. The
// control plane copies them out of each built image (Env.Assets/<release>,
// laid out by URL path under www, described by MetaFile and compressed with
// Precompress); the switchboard serves them from the live release and, for
// hashed files, from the releases retired in the last AssetsKept as well: a
// page loaded before a deploy keeps finding its chunks after it, though the
// new release no longer has them. A request for anything else, or for a
// file no release has, goes to the app.

// AssetDir is one client-asset directory of a build, served at Path.
type AssetDir struct {
	Dir  string `json:"dir" doc:"Directory in the build, relative to the app"`
	Path string `json:"path" doc:"URL path its files are served at"`
	// Immutable are URL prefixes whose files have content hashes in their
	// names: cached for a year, and kept for pages of earlier releases. An
	// entry starting with "!" is a path under one of them that keeps its
	// name across releases (Nuxt's /_nuxt/builds/latest.json): revalidated,
	// and the live release's only.
	Immutable []string `json:"immutable,omitempty" doc:"URL prefixes of files named by content hash: cached for a year, and still served for pages of the previous release"`
	// LiveOnly: files that keep their names across releases (Next.js's
	// public/), served from the live release only.
	LiveOnly bool `json:"liveOnly,omitempty" doc:"Served from the live release only (files that keep their names across releases)"`
	// Pages: the .html files in it are pages the framework prerendered,
	// which its own server answers from the files before any app code runs
	// (Astro, SvelteKit, Nuxt, React Router, TanStack Start). The box
	// answers them at their paths (/about for about/index.html or
	// about.html) from the live release, so a sleeping app need not wake.
	Pages bool `json:"pages,omitempty" doc:"Its .html files are prerendered pages, answered at their paths without the app"`
}

// AssetsKept is how long an earlier release's hashed files stay served.
const AssetsKept = 24 * time.Hour

// MetaFile is a release's list of AssetDirs, next to its www directory.
const MetaFile = "assets.json"

// PagesFile maps the paths of a release's prerendered pages to their files
// in www ("/about" → "about/index.html"), next to MetaFile. The runtime
// writes it (see AssetDir.Pages); the edge serves exact matches only, so
// it never looks for files on a request it passes to the app.
const PagesFile = "pages.json"

// releaseMeta is what serving needs from one release: its MetaFile and
// PagesFile.
type releaseMeta struct {
	dirs  []AssetDir
	pages map[string]string
}

// assetMeta reads (once) what serving needs from one release.
func (b *Board) assetMeta(dir string) releaseMeta {
	b.metaMu.Lock()
	m, ok := b.metas[dir]
	b.metaMu.Unlock()
	if ok {
		return m
	}
	raw, err := os.ReadFile(filepath.Join(dir, MetaFile))
	if err != nil {
		return m // not extracted (yet): asked again next time
	}
	_ = json.Unmarshal(raw, &m.dirs)
	if raw, err := os.ReadFile(filepath.Join(dir, PagesFile)); err == nil {
		_ = json.Unmarshal(raw, &m.pages)
	}
	b.metaMu.Lock()
	b.metas[dir] = m
	b.metaMu.Unlock()
	return m
}

// ServeAsset answers a GET or HEAD from the client assets of the app
// environment's releases, and reports whether it did. prefix is the path
// the app's route adds (an app served at example.com/shop sees /shop/...).
func (b *Board) ServeAsset(w http.ResponseWriter, req *http.Request, st *Env, prefix string) bool {
	if st == nil || st.Live == "" || st.Assets == "" || (req.Method != http.MethodGet && req.Method != http.MethodHead) {
		return false
	}
	p := strings.TrimPrefix(req.URL.Path, prefix)
	if p == "" && prefix != "" {
		p = "/"
	}
	if p == "" || p[0] != '/' {
		return false
	}
	if clean := path.Clean(p); clean != p && clean+"/" != p {
		return false
	}
	// A prerendered page: the live release's only, revalidated on every use.
	live := filepath.Join(st.Assets, st.Live)
	if f, ok := b.assetMeta(live).pages[p]; ok && serveFile(w, req, filepath.Join(live, "www"), "/"+f, false) {
		return true
	}
	if strings.HasSuffix(p, "/") {
		return false
	}
	releases := []string{st.Live}
	for _, rt := range st.Retired {
		if time.Since(rt.At) < AssetsKept {
			releases = append(releases, rt.Deploy)
		}
	}
	for i, id := range releases {
		dir := filepath.Join(st.Assets, id)
		hashed, bridged := assetClass(b.assetMeta(dir).dirs, p)
		if i > 0 && !bridged {
			continue // only hashed files outlive their release
		}
		if serveFile(w, req, filepath.Join(dir, "www"), p, hashed) {
			return true
		}
	}
	return false
}

// assetClass says whether p is a hashed file (immutable) and whether pages
// of a release that is no longer live may still load it: hashed files, and
// everything under an app's own `assets` setting (which says nothing about
// hashes).
func assetClass(meta []AssetDir, p string) (hashed, bridged bool) {
	for _, a := range meta {
		for _, pre := range a.Immutable {
			if not, ok := strings.CutPrefix(pre, "!"); ok && strings.HasPrefix(p, not) {
				return false, false
			}
		}
	}
	for _, a := range meta {
		for _, pre := range a.Immutable {
			if !strings.HasPrefix(pre, "!") && strings.HasPrefix(p, pre) {
				return true, true
			}
		}
		if len(a.Immutable) == 0 && !a.LiveOnly && (a.Path == "/" || strings.HasPrefix(p, a.Path+"/")) {
			bridged = true
		}
	}
	return false, bridged
}

// Text assets are compressed once, when they are copied out, at the highest
// levels (too slow for the edge to do per response): a zstd and a gzip copy
// next to each file (.zst, .gz). There is no Brotli encoder in the box.
var (
	compressible   = map[string]bool{".js": true, ".mjs": true, ".cjs": true, ".css": true, ".html": true, ".htm": true, ".json": true, ".map": true, ".svg": true, ".txt": true, ".xml": true, ".webmanifest": true, ".wasm": true, ".ttf": true, ".otf": true, ".ico": true}
	encodings      = []struct{ name, ext string }{{"zstd", ".zst"}, {"gzip", ".gz"}}
	minCompressLen = int64(1024)
)

// Precompress writes the compressed copies of the text files in dir, on
// every CPU, and reports how many files it compressed. A copy that saves
// less than a tenth is not kept.
func Precompress(dir string) (int, error) {
	zw, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return 0, err
	}
	defer zw.Close()
	var files []string
	err = filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err == nil && e.Type().IsRegular() && compressible[strings.ToLower(filepath.Ext(p))] {
			files = append(files, p)
		}
		return err
	})
	if err != nil {
		return 0, err
	}
	var (
		next     atomic.Int64
		done     atomic.Int64
		firstErr error
		errOnce  sync.Once
		wg       sync.WaitGroup
	)
	for range min(runtime.GOMAXPROCS(0), max(1, len(files))) {
		wg.Go(func() {
			for {
				i := int(next.Add(1)) - 1
				if i >= len(files) {
					return
				}
				ok, err := precompressFile(zw, files[i])
				if err != nil {
					errOnce.Do(func() { firstErr = err })
					return
				}
				if ok {
					done.Add(1)
				}
			}
		})
	}
	wg.Wait()
	return int(done.Load()), firstErr
}

// precompressFile writes p's zstd and gzip copies (zw is safe for
// concurrent EncodeAll), and reports whether it kept one.
func precompressFile(zw *zstd.Encoder, p string) (bool, error) {
	raw, err := os.ReadFile(p)
	if err != nil || int64(len(raw)) <= minCompressLen {
		return false, err
	}
	var gz bytes.Buffer
	gw, _ := gzip.NewWriterLevel(&gz, gzip.BestCompression)
	_, _ = gw.Write(raw)
	_ = gw.Close()
	kept := false
	for _, out := range []struct {
		ext string
		b   []byte
	}{{".zst", zw.EncodeAll(raw, nil)}, {".gz", gz.Bytes()}} {
		if len(out.b) < len(raw)*9/10 {
			if err := os.WriteFile(p+out.ext, out.b, 0o644); err != nil {
				return kept, err
			}
			kept = true
		}
	}
	return kept, nil
}

// accepts reports whether an Accept-Encoding header allows coding.
func accepts(header, coding string) bool {
	for _, part := range strings.Split(header, ",") {
		name, params, _ := strings.Cut(part, ";")
		if !strings.EqualFold(strings.TrimSpace(name), coding) {
			continue
		}
		q := strings.TrimSpace(params)
		if v, ok := strings.CutPrefix(q, "q="); ok {
			f, err := strconv.ParseFloat(v, 64)
			return err == nil && f > 0
		}
		return true
	}
	return false
}

var extraTypes = map[string]string{".cjs": "text/javascript; charset=utf-8", ".woff2": "font/woff2", ".woff": "font/woff", ".ttf": "font/ttf", ".otf": "font/otf",
	".map": "application/json", ".ico": "image/x-icon", ".txt": "text/plain; charset=utf-8", ".webmanifest": "application/manifest+json"}

// serveFile serves www/p if it is a regular file inside www (symlinks may
// not leave it), as its compressed copy when the client takes one.
func serveFile(w http.ResponseWriter, req *http.Request, www, p string, hashed bool) bool {
	root, err := os.OpenRoot(www)
	if err != nil {
		return false
	}
	defer root.Close()
	name := strings.TrimPrefix(p, "/")
	f, err := root.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	h := w.Header()
	ext := strings.ToLower(path.Ext(p))
	if t := extraTypes[ext]; t != "" {
		h.Set("Content-Type", t)
	} else if t := mime.TypeByExtension(ext); t != "" {
		h.Set("Content-Type", t)
	}
	tag := ""
	if compressible[ext] && fi.Size() > minCompressLen {
		h.Add("Vary", "Accept-Encoding")
		for _, enc := range encodings {
			if !accepts(req.Header.Get("Accept-Encoding"), enc.name) {
				continue
			}
			cf, err := root.Open(name + enc.ext)
			if err != nil {
				continue
			}
			defer cf.Close()
			if cfi, err := cf.Stat(); err == nil && cfi.Mode().IsRegular() {
				// The edge leaves a response with a Content-Encoding as it is.
				h.Set("Content-Encoding", enc.name)
				f, fi, tag = cf, cfi, "-"+enc.name
				break
			}
		}
	}
	h.Set("ETag", fmt.Sprintf(`"%x-%x%s"`, fi.ModTime().UnixNano(), fi.Size(), tag))
	if hashed {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "public, max-age=0, must-revalidate")
	}
	http.ServeContent(w, req, fi.Name(), fi.ModTime(), f)
	return true
}

// notPages are prerendered files a framework's server does not answer at
// their own path: error pages and SPA shells, which it sends for other
// paths (with their own status).
var notPages = map[string]bool{"404.html": true, "500.html": true, "200.html": true, "_shell.html": true, "__spa-fallback.html": true}

// Pages finds the prerendered pages under www of the dirs with Pages set:
// the path each .html file answers at, as the frameworks' own servers do
// (/ for index.html, /a and /a/ for a/index.html, /a for a.html; a.html
// wins /a), mapped to the file relative to www. Files under an immutable
// prefix are not pages.
func Pages(www string, dirs []AssetDir) map[string]string {
	pages := map[string]string{}
	for _, a := range dirs {
		if !a.Pages {
			continue
		}
		root := filepath.Join(www, filepath.FromSlash(strings.TrimPrefix(a.Path, "/")))
		_ = filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
			if err != nil || !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".html") || notPages[e.Name()] {
				return nil
			}
			rel, err := filepath.Rel(www, p)
			if err != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if hashed, _ := assetClass(dirs, "/"+rel); hashed {
				return nil
			}
			if dir, ok := strings.CutSuffix(rel, "index.html"); ok && (dir == "" || strings.HasSuffix(dir, "/")) {
				url := "/" + dir
				pages[url] = rel
				if dir != "" {
					if _, taken := pages[strings.TrimSuffix(url, "/")]; !taken {
						pages[strings.TrimSuffix(url, "/")] = rel
					}
				}
				return nil
			}
			pages["/"+strings.TrimSuffix(rel, ".html")] = rel
			return nil
		})
	}
	return pages
}
