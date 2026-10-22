package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Shared bases. Railpack assembles every app's runtime image from the same
// lower parts: the runtime base step (Debian and its apt packages) and the
// toolchain mise installed (/mise/installs, mise itself, its config). It
// copies them in on every build, and a copy BuildKit runs again (its cache
// record was evicted) writes new directory times, so the layer differs, and
// containerd unpacks every layer above it again: each kept image of each
// app held its own copy of the whole stack, node_modules included.
//
// So the box builds those lower parts once, as an image of their own named
// by a hash of the steps that make them (baseRepo:<key>), and the app's plan
// stacks on it by digest. Apps on the same toolchain share it, and an app's
// builds share every layer up to their own files.
//
// The image an app gets must not change: the first build with a new base
// (and any change to the plan around it) is built both ways, and the box
// keeps the shared-base image only when its files and config match those of
// the image today's builder makes (gateImage); otherwise it ships today's
// image and says why. The verdict is kept per key, so later builds are built
// once.

// baseRepo names shared base images. .invalid never resolves (RFC 6761):
// BuildKit finds the image in the box's own store, never in a registry.
const baseRepo = "tiffin.invalid/base"

// gateVersion is part of every verdict's key: a change to how the box builds
// or compares images makes it compare again.
const gateVersion = "4"

// sharedBase is a Railpack plan split at its shared lower part.
type sharedBase struct {
	key  string         // hash of the base plan
	plan map[string]any // the base image's own plan
	// app is the app's plan, its base and the inputs the base carries
	// taken out; withBase sets the base image in it.
	app map[string]any
	// files the base's steps copy from the app's source (in srcDir).
	srcDir string
	files  []string
}

// ref is the base image's name.
func (s *sharedBase) ref() string { return baseRepo + ":" + s.key[:16] }

