package runtime

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/sdkpkg"
)

// Next.js apps get the box's adapter: Railpack builds in /app, so the
// adapter written to .tiffin/next in the build context is at nextAdapterPath
// in the image, and the image's own env points Next.js at it (at build and
// at `next start`). Next.js before 16.2 ignores the variable.
const (
	nextDir         = ".tiffin/next"
	nextAdapterPath = "/app/" + nextDir + "/adapter.js"
	nextAdapterEnv  = "NEXT_ADAPTER_PATH"
	// nextKeyEnv keeps Server Actions working across deploys: Next.js
	// derives action IDs from it and encrypts closed-over values with it.
	nextKeyEnv = "NEXT_SERVER_ACTIONS_ENCRYPTION_KEY"
	// nextImageCache is where `next start` keeps optimized images; each app
	// environment mounts one host directory there, kept across deploys.
	nextImageCache = "/app/.next/cache/images"
	// nextImageCacheBytes caps that directory (Next.js evicts the least used).
	nextImageCacheBytes = 512 << 20
)

//go:embed nextadapter.js
var nextAdapterJS string

// nextBox is what the adapter is told about this build.
type nextBox struct {
	DeploymentID    string `json:"deploymentId"`
	Cache           bool   `json:"cache"`
	ImageCacheBytes int64  `json:"imageCacheBytes"`
}

// writeNextAdapter writes the adapter (and, for the shared cache, the
// @shiptiffin/sdk cache handlers) into the build context.
func writeNextAdapter(srcDir string, box nextBox) error {
	dir := filepath.Join(srcDir, nextDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	cfg, err := json.Marshal(box)
	if err != nil {
		return err
	}
	files := map[string][]byte{
		"adapter.js":   []byte(strings.Replace(nextAdapterJS, "/*BOX*/ {}", string(cfg), 1)),
		"package.json": []byte(`{"type":"module"}` + "\n"), // ESM whatever the app's own package.json says
	}
	if box.Cache {
		hs, err := sdkpkg.NextCacheHandlers()
		if err != nil {
			return err
		}
		for k, v := range hs {
			files[k] = v
		}
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			return err
		}
	}
	return keepInBuild(srcDir, nextDir)
}

// keepInBuild makes sure a .dockerignore that leaves out dot-directories
// does not leave out dir, which the box wrote (Railpack applies it to the
// build context).
func keepInBuild(srcDir, dir string) error {
	di := filepath.Join(srcDir, ".dockerignore")
	if raw, err := os.ReadFile(di); err == nil {
		raw = append(raw, "\n!.tiffin\n!"+dir+"\n!"+dir+"/**\n"...)
		return os.WriteFile(di, raw, 0o644)
	}
	return nil
}

// setDeployEnv adds env vars to the image a Railpack plan builds.
func setDeployEnv(planPath string, env map[string]string) error {
	raw, err := os.ReadFile(planPath)
	if err != nil {
		return err
	}
	var plan map[string]any
	if err := json.Unmarshal(raw, &plan); err != nil {
		return err
	}
	deploy, _ := plan["deploy"].(map[string]any)
	if deploy == nil {
		deploy = map[string]any{}
		plan["deploy"] = deploy
	}
	vars, _ := deploy["variables"].(map[string]any)
	if vars == nil {
		vars = map[string]any{}
		deploy["variables"] = vars
	}
	for k, v := range env {
		vars[k] = v
	}
	out, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(planPath, out, 0o644)
}

// nextVersionRe reads the version a package.json range starts from:
// "16.3.8", "^15.1.0", "~16.1", ">=14".
var nextVersionRe = regexp.MustCompile(`^\s*(?:[\^~]|>=?)?\s*v?(\d+)(?:\.(\d+))?`)

// nextBefore162 reports the app's Next.js version when package.json pins one
// older than 16.2 (tags like "latest" or "canary" are taken to be new).
func nextBefore162(dir string) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return "", false
	}
	var pj struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if json.Unmarshal(raw, &pj) != nil {
		return "", false
	}
	m := nextVersionRe.FindStringSubmatch(pj.Dependencies["next"])
	if m == nil {
		return "", false
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	v := strings.TrimSpace(pj.Dependencies["next"])
	return v, major < 16 || (major == 16 && minor < 2)
}

