package portable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"filippo.io/age"
	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/boxfile"
	"github.com/btahir/tiffin/internal/change"
	"github.com/btahir/tiffin/internal/ids"
	"github.com/btahir/tiffin/internal/mod/runtime"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/state"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

// ExtExposesProjectData marks operations that hand out every piece of one
// project's data (database, files, cache, secrets): full access to that
// project only.
const ExtExposesProjectData = "x-tiffin-exposes-project-data"

// Project jobs.
const (
	JobDuplicate = "duplicate"
	JobImport    = "import"

	JobUploaded = "uploaded" // an import's archive is on the box, waiting for apply
	JobRunning  = "running"
	JobDone     = "done"
	JobFailed   = "failed"
)

// ProjectExport is one project export. The download makes the archive as
// it streams; nothing is kept on the box.
type ProjectExport struct {
	ID             string                   `json:"id" doc:"Project export ID (px_...)"`
	Project        string                   `json:"project"`
	Status         string                   `json:"status" enum:"pending,running,done,failed,expired" doc:"pending (the download starts it) → running → done or failed"`
	Phase          string                   `json:"phase,omitempty" doc:"What it is doing now"`
	IncludeSecrets bool                     `json:"includeSecrets" doc:"Secrets are inside in plain text (.env)"`
	WithHistory    bool                     `json:"withHistory"`
	CreatedBy      string                   `json:"createdBy"`
	CreatedAt      time.Time                `json:"createdAt"`
	StartedAt      *time.Time               `json:"startedAt,omitempty"`
	FinishedAt     *time.Time               `json:"finishedAt,omitempty"`
	ContentBytes   int64                    `json:"contentBytes" doc:"Uncompressed bytes written so far"`
	SizeBytes      int64                    `json:"sizeBytes,omitempty" doc:"Archive size"`
	SHA256         string                   `json:"sha256,omitempty" doc:"SHA-256 of the archive file"`
	DurationMs     int64                    `json:"durationMs,omitempty"`
	Parts          map[string]boxfile.Stats `json:"parts,omitempty"`
	Secrets        []string                 `json:"secrets,omitempty" doc:"Secret names in the archive"`
	SecretsNote    string                   `json:"secretsNote,omitempty"`
	Apps           []AppResult              `json:"apps,omitempty" doc:"The apps and their addresses on this box"`
	Download       string                   `json:"download" doc:"GET this path for the archive (once)"`
	FileName       string                   `json:"fileName" doc:"Suggested file name"`
	Error          string                   `json:"error,omitempty"`
	Hint           string                   `json:"hint,omitempty"`
}

// ArchiveSummary is what an uploaded project archive holds.
type ArchiveSummary struct {
	Project       string    `json:"project"`
	Domain        string    `json:"domain" doc:"The box it came from"`
	TiffinVersion string    `json:"tiffinVersion"`
	ExportedAt    time.Time `json:"exportedAt"`
	Apps          []string  `json:"apps"`
	Buckets       []string  `json:"buckets"`
	Postgres      bool      `json:"postgres"`
	Valkey        bool      `json:"valkey"`
	Secrets       []string  `json:"secrets"`
	SecretsPlain  bool      `json:"secretsPlain" doc:"Secrets are in plain text (.env)"`
	SecretsHere   bool      `json:"secretsHere" doc:"Sealed secrets are sealed to this box's key (it made the archive)"`
	Recipient     string    `json:"recipient,omitempty" doc:"The key sealed secrets need (the source box's)"`
	History       bool      `json:"history"`
	Taken         bool      `json:"taken" doc:"A project of this name exists here: import it under another name"`
}

// ProjectJob is a duplicate or an import: a new project being made.
type ProjectJob struct {
	ID             string          `json:"id" doc:"Job ID (pj_...)"`
	Kind           string          `json:"kind" enum:"duplicate,import"`
	Status         string          `json:"status" enum:"uploaded,running,done,failed" doc:"uploaded (an import waiting for apply) → running → done or failed. A job whose project was created but an app of which did not start is failed: created is true, apps says which app and why"`
	Phase          string          `json:"phase,omitempty" doc:"What it is doing now"`
	Percent        int             `json:"percent"`
	From           string          `json:"from" doc:"The project copied (duplicate) or the archive's project (import)"`
	Project        string          `json:"project,omitempty" doc:"The new project"`
	Source         *ArchiveSummary `json:"source,omitempty" doc:"What the uploaded archive holds (imports)"`
	SizeBytes      int64           `json:"sizeBytes,omitempty"`
	SHA256         string          `json:"sha256,omitempty"`
	CreatedBy      string          `json:"createdBy"`
	CreatedAt      time.Time       `json:"createdAt"`
	StartedAt      *time.Time      `json:"startedAt,omitempty"`
	FinishedAt     *time.Time      `json:"finishedAt,omitempty"`
	DurationMs     int64           `json:"durationMs,omitempty"`
	Created        bool            `json:"created" doc:"The new project exists (also after a failure: look at it, or destroy it)"`
	Change         string          `json:"change,omitempty" doc:"The change that created it: its History's first entry"`
	Apps           []AppResult     `json:"apps,omitempty"`
	SecretsMissing []string        `json:"secretsMissing,omitempty" doc:"Secrets not imported: set them by hand"`
	Notes          []string        `json:"notes,omitempty" doc:"What stayed behind (custom domains, GitHub deploys...)"`
	Healthy        bool            `json:"healthy" doc:"Done, and every app that had a release is live"`
	Error          string          `json:"error,omitempty"`
	Hint           string          `json:"hint,omitempty"`
}

