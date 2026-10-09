package runtime

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/shiptiffin/tiffin/internal/manifest"
)

// Dockerfile builds. The app's Dockerfile is built by BuildKit's own
// dockerfile frontend, with the uploaded source as its only context (no
// named contexts, no SSH agent, no entitlements: buildkitd allows none, so
// RUN --network=host and --security=insecure are refused). Env reaches the
// build two ways:
//
//   - build args: the app's plain env (tiffin.config.ts, never secret) and
//     its browser env (NEXT_PUBLIC_*, VITE_*…, public by definition), for
//     the ARGs the Dockerfile declares;
//   - BuildKit secrets: everything else the instances get (secrets, the
//     services' URLs), for RUN --mount=type=secret,id=NAME[,env=NAME]. They
//     are in no layer, no image history and no log.
//
// The image runs like any other: $PORT is set and the app must listen on
// it (EXPOSE is not used), health checks, zero-downtime switches, logs,
// rollbacks and image cleanup are the same.

// dockerfileOf is the Dockerfile an app builds with, relative to src (its
// unpacked source; dir is the app's folder in it), and why, or "" for a
// Railpack build. Builder "dockerfile" names it (default "Dockerfile");
// builder auto takes a Dockerfile at the top of the app's folder when the
// folder has nothing Railpack is tuned for (no package.json, no Python
// project) and the app is not a static site.
func dockerfileOf(spec manifest.App, src, dir string) (file, why string) {
	appDir := filepath.Join(src, filepath.FromSlash(dir))
	switch {
	case spec.Builder == manifest.BuilderDockerfile:
		return path.Join(dir, orDefaultStr(spec.Dockerfile, manifest.DefaultDockerfile)), "builder \"dockerfile\""
	case spec.Builder != "" && spec.Builder != manifest.BuilderAuto:
		return "", ""
	case spec.Framework == manifest.FrameworkStatic:
		return "", ""
	}
	if !regularFile(filepath.Join(appDir, manifest.DefaultDockerfile)) {
		return "", ""
	}
	for _, f := range []string{"package.json", "requirements.txt", "pyproject.toml"} {
		if exists(filepath.Join(appDir, f)) {
			return "", ""
		}
	}
	return path.Join(dir, manifest.DefaultDockerfile), "a Dockerfile and no package.json or Python project in the app's folder"
}

func regularFile(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode().IsRegular()
}

// startOverride is the command that replaces an image's own (a Dockerfile
// or prebuilt image whose app sets command); "" keeps the image's.
func startOverride(spec manifest.App) string {
	return strings.TrimSpace(spec.Command)
}