// prepareNext wires a Next.js build to the box: the adapter, and the env
// that points Next.js at it during the build. It returns the env the image
// needs at run time (nil when the app brings its own adapter).
func prepareNext(req BuildRequest, env map[string]string) map[string]string {
	if req.Env[nextAdapterEnv] != "" {
		fmt.Fprintf(req.Log, "==> Next.js: the app sets %s, so the box's adapter stays out\n", nextAdapterEnv)
		return nil
	}
	if v, old := nextBefore162(req.appDir()); old {
		w := "Next.js " + v + " is older than 16.2, which the box's adapter needs: this app gets no deploymentId (a tab left open across a deploy can break), " +
			"no cache shared between its copies, and it compresses its own responses. Upgrade next to 16.2 or later."
		fmt.Fprintf(req.Log, "==> warning: %s\n", w)
		req.Deploy.Warnings = append(req.Deploy.Warnings, w)
	}
	box := nextBox{DeploymentID: req.Deploy.ID, Cache: req.NextCache, ImageCacheBytes: nextImageCacheBytes}
	if err := writeNextAdapter(req.SrcDir, box); err != nil {
		fmt.Fprintf(req.Log, "==> Next.js: could not add the box's adapter (%v); building without it\n", err)
		return nil
	}
	env[nextAdapterEnv] = nextAdapterPath
	cache := "Next.js's own cache in each instance"
	if box.Cache {
		cache = "one shared cache in Valkey"
	}
	fmt.Fprintf(req.Log, "==> Next.js: the box's adapter sets deploymentId %s and %s, leaves compression to the edge (Next.js 16.2+; next.config wins where it sets these)\n", box.DeploymentID, cache)
	return map[string]string{nextAdapterEnv: nextAdapterPath}
}

// nextOrigin tells Next.js where the app is served, at build and run time,
// through the variables it reads on Vercel: without them it resolves
// og:image, twitter:image and other file-based metadata images against
// http://localhost:$PORT unless the app sets metadataBase. Production gets
// VERCEL_PROJECT_PRODUCTION_URL; a preview gets its own URL in
// VERCEL_BRANCH_URL with VERCEL_ENV=preview, which is how Next.js picks it.
// VERCEL and VERCEL_URL stay unset: libraries take those to mean the app
// runs on Vercel. The app's own values win.
func nextOrigin(env map[string]string, prodURL, ownURL string, preview bool) {
	set := func(k, raw string) {
		// The host only: Next.js adds basePath to the paths itself.
		if u, err := url.Parse(raw); err == nil && u.Scheme == "https" && u.Host != "" && env[k] == "" {
			env[k] = u.Host
		}
	}
	set("VERCEL_PROJECT_PRODUCTION_URL", prodURL)
	if preview && env["VERCEL_ENV"] == "" && env["VERCEL_BRANCH_URL"] == "" {
		set("VERCEL_BRANCH_URL", ownURL)
		if env["VERCEL_BRANCH_URL"] != "" {
			env["VERCEL_ENV"] = "preview"
		}
	}
}

// nextImagePath is Next.js's image optimizer, which next/image's default
// loader points every image at (/_next/image?url=<src>&w=&q=).
const nextImagePath = "/_next/image"

// Widths and qualities the box's image transforms take (Next.js's defaults).
var (
	transformWidths    = []int{16, 32, 48, 64, 96, 128, 256, 384, 640, 750, 828, 1080, 1200, 1920, 2048, 3840}
	transformQualities = []int{50, 75, 90, 100}
)

// fileServer is the storage module's side of files.<domain>.
type fileServer interface {
	ServeFile(w http.ResponseWriter, r *http.Request)
}

// findFileServer is the storage module, if it is in this build.
var findFileServer = func() fileServer {
	for _, mod := range platform.Modules() {
		if f, ok := mod.(fileServer); ok {
			return f
		}
	}
	return nil
}

