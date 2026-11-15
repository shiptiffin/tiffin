package runtime

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/shiptiffin/tiffin/internal/manifest"
	"github.com/shiptiffin/tiffin/internal/mod/runtime/vercelcfg"
	"github.com/shiptiffin/tiffin/internal/platform"
)

// The box reads what an app already has, so an app moves from Vercel
// without changes: its folder in a workspace upload, a Next.js static
// export, and its vercel.json (build settings, crons, headers, redirects,
// rewrites). This file reads them for a build and hands vercel.json's
// crons to the queue.

// readApp reads an unpacked source for the build, saying in the build log
// what it found.
func (r *rt) readApp(d *Deploy, spec *manifest.App, req *BuildRequest, log io.Writer) error {
	if d.Dir != "" {
		if _, err := subdir(req.SrcDir, d.Dir); err != nil {
			return &BuildError{Msg: fmt.Sprintf("folder %s is not in the uploaded source", d.Dir),
				Hint: "dir names the app's folder inside the upload, e.g. apps/web."}
		}
		req.Dir = d.Dir
		fmt.Fprintf(log, "==> workspace: the app is %s/ in a workspace (monorepo): dependencies install at its top, the app builds in its folder\n", d.Dir)
	}
	appDir := req.appDir()
	if file, why := dockerfileOf(*spec, req.SrcDir, d.Dir); file != "" {
		req.Dockerfile, d.Builder, d.Dockerfile = file, string(manifest.BuilderDockerfile), file
		req.Spec.Builder = manifest.BuilderDockerfile
		fmt.Fprintf(log, "==> builder: the Dockerfile %s (%s)\n", file, why)
	}
	req.Export = req.Dockerfile == "" && spec.Role != manifest.RoleWorker && nextExport(appDir)
	if spec.Output != "" && spec.Framework == manifest.FrameworkNext && !req.Export {
		fmt.Fprintf(log, "==> note: the output folder (%s) is only served for a Next.js static export (output: \"export\"); this app runs as a server\n", spec.Output)
	}
	if req.Export {
		d.Framework = "next+export"
	}
	v, err := vercelcfg.Read(appDir)
	if err != nil {
		return &BuildError{Msg: err.Error(), Hint: "Fix vercel.json (or remove it): the box reads its build settings, crons, headers, redirects and rewrites."}
	}
	d.Launch = ""
	server := spec.Framework == manifest.FrameworkBun || spec.Framework == manifest.FrameworkAstro
	if req.Dockerfile == "" && spec.Role != manifest.RoleWorker && (server || spec.Framework == manifest.FrameworkStatic) {
		// The framework's output follows the build command that runs: the
		// app's settings win over vercel.json, which wins over package.json.
		build := spec.Build
		if build == "" && v != nil {
			build = v.BuildCommand
		}
		if spec.Framework == manifest.FrameworkStatic {
			if deps, inConfig := astroNodeServer(appDir); deps && inConfig {
				return staticAstroServerError(orDefaultStr(d.Dir, "."))
			}
		}
		onNode := spec.Runtime == manifest.RuntimeNode
		l := launchFor(appDir, onNode, build)
		if server && spec.Runtime == "" && l != nil && l.ID == "astro" && l.Err == nil && l.Files == nil && packageDeps(appDir)["@astrojs/node"] {
			// @astrojs/node supports Node.js only (Astro lists no Bun
			// adapter): its server builds and runs on Node.js unless the app
			// asks for Bun. The rest of the build reads req.Spec.
			req.Spec.Runtime = manifest.RuntimeNode
			l = launchFor(appDir, true, build)
			fmt.Fprintf(log, "==> runtime: Node.js, as Astro's @astrojs/node adapter supports it (runtime: \"bun\" in tiffin.config.ts runs it on Bun)\n")
		}
		if l != nil && (l.Files != nil || server) {
			if spec.Command != "" && l.Files == nil && l.Start != "" {
				l.Start, l.Why = "", "the app's command ("+spec.Command+")"
			}
			req.Launch = l
			if l.Files == nil && l.Err == nil {
				d.Launch = l.ID
			}
		}
		if spec.Framework == manifest.FrameworkAstro && (l == nil || l.ID != "astro") {
			fmt.Fprintf(log, "==> note: framework is \"astro\" but %s/package.json does not list astro: building it like any Bun or Node server\n", orDefaultStr(d.Dir, "."))
		}
	}
	if v == nil {
		return nil
	}
	if req.Export || spec.Framework == manifest.FrameworkStatic {
		if len(v.Crons) > 0 {
			v.Ignored = append(v.Ignored, "crons (a static site has no server to call)")
			v.Crons = nil
		}
	} else {
		v.ForContainer()
	}
	if err := v.Rules.Validate(); err != nil {
		return &BuildError{Msg: "vercel.json: " + err.Error(), Hint: "Fix that rule in vercel.json, or remove it."}
	}
	if s := v.Summary(); s != "" {
		fmt.Fprintf(log, "==> vercel.json: %s\n", s)
	}
	if len(v.Ignored) > 0 {
		fmt.Fprintf(log, "==> vercel.json: not used by the box: %s\n", strings.Join(v.Ignored, ", "))
	}
	if d.Preview != "" && len(v.Crons) > 0 {
		fmt.Fprintf(log, "==> vercel.json: crons run for production only, as on Vercel\n")
	}
	req.Vercel, d.Vercel = v, v
	return nil
}

