package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/manifest"
	"github.com/btahir/tiffin/internal/mod/runtime/srcpack"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

const (
	projectPattern = "^[a-z][a-z0-9-]{0,39}$"
	deployPattern  = "^dep_[0-9A-Z]{26}$"
	previewPattern = "^[a-z0-9][a-z0-9-]{0,29}$"
	// MaxUpload bounds a source or image upload.
	MaxUpload = 4 << 30
	// maxFilesBody bounds inline-file deploys (agents).
	maxFilesBody = 8 << 20
	maxFollow    = 10 * time.Minute
)

var previewRe = regexp.MustCompile(previewPattern)

type deployBody struct {
	Files map[string]string `json:"files,omitempty" doc:"Inline source files, path → UTF-8 content (at most 8 MB in total), for agents that cannot upload a tarball. Include package.json and a lockfile. Command-line users run tiffin deploy, which uploads a gzipped tar of the app directory to this same endpoint (Content-Type: application/gzip)."`
}

// DeployList is a page of deploys.
type DeployList struct {
	Deploys []*Deploy `json:"deploys"`
}

// BuildLog is (part of) a deploy's build log.
type BuildLog struct {
	Deploy string `json:"deploy"`
	Status string `json:"status"`
	Text   string `json:"text" doc:"Build output from offset on"`
	Offset int64  `json:"offset" doc:"Pass as offset to read only what comes next"`
	Done   bool   `json:"done" doc:"The deploy finished; the log will not grow (except for later rollbacks)"`
}

// LogPage is a page of app log lines.
type LogPage struct {
	Lines []LogLine `json:"lines"`
	Next  string    `json:"next,omitempty" doc:"Pass as since to get only newer lines"`
}

// InstanceStatus is one instance as the runtime sees it.
type InstanceStatus struct {
	Instance
	Running bool   `json:"running"`
	State   string `json:"state" doc:"Container state (running, exited, restarting...)"`
}

// EnvStatus is one app environment (production or a preview).
type EnvStatus struct {
	Preview   string           `json:"preview,omitempty"`
	URL       string           `json:"url,omitempty"`
	Live      *Deploy          `json:"live,omitempty" doc:"The deploy serving it"`
	Instances []InstanceStatus `json:"instances"`
	Stopped   bool             `json:"stopped,omitempty" doc:"The app was deleted; undo the change to bring it back"`
	Sleeping  bool             `json:"sleeping,omitempty" doc:"A preview with no recent requests; the next request wakes it"`
	Draining  []DrainSet       `json:"draining,omitempty" doc:"Earlier releases still running, without traffic, for workflow runs pinned to them"`
	UpdatedAt time.Time        `json:"updatedAt"`
}

// AppRuntime is what runs for an app.
type AppRuntime struct {
	Project   string      `json:"project"`
	App       string      `json:"app"`
	Framework string      `json:"framework"`
	Role      string      `json:"role"`
	Routes    []string    `json:"routes,omitempty"`
	Prod      *EnvStatus  `json:"production,omitempty"`
	Previews  []EnvStatus `json:"previews"`
	Hint      string      `json:"hint,omitempty"`
}

// GitInfo tells clients how to push to the box.
type GitInfo struct {
	URL          string `json:"url" doc:"git remote URL; authenticate with any username and a Tiffin token as the password"`
	Instructions string `json:"instructions"`
}