// buildDockerfile builds req.Dockerfile with BuildKit into the deploy's image.
func (b *boxBuilder) buildDockerfile(ctx context.Context, req BuildRequest, ref string) (BuildResult, error) {
	d := req.Deploy
	rel := filepath.FromSlash(req.Dockerfile)
	file := filepath.Join(req.SrcDir, rel)
	if !strings.HasPrefix(file, filepath.Clean(req.SrcDir)+string(filepath.Separator)) {
		return BuildResult{}, &BuildError{Msg: "the Dockerfile path " + req.Dockerfile + " is outside the app's source",
			Hint: "Name a Dockerfile inside the app's folder, like Dockerfile or docker/web.Dockerfile."}
	}
	// The path must be a plain file of the source itself, not a link out of it.
	if real, err := filepath.EvalSymlinks(file); err != nil || !regularFile(real) || !inside(req.SrcDir, real) {
		return BuildResult{}, &BuildError{Msg: "no Dockerfile at " + req.Dockerfile,
			Hint: "Add one there, name the right file (Dockerfile path in the app's Build and deploy settings, dockerfile in tiffin.config.ts), or switch the builder back to automatic."}
	}
	target := strings.TrimSpace(req.Spec.Target)
	how := "==> building " + req.Dockerfile
	if target != "" {
		how += " (stage " + target + ")"
	}
	context := "the app's folder"
	if req.Dir != "" {
		context = "the top of its workspace"
	}
	fmt.Fprintf(req.Log, "%s with BuildKit; context: %s\n", how, context)
	if len(req.Spec.Packages) > 0 {
		fmt.Fprintf(req.Log, "==> note: packages (%s) are not added to a Dockerfile build; install them in the Dockerfile\n", strings.Join(req.Spec.Packages, ", "))
	}

	args, secrets := dockerfileEnv(req)
	argKeys, secretKeys := sortedKeys(args), sortedKeys(secrets)
	if len(argKeys) > 0 {
		fmt.Fprintf(req.Log, "==> build args (for the ARGs the Dockerfile declares): %s\n", strings.Join(argKeys, ", "))
	}
	if len(secretKeys) > 0 {
		fmt.Fprintf(req.Log, "==> build secrets (RUN --mount=type=secret,id=NAME,env=NAME): %s\n", strings.Join(secretKeys, ", "))
	}
	defer b.limitBuild(d.Project, req.Log)()

	bargs := []string{"build",
		"--progress", "plain",
		"--frontend", "dockerfile.v0",
		// cmdline set: BuildKit's own Dockerfile frontend builds it, never
		// the image a "# syntax=" line names, which could ignore the cache
		// namespace below.
		"--opt", "cmdline=",
		// The app's own namespace for RUN --mount=type=cache: an id another
		// project's Dockerfile picks never reaches this app's caches.
		"--opt", "build-arg:BUILDKIT_CACHE_MOUNT_NS=" + cacheNamespace(d.Project, d.App),
		"--local", "context=" + req.SrcDir,
		"--local", "dockerfile=" + filepath.Dir(file),
		"--opt", "filename=" + filepath.Base(file),
		"--output", "type=image,name=" + ref + ",unpack=true"}
	if target != "" {
		bargs = append(bargs, "--opt", "target="+target)
	}
	for _, k := range argKeys {
		// Plain and browser env only: values the app does not keep secret.
		bargs = append(bargs, "--opt", "build-arg:"+k+"="+args[k])
	}
	// Secret values travel in buildctl's env (under the box's names), never
	// on its command line.
	sargs, senv := secretArgs(secretKeys, secrets)
	bargs = append(bargs, sargs...)
	const (
		noStage, noStage2 = "target stage", "could not be found"
		entHost, entInsec = "network.host", "security.insecure"
	)
	out := &buildOutput{marks: []string{noStage, noStage2, entHost, entInsec}}
	err := runLoggedEnv(ctx, io.MultiWriter(req.Log, out), req.SrcDir, senv, b.tool("buildctl"), bargs...)
	first, oom, dg := out.result()
	if err != nil {
		msg, hint := "the Dockerfile build failed: "+err.Error(), "Read the build log (deploys build-log): the first error is usually the cause."
		if first != "" {
			msg = "the Dockerfile build failed: " + first
		}
		switch {
		case oom:
			hint = "The build ran out of memory. Build elsewhere and deploy with --prebuilt, or give the box more memory."
		case out.saw(noStage) && out.saw(noStage2):
			hint = "The Dockerfile has no stage named " + target + ": fix the build target in the app's Build and deploy settings."
		case out.saw(entHost) || out.saw(entInsec):
			hint = "The box doesn't allow RUN --network=host or --security=insecure in builds; take them out of the Dockerfile."
		}
		return BuildResult{}, &BuildError{Msg: msg, Hint: hint}
	}
	if dg == "" {
		dg, _ = b.eng.ImageDigest(ctx, ref)
	}
	return BuildResult{Image: ref, Digest: dg, Start: startOverride(req.Spec)}, nil
}

// dockerfileEnv splits the env a Dockerfile build may see into build args
// (plain and browser env, which are not secret) and BuildKit secrets
// (everything else). A Next.js app's Server Actions key is a secret.
func dockerfileEnv(req BuildRequest) (args, secrets map[string]string) {
	args, secrets = map[string]string{}, map[string]string{}
	for k, v := range req.RunEnv {
		secrets[k] = v
	}
	for k, v := range req.Env {
		if strings.HasPrefix(k, "BUILDKIT_") {
			// BuildKit reads build args of these names as its own settings
			// (the syntax frontend, the cache namespace): never the app's.
			continue
		}
		if k == nextKeyEnv {
			secrets[k] = v
			continue
		}
		args[k] = v
		delete(secrets, k)
	}
	return args, secrets
}

// inside reports whether p is root or below it (both cleaned, links resolved).
func inside(root, p string) bool {
	r, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(r, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// imageHint is what a Dockerfile or prebuilt image that never answered its
// health check most likely got wrong: the port.
func imageHint(spec manifest.App) string {
	if spec.Builder != manifest.BuilderDockerfile && spec.Builder != manifest.BuilderPrebuilt {
		return ""
	}
	return " An image must listen on $PORT, which the box sets for each instance; the port its Dockerfile EXPOSEs is not used."
}