// staticAstroServerError is the deploy error for an Astro app declared
// static whose config uses @astrojs/node: the adapter builds a server
// (<outDir>/server/entry.mjs, the pages in <outDir>/client), not a site of
// files, so a static build finds no index.html to serve.
func staticAstroServerError(dir string) *BuildError {
	return &BuildError{Msg: "the app is declared framework \"static\", but " + dir + " is an Astro app with the @astrojs/node adapter, which builds a server (dist/server/entry.mjs), not a site of files",
		Hint: "Set framework: \"astro\" in tiffin.config.ts (the app's Framework setting) and remove output, so the box runs the server and serves its prerendered pages itself. " +
			"Or, for a site of files only, remove @astrojs/node and the adapter line from astro.config."}
}

// appCronSetter is the queue module: it replaces the crons an app's own
// files declare (see internal/mod/queue/contract.go).
type appCronSetter interface {
	SetAppCrons(ctx context.Context, p *platform.Platform, project, app, origin string, crons map[string]manifest.Cron) error
}

// syncCrons hands the queue the vercel.json crons of the app's live
// production release (none when it has none, or is not deployed). It says
// what it did in log, when there is one.
func (r *rt) syncCrons(ctx context.Context, project, app string, log io.Writer) error {
	crons := map[string]manifest.Cron{}
	if st, err := r.st.getState(ctx, project, app, ""); err == nil && st.Live != "" && !st.Stopped {
		if d, err := r.st.getDeploy(ctx, project, app, st.Live); err == nil && d.Vercel != nil {
			for _, c := range d.Vercel.Crons {
				crons[c.Name] = manifest.Cron{Schedule: c.Schedule, App: app, Path: c.Path}
			}
		}
	}
	for _, mod := range platform.Modules() {
		s, ok := mod.(appCronSetter)
		if !ok {
			continue
		}
		err := s.SetAppCrons(ctx, r.p, project, app, vercelcfg.File, crons)
		if log != nil && len(crons) > 0 {
			names := make([]string, 0, len(crons))
			for n, c := range crons {
				names = append(names, fmt.Sprintf("%s (GET %s, %s UTC)", n, c.Path, c.Schedule))
			}
			sort.Strings(names)
			if err != nil {
				fmt.Fprintf(log, "==> vercel.json crons: not registered yet (%v); they are once the queue runs\n", err)
			} else {
				fmt.Fprintf(log, "==> vercel.json crons: %s (queue crons list shows them)\n", strings.Join(names, ", "))
			}
		}
		return err
	}
	return nil
}
