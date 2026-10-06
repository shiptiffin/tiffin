// Package portable makes a box portable: `tiffin box export` writes one
// archive (.tiffin, a zstd-compressed tar with a manifest and a verified
// trailer) holding everything needed to recreate the box elsewhere, and
// `tiffin box import` puts it onto a fresh (or, with replace, an existing)
// box and brings every app back up from the exported artifacts.
//
// What an archive holds (see export.go for the order):
//
//	platform/state.db       the platform state (projects, changes, tokens,
//	                        people, passkeys, settings, deploy records, and
//	                        every secret, still age-encrypted)
//	platform/secrets.key    the box key, only with --include-key
//	files/edge/...          the HTTPS CA and certificates
//	files/<set>/...         storage (buckets, objects, S3 accounts), email,
//	                        analytics, observe settings, static sites, git,
//	                        apps' disk folders
//	runtime/images.tar@N    `nerdctl save` of every live deploy's image
//	valkey/dump.rdb         a BGSAVE snapshot
//	postgres/...            roles, then one plain pg_dump per database (auth
//	                        and queue state live there too) and pg_cron jobs
//	history/...             logs, metrics and build logs (--with-history)
//
// Postgres goes in logically (pg_dump, plain SQL) rather than as a
// pgBackRest copy: plain SQL restores into any later Postgres and does not
// carry the source cluster's identity, WAL or repository with it.
//
// Consistency: app containers and the object store are paused for a moment
// (typically well under a second) while Postgres snapshots are exported
// (pg_export_snapshot, which every pg_dump then reads), Valkey forks its
// snapshot and the file trees are copied with reflinks. Everything after
// that streams from those snapshots while the box keeps serving.
//
// HTTP API (the dashboard calls these; see api.go):
//
//	POST   /v1/box/exports                 start an export → Export
//	GET    /v1/box/exports[/{id}]          list / progress
//	GET    /v1/box/exports/{id}/download   the archive (streams the export)
//	DELETE /v1/box/exports/{id}            cancel or delete
//	POST   /v1/box/imports                 upload an archive (raw body) → Import
//	GET    /v1/box/imports[/{id}]          list / progress
//	POST   /v1/box/imports/{id}/apply      restore it (confirm flow, owner only)
//	DELETE /v1/box/imports/{id}            discard an upload
package portable

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/btahir/tiffin/internal/boxfile"
	"github.com/btahir/tiffin/internal/platform"
)

func init() { platform.Register(&Module{}) }

// Module implements the portable module.
type Module struct {
	mu      sync.Mutex
	ctx     context.Context // box lifetime, set by Start
	p       *platform.Platform
	cancels map[string]context.CancelFunc // running exports and imports
	saveMu  sync.Mutex
	projMu  sync.Mutex // one project duplicate or import at a time
	be      backend    // tests' services; nil: the box's
}

func (*Module) Name() string { return "portable" }

// Order: after every data module, so they are up when Start resumes an import.
func (*Module) Order() int { return 70 }

// dir is the module's directory on the data disk.
func dir(p *platform.Platform) string {
	root := "/var/lib/tiffin"
	if p != nil && p.DataRoot != "" {
		root = p.DataRoot
	}
	return filepath.Join(root, "portable")
}

// Export statuses.
const (
	ExportPending = "pending" // waiting for its download to start it (stream mode)
	ExportRunning = "running"
	ExportDone    = "done"
	ExportFailed  = "failed"
	ExportExpired = "expired"
)

