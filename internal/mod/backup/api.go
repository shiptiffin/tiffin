package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/btahir/tiffin/internal/api"
	"github.com/btahir/tiffin/internal/mod/datakit"
	"github.com/btahir/tiffin/internal/platform"
	"github.com/btahir/tiffin/internal/tokens"
	"github.com/danielgtaylor/huma/v2"
)

// Overview is the backup list with the schedule and repository size.
type BackupOverview struct {
	Backups      []Backup       `json:"backups" doc:"Newest first"`
	Schedule     BackupSchedule `json:"schedule"`
	LastOKAt     *time.Time     `json:"lastOkAt" doc:"When the newest successful backup started"`
	RepoBytes    int64          `json:"repoBytes" doc:"Disk used by the local backup repository and sets"`
	Destinations []string       `json:"destinations" doc:"Where backups are stored"`
	LastDrill    *BackupDrill   `json:"lastDrill" doc:"The newest restore drill (null when none ran); see GET /v1/backups/drills"`
}

func onBox(p *platform.Platform) error {
	if p == nil || p.DB == nil {
		return api.NewProblem(501, "internal", datakit.ErrOffBox.Error())
	}
	return nil
}

func busy(err error) error {
	if errors.Is(err, ErrBusy) {
		p := api.NewProblem(409, "conflict", err.Error())
		p.Hint = "check `tiffin backups list` and try again in a minute"
		return p
	}
	return err
}

