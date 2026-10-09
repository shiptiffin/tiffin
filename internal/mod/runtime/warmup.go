package runtime

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shiptiffin/tiffin/internal/manifest"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/starters"
)

// warmUpKey marks the build cache as warm for these tool versions: a new
// Railpack or Bun means new frontend and toolchain layers, so it warms again.
var warmUpKey = "warmed-" + RailpackVersion + "-" + BunVersion

// warmUpAttempts bounds how often a warm-up that deploys keep stopping starts
// again; by then those deploys have warmed most of the cache themselves.
const warmUpAttempts = 3

// warmSlot is how the warm-up shares the build slot with deploys: deploys
// always win. A deploy that wants the slot stops a running warm-up, and the
// warm-up starts only while no deploy waits, none built for a while and no
// resource is still converging (a box that just started or imported).
type warmSlot struct {
	mu      sync.Mutex
	cancel  context.CancelFunc // stops the running warm-up build; nil when none runs
	holder  string             // the deploy's app ("shop/web") building now; "" for none or the warm-up
	waiting atomic.Int32       // deploys waiting for the slot
	last    atomic.Int64       // unix nanos a deploy last released the slot
	poll    time.Duration      // how often a waiting warm-up looks again
	quiet   time.Duration      // how long after a deploy's build it waits
}

// acquireBuild takes the build slot for a deploy (who: "shop/web"), stopping
// the warm-up if it holds it. A deploy that has to wait says so in its log.
func (r *rt) acquireBuild(ctx context.Context, who string, log io.Writer) error {
	r.warm.waiting.Add(1)
	defer r.warm.waiting.Add(-1)
	r.warm.mu.Lock()
	wait := fmt.Sprintf("==> waiting for another build (%s) to finish: the box builds one at a time\n", cmp.Or(r.warm.holder, "another deploy"))
	if r.warm.cancel != nil {
		r.warm.cancel()
		wait = "==> stopping the build warm-up for this deploy\n"
	}
	r.warm.mu.Unlock()
	select {
	case r.build <- struct{}{}:
	default:
		io.WriteString(log, wait)
		select {
		case r.build <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	r.warm.mu.Lock()
	r.warm.holder = who
	r.warm.mu.Unlock()
	return nil
}

// releaseBuild gives a deploy's build slot back.
func (r *rt) releaseBuild() {
	r.warm.last.Store(time.Now().UnixNano())
	r.warm.mu.Lock()
	r.warm.holder = ""
	r.warm.mu.Unlock()
	<-r.build
}

// warmUp builds the Next.js starter once, in the background, after the box
// first starts (tiffin up), and throws the image away. BuildKit keeps what
// the build pulled and made: Railpack's frontend, the builder and runtime
// base images and the Node and Bun toolchain layers, so a fresh box's first
// real deploy doesn't download them (a dogfood run's first build took 134 s).
// It never holds up a deploy: it waits for a quiet box and yields to any
// deploy that comes in (starting again later, up to warmUpAttempts times).
func (r *rt) warmUp(ctx context.Context) {
	for range warmUpAttempts {
		if _, ok, _ := r.p.DB.KVGet(ctx, "runtime", warmUpKey); ok {
			return
		}
		if !r.waitQuiet(ctx) {
			return
		}
		if !r.warmUpOnce(ctx) {
			return
		}
	}
}

// waitQuiet waits until the box has nothing better to do with the build
// slot. It returns false when ctx ends.
func (r *rt) waitQuiet(ctx context.Context) bool {
	for {
		if r.quiet(ctx) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(r.warm.poll):
		}
	}
}

func (r *rt) quiet(ctx context.Context) bool {
	if r.warm.waiting.Load() > 0 || len(r.build) > 0 {
		return false
	}
	if last := r.warm.last.Load(); last != 0 && time.Since(time.Unix(0, last)) < r.warm.quiet {
		return false
	}
	return !converging(ctx, r.p)
}

// converging reports whether any project has a resource still pending: the
// box just started, imported or applied, and its apps come first.
func converging(ctx context.Context, p *platform.Platform) bool {
	projects, err := p.DB.ListProjects(ctx)
	if err != nil {
		return true
	}
	for _, pr := range projects {
		st, err := p.DB.ResourceStatuses(ctx, pr)
		if err != nil {
			return true
		}
		for _, s := range st {
			if s.State == platform.StatePending {
				return true
			}
		}
	}
	return false
}

// warmUpOnce runs one warm-up build. It returns true when a deploy stopped it
// (try again later) and false when it finished, failed or the box stops.
func (r *rt) warmUpOnce(ctx context.Context) (preempted bool) {
	select {
	case r.build <- struct{}{}:
	case <-ctx.Done():
		return false
	}
	wctx, cancel := context.WithCancel(ctx)
	r.warm.mu.Lock()
	if r.warm.waiting.Load() > 0 { // a deploy came in while we took the slot
		r.warm.mu.Unlock()
		cancel()
		<-r.build
		return true
	}
	r.warm.cancel = cancel
	r.warm.mu.Unlock()
	defer func() {
		r.warm.mu.Lock()
		r.warm.cancel = nil
		r.warm.mu.Unlock()
		cancel()
		<-r.build
	}()

	began := time.Now()
	dir, err := os.MkdirTemp(r.opt.DataDir, "warmup-")
	if err != nil {
		return false
	}
	defer os.RemoveAll(dir)
	src := filepath.Join(dir, "src")
	if err := starters.WriteTo("nextjs", src); err != nil {
		r.p.Log.Error("build warm-up", "err", err)
		return false
	}
	logf, err := os.Create(filepath.Join(r.opt.LogDir, "warmup.log"))
	if err != nil {
		return false
	}
	defer logf.Close()
	d := &Deploy{ID: "warmup", Project: "tiffin-warmup", App: "web", Framework: string(manifest.FrameworkNext)}
	res, err := r.bld.Build(wctx, BuildRequest{Deploy: d, Spec: manifest.App{Framework: manifest.FrameworkNext},
		SrcDir: src, WorkDir: dir, Env: map[string]string{"TIFFIN_PROJECT": d.Project, "TIFFIN_APP": d.App}, Log: logf})
	if err == nil && wctx.Err() != nil {
		err = wctx.Err()
	}
	if err != nil {
		switch {
		case ctx.Err() != nil:
		case wctx.Err() != nil:
			r.p.Log.Info("build warm-up stopped for a deploy; it tries again once the box is quiet")
			return true
		default:
			r.p.Log.Warn("build warm-up failed; the first deploy downloads the build tools instead", "err", err, "log", logf.Name())
		}
		return false
	}
	if res.Image != "" {
		_ = r.eng.RemoveImage(ctx, res.Image)
	}
	_ = r.p.DB.KVPut(ctx, "runtime", warmUpKey, []byte(time.Now().UTC().Format(time.RFC3339)))
	r.p.Log.Info("build cache warmed", "seconds", int(time.Since(began).Seconds()))
	return false
}
