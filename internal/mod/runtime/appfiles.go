package runtime

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/mod/runtime/vercelcfg"
	"github.com/btahir/tiffin/internal/platform"
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
		fmt.Fprintf(log, "==> workspace: the app is %s/ in a workspace whose packages it uses: dependencies install at the top, the app builds in its folder\n", d.Dir)
	}
	appDir := req.appDir()
	req.Export = spec.Role != manifest.RoleWorker && nextExport(appDir)
	if req.Export {
		d.Framework = "next+export"
	}
	v, err := vercelcfg.Read(appDir)
	if err != nil {
		return &BuildError{Msg: err.Error(), Hint: "Fix vercel.json (or remove it): the box reads its build settings, crons, headers, redirects and rewrites."}
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