// splitBase finds the lower part of a plan's runtime image that does not
// depend on the app's source: the deploy stage's base (a step or an image)
// and the deploy inputs before the first one that does (Railpack puts
// mise's first). nil when there is none to share.
func splitBase(plan map[string]any, srcDir string) *sharedBase {
	deploy, _ := plan["deploy"].(map[string]any)
	if deploy == nil {
		return nil
	}
	steps := map[string]map[string]any{}
	stepList, _ := plan["steps"].([]any)
	for _, s := range stepList {
		if m, _ := s.(map[string]any); m != nil {
			if name, _ := m["name"].(string); name != "" {
				steps[name] = m
			}
		}
	}
	// sourceFree: the step and every step it builds on read no local files
	// but single ones they copy in by name (Python's .python-version), which
	// go in the key and the base's build context.
	copied := map[string]bool{}
	var sourceFree func(name string, depth int) bool
	sourceFree = func(name string, depth int) bool {
		s := steps[name]
		if s == nil || depth > 20 {
			return false
		}
		cmds, _ := s["commands"].([]any)
		for _, c := range cmds {
			m, _ := c.(map[string]any)
			src, ok := m["src"].(string)
			if !ok {
				continue
			}
			clean := filepath.ToSlash(filepath.Clean(src))
			if clean != src || src == "." || strings.HasPrefix(src, "/") || strings.HasPrefix(src, "..") || strings.ContainsAny(src, "*?[") || !regularFile(filepath.Join(srcDir, src)) {
				return false
			}
			copied[src] = true
		}
		ins, _ := s["inputs"].([]any)
		for _, in := range ins {
			m, _ := in.(map[string]any)
			switch {
			case m == nil || m["local"] == true:
				return false
			case m["step"] != nil:
				if n, _ := m["step"].(string); !sourceFree(n, depth+1) {
					return false
				}
			case m["image"] == nil:
				return false
			}
		}
		return true
	}
	// The base is a step that reads no source (Node.js: the runtime image
	// and its apt packages), or an image (Python).
	base, _ := deploy["base"].(map[string]any)
	baseStep, _ := base["step"].(string)
	baseImage, _ := base["image"].(string)
	switch {
	case len(base) != 1:
		return nil
	case baseStep != "" && !sourceFree(baseStep, 0):
		return nil
	case baseStep == "" && baseImage == "":
		return nil
	}
	inputs, _ := deploy["inputs"].([]any)
	n := 0
	for _, in := range inputs {
		m, _ := in.(map[string]any)
		step, _ := m["step"].(string)
		if m == nil || step == "" || m["local"] == true || !sourceFree(step, 0) {
			break
		}
		n++
	}
	// The steps the base needs, in the plan's order.
	need := map[string]bool{}
	var mark func(name string)
	mark = func(name string) {
		if need[name] {
			return
		}
		need[name] = true
		ins, _ := steps[name]["inputs"].([]any)
		for _, in := range ins {
			if m, _ := in.(map[string]any); m != nil {
				if s, _ := m["step"].(string); s != "" {
					mark(s)
				}
			}
		}
	}
	if n == 0 {
		return nil // nothing but the base: no copies to share
	}
	if baseStep != "" {
		mark(baseStep)
	}
	for _, in := range inputs[:n] {
		m, _ := in.(map[string]any)
		step, _ := m["step"].(string)
		mark(step)
	}
	var baseSteps []any
	for _, s := range stepList {
		m, _ := s.(map[string]any)
		if name, _ := m["name"].(string); name != "" && need[name] {
			baseSteps = append(baseSteps, m)
		}
	}
	bp := map[string]any{
		"steps":  baseSteps,
		"caches": plan["caches"],
		"deploy": map[string]any{"base": base, "inputs": inputs[:n]},
	}
	raw, err := json.Marshal(bp) // map keys sorted: the same plan, the same bytes
	if err != nil {
		return nil
	}
	key := sha256.New()
	key.Write(raw)
	key.Write([]byte(RailpackFrontend))
	files := slices.Sorted(maps.Keys(copied))
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(srcDir, f))
		if err != nil {
			return nil
		}
		fmt.Fprintf(key, "\x00%s\x00%x", f, sha256.Sum256(b))
	}
	var sum [sha256.Size]byte
	copy(sum[:], key.Sum(nil))

	app := cloneJSON(plan)
	ad, _ := app["deploy"].(map[string]any)
	if ad == nil {
		return nil
	}
	ad["inputs"] = cloneJSON(map[string]any{"i": inputs[n:]})["i"]
	delete(ad, "base")
	return &sharedBase{key: hex.EncodeToString(sum[:]), plan: bp, app: app, srcDir: srcDir, files: files}
}

// verdictKey keys the gate's verdict: the base image (by digest, so a
// rebuilt base is checked again), the whole plan stacked on it (every step,
// as env and paths reach the image from them), the Railpack version and how
// the box compares.
func (s *sharedBase) verdictKey(baseDigest string) string {
	raw, _ := json.Marshal(s.app)
	h := sha256.New()
	for _, part := range []string{s.key, baseDigest, string(raw), RailpackVersion, gateVersion} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// withBase is the app's plan on the base image (pinned by digest).
func (s *sharedBase) withBase(digest string) map[string]any {
	p := cloneJSON(s.app)
	// name:tag@digest: BuildKit finds a name in the box's store, and the
	// digest pins it (a bare name@digest is looked up in a registry).
	if d, _ := p["deploy"].(map[string]any); d != nil {
		d["base"] = map[string]any{"image": s.ref() + "@" + digest}
	}
	return p
}

func cloneJSON(v map[string]any) map[string]any {
	raw, _ := json.Marshal(v)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

// writePlan writes plan as dir/railpack-plan.json.
func writePlan(dir string, plan map[string]any) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "railpack-plan.json"), raw, 0o644)
}

// gateVerdict is what the gate decided for a verdict key: same (the
// shared-base image is the one today's builder makes) or why not.
type gateVerdict struct {
	Same bool      `json:"same"`
	Why  string    `json:"why,omitempty"`
	At   time.Time `json:"at"`
}

func (b *boxBuilder) verdict(key string) *gateVerdict {
	if b.gateDir == "" {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(b.gateDir, key+".json"))
	if err != nil {
		return nil
	}
	var v gateVerdict
	if json.Unmarshal(raw, &v) != nil || time.Since(v.At) > recheckAfter {
		return nil
	}
	return &v
}