// RegisterAPI adds the backup operations.
func (*Module) RegisterAPI(a huma.API, p *platform.Platform) {
	const tag = "backups"

	bl := api.Op("backups-list", http.MethodGet, "/v1/backups", "backups list", api.RiskRead,
		"List backups", "Backup sets (Postgres via pgBackRest, Valkey snapshot, platform state), newest first, with the schedule.", tag)
	huma.Register(a, bl, api.Wrap(func(ctx context.Context, _ *struct{}) (*struct{ Body *BackupOverview }, error) {
		if err := api.PrincipalFrom(ctx).Require(tokens.ScopeRead, ""); err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		list, err := List(ctx, p)
		if err != nil {
			return nil, err
		}
		out := &BackupOverview{Backups: list, Schedule: getSchedule(ctx, p), RepoBytes: datakit.DirSize(Root), Destinations: []string{"local: " + Root}}
		if last := lastOK(list, ""); last != nil {
			t := last.StartedAt
			out.LastOKAt = &t
		}
		if drills, err := ListDrills(ctx, p); err == nil && len(drills) > 0 {
			out.LastDrill = &drills[0]
			if live := runningDrill(drills[0].ID); live != nil {
				out.LastDrill = live
			}
		}
		return &struct{ Body *BackupOverview }{out}, nil
	}))

	bc := api.Op("backup-create", http.MethodPost, "/v1/backups", "backup", api.RiskWrite,
		"Back up the box now",
		"Takes a backup set now and waits for it: Postgres (pgBackRest; incremental unless you ask for full or none exists yet), "+
			"a Valkey snapshot and the platform state. Needs apply:reversible on all projects.", tag)
	bc.Errors = append(bc.Errors, 409)
	huma.Register(a, bc, api.Wrap(func(ctx context.Context, in *struct {
		Body struct {
			Kind string `json:"kind,omitempty" enum:"full,incremental" doc:"Default incremental"`
		}
	}) (*struct{ Body *Backup }, error) {
		pr := api.PrincipalFrom(ctx)
		if err := pr.Require(tokens.ScopeApplyReversible, ""); err != nil {
			return nil, err
		}
		if !pr.CanProject("*") {
			return nil, fmt.Errorf("%w: backups cover every project; this needs a token for all projects", tokens.ErrForbidden)
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		kind := in.Body.Kind
		if kind == "" {
			kind = "incremental"
		}
		b, err := Take(ctx, p, kind, "manual")
		if err != nil {
			if b != nil {
				return nil, api.NewProblem(500, "internal", "backup "+b.ID+" failed: "+err.Error())
			}
			return nil, busy(err)
		}
		_ = p.DB.Audit(ctx, pr.TokenID, "backup.create", b.ID, map[string]any{"session": pr.Session, "kind": b.Kind})
		return &struct{ Body *Backup }{b}, nil
	}))

	rs := api.Op("backup-restore", http.MethodPost, "/v1/backups/{id}/restore", "restore", api.RiskDestructive,
		"Restore a backup",
		"Puts a backup back: by default the whole Postgres cluster and all Valkey data (targets: postgres, valkey, files). "+
			"Everything changed since the backup is lost, so a safety backup of the current state is taken first. "+
			"Without confirm nothing changes: you get status 428 with what would be overwritten and the confirm value. Box owner only.", tag)
	rs.Errors = append(rs.Errors, 404, 409, 428)
	rs.Extensions[api.ExtConfirm] = true
	huma.Register(a, rs, api.Wrap(func(ctx context.Context, in *struct {
		ID   string `path:"id" pattern:"^bk_[0-9A-Z]{26}$" doc:"Backup ID"`
		Body struct {
			Targets []string `json:"targets,omitempty" doc:"What to restore: postgres, valkey, files (default postgres and valkey)"`
			Confirm string   `json:"confirm,omitempty" doc:"The confirm value from the preview (status 428)"`
		}
	}) (*struct{ Body *BackupRestored }, error) {
		pr := api.PrincipalFrom(ctx)
		if !pr.BoxAdmin() {
			return nil, fmt.Errorf("%w: restoring a backup overwrites every project; it needs the box owner's token", tokens.ErrForbidden)
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		b, err := get(ctx, p, in.ID)
		if errors.Is(err, os.ErrNotExist) {
			return nil, api.NewProblem(404, "not_found", "no backup "+in.ID)
		}
		if err != nil {
			return nil, err
		}
		if b.Status != "ok" {
			return nil, api.NewProblem(409, "precondition", "backup "+b.ID+" did not succeed ("+b.Status+"); pick another from `tiffin backups list`")
		}
		targets, err := normalizeTargets(in.Body.Targets)
		if err != nil {
			return nil, api.NewProblem(422, "validation", err.Error())
		}
		preview, err := Preview(ctx, p, b, targets)
		if err != nil {
			return nil, err
		}
		if err := datakit.RequireConfirm(in.Body.Confirm, preview.Key(), preview); err != nil {
			return nil, err
		}
		_ = p.DB.Audit(ctx, pr.TokenID, "backup.restore", b.ID, map[string]any{"session": pr.Session, "targets": targets})
		out, err := Restore(ctx, p, b, targets)
		if err != nil {
			return nil, busy(err)
		}
		return &struct{ Body *BackupRestored }{out}, nil
	}))

	ss := api.Op("backups-schedule-set", http.MethodPut, "/v1/backups/schedule", "backups schedule", api.RiskWrite,
		"Change the backup schedule", "How often full and incremental backups run, how many full backups are kept, and whether and how often "+
			"restore drills run. Only the fields you send change. Box owner only.", tag)
	huma.Register(a, ss, api.Wrap(func(ctx context.Context, in *struct {
		Body struct {
			Enabled               *bool `json:"enabled,omitempty" doc:"Take backups automatically"`
			FullEveryHours        *int  `json:"fullEveryHours,omitempty" minimum:"1" maximum:"720" doc:"Hours between full backups (default 24)"`
			IncrementalEveryHours *int  `json:"incrementalEveryHours,omitempty" minimum:"0" maximum:"168" doc:"Hours between incremental backups; 0 turns them off (default 1)"`
			RetainFull            *int  `json:"retainFull,omitempty" minimum:"1" maximum:"60" doc:"Full backups to keep, with their incrementals (default 7)"`
		}
	}) (*struct{ Body BackupSchedule }, error) {
		pr := api.PrincipalFrom(ctx)
		if !pr.BoxAdmin() {
			return nil, fmt.Errorf("%w: the backup schedule needs the box owner's token", tokens.ErrForbidden)
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		s := getSchedule(ctx, p)
		if v := in.Body.Enabled; v != nil {
			s.Enabled = *v
		}
		if v := in.Body.FullEveryHours; v != nil {
			s.FullEveryHours = *v
		}
		if v := in.Body.IncrementalEveryHours; v != nil {
			s.IncrementalEveryHours = *v
		}
		if v := in.Body.RetainFull; v != nil {
			s.RetainFull = *v
		}
		raw, _ := json.Marshal(s)
		if err := p.DB.KVPut(ctx, nsMeta, "schedule", raw); err != nil {
			return nil, err
		}
		_ = p.DB.Audit(ctx, pr.TokenID, "backup.schedule", "", s)
		return &struct{ Body BackupSchedule }{s}, nil
	}))

	registerDrills(a, p, tag)
}