// jobRun saves a project job's progress (throttled).
type jobRun struct {
	m    *Module
	p    *platform.Platform
	mu   sync.Mutex
	rec  *ProjectJob
	last time.Time
}

func (j *jobRun) set(f func(*ProjectJob), force bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	f(j.rec)
	if force || time.Since(j.last) > time.Second {
		j.last = time.Now()
		cp := *j.rec
		_ = j.m.save(j.p, "project-jobs", cp.ID, &cp)
	}
}

func (j *jobRun) reporter() reporter {
	return reporter{phase: func(s string, pct int) {
		j.set(func(r *ProjectJob) { r.Phase, r.Percent = s, max(r.Percent, pct) }, true)
	}}
}

// backend is the box's, or a test's.
func (m *Module) backend(p *platform.Platform) backend {
	if m.be != nil {
		return m.be
	}
	return boxBackend{p: p}
}

// finish records how a job ended.
func (j *jobRun) finish(res *imported, err error) {
	j.set(func(r *ProjectJob) {
		r.Phase, r.FinishedAt = "", now()
		if r.StartedAt != nil {
			r.DurationMs = time.Since(*r.StartedAt).Milliseconds()
		}
		if res != nil {
			r.Project = res.project
			r.Created, r.Change, r.Apps, r.SecretsMissing, r.Notes = res.created, res.change, res.apps, res.missing, res.notes
		}
		if err != nil {
			r.Status, r.Error = JobFailed, err.Error()
			var prob *api.Problem
			switch {
			case r.Created:
				r.Hint = fmt.Sprintf("Project %s was created, but not everything came across. Look at it with `tiffin projects get %[1]s`; "+
					"to start over, destroy it (`tiffin projects destroy %[1]s`) and try again under another name.", r.Project)
			case errors.As(err, &prob) && prob.Hint != "":
				r.Hint = "Nothing was created. " + prob.Hint
			case errors.Is(err, ErrInvalid):
				r.Hint = "Nothing was created. Make the archive again with `tiffin projects export`."
			default:
				r.Hint = "Nothing was created. Fix the cause and try again."
			}
			return
		}
		r.Percent = 100
		// An app that did not start fails the job: the project is there with
		// its data, but it is not what was copied until that app runs.
		var failed, why []string
		var first *AppResult
		for i, a := range r.Apps {
			if a.Status != runtime.StatusLive && a.Status != "none" {
				failed = append(failed, a.App)
				msg := a.Error
				if msg == "" {
					msg = "its deploy is " + a.Status
				}
				why = append(why, fmt.Sprintf("app %s did not start: %s", a.App, msg))
				if first == nil {
					first = &r.Apps[i]
				}
			}
		}
		if first == nil {
			r.Status, r.Healthy = JobDone, true
			return
		}
		r.Status, r.Error = JobFailed, strings.Join(why, "; ")
		r.Hint = fmt.Sprintf("Project %s was created with its data, but %s did not start.", r.Project, strings.Join(failed, " and "))
		if first.Hint != "" {
			r.Hint += " " + first.Hint
		}
		if first.Deploy != "" {
			r.Hint += fmt.Sprintf(" Its build log: `tiffin deploys build-log %s %s %s`.", r.Project, first.App, first.Deploy)
		}
		r.Hint += fmt.Sprintf(" Deploy it again once fixed, or destroy the project (`tiffin projects destroy %s`) and import again.", r.Project)
	}, true)
	j.mu.Lock()
	rec := *j.rec
	j.mu.Unlock()
	if rec.Status == JobFailed {
		j.p.Log.Error("project "+rec.Kind+" failed", "job", rec.ID, "project", rec.Project, "err", rec.Error)
	} else {
		j.p.Log.Info("project "+rec.Kind+" done", "job", rec.ID, "project", rec.Project)
	}
}

