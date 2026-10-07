package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/mod/runtime/srcpack"
	"github.com/btahir/tiffin/internal/starters"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
)

// Creating apps from the dashboard: starter templates shipped inside tiffin
// and deploys from a public git URL. Both run through the same pipeline as
// tiffin deploy (deploy record, build log, health checks, rollback).

// TemplateList is the starter catalog.
type TemplateList struct {
	Templates []starters.Starter `json:"templates"`
}

type templateBody struct {
	Template string `json:"template" enum:"nextjs,tanstack-start,sveltekit,react-router,nuxt,astro,vite-react,hono,fastapi,static-site,guestbook" doc:"Starter ID from templates list (e.g. astro)"`
}

type gitDeployBody struct {
	URL  string `json:"url" minLength:"1" maxLength:"2048" example:"https://github.com/owner/repo" doc:"https URL of a public git repository. No credentials, no other schemes; the host must be public."`
	Ref  string `json:"ref,omitempty" maxLength:"200" doc:"Branch, tag or full commit SHA. Default: the repository's default branch."`
	Path string `json:"path,omitempty" maxLength:"500" doc:"The app's directory inside the repository, e.g. apps/web. Default: the top."`
}

func (m *Module) registerCreate(a huma.API) {
	appPath := "/v1/projects/{project}/apps/{app}"

	huma.Register(a, api.Op("templates-list", http.MethodGet, "/v1/templates", "templates list", api.RiskRead, "List starter templates",
		"Small, working starter apps shipped inside tiffin. Each has a kind (web: a web app with a server; static: a static site; "+
			"api: a JSON API) and a preset (the framework: nextjs, tanstack-start, sveltekit, react-router, nuxt, astro, vite-react, hono, fastapi); listed ones are offered "+
			"when starting a project, and default marks each kind's usual pick. Also: the services they need and the manifest fragment "+
			"to merge into a project (apps + services). To start a project from one: merge the fragment into the project's manifest "+
			"(projects manifest), plan and apply it, then deploys template with the template id, e.g. "+
			"`tiffin deploys template shop site --template astro`.", "apps"),
		api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body TemplateList }, error) {
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, ""); err != nil {
				return nil, err
			}
			all, err := starters.List()
			if err != nil {
				return nil, err
			}
			return &struct{ Body TemplateList }{TemplateList{all}}, nil
		}))

	tp := api.Op("deploy-template", http.MethodPost, appPath+"/deploys/template", "deploys template", api.RiskWrite, "Deploy a starter template",
		"Deploys a starter template's source (see templates list) to an app, through the regular pipeline: build on the box, "+
			"health check, zero-downtime switch, rollback later. The app must already exist with the template's framework, and the "+
			"services the template uses must be on: merge the template's fragment into the manifest and apply it first. "+
			"Returns at once with the queued deploy; poll deploys get until status is live or failed.", "apps")
	tp.DefaultStatus = http.StatusAccepted
	tp.Errors = append(tp.Errors, 404, 409)
	huma.Register(a, tp, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		App     string `path:"app" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"App name"`
		Preview string `query:"preview" pattern:"^[a-z0-9][a-z0-9-]{0,29}$" doc:"Deploy as a preview with this name instead of production"`
		Body    templateBody
	}) (*struct{ Body *Deploy }, error) {
		r, err := m.rt()
		if err != nil {
			return nil, unavailable(err)
		}
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		st, ok := starters.Get(in.Body.Template)
		if !ok {
			return nil, problem(422, "validation", "no template "+in.Body.Template, "List them with templates list.")
		}
		spec, perr := r.checkDeployable(ctx, in.Project, in.App, in.Preview, false)
		if perr != nil {
			return nil, perr
		}
		if string(spec.Framework) != st.Framework {
			return nil, problem(409, "precondition", fmt.Sprintf("app %s uses framework %s but template %s is a %s app", in.App, spec.Framework, st.ID, st.Framework),
				fmt.Sprintf("Set apps.%s.framework to %q (plan and apply), or deploy the template to another app.", in.App, st.Framework))
		}
		if missing := r.missingServices(ctx, in.Project, st.Services); len(missing) > 0 {
			return nil, problem(409, "precondition", fmt.Sprintf("template %s needs %s, which project %s does not have on", st.ID, strings.Join(missing, ", "), in.Project),
				"Merge the template's fragment into the manifest (add services."+strings.Join(missing, ", services.")+"), plan and apply, then deploy again.")
		}
		d, err := r.newDeploy(ctx, in.Project, in.App, in.Preview, SourceTemplate, pr.TokenID, spec)
		if err != nil {
			return nil, err
		}
		d.Template = st.ID
		src := filepath.Join(r.workDir(d), "source.tgz")
		tree := filepath.Join(r.workDir(d), "template")
		n, err := packStarter(st.ID, tree, src)
		if err != nil {
			_ = r.st.deleteDeploy(ctx, d)
			_ = os.RemoveAll(r.workDir(d))
			return nil, err
		}
		d.SourceBytes = n
		_ = r.st.putDeploy(ctx, d)
		r.startFrom(d, SourceTemplate, func(_ context.Context, log io.Writer) (string, error) {
			fmt.Fprintf(log, "==> template %s (%s): %d files\n", st.ID, st.Name, st.Files)
			return src, nil
		})
		_ = r.p.DB.Audit(ctx, pr.TokenID, "deploy.create", in.Project+"/"+in.App, map[string]any{"deploy": d.ID, "preview": in.Preview, "source": SourceTemplate, "template": st.ID, "session": pr.Session})
		return &struct{ Body *Deploy }{d}, nil
	}))

	gd := api.Outbound(api.Op("deploy-git", http.MethodPost, appPath+"/deploys/git", "deploys git", api.RiskWrite, "Deploy from a git URL",
		"Deploys an app from a public git repository: the box shallow-clones one commit (https only, no credentials, public hosts "+
			"only, no submodules, size and time limits), then builds and releases it through the regular pipeline (build log, health "+
			"check, zero-downtime switch, rollback). The clone shows in the build log. The app must already exist: add it to the "+
			"manifest and apply first. Returns at once with the queued deploy; poll deploys get until status is live or failed. "+
			"For private code, push to the box instead (git info).", "apps"))
	gd.DefaultStatus = http.StatusAccepted
	gd.Errors = append(gd.Errors, 404)
	huma.Register(a, gd, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		App     string `path:"app" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"App name"`
		Preview string `query:"preview" pattern:"^[a-z0-9][a-z0-9-]{0,29}$" doc:"Deploy as a preview with this name instead of production"`
		Body    gitDeployBody
	}) (*struct{ Body *Deploy }, error) {
		r, err := m.rt()
		if err != nil {
			return nil, unavailable(err)
		}
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		spec, perr := r.checkDeployable(ctx, in.Project, in.App, in.Preview, false)
		if perr != nil {
			return nil, perr
		}
		gs, err := checkGitSource(ctx, in.Body.URL, in.Body.Ref, in.Body.Path, r.resolve)
		var ue *gitURLError
		if errors.As(err, &ue) {
			return nil, problem(422, "validation", ue.msg, ue.hint)
		}
		if err != nil {
			return nil, err
		}
		d, err := r.newDeploy(ctx, in.Project, in.App, in.Preview, SourceGit, pr.TokenID, spec)
		if err != nil {
			return nil, err
		}
		d.Repo, d.Ref = gs.String(), gs.Ref
		_ = r.st.putDeploy(ctx, d)
		r.startFrom(d, SourceGit, func(ctx context.Context, log io.Writer) (string, error) { return r.cloneSource(ctx, d, gs, log) })
		_ = r.p.DB.Audit(ctx, pr.TokenID, "deploy.create", in.Project+"/"+in.App, map[string]any{"deploy": d.ID, "preview": in.Preview, "source": SourceGit, "repo": d.Repo, "ref": d.Ref, "path": gs.Path, "session": pr.Session})
		return &struct{ Body *Deploy }{d}, nil
	}))
}