// serveBucketImage answers next/image optimizer requests for files in the
// project's own buckets (files.<domain>/<project>/...) with the box's image
// transforms, and reports whether it did. The app's optimizer cannot fetch
// them: they resolve to the box itself, a private address Next.js refuses,
// and a local box's port is not one the app can reach. The box resizes and
// caches them on disk, outside the app's memory. Other images go to the app.
func (r *rt) serveBucketImage(w http.ResponseWriter, req *http.Request, st *AppState, prefix string) bool {
	if (req.Method != http.MethodGet && req.Method != http.MethodHead) || strings.TrimPrefix(req.URL.Path, prefix) != nextImagePath {
		return false
	}
	q := req.URL.Query()
	src, err := url.Parse(q.Get("url"))
	if err != nil || !src.IsAbs() {
		return false
	}
	files, err := url.Parse(r.p.URL(r.p.Host("files")))
	if err != nil || src.Scheme != files.Scheme || !strings.EqualFold(src.Host, files.Host) || !strings.HasPrefix(src.Path, "/"+st.Project+"/") {
		return false
	}
	fsrv := findFileServer()
	if fsrv == nil {
		return false
	}
	width, _ := strconv.Atoi(q.Get("w"))
	quality, err := strconv.Atoi(q.Get("q"))
	if err != nil {
		quality = 75
	}
	tq := src.Query() // a signed URL keeps its signature
	tq.Set("w", strconv.Itoa(atLeast(transformWidths, width)))
	tq.Set("q", strconv.Itoa(nearest(transformQualities, quality)))
	tq.Set("f", "original")
	if strings.Contains(req.Header.Get("Accept"), "image/webp") {
		tq.Set("f", "webp") // Next.js's default format
	}
	out := req.Clone(req.Context())
	out.Host = files.Host
	out.URL = &url.URL{Path: src.Path, RawQuery: tq.Encode()}
	out.RequestURI = ""
	w.Header().Set("Vary", "Accept")
	fsrv.ServeFile(w, out)
	return true
}

// atLeast is the smallest of sorted ns that is at least n (else the largest).
func atLeast(ns []int, n int) int {
	for _, v := range ns {
		if v >= n {
			return v
		}
	}
	return ns[len(ns)-1]
}

// nearest is the element of ns closest to n.
func nearest(ns []int, n int) int {
	best := ns[0]
	for _, v := range ns {
		if abs(v-n) < abs(best-n) {
			best = v
		}
	}
	return best
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// hasService reports whether a project has a service (valkey, postgres).
func (r *rt) hasService(ctx context.Context, project, name string) bool {
	_, res, err := r.p.DB.Load(ctx, project)
	if err != nil {
		return false
	}
	_, ok := res[change.KindService+"/"+name]
	return ok
}

const nsNextKeys = "runtime/next-keys"

// nextActionsKey is the Server Actions key of an app: the app's own
// (env or secret) when it sets one, otherwise one the box made for it once
// and keeps, sealed. Previews share their app's key.
func (r *rt) nextActionsKey(ctx context.Context, project, app string, env map[string]string) (string, error) {
	if v := env[nextKeyEnv]; v != "" {
		return v, nil
	}
	r.keyMu.Lock()
	defer r.keyMu.Unlock()
	k := project + "/" + app
	sealed, ok, err := r.p.DB.KVGet(ctx, nsNextKeys, k)
	if err != nil {
		return "", err
	}
	if ok {
		plain, err := r.p.Secrets.Open(sealed)
		return string(plain), err
	}
	b := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	key := base64.StdEncoding.EncodeToString(b)
	if sealed, err = r.p.Secrets.Seal([]byte(key)); err != nil {
		return "", err
	}
	return key, r.p.DB.KVPut(ctx, nsNextKeys, k, sealed)
}

// forgetNextKeys drops a destroyed project's Server Actions keys.
func (r *rt) forgetNextKeys(ctx context.Context, project string) error {
	kv, err := r.p.DB.KVList(ctx, nsNextKeys)
	if err != nil {
		return err
	}
	for k := range kv {
		if strings.HasPrefix(k, project+"/") {
			if err := r.p.DB.KVDelete(ctx, nsNextKeys, k); err != nil {
				return err
			}
		}
	}
	return nil
}

// nextCacheDir is the host directory an app environment's instances keep
// optimized images in.
func (r *rt) nextCacheDir(project, app, preview string) string {
	return filepath.Join(r.opt.DataDir, "next-cache", project, app, envDirName(preview), "images")
}

// envDirName names an app environment's directories: prod or pr-<preview>.
func envDirName(preview string) string {
	if preview == "" {
		return "prod"
	}
	return "pr-" + preview
}