// start runs work in the background (box lifetime), one duplicate or
// import at a time.
func (m *Module) startJob(p *platform.Platform, rec *ProjectJob, work func(ctx context.Context, rep reporter) (*imported, error)) {
	ctx, cancel := context.WithCancel(m.boxCtx())
	m.setCancel(rec.ID, cancel)
	j := &jobRun{m: m, p: p, rec: rec}
	go func() {
		defer cancel()
		defer m.setCancel(rec.ID, nil)
		j.set(func(r *ProjectJob) {
			r.Status, r.StartedAt, r.Error, r.Hint, r.Percent = JobRunning, now(), "", "", 0
			r.Phase = "waiting for another duplicate or import to finish"
		}, true)
		m.projMu.Lock()
		defer m.projMu.Unlock()
		res, err := work(ctx, j.reporter())
		j.finish(res, err)
	}()
}

var errStopped = errors.New("the import stopped")

// duplicate copies project from into a new project: an export streamed
// straight into an import (images are tagged, not copied).
func (m *Module) duplicate(ctx context.Context, p *platform.Platform, pr *tokens.Principal, from, name string, rep reporter) (*imported, error) {
	b := m.backend(p)
	stage := filepath.Join(dir(p), "staging", ids.New("dup"))
	defer os.RemoveAll(stage)
	r, w := io.Pipe()
	wctx, stop := context.WithCancel(ctx)
	defer stop()
	werr := make(chan error, 1)
	go func() {
		_, err := writeProject(wctx, p, b, from, exportOptions{sameBox: true, stage: filepath.Join(stage, "out")}, reporter{}, w)
		_ = w.CloseWithError(err)
		werr <- err
	}()
	res, err := importProject(ctx, p, b, r, importOptions{name: name, intent: "Duplicated from " + from, sameBox: true,
		stage: filepath.Join(stage, "in"), principal: pr}, rep)
	_ = r.CloseWithError(errStopped)
	if err != nil {
		stop() // the import gave up: so does the export
	}
	e := <-werr
	if e != nil && !errors.Is(e, errStopped) && !errors.Is(e, context.Canceled) && (err == nil || res == nil || !res.created) {
		err = e // the export's own failure says more than the import's view of it
	}
	return res, err
}

// exportTo writes a project export's archive to out and records it.
func (m *Module) exportTo(ctx context.Context, p *platform.Platform, rec *ProjectExport, out io.Writer) error {
	start := time.Now()
	stage := filepath.Join(dir(p), "staging", rec.ID)
	defer os.RemoveAll(stage)
	var mu sync.Mutex
	last := time.Time{}
	set := func(f func(*ProjectExport), force bool) {
		mu.Lock()
		defer mu.Unlock()
		f(rec)
		if force || time.Since(last) > time.Second {
			last = time.Now()
			cp := *rec
			_ = m.save(p, "project-exports", cp.ID, &cp)
		}
	}
	set(func(e *ProjectExport) { e.Status, e.StartedAt = ExportRunning, now() }, true)
	rep := reporter{phase: func(s string, _ int) { set(func(e *ProjectExport) { e.Phase = s }, true) },
		bytes: func(n int64) { set(func(e *ProjectExport) { e.ContentBytes += n }, false) }}
	wr, err := writeProject(ctx, p, m.backend(p), rec.Project, exportOptions{includeSecrets: rec.IncludeSecrets, withHistory: rec.WithHistory, stage: stage}, rep, out)
	if err != nil {
		set(func(e *ProjectExport) {
			e.Status, e.Phase, e.FinishedAt, e.Error = ExportFailed, "", now(), err.Error()
			e.Hint = "Start a new export; if it fails again, check `journalctl -u tiffin` on the box."
			if errors.Is(err, context.Canceled) {
				e.Error, e.Hint = "cancelled: the download stopped before the archive was complete", "Start a new export and keep the download running until it ends."
			}
		}, true)
		return err
	}
	set(func(e *ProjectExport) {
		e.Status, e.Phase, e.FinishedAt = ExportDone, "", now()
		e.SizeBytes, e.SHA256, e.Parts, e.DurationMs = wr.size, wr.sha256, wr.parts, time.Since(start).Milliseconds()
		e.Secrets = wr.info.Secrets.Names
		switch {
		case len(e.Secrets) == 0:
		case wr.info.Secrets.Plain:
			e.SecretsNote = "Secrets are inside in plain text (.env): keep the file private."
		default:
			e.SecretsNote = "Secrets are sealed to this box's key: importing on this box brings them; another box needs that key " +
				"(--secrets-key-file), or they are left out and listed."
		}
		e.Apps = nil
		for _, a := range wr.info.Apps {
			st := "none"
			if a.Deploy != "" {
				st = runtime.StatusLive
			}
			e.Apps = append(e.Apps, AppResult{App: a.Name, Deploy: a.Deploy, Status: st, URL: a.URL})
		}
	}, true)
	return nil
}