// Export is one export job.
type Export struct {
	ID          string     `json:"id" doc:"Export ID (ex_...)"`
	Status      string     `json:"status" enum:"pending,running,done,failed,expired" doc:"pending (stream mode: the download starts it) → running → done or failed"`
	Mode        string     `json:"mode" enum:"stream,stored" doc:"stream: the archive is made while it downloads, nothing is kept on the box; stored: it is written to the box first and can be downloaded (and resumed) until deleted"`
	Phase       string     `json:"phase,omitempty" doc:"What it is doing now"`
	IncludeKey  bool       `json:"includeKey"`
	WithHistory bool       `json:"withHistory"`
	CreatedBy   string     `json:"createdBy"`
	CreatedAt   time.Time  `json:"createdAt"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
	// Progress.
	EstimatedBytes int64 `json:"estimatedBytes" doc:"Estimated uncompressed size (images not counted until saved)"`
	ContentBytes   int64 `json:"contentBytes" doc:"Uncompressed bytes written so far"`
	WrittenBytes   int64 `json:"writtenBytes" doc:"Archive (compressed) bytes written so far"`
	Percent        int   `json:"percent"`
	// Result.
	SizeBytes      int64                    `json:"sizeBytes,omitempty" doc:"Archive size"`
	SHA256         string                   `json:"sha256,omitempty" doc:"SHA-256 of the archive file"`
	DurationMs     int64                    `json:"durationMs,omitempty"`
	WritesPausedMs int64                    `json:"writesPausedMs" doc:"How long app writes were paused for the snapshot"`
	Parts          map[string]boxfile.Stats `json:"parts,omitempty"`
	Projects       []string                 `json:"projects,omitempty"`
	Key            KeyInfo                  `json:"key"`
	Download       string                   `json:"download" doc:"GET this path for the archive"`
	FileName       string                   `json:"fileName" doc:"Suggested file name"`
	Error          string                   `json:"error,omitempty"`
	Hint           string                   `json:"hint,omitempty"`
}

// KeyInfo says where the key that decrypts the archive's secrets is.
type KeyInfo struct {
	Included  bool   `json:"included" doc:"The box key is inside the archive"`
	Recipient string `json:"recipient,omitempty" doc:"age public key the secrets are encrypted to"`
	Location  string `json:"location" doc:"Where the key is on this box"`
	Note      string `json:"note"`
}

// Import statuses.
const (
	ImportUploaded   = "uploaded"   // verified, waiting for apply
	ImportApplying   = "applying"   // restoring data
	ImportRestarting = "restarting" // the service restarts to swap in the state
	ImportConverging = "converging" // projects reconcile, apps start
	ImportDone       = "done"
	ImportFailed     = "failed"
)

// Import is one uploaded archive and its restore job.
type Import struct {
	ID         string                   `json:"id" doc:"Import ID (im_...)"`
	Status     string                   `json:"status" enum:"uploaded,applying,restarting,converging,done,failed"`
	Phase      string                   `json:"phase,omitempty" doc:"What it is doing now"`
	UploadedBy string                   `json:"uploadedBy"`
	UploadedAt time.Time                `json:"uploadedAt"`
	SizeBytes  int64                    `json:"sizeBytes"`
	SHA256     string                   `json:"sha256"`
	Source     Summary                  `json:"source"`
	Parts      map[string]boxfile.Stats `json:"parts,omitempty"`
	// Apply.
	Replace      bool       `json:"replace"`
	AppliedBy    string     `json:"appliedBy,omitempty"`
	StartedAt    *time.Time `json:"startedAt,omitempty"`
	RestartedAt  *time.Time `json:"restartedAt,omitempty"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
	DurationMs   int64      `json:"durationMs,omitempty"`
	Percent      int        `json:"percent"`
	SafetyBackup string     `json:"safetyBackup,omitempty" doc:"Backup of this box taken before replacing it"`
	// Touched: data on this box has been replaced (a failure after this
	// point leaves a mix; the safety backup puts the box back).
	Touched bool    `json:"touched"`
	Healthy bool    `json:"healthy" doc:"Every project converged and every status check passes"`
	Failing []Check `json:"failing,omitempty" doc:"Checks or resources not green when the import finished"`
	Error   string  `json:"error,omitempty"`
	Hint    string  `json:"hint,omitempty"`
}

// Check is one failing check or resource.
type Check struct {
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
}

// Summary is the part of an archive's manifest people need to decide.
type Summary struct {
	TiffinVersion  string    `json:"tiffinVersion"`
	CreatedAt      time.Time `json:"createdAt"`
	Domain         string    `json:"domain"`
	Hostname       string    `json:"hostname"`
	Projects       []string  `json:"projects"`
	Databases      []string  `json:"databases"`
	Images         int       `json:"images"`
	IncludesKey    bool      `json:"includesKey"`
	Recipient      string    `json:"recipient"`
	WithHistory    bool      `json:"withHistory"`
	WritesPausedMs int64     `json:"writesPausedMs"`
}

func summarize(m *boxfile.Manifest) Summary {
	return Summary{TiffinVersion: m.TiffinVersion, CreatedAt: m.CreatedAt, Domain: m.Source.Domain, Hostname: m.Source.Hostname,
		Projects: m.Projects, Databases: m.Databases, Images: len(m.Images), IncludesKey: m.IncludesKey,
		Recipient: m.Recipient, WithHistory: m.WithHistory, WritesPausedMs: m.Consistency.WritesPausedMs}
}

// ---- job records: JSON files on the data disk (not the state database,
// which an import replaces underneath them) ----

func recordPath(p *platform.Platform, kind, id string) string {
	return filepath.Join(dir(p), kind, id+".json")
}

func archivePath(p *platform.Platform, kind, id string) string {
	return filepath.Join(dir(p), kind, id+boxfile.FileExt)
}