func (m *Module) RegisterAPI(a huma.API, p *platform.Platform) {
	appPath := "/v1/projects/{project}/apps/{app}"

	create := api.Op("deploy-create", http.MethodPost, appPath+"/deploys", "deploys create", api.RiskWrite, "Deploy an app",
		"Builds and releases a new version of an app with zero downtime: the source is built on the box (Railpack + BuildKit; "+
			"static sites are served as files), new instances start, pass their health check, take over the app's routes, "+
			"and the old ones drain and stop. Returns at once with the queued deploy; then deploys get with wait=120 answers "+
			"when it is live or failed (the failure says why). A failed deploy never takes the running version down. "+
			"Send a gzipped tar of the app directory (Content-Type: application/gzip; tiffin deploy does this), "+
			"an image tarball with prebuilt=true, or JSON with inline files. "+
			"The app must already exist: add it to tiffin.config.ts and apply first.", "apps")
	create.DefaultStatus = http.StatusAccepted
	create.MaxBodyBytes = maxFilesBody
	create.Errors = append(create.Errors, 404, 413)
	create.Middlewares = huma.Middlewares{m.uploadMiddleware(a)}
	huma.Register(a, api.Idempotent(create), api.Wrap(func(ctx context.Context, in *struct {
		Project  string      `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		App      string      `path:"app" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"App name"`
		Preview  string      `query:"preview" pattern:"^[a-z0-9][a-z0-9-]{0,29}$" doc:"Deploy as a preview with this name, served at <preview>--<app address>.<domain> (pr-12--shop for the app at shop). Production is untouched."`
		Prebuilt bool        `query:"prebuilt" doc:"The upload is an image tarball (docker save) instead of source"`
		Body     *deployBody `required:"false"`
	}) (*struct{ Body *Deploy }, error) {
		r, err := m.rt()
		if err != nil {
			return nil, unavailable(err)
		}
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		if in.Body == nil || len(in.Body.Files) == 0 {
			return nil, problem(422, "validation", "send files in the JSON body, or upload a gzipped tar of the source with Content-Type: application/gzip", "tiffin deploy uploads the current directory for you.")
		}
		spec, perr := r.checkDeployable(ctx, in.Project, in.App, in.Preview, false)
		if perr != nil {
			return nil, perr
		}
		d, err := r.newDeploy(ctx, in.Project, in.App, in.Preview, SourceFiles, pr.TokenID, spec)
		if err != nil {
			return nil, err
		}
		src := filepath.Join(r.workDir(d), "source.tgz")
		n, err := packFiles(in.Body.Files, filepath.Join(r.workDir(d), "inline"), src)
		if err != nil {
			_ = r.st.deleteDeploy(ctx, d)
			return nil, problem(422, "validation", err.Error(), "File paths must be relative, inside the app, without '..'.")
		}
		d.SourceBytes = n
		_ = r.st.putDeploy(ctx, d)
		r.start(d, src, SourceFiles)
		_ = r.p.DB.Audit(ctx, pr.TokenID, "deploy.create", in.Project+"/"+in.App, map[string]any{"deploy": d.ID, "preview": in.Preview, "source": SourceFiles, "session": pr.Session})
		return &struct{ Body *Deploy }{d}, nil
	}))

	huma.Register(a, api.Untrusted(api.Op("deploys-list", http.MethodGet, appPath+"/deploys", "deploys list", api.RiskRead, "List an app's deploys",
		"Deploys of an app, newest first, with status, image digest, build time and URL. Production only unless preview or all is set.", "apps")),
		api.Wrap(func(ctx context.Context, in *struct {
			Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
			App     string `path:"app" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"App name"`
			Preview string `query:"preview" doc:"Only this preview's deploys"`
			All     bool   `query:"all" doc:"Production and every preview"`
			Limit   int    `query:"limit" minimum:"1" maximum:"200" default:"20" doc:"Maximum deploys to return"`
		}) (*struct{ Body DeployList }, error) {
			r, err := m.rt()
			if err != nil {
				return nil, unavailable(err)
			}
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
				return nil, err
			}
			env := in.Preview
			if in.All {
				env = "*"
			}
			ds, err := r.st.listDeploys(ctx, in.Project, in.App, env)
			if err != nil {
				return nil, err
			}
			if len(ds) > in.Limit {
				ds = ds[:in.Limit]
			}
			return &struct{ Body DeployList }{DeployList{ds}}, nil
		}))

	type deployPath struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		App     string `path:"app" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"App name"`
		ID      string `path:"id" pattern:"^dep_[0-9A-Z]{26}$" doc:"Deploy ID"`
	}
	get := api.Op("deploy-get", http.MethodGet, appPath+"/deploys/{id}", "deploys get", api.RiskRead, "Get a deploy",
		"One deploy: status (queued, building, starting, live, failed, superseded, rolled_back, stopped), error and hint when it failed, image digest, timings and URL. "+
			"Pass wait (seconds) to get the answer once the deploy is live or failed instead of polling.", "apps")
	get.Errors = append(get.Errors, 404)
	huma.Register(a, api.Untrusted(get), api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		App     string `path:"app" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"App name"`
		ID      string `path:"id" pattern:"^dep_[0-9A-Z]{26}$" doc:"Deploy ID"`
		Wait    int    `query:"wait" minimum:"0" maximum:"300" doc:"Seconds to wait for the deploy to finish (live, failed...) before answering; 0 answers at once"`
	}) (*struct{ Body *Deploy }, error) {
		r, err := m.rt()
		if err != nil {
			return nil, unavailable(err)
		}
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
			return nil, err
		}
		d, err := r.st.getDeploy(ctx, in.Project, in.App, in.ID)
		if err != nil {
			return nil, notFound(err, "deploy "+in.ID)
		}
		for deadline := time.Now().Add(time.Duration(in.Wait) * time.Second); !d.Terminal() && time.Now().Before(deadline); {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
			if d, err = r.st.getDeploy(ctx, in.Project, in.App, in.ID); err != nil {
				return nil, err
			}
		}
		return &struct{ Body *Deploy }{d}, nil
	}))

	bl := api.Untrusted(api.Op("deploy-build-log", http.MethodGet, appPath+"/deploys/{id}/build-log", "deploys build-log", api.RiskRead, "Read a deploy's build log",
		"The build output (Railpack plan, BuildKit steps, health checks, the failure reason). Read it in pieces with offset; "+
			"terminals can stream it with follow=true and Accept: text/event-stream.", "apps"))
	bl.Errors = append(bl.Errors, 404)
	bl.Middlewares = huma.Middlewares{m.followBuildLog(a)}
	huma.Register(a, bl, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		App     string `path:"app" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"App name"`
		ID      string `path:"id" pattern:"^dep_[0-9A-Z]{26}$" doc:"Deploy ID"`
		Offset  int64  `query:"offset" minimum:"0" doc:"Byte offset to start from (the offset of a previous read)"`
		Follow  bool   `query:"follow" doc:"Stream as server-sent events until the deploy finishes (needs Accept: text/event-stream)"`
	}) (*struct{ Body BuildLog }, error) {
		r, err := m.rt()
		if err != nil {
			return nil, unavailable(err)
		}
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
			return nil, err
		}
		d, err := r.st.getDeploy(ctx, in.Project, in.App, in.ID)
		if err != nil {
			return nil, notFound(err, "deploy "+in.ID)
		}
		text, off := r.readBuildLog(d, in.Offset, 4<<20)
		return &struct{ Body BuildLog }{BuildLog{Deploy: d.ID, Status: d.Status, Text: string(text), Offset: off, Done: d.Terminal()}}, nil
	}))

	rb := api.Op("deploy-rollback", http.MethodPost, appPath+"/deploys/{id}/rollback", "deploys rollback", api.RiskWrite, "Roll back to a deploy",
		"Makes an earlier deploy (status superseded or rolled_back) live again, with zero downtime: its instances start from the kept image, "+
			"pass health checks and take over; the current deploy becomes rolled_back. Waits until the switch is done.", "apps")
	rb.Errors = append(rb.Errors, 404, 409, 503)
	huma.Register(a, rb, api.Wrap(func(ctx context.Context, in *deployPath) (*struct{ Body *Deploy }, error) {
		r, err := m.rt()
		if err != nil {
			return nil, unavailable(err)
		}
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		d, err := r.rollback(ctx, in.Project, in.App, in.ID)
		if err != nil {
			return nil, r.toProblem(err, "deploy "+in.ID)
		}
		_ = r.p.DB.Audit(ctx, pr.TokenID, "deploy.rollback", in.Project+"/"+in.App, map[string]any{"deploy": d.ID, "session": pr.Session})
		return &struct{ Body *Deploy }{d}, nil
	}))

	lg := api.Untrusted(api.Op("app-logs", http.MethodGet, appPath+"/logs", "apps logs", api.RiskRead, "Read an app's logs",
		"What the app's instances wrote to stdout and stderr, oldest first: the newest limit lines, or the lines after since. "+
			"Poll with since=<next> for new lines; terminals can stream with follow=true and Accept: text/event-stream.", "apps"))
	lg.Middlewares = huma.Middlewares{m.followLogs(a)}
	huma.Register(a, lg, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		App     string `path:"app" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"App name"`
		Since   string `query:"since" doc:"Only lines after this time: RFC 3339 (the next value of a previous page) or a duration like 15m"`
		Limit   int    `query:"limit" minimum:"1" maximum:"5000" default:"200" doc:"Maximum lines"`
		Deploy  string `query:"deploy" doc:"Only lines from this deploy's instances"`
		Preview string `query:"preview" doc:"A preview's logs instead of production's"`
		Follow  bool   `query:"follow" doc:"Stream new lines as server-sent events (needs Accept: text/event-stream)"`
	}) (*struct{ Body LogPage }, error) {
		r, err := m.rt()
		if err != nil {
			return nil, unavailable(err)
		}
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
			return nil, err
		}
		since, err := parseSince(in.Since, time.Now())
		if err != nil {
			return nil, problem(422, "validation", err.Error(), "Use an RFC 3339 time or a duration like 15m.")
		}
		lines := r.readLogs(in.Project, in.App, in.Preview, in.Deploy, since, in.Limit)
		page := LogPage{Lines: lines}
		if n := len(lines); n > 0 {
			page.Next = lines[n-1].Time.Format(time.RFC3339Nano)
		} else if in.Since != "" {
			page.Next = since.Format(time.RFC3339Nano)
		}
		return &struct{ Body LogPage }{page}, nil
	}))

	st := api.Op("app-runtime", http.MethodGet, appPath+"/runtime", "apps status", api.RiskRead, "Show what runs for an app",
		"The live deploy, its instances (port, running or not), URL, and the app's previews.", "apps")
	st.Errors = append(st.Errors, 404)
	huma.Register(a, st, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		App     string `path:"app" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"App name"`
	}) (*struct{ Body AppRuntime }, error) {
		r, err := m.rt()
		if err != nil {
			return nil, unavailable(err)
		}
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
			return nil, err
		}
		out, err := r.appRuntime(ctx, in.Project, in.App)
		if err != nil {
			return nil, r.toProblem(err, "app "+in.App)
		}
		return &struct{ Body AppRuntime }{*out}, nil
	}))

	rs := api.Op("app-restart", http.MethodPost, appPath+"/restart", "apps restart", api.RiskWrite, "Restart an app",
		"Replaces every instance of the live deploy with fresh ones (zero downtime: new instances must pass health checks first). "+
			"Env and secret changes already restart apps on their own.", "apps")
	rs.Errors = append(rs.Errors, 404, 409, 503)
	huma.Register(a, rs, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		App     string `path:"app" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"App name"`
		Preview string `query:"preview" doc:"Restart this preview instead of production"`
	}) (*struct{ Body *Deploy }, error) {
		r, err := m.rt()
		if err != nil {
			return nil, unavailable(err)
		}
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		d, err := r.restart(ctx, in.Project, in.App, in.Preview)
		if err != nil {
			return nil, r.toProblem(err, "app "+in.App)
		}
		_ = r.p.DB.Audit(ctx, pr.TokenID, "app.restart", in.Project+"/"+in.App, map[string]any{"preview": in.Preview, "session": pr.Session})
		return &struct{ Body *Deploy }{d}, nil
	}))

	huma.Register(a, api.Op("previews-list", http.MethodGet, appPath+"/previews", "previews list", api.RiskRead, "List an app's previews",
		"Preview environments of an app: URL, live deploy, and whether they sleep (previews scale to zero when idle and wake on the next request).", "apps"),
		api.Wrap(func(ctx context.Context, in *struct {
			Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
			App     string `path:"app" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"App name"`
		}) (*struct{ Body []EnvStatus }, error) {
			r, err := m.rt()
			if err != nil {
				return nil, unavailable(err)
			}
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
				return nil, err
			}
			out, err := r.appRuntime(ctx, in.Project, in.App)
			if err != nil {
				return nil, r.toProblem(err, "app "+in.App)
			}
			return &struct{ Body []EnvStatus }{out.Previews}, nil
		}))

	pd := api.Op("preview-delete", http.MethodDelete, appPath+"/previews/{name}", "previews delete", api.RiskDestructive, "Delete a preview",
		"Stops a preview and removes its route. Its deploy records stay listed; production is not touched.", "apps")
	pd.Errors = append(pd.Errors, 404)
	huma.Register(a, pd, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		App     string `path:"app" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"App name"`
		Name    string `path:"name" pattern:"^[a-z0-9][a-z0-9-]{0,29}$" doc:"Preview name"`
	}) (*struct{}, error) {
		r, err := m.rt()
		if err != nil {
			return nil, unavailable(err)
		}
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		if err := r.deletePreview(ctx, in.Project, in.App, in.Name); err != nil {
			return nil, r.toProblem(err, "preview "+in.Name)
		}
		_ = r.p.DB.Audit(ctx, pr.TokenID, "preview.delete", in.Project+"/"+in.App+"@"+in.Name, map[string]any{"session": pr.Session})
		return &struct{}{}, nil
	}))

	sl := api.Op("preview-sleep", http.MethodPost, appPath+"/previews/{name}/sleep", "previews sleep", api.RiskWrite, "Put a preview to sleep",
		"Stops a preview's instance now to free memory; its URL keeps working and the next request wakes it (a few seconds). "+
			"Previews also sleep on their own after 15 idle minutes.", "apps")
	sl.Errors = append(sl.Errors, 404)
	huma.Register(a, sl, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		App     string `path:"app" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"App name"`
		Name    string `path:"name" pattern:"^[a-z0-9][a-z0-9-]{0,29}$" doc:"Preview name"`
	}) (*struct{ Body EnvStatus }, error) {
		r, err := m.rt()
		if err != nil {
			return nil, unavailable(err)
		}
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		st, err := r.st.getState(ctx, in.Project, in.App, in.Name)
		if err != nil {
			return nil, err
		}
		if st.Live == "" {
			return nil, problem(404, "not_found", "no preview "+in.Name, "")
		}
		r.sleep(ctx, in.Project, in.App, in.Name)
		out, err := r.appRuntime(ctx, in.Project, in.App)
		if err != nil {
			return nil, err
		}
		for _, p := range out.Previews {
			if p.Preview == in.Name {
				return &struct{ Body EnvStatus }{p}, nil
			}
		}
		return nil, problem(404, "not_found", "no preview "+in.Name, "")
	}))

	huma.Register(a, api.Op("git-info", http.MethodGet, "/v1/projects/{project}/git", "git info", api.RiskRead, "Show the project's git remote",
		"The box hosts a git remote per project. Pushing main (or master) deploys every app in the pushed tiffin.config.ts; "+
			"pushing another branch deploys previews named after it. Authenticate with any username and a token as the password.", "apps"),
		api.Wrap(func(ctx context.Context, in *struct {
			Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		}) (*struct{ Body GitInfo }, error) {
			if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
				return nil, err
			}
			base := ""
			if p != nil {
				base = strings.TrimRight(p.PublicURL, "/")
			}
			u := base + "/v1/git/" + in.Project + ".git"
			return &struct{ Body GitInfo }{GitInfo{URL: u, Instructions: "tiffin git-remote --add sets this up (remote, CA and a credential helper). Then: git push tiffin main"}}, nil
		}))

	m.registerGit(a)
	m.registerCreate(a)
	m.registerGitHubOps(a)
}

// checkDeployable validates a deploy request against the applied app.
func (r *rt) checkDeployable(ctx context.Context, project, app, preview string, prebuilt bool) (*manifest.App, *api.Problem) {
	if preview != "" && !previewRe.MatchString(preview) {
		return nil, problem(422, "validation", "preview names are 1-30 lowercase letters, digits and dashes, starting with a letter or digit", "")
	}
	spec, err := r.appSpec(ctx, project, app)
	if errors.Is(err, errNotFound) {
		return nil, problem(404, "not_found", "project "+project+" has no app "+app,
			"Add apps."+app+" to tiffin.config.ts, then run tiffin plan and tiffin apply --confirm <hash>.")
	}
	if err != nil {
		return nil, problem(500, "internal", err.Error(), "")
	}
	if prebuilt && spec.Framework == manifest.FrameworkStatic {
		return nil, problem(422, "validation", "static apps are served as files; --prebuilt takes an image tarball for container apps", "")
	}
	return spec, nil
}

// uploadMiddleware streams a gzipped tar (or image tarball) request body to
// disk and queues the deploy. JSON requests go on to the regular handler.
func (m *Module) uploadMiddleware(a huma.API) func(huma.Context, func(huma.Context)) {
	return func(hctx huma.Context, next func(huma.Context)) {
		ct := strings.ToLower(strings.TrimSpace(strings.Split(hctx.Header("Content-Type"), ";")[0]))
		if ct == "" || ct == "application/json" {
			next(hctx)
			return
		}
		ctx := hctx.Context()
		r, err := m.rt()
		if err != nil {
			writeProblem(hctx, unavailable(err))
			return
		}
		pr := api.PrincipalFrom(ctx)
		project, app := hctx.Param("project"), hctx.Param("app")
		if !validSlug(project) || !validSlug(app) {
			writeProblem(hctx, problem(422, "validation", "invalid project or app name", ""))
			return
		}
		if err := pr.Require(tokens.ScopeApplyReversible, project); err != nil {
			writeProblem(hctx, problem(403, "forbidden", err.Error(), "Deploying needs a key with full access to this project."))
			return
		}
		prebuilt := hctx.Query("prebuilt") == "true"
		switch ct {
		case "application/gzip", "application/x-gzip", "application/x-tar", "application/octet-stream":
		default:
			writeProblem(hctx, problem(415, "bad_request", "unsupported Content-Type "+ct, "Send application/gzip (a .tar.gz of the source) or application/json with files."))
			return
		}
		preview := hctx.Query("preview")
		spec, perr := r.checkDeployable(ctx, project, app, preview, prebuilt)
		if perr != nil {
			writeProblem(hctx, perr)
			return
		}
		source := SourceUpload
		file := "source.tgz"
		if prebuilt {
			source, file = SourcePrebuilt, "image.tar"
		}
		d, err := r.newDeploy(ctx, project, app, preview, source, pr.TokenID, spec)
		if err != nil {
			writeProblem(hctx, problem(500, "internal", err.Error(), ""))
			return
		}
		dest := filepath.Join(r.workDir(d), file)
		n, err := saveBody(hctx.BodyReader(), dest, MaxUpload)
		if err != nil {
			_ = r.st.deleteDeploy(ctx, d)
			_ = os.RemoveAll(r.workDir(d))
			status := 400
			if errors.Is(err, errTooLarge) {
				status = 413
			}
			writeProblem(hctx, problem(status, "bad_request", "upload failed: "+err.Error(), ""))
			return
		}
		d.SourceBytes = n
		_ = r.st.putDeploy(ctx, d)
		r.start(d, dest, source)
		_ = r.p.DB.Audit(ctx, pr.TokenID, "deploy.create", project+"/"+app, map[string]any{"deploy": d.ID, "preview": preview, "source": source, "bytes": n, "session": pr.Session})
		writeJSON(hctx, http.StatusAccepted, d)
	}
}

var errTooLarge = errors.New("upload is larger than 4 GB")

func saveBody(body io.Reader, dest string, limit int64) (int64, error) {
	if body == nil {
		return 0, errors.New("empty body")
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, io.LimitReader(body, limit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return n, err
	}
	if n > limit {
		return n, errTooLarge
	}
	if n == 0 {
		return 0, errors.New("empty body")
	}
	return n, nil
}

// packFiles writes inline files into dir and packs them as a source archive.
func packFiles(files map[string]string, dir, dest string) (int64, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	for name, body := range files {
		clean := path.Clean(strings.ReplaceAll(name, `\`, "/"))
		if clean == "." || strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") {
			return 0, fmt.Errorf("invalid file path %q", name)
		}
		p := filepath.Join(dir, filepath.FromSlash(clean))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return 0, err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return 0, err
		}
	}
	f, err := os.Create(dest)
	if err != nil {
		return 0, err
	}
	st, err := srcpack.Pack(dir, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return st.Bytes, err
}

// followBuildLog streams a build log as server-sent events.
func (m *Module) followBuildLog(a huma.API) func(huma.Context, func(huma.Context)) {
	return func(hctx huma.Context, next func(huma.Context)) {
		if hctx.Query("follow") != "true" || !strings.Contains(hctx.Header("Accept"), "text/event-stream") {
			next(hctx)
			return
		}
		ctx := hctx.Context()
		r, err := m.rt()
		if err != nil {
			writeProblem(hctx, unavailable(err))
			return
		}
		project, app, id := hctx.Param("project"), hctx.Param("app"), hctx.Param("id")
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, project); err != nil {
			writeProblem(hctx, problem(403, "forbidden", err.Error(), ""))
			return
		}
		d, err := r.st.getDeploy(ctx, project, app, id)
		if err != nil {
			writeProblem(hctx, problem(404, "not_found", "no deploy "+id, ""))
			return
		}
		var off int64
		fmt.Sscan(hctx.Query("offset"), &off)
		sse := startSSE(hctx)
		end := time.Now().Add(maxFollow)
		for {
			text, noff := r.readBuildLog(d, off, 1<<20)
			if len(text) > 0 {
				off = noff
				sse.event("log", map[string]any{"text": string(text), "offset": off})
				continue
			}
			if d.Terminal() {
				// One last read after the final status, then done.
				if text, noff := r.readBuildLog(d, off, 1<<20); len(text) > 0 {
					off = noff
					sse.event("log", map[string]any{"text": string(text), "offset": off})
				}
				sse.event("done", d)
				return
			}
			if time.Now().After(end) {
				sse.event("timeout", map[string]any{"offset": off})
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(300 * time.Millisecond):
			}
			sse.heartbeat()
			if nd, err := r.st.getDeploy(ctx, project, app, id); err == nil {
				d = nd
			}
		}
	}
}

// followLogs streams new app log lines as server-sent events.
func (m *Module) followLogs(a huma.API) func(huma.Context, func(huma.Context)) {
	return func(hctx huma.Context, next func(huma.Context)) {
		if hctx.Query("follow") != "true" || !strings.Contains(hctx.Header("Accept"), "text/event-stream") {
			next(hctx)
			return
		}
		ctx := hctx.Context()
		r, err := m.rt()
		if err != nil {
			writeProblem(hctx, unavailable(err))
			return
		}
		project, app := hctx.Param("project"), hctx.Param("app")
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, project); err != nil {
			writeProblem(hctx, problem(403, "forbidden", err.Error(), ""))
			return
		}
		preview, deploy := hctx.Query("preview"), hctx.Query("deploy")
		f := r.follow(project, app, preview, deploy)
		sse := startSSE(hctx)
		// Send recent lines first so the stream has context.
		if since := hctx.Query("since"); since != "" {
			if t, err := parseSince(since, time.Now()); err == nil {
				for _, l := range r.readLogs(project, app, preview, deploy, t, 1000) {
					sse.event("log", l)
				}
			}
		}
		end := time.Now().Add(maxFollow)
		for time.Now().Before(end) {
			for _, l := range f.poll() {
				sse.event("log", l)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(400 * time.Millisecond):
			}
			sse.heartbeat()
		}
		sse.event("timeout", map[string]any{"reconnect": true})
	}
}

type sseWriter struct {
	w        http.ResponseWriter
	rc       *http.ResponseController
	lastBeat time.Time
}

func startSSE(hctx huma.Context) *sseWriter {
	_, w := humago.Unwrap(hctx)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	s := &sseWriter{w: w, rc: http.NewResponseController(w), lastBeat: time.Now()}
	_ = s.rc.Flush()
	return s
}

func (s *sseWriter) event(name string, v any) {
	b, _ := json.Marshal(v)
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, b)
	_ = s.rc.Flush()
	s.lastBeat = time.Now()
}

func (s *sseWriter) heartbeat() {
	if time.Since(s.lastBeat) > 15*time.Second {
		fmt.Fprint(s.w, ": keep-alive\n\n")
		_ = s.rc.Flush()
		s.lastBeat = time.Now()
	}
}

func writeJSON(hctx huma.Context, status int, v any) {
	hctx.SetHeader("Content-Type", "application/json")
	hctx.SetStatus(status)
	_ = json.NewEncoder(hctx.BodyWriter()).Encode(v)
}

func writeProblem(hctx huma.Context, p *api.Problem) {
	hctx.SetHeader("Content-Type", "application/problem+json")
	hctx.SetStatus(p.Status)
	_ = json.NewEncoder(hctx.BodyWriter()).Encode(p)
}

func problem(status int, code, detail, hint string) *api.Problem {
	p := api.NewProblem(status, code, detail)
	p.Hint = hint
	return p
}

func unavailable(err error) *api.Problem {
	return problem(503, "internal", err.Error(), "Apps run on a box: tiffin up, then point the CLI at it.")
}

func notFound(err error, what string) error {
	if errors.Is(err, errNotFound) {
		return problem(404, "not_found", "no "+what, "")
	}
	return err
}

func (r *rt) toProblem(err error, what string) error {
	var se *stateError
	var he *healthError
	var ste *startError
	switch {
	case errors.Is(err, errNotFound):
		return problem(404, "not_found", "no "+what, "")
	case errors.As(err, &se):
		return problem(409, "precondition", se.msg, se.hint)
	case errors.As(err, &he):
		return problem(409, "precondition", he.msg, he.hint)
	case errors.As(err, &ste):
		return problem(503, "internal", "the container runtime would not start an instance: "+ste.msg, startHint)
	}
	return err
}

func validSlug(s string) bool {
	ok, _ := regexp.MatchString(projectPattern, s)
	return ok
}

// parseSince accepts RFC 3339 times and durations ("15m" means 15 minutes ago).
func parseSince(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return now.Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("since %q is neither an RFC 3339 time nor a duration", s)
}

// appRuntime describes what runs for an app.
func (r *rt) appRuntime(ctx context.Context, project, app string) (*AppRuntime, error) {
	spec, err := r.appSpec(ctx, project, app)
	if err != nil {
		return nil, err
	}
	out := &AppRuntime{Project: project, App: app, Framework: string(spec.Framework), Role: string(spec.Role), Routes: spec.Routes, Previews: []EnvStatus{}}
	states, err := r.st.statesOf(ctx, project, app)
	if err != nil {
		return nil, err
	}
	for _, s := range states {
		es := EnvStatus{Preview: s.Preview, Stopped: s.Stopped, Sleeping: s.Sleeping, Draining: s.Draining, UpdatedAt: s.UpdatedAt, Instances: []InstanceStatus{}}
		if s.Live != "" {
			if d, err := r.st.getDeploy(ctx, project, app, s.Live); err == nil {
				// The address now, from the current routes (a route change since the deploy moves it).
				es.Live, es.URL = d, r.deployURL(d, spec)
			}
		}
		for _, in := range s.Instances {
			is := InstanceStatus{Instance: in, State: "missing"}
			if c, err := r.eng.Inspect(ctx, in.Name); err == nil && c != nil {
				is.Running, is.State = c.Running, c.Status
			}
			es.Instances = append(es.Instances, is)
		}
		if s.Preview == "" {
			out.Prod = &es
		} else {
			out.Previews = append(out.Previews, es)
		}
	}
	if out.Prod == nil || out.Prod.Live == nil {
		out.Hint = "Not deployed yet: tiffin deploy --app " + app
	}
	return out, nil
}

// restart replaces an environment's instances (same deploy, fresh containers).
func (r *rt) restart(ctx context.Context, project, app, preview string) (*Deploy, error) {
	spec, err := r.appSpec(ctx, project, app)
	if err != nil {
		return nil, err
	}
	st, err := r.st.getState(ctx, project, app, preview)
	if err != nil {
		return nil, err
	}
	if st.Live == "" {
		return nil, &stateError{"nothing is deployed yet", "tiffin deploy --app " + app}
	}
	d, err := r.st.getDeploy(ctx, project, app, st.Live)
	if err != nil {
		return nil, err
	}
	if d.StaticRoot != "" {
		return d, nil // files have nothing to restart
	}
	if err := r.promote(ctx, d, spec, modeRestart, io.Discard); err != nil {
		return nil, err
	}
	return d, nil
}

// deletePreview stops a preview and forgets it.
func (r *rt) deletePreview(ctx context.Context, project, app, name string) error {
	unlock := r.lock(envKey(project, app, name))
	defer unlock()
	st, err := r.st.getState(ctx, project, app, name)
	if err != nil {
		return err
	}
	if st.Live == "" {
		return errNotFound
	}
	if err := r.st.deleteState(ctx, st); err != nil {
		return err
	}
	_ = r.refreshIfNeeded(ctx)
	r.removeInstances(ctx, st.Instances)
	if d, err := r.st.getDeploy(ctx, project, app, st.Live); err == nil {
		d.Status = StatusStopped
		_ = r.st.putDeploy(ctx, d)
	}
	_ = os.RemoveAll(r.envLogDir(project, app, name))
	return nil
}
