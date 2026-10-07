package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/edge/switchboard"
	"github.com/btahir/tiffin/internal/manifest"
)

// Client assets: the hashed JS and CSS a server app's pages load. The box
// copies them out of each built image and the switchboard serves them
// itself (see switchboard.ServeAsset), from the live release and, for hashed
// files, from the releases live in the last assetsKept as well.

// AssetDir is one client-asset directory of a build, served at Path.
type AssetDir = switchboard.AssetDir

// assetsKept is how long an earlier release's hashed files stay served,
// and maxRetired how many earlier releases at most.
const (
	assetsKept = switchboard.AssetsKept
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
	// Nuxt's build manifest keeps its name (the client polls it for a new
	// version); its fonts (@nuxt/fonts) are named by hash.
	{[]string{"nuxt"}, []AssetDir{{Dir: ".output/public", Path: "/", Immutable: []string{"/_nuxt/", "/_fonts/", "!/_nuxt/builds/latest.json"}}}},
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
	if err := plainTree(tmp); err != nil {
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
	if err := os.WriteFile(filepath.Join(tmp, switchboard.MetaFile), meta, 0o644); err != nil {
		return 0, 0, err
	}
	n, size := countFiles(www)
	if err := switchboard.Precompress(www); err != nil {
		return 0, 0, err
	}
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