func (m *Module) save(p *platform.Platform, kind, id string, v any) error {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	path := recordPath(p, kind, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func load[T any](p *platform.Platform, kind, id string) (*T, error) {
	b, err := os.ReadFile(recordPath(p, kind, filepath.Base(id)))
	if err != nil {
		return nil, err
	}
	var v T
	return &v, json.Unmarshal(b, &v)
}

func list[T any](p *platform.Platform, kind string) ([]*T, error) {
	ents, err := os.ReadDir(filepath.Join(dir(p), kind))
	if errors.Is(err, os.ErrNotExist) {
		return []*T{}, nil
	} else if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range ents {
		if id, ok := strings.CutSuffix(e.Name(), ".json"); ok {
			ids = append(ids, id)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	out := make([]*T, 0, len(ids))
	for _, id := range ids {
		if v, err := load[T](p, kind, id); err == nil {
			out = append(out, v)
		}
	}
	return out, nil
}

func (m *Module) setCancel(id string, c context.CancelFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancels == nil {
		m.cancels = map[string]context.CancelFunc{}
	}
	if c == nil {
		delete(m.cancels, id)
		return
	}
	m.cancels[id] = c
}

func (m *Module) cancel(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.cancels[id]; ok {
		c()
		return true
	}
	return false
}

func (m *Module) boxCtx() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx == nil {
		return context.Background()
	}
	return m.ctx
}

// Start resumes an import the service restarted for, fails jobs a restart
// interrupted, and removes expired exports.
func (m *Module) Start(ctx context.Context, p *platform.Platform) error {
	m.mu.Lock()
	m.ctx, m.p = ctx, p
	m.mu.Unlock()
	if exps, err := list[Export](p, "exports"); err == nil {
		for _, e := range exps {
			switch {
			case e.Status == ExportRunning:
				e.Status, e.Error, e.Phase = ExportFailed, "interrupted: the box restarted during the export", ""
				e.Hint = "Start a new export."
				_ = m.save(p, "exports", e.ID, e)
				_ = os.Remove(archivePath(p, "exports", e.ID) + ".part")
			case e.Status == ExportPending && time.Since(e.CreatedAt) > pendingTTL:
				m.expire(p, e)
			}
		}
	}
	if ims, err := list[Import](p, "imports"); err == nil {
		for _, im := range ims {
			switch im.Status {
			case ImportApplying:
				im.Status, im.Error, im.Phase = ImportFailed, "interrupted: the box restarted during the restore", ""
				im.Hint = restoreHint(im)
				_ = m.save(p, "imports", im.ID, im)
				_ = os.RemoveAll(pendingDir(p))
			case ImportRestarting:
				go m.resume(ctx, p, im)
			case ImportConverging:
				go m.converge(ctx, p, im)
			}
		}
	}
	m.sweepProjects(p, true)
	go m.sweep(ctx, p)
	return nil
}

// sweepProjects fails project exports and jobs a restart interrupted (at
// start) and forgets uploads and exports nobody used.
func (m *Module) sweepProjects(p *platform.Platform, restarted bool) {
	if exps, err := list[ProjectExport](p, "project-exports"); err == nil {
		for _, e := range exps {
			switch {
			case restarted && e.Status == ExportRunning:
				e.Status, e.Phase, e.Error, e.Hint = ExportFailed, "", "interrupted: the box restarted during the export", "Start a new export."
				_ = m.save(p, "project-exports", e.ID, e)
			case e.Status == ExportPending && time.Since(e.CreatedAt) > pendingTTL:
				e.Status, e.Phase, e.Hint = ExportExpired, "", "Nobody downloaded it within an hour. Start a new export."
				_ = m.save(p, "project-exports", e.ID, e)
			}
		}
	}
	if jobs, err := list[ProjectJob](p, "project-jobs"); err == nil {
		for _, j := range jobs {
			switch {
			case restarted && j.Status == JobRunning:
				j.Status, j.Phase, j.FinishedAt, j.Error = JobFailed, "", now(), "interrupted: the box restarted during the "+j.Kind
				j.Hint = "Start it again."
				if j.Project != "" {
					j.Hint = "If project " + j.Project + " was made, look at it (tiffin projects get " + j.Project + ") or destroy it, then start again under another name."
				}
				_ = m.save(p, "project-jobs", j.ID, j)
			case (j.Status == JobUploaded || j.Status == JobFailed) && time.Since(j.CreatedAt) > uploadTTL:
				if os.Remove(archivePath(p, "project-imports", j.ID)) == nil && j.Status == JobUploaded {
					j.Status, j.Error, j.Hint = JobFailed, "expired: not imported within a day", "Upload the archive again."
					_ = m.save(p, "project-jobs", j.ID, j)
				}
			}
		}
	}
}

// uploadTTL is how long an uploaded project archive waits to be imported.
const uploadTTL = 24 * time.Hour

// pendingTTL is how long a stream-mode export waits for its download.
const pendingTTL = time.Hour

func (m *Module) expire(p *platform.Platform, e *Export) {
	e.Status, e.Phase = ExportExpired, ""
	e.Hint = "Nobody downloaded it within an hour. Start a new export."
	_ = m.save(p, "exports", e.ID, e)
}

// sweep expires stream-mode exports nobody downloaded.
func (m *Module) sweep(ctx context.Context, p *platform.Platform) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		exps, err := list[Export](p, "exports")
		if err != nil {
			continue
		}
		for _, e := range exps {
			if e.Status == ExportPending && time.Since(e.CreatedAt) > pendingTTL {
				m.expire(p, e)
			}
		}
		m.sweepProjects(p, false)
	}
}

func now() *time.Time {
	t := time.Now().UTC()
	return &t
}
