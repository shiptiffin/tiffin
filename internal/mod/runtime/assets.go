package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/manifest"
)

// Client assets: the hashed JS and CSS a server app's pages load. The box
// copies them out of each built image and serves them itself, from the live
// release and, for hashed files, from the releases live in the last
// assetsKept as well: a page loaded before a deploy keeps finding its
// chunks after it, though the new release no longer has them. A request
// for anything else, or for a file no release has, goes to the app.

// AssetDir is one client-asset directory of a build, served at Path.
type AssetDir struct {
	Dir  string `json:"dir" doc:"Directory in the build, relative to the app"`
	Path string `json:"path" doc:"URL path its files are served at"`
	// Immutable are URL prefixes whose files have content hashes in their
	// names: cached for a year, and kept for pages of earlier releases.
	Immutable []string `json:"immutable,omitempty" doc:"URL prefixes of files named by content hash: cached for a year, and still served for pages of the previous release"`
	// LiveOnly: files that keep their names across releases (Next.js's
	// public/), served from the live release only.
	LiveOnly bool `json:"liveOnly,omitempty" doc:"Served from the live release only (files that keep their names across releases)"`
}

// assetsKept is how long an earlier release's hashed files stay served,
// and maxRetired how many earlier releases at most.
const (
	assetsKept = 24 * time.Hour
	maxRetired = 3
)

// assetKind is a framework whose build output the box knows, found by a
// dependency in package.json.
type assetKind struct {
	deps []string
	dirs []AssetDir
}

// knownAssets maps build outputs to where the app serves them. The first
// kind with a dependency the app has wins.
var knownAssets = []assetKind{
	{[]string{"next"}, nextAssets},
	{[]string{"nuxt"}, []AssetDir{{Dir: ".output/public", Path: "/", Immutable: []string{"/_nuxt/"}}}},
	{[]string{"@tanstack/react-start", "@tanstack/solid-start"}, []AssetDir{{Dir: ".output/public", Path: "/", Immutable: []string{"/assets/"}}}},
	{[]string{"@solidjs/start"}, []AssetDir{{Dir: ".output/public", Path: "/", Immutable: []string{"/_build/assets/"}}}},
	{[]string{"@react-router/dev", "@remix-run/dev"}, []AssetDir{{Dir: "build/client", Path: "/", Immutable: []string{"/assets/"}}}},
	{[]string{"@sveltejs/adapter-node"}, []AssetDir{{Dir: "build/client", Path: "/", Immutable: []string{"/_app/immutable/"}}}},
	{[]string{"@astrojs/node"}, []AssetDir{{Dir: "dist/client", Path: "/", Immutable: []string{"/_astro/"}}}},
}

var nextAssets = []AssetDir{
	{Dir: ".next/static", Path: "/_next/static", Immutable: []string{"/_next/static/"}},
	{Dir: "public", Path: "/", LiveOnly: true},
}

// clientAssets says which directories of a build to serve: the app's own
// `assets` setting, else what its framework's build is known to produce.
// srcDir is "" when there is no source (a prebuilt image).
func clientAssets(srcDir string, spec *manifest.App) []AssetDir {
	if spec.Assets != nil {
		return []AssetDir{{Dir: spec.Assets.Dir, Path: "/" + strings.Trim(spec.Assets.Path, "/")}}
	}
	if spec.Role == manifest.RoleWorker || spec.Framework == manifest.FrameworkStatic {
		return nil
	}
	if spec.Framework == manifest.FrameworkNext {
		return nextAssets
	}
	deps := packageDeps(srcDir)
	for _, k := range knownAssets {
		for _, d := range k.deps {
			if deps[d] {
				return k.dirs
			}
		}
	}
	return nil
}

func packageDeps(dir string) map[string]bool {
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil
	}
	var pkg struct {
		Deps    map[string]string `json:"dependencies"`
		DevDeps map[string]string `json:"devDependencies"`
	}
	_ = json.Unmarshal(raw, &pkg)
	out := map[string]bool{}
	for k := range pkg.Deps {
		out[k] = true
	}
	for k := range pkg.DevDeps {
		out[k] = true
	}
	return out
}

// assetsDir holds an app environment's extracted client assets, one
// directory per release: <id>/www is laid out by URL path.
func (r *rt) assetsDir(project, app, preview string) string {
	return filepath.Join(r.opt.DataDir, "assets", project, app, envDirName(preview))
}

