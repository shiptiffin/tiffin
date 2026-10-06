package runtime

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/btahir/tiffin/internal/change"
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
// tiffin-sdk cache handlers) into the build context.
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

// prepareNext wires a Next.js build to the box: the adapter, and the env
// that points Next.js at it during the build. It returns the env the image
// needs at run time (nil when the app brings its own adapter).
func prepareNext(req BuildRequest, env map[string]string) map[string]string {
	if req.Env[nextAdapterEnv] != "" {
		fmt.Fprintf(req.Log, "==> Next.js: the app sets %s, so the box's adapter stays out\n", nextAdapterEnv)
		return nil
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