// recheckAfter: a verdict is checked again after a week, as a base is
// rebuilt (a difference may also have come from a step the cache had lost
// and ran again).
const recheckAfter = 7 * 24 * time.Hour

// baseMaxAge: a base older than this is built again, its apt step afresh,
// as a legacy build's was whenever BuildKit had evicted it (its cache
// keeps such records for a week): security fixes in the runtime image and
// its packages reach apps at least as soon as before. The weekly box
// maintenance redeploys every app, and the first build past the week
// rebuilds the base for all of them.
const baseMaxAge = 7 * 24 * time.Hour

func (b *boxBuilder) keepVerdict(key string, v gateVerdict) {
	if b.gateDir == "" || os.MkdirAll(b.gateDir, 0o700) != nil {
		return
	}
	raw, _ := json.Marshal(v)
	tmp := filepath.Join(b.gateDir, key+".tmp")
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, filepath.Join(b.gateDir, key+".json"))
	}
}

// ensureBase returns the base image's digest, building it unless the store
// has one younger than baseMaxAge.
func (b *boxBuilder) ensureBase(ctx context.Context, s *sharedBase, workDir string, log io.Writer) (string, error) {
	if imgs, err := b.eng.Images(ctx); err == nil {
		for _, im := range imgs {
			if imageKey(im.Name) == imageKey(s.ref()) && !im.Created.IsZero() && time.Since(im.Created) < baseMaxAge {
				if dg, err := b.eng.ImageDigest(ctx, s.ref()); err == nil && dg != "" {
					return dg, nil
				}
			}
		}
	}
	dir := filepath.Join(workDir, "base-plan")
	if err := writePlan(dir, s.plan); err != nil {
		return "", err
	}
	// Its build context: only the files its steps copy in.
	empty := filepath.Join(workDir, "base-context")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		return "", err
	}
	for _, f := range s.files {
		b, err := os.ReadFile(filepath.Join(s.srcDir, f))
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(empty, f)), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(empty, f), b, 0o644); err != nil {
			return "", err
		}
	}
	fmt.Fprintf(log, "==> building the shared base image %s (the runtime and toolchain, once a week for every app that uses them)\n", s.ref())
	out := &buildOutput{}
	// The cache key names the steps' cache mounts, so a new one each day
	// runs the apt step again, as BuildKit runs it once it has evicted it;
	// image tags are resolved afresh either way.
	cacheKey := "tiffin-base-" + time.Now().UTC().Format("20060102")
	err := runLogged(ctx, io.MultiWriter(log, out), empty, b.tool("buildctl"),
		b.railpackArgs(empty, dir, cacheKey, "type=image,name="+s.ref()+",unpack=true")...)
	if err != nil {
		return "", err
	}
	_, _, dg := out.result()
	if dg == "" {
		dg, err = b.eng.ImageDigest(ctx, s.ref())
	}
	if err != nil || !strings.HasPrefix(dg, "sha256:") {
		return "", fmt.Errorf("no digest for %s (%v)", s.ref(), err)
	}
	return dg, nil
}

