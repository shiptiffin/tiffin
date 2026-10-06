package portable

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/boxfile"
	"github.com/btahir/tiffin/internal/ids"
	"github.com/btahir/tiffin/internal/mod/datakit"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

// ExtExposesAllData marks operations that hand out (or take in) every
// project's data, secrets included: owner and admins only.
const ExtExposesAllData = "x-tiffin-exposes-all-data"

func onBox(p *platform.Platform) error {
	if p == nil || p.DB == nil || p.Home == "" {
		return api.NewProblem(501, "internal", datakit.ErrOffBox.Error())
	}
	return nil
}

// requireAdmin: exports and imports cover every project, secrets included.
func requireAdmin(ctx context.Context, what string) (*tokens.Principal, error) {
	pr := api.PrincipalFrom(ctx)
	if pr == nil || !pr.BoxAdmin() {
		return nil, fmt.Errorf("%w: %s covers every project's data and secrets; it needs the box owner's or an admin's token", tokens.ErrForbidden, what)
	}
	return pr, nil
}

func notFound(kind, id string) error {
	p := api.NewProblem(404, "not_found", "no "+kind+" "+id)
	p.Hint = "list them with `tiffin box " + kind + "s list`"
	return p
}

func busyProblem(err error) error {
	p := api.NewProblem(409, "conflict", err.Error())
	p.Hint = "wait for the running backup, restore, export or import to finish (tiffin backups list, tiffin box exports list, tiffin box imports list)"
	return p
}

func binary(desc string) map[string]*huma.MediaType {
	return map[string]*huma.MediaType{"application/octet-stream": {Schema: &huma.Schema{Type: "string", Format: "binary", Description: desc}}}
}

// uploadInput hands the raw request body to the handler (huma does not
// read bodies of operations without a Body field).
type uploadInput struct {
	ContentLength int64 `header:"Content-Length" doc:"Archive size in bytes"`
	body          io.Reader
}

func (u *uploadInput) Resolve(ctx huma.Context) []error {
	u.body = ctx.BodyReader()
	return nil
}

// importUploadInput is a project archive upload, or with Check its check.
type importUploadInput struct {
	Name          string `query:"name" pattern:"^([a-z](-?[a-z0-9]){0,39})?$" maxLength:"40" doc:"The name it will be imported under: refused before the upload if it is not free here"`
	Check         bool   `query:"check" doc:"Store nothing: check the name and, if sent, the archive's start"`
	ContentLength int64  `header:"Content-Length" doc:"Archive size in bytes"`
	body          io.Reader
}

func (u *importUploadInput) Resolve(ctx huma.Context) []error {
	u.body = ctx.BodyReader()
	return nil
}

// hasBytes reports whether r has anything to read.
func hasBytes(r *bufio.Reader) bool {
	_, err := r.Peek(1)
	return err == nil
}

