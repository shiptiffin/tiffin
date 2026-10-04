package runtime

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/starters"
)

// warmUpKey marks the build cache as warm for these tool versions: a new
// Railpack or Bun means new frontend and toolchain layers, so it warms again.
var warmUpKey = "warmed-" + RailpackVersion + "-" + BunVersion

// warmUp builds the Next.js starter once, in the background, after the box
// first starts (tiffin up), and throws the image away. BuildKit keeps what
// the build pulled and made: Railpack's frontend, the builder and runtime
// base images and the Node and Bun toolchain layers, so a fresh box's first
// real deploy doesn't download them (a dogfood run's first build took 134 s).
// It holds the build slot like a deploy, so a deploy that comes in meanwhile
// waits for it and then finds the cache warm.
func (r *rt) warmUp(ctx context.Context) {
	if _, ok, _ := r.p.DB.KVGet(ctx, "runtime", warmUpKey); ok {
		return
	}
	select {
	case r.build <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-r.build }()
	began := time.Now()
	dir, err := os.MkdirTemp(r.opt.DataDir, "warmup-")
	if err != nil {
		return
	}
	defer os.RemoveAll(dir)
	src := filepath.Join(dir, "src")
	if err := starters.WriteTo("next-postgres", src); err != nil {
		r.p.Log.Error("build warm-up", "err", err)
		return
	}
	logf, err := os.Create(filepath.Join(r.opt.LogDir, "warmup.log"))
	if err != nil {
		return
	}
	defer logf.Close()
	d := &Deploy{ID: "warmup", Project: "tiffin-warmup", App: "web", Framework: string(manifest.FrameworkNext)}
	res, err := r.bld.Build(ctx, BuildRequest{Deploy: d, Spec: manifest.App{Framework: manifest.FrameworkNext},
		SrcDir: src, WorkDir: dir, Env: map[string]string{"TIFFIN_PROJECT": d.Project, "TIFFIN_APP": d.App}, Log: logf})
	if err != nil {
		if ctx.Err() == nil {
			r.p.Log.Warn("build warm-up failed; the first deploy downloads the build tools instead", "err", err, "log", logf.Name())
		}
		return
	}
	if res.Image != "" {
		_ = r.eng.RemoveImage(ctx, res.Image)
	}
	_ = r.p.DB.KVPut(ctx, "runtime", warmUpKey, []byte(time.Now().UTC().Format(time.RFC3339)))
	r.p.Log.Info("build cache warmed", "seconds", int(time.Since(began).Seconds()))
}
