package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/shiptiffin/tiffin/internal/api"
	"github.com/shiptiffin/tiffin/internal/mod/datakit"
	"github.com/shiptiffin/tiffin/internal/platform"
	"github.com/shiptiffin/tiffin/internal/tokens"
)

// Overview is the backup list with the schedule and repository size.
type BackupOverview struct {
	Backups      []Backup          `json:"backups" doc:"Newest first"`
	Schedule     BackupSchedule    `json:"schedule"`
	LastOKAt     *time.Time        `json:"lastOkAt" doc:"When the newest successful backup started"`
	RepoBytes    int64             `json:"repoBytes" doc:"Disk used by the local backup repository and sets"`
	Destinations []string          `json:"destinations" doc:"Where backups are stored"`
	Offsite      *BackupOffsite    `json:"offsite" doc:"Copies off the box: on or off, the newest copy and its size (see GET /v1/backups/offsite)"`
	LastDrill    *BackupDrill      `json:"lastDrill" doc:"The newest restore drill (null when none ran); see GET /v1/backups/drills"`
	Restorable   *BackupRestorable `json:"restorable" doc:"The moments Postgres can be restored to (POST /v1/backups/latest/restore with time); null with no successful backup"`
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
		if err := api.PrincipalFrom(ctx).RequireBox(tokens.ScopeRead); err != nil {
			return nil, err
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		list, err := List(ctx, p)
		if err != nil {
			return nil, err
		}
		out := &BackupOverview{Backups: list, Schedule: getSchedule(ctx, p), RepoBytes: datakit.DirSize(Root), Destinations: []string{"local: " + Root},
			Offsite: offsiteView(ctx, p), Restorable: restorableRange(list, time.Now())}
		if c, _ := current(); c != nil {
			out.Destinations = append(out.Destinations, "off-box: "+c.where())
		}
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
			"a Valkey snapshot and the platform state. Needs full access to all projects.", tag)
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
		// Not tied to the call: a client that stops waiting must not kill pgBackRest mid-backup.
		b, err := Take(context.WithoutCancel(ctx), p, kind, "manual")
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
		"Puts a backup back: by default the whole Postgres cluster and all Valkey data (targets: postgres, valkey, files, platform, or all). "+
			"Everything changed since the backup is lost, so a safety backup of the current state is taken first. "+
			"id is a backup ID or latest. time (with id latest) restores Postgres to that moment instead: any time in the overview's restorable range "+
			"(the newest backup set before it, then the archived log replayed up to it); Valkey and files have no log, so they go back to that set, "+
			"the newest at or before the moment, not to the second. from=offsite restores the copy in the bucket (`tiffin backups offsite list`), which works on a new box "+
			"after `tiffin backups offsite set ... --passphrase`: on a box with no projects every target is restored by default (platform: "+
			"projects, settings, secrets, tokens, the box key; this box's owner token, domain and backup settings are kept), and no safety backup is taken. "+
			"Without confirm nothing changes: you get status 428 with what would be overwritten and the confirm value. "+
			"A restore from the bucket goes on if the call gives up waiting (pass timeoutSeconds to wait longer). Box owner only.", tag)
	rs.Errors = append(rs.Errors, 404, 409, 422, 428)
	rs.Extensions[api.ExtConfirm] = true
	huma.Register(a, rs, api.Wrap(func(ctx context.Context, in *struct {
		ID   string `path:"id" pattern:"^(bk_[0-9A-Z]{26}|latest)$" doc:"Backup ID, or latest (the newest successful one)"`
		Body struct {
			Targets        []string `json:"targets,omitempty" doc:"What to restore: postgres, valkey, files, platform, all (default postgres and valkey; all on a box with no projects)"`
			From           string   `json:"from,omitempty" enum:"local,offsite," doc:"local (default): this box's copy; offsite: the copy in the bucket"`
			Time           string   `json:"time,omitempty" doc:"Restore Postgres to this moment (RFC 3339 such as 2026-10-07T14:32:00Z, or 2026-10-07 14:32 in UTC); id must be latest. Valkey and files go back to the newest set at or before it"`
			Confirm        string   `json:"confirm,omitempty" doc:"The confirm value from the preview (status 428)"`
			TimeoutSeconds int      `json:"timeoutSeconds,omitempty" minimum:"0" maximum:"7200" doc:"How long the call waits for a restore from the bucket (default 60 s; it goes on after)"`
		}
	}) (*struct{ Body *BackupRestored }, error) {
		pr := api.PrincipalFrom(ctx)
		if !pr.BoxAdmin() {
			return nil, fmt.Errorf("%w: restoring a backup overwrites every project; it needs the box owner's token", tokens.ErrForbidden)
		}
		if err := onBox(p); err != nil {
			return nil, err
		}
		from := in.Body.From
		if from == "" {
			from = SourceLocal
		}
		var b *Backup
		var rec *offsiteSet
		var at *time.Time
		if in.Body.Time != "" {
			if from == SourceOffsite {
				pb := api.NewProblem(422, "validation", "a point-in-time restore is from this box's copy only; from the bucket, restore a whole backup")
				pb.Hint = "drop time, or drop from=offsite"
				return nil, pb
			}
			if in.ID != "latest" {
				pb := api.NewProblem(422, "validation", "with time, the backup is chosen for you: use latest as the id")
				pb.Hint = "`tiffin restore latest --time \"2026-10-07 14:32\"`"
				return nil, pb
			}
			t, err := parseMoment(in.Body.Time)
			if err != nil {
				return nil, api.NewProblem(422, "validation", err.Error())
			}
			list, err := List(ctx, p)
			if err != nil {
				return nil, err
			}
			got, err := pickForTime(list, t, time.Now())
			if err != nil {
				pb := api.NewProblem(422, "validation", err.Error())
				pb.Hint = "the overview's restorable range (`tiffin backups list`) has the moments you can pick"
				return nil, pb
			}
			b, at = got, &t
		} else if from == SourceOffsite {
			got, err := pickOffsite(ctx, in.ID)
			switch {
			case errors.Is(err, ErrOffsiteOff):
				return nil, offsiteProblem(409, "precondition", err, "on a new box: `tiffin backups offsite set ... --passphrase <the passphrase>`")
			case errors.Is(err, os.ErrNotExist):
				pb := api.NewProblem(404, "not_found", "no backup "+in.ID+" in the bucket")
				pb.Hint = "list them with `tiffin backups offsite list`"
				return nil, pb
			case err != nil:
				return nil, api.NewProblem(409, "precondition", err.Error())
			}
			rec, b = got, &got.Backup
		} else {
			if in.ID == "latest" {
				list, err := List(ctx, p)
				if err != nil {
					return nil, err
				}
				b = lastOK(list, "")
				if b == nil {
					return nil, api.NewProblem(404, "not_found", "this box has no successful backup; to restore from the bucket pass from=offsite")
				}
			} else {
				got, err := get(ctx, p, in.ID)
				if errors.Is(err, os.ErrNotExist) {
					return nil, api.NewProblem(404, "not_found", "no backup "+in.ID+" on this box (for one in the bucket, pass from=offsite)")
				}
				if err != nil {
					return nil, err
				}
				b = got
			}
			if b.Status != "ok" {
				return nil, api.NewProblem(409, "precondition", "backup "+b.ID+" did not succeed ("+b.Status+"); pick another from `tiffin backups list`")
			}
		}
		targets, err := normalizeTargets(in.Body.Targets, defaultTargets(ctx, p))
		if err != nil {
			return nil, api.NewProblem(422, "validation", err.Error())
		}
		if err := checkTargets(ctx, p, targets); err != nil {
			return nil, api.NewProblem(422, "validation", err.Error())
		}
		preview, err := Preview(ctx, p, b, from, targets, at)
		if err != nil {
			return nil, err
		}
		if err := datakit.RequireConfirm(in.Body.Confirm, preview.Key(), preview); err != nil {
			return nil, err
		}
		audit := map[string]any{"session": pr.Session, "targets": targets, "from": from}
		if at != nil {
			audit["time"] = at.Format(time.RFC3339)
		}
		_ = p.DB.Audit(ctx, pr.TokenID, "backup.restore", b.ID, audit)
		if from == SourceLocal {
			// Once it starts it runs to the end: a dropped connection must not
			// stop it halfway with Postgres or Valkey down.
			out, err := Restore(context.WithoutCancel(ctx), p, b, targets, at)
			if err != nil {
				return nil, busy(err)
			}
			return &struct{ Body *BackupRestored }{out}, nil
		}
		// From the bucket: it can take a while, so it is not tied to the call.
		type result struct {
			out *BackupRestored
			err error
		}
		done := make(chan result, 1)
		go func() {
			out, err := RestoreOffsite(context.WithoutCancel(ctx), p, rec, targets)
			done <- result{out, err}
		}()
		wait := time.Duration(max(in.Body.TimeoutSeconds, 60)) * time.Second
		select {
		case r := <-done:
			if r.err != nil {
				return nil, busy(r.err)
			}
			return &struct{ Body *BackupRestored }{r.out}, nil
		case <-time.After(wait):
			pb := api.NewProblem(409, "conflict", "the restore from the bucket is still running; it goes on without this call")
			pb.Hint = "follow it with `tiffin status` and `journalctl -u tiffin` on the box; pass a longer timeoutSeconds next time"
			return nil, pb
		}
	}))

	ss := api.Op("backups-schedule-set", http.MethodPut, "/v1/backups/schedule", "backups schedule", api.RiskWrite,
		"Change the backup schedule", "How often full and incremental backups run, how many full backups are kept, and whether and how often "+
			"restore drills run. Only the fields you send change. Box owner only.", tag)
	huma.Register(a, ss, api.Wrap(func(ctx context.Context, in *struct {
		Body struct {
			Enabled               *bool `json:"enabled,omitempty" doc:"Take backups automatically"`
			FullEveryHours        *int  `json:"fullEveryHours,omitempty" minimum:"1" maximum:"720" doc:"Hours between full backups (default 24)"`
			IncrementalEveryHours *int  `json:"incrementalEveryHours,omitempty" minimum:"0" maximum:"168" doc:"Hours between incremental backups; 0 turns them off (default 6)"`
			RetainFull            *int  `json:"retainFull,omitempty" minimum:"1" maximum:"60" doc:"Full backups to keep, with their incrementals (default 7)"`
			DrillEnabled          *bool `json:"drillEnabled,omitempty" doc:"Run restore drills automatically (default on)"`
			DrillEveryDays        *int  `json:"drillEveryDays,omitempty" minimum:"1" maximum:"90" doc:"Days between scheduled restore drills (default 7)"`
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
		if v := in.Body.DrillEnabled; v != nil {
			s.DrillEnabled = *v
		}
		if v := in.Body.DrillEveryDays; v != nil {
			s.DrillEveryDays = *v
		}
		raw, _ := json.Marshal(s)
		if err := p.DB.KVPut(ctx, nsMeta, "schedule", raw); err != nil {
			return nil, err
		}
		_ = p.DB.Audit(ctx, pr.TokenID, "backup.schedule", "", s)
		return &struct{ Body BackupSchedule }{s}, nil
	}))

	registerDrills(a, p, tag)
	registerOffsite(a, p, tag)
}