// RegisterAPI adds the export and import operations.
func (m *Module) RegisterAPI(a huma.API, p *platform.Platform) {
	const tag = "box"

	// ---- exports ----

	ec := api.Op("box-export-create", http.MethodPost, "/v1/box/exports", "box exports create", api.RiskWrite,
		"Export the whole box",
		"Starts an export of everything needed to recreate this box elsewhere: platform state (projects, changes, tokens, people, passkeys, "+
			"settings, deploy records and secrets, still encrypted), every Postgres database (pg_dump), Valkey, buckets and objects, email, "+
			"analytics, the live deploys' app images and static sites, git repositories and the HTTPS certificate authority. "+
			"The archive exposes all of the box's data, so this needs the owner's or an admin's token. "+
			"stream (default): GET the download path to run it; the archive is made as it downloads and nothing is kept on the box. "+
			"store: the archive is written to the box first (needs free disk) and can be downloaded until deleted. "+
			"includeKey puts the box key (which decrypts every secret) inside; without it, import needs that key separately. "+
			"App writes pause for a moment (usually under a second) while a consistent snapshot is taken.", tag)
	ec.Extensions[ExtExposesAllData] = true
	ec.Errors = append(ec.Errors, 409)
	huma.Register(a, api.Idempotent(ec), api.Wrap(func(ctx context.Context, in *struct {
		Body struct {
			IncludeKey  bool `json:"includeKey,omitempty" doc:"Put the box key that decrypts every secret into the archive"`
			WithHistory bool `json:"withHistory,omitempty" doc:"Also export logs, metrics, build logs and database snapshots"`
			Store       bool `json:"store,omitempty" doc:"Write the archive on the box first instead of streaming it on download"`
		}
	}) (*struct{ Body *Export }, error) {
		pr, err := requireAdmin(ctx, "an export")
		if err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		mode := "stream"
		if in.Body.Store {
			mode = "stored"
		}
		rec := &Export{ID: ids.New("ex"), Status: ExportPending, Mode: mode, IncludeKey: in.Body.IncludeKey, WithHistory: in.Body.WithHistory,
			CreatedBy: pr.TokenID, CreatedAt: time.Now().UTC(), Key: keyInfo(p, in.Body.IncludeKey)}
		rec.Download = "/v1/box/exports/" + rec.ID + "/download"
		rec.FileName = "tiffin-" + p.Domain + "-" + rec.CreatedAt.Format("20060102-150405") + boxfile.FileExt
		if in.Body.Store {
			rec.Phase = "starting"
		} else {
			rec.Phase = "waiting for the download to start it"
		}
		if err := m.save(p, "exports", rec.ID, rec); err != nil {
			return nil, err
		}
		_ = p.DB.Audit(ctx, pr.TokenID, "box.export", rec.ID, map[string]any{"session": pr.Session, "mode": mode, "includeKey": rec.IncludeKey, "withHistory": rec.WithHistory})
		if in.Body.Store {
			m.storeExport(p, rec)
		}
		return &struct{ Body *Export }{rec}, nil
	}))

	el := api.Op("box-exports-list", http.MethodGet, "/v1/box/exports", "box exports list", api.RiskRead,
		"List box exports", "Exports of this box, newest first, with progress and results.", tag)
	huma.Register(a, el, api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body []*Export }, error) {
		if _, err := requireAdmin(ctx, "listing exports"); err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		l, err := list[Export](p, "exports")
		return &struct{ Body []*Export }{l}, err
	}))

	eg := api.Op("box-export-get", http.MethodGet, "/v1/box/exports/{id}", "box exports get", api.RiskRead,
		"Show a box export", "One export: status, phase and progress while it runs; size, SHA-256, what it holds and where the key is when done.", tag)
	eg.Errors = append(eg.Errors, 404)
	huma.Register(a, eg, api.Wrap(func(ctx context.Context, in *struct {
		ID string `path:"id" pattern:"^ex_[0-9A-Z]{26}$" doc:"Export ID"`
	}) (*struct{ Body *Export }, error) {
		if _, err := requireAdmin(ctx, "an export"); err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		e, err := load[Export](p, "exports", in.ID)
		if err != nil {
			return nil, notFound("export", in.ID)
		}
		return &struct{ Body *Export }{e}, nil
	}))

	ed := api.Op("box-export-download", http.MethodGet, "/v1/box/exports/{id}/download", "-", api.RiskRead,
		"Download a box export",
		"The archive (application/octet-stream). For a stream export this runs the export and streams it as it is made (once; "+
			"the export's record shows progress and, at the end, the size and SHA-256 to compare). For a stored export it serves the file "+
			"(Range requests resume a broken download). Owner or admin only.", tag)
	ed.Extensions[ExtExposesAllData] = true
	ed.Errors = append(ed.Errors, 404, 409)
	ed.Responses = map[string]*huma.Response{"200": {Description: "The archive", Content: binary("A .tiffin archive (zstd-compressed tar)")}}
	huma.Register(a, ed, api.Wrap(func(ctx context.Context, in *struct {
		ID string `path:"id" pattern:"^ex_[0-9A-Z]{26}$" doc:"Export ID"`
	}) (*huma.StreamResponse, error) {
		pr, err := requireAdmin(ctx, "an export")
		if err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		rec, err := load[Export](p, "exports", in.ID)
		if err != nil {
			return nil, notFound("export", in.ID)
		}
		switch {
		case rec.Mode == "stored" && rec.Status == ExportDone:
			path := archivePath(p, "exports", rec.ID)
			if _, err := os.Stat(path); err != nil {
				return nil, api.NewProblem(410, "not_found", "export "+rec.ID+"'s archive was deleted")
			}
			return &huma.StreamResponse{Body: func(hctx huma.Context) {
				r, w := humago.Unwrap(hctx)
				f, err := os.Open(path)
				if err != nil {
					http.Error(w, err.Error(), http.StatusGone)
					return
				}
				defer f.Close()
				fi, _ := f.Stat()
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Disposition", `attachment; filename="`+rec.FileName+`"`)
				w.Header().Set("X-Tiffin-SHA256", rec.SHA256)
				http.ServeContent(w, r, rec.FileName, fi.ModTime(), f)
			}}, nil
		case rec.Mode == "stored":
			p := api.NewProblem(409, "precondition", "export "+rec.ID+" is "+rec.Status+"; a stored export can be downloaded once it is done")
			p.Hint = "poll `tiffin box exports get " + rec.ID + "` until status is done"
			return nil, p
		case rec.Status != ExportPending:
			p := api.NewProblem(409, "precondition", "export "+rec.ID+" is "+rec.Status+"; a stream export downloads exactly once")
			p.Hint = "start a new export with `tiffin box export`"
			return nil, p
		}
		_ = p.DB.Audit(ctx, pr.TokenID, "box.export.download", rec.ID, map[string]any{"session": pr.Session})
		return &huma.StreamResponse{Body: func(hctx huma.Context) {
			_, w := humago.Unwrap(hctx)
			ctx, cancel := context.WithCancel(hctx.Context())
			defer cancel()
			m.setCancel(rec.ID, cancel)
			defer m.setCancel(rec.ID, nil)
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", `attachment; filename="`+rec.FileName+`"`)
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			fw := &flushWriter{w: w}
			if err := m.runExport(ctx, p, rec, fw); err != nil {
				m.failExport(p, rec, err)
				// Cut the connection so the client sees an incomplete download.
				panic(http.ErrAbortHandler)
			}
			fw.flush()
		}}, nil
	}))

	edel := api.Op("box-export-delete", http.MethodDelete, "/v1/box/exports/{id}", "box exports delete", api.RiskWrite,
		"Delete a box export", "Cancels a running export, or deletes a stored export's archive from the box. The record stays (status failed or expired).", tag)
	edel.Errors = append(edel.Errors, 404)
	huma.Register(a, edel, api.Wrap(func(ctx context.Context, in *struct {
		ID string `path:"id" pattern:"^ex_[0-9A-Z]{26}$" doc:"Export ID"`
	}) (*struct{ Body *Export }, error) {
		pr, err := requireAdmin(ctx, "an export")
		if err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		rec, err := load[Export](p, "exports", in.ID)
		if err != nil {
			return nil, notFound("export", in.ID)
		}
		if m.cancel(rec.ID) {
			for i := 0; i < 50; i++ { // let the run record its end
				time.Sleep(100 * time.Millisecond)
				if r, err := load[Export](p, "exports", in.ID); err == nil && r.Status != ExportRunning {
					rec = r
					break
				}
			}
		}
		_ = os.Remove(archivePath(p, "exports", rec.ID))
		if rec.Status == ExportPending || rec.Status == ExportDone {
			rec.Status, rec.Phase = ExportExpired, ""
			rec.Hint = "Deleted."
			_ = m.save(p, "exports", rec.ID, rec)
		}
		_ = p.DB.Audit(ctx, pr.TokenID, "box.export.delete", rec.ID, map[string]any{"session": pr.Session})
		return &struct{ Body *Export }{rec}, nil
	}))

	// ---- imports ----

	iu := api.Op("box-import-upload", http.MethodPost, "/v1/box/imports", "-", api.RiskWrite,
		"Upload a box export to import",
		"Send a .tiffin archive as the raw request body (application/octet-stream). It is stored on the box and verified as it arrives "+
			"(manifest, every entry against the trailer's digest, archive version). Nothing on the box changes until you apply it: "+
			"POST /v1/box/imports/{id}/apply. Owner or admin only.", tag)
	iu.Extensions[ExtExposesAllData] = true
	iu.Errors = append(iu.Errors, 409, 413, 507)
	iu.RequestBody = &huma.RequestBody{Required: true, Description: "The archive", Content: binary("A .tiffin archive made by tiffin box export")}
	huma.Register(a, api.Idempotent(iu), api.Wrap(func(ctx context.Context, in *uploadInput) (*struct{ Body *Import }, error) {
		pr, err := requireAdmin(ctx, "an import")
		if err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		if in.body == nil {
			return nil, api.NewProblem(400, "validation", "send the archive as the request body")
		}
		rec, err := m.receive(ctx, p, in.body, in.ContentLength, pr.TokenID, p.Version)
		switch {
		case errors.Is(err, errNoSpace):
			return nil, api.NewProblem(507, "precondition", err.Error())
		case errors.Is(err, ErrInvalid):
			prob := api.NewProblem(422, "validation", err.Error())
			prob.Hint = "make the archive again with `tiffin box export`, or update this box with `tiffin up` if it came from a newer Tiffin"
			return nil, prob
		case err != nil:
			return nil, err
		}
		_ = p.DB.Audit(ctx, pr.TokenID, "box.import.upload", rec.ID, map[string]any{"session": pr.Session, "bytes": rec.SizeBytes, "sha256": rec.SHA256})
		return &struct{ Body *Import }{rec}, nil
	}))

	il := api.Op("box-imports-list", http.MethodGet, "/v1/box/imports", "box imports list", api.RiskRead,
		"List box imports", "Uploaded archives and their restores, newest first.", tag)
	huma.Register(a, il, api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body []*Import }, error) {
		if _, err := requireAdmin(ctx, "listing imports"); err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		l, err := list[Import](p, "imports")
		return &struct{ Body []*Import }{l}, err
	}))

	ig := api.Op("box-import-get", http.MethodGet, "/v1/box/imports/{id}", "box imports get", api.RiskRead,
		"Show a box import", "One import: what the archive holds, then the restore's status, phase and progress, and at the end whether the box is healthy.", tag)
	ig.Errors = append(ig.Errors, 404)
	huma.Register(a, ig, api.Wrap(func(ctx context.Context, in *struct {
		ID string `path:"id" pattern:"^im_[0-9A-Z]{26}$" doc:"Import ID"`
	}) (*struct{ Body *Import }, error) {
		if _, err := requireAdmin(ctx, "an import"); err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		im, err := load[Import](p, "imports", in.ID)
		if err != nil {
			return nil, notFound("import", in.ID)
		}
		return &struct{ Body *Import }{im}, nil
	}))

	ia := api.Op("box-import-apply", http.MethodPost, "/v1/box/imports/{id}/apply", "box imports apply", api.RiskDestructive,
		"Restore an uploaded box export onto this box",
		"Replaces this box's projects, data, settings, tokens and people with the archive's, loads the apps' images and files, "+
			"restarts the box's service once and waits until every project has converged. Irreversible. "+
			"A box that already has projects is refused unless replace is true (a full backup of it is taken first). "+
			"If the archive does not carry the box key, pass it as secretsKey. Without confirm nothing changes: you get status 428 with "+
			"what would be replaced and the confirm value. Box owner only. Poll box imports get until status is done.", tag)
	ia.Extensions[api.ExtConfirm] = true
	ia.Extensions[ExtExposesAllData] = true
	ia.Errors = append(ia.Errors, 404, 409, 428)
	ia.DefaultStatus = http.StatusAccepted
	huma.Register(a, ia, api.Wrap(func(ctx context.Context, in *struct {
		ID   string `path:"id" pattern:"^im_[0-9A-Z]{26}$" doc:"Import ID"`
		Body struct {
			Replace    bool   `json:"replace,omitempty" doc:"Replace a box that already has projects (a full backup of it is taken first)"`
			SecretsKey string `json:"secretsKey,omitempty" maxLength:"200" doc:"The source box's key (AGE-SECRET-KEY-1...), when the archive does not include it"`
			Confirm    string `json:"confirm,omitempty" doc:"The confirm value from the preview (status 428)"`
		}
	}) (*struct{ Body *Import }, error) {
		pr := api.PrincipalFrom(ctx)
		if pr == nil || !pr.BoxAdmin() || (pr.Role != "" && pr.Role != tokens.RoleOwner) {
			return nil, fmt.Errorf("%w: importing replaces the whole box; it needs the box owner's token or a key with full access to all projects", tokens.ErrForbidden)
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		rec, err := load[Import](p, "imports", in.ID)
		if err != nil {
			return nil, notFound("import", in.ID)
		}
		if rec.Status != ImportUploaded && !(rec.Status == ImportFailed && !rec.Touched) {
			prob := api.NewProblem(409, "precondition", "import "+rec.ID+" is "+rec.Status)
			prob.Hint = "upload the archive again to start over"
			return nil, prob
		}
		if _, err := os.Stat(archivePath(p, "imports", rec.ID)); err != nil {
			return nil, api.NewProblem(409, "precondition", "the archive of import "+rec.ID+" is gone; upload it again")
		}
		existing, err := p.DB.ListProjects(ctx)
		if err != nil {
			return nil, err
		}
		if len(existing) > 0 && !in.Body.Replace {
			prob := api.NewProblem(409, "precondition", fmt.Sprintf("this box already has %d project(s) (%v); importing would replace them", len(existing), existing))
			prob.Hint = "repeat with replace=true to replace everything on this box (tiffin box import <file> --replace --confirm <box name>)"
			return nil, prob
		}
		if err := checkKey(rec.Source, in.Body.SecretsKey); err != nil {
			prob := api.NewProblem(422, "validation", err.Error())
			prob.Hint = "pass secretsKey (tiffin box import <file> --key-file <file>)"
			return nil, prob
		}
		pv, err := preview(ctx, p, rec, in.Body.Replace)
		if err != nil {
			return nil, err
		}
		if err := datakit.RequireConfirm(in.Body.Confirm, pv.Key(), pv); err != nil {
			return nil, err
		}
		if all, err := list[Import](p, "imports"); err == nil {
			for _, o := range all {
				switch o.Status {
				case ImportApplying, ImportRestarting, ImportConverging:
					return nil, busyProblem(fmt.Errorf("import %s is %s", o.ID, o.Status))
				}
			}
		}
		rec.AppliedBy = pr.TokenID
		_ = p.DB.Audit(ctx, pr.TokenID, "box.import.apply", rec.ID, map[string]any{"session": pr.Session, "replace": in.Body.Replace,
			"sha256": rec.SHA256, "projects": rec.Source.Projects, "replaced": existing})
		rec.Status, rec.Phase, rec.Error, rec.Hint = ImportApplying, "starting", "", ""
		if err := m.save(p, "imports", rec.ID, rec); err != nil {
			return nil, err
		}
		m.startImport(p, rec, applyOptions{replace: in.Body.Replace, key: in.Body.SecretsKey})
		cp := *rec
		return &struct{ Body *Import }{&cp}, nil
	}))

	idel := api.Op("box-import-delete", http.MethodDelete, "/v1/box/imports/{id}", "box imports delete", api.RiskWrite,
		"Discard an uploaded box export", "Deletes an uploaded archive that has not been applied (or whose apply failed).", tag)
	idel.Errors = append(idel.Errors, 404, 409)
	huma.Register(a, idel, api.Wrap(func(ctx context.Context, in *struct {
		ID string `path:"id" pattern:"^im_[0-9A-Z]{26}$" doc:"Import ID"`
	}) (*struct{ Body *Import }, error) {
		pr, err := requireAdmin(ctx, "an import")
		if err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		rec, err := load[Import](p, "imports", in.ID)
		if err != nil {
			return nil, notFound("import", in.ID)
		}
		switch rec.Status {
		case ImportApplying, ImportRestarting, ImportConverging:
			return nil, api.NewProblem(409, "precondition", "import "+rec.ID+" is "+rec.Status+"; it cannot be discarded now")
		}
		_ = os.Remove(archivePath(p, "imports", rec.ID))
		_ = os.Remove(recordPath(p, "imports", rec.ID))
		_ = p.DB.Audit(ctx, pr.TokenID, "box.import.delete", rec.ID, map[string]any{"session": pr.Session})
		return &struct{ Body *Import }{rec}, nil
	}))

	m.registerProjects(a, p)
}

// flushWriter flushes the response every few MiB so the download moves.
type flushWriter struct {
	w http.ResponseWriter
	n int
}

func (f *flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	f.n += n
	if f.n >= 4<<20 {
		f.flush()
	}
	return n, err
}

func (f *flushWriter) flush() {
	f.n = 0
	if fl, ok := f.w.(http.Flusher); ok {
		fl.Flush()
	}
}