// missingServices lists the services (postgres, valkey...) a project does
// not have on.
func (r *rt) missingServices(ctx context.Context, project string, want []string) []string {
	_, res, err := r.p.DB.Load(ctx, project)
	if err != nil {
		return want
	}
	var out []string
	for _, s := range want {
		if _, ok := res[change.KindService+"/"+s]; !ok {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// packStarter writes a starter's files to tree and packs them into dest.
func packStarter(id, tree, dest string) (int64, error) {
	defer os.RemoveAll(tree)
	if err := starters.WriteTo(id, tree); err != nil {
		return 0, err
	}
	f, err := os.Create(dest)
	if err != nil {
		return 0, err
	}
	st, err := srcpack.Pack(tree, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return st.Bytes, err
}

// cloneSource clones a repository for d and packs the app's directory as
// the deploy's source archive.
func (r *rt) cloneSource(ctx context.Context, d *Deploy, gs *gitSource, log io.Writer) (string, error) {
	dir := filepath.Join(r.workDir(d), "clone")
	defer os.RemoveAll(dir)
	commit, err := fetchGit(ctx, gs, dir, log)
	if err != nil {
		return "", err
	}
	d.Commit = commit
	_ = r.st.putDeploy(ctx, d)
	app, err := subdir(dir, gs.Path)
	if err != nil {
		return "", &BuildError{Msg: fmt.Sprintf("path %q is not a directory in the repository", gs.Path), Hint: "Set path to the app's folder, relative to the repository's top."}
	}
	// An app in a workspace (monorepo) builds from the workspace's top.
	if root, rel, ok := srcpack.WorkspaceRoot(app, dir); ok {
		app, d.Dir = root, rel
	}
	src := filepath.Join(r.workDir(d), "source.tgz")
	f, err := os.Create(src)
	if err != nil {
		return "", err
	}
	st, err := srcpack.Pack(app, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", &BuildError{Msg: "could not pack the repository: " + err.Error(), Hint: "Deploy a smaller app directory with path."}
	}
	d.SourceBytes = st.Bytes
	_ = r.st.putDeploy(ctx, d)
	return src, nil
}

// resolve looks up a git host (tests replace gitResolve).
func (r *rt) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if r.gitResolve != nil {
		return r.gitResolve(ctx, host)
	}
	return defaultResolve(ctx, host)
}