// summarizeProject reads project.json from a stored archive.
func summarizeProject(ctx context.Context, p *platform.Platform, b backend, path string) (*ArchiveSummary, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	ar, err := boxfile.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer ar.Close()
	e, err := ar.Next()
	if err != nil || e.Name != "project.json" {
		return nil, fmt.Errorf("%w: the archive does not start with project.json", ErrInvalid)
	}
	var info ProjectInfo
	if err := json.NewDecoder(e.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("%w: project.json: %v", ErrInvalid, err)
	}
	s := &ArchiveSummary{Project: info.Project, Domain: info.Source.Domain, TiffinVersion: info.TiffinVersion, ExportedAt: info.ExportedAt,
		Apps: []string{}, Buckets: []string{}, Postgres: info.Postgres != nil, Valkey: info.Valkey != nil, Secrets: info.Secrets.Names,
		SecretsPlain: info.Secrets.Plain, Recipient: info.Secrets.Recipient, History: info.History}
	if s.Secrets == nil {
		s.Secrets = []string{}
	}
	for _, a := range info.Apps {
		s.Apps = append(s.Apps, a.Name)
	}
	for _, bk := range info.Buckets {
		s.Buckets = append(s.Buckets, bk.Name)
	}
	mine, _ := keyRecipient(p.Home)
	s.SecretsHere = s.Recipient != "" && s.Recipient == mine
	existing, _ := p.DB.ListProjects(ctx)
	s.Taken = checkNewName(ctx, b, existing, info.Project) != nil
	return s, nil
}

// fullAccess: an export or duplicate hands out all of a project's data.
func fullAccess(ctx context.Context, project string) (*tokens.Principal, error) {
	pr := api.PrincipalFrom(ctx)
	if pr == nil {
		return nil, tokens.ErrUnauthenticated
	}
	if err := pr.Require(tokens.ScopeApplyIrreversible, project); err != nil {
		return nil, fmt.Errorf("%w (it hands out all of the project's data, secrets included, so it needs full access to %s)", err, project)
	}
	return pr, nil
}

func projectExists(ctx context.Context, p *platform.Platform, project string) error {
	_, res, err := p.DB.Load(ctx, project)
	if err != nil {
		return err
	}
	if len(res) == 0 {
		return api.NewProblem(404, "not_found", "project "+project+" does not exist")
	}
	return nil
}

func jobNotFound(id string) error {
	return api.NewProblem(404, "not_found", "no project job "+id)
}