// onSharedBase builds the app's image on its shared base when the plan has
// one and the gate allows it, else as legacy builds it (today's way).
// candidate builds a plan directory into an image of the given name and
// returns its digest.
func (b *boxBuilder) onSharedBase(ctx context.Context, req BuildRequest, planPath, ref string, legacy func() (BuildResult, error),
	candidate func(planDir, name string, log io.Writer) (dg, first string, err error), fail func(first string, err error) error) (res BuildResult, err error) {
	// A fault in the shared-base path ships today's image: the one built
	// already, else a build of it.
	var built *BuildResult
	var builtErr error
	runLegacy := legacy
	legacy = func() (BuildResult, error) {
		r, e := runLegacy()
		built, builtErr = &r, e
		return r, e
	}
	defer func() {
		if p := recover(); p != nil {
			fmt.Fprintf(req.Log, "==> image: built as before (the shared-base path failed: %v)\n", p)
			if built != nil {
				res, err = *built, builtErr
				return
			}
			res, err = runLegacy()
		}
	}()
	raw, err := os.ReadFile(planPath)
	if err != nil {
		return BuildResult{}, err
	}
	var plan map[string]any
	if err := json.Unmarshal(raw, &plan); err != nil {
		return BuildResult{}, err
	}
	s := splitBase(plan, req.SrcDir)
	if s == nil {
		return legacy()
	}
	earlier := func(why string) (BuildResult, error) {
		fmt.Fprintf(req.Log, "==> image: built as before, not on the shared base (%s)\n", why)
		return legacy()
	}
	bdg, err := b.ensureBase(ctx, s, req.WorkDir, req.Log)
	if err != nil {
		return earlier("the shared base did not build: " + err.Error())
	}
	vkey := s.verdictKey(bdg)
	v := b.verdict(vkey)
	if v != nil && !v.Same {
		return earlier("its files differed when the box checked: " + v.Why)
	}
	dir := filepath.Join(req.WorkDir, "plan-shared")
	if err := writePlan(dir, s.withBase(bdg)); err != nil {
		return BuildResult{}, err
	}
	onBase := func(dg string) BuildResult {
		if dg == "" {
			dg, _ = b.eng.ImageDigest(ctx, ref)
		}
		return BuildResult{Image: ref, Digest: dg, Base: s.ref()}
	}
	if v != nil {
		// Checked before for this base and plan: built once, on the base.
		dg, first, err := candidate(dir, ref, req.Log)
		if err == nil {
			fmt.Fprintf(req.Log, "==> image: on the shared base %s\n", s.ref())
			return onBase(dg), nil
		}
		// Above the base the steps are the same either way: a failure there
		// (the app's own build) would fail again, so only one that involves
		// the base builds the earlier way.
		if ctx.Err() != nil || !strings.Contains(first, baseRepo) {
			return BuildResult{}, fail(first, err)
		}
		return earlier("the build on it failed: " + first)
	}
	// First build of this base and plan: today's image is the one that
	// ships unless the one on the base has the same files and config.
	res, err = legacy()
	if err != nil {
		return res, err
	}
	if b.diskPressure != nil && b.diskPressure() {
		// Comparing takes a second image's worth of disk: not now.
		fmt.Fprintf(req.Log, "==> image: as built; the data disk is past the disk guard's warning level, so the image on the shared base is not checked this time\n")
		return res, nil
	}
	fmt.Fprintf(req.Log, "==> checking the image on the shared base %s against this one (once per base and build plan, and weekly)\n", s.ref())
	cand := ref + "-shared"
	var clog strings.Builder
	cdg, _, err := candidate(dir, cand, &clog)
	if err != nil {
		fmt.Fprintf(req.Log, "==> image: as built; the build on the shared base failed (%v): %s\n", err, lastLines(clog.String(), 3))
		return res, nil
	}
	defer func() {
		rctx, cancel := cleanupContext(ctx)
		defer cancel()
		_ = b.eng.RemoveImage(rctx, cand)
	}()
	same, why, err := gateImage(ctx, b.eng, cand, ref)
	switch {
	case err != nil:
		fmt.Fprintf(req.Log, "==> image: as built; the box could not compare it with the one on the shared base (%v)\n", err)
		return res, nil
	case !same:
		b.keepVerdict(vkey, gateVerdict{Why: why, At: time.Now().UTC()})
		fmt.Fprintf(req.Log, "==> image: as built; the one on the shared base differs (%s), so this app keeps building this way\n", why)
		return res, nil
	}
	if err := b.eng.TagImage(ctx, cand, ref); err != nil {
		fmt.Fprintf(req.Log, "==> image: as built; could not name the one on the shared base (%v)\n", err)
		return res, nil
	}
	b.keepVerdict(vkey, gateVerdict{Same: true, At: time.Now().UTC()})
	fmt.Fprintf(req.Log, "==> image: on the shared base %s (the same files and config as the image built before it)\n", s.ref())
	return onBase(cdg), nil
}