// ensureAssets copies d's client assets out of its image, once. It only
// logs failures: the app then serves its assets itself, as without this.
func (r *rt) ensureAssets(ctx context.Context, d *Deploy, log io.Writer) {
	if len(d.Assets) == 0 || d.Image == "" {
		return
	}
	dir := filepath.Join(r.assetsDir(d.Project, d.App, d.Preview), d.ID)
	if exists(filepath.Join(dir, "www")) {
		return
	}
	n, size, err := r.extractAssets(ctx, d, dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		fmt.Fprintf(log, "==> client assets: could not copy them out of the image (%v); the app serves them itself\n", err)
		r.p.Log.Warn("runtime: client assets", "deploy", d.ID, "err", err)
		return
	}
	if n == 0 {
		return
	}
	var where []string
	for _, a := range d.Assets {
		where = append(where, a.Dir+" at "+a.Path)
	}
	fmt.Fprintf(log, "==> client assets: the box serves %d files (%s) from %s; pages of the previous release keep their hashed files for %s\n",
		n, humanBytes(size), strings.Join(where, ", "), assetsKept)
}

func (r *rt) extractAssets(ctx context.Context, d *Deploy, dir string) (int, int64, error) {
	tmp := dir + ".tmp"
	_ = os.RemoveAll(tmp)
	_ = os.RemoveAll(dir)
	defer os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return 0, 0, err
	}
	dirs := make([]string, len(d.Assets))
	for i, a := range d.Assets {
		dirs[i] = a.Dir
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := r.eng.CopyOut(cctx, d.Image, dirs, tmp); err != nil {
		return 0, 0, err
	}
	// Lay them out by URL path, shortest first, so "/" never lands on a
	// directory placed for a longer path.
	order := make([]int, len(d.Assets))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return len(d.Assets[order[a]].Path) < len(d.Assets[order[b]].Path) })
	www := filepath.Join(tmp, "www")
	if err := os.MkdirAll(www, 0o755); err != nil {
		return 0, 0, err
	}
	for _, i := range order {
		from := filepath.Join(tmp, strconv.Itoa(i))
		if !exists(from) {
			continue
		}
		to := filepath.Join(www, filepath.FromSlash(strings.TrimPrefix(d.Assets[i].Path, "/")))
		if to == www {
			_ = os.Remove(www)
		} else {
			_ = os.RemoveAll(to)
			if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
				return 0, 0, err
			}
		}
		if err := os.Rename(from, to); err != nil {
			return 0, 0, err
		}
	}
	meta, _ := json.Marshal(d.Assets)
	if err := os.WriteFile(filepath.Join(tmp, "assets.json"), meta, 0o644); err != nil {
		return 0, 0, err
	}
	n, size := countFiles(www)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return 0, 0, err
	}
	return n, size, os.Rename(tmp, dir)
}

