package runtime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/danielgtaylor/huma/v2"

	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// registerRedeploy adds deploys redeploy: build the live version's source
// again with the app's current settings (a build setting changed, say).
func (m *Module) registerRedeploy(a huma.API, appPath string) {
	op := api.Op("deploy-redeploy", http.MethodPost, appPath+"/deploys/redeploy", "deploys redeploy", api.RiskWrite, "Redeploy the live version",
		"Builds the source of the app's live production version again, with the app's current build settings, env and secrets, "+
			"and releases it with zero downtime like any deploy. Use it after changing how the app builds (install, build, start, "+
			"builder, root directory): those apply from the next deploy. An app connected to GitHub deploys its branch instead "+
			"(deploys github). A prebuilt image has no source to build again.", "apps")
	op.DefaultStatus = http.StatusAccepted
	op.Errors = append(op.Errors, 404, 409, 422, 503)
	huma.Register(a, op, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		App     string `path:"app" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"App name"`
	}) (*struct{ Body *Deploy }, error) {
		r, err := m.rt()
		if err != nil {
			return nil, unavailable(err)
		}
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		d, perr := r.redeployLive(ctx, in.Project, in.App, pr.TokenID)
		if perr != nil {
			return nil, perr
		}
		_ = r.p.DB.Audit(ctx, pr.TokenID, "deploy.redeploy", in.Project+"/"+in.App, map[string]any{"deploy": d.ID, "session": pr.Session})
		return &struct{ Body *Deploy }{d}, nil
	}))
}

// redeployLive queues a build of the live production version's source.
func (r *rt) redeployLive(ctx context.Context, project, app, by string) (*Deploy, *api.Problem) {
	spec, perr := r.checkDeployable(ctx, project, app, "", false)
	if perr != nil {
		return nil, perr
	}
	st, err := r.st.getState(ctx, project, app, "")
	if err != nil && !errors.Is(err, errNotFound) {
		return nil, problem(500, "internal", err.Error(), "")
	}
	if st == nil || st.Live == "" {
		return nil, problem(409, "conflict", "app "+app+" has no live version to build again", "Deploy it first: tiffin deploy, a template, or its GitHub branch.")
	}
	d, err := r.st.getDeploy(ctx, project, app, st.Live)
	if err != nil {
		return nil, problem(500, "internal", err.Error(), "")
	}
	src := filepath.Join(r.workDir(d), sourceFile)
	if d.Source == SourcePrebuilt || !exists(src) {
		return nil, problem(409, "conflict", "the live version of "+app+" is a prebuilt image, or its source is no longer kept, so there is nothing to build again",
			"Deploy it again from its source (tiffin deploy, or its GitHub branch).")
	}
	nd, err := r.newDeploy(ctx, project, app, "", d.Source, by, spec)
	if err != nil {
		return nil, problem(500, "internal", err.Error(), "")
	}
	nd.Commit, nd.Repo, nd.Ref, nd.Message, nd.Author, nd.Template, nd.Dir = d.Commit, d.Repo, d.Ref, d.Message, d.Author, d.Template, d.Dir
	nd.Trigger, nd.SourceBytes = "redeploy", d.SourceBytes
	dst := filepath.Join(r.workDir(nd), sourceFile)
	if err := linkOrCopy(src, dst); err != nil {
		_ = r.st.deleteDeploy(ctx, nd)
		return nil, problem(500, "internal", fmt.Sprintf("copy the source of %s: %v", d.ID, err), "")
	}
	_ = r.st.putDeploy(ctx, nd)
	if log, err := r.openBuildLog(nd); err == nil {
		v := ""
		if d.Version > 0 {
			v = fmt.Sprintf(" (v%d)", d.Version)
		}
		fmt.Fprintf(log, "==> redeploy of %s%s: its source, built again with the app's current settings\n", d.ID, v)
		log.Close()
	}
	r.start(nd, dst, d.Source)
	return nd, nil
}