// registerProjects adds the project duplicate, export, import, stop and
// start operations.
func (m *Module) registerProjects(a huma.API, p *platform.Platform) {
	const tag = "projects"

	// ---- duplicate ----

	dup := api.Op("project-duplicate", http.MethodPost, "/v1/projects/{project}/duplicate", "projects duplicate", api.RiskWrite,
		"Duplicate a project",
		"Makes a full copy of a project on this box under a new name: its database, buckets and files, cache keys, secrets, "+
			"settings and apps (started from the same images or files, at the new project's own addresses: shop → shop-copy). "+
			"Custom domains and GitHub deploys stay with the original; the copy starts its own History (\"Duplicated from <project>\"). "+
			"Returns at once with a job; poll projects jobs get until status is done. To undo, destroy the copy. "+
			"Needs full access to the project and permission to create the new one.", tag)
	dup.Extensions[ExtExposesProjectData] = true
	dup.DefaultStatus = http.StatusAccepted
	dup.Errors = append(dup.Errors, 404, 409)
	huma.Register(a, dup, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Body    struct {
			Name string `json:"name" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"The copy's name, e.g. shop-copy"`
		}
	}) (*struct{ Body *ProjectJob }, error) {
		pr, err := fullAccess(ctx, in.Project)
		if err != nil {
			return nil, err
		}
		if err := pr.Require(tokens.ScopeApplyReversible, in.Body.Name); err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		if err := projectExists(ctx, p, in.Project); err != nil {
			return nil, err
		}
		existing, err := p.DB.ListProjects(ctx)
		if err != nil {
			return nil, err
		}
		if err := checkNewName(ctx, m.backend(p), existing, in.Body.Name); err != nil {
			return nil, err
		}
		rec := &ProjectJob{ID: ids.New("pj"), Kind: JobDuplicate, Status: JobRunning, Phase: "starting", From: in.Project, Project: in.Body.Name,
			CreatedBy: pr.TokenID, CreatedAt: time.Now().UTC()}
		if err := m.save(p, "project-jobs", rec.ID, rec); err != nil {
			return nil, err
		}
		_ = p.DB.Audit(ctx, pr.TokenID, "project.duplicate", in.Project, map[string]any{"session": pr.Session, "job": rec.ID, "name": in.Body.Name})
		cp := *rec
		m.startJob(p, rec, func(ctx context.Context, rep reporter) (*imported, error) {
			return m.duplicate(ctx, p, pr, in.Project, in.Body.Name, rep)
		})
		return &struct{ Body *ProjectJob }{&cp}, nil
	}))

	jg := api.Op("project-job-get", http.MethodGet, "/v1/project-jobs/{id}", "projects jobs get", api.RiskRead,
		"Show a duplicate or import",
		"One project duplicate or import: what it copies, then its phase and progress, and at the end the new project, its change, "+
			"its apps' addresses, secrets left out and what stayed behind.", tag)
	jg.Errors = append(jg.Errors, 404)
	huma.Register(a, jg, api.Wrap(func(ctx context.Context, in *struct {
		ID string `path:"id" pattern:"^pj_[0-9A-Z]{26}$" doc:"Job ID"`
	}) (*struct{ Body *ProjectJob }, error) {
		if err := onBox(p); err != nil {
			return nil, err
		}
		rec, err := load[ProjectJob](p, "project-jobs", in.ID)
		if err != nil {
			return nil, jobNotFound(in.ID)
		}
		pr := api.PrincipalFrom(ctx)
		if pr == nil || (pr.TokenID != rec.CreatedBy && !pr.BoxAdmin()) {
			return nil, jobNotFound(in.ID) // someone else's
		}
		return &struct{ Body *ProjectJob }{rec}, nil
	}))

	// ---- export ----

	ec := api.Op("project-export-create", http.MethodPost, "/v1/projects/{project}/exports", "projects export", api.RiskWrite,
		"Export a project",
		"Starts an export of one project to a single .tiffin file of ordinary files: database.sql (pg_dump), files/<bucket>/..., "+
			"the cache keys, the apps (image tarball or static files, and the git repository), tiffin.config.ts, project.json, and a "+
			"docker-compose.yml with a README that run it without Tiffin. GET the download path to run it: the archive is made as it "+
			"downloads and nothing is kept on the box. Secrets stay sealed to this box's key unless includeSecrets puts them inside in "+
			"plain text (.env). Needs full access to the project.", tag)
	ec.Extensions[ExtExposesProjectData] = true
	ec.Errors = append(ec.Errors, 404)
	huma.Register(a, ec, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Body    struct {
			IncludeSecrets bool `json:"includeSecrets,omitempty" doc:"Put the secrets inside in plain text (.env): anyone with the file can read them"`
			WithHistory    bool `json:"withHistory,omitempty" doc:"Also export the project's History (every change)"`
		}
	}) (*struct{ Body *ProjectExport }, error) {
		pr, err := fullAccess(ctx, in.Project)
		if err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		if err := projectExists(ctx, p, in.Project); err != nil {
			return nil, err
		}
		rec := &ProjectExport{ID: ids.New("px"), Project: in.Project, Status: ExportPending, Phase: "waiting for the download to start it",
			IncludeSecrets: in.Body.IncludeSecrets, WithHistory: in.Body.WithHistory, CreatedBy: pr.TokenID, CreatedAt: time.Now().UTC()}
		rec.Download = "/v1/projects/" + in.Project + "/exports/" + rec.ID + "/download"
		rec.FileName = in.Project + "-" + rec.CreatedAt.Format("20060102-150405") + boxfile.FileExt
		if err := m.save(p, "project-exports", rec.ID, rec); err != nil {
			return nil, err
		}
		_ = p.DB.Audit(ctx, pr.TokenID, "project.export", in.Project, map[string]any{"session": pr.Session, "export": rec.ID,
			"includeSecrets": rec.IncludeSecrets, "withHistory": rec.WithHistory})
		return &struct{ Body *ProjectExport }{rec}, nil
	}))

	eg := api.Op("project-export-get", http.MethodGet, "/v1/projects/{project}/exports/{id}", "projects exports get", api.RiskRead,
		"Show a project export", "One project export: status and phase while it downloads; size, SHA-256, what it holds and the apps' addresses when done.", tag)
	eg.Errors = append(eg.Errors, 404)
	huma.Register(a, eg, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		ID      string `path:"id" pattern:"^px_[0-9A-Z]{26}$" doc:"Project export ID"`
	}) (*struct{ Body *ProjectExport }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, in.Project); err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		rec, err := load[ProjectExport](p, "project-exports", in.ID)
		if err != nil || rec.Project != in.Project {
			return nil, api.NewProblem(404, "not_found", "no export "+in.ID+" of project "+in.Project)
		}
		return &struct{ Body *ProjectExport }{rec}, nil
	}))

	ed := api.Op("project-export-download", http.MethodGet, "/v1/projects/{project}/exports/{id}/download", "-", api.RiskRead,
		"Download a project export",
		"The archive (application/octet-stream), made as it streams (once). The export's record has the size and SHA-256 to compare at the end. "+
			"Needs full access to the project.", tag)
	ed.Extensions[ExtExposesProjectData] = true
	ed.Errors = append(ed.Errors, 404, 409)
	ed.Responses = map[string]*huma.Response{"200": {Description: "The archive", Content: binary("A .tiffin project archive (zstd-compressed tar)")}}
	huma.Register(a, ed, api.Wrap(func(ctx context.Context, in *struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		ID      string `path:"id" pattern:"^px_[0-9A-Z]{26}$" doc:"Project export ID"`
	}) (*huma.StreamResponse, error) {
		pr, err := fullAccess(ctx, in.Project)
		if err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		rec, err := load[ProjectExport](p, "project-exports", in.ID)
		if err != nil || rec.Project != in.Project {
			return nil, api.NewProblem(404, "not_found", "no export "+in.ID+" of project "+in.Project)
		}
		if rec.Status != ExportPending {
			prob := api.NewProblem(409, "precondition", "export "+rec.ID+" is "+rec.Status+"; an export downloads exactly once")
			prob.Hint = "start a new one with `tiffin projects export " + in.Project + "`"
			return nil, prob
		}
		_ = p.DB.Audit(ctx, pr.TokenID, "project.export.download", in.Project, map[string]any{"session": pr.Session, "export": rec.ID})
		return &huma.StreamResponse{Body: func(hctx huma.Context) {
			_, w := humago.Unwrap(hctx)
			ctx, cancel := context.WithCancel(hctx.Context())
			defer cancel()
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", `attachment; filename="`+rec.FileName+`"`)
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			fw := &flushWriter{w: w}
			if err := m.exportTo(ctx, p, rec, fw); err != nil {
				p.Log.Error("project export failed", "export", rec.ID, "err", err)
				panic(http.ErrAbortHandler) // the client sees an incomplete download
			}
			fw.flush()
		}}, nil
	}))

	// ---- import ----

	iu := api.Op("project-import-upload", http.MethodPost, "/v1/project-imports", "-", api.RiskWrite,
		"Upload a project export to import",
		"Send a .tiffin project archive as the raw request body (application/octet-stream). It is stored on the box and verified as it "+
			"arrives; the job it returns says what the archive holds and whether its name is free here. Nothing changes until you apply it.", tag)
	iu.Errors = append(iu.Errors, 413, 422, 507)
	iu.RequestBody = &huma.RequestBody{Required: true, Description: "The archive", Content: binary("A .tiffin archive made by tiffin projects export")}
	huma.Register(a, iu, api.Wrap(func(ctx context.Context, in *uploadInput) (*struct{ Body *ProjectJob }, error) {
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyReversible, ""); err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		if in.body == nil {
			return nil, api.NewProblem(400, "validation", "send the archive as the request body")
		}
		if in.ContentLength > 0 {
			if free := freeBytes(dir(p)); free >= 0 && free < in.ContentLength+512<<20 {
				return nil, api.NewProblem(507, "precondition", fmt.Sprintf("not enough disk space for the upload (%s free, the archive is %s)", human(free), human(in.ContentLength)))
			}
		}
		rec := &ProjectJob{ID: ids.New("pj"), Kind: JobImport, Status: JobUploaded, CreatedBy: pr.TokenID, CreatedAt: time.Now().UTC()}
		path := archivePath(p, "project-imports", rec.ID)
		st, err := storeArchive(path, in.body, func(man *boxfile.Manifest) error { return boxfile.CheckProject(man, state.SchemaVersion(), p.Version) })
		if err != nil {
			if errors.Is(err, ErrInvalid) {
				prob := api.NewProblem(422, "validation", err.Error())
				prob.Hint = "make the archive with `tiffin projects export`, or update this box with `tiffin up` if it came from a newer Tiffin"
				return nil, prob
			}
			return nil, err
		}
		if rec.Source, err = summarizeProject(ctx, p, m.backend(p), path); err != nil {
			_ = os.Remove(path)
			return nil, api.NewProblem(422, "validation", err.Error())
		}
		rec.From, rec.SizeBytes, rec.SHA256 = rec.Source.Project, st.size, st.sha256
		if err := m.save(p, "project-jobs", rec.ID, rec); err != nil {
			return nil, err
		}
		_ = p.DB.Audit(ctx, pr.TokenID, "project.import.upload", rec.From, map[string]any{"session": pr.Session, "job": rec.ID, "bytes": st.size, "sha256": st.sha256})
		return &struct{ Body *ProjectJob }{rec}, nil
	}))

	ia := api.Op("project-import-apply", http.MethodPost, "/v1/project-imports/{id}/apply", "projects imports apply", api.RiskWrite,
		"Import an uploaded project export",
		"Creates a new project from an uploaded archive, beside the box's other projects (an import never replaces one): its database, "+
			"files, cache, secrets and settings, then its apps from the archive's images or files. A name in use is refused: pass name. "+
			"Under another name than the archive's, the apps get the new project's own addresses and custom domains and GitHub deploys "+
			"are left out. Secrets sealed to another box's key need that key (secretsKey), or withoutSecrets to leave them out (the job "+
			"lists them). Returns at once; poll projects jobs get until status is done. Needs permission to create the project.", tag)
	ia.DefaultStatus = http.StatusAccepted
	ia.Errors = append(ia.Errors, 404, 409, 422)
	huma.Register(a, ia, api.Wrap(func(ctx context.Context, in *struct {
		ID   string `path:"id" pattern:"^pj_[0-9A-Z]{26}$" doc:"Job ID of the upload"`
		Body struct {
			Name           string `json:"name,omitempty" pattern:"^([a-z][a-z0-9-]{0,39})?$" doc:"The new project's name; default the archive's"`
			SecretsKey     string `json:"secretsKey,omitempty" maxLength:"200" doc:"The source box's key (AGE-SECRET-KEY-1...), for secrets sealed to it"`
			WithoutSecrets bool   `json:"withoutSecrets,omitempty" doc:"Leave the secrets out (the job lists them, to set by hand)"`
			Intent         string `json:"intent,omitempty" maxLength:"500" doc:"The new project's first History entry"`
		}
	}) (*struct{ Body *ProjectJob }, error) {
		pr := api.PrincipalFrom(ctx)
		if err := onBox(p); err != nil {
			return nil, err
		}
		rec, err := load[ProjectJob](p, "project-jobs", in.ID)
		if err != nil || rec.Kind != JobImport || (rec.CreatedBy != pr.TokenID && !pr.BoxAdmin()) {
			return nil, jobNotFound(in.ID)
		}
		if rec.Status != JobUploaded && !(rec.Status == JobFailed && !rec.Created) {
			prob := api.NewProblem(409, "precondition", "import "+rec.ID+" is "+rec.Status)
			prob.Hint = "upload the archive again to start over"
			return nil, prob
		}
		path := archivePath(p, "project-imports", rec.ID)
		if _, err := os.Stat(path); err != nil {
			return nil, api.NewProblem(409, "precondition", "the archive of import "+rec.ID+" is gone; upload it again")
		}
		name := in.Body.Name
		if name == "" {
			name = rec.From
		}
		if err := pr.Require(tokens.ScopeApplyReversible, name); err != nil {
			return nil, err
		}
		existing, err := p.DB.ListProjects(ctx)
		if err != nil {
			return nil, err
		}
		if err := checkNewName(ctx, m.backend(p), existing, name); err != nil {
			return nil, err
		}
		o := importOptions{name: name, skipSecrets: in.Body.WithoutSecrets, principal: pr, intent: in.Body.Intent}
		if o.intent == "" {
			o.intent = fmt.Sprintf("Imported from an export of %s (%s, %s)", rec.From, orDash(rec.Source.Domain), rec.Source.ExportedAt.Format("2006-01-02"))
		}
		s := rec.Source
		if len(s.Secrets) > 0 && !s.SecretsPlain && !s.SecretsHere && !o.skipSecrets {
			if strings.TrimSpace(in.Body.SecretsKey) == "" {
				prob := api.NewProblem(422, "validation", fmt.Sprintf("the archive's secrets (%s) are sealed to another box's key (%s)", strings.Join(s.Secrets, ", "), s.Recipient))
				prob.Hint = "pass that box's key as secretsKey (tiffin projects import --secrets-key-file <file>), or withoutSecrets to set them by hand afterwards"
				return nil, prob
			}
			id, err := age.ParseX25519Identity(strings.TrimSpace(in.Body.SecretsKey))
			if err != nil {
				return nil, api.NewProblem(422, "validation", "secretsKey is not an age secret key (AGE-SECRET-KEY-1...)")
			}
			if got := id.Recipient().String(); got != s.Recipient {
				return nil, api.NewProblem(422, "validation", fmt.Sprintf("that key (%s) is not the one the secrets are sealed to (%s)", got, s.Recipient))
			}
			o.sourceKey = id
		}
		rec.Status, rec.Phase, rec.Project, rec.Error, rec.Hint = JobRunning, "starting", name, "", ""
		if err := m.save(p, "project-jobs", rec.ID, rec); err != nil {
			return nil, err
		}
		_ = p.DB.Audit(ctx, pr.TokenID, "project.import", name, map[string]any{"session": pr.Session, "job": rec.ID, "from": rec.From, "sha256": rec.SHA256})
		cp := *rec
		o.stage = filepath.Join(dir(p), "staging", rec.ID)
		m.startJob(p, rec, func(ctx context.Context, rep reporter) (*imported, error) {
			defer os.RemoveAll(o.stage)
			f, err := os.Open(path)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			res, err := importProject(ctx, p, m.backend(p), f, o, rep)
			if err == nil || (res != nil && res.created) {
				_ = os.Remove(path) // it may hold plain secrets: only kept for a retry
			}
			return res, err
		})
		return &struct{ Body *ProjectJob }{&cp}, nil
	}))

	idel := api.Op("project-import-delete", http.MethodDelete, "/v1/project-imports/{id}", "projects imports discard", api.RiskWrite,
		"Discard an uploaded project export", "Deletes an uploaded archive that has not been imported (or whose import failed before making anything).", tag)
	idel.Errors = append(idel.Errors, 404, 409)
	huma.Register(a, idel, api.Wrap(func(ctx context.Context, in *struct {
		ID string `path:"id" pattern:"^pj_[0-9A-Z]{26}$" doc:"Job ID of the upload"`
	}) (*struct{ Body *ProjectJob }, error) {
		pr := api.PrincipalFrom(ctx)
		if err := onBox(p); err != nil {
			return nil, err
		}
		rec, err := load[ProjectJob](p, "project-jobs", in.ID)
		if err != nil || rec.Kind != JobImport || (rec.CreatedBy != pr.TokenID && !pr.BoxAdmin()) {
			return nil, jobNotFound(in.ID)
		}
		if rec.Status == JobRunning {
			return nil, api.NewProblem(409, "precondition", "import "+rec.ID+" is running; it cannot be discarded now")
		}
		_ = os.Remove(archivePath(p, "project-imports", rec.ID))
		_ = os.Remove(recordPath(p, "project-jobs", rec.ID))
		return &struct{ Body *ProjectJob }{rec}, nil
	}))

	// ---- stop and start ----

	type stopIn = struct {
		Project string `path:"project" pattern:"^[a-z][a-z0-9-]{0,39}$" doc:"Project slug"`
		Body    *struct {
			Intent string `json:"intent,omitempty" maxLength:"500" doc:"Why, in one sentence (History shows it)"`
		} `required:"false"`
	}
	stopStart := func(ctx context.Context, in *stopIn, stop bool) (*struct{ Body api.ApplyResult }, error) {
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyReversible, in.Project); err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		if err := projectExists(ctx, p, in.Project); err != nil {
			return nil, err
		}
		intent := "Stop every app of " + in.Project
		if !stop {
			intent = "Start the apps of " + in.Project + " again"
		}
		if in.Body != nil && in.Body.Intent != "" {
			intent = in.Body.Intent
		}
		plan, err := p.Engine.PlanEdit(ctx, in.Project, func(cur map[string]change.Resource) (map[string]change.Resource, error) {
			if stop {
				if _, ok := cur[change.KindStopped]; !ok {
					spec, _ := json.Marshal(map[string]string{"reason": intent})
					cur[change.KindStopped] = change.Resource{Address: change.KindStopped, Spec: spec}
				}
			} else {
				delete(cur, change.KindStopped)
			}
			return cur, nil
		})
		if err != nil {
			return nil, err
		}
		c, err := p.Engine.Apply(ctx, change.ApplyRequest{Plan: plan, Confirm: plan.Hash, Actor: pr.Actor(), Intent: intent, Authorize: pr.Authorizer()})
		if err != nil {
			return nil, err
		}
		p.AfterApply(c)
		out := api.ApplyResult{Applied: c != nil, Change: c}
		if c == nil {
			out.Plan = plan
		}
		return &struct{ Body api.ApplyResult }{out}, nil
	}
	so := api.Op("project-stop", http.MethodPost, "/v1/projects/{project}/stop", "projects stop", api.RiskWrite,
		"Stop a project's apps",
		"Stops every app of the project and keeps them stopped (deploys and restarts are refused) until it is started again; its data "+
			"and settings stay as they are. tiffin projects move leaves the project on the old box this way. A change in History: undo it, "+
			"or projects start, to bring the apps back.", tag)
	so.Errors = append(so.Errors, 404)
	huma.Register(a, so, api.Wrap(func(ctx context.Context, in *stopIn) (*struct{ Body api.ApplyResult }, error) {
		return stopStart(ctx, in, true)
	}))
	sa := api.Op("project-start", http.MethodPost, "/v1/projects/{project}/start", "projects start", api.RiskWrite,
		"Start a stopped project's apps", "Lifts a project's stop: its apps start again from their live deploys. Nothing to do if it is not stopped.", tag)
	sa.Errors = append(sa.Errors, 404)
	huma.Register(a, sa, api.Wrap(func(ctx context.Context, in *stopIn) (*struct{ Body api.ApplyResult }, error) {
		return stopStart(ctx, in, false)
	}))
}