// retire records that release prev stopped serving at now: its hashed
// files stay served for assetsKept. live is the release taking over.
func retire(list []Retired, prev, live string, now time.Time) []Retired {
	out := []Retired{}
	if prev != "" && prev != live {
		out = append(out, Retired{Deploy: prev, At: now})
	}
	for _, r := range list {
		if r.Deploy != prev && r.Deploy != live && now.Sub(r.At) < assetsKept && len(out) < maxRetired {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// assetMeta is what serving needs from one release's assets.json.
func (r *rt) assetMeta(dir string) []AssetDir {
	r.mu.Lock()
	m, ok := r.assetMetas[dir]
	r.mu.Unlock()
	if ok {
		return m
	}
	raw, err := os.ReadFile(filepath.Join(dir, "assets.json"))
	if err != nil {
		return nil // not extracted (yet): asked again next time
	}
	_ = json.Unmarshal(raw, &m)
	r.mu.Lock()
	r.assetMetas[dir] = m
	r.mu.Unlock()
	return m
}

// serveAsset answers a GET or HEAD from the client assets of the app
// environment's releases, and reports whether it did. prefix is the path
// the app's route adds (an app served at example.com/shop sees /shop/...).
func (r *rt) serveAsset(w http.ResponseWriter, req *http.Request, st *AppState, prefix string) bool {
	if st == nil || st.Live == "" || (req.Method != http.MethodGet && req.Method != http.MethodHead) {
		return false
	}
	p := strings.TrimPrefix(req.URL.Path, prefix)
	if p == "" || p[0] != '/' || strings.HasSuffix(p, "/") || path.Clean(p) != p {
		return false
	}
	base := r.assetsDir(st.Project, st.App, st.Preview)
	releases := []string{st.Live}
	for _, rt := range st.Retired {
		if time.Since(rt.At) < assetsKept {
			releases = append(releases, rt.Deploy)
		}
	}
	for i, id := range releases {
		dir := filepath.Join(base, id)
		meta := r.assetMeta(dir)
		hashed, bridged := assetClass(meta, p)
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
			if strings.HasPrefix(p, pre) {
				return true, true
			}
		}
		if len(a.Immutable) == 0 && !a.LiveOnly && (a.Path == "/" || strings.HasPrefix(p, a.Path+"/")) {
			bridged = true
		}
	}
	return false, bridged
}

var extraTypes = map[string]string{".woff2": "font/woff2", ".woff": "font/woff", ".ttf": "font/ttf", ".otf": "font/otf",
	".map": "application/json", ".ico": "image/x-icon", ".txt": "text/plain; charset=utf-8", ".webmanifest": "application/manifest+json"}

// serveFile serves www/p if it is a regular file inside www (symlinks may
// not leave it).
func serveFile(w http.ResponseWriter, req *http.Request, www, p string, hashed bool) bool {
	root, err := os.OpenRoot(www)
	if err != nil {
		return false
	}
	defer root.Close()
	f, err := root.Open(strings.TrimPrefix(p, "/"))
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
	h.Set("ETag", fmt.Sprintf(`"%x-%x"`, fi.ModTime().UnixNano(), fi.Size()))
	if hashed {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "public, max-age=0, must-revalidate")
	}
	http.ServeContent(w, req, fi.Name(), fi.ModTime(), f)
	return true
}

var assetDirName = regexp.MustCompile(`^dep_[0-9A-Za-z]+$`)

// pruneAssets removes client assets no release serves any more: releases
// neither live nor retired within assetsKept (left an hour, so a deploy
// copying its assets right now keeps them), and environments that are gone.
// The same goes for Next.js image caches of environments that are gone.
func (r *rt) pruneAssets(ctx context.Context) {
	states, err := r.st.allStates(ctx)
	if err != nil {
		return
	}
	keep := map[string]bool{} // env dir → whether it exists; release dir → kept
	for _, s := range states {
		env := r.assetsDir(s.Project, s.App, s.Preview)
		keep[env] = true
		keep[filepath.Join(env, s.Live)] = true
		for _, rt := range s.Retired {
			if time.Since(rt.At) < assetsKept {
				keep[filepath.Join(env, rt.Deploy)] = true
			}
		}
		keep[filepath.Dir(r.nextCacheDir(s.Project, s.App, s.Preview))] = true
	}
	recent := func(p string) bool {
		fi, err := os.Stat(p)
		return err == nil && time.Since(fi.ModTime()) < time.Hour
	}
	for _, root := range []string{filepath.Join(r.opt.DataDir, "assets"), filepath.Join(r.opt.DataDir, "next-cache")} {
		envs, _ := filepath.Glob(filepath.Join(root, "*", "*", "*"))
		for _, env := range envs {
			if !keep[env] {
				if !recent(env) { // a first deploy has no state until it is live
					r.removeDir(env)
				}
				continue
			}
			if !strings.HasSuffix(root, "assets") {
				continue
			}
			rels, _ := os.ReadDir(env)
			for _, rel := range rels {
				p := filepath.Join(env, rel.Name())
				name := strings.TrimSuffix(rel.Name(), ".tmp")
				if keep[p] || !assetDirName.MatchString(name) || recent(p) {
					continue
				}
				r.removeDir(p)
			}
		}
	}
}

func (r *rt) removeDir(dir string) {
	r.mu.Lock()
	for k := range r.assetMetas {
		if k == dir || strings.HasPrefix(k, dir+string(filepath.Separator)) {
			delete(r.assetMetas, k)
		}
	}
	r.mu.Unlock()
	if err := os.RemoveAll(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		r.p.Log.Warn("runtime: remove directory", "dir", dir, "err", err)
	}
}

// forgetFiles removes the client assets and image cache of an app
// environment (preview "" with all true: every environment of the app;
// app "" too: the whole project).
func (r *rt) forgetFiles(project, app, preview string, all bool) {
	for _, root := range []string{"assets", "next-cache"} {
		dir := filepath.Join(r.opt.DataDir, root, project)
		if app != "" {
			dir = filepath.Join(dir, app)
			if !all {
				dir = filepath.Join(dir, envDirName(preview))
			}
		}
		r.removeDir(dir)
	}
}
